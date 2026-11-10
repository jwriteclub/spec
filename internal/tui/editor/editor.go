// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package editor

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// Options configures an Editor.
type Options struct {
	// HashToSection turns on the #→§ conversion while typing.
	HashToSection bool
	// TabSpaces is how many spaces the tab key inserts (default 4).
	TabSpaces int
}

// RowPos is a display row: the 0-based logical line and the row within it.
type RowPos struct {
	Line, Sub int
}

// less orders row positions top to bottom.
func (p RowPos) less(q RowPos) bool {
	return p.Line < q.Line || (p.Line == q.Line && p.Sub < q.Sub)
}

// Editor is one open file: buffer, cursor, selection, scroll position and
// highlight state, laid out in a pane of a given size.
type Editor struct {
	path string
	opts Options
	buf  *Buffer

	cursor int
	goal   int // goal column for vertical motion; -1 recomputes it
	mark   int // selection anchor; -1 when there is no selection
	sticky bool

	top           RowPos
	width, height int

	analysis Analysis
	parsed   uint64 // buffer version the analysis was computed from
	spans    []Span
	match    pos.Range
	extra    []Span
	ghosts   []Ghost

	cache        map[int][]Row
	cacheVersion uint64
	cacheWidth   int

	dragFrom int // -1 when no drag is in progress
	flash    string
}

// New returns an editor for path holding src.
func New(path string, src []byte, opts Options) *Editor {
	if opts.TabSpaces <= 0 {
		opts.TabSpaces = 4
	}
	return &Editor{
		path: path, opts: opts, buf: NewBuffer(src),
		goal: -1, mark: -1, dragFrom: -1,
		width: 80, height: 24,
	}
}

// Path returns the project-relative path of the file.
func (e *Editor) Path() string { return e.path }

// Buffer returns the editor's buffer.
func (e *Editor) Buffer() *Buffer { return e.buf }

// Cursor returns the cursor's byte offset.
func (e *Editor) Cursor() int { return e.cursor }

// TopRow returns the first displayed row.
func (e *Editor) TopRow() RowPos { return e.top }

// Size returns the pane size, gutter included.
func (e *Editor) Size() (int, int) { return e.width, e.height }

// SetOptions replaces the options.
func (e *Editor) SetOptions(o Options) {
	if o.TabSpaces <= 0 {
		o.TabSpaces = 4
	}
	e.opts = o
}

// TakeFlash returns and clears the editor's pending status message.
func (e *Editor) TakeFlash() string {
	f := e.flash
	e.flash = ""
	return f
}

// SetSize sets the pane size in cells, gutter included.
func (e *Editor) SetSize(w, h int) {
	e.width, e.height = max(w, 8), max(h, 1)
	e.ensureVisible()
}

// GutterWidth returns the width of the line-number gutter: the digits, a
// space, the diagnostic marker and a space.
func (e *Editor) GutterWidth() int {
	digits := len(fmt.Sprint(e.buf.Index().LineCount()))
	return max(digits, 3) + 3
}

// TextWidth returns the width of the text area.
func (e *Editor) TextWidth() int {
	return max(e.width-e.GutterWidth(), 3)
}

// wrapWidth is the width rows are wrapped at: one cell is kept free for the
// cursor at the end of a full row.
func (e *Editor) wrapWidth() int {
	return e.TextWidth() - 1
}

// ---- analysis and highlight ----

// SetAnalysis installs the result of parsing the buffer at version. A
// result for an older version is ignored (a newer parse will follow).
func (e *Editor) SetAnalysis(a Analysis, version uint64) {
	if version != e.buf.Version() {
		return
	}
	e.analysis = a
	e.parsed = version
	e.spans = Spans(a)
}

// Analysis returns the last analysis and whether it is current.
func (e *Editor) Analysis() (Analysis, bool) {
	return e.analysis, e.parsed == e.buf.Version() && !e.unanalyzed()
}

// Stale reports whether the buffer changed since the last analysis.
func (e *Editor) Stale() bool {
	return e.parsed != e.buf.Version() || e.unanalyzed()
}

// unanalyzed reports whether no analysis has been installed.
func (e *Editor) unanalyzed() bool {
	return e.analysis.File == nil && e.analysis.Path == ""
}

// Spans returns the current (possibly shifted) highlight spans.
func (e *Editor) Spans() []Span { return e.spans }

// SetMatch highlights r as the current find match (an empty range clears).
func (e *Editor) SetMatch(r pos.Range) { e.match = r }

// SetGhosts sets the display-only text drawn in the buffer (sorted by
// offset; nil for none). The rows are laid out again when it changes.
func (e *Editor) SetGhosts(gs []Ghost) {
	if slices.Equal(gs, e.ghosts) {
		return
	}
	e.ghosts = gs
	e.cache = nil
}

