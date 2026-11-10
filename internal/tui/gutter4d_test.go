// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// barsOf lists the lines of path that have a bar, by kind.
func barsOf(a *App, path string) map[barKind][]int {
	out := map[barKind][]int{}
	bars := a.gutterBars(path)
	for l := 1; l <= strings.Count(string(a.ws.Source(path)), "\n")+1; l++ {
		if k := bars[l]; k != barNone {
			out[k] = append(out[k], l)
		}
	}
	return out
}

func TestChangedLinePipeKeepsExtentAndDiagnosticPriority(t *testing.T) {
	th := NewThemeProfile(true, false, false, false)
	pipe := markerCell(th, "", barAnchor, true)
	assert.Equal(t, 1, ansi.StringWidth(pipe), "the changed-line glyph is centered in the existing one-cell marker slot")
	assert.Contains(t, pipe, th.Bar[barAnchor].Render("│"), "the exact line keeps the broad extent background")
	diagnostic := markerCell(th, diag.Warning, barAnchor, true)
	expectedDiagnostic := th.MarkWarn.Background(th.Bar[barAnchor].GetBackground()).Render("W")
	assert.Equal(t, expectedDiagnostic, diagnostic, "a diagnostic wins the foreground while retaining the extent background")

	ascii := NewThemeProfile(true, false, false, true)
	assert.Contains(t, markerCell(ascii, "", barAnchor, true), ascii.Bar[barAnchor].Render("|"))
}

// sidebarApp opens the demo repository with the two fixed proposals, its
// baseline loaded, and shows the sidebar on the change k3x7a (newest
// first), with the editor focused: k3x7a is the shown proposal.
func sidebarApp(t *testing.T) *App {
	t.Helper()
	dir := demoRepo(t)
	fixedProposals(t, dir)
	a := newTestApp(t, testOptions(dir))
	a.ws.Base = a.ws.LoadBaseline(context.Background())
	require.NotNil(t, a.ws.Base)
	assert.Nil(t, a.shown(), "the sidebar is hidden: nothing is shown")
	assert.Empty(t, barsOf(a, "demo.spec.md"), "nothing differs from HEAD")
	send(a, key("ctrl+r"), key("esc"))
	require.Equal(t, CtxEditor, a.context())
	require.NotNil(t, a.shown())
	require.Equal(t, "k3x7a", a.shown().p.ID)
	return a
}

// The gutter marks the selected card's edits and changed anchors; the
// status line reports the active editor state without exposing proposal IDs.
func TestProposalBars(t *testing.T) {
	a := sidebarApp(t)
	assert.NotContains(t, statusOf(a), "k3x7a")

	// demo.spec.md: the prose fix is line 15; $Frame.Timing (9-10) is not
	// changed vs HEAD yet.
	assert.Equal(t, map[barKind][]int{barFix: {15}}, barsOf(a, "demo.spec.md"))
	require.NoError(t, a.open(Location{Path: "other.spec.md"}))
	assert.Equal(t, map[barKind][]int{barFix: {5}}, barsOf(a, "other.spec.md"))
	require.NoError(t, a.open(Location{Path: "demo.spec.md"}))

	// An edit elsewhere is a thin yellow pipe (a word replaced); one
	// inside the anchor makes it changed vs HEAD (green), and the edited
	// line purple (the proposal did not see it).
	ed := a.cur.Ed
	ed.SetCursor(len("# demo"))
	send(a, key("!"))
	at, _ := ed.Find("Checked by: TestFragment", 0)
	ed.SetCursor(at.Start)
	send(a, key("!"))
	assert.Equal(t, map[barKind][]int{barAnchor: {9}, barOther: {10}, barProseBoth: {1}, barFix: {15}}, barsOf(a, "demo.spec.md"))
	assert.Equal(t, map[int]bool{10: true}, a.gutterChangedLines("demo.spec.md"), "the thin overlay marks the edited line within the filled anchor extent")

	// The bar is drawn in the marker column, on every row of the line.
	rendered := strings.Join(a.renderEditor(ed, 60, 20), "\n")
	assert.Contains(t, rendered, a.theme.Bar[barAnchor].Render(" "))
	assert.Contains(t, rendered, a.theme.Bar[barFix].Render(" "))
	assert.Contains(t, rendered, a.theme.Bar[barOther].Render(" "))
	assert.Contains(t, rendered, a.theme.Pipe[barProseBoth].Render("│"))

	// Editing the fix's text makes it stale: a dimmer cyan.
	at, _ = ed.Find("for the reader", 0)
	ed.SetCursor(at.Start)
	send(a, key("x"))
	assert.Equal(t, []int{15}, barsOf(a, "demo.spec.md")[barFixStale])

	// Hiding the sidebar ends the preview; the changed anchor stays green
	// and the prose edits purple (line 15: the x typed into the fix's text).
	send(a, key("ctrl+r"), key("ctrl+r"))
	assert.Nil(t, a.shown())
	assert.Equal(t, map[barKind][]int{barAnchor: {9, 10}, barProseBoth: {1, 15}}, barsOf(a, "demo.spec.md"))
}

