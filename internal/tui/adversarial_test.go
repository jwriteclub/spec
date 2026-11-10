// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/tui/editor"
)

// checkFits asserts that the frame fits the terminal: no line wider than
// the width, no more lines than the height, the cursor inside.
func checkFits(t *testing.T, a *App, what string) {
	t.Helper()
	v := a.View()
	content := v.Content
	lines := strings.Split(content, "\n")
	if a.width <= 0 || a.height <= 0 {
		assert.Equal(t, "", strings.TrimSpace(ansi.Strip(content)), "%s %dx%d: no room, nothing drawn", what, a.width, a.height)
		return
	}
	assert.LessOrEqual(t, len(lines), a.height, "%s %dx%d: too many lines", what, a.width, a.height)
	for k, l := range lines {
		assert.LessOrEqual(t, ansi.StringWidth(l), a.width, "%s %dx%d: line %d %q", what, a.width, a.height, k, ansi.Strip(l))
	}
	if v.Cursor != nil {
		assert.GreaterOrEqual(t, v.Cursor.X, 0, what)
		assert.GreaterOrEqual(t, v.Cursor.Y, 0, what)
		assert.Less(t, v.Cursor.X, a.width, what)
		assert.Less(t, v.Cursor.Y, a.height, what)
	}
}

// TestFrameFitsEverySize resizes through tiny and odd sizes in every
// screen state and checks the frame never exceeds the terminal.
func TestFrameFitsEverySize(t *testing.T) {
	dir := lensdProject(t)
	sizes := [][2]int{{0, 0}, {0, 10}, {10, 0}, {1, 1}, {5, 2}, {8, 3}, {20, 5}, {30, 8}, {49, 12}, {50, 12}, {51, 6}, {100, 30}, {200, 4}}
	states := []struct {
		name  string
		setup func(a *App)
	}{
		{"initial", func(a *App) {}},
		{"no help bar", func(a *App) { send(a, key("alt+x")) }},
		{"outline focus", func(a *App) { send(a, key("f2")) }},
		{"outline hidden", func(a *App) { send(a, key("f2"), key("f2")) }},
		{"hover", func(a *App) {
			find(a, "$Time.Elapsed),")
			send(a, key("ctrl+l"))
			require.NotNil(t, a.popup)
		}},
		{"quick open", func(a *App) { send(a, key("ctrl+q")); send(a, typeText("time")...) }},
		{"find prompt", func(a *App) { send(a, key("ctrl+w")); send(a, typeText(strings.Repeat("long query ", 20))...) }},
		{"save prompt", func(a *App) { send(a, typeText("x")...); send(a, key("ctrl+x")) }},
		{"help", func(a *App) { send(a, key("ctrl+g")) }},
		{"wide text", func(a *App) {
			send(a, tea.PasteMsg{Content: "\t\t\u4e2d\u6587\U0001f44d\U0001f3fd x\n"})
		}},
		{"flash", func(a *App) { send(a, key("ctrl+p")) }},
	}
	for _, st := range states {
		a := newTestApp(t, testOptions(dir))
		st.setup(a)
		for _, sz := range sizes {
			send(a, tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			checkFits(t, a, st.name)
		}
		// And back to a normal size.
		send(a, tea.WindowSizeMsg{Width: frameW, Height: frameH})
		checkFits(t, a, st.name)
	}
}

// TestDanglingRefWhileTyping: a reference to a missing anchor is flagged
// as soon as it is typed, and the flag goes away when it is fixed.
func TestDanglingRefWhileTyping(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	find(a, "lensd is a Go web gateway")
	badAt := func() bool {
		for _, s := range a.cur.Ed.Spans() {
			if s.Flags&editor.FBadRef != 0 && strings.HasPrefix(a.cur.Ed.Buffer().Text(rangeOf(s)), "$Time.Elapsex") {
				return true
			}
		}
		return false
	}
	send(a, typeText("$Time.Elapsex ")...)
	assert.True(t, badAt(), "dangling reference flagged")
	send(a, key("backspace"), key("backspace"))
	send(a, typeText("d ")...)
	assert.False(t, badAt())
	off := strings.Index(string(a.cur.Ed.Buffer().Bytes()), "$Time.Elapsed lensd")
	require.GreaterOrEqual(t, off, 0)
	for _, s := range a.cur.Ed.Spans() {
		if s.Start == off {
			assert.Zero(t, s.Flags&editor.FBadRef, "resolved reference is not flagged")
		}
	}
}

func rangeOf(s editor.Span) pos.Range { return pos.Range{Start: s.Start, End: s.End} }

// TestClickAfterScrollWithOutline: hit-testing stays exact after the editor
// scrolled and with the outline shown, hidden and shown again.
func TestClickAfterScrollWithOutline(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	src := string(a.cur.Ed.Buffer().Bytes())
	for _, outline := range []bool{true, false, true} {
		if a.showOutline != outline {
			send(a, key("f2"))
			if a.showOutline != outline {
				send(a, key("f2"))
			}
			send(a, key("esc"))
		}
		send(a, tea.MouseWheelMsg{X: 60, Y: 5, Button: tea.MouseWheelDown})
		send(a, tea.MouseWheelMsg{X: 60, Y: 5, Button: tea.MouseWheelDown})
		ex, ey, ew, eh := a.editorRect()
		hits := 0
		for y := ey; y < ey+eh; y++ {
			for x := ex + a.cur.Ed.GutterWidth(); x < ex+ew; x += 7 {
				send(a, click(x, y, 0), tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
				off := a.cur.Ed.Cursor()
				cx, cy, ok := a.cur.Ed.CellOf(off)
				require.True(t, ok)
				assert.Equal(t, y, ey+cy, "outline %v: click at %d,%d", outline, x, y)
				if cx+ex == x {
					hits++
				}
				require.LessOrEqual(t, off, len(src))
			}
		}
		assert.Greater(t, hits, 50, "outline %v", outline)
	}
}

// TestCRLFBomPasteAndSaveByteExact: in a BOM + CRLF file, a pasted
// multi-line selection replacement keeps CRLF, and saving touches nothing
// outside the edit.
func TestCRLFBomPasteAndSaveByteExact(t *testing.T) {
	dir := t.TempDir()
	orig := "\ufeff# 1. Bom\r\n\r\n**$A.One (one).** Body\twith tab, \u4e2d.\r\n\r\nTail line\r\nno final newline"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.spec.md"), []byte(orig), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spec.toml"), []byte("name = \"x\"\n"), 0o644))
	a := newTestApp(t, testOptions(dir))
	ed := a.cur.Ed
	assert.Equal(t, len("\ufeff"), ed.Cursor(), "the cursor starts after the BOM")
	// Select "Tail" and paste over it.
	at := strings.Index(orig, "Tail")
	ed.SetCursor(at)
	send(a, key("shift+right"), key("shift+right"), key("shift+right"), key("shift+right"))
	sel, ok := ed.Selection()
	require.True(t, ok)
	require.Equal(t, "Tail", ed.Buffer().Text(sel))
	send(a, tea.PasteMsg{Content: "one\ntwo\r\nthree"})
	send(a, key("ctrl+o"))
	got, err := os.ReadFile(filepath.Join(dir, "x.spec.md"))
	require.NoError(t, err)
	want := strings.Replace(orig, "Tail", "one\r\ntwo\r\nthree", 1)
	assert.Equal(t, want, string(got))

	// Undo and save again restores the original bytes exactly.
	send(a, key("ctrl+z"), key("ctrl+o"))
	got, err = os.ReadFile(filepath.Join(dir, "x.spec.md"))
	require.NoError(t, err)
	assert.Equal(t, orig, string(got))
	assert.False(t, a.cur.Ed.Buffer().Dirty())

	// Typing enter at the end keeps CRLF.
	send(a, key("ctrl+end"), key("enter"))
	assert.True(t, strings.HasSuffix(string(a.cur.Ed.Buffer().Bytes()), "newline\r\n"))
}