// SetExtra sets highlight spans the host computes for the current text
// (the words changed since HEAD); they are drawn over the analysis spans
// until the next call.
func (e *Editor) SetExtra(spans []Span) { e.extra = spans }

// afterEdits updates derived state after the buffer changed.
func (e *Editor) afterEdits(edits []Edit) {
	for _, ed := range edits {
		e.spans = shiftSpans(e.spans, ed)
	}
	e.match = pos.Range{}
	e.goal = -1
}

// ---- rows ----

// lineCount returns the number of logical lines.
func (e *Editor) lineCount() int {
	return e.buf.Index().LineCount()
}

// lineRange returns the content range of 0-based line. The first line's
// range starts after a byte order mark: the BOM is not text, and the
// cursor never goes before it, so typing at the top keeps it first.
func (e *Editor) lineRange(line int) pos.Range {
	r := e.buf.Index().LineRange(line + 1)
	if line == 0 {
		r.Start = min(e.bomLen(), r.End)
	}
	return r
}

// bomLen returns the length of the UTF-8 byte order mark the buffer starts
// with, or 0.
func (e *Editor) bomLen() int {
	if bytes.HasPrefix(e.buf.Bytes(), []byte(byteOrderMark)) {
		return len(byteOrderMark)
	}
	return 0
}

// lineBounds returns the whole of 0-based line, its terminator included
// (to the end of the buffer on the last line): what ^K cuts.
func (e *Editor) lineBounds(line int) pos.Range {
	r := pos.Range{Start: e.lineRange(line).Start, End: e.buf.Index().LineStart(line + 2)}
	if line+1 >= e.lineCount() {
		r.End = e.buf.Len()
	}
	return r
}

// lineOf returns the 0-based line holding off.
func (e *Editor) lineOf(off int) int {
	return e.buf.Index().Line(off) - 1
}

// Rows returns the display rows of 0-based line.
func (e *Editor) Rows(line int) []Row {
	if e.cache == nil || e.cacheVersion != e.buf.Version() || e.cacheWidth != e.wrapWidth() {
		e.cache = map[int][]Row{}
		e.cacheVersion = e.buf.Version()
		e.cacheWidth = e.wrapWidth()
	}
	if rs, ok := e.cache[line]; ok {
		return rs
	}
	lr := e.lineRange(line)
	rs := wrapLine(e.buf.Bytes(), lr.Start, lr.End, line, e.wrapWidth(), e.ghosts)
	if len(e.cache) > 4096 {
		e.cache = map[int][]Row{}
	}
	e.cache[line] = rs
	return rs
}

// rowAt returns the row at p, clamped to the line's rows.
func (e *Editor) rowAt(p RowPos) Row {
	rs := e.Rows(p.Line)
	return rs[min(max(p.Sub, 0), len(rs)-1)]
}

// next returns the row after p, or false at the end.
func (e *Editor) next(p RowPos) (RowPos, bool) {
	if p.Sub+1 < len(e.Rows(p.Line)) {
		return RowPos{p.Line, p.Sub + 1}, true
	}
	if p.Line+1 < e.lineCount() {
		return RowPos{p.Line + 1, 0}, true
	}
	return p, false
}

// prev returns the row before p, or false at the start.
func (e *Editor) prev(p RowPos) (RowPos, bool) {
	if p.Sub > 0 {
		return RowPos{p.Line, p.Sub - 1}, true
	}
	if p.Line > 0 {
		return RowPos{p.Line - 1, len(e.Rows(p.Line-1)) - 1}, true
	}
	return p, false
}

// rowOf returns the row position displaying the cursor at off.
func (e *Editor) rowOf(off int) RowPos {
	line := e.lineOf(off)
	rs := e.Rows(line)
	for _, r := range rs {
		if r.Contains(off) {
			return RowPos{line, r.Sub}
		}
	}
	return RowPos{line, len(rs) - 1}
}

// colOf returns the display column of off in its row.
func (e *Editor) colOf(off int) int {
	r := e.rowAt(e.rowOf(off))
	c, _ := r.ColOf(off)
	return c
}

// ensureVisible scrolls so the cursor's row is on screen.
func (e *Editor) ensureVisible() {
	cur := e.rowOf(e.cursor)
	if e.top.Line >= e.lineCount() {
		e.top = RowPos{}
	}
	e.top.Sub = min(e.top.Sub, len(e.Rows(e.top.Line))-1)
	if cur.less(e.top) {
		e.top = cur
		return
	}
	p := e.top
	for k := 0; k < e.height; k++ {
		if p == cur {
			return
		}
		var ok bool
		if p, ok = e.next(p); !ok {
			break
		}
	}
	// Below the screen: put the cursor on the last row.
	p = cur
	for k := 0; k < e.height-1; k++ {
		q, ok := e.prev(p)
		if !ok {
			break
		}
		p = q
	}
	e.top = p
}

