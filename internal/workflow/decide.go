// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"errors"
	"slices"

	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// PrepareInto prepares the accept hunks of p (the proposal as the reviewer
// saw it) against overlay, the editor's buffers over the files on disk,
// with the guards approving runs: a finding hunk that would add a lint
// error fails alone, and the change must leave its anchor defined once. It
// writes no source files, but persists primary staleness. The caller applies
// the edits to its buffers and, once they are saved, records the decision
// with Settle. A proposal revised or decided elsewhere since
// it was seen is exit 1 `changed` or `not_pending`; a stale change is exit
// 1 `stale` (the proposal is left pending). A `changed` or `not_pending`
// error carries the proposal as it is now.
func (w *Workflow) PrepareInto(proj *project.Project, seen *proposal.Proposal, accept []int, overlay map[string][]byte) (*proposal.Prepared, *Error) {
	lock, err := project.AcquireLock(proj.Root, "prepare proposal")
	if err != nil {
		return nil, LockErr(err)
	}
	defer func() { _ = lock.Release() }()
	return w.PrepareIntoLocked(lock, proj, seen, accept, overlay)
}

// PrepareIntoLocked prepares against buffers while the caller holds the decision
// lock through preparation and settlement. Primary staleness is saved durably.
func (w *Workflow) PrepareIntoLocked(lock *project.Lock, proj *project.Project, seen *proposal.Proposal, accept []int, overlay map[string][]byte) (*proposal.Prepared, *Error) {
	if e := held(lock); e != nil {
		return nil, e
	}
	if e := w.CheckRecovery(proj.Root, seen.ID); e != nil {
		return nil, e
	}
	cur, e := w.LoadProposal(proj.Root, seen.ID)
	if e != nil {
		return nil, e
	}
	if cur.Status != proposal.StatusPending {
		e := fail(ExitFail, "not_pending", "proposal %s is already %s", cur.ID, cur.Status)
		e.Proposal = cur
		return nil, e
	}
	if e := CheckReady(cur); e != nil {
		return nil, e
	}
	if ns := Changed(seen, cur, SortedInts(accept)); len(ns) > 0 || seen.Revision != cur.Revision {
		e := fail(ExitFail, "changed", "proposal %s changed since it was shown (hunk %s revised elsewhere); review it again", cur.ID, JoinInts(ns))
		e.Proposal = cur
		return nil, e
	}
	// The buffers may hold hunks no save has recorded yet: seen says which,
	// and overlay is judged with them applied.
	if seen.Decision != nil {
		d := MergeDecision(cur, *seen.Decision)
		cur.Decision = &d
	}
	if len(cur.CheckFiles) > 0 {
		cur.CheckFiles = seen.CheckFiles
	}
	set := map[int]bool{}
	for _, n := range accept {
		set[n] = true
	}
	fresh, err := project.Open(proj.Root)
	if err != nil {
		return nil, internalErr(err)
	}
	proj = fresh
	loaded, err := proj.Load(overlay)
	if err != nil {
		return nil, internalErr(err)
	}
	if e := w.checkPrimaryLocked(proj.Root, cur, loaded); e != nil {
		return nil, e
	}
	if e := CheckSnapshot(cur, loaded.Spec); e != nil {
		return nil, e
	}
	if e := wholeModel(cur, set); e != nil {
		return nil, e
	}
	prep, err := prepareProposal(cur, loaded, set)
	if err != nil {
		var stale *proposal.StaleError
		if errors.As(err, &stale) {
			return nil, fail(ExitFail, "stale", "proposal %s no longer applies: %s", cur.ID, stale.Error())
		}
		return nil, internalErr(err)
	}
	if !cur.IsModel() {
		if prep, err = dropBreakingHunks(proj, loaded.Spec, cur, set, prep, overlay); err != nil {
			return nil, internalErr(err)
		}
	}
	if pr := cur.Primary; pr != nil && set[pr.N] && !pr.InWorktree && !cur.AppliedHunk(pr.N) {
		plan, err := edit.NewPlan(proj.Root, prep.Edits)
		if err != nil {
			return nil, fail(ExitFail, "stale", "proposal %s does not apply cleanly: %v", cur.ID, err)
		}
		if e := checkPrimaryResult(proj, plan, pr, overlay); e != nil {
			return nil, e
		}
	}
	return prep, nil
}

