// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"context"
	"crypto/sha256"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/formal/tla"
	"github.com/jwriteclub/spec/internal/pos"
)

// modelCheckTimeout bounds one model's check: prudence takes milliseconds,
// so this only stops a runaway.
const modelCheckTimeout = 5 * time.Second

// ModelCheck is one model's full check (prudence's semantic and level
// checking) as the editor and lint see it.
type ModelCheck struct {
	// Ran is false when the model was not checked: it has a syntax error
	// (formal-syntax shows those), or no checker reads its language.
	Ran bool
	// Report is the checker's report; Err an operational failure.
	Report formal.Report
	Err    error
	// Diags are the findings in the model itself beyond syntax, as
	// formal-semantic lint diagnostics. Syntax-stage findings come from the
	// scan (formal-syntax), and an incomplete check is Note, not a finding.
	Diags []diag.Diagnostic
}

// Note says in a line how far the check got, for a status line: empty when
// it did not run.
func (c ModelCheck) Note() string {
	switch {
	case !c.Ran:
		return ""
	case c.Err != nil:
		return "prudence: check failed: " + c.Err.Error()
	case c.Report.Completion == formal.CompletionComplete:
		return "prudence: checked"
	case c.Report.Level == formal.LevelSemantic:
		return "prudence: semantic checked, level check pending"
	case c.Report.Incomplete != "":
		return "prudence: incomplete at " + c.Report.Level + ": " + c.Report.Incomplete
	}
	return "prudence: incomplete at " + c.Report.Level
}

// CheckCache keeps each model's latest check, so a reparse checks again only
// the models whose text, or the text of a module they resolved, changed. It
// is safe for concurrent use. A nil cache checks every time.
type CheckCache struct {
	mu      sync.Mutex
	entries map[string]checkEntry
}

// checkEntry is a model's check and what it depended on: the model's text
// and, for each module name the checker asked for, the hash of the text the
// resolver gave ("" when it had none).
type checkEntry struct {
	key   string
	deps  map[string]string
	check ModelCheck
}

// NewCheckCache returns an empty cache.
func NewCheckCache() *CheckCache {
	return &CheckCache{entries: map[string]checkEntry{}}
}

// CheckModels checks models under root within limits (spec.toml's,
// Config.CheckLimits), resolving modules through ModuleResolver with the
// models' own texts as open buffers. It returns each model's check by path.
func (c *CheckCache) CheckModels(ctx context.Context, root string, models []*formal.File, limits formal.CheckLimits) map[string]ModelCheck {
	open := map[string][]byte{}
	for _, f := range models {
		if f.ValidUTF8 && f.ReadError == "" {
			open[f.Path] = f.Source()
		}
	}
	// memo is shared by every model's resolver. prudence does not promise to
	// call a resolver from one goroutine, so it is guarded.
	var memoMu sync.Mutex
	memo := map[string]memoModule{}
	resolverFor := func(model string) formal.Resolver {
		base := ModuleResolver(root, model, func(rel string) ([]byte, bool) {
			src, ok := open[rel]
			return src, ok
		})
		dir := path.Dir(path.Clean(model))
		return func(name string) (string, []byte, bool) {
			k := dir + "\x00" + name
			memoMu.Lock()
			m, ok := memo[k]
			memoMu.Unlock()
			if ok {
				return m.rel, m.src, m.ok
			}
			rel, src, ok := base(name)
			memoMu.Lock()
			memo[k] = memoModule{rel, src, ok}
			memoMu.Unlock()
			return rel, src, ok
		}
	}
	out := make(map[string]ModelCheck, len(models))
	seen := map[string]bool{}
	for _, f := range models {
		seen[f.Path] = true
		out[f.Path] = c.check(ctx, f, resolverFor(f.Path), limits)
	}
	if c != nil {
		c.mu.Lock()
		for p := range c.entries {
			if !seen[p] {
				delete(c.entries, p)
			}
		}
		c.mu.Unlock()
	}
	return out
}

type memoModule struct {
	rel string
	src []byte
	ok  bool
}

