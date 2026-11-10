// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package format formats a spec file's whitespace: it wraps paragraphs and
// list-item text to a width, strips trailing whitespace, collapses runs of
// blank lines and ends the file with one newline. It changes whitespace
// only, and proves it: the output is re-parsed and refused unless every
// anchor's hash, the anchor set and the Markdown structure are unchanged.
// `spec fmt` and ^F in `spec ui` both use Source.
package format

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark/ast"

	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// DefaultWidth is the wrap width when spec.toml has no [format] width.
const DefaultWidth = 100

// Options configures Source.
type Options struct {
	// Width is the column paragraphs wrap at; 0 means DefaultWidth.
	Width int
}

// Result is a formatted file.
type Result struct {
	// Text is the formatted source (the input itself when nothing changed).
	Text []byte
	// Changed counts the lines that changed; 0 when the file was already
	// formatted.
	Changed int
}

// RefusedError says why a file was left as it was.
type RefusedError struct {
	Path   string
	Reason string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("%s was not formatted: %s", e.Path, e.Reason)
}

// Source formats the spec file path holding src. On a *RefusedError
// nothing changes: the file has CR line endings or invalid UTF-8, or the
// output failed the safety check.
func Source(path string, src []byte, o Options) (Result, error) {
	width := o.Width
	if width <= 0 {
		width = DefaultWidth
	}
	if bytes.IndexByte(src, '\r') >= 0 {
		return Result{Text: src}, &RefusedError{Path: path, Reason: "it has CR line endings; convert them to LF first"}
	}
	f := mdspec.ParseFile(path, src)
	if !f.ValidUTF8 {
		return Result{Text: src}, &RefusedError{Path: path, Reason: "it is not valid UTF-8"}
	}
	out := newFormatter(f, width).run()
	if bytes.Equal(out, src) {
		return Result{Text: src}, nil
	}
	if reason := Verify(path, src, out); reason != "" {
		return Result{Text: src}, &RefusedError{Path: path, Reason: reason}
	}
	return Result{Text: out, Changed: changedLines(string(src), string(out))}, nil
}

// Verify returns why after is not a whitespace-only reformatting of before
// ("" when it is): the anchors (names, kinds, forms, titles and hashes, in
// order) and the Markdown structure (the node kinds in order, with list
// tightness, heading levels and hard line breaks) must be identical.
func Verify(path string, before, after []byte) string {
	fb, fa := mdspec.ParseFile(path, before), mdspec.ParseFile(path, after)
	if len(fb.Anchors) != len(fa.Anchors) {
		return fmt.Sprintf("the result would have %d anchors instead of %d", len(fa.Anchors), len(fb.Anchors))
	}
	for i, b := range fb.Anchors {
		a := fa.Anchors[i]
		switch {
		case a.Name != b.Name || a.Kind != b.Kind || a.Form != b.Form:
			return fmt.Sprintf("anchor $%s would become %s $%s", b.Name, a.Kind, a.Name)
		case a.Title != b.Title:
			return fmt.Sprintf("the title of $%s would change", b.Name)
		case a.Hash != b.Hash:
			return fmt.Sprintf("the hash of $%s would change", b.Name)
		}
	}
	sb, sa := structure(fb.Document()), structure(fa.Document())
	for i := range min(len(sb), len(sa)) {
		if sb[i] != sa[i] {
			return fmt.Sprintf("the Markdown structure would change (%s becomes %s)", sb[i], sa[i])
		}
	}
	if len(sb) != len(sa) {
		return "the Markdown structure would change"
	}
	return ""
}

// structure lists the node kinds of doc in document order. Text nodes are
// left out, since wrapping splits and joins them, except for hard line
// breaks.
func structure(doc ast.Node) []string {
	var out []string
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Text:
			if v.HardLineBreak() {
				out = append(out, "HardLineBreak")
			}
		case *ast.String:
		case *ast.List:
			out = append(out, fmt.Sprintf("List(tight=%v)", v.IsTight))
		case *ast.Heading:
			out = append(out, fmt.Sprintf("Heading(%d)", v.Level))
		default:
			out = append(out, n.Kind().String())
		}
		return ast.WalkContinue, nil
	})
	return out
}

