// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// proposeExternal publishes the author's primary text before making any model
// request. A required-check failure is durable pending work, not lost text.
func (w *Workflow) proposeExternal(ctx context.Context, proj *project.Project, req ProposeRequest, target *mdspec.Spec, edits []*pendingEdit, restores []edit.FileEdit, res *ProposeResult) (*ProposeResult, *Error) {
	if ctx.Err() != nil {
		return nil, fail(ExitFail, "cancelled", "cancelled before publishing proposal")
	}
	token, owner, e := newCheckOwner(proj.Root)
	if e != nil {
		return nil, e
	}
	defer func() { _ = owner.Release() }()
	for _, group := range batchGroups(edits, req.Keep) {
		p := &proposal.Proposal{Created: w.stamp(), Author: req.Author, SourceKind: "agent", Kind: proposal.KindChange, Why: group[0].change.Why, Status: proposal.StatusPending, CheckState: "queued", CheckOwner: token}
		p.Primary, p.Batch = primariesOf(group)
		FillBase(p, target)
		snapshotCheck(p, target)
		res.Proposals = append(res.Proposals, p)
	}
	if len(restores) > 0 {
		if e := w.writeRestore(proj.Root, restores, res.Proposals); e != nil {
			return nil, e
		}
	} else {
		lock, e := w.DecisionLock(ctx, proj.Root, "publish proposals")
		if e != nil {
			return nil, e
		}
		for i, p := range res.Proposals {
			if err := w.Store(proj.Root).Create(p); err != nil {
				_ = lock.Release()
				res.Proposals = res.Proposals[:i]
				return res, internalErr(err)
			}
		}
		_ = lock.Release()
	}
	for _, pe := range edits {
		if !pe.primary.InWorktree {
			res.Restored = append(res.Restored, "$"+pe.change.Name)
		}
	}
	for i, p := range res.Proposals {
		checked, e := w.checkPublished(ctx, proj, p.ID, req.Full, req.OnStage, nil, false, false, token)
		if checked != nil {
			res.Proposals[i] = checked
		}
		// Failures stay on the published card; callers receive its identity and
		// may retry it. A concurrent decision must not be overwritten.
		if e != nil && (checked == nil || checked.Status == proposal.StatusPending && checked.StalePrimary == nil && (checked.CheckState == "running" || checked.CheckState == "queued")) {
			return res, e
		}
	}
	return res, nil
}

func snapshotHashes(spec *mdspec.Spec) map[string]string {
	out := map[string]string{}
	for _, f := range spec.Files {
		out[f.Path] = proposal.FileHash(f.Source())
	}
	return out
}

func sameSnapshot(want map[string]string, spec *mdspec.Spec) bool {
	if len(want) != len(spec.Files) {
		return false
	}
	for _, f := range spec.Files {
		if want[f.Path] != proposal.FileHash(f.Source()) {
			return false
		}
	}
	return true
}

// batchGroups splits the edits of one spec propose run into proposals: one
// batch holding them all, or with keep (the edits stay in the working tree,
// and a check of each may be answered and redone alone) one per edit.
func batchGroups(edits []*pendingEdit, keep bool) [][]*pendingEdit {
	if !keep {
		return [][]*pendingEdit{edits}
	}
	out := make([][]*pendingEdit, 0, len(edits))
	for _, pe := range edits {
		out = append(out, []*pendingEdit{pe})
	}
	return out
}

// primariesOf numbers the primaries of group 1..k and returns the first and
// the rest.
func primariesOf(group []*pendingEdit) (*proposal.Primary, []*proposal.Primary) {
	var rest []*proposal.Primary
	for i, pe := range group {
		pe.primary.N = i + 1
		if i > 0 {
			rest = append(rest, pe.primary)
		}
	}
	return group[0].primary, rest
}

// CheckPublished retries the original snapshot. RecheckPublished explicitly
// refreshes the full context while preserving the author's primary intent.
func (w *Workflow) CheckPublished(ctx context.Context, proj *project.Project, id string, full bool, onStage func(StageEvent)) (*proposal.Proposal, *Error) {
	return w.checkPublished(ctx, proj, id, full, onStage, nil, false, false, "")
}

// RecheckPublished captures a new generation against immutable editor overlays
// (nil means disk). The caller must still check the current target at approval.
func (w *Workflow) RecheckPublished(ctx context.Context, proj *project.Project, id string, full bool, onStage func(StageEvent), overlay map[string][]byte) (*proposal.Proposal, *Error) {
	captured := map[string][]byte{}
	for path, data := range overlay {
		captured[path] = append([]byte(nil), data...)
	}
	return w.checkPublished(ctx, proj, id, full, onStage, captured, true, true, "")
}

