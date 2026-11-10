// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// CheckChange runs the per-edit cascade for ch over spec (the text the
// proposal applies to) and returns the proposal, not yet filed: the gate
// and triage stages (cascade.go), then, unless one of them cleared the
// edit, the contradiction check. A cleared proposal has no findings, its
// Inference.ClearedBy names the stage and its summary says why. protected
// are the ranges no finding hunk may touch.
func (w *Workflow) CheckChange(ck *Checker, spec *mdspec.Spec, ch *specctx.Change, pr *proposal.Primary, protected map[string][]pos.Range, author string) (*proposal.Proposal, *Error) {
	stages, cleared := ck.screen(spec, ch)
	if e := ck.cancelled(); e != nil {
		return nil, e
	}
	if cleared != nil {
		rec := ck.Record
		rec.Stages, rec.ClearedBy = stages, cleared.Name
		p := &proposal.Proposal{
			Created: w.stamp(), Author: author, Kind: proposal.KindChange,
			Why: ch.Why, Summary: ClearedText(*cleared) + "; the check did not run",
			Primary: pr, Status: proposal.StatusPending, Inference: &rec,
		}
		FillBase(p, spec)
		return p, nil
	}
	anchor := "$" + ch.Name
	check := proposal.Stage{Name: proposal.StageCheck, Provider: ck.Record.Provider, Model: ck.Record.Model}
	start := ck.clock()
	ck.emit(anchor, withOutcome(check, proposal.OutcomeRunning), start)
	c := specctx.Build(spec, specctx.Target{Change: ch}, ck.options())
	res, e := ck.Run(prompt.Change, c)
	if e != nil {
		check.Outcome, check.Reason = proposal.OutcomeFailed, oneLineText(e.Message)
		check.LatencyMS = ck.clock().Sub(start).Milliseconds()
		ck.emit(anchor, check, start)
		return nil, e
	}
	hunks, notes := finding.Verify(res, SourcesOf(spec), finding.Options{Protected: protected, FirstN: 2})
	rec := ck.Record
	check.Model, check.LatencyMS, check.CostUSD = rec.Model, ck.clock().Sub(start).Milliseconds(), rec.CostUSD
	check.Outcome, check.Findings = proposal.OutcomeDone, len(hunks)+len(notes)+len(res.Questions)
	if stages != nil {
		rec.Stages = append(stages, check)
	}
	ck.emit(anchor, check, start)
	p := &proposal.Proposal{
		Created: w.stamp(), Author: author, Kind: proposal.KindChange,
		Why: ch.Why, Summary: strings.TrimSpace(res.Summary), Primary: pr, Hunks: hunks, Notes: notes,
		Questions: res.Questions, Status: proposal.StatusPending, Inference: &rec,
	}
	FillBase(p, spec)
	return p, nil
}

// ProposeRequest says what `spec propose` turns into proposals.
type ProposeRequest struct {
	// Names are the anchors to propose (bare, without $). None means every
	// changed anchor not explained by an applied proposal, for a human or
	// with Keep; anyone else is refused (names_required), because the working
	// tree cannot say whose edit is whose.
	Names []string
	Why   string
	// Author is --by (or the human author).
	Author     string
	SourceKind string
	// Keep leaves the edits in the working tree and proposes only the
	// follow-on fixes.
	Keep bool
	// AutoApprove keeps the edits of proposals --auto-approve will apply
	// (they are not restored).
	AutoApprove bool
	Full        bool
	// OnStage, when set, hears each cascade stage of each anchor start and
	// end, on the calling goroutine.
	OnStage func(StageEvent)
}

// ProposeResult is what Propose made.
type ProposeResult struct {
	// Proposals are filed already without Keep (the restore needs them
	// filed first); with Keep their ID is "" until the caller files them.
	// Without Keep a proposal the cascade cleared is among them (an
	// agent's change is always reviewed).
	Proposals []*proposal.Proposal
	// Cleared are, with Keep, the checks the cascade cleared: not to be
	// filed (Inference.ClearedBy says which stage cleared each).
	Cleared []*proposal.Proposal
	// Skipped are the changed anchors that need no proposal, with why.
	Skipped []string
	// Restored are the anchors ("$Name") whose HEAD text was put back.
	Restored []string
}

