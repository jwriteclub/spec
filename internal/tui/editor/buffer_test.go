// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package editor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/pos"
)

func rng(s, e int) pos.Range { return pos.Range{Start: s, End: e} }

func TestBufferInsertDeleteUndoRedo(t *testing.T) {
	b := NewBuffer([]byte("hello world\n"))
	assert.False(t, b.Dirty())
	v0 := b.Version()

	b.Replace(rng(5, 5), ",", KindOther, 5, 6)
	assert.Equal(t, "hello, world\n", string(b.Bytes()))
	assert.True(t, b.Dirty())
	assert.Greater(t, b.Version(), v0)
	assert.Equal(t, 2, b.Index().LineCount())

	b.Replace(rng(0, 5), "HELLO", KindOther, 0, 5)
	assert.Equal(t, "HELLO, world\n", string(b.Bytes()))

	_, cur, ok := b.Undo()
	require.True(t, ok)
	assert.Equal(t, 0, cur)
	assert.Equal(t, "hello, world\n", string(b.Bytes()))

	_, cur, ok = b.Undo()
	require.True(t, ok)
	assert.Equal(t, 5, cur)
	assert.Equal(t, "hello world\n", string(b.Bytes()))
	assert.False(t, b.Dirty(), "undoing back to the saved state is clean")
	_, _, ok = b.Undo()
	assert.False(t, ok)

	_, cur, ok = b.Redo()
	require.True(t, ok)
	assert.Equal(t, 6, cur)
	assert.Equal(t, "hello, world\n", string(b.Bytes()))

	// A new edit drops the redo stack.
	b.Replace(rng(0, 0), ">", KindOther, 0, 1)
	assert.False(t, b.CanRedo())
	assert.Equal(t, ">hello, world\n", string(b.Bytes()))
}

func TestBufferTypingCoalesces(t *testing.T) {
	b := NewBuffer([]byte("ab\n"))
	for i, c := range "xyz" {
		b.Replace(rng(1+i, 1+i), string(c), KindType, 1+i, 2+i)
	}
	assert.Equal(t, "axyzb\n", string(b.Bytes()))
	_, cur, ok := b.Undo()
	require.True(t, ok)
	assert.Equal(t, "ab\n", string(b.Bytes()), "one undo removes the typed run")
	assert.Equal(t, 1, cur)

	_, cur, _ = b.Redo()
	assert.Equal(t, "axyzb\n", string(b.Bytes()))
	assert.Equal(t, 4, cur)
}

func TestBufferCoalescingBreaks(t *testing.T) {
	b := NewBuffer(nil)
	b.Replace(rng(0, 0), "a", KindType, 0, 1)
	b.Replace(rng(1, 1), "b", KindType, 1, 2)
	// Typing somewhere else starts a new step.
	b.Replace(rng(0, 0), "c", KindType, 0, 1)
	assert.Equal(t, "cab", string(b.Bytes()))
	b.Undo()
	assert.Equal(t, "ab", string(b.Bytes()))

	// Seal (a cursor motion) starts a new step even at the same place.
	b.Seal()
	b.Replace(rng(2, 2), "d", KindType, 2, 3)
	b.Undo()
	assert.Equal(t, "ab", string(b.Bytes()))

	// A line break is never part of a typed run.
	b.Replace(rng(2, 2), "e", KindType, 2, 3)
	b.Replace(rng(3, 3), "\n", KindType, 3, 4)
	b.Undo()
	assert.Equal(t, "abe", string(b.Bytes()))
	b.Undo()
	assert.Equal(t, "ab", string(b.Bytes()))
}

func TestBufferBackspaceAndDeleteCoalesce(t *testing.T) {
	b := NewBuffer([]byte("abcdef"))
	// Backspace from the end three times.
	b.Replace(rng(5, 6), "", KindDelete, 6, 5)
	b.Replace(rng(4, 5), "", KindDelete, 5, 4)
	b.Replace(rng(3, 4), "", KindDelete, 4, 3)
	assert.Equal(t, "abc", string(b.Bytes()))
	_, cur, _ := b.Undo()
	assert.Equal(t, "abcdef", string(b.Bytes()))
	assert.Equal(t, 6, cur)

	// Forward delete at the same offset.
	b.Replace(rng(1, 2), "", KindDelete, 1, 1)
	b.Replace(rng(1, 2), "", KindDelete, 1, 1)
	assert.Equal(t, "adef", string(b.Bytes()))
	b.Undo()
	assert.Equal(t, "abcdef", string(b.Bytes()))

	// Typing does not join a deletion.
	b.Replace(rng(0, 1), "", KindDelete, 1, 0)
	b.Replace(rng(0, 0), "z", KindType, 0, 1)
	b.Undo()
	assert.Equal(t, "bcdef", string(b.Bytes()))
}

func TestBufferSaveAndReset(t *testing.T) {
	b := NewBuffer([]byte("x"))
	b.Replace(rng(1, 1), "y", KindType, 1, 2)
	b.MarkSaved()
	assert.False(t, b.Dirty())
	// Typing after a save is a new step, so undo returns to the save point.
	b.Replace(rng(2, 2), "z", KindType, 2, 3)
	assert.True(t, b.Dirty())
	b.Undo()
	assert.False(t, b.Dirty())
	assert.Equal(t, "xy", string(b.Bytes()))

	b.Reset([]byte("new"))
	assert.False(t, b.Dirty())
	assert.False(t, b.CanUndo())
	assert.Equal(t, "new", string(b.Bytes()))
}

func TestShiftSpans(t *testing.T) {
	spans := []Span{{0, 3, FInv}, {5, 8, FDef}, {10, 12, FCode}}
	// Insert inside the second span: it grows; the third moves.
	got := shiftSpans(append([]Span(nil), spans...), Edit{Off: 6, New: "xx"})
	assert.Equal(t, []Span{{0, 3, FInv}, {5, 10, FDef}, {12, 14, FCode}}, got)
	// Insert at a span's start pushes it.
	got = shiftSpans(append([]Span(nil), spans...), Edit{Off: 5, New: "x"})
	assert.Equal(t, []Span{{0, 3, FInv}, {6, 9, FDef}, {11, 13, FCode}}, got)
	// Delete across the end of the first span and the start of the second.
	got = shiftSpans(append([]Span(nil), spans...), Edit{Off: 2, Old: "abcd"})
	assert.Equal(t, []Span{{0, 2, FInv}, {2, 4, FDef}, {6, 8, FCode}}, got)
	// Delete a whole span.
	got = shiftSpans(append([]Span(nil), spans...), Edit{Off: 10, Old: "ab"})
	assert.Equal(t, []Span{{0, 3, FInv}, {5, 8, FDef}}, got)
}