// CenterCursor scrolls so the cursor's row is a third of the way down.
func (e *Editor) CenterCursor() {
	p := e.rowOf(e.cursor)
	for k := 0; k < e.height/3; k++ {
		q, ok := e.prev(p)
		if !ok {
			break
		}
		p = q
	}
	e.top = p
	e.ensureVisible()
}

// ---- cursor and selection ----

// snap moves off back to the start of the glyph that contains it (and
// past a byte order mark).
func (e *Editor) snap(off int) int {
	off = min(max(off, e.bomLen()), e.buf.Len())
	lr := e.lineRange(e.lineOf(off))
	if off >= lr.End {
		if off > lr.End {
			// Inside a line terminator ("\r\n"): the end of the content.
			return lr.End
		}
		return off
	}
	for _, g := range glyphsOf(e.buf.Bytes(), lr.Start, lr.End) {
		if off < g.End() {
			return g.Off
		}
	}
	return off
}

// SetCursor moves the cursor to off (snapped to a glyph start) and clears a
// non-sticky selection.
func (e *Editor) SetCursor(off int) {
	e.cursor = e.snap(off)
	e.goal = -1
	if !e.sticky {
		e.mark = -1
	}
	e.buf.Seal()
	e.ensureVisible()
}

// moveTo moves the cursor for a motion command. With sel, the selection
// extends (a selection starts at the old cursor).
func (e *Editor) moveTo(off int, sel bool) {
	if sel {
		if e.mark < 0 {
			e.mark = e.cursor
		}
	} else if !e.sticky {
		e.mark = -1
	}
	e.cursor = e.snap(off)
	e.buf.Seal()
	e.ensureVisible()
}

// ToggleMark sets the mark at the cursor (a sticky selection that motion
// extends), or clears it when one is set. It reports whether the mark is
// now set.
func (e *Editor) ToggleMark() bool {
	if e.mark >= 0 {
		e.mark = -1
		e.sticky = false
		return false
	}
	e.mark = e.cursor
	e.sticky = true
	return true
}

// Marked reports whether a sticky or nonempty selection mark is present.
func (e *Editor) Marked() bool { return e.mark >= 0 }

// ClearMark removes any selection.
func (e *Editor) ClearMark() {
	e.mark = -1
	e.sticky = false
}

// Selection returns the selected range, if any.
func (e *Editor) Selection() (pos.Range, bool) {
	if e.mark < 0 || e.mark == e.cursor {
		return pos.Range{}, false
	}
	return pos.Range{Start: min(e.mark, e.cursor), End: max(e.mark, e.cursor)}, true
}

// SetSelection selects from anchor to cursor as a mouse drag does (the
// cursor at cursor; a drag in progress goes on from anchor). A replay
// sets the selection a recorded drag ended with.
func (e *Editor) SetSelection(anchor, cursor int) {
	e.mark = e.snap(anchor)
	e.sticky = false
	e.cursor = e.snap(cursor)
	e.goal = -1
	if e.dragFrom >= 0 {
		e.dragFrom = e.mark
	}
	e.buf.Seal()
	e.ensureVisible()
}

// SetTopRow scrolls so that p is the first displayed row, as far as the
// buffer allows (the last screen stays full), moving the cursor onto the
// screen as ScrollRows does.
func (e *Editor) SetTopRow(p RowPos) {
	for e.top.less(p) {
		before := e.top
		e.ScrollRows(1)
		if e.top == before {
			return
		}
	}
	for p.less(e.top) {
		before := e.top
		e.ScrollRows(-1)
		if e.top == before {
			return
		}
	}
}

// Left moves one glyph left (across line breaks).
func (e *Editor) Left(sel bool) {
	e.goal = -1
	if r, ok := e.Selection(); ok && !sel && !e.sticky {
		e.moveTo(r.Start, false)
		return
	}
	e.moveTo(e.prevGlyph(e.cursor), sel)
	e.goal = -1
}

// Right moves one glyph right (across line breaks).
func (e *Editor) Right(sel bool) {
	e.goal = -1
	if r, ok := e.Selection(); ok && !sel && !e.sticky {
		e.moveTo(r.End, false)
		return
	}
	e.moveTo(e.nextGlyph(e.cursor), sel)
	e.goal = -1
}

