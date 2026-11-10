// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package pos converts between byte offsets in a source file and the
// line/column forms used by humans (1-based lines, 1-based byte columns) and
// by the Language Server Protocol (0-based lines, 0-based UTF-16 code units).
//
// Every range in logedit is a half-open [Start, End) byte range into a file's
// raw source, including any leading byte order mark. Lines are terminated by
// '\n'; a '\r' before the '\n' belongs to the line's terminator, not to its
// content. A lone '\r' is not a line break (goldmark does not treat it as one
// either).
package pos

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// Range is a half-open [Start, End) byte range into a file's raw source.
type Range struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Len returns the number of bytes in r.
func (r Range) Len() int {
	return r.End - r.Start
}

// Empty reports whether r covers no bytes.
func (r Range) Empty() bool {
	return r.End <= r.Start
}

// Contains reports whether offset off lies inside r.
func (r Range) Contains(off int) bool {
	return off >= r.Start && off < r.End
}

// ContainsRange reports whether o lies entirely inside r.
func (r Range) ContainsRange(o Range) bool {
	return o.Start >= r.Start && o.End <= r.End
}

// Overlaps reports whether r and o share at least one byte.
func (r Range) Overlaps(o Range) bool {
	return r.Start < o.End && o.Start < r.End
}

// Valid reports whether r is a well-formed range inside a source of n bytes.
func (r Range) Valid(n int) bool {
	return r.Start >= 0 && r.Start <= r.End && r.End <= n
}

// String formats r as "start-end".
func (r Range) String() string {
	return fmt.Sprintf("%d-%d", r.Start, r.End)
}

// Position is a human position: 1-based line and 1-based byte column.
type Position struct {
	Line int `json:"line"`
	Col  int `json:"col"`
}

// String formats p as "line:col".
func (p Position) String() string {
	return fmt.Sprintf("%d:%d", p.Line, p.Col)
}

// LSPPosition is a Language Server Protocol position: 0-based line and
// 0-based character offset in UTF-16 code units.
type LSPPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Index answers position queries for one immutable source. It is safe for
// concurrent use once built.
type Index struct {
	src    []byte
	starts []int // byte offset of the first byte of each line
}

// NewIndex builds an index over src. The caller must not modify src
// afterwards.
func NewIndex(src []byte) *Index {
	starts := make([]int, 1, len(src)/40+1)
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return &Index{src: src, starts: starts}
}

// Len returns the length of the indexed source in bytes.
func (x *Index) Len() int {
	return len(x.src)
}

// HasBOM reports whether the source starts with a UTF-8 byte order mark.
func (x *Index) HasBOM() bool {
	return len(x.src) >= 3 && x.src[0] == 0xEF && x.src[1] == 0xBB && x.src[2] == 0xBF
}

// LineCount returns the number of lines. A source ending in '\n' has an empty
// final line, so "a\n" has two lines.
func (x *Index) LineCount() int {
	return len(x.starts)
}

func (x *Index) clamp(off int) int {
	if off < 0 {
		return 0
	}
	if off > len(x.src) {
		return len(x.src)
	}
	return off
}

// lineOf returns the 0-based line containing off (already clamped).
func (x *Index) lineOf(off int) int {
	return sort.Search(len(x.starts), func(i int) bool { return x.starts[i] > off }) - 1
}

// Line returns the 1-based line containing offset off. Offsets outside the
// source are clamped.
func (x *Index) Line(off int) int {
	return x.lineOf(x.clamp(off)) + 1
}

// LineStart returns the byte offset of the first byte of 1-based line. Lines
// outside the source are clamped.
func (x *Index) LineStart(line int) int {
	i := line - 1
	if i < 0 {
		return 0
	}
	if i >= len(x.starts) {
		return len(x.src)
	}
	return x.starts[i]
}

// LineRange returns the content of 1-based line, excluding its "\n" or "\r\n"
// terminator.
func (x *Index) LineRange(line int) Range {
	i := line - 1
	if i < 0 || i >= len(x.starts) {
		n := len(x.src)
		return Range{Start: n, End: n}
	}
	start := x.starts[i]
	end := len(x.src)
	if i+1 < len(x.starts) {
		end = x.starts[i+1] - 1 // drop '\n'
		if end > start && x.src[end-1] == '\r' {
			end--
		}
	}
	return Range{Start: start, End: end}
}

