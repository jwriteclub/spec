// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/workflow"
)

// baselineTimeout bounds a baseline load.
const baselineTimeout = 20 * time.Second

// ---- ^P: propose ----

// propose starts ^P, without a prompt (plan-step4e-review-flow.md). On a
// diagnostic it fixes it: a mechanical fix goes into the buffer at once, a
// model fix runs in the background and lands in the sidebar. Otherwise, in
// an anchor changed vs HEAD it checks that anchor's change, and outside
// every anchor it checks every changed anchor; the findings land in the
// sidebar. A check reads the files, so modified buffers are saved first,
// after asking.
func (a *App) propose() tea.Cmd {
	if a.busy() || a.inModel() {
		return nil
	}
	if d, ok := workflow.MostSevere(a.diagsAt(a.cur.Ed.Cursor())); ok {
		return a.fix(d)
	}
	names, why := a.proposeTarget()
	if why != "" {
		a.setFlash("%s", why)
		return nil
	}
	return a.saveFirst("Check", func() tea.Cmd { return a.runPropose(names) })
}

// proposeTarget says what a check at the cursor checks: the changed anchor
// under the cursor (names holds it), or every changed anchor (names nil)
// when the cursor is in no anchor. why says why there is nothing to check
// ("" when there is).
func (a *App) proposeTarget() (names []string, why string) {
	if f := a.ws.File(a.cur.Ed.Path()); f != nil {
		if an := f.AnchorAt(a.cur.Ed.Cursor()); an != nil {
			if a.needsCheck(an) {
				return []string{an.Name}, ""
			}
			if r := a.renamesText(); r != "" && a.changeState(an) != "" {
				return nil, nothingToCheckRenamed(r)
			}
			return nil, fmt.Sprintf("Nothing to propose: $%s is unchanged vs HEAD (^P outside every anchor checks all changed anchors)", an.Name)
		}
	}
	if a.anyNeedsCheck() {
		return nil, ""
	}
	if r := a.renamesText(); r != "" {
		return nil, nothingToCheckRenamed(r)
	}
	return nil, "Nothing to propose: no anchor changed vs HEAD and no diagnostic at the cursor"
}

// proposeBlocked says why ^P has nothing to do ("" when it has). It reads
// the current analysis without reparsing, so the help bar can call it.
func (a *App) proposeBlocked() string {
	if a.inModel() {
		return modelClosed
	}
	if _, ok := a.diagUnderCursor(); ok {
		return ""
	}
	_, why := a.proposeTarget()
	return why
}

