// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

// The prose finding of the demo spec, and one in other.spec.md.
const (
	proseFind    = "Prose that mentions $Frame.Order for the reader."
	proseReplace = "Prose that mentions $Frame.Order and the 5 s bound for the reader."
	limitFind    = "MUST hold at most 8 items"
	limitReplace = "MUST hold at most 5 items"
)

// checkReply is the model's answer to a contradiction check: two fixes.
func checkReply(t *testing.T) string {
	return modelJSON(t, map[string]any{
		"summary": "the prose and the extra queue should follow the bound",
		"findings": []map[string]any{
			{"file": "demo.spec.md", "issue": "missing_update", "severity": "inconsistent", "explanation": "mention the bound", "find": proseFind, "replace": proseReplace},
			{"file": "other.spec.md", "anchor": "$Extra.Limit", "issue": "numeric_mismatch", "severity": "cosmetic", "explanation": "align the queue size", "find": limitFind, "replace": limitReplace},
		},
		"questions": []string{},
	})
}

// uiOptions are the options of a step 4b flow on dir.
func uiOptions(dir string, wf *workflow.Workflow) Options {
	o := testOptions(dir)
	o.Baseline = true
	o.Workflow = wf
	return o
}

// agentProposal files the change of $Frame.Timing an agent would, with
// the prose fix as hunk 2 (and the queue fix as hunk 3 when both).
func agentProposal(t *testing.T, dir string, both bool) *proposal.Proposal {
	t.Helper()
	proj, err := project.Open(dir)
	require.NoError(t, err)
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	src := string(loaded.Spec.File("demo.spec.md").Source())
	start := strings.Index(src, proseFind)
	require.GreaterOrEqual(t, start, 0)
	p := &proposal.Proposal{
		SourceKind: "agent", Author: "agent-7", Kind: proposal.KindChange, Why: "bound fragment length",
		Summary: "the prose should mention the bound",
		Primary: &proposal.Primary{N: 1, Action: "change", Name: "$Frame.Timing", File: "demo.spec.md",
			OldKind: "inv", NewKind: "inv", OldText: timingOld, NewText: timingNew},
		Hunks: []finding.Hunk{{N: 2, File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent",
			Explanation: "mention the bound", Find: proseFind, Replace: proseReplace}},
		Status: proposal.StatusPending,
	}
	p.Hunks[0].Range.Start, p.Hunks[0].Range.End = start, start+len(proseFind)
	if both {
		other := string(loaded.Spec.File("other.spec.md").Source())
		s := strings.Index(other, limitFind)
		h := finding.Hunk{N: 3, File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch",
			Severity: "cosmetic", Explanation: "align the queue size", Find: limitFind, Replace: limitReplace}
		h.Range.Start, h.Range.End = s, s+len(limitFind)
		p.Hunks = append(p.Hunks, h)
	}
	workflow.FillBase(p, loaded.Spec)
	require.NoError(t, proposal.NewStore(dir).Create(p))
	return p
}

// reviewOf returns the review screen on top, or nil.
func reviewOf(a *App) *reviewScreen {
	r, _ := a.top().(*reviewScreen)
	return r
}