// changedLines counts the lines that differ: the larger of the lines
// removed and the lines added.
func changedLines(before, after string) int {
	removed, added := 0, 0
	for _, e := range udiff.Lines(before, after) {
		removed += strings.Count(before[e.Start:e.End], "\n")
		added += strings.Count(e.New, "\n")
	}
	return max(removed, added, 1)
}

// formatter formats one parsed file.
type formatter struct {
	f     *mdspec.File
	src   []byte
	idx   *pos.Index
	width int

	keep      map[int]bool // lines left exactly as they are
	hard      map[int]bool // lines that end in a hard line break
	reflow    map[int]paragraph
	protected []pos.Range // sorted, merged: no line break goes in or out
}

// paragraph is a reflowed paragraph: it replaces lines first..last.
type paragraph struct {
	last  int
	lines []string
}

func newFormatter(f *mdspec.File, width int) *formatter {
	return &formatter{
		f: f, src: f.Source(), idx: f.Index(), width: width,
		keep: map[int]bool{}, hard: map[int]bool{}, reflow: map[int]paragraph{},
	}
}

func (m *formatter) run() []byte {
	m.markKept()
	m.markProtected()
	m.markHardBreaks()
	_ = ast.Walk(m.f.Document(), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			m.wrapParagraph(n)
			return ast.WalkSkipChildren, nil
		case *ast.Blockquote:
			return ast.WalkSkipChildren, nil // quotes are left as written
		}
		return ast.WalkContinue, nil
	})
	return m.emit()
}

// markKept marks the lines of code blocks, HTML blocks, front matter and
// link reference definitions: they keep every byte.
func (m *formatter) markKept() {
	for _, op := range m.f.Opaque {
		switch op.Kind {
		case mdspec.OpaqueCodeBlock, mdspec.OpaqueHTMLBlock, mdspec.OpaqueFrontMatter:
		case mdspec.OpaqueLinkDest:
			if !m.atLineStart(op.Range.Start) || m.src[op.Range.Start] != '[' {
				continue // an inline link's destination, inside a paragraph
			}
		default:
			continue
		}
		end := max(op.Range.End-1, op.Range.Start)
		for l := m.idx.Line(op.Range.Start); l <= m.idx.Line(end); l++ {
			m.keep[l] = true
		}
	}
}

// atLineStart reports whether only spaces come before off on its line.
func (m *formatter) atLineStart(off int) bool {
	ls := m.idx.LineStart(m.idx.Line(off))
	return len(bytes.Trim(m.src[ls:off], " ")) == 0
}

// markProtected collects the ranges a wrap never breaks inside or joins:
// inline code, HTML, autolinks, links, $Name and § tokens and anchor
// labels.
func (m *formatter) markProtected() {
	var rs []pos.Range
	for _, op := range m.f.Opaque {
		rs = append(rs, op.Range)
	}
	for _, o := range m.f.Occurrences {
		rs = append(rs, o.Range)
	}
	for _, r := range m.f.SectionRefs {
		rs = append(rs, r.Range)
	}
	rs = append(rs, m.f.MalformedSectionRefs...)
	for _, a := range m.f.Anchors {
		if a.Form == mdspec.FormParagraph {
			rs = append(rs, a.LabelRange)
		}
	}
	_ = ast.Walk(m.f.Document(), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.Link, *ast.Image:
			if r, ok := m.linkRange(n); ok {
				rs = append(rs, r)
			}
		}
		return ast.WalkContinue, nil
	})
	sort.Slice(rs, func(i, j int) bool { return rs[i].Start < rs[j].Start })
	for _, r := range rs {
		if r.End <= r.Start {
			continue
		}
		if k := len(m.protected); k > 0 && r.Start < m.protected[k-1].End {
			m.protected[k-1].End = max(m.protected[k-1].End, r.End)
			continue
		}
		m.protected = append(m.protected, r)
	}
}