// runPropose runs `spec propose --keep` for names (all changed anchors
// when empty) in the background and files the proposals; their findings
// and suggested edits go to the sidebar. The cascade's stages show live in
// a card per anchor at the top of the sidebar and in the status line; an
// edit the gate or triage clears files nothing and its card says why. An
// earlier pending check of the same anchor by Chris is superseded.
func (a *App) runPropose(names []string) tea.Cmd {
	if a.busy() {
		return nil
	}
	wf, proj := a.wf, a.ws.Project
	label := "Checking every changed anchor"
	if len(names) > 0 {
		label = "Checking $" + strings.Join(names, ", $")
	}
	a.side.runs = nil
	for _, n := range names {
		a.side.run("$" + n)
	}
	if a.sideFits() {
		a.showSide()
	}
	events := make(chan workflow.StageEvent, 16)
	cmd := a.startJob(label, func(ctx context.Context) func(*App) tea.Cmd {
		defer close(events)
		res, e := wf.Propose(ctx, proj, workflow.ProposeRequest{
			Names: names, Author: proposal.AuthorHuman, SourceKind: "human", Keep: true, OnStage: stageSender(ctx, events),
		})
		if e == nil && ctx.Err() != nil {
			// Cancelled after the model answered: file nothing.
			return nil
		}
		if e == nil {
			// The spell check of the proposed text, as `spec propose` runs
			// it: its flags become notes, so the cards show them.
			wf.SpellNotes(ctx, proj, append(append([]*proposal.Proposal(nil), res.Proposals...), res.Cleared...))
			st := wf.Store(proj.Root)
			for _, p := range res.Proposals {
				if err := st.Create(p); err != nil {
					e = &workflow.Error{Exit: workflow.ExitFail, Code: "write_failed", Message: fmt.Sprintf("cannot write %s: %v", proposal.Dir, err)}
					break
				}
			}
		}
		return func(a *App) tea.Cmd {
			if e != nil {
				a.finishRuns(nil, nil, oneLine(e.Message))
				a.notice("Check failed", e)
				return nil
			}
			a.finishRuns(res.Proposals, res.Cleared, "")
			a.noteCleared(res.Cleared)
			if len(res.Proposals) == 0 && len(res.Cleared) == 0 {
				msg := "No changed anchor to check"
				if len(res.Skipped) > 0 {
					msg += " (skipped: " + strings.Join(res.Skipped, "; ") + ")"
				}
				a.setFlash("%s", msg)
				return nil
			}
			var cleared []string
			for _, p := range res.Cleared {
				for _, st := range p.Inference.Stages {
					if st.Name == p.Inference.ClearedBy {
						cleared = append(cleared, p.Primary.Name+": "+workflow.ClearedText(st))
					}
				}
			}
			if len(res.Proposals) == 0 {
				a.setFlash("%s", strings.Join(cleared, "; "))
				return nil
			}
			a.loadPending()
			var checked []string
			var cmds []tea.Cmd
			for _, p := range res.Proposals {
				cmds = append(cmds, a.supersede(p))
				checked = append(checked, p.Primary.Name)
				if !hasItems(p) {
					// Nothing found: the check is done.
					cmds = append(cmds, a.settleIfDone(p))
				}
			}
			a.loadPending()
			a.landed(res.Proposals, "Checked "+strings.Join(checked, ", "))
			if len(cleared) > 0 {
				a.flash += "; " + strings.Join(cleared, "; ")
			}
			// Reverted while the check ran: nothing left to check.
			cmds = append(cmds, a.withdrawReverted())
			return tea.Batch(cmds...)
		}
	})
	return tea.Batch(cmd, waitStage(a.jobSeq, events))
}

// supersede rejects Chris's earlier pending checks of the anchor p checks:
// the sidebar shows the latest check only. A check Chris is deciding
// already (waiting for .spec/lock) is left to that decision.
func (a *App) supersede(p *proposal.Proposal) tea.Cmd {
	if p.Primary == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, old := range a.pending {
		if old.ID == p.ID || !old.HumanSource() || old.Primary == nil || !old.Primary.InWorktree || old.Primary.Name != p.Primary.Name || a.waitingOn(old.ID) {
			continue
		}
		id := old.ID
		cmds = append(cmds, a.underLock(id, "supersede "+id, func(a *App, lock *project.Lock) tea.Cmd {
			if e := a.wf.CheckRecovery(a.ws.Root, id); e != nil {
				return nil
			}
			if _, e := a.wf.RejectLocked(lock, a.ws.Root, id, "superseded by check "+p.ID, proposal.AuthorHuman); e != nil {
				lg.WithError(e).Warn("cannot supersede " + id)
			}
			a.loadPending()
			return nil
		}))
	}
	return tea.Batch(cmds...)
}

