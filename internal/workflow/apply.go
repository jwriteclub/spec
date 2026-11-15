// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// FillBase records the hashes of the files and anchors p touches in spec.
func FillBase(p *proposal.Proposal, spec *mdspec.Spec) {
	p.Base = proposal.Base{Files: map[string]string{}, Anchors: map[string]string{}}
	files := map[string]bool{}
	anchors := map[string]bool{}
	for _, pr := range p.Primaries() {
		files[pr.File] = true
		anchors[strings.TrimPrefix(pr.Name, "$")] = true
	}
	for _, h := range p.Hunks {
		files[h.File] = true
		if f := spec.File(h.File); f != nil {
			if a := f.AnchorAt(h.Range.Start); a != nil {
				anchors[a.Name] = true
			}
		}
	}
	for path := range files {
		if f := spec.File(path); f != nil {
			p.Base.Files[path] = proposal.FileHash(f.Source())
		}
	}
	for name := range anchors {
		if as := spec.Lookup(name); len(as) == 1 {
			p.Base.Anchors["$"+name] = as[0].Hash
		}
	}
}

// Eligible reports whether --auto-approve may apply p: no breaking finding
// and no question.
func Eligible(p *proposal.Proposal) bool {
	return p.Status == proposal.StatusPending && p.StalePrimary == nil && !p.ChecksBlocking() && !finding.Breaking(p.Hunks, p.Notes) && len(p.Questions) == 0
}

// Applied is what an approval wrote.
type Applied struct {
	Proposal *proposal.Proposal
	// Trailers and Commit are set when the approval committed.
	Trailers []string
	Commit   string
	// Warning preserves successful commits whose index still needs inspection.
	Warning string
	// Skipped are the follow-on hunks a default approval (no hunks named)
	// left out.
	Skipped []int
}

// Apply writes the accepted hunks of pending proposal id under the project
// lock. With accept nil a change proposal applies its primaries only (the
// change itself, every anchor of a batch): the check's follow-on hunks need
// naming, so an approval never writes model text the approver did not pick;
// Applied.Skipped lists them. A proposal without a primary (analysis, fix,
// model) applies all its hunks. Apply then records the decision, and commits
// with the trailers when commit is set. A stale primary remains pending:
// without durable evidence, prior interrupted writes cannot be inferred.
func (w *Workflow) Apply(root, id string, accept []int, by string, commit bool) (*Applied, *Error) {
	return w.apply(root, id, accept, by, commit, nil)
}

// ApplySeen is Apply for a reviewer who decided on seen, the proposal as
// it was shown: when the change or an accepted hunk differs on disk (it
// was revised elsewhere since), nothing is written and the error is exit 1
// `changed`, so no hunk is applied that the reviewer has not seen.
func (w *Workflow) ApplySeen(root string, seen *proposal.Proposal, accept []int, by string, commit bool) (*Applied, *Error) {
	return w.apply(root, seen.ID, accept, by, commit, seen)
}