// prevGlyph returns the start of the glyph before off, crossing a line
// break to the end of the previous line.
func (e *Editor) prevGlyph(off int) int {
	line := e.lineOf(off)
	lr := e.lineRange(line)
	if off <= lr.Start {
		if line == 0 {
			return lr.Start
		}
		return e.lineRange(line - 1).End
	}
	prev := lr.Start
	for _, g := range glyphsOf(e.buf.Bytes(), lr.Start, lr.End) {
		if g.Off >= off {
			break
		}
		prev = g.Off
	}
	return prev
}

// nextGlyph returns the offset after the glyph at off, crossing a line
// break to the start of the next line.
func (e *Editor) nextGlyph(off int) int {
	line := e.lineOf(off)
	lr := e.lineRange(line)
	if off >= lr.End {
		if line+1 >= e.lineCount() {
			return e.buf.Len()
		}
		return e.lineRange(line + 1).Start
	}
	for _, g := range glyphsOf(e.buf.Bytes(), off, lr.End) {
		return g.End()
	}
	return lr.End
}

// vertical moves n display rows (negative is up), keeping the goal column.
func (e *Editor) vertical(n int, sel bool) {
	if e.goal < 0 {
		e.goal = e.colOf(e.cursor)
	}
	goal := e.goal
	p := e.rowOf(e.cursor)
	moved := false
	for n != 0 {
		var q RowPos
		var ok bool
		if n > 0 {
			q, ok = e.next(p)
			n--
		} else {
			q, ok = e.prev(p)
			n++
		}
		if !ok {
			break
		}
		p = q
		moved = true
	}
	off := e.rowAt(p).OffsetAt(goal)
	if !moved {
		off = e.cursor
	}
	e.moveTo(off, sel)
	e.goal = goal
}

// Up moves one display row up.
func (e *Editor) Up(sel bool) { e.vertical(-1, sel) }

// Down moves one display row down.
func (e *Editor) Down(sel bool) { e.vertical(1, sel) }

// LineStart moves to the start of the logical line.
func (e *Editor) LineStart(sel bool) {
	e.moveTo(e.lineRange(e.lineOf(e.cursor)).Start, sel)
}

// LineEnd moves to the end of the logical line.
func (e *Editor) LineEnd(sel bool) {
	e.moveTo(e.lineRange(e.lineOf(e.cursor)).End, sel)
}

// Top moves to the start of the buffer.
func (e *Editor) Top(sel bool) { e.moveTo(0, sel) }

// Bottom moves to the end of the buffer.
func (e *Editor) Bottom(sel bool) { e.moveTo(e.buf.Len(), sel) }

// PageDown scrolls and moves the cursor down a screen less two rows.
func (e *Editor) PageDown(sel bool) {
	n := max(e.height-2, 1)
	e.ScrollRows(n)
	e.vertical(n, sel)
}

// PageUp scrolls and moves the cursor up a screen less two rows.
func (e *Editor) PageUp(sel bool) {
	n := max(e.height-2, 1)
	e.ScrollRows(-n)
	e.vertical(-n, sel)
}

// ScrollRows moves the view by n rows without moving the cursor, unless the
// cursor would leave the screen, in which case it moves onto the nearest
// visible row.
func (e *Editor) ScrollRows(n int) {
	for ; n > 0; n-- {
		q, ok := e.next(e.top)
		if !ok {
			break
		}
		// Keep the last screen full.
		if !e.rowsBelow(q, e.height-1) {
			break
		}
		e.top = q
	}
	for ; n < 0; n++ {
		q, ok := e.prev(e.top)
		if !ok {
			break
		}
		e.top = q
	}
	cur := e.rowOf(e.cursor)
	last := e.top
	for k := 1; k < e.height; k++ {
		q, ok := e.next(last)
		if !ok {
			break
		}
		last = q
	}
	goal := e.goal
	if goal < 0 {
		goal = e.colOf(e.cursor)
	}
	switch {
	case cur.less(e.top):
		e.cursor = e.rowAt(e.top).OffsetAt(goal)
		e.goal = goal
	case last.less(cur):
		e.cursor = e.rowAt(last).OffsetAt(goal)
		e.goal = goal
	}
	e.buf.Seal()
}

// rowsBelow reports whether at least n rows follow p.
func (e *Editor) rowsBelow(p RowPos, n int) bool {
	for k := 0; k < n; k++ {
		var ok bool
		if p, ok = e.next(p); !ok {
			return false
		}
	}
	return true
}

// wordClass classifies a rune for word motion.
func wordClass(r rune) int {
	switch {
	case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
		return 2
	case unicode.IsSpace(r):
		return 0
	}
	return 1
}

// WordRight moves to the start of the next word.
func (e *Editor) WordRight(sel bool) {
	src := e.buf.Bytes()
	i := e.cursor
	for i < len(src) {
		r, n := utf8.DecodeRune(src[i:])
		if wordClass(r) != 2 {
			break
		}
		i += n
	}
	for i < len(src) {
		r, n := utf8.DecodeRune(src[i:])
		if wordClass(r) == 2 {
			break
		}
		i += n
	}
	e.moveTo(i, sel)
}