// landed shows what new proposals ps added to the sidebar: it shows the
// sidebar (as a column, when there is room), selects their first card and
// flashes a count that leads with the key. what names the run when it
// found nothing.
func (a *App) landed(ps []*proposal.Proposal, what string) {
	pr, fx, qs := 0, 0, 0
	for _, p := range ps {
		for _, c := range a.side.cards {
			if c.p.ID != p.ID {
				continue
			}
			switch c.kind {
			case cardEdit:
				fx++
			case cardProblem:
				pr++
			case cardQuestion:
				qs++
			}
		}
	}
	if pr+fx+qs == 0 {
		a.setFlash("%s: nothing found", what)
		return
	}
	for _, p := range ps {
		if slices.ContainsFunc(a.side.cards, func(c *card) bool { return c.p.ID == p.ID }) {
			a.side.selectProposal(p.ID)
			break
		}
	}
	var parts []string
	if pr > 0 {
		parts = append(parts, plural(pr, "problem"))
	}
	if fx > 0 {
		parts = append(parts, fixes(fx))
	}
	if qs > 0 {
		parts = append(parts, plural(qs, "question"))
	}
	if a.sideFits() {
		a.showSide()
	}
	a.setFlash("^R %s", strings.Join(parts, ", "))
}

// ---- ^P on a diagnostic: fix ----

// fix fixes diagnostic d of the current file, without a prompt: a rule
// that cannot be fixed here flashes what to do by hand; a mechanical fix is
// applied to the buffer at once (^Z undoes it); a model fix runs in the
// background and its edit lands in the sidebar. Both read the buffers, so
// nothing needs saving first.
func (a *App) fix(d diag.Diagnostic) tea.Cmd {
	if a.busy() || a.ws.IsModel(d.File) {
		return nil
	}
	if d.Code == diag.CodeSpell {
		return a.fixSpelling(d)
	}
	class, known := workflow.FixClassOf(d.Code)
	if !known || class == workflow.FixManual {
		a.setFlash("%s cannot be fixed by an edit here: %s", d.Code, workflow.FixManualText(d.Code))
		return nil
	}
	f := a.ws.File(d.File)
	if f == nil {
		return nil
	}
	at := f.Index().Position(d.Range.Start)
	overlay := map[string][]byte{}
	for p, doc := range a.ws.Docs() {
		overlay[p] = doc.Ed.Buffer().Bytes()
	}
	req := workflow.FixRequest{File: d.File, Line: at.Line, Col: at.Col, Code: d.Code, Author: proposal.AuthorHuman, Overlay: overlay}
	if class == workflow.FixMechanical {
		req.NoModel = true
		res, e := a.wf.Fix(context.Background(), a.ws.Project, req)
		switch {
		case e == nil:
			return a.applyMechanical(res)
		case e.Code != "needs_model":
			a.notice("Fix failed", e)
			return nil
		}
		req.NoModel = false
	}
	return a.runFix(req)
}

// fixSpelling replaces a flagged word with the spell check's fix, as one
// undo step.
func (a *App) fixSpelling(d diag.Diagnostic) tea.Cmd {
	f, ok := a.ws.SpellFlag(d)
	if !ok || f.Fix == "" {
		return nil
	}
	doc, err := a.ws.Doc(f.File)
	if err != nil {
		a.setFlash("Cannot open %s: %v", f.File, err)
		return nil
	}
	src := string(doc.Ed.Buffer().Bytes())
	if f.Range.End > len(src) || src[f.Range.Start:f.Range.End] != f.Word {
		a.setFlash("Not fixed: the text changed meanwhile")
		return nil
	}
	after := src[:f.Range.Start] + f.Fix + src[f.Range.End:]
	doc.Ed.ReplaceAll(after, mapOffset(src, after, doc.Ed.Cursor()))
	cmd := a.edited()
	a.setFlash("Spelling: %s is now %s; ^Z undoes", f.Word, f.Fix)
	return cmd
}

// applyMechanical applies a computed fix to its buffer as one undo step
// and flashes what it did.
func (a *App) applyMechanical(res *workflow.FixResult) tea.Cmd {
	if len(res.Proposal.Hunks) == 0 {
		return nil
	}
	h := res.Proposal.Hunks[0]
	d, err := a.ws.Doc(h.File)
	if err != nil {
		a.setFlash("Cannot open %s: %v", h.File, err)
		return nil
	}
	src := string(d.Ed.Buffer().Bytes())
	if strings.Count(src, h.Find) != 1 {
		a.setFlash("Not fixed: the text changed meanwhile; ^P again")
		return nil
	}
	after := strings.Replace(src, h.Find, h.Replace, 1)
	if d != a.cur {
		a.jump(Location{Path: h.File, Off: strings.Index(src, h.Find)})
	}
	d.Ed.ReplaceAll(after, mapOffset(src, after, d.Ed.Cursor()))
	what := strings.TrimSuffix(strings.TrimSpace(h.Explanation), ".")
	if i := strings.Index(what, ", "); i > 0 {
		what = what[:i]
	}
	cmd := a.edited()
	a.setFlash("%s; ^Z undoes", what)
	return cmd
}