// (b) An agent's proposal filed while the UI runs shows in the badge and
// the sidebar, is reviewed hunk by hunk and applied into the buffers
// (step 4e: nothing is written to the files or committed).
func TestAgentProposalArrives(t *testing.T) {
	dir := demoRepo(t)
	opts := uiOptions(dir, testWorkflow(t, nil))
	opts.WatchProposals = true
	f := startFlow(t, opts)
	assert.NotContains(t, f.frame(), "proposal]")

	// Filed by another process: a card arrives, the flash names it and the
	// key, the help bar counts it, and the editor keeps the keyboard.
	p := agentProposal(t, dir, true)
	f.until("the badge", func(a *App) bool { return a.badge() == "[1 proposal, 2 checks]" })
	f.do(func(a *App) {
		assert.Contains(t, a.flash, "Agent proposal from agent-7 for $Frame.Timing")
		assert.False(t, a.sideShown)
	})

	f.do(func(a *App) {
		assert.Equal(t, CtxEditor, a.context(), "no focus change")
		require.Equal(t, 3, itemCards(a), "one agent parent and two related problems")
		assert.Equal(t, cardProposal, a.side.cards[0].kind)
	})

	f.send(key("ctrl+r"))
	frame := f.frame()
	assert.Contains(t, frame, "Agent proposal")
	assert.Contains(t, frame, "agent-7")
	assert.Contains(t, frame, "$Frame.Timing")
	f.send(key("enter"))
	f.do(func(a *App) { require.NotNil(t, reviewOf(a)) })
	frame = f.frame()
	assert.Contains(t, frame, "by agent-7")
	assert.Contains(t, frame, "- **$Frame.Timing (fragment timing).** Every fragment MUST have a duration above zero in")

	f.send(key("y"), key("y"), key("A"))
	f.until("the approval", func(a *App) bool { return len(a.screens) == 1 })
	f.do(func(a *App) {
		assert.Empty(t, a.pending)
		assert.Empty(t, a.side.cards, "the sidebar no longer shows it")
	})
	want := strings.Replace(strings.Replace(demoSpec, timingOld, timingNew, 1), proseFind, proseReplace, 1)
	wantOther := strings.Replace(otherSpec, limitFind, limitReplace, 1)
	assert.Equal(t, demoSpec, readFile(t, dir, "demo.spec.md"), "nothing written")
	assert.Equal(t, otherSpec, readFile(t, dir, "other.spec.md"), "nothing written")
	rec, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, rec.Status, "not recorded until the save")
	f.do(func(a *App) {
		assert.Equal(t, want, string(a.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()), "applied into the buffer")
		assert.Equal(t, wantOther, string(a.ws.Docs()["other.spec.md"].Ed.Buffer().Bytes()))
		assert.True(t, a.ws.Docs()["demo.spec.md"].Ed.Buffer().Dirty())
		assert.Contains(t, a.flash, "Applied · unsaved · hunks 1,2,3 of proposal "+p.ID+" into the buffer (^O saves; nothing is committed)")
		saveBuffers(t, a)
	})
	rec, err = proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, rec.Status)
	assert.Equal(t, []int{1, 2, 3}, rec.Decision.Applied)
	assert.Equal(t, proposal.AuthorHuman, rec.Decision.By)
	f.finish()
}