// WordLeft moves to the start of the previous word.
func (e *Editor) WordLeft(sel bool) {
	src := e.buf.Bytes()
	i := e.cursor
	for i > 0 {
		r, n := utf8.DecodeLastRune(src[:i])
		if wordClass(r) == 2 {
			break
		}
		i -= n
	}
	for i > 0 {
		r, n := utf8.DecodeLastRune(src[:i])
		if wordClass(r) != 2 {
			break
		}
		i -= n
	}
	e.moveTo(i, sel)
}

// GotoLine moves to the start of 1-based line n (clamped).
func (e *Editor) GotoLine(n int) {
	n = min(max(n, 1), e.lineCount())
	e.SetCursor(e.lineRange(n - 1).Start)
	e.CenterCursor()
}

// ---- editing ----

// newline returns the line terminator the file uses.
func (e *Editor) newline() string {
	src := e.buf.Bytes()
	if i := bytes.IndexByte(src, '\n'); i > 0 && src[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// replace edits the buffer and moves the cursor to cursorAfter.
func (e *Editor) replace(r pos.Range, text string, kind EditKind, cursorAfter int) {
	ed := e.buf.Replace(r, text, kind, e.cursor, cursorAfter)
	e.afterEdits([]Edit{ed})
	e.cursor = min(max(cursorAfter, 0), e.buf.Len())
	e.ensureVisible()
}

// deleteSelection removes the selection, if any, and reports whether it did.
func (e *Editor) deleteSelection() bool {
	r, ok := e.Selection()
	if !ok {
		return false
	}
	e.buf.Seal()
	e.replace(r, "", KindOther, r.Start)
	e.buf.Seal()
	e.ClearMark()
	return true
}

// Type inserts typed text at the cursor, replacing a selection. Typing
// coalesces into one undo step. When the text ends a #N token that names a
// section, the '#' becomes '§' as a separate undo step.
func (e *Editor) Type(text string) {
	if text == "" {
		return
	}
	r := e.takeSelection()
	e.replace(r, text, KindType, r.Start+len(text))
	if e.opts.HashToSection {
		e.convertHash(r.Start, text)
	}
}

// takeSelection returns the range typed or pasted text replaces: the
// selection, or the empty range at the cursor. Replacing a selection is
// one undo step with the text that replaces it, so the open undo group is
// closed first. The selection is cleared.
func (e *Editor) takeSelection() pos.Range {
	r, ok := e.Selection()
	if !ok {
		return pos.Range{Start: e.cursor, End: e.cursor}
	}
	e.buf.Seal()
	e.ClearMark()
	return r
}

// Insert inserts text at the cursor (a paste) as one undo step, without the
// #→§ conversion. A selection is replaced.
func (e *Editor) Insert(text string) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if nl := e.newline(); nl != "\n" {
		text = strings.ReplaceAll(text, "\n", nl)
	}
	if text == "" {
		return
	}
	r := e.takeSelection()
	e.buf.Seal()
	e.replace(r, text, KindOther, r.Start+len(text))
	e.buf.Seal()
}

// ReplaceRange replaces r with text as one undo step and puts the cursor
// after it (completion inserting a name over what was typed).
func (e *Editor) ReplaceRange(r pos.Range, text string) {
	r.Start = min(max(r.Start, 0), e.buf.Len())
	r.End = min(max(r.End, r.Start), e.buf.Len())
	e.ClearMark()
	e.buf.Seal()
	e.replace(r, text, KindOther, r.Start+len(text))
	e.buf.Seal()
}

// ReplaceAll replaces the whole source with text as one undo step (^F
// formatting) and puts the cursor at offset cursor of text. Only the span
// between the common prefix and suffix is replaced.
func (e *Editor) ReplaceAll(text string, cursor int) {
	old := e.buf.Bytes()
	p := 0
	for p < len(old) && p < len(text) && old[p] == text[p] {
		p++
	}
	sfx := 0
	for sfx < len(old)-p && sfx < len(text)-p && old[len(old)-1-sfx] == text[len(text)-1-sfx] {
		sfx++
	}
	e.ClearMark()
	e.buf.Seal()
	e.replace(pos.Range{Start: p, End: len(old) - sfx}, text[p:len(text)-sfx], KindOther, cursor)
	e.cursor = e.snap(e.cursor)
	e.buf.Seal()
}

// Newline inserts a line break as its own undo step.
func (e *Editor) Newline() {
	r := e.takeSelection()
	e.buf.Seal()
	nl := e.newline()
	e.replace(r, nl, KindOther, r.Start+len(nl))
	if e.opts.HashToSection {
		e.convertHash(r.Start, "\n")
	}
	e.buf.Seal()
}

// Tab inserts spaces up to the configured count.
func (e *Editor) Tab() {
	e.Type(strings.Repeat(" ", e.opts.TabSpaces))
}

// Backspace deletes the selection or the glyph (or line break) before the
// cursor.
func (e *Editor) Backspace() {
	if e.deleteSelection() || e.cursor <= e.bomLen() {
		return
	}
	from := e.prevGlyph(e.cursor)
	kind := KindDelete
	if e.lineOf(from) != e.lineOf(e.cursor) {
		kind = KindOther
		e.buf.Seal()
	}
	e.replace(pos.Range{Start: from, End: e.cursor}, "", kind, from)
	if kind == KindOther {
		e.buf.Seal()
	}
}

// Delete deletes the selection or the glyph (or line break) at the cursor.
func (e *Editor) Delete() {
	if e.deleteSelection() || e.cursor >= e.buf.Len() {
		return
	}
	to := e.nextGlyph(e.cursor)
	kind := KindDelete
	if e.lineOf(to) != e.lineOf(e.cursor) {
		kind = KindOther
		e.buf.Seal()
	}
	e.replace(pos.Range{Start: e.cursor, End: to}, "", kind, e.cursor)
	if kind == KindOther {
		e.buf.Seal()
	}
}

// Cut removes the selection, or the cursor's whole line with its line
// break, and returns the removed text.
func (e *Editor) Cut() string {
	r, ok := e.Selection()
	if !ok {
		r = e.lineBounds(e.lineOf(e.cursor))
	}
	text := e.buf.Text(r)
	if text == "" {
		return ""
	}
	e.buf.Seal()
	e.replace(r, "", KindOther, r.Start)
	e.buf.Seal()
	e.ClearMark()
	return text
}

// CanCut reports whether Cut would remove anything: a selection, or a
// line that is not the empty last one.
func (e *Editor) CanCut() bool {
	if _, ok := e.Selection(); ok {
		return true
	}
	return !e.lineBounds(e.lineOf(e.cursor)).Empty()
}

// Copy returns the selection, or the cursor's whole line with its line
// break, without changing the buffer. The mark is cleared.
func (e *Editor) Copy() string {
	r, ok := e.Selection()
	if !ok {
		r = e.lineBounds(e.lineOf(e.cursor))
	}
	e.ClearMark()
	return e.buf.Text(r)
}

// Undo reverts the last undo step and reports whether there was one.
func (e *Editor) Undo() bool {
	edits, cur, ok := e.buf.Undo()
	if !ok {
		return false
	}
	e.afterEdits(edits)
	e.ClearMark()
	e.cursor = e.snap(cur)
	e.ensureVisible()
	return true
}

// Redo reapplies the last undone step and reports whether there was one.
func (e *Editor) Redo() bool {
	edits, cur, ok := e.buf.Redo()
	if !ok {
		return false
	}
	e.afterEdits(edits)
	e.ClearMark()
	e.cursor = e.snap(cur)
	e.ensureVisible()
	return true
}

// Reload replaces the buffer with src (the file changed on disk), keeping
// the cursor at the nearest offset. History is cleared.
func (e *Editor) Reload(src []byte) {
	cur := e.cursor
	e.buf.Reset(src)
	e.spans = nil
	e.analysis = Analysis{}
	e.parsed = 0
	e.ClearMark()
	e.cursor = e.snap(cur)
	e.goal = -1
	e.ensureVisible()
}

// byteOrderMark is the UTF-8 encoding of U+FEFF.
const byteOrderMark = "\ufeff"

// ---- #→§ ----

// isTokenByte reports whether c can continue a #N.N[a-z] token while typing.
func isTokenByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '.' || c == '_'
}