// pendingEdit is one anchor edit being turned into a proposal.
type pendingEdit struct {
	entry   baseline.Entry
	change  *specctx.Change
	primary *proposal.Primary
	// restore puts the HEAD text back (nil with Keep).
	restore *edit.Splice
	file    string
}

// Propose turns the working tree's edits of anchors (against HEAD) into
// proposals: one contradiction check per changed anchor. Without Keep the
// proposals are filed and the HEAD text is restored, both under the lock.
// All model calls happen before anything is written, so a provider failure
// (or the end of ctx) leaves the working tree as it was. A change an applied
// proposal explains is skipped, named or not; one with a further edit on top
// is refused without Keep, since restoring HEAD would revert the applied
// change.
func (w *Workflow) Propose(ctx context.Context, proj *project.Project, req ProposeRequest) (*ProposeResult, *Error) {
	if e := w.CheckRecoveryBeforeLock(proj.Root, ""); e != nil {
		return nil, e
	}
	want := map[string]bool{}
	for _, n := range req.Names {
		want[n] = true
	}
	root := proj.Root
	loaded, err := proj.Load(nil)
	if err != nil {
		return nil, internalErr(err)
	}
	work := loaded.Spec
	b, e := w.LoadBaseline(proj, baseline.Source{}, false)
	if e != nil {
		return nil, e
	}
	if b.State == baseline.StateUnavailable {
		return nil, NoBaseline(b)
	}
	d := baseline.Compare(b.Spec, work)
	for _, sk := range d.Skipped {
		if len(want) == 0 || want[sk.Name] {
			return nil, fail(ExitCheck, "lint_errors", "$%s is defined more than once, so its change cannot be detected; give it one definition first (spec lint)", sk.Name)
		}
	}

	// Without this evidence an applied, uncommitted change looks like an edit
	// to propose, and restoring HEAD would revert it. So stop before any restore.
	evidence, err := w.Store(root).Applied()
	if err != nil {
		return nil, fail(ExitFail, "proposals_unreadable", "cannot read the proposal store, so applied changes cannot be told from new edits: %v", err)
	}
	needNames := len(want) == 0 && req.SourceKind != "human" && !req.Keep
	var unnamed []string
	var edits []*pendingEdit
	res := &ProposeResult{Skipped: []string{}}
	found := map[string]bool{}
	for _, e := range d.Entries {
		if len(want) > 0 && !want[e.Name] && !want[e.OldName] {
			continue
		}
		found[e.Name], found[e.OldName] = true, true
		switch {
		case e.Status == baseline.Renamed && (e.Changed || e.KindChanged):
			return nil, fail(ExitFail, "bad_change", "$%s was renamed from $%s and its body changed too; split them: undo the body edit, commit the rename (spec status prints its Spec-Rename line), then edit the body and run spec propose", e.Name, e.OldName)
		case e.Status == baseline.Renamed:
			res.Skipped = append(res.Skipped, fmt.Sprintf("$%s (renamed from $%s: commit it with the Spec-Rename line from spec status)", e.Name, e.OldName))
			continue
		case !e.NeedsChangeTrailer():
			res.Skipped = append(res.Skipped, fmt.Sprintf("$%s (%s: needs no proposal)", e.Name, e.Status))
			continue
		}
		acc := AccountFor(e, b.Spec, work, evidence)
		switch {
		case acc.Whole:
			res.Skipped = append(res.Skipped, fmt.Sprintf("$%s (applied by proposal %s: commit it with the Spec-Change line from spec status)", e.Name, strings.Join(acc.IDs, ", ")))
			continue
		case needNames:
			unnamed = append(unnamed, e.Name)
			continue
		case len(acc.IDs) > 0 && !req.Keep:
			return nil, fail(ExitFail, "applied_not_committed", "$%s holds the uncommitted change of applied proposal %s and a further edit, and restoring HEAD would revert the applied change; commit the applied change first: undo the further edit, commit with the Spec-Change line from spec status, then redo the edit and run spec propose %s", e.Name, strings.Join(acc.IDs, ", "), e.Name)
		}
		pe, e := pendingFor(b.Spec, work, e, req.Keep)
		if e != nil {
			return nil, e
		}
		pe.change.Why = strings.TrimSpace(req.Why)
		edits = append(edits, pe)
	}
	if len(unnamed) > 0 {
		return nil, namesRequired(unnamed, req)
	}
	for _, n := range req.Names {
		if !found[n] {
			return nil, fail(ExitFail, "not_found", "$%s does not differ from HEAD; spec status lists the changed anchors", n)
		}
	}
	if len(edits) == 0 {
		res.Proposals = []*proposal.Proposal{}
		return res, nil
	}

	// The text the proposals apply to: the working tree with every edited
	// anchor restored (or, with Keep, the working tree itself).
	target := work
	var fileEdits []edit.FileEdit
	if !req.Keep {
		byFile := map[string][]edit.Splice{}
		for _, pe := range restoreOrder(edits) {
			byFile[pe.file] = append(byFile[pe.file], *pe.restore)
		}
		overlay := map[string][]byte{}
		for path, sps := range byFile {
			src := work.File(path).Source()
			after, err := edit.Apply(src, sps)
			if err != nil {
				return nil, fail(ExitFail, "bad_change", "cannot restore the HEAD text in %s: %v; use --keep", path, err)
			}
			overlay[path] = after
			fileEdits = append(fileEdits, edit.FileEdit{Path: path, Snapshot: src, Splices: sps})
		}
		restored, err := proj.Load(overlay)
		if err != nil {
			return nil, internalErr(err)
		}
		target = restored.Spec
	}

	if req.SourceKind == "agent" {
		return w.proposeExternal(ctx, proj, req, target, edits, fileEdits, res)
	}
	ck, e := w.OpenChecker(ctx, req.Full)
	if e != nil {
		return nil, e
	}
	ck.OnStage = req.OnStage
	var ps []*proposal.Proposal
	for _, pe := range edits {
		protected := map[string][]pos.Range{}
		if pe.change.Action != specctx.ActionAdd || req.Keep {
			if as := target.Lookup(pe.change.Name); len(as) == 1 {
				protected[as[0].File] = []pos.Range{as[0].BodyRange}
			}
		}
		ck.ResetRecord()
		p, e := w.CheckChange(ck, target, pe.change, pe.primary, protected, req.Author)
		if e != nil {
			return nil, e
		}
		p.SourceKind = req.SourceKind
		ps = append(ps, p)
	}
	res.Proposals = ps
	if req.Keep {
		res.Proposals = []*proposal.Proposal{}
		for _, p := range ps {
			if p.Inference != nil && p.Inference.ClearedBy != "" {
				res.Cleared = append(res.Cleared, p)
			} else {
				res.Proposals = append(res.Proposals, p)
			}
		}
	}

	if !req.Keep {
		// Auto-approved proposals keep their edit: they are not restored.
		index := map[*pendingEdit]int{}
		for i, pe := range edits {
			index[pe] = i
		}
		var keepEdits []edit.FileEdit
		for _, fe := range fileEdits {
			var sps []edit.Splice
			for _, pe := range restoreOrder(edits) {
				if pe.file == fe.Path && !(req.AutoApprove && Eligible(ps[index[pe]])) {
					sps = append(sps, *pe.restore)
				}
			}
			if len(sps) > 0 {
				keepEdits = append(keepEdits, edit.FileEdit{Path: fe.Path, Snapshot: fe.Snapshot, Splices: sps})
			}
		}
		for i, pe := range edits {
			if req.AutoApprove && Eligible(ps[i]) {
				pe.primary.InWorktree = true
			}
		}
		if e := w.writeRestore(root, keepEdits, ps); e != nil {
			return nil, e
		}
	}
	for _, pe := range edits {
		if !pe.primary.InWorktree {
			res.Restored = append(res.Restored, "$"+pe.change.Name)
		}
	}
	return res, nil
}