// Settle records the decision on pending proposal id without writing a
// file: the hunks d.Applied have reached the disk (status applied), or the
// reviewer dismissed every hunk (status rejected, with d.Reason). A proposal decided elsewhere meanwhile is exit 1
// `not_pending`. It holds .spec/lock (see decisionLock).
func (w *Workflow) Settle(root, id string, d proposal.Decision) (*proposal.Proposal, *Error) {
	lock, e := w.decisionLock(root, "spec ui")
	if e != nil {
		return nil, e
	}
	defer func() { _ = lock.Release() }()
	return w.SettleLocked(lock, root, id, d)
}

// SettleLocked is Settle for a caller that holds .spec/lock (taken with
// DecisionLock).
func (w *Workflow) SettleLocked(lock *project.Lock, root, id string, d proposal.Decision, snapshots ...map[string]string) (*proposal.Proposal, *Error) {
	return w.settleLocked(lock, root, id, d, nil, snapshots...)
}

// settleLocked is SettleLocked that also records check, a model check made at
// approval, on a model proposal applied.
func (w *Workflow) settleLocked(lock *project.Lock, root, id string, d proposal.Decision, check *proposal.ModelCheck, snapshots ...map[string]string) (*proposal.Proposal, *Error) {
	if e := held(lock); e != nil {
		return nil, e
	}
	if e := w.CheckRecovery(root, id); e != nil {
		return nil, e
	}
	cur, e := w.LoadProposal(root, id)
	if e != nil {
		return nil, e
	}
	if cur.Status != proposal.StatusPending {
		return cur, fail(ExitFail, "not_pending", "proposal %s was %s meanwhile", cur.ID, cur.Status)
	}
	d.By = ByOr(d.By)
	d.At = w.stamp()
	d = MergeDecision(cur, d)
	cur.Status = SettleStatus(cur, d)
	if cur.Status == proposal.StatusRejected && d.Reason == "" {
		d.Reason = "dismissed in spec ui"
	}
	cur.Decision = &d
	if len(snapshots) > 0 && len(cur.CheckFiles) > 0 && len(d.Applied) > 0 {
		cur.CheckFiles = snapshots[0]
	}
	if check != nil && cur.IsModel() && len(d.Applied) > 0 {
		cur.Model.Check = *check
	}
	cur.Normalize()
	if err := w.Store(root).Save(cur); err != nil {
		return nil, internalErr(err)
	}
	return cur, nil
}

// SettleStatus is the status settling p with merged decision d gives it.
func SettleStatus(p *proposal.Proposal, d proposal.Decision) string {
	status := proposal.StatusApplied
	if len(d.Failed) > 0 {
		status = proposal.StatusPending
	} else if len(d.Applied) == 0 {
		status = proposal.StatusRejected
	}
	if pr := p.Primary; pr != nil && !pr.InWorktree && len(d.Applied) > 0 && !slices.Contains(d.Applied, pr.N) {
		status = proposal.StatusPending
	}
	return status
}

// CheckReady prevents required-check failures or progress from masquerading
// as a reviewable, completed proposal.
func CheckReady(p *proposal.Proposal) *Error {
	if p.Status == proposal.StatusPending && p.StalePrimary != nil {
		e := fail(ExitFail, "stale", "%s", p.StalePrimary.Reason)
		e.Proposal = p
		return e
	}
	if !p.ChecksBlocking() {
		return nil
	}
	e := fail(ExitFail, "check_required", "proposal %s requires a completed check (%s): %s", p.ID, p.CheckState, p.CheckMessage)
	e.Proposal = p
	return e
}

