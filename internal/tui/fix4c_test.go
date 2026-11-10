// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/proposal"
)

// The step 4c fixture: $Frame.Order has a stray $ after $Time.Media
// (warning math-hazard), and $Time.Wall is referenced nowhere (warning
// def-unreferenced).
const (
	orderPlain = "**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, see $Frame.Timing.\nChecked by: TestOrder."
	orderGlued = "**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, at most 5$ apart, see $Frame.Timing.\nChecked by: TestOrder."
	orderFixed = "**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, at most `5$` apart, see $Frame.Timing.\nChecked by: TestOrder."
	wallDef    = "**Def $Time.Wall (wall time).** Wall-clock time of the host, in seconds."
	timingCite = "**$Frame.Timing (fragment timing).** Every fragment MUST have a duration above zero in $Time.Media, not in $Time.Wall.\nChecked by: TestFragment."
)

func fixSpec4c() string {
	s := strings.Replace(demoSpec, orderPlain, orderGlued, 1)
	return strings.Replace(s, "## 2. Rules", wallDef+"\n\n## 2. Rules", 1)
}

// fixRepo is a committed repository holding the step 4c fixture.
func fixRepo(t *testing.T) string {
	t.Helper()
	dir := demoRepo(t)
	writeFile(t, dir, "demo.spec.md", fixSpec4c())
	gitRun(t, dir, "commit", "-q", "-am", "fixture")
	return dir
}

// popupText is the open popup's text, without styles.
func popupText(a *App) string {
	if a.popup == nil {
		return ""
	}
	return ansi.Strip(strings.Join(a.popup.lines, "\n"))
}

// Hovering the gutter's W shows the same popup as hovering the underline.
func TestHoverGutterMatchesUnderline(t *testing.T) {
	dir := demoProject(t)
	writeFile(t, dir, "demo.spec.md", fixSpec4c())
	a := newTestApp(t, testOptions(dir))
	x, y, _ := cellOfText(t, a, "$Time.Wall")

	send(a, tea.MouseMotionMsg{X: x + 2, Y: y}, hoverMsg{seq: a.hoverSeq + 1})
	require.NotNil(t, a.popup)
	underline := popupText(a)
	assert.Contains(t, underline, "def-unreferenced")
	assert.Contains(t, underline, "definition $Time.Wall is not referenced")
	assert.Equal(t, CtxDiagPopup, a.context())
	assert.True(t, barEnabled(a, CtxDiagPopup, ActFix), "the popup offers the fix")
	assert.Regexp(t, `\^P +Fix`, plain(a))

	// Away, then onto the gutter marker of the same line.
	send(a, tea.MouseMotionMsg{X: x + 2, Y: 0})
	require.Nil(t, a.popup)
	ex, _, _, _ := a.editorRect()
	gutter := a.cur.Ed.GutterWidth()
	require.Contains(t, ansi.Strip(strings.Split(plain(a), "\n")[y]), "W", "the gutter marks the line")
	send(a, tea.MouseMotionMsg{X: ex + gutter - 2, Y: y}, hoverMsg{seq: a.hoverSeq + 1})
	require.NotNil(t, a.popup, "resting on the gutter opens the popup")
	assert.Equal(t, underline, popupText(a), "the same popup text")
	assert.Equal(t, CtxDiagPopup, a.context())
	assert.True(t, a.popup.mouse)

	// Moving along the gutter keeps it; moving off closes it.
	send(a, tea.MouseMotionMsg{X: ex, Y: y})
	assert.NotNil(t, a.popup)
	send(a, tea.MouseMotionMsg{X: ex, Y: 0})
	assert.Nil(t, a.popup)

	// A gutter row without a marker shows nothing.
	send(a, tea.MouseMotionMsg{X: ex + 1, Y: 0}, hoverMsg{seq: a.hoverSeq + 1})
	assert.Nil(t, a.popup)
}

// A diagnostic without a token ("#1.1") opens a popup when hovered too.
func TestHoverUnderlineWithoutToken(t *testing.T) {
	dir := demoProject(t)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "## 1. Terms", "## 1.1 Terms", 1)+"\nSee #1.1 for terms.\n")
	a := newTestApp(t, testOptions(dir))
	x, y, _ := cellOfText(t, a, "#1.1 for")
	send(a, tea.MouseMotionMsg{X: x + 1, Y: y}, hoverMsg{seq: a.hoverSeq + 1})
	require.NotNil(t, a.popup)
	assert.Contains(t, popupText(a), "section-hash-ref")
	assert.Equal(t, CtxDiagPopup, a.context())
}

