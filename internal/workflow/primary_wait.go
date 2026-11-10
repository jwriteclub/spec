// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// ResidualGuidance explains how to close partially applied work without losing
// its receipt or suggesting a recheck that cannot preserve hunk identities.
func ResidualGuidance(p *proposal.Proposal) string {
	return fmt.Sprintf("Proposal %s already has applied edits. Inspect the applied hunks; ask the user to explicitly reject the remaining edits with `spec reject %s --why ...` to close this receipt, then file the remaining work as a new proposal. Applied edits and their receipt are preserved", p.ID, p.ID)
}

// observePrimaryLocked validates the current primary without calling a model.
// Callers hold the project lock and supply a freshly loaded proposal and source.
// p may carry unsaved state laid over it (PrepareIntoLocked), so only the marker
// is persisted, on the proposal as stored.
// It never clears an earlier observation, even if the source was reverted.
// A model proposal has no primary: the marker says that one of its hunks no
// longer applies, which stales the whole edit.
func (w *Workflow) observePrimaryLocked(root string, p *proposal.Proposal, loaded *project.Loaded) *Error {
	if !observable(p) {
		return nil
	}
	var err error
	if p.IsModel() {
		err = validateModel(p, loaded)
	} else {
		err = validateCheckPrimary(p, loaded.Spec)
	}
	if err != nil {
		reason := fmt.Sprintf("%s. Proposal remains pending. Inspect proposal %s and the current source; ask the user to explicitly close or rework it, then file the remaining work again", err, p.ID)
		if p.IsModel() {
			reason = fmt.Sprintf("%s. Proposal remains pending. Inspect proposal %s and the model as it is now; withdraw it with `spec reject %s --why \"withdrawn: ...\" --by YOUR-NAME` and send the change again against the current text", err, p.ID, p.ID)
		}
		stored, e := w.LoadProposal(root, p.ID)
		if e != nil {
			return e
		}
		stored.StalePrimary = &proposal.StalePrimary{Reason: reason}
		if err := w.Store(root).Save(stored); err != nil {
			return internalErr(err)
		}
		// Only now is the marker durable evidence.
		p.StalePrimary, p.Revision = stored.StalePrimary, stored.Revision
	}
	return nil
}

// observable reports whether p is pending work whose applicability can be
// observed: an unapplied primary, or a model edit.
func observable(p *proposal.Proposal) bool {
	if p.Status != proposal.StatusPending || p.StalePrimary != nil {
		return false
	}
	if p.IsModel() {
		return true
	}
	return p.Primary != nil && !p.AppliedHunk(p.Primary.N)
}

func (w *Workflow) checkPrimaryLocked(root string, p *proposal.Proposal, loaded *project.Loaded) *Error {
	if e := w.observePrimaryLocked(root, p, loaded); e != nil {
		return e
	}
	if p.Status == proposal.StatusPending && p.StalePrimary != nil {
		e := fail(ExitFail, "stale", "%s", p.StalePrimary.Reason)
		e.Proposal = p
		return e
	}
	return nil
}

// Wait watches receipts and observes primary applicability against current
// source on each poll (at most one second apart). Recovery checks and observation
// run under the project lock, before any stale result is returned. Pending
// records remain undecided and are never acknowledged by this operation.
func (w *Workflow) Wait(ctx context.Context, root string, ids []string, poll time.Duration) ([]*proposal.Proposal, error) {
	if poll <= 0 || poll > time.Second {
		poll = time.Second
	}
	return w.Store(root).WaitObserved(ctx, ids, poll, func(ps []*proposal.Proposal) error {
		lock, err := project.AcquireLock(root, "observe pending proposals")
		if err != nil {
			var busy *project.LockHeldError
			if !errors.As(err, &busy) {
				return LockErr(err)
			}
			// Never report stale while a stale receipt is fenced by the lock.
			for _, p := range ps {
				if p.Status == proposal.StatusPending && (p.StalePrimary != nil || checkArchivedRecovery(root, p.ID) != nil) {
					return LockErr(err)
				}
			}
			// An approval or commit is under way: mid-write, its receipt can
			// read pending or decided while its writer holds the lock. Judge
			// the batch once it lets go, under the lock, where unfinished
			// work is recovery_required and finished work is its decision.
			if w.CheckRecovery(root, "") != nil {
				return proposal.ErrObserveLater
			}
			// Ordinary receipt writers are brief and need no recovery action.
			return nil
		}
		defer func() { _ = lock.Release() }()
		// Inspect every ID first: archived interruption evidence for a later ID
		// has priority over a stale marker on an earlier one.
		if e := w.CheckRecovery(root, ""); e != nil {
			return e
		}
		for i, p := range ps {
			cur, e := w.LoadProposal(root, p.ID)
			if e != nil {
				return e
			}
			ps[i] = cur
			// Acknowledged evidence still fences unresolved work. Once Chris
			// explicitly closes it, deliver that receipt; never replay writes.
			if !cur.Decided() {
				if e := w.CheckRecovery(root, cur.ID); e != nil {
					return e
				}
			}
		}
		var loaded *project.Loaded
		for _, p := range ps {
			if !observable(p) {
				continue
			}
			if loaded == nil {
				var err error
				if loaded, err = project.Load(root, nil); err != nil {
					return internalErr(err)
				}
			}
			if e := w.observePrimaryLocked(root, p, loaded); e != nil {
				return e
			}
		}
		return nil
	})
}