// MergeDecision is d with the hunks p already records as applied.
func MergeDecision(p *proposal.Proposal, d proposal.Decision) proposal.Decision {
	if p.Decision == nil {
		return d
	}
	for _, n := range p.Decision.Applied {
		found := false
		for _, m := range d.Applied {
			if n == m {
				found = true
				break
			}
		}
		if !found {
			d.Applied = append(d.Applied, n)
		}
	}
	d.Applied = SortedInts(d.Applied)
	return d
}

// SettleSeenLocked refuses to settle a revision the reviewer has not seen.
// Callers must hold the same lock across PrepareInto, buffer mutation and settlement.
func (w *Workflow) SettleSeenLocked(lock *project.Lock, root string, seen *proposal.Proposal, d proposal.Decision) (*proposal.Proposal, *Error) {
	if e := held(lock); e != nil {
		return nil, e
	}
	if e := w.CheckRecovery(root, seen.ID); e != nil {
		return nil, e
	}
	cur, e := w.LoadProposal(root, seen.ID)
	if e != nil {
		return nil, e
	}
	if cur.Revision != seen.Revision || len(Changed(seen, cur, nil)) != 0 {
		e := fail(ExitFail, "changed", "proposal %s was revised; review it again", cur.ID)
		e.Proposal = cur
		return nil, e
	}
	var check *proposal.ModelCheck
	if seen.IsModel() && seen.Model.Check.Rechecked {
		check = &seen.Model.Check
	}
	return w.settleLocked(lock, root, seen.ID, d, check, seen.CheckFiles)
}

// CheckSnapshot prevents a completed required check from authorizing an edit
// against different surrounding context. The caller supplies freshly discovered
// files, with editor overlays when application targets buffers.
func CheckSnapshot(p *proposal.Proposal, spec *mdspec.Spec) *Error {
	if len(p.CheckFiles) == 0 || sameSnapshot(p.CheckFiles, spec) {
		return nil
	}
	e := fail(ExitFail, "stale_check", "Source files changed since the required check; recheck proposal %s before applying", p.ID)
	if p.Decision != nil && len(p.Decision.Applied) > 0 {
		e.Message = "Source files changed since the required check. " + ResidualGuidance(p)
	}
	e.Proposal = p
	return e
}

// RecordPartialSeenLocked records successful buffer edits while independent
// review items remain open. Undecided work is not mislabeled as a failure.
// The caller holds the lock across preparation, buffer mutation and this CAS.
func (w *Workflow) RecordPartialSeenLocked(lock *project.Lock, root string, seen *proposal.Proposal, d proposal.Decision) (*proposal.Proposal, *Error) {
	if e := held(lock); e != nil {
		return nil, e
	}
	if e := w.CheckRecovery(root, seen.ID); e != nil {
		return nil, e
	}
	cur, e := w.LoadProposal(root, seen.ID)
	if e != nil {
		return nil, e
	}
	if cur.Status != proposal.StatusPending || cur.Revision != seen.Revision || len(Changed(seen, cur, nil)) != 0 {
		e := fail(ExitFail, "changed", "proposal %s changed while applying; review it again", seen.ID)
		e.Proposal = cur
		return cur, e
	}
	d = MergeDecision(cur, d)
	if cur.Decision != nil {
		for _, old := range cur.Decision.Failed {
			found := false
			for _, current := range d.Failed {
				if current.N == old.N {
					found = true
				}
			}
			for _, n := range d.Applied {
				if n == old.N {
					found = true
				}
			}
			if !found {
				d.Failed = append(d.Failed, old)
			}
		}
	}
	d.By, d.At = ByOr(d.By), w.stamp()
	cur.Decision, cur.Status = &d, proposal.StatusPending
	if len(cur.CheckFiles) > 0 {
		cur.CheckFiles = seen.CheckFiles
	}
	cur.Normalize()
	if err := w.Store(root).Save(cur); err != nil {
		return nil, internalErr(err)
	}
	return cur, nil
}
