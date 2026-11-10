// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package editor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/mdspec"
)

// analyze parses the editor's buffer and installs the analysis.
func analyze(e *Editor) {
	f := mdspec.ParseFile(e.Path(), e.Buffer().Bytes())
	s := mdspec.Build([]*mdspec.File{f})
	e.SetAnalysis(Analysis{File: f, Spec: s, Diags: s.Diagnostics}, e.Buffer().Version())
}

func newEditor(src string) *Editor {
	e := New("t.spec.md", []byte(src), Options{HashToSection: true})
	e.SetSize(60, 10)
	analyze(e)
	return e
}

func typeString(e *Editor, s string) {
	for _, r := range s {
		if r == '\n' {
			e.Newline()
			continue
		}
		e.Type(string(r))
	}
}

func text(e *Editor) string { return string(e.Buffer().Bytes()) }

const sectioned = "# Title\n\n## 3. Three\n\nBody.\n\n## 5.2a. Five\n\nMore.\n"

func TestTypeAndUndo(t *testing.T) {
	e := newEditor("ab\n")
	e.SetCursor(1)
	typeString(e, "xyz")
	assert.Equal(t, "axyzb\n", text(e))
	assert.Equal(t, 4, e.Cursor())
	require.True(t, e.Undo())
	assert.Equal(t, "ab\n", text(e))
	assert.Equal(t, 1, e.Cursor())
	require.True(t, e.Redo())
	assert.Equal(t, "axyzb\n", text(e))
	assert.Equal(t, 4, e.Cursor())

	e.Backspace()
	e.Backspace()
	assert.Equal(t, "axb\n", text(e))
	e.Undo()
	assert.Equal(t, "axyzb\n", text(e))

	e.SetCursor(0)
	e.Delete()
	assert.Equal(t, "xyzb\n", text(e))
	e.LineEnd(false)
	e.Delete() // joins with the (empty) next line
	assert.Equal(t, "xyzb", text(e))
}

func TestMultibyteMotionAndDelete(t *testing.T) {
	e := newEditor("a" + eAcute + cjk + "\n")
	e.SetCursor(0)
	e.Right(false)
	assert.Equal(t, 1, e.Cursor())
	e.Right(false)
	assert.Equal(t, 1+len(eAcute), e.Cursor(), "a combining sequence is one step")
	e.Delete()
	assert.Equal(t, "a"+eAcute+cjk[3:]+"\n", text(e))
	e.Backspace()
	assert.Equal(t, "a"+cjk[3:]+"\n", text(e))
	// A cursor set inside a glyph snaps to its start.
	e.SetCursor(2)
	assert.Equal(t, 1, e.Cursor())
}

func TestSelection(t *testing.T) {
	e := newEditor("one two three\nfour five\n")
	e.SetCursor(4)
	e.Right(true)
	e.Right(true)
	e.Right(true)
	r, ok := e.Selection()
	require.True(t, ok)
	assert.Equal(t, rng(4, 7), r)
	assert.Equal(t, "two", e.Buffer().Text(r))

	// Unshifted motion collapses a shift selection.
	e.Left(false)
	_, ok = e.Selection()
	assert.False(t, ok)
	assert.Equal(t, 4, e.Cursor())

	// Selecting down across lines, then typing replaces the selection.
	e.Down(true)
	r, _ = e.Selection()
	assert.Equal(t, "two three\nfour", e.Buffer().Text(r))
	e.Type("X")
	e.Type("Y")
	assert.Equal(t, "one XY five\n", text(e))
	e.Undo() // typing over a selection is one undo step
	assert.Equal(t, "one two three\nfour five\n", text(e))

	// The sticky mark (^^) survives plain motion.
	e.SetCursor(0)
	require.True(t, e.ToggleMark())
	e.WordRight(false)
	e.WordRight(false)
	r, ok = e.Selection()
	require.True(t, ok)
	assert.Equal(t, "one two ", e.Buffer().Text(r))
	assert.Equal(t, "one two ", e.Copy())
	_, ok = e.Selection()
	assert.False(t, ok, "copy clears the mark")

	// Cut without a selection takes the whole line.
	e.SetCursor(16)
	assert.Equal(t, "four five\n", e.Cut())
	assert.Equal(t, "one two three\n", text(e))
	e.SetCursor(0)
	e.Insert("four five\n")
	assert.Equal(t, "four five\none two three\n", text(e))

	// A drag selects from the click to the release.
	e.SetCursor(0)
	g := e.GutterWidth()
	e.Click(g+5, 0)
	e.Drag(g+9, 0)
	e.Release()
	r, ok = e.Selection()
	require.True(t, ok)
	assert.Equal(t, "five", e.Buffer().Text(r))
}

