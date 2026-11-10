// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"context"
	"fmt"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/tui/editor"
)

// TestSidebarClickFocus: clicking a card focuses the sidebar and selects
// it; clicking the editor takes the focus back, the sidebar stays.
func TestSidebarClickFocus(t *testing.T) {
	dir := demoProject(t)
	fixedProposals(t, dir)
	a := newTestApp(t, testOptions(dir))
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	send(a, key("ctrl+r"), key("esc"))
	require.True(t, a.sideColumn(), "160 columns: the sidebar has a column")
	require.Equal(t, CtxEditor, a.context())

	// Click the second card (the analysis's edit).
	x, _ := a.sideRect()
	_ = a.sideView(sideWidth, a.bodyHeight())
	s := a.side
	require.Len(t, s.cards, 3, "the proposal, the analysis's problem and its fix")
	send(a, click(x+3, s.starts[1]+1-s.scroll, 0))
	assert.Equal(t, CtxChecks, a.context())
	assert.Equal(t, 1, s.sel)
	assert.Equal(t, "m7qd2", a.shown().p.ID)

	ex, ey, _, _ := a.editorRect()
	send(a, click(ex+10, ey+2, 0))
	assert.Equal(t, CtxEditor, a.context())
	assert.True(t, a.sideShown)
}

// changedDemo is a committed demo repository with $Frame.Timing changed on
// disk, opened with its HEAD baseline loaded.
func changedDemo(t *testing.T) *App {
	t.Helper()
	dir := demoRepo(t)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "above zero", "of at most 5 s", 1))
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.ws.Base = a.ws.LoadBaseline(context.Background())
	require.Equal(t, baseline.StateOK, a.ws.Base.State, a.ws.Base.Reason)
	return a
}

// TestProposeRouting (step 4e): ^P never asks for a message. In a
// changed anchor it checks that anchor; in an unchanged anchor there is
// nothing to propose; outside every anchor it checks every changed anchor.
func TestProposeRouting(t *testing.T) {
	a := changedDemo(t)
	find(a, "of at most 5 s")
	send(a, key("ctrl+p"))
	assert.Nil(t, a.prompt, "no message is asked")
	require.NotNil(t, a.job)
	assert.Equal(t, "Checking $Frame.Timing", a.job.label)
	a.cancelJob()

	find(a, "Fragments MUST arrive")
	beforeReceipt := a.flash
	send(a, key("ctrl+p"))
	assert.Nil(t, a.job)
	assert.Equal(t, beforeReceipt, a.flash)
	assert.False(t, barEnabled(a, CtxEditor, ActPropose), "dimmed there")

	send(a, key("ctrl+home"), key("ctrl+p"))
	require.NotNil(t, a.job)
	assert.Equal(t, "Checking every changed anchor", a.job.label)
	a.cancelJob()
}

// underlineRuns renders a run of every underlined highlight in th, as the
// 256-colour profile sends it.
func underlineRuns(t *testing.T, th *Theme) string {
	t.Helper()
	var b strings.Builder
	for _, f := range []editor.Flag{editor.FBadRef, editor.FInv | editor.FDiagError, editor.FDiagWarn, editor.FDiagHint, editor.FSection | editor.FDiagHint} {
		b.WriteString(th.Run(f).Render("x"))
	}
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ANSI256}
	_, err := w.WriteString(b.String())
	require.NoError(t, err)
	return buf.String()
}