// namesRequired refuses a propose that names nothing, listing what it could
// have meant and the command that files it.
func namesRequired(names []string, req ProposeRequest) *Error {
	argv := append([]string{"spec", "propose"}, names...)
	argv = append(argv, "--why", strings.TrimSpace(req.Why))
	if req.Author != "" {
		argv = append(argv, "--by", req.Author)
	}
	shown := make([]string, 0, len(argv))
	for _, a := range argv {
		if strings.ContainsAny(a, " \t\"'") {
			a = strconv.Quote(a)
		}
		shown = append(shown, a)
	}
	e := fail(ExitFail, "names_required", "name the anchors you edited, so that no one else's edit moves into your proposal: $%s differ from HEAD and no applied proposal explains them; leave out any that are not yours: %s", strings.Join(names, ", $"), strings.Join(shown, " "))
	e.Names, e.Argv = names, argv
	return e
}

// restoreOrder returns edits with the removed anchors in HEAD order. Two
// adjacent removed anchors are put back at the same offset (the second one's
// neighbor is absent until the first is restored), and splices at one offset
// apply in the order given.
func restoreOrder(edits []*pendingEdit) []*pendingEdit {
	out := append([]*pendingEdit(nil), edits...)
	headStart := func(pe *pendingEdit) int {
		if pe.entry.Status == baseline.Removed && pe.entry.Old != nil {
			return pe.entry.Old.BodyRange.Start
		}
		return -1
	}
	sort.SliceStable(out, func(i, j int) bool { return headStart(out[i]) < headStart(out[j]) })
	return out
}

