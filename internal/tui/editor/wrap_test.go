// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package editor

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Non-ASCII test text, as codepoint escapes (no invisible characters in
// source).
const (
	cjk       = "\u4e2d\u6587\u5b57\u7b26" // four wide ideographs
	thumbs    = "\U0001f44d\U0001f3fd"     // thumbs up with a skin tone modifier: one cluster
	family    = "\U0001f468\u200d\U0001f469\u200d\U0001f467"
	eAcute    = "e\u0301" // e + combining acute: one cluster
	loneMark  = "\u0301"  // a combining mark with no base
	zwsp      = "\u200b"
	bom       = "\ufeff"
	invalid   = "\xff\xfe"
	tabbed    = "a\tb"
	controlCh = "x\x01y"
)

func wrapString(s string, width int) []Row {
	return WrapLine([]byte(s), 0, len(s), 0, width)
}

func rowTexts(src string, rows []Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, src[r.Start:r.End])
	}
	return out
}

func TestWrapWords(t *testing.T) {
	s := "the quick brown fox jumps"
	rows := wrapString(s, 10)
	assert.Equal(t, []string{"the quick ", "brown fox ", "jumps"}, rowTexts(s, rows))
	for _, r := range rows {
		assert.LessOrEqual(t, r.Width, 10)
	}
	// A space that ends a full row hangs past it rather than starting the
	// next row.
	s = "abcde fghij klm"
	rows = wrapString(s, 5)
	assert.Equal(t, []string{"abcde ", "fghij ", "klm"}, rowTexts(s, rows))
	assert.Equal(t, 6, rows[0].Width)
	assert.True(t, rows[2].Last)
	assert.False(t, rows[0].Last)

	// A word longer than the row breaks inside the word.
	s = "abcdefghijklmnop qr"
	assert.Equal(t, []string{"abcdefghij", "klmnop qr"}, rowTexts(s, wrapString(s, 10)))

	// An empty line is one empty row.
	rows = wrapString("", 10)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].Last)
	assert.Equal(t, 0, rows[0].Width)
}

func TestWrapWideAndClusters(t *testing.T) {
	// Wide ideographs take two cells and never split across rows.
	rows := wrapString(cjk+cjk, 5)
	for _, r := range rows {
		assert.LessOrEqual(t, r.Width, 5)
		for _, g := range r.Glyphs {
			assert.Equal(t, 2, g.W)
			assert.Equal(t, 3, g.Len)
		}
	}
	assert.Len(t, rows, 4) // 8 ideographs, 2 per row

	// An emoji with a modifier and a ZWJ family are each one glyph.
	for _, s := range []string{thumbs, family} {
		rows = wrapString("a"+s+"b", 20)
		require.Len(t, rows, 1)
		gs := rows[0].Glyphs
		require.Len(t, gs, 3, s)
		assert.Equal(t, len(s), gs[1].Len)
		assert.Equal(t, 2, gs[1].W)
	}

	// A combining mark joins its base.
	rows = wrapString("x"+eAcute+"y", 20)
	gs := rows[0].Glyphs
	require.Len(t, gs, 3)
	assert.Equal(t, 1, gs[1].W)
	assert.Equal(t, len(eAcute), gs[1].Len)

	// Zero-width and invalid input still take a visible cell each.
	// (A lone combining mark is tested at the start of the line: after
	// another character it would join that character's cluster.)
	for _, s := range []string{loneMark, zwsp, bom} {
		rows = wrapString(s+"b", 20)
		require.Len(t, rows[0].Glyphs, 2, "%q", s)
		assert.Equal(t, 1, rows[0].Glyphs[0].W, "%q", s)
		assert.Equal(t, zeroWidthMark, rows[0].Glyphs[0].Text)
	}
	rows = wrapString(invalid, 20)
	require.Len(t, rows[0].Glyphs, 2)
	assert.Equal(t, invalidMark, rows[0].Glyphs[0].Text)

	rows = wrapString(tabbed, 20)
	assert.Equal(t, TabCells+2, rows[0].Width)
	rows = wrapString(controlCh, 20)
	assert.Equal(t, "^A", rows[0].Glyphs[1].Text)
}