// ^] stops on the shown proposal's edits in file order; a hunk skipped in
// the review loses its bar.
func TestProposalBarsSkipAndJump(t *testing.T) {
	a := sidebarApp(t)

	// No edits since HEAD: ^] stops on the two fixes.
	send(a, key("ctrl+]"))
	assert.Equal(t, "Fix 1/2 (line 15)", a.flash)
	assert.Equal(t, "demo.spec.md", a.cur.Ed.Path())
	send(a, key("ctrl+]"))
	assert.Equal(t, "Fix 2/2 ($Extra.Limit)", a.flash)
	assert.Equal(t, "other.spec.md", a.cur.Ed.Path())
	send(a, key("shift+f8"))
	assert.Equal(t, "Fix 1/2 (line 15)", a.flash)

	// In the review, skipping the prose fix takes its bar away.
	send(a, key("ctrl+r"), key("enter"))
	r := reviewOf(a)
	require.NotNil(t, r)
	r.state[2] = hunkSkipped
	assert.Empty(t, barsOf(a, "demo.spec.md")[barFix])
	send(a, key("ctrl+x"), key("esc"))

	// An edit is a stop too, in file order.
	require.NoError(t, a.open(Location{Path: "demo.spec.md"}))
	a.cur.Ed.SetCursor(0)
	send(a, key("!"), key("ctrl+home"), key("ctrl+]"))
	assert.Equal(t, "Fix 1/2 (line 15)", a.flash, "the edit is on the cursor's line")
	send(a, key("ctrl+]"), key("ctrl+]"))
	assert.Contains(t, a.flash, "Edit 1/1")
}

// Rejecting the proposal ends its preview; so does a decision made
// elsewhere.
func TestProposalBarsEnd(t *testing.T) {
	a := sidebarApp(t)
	send(a, key("ctrl+r"), key("n"))
	require.NotNil(t, a.prompt)
	for _, c := range "no" {
		send(a, key(string(c)))
	}
	send(a, key("enter"))
	require.NotNil(t, a.shown())
	assert.Equal(t, "m7qd2", a.shown().p.ID, "the next card's proposal")
	assert.NotContains(t, statusOf(a), "m7qd2")

	_, e := a.wf.Reject(a.ws.Root, "m7qd2", "elsewhere", "agent-9")
	require.Nil(t, e)
	a.onProposalsChanged()
	assert.Nil(t, a.shown(), "decided elsewhere: no card left")
	assert.Empty(t, barsOf(a, "demo.spec.md"))
	assert.NotContains(t, statusOf(a), "m7qd2")
}