// convertHash checks whether text, just typed at off, ends a "#N.N[a-z]"
// token naming an existing section, and if so replaces the '#' with '§' as
// its own undo step.
func (e *Editor) convertHash(off int, text string) {
	if text == "" || isTokenByte(text[0]) || e.analysis.Spec == nil {
		return
	}
	src := e.buf.Bytes()
	i := off
	for i > 0 && ((src[i-1] >= '0' && src[i-1] <= '9') || (src[i-1] >= 'a' && src[i-1] <= 'z') || src[i-1] == '.') {
		i--
	}
	if i == 0 || src[i-1] != '#' {
		return
	}
	hash := i - 1
	if hash > 0 {
		if c := src[hash-1]; (isTokenByte(c) && c != '.') || c == '#' || c == '&' || c == '\\' {
			return
		}
	}
	number := strings.TrimRight(string(src[i:off]), ".")
	if !mdspec.ValidSectionNumber(number) {
		return
	}
	if e.inCode(hash) {
		return
	}
	if st, _, _ := e.analysis.Spec.ResolveSection(e.path, "", number); st != mdspec.Resolved {
		return
	}
	if inOpaque(e.path, src, hash) {
		return
	}
	e.buf.Seal()
	e.replace(pos.Range{Start: hash, End: hash + 1}, "§", KindOther, e.cursor+len("§")-1)
	e.buf.Seal()
	e.flash = "#" + number + " → §" + number + " (^Z undoes)"
}

