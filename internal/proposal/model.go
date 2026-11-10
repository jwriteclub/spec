// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package proposal

import (
	"fmt"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/pos"
)

// A model proposal edits one formal model: find/replace hunks in its text,
// applied all or nothing, since a model is checked as a whole. It has no
// primary, no inference and no trailer. Edits elsewhere in the model do not
// make it stale; a hunk whose text is no longer there does.

// Model check states.
const (
	// ModelNotChecked: no checker is registered (formal.CheckerFor).
	ModelNotChecked = "not_checked"
	// ModelClean: the checker found no error in the proposed text. Unless
	// FullCheck, it checked syntax only.
	ModelClean = "clean"
	// ModelErrors: the proposed text has errors. At filing they are errors the
	// base already had (a new one refuses filing); at the approval recheck
	// (Rechecked) any error sets it.
	ModelErrors = "errors"
	// ModelCheckFailed: the checker could not run (Message says why).
	ModelCheckFailed = "failed"
)

// ModelCheck is the checker's verdict on a model proposal's text.
type ModelCheck struct {
	State   string `json:"state"`
	Checker string `json:"checker,omitempty"`
	Version string `json:"version,omitempty"`
	Level   string `json:"level,omitempty"`
	// Completion says whether the checker finished the full check it was
	// asked for ("" when it did not say).
	Completion formal.Completion `json:"completion,omitempty"`
	// Message says why a check failed to run.
	Message string `json:"message,omitempty"`
	// Diagnostics are about the proposed text: the model with the hunks
	// applied, positions in that text.
	Diagnostics []formal.Diagnostic `json:"diagnostics"`
	// Hash is the sha256 of the model text the hunks were applied to for
	// this check; approval rechecks when the model is no longer that text.
	Hash string `json:"hash"`
	// Rechecked is set when approval checked again because the model had
	// moved since filing.
	Rechecked bool  `json:"rechecked,omitempty"`
	LatencyMS int64 `json:"latency_ms"`
}

// Errors counts the error diagnostics.
func (c ModelCheck) Errors() int {
	n := 0
	for _, d := range c.Diagnostics {
		if d.Severity == formal.SeverityError {
			n++
		}
	}
	return n
}

// FullCheck reports whether the check covered the model's semantics. A check
// that did not finish, or stopped at syntax, checked syntax only.
func (c ModelCheck) FullCheck() bool {
	return formal.FullCheck(c.Level, c.Completion)
}

// LinkFinding is a tag-link finding (`spec explain citations`) that the
// proposed text adds, at a 1-based line and byte column of that text.
type LinkFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Message  string `json:"message"`
}

// Model is what a model proposal edits.
type Model struct {
	// File is the model's project-relative path.
	File string `json:"file"`
	// Module is its module name ("" without a module header).
	Module string `json:"module"`
	// BaseHash is the sha256 of the model's text when the proposal was made.
	BaseHash string `json:"base_hash"`
	// Decls are the top-level definitions the hunks touch, in file order.
	Decls []string      `json:"decls"`
	Check ModelCheck    `json:"check"`
	Links []LinkFinding `json:"links"`
}

// IsModel reports whether p is a model proposal.
func (p *Proposal) IsModel() bool {
	return p.Kind == KindModel && p.Model != nil
}

// PrepareModel computes the splices of model proposal p against src, the
// model's current text. accept must hold every hunk not yet applied: a model
// edit applies as a whole. A hunk that no longer applies makes the whole
// proposal a *StaleError naming the model.
func (p *Proposal) PrepareModel(src []byte, accept map[int]bool) (*Prepared, error) {
	if !p.IsModel() {
		return nil, fmt.Errorf("proposal %s is not a model proposal", p.ID)
	}
	file := p.Model.File
	out := &Prepared{Applied: []int{}, Failed: []Failure{}}
	var sps []edit.Splice
	for _, h := range p.Hunks {
		if p.AppliedHunk(h.N) {
			continue
		}
		if !accept[h.N] {
			return nil, fmt.Errorf("hunk %d of model proposal %s is left out; a model edit applies as a whole", h.N, p.ID)
		}
		sp, why := locate(src, h, sps)
		if why != "" {
			return nil, &StaleError{Name: file, Reason: fmt.Sprintf("hunk %d no longer applies (%s); a model edit applies as a whole", h.N, why)}
		}
		sps = append(sps, sp)
		out.Applied = append(out.Applied, h.N)
	}
	if len(sps) > 0 {
		out.Edits = []edit.FileEdit{{Path: file, Snapshot: src, Splices: sps}}
	}
	return out, nil
}

// ModelHunks turns base, a model's text, and proposed, its new text, into
// hunks numbered from 1 whose Find each occurs exactly once in base: each
// changed run of lines, grown by whole lines of context until it is unique.
// Runs whose context meets are merged into one hunk.
func ModelHunks(file, base, proposed string) []finding.Hunk {
	type group struct {
		start, end int
		edits      []udiff.Edit
	}
	var groups []*group
	for _, e := range udiff.Lines(base, proposed) {
		g := &group{start: e.Start, end: e.End, edits: []udiff.Edit{e}}
		grow(base, &g.start, &g.end)
		for n := len(groups); n > 0 && g.start <= groups[n-1].end; n-- {
			last := groups[n-1]
			g.start, g.end = min(g.start, last.start), max(g.end, last.end)
			g.edits = append(last.edits, g.edits...)
			groups = groups[:n-1]
		}
		groups = append(groups, g)
	}
	out := make([]finding.Hunk, 0, len(groups))
	for i, g := range groups {
		var b strings.Builder
		at := g.start
		for _, e := range g.edits {
			b.WriteString(base[at:e.Start])
			b.WriteString(e.New)
			at = e.End
		}
		b.WriteString(base[at:g.end])
		out = append(out, finding.Hunk{
			N: i + 1, File: file, Issue: "model edit",
			Find: base[g.start:g.end], Replace: b.String(),
			Range: pos.Range{Start: g.start, End: g.end},
		})
	}
	return out
}

// grow widens [start, end) of text by whole lines, one on each side at a
// time, until it is not empty and occurs exactly once in text.
func grow(text string, start, end *int) {
	for *start > 0 || *end < len(text) {
		if *end > *start && strings.Count(text, text[*start:*end]) == 1 {
			return
		}
		if *start > 0 {
			*start = strings.LastIndexByte(text[:*start-1], '\n') + 1
		}
		if *end < len(text) {
			if i := strings.IndexByte(text[*end:], '\n'); i >= 0 {
				*end += i + 1
			} else {
				*end = len(text)
			}
		}
	}
}
