// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package editor is the text editor component of the TUI: a byte buffer
// with undo, a soft-wrapping view model with hit-testing, highlighting from
// mdspec ranges, and the #→§ conversion. It knows nothing about Bubble Tea;
// package tui maps keys and mouse events onto it.
package editor

import (
	"slices"

	"github.com/jwriteclub/spec/internal/pos"
)

// Edit is one byte-range splice: the Old bytes at Off are replaced by New.
// It is the same model as package edit's Splice.
type Edit struct {
	Off int
	Old string
	New string
}

// End returns the end of the replaced range in the source before the edit.
func (e Edit) End() int {
	return e.Off + len(e.Old)
}

// inverse returns the edit that undoes e.
func (e Edit) inverse() Edit {
	return Edit{Off: e.Off, Old: e.New, New: e.Old}
}

// EditKind classifies an edit for undo coalescing.
type EditKind int

// Edit kinds.
const (
	// KindOther never coalesces.
	KindOther EditKind = iota
	// KindType is typed text; a run of typing at the cursor coalesces.
	KindType
	// KindDelete is backspace or delete; a run of deletions coalesces.
	KindDelete
)

// group is one undo step: the edits in the order they were applied, and
// the cursor before and after.
type group struct {
	edits  []Edit
	kind   EditKind
	before int
	after  int
	id     uint64
}

// Buffer is a byte source with a line index and an undo history.
type Buffer struct {
	src  []byte
	idx  *pos.Index
	undo []*group
	redo []*group
	// open is true while the top undo group may still absorb edits.
	open    bool
	version uint64
	nextID  uint64
	savedID uint64
}

// NewBuffer returns a buffer holding a copy of src, marked as saved.
func NewBuffer(src []byte) *Buffer {
	b := &Buffer{src: append([]byte(nil), src...)}
	b.idx = pos.NewIndex(b.src)
	return b
}

// Bytes returns the current source. The caller must not modify it, and it
// is invalidated by the next edit.
func (b *Buffer) Bytes() []byte {
	return b.src
}

// Len returns the source length in bytes.
func (b *Buffer) Len() int {
	return len(b.src)
}

// Index returns the line index of the current source.
func (b *Buffer) Index() *pos.Index {
	return b.idx
}

// Version increases with every change to the source.
func (b *Buffer) Version() uint64 {
	return b.version
}

// Text returns the bytes of r as a string, clipped to the source.
func (b *Buffer) Text(r pos.Range) string {
	s, e := max(r.Start, 0), min(r.End, len(b.src))
	if s >= e {
		return ""
	}
	return string(b.src[s:e])
}

// stateID identifies the current history position.
func (b *Buffer) stateID() uint64 {
	if len(b.undo) == 0 {
		return 0
	}
	return b.undo[len(b.undo)-1].id
}

// StateID identifies the current history position, for Holds and Undone.
func (b *Buffer) StateID() uint64 {
	return b.stateID()
}

// Holds reports whether the source includes the edits up to history position
// id: it was reached and has not been undone since.
func (b *Buffer) Holds(id uint64) bool {
	return id == 0 || slices.ContainsFunc(b.undo, func(g *group) bool { return g.id == id })
}

// Undone reports whether history position id was undone and can be redone.
func (b *Buffer) Undone(id uint64) bool {
	return slices.ContainsFunc(b.redo, func(g *group) bool { return g.id == id })
}

// Dirty reports whether the source differs from the last saved state (by
// history position, so undoing back to the save point is clean).
func (b *Buffer) Dirty() bool {
	return b.stateID() != b.savedID
}

// MarkSaved records the current state as saved and closes the open undo
// group, so later typing starts a new one.
func (b *Buffer) MarkSaved() {
	b.open = false
	b.savedID = b.stateID()
}

// Reset replaces the whole source (a reload from disk) and clears the
// history. The buffer is clean afterwards.
func (b *Buffer) Reset(src []byte) {
	b.src = append([]byte(nil), src...)
	b.idx = pos.NewIndex(b.src)
	b.undo, b.redo = nil, nil
	b.open = false
	b.savedID = 0
	b.version++
}