// Position converts a byte offset to a 1-based line and 1-based byte column.
// Offsets outside the source are clamped.
func (x *Index) Position(off int) Position {
	off = x.clamp(off)
	l := x.lineOf(off)
	return Position{Line: l + 1, Col: off - x.starts[l] + 1}
}

// Offset converts a 1-based line and byte column to a byte offset. It reports
// false when the position is outside the source; a column one past the end
// of the line's content (the terminator) is valid.
func (x *Index) Offset(p Position) (int, bool) {
	if p.Line < 1 || p.Line > len(x.starts) || p.Col < 1 {
		return 0, false
	}
	start := x.starts[p.Line-1]
	limit := len(x.src)
	if p.Line < len(x.starts) {
		limit = x.starts[p.Line] - 1
	}
	off := start + p.Col - 1
	if off > limit {
		return 0, false
	}
	return off, true
}

// utf16Units returns how many UTF-16 code units encode b. An invalid byte
// counts as one unit (it decodes to U+FFFD).
func utf16Units(b []byte) int {
	n := 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r >= 0x10000 && size == 4 {
			n += 2
		} else {
			n++
		}
		b = b[size:]
	}
	return n
}

// LSP converts a byte offset to an LSP position. An offset inside a
// multi-byte character maps to the start of that character. Offsets outside
// the source are clamped.
func (x *Index) LSP(off int) LSPPosition {
	off = x.clamp(off)
	l := x.lineOf(off)
	start := x.starts[l]
	// Back up to a rune boundary, but never before the line start.
	for off > start && off < len(x.src) && !utf8.RuneStart(x.src[off]) {
		off--
	}
	return LSPPosition{Line: l, Character: utf16Units(x.src[start:off])}
}

// FromLSP converts an LSP position to a byte offset. A line past the end maps
// to the end of the source; a character past the end of a line maps to the end
// of that line's content; a character that falls between the two halves of a
// surrogate pair maps to the start of that character.
func (x *Index) FromLSP(p LSPPosition) int {
	if p.Line < 0 {
		return 0
	}
	if p.Line >= len(x.starts) {
		return len(x.src)
	}
	lr := x.LineRange(p.Line + 1)
	off := lr.Start
	units := 0
	for off < lr.End {
		r, size := utf8.DecodeRune(x.src[off:lr.End])
		w := 1
		if r >= 0x10000 && size == 4 {
			w = 2
		}
		if units+w > p.Character {
			return off
		}
		units += w
		off += size
	}
	return lr.End
}

// LSPRange converts a byte range to a pair of LSP positions.
func (x *Index) LSPRange(r Range) (LSPPosition, LSPPosition) {
	return x.LSP(r.Start), x.LSP(r.End)
}

// LSPUTF8 converts a byte offset to an LSP position whose character counts
// UTF-8 code units (bytes), for clients that negotiate the "utf-8" position
// encoding. An offset inside a multi-byte character maps to the start of that
// character. Offsets outside the source are clamped.
func (x *Index) LSPUTF8(off int) LSPPosition {
	off = x.clamp(off)
	l := x.lineOf(off)
	start := x.starts[l]
	for off > start && off < len(x.src) && !utf8.RuneStart(x.src[off]) {
		off--
	}
	return LSPPosition{Line: l, Character: off - start}
}

// FromLSPUTF8 converts an LSP position counted in UTF-8 code units to a byte
// offset. A line past the end maps to the end of the source; a character past
// the end of a line maps to the end of that line's content; a character inside
// a multi-byte character maps to the start of that character.
func (x *Index) FromLSPUTF8(p LSPPosition) int {
	if p.Line < 0 {
		return 0
	}
	if p.Line >= len(x.starts) {
		return len(x.src)
	}
	lr := x.LineRange(p.Line + 1)
	if p.Character < 0 {
		return lr.Start
	}
	off := lr.Start + p.Character
	if off >= lr.End {
		return lr.End
	}
	for off > lr.Start && !utf8.RuneStart(x.src[off]) {
		off--
	}
	return off
}