// The bar colours follow their local palette tokens, with a readable
// diagnostic letter on each background.
func TestBarContrast(t *testing.T) {
	for _, dark := range []bool{true, false} {
		th := NewTheme(dark, false)
		for k, c := range th.bars {
			name := map[barKind]string{barAnchor: "green", barFix: "cyan", barFixStale: "stale cyan", barOther: "purple"}[k]
			assert.NotNil(t, c, "dark=%v: %s has a palette token", dark, name)
			assert.GreaterOrEqual(t, contrast(onColor(c), c), 4.5, "dark=%v: a diagnostic letter on %s", dark, name)
			assert.Equal(t, color.RGBAModel.Convert(c), color.RGBAModel.Convert(th.Bar[k].GetBackground()), "dark=%v: %s uses its local background fill", dark, name)
		}
	}
}

// Apple Terminal: the bars are backgrounds only, so PlainUnderline frames
// still carry no SGR 58 (which Terminal.app reads as blink).
func TestBarsPlainUnderline(t *testing.T) {
	dir := demoRepo(t)
	fixedProposals(t, dir)
	o := testOptions(dir)
	o.PlainUnderline = true
	a := newTestApp(t, o)
	a.ws.Base = a.ws.LoadBaseline(context.Background())
	send(a, key("ctrl+r"), key("enter"), key("ctrl+x"), key("esc"))
	require.NotNil(t, a.shown())
	a.cur.Ed.SetCursor(0)
	send(a, key("!"))
	frame := styled(t, a)
	require.NotContains(t, ansi.Strip(frame), "k3x7a")
	assert.Contains(t, frame, "48;5;", "the bars are drawn")
	assert.NotContains(t, frame, "58;")
	assert.NotContains(t, frame, "[5m")
	assert.NotContains(t, frame, ";5m")
}

// ^N files an analysis: shown in the sidebar, its fix is cyan and the
// edit it analysed purple; dismissing the card ends it.
func TestAnalysisBars(t *testing.T) {
	dir := demoRepo(t)
	writeFile(t, dir, "demo.spec.md", demoSpec+"\nA new line of prose.\n")
	m := newFakeModel(t, modelJSON(t, map[string]any{"summary": "one fix", "questions": []string{},
		"findings": []map[string]any{{"file": "other.spec.md", "anchor": "$Extra.Limit", "issue": "numeric_mismatch",
			"severity": "inconsistent", "explanation": "8 here, 5 there", "find": limitFind, "replace": limitReplace}}}))
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	f.send(key("ctrl+n"))
	f.until("the card", func(a *App) bool { return a.job == nil && itemCards(a) == 1 })
	f.send(key("ctrl+r"), key("esc"))
	f.do(func(a *App) {
		sp := a.shown()
		require.NotNil(t, sp)
		assert.Equal(t, map[barKind][]int{barProseAdd: {17}}, barsOf(a, "demo.spec.md"), "the added words' line (not the blank line)")
		send(a, key("ctrl+g"), key("s"))
		_, ok := a.top().(*statusReferenceScreen)
		require.True(t, ok, "Help → Status exposes the active review identity")
		assert.Contains(t, plain(a), sp.p.ID)
		send(a, key("esc"), key("esc"))
	})
	// Showing the sidebar does not move the editor.
	f.do(func(a *App) { assert.Equal(t, "demo.spec.md", a.cur.Ed.Path()) })
	f.send(key("ctrl+home"), key("ctrl+]"))
	f.do(func(a *App) {
		assert.Equal(t, "demo.spec.md", a.cur.Ed.Path())
		assert.Equal(t, "Edit 1/1: prose in §2: added A new line of prose.", a.flash)
	})
	f.send(key("ctrl+]"))
	f.do(func(a *App) {
		assert.Equal(t, "other.spec.md", a.cur.Ed.Path())
		assert.Equal(t, "Fix 1/1 ($Extra.Limit)", a.flash)
		assert.Equal(t, map[barKind][]int{barFix: {5}}, barsOf(a, "other.spec.md"))
	})
	f.send(key("ctrl+r"), key("n"))
	f.do(func(a *App) {
		assert.Empty(t, a.side.cards, "dismissed")
		assert.Nil(t, a.shown())
	})
	f.finish()
}