// check checks one model, from the cache when nothing it read has changed.
func (c *CheckCache) check(ctx context.Context, f *formal.File, resolve formal.Resolver, limits formal.CheckLimits) ModelCheck {
	ck := formal.CheckerFor(f.Path)
	if ck == nil || !f.ValidUTF8 || f.ReadError != "" || f.HasError() {
		return ModelCheck{}
	}
	src := f.Source()
	sum := sha256.Sum256(src)
	key := scanVersions[path.Ext(f.Path)] + "\x00" + strconv.Itoa(limits.MaxAcquisitionVisits) + "\x00" + string(sum[:])
	if c != nil {
		c.mu.Lock()
		e, ok := c.entries[f.Path]
		c.mu.Unlock()
		if ok && e.key == key && depsUnchanged(e.deps, resolve) {
			return e.check
		}
	}
	deps := map[string]string{}
	var mu sync.Mutex
	recording := func(name string) (string, []byte, bool) {
		rel, text, ok := resolve(name)
		h := ""
		if ok {
			s := sha256.Sum256(text)
			h = string(s[:])
		}
		mu.Lock()
		deps[name] = h
		mu.Unlock()
		return rel, text, ok
	}
	cctx, cancel := context.WithTimeout(ctx, modelCheckTimeout)
	defer cancel()
	rep, err := ck.Check(cctx, formal.CheckRequest{Path: f.Path, Src: src, Resolve: recording, Limits: limits})
	mc := ModelCheck{Ran: true, Report: rep, Err: err}
	if err == nil {
		mc.Diags = checkDiags(f, rep)
	}
	// Only a finished check is kept: an operational error (a timeout, an
	// ended context) is retried on the next reparse.
	if c != nil && err == nil && ctx.Err() == nil && cctx.Err() == nil {
		c.mu.Lock()
		c.entries[f.Path] = checkEntry{key: key, deps: deps, check: mc}
		c.mu.Unlock()
	}
	return mc
}

// depsUnchanged reports whether every module a check resolved resolves to
// the same text now.
func depsUnchanged(deps map[string]string, resolve formal.Resolver) bool {
	for name, h := range deps {
		_, text, ok := resolve(name)
		now := ""
		if ok {
			s := sha256.Sum256(text)
			now = string(s[:])
		}
		if now != h {
			return false
		}
	}
	return true
}

// checkDiags returns the report's findings in f beyond the syntax stage as
// formal-semantic diagnostics. A finding in a module f extends shows when
// that module is checked itself, not here. While the check is incomplete
// (it reached a construct prudence cannot finish checking yet) its semantic
// errors are labelled hints, as in model proposals (formal.Report.DemoteIncomplete); they become
// errors once the check completes. A check stopped by the acquisition limit
// is a formal-check-limit hint, since raising the limit lets it finish; any
// other incomplete check is only Note.
func checkDiags(f *formal.File, rep formal.Report) []diag.Diagnostic {
	rep = rep.DemoteIncomplete()
	var out []diag.Diagnostic
	src := f.Source()
	for _, d := range rep.Diagnostics {
		if d.Incomplete && d.Code == tla.CodeAcquisitionLimit {
			out = append(out, diag.Diagnostic{
				Code: diag.CodeFormalCheckLimit, Severity: diag.Hint, File: f.Path,
				Range: widen(src, pos.Range{Start: d.Start.Offset, End: max(d.Start.Offset, d.End.Offset)}), Message: d.Message,
			})
			continue
		}
		if d.Path != f.Path || d.Incomplete || d.Phase == formal.LevelLexical || d.Phase == formal.LevelSyntax {
			continue
		}
		out = append(out, diag.Diagnostic{
			Code: diag.CodeFormalSemantic, Severity: lintSeverity(d.Severity), File: f.Path,
			Range: widen(src, pos.Range{Start: d.Start.Offset, End: max(d.Start.Offset, d.End.Offset)}), Message: d.Message,
		})
	}
	return out
}

// lintSeverity maps a checker severity to a lint severity; information is a
// hint, the lowest lint has.
func lintSeverity(s formal.Severity) diag.Severity {
	switch s {
	case formal.SeverityError:
		return diag.Error
	case formal.SeverityWarning:
		return diag.Warning
	}
	return diag.Hint
}

// widen keeps r within src and widens an empty range to the character at
// it, so an editor can underline it.
func widen(src []byte, r pos.Range) pos.Range {
	r.Start = min(max(r.Start, 0), len(src))
	r.End = min(max(r.End, r.Start), len(src))
	if r.Empty() && r.Start < len(src) && src[r.Start] != '\n' && src[r.Start] != '\r' {
		r.End = r.Start + 1
		for r.End < len(src) && src[r.End]&0xC0 == 0x80 {
			r.End++
		}
	}
	return r
}