// TestPlainUnderline: Terminal.app does not know SGR 58 (underline colour)
// and reads ESC[4;58;5;Nm's 5 as blink, so with PlainUnderline the
// underlined runs carry no 58 sequence and no underline style; otherwise
// the colours stay.
func TestPlainUnderline(t *testing.T) {
	for _, dark := range []bool{true, false} {
		coloured := underlineRuns(t, NewTheme(dark, false))
		assert.Contains(t, coloured, "58;5;", "underline colours are kept by default")
		plainRuns := underlineRuns(t, NewTheme(dark, true))
		assertPlainUnderlineCells(t, plainRuns, 10, 1)
		assert.NotContains(t, plainRuns, "4:", "no underline style (dotted)")
		assert.Contains(t, plainRuns, "4m", "still underlined")
	}

	// The whole frame, with a bad reference and a diagnostic on screen,
	// and after the terminal reports its background.
	for _, plainUL := range []bool{false, true} {
		dir := demoProject(t)
		writeFile(t, dir, "demo.spec.md", demoSpec+"\nSee $No.Such.Name and §9.\n")
		o := testOptions(dir)
		o.PlainUnderline = plainUL
		o.FixedTheme = false
		a := newTestApp(t, o)
		send(a, tea.BackgroundColorMsg{Color: color.White})
		frame := styled(t, a)
		require.Contains(t, ansi.Strip(frame), "See $No.Such.Name and §9.")
		if plainUL {
			assertPlainUnderlineCells(t, frame, frameW, frameH)
		} else {
			assert.Contains(t, frame, "58;5;")
		}
	}
}

// Inspect terminal attributes: foreground index 58 is unrelated to SGR 58.
func assertPlainUnderlineCells(t *testing.T, text string, width, height int) {
	t.Helper()
	em := vt.NewEmulator(width, height)
	defer em.Close()
	for y, line := range strings.Split(text, "\n") {
		if y >= height {
			break
		}
		_, err := em.WriteString(fmt.Sprintf("\x1b[%d;1H%s\x1b[0m", y+1, line))
		require.NoError(t, err)
	}
	for y := range height {
		for x := range width {
			if cell := em.CellAt(x, y); cell != nil {
				assert.Nil(t, cell.Style.UnderlineColor, "cell %d,%d", x, y)
				assert.True(t, cell.Style.Underline == uv.UnderlineNone || cell.Style.Underline == uv.UnderlineSingle, "plain underline at %d,%d", x, y)
			}
		}
	}
}

// status is the status line of the plain frame.
func statusOf(a *App) string {
	row := a.bodyHeight()
	if a.showHelpBar && a.height > 36 {
		row++
	}
	return strings.Split(plain(a), "\n")[row]
}

