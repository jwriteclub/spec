// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package formal

import (
	"context"
	"path"
	"strings"
	"sync"
)

// The check slot: a model checker that parses a model and resolves its names,
// reporting what is wrong with positions in the text it was given. Package
// project fills it for ".tla" with prudence, a Go port of SANY (tla.Checker),
// which checks syntax so far: it reports semantic checking incomplete. With no
// checker for its extension a model proposal is filed "not checked". logedit
// never runs Java.

// Severity is how bad a checker's diagnostic is.
type Severity string

// Severities.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	// SeverityHint is information, such as that a check could not finish.
	SeverityHint Severity = "hint"
)

// Completion says whether a checker finished the check it was asked for. It
// says nothing about whether the model is valid.
type Completion string

// Completions. The zero value is unknown.
const (
	CompletionUnknown    Completion = ""
	CompletionComplete   Completion = "complete"
	CompletionIncomplete Completion = "incomplete"
)

// Levels a checker reports, in prudence's words.
const (
	LevelLexical  = "lexical"
	LevelSyntax   = "syntax"
	LevelSemantic = "semantic"
	LevelComplete = "complete"
)

// FullCheck reports whether a check at level that ended with completion
// checked the model's semantics, not only its syntax.
func FullCheck(level string, completion Completion) bool {
	return completion == CompletionComplete && (level == LevelSemantic || level == LevelComplete)
}

// Position is a place in a module's text: Line and Column are 1-based,
// Column counting bytes, and Offset is the byte offset from the start.
type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
	Offset int `json:"offset"`
}

// Diagnostic is one problem a checker found, in the module at Path (the
// checked text, or a module it resolved).
type Diagnostic struct {
	Path  string   `json:"path"`
	Start Position `json:"start"`
	// End is where the diagnostic's span ends (exclusive); the editor
	// underlines Start to End.
	End      Position `json:"-"`
	Severity Severity `json:"severity"`
	// Code is the checker's code for the diagnostic, when it has one.
	Code string `json:"code,omitempty"`
	// Phase is the analysis phase that produced it ("lexical", "syntax",
	// "semantic"), when the checker says.
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message"`
	// Incomplete marks the checker's note that the check did not finish,
	// rather than a problem in the model.
	Incomplete bool `json:"-"`
}

// IncompleteSemanticLabel prefixes the message of a semantic diagnostic that
// an incomplete check produced and DemoteIncomplete turned into a hint.
const IncompleteSemanticLabel = "[incomplete semantic check] "

// semantic reports whether d came from the semantic or level phase: by
// Phase, or by code ("semantic/4004") when the checker names no phase.
func (d Diagnostic) semantic() bool {
	if d.Phase != "" {
		return d.Phase == LevelSemantic || d.Phase == LevelComplete
	}
	return strings.HasPrefix(d.Code, "semantic")
}

// DemoteIncomplete returns r with its semantic errors made hints, labelled,
// unless its check completed. A semantic check that did not finish can report
// false errors (a standard module not yet loaded leaves '+' undeclared), so
// only syntax-phase errors may refuse filing or need a second confirmation.
// The result shares no storage with r.
func (r Report) DemoteIncomplete() Report {
	if r.Completion == CompletionComplete || r.Diagnostics == nil {
		return r
	}
	ds := make([]Diagnostic, len(r.Diagnostics))
	copy(ds, r.Diagnostics)
	for i := range ds {
		if ds[i].Severity == SeverityError && ds[i].semantic() {
			ds[i].Severity = SeverityHint
			ds[i].Message = IncompleteSemanticLabel + ds[i].Message
		}
	}
	r.Diagnostics = ds
	return r
}

// Resolver returns the text of a module the checked one extends or
// instantiates: the project's path for it and its bytes, preferring unsaved
// text. ok is false when the project has no such module; a checker brings the
// standard modules itself.
type Resolver func(module string) (path string, src []byte, ok bool)

// CheckLimits bounds a check's work. The zero value leaves every bound at the
// checker's default.
type CheckLimits struct {
	// MaxAcquisitionVisits is the TLA+ checker's (prudence's) per-pass budget
	// of visits while it acquires the model and the modules it loads; 0 is
	// prudence's default. A check that exhausts it stops incomplete.
	MaxAcquisitionVisits int
}

// Override returns l with each bound that o sets replaced by o's.
func (l CheckLimits) Override(o CheckLimits) CheckLimits {
	if o.MaxAcquisitionVisits > 0 {
		l.MaxAcquisitionVisits = o.MaxAcquisitionVisits
	}
	return l
}

// CheckRequest is one model to check.
type CheckRequest struct {
	// Path is the model's project-relative path.
	Path string
	// Src is the text to check, which need not be on disk.
	Src     []byte
	Resolve Resolver
	// Limits bounds the check; the zero value is the checker's defaults.
	Limits CheckLimits
}

// Report is what a checker found.
type Report struct {
	// Checker and Version name the checker that ran.
	Checker string
	Version string
	// Level is the furthest phase examined, in the checker's words
	// ("syntax", "semantic"), whether or not it found problems there.
	Level string
	// Completion says whether the requested check finished.
	Completion Completion
	// Incomplete is the checker's reason the check did not finish, when it
	// gave one.
	Incomplete  string
	Diagnostics []Diagnostic
}

// Errors counts the report's error diagnostics.
func (r Report) Errors() int {
	n := 0
	for _, d := range r.Diagnostics {
		if d.Severity == SeverityError {
			n++
		}
	}
	return n
}

// Checker checks a model's text. It must have no side effects, be safe to
// call concurrently and stop when ctx ends. Its error is operational only (ctx
// ended, a depth bound): problems in the model are diagnostics.
type Checker interface {
	Check(ctx context.Context, req CheckRequest) (Report, error)
}

var (
	checkersMu sync.RWMutex
	checkers   = map[string]Checker{}
)

// RegisterChecker makes c the checker for models with extension ext (".tla").
// A nil c empties the slot.
func RegisterChecker(ext string, c Checker) {
	checkersMu.Lock()
	defer checkersMu.Unlock()
	if c == nil {
		delete(checkers, ext)
		return
	}
	checkers[ext] = c
}

// WithLimits returns c with the bounds o sets overriding those of each
// request, as a command-line flag overrides spec.toml; c itself when o sets
// none.
func WithLimits(c Checker, o CheckLimits) Checker {
	if c == nil || o == (CheckLimits{}) {
		return c
	}
	return limitedChecker{inner: c, limits: o}
}

// limitedChecker is WithLimits's checker.
type limitedChecker struct {
	inner  Checker
	limits CheckLimits
}

// Check implements Checker.
func (c limitedChecker) Check(ctx context.Context, req CheckRequest) (Report, error) {
	req.Limits = req.Limits.Override(c.limits)
	return c.inner.Check(ctx, req)
}

// CheckerFor returns the checker registered for the model at p, or nil.
func CheckerFor(p string) Checker {
	checkersMu.RLock()
	defer checkersMu.RUnlock()
	return checkers[path.Ext(p)]
}