// TestUndoAfterHashConversionInApp: one ^Z restores the '#'.
func TestUndoAfterHashConversionInApp(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	find(a, "lensd is a Go web gateway")
	send(a, typeText("PR #1 ")...)
	src := string(a.cur.Ed.Buffer().Bytes())
	require.Contains(t, src, "PR §1 lensd")
	assert.Contains(t, plain(a), "#1 → §1")
	send(a, key("ctrl+z"))
	assert.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "PR #1 lensd")
	send(a, key("ctrl+z"))
	assert.NotContains(t, string(a.cur.Ed.Buffer().Bytes()), "PR #1")
	assert.False(t, a.cur.Ed.Buffer().Dirty())

	// In a code span typed now, no conversion.
	send(a, typeText("`#1` ")...)
	assert.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "`#1` lensd")
}

// TestEveryEditorKeyRuns presses every key of every binding in every
// context it applies to, on a small screen, without a panic and with the
// frame fitting.
func TestEveryEditorKeyRuns(t *testing.T) {
	dir := lensdProject(t)
	for _, b := range Keymap {
		for _, k := range b.Keys {
			a := newTestApp(t, testOptions(dir))
			send(a, tea.WindowSizeMsg{Width: 40, Height: 10})
			find(a, "$Time.Elapsed),")
			send(a, key(k))
			checkFits(t, a, fmt.Sprintf("key %s", k))
			send(a, key(k))
			checkFits(t, a, fmt.Sprintf("key %s twice", k))
		}
	}
}