// Mechanical, no provider configured (step 4e): ^P on the stray $ wraps it
// in backticks in the buffer at once, with no prompt, no job and nothing
// filed, saved or committed; ^Z undoes it.
func TestFixMechanicalFlow(t *testing.T) {
	dir := fixRepo(t)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, nil)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	f.findIn("5$ apart")
	f.send(key("right"))
	f.do(func(a *App) {
		d, ok := a.diagUnderCursor()
		require.True(t, ok)
		assert.Equal(t, "math-hazard", d.Code)
		assert.True(t, barEnabled(a, CtxEditor, ActPropose))
	})
	f.send(key("ctrl+p"))
	want := strings.Replace(fixSpec4c(), orderGlued, orderFixed, 1)
	f.do(func(a *App) {
		assert.Nil(t, a.prompt, "no prompt")
		assert.Nil(t, a.job, "no job: computed at once")
		d := a.ws.Docs()["demo.spec.md"]
		assert.Equal(t, want, string(d.Ed.Buffer().Bytes()), "byte-exact, in the buffer")
		assert.True(t, d.Ed.Buffer().Dirty())
		assert.Equal(t, "Put 5$ in backticks; ^Z undoes", a.flash)
	})
	assert.Equal(t, fixSpec4c(), readFile(t, dir, "demo.spec.md"), "nothing saved")
	assert.Empty(t, proposalsIn(t, dir), "nothing filed")
	assert.Equal(t, "fixture\n", gitRun(t, dir, "log", "-1", "--format=%s"), "nothing committed")

	f.send(key("ctrl+z"))
	f.do(func(a *App) {
		assert.Equal(t, fixSpec4c(), string(a.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()), "^Z undoes the fix")
	})
	f.finish()
}

// Model, with the fake provider: ^P in the gutter's popup fixes
// def-unreferenced in the background, without a prompt; the request
// carries the rule's instruction; the edit lands in the sidebar, and y
// applies it to the buffer without writing the file or committing.
func TestFixModelFlow(t *testing.T) {
	dir := fixRepo(t)
	reply := modelJSON(t, map[string]any{
		"summary": "cite the wall clock where durations are measured",
		"findings": []map[string]any{{"file": "demo.spec.md", "anchor": "$Frame.Timing", "explanation": "durations are not wall time",
			"find": timingOld, "replace": timingCite}},
		"questions": []string{},
	})
	m := newFakeModel(t, reply)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	var gx, gy int
	f.do(func(a *App) {
		_, y, _ := cellOfText(t, a, "$Time.Wall")
		ex, _, _, _ := a.editorRect()
		gx, gy = ex+1, y
	})
	f.send(tea.MouseMotionMsg{X: gx, Y: gy})
	f.until("the gutter popup", func(a *App) bool { return a.popup != nil })
	f.do(func(a *App) { require.Equal(t, CtxDiagPopup, a.context()) })
	f.send(key("ctrl+p"))
	f.do(func(a *App) {
		assert.Nil(t, a.prompt, "no prompt")
		assert.Nil(t, a.popup)
		require.NotNil(t, a.job)
	})
	f.until("the card", func(a *App) bool { return a.job == nil && itemCards(a) == 1 })
	var id string
	f.do(func(a *App) {
		c := a.side.cards[0]
		id = c.p.ID
		assert.Equal(t, cardProblem, c.kind, "the problem, its fix under it")
		assert.Contains(t, c.hunk.Replace, "$Time.Wall")
		assert.Equal(t, "model", c.p.Fix.Class)
		assert.Equal(t, "^R 1 problem, 1 fix", a.flash)
		assert.Equal(t, "[1 check]", a.badge())
	})
	reqs := m.requests()
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0], "## How to fix `def-unreferenced`", "the rule's instruction")
	assert.Contains(t, reqs[0], "Cite the definition there")
	assert.Contains(t, reqs[0], `code: def-unreferenced\n`)

	f.send(key("ctrl+r"), key("down"))
	f.do(func(a *App) { require.Equal(t, cardEdit, a.side.selected().kind) })
	assert.Contains(t, f.frame(), "durations are not wall time")
	f.do(func(a *App) {
		assert.Equal(t, "cite the wall clock where durations are measured", a.side.selected().p.Summary)
	})
	f.send(key("y"))
	want := strings.Replace(fixSpec4c(), timingOld, timingCite, 1)
	f.do(func(a *App) {
		assert.Equal(t, want, string(a.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()), "applied to the buffer")
		assert.Empty(t, a.side.cards)
		assert.Contains(t, a.flash, "Applied · unsaved · to demo.spec.md (^Z undoes, ^O saves; nothing is committed)")
	})
	assert.Equal(t, fixSpec4c(), readFile(t, dir, "demo.spec.md"), "the file is not written")
	assert.Equal(t, "fixture\n", gitRun(t, dir, "log", "-1", "--format=%s"), "nothing committed")
	rec, err := proposal.NewStore(dir).Load(id)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, rec.Status, "not recorded until the save")
	f.do(func(a *App) { saveBuffers(t, a) })
	rec, err = proposal.NewStore(dir).Load(id)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, rec.Status, "every item decided: settled")
	assert.Empty(t, rec.Decision.Commit)
	f.finish()
}