// RefreshForAutoApprove refreshes changed context without clearing a durable
// primary failure. Clearing that marker requires an explicitly requested recheck.
func (w *Workflow) RefreshForAutoApprove(ctx context.Context, proj *project.Project, id string, full bool) (*proposal.Proposal, *Error) {
	return w.checkPublished(ctx, proj, id, full, nil, nil, true, false, "")
}

func (w *Workflow) checkPublished(ctx context.Context, proj *project.Project, id string, full bool, onStage func(StageEvent), overlay map[string][]byte, recheck, explicit bool, batchOwner string) (*proposal.Proposal, *Error) {
	if e := w.CheckRecoveryBeforeLock(proj.Root, id); e != nil {
		return nil, e
	}
	// Reconciliation consumes ready checkpoints before any new model work.
	if e := w.ReconcileChecks(proj.Root); e != nil {
		return nil, e
	}
	token, owner, e := newCheckOwner(proj.Root)
	if e != nil {
		return nil, e
	}
	defer func() { _ = owner.Release() }()
	lock, e := w.DecisionLock(ctx, proj.Root, "claim required check")
	if e != nil {
		return nil, e
	}
	p, e := w.LoadProposal(proj.Root, id)
	if e != nil {
		_ = lock.Release()
		return nil, e
	}
	refuse := func(e *Error) (*proposal.Proposal, *Error) { _ = lock.Release(); return p, e }
	if e := w.CheckRecovery(proj.Root, id); e != nil {
		return refuse(e)
	}
	if p.Status != proposal.StatusPending || p.Primary == nil || !recheck && !HasCheckSnapshot(p) {
		return refuse(fail(ExitFail, "retry_unavailable", "proposal %s has no retryable required-check snapshot", id))
	}
	if p.Decision != nil && len(p.Decision.Applied) > 0 {
		return refuse(fail(ExitFail, "retry_unavailable", "%s", ResidualGuidance(p)))
	}
	if p.CheckOwner != "" && p.CheckOwner != batchOwner {
		old, err := ownerLock(proj.Root, p.CheckOwner)
		if err != nil {
			var busy *project.LockHeldError
			if errors.As(err, &busy) {
				return refuse(fail(ExitFail, "check_running", "proposal %s is being checked by another worker", id))
			}
			return refuse(internalErr(err))
		}
		if old != nil {
			defer func() { _ = old.Release() }()
		}
		// A writer may have kept reconciliation from taking the project lock.
		// Check again while claiming, before replacing the dead attempt token.
		cp, err := loadCheckpoint(proj.Root, p)
		if err != nil {
			return refuse(internalErr(err))
		}
		if cp != nil {
			recovered, e := w.finishCheckLocked(proj.Root, *cp, overlay)
			if recovered != nil {
				p = recovered
			}
			if e != nil && !(recheck && e.Code == "stale") {
				return refuse(e)
			}
			if p.CheckState == "complete" && !recheck {
				return refuse(nil)
			}
		}
	}
	loaded, err := project.Load(proj.Root, overlay)
	if err != nil {
		return refuse(internalErr(err))
	}
	if e := w.checkPrimaryLocked(proj.Root, p, loaded); e != nil && (e.Code != "stale" || !explicit || validateCheckPrimary(p, loaded.Spec) != nil) {
		return refuse(e)
	}
	if !recheck && !checkFresh(p, loaded.Spec) {
		p.CheckState, p.CheckMessage, p.CheckOwner = "stale", "Text the check depends on changed; recheck this proposal", ""
		if err := w.Store(proj.Root).Save(p); err != nil {
			return refuse(internalErr(err))
		}
		return refuse(fail(ExitFail, "stale", "required-check snapshot changed; recheck the proposal"))
	}
	if !recheck && p.CheckState == "complete" {
		return refuse(nil)
	}
	for _, pr := range p.Primaries() {
		if as := loaded.Spec.Lookup(strings.TrimPrefix(pr.Name, "$")); len(as) == 1 {
			pr.File = as[0].File
		}
	}
	// The check is fresh here (or a recheck), so this records the same
	// dependencies, or turns a legacy whole-tree snapshot into them.
	snapshotCheck(p, loaded.Spec)
	if recheck {
		p.Hunks, p.Notes, p.Questions, p.Summary, p.Inference = nil, nil, nil, "", nil
		FillBase(p, loaded.Spec)
	}
	p.CheckGeneration++
	p.CheckOwner, p.CheckState, p.CheckMessage = token, "running", "Checking proposed change"
	if err := w.Store(proj.Root).Save(p); err != nil {
		return refuse(internalErr(err))
	}
	_ = lock.Release()
	finish := func(result *proposal.Proposal, reason *Error) (*proposal.Proposal, *Error) {
		cp := checkCheckpoint{Seen: p, State: "complete", Message: "Check finished", Result: result, Recheck: explicit}
		if reason != nil {
			cp.State, cp.Message, cp.Result = "failed", reason.Message, nil
			if reason.Code == "cancelled" {
				cp.Message = "Check cancelled; proposal remains pending"
			}
			if reason.Code == "stale" {
				cp.State = "stale"
			}
		}
		if err := saveCheckpoint(proj.Root, cp); err != nil {
			return p, internalErr(err)
		}
		if cp.State == "complete" && w.BeforeCommit != nil {
			w.BeforeCommit()
		}
		lock, e := w.checkCompletionLock(proj.Root)
		if e != nil {
			return p, e
		}
		defer func() { _ = lock.Release() }()
		saved, e := w.finishCheckLocked(proj.Root, cp, overlay)
		if e != nil {
			return saved, e
		}
		return saved, reason
	}
	ck, e := w.OpenChecker(ctx, full)
	if e != nil {
		return finish(nil, e)
	}
	ck.OnStage = func(ev StageEvent) {
		if onStage != nil {
			onStage(ev)
		}
		// Progress is optional. Never delay or discard paid work for its write.
		lock, err := project.AcquireLock(proj.Root, "required check progress")
		if err != nil {
			return
		}
		defer func() { _ = lock.Release() }()
		cur, e := w.LoadProposal(proj.Root, p.ID)
		if e != nil || cur.Status != proposal.StatusPending || cur.Revision != p.Revision || cur.CheckOwner != token {
			return
		}
		cur.CheckMessage = fmt.Sprintf("%s: %s", ev.Stage.Name, ev.Stage.Outcome)
		if err := w.Store(proj.Root).Save(cur); err == nil {
			p = cur
		}
	}
	prs := p.Primaries()
	chs := make([]*specctx.Change, 0, len(prs))
	protected := map[string][]pos.Range{}
	for _, pr := range prs {
		ch := changeOf(pr, p.Why)
		chs = append(chs, ch)
		if ch.Action != specctx.ActionAdd || pr.InWorktree {
			if as := loaded.Spec.Lookup(ch.Name); len(as) == 1 {
				protected[as[0].File] = append(protected[as[0].File], as[0].BodyRange)
			}
		}
	}
	result, e := w.CheckBatch(ck, loaded.Spec, chs, prs, protected, p.Author)
	// Cancellation before the checkpoint boundary wins over a successful reply.
	if ctx.Err() != nil {
		e = fail(ExitFail, "cancelled", "Check cancelled")
	}
	return finish(result, e)
}

