// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

// An approval in spec ui edits the buffers. It becomes true when a save puts
// it on disk, so that is when the store hears of it: until then an agent
// reads pending, `--wait` keeps waiting, and discarding the buffer leaves a
// proposal that can be approved again.
//
// In this UI the approval still looks done at once: loadPending lays it over
// the proposal, and hides a proposal it finishes. ^Z of the approval brings
// the proposal back and ^Y hides it again.
//
// A save and its receipts share one hold of .spec/lock: a waiter that found
// the new text with the proposal still pending would call it stale. For the
// same reason, saving one file of several records that file's hunks at once.

// bufferedApply is one apply into the buffers that no save has made true.
type bufferedApply struct {
	// seen is the proposal as it was approved.
	seen *proposal.Proposal
	// decision is what a save records. Nil when there is nothing but the
	// sidebar decisions keys, which settleIfDone records.
	decision *proposal.Decision
	// whole says the approval decides the proposal, not only these hunks.
	whole bool
	// edits holds, for each file not saved yet, its buffer's history state
	// right after the apply.
	edits map[string]uint64
	// keys are the sidebar decisions this apply took.
	keys []string
}

// receipts is what a save recorded, for its status line.
type receipts struct {
	notes  []string
	warned bool
}

// holdApply remembers an apply until its files are saved; seen shows the
// decision from now on. A file whose buffer matches the disk needs no save.
func (a *App) holdApply(lock *project.Lock, seen *proposal.Proposal, d *proposal.Decision, whole bool, edits map[string]uint64, keys []string) {
	a.reconcileBuffered()
	e := &bufferedApply{seen: seen, decision: d, whole: whole, edits: map[string]uint64{}, keys: keys}
	for path, state := range edits {
		if doc, ok := a.ws.Docs()[path]; ok && doc.Ed.Buffer().Dirty() {
			e.edits[path] = state
		}
	}
	if d != nil {
		merged := workflow.MergeDecision(seen, *d)
		seen.Decision = &merged
	}
	a.buffered = append(a.buffered, e)
	if len(e.edits) == 0 {
		r := &receipts{}
		a.recordLanded(lock, seen.ID, "", r)
		if r.warned {
			lg.WithField("proposal", seen.ID).Warn(strings.Join(r.notes, "; "))
		}
	}
}

// holdHand remembers fix key of p, made by hand in path's unsaved buffer,
// until that buffer is saved.
func (a *App) holdHand(p *proposal.Proposal, path, key string) {
	doc, ok := a.ws.Docs()[path]
	if !ok || !doc.Ed.Buffer().Dirty() {
		return
	}
	edits := map[string]uint64{path: doc.Ed.Buffer().StateID()}
	a.buffered = append(a.buffered, &bufferedApply{seen: p, edits: edits, keys: []string{key}})
}

// bufferedOf are the unsaved applies of proposal id.
func (a *App) bufferedOf(id string) []*bufferedApply {
	var out []*bufferedApply
	for _, e := range a.buffered {
		if e.seen.ID == id {
			out = append(out, e)
		}
	}
	return out
}

// inBuffers reports whether every unsaved edit of e is in its buffer now.
func (a *App) inBuffers(e *bufferedApply) bool {
	for path, state := range e.edits {
		doc, ok := a.ws.Docs()[path]
		if !ok || !doc.Ed.Buffer().Holds(state) {
			return false
		}
	}
	return true
}

// withBuffered lays the applies in the buffers over p, as loaded from the
// store, and reports whether they finish it.
func (a *App) withBuffered(p *proposal.Proposal) bool {
	held, whole := false, false
	for _, e := range a.bufferedOf(p.ID) {
		if !a.inBuffers(e) {
			continue
		}
		held, whole = true, whole || e.whole
		if e.decision != nil {
			merged := workflow.MergeDecision(p, *e.decision)
			p.Decision = &merged
		}
		if len(p.CheckFiles) > 0 {
			p.CheckFiles = e.seen.CheckFiles
		}
	}
	switch {
	case !held:
		return false
	case whole && p.Decision != nil:
		return workflow.SettleStatus(p, *p.Decision) != proposal.StatusPending
	}
	return suggestions(p) && a.proposalSettled(p)
}