// checkRoundTrip checks every cell of every row: cell → offset → cell lands
// on the glyph covering the cell, and every glyph start maps back to
// itself.
func checkRoundTrip(t *testing.T, name string, rows []Row, width int) {
	t.Helper()
	for _, r := range rows {
		for col := 0; col < width; col++ {
			off := r.OffsetAt(col)
			// The offset is displayed on this row (or, past the end of a
			// wrapped row, at its last glyph).
			c, ok := r.ColOf(off)
			if !assert.True(t, ok, "%s line %d sub %d col %d: offset %d not on the row", name, r.Line, r.Sub, col, off) {
				return
			}
			if col < r.Width {
				// The cell lies inside the glyph that starts at c.
				var g *Glyph
				for k := range r.Glyphs {
					if r.Glyphs[k].Off == off {
						g = &r.Glyphs[k]
					}
				}
				if !assert.NotNil(t, g, "%s: offset %d is not a glyph start", name, off) {
					return
				}
				assert.Equal(t, g.Col, c)
				assert.True(t, col >= g.Col && col < g.Col+g.W, "%s: col %d outside glyph at %d+%d", name, col, g.Col, g.W)
			}
			// And the offset maps back to the same cell's glyph.
			assert.Equal(t, off, r.OffsetAt(c), "%s line %d col %d", name, r.Line, col)
		}
		for _, g := range r.Glyphs {
			c, ok := r.ColOf(g.Off)
			assert.True(t, ok)
			assert.Equal(t, g.Col, c)
			assert.Equal(t, g.Off, r.OffsetAt(g.Col))
		}
	}
}

func TestRoundTripFixtures(t *testing.T) {
	fixtures := map[string]string{
		"synthetic": strings.Join([]string{
			"plain ascii words that wrap around the edge of a narrow pane",
			cjk + " mixed " + cjk + cjk + " text",
			thumbs + family + " emoji " + thumbs + thumbs + thumbs,
			eAcute + eAcute + " combining " + loneMark + zwsp + bom,
			tabbed + controlCh + invalid,
			"",
		}, "\n"),
	}
	for _, p := range []string{"../../../testdata/lensd/lensd.spec.md", "../../../testdata/edge/encoding-unicode.spec.md", "../../../testdata/edge/encoding-crlf.spec.md"} {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		fixtures[p] = string(b)
	}
	for name, src := range fixtures {
		for _, width := range []int{7, 20, 64, 99} {
			e := New("x.spec.md", []byte(src), Options{})
			e.SetSize(width+e.GutterWidth()+1, 30)
			require.Equal(t, width, e.wrapWidth())
			for line := 0; line < e.lineCount(); line++ {
				rows := e.Rows(line)
				lr := e.lineRange(line)
				assert.Equal(t, lr.Start, rows[0].Start)
				assert.Equal(t, lr.End, rows[len(rows)-1].End)
				for k := 1; k < len(rows); k++ {
					assert.Equal(t, rows[k-1].End, rows[k].Start, "rows tile the line")
				}
				checkRoundTrip(t, name, rows, width+1)
			}
		}
	}
}

// TestEditorCellRoundTrip runs the round trip through the editor's pane
// coordinates (gutter included) on every visible cell of the lensd fixture.
func TestEditorCellRoundTrip(t *testing.T) {
	b, err := os.ReadFile("../../../testdata/lensd/lensd.spec.md")
	require.NoError(t, err)
	e := New("lensd.spec.md", b, Options{})
	e.SetSize(60, 25)
	for page := 0; page < 40; page++ {
		for y := 0; y < 25; y++ {
			for x := 0; x < 60; x++ {
				off, ok := e.OffsetAtCell(x, y)
				if !ok {
					continue
				}
				cx, cy, vis := e.CellOf(off)
				require.True(t, vis, "offset %d from (%d,%d) is off screen", off, x, y)
				assert.Equal(t, y, cy, "(%d,%d) -> %d -> (%d,%d)", x, y, off, cx, cy)
				back, _ := e.OffsetAtCell(cx, cy)
				assert.Equal(t, off, back)
			}
		}
		e.ScrollRows(24)
	}
}