func validateCheckPrimary(p *proposal.Proposal, spec *mdspec.Spec) error {
	accept := map[int]bool{}
	for _, pr := range p.Primaries() {
		if p.AppliedHunk(pr.N) {
			continue
		}
		if !pr.InWorktree {
			accept[pr.N] = true
			continue
		}
		as := spec.Lookup(strings.TrimPrefix(pr.Name, "$"))
		if pr.Action == specctx.ActionRemove {
			if len(as) != 0 {
				return fmt.Errorf("%s is present again", pr.Name)
			}
		} else if len(as) != 1 || !slices.Contains(p.CheckedTextsOf(pr), spec.File(as[0].File).Text(as[0].BodyRange)) {
			return fmt.Errorf("%s no longer matches the proposed text", pr.Name)
		}
	}
	if len(accept) > 0 {
		if _, err := p.Prepare(spec, accept); err != nil {
			return err
		}
	}
	return nil
}

// PublishChange is the direct-change counterpart of spec propose: preserve the
// primary before checking, even when no provider is configured.
func (w *Workflow) PublishChange(ctx context.Context, proj *project.Project, spec *mdspec.Spec, primary *proposal.Primary, why, author string, full bool) (*proposal.Proposal, *Error) {
	token, owner, e := newCheckOwner(proj.Root)
	if e != nil {
		return nil, e
	}
	defer func() { _ = owner.Release() }()
	p := &proposal.Proposal{CheckOwner: token, Created: w.stamp(), Author: author, SourceKind: "agent", Kind: proposal.KindChange, Why: why, Primary: primary, Status: proposal.StatusPending, CheckState: "queued"}
	FillBase(p, spec)
	snapshotCheck(p, spec)
	lock, e := w.DecisionLock(ctx, proj.Root, "publish proposal")
	if e != nil {
		return nil, e
	}
	err := w.Store(proj.Root).Create(p)
	_ = lock.Release()
	if err != nil {
		return nil, internalErr(err)
	}
	checked, e := w.checkPublished(ctx, proj, p.ID, full, nil, nil, false, false, token)
	if checked == nil {
		return p, e
	}
	return checked, e
}