// (c) A rejection with a reason reaches an agent waiting on the proposal.
func TestRejectReachesWaiter(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	got := make(chan *proposal.Proposal, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() {
		ps, err := proposal.NewStore(dir).Wait(ctx, []string{p.ID}, 20*time.Millisecond)
		if err == nil && len(ps) == 1 {
			got <- ps[0]
		}
		close(got)
	}()

	f := startFlow(t, uiOptions(dir, testWorkflow(t, nil)))
	f.send(key("ctrl+r"), key("enter"), key("R"))
	f.do(func(a *App) {
		require.NotNil(t, a.prompt)
		assert.Equal(t, promptReject, a.prompt.kind)
	})
	assert.Contains(t, f.frame(), "Reason to reject "+p.ID)
	f.send(key("enter"))
	f.do(func(a *App) {
		require.NotNil(t, a.prompt, "an empty reason is not accepted")
		assert.Contains(t, a.flash, "A rejection needs a reason")
	})
	f.typeIn("5 s is too short for keyframe gaps")
	f.send(key("enter"))

	select {
	case w := <-got:
		require.NotNil(t, w, "the waiter saw the decision")
		assert.Equal(t, proposal.StatusRejected, w.Status)
		assert.Equal(t, "5 s is too short for keyframe gaps", w.Decision.Reason)
		assert.Equal(t, proposal.AuthorHuman, w.Decision.By)
	case <-time.After(15 * time.Second):
		t.Fatal("the waiter did not see the rejection")
	}
	assert.Equal(t, demoSpec, readFile(t, dir, "demo.spec.md"), "nothing written")
	f.do(func(a *App) {
		assert.Contains(t, a.flash, "Rejected "+p.ID)
		assert.Empty(t, a.pending)
		assert.Equal(t, 1, len(a.screens), "the review is closed")
		assert.Empty(t, a.side.cards)
	})
	f.finish()
}

// (d) Without a provider, ^P shows doctor's fix line in a popup and the UI
// carries on.
func TestProposeWithoutProvider(t *testing.T) {
	dir := demoRepo(t)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "above zero", "of at most 5 s", 1))
	f := startFlow(t, uiOptions(dir, testWorkflow(t, nil)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	f.findIn("at most 5 s")
	f.send(key("ctrl+p"))
	f.do(func(a *App) { assert.Nil(t, a.prompt, "no prompt: the check runs at once") })
	f.until("the notice", func(a *App) bool { return a.popup != nil && a.popup.notice })
	frame := f.frame()
	assert.Contains(t, frame, "Check failed")
	assert.Contains(t, frame, "no user config at")
	assert.Contains(t, frame, "fix: create it with a [providers.NAME] table (`spec explain config` has")
	assert.Contains(t, frame, "`spec doctor` checks the provider")
	f.do(func(a *App) { assert.Equal(t, CtxNotice, a.context()) })
	assert.Contains(t, f.frame(), "Any key closes this.", "the notice explains how to close")
	assert.NotContains(t, f.frame(), "^T Follow", "not the hover popup's bar")
	f.send(key("x"))
	f.do(func(a *App) {
		assert.Nil(t, a.popup, "any key closes it")
		assert.Nil(t, a.job)
		assert.Equal(t, CtxEditor, a.context())
		assert.Empty(t, a.pending, "nothing filed")
	})
	assert.Equal(t, strings.Replace(demoSpec, "above zero", "of at most 5 s", 1), readFile(t, dir, "demo.spec.md"))
	f.finish()
}

// (e) ^N puts the findings in the sidebar; Enter jumps; y applies the
// suggested edit into the buffer.
func TestAnalyzeFindings(t *testing.T) {
	dir := demoRepo(t)
	writeFile(t, dir, "other.spec.md", strings.Replace(otherSpec, "Checked by: TestLimit.", "Drained every 30 s. Checked by: TestLimit.", 1))
	reply := modelJSON(t, map[string]any{
		"summary": "one size disagrees",
		"findings": []map[string]any{
			{"file": "other.spec.md", "anchor": "$Extra.Limit", "issue": "numeric_mismatch", "severity": "inconsistent", "explanation": "8 items here, 5 in the design", "find": limitFind, "replace": limitReplace},
			{"file": "demo.spec.md", "issue": "ambiguity", "severity": "cosmetic", "explanation": "the drain interval is not defined elsewhere"},
		},
		"questions": []string{"Is 30 s the drain interval everywhere?"},
	})
	m := newFakeModel(t, reply)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.send(key("ctrl+n"))
	f.until("the findings", func(a *App) bool { return a.job == nil && itemCards(a) == 3 })
	f.do(func(a *App) {
		assert.Equal(t, "^R 2 problems, 1 fix, 1 question", a.flash, "short, and it leads with the key")
		assert.False(t, a.sideShown, "100 columns: no room for a column, so it waits for ^R")
	})
	assert.Contains(t, m.requests()[0], "Drained every 30 s", "the edit since HEAD was sent")
	f.do(func(a *App) { a.showSide() })
	f.send(key("shift+tab"))
	frame := f.frame()
	assert.Contains(t, frame, "Review")
	f.do(func(a *App) { assert.Equal(t, "^R 2 problems, 1 fix, 1 question", a.flash) })
	assert.Contains(t, frame, "numeric_mismatch")
	assert.Contains(t, frame, "8 items here, 5 in the design")
	f.do(func(a *App) { assert.Equal(t, CtxChecks, a.context()) })

	// Enter jumps to the quoted text and gives the editor focus.
	f.send(key("enter"))
	f.do(func(a *App) {
		assert.Equal(t, "other.spec.md", a.cur.Ed.Path())
		src := string(a.cur.Ed.Buffer().Bytes())
		assert.Equal(t, strings.Index(src, limitFind), a.cur.Ed.Cursor())
		assert.Equal(t, CtxEditor, a.context(), "the editor has focus, the sidebar stays")
		assert.True(t, a.sideShown)
	})
	// Shift+Tab focuses the visible sidebar without starting another check.
	f.send(key("shift+tab"), key("y"))
	want := strings.Replace(strings.Replace(otherSpec, "Checked by: TestLimit.", "Drained every 30 s. Checked by: TestLimit.", 1), limitFind, limitReplace, 1)
	f.do(func(a *App) {
		assert.Equal(t, want, string(a.ws.Docs()["other.spec.md"].Ed.Buffer().Bytes()))
		assert.Equal(t, 2, itemCards(a), "the note and the question stay")
	})
	assert.NotEqual(t, want, readFile(t, dir, "other.spec.md"), "the file is not written")
	f.finish()
}

// (f) f sends feedback; the model's revision replaces the hunk.
func TestFeedbackRevisesHunk(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	revised := "Prose that mentions $Frame.Order and the 5 second bound for the reader."
	m := newFakeModel(t, modelJSON(t, map[string]string{"find": proseFind, "replace": revised, "explanation": "spell out seconds"}))
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.send(key("ctrl+r"), key("enter"))
	f.do(func(a *App) { require.Equal(t, 2, reviewOf(a).selected()) })
	f.send(key("f"))
	f.typeIn("spell out seconds")
	f.send(key("ctrl+o")) // the feedback box: ^O sends it
	f.until("the revision", func(a *App) bool {
		r := reviewOf(a)
		return r != nil && len(r.p.Hunks) == 1 && r.p.Hunks[0].Replace == revised && a.job == nil
	})
	reqs := m.requests()
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0], "spell out seconds")
	assert.Contains(t, f.frame(), "+ Prose that mentions $Frame.Order and the 5 second bound for the reader.")
	stored, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, revised, stored.Hunks[0].Replace, "the revision is saved in the proposal")
	assert.Equal(t, "spell out seconds", stored.Hunks[0].Explanation)

	f.send(key("y"), key("A"))
	f.until("the approval", func(a *App) bool { return len(a.screens) == 1 })
	f.do(func(a *App) {
		assert.Equal(t, strings.Replace(strings.Replace(demoSpec, timingOld, timingNew, 1), proseFind, revised, 1),
			string(a.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()))
	})
	f.finish()
}