// inOpaque reports whether off lies in text that is not prose (a code span
// or block, HTML, a link destination) in a fresh parse of src. The
// highlight can be stale or not yet know the construct being typed (the
// closing backtick of `#3`, a fence opened a moment ago, the ")" of
// [x](#3)), so a candidate conversion is checked against the bytes as they
// are now. It parses the whole file (about 2 ms at 1,500 lines), only when a
// #N token naming a section has just been completed.
func inOpaque(path string, src []byte, off int) bool {
	f := mdspec.ParseFile(path, src)
	for _, o := range f.Opaque {
		if off >= o.Range.Start && off < o.Range.End {
			return true
		}
	}
	return false
}

// InProse reports whether off lies in prose: not in a code span or block
// (by the current highlight), nor in anything opaque in a fresh parse of
// the buffer (a construct the highlight does not know yet).
func (e *Editor) InProse(off int) bool {
	return !e.inCode(off) && !inOpaque(e.path, e.buf.Bytes(), off)
}

// inCode reports whether off lies in a code span or block, by the current
// highlight.
func (e *Editor) inCode(off int) bool {
	for _, s := range e.spans {
		if s.Flags&FCode != 0 && off >= s.Start && off < s.End {
			return true
		}
	}
	return false
}

// ---- find ----

// Find returns the next occurrence of query at or after from, wrapping
// around, matching case-insensitively for an all-lowercase query.
func (e *Editor) Find(query string, from int) (pos.Range, bool) {
	if query == "" {
		return pos.Range{}, false
	}
	src := e.buf.Bytes()
	hay, needle := src, []byte(query)
	if strings.ToLower(query) == query {
		hay = bytes.ToLower(src)
		if len(hay) != len(src) {
			hay = src // case folding changed lengths; match exactly
		}
	}
	from = min(max(from, 0), len(src))
	if i := bytes.Index(hay[from:], needle); i >= 0 {
		return pos.Range{Start: from + i, End: from + i + len(needle)}, true
	}
	if i := bytes.Index(hay[:min(from+len(needle), len(hay))], needle); i >= 0 {
		return pos.Range{Start: i, End: i + len(needle)}, true
	}
	return pos.Range{}, false
}

// ---- mouse ----

// OffsetAtCell returns the byte offset under pane cell (x, y), and false
// for a cell below the end of the buffer. A cell in the gutter maps to the
// start of the row.
func (e *Editor) OffsetAtCell(x, y int) (int, bool) {
	if y < 0 || y >= e.height {
		return 0, false
	}
	p := e.top
	for k := 0; k < y; k++ {
		var ok bool
		if p, ok = e.next(p); !ok {
			return 0, false
		}
	}
	r := e.rowAt(p)
	col := x - e.GutterWidth()
	if col < 0 {
		return r.Start, true
	}
	return r.OffsetAt(col), true
}

// RowAtCell returns the display row at pane row y, and false below the end
// of the buffer.
func (e *Editor) RowAtCell(y int) (Row, bool) {
	if y < 0 || y >= e.height {
		return Row{}, false
	}
	p := e.top
	for k := 0; k < y; k++ {
		var ok bool
		if p, ok = e.next(p); !ok {
			return Row{}, false
		}
	}
	return e.rowAt(p), true
}

// CellOf returns the pane cell where off is displayed, and false when it is
// off screen.
func (e *Editor) CellOf(off int) (int, int, bool) {
	target := e.rowOf(off)
	p := e.top
	for y := 0; y < e.height; y++ {
		if p == target {
			c, _ := e.rowAt(p).ColOf(off)
			return e.GutterWidth() + c, y, true
		}
		var ok bool
		if p, ok = e.next(p); !ok {
			break
		}
	}
	return 0, 0, false
}

// CursorCell returns the cursor's pane cell.
func (e *Editor) CursorCell() (int, int, bool) {
	return e.CellOf(e.cursor)
}

// Click places the cursor at pane cell (x, y) and starts a drag there.
func (e *Editor) Click(x, y int) bool {
	off, ok := e.OffsetAtCell(x, y)
	if !ok {
		off = e.buf.Len()
	}
	e.ClearMark()
	e.SetCursor(off)
	e.dragFrom = e.cursor
	return true
}