// bufferedShape says which applies exist and which are in the buffers: what
// loadPending's view of the proposals depends on.
func (a *App) bufferedShape() string {
	var b strings.Builder
	for _, e := range a.buffered {
		fmt.Fprintf(&b, "%s=%t;", e.seen.ID, a.inBuffers(e))
	}
	return b.String()
}

// reconcileBuffered follows the buffers after an edit. An apply that can no
// longer come back (the buffer was reloaded, or typing replaced the redo
// history) is forgotten. So is one that took sidebar decisions as soon as it
// is undone, which returns their cards; ^Y then finds the fix done by hand.
func (a *App) reconcileBuffered() {
	a.buffered = slices.DeleteFunc(a.buffered, func(e *bufferedApply) bool {
		if a.inBuffers(e) {
			return false
		}
		drop := len(e.keys) > 0
		for path, state := range e.edits {
			if doc, ok := a.ws.Docs()[path]; !ok || !doc.Ed.Buffer().Undone(state) && !doc.Ed.Buffer().Holds(state) {
				drop = true
			}
		}
		if drop {
			for _, k := range e.keys {
				delete(a.side.state.Decided[e.seen.ID], k)
			}
		}
		return drop
	})
	if a.bufferedShape() != a.bufferedShown {
		a.saveChecks()
		a.loadPending()
	}
}

// saveDoc writes d's buffer and, under the same lock, records what that made
// true.
func (a *App) saveDoc(d *Doc) (*receipts, error) {
	lock, err := project.AcquireLock(a.ws.Root, "spec ui")
	if err != nil {
		return nil, err
	}
	defer func() {
		if rerr := lock.Release(); rerr != nil {
			lg.WithError(rerr).Warn("releasing the project lock")
		}
	}()
	if _, err := a.ws.SaveLocked(d); err != nil {
		return nil, err
	}
	return a.recordSaved(lock, d.Ed.Path()), nil
}

// recordSaved records what the save of path made true: each apply whose edit
// in path is part of the saved text, then every check that finishes.
func (a *App) recordSaved(lock *project.Lock, path string) *receipts {
	r := &receipts{}
	a.reconcileBuffered()
	var ids []string
	for _, e := range a.buffered {
		state, ok := e.edits[path]
		if !ok || !a.ws.Docs()[path].Ed.Buffer().Holds(state) {
			continue
		}
		delete(e.edits, path)
		if !slices.Contains(ids, e.seen.ID) {
			ids = append(ids, e.seen.ID)
		}
	}
	if len(ids) == 0 {
		return r
	}
	for _, id := range ids {
		a.recordLanded(lock, id, path, r)
	}
	a.loadPending()
	for _, p := range a.settleDecided(lock) {
		if p.Status == proposal.StatusApplied {
			r.notes = append(r.notes, p.ID+" applied")
		}
	}
	a.saveChecks()
	a.loadPending()
	return r
}

// recordLanded writes proposal id's receipt after path was saved: the whole
// decision once every apply of it has reached the disk, else the hunks in
// path.
func (a *App) recordLanded(lock *project.Lock, id, path string, r *receipts) {
	es := a.bufferedOf(id)
	seen := es[len(es)-1].seen
	var d *proposal.Decision
	whole := false
	if slices.ContainsFunc(es, func(e *bufferedApply) bool { return len(e.edits) > 0 }) {
		d = landedHunks(es, path)
	} else {
		d, whole = allOf(es)
		a.buffered = slices.DeleteFunc(a.buffered, func(e *bufferedApply) bool { return e.seen.ID == id })
	}
	if d == nil {
		return
	}
	var rec *proposal.Proposal
	var e *workflow.Error
	if whole {
		rec, e = a.wf.SettleSeenLocked(lock, a.ws.Root, seen, *d)
	} else {
		rec, e = a.wf.RecordPartialSeenLocked(lock, a.ws.Root, seen, *d)
	}
	if e != nil {
		a.receiptRefused(id, e, r)
		if e.Code == "changed" || e.Code == "not_pending" {
			a.forgetApplies(id, es)
		}
		return
	}
	for _, other := range a.bufferedOf(id) {
		other.seen.Revision = rec.Revision
	}
	if rec.Status == proposal.StatusApplied {
		r.notes = append(r.notes, id+" applied")
	}
}