// writeRestore files ps and then writes the HEAD texts back, both under
// the lock. The proposals hold the only copy of the edits once the restore
// is written, so they are filed first; if the restore fails, they are
// removed again and the edits stay in the working tree.
func (w *Workflow) writeRestore(root string, fileEdits []edit.FileEdit, ps []*proposal.Proposal) *Error {
	if len(fileEdits) == 0 {
		return nil
	}
	lock, err := project.AcquireLock(root, "spec propose")
	if err != nil {
		return LockErr(err)
	}
	defer func() { _ = lock.Release() }()
	if e := w.CheckRecovery(root, ""); e != nil {
		return e
	}
	plan, err := edit.NewPlan(root, fileEdits)
	if err != nil {
		return internalErr(err)
	}
	st := w.Store(root)
	var filed []string
	unfile := func() {
		for _, id := range filed {
			if err := st.Remove(id); err != nil && !errors.Is(err, proposal.ErrNotFound) {
				lg.WithError(err).Warn("cannot remove a proposal whose restore failed")
			}
		}
		for _, p := range ps {
			p.ID = ""
		}
	}
	for _, p := range ps {
		if err := st.Create(p); err != nil {
			unfile()
			return fail(ExitFail, "write_failed", "cannot write %s: %v; nothing was restored, the edits stay in the working tree", proposal.Dir, err)
		}
		filed = append(filed, p.ID)
	}
	if w.BeforeCommit != nil {
		w.BeforeCommit()
	}
	if err := plan.Commit(); err != nil {
		unfile()
		return WriteErr("spec propose", err)
	}
	return nil
}

