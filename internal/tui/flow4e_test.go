// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
)

// Step 4e (notes/plan-step4e-review-flow.md): the Checks sidebar, editing
// never commits, the commit view, ^P's routing and the change highlights.

// hookRepo is demoRepo with a commit-msg hook that records each run.
func hookRepo(t *testing.T) string {
	t.Helper()
	dir := demoRepo(t)
	hook := filepath.Join(dir, ".git", "hooks", "commit-msg")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o755))
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho ran >> \"$(git rev-parse --git-dir)/hook-ran\"\n"), 0o755))
	return dir
}

// The whole loop: ^P checks the edited anchor with no prompt; the
// suggested edits land in the sidebar; y applies one into the buffer and
// n dismisses the other, and neither writes the file or commits; the
// commit view then commits only the ticked changes, through the hook, and
// the unticked anchor stays modified in the working tree.
func TestCheckSidebarCommit(t *testing.T) {
	dir := hookRepo(t)
	nothing := modelJSON(t, map[string]any{"summary": "consistent", "findings": []any{}, "questions": []string{}})
	draft := modelJSON(t, map[string]string{"subject": "spec: bound $Frame.Timing to 5 s", "body": "- mention the bound in the prose"})
	m := newFakeModel(t, checkReply(t), nothing, draft)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })

	f.findIn("above zero")
	for range len("above zero") {
		f.send(key("shift+right"))
	}
	f.typeIn("of at most 5 s")
	assert.Contains(t, f.frame(), "Unsaved")
	assert.Contains(t, f.frame(), "$Frame.Timing")

	// ^P: save first (a check reads the files), then the check runs: no
	// message is asked.
	f.send(key("ctrl+p"))
	f.do(func(a *App) { require.Equal(t, CtxSaveFirst, a.context()) })
	assert.Contains(t, f.frame(), "Check reads the files: save demo.spec.md first?")
	f.send(key("y"))
	f.do(func(a *App) { assert.Nil(t, a.prompt, "no message prompt") })
	timed := strings.Replace(demoSpec, "above zero", "of at most 5 s", 1)
	assert.Equal(t, timed, readFile(t, dir, "demo.spec.md"), "saved first")
	f.until("the cards", func(a *App) bool { return a.job == nil && itemCards(a) == 2 })
	f.do(func(a *App) {
		assert.Equal(t, "^R 2 problems, 2 fixes", a.flash)
		assert.Equal(t, "[2 checks]", a.badge())
	})
	assert.NotContains(t, m.requests()[0], `"why"`, "the check ran without a why")

	// The sidebar: y applies the prose edit into the buffer; n dismisses
	// the queue edit.
	f.send(key("ctrl+r"))
	frame := f.frame()
	assert.Contains(t, frame, "Your edit")
	assert.Contains(t, frame, "$Frame.Timing")
	assert.Contains(t, frame, "missing_update")
	f.send(key("y"))
	withProse := strings.Replace(timed, proseFind, proseReplace, 1)
	f.do(func(a *App) {
		assert.Equal(t, withProse, string(a.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()))
		assert.True(t, a.ws.Docs()["demo.spec.md"].Ed.Buffer().Dirty())
	})
	f.send(key("n"))
	f.do(func(a *App) { assert.Empty(t, a.side.cards) })
	assert.Equal(t, timed, readFile(t, dir, "demo.spec.md"), "accepting never writes the file")
	assert.Equal(t, otherSpec, readFile(t, dir, "other.spec.md"))
	assert.Equal(t, "base\n", gitRun(t, dir, "log", "-1", "--format=%s"), "accepting never commits")
	ps := proposalsIn(t, dir)
	require.Len(t, ps, 1)
	assert.Equal(t, proposal.StatusPending, ps[0].Status, "not recorded until the save")
	f.do(func(a *App) { saveBuffers(t, a) })
	ps = proposalsIn(t, dir)
	require.Len(t, ps, 1)
	assert.Equal(t, proposal.StatusApplied, ps[0].Status, "every item decided: settled")
	assert.Equal(t, []int{1, 2}, ps[0].Decision.Applied)

	// A second anchor edited in the same file. ^R is the commit flow: it
	// checks the changed anchor no check has seen ($Frame.Order), saving
	// first; A then opens the commit view, whose message the model drafts.
	f.send(key("esc"))
	f.findIn("in order of")
	f.typeIn("strictly ")
	f.do(func(a *App) {
		assert.Equal(t, "Checks", a.labelOf(CtxEditor, mustLookup(t, CtxEditor, "ctrl+r")), "^R Commit on the help bar")
	})
	f.send(key("ctrl+r"))
	f.do(func(a *App) { require.Equal(t, CtxSaveFirst, a.context()) })
	f.send(key("y"))
	f.until("the check of $Frame.Order", func(a *App) bool { return a.job == nil && len(m.requests()) == 2 })
	assert.Contains(t, m.requests()[1], "Frame.Order")
	f.do(func(a *App) { assert.Equal(t, CtxChecks, a.context(), "the sidebar has focus") })
	f.send(key("A"))
	var cs *commitScreen
	f.do(func(a *App) {
		var ok bool
		cs, ok = a.top().(*commitScreen)
		require.True(t, ok, "the commit view")
		var ids []string
		for _, c := range cs.changes {
			ids = append(ids, c.ID)
		}
		assert.Equal(t, []string{"$Frame.Timing", "$Frame.Order", "prose:demo.spec.md"}, ids)
		assert.Equal(t, "checked", cs.state["$Frame.Timing"])
		assert.Equal(t, "checked", cs.state["$Frame.Order"])
	})
	f.until("the draft", func(a *App) bool { return !cs.drafting })
	frame = f.frame()
	assert.Contains(t, frame, "[x] inv $Frame.Timing changed")
	assert.Contains(t, frame, "spec: bound $Frame.Timing to 5 s")
	assert.Contains(t, frame, "drafted by the model")
	reqs := m.requests()
	require.Len(t, reqs, 3)
	assert.Contains(t, reqs[2], "Write the commit message")
	assert.Contains(t, reqs[2], "of at most 5 s", "the anchor's new text")
	assert.Contains(t, reqs[2], "mention the bound", "the accepted fix")

	// Untick $Frame.Order: the trailers follow, and the model drafts again.
	f.send(key("down"), key("n"))
	f.until("the second draft", func(a *App) bool { return !cs.drafting && len(m.requests()) == 4 })
	assert.NotContains(t, m.requests()[3], "inv $Frame.Order", "only the ticked changes")
	frame = f.frame()
	assert.Contains(t, frame, "[ ] inv $Frame.Order changed")
	assert.Contains(t, frame, "Spec-Change: $Frame.Timing")
	assert.NotContains(t, frame, "Spec-Change: $Frame.Order")
	f.send(key("A"))
	f.until("the commit", func(a *App) bool { return a.job == nil && len(a.screens) == 1 })

	saved := strings.Replace(withProse, "arrive in order", "arrive strictly in order", 1)
	assert.Equal(t, saved, readFile(t, dir, "demo.spec.md"), "the working tree keeps every edit")
	assert.Equal(t, withProse, gitRun(t, dir, "show", "HEAD:demo.spec.md"), "the commit holds only the ticked changes")
	assert.Equal(t, " M demo.spec.md\n", gitRun(t, dir, "status", "--porcelain"), "the rest is modified and unstaged")
	msg := gitRun(t, dir, "log", "-1", "--format=%B")
	assert.Contains(t, msg, "spec: bound $Frame.Timing to 5 s\n\n- mention the bound in the prose\n")
	assert.Regexp(t, `(?m)^Spec-Change: \$Frame\.Timing inv:[0-9a-f]{8} -> inv:[0-9a-f]{8}$`, msg)
	assert.NotContains(t, msg, "$Frame.Order")
	assert.Equal(t, "ran\n", readFile(t, filepath.Join(dir, ".git"), "hook-ran"), "the commit-msg hook ran")
	f.do(func(a *App) {
		assert.Contains(t, a.flash, "Committed ")
		assert.Contains(t, a.flash, "2 changes with Spec-Change $Frame.Timing; 1 change left uncommitted")
	})
	f.until("the new baseline", func(a *App) bool {
		an := a.ws.File("demo.spec.md").AnchorAt(strings.Index(saved, "**$Frame.Timing"))
		return an != nil && a.changeState(an) == ""
	})
	f.finish()
}