// landedHunks takes the hunks in path out of es: the receipt of a save that
// finishes only part of them (nil when none is in path).
func landedHunks(es []*bufferedApply, path string) *proposal.Decision {
	var landed []int
	for _, e := range es {
		if e.decision == nil {
			continue
		}
		e.decision.Applied = slices.DeleteFunc(e.decision.Applied, func(n int) bool {
			if hunkFile(e.seen, n) != path {
				return false
			}
			landed = append(landed, n)
			return true
		})
	}
	if len(landed) == 0 {
		return nil
	}
	landed = workflow.SortedInts(landed)
	return &proposal.Decision{Accepted: landed, Applied: landed, By: proposal.AuthorHuman}
}

// allOf combines the decisions of es, the applies of one proposal (nil when
// they are only sidebar decisions), and says whether one decides it whole.
func allOf(es []*bufferedApply) (*proposal.Decision, bool) {
	var d *proposal.Decision
	whole := false
	for _, e := range es {
		if e.decision == nil {
			continue
		}
		if d == nil {
			d = &proposal.Decision{By: proposal.AuthorHuman}
		}
		whole = whole || e.whole
		d.Accepted = append(d.Accepted, e.decision.Accepted...)
		d.Applied = append(d.Applied, e.decision.Applied...)
		d.Failed = append(d.Failed, e.decision.Failed...)
	}
	if d == nil {
		return nil, false
	}
	d.Accepted, d.Applied = workflow.SortedInts(d.Accepted), workflow.SortedInts(d.Applied)
	d.Failed = slices.DeleteFunc(d.Failed, func(f proposal.Failure) bool { return slices.Contains(d.Applied, f.N) })
	return d, whole
}

// hunkFile is the file hunk n of p edits ("" when p has no such hunk).
func hunkFile(p *proposal.Proposal, n int) string {
	if p.Primary != nil && p.Primary.N == n {
		return p.Primary.File
	}
	h, _ := hunkOf(p, n)
	return h.File
}

// receiptRefused says that a saved change could not be recorded: the proposal
// was decided or revised elsewhere after the approval. The file keeps it.
func (a *App) receiptRefused(id string, e *workflow.Error, r *receipts) {
	r.warned = true
	if p, le := a.wf.LoadProposal(a.ws.Root, id); le == nil && p.Status != proposal.StatusPending {
		r.notes = append(r.notes, fmt.Sprintf("%s was %s meanwhile; not recorded as applied", id, p.Status))
		return
	}
	if e.Code == "changed" {
		r.notes = append(r.notes, fmt.Sprintf("%s changed meanwhile; not recorded as applied, review it again", id))
		return
	}
	r.notes = append(r.notes, fmt.Sprintf("%s not recorded as applied: %s", id, e.Message))
}

// forgetApplies ends es, the applies of proposal id, after a refusal no retry
// can pass: the proposal shows again and the sidebar asks about their hunks
// again. What they put in the buffers stays there, for the reviewer to judge.
func (a *App) forgetApplies(id string, es []*bufferedApply) {
	a.buffered = slices.DeleteFunc(a.buffered, func(e *bufferedApply) bool { return e.seen.ID == id })
	for _, e := range es {
		for _, k := range e.keys {
			delete(a.side.state.Decided[id], k)
		}
	}
	if len(a.side.state.Decided[id]) == 0 {
		delete(a.side.state.Decided, id)
	}
}

// durableDecisions are the sidebar's decisions a restart should find: those
// of an apply no save has made true go when the buffer does.
func (a *App) durableDecisions() map[string]map[string]string {
	if len(a.buffered) == 0 {
		return a.side.state.Decided
	}
	out := map[string]map[string]string{}
	for id, keys := range a.side.state.Decided {
		out[id] = map[string]string{}
		for k, how := range keys {
			out[id][k] = how
		}
	}
	for _, e := range a.buffered {
		for _, k := range e.keys {
			delete(out[e.seen.ID], k)
		}
	}
	return out
}
