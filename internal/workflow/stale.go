// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// ChecksFile is where spec ui keeps the author's decisions on the items
// of pending proposals, under the project.
const ChecksFile = ".spec/checks.json"

// SidebarActedOn reads ChecksFile and reports which pending proposals the
// author has decided an item of in spec ui (none when there is no file).
func SidebarActedOn(root string) (func(id string) bool, error) {
	none := func(string) bool { return false }
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ChecksFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return none, nil
	}
	if err != nil {
		return none, err
	}
	var st struct {
		Decided map[string]map[string]string `json:"decided"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return none, err
	}
	return func(id string) bool { return ActedOn(st.Decided[id]) }, nil
}

// ActedOn reports whether items (a proposal's item decisions, as spec ui
// keeps them) decide anything.
func ActedOn(items map[string]string) bool {
	for _, d := range items {
		if d != "" {
			return true
		}
	}
	return false
}

// Covers reports whether an analysis of scope newer covers what one of
// scope older covered: everything covers everything; the text that
// differs from HEAD covers the same against the same HEAD; files cover a
// subset of them. An analysis filed before scopes were recorded counts as
// one of the text that differs from HEAD.
func Covers(newer, older *proposal.Scope) bool {
	if newer == nil {
		return false
	}
	o := proposal.Scope{Mode: ModeChanged}
	if older != nil {
		o = *older
	}
	switch newer.Mode {
	case ModeAll:
		return true
	case ModeChanged:
		return o.Mode == ModeChanged && (o.Head == "" || o.Head == newer.Head)
	case ModeFiles:
		if o.Mode != ModeFiles {
			return false
		}
		for _, f := range o.Files {
			if !slices.Contains(newer.Files, f) {
				return false
			}
		}
		return true
	}
	return false
}

// StaleAnalysis reports whether pending proposal old is an analysis that
// analysis newer makes stale: an older analysis of a scope newer covers,
// not an answer (a rethink carries the author's notes), with no hunk
// applied. Whether the author acted on it in spec ui is the caller's to
// add.
func StaleAnalysis(newer, old *proposal.Proposal) bool {
	if newer == nil || old == nil || old.ID == newer.ID || old.Kind != proposal.KindAnalysis || old.Status != proposal.StatusPending || old.Rethink != nil {
		return false
	}
	if old.Created.After(newer.Created) || old.Decision != nil && len(old.Decision.Applied) > 0 {
		return false
	}
	return Covers(newer.Scope, old.Scope)
}

// SupersedeAnalysesLocked rejects the pending analyses that analysis
// newer makes stale (StaleAnalysis) and the author has not acted on (keep
// reports those; nil keeps none), as superseded by newer, keeping their
// records. newer need not be filed (an analysis that found nothing is
// not). It returns the IDs it rejected. The caller holds .spec/lock.
func (w *Workflow) SupersedeAnalysesLocked(lock *project.Lock, root string, newer *proposal.Proposal, keep func(id string) bool, by string) ([]string, *Error) {
	if e := held(lock); e != nil {
		return nil, e
	}
	ps, err := w.Store(root).List()
	if err != nil {
		return nil, internalErr(err)
	}
	why := "superseded by a newer analysis of the same scope"
	if newer != nil && newer.ID != "" {
		why = "superseded by analysis " + newer.ID
	}
	var out []string
	for _, old := range ps {
		if !StaleAnalysis(newer, old) || keep != nil && keep(old.ID) {
			continue
		}
		if e := w.CheckRecovery(root, old.ID); e != nil {
			continue
		}
		if _, e := w.RejectLocked(lock, root, old.ID, why, ByOr(by)); e != nil {
			if e.Code != "not_pending" {
				lg.WithError(e).Warn("cannot supersede " + old.ID)
			}
			continue
		}
		out = append(out, old.ID)
	}
	return out, nil
}

// SupersedeAnalyses is SupersedeAnalysesLocked for the CLI: it takes
// .spec/lock and keeps the analyses the author acted on in spec ui
// (ChecksFile).
func (w *Workflow) SupersedeAnalyses(ctx context.Context, root string, newer *proposal.Proposal, by string) ([]string, *Error) {
	keep, err := SidebarActedOn(root)
	if err != nil {
		return nil, fail(ExitFail, "internal", "cannot read %s: %v; no analysis was superseded", ChecksFile, err)
	}
	lock, e := w.DecisionLock(ctx, root, "spec analyze: supersede stale analyses")
	if e != nil {
		return nil, e
	}
	defer func() { _ = lock.Release() }()
	return w.SupersedeAnalysesLocked(lock, root, newer, keep, by)
}
