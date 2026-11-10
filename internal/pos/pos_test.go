// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package pos

import (
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/jwriteclub/spec/internal/golden"
)

func TestRange(t *testing.T) {
	r := Range{Start: 2, End: 5}
	assert.Equal(t, 3, r.Len())
	assert.False(t, r.Empty())
	assert.True(t, r.Contains(2))
	assert.True(t, r.Contains(4))
	assert.False(t, r.Contains(5))
	assert.True(t, r.Overlaps(Range{Start: 4, End: 9}))
	assert.False(t, r.Overlaps(Range{Start: 5, End: 9}))
	assert.True(t, r.ContainsRange(Range{Start: 2, End: 5}))
	assert.False(t, r.ContainsRange(Range{Start: 1, End: 5}))
	assert.True(t, r.Valid(5))
	assert.False(t, r.Valid(4))
	assert.False(t, Range{Start: 3, End: 2}.Valid(10))
	assert.True(t, Range{Start: 3, End: 3}.Empty())
	assert.Equal(t, "2-5", r.String())
}

func TestLinesLF(t *testing.T) {
	src := []byte("ab\ncd\n\nef")
	x := NewIndex(src)
	assert.Equal(t, 4, x.LineCount())
	assert.Equal(t, Position{Line: 1, Col: 1}, x.Position(0))
	assert.Equal(t, Position{Line: 1, Col: 3}, x.Position(2)) // the '\n'
	assert.Equal(t, Position{Line: 2, Col: 1}, x.Position(3))
	assert.Equal(t, Position{Line: 3, Col: 1}, x.Position(6))
	assert.Equal(t, Position{Line: 4, Col: 3}, x.Position(9)) // EOF
	assert.Equal(t, Position{Line: 4, Col: 3}, x.Position(99))
	assert.Equal(t, Position{Line: 1, Col: 1}, x.Position(-4))
	assert.Equal(t, Range{Start: 3, End: 5}, x.LineRange(2))
	assert.Equal(t, Range{Start: 6, End: 6}, x.LineRange(3))
	assert.Equal(t, Range{Start: 7, End: 9}, x.LineRange(4))
	assert.Equal(t, Range{Start: 9, End: 9}, x.LineRange(5))
	assert.Equal(t, 2, x.Line(4))
	assert.Equal(t, 7, x.LineStart(4))
	assert.Equal(t, 0, x.LineStart(0))
	assert.Equal(t, 9, x.LineStart(9))
}

func TestLinesCRLF(t *testing.T) {
	src := []byte("ab\r\ncd\r\n")
	x := NewIndex(src)
	assert.Equal(t, 3, x.LineCount())
	assert.Equal(t, Range{Start: 0, End: 2}, x.LineRange(1))
	assert.Equal(t, Range{Start: 4, End: 6}, x.LineRange(2))
	assert.Equal(t, Range{Start: 8, End: 8}, x.LineRange(3))
	assert.Equal(t, Position{Line: 2, Col: 2}, x.Position(5))
	assert.Equal(t, LSPPosition{Line: 1, Character: 1}, x.LSP(5))
	assert.Equal(t, 5, x.FromLSP(LSPPosition{Line: 1, Character: 1}))
	// Past the end of the line's content lands before the CR.
	assert.Equal(t, 6, x.FromLSP(LSPPosition{Line: 1, Character: 40}))
	// A lone CR is not a line break.
	y := NewIndex([]byte("a\rb\nc"))
	assert.Equal(t, 2, y.LineCount())
	assert.Equal(t, Position{Line: 1, Col: 3}, y.Position(2))
}

func TestOffset(t *testing.T) {
	x := NewIndex([]byte("ab\ncd"))
	off, ok := x.Offset(Position{Line: 2, Col: 2})
	require.True(t, ok)
	assert.Equal(t, 4, off)
	off, ok = x.Offset(Position{Line: 1, Col: 3}) // the terminator
	require.True(t, ok)
	assert.Equal(t, 2, off)
	_, ok = x.Offset(Position{Line: 1, Col: 4})
	assert.False(t, ok)
	_, ok = x.Offset(Position{Line: 3, Col: 1})
	assert.False(t, ok)
	_, ok = x.Offset(Position{Line: 0, Col: 1})
	assert.False(t, ok)
	off, ok = x.Offset(Position{Line: 2, Col: 3}) // EOF
	require.True(t, ok)
	assert.Equal(t, 5, off)
}

func TestUTF16(t *testing.T) {
	// "é" is 2 bytes / 1 unit, "€" 3 bytes / 1 unit, "😀" 4 bytes / 2 units.
	src := []byte("é€😀$Ab.Cd\nx")
	x := NewIndex(src)
	dollar := len("é€😀")
	assert.Equal(t, LSPPosition{Line: 0, Character: 4}, x.LSP(dollar))
	assert.Equal(t, Position{Line: 1, Col: dollar + 1}, x.Position(dollar))
	assert.Equal(t, dollar, x.FromLSP(LSPPosition{Line: 0, Character: 4}))
	// Inside the surrogate pair maps to the start of the emoji.
	emoji := len("é€")
	assert.Equal(t, emoji, x.FromLSP(LSPPosition{Line: 0, Character: 3}))
	// An offset inside a multi-byte character maps to its start.
	assert.Equal(t, LSPPosition{Line: 0, Character: 2}, x.LSP(emoji+2))
	assert.Equal(t, LSPPosition{Line: 1, Character: 0}, x.LSP(len(src)-1))
	assert.Equal(t, len(src), x.FromLSP(LSPPosition{Line: 7, Character: 0}))
	assert.Equal(t, 0, x.FromLSP(LSPPosition{Line: -1, Character: 0}))
}