// Changed returns the numbers of the hunks among accept (all when nil)
// whose text differs between a and b, the change itself included.
func Changed(a, b *proposal.Proposal, accept []int) []int {
	want := map[int]bool{}
	for _, n := range accept {
		want[n] = true
	}
	all := accept == nil
	var out []int
	prims := func(p *proposal.Proposal) map[int]*proposal.Primary {
		m := map[int]*proposal.Primary{}
		for _, pr := range p.Primaries() {
			m[pr.N] = pr
		}
		return m
	}
	pa, pb := prims(a), prims(b)
	pnums := map[int]bool{}
	for n := range pa {
		pnums[n] = true
	}
	for n := range pb {
		pnums[n] = true
	}
	if len(pnums) == 0 && (a.Primary != nil || b.Primary != nil) {
		pnums[1] = true
	}
	// A batch whose membership changed differs as a whole.
	shape := len(pa) != len(pb)
	for n := range pnums {
		if !all && !want[n] && !shape {
			continue
		}
		if !reflect.DeepEqual(pa[n], pb[n]) || shape {
			out = append(out, n)
		}
	}
	hunks := func(p *proposal.Proposal) map[int]finding.Hunk {
		m := map[int]finding.Hunk{}
		for _, h := range p.Hunks {
			m[h.N] = h
		}
		return m
	}
	ha, hb := hunks(a), hunks(b)
	nums := map[int]bool{}
	for n := range ha {
		nums[n] = true
	}
	for n := range hb {
		nums[n] = true
	}
	for n := range nums {
		if !all && !want[n] {
			continue
		}
		x, okA := ha[n]
		y, okB := hb[n]
		if okA != okB || !reflect.DeepEqual(x, y) {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

func (w *Workflow) apply(root, id string, accept []int, by string, commit bool, seen *proposal.Proposal) (*Applied, *Error) {
	lock, err := project.AcquireLock(root, "spec approve "+id)
	if err != nil {
		return nil, LockErr(err)
	}
	defer func() { _ = lock.Release() }()

	if e := w.CheckRecovery(root, id); e != nil {
		return nil, e
	}
	if commit {
		if e := w.commitPreflight(root); e != nil {
			return nil, e
		}
	}
	st := w.Store(root)
	p, e := w.LoadProposal(root, id)
	if e != nil {
		return nil, e
	}
	if p.Status != proposal.StatusPending {
		return nil, fail(ExitFail, "not_pending", "proposal %s is already %s", id, p.Status)
	}
	if e := CheckReady(p); e != nil {
		return nil, e
	}
	skipped := []int{}
	if accept == nil {
		accept = p.PrimaryNumbers()
		if len(accept) == 0 {
			accept = p.Numbers()
		} else if len(pendingPrimaries(p, setOf(accept))) == 0 && !anyInWorktree(p) {
			return nil, usageErr("the change of proposal %s is already applied; name the remaining hunks with --hunks (%s) or use --all", id, JoinInts(p.FollowOns()))
		} else {
			skipped = p.FollowOns()
		}
	}
	set := map[int]bool{}
	valid := map[int]bool{}
	for _, n := range p.Numbers() {
		valid[n] = true
	}
	for _, n := range accept {
		if !valid[n] {
			return nil, usageErr("proposal %s has no hunk %d (hunks: %s)", id, n, JoinInts(p.Numbers()))
		}
		set[n] = true
	}
	for _, pr := range p.Primaries() {
		if set[pr.N] {
			continue
		}
		if p.IsBatch() {
			return nil, usageErr("hunks %s are the batch's changes and cannot be left out (hunk %d is not named); to drop the batch, run `spec reject %s --why ...`", JoinInts(p.PrimaryNumbers()), pr.N, id)
		}
		return nil, usageErr("hunk %d is the change itself and cannot be left out; to drop the change, run `spec reject %s --why ...`", pr.N, id)
	}
	if seen != nil {
		if ns := Changed(seen, p, SortedInts(accept)); len(ns) > 0 || seen.Revision != p.Revision {
			e := fail(ExitFail, "changed", "proposal %s changed since it was shown (hunk %s revised elsewhere); review it again; nothing was written", id, JoinInts(ns))
			e.Proposal = p
			return nil, e
		}
	}

	proj, e := openRoot(root)
	if e != nil {
		return nil, e
	}
	loaded, err := proj.Load(nil)
	if err != nil {
		return nil, internalErr(err)
	}
	if e := w.checkPrimaryLocked(root, p, loaded); e != nil {
		return nil, e
	}
	if e := CheckSnapshot(p, loaded.Spec); e != nil {
		return nil, e
	}
	if e := wholeModel(p, set); e != nil {
		return nil, e
	}
	prep, err := prepareProposal(p, loaded, set)
	if err != nil {
		var stale *proposal.StaleError
		if errors.As(err, &stale) {
			e := fail(ExitFail, "stale", "proposal %s no longer applies: %s; it remains pending because prior writes cannot be inferred. Inspect the proposal, HEAD, worktree and index; then explicitly reject it with `spec reject %s --why ...` and file only remaining work again", id, stale.Error(), id)
			e.Proposal = p
			return nil, e
		}
		return nil, internalErr(err)
	}
	if !p.IsModel() {
		if prep, err = dropBreakingHunks(proj, loaded.Spec, p, set, prep, nil); err != nil {
			return nil, internalErr(err)
		}
	}
	if len(prep.Applied) == 0 {
		var why []string
		for _, f := range prep.Failed {
			why = append(why, fmt.Sprintf("hunk %d: %s", f.N, f.Reason))
		}
		return nil, fail(ExitFail, "stale", "none of the accepted hunks of %s applies any more (%s); reject it with `spec reject %s --why stale`", id, strings.Join(why, "; "), id)
	}
	plan, err := edit.NewPlan(root, prep.Edits)
	if err != nil {
		return nil, fail(ExitFail, "stale", "proposal %s does not apply cleanly: %v", id, err)
	}
	for _, pr := range pendingPrimaries(p, set) {
		if e := checkPrimaryResult(proj, plan, pr, nil); e != nil {
			return nil, e
		}
	}
	// A model that moved since filing is checked again as approval would
	// leave it. Errors do not block approving; they are reported.
	if p.IsModel() && len(plan.Outputs) == 1 {
		w.recheckModel(context.Background(), loaded, p, plan.Outputs[0].Before, plan.Outputs[0].After)
	}
	if w.BeforeCommit != nil {
		w.BeforeCommit()
	}
	if HasCheckSnapshot(p) {
		fresh, err := project.Load(root, nil)
		if err != nil {
			return nil, internalErr(err)
		}
		if e := CheckSnapshot(p, fresh.Spec); e != nil {
			return nil, e
		}
	}
	// Every other decision takes the lock too, so p is still pending: a
	// rejection meanwhile waits for this approval and is then refused.
	intent, e := w.beginApproval(root, p, accept, prep.Applied, plan.Outputs)
	if e != nil {
		return nil, e
	}
	if err := plan.Commit(); err != nil {
		var partial *edit.WriteError
		if errors.As(err, &partial) && len(partial.RestoreFailed) > 0 {
			e := fail(ExitFail, "recovery_required", "%v; inspect the partial write with `spec recover` before retrying", err)
			e.Written, e.Proposal = true, intent.Proposal
			return nil, e
		}
		if err := finishRecovery(root); err != nil {
			return nil, internalErr(err)
		}
		return nil, WriteErr("spec approve", err)
	}

	if HasCheckSnapshot(p) {
		// The snapshot was verified above, under the lock: the files now
		// differ from it by this approval's own edits only.
		overlay := map[string][]byte{}
		for _, out := range plan.Outputs {
			overlay[out.Path] = out.After
		}
		if after, err := proj.Load(overlay); err == nil {
			RefreshOwnSnapshot(p, after.Spec)
		} else {
			lg.WithError(err).Warn("cannot refresh the check snapshot after applying")
		}
	}
	p.Status = proposal.StatusApplied
	if len(prep.Failed) > 0 {
		p.Status = proposal.StatusPending
	}
	d := MergeDecision(p, proposal.Decision{
		Accepted: SortedInts(accept), Applied: prep.Applied, Failed: prep.Failed,
		By: by, At: w.stamp(),
	})
	p.Decision = &d
	res := &Applied{Proposal: p, Trailers: []string{}, Skipped: skipped}
	var commitErr *Error
	if commit {
		res.Trailers, res.Commit, commitErr = w.commitTouched(proj, p, plan.Outputs, intent)
		p.Decision.Commit = res.Commit
		res.Warning = intent.warning
	}
	if warn := ModelErrorsWarning(p); warn != "" {
		res.Warning = strings.TrimSpace(warn + ". " + res.Warning)
	}
	if err := st.Save(p); err != nil {
		if res.Commit != "" {
			res.Warning = strings.TrimSpace(res.Warning + " Committed " + res.Commit + ", but the approval receipt could not be saved: " + err.Error() + "; run `spec recover` before continuing")
			return res, nil
		}
		e := fail(ExitFail, "recovery_required", "The files were written but the receipt could not be saved: %v; run `spec recover` before retrying", err)
		e.Written, e.Proposal, e.Trailers = true, intent.Proposal, res.Trailers
		return nil, e
	}
	// A failed ref transaction can have an uncertain outcome. Keep its exact
	// prepared commit identity for inspection even though file writes are recorded.
	if commitErr != nil && intent.refUncertain {
		commitErr.Code = "recovery_required"
		commitErr.Message += "; inspect the interrupted commit with `spec recover` before retrying"
	} else if !intent.NeedsInspection {
		if err := finishRecovery(root); err != nil {
			if res.Commit != "" {
				res.Warning = strings.TrimSpace(res.Warning + " Commit and receipt saved, but interruption evidence cleanup could not be confirmed: " + err.Error() + "; inspect `spec recover`")
				return res, nil
			}
			e := fail(ExitFail, "recovery_required", "The receipt was saved, but interruption evidence cleanup could not be confirmed: %v; inspect `spec recover`", err)
			e.Written, e.Proposal, e.Trailers = true, p, res.Trailers
			return nil, e
		}
	}
	if commitErr != nil {
		commitErr.Proposal, commitErr.Written, commitErr.Trailers = p, true, res.Trailers
		return nil, commitErr
	}
	return res, nil
}

// Touched returns the files an applied proposal wrote (and, for a change
// already in the working tree, its file), sorted.
func Touched(p *proposal.Proposal) []string {
	set := map[string]bool{}
	if d := p.Decision; d != nil {
		applied := map[int]bool{}
		for _, n := range d.Applied {
			applied[n] = true
		}
		for _, pr := range p.Primaries() {
			if !applied[pr.N] {
				continue
			}
			set[pr.File] = true
			if pr.Placement != nil && pr.Placement.File != "" {
				set[pr.Placement.File] = true
			}
		}
		for _, h := range p.Hunks {
			if applied[h.N] {
				set[h.File] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// dropBreakingHunks fails, alone, every accepted finding hunk that would
// introduce a lint error (a second definition of a name, a label edit that
// leaves references dangling, broken markup) on top of the primary change.
// The model writes those hunks, so approval must not trust them to keep the
// spec valid. It returns prep recomputed without them.
func dropBreakingHunks(proj *project.Project, spec *mdspec.Spec, p *proposal.Proposal, set map[int]bool, prep *proposal.Prepared, overlay map[string][]byte) (*proposal.Prepared, error) {
	only := map[int]bool{}
	for _, n := range p.PrimaryNumbers() {
		only[n] = true
	}
	var findings []int
	for _, n := range prep.Applied {
		if !only[n] {
			findings = append(findings, n)
		}
	}
	if len(findings) == 0 {
		return prep, nil
	}
	errsAfter := func(accept map[int]bool) ([]lintError, error) {
		pp, err := p.Prepare(spec, accept)
		if err != nil {
			return nil, err
		}
		return lintErrorsOf(proj, pp.Edits, overlay)
	}
	base, err := errsAfter(only)
	if err != nil {
		return nil, err
	}
	var failed []proposal.Failure
	for _, n := range findings {
		one := map[int]bool{n: true}
		for k := range only {
			one[k] = true
		}
		after, err := errsAfter(one)
		if err != nil {
			return nil, err
		}
		if d := firstNew(after, base); d != nil {
			failed = append(failed, proposal.Failure{N: n, Reason: fmt.Sprintf("it would introduce a lint error in %s: %s", d.File, d.Message)})
			delete(set, n)
		}
	}
	if len(failed) == 0 {
		return prep, nil
	}
	out, err := p.Prepare(spec, set)
	if err != nil {
		return nil, err
	}
	out.Failed = append(out.Failed, failed...)
	sort.Slice(out.Failed, func(i, j int) bool { return out.Failed[i].N < out.Failed[j].N })
	return out, nil
}

// lintError is an error diagnostic with a key that survives edits
// elsewhere: its code, file and the text it covers.
type lintError struct {
	key string
	d   diag.Diagnostic
}

// lintErrorsOf lints the project with edits applied in memory (over base,
// the editor's buffers, when given) and returns its error diagnostics.
func lintErrorsOf(proj *project.Project, edits []edit.FileEdit, base map[string][]byte) ([]lintError, error) {
	overlay := map[string][]byte{}
	for path, src := range base {
		overlay[path] = src
	}
	if len(edits) > 0 {
		plan, err := edit.NewPlan(proj.Root, edits)
		if err != nil {
			return nil, err
		}
		for _, out := range plan.Outputs {
			overlay[out.Path] = out.After
		}
	}
	loaded, err := proj.Load(overlay)
	if err != nil {
		return nil, err
	}
	var out []lintError
	for _, d := range lint.Run(loaded.Spec, lint.Options{Overrides: proj.Config.Lint}) {
		if d.Severity != diag.Error {
			continue
		}
		text := ""
		if f := loaded.Spec.File(d.File); f != nil && d.Range.Valid(len(f.Source())) {
			text = f.Text(d.Range)
		}
		out = append(out, lintError{key: d.Code + "\x00" + d.File + "\x00" + text, d: d})
	}
	return out, nil
}

// firstNew returns the first error of after that base does not have (as
// many times).
func firstNew(after, base []lintError) *diag.Diagnostic {
	have := map[string]int{}
	for _, e := range base {
		have[e.key]++
	}
	for i := range after {
		if have[after[i].key] > 0 {
			have[after[i].key]--
			continue
		}
		return &after[i].d
	}
	return nil
}

// checkPrimaryResult parses the planned files and verifies that the
// primary anchor ends up defined once (or, for a removal, not at all).
func checkPrimaryResult(proj *project.Project, plan *edit.Plan, pr *proposal.Primary, base map[string][]byte) *Error {
	overlay := map[string][]byte{}
	for path, src := range base {
		overlay[path] = src
	}
	for _, out := range plan.Outputs {
		overlay[out.Path] = out.After
	}
	after, err := proj.Load(overlay)
	if err != nil {
		return internalErr(err)
	}
	name := strings.TrimPrefix(pr.Name, "$")
	n := len(after.Spec.Lookup(name))
	want := 1
	if pr.Action == specctx.ActionRemove {
		want = 0
	}
	if n != want {
		return fail(ExitFail, "bad_change", "applying the change would leave %s defined %d times instead of %d (check the new text's label and where it goes); nothing was written", pr.Name, n, want)
	}
	return nil
}

// commitTouched commits what the approval of p wrote, and nothing else:
// the changes (see Changes) that the written edits touch, plus the change
// of the anchor a --keep proposal checked. Other edits of the same files
// stay in the working tree, uncommitted. It returns the trailers, the
// commit hash, and an error when committing failed (the files stay
// written).
func (w *Workflow) commitTouched(proj *project.Project, p *proposal.Proposal, outputs []edit.Output, intent *RecoveryIntent) ([]string, string, *Error) {
	b, e := w.LoadBaseline(proj, baseline.Source{}, false)
	if e != nil {
		return []string{}, "", e
	}
	if b.State == baseline.StateUnavailable {
		return []string{}, "", fail(ExitFail, "commit_failed", "the files were written, but cannot commit: %s", b.Reason)
	}
	if p.IsModel() {
		return w.commitModel(proj, b, p, outputs, intent)
	}
	loaded, err := proj.Load(nil)
	if err != nil {
		return []string{}, "", internalErr(err)
	}
	written := map[string][][2]int{}
	for _, out := range outputs {
		written[out.Path] = lineRuns(string(out.Before), string(out.After))
	}
	primaries := p.Names()
	var sel []Change
	for _, c := range Changes(b.Spec, loaded.Spec) {
		hit := slices.ContainsFunc(c.Anchors, func(a ChangedAnchor) bool { return slices.Contains(primaries, a.Name) })
		for _, f := range c.Files {
			for _, r := range c.Ranges(f) {
				for _, wr := range written[f] {
					hit = hit || touches(r, wr)
				}
			}
		}
		if hit {
			sel = append(sel, c)
		}
	}
	if len(sel) == 0 {
		return []string{}, "", fail(ExitFail, "commit_failed", "the files were written, but nothing the proposal touched differs from HEAD; nothing to commit")
	}

	var msg strings.Builder
	msg.WriteString(CommitSubject(p) + "\n")
	body := p.Why
	if body == "" {
		body = p.Summary
	}
	if strings.TrimSpace(body) != "" {
		msg.WriteString("\n" + strings.TrimSpace(body) + "\n")
	}
	res, e := w.commitSelected(proj, b, loaded.Spec, sel, msg.String(), intent)
	if e != nil {
		if e.Code == "commit_failed" && !strings.HasPrefix(e.Message, "the files were written") {
			e.Message = "the files were written, but " + e.Message
		}
		trailers := e.Trailers
		if trailers == nil {
			trailers = []string{}
		}
		return trailers, "", e
	}
	if res.Warning != "" {
		intent.warning = res.Warning
		lg.Warn(res.Warning)
	}
	return res.Trailers, res.Commit, nil
}

// CommitSubject is the first line of an approval's commit message.
func CommitSubject(p *proposal.Proposal) string {
	if pr := p.Primary; pr != nil {
		verb := map[string]string{
			specctx.ActionAdd: "add", specctx.ActionRemove: "remove", specctx.ActionKind: "change the kind of",
		}[pr.Action]
		if verb == "" {
			verb = "change"
		}
		if p.IsBatch() {
			return fmt.Sprintf("spec: %s and %d more (proposal %s)", strings.Join([]string{verb, pr.Name}, " "), len(p.Primaries())-1, p.ID)
		}
		return fmt.Sprintf("spec: %s %s (proposal %s)", verb, pr.Name, p.ID)
	}
	if fx := p.Fix; fx != nil {
		return fmt.Sprintf("spec: fix %s in %s (proposal %s)", fx.Code, fx.File, p.ID)
	}
	if p.IsModel() {
		return fmt.Sprintf("spec: change model %s (proposal %s)", p.Model.File, p.ID)
	}
	return fmt.Sprintf("spec: consistency fixes (proposal %s)", p.ID)
}

// Reject records a rejection of pending proposal id under .spec/lock (see
// decisionLock): a rejection while an approval of id writes waits for it,
// and is then refused as `not_pending`.
func (w *Workflow) Reject(root, id, why, by string) (*proposal.Proposal, *Error) {
	lock, e := w.decisionLock(root, "spec reject "+id)
	if e != nil {
		return nil, e
	}
	defer func() { _ = lock.Release() }()
	return w.RejectLocked(lock, root, id, why, by)
}

// RejectLocked is Reject for a caller that holds .spec/lock (taken with
// DecisionLock).
func (w *Workflow) RejectLocked(lock *project.Lock, root, id, why, by string) (*proposal.Proposal, *Error) {
	if e := held(lock); e != nil {
		return nil, e
	}
	if e := w.CheckRecovery(root, ""); e != nil {
		return nil, e
	}
	interrupted, err := archivedRecovery(root, id)
	if err != nil {
		return nil, recoveryReadError(err)
	}
	if interrupted != nil {
		why += "; closes unresolved interrupted approval; prior writes are not classified by this rejection (evidence: " + recoveryDir + "/" + interrupted.ID + ".json)"
	}
	p, e := w.LoadProposal(root, id)
	if e != nil {
		return nil, e
	}
	if p.Status != proposal.StatusPending {
		return nil, fail(ExitFail, "not_pending", "proposal %s is already %s", id, p.Status)
	}
	p.Status = proposal.StatusRejected
	d := MergeDecision(p, proposal.Decision{Reason: why, By: by, At: w.stamp()})
	if len(d.Applied) > 0 {
		p.Status = proposal.StatusApplied
		d.Reason = "Remaining edits rejected: " + why
	}
	p.Decision = &d
	if err := w.Store(root).Save(p); err != nil {
		return nil, internalErr(err)
	}
	return p, nil
}

// SetHunk replaces finding hunk h.N of pending proposal id with h, in the
// proposal as it is on disk now (it may have been decided elsewhere
// meanwhile: saving an older copy would undo that decision), under
// .spec/lock (see decisionLock).
func (w *Workflow) SetHunk(root, id string, h finding.Hunk) (*proposal.Proposal, *Error) {
	lock, e := w.decisionLock(root, "spec revise "+id)
	if e != nil {
		return nil, e
	}
	defer func() { _ = lock.Release() }()
	return w.SetHunkLocked(lock, root, id, h)
}

// SetHunkLocked is SetHunk for a caller that holds .spec/lock (taken with
// DecisionLock).
func (w *Workflow) SetHunkLocked(lock *project.Lock, root, id string, h finding.Hunk) (*proposal.Proposal, *Error) {
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
		return cur, fail(ExitFail, "not_pending", "proposal %s was %s meanwhile; the revision is dropped", cur.ID, cur.Status)
	}
	if cur.AppliedHunk(h.N) {
		return cur, fail(ExitFail, "already_applied", "hunk %d was already applied; edit the document instead", h.N)
	}
	found := false
	for i := range cur.Hunks {
		if cur.Hunks[i].N == h.N {
			cur.Hunks[i], found = h, true
		}
	}
	if !found {
		return cur, fail(ExitFail, "not_found", "proposal %s has no hunk %d any more", cur.ID, h.N)
	}
	if cur.IsModel() {
		// The check judged the text before this edit: approval checks again.
		cur.Model.Check.Hash = ""
	}
	if err := w.Store(root).Save(cur); err != nil {
		return nil, internalErr(err)
	}
	return cur, nil
}

// JoinInts formats numbers as "1,2,3".
func JoinInts(ns []int) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ",")
}

// SortedInts returns a sorted copy of ns.
func SortedInts(ns []int) []int {
	out := append([]int{}, ns...)
	sort.Ints(out)
	return out
}

// SetHunkSeen makes model/editor revisions conditional on the reviewed version.
func (w *Workflow) SetHunkSeen(root string, seen *proposal.Proposal, h finding.Hunk) (*proposal.Proposal, *Error) {
	lock, e := w.decisionLock(root, "spec revise "+seen.ID)
	if e != nil {
		return nil, e
	}
	defer func() { _ = lock.Release() }()
	return w.SetHunkSeenLocked(lock, root, seen, h)
}

// SetHunkSeenLocked is the same compare-and-swap while the caller holds the lock.
func (w *Workflow) SetHunkSeenLocked(lock *project.Lock, root string, seen *proposal.Proposal, h finding.Hunk) (*proposal.Proposal, *Error) {
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
	if cur.Revision != seen.Revision || len(Changed(seen, cur, []int{h.N})) > 0 {
		return cur, fail(ExitFail, "changed", "proposal changed while revising; review it again")
	}
	return w.SetHunkLocked(lock, root, seen.ID, h)
}

// setOf returns ns as a set.
func setOf(ns []int) map[int]bool {
	out := map[int]bool{}
	for _, n := range ns {
		out[n] = true
	}
	return out
}

// anyInWorktree reports whether a primary of p is already in the working
// tree (approving it writes nothing, yet still decides it).
func anyInWorktree(p *proposal.Proposal) bool {
	for _, pr := range p.Primaries() {
		if pr.InWorktree && !p.AppliedHunk(pr.N) {
			return true
		}
	}
	return false
}