// (g) Approving applies into a modified buffer, on top of Chris's own
// unsaved edits (step 4e: the file is not touched).
func TestApproveIntoDirtyBuffer(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, nil)))
	f.do(func(a *App) { require.Equal(t, "demo.spec.md", a.cur.Ed.Path()) })
	f.typeIn("Mine. ")
	f.send(key("ctrl+r"), key("enter"), key("y"), key("A"))
	f.until("the approval", func(a *App) bool { return len(a.screens) == 1 })
	f.do(func(a *App) {
		d := a.ws.Docs()["demo.spec.md"]
		assert.Equal(t, "Mine. "+strings.Replace(strings.Replace(demoSpec, timingOld, timingNew, 1), proseFind, proseReplace, 1), string(d.Ed.Buffer().Bytes()))
		// One ^Z takes the approval back; the typing stays.
		require.True(t, d.Ed.Undo())
		assert.Equal(t, "Mine. "+demoSpec, string(d.Ed.Buffer().Bytes()))
	})
	assert.Equal(t, demoSpec, readFile(t, dir, "demo.spec.md"))
	rec, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, rec.Status, "the approval was taken back before any save")
	f.finish()
}

// (h) A hunk whose text changed since the proposal is shown as stale and
// cannot be accepted.
func TestStaleHunk(t *testing.T) {
	dir := demoRepo(t)
	agentProposal(t, dir, true)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "for the reader.", "for readers.", 1))
	f := startFlow(t, uiOptions(dir, testWorkflow(t, nil)))
	f.send(key("ctrl+r"), key("enter"))
	f.do(func(a *App) {
		r := reviewOf(a)
		require.NotNil(t, r)
		assert.Contains(t, r.stale[2], "occurs 0 times")
		assert.NotContains(t, r.stale, 3)
	})
	frame := f.frame()
	assert.Contains(t, frame, "STALE")
	assert.Contains(t, frame, "stale: the text to replace occurs 0 times in demo.spec.md now")
	var beforeReceipt string
	f.do(func(a *App) { reviewOf(a).sel = 1; beforeReceipt = a.flash })
	f.send(key("y"))
	f.do(func(a *App) {
		r := reviewOf(a)
		assert.Equal(t, hunkUndecided, r.state[2], "a stale hunk cannot be accepted")
		assert.Equal(t, beforeReceipt, a.flash)
		assert.Equal(t, 1, r.sel)
	})
	// The rest still applies.
	f.send(key("n"), key("y"), key("A"))
	f.until("the approval", func(a *App) bool { return len(a.screens) == 1 })
	f.do(func(a *App) {
		assert.Equal(t, strings.Replace(strings.Replace(demoSpec, "for the reader.", "for readers.", 1), timingOld, timingNew, 1),
			string(a.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()))
		assert.Equal(t, strings.Replace(otherSpec, limitFind, limitReplace, 1), string(a.ws.Docs()["other.spec.md"].Ed.Buffer().Bytes()))
	})
	f.finish()
}