// TestJumpToEdits: ^] (and F8) goes to the next content edit since HEAD
// (a changed anchor, or a prose paragraph whose words changed), across the
// project's files in order and wrapping; shift+F8 goes back. An unsaved
// buffer counts, and the flash says what changed.
func TestJumpToEdits(t *testing.T) {
	dir := demoRepo(t)
	edited := strings.Replace(demoSpec, "above zero", "of at most 5 s", 1)
	edited = strings.Replace(edited, "Prose that mentions", "Prose that names", 1)
	writeFile(t, dir, "demo.spec.md", edited)
	a := newTestApp(t, testOptions(dir))
	send(a, key("ctrl+]"))
	assert.Equal(t, "No HEAD baseline yet: edits are changes since HEAD", a.flash)
	a.ws.Base = a.ws.LoadBaseline(context.Background())
	require.Equal(t, baseline.StateOK, a.ws.Base.State, a.ws.Base.Reason)

	// An unsaved paragraph at the end of other.spec.md (a blank line, then
	// the prose: the edit starts at the blank line 6).
	a.jump(Location{Path: "other.spec.md"})
	send(a, key("ctrl+end"))
	send(a, typeText("\nUnsaved prose.")...)
	require.True(t, a.cur.Ed.Buffer().Dirty())
	a.jump(Location{Path: "demo.spec.md"})

	lineStart := func(path string, line int) Location {
		return Location{Path: path, Off: a.ws.File(path).Index().LineStart(line)}
	}
	at := func(path, q string) Location {
		return Location{Path: path, Off: strings.Index(string(a.ws.Source(path)), q)}
	}
	timing := "Edit 1/3: inv $Frame.Timing changed: replaced above zero with of at most 5 s"
	prose := "Edit 2/3: prose in §2: replaced mentions with names"
	unsaved := "Edit 3/3: prose in §1: added Unsaved prose."
	steps := []struct {
		key   string
		flash string
		at    Location
	}{
		{"ctrl+]", timing, lineStart("demo.spec.md", 9)},
		{"f8", prose, at("demo.spec.md", "names")},
		{"ctrl+]", unsaved, at("other.spec.md", "Unsaved")},
		{"ctrl+]", timing, lineStart("demo.spec.md", 9)},
		{"shift+f8", unsaved, at("other.spec.md", "Unsaved")},
		{"shift+f8", prose, at("demo.spec.md", "names")},
	}
	for k, s := range steps {
		send(a, key(s.key))
		assert.Equal(t, s.flash, a.flash, "step %d (%s)", k, s.key)
		assert.Equal(t, s.at, a.here(), "step %d (%s)", k, s.key)
		assert.Contains(t, statusOf(a), strings.Split(s.flash, ":")[0]+":", "step %d: bounded receipt remains visible; full detail is in a.flash", k)
	}
	// ^B goes back along the jumps.
	send(a, key("ctrl+b"))
	assert.Equal(t, at("other.spec.md", "Unsaved"), a.here())

	// From inside an edit, ^] goes to the following one; from a line after
	// it, shift+F8 comes back to its stop.
	a.jump(Location{Path: "demo.spec.md", Off: lineStart("demo.spec.md", 9).Off + 3})
	send(a, key("ctrl+]"))
	assert.Equal(t, prose, a.flash)
	a.jump(Location{Path: "demo.spec.md", Off: lineStart("demo.spec.md", 10).Off + 3})
	send(a, key("shift+f8"))
	assert.Equal(t, timing, a.flash)
	assert.Equal(t, lineStart("demo.spec.md", 9), a.here())

	// Undoing the unsaved line leaves the saved edits.
	a.jump(Location{Path: "other.spec.md"})
	for range 20 {
		if !a.cur.Ed.Buffer().Dirty() {
			break
		}
		send(a, key("ctrl+z"))
	}
	require.False(t, a.cur.Ed.Buffer().Dirty())
	send(a, key("ctrl+]"))
	assert.Equal(t, "Edit 1/2: inv $Frame.Timing changed: replaced above zero with of at most 5 s", a.flash, "wrapped from the last file")

	// Nothing changed.
	b := newTestApp(t, testOptions(demoRepo(t)))
	b.ws.Base = b.ws.LoadBaseline(context.Background())
	send(b, key("ctrl+]"))
	assert.Equal(t, "No edits since HEAD", b.flash)
	assert.Equal(t, Location{Path: "demo.spec.md"}, b.here())
}

// TestJumpToEditKeys: ^], F8 and shift+F8 arrive from the terminal as
// distinct keys and are bound in the editor, on the help screen only (the
// editor's help bar is full at 16 entries).
func TestJumpToEditKeys(t *testing.T) {
	r := runRaw(t, &keyRecorder{want: 3}, "\x1d\x1b[19~\x1b[19;2~")
	assert.Equal(t, []string{"ctrl+]", "f8", "shift+f8"}, r.keys)
	for k, act := range []Action{ActNextEdit, ActNextEdit, ActPrevEdit} {
		b, ok := Lookup(CtxEditor, r.keys[k])
		require.True(t, ok, r.keys[k])
		assert.Equal(t, act, b.Action, r.keys[k])
		assert.False(t, b.Bar, "%s is on the help screen only", r.keys[k])
	}
	help := strings.Join(newHelpScreen().lines, "\n")
	assert.Contains(t, help, "  ctrl+], f8 ")
	assert.Contains(t, help, "  shift+f8, f18 ")
	b, ok := Lookup(CtxEditor, "f18")
	require.True(t, ok)
	assert.Equal(t, ActPrevEdit, b.Action, "Terminal.app sends F18 for shift+F8")
}