// Chris's report (manual test of 4d): ^P in a section he had not edited
// went to the commit message prompt for $Frame.SegmentShape. The playground's
// $Frame.SegmentShape differed from HEAD by one thing only: the `5$` he
// typed to raise a math-hazard warning. In 4c/4d a changed anchor won over
// the diagnostic under the cursor, so ^P on that W checked the anchor (the
// edit was the warning itself). Since 4e the diagnostic wins: ^P fixes it.
func TestCtrlPOnTypedWarning(t *testing.T) {
	gitEnv(t)
	dir := lensdProject(t)
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.ws.Base = a.ws.LoadBaseline(context.Background())

	// What Chris typed: "5$" in an anchor body, after a $Name on the line.
	find(a, "($Hub.MemoryBound) MUST")
	send(a, key("ctrl+right"), key("ctrl+right"), key("ctrl+right"))
	send(a, typeText("5$ ")...)
	src := string(a.cur.Ed.Buffer().Bytes())
	require.Contains(t, src, "($Hub.MemoryBound) 5$ MUST")
	at := strings.Index(src, "5$ MUST") + 1
	a.cur.Ed.SetCursor(at)
	an := a.ws.File(a.cur.Ed.Path()).AnchorAt(at)
	require.NotNil(t, an)
	require.Equal(t, "Frame.SegmentShape", an.Name)
	require.Equal(t, "changed", a.changeState(an), "the typed warning is the anchor's only change")
	d, ok := a.diagUnderCursor()
	require.True(t, ok)
	require.Equal(t, "math-hazard", d.Code)

	send(a, key("ctrl+p"))
	assert.Nil(t, a.prompt, "4d asked for $Frame.SegmentShape's commit message here")
	assert.Nil(t, a.job)
	assert.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "($Hub.MemoryBound) `5$` MUST", "the warning is fixed")
	assert.Equal(t, "Put 5$ in backticks; ^Z undoes", a.flash)

	// In an anchor that is unchanged, with no diagnostic: nothing to do,
	// even though another anchor is changed (4d checked every changed
	// anchor from anywhere outside a changed one).
	find(a, "Running rendition ffmpegs")
	beforeReceipt := a.flash
	send(a, key("ctrl+p"))
	assert.Nil(t, a.job)
	assert.Equal(t, beforeReceipt, a.flash)
}