func TestVerticalMotionKeepsGoal(t *testing.T) {
	e := newEditor("abcdefgh\nab\nabcdefgh\n")
	e.SetCursor(6)
	e.Down(false)
	assert.Equal(t, 11, e.Cursor(), "clamped to the end of the short line")
	e.Down(false)
	assert.Equal(t, 18, e.Cursor(), "back at column 6")
	e.Up(false)
	e.Up(false)
	assert.Equal(t, 6, e.Cursor())
}

func TestWrappedMotionAndScroll(t *testing.T) {
	long := strings.Repeat("word ", 40)
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, long)
	}
	e := newEditor(strings.Join(lines, "\n"))
	assert.Greater(t, len(e.Rows(0)), 1)
	start := e.rowOf(e.Cursor())
	e.Down(false)
	assert.Equal(t, RowPos{0, start.Sub + 1}, e.rowOf(e.Cursor()), "down moves one display row")
	e.PageDown(false)
	_, y, ok := e.CursorCell()
	assert.True(t, ok)
	assert.Less(t, y, 10)
	e.Bottom(false)
	_, _, ok = e.CursorCell()
	assert.True(t, ok, "the cursor stays visible")
	e.Top(false)
	assert.Equal(t, RowPos{}, e.TopRow())

	// Scrolling the view drags the cursor along when it would leave.
	e.ScrollRows(20)
	_, y, ok = e.CursorCell()
	assert.True(t, ok)
	assert.Equal(t, 0, y)
}

func TestHashToSection(t *testing.T) {
	e := newEditor(sectioned)
	e.Bottom(false)
	typeString(e, "See #3 and #5.2a.\n")
	assert.Equal(t, sectioned+"See §3 and §5.2a.\n", text(e))
	assert.Contains(t, e.TakeFlash(), "§5.2a")

	// One undo restores the '#', leaving the typed text.
	e = newEditor(sectioned)
	e.Bottom(false)
	typeString(e, "See #3 ")
	assert.Equal(t, sectioned+"See §3 ", text(e))
	require.True(t, e.Undo())
	assert.Equal(t, sectioned+"See #3 ", text(e))
	assert.Equal(t, len(sectioned+"See #3 "), e.Cursor())
	require.True(t, e.Redo())
	assert.Equal(t, sectioned+"See §3 ", text(e))
}

func TestHashToSectionNegatives(t *testing.T) {
	cases := map[string]string{
		"no such section":      "PR #4 ",
		"no such subsection":   "see #3.1 ",
		"after a word char":    "PR#3 ",
		"html entity":          "&#3 ",
		"double hash":          "##3 ",
		"still typing":         "#3",
		"token continues":      "#3.",
		"escaped":              "\\#3 ",
		"letter suffix absent": "#5.2b ",
	}
	for name, typed := range cases {
		e := newEditor(sectioned)
		e.Bottom(false)
		typeString(e, typed)
		assert.Equal(t, sectioned+typed, text(e), name)
	}

	// "PR #3" converts when section 3 exists: the '#' follows a space.
	e := newEditor(sectioned)
	e.Bottom(false)
	typeString(e, "PR #3 ")
	assert.Equal(t, sectioned+"PR §3 ", text(e))

	// Inside a code span, never.
	e = newEditor(sectioned + "`x #3 y`\n")
	e.SetCursor(len(sectioned) + 5)
	e.Type(" ")
	assert.Equal(t, sectioned+"`x #3  y`\n", text(e))

	// Turned off by the setting.
	e = newEditor(sectioned)
	e.SetOptions(Options{HashToSection: false})
	e.Bottom(false)
	typeString(e, "#3 ")
	assert.Equal(t, sectioned+"#3 ", text(e))

	// A paste never converts.
	e = newEditor(sectioned)
	e.Bottom(false)
	e.Insert("#3 ")
	assert.Equal(t, sectioned+"#3 ", text(e))
}