// TestConflictCancel: ^C dismisses the changed-on-disk banner (the plan's
// "^C cancels any prompt"); the buffer stays modified and the next save
// asks again rather than overwriting the other edit.
func TestConflictCancel(t *testing.T) {
	dir := lensdProject(t)
	orig := readFile(t, dir, "lensd.spec.md")
	a := newTestApp(t, testOptions(dir))
	send(a, typeText("MINE ")...)
	writeFile(t, dir, "lensd.spec.md", "THEIRS\n"+orig)
	send(a, fileChangedMsg{paths: []string{"lensd.spec.md"}})
	require.Equal(t, CtxConflict, a.context())
	send(a, key("d"))
	require.NotNil(t, a.popup)
	send(a, key("ctrl+c"))
	assert.Nil(t, a.prompt)
	assert.Nil(t, a.popup)
	assert.Equal(t, CtxEditor, a.context())
	assert.True(t, a.cur.Ed.Buffer().Dirty())
	send(a, key("ctrl+o"))
	assert.Equal(t, CtxConflict, a.context(), "the next save asks again")
	assert.True(t, strings.HasPrefix(readFile(t, dir, "lensd.spec.md"), "THEIRS\n"))
	send(a, key("esc"))
	assert.Nil(t, a.prompt)
}

// TestOutlineWheelSurvivesRender: scrolling the outline with the wheel is
// not undone by the next frame (which keeps the selection visible only
// when the selection moves).
func TestOutlineWheelSurvivesRender(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	a.outlineFocus = true // explicit focus preserves outline access at narrow widths
	a.outline.sel, a.outline.shown, a.outline.scroll = 0, 0, 0
	a.layout()
	require.Greater(t, len(a.outline.entries), a.bodyHeight()+12)
	for k := 0; k < 4; k++ {
		send(a, tea.MouseWheelMsg{X: 2, Y: 5, Button: tea.MouseWheelDown})
		plain(a)
	}
	assert.Equal(t, 12, a.outline.scroll)
	first := strings.TrimSpace(strings.Split(plain(a), "\n")[0])
	assert.Contains(t, first, strings.TrimSpace(a.outline.entries[a.outline.scroll].text))

	// A click then hits the entry drawn under the mouse.
	send(a, click(2, 3, 0))
	assert.Equal(t, a.outline.scroll+3, a.outline.sel, "clicking row 3 selects the entry drawn there")
}

// TestPasteClosesPopup: a paste while a hover popup is open goes to the
// editor, like any key the popup does not bind.
func TestPasteClosesPopup(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	find(a, "$Time.Elapsed),")
	send(a, key("ctrl+l"))
	require.NotNil(t, a.popup)
	at := a.cur.Ed.Cursor()
	send(a, tea.PasteMsg{Content: "pasted "})
	assert.Nil(t, a.popup)
	assert.Equal(t, "pasted ", a.cur.Ed.Buffer().Text(pos.Range{Start: at, End: at + 7}))
}

// TestCutRunBrokenByClick: consecutive ^K cuts accumulate, but a click in
// between starts a new cut buffer (nano: any cursor move does).
func TestCutRunBrokenByClick(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	send(a, key("ctrl+k"))
	first := a.cutBuf
	x, y, _ := cellOfText(t, a, "lensd is a Go web gateway")
	send(a, click(x, y, 0), tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	send(a, key("ctrl+k"))
	assert.NotContains(t, a.cutBuf, first)
	assert.True(t, strings.HasPrefix(a.cutBuf, "lensd is a Go web gateway"))
}

// TestTypingInCodeSpan: a $Name or #N typed inside a code span is neither
// highlighted as a reference nor converted.
func TestTypingInCodeSpan(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	find(a, "time.Since` of")
	send(a, typeText("$Time.Elapsed #1 $Nope ")...)
	src := string(a.cur.Ed.Buffer().Bytes())
	at := strings.Index(src, "$Time.Elapsed #1 $Nope time.Since")
	require.GreaterOrEqual(t, at, 0, "typed text kept, #1 not converted")
	for _, s := range a.cur.Ed.Spans() {
		if s.End > at && s.Start < at+22 {
			assert.Zero(t, s.Flags&(editor.FInv|editor.FDef|editor.FBadRef|editor.FSection), "span %+v", s)
		}
	}
}

// TestSaveAfterFileDeleted: when the file was deleted on disk, ^O writes
// the buffer back instead of failing for good (which left ^X's only way out
// discarding the edits).
func TestSaveAfterFileDeleted(t *testing.T) {
	dir := lensdProject(t)
	a := newTestApp(t, testOptions(dir))
	send(a, typeText("MINE ")...)
	require.NoError(t, os.Remove(filepath.Join(dir, "lensd.spec.md")))
	send(a, fileChangedMsg{paths: []string{"lensd.spec.md"}})
	assert.Contains(t, plain(a), "lensd.spec.md was deleted on disk")
	send(a, key("ctrl+o"))
	assert.Nil(t, a.prompt)
	assert.False(t, a.cur.Ed.Buffer().Dirty())
	assert.True(t, strings.HasPrefix(readFile(t, dir, "lensd.spec.md"), "MINE # lensd"))
	assert.Contains(t, plain(a), "Saved")
}