// (i) Completion of $Name and #N (inserted as §N), never in code.
func TestCompletion(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	find(a, "of scope.")
	send(a, key("ctrl+e"))
	send(a, typeText(" $Frame.Ti")...)
	require.NotNil(t, a.comp)
	assert.Equal(t, CtxCompletion, a.context())
	require.NotEmpty(t, a.comp.items)
	assert.Equal(t, "Frame.Timing", a.comp.items[0].key)
	assert.Contains(t, plain(a), "$Frame.Timing  inv  fragment timing")
	send(a, key("tab"))
	assert.Nil(t, a.comp)
	src := string(a.cur.Ed.Buffer().Bytes())
	assert.Contains(t, src, "of scope. $Frame.Timing\n")

	// # lists sections and inserts §.
	send(a, typeText(" #0.")...)
	require.NotNil(t, a.comp)
	assert.Equal(t, "#", a.comp.sigil)
	assert.Equal(t, "0.1 Time", a.comp.items[0].key)
	send(a, key("down"), key("enter"))
	src = string(a.cur.Ed.Buffer().Bytes())
	assert.Contains(t, src, "of scope. $Frame.Timing §0.2\n")
	// One undo restores what was typed.
	send(a, key("ctrl+z"))
	assert.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "of scope. $Frame.Timing #0.\n")

	// Esc closes; typing on filters; no match closes.
	send(a, typeText(" $Zz")...)
	assert.Nil(t, a.comp, "nothing matches $Zz")
	send(a, typeText(" $")...)
	require.NotNil(t, a.comp)
	send(a, key("esc"))
	assert.Nil(t, a.comp)

	// Not after a word character, and never in code.
	send(a, typeText(" a$")...)
	assert.Nil(t, a.comp, "a$ is not a reference")
	a = newTestApp(t, testOptions(lensdProject(t)))
	find(a, "**Def $Name")
	send(a, typeText("$")...)
	assert.Nil(t, a.comp, "inside a code span")
	find(a, "// Spec:")
	send(a, typeText(" $")...)
	assert.Nil(t, a.comp, "inside a code block")
	find(a, "// Spec:")
	send(a, typeText("#")...)
	assert.Nil(t, a.comp, "# inside a code block")
}

// ^C stops a running check: nothing is filed, and the UI carries on.
func TestProposeCancel(t *testing.T) {
	dir := demoRepo(t)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "above zero", "of at most 5 s", 1))
	m := newFakeModel(t, checkReply(t))
	m.gate = make(chan struct{}) // never answers
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	f.send(key("ctrl+p"))
	f.do(func(a *App) {
		assert.Nil(t, a.prompt, "no prompt")
		require.NotNil(t, a.job)
		assert.Equal(t, "Checking every changed anchor", a.job.label, "the cursor is in no anchor")
	})
	f.until("the request", func(a *App) bool { return a.job != nil && len(m.requests()) == 1 })
	var beforeReceipt string
	var running *job
	f.do(func(a *App) { beforeReceipt, running = a.flash, a.job })
	f.send(key("ctrl+p"))
	f.do(func(a *App) {
		assert.Equal(t, beforeReceipt, a.flash)
		assert.Same(t, running, a.job)
		assert.Len(t, m.requests(), 1)
	})
	f.send(key("ctrl+c"))
	f.do(func(a *App) {
		assert.Nil(t, a.job)
		assert.Contains(t, a.flash, "Cancelled: checking every changed anchor")
	})
	time.Sleep(50 * time.Millisecond)
	f.do(func(a *App) {
		assert.Empty(t, a.side.cards, "a cancelled check shows nothing")
		assert.Empty(t, a.pending)
	})
	ps, err := proposal.NewStore(dir).List()
	require.NoError(t, err)
	assert.Empty(t, ps)
	f.finish()
}

