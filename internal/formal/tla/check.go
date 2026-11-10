// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tla

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/jwriteclub/prudence"

	"github.com/jwriteclub/spec/internal/formal"
)

// CheckerName names prudence in a model proposal's check.
const CheckerName = "prudence"

// ParserVersion is the version of prudence that Scan and Lex use.
const ParserVersion = prudence.Version

// DefaultMaxAcquisitionVisits is prudence's acquisition visit budget when
// formal.CheckLimits leaves it unset.
const DefaultMaxAcquisitionVisits = prudence.DefaultMaxAcquisitionVisits

// CodeAcquisitionLimit is the code of the note prudence gives when a check
// spends its module-acquisition visit budget (formal.CheckLimits).
const CodeAcquisitionLimit = prudence.CodeAcquisitionLimit

// AcquisitionLimitMessage says that a check stopped at the acquisition limit
// of limit visits (0: prudence's default) and how to raise it.
func AcquisitionLimitMessage(limit int) string {
	if limit <= 0 {
		limit = DefaultMaxAcquisitionVisits
	}
	return fmt.Sprintf("the model hit the acquisition limit of %d visits, so its semantic check is incomplete; "+
		"raise it with --max-acquisition-visits N, or max_acquisition_visits = N under [tla] in spec.toml", limit)
}

// Checker checks TLA+ models with prudence. It implements formal.Checker. The
// resolver hands prudence the project's modules; it brings the standard ones
// (Naturals, Sequences, TLC and the rest) itself. Check asks for the full
// check; until prudence's semantic port lands it checks syntax and reports the
// rest incomplete. req.Limits.MaxAcquisitionVisits becomes prudence's
// Options.MaxAcquisitionVisits; a check that spends it carries a hint
// CodeAcquisitionLimit (AcquisitionLimitMessage) and is incomplete.
type Checker struct{}

// Check implements formal.Checker.
func (Checker) Check(ctx context.Context, req formal.CheckRequest) (formal.Report, error) {
	rep := formal.Report{Checker: CheckerName, Version: prudence.Version}
	ms := &modules{texts: map[string][]byte{req.Path: req.Src}}
	res, err := prudence.Check(ctx, prudence.Request{
		Origin: prudence.OriginWorkspace, Path: req.Path, Text: req.Src, Resolve: ms.resolver(req.Resolve),
		Options: prudence.Options{MaxAcquisitionVisits: req.Limits.MaxAcquisitionVisits},
	})
	if err != nil {
		return rep, err
	}
	rep.Level = string(res.Level)
	rep.Completion = completion(res.Completion)
	for _, d := range res.Diagnostics {
		p := d.Path
		if p == "" {
			p = req.Path
		}
		text := ms.text(p)
		fd := formal.Diagnostic{
			Path: p, Start: position(text, d.Span.Start.ByteOffset), End: position(text, max(d.Span.Start.ByteOffset, d.Span.End.ByteOffset)),
			Severity: checkSeverity(d.Severity), Code: d.Code, Phase: string(d.Phase), Message: Message(d.Message),
			Incomplete: d.Code == prudence.CodeIncomplete || d.Code == prudence.CodeAcquisitionLimit,
		}
		if d.Code == prudence.CodeAcquisitionLimit {
			// prudence's own text names its Go option; say what a user sets.
			fd.Message = AcquisitionLimitMessage(req.Limits.MaxAcquisitionVisits)
		}
		if fd.Incomplete && rep.Incomplete == "" {
			rep.Incomplete = fd.Message
		}
		rep.Diagnostics = append(rep.Diagnostics, fd)
	}
	return rep, nil
}

// modules is the text of each module a check resolved, by path, so its
// diagnostics' offsets can become lines and columns.
type modules struct {
	mu    sync.Mutex
	texts map[string][]byte
}

// resolver adapts the project's resolver to prudence's: a module the project
// does not have is prudence.ErrModuleNotFound, and a check whose context ended
// gets that error instead.
func (ms *modules) resolver(resolve formal.Resolver) prudence.Resolver {
	return func(ctx context.Context, module string) (prudence.Source, error) {
		if err := ctx.Err(); err != nil {
			return prudence.Source{}, err
		}
		if resolve == nil {
			return prudence.Source{}, fmt.Errorf("module %s: %w", module, prudence.ErrModuleNotFound)
		}
		p, src, ok := resolve(module)
		if !ok {
			return prudence.Source{}, fmt.Errorf("module %s: %w", module, prudence.ErrModuleNotFound)
		}
		ms.mu.Lock()
		ms.texts[p] = src
		ms.mu.Unlock()
		return prudence.Source{Path: p, Text: src, Origin: prudence.OriginWorkspace}, nil
	}
}

// text returns the text of the module at p, or nil.
func (ms *modules) text(p string) []byte {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.texts[p]
}

// completion maps prudence's completion to the checker slot's.
func completion(c prudence.Completion) formal.Completion {
	switch c {
	case prudence.CompletionComplete:
		return formal.CompletionComplete
	case prudence.CompletionIncomplete:
		return formal.CompletionIncomplete
	}
	return formal.CompletionUnknown
}

// checkSeverity maps a prudence severity to a check diagnostic's. Info and
// hint, among them analysis/incomplete, are hints: never an error, so they
// cannot refuse filing.
func checkSeverity(s prudence.Severity) formal.Severity {
	switch s {
	case prudence.SeverityError:
		return formal.SeverityError
	case prudence.SeverityWarning:
		return formal.SeverityWarning
	}
	return formal.SeverityHint
}

// position returns the 1-based line and byte column of off in src.
func position(src []byte, off int) formal.Position {
	off = clamp(off, src)
	lineStart := bytes.LastIndexByte(src[:off], '\n') + 1
	return formal.Position{Line: bytes.Count(src[:off], []byte("\n")) + 1, Column: off - lineStart + 1, Offset: off}
}