func TestInvalidUTF8(t *testing.T) {
	src := []byte("a\xff\xfeb")
	x := NewIndex(src)
	assert.Equal(t, LSPPosition{Line: 0, Character: 3}, x.LSP(3))
	assert.Equal(t, 3, x.FromLSP(LSPPosition{Line: 0, Character: 3}))
}

// TestLSPRoundTrip checks, for every rune boundary of a mixed source, that
// LSP and FromLSP are inverse and agree with an independent UTF-16 count.
func TestLSPRoundTrip(t *testing.T) {
	src := []byte("line one\r\nzwei: ä ö ü\n😀😀 x 𝄞\n\nend")
	x := NewIndex(src)
	for off := 0; off <= len(src); off++ {
		if off < len(src) && !utf8.RuneStart(src[off]) {
			continue
		}
		p := x.LSP(off)
		ls := x.LineStart(p.Line + 1)
		want := len(utf16.Encode([]rune(string(src[ls:off]))))
		assert.Equal(t, want, p.Character, "offset %d", off)
		back := x.FromLSP(p)
		if off < len(src) && src[off] == '\n' && off > 0 && src[off-1] == '\r' {
			continue // the '\n' of CRLF is not addressable separately
		}
		assert.Equal(t, off, back, "offset %d", off)
		lc := x.Position(off)
		o2, ok := x.Offset(lc)
		require.True(t, ok)
		assert.Equal(t, off, o2)
	}
}

func TestLSPRange(t *testing.T) {
	x := NewIndex([]byte("ab\ncd"))
	s, e := x.LSPRange(Range{Start: 1, End: 4})
	assert.Equal(t, LSPPosition{Line: 0, Character: 1}, s)
	assert.Equal(t, LSPPosition{Line: 1, Character: 1}, e)
}

func TestLSPUTF8(t *testing.T) {
	// "a" (1 byte), "é" (2), "😀" (4), then "b" on the next line after CRLF.
	src := []byte("aé\U0001F600z\r\nb")
	x := NewIndex(src)
	assert.Equal(t, LSPPosition{Line: 0, Character: 0}, x.LSPUTF8(0))
	assert.Equal(t, LSPPosition{Line: 0, Character: 1}, x.LSPUTF8(1))
	assert.Equal(t, LSPPosition{Line: 0, Character: 1}, x.LSPUTF8(2)) // inside é
	assert.Equal(t, LSPPosition{Line: 0, Character: 3}, x.LSPUTF8(3))
	assert.Equal(t, LSPPosition{Line: 0, Character: 3}, x.LSPUTF8(5)) // inside 😀
	assert.Equal(t, LSPPosition{Line: 0, Character: 7}, x.LSPUTF8(7))
	assert.Equal(t, LSPPosition{Line: 1, Character: 0}, x.LSPUTF8(10))
	assert.Equal(t, LSPPosition{Line: 1, Character: 1}, x.LSPUTF8(99)) // clamped

	assert.Equal(t, 3, x.FromLSPUTF8(LSPPosition{Line: 0, Character: 3}))
	assert.Equal(t, 3, x.FromLSPUTF8(LSPPosition{Line: 0, Character: 4})) // inside 😀
	assert.Equal(t, 8, x.FromLSPUTF8(LSPPosition{Line: 0, Character: 50}))
	assert.Equal(t, 10, x.FromLSPUTF8(LSPPosition{Line: 1, Character: 0}))
	assert.Equal(t, 0, x.FromLSPUTF8(LSPPosition{Line: -1, Character: 0}))
	assert.Equal(t, len(src), x.FromLSPUTF8(LSPPosition{Line: 9, Character: 0}))
	for off := 0; off <= len(src); off++ {
		if off < len(src) && !utf8.RuneStart(src[off]) {
			continue
		}
		if off == 9 {
			continue // the '\n' of CRLF maps back to the end of the line content
		}
		assert.Equal(t, off, x.FromLSPUTF8(x.LSPUTF8(off)), "offset %d", off)
	}
}

func TestHasBOM(t *testing.T) {
	assert.True(t, NewIndex([]byte("\xef\xbb\xbfa")).HasBOM())
	assert.True(t, NewIndex([]byte("\xef\xbb\xbf")).HasBOM())
	assert.False(t, NewIndex([]byte("\xef\xbb")).HasBOM())
	assert.False(t, NewIndex([]byte("a\xef\xbb\xbf")).HasBOM())
	assert.False(t, NewIndex(nil).HasBOM())
}