// linkRange is a link's source range, from its "[" (or "![") to the end
// of its destination: its text's start back to the bracket, to the end of
// the link destination region mdspec recorded after it.
func (m *formatter) linkRange(n ast.Node) (pos.Range, bool) {
	start, stop := -1, -1
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := c.(*ast.Text); ok && entering {
			if start < 0 || t.Segment.Start < start {
				start = t.Segment.Start
			}
			stop = max(stop, t.Segment.Stop)
		}
		return ast.WalkContinue, nil
	})
	if start < 0 {
		return pos.Range{}, false
	}
	for start > 0 && m.src[start-1] != '[' {
		start--
	}
	start = max(start-1, 0)
	if start > 0 && m.src[start-1] == '!' {
		start--
	}
	end := stop
	for _, op := range m.f.Opaque {
		if op.Kind == mdspec.OpaqueLinkDest && op.Range.Start >= stop && op.Range.Start <= stop+1 {
			end = op.Range.End
			break
		}
	}
	return pos.Range{Start: start, End: end}, true
}

// markHardBreaks marks the lines that end in a hard line break (two
// spaces or a backslash): their trailing spaces are kept.
func (m *formatter) markHardBreaks() {
	_ = ast.Walk(m.f.Document(), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := n.(*ast.Text); ok && entering && t.HardLineBreak() {
			m.hard[m.idx.Line(t.Segment.Stop)] = true
		}
		return ast.WalkContinue, nil
	})
}

// wrapParagraph reflows paragraph n when it sits at the top level or in a
// list (not in a quote or a table), recording its new lines.
func (m *formatter) wrapParagraph(n ast.Node) {
	topLevel := n.Parent() != nil && n.Parent().Kind() == ast.KindDocument
	for a := n.Parent(); a != nil && a.Kind() != ast.KindDocument; a = a.Parent() {
		if a.Kind() != ast.KindList && a.Kind() != ast.KindListItem {
			return
		}
	}
	ls := n.Lines()
	if ls == nil || ls.Len() == 0 {
		return
	}
	for i := 0; i < ls.Len(); i++ {
		if ls.At(i).Padding > 0 {
			return // tab-expanded indentation: left as written
		}
	}
	start := ls.At(0).Start
	end := ls.At(ls.Len() - 1).Stop
	for end > start && isSpace(m.src[end-1]) {
		end--
	}
	if end <= start {
		return
	}
	first, last := m.idx.Line(start), m.idx.Line(end-1)
	for l := first; l <= last; l++ {
		if m.keep[l] {
			return
		}
	}
	lineStart := m.idx.LineStart(first)
	if first == 1 && m.f.HasBOM {
		lineStart += 3
	}
	prefix := string(m.src[lineStart:start])
	if strings.ContainsRune(prefix, '\t') {
		return
	}
	indent := ""
	if !topLevel {
		indent = strings.Repeat(" ", ansi.StringWidth(prefix))
	}
	m.reflow[first] = paragraph{last: last, lines: m.wrap(m.words(start, end), prefix, indent)}
}

// word is one unbreakable stretch of a paragraph. hard holds the bytes
// before the newline of a hard line break that follows it ("  ", or ""
// after a backslash); brk is true when there is one.
type word struct {
	text string
	hard string
	brk  bool
}

// words splits src[start:end] at whitespace outside protected ranges.
func (m *formatter) words(start, end int) []word {
	var out []word
	var cur []byte
	k := sort.Search(len(m.protected), func(i int) bool { return m.protected[i].End > start })
	for i := start; i < end; {
		for k < len(m.protected) && m.protected[k].End <= i {
			k++
		}
		if k < len(m.protected) && m.protected[k].Start <= i {
			stop := min(m.protected[k].End, end)
			cur = append(cur, m.src[i:stop]...)
			i = stop
			continue
		}
		c := m.src[i]
		if !isSpace(c) {
			cur = append(cur, c)
			i++
			continue
		}
		if len(cur) > 0 {
			out = append(out, word{text: string(cur)})
			cur = nil
		}
		j := i
		for j < end && isSpace(m.src[j]) && m.src[j] != '\n' {
			j++
		}
		if j < end && m.src[j] == '\n' && m.hard[m.idx.Line(j)] && len(out) > 0 {
			w := &out[len(out)-1]
			w.hard, w.brk = string(m.src[i:j]), true
		}
		for j < end && isSpace(m.src[j]) {
			j++
		}
		i = j
	}
	if len(cur) > 0 {
		out = append(out, word{text: string(cur)})
	}
	return out
}

