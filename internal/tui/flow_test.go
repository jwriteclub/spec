// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/project"
)

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	return string(b)
}

func writeFile(t *testing.T, dir, name, text string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644))
}

// find moves the cursor to the first match of q after it (^W).
func find(a *App, q string) {
	send(a, key("ctrl+w"))
	send(a, typeText(q)...)
	send(a, key("enter"))
}

func TestTypeSaveAndExit(t *testing.T) {
	dir := lensdProject(t)
	a := newTestApp(t, testOptions(dir))
	send(a, typeText("Hello. ")...)
	assert.True(t, a.cur.Ed.Buffer().Dirty())
	assert.Equal(t, "Unsaved", a.persistence())

	send(a, key("ctrl+o"))
	assert.False(t, a.cur.Ed.Buffer().Dirty())
	assert.True(t, strings.HasPrefix(readFile(t, dir, "lensd.spec.md"), "Hello. # lensd specification"))
	assert.Contains(t, plain(a), "Saved lensd.spec.md")

	// One ^Z undoes the typed run; exiting then asks to save.
	send(a, key("ctrl+z"))
	assert.True(t, a.cur.Ed.Buffer().Dirty())
	a.Update(key("ctrl+x"))
	require.NotNil(t, a.prompt)
	assert.Equal(t, CtxConfirm, a.context())
	assert.Contains(t, plain(a), "Save modified buffer lensd.spec.md?")
	// ^C cancels; N exits without saving.
	send(a, key("ctrl+c"))
	assert.Nil(t, a.prompt)
	a.Update(key("ctrl+x"))
	_, cmd := a.Update(key("n"))
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
	assert.True(t, strings.HasPrefix(readFile(t, dir, "lensd.spec.md"), "Hello. "), "not saved")
}

