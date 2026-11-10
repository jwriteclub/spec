// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"hash/maphash"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aymanbagabas/go-udiff"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// ^] stops on three kinds of edit since HEAD, told apart (step 4e
// follow-up): a changed anchor, prose whose words changed, and a
// paragraph that only changed in whitespace or wrapping (^F, spec fmt).
// Words are compared with whitespace and line breaks ignored, so a
// reflowed paragraph is "format only" and a real edit inside it still
// shows as the words it changed.

// contentEdit is a run of words of the current text that differs from
// HEAD, compared word by word (whitespace and line breaks ignored).
type contentEdit struct {
	// start and end are the run's byte range in the current text; equal
	// where words were only removed (the place they were).
	start, end int
	// oldStart is where the run starts in HEAD's text.
	oldStart int
	// ghost is the removed words as they are drawn when removals are
	// shown, before the byte at ghostAt ("" when nothing was removed).
	ghost   string
	ghostAt int
	// added are the current words of the run, removed the HEAD ones.
	added, removed []string
}

// specEdit is one ^] stop: a changed anchor, or a changed prose paragraph.
type specEdit struct {
	path string
	// start is where the stop lands: an anchor's label, the first changed
	// word of prose, the first reflowed line; line is its line.
	start int
	line  int
	// kind is stopAnchor, stopProse or stopFormat.
	kind int
	// label says what changed: "inv $Name changed: added ($Time.Wall)",
	// "prose in §3: replaced 5 with 10", "format only in §2 (whitespace
	// or wrapping)".
	label string
}

