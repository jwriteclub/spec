// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package editor

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// WidthMethod is how display widths are measured. It matches Lip Gloss's
// canvas, which composes the frame.
const WidthMethod = ansi.GraphemeWidth

// TabCells is the display width of a tab character.
const TabCells = 4

// Codepoints used for characters that have no visible width of their own.
const (
	zeroWidthMark = "\u00b7" // middle dot: a zero-width character (BOM, ZWSP, lone combining mark)
	invalidMark   = "\ufffd" // replacement character: a byte that is not UTF-8
)

// Glyph is one grapheme cluster on screen: Len source bytes at Off, drawn
// as Text in W cells starting at column Col of its row.
type Glyph struct {
	Off  int
	Len  int
	Col  int
	W    int
	Text string
	// Space is true for a space or tab (a word-wrap opportunity).
	Space bool
	// Ghost is true for display-only text (Len 0) that is not in the
	// buffer: removed words shown where they were (SetGhosts).
	Ghost bool
	// GhostFlags are a ghost glyph's highlight classes (0: FRemoved).
	GhostFlags Flag
}

// Ghost is display-only text drawn before the buffer byte at Off.
type Ghost struct {
	Off  int
	Text string
	// Flags are its highlight classes (0: FRemoved, a removed word).
	Flags Flag
}

// End returns the offset just after the glyph's bytes.
func (g Glyph) End() int {
	return g.Off + g.Len
}

// Row is one display row: a soft-wrapped piece of a logical line.
type Row struct {
	// Line is the 0-based logical line.
	Line int
	// Sub is the index of this row within its line.
	Sub int
	// Start and End are the row's byte range. For the last row of a line,
	// End is the end of the line's content (before its terminator).
	Start, End int
	// Last is true for the last row of a line.
	Last   bool
	Glyphs []Glyph
	// Width is the number of cells the glyphs use.
	Width int
}

// glyphsOf splits src[start:end] into glyphs with their widths. Columns are
// not assigned.
func glyphsOf(src []byte, start, end int) []Glyph {
	var out []Glyph
	i := start
	for i < end {
		b := src[i:end]
		switch c := b[0]; {
		case c == '\t':
			out = append(out, Glyph{Off: i, Len: 1, W: TabCells, Text: strings.Repeat(" ", TabCells), Space: true})
			i++
			continue
		case c == ' ':
			out = append(out, Glyph{Off: i, Len: 1, W: 1, Text: " ", Space: true})
			i++
			continue
		case c < 0x20 || c == 0x7f:
			// Caret notation for other control characters.
			out = append(out, Glyph{Off: i, Len: 1, W: 2, Text: "^" + string(rune(c^0x40))})
			i++
			continue
		case c >= 0x80:
			if r, size := utf8.DecodeRune(b); r == utf8.RuneError && size <= 1 {
				out = append(out, Glyph{Off: i, Len: 1, W: 1, Text: invalidMark})
				i++
				continue
			}
		}
		cluster, w := ansi.FirstGraphemeCluster(b, WidthMethod)
		n := len(cluster)
		if n == 0 {
			n = 1
		}
		// A cluster must not swallow a following invalid byte or control
		// character; graphemes never do for valid text.
		text := string(b[:n])
		if !utf8.ValidString(text) {
			_, size := utf8.DecodeRune(b)
			n = max(size, 1)
			text = string(b[:n])
			w = WidthMethod.StringWidth(text)
		}
		if w <= 0 {
			text, w = zeroWidthMark, 1
		}
		out = append(out, Glyph{Off: i, Len: n, W: w, Text: text})
		i += n
	}
	return out
}

// WrapLine lays out the logical line with content [start, end) of src into
// rows at most width cells wide (plus one hanging space), breaking after
// spaces where it can and inside a word only when the word alone is wider
// than a row. An empty line
// is one empty row. line is the 0-based line number stored in the rows.
func WrapLine(src []byte, start, end, line, width int) []Row {
	return wrapLine(src, start, end, line, width, nil)
}