// runFix runs a model fix in the background, files it, and shows its edit
// in the sidebar.
func (a *App) runFix(req workflow.FixRequest) tea.Cmd {
	if a.busy() {
		return nil
	}
	wf, proj := a.wf, a.ws.Project
	return a.startJob("Fixing "+req.Code, func(ctx context.Context) func(*App) tea.Cmd {
		res, e := wf.Fix(ctx, proj, req)
		if e == nil && ctx.Err() != nil {
			return nil // cancelled after the model answered: file nothing
		}
		if e == nil {
			if err := wf.Store(proj.Root).Create(res.Proposal); err != nil {
				e = &workflow.Error{Exit: workflow.ExitFail, Code: "write_failed", Message: fmt.Sprintf("cannot write %s: %v", proposal.Dir, err)}
			}
		}
		return func(a *App) tea.Cmd {
			if e != nil {
				title := "Fix failed"
				if e.Code == "no_fix" {
					title = "No fix proposed"
				}
				a.notice(title, e)
				return nil
			}
			a.loadPending()
			a.landed([]*proposal.Proposal{res.Proposal}, "Fix "+req.Code)
			return nil
		}
	})
}

// saveFirst runs then, first asking to save the modified buffers (a check
// reads the files on disk). What it runs reads spec files only, so a modified
// model is not asked about.
func (a *App) saveFirst(what string, then func() tea.Cmd) tea.Cmd {
	var dirty []string
	for _, p := range a.ws.OpenPaths() {
		if d, ok := a.ws.Docs()[p]; ok && d.Ed.Buffer().Dirty() && !a.ws.IsModel(p) {
			dirty = append(dirty, p)
		}
	}
	if len(dirty) == 0 {
		return then()
	}
	a.prompt = &prompt{kind: promptSaveFirst, then: then,
		label: fmt.Sprintf("%s reads the files: save %s first? ", what, strings.Join(dirty, ", "))}
	return nil
}

// saveAll saves every modified buffer saveFirst asked about and reports
// whether all saved.
func (a *App) saveAll() bool {
	for _, p := range a.ws.OpenPaths() {
		d, ok := a.ws.Docs()[p]
		if !ok || !d.Ed.Buffer().Dirty() || a.ws.IsModel(p) {
			continue
		}
		r, err := a.saveDoc(d)
		if err != nil {
			if errors.Is(err, ErrConflict) {
				a.startConflict(p)
				return false
			}
			a.setFlash("Error writing %s: %s", p, errorText(err))
			return false
		}
		if r.warned {
			a.savedStatus(p, r)
		}
	}
	return true
}