// A card whose text changed since is stale: y refuses it and the buffer
// stays as it is; the card says so.
func TestStaleCardCannotBeAccepted(t *testing.T) {
	dir := demoProject(t)
	fixedProposals(t, dir)
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	require.NoError(t, a.open(Location{Path: "other.spec.md"}))
	find(a, "8 items")
	send(a, key("delete"), typeText("9")[0])
	before := string(a.cur.Ed.Buffer().Bytes())
	send(a, key("ctrl+r"), key("down"), key("down"))
	c := a.side.selected()
	require.Equal(t, cardEdit, c.kind)
	require.Equal(t, "m7qd2", c.p.ID)
	assert.Contains(t, a.cardStale(c), "occurs 0 times")
	assert.Equal(t, fixGone, a.fixState(c.p, c.hunk), "edited by hand otherwise: no longer applies")
	assert.False(t, barEnabled(a, CtxChecks, ActAcceptHunk), "y is dimmed")
	assert.Contains(t, plain(a), "No longer applies")
	beforeReceipt := a.flash
	send(a, key("y"))
	assert.Equal(t, beforeReceipt, a.flash)
	send(a, key("up"), key("y"))
	assert.Equal(t, beforeReceipt, a.flash, "disabled problem acceptance is inert too")
	assert.Equal(t, before, string(a.ws.Docs()["other.spec.md"].Ed.Buffer().Bytes()), "nothing applied")
	assert.Equal(t, 2, itemCards(a), "the card stays")
	p, err := proposal.NewStore(dir).Load("m7qd2")
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, p.Status)
}