// Drag extends a selection from the click to pane cell (x, y).
func (e *Editor) Drag(x, y int) {
	if e.dragFrom < 0 {
		return
	}
	off, ok := e.OffsetAtCell(min(max(x, 0), e.width-1), min(max(y, 0), e.height-1))
	if !ok {
		off = e.buf.Len()
	}
	if y < 0 {
		e.ScrollRows(-1)
	} else if y >= e.height {
		e.ScrollRows(1)
	}
	e.mark = e.dragFrom
	e.sticky = false
	e.cursor = e.snap(off)
	e.goal = -1
	e.ensureVisible()
}

// Release ends a drag.
func (e *Editor) Release() {
	e.dragFrom = -1
}

// ---- screen ----

// Run is a stretch of glyphs with the same highlight.
type Run struct {
	Text  string
	Flags Flag
	Cells int
}

// ScreenRow is one row of the pane, ready to paint.
type ScreenRow struct {
	// Row is the display row; Valid is false below the end of the buffer.
	Row   Row
	Valid bool
	// LineNo is the 1-based line number on a line's first row, else 0.
	LineNo int
	// Marker is the most severe diagnostic on the line ("" for none),
	// shown on the line's first row.
	Marker diag.Severity
	// CursorLine is true on rows of the cursor's logical line.
	CursorLine bool
	Runs       []Run
}

// Screen lays out the visible rows with their highlight runs.
func (e *Editor) Screen() []ScreenRow {
	out := make([]ScreenRow, 0, e.height)
	var rows []Row
	p := e.top
	for len(rows) < e.height {
		rows = append(rows, e.rowAt(p))
		var ok bool
		if p, ok = e.next(p); !ok {
			break
		}
	}
	var vis []Span
	if len(rows) > 0 {
		vs := e.lineRange(rows[0].Line).Start
		ve := e.lineRange(rows[len(rows)-1].Line).End
		for _, s := range e.spans {
			if s.End >= vs && s.Start <= ve {
				vis = append(vis, s)
			}
		}
		for _, s := range e.extra {
			if s.End > s.Start && s.End >= vs && s.Start <= ve {
				vis = append(vis, s)
			}
		}
	}
	if sel, ok := e.Selection(); ok {
		vis = append(vis, Span{Start: sel.Start, End: sel.End, Flags: FSelect})
	}
	if e.match.End > e.match.Start {
		vis = append(vis, Span{Start: e.match.Start, End: e.match.End, Flags: FMatch})
	}
	curLine := e.lineOf(e.cursor)
	for _, r := range rows {
		sr := ScreenRow{Row: r, Valid: true, CursorLine: r.Line == curLine}
		if r.Sub == 0 {
			sr.LineNo = r.Line + 1
			sr.Marker = e.marker(vis, e.lineRange(r.Line))
		}
		sr.Runs = runsOf(r, vis)
		out = append(out, sr)
	}
	for len(out) < e.height {
		out = append(out, ScreenRow{})
	}
	return out
}

// marker returns the most severe diagnostic class on line range lr.
func (e *Editor) marker(vis []Span, lr pos.Range) diag.Severity {
	var best diag.Severity
	for _, s := range vis {
		if s.Start >= max(lr.End, lr.Start+1) || s.End <= lr.Start {
			continue
		}
		switch {
		case s.Flags&FDiagError != 0:
			return diag.Error
		case s.Flags&FDiagWarn != 0:
			best = diag.Warning
		case s.Flags&FDiagHint != 0 && best == "":
			best = diag.Hint
		}
	}
	return best
}

// runsOf groups a row's glyphs into runs of equal flags.
func runsOf(r Row, vis []Span) []Run {
	var local []Span
	for _, s := range vis {
		if s.End > r.Start && s.Start < max(r.End, r.Start+1) {
			local = append(local, s)
		}
	}
	var out []Run
	var b strings.Builder
	var cur Flag
	cells := 0
	flush := func() {
		if b.Len() > 0 {
			out = append(out, Run{Text: b.String(), Flags: cur, Cells: cells})
			b.Reset()
			cells = 0
		}
	}
	for _, g := range r.Glyphs {
		var fl Flag
		if g.Ghost {
			fl = g.GhostFlags
			if fl == 0 {
				fl = FRemoved
			}
		} else {
			for _, s := range local {
				if s.Start < g.End() && s.End > g.Off {
					fl |= s.Flags
				}
			}
		}
		if fl != cur {
			flush()
			cur = fl
		}
		b.WriteString(g.Text)
		cells += g.W
	}
	flush()
	return out
}