// notice shows a failure in a popup over any screen: the message and, on a
// line of its own, how to fix it (what spec doctor says).
func (a *App) notice(title string, err error) {
	msg := termtext.Clean(err.Error())
	var we *workflow.Error
	if errors.As(err, &we) && we.Code == "cancelled" {
		a.setFlash("Cancelled")
		return
	}
	a.setStatus(statusFailure, title, "%s: %s", title, msg)
	lines := []string{a.theme.PopupTitle.Render(title)}
	w := min(max(a.width-10, 20), 90)
	body, fix, hasFix := strings.Cut(msg, "; fix: ")
	lines = append(lines, wrapText(body, w)...)
	if hasFix {
		lines = append(lines, "")
		lines = append(lines, wrapText("fix: "+fix, w)...)
	}
	if errors.As(err, &we) && (we.Code == "provider_unavailable" || we.Code == "provider_failed") {
		lines = append(lines, "", a.theme.PopupDim.Render("`spec doctor` checks the provider; `spec explain config` shows the format."))
	}
	lines = append(lines, "", a.theme.PopupDim.Render("Any key closes this."))
	p := &popup{lines: lines, notice: true}
	p.inner = 0
	for _, l := range lines {
		p.inner = max(p.inner, displayWidth(l))
	}
	p.inner = min(p.inner, max(a.width-4, 10))
	p.boxW = p.inner + 4
	p.boxH = len(lines) + 2
	p.x = max((a.width-p.boxW)/2, 0)
	p.y = max((a.bodyHeight()-p.boxH)/3, 0)
	a.popup = p
	lg.WithError(err).WithField("title", title).Info("notice")
}

// ---- ^N: analyze ----

// analyze starts ^N: the consistency analysis of the edits since HEAD.
func (a *App) analyze() tea.Cmd {
	if a.busy() {
		return nil
	}
	return a.saveFirst("Analyze", a.runAnalyze)
}

// runAnalyze runs `spec analyze` (edits since HEAD) in the background;
// what it finds goes to the sidebar.
func (a *App) runAnalyze() tea.Cmd {
	if a.busy() {
		return nil
	}
	wf, proj := a.wf, a.ws.Project
	return a.startJob("Analyzing the edits since HEAD", func(ctx context.Context) func(*App) tea.Cmd {
		an, e := wf.Analyze(ctx, proj, workflow.AnalyzeRequest{Mode: workflow.ModeChanged, Author: proposal.AuthorHuman})
		if e == nil && !an.Nothing && !an.Saved && hasItems(an.Proposal) && ctx.Err() == nil {
			// Findings without an edit are filed too, so the sidebar
			// keeps them across a restart.
			if err := wf.Store(proj.Root).Create(an.Proposal); err != nil {
				e = &workflow.Error{Exit: workflow.ExitFail, Code: "write_failed", Message: fmt.Sprintf("cannot write %s: %v", proposal.Dir, err)}
			}
		}
		return func(a *App) tea.Cmd {
			if e != nil {
				a.notice("Analyze failed", e)
				return nil
			}
			if an.Nothing {
				a.setFlash("Nothing differs from HEAD: nothing to analyze")
				return nil
			}
			a.loadPending()
			a.ws.Relint()
			a.landed([]*proposal.Proposal{an.Proposal}, "Analysis")
			return a.supersedeAnalyses(an.Proposal)
		}
	})
}

// supersedeAnalyses rejects the earlier pending analyses that analysis
// newer makes stale (workflow.StaleAnalysis), as superseded, so their
// cards leave the sidebar; one Chris has decided an item of, or is
// deciding, stays.
func (a *App) supersedeAnalyses(newer *proposal.Proposal) tea.Cmd {
	if newer == nil || !slices.ContainsFunc(a.pending, func(p *proposal.Proposal) bool { return workflow.StaleAnalysis(newer, p) }) {
		return nil
	}
	return a.underLock("analyses", "supersede stale analyses", func(a *App, lock *project.Lock) tea.Cmd {
		keep := func(id string) bool {
			return workflow.ActedOn(a.side.state.Decided[id]) || a.waitingOn(id) || len(a.bufferedOf(id)) > 0
		}
		ids, e := a.wf.SupersedeAnalysesLocked(lock, a.ws.Root, newer, keep, proposal.AuthorHuman)
		if e != nil {
			lg.WithError(e).Warn("cannot supersede stale analyses")
			return nil
		}
		if len(ids) > 0 {
			a.loadPending()
			what := "the earlier analysis"
			if len(ids) > 1 {
				what = fmt.Sprintf("%d earlier analyses", len(ids))
			}
			a.flash += "; cleared the cards of " + what
		}
		return nil
	})
}