// wrap fills lines of at most m.width columns with ws: the first line
// starts with prefix, the others with indent. A word wider than the line
// gets a line of its own; a word that would turn a line into a list item,
// heading, quote, table, fence or HTML stays on the line before.
func (m *formatter) wrap(ws []word, prefix, indent string) []string {
	var lines []string
	var b strings.Builder
	b.WriteString(prefix)
	col := ansi.StringWidth(prefix)
	empty := true
	for i, w := range ws {
		head, _, multi := strings.Cut(w.text, "\n")
		switch {
		case empty:
		case col+1+ansi.StringWidth(head) <= m.width || !breakBefore(w.text) || !breakAfter(ws[i-1].text):
			b.WriteByte(' ')
			col++
		default:
			lines = append(lines, b.String())
			b.Reset()
			b.WriteString(indent)
			col = len(indent)
		}
		b.WriteString(w.text)
		if multi {
			col = ansi.StringWidth(w.text[strings.LastIndexByte(w.text, '\n')+1:])
		} else {
			col += ansi.StringWidth(w.text)
		}
		empty = false
		if w.brk {
			b.WriteString(w.hard)
			lines = append(lines, b.String())
			b.Reset()
			b.WriteString(indent)
			col, empty = len(indent), true
		}
	}
	if !empty {
		lines = append(lines, b.String())
	}
	return lines
}

// lineStarts are the words that make a line something other than paragraph
// text: list markers, headings, quotes, tables, fences, setext underlines,
// thematic breaks and HTML.
var lineStarts = regexp.MustCompile("^(?:[-+*]$|[0-9]{1,9}[.)]$|#{1,6}$|[-=]+$|[*_]{3,}$|[>|<]|```|~~~|[-:|]*-[-:|]*$)")

// breakBefore reports whether a line may start with w.
func breakBefore(w string) bool {
	return !lineStarts.MatchString(w)
}

// breakAfter reports whether a line may end with w: not with a backslash,
// which would make a hard line break.
func breakAfter(w string) bool {
	return !strings.HasSuffix(w, "\\")
}

// emit writes the formatted file: reflowed paragraphs, kept lines as they
// are, other lines without trailing whitespace; runs of blank lines become
// one, and the file ends with one newline.
func (m *formatter) emit() []byte {
	var out bytes.Buffer
	if m.f.HasBOM {
		out.WriteString("\xef\xbb\xbf")
	}
	blank := true // at the start, a blank line is dropped
	n := m.idx.LineCount()
	for l := 1; l <= n; l++ {
		if p, ok := m.reflow[l]; ok {
			for _, s := range p.lines {
				out.WriteString(s)
				out.WriteByte('\n')
			}
			l, blank = p.last, false
			continue
		}
		r := m.idx.LineRange(l)
		start := r.Start
		if l == 1 && m.f.HasBOM {
			start += 3
		}
		text := m.src[start:r.End]
		if m.keep[l] {
			out.Write(text)
			out.WriteByte('\n')
			blank = false
			continue
		}
		if !m.hard[l] && !m.insideProtected(r.End) {
			text = bytes.TrimRight(text, " \t")
		}
		if len(bytes.TrimSpace(text)) == 0 && len(text) == 0 {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out.Write(text)
		out.WriteByte('\n')
	}
	b := out.Bytes()
	bom := 0
	if m.f.HasBOM {
		bom = 3
	}
	for len(b) > bom && b[len(b)-1] == '\n' && (len(b) == bom+1 || b[len(b)-2] == '\n') {
		b = b[:len(b)-1]
	}
	if len(b) == bom+1 && b[bom] == '\n' {
		b = b[:bom]
	}
	if len(b) == bom {
		return b
	}
	if b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	return b
}

// insideProtected reports whether off (a line end) is inside a protected
// range, such as a code span that continues on the next line.
func (m *formatter) insideProtected(off int) bool {
	k := sort.Search(len(m.protected), func(i int) bool { return m.protected[i].End > off })
	return k < len(m.protected) && m.protected[k].Start < off
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n'
}