// Seal closes the open undo group: the next edit starts a new one.
func (b *Buffer) Seal() {
	b.open = false
}

// apply splices e into the source without touching the history.
func (b *Buffer) apply(e Edit) {
	out := make([]byte, 0, len(b.src)-len(e.Old)+len(e.New))
	out = append(out, b.src[:e.Off]...)
	out = append(out, e.New...)
	out = append(out, b.src[e.End():]...)
	b.src = out
	b.idx = pos.NewIndex(b.src)
	b.version++
}

// Replace replaces the bytes of r with text and records the change for undo.
// cursorBefore and cursorAfter are the cursor offsets to restore on undo and
// redo. A KindType or KindDelete edit joins the open group when it continues
// it (typing right after the previous insertion, deleting next to the
// previous deletion); anything else starts a new group. r is clipped to the
// source. It returns the edit applied.
func (b *Buffer) Replace(r pos.Range, text string, kind EditKind, cursorBefore, cursorAfter int) Edit {
	s, en := max(r.Start, 0), min(r.End, len(b.src))
	if en < s {
		en = s
	}
	e := Edit{Off: s, Old: string(b.src[s:en]), New: text}
	if e.Old == e.New {
		return e
	}
	b.apply(e)
	b.redo = nil
	if b.open && kind != KindOther && len(b.undo) > 0 {
		top := b.undo[len(b.undo)-1]
		if top.kind == kind && continues(top.edits[len(top.edits)-1], e, kind) {
			top.edits = append(top.edits, e)
			top.after = cursorAfter
			return e
		}
	}
	b.nextID++
	b.undo = append(b.undo, &group{edits: []Edit{e}, kind: kind, before: cursorBefore, after: cursorAfter, id: b.nextID})
	b.open = kind != KindOther
	return e
}

// continues reports whether e extends the run ending with last.
func continues(last, e Edit, kind EditKind) bool {
	switch kind {
	case KindType:
		// An insertion right where the previous one ended, without a line
		// break (a new line starts a new undo step).
		// The run may start by replacing a selection (last.Old not empty).
		return e.Old == "" && e.Off == last.Off+len(last.New) && !hasNewline(e.New)
	case KindDelete:
		if e.New != "" || last.New != "" || hasNewline(e.Old) {
			return false
		}
		// Backspace removes the bytes just before the previous deletion;
		// delete removes the bytes at the same offset.
		return e.End() == last.Off || e.Off == last.Off
	}
	return false
}

func hasNewline(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return true
		}
	}
	return false
}

// CanUndo reports whether there is something to undo.
func (b *Buffer) CanUndo() bool {
	return len(b.undo) > 0
}

// CanRedo reports whether there is something to redo.
func (b *Buffer) CanRedo() bool {
	return len(b.redo) > 0
}

// Undo reverts the last group. It returns the edits it applied (in order)
// and the cursor to restore, or ok false when there is nothing to undo.
func (b *Buffer) Undo() (applied []Edit, cursor int, ok bool) {
	if len(b.undo) == 0 {
		return nil, 0, false
	}
	g := b.undo[len(b.undo)-1]
	b.undo = b.undo[:len(b.undo)-1]
	for i := len(g.edits) - 1; i >= 0; i-- {
		inv := g.edits[i].inverse()
		b.apply(inv)
		applied = append(applied, inv)
	}
	b.redo = append(b.redo, g)
	b.open = false
	return applied, g.before, true
}

// Redo reapplies the last undone group.
func (b *Buffer) Redo() (applied []Edit, cursor int, ok bool) {
	if len(b.redo) == 0 {
		return nil, 0, false
	}
	g := b.redo[len(b.redo)-1]
	b.redo = b.redo[:len(b.redo)-1]
	for _, e := range g.edits {
		b.apply(e)
		applied = append(applied, e)
	}
	b.undo = append(b.undo, g)
	b.open = false
	return applied, g.after, true
}