// e edits a suggested edit's replacement; ^O keeps it and applies it
// into the buffer.
func TestEditCard(t *testing.T) {
	dir := demoProject(t)
	fixedProposals(t, dir)
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	send(a, key("ctrl+r"), key("down"), key("e"))
	require.Equal(t, CtxHunkEdit, a.context())
	assert.Contains(t, plain(a), "- "+limitFind)
	send(a, key("backspace"), key("backspace"), key("backspace"), key("backspace"), key("backspace"))
	send(a, typeText("ITEMS")...)
	send(a, key("ctrl+o"))
	assert.Equal(t, CtxChecks, a.context(), "back in the sidebar")
	assert.Equal(t, strings.Replace(otherSpec, limitFind, "MUST hold at most 5 ITEMS", 1), string(a.ws.Docs()["other.spec.md"].Ed.Buffer().Bytes()))
	assert.Equal(t, otherSpec, readFile(t, dir, "other.spec.md"), "not written")
	p, err := proposal.NewStore(dir).Load("m7qd2")
	require.NoError(t, err)
	assert.Equal(t, "MUST hold at most 5 ITEMS", p.Hunks[0].Replace, "the edit is kept in the proposal")
	assert.Equal(t, proposal.StatusPending, p.Status, "not recorded until the save")
	saveBuffers(t, a)
	p, err = proposal.NewStore(dir).Load("m7qd2")
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, p.Status)
}

// What changed is visible: a changed anchor has the change bar on every
// line and a mark in the outline, an anchor with open cards a count, and
// the anchor under the cursor its extent in the gutter.
func TestChangeHighlights(t *testing.T) {
	a := changedDemo(t)
	// $Frame.Timing is lines 9-10: green without any proposal shown.
	assert.Nil(t, a.shown())
	assert.Equal(t, map[barKind][]int{barAnchor: {9, 10}}, barsOf(a, "demo.spec.md"))

	// The outline: the bar in front of the changed anchor only.
	ol := a.outline.view(a, 33, 20)
	for _, l := range ol {
		switch {
		case strings.Contains(ansi.Strip(l), "$Frame.Timing"):
			assert.Contains(t, l, a.theme.Bar[barAnchor].Render(" "), "changed: marked")
		case strings.Contains(ansi.Strip(l), "$Frame.Order"):
			assert.NotContains(t, l, a.theme.Bar[barAnchor].Render(" "), "unchanged")
		}
	}
	// A card about $Frame.Order: its count.
	p := &proposal.Proposal{ID: "q1", Kind: proposal.KindAnalysis, Notes: []finding.Note{{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "ambiguity", Severity: "cosmetic"}}}
	a.side.cards = []*card{{kind: cardProblem, p: p, key: "n0", prob: clustersOf(p)[0].problems[0]}}
	for _, l := range a.outline.view(a, 33, 20) {
		if strings.Contains(ansi.Strip(l), "$Frame.Order") {
			assert.Contains(t, ansi.Strip(l), "$Frame.Order 1")
		}
	}
	a.side.cards = nil

	// The extent of the anchor under the cursor: a tick on its lines.
	find(a, "Fragments MUST arrive")
	ex, _ := a.cursorExtent(a.cur.Ed)
	assert.Equal(t, [2]int{12, 13}, ex, "$Frame.Order")
	_, _, w, h := a.editorRect()
	ticks := 0
	for _, l := range a.renderEditor(a.cur.Ed, w, h) {
		if strings.Contains(l, a.theme.Tick.Render("│")) {
			ticks++
		}
	}
	assert.Equal(t, 3, ticks, "a tick on every row of lines 12-13 (12 wraps in two rows)")
	// Outside every anchor: the section's extent, fainter.
	find(a, "## 2. Rules")
	ex, st := a.cursorExtent(a.cur.Ed)
	assert.Equal(t, 7, ex[0])
	assert.Equal(t, a.theme.TickSection.Render("│"), st.Render("│"))
}

// The sidebar's decisions persist in .spec/checks.json: a dismissed card
// stays dismissed after a restart, and the commit message draft is kept.
func TestSidebarPersists(t *testing.T) {
	dir := demoProject(t)
	fixedProposals(t, dir)
	p := &proposal.Proposal{ID: "q2x7a", Author: "human", Kind: proposal.KindAnalysis, Status: proposal.StatusPending,
		Notes:     []finding.Note{{File: "demo.spec.md", Issue: "ambiguity", Severity: "cosmetic", Explanation: "first"}},
		Questions: []string{"second?"}}
	require.NoError(t, proposal.NewStore(dir).Save(p))
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	require.Equal(t, 4, itemCards(a))
	send(a, key("ctrl+r"))
	a.side.selectProposal("q2x7a")
	send(a, key("n"))
	a.side.state.Message = "spec: draft"
	a.saveChecks()

	b := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	require.Equal(t, 3, itemCards(b), "the dismissed note stays dismissed")
	assert.Equal(t, "spec: draft", b.side.state.Message)
	send(b, key("ctrl+r"))
	b.side.selectProposal("q2x7a")
	require.Equal(t, cardQuestion, b.side.selected().kind)
	send(b, key("n"))
	got, err := proposal.NewStore(dir).Load("q2x7a")
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusRejected, got.Status, "all dismissed: settled as rejected")
	assert.Equal(t, "dismissed in spec ui", got.Decision.Reason)
	assert.Equal(t, 2, itemCards(b))
}