// pendingFor builds the change, the primary hunk and the restore splice for
// one changed anchor.
func pendingFor(head, work *mdspec.Spec, e baseline.Entry, keep bool) (*pendingEdit, *Error) {
	pe := &pendingEdit{entry: e}
	ch := &specctx.Change{Name: e.Name}
	pr := &proposal.Primary{N: 1, Name: "$" + e.Name, InWorktree: keep}
	switch e.Status {
	case baseline.Added:
		a := e.New
		f := work.File(a.File)
		text := f.Text(a.BodyRange)
		pl := proposal.PlacementOf(f, a)
		ch.Action, ch.File, ch.NewKind, ch.NewText = specctx.ActionAdd, a.File, a.Kind, text
		ch.Where = DescribePlacement(pl)
		pr.Placement = &pl
		if !keep {
			sp := proposal.RemovalSplice(f.Source(), a.BodyRange)
			pe.restore, pe.file = &sp, a.File
		}
	case baseline.Removed:
		a := e.Old
		hf := head.File(a.File)
		text := hf.Text(a.BodyRange)
		ch.Action, ch.File, ch.OldKind, ch.OldText = specctx.ActionRemove, a.File, a.Kind, text
		if !keep {
			cur := work.File(a.File)
			if cur == nil {
				return nil, fail(ExitFail, "bad_change", "$%s was removed together with its file %s, so its text cannot be restored; recreate the file or use --keep", e.Name, a.File)
			}
			pl := proposal.PlacementOf(hf, a)
			pl.File = a.File
			sp, err := proposal.InsertSplice(cur, pl, text)
			if err != nil {
				return nil, fail(ExitFail, "bad_change", "cannot put $%s back: %v; use --keep", e.Name, err)
			}
			pe.restore, pe.file = &sp, a.File
		}
	default: // changed, kind changed
		o, n := e.Old, e.New
		oldText := head.File(o.File).Text(o.BodyRange)
		f := work.File(n.File)
		newText := f.Text(n.BodyRange)
		ch.Action = specctx.ActionChange
		if !e.Changed && e.KindChanged {
			ch.Action = specctx.ActionKind
		}
		ch.File, ch.OldKind, ch.NewKind, ch.OldText, ch.NewText = n.File, o.Kind, n.Kind, oldText, newText
		if !keep {
			sp := edit.Splice{Range: n.BodyRange, Old: newText, New: oldText}
			pe.restore, pe.file = &sp, n.File
		}
	}
	pr.Action, pr.File, pr.OldKind, pr.NewKind = ch.Action, ch.File, string(ch.OldKind), string(ch.NewKind)
	pr.OldText, pr.NewText = ch.OldText, ch.NewText
	pe.change, pe.primary = ch, pr
	return pe, nil
}

// DescribePlacement says in words where an added anchor goes.
func DescribePlacement(pl proposal.Placement) string {
	switch {
	case pl.AtStart:
		return "at the start of " + pl.File
	case pl.After != "":
		return "after " + pl.After
	case pl.Section != "":
		return "in §" + pl.Section + " of " + pl.File
	}
	return "in " + pl.File
}

// Duplicate is a pending proposal for an anchor that is about to get
// another one.
type Duplicate struct {
	// Name is the anchor, with its $.
	Name string
	// ID is the pending proposal that already covers it.
	ID string
}

// Message says it for a person: "$Name already has pending proposal ID".
func (d Duplicate) Message() string {
	return d.Name + " already has pending proposal " + d.ID
}

// PendingFor returns, for each of names (bare or with $) that is the anchor
// of a pending proposal in existing, the first such proposal, in the order
// of names. Decided proposals and analyses (which have no anchor) do not
// count.
func PendingFor(existing []*proposal.Proposal, names []string) []Duplicate {
	var out []Duplicate
	seen := map[string]bool{}
	for _, n := range names {
		n = "$" + strings.TrimPrefix(n, "$")
		if seen[n] {
			continue
		}
		seen[n] = true
		for _, p := range existing {
			if p.Status == proposal.StatusPending && p.Primary != nil && p.Primary.Name == n {
				out = append(out, Duplicate{Name: n, ID: p.ID})
				break
			}
		}
	}
	return out
}
