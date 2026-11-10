// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/termtext"
)

// wordHighlightMax is the share of a changed block above which its
// changed words are not highlighted (everything changed: the line colors
// say it all).
const wordHighlightMax = 0.7

// diffLines renders the change from old to new as a unified diff: lines
// both share are shown dim with "  ", removed lines in red with "- ",
// added lines in green with "+ ", and within a changed block the changed
// words are highlighted. Every line is wrapped to fit w cells after
// indent.
func diffLines(th *Theme, old, new string, w int, indent string) []string {
	old = strings.ReplaceAll(old, "\r\n", "\n")
	new = strings.ReplaceAll(new, "\r\n", "\n")
	old, new = termtext.Clean(old), termtext.Clean(new)
	if old != "" && !strings.HasSuffix(old, "\n") {
		old += "\n"
	}
	if new != "" && !strings.HasSuffix(new, "\n") {
		new += "\n"
	}
	var out []string
	emit := func(text, sign string, base, word lipgloss.Style, marks [][2]int) {
		out = append(out, styleWrapped(text, w, indent+sign, indent+"  ", base, word, marks)...)
	}
	context := func(text string) {
		for _, l := range splitLines(text) {
			emit(l, "  ", th.PopupDim, th.PopupDim, nil)
		}
	}
	block := func(o, n string) {
		delMarks, insMarks := wordMarks(o, n)
		off := 0
		for _, l := range splitLines(o) {
			emit(l, "- ", th.DiffDel, th.DiffDelWord, shiftMarks(delMarks, off, len(l)))
			off += len(l) + 1
		}
		off = 0
		for _, l := range splitLines(n) {
			emit(l, "+ ", th.DiffAdd, th.DiffAddWord, shiftMarks(insMarks, off, len(l)))
			off += len(l) + 1
		}
	}
	at := 0
	for _, e := range udiff.Lines(old, new) {
		context(old[at:e.Start])
		block(old[e.Start:e.End], e.New)
		at = e.End
	}
	context(old[at:])
	return out
}

// splitLines splits text into its lines, without the line breaks.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// wordMarks returns the byte ranges of o that were removed and of n that
// were added, widened to whole words; none when most of the block
// changed.
func wordMarks(o, n string) (del, ins [][2]int) {
	if o == "" || n == "" {
		return nil, nil
	}
	delta := 0
	changed := 0
	for _, e := range udiff.Strings(o, n) {
		if e.End > e.Start {
			del = append(del, widen(o, e.Start, e.End))
		}
		ns := e.Start + delta
		if len(e.New) > 0 {
			ins = append(ins, widen(n, ns, ns+len(e.New)))
		}
		delta += len(e.New) - (e.End - e.Start)
		changed += max(e.End-e.Start, len(e.New))
	}
	if float64(changed) > wordHighlightMax*float64(max(len(o), len(n))) {
		return nil, nil
	}
	return del, ins
}

// widen grows [s, e) of text to word boundaries.
func widen(text string, s, e int) [2]int {
	for s > 0 && isWordByte(text[s-1]) {
		s--
	}
	for e < len(text) && isWordByte(text[e]) {
		e++
	}
	return [2]int{s, e}
}

// isWordByte reports whether c belongs to a word (non-ASCII bytes do).
func isWordByte(c byte) bool {
	return c >= utf8.RuneSelf || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// shiftMarks returns the parts of marks inside the line at [off, off+n),
// relative to the line.
func shiftMarks(marks [][2]int, off, n int) [][2]int {
	var out [][2]int
	for _, m := range marks {
		s, e := max(m[0], off), min(m[1], off+n)
		if s < e {
			out = append(out, [2]int{s - off, e - off})
		}
	}
	return out
}

// styleWrapped wraps line to w cells (first after first, the rest after
// cont), styling it with base and the marked byte ranges with word.
func styleWrapped(line string, w int, first, cont string, base, word lipgloss.Style, marks [][2]int) []string {
	// A tab would be drawn wider than the one cell it is counted as.
	line = strings.ReplaceAll(line, "\t", " ")
	room := max(w-ansi.StringWidth(first), 1)
	segs := wrapOffsets(line, room)
	out := make([]string, 0, len(segs))
	for k, sg := range segs {
		prefix := cont
		if k == 0 {
			prefix = first
		}
		out = append(out, prefix+styleRange(line, sg[0], sg[1], base, word, marks))
	}
	return out
}

// styleRange styles line[s:e], marked bytes with word, the rest with base.
func styleRange(line string, s, e int, base, word lipgloss.Style, marks [][2]int) string {
	var b strings.Builder
	at := s
	for _, m := range marks {
		ms, me := max(m[0], s), min(m[1], e)
		if ms >= me || ms < at {
			continue
		}
		if ms > at {
			b.WriteString(base.Render(line[at:ms]))
		}
		b.WriteString(word.Render(line[ms:me]))
		at = me
	}
	if at < e {
		b.WriteString(base.Render(line[at:e]))
	}
	return b.String()
}

// wrapOffsets splits line into byte ranges of at most w cells, breaking
// after spaces where it can. An empty line is one empty range.
func wrapOffsets(line string, w int) [][2]int {
	if line == "" {
		return [][2]int{{0, 0}}
	}
	var out [][2]int
	start := 0
	for start < len(line) {
		width := 0
		end := start
		lastBreak := -1
		for end < len(line) {
			r, n := utf8.DecodeRuneInString(line[end:])
			cw := ansi.StringWidth(string(r))
			if width+cw > w {
				break
			}
			width += cw
			end += n
			if r == ' ' {
				lastBreak = end
			}
		}
		if end < len(line) && lastBreak > start {
			end = lastBreak
		}
		if end == start {
			// One glyph wider than the room: take it anyway.
			_, n := utf8.DecodeRuneInString(line[start:])
			end = start + n
		}
		out = append(out, [2]int{start, end})
		start = end
	}
	return out
}
