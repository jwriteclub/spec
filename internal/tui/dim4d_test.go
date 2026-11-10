// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// barState is ctx's help bar as "KEY Label", with a leading "~" on an
// entry drawn dimmed. It also checks the rendering: a dimmed entry is
// drawn in the dim styles, the others in the usual ones.
func barState(t *testing.T, a *App, ctx Context) []string {
	t.Helper()
	rendered := strings.Join(a.helpBar(ctx, 400), "\n")
	th := a.theme
	var out []string
	for _, b := range BarBindings(ctx) {
		label := a.labelOf(ctx, b)
		e := b.Key + " " + label
		if a.canRun(ctx, b) {
			assert.Contains(t, rendered, th.HelpLabel.Render(" "+label), "%s: %s drawn as available", ctx, e)
		} else {
			assert.Contains(t, rendered, th.HelpLabelDim.Render(" "+label), "%s: %s drawn dimmed", ctx, e)
			e = "~" + e
		}
		out = append(out, e)
	}
	return out
}

// barEnabled reports whether the entry of action act is drawn available
// in ctx.
func barEnabled(a *App, ctx Context, act Action) bool {
	for _, b := range BarBindings(ctx) {
		if b.Action == act {
			return a.canRun(ctx, b)
		}
	}
	return false
}

// TestHelpBarDims pins which editor entries are dimmed, slot by slot, as
// the state changes; a dimmed key leaves the receipt and editor state unchanged.
func TestHelpBarDims(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	assert.Equal(t, []string{"^G Help", "^X Exit", "^O Save", "^Q Open", "^W Where Is", "~^P Propose", "^K Cut",
		"~^U Paste", "~^Z Undo", "~^Y Redo", "~^T Follow", "^R Checks", "~^B Back", "^F Format", "~^C Copy",
		"^N Analyze", "^^ Outline", "⇧Tab Next Pane"}, barState(t, a, CtxEditor), "a fresh buffer with no baseline, no history and no cut text")

	a.flash = "Previous receipt"
	for _, k := range []string{"ctrl+p", "ctrl+u", "ctrl+z", "ctrl+y", "ctrl+t", "ctrl+b", "ctrl+c"} {
		before := string(a.cur.Ed.Buffer().Bytes())
		send(a, key(k))
		assert.Equal(t, "Previous receipt", a.flash, k)
		assert.Equal(t, CtxEditor, a.context(), k)
		assert.Equal(t, before, string(a.cur.Ed.Buffer().Bytes()), k)
	}

	// Typing, a token under the cursor, a selection, a cut and a jump turn
	// entries on, in their slots.
	x, y, _ := cellOfText(t, a, "$Time.Elapsed)")
	send(a, tea.MouseClickMsg{X: x + 1, Y: y, Button: tea.MouseLeft})
	assert.True(t, barEnabled(a, CtxEditor, ActToTarget), "^T on a reference")
	send(a, key("ctrl+t"))
	assert.True(t, barEnabled(a, CtxEditor, ActBack), "^B after a jump")
	send(a, key("x"))
	assert.True(t, barEnabled(a, CtxEditor, ActUndo))
	send(a, key("ctrl+z"))
	assert.True(t, barEnabled(a, CtxEditor, ActRedo))
	send(a, key("shift+right"))
	assert.True(t, barEnabled(a, CtxEditor, ActCopy), "^C with a selection")
	send(a, key("ctrl+k"))
	assert.True(t, barEnabled(a, CtxEditor, ActPaste))
	assert.Equal(t, []string{"^G Help", "^X Exit", "^O Save", "^Q Open", "^W Where Is", "~^P Propose", "^K Cut",
		"^U Paste", "^Z Undo", "~^Y Redo", "~^T Follow", "^R Checks", "^B Back", "^F Format", "~^C Copy",
		"^N Analyze", "^^ Outline", "⇧Tab Next Pane"}, barState(t, a, CtxEditor), "the cut took the reference with it; the buffer is modified: ^R Commit")

	// A job running dims ^P and ^N, and turns ^C on (it cancels).
	a.job = &job{label: "Checking", cancel: func() {}}
	assert.False(t, barEnabled(a, CtxEditor, ActAnalyze))
	assert.True(t, barEnabled(a, CtxEditor, ActCopy))
	beforeReceipt, beforeJob := a.flash, a.job
	send(a, key("ctrl+n"))
	assert.Equal(t, beforeReceipt, a.flash)
	assert.Same(t, beforeJob, a.job)
	a.job = nil

	// Nothing left to cut on an empty last line.
	send(a, key("ctrl+end"))
	if !a.cur.Ed.CanCut() {
		assert.False(t, barEnabled(a, CtxEditor, ActCut))
		beforeReceipt := a.flash
		send(a, key("ctrl+k"))
		assert.Equal(t, beforeReceipt, a.flash)
	}
}