// wrapLine is WrapLine with ghost text: each ghost whose offset is in
// [start, end] is laid out as glyphs of no source length before the byte
// at its offset.
func wrapLine(src []byte, start, end, line, width int, ghosts []Ghost) []Row {
	width = max(width, 2)
	glyphs := withGhosts(glyphsOf(src, start, end), start, end, ghosts)
	for k, g := range glyphs {
		if g.Space && g.W > width {
			// A tab in a pane narrower than a tab stop: draw it as wide as
			// the row, so no row is wider than the pane.
			glyphs[k].W = width
			glyphs[k].Text = strings.Repeat(" ", width)
		}
	}
	var rows []Row
	rowStart := 0 // index into glyphs
	for rowStart < len(glyphs) {
		col := 0
		brk := -1 // index after the last space on this row
		i := rowStart
		for i < len(glyphs) {
			g := glyphs[i]
			if col+g.W > width {
				if g.Space && g.W == 1 && col == width && i > rowStart && i+1 < len(glyphs) {
					// A space may hang one cell past the wrap width (the
					// cell kept for the cursor), so no row starts with
					// the space that ended the previous one. A space that
					// ends the line does not hang: the cursor after it
					// needs a cell of its own.
					i++
					brk = i
				}
				break
			}
			col += g.W
			i++
			if g.Space {
				brk = i
			}
		}
		cut := i
		if i < len(glyphs) {
			switch {
			case glyphs[i].Space:
				// Break before an overflowing space; it starts the next row.
			case brk > rowStart:
				cut = brk
			}
			if cut == rowStart {
				// A glyph wider than the row: put it on a row of its own.
				cut = rowStart + 1
			}
		}
		rows = append(rows, makeRow(glyphs[rowStart:cut], line, len(rows)))
		rowStart = cut
	}
	if len(rows) == 0 {
		rows = append(rows, Row{Line: line, Start: start, End: end})
	}
	for k := range rows {
		if k+1 < len(rows) {
			rows[k].End = rows[k+1].Start
		} else {
			rows[k].End = end
			rows[k].Last = true
		}
	}
	return rows
}

// withGhosts inserts the ghosts in [start, end] into gs, before the glyph
// at each one's offset.
func withGhosts(gs []Glyph, start, end int, ghosts []Ghost) []Glyph {
	var in []Ghost
	for _, g := range ghosts {
		if g.Off >= start && g.Off <= end && g.Text != "" {
			in = append(in, g)
		}
	}
	if len(in) == 0 {
		return gs
	}
	out := make([]Glyph, 0, len(gs)+len(in)*8)
	k := 0
	for _, g := range gs {
		for k < len(in) && in[k].Off <= g.Off {
			out = append(out, ghostGlyphs(in[k])...)
			k++
		}
		out = append(out, g)
	}
	for ; k < len(in); k++ {
		out = append(out, ghostGlyphs(in[k])...)
	}
	return out
}

// ghostGlyphs lays out a ghost's text as glyphs at its offset.
func ghostGlyphs(g Ghost) []Glyph {
	text := []byte(strings.ReplaceAll(g.Text, "\n", " "))
	gs := glyphsOf(text, 0, len(text))
	for k := range gs {
		gs[k].Off, gs[k].Len, gs[k].Ghost, gs[k].GhostFlags = g.Off, 0, true, g.Flags
	}
	return gs
}

// makeRow assigns columns to gs and wraps them in a Row.
func makeRow(gs []Glyph, line, sub int) Row {
	out := make([]Glyph, len(gs))
	col := 0
	for k, g := range gs {
		g.Col = col
		col += g.W
		out[k] = g
	}
	return Row{Line: line, Sub: sub, Start: gs[0].Off, Glyphs: out, Width: col}
}

// OffsetAt returns the byte offset under column col of the row (hit-test).
// A column inside a wide glyph maps to that glyph. A column past the text
// maps to the end of the last row of a line, and to the last glyph of a
// wrapped row (so the cursor stays on the clicked row).
func (r Row) OffsetAt(col int) int {
	for _, g := range r.Glyphs {
		if col < g.Col+g.W {
			return g.Off
		}
	}
	if r.Last || len(r.Glyphs) == 0 {
		return r.End
	}
	return r.Glyphs[len(r.Glyphs)-1].Off
}

// ColOf returns the column of byte offset off in the row, and whether the
// row holds it. An offset inside a glyph maps to the glyph's column. The
// end of a line belongs to its last row, at the column after the text.
func (r Row) ColOf(off int) (int, bool) {
	if off < r.Start || off > r.End || (off == r.End && !r.Last) {
		return 0, false
	}
	for _, g := range r.Glyphs {
		if off < g.End() {
			return g.Col, true
		}
	}
	return r.Width, true
}

// Contains reports whether the row displays the cursor at off.
func (r Row) Contains(off int) bool {
	_, ok := r.ColOf(off)
	return ok
}