// e edits a hunk's replacement in the small editor; ^O keeps it.
func TestEditHunk(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, nil)))
	f.send(key("ctrl+r"), key("enter"), key("e"))
	f.do(func(a *App) { require.Equal(t, CtxHunkEdit, a.context()) })
	frame := f.frame()
	assert.Contains(t, frame, "Edit 2 in demo.spec.md: the text that replaces")
	assert.Contains(t, frame, "- "+proseFind)
	// The cursor starts at the end: replace "reader." with "readers.".
	f.send(key("backspace"))
	f.typeIn("s.")
	f.send(key("ctrl+o"))
	edited := strings.Replace(proseReplace, "reader.", "readers.", 1)
	f.do(func(a *App) {
		r := reviewOf(a)
		require.NotNil(t, r)
		assert.Equal(t, hunkAccepted, r.state[2])
		assert.Equal(t, edited, r.p.Hunks[0].Replace)
		assert.Contains(t, a.flash, "Hunk 2 edited and accepted")
	})
	stored, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, edited, stored.Hunks[0].Replace)

	// ^X in the editor discards.
	f.send(key("e"), key("x"), key("ctrl+x"))
	f.do(func(a *App) {
		assert.Equal(t, edited, reviewOf(a).p.Hunks[0].Replace)
		assert.Contains(t, a.flash, "discarded")
	})
	f.send(key("A"))
	f.until("the approval", func(a *App) bool { return len(a.screens) == 1 })
	f.do(func(a *App) {
		assert.Equal(t, strings.Replace(strings.Replace(demoSpec, timingOld, timingNew, 1), proseFind, edited, 1),
			string(a.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()))
	})
	f.finish()
}

// ^P never asks before it runs (step 4e): an anchor with a pending
// proposal is checked again at once; Chris's earlier check of the same
// anchor is superseded, an agent's proposal is left alone.
func TestProposeSupersedesEarlierCheck(t *testing.T) {
	dir := demoRepo(t)
	agent := agentProposal(t, dir, false)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "above zero", "of at most 5 s", 1))
	m := newFakeModel(t, checkReply(t))
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	f.findIn("of at most 5 s")
	f.send(key("ctrl+p"))
	f.do(func(a *App) { assert.Nil(t, a.prompt, "no question first") })
	f.until("the first check", func(a *App) bool { return a.job == nil && len(a.pending) == 2 })
	var first string
	f.do(func(a *App) {
		for _, p := range a.pending {
			if p.ID != agent.ID {
				first = p.ID
			}
		}
	})
	f.send(key("ctrl+p"))
	f.until("the second check", func(a *App) bool {
		return a.job == nil && len(a.pending) == 2 && !slices.ContainsFunc(a.pending, func(p *proposal.Proposal) bool { return p.ID == first })
	})
	old, err := proposal.NewStore(dir).Load(first)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusRejected, old.Status)
	assert.Contains(t, old.Decision.Reason, "superseded by check ")
	ag, err := proposal.NewStore(dir).Load(agent.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, ag.Status, "the agent's proposal stays")
	f.finish()
}