// The popup's ^T is dimmed when the popup has nothing to follow: a
// definition's own popup.
func TestHelpBarDimsPopup(t *testing.T) {
	dir := demoProject(t)
	writeFile(t, dir, "demo.spec.md", fixSpec4c())
	a := newTestApp(t, testOptions(dir))
	x, y, _ := cellOfText(t, a, "$Time.Media, at most")
	send(a, tea.MouseMotionMsg{X: x + 2, Y: y}, hoverMsg{seq: a.hoverSeq + 1})
	require.NotNil(t, a.popup)
	require.NotNil(t, a.popup.target)
	assert.Equal(t, []string{"^T Follow", "Esc Close"}, barState(t, a, a.context()), "a reference's popup")

	x, y, _ = cellOfText(t, a, "$Time.Wall")
	send(a, tea.MouseMotionMsg{X: x + 2, Y: 0})
	send(a, tea.MouseMotionMsg{X: x + 2, Y: y}, hoverMsg{seq: a.hoverSeq + 1})
	require.Equal(t, CtxDiagPopup, a.context())
	assert.Equal(t, []string{"~^T Follow", "^P Fix", "Esc Close"}, barState(t, a, CtxDiagPopup), "the definition's own popup")
	beforeReceipt := a.flash
	send(a, key("ctrl+t"))
	assert.Equal(t, beforeReceipt, a.flash)
}

// The sidebar's keys follow the selected card; the review of an agent's
// proposal: the change itself cannot be accepted, skipped, revised or
// edited; a finding hunk can.
func TestHelpBarDimsReview(t *testing.T) {
	dir := demoProject(t)
	fixedProposals(t, dir)
	a := newTestApp(t, testOptions(dir))
	assert.True(t, barEnabled(a, CtxEditor, ActReview))
	send(a, key("ctrl+r"))
	require.Equal(t, CtxChecks, a.context())
	require.Equal(t, cardProposal, a.side.selected().kind, "the agent's proposal, newest first")
	assert.Equal(t, []string{"y Accept", "n Dismiss", "~e Edit", "~f Feedback", "Enter Go To", "c Message", "A Commit",
		"^R Hide", "^F Format", "r Removed", "Up Prev", "Down Next", "Esc Editor", "⇧Tab Next Pane"}, barState(t, a, CtxChecks))
	send(a, key("down"))
	assert.Equal(t, cardProblem, a.side.selected().kind, "the analysis's problem: e and f act on its one fix")
	assert.Equal(t, []string{"y Accept", "n Dismiss", "e Edit", "f Feedback", "Enter Go To", "c Message", "A Commit",
		"^R Hide", "^F Format", "r Removed", "Up Prev", "Down Next", "Esc Editor", "⇧Tab Next Pane"}, barState(t, a, CtxChecks))
	send(a, key("up"), key("enter"))
	r := reviewOf(a)
	require.NotNil(t, r)
	r.sel = 0
	require.True(t, r.isPrimary(r.selected()))
	assert.Equal(t, []string{"~y Accept", "~n Skip", "~f Feedback", "~e Edit", "A Apply", "R Reject",
		"Esc Close", "Up Prev", "Down Next", "~r Recheck"}, barState(t, a, CtxReview))
	beforeReceipt := a.flash
	send(a, key("n"))
	assert.Equal(t, beforeReceipt, a.flash)
	assert.Equal(t, 0, r.sel)

	send(a, key("down"))
	assert.Equal(t, []string{"y Accept", "n Skip", "f Feedback", "e Edit", "A Apply", "R Reject",
		"Esc Close", "Up Prev", "Down Next", "~r Recheck"}, barState(t, a, CtxReview))
	_, cancel := context.WithCancel(context.Background())
	a.job = &job{label: "Revising", cancel: cancel}
	assert.False(t, barEnabled(a, CtxReview, ActFeedback), "one job at a time")
	a.job = nil
}

// The dim entries keep their slots: the plain text of every bar is the
// same whatever is dimmed.
func TestHelpBarDimsKeepSlots(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	before := ansi.Strip(strings.Join(a.helpBar(CtxEditor, frameW), "\n"))
	send(a, key("x"), key("ctrl+k"), key("ctrl+z"), key("ctrl+z"))
	require.False(t, a.cur.Ed.Buffer().Dirty(), "undone: ^R says Checks again")
	after := ansi.Strip(strings.Join(a.helpBar(CtxEditor, frameW), "\n"))
	assert.Equal(t, before, after)
}
