// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package editor

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/pos"
)

// checkInvariants asserts what must hold after any operation: the cursor
// is inside the buffer, on a glyph boundary and on screen, and no row is
// wider than the text area.
func checkInvariants(t *testing.T, e *Editor, step string) {
	t.Helper()
	n := e.Buffer().Len()
	require.GreaterOrEqual(t, e.Cursor(), 0, step)
	require.LessOrEqual(t, e.Cursor(), n, step)
	switch step {
	case "left/right", "up/down", "page", "word/ends", "click":
		// Motion lands on glyph boundaries. (Typed text can join the
		// cluster after it, a base before a lone combining mark, and the
		// cursor then stays where the text ended.)
		require.Equal(t, e.snap(e.Cursor()), e.Cursor(), "%s: cursor not on a glyph boundary", step)
	}
	x, _, ok := e.CursorCell()
	require.True(t, ok, "%s: cursor off screen", step)
	w, _ := e.Size()
	require.Less(t, x, max(w, e.GutterWidth()+e.TextWidth()), "%s: cursor past the pane", step)
	for _, sr := range e.Screen() {
		if !sr.Valid {
			continue
		}
		cells := 0
		for _, r := range sr.Runs {
			cells += r.Cells
		}
		require.Equal(t, sr.Row.Width, cells, step)
		require.LessOrEqual(t, sr.Row.Width, e.TextWidth(), "%s: row %q wider than the text area", step, e.Buffer().Text(rangeOf(sr.Row)))
	}
}

func rangeOf(r Row) pos.Range { return pos.Range{Start: r.Start, End: r.End} }

func TestRandomEditsKeepInvariants(t *testing.T) {
	pieces := []string{"a", "b", " ", "#", "3", "§", cjk[:3], thumbs, family, eAcute, loneMark, zwsp, "\t", "`", "$", "x y z"}
	srcs := []string{
		sectioned,
		"",
		"x",
		"line\r\nwith crlf\r\n\r\nend",
		bom + "# 3. Bom\r\n\r\nText " + cjk + "\r\n",
		strings.Repeat(cjk+" "+thumbs+family+" ", 30) + "\n",
	}
	for si, src := range srcs {
		for _, w := range []int{8, 9, 11, 17, 40} {
			rng := rand.New(rand.NewSource(int64(si*100 + w)))
			e := New("t.spec.md", []byte(src), Options{HashToSection: true})
			e.SetSize(w, 5)
			analyze(e)
			for k := 0; k < 400; k++ {
				var step string
				switch op := rng.Intn(16); op {
				case 0, 1, 2:
					p := pieces[rng.Intn(len(pieces))]
					step = "type " + p
					e.Type(p)
				case 3:
					step = "newline"
					e.Newline()
				case 4:
					step = "backspace"
					e.Backspace()
				case 5:
					step = "delete"
					e.Delete()
				case 6:
					step = "left/right"
					if rng.Intn(2) == 0 {
						e.Left(rng.Intn(3) == 0)
					} else {
						e.Right(rng.Intn(3) == 0)
					}
				case 7:
					step = "up/down"
					if rng.Intn(2) == 0 {
						e.Up(rng.Intn(3) == 0)
					} else {
						e.Down(rng.Intn(3) == 0)
					}
				case 8:
					step = "undo"
					e.Undo()
				case 9:
					step = "redo"
					e.Redo()
				case 10:
					step = "paste"
					e.Insert("p1\r\np2\nq" + cjk[:3])
				case 11:
					step = "cut"
					e.Cut()
				case 12:
					step = "page"
					if rng.Intn(2) == 0 {
						e.PageDown(false)
					} else {
						e.PageUp(false)
					}
				case 13:
					step = "click"
					e.Click(rng.Intn(w+2)-1, rng.Intn(6))
					e.Drag(rng.Intn(w+2)-1, rng.Intn(7)-1)
					e.Release()
				case 14:
					step = "scroll"
					e.ScrollRows(rng.Intn(7) - 3)
				case 15:
					step = "word/ends"
					switch rng.Intn(4) {
					case 0:
						e.WordLeft(false)
					case 1:
						e.WordRight(false)
					case 2:
						e.Top(false)
					case 3:
						e.Bottom(false)
					}
				}
				if rng.Intn(4) == 0 {
					analyze(e)
				}
				checkInvariants(t, e, step)
			}
			// Undo everything returns the original bytes.
			for e.Undo() {
			}
			assert.Equal(t, src, text(e), "src %d width %d", si, w)
			assert.False(t, e.Buffer().Dirty())
		}
	}
}