// Not fixable here: ^P flashes what to do by hand and files nothing.
func TestFixNotFixableFlash(t *testing.T) {
	dir := demoProject(t)
	writeFile(t, dir, "latin.spec.md", "# latin\n\nCaf\xe9 au lait.\n")
	a := newTestApp(t, testOptions(dir))
	require.NoError(t, a.open(Location{Path: "latin.spec.md"}))
	var d diag.Diagnostic
	for _, x := range a.ws.FileDiags("latin.spec.md") {
		if x.Code == diag.CodeFileNotUTF8 {
			d = x
		}
	}
	require.Equal(t, diag.CodeFileNotUTF8, d.Code)
	a.cur.Ed.SetCursor(d.Range.Start)
	send(a, key("ctrl+p"))
	assert.Nil(t, a.prompt, "no commit message: nothing to propose")
	assert.Nil(t, a.job)
	assert.Contains(t, a.flash, "file-not-utf8 cannot be fixed by an edit here: save the file as UTF-8")
	assert.Empty(t, proposalsIn(t, dir))
}

// proposalsIn lists the proposals filed in dir.
func proposalsIn(t *testing.T, dir string) []*proposal.Proposal {
	t.Helper()
	ps, err := proposal.NewStore(dir).List()
	require.NoError(t, err)
	return ps
}

// The diagnostic wins (step 4e, the reverse of 4c): ^P on a warning in an
// anchor changed vs HEAD fixes the warning; elsewhere in that anchor it
// checks the change, without a prompt for a message.
func TestDiagnosticWinsOverChangedAnchor(t *testing.T) {
	dir := fixRepo(t)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, nil)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	f.findIn("TestOrder.")
	f.typeIn("All")
	f.findIn("5$ apart")
	f.send(key("right"))
	f.do(func(a *App) {
		require.NotEmpty(t, a.diagsAt(a.cur.Ed.Cursor()), "the warning is under the cursor")
		an := a.ws.File("demo.spec.md").AnchorAt(a.cur.Ed.Cursor())
		require.NotNil(t, an)
		require.Equal(t, "changed", a.changeState(an))
	})
	f.send(key("ctrl+p"))
	f.do(func(a *App) {
		assert.Nil(t, a.prompt)
		assert.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "`5$`", "the warning is fixed")
	})
	// Elsewhere in the changed anchor: its check, after saving (no
	// message is asked; no provider, so it fails at once).
	f.findIn("Fragments MUST arrive")
	f.send(key("ctrl+p"))
	f.do(func(a *App) { require.Equal(t, CtxSaveFirst, a.context()) })
	assert.Contains(t, f.frame(), "Check reads the files")
	f.send(key("y"))
	f.do(func(a *App) { assert.Nil(t, a.prompt, "no message prompt") })
	f.until("the check to fail", func(a *App) bool { return a.job == nil && a.popup != nil && a.popup.notice })
	f.finish()
}

// Every step 4c popup fits the terminal at every size, the help bar
// included.
func TestDiagPopupFitsWidth(t *testing.T) {
	dir := demoProject(t)
	writeFile(t, dir, "demo.spec.md", fixSpec4c())
	for _, size := range [][2]int{{100, 30}, {80, 24}, {60, 20}, {40, 16}, {24, 10}} {
		w, h := size[0], size[1]
		a := newTestApp(t, testOptions(dir))
		a.Update(tea.WindowSizeMsg{Width: w, Height: h})
		find(a, "$Time.Wall")
		send(a, key("ctrl+l"))
		require.NotNil(t, a.popup, "%dx%d", w, h)
		require.Equal(t, CtxDiagPopup, a.context())
		frame, _ := a.compose()
		lines := strings.Split(frame, "\n")
		assert.LessOrEqual(t, len(lines), h, "%dx%d", w, h)
		for k, l := range lines {
			assert.LessOrEqual(t, ansi.StringWidth(l), w, "%dx%d: line %d", w, h, k)
		}
		for _, l := range a.helpBar(CtxDiagPopup, w) {
			assert.LessOrEqual(t, ansi.StringWidth(l), w, "%dx%d: help bar", w, h)
		}
		send(a, key("ctrl+p"))
		require.NotNil(t, a.job, "%dx%d: the model fix runs", w, h)
		frame, _ = a.compose()
		for k, l := range strings.Split(frame, "\n") {
			assert.LessOrEqual(t, ansi.StringWidth(l), w, "%dx%d fix running: line %d", w, h, k)
		}
		a.cancelJob()
	}
}

// The help screen's ^P entry says what ^P does on a diagnostic.
func TestProposeKeyDescribesFix(t *testing.T) {
	b, ok := Lookup(CtxEditor, "ctrl+p")
	require.True(t, ok)
	assert.Contains(t, b.Desc, "on a diagnostic, fix it")
	b, ok = Lookup(CtxDiagPopup, "ctrl+p")
	require.True(t, ok)
	assert.Equal(t, ActFix, b.Action)
	_, ok = Lookup(CtxPopup, "ctrl+p")
	assert.False(t, ok, "a popup without a diagnostic leaves ^P to the editor")
}