func TestHighlightSpans(t *testing.T) {
	src := "# Spec\n\n## 1. One\n\n**$A.Rule (rule).** Uses $Bee and $Gone and §1 and §9.\n\n**Def $Bee (bee).** A `code $A.Rule` span.\n"
	e := newEditor(src)
	flagAt := func(sub string) Flag {
		i := strings.Index(src, sub)
		require.GreaterOrEqual(t, i, 0, sub)
		var f Flag
		for _, s := range e.Spans() {
			if i >= s.Start && i < s.End {
				f |= s.Flags
			}
		}
		return f
	}
	assert.NotZero(t, flagAt("$A.Rule (")&FInvSite)
	assert.NotZero(t, flagAt("$A.Rule (")&FLabel)
	assert.NotZero(t, flagAt("$Bee and")&FDef)
	assert.NotZero(t, flagAt("$Gone")&FBadRef)
	assert.NotZero(t, flagAt("$Gone")&FDiagError)
	assert.NotZero(t, flagAt("§1 ")&FSection)
	assert.NotZero(t, flagAt("§9")&FBadRef)
	assert.NotZero(t, flagAt("$Bee (bee)")&FDefSite)
	assert.NotZero(t, flagAt("code $A")&FCode)
	assert.Zero(t, flagAt("code $A")&FInv, "no token inside code")
	assert.NotZero(t, flagAt("## 1. One")&FSection)
	assert.NotZero(t, flagAt("# Spec")&FSection)

	// The gutter marks the line with the dangling reference.
	rows := e.Screen()
	var marked []int
	for _, r := range rows {
		if r.Marker != "" {
			marked = append(marked, r.LineNo)
		}
	}
	assert.Equal(t, []int{5}, marked)

	// Typing before a token shifts its highlight until the next parse.
	before := flagAt("$Gone")
	e.SetCursor(strings.Index(src, "Uses"))
	e.Type("xx")
	assert.True(t, e.Stale())
	i := strings.Index(text(e), "$Gone")
	var f Flag
	for _, s := range e.Spans() {
		if i >= s.Start && i < s.End {
			f |= s.Flags
		}
	}
	assert.Equal(t, before, f)
}

func TestFind(t *testing.T) {
	e := newEditor("Alpha beta alpha\n")
	r, ok := e.Find("alpha", 1)
	require.True(t, ok)
	assert.Equal(t, rng(11, 16), r)
	r, ok = e.Find("alpha", 12)
	require.True(t, ok)
	assert.Equal(t, rng(0, 5), r, "wraps around, case-insensitive")
	r, ok = e.Find("Alpha", 1)
	require.True(t, ok)
	assert.Equal(t, rng(0, 5), r, "an uppercase query matches case")
	_, ok = e.Find("gamma", 0)
	assert.False(t, ok)
}

func TestCRLFNewline(t *testing.T) {
	e := newEditor("a\r\nb\r\n")
	e.SetCursor(1)
	e.Newline()
	assert.Equal(t, "a\r\n\r\nb\r\n", text(e))
	// The cursor never rests between '\r' and '\n'.
	e.SetCursor(2)
	assert.Equal(t, 1, e.Cursor())
}

// ReplaceAll is one undo step and places the cursor where asked.
func TestReplaceAll(t *testing.T) {
	e := newEditor("one\ntwo   \n\n\nthree\n")
	typeString(e, "x")
	e.ReplaceAll("xone two\n\nthree\n", 5)
	assert.Equal(t, "xone two\n\nthree\n", text(e))
	assert.Equal(t, 5, e.Cursor())
	require.True(t, e.Undo())
	assert.Equal(t, "xone\ntwo   \n\n\nthree\n", text(e), "one undo step, separate from the typing")
	require.True(t, e.Undo())
	assert.Equal(t, "one\ntwo   \n\n\nthree\n", text(e))
	require.True(t, e.Redo())
	require.True(t, e.Redo())
	assert.Equal(t, "xone two\n\nthree\n", text(e))
}