func TestCRLFCursorNeverSplitsTerminator(t *testing.T) {
	e := newEditor("ab\r\ncd\r\n")
	for off := 0; off <= e.Buffer().Len(); off++ {
		e.SetCursor(off)
		assert.NotEqual(t, 3, e.Cursor(), "cursor between CR and LF")
	}
	e.SetCursor(4) // start of "cd"
	e.Backspace()
	assert.Equal(t, "abcd\r\n", text(e))
	e.Undo()
	e.SetCursor(2)
	e.Delete()
	assert.Equal(t, "abcd\r\n", text(e))
	e.Undo()
	e.SetCursor(2)
	e.Right(false)
	assert.Equal(t, 4, e.Cursor())
	e.Left(false)
	assert.Equal(t, 2, e.Cursor())
	// A paste with LF, CRLF and CR keeps the file's CRLF.
	e.Insert("x\ny\r\nz\rw")
	assert.Equal(t, "abx\r\ny\r\nz\r\nw\r\ncd\r\n", text(e))
}

// TestHashInNewCodeSpan: typing a code span `#3` must not convert the #,
// even though the span did not exist at the last parse.
func TestHashInNewCodeSpan(t *testing.T) {
	e := newEditor(sectioned + "\n")
	e.Bottom(false)
	for _, r := range "`#3`" {
		e.Type(string(r))
		analyze(e) // even with a current parse after every key
	}
	e.Type(" ")
	assert.True(t, strings.HasSuffix(text(e), "`#3` "), "got %q", text(e)[len(sectioned):])

	// Inside a fenced block typed faster than the parse.
	e = newEditor(sectioned + "\n")
	e.Bottom(false)
	typeString(e, "```\n#3 ")
	assert.True(t, strings.HasSuffix(text(e), "```\n#3 "), "got %q", text(e)[len(sectioned):])

	// A link fragment is not a section reference.
	e = newEditor(sectioned + "\n")
	e.Bottom(false)
	typeString(e, "[see](#3) and #3 ")
	assert.True(t, strings.HasSuffix(text(e), "[see](#3) and §3 "), "got %q", text(e)[len(sectioned):])
}

func TestHashUndoAndPR(t *testing.T) {
	e := newEditor(sectioned + "\n")
	e.Bottom(false)
	typeString(e, "See PR #3, ok")
	assert.True(t, strings.HasSuffix(text(e), "See PR §3, ok"), "section 3 exists: converts")
	e.Undo() // " ok", typed after the conversion
	e.Undo() // the conversion
	assert.True(t, strings.HasSuffix(text(e), "See PR #3,"), "got %q", text(e)[len(sectioned):])
	e.Redo()
	assert.True(t, strings.HasSuffix(text(e), "See PR §3,"))
}

func TestTinyWidthsDoNotOverflow(t *testing.T) {
	src := "a\tb\t\tc " + cjk + thumbs + family + " " + strings.Repeat("\t", 5) + "\x01\x02\n"
	for w := 0; w <= 12; w++ {
		e := New("t.spec.md", []byte(src), Options{})
		e.SetSize(w, 3)
		for off := 0; off <= len(src); off++ {
			e.SetCursor(off)
			checkInvariants(t, e, "width "+string(rune('0'+w)))
		}
	}
}

// TestBOMStaysFirst: the cursor never goes before a byte order mark, so
// typing, cutting or backspacing at the top of the file keeps the BOM as
// the file's first bytes.
func TestBOMStaysFirst(t *testing.T) {
	src := bom + "# 1. Bom\r\n\r\nText\r\n"
	e := newEditor(src)
	e.SetCursor(0)
	assert.Equal(t, len(bom), e.Cursor())
	e.Top(false)
	e.LineStart(false)
	assert.Equal(t, len(bom), e.Cursor())
	e.Type("x")
	assert.Equal(t, bom+"x# 1. Bom\r\n\r\nText\r\n", text(e))
	e.Backspace()
	e.Backspace()
	assert.Equal(t, src, text(e))
	e.Left(false)
	assert.Equal(t, len(bom), e.Cursor())
	g := e.GutterWidth()
	e.Click(g, 0)
	assert.Equal(t, len(bom), e.Cursor())
	e.Click(0, 0)
	assert.Equal(t, len(bom), e.Cursor())
	assert.Equal(t, "# 1. Bom\r\n", e.Copy())
	assert.Equal(t, "# 1. Bom\r\n", e.Cut())
	assert.Equal(t, bom+"\r\nText\r\n", text(e))
	e.Undo()
	e.Down(true)
	e.Up(true)
	e.Up(true)
	e.Backspace()
	assert.True(t, strings.HasPrefix(text(e), bom), "got %q", text(e))
}

// TestCursorAfterHangingSpace: a line that fills a row and ends with a
// space keeps the cursor at its end inside the pane.
func TestCursorAfterHangingSpace(t *testing.T) {
	for _, w := range []int{12, 20, 33} {
		e := New("t.spec.md", nil, Options{})
		e.SetSize(w, 4)
		line := strings.Repeat("a", e.wrapWidth()) + " "
		e.Type(line)
		x, _, ok := e.CursorCell()
		require.True(t, ok)
		assert.Less(t, x, w, "width %d: cursor at the end of %q", w, line)
		e.Type("b")
		x, _, ok = e.CursorCell()
		require.True(t, ok)
		assert.Less(t, x, w, "width %d", w)
	}
}