func TestExitSavesWithYes(t *testing.T) {
	dir := lensdProject(t)
	a := newTestApp(t, testOptions(dir))
	send(a, typeText("X")...)
	a.Update(key("ctrl+x"))
	_, cmd := a.Update(key("y"))
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
	assert.True(t, strings.HasPrefix(readFile(t, dir, "lensd.spec.md"), "X# lensd"))

	// A clean buffer exits at once.
	a = newTestApp(t, testOptions(dir))
	_, cmd = a.Update(key("ctrl+x"))
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestSaveWhileLocked(t *testing.T) {
	dir := lensdProject(t)
	a := newTestApp(t, testOptions(dir))
	lock, err := project.AcquireLock(dir, "agent")
	require.NoError(t, err)
	send(a, typeText("X")...)
	send(a, key("ctrl+o"))
	assert.True(t, a.cur.Ed.Buffer().Dirty())
	assert.Contains(t, a.flash, "another spec process is writing")
	require.NoError(t, lock.Release())
	send(a, key("ctrl+o"))
	assert.False(t, a.cur.Ed.Buffer().Dirty())
}

func TestHashConversionInApp(t *testing.T) {
	dir := lensdProject(t)
	a := newTestApp(t, testOptions(dir))
	find(a, "of scope.")
	send(a, key("ctrl+e"))
	send(a, typeText(" See #1 and PR #99 ")...)
	src := string(a.cur.Ed.Buffer().Bytes())
	assert.Contains(t, src, "of scope. See §1 and PR #99 \n")

	// Right after the conversion, one undo restores the '#'.
	send(a, typeText("#1.")...)
	send(a, key("enter"))
	src = string(a.cur.Ed.Buffer().Bytes())
	assert.Contains(t, src, "PR #99 §1.\n")
	assert.Contains(t, plain(a), "#1 → §1 (^Z undoes)", "the conversion is flashed")
	send(a, key("ctrl+z"))
	src = string(a.cur.Ed.Buffer().Bytes())
	assert.Contains(t, src, "PR #99 #1.\n")

	// The setting turns it off.
	opts := testOptions(dir)
	opts.HashToSection = false
	a = newTestApp(t, opts)
	send(a, typeText("#1 ")...)
	assert.True(t, strings.HasPrefix(string(a.cur.Ed.Buffer().Bytes()), "#1 "))
}

func TestQuickOpen(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	send(a, key("ctrl+q"))
	require.NotNil(t, a.list)
	assert.Equal(t, CtxList, a.context())
	send(a, typeText("§0.2")...)
	require.NotEmpty(t, a.list.items)
	assert.Contains(t, a.list.items[0].key, "0.2 Intervals")
	send(a, key("enter"))
	assert.Nil(t, a.list)
	src := string(a.cur.Ed.Buffer().Bytes())
	assert.Equal(t, strings.Index(src, "### 0.2 Intervals"), a.cur.Ed.Cursor())

	// Anchors match with or without $; "#" works for sections too.
	send(a, key("ctrl+q"))
	send(a, typeText("$frame.timing")...)
	assert.Contains(t, a.list.items[0].key, "Frame.Timing")
	send(a, key("esc"))
	assert.Nil(t, a.list)
	send(a, key("ctrl+q"))
	send(a, typeText("#1")...)
	assert.True(t, strings.HasPrefix(a.list.items[0].key, "1 "))
	send(a, key("backspace"), key("backspace"))
	assert.Empty(t, a.list.query)
}

func TestRefsAndBackForward(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	find(a, "**Def $Time.Elapsed")
	send(a, key("right"), key("right"), key("right"), key("right"), key("right"), key("right"), key("right"))
	start := a.cur.Ed.Cursor()
	send(a, key("ctrl+\\"))
	require.NotNil(t, a.list, a.flash)
	assert.Contains(t, a.list.title, "References to $Time.Elapsed")
	n := len(a.list.items)
	assert.Equal(t, 2, n)
	send(a, key("down"), key("enter"))
	assert.NotEqual(t, start, a.cur.Ed.Cursor())
	jumped := a.cur.Ed.Cursor()
	send(a, key("ctrl+b"))
	assert.Equal(t, start, a.cur.Ed.Cursor())
	send(a, key("alt+right"))
	assert.Equal(t, jumped, a.cur.Ed.Cursor())
	send(a, key("alt+right"))
	assert.Empty(t, a.flash, "disabled Forward leaves status unchanged")
	// The alt aliases still work.
	send(a, key("alt+left"))
	assert.Equal(t, start, a.cur.Ed.Cursor())
	send(a, key("alt+."))
	assert.Equal(t, jumped, a.cur.Ed.Cursor())
}

func TestToTargetSection(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	find(a, "§1 \"quoted")
	// That one is inside code: not a reference.
	send(a, key("ctrl+t"))
	assert.Empty(t, a.flash, "disabled Follow leaves status unchanged")

	// A § reference typed in prose jumps to its heading.
	a.cur.Ed.SetCursor(0)
	send(a, typeText("See §1 now. ")...)
	a.cur.Ed.SetCursor(len("See §"))
	send(a, key("ctrl+t"))
	src := string(a.cur.Ed.Buffer().Bytes())
	assert.Equal(t, strings.Index(src, "## 1. Frames"), a.cur.Ed.Cursor())
	// And a dangling one says so.
	a.cur.Ed.SetCursor(0)
	send(a, typeText("§77 ")...)
	a.cur.Ed.SetCursor(1)
	send(a, key("ctrl+t"))
	assert.Contains(t, a.flash, "§77 does not match")
}

func TestGoToLine(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	send(a, key("ctrl+_"))
	assert.Equal(t, CtxPrompt, a.context())
	send(a, typeText("46")...)
	send(a, key("enter"))
	assert.Equal(t, 46, a.cur.Ed.Buffer().Index().Line(a.cur.Ed.Cursor()))
	assert.Contains(t, a.statusLine(a.width), "46:1", "the status line shows the position")
	// Esc and ^C cancel a prompt.
	send(a, key("ctrl+_"), key("esc"))
	assert.Nil(t, a.prompt)
	send(a, key("ctrl+_"), key("ctrl+c"))
	assert.Nil(t, a.prompt)
	assert.Contains(t, a.flash, "Cancelled")
}

// TestFindRepeat: ^W starts empty and shows the last search; Enter on an
// empty answer finds it again (nano), wrapping at the end.
func TestFindRepeat(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	src := string(a.cur.Ed.Buffer().Bytes())
	find(a, "$Time.Elapsed")
	first := strings.Index(src, "$Time.Elapsed")
	assert.Equal(t, first, a.cur.Ed.Cursor())
	send(a, key("ctrl+w"))
	assert.Empty(t, a.prompt.input)
	assert.Contains(t, plain(a), "Search [$Time.Elapsed]:")
	send(a, key("enter"))
	second := first + 1 + strings.Index(src[first+1:], "$Time.Elapsed")
	assert.Equal(t, second, a.cur.Ed.Cursor())
	// Repeating until it wraps comes back to the first.
	for k := 0; k < 10 && a.cur.Ed.Cursor() != first; k++ {
		send(a, key("ctrl+w"), key("enter"))
	}
	assert.Equal(t, first, a.cur.Ed.Cursor())
	// With no search yet, an empty answer says so.
	a = newTestApp(t, testOptions(lensdProject(t)))
	send(a, key("ctrl+w"), key("enter"))
	assert.Contains(t, a.flash, "No current search pattern")
}

func TestHelpScreenAndBar(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	send(a, key("ctrl+g"))
	assert.Equal(t, CtxHelp, a.context())
	frame := plain(a)
	assert.Contains(t, frame, "opened from Editor")
	assert.Contains(t, frame, "Esc Close")
	send(a, key("ctrl+v"), key("ctrl+x"))
	assert.Equal(t, CtxEditor, a.context())

	send(a, key("alt+x"))
	assert.NotContains(t, plain(a), "^G  Help")
	send(a, key("alt+x"))
	assert.Contains(t, plain(a), "^G  Help")
}

func TestCutPasteAndCopy(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	first := "# lensd specification\n"
	send(a, key("ctrl+k"), key("ctrl+k"))
	assert.Equal(t, first+"\n", a.cutBuf, "consecutive cuts accumulate")
	send(a, key("ctrl+u"))
	assert.True(t, strings.HasPrefix(string(a.cur.Ed.Buffer().Bytes()), first+"\n"))
	// ^C copies the selection.
	send(a, key("ctrl+home"), key("shift+right"), key("shift+right"))
	_, cmd := a.Update(key("ctrl+c"))
	require.NotNil(t, cmd, "copy also goes to the system clipboard (OSC 52)")
	assert.Equal(t, "# ", a.cutBuf)
	assert.Contains(t, a.flash, "Copied 2 bytes")
	_, sel := a.cur.Ed.Selection()
	assert.False(t, sel, "copying clears the selection")

	// With no selection, ^C cancels: it copies nothing and clears the mark.
	send(a, key("ctrl+space"))
	a.Update(key("ctrl+c"))
	assert.Equal(t, "# ", a.cutBuf)
	send(a, key("right"))
	_, sel = a.cur.Ed.Selection()
	assert.False(t, sel, "^C cleared the mark")

	// ^C closes a popup (and copies nothing).
	find(a, "$Time.Elapsed),")
	send(a, key("ctrl+l"))
	require.NotNil(t, a.popup)
	a.Update(key("ctrl+c"))
	assert.Nil(t, a.popup)
	assert.Equal(t, CtxEditor, a.context())
}

// TestUndoRedoKeys: ^Z undoes, ^Y redoes (it no longer pages up).
func TestUndoRedoKeys(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	orig := string(a.cur.Ed.Buffer().Bytes())
	send(a, typeText("abc ")...)
	send(a, key("ctrl+z"))
	assert.Equal(t, orig, string(a.cur.Ed.Buffer().Bytes()))
	send(a, key("ctrl+y"))
	assert.True(t, strings.HasPrefix(string(a.cur.Ed.Buffer().Bytes()), "abc # lensd"))
	send(a, key("ctrl+y"))
	assert.Empty(t, a.flash, "disabled Redo is inert")

	// Paging is PgUp/PgDn and ^V.
	send(a, key("ctrl+v"))
	paged := a.cur.Ed.TopRow()
	send(a, key("pgup"))
	assert.NotEqual(t, paged, a.cur.Ed.TopRow())
}

// TestOutlineF2: F2 shows and focuses the outline, and hides it again.
func TestOutlineF2(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	send(a, key("f2"))
	assert.True(t, a.outlineFocus)
	assert.Equal(t, CtxOutline, a.context())
	send(a, key("f2"))
	assert.False(t, a.showOutline)
	assert.Equal(t, CtxEditor, a.context())
}

func TestExternalEditReloads(t *testing.T) {
	dir := lensdProject(t)
	a := newTestApp(t, testOptions(dir))
	send(a, key("ctrl+v"))
	cur := a.cur.Ed.Cursor()
	orig := readFile(t, dir, "lensd.spec.md")
	writeFile(t, dir, "lensd.spec.md", "AGENT\n"+orig)
	send(a, fileChangedMsg{paths: []string{"lensd.spec.md"}})
	assert.True(t, strings.HasPrefix(string(a.cur.Ed.Buffer().Bytes()), "AGENT\n"))
	assert.False(t, a.cur.Ed.Buffer().Dirty())
	assert.Equal(t, cur, a.cur.Ed.Cursor(), "the cursor keeps its offset")
	assert.Contains(t, a.flash, "Reloaded lensd.spec.md")

	// Our own save is not a change.
	send(a, typeText("x")...)
	send(a, key("ctrl+o"))
	send(a, fileChangedMsg{paths: []string{"lensd.spec.md"}})
	assert.Nil(t, a.prompt)
}

func TestExternalEditConflict(t *testing.T) {
	dir := lensdProject(t)
	orig := readFile(t, dir, "lensd.spec.md")
	for _, choice := range []string{"k", "t"} {
		writeFile(t, dir, "lensd.spec.md", orig)
		a := newTestApp(t, testOptions(dir))
		send(a, typeText("MINE ")...)
		writeFile(t, dir, "lensd.spec.md", "THEIRS\n"+orig)
		send(a, fileChangedMsg{paths: []string{"lensd.spec.md"}})
		require.NotNil(t, a.prompt)
		assert.Equal(t, CtxConflict, a.context())
		assert.Contains(t, plain(a), "changed on disk while you were editing it")
		send(a, key("d"))
		require.NotNil(t, a.popup)
		assert.Contains(t, plain(a), "+MINE # lensd specification")
		send(a, key(choice))
		assert.Nil(t, a.prompt)
		switch choice {
		case "k":
			assert.True(t, a.cur.Ed.Buffer().Dirty())
			send(a, key("ctrl+o"))
			assert.True(t, strings.HasPrefix(readFile(t, dir, "lensd.spec.md"), "MINE # lensd"), "keep mine overwrites")
		case "t":
			assert.False(t, a.cur.Ed.Buffer().Dirty())
			assert.True(t, strings.HasPrefix(string(a.cur.Ed.Buffer().Bytes()), "THEIRS\n"))
		}
	}

	// A save over an external change does not overwrite it.
	writeFile(t, dir, "lensd.spec.md", orig)
	a := newTestApp(t, testOptions(dir))
	send(a, typeText("MINE ")...)
	writeFile(t, dir, "lensd.spec.md", "THEIRS\n"+orig)
	send(a, key("ctrl+o"))
	require.NotNil(t, a.prompt)
	assert.Equal(t, CtxConflict, a.context())
	assert.True(t, strings.HasPrefix(readFile(t, dir, "lensd.spec.md"), "THEIRS\n"))
}

func TestTargets(t *testing.T) {
	dir := lensdProject(t)
	src := readFile(t, dir, "lensd.spec.md")
	for _, tc := range []struct {
		target Target
		want   string
	}{
		{Target{Name: "Time.Elapsed"}, "**Def $Time.Elapsed"},
		{Target{Number: "0.2"}, "### 0.2 Intervals"},
		{Target{Stem: "lensd", Number: "1"}, "## 1. Frames"},
		{Target{File: "lensd.spec.md", Line: 17}, "## Context"},
	} {
		opts := testOptions(dir)
		opts.Target = tc.target
		a := newTestApp(t, opts)
		assert.Equal(t, strings.Index(src, tc.want), a.cur.Ed.Cursor(), "%+v", tc.target)
	}
	for _, bad := range []Target{{Name: "Nope.Missing"}, {Number: "42"}, {File: "nope.spec.md"}} {
		opts := testOptions(dir)
		opts.Target = bad
		_, err := New(opts)
		var te *TargetError
		assert.ErrorAs(t, err, &te, "%+v", bad)
	}
}

func TestLookWithoutToken(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	send(a, key("ctrl+l"))
	assert.Nil(t, a.popup)
	assert.Contains(t, a.flash, "Nothing to look at")
	// A diagnostic alone opens a popup (the warning on line 40).
	send(a, key("ctrl+_"))
	send(a, typeText("40")...)
	send(a, key("enter"))
	d := a.ws.FileDiags("lensd.spec.md")
	require.NotEmpty(t, d)
	a.cur.Ed.SetCursor(d[0].Range.Start)
	send(a, key("ctrl+l"))
	require.NotNil(t, a.popup)
	assert.Contains(t, plain(a), d[0].Code)
	// Any other key closes it and still acts.
	send(a, key("right"))
	assert.Nil(t, a.popup)
	assert.Equal(t, d[0].Range.Start+1, a.cur.Ed.Cursor())
}

// TestExitAsksPerBuffer: with two modified files, ^X asks about each; N
// skips one, Y saves the other.
func TestExitAsksPerBuffer(t *testing.T) {
	dir := t.TempDir()
	copyDir(t, "../../testdata/multi", dir)
	a := newTestApp(t, testOptions(dir))
	require.Equal(t, "hls.spec.md", a.cur.Ed.Path())
	send(a, typeText("A")...)
	send(a, key("ctrl+q"))
	send(a, typeText("rws.spec.md")...)
	send(a, key("enter"))
	require.Equal(t, "rws.spec.md", a.cur.Ed.Path())
	send(a, typeText("B")...)

	send(a, key("ctrl+x"))
	require.NotNil(t, a.prompt)
	assert.Contains(t, a.prompt.label, "hls.spec.md")
	send(a, key("n"))
	require.NotNil(t, a.prompt)
	assert.Contains(t, a.prompt.label, "rws.spec.md")
	_, cmd := a.Update(key("y"))
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
	assert.True(t, strings.HasPrefix(readFile(t, dir, "rws.spec.md"), "B# rws"))
	assert.True(t, strings.HasPrefix(readFile(t, dir, "hls.spec.md"), "# hls"), "not saved")
}