// words splits s into its maximal runs of non-space characters, as byte
// ranges.
func words(s string) [][2]int {
	var out [][2]int
	start := -1
	for i, r := range s {
		if unicode.IsSpace(r) {
			if start >= 0 {
				out = append(out, [2]int{start, i})
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}

// contentEdits compares old and cur word by word and returns the runs of
// cur that differ, in order.
func contentEdits(old, cur string) []contentEdit {
	ow, cw := words(old), words(cur)
	// One word per line, so a line diff is a word diff.
	join := func(s string, ws [][2]int) (string, []int) {
		var b strings.Builder
		starts := make([]int, len(ws))
		for k, w := range ws {
			starts[k] = b.Len()
			b.WriteString(s[w[0]:w[1]])
			b.WriteByte('\n')
		}
		return b.String(), starts
	}
	oj, ostarts := join(old, ow)
	cj, _ := join(cur, cw)
	index := func(off int) int { return sort.SearchInts(ostarts, off) }
	text := func(s string, ws [][2]int, from, to int) []string {
		var out []string
		for _, w := range ws[from:to] {
			out = append(out, s[w[0]:w[1]])
		}
		return out
	}
	var out []contentEdit
	delta := 0 // cur word index minus old word index after the edits so far
	for _, e := range udiff.Lines(oj, cj) {
		o0, o1 := index(e.Start), index(e.End)
		n := strings.Count(e.New, "\n")
		c0 := o0 + delta
		c1 := c0 + n
		delta += n - (o1 - o0)
		ce := contentEdit{added: text(cur, cw, c0, c1), removed: text(old, ow, o0, o1), oldStart: len(old)}
		if o0 < len(ow) {
			ce.oldStart = ow[o0][0]
		}
		gone := strings.Join(ce.removed, " ")
		switch {
		case n > 0:
			ce.start, ce.end = cw[c0][0], cw[c1-1][1]
			if gone != "" {
				ce.ghostAt, ce.ghost = ce.start, gone+" "
			}
		case c0 > 0:
			// Words only removed: they were after the word before.
			ce.start = cw[c0-1][1]
			ce.end, ce.ghostAt, ce.ghost = ce.start, ce.start, " "+gone
		case c0 < len(cw):
			ce.start = cw[c0][0]
			ce.end, ce.ghostAt, ce.ghost = ce.start, ce.start, gone+" "
		default:
			ce.start, ce.end, ce.ghostAt, ce.ghost = len(cur), len(cur), len(cur), gone
		}
		out = append(out, ce)
	}
	return out
}

// describe says what the edits es did in a few words: "added X", "removed
// Y", "replaced Y with X", and how many more there are.
func describe(es []contentEdit, themes ...*Theme) string {
	th := uiTheme(themes)
	if len(es) == 0 {
		return ""
	}
	e := es[0]
	quote := func(ws []string) string {
		s := strings.Join(ws, " ")
		if utf8.RuneCountInString(s) > 32 {
			r := []rune(s)
			s = string(r[:31]) + th.uiText("…")
		}
		return s
	}
	var s string
	switch {
	case len(e.removed) == 0:
		s = "added " + quote(e.added)
	case len(e.added) == 0:
		s = "removed " + quote(e.removed)
	default:
		s = "replaced " + quote(e.removed) + " with " + quote(e.added)
	}
	if len(es) > 1 {
		s += fmt.Sprintf(" (+%d more)", len(es)-1)
	}
	return s
}

// fileEdits is the comparison of one file's buffer with HEAD.
type fileEdits struct {
	// content are the word runs that differ.
	content []contentEdit
	// format are the 1-based lines that differ from HEAD without a word
	// of theirs changed (whitespace and wrapping only), in order.
	format []int
	// formatBlocks are the byte ranges of the paragraphs those lines are
	// in that changed no word (one ^] stop each).
	formatBlocks [][2]int
}

// blocks splits s into paragraphs: each a run of non-blank lines with the
// blank lines after it (the first one also takes the blank lines before
// it), as byte ranges covering s.
func blocks(s string) [][2]int {
	var out [][2]int
	start, inBlank := 0, false
	seenText := false
	for off := 0; off < len(s); {
		end := strings.IndexByte(s[off:], '\n')
		if end < 0 {
			end = len(s)
		} else {
			end += off + 1
		}
		blank := strings.TrimSpace(s[off:end]) == ""
		if !blank && inBlank && seenText {
			out = append(out, [2]int{start, off})
			start = off
		}
		if !blank {
			seenText = true
		}
		inBlank = blank
		off = end
	}
	if start < len(s) || len(out) == 0 {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}

// formatOnly finds what changed between old and cur in whitespace or
// wrapping only: the paragraphs are diffed as units, and within each
// changed run of paragraphs the lines are; a changed line holding none of
// the changed words (content) is format only. It returns those lines
// (1-based) and the paragraphs of cur they are in that hold no changed
// word.
func formatOnly(old, cur string, content []contentEdit) ([]int, [][2]int) {
	ob, cb := blocks(old), blocks(cur)
	enc := func(s string, bs [][2]int) (string, []int) {
		var b strings.Builder
		starts := make([]int, len(bs))
		for k, r := range bs {
			starts[k] = b.Len()
			// The blank lines after a paragraph do not count: adding a
			// paragraph adds one, and that is the new paragraph's edit.
			b.WriteString(strings.ReplaceAll(strings.TrimRight(s[r[0]:r[1]], " \t\r\n"), "\n", "\x00"))
			b.WriteByte('\n')
		}
		return b.String(), starts
	}
	oj, ostarts := enc(old, ob)
	cj, _ := enc(cur, cb)
	x := pos.NewIndex([]byte(cur))
	wordLines := map[int]bool{}
	for _, e := range content {
		for l := x.Line(e.start); l <= x.Line(max(e.end-1, e.start)); l++ {
			wordLines[l] = true
		}
	}
	var lines []int
	var paras [][2]int
	delta := 0
	for _, e := range udiff.Lines(oj, cj) {
		o0, o1 := sort.SearchInts(ostarts, e.Start), sort.SearchInts(ostarts, e.End)
		n := strings.Count(e.New, "\n")
		c0 := o0 + delta
		c1 := c0 + n
		delta += n - (o1 - o0)
		if n == 0 {
			continue // paragraphs removed: their words are content edits
		}
		oText := ""
		if o1 > o0 {
			oText = old[ob[o0][0]:ob[o1-1][1]]
		}
		base := cb[c0][0]
		cText := cur[base:cb[c1-1][1]]
		touched := map[int]bool{} // blocks with a format-only line
		for _, run := range changedRuns(oText, cText) {
			r0, r1 := base+run[0], base+max(run[1], run[0]+1)
			for l := x.Line(min(r0, len(cur))); l <= x.Line(min(max(r1-1, r0), len(cur))); l++ {
				if wordLines[l] || slices.Contains(lines, l) {
					continue
				}
				lines = append(lines, l)
				off := x.LineStart(l)
				for k := c0; k < c1; k++ {
					if off >= cb[k][0] && off < max(cb[k][1], cb[k][0]+1) {
						touched[k] = true
					}
				}
			}
		}
		for k := c0; k < c1; k++ {
			if !touched[k] {
				continue
			}
			words := slices.ContainsFunc(content, func(e contentEdit) bool { return e.start >= cb[k][0] && e.start < cb[k][1] })
			if !words {
				paras = append(paras, cb[k])
			}
		}
	}
	slices.Sort(lines)
	return lines, paras
}

// editCacheEntry caches a file's fileEdits for one pair of texts.
type editCacheEntry struct {
	old, cur uint64
	edits    fileEdits
}

// editSeed hashes the texts the cache is keyed by.
var editSeed = maphash.MakeSeed()

// headSource returns path's text at HEAD ("" for a file new since HEAD),
// and false when there is no baseline to compare with.
func (a *App) headSource(path string) (string, bool) {
	b := a.ws.Base
	if b == nil || b.State == baseline.StateUnavailable || (b.State != baseline.StateUnborn && b.Spec == nil) {
		return "", false
	}
	if b.State != baseline.StateUnborn {
		if of := b.Spec.File(path); of != nil {
			return string(of.Source()), true
		}
	}
	return "", true
}

// fileEditsOf compares path's buffer src with HEAD (cached: the gutter
// asks on every frame), and false when there is no baseline.
func (a *App) fileEditsOf(path, src string) (fileEdits, bool) {
	old, ok := a.headSource(path)
	if !ok {
		return fileEdits{}, false
	}
	ho, hc := maphash.String(editSeed, old), maphash.String(editSeed, src)
	if e, ok := a.editCache[path]; ok && e.old == ho && e.cur == hc {
		return e.edits, true
	}
	fe := fileEdits{content: contentEdits(old, src)}
	fe.format, fe.formatBlocks = formatOnly(old, src, fe.content)
	if a.editCache == nil {
		a.editCache = map[string]editCacheEntry{}
	}
	a.editCache[path] = editCacheEntry{old: ho, cur: hc, edits: fe}
	return fe, true
}

// changedRuns returns the byte ranges in cur of the runs of lines that
// differ from old; a run where lines were only deleted is empty.
func changedRuns(old, cur string) [][2]int {
	var out [][2]int
	delta := 0 // cur offset minus old offset after the edits so far
	for _, e := range udiff.Lines(old, cur) {
		start := e.Start + delta
		end := start + len(e.New)
		delta += len(e.New) - (e.End - e.Start)
		if n := len(out); n > 0 && start <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], end)
			continue
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

// anchorSpan is an anchor's range, label through body.
func anchorSpan(an *mdspec.Anchor) [2]int {
	return [2]int{an.LabelRange.Start, max(an.BodyRange.End, an.LabelRange.End)}
}

// editAnchor is the anchor of f (the buffer's analysis) that word run e
// belongs to: the one it is in, or for words only removed, the one they
// were in at HEAD (head, nil when the file is new), found by name. nil
// when it is prose.
func (a *App) editAnchor(f, head *mdspec.File, e contentEdit) *mdspec.Anchor {
	if len(e.added) == 0 && head != nil {
		if old := head.AnchorAt(e.oldStart); old != nil && e.oldStart < anchorSpan(old)[1] {
			for _, an := range f.Anchors {
				if an.Name == old.Name {
					return an
				}
			}
		}
	}
	if an := f.AnchorAt(e.start); an != nil && e.start < anchorSpan(an)[1] {
		return an
	}
	return nil
}

// headingBefore returns the text of the last ATX heading that starts
// before off in src ("" when none).
func headingBefore(src string, off int) string {
	text := ""
	for start := 0; start < off && start < len(src); {
		end := strings.IndexByte(src[start:], '\n')
		if end < 0 {
			end = len(src)
		} else {
			end += start
		}
		if line := src[start:end]; strings.HasPrefix(line, "#") {
			text = strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
		start = end + 1
	}
	return text
}

// paragraphOf returns the blank-line-delimited block of src around off.
func paragraphOf(src string, off int) [2]int {
	off = min(max(off, 0), len(src))
	start := 0
	if i := strings.LastIndex(src[:off], "\n\n"); i >= 0 {
		start = i + 2
	}
	end := len(src)
	if i := strings.Index(src[off:], "\n\n"); i >= 0 {
		end = off + i
	}
	return [2]int{start, end}
}

// Kinds of ^] stop.
const (
	stopAnchor = iota // an anchor whose hash differs from HEAD
	stopProse         // prose whose words changed
	stopFormat        // whitespace or wrapping only
)

// edits returns the ^] stops in every spec file, in project order, of
// three kinds that never merge: one per anchor that differs from HEAD (new
// or changed), at its label; one per prose paragraph whose words changed,
// at the first changed word; and one per paragraph that differs only in
// whitespace or wrapping (format only). Each file's buffer is compared
// when it is open (unsaved edits count). When there is nothing to compare
// against it returns a message saying why instead.
func (a *App) edits() (out []specEdit, why string) {
	b := a.ws.Base
	switch {
	case b == nil:
		return nil, "No HEAD baseline yet: edits are changes since HEAD"
	case b.State == baseline.StateUnavailable || (b.State != baseline.StateUnborn && b.Spec == nil):
		return nil, "No HEAD baseline: " + b.Reason
	}
	a.fresh()
	for _, p := range a.ws.Paths() {
		f := a.ws.File(p)
		if f == nil {
			continue
		}
		src := string(a.ws.Source(p))
		fe, ok := a.fileEditsOf(p, src)
		if !ok {
			continue
		}
		x := f.Index()
		var head *mdspec.File
		if b.State != baseline.StateUnborn {
			head = b.Spec.File(p)
		}
		// Word runs by anchor (a word run in an anchor the hash calls
		// unchanged still stops there), the rest by paragraph.
		byAnchor := map[*mdspec.Anchor][]contentEdit{}
		paras := map[[2]int][]contentEdit{}
		var paraOrder [][2]int
		for _, e := range fe.content {
			if an := a.editAnchor(f, head, e); an != nil {
				byAnchor[an] = append(byAnchor[an], e)
				continue
			}
			pr := paragraphOf(src, e.start)
			if paras[pr] == nil {
				paraOrder = append(paraOrder, pr)
			}
			paras[pr] = append(paras[pr], e)
		}
		var stopped [][2]int // anchors and paragraphs with a content stop
		for _, an := range f.Anchors {
			es := byAnchor[an]
			st := a.changeState(an)
			if st == "" && len(es) == 0 {
				continue
			}
			if st == "" {
				st = "edited"
			}
			at := an.LabelRange.Start
			if at > len(src) {
				continue
			}
			label := fmt.Sprintf("%s $%s %s", an.Kind, an.Name, st)
			if len(es) > 0 && st != "new" {
				label += ": " + describe(es, a.theme)
			}
			stopped = append(stopped, anchorSpan(an))
			out = append(out, specEdit{path: p, kind: stopAnchor, start: at, line: x.Line(at), label: label})
		}
		for _, pr := range paraOrder {
			es := paras[pr]
			at := min(es[0].start, len(src))
			stopped = append(stopped, pr)
			out = append(out, specEdit{path: p, kind: stopProse, start: at, line: x.Line(at), label: "prose in " + placeName(f, src, at, a.theme) + ": " + describe(es, a.theme)})
		}
		// Format only: one stop per paragraph, unless a changed anchor
		// covers it.
		for _, pr := range fe.formatBlocks {
			at := pr[0]
			for at < pr[1] && at < len(src) && unicode.IsSpace(rune(src[at])) {
				at++ // a leading blank line: stop on the text
			}
			if at > len(src) || slices.ContainsFunc(stopped, func(r [2]int) bool { return r[0] <= at && at <= r[1] }) {
				continue
			}
			where := placeName(f, src, at, a.theme)
			if an := f.AnchorAt(at); an != nil && at < anchorSpan(an)[1] {
				where = "$" + an.Name
			}
			out = append(out, specEdit{path: p, kind: stopFormat, start: at, line: x.Line(at), label: "format only in " + where + " (whitespace or wrapping)"})
		}
	}
	paths := a.ws.Paths()
	slices.SortStableFunc(out, func(x, y specEdit) int {
		if c := slices.Index(paths, x.path) - slices.Index(paths, y.path); c != 0 {
			return c
		}
		return x.start - y.start
	})
	return out, ""
}

// placeName names where offset at of file f (source src) is: its numbered
// section ("§3"), else the heading above it, else its line.
func placeName(f *mdspec.File, src string, at int, themes ...*Theme) string {
	th := uiTheme(themes)
	if s := f.SectionAt(at); s != nil && s.Number != "" {
		return th.uiText("§") + s.Number
	}
	if h := headingBefore(src, at); h != "" {
		return h
	}
	return fmt.Sprintf("line %d", f.Index().Line(at))
}

// jumpStop is a place ^] and F8 stop: an edit since HEAD, or a suggested
// fix of the active proposal.
type jumpStop struct {
	path  string
	start int
	line  int
	edit  bool   // an edit since HEAD, else a fix
	label string // what changed, or the fix's anchor or line
	k, n  int    // its number among the edits, or among the fixes
}

// jumpToEdit jumps to the next edit or suggested fix after the cursor
// (dir 1) or the previous one before it (dir -1), wrapping around, and
// flashes which it is: "Edit 2/5: inv $Name changed: added X", "Fix 1/3
// ($Name)".
func (a *App) jumpToEdit(dir int) {
	if a.jumpToLanded(dir) {
		return
	}
	es, why := a.edits()
	fs := a.fixStops()
	if len(es) == 0 && len(fs) == 0 {
		if why == "" {
			why = "No edits since HEAD"
		}
		a.setFlash("%s", why)
		return
	}
	var stops []jumpStop
	for i, e := range es {
		stops = append(stops, jumpStop{path: e.path, start: e.start, line: e.line, edit: true, label: e.label, k: i + 1, n: len(es)})
	}
	for i, f := range fs {
		stops = append(stops, jumpStop{path: f.path, start: f.start, line: f.line, label: f.label, k: i + 1, n: len(fs)})
	}
	paths := a.ws.Paths()
	key := func(path string, off int) [2]int { return [2]int{slices.Index(paths, path), off} }
	cmp := func(x, y [2]int) int {
		if x[0] != y[0] {
			return x[0] - y[0]
		}
		return x[1] - y[1]
	}
	// Stops are ordered by offset. Forward is the first stop after the
	// cursor; back, the last one before it.
	slices.SortStableFunc(stops, func(x, y jumpStop) int { return cmp(key(x.path, x.start), key(y.path, y.start)) })
	here := a.here()
	at := key(here.Path, here.Off)
	k := -1
	if dir > 0 {
		k = slices.IndexFunc(stops, func(s jumpStop) bool { return cmp(key(s.path, s.start), at) > 0 })
		if k < 0 {
			k = 0
		}
	} else {
		for i := len(stops) - 1; i >= 0; i-- {
			if cmp(key(stops[i].path, stops[i].start), at) < 0 {
				k = i
				break
			}
		}
		if k < 0 {
			k = len(stops) - 1
		}
	}
	s := stops[k]
	a.jump(Location{Path: s.path, Off: s.start})
	if !s.edit {
		a.setFlash("Fix %d/%d (%s)", s.k, s.n, s.label)
		return
	}
	a.setFlash("Edit %d/%d: %s", s.k, s.n, s.label)
}