// The sidebar is a column when the editor keeps 60 columns, else drawn
// over the text; every frame fits the terminal either way.
func TestSidebarLayout(t *testing.T) {
	dir := demoProject(t)
	fixedProposals(t, dir)
	for _, size := range [][2]int{{180, 40}, {140, 36}, {100, 30}, {80, 24}, {40, 16}} {
		w, h := size[0], size[1]
		a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
		a.Update(tea.WindowSizeMsg{Width: w, Height: h})
		send(a, key("ctrl+r"))
		_, _, ew, _ := a.editorRect()
		if a.sideColumn() {
			assert.GreaterOrEqual(t, ew, minEditorWidth, "%dx%d", w, h)
		} else {
			assert.Equal(t, w-a.outlineWidth(), ew, "%dx%d: drawn over the text", w, h)
		}
		for _, k := range []string{"down", "esc"} {
			frame, _ := a.compose()
			lines := strings.Split(frame, "\n")
			assert.LessOrEqual(t, len(lines), h, "%dx%d", w, h)
			for n, l := range lines {
				assert.LessOrEqual(t, ansi.StringWidth(l), w, "%dx%d line %d", w, h, n)
			}
			send(a, key(k))
		}
	}
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 36})
	send(a, key("ctrl+r"))
	assert.True(t, a.sideColumn(), "140 columns with the outline: a column")
}

// Acceptance 3: no prompt asks for a commit message before the change is
// shown; the 4b/4c prompts are gone from the UI's code.
func TestNoUpFrontCommitPrompt(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		assert.NotContains(t, string(b), "Commit message "+"for", f) // split: grep the tree finds nothing
	}
}

// TestSaveSidebarRender writes a plain frame of the editor with the
// sidebar beside it (for the pull request) when SPEC_SIDEBAR_RENDER names
// a file.
func TestSaveSidebarRender(t *testing.T) {
	out := os.Getenv("SPEC_SIDEBAR_RENDER")
	if out == "" {
		t.Skip("SPEC_SIDEBAR_RENDER is not set")
	}
	dir := demoRepo(t)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "above zero", "of at most 5 s", 1))
	m := newFakeModel(t, modelJSON(t, map[string]any{
		"summary": "the prose and the extra queue should follow the new bound",
		"findings": []map[string]any{
			{"file": "demo.spec.md", "issue": "missing_update", "severity": "inconsistent", "explanation": "The prose should mention the bound so readers see it.", "find": proseFind, "replace": proseReplace},
			{"file": "other.spec.md", "anchor": "$Extra.Limit", "issue": "numeric_mismatch", "severity": "cosmetic", "explanation": "Align the queue size with the fragment bound.", "find": limitFind, "replace": limitReplace},
			{"file": "demo.spec.md", "anchor": "$Frame.Order", "issue": "ambiguity", "severity": "breaking", "explanation": "Order of arrival or of media time? A 5 s bound reorders long fragments."},
		},
		"questions": []string{"Should the bound apply to the last fragment of a stream too?"},
	}))
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	f.send(tea.WindowSizeMsg{Width: 150, Height: 34})
	f.findIn("of at most 5 s")
	f.send(key("ctrl+p"))
	f.until("the cards", func(a *App) bool { return a.job == nil && itemCards(a) == 4 })
	f.send(key("ctrl+r"))
	var frame string
	f.do(func(a *App) {
		content, _ := a.render()
		frame = ansi.Strip(content)
	})
	require.NoError(t, os.WriteFile(out, []byte(frame), 0o644))
	f.finish()
}

// mustLookup returns the binding of k in ctx.
func mustLookup(t *testing.T, ctx Context, k string) Binding {
	t.Helper()
	b, ok := Lookup(ctx, k)
	require.True(t, ok, k)
	return b
}