// Sidebar Enter jumps to a named place, or stays inert when no location
// exists. An analysis that found nothing says so.
func TestSidebarKeysShowFeedback(t *testing.T) {
	const added = "Drained every 30 s."
	cases := []struct {
		name      string
		findings  []map[string]any
		questions []string
		// enter is the flash after Enter ("" when Enter jumps to jumpTo).
		enter  string
		jumpTo string
	}{
		{
			name:     "note quoting the file",
			findings: []map[string]any{{"file": "demo.spec.md", "issue": "ambiguity", "severity": "cosmetic", "explanation": "the drain is vague", "find": added}},
			jumpTo:   "demo.spec.md  17:1",
		},
		{
			name:     "note naming the file by another path",
			findings: []map[string]any{{"file": "./docs/demo.spec.md", "issue": "ambiguity", "severity": "cosmetic", "explanation": "the drain is vague"}},
			jumpTo:   "demo.spec.md  1:1",
		},
		{
			name:     "note with no place",
			findings: []map[string]any{{"issue": "ambiguity", "severity": "cosmetic", "explanation": "vague overall"}},
			enter:    "This card names no place in the spec",
		},
		{
			name: "question only", questions: []string{"Is 30 s the drain interval everywhere?"},
			enter: "This card names no place in the spec",
		},
		{
			name:     "one fix",
			findings: []map[string]any{{"file": "other.spec.md", "anchor": "$Extra.Limit", "issue": "numeric_mismatch", "severity": "inconsistent", "explanation": "8 items here, 5 in the design", "find": limitFind, "replace": limitReplace}},
			jumpTo:   "other.spec.md  5:", // the quoted text, mid-line
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := demoRepo(t)
			writeFile(t, dir, "demo.spec.md", demoSpec+"\n"+added+"\n")
			qs := c.questions
			if qs == nil {
				qs = []string{}
			}
			fs := c.findings
			if fs == nil {
				fs = []map[string]any{}
			}
			m := newFakeModel(t, modelJSON(t, map[string]any{"summary": "trivial", "findings": fs, "questions": qs}))
			f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
			f.send(key("ctrl+n"))
			f.until("the findings", func(a *App) bool { return a.job == nil && itemCards(a) > 0 })
			f.send(key("ctrl+r"))
			var receipt, path string
			var cursor int
			f.do(func(a *App) {
				require.Equal(t, CtxChecks, a.context())
				assert.Equal(t, c.enter == "", barEnabled(a, CtxChecks, ActAccept), "Enter is disabled without a place to go")
				receipt, path, cursor = a.flash, a.cur.Ed.Path(), a.cur.Ed.Cursor()
			})
			f.send(key("enter"))
			f.do(func(a *App) {
				if c.enter != "" {
					assert.Equal(t, receipt, a.flash)
					assert.Equal(t, CtxChecks, a.context())
					assert.Equal(t, path, a.cur.Ed.Path())
					assert.Equal(t, cursor, a.cur.Ed.Cursor())
				} else {
					assert.Equal(t, CtxEditor, a.context())
					prefix := string(a.cur.Ed.Buffer().Bytes()[:a.cur.Ed.Cursor()])
					line := strings.Count(prefix, "\n") + 1
					column := len(prefix) - strings.LastIndex(prefix, "\n")
					assert.True(t, strings.HasPrefix(fmt.Sprintf("%s  %d:%d", a.cur.Ed.Path(), line, column), c.jumpTo), "jumped to %s:%d:%d, want %s", a.cur.Ed.Path(), line, column, c.jumpTo)
				}
			})
			f.finish()
		})
	}
	t.Run("no findings", func(t *testing.T) {
		dir := demoRepo(t)
		writeFile(t, dir, "demo.spec.md", demoSpec+"\n"+added+"\n")
		m := newFakeModel(t, modelJSON(t, map[string]any{"summary": "trivial", "findings": []any{}, "questions": []string{}}))
		f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
		f.send(key("ctrl+n"))
		f.until("the analysis", func(a *App) bool { return a.job == nil && a.flash != "" })
		f.do(func(a *App) {
			assert.Equal(t, "Analysis: nothing found", a.flash)
			assert.Empty(t, a.side.cards)
		})
		f.send(key("ctrl+r"))
		assert.Contains(t, f.frame(), "Nothing to review.")
		f.finish()
	})
}

// The flash after Apply on a review names the proposal as one: proposal ids
// are drawn from a-z and 2-7, so "hunk 1 of 56756" read as one hunk of
// 56756 in a demo recording.
func TestAppliedHunksTextNamesProposal(t *testing.T) {
	assert.Equal(t, "Applied hunk 1 of proposal 56756 into the buffer", appliedHunksText("Applied", []int{1}, "56756"))
	assert.Equal(t, "Applied and formatted hunks 1,3 of proposal kq3ab into the buffer", appliedHunksText("Applied and formatted", []int{1, 3}, "kq3ab"))
}
