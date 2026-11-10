// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package mdspec

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"go.abhg.dev/goldmark/frontmatter"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/pos"
)

// markdown is the shared goldmark instance: GFM (tables, strikethrough,
// linkify, task lists), front matter, no math. Its parser is safe for
// concurrent use.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM, &frontmatter.Extender{}))

var bom = []byte{0xEF, 0xBB, 0xBF}

const nameRE = `[A-Z][A-Za-z0-9]*(?:\.[A-Z][A-Za-z0-9]*)*`

var (
	// Paragraph-form label, matched against the raw inner source of the
	// strong node (plan 1.3).
	labelRE = regexp.MustCompile(`^(Def[ ]+)?\$(` + nameRE + `)(?:[ ]+\(([^()\n]+)\))?\.?$`)
	// Section-form label, matched against the raw heading text.
	headingLabelRE = regexp.MustCompile(`^(Def[ ]+)?\$(` + nameRE + `)(?::[ ]+([^()\n]+))?$`)
	// A strong node that looks like it was meant as a label (def-malformed):
	// `$` (after an optional Def-like word) followed by a plausible name
	// start, a capital letter or a lowercase dotted name (`$frame.timing`).
	// So `**$5.00 off**`, `**$ amounts**` and `**$x**` are plain text.
	labelLikeRE = regexp.MustCompile(`^(?:(?i:def(?:inition)?)[.:]?[ ]+)?\$(?:[A-Z]|[a-z][A-Za-z0-9_]*\.[A-Za-z])`)
	// Numbered section heading (plan 1.6): N. | N[a-z]. | N(.N)+[a-z]? with an
	// optional trailing period, then spaces and a title.
	sectionHeadingRE = regexp.MustCompile(`^([0-9]+[a-z]?\.|[0-9]+(?:\.[0-9]+)+[a-z]?\.?)[ ]+(\S[\s\S]*)$`)
)

// ParseFile parses one spec file. It never fails: problems become
// diagnostics on the returned File. path is the project-relative slash path
// recorded in every range-bearing value; src is copied.
func ParseFile(path string, src []byte) *File {
	s := make([]byte, len(src))
	copy(s, src)
	f := &File{Path: path, src: s, index: pos.NewIndex(s), ValidUTF8: utf8.Valid(s)}
	if !f.ValidUTF8 {
		at := firstInvalidUTF8(s)
		f.Diagnostics = append(f.Diagnostics, diag.Diagnostic{
			Code: diag.CodeFileNotUTF8, Severity: diag.Error, File: path,
			Range:   pos.Range{Start: at, End: at + 1},
			Message: fmt.Sprintf("file is not valid UTF-8 (first bad byte at offset %d); it is left out of the project until fixed", at),
		})
	}
	reader := text.NewReader(s)
	if bytes.HasPrefix(s, bom) {
		f.HasBOM = true
		f.Diagnostics = append(f.Diagnostics, diag.Diagnostic{
			Code: diag.CodeFileBOM, Severity: diag.Warning, File: path,
			Range:   pos.Range{Start: 0, End: len(bom)},
			Message: "file starts with a byte order mark; remove it",
		})
		reader.Advance(len(bom))
	}
	pc := parser.NewContext()
	f.doc = markdown.Parser().Parse(reader, parser.WithContext(pc))

	p := &fileParser{f: f, src: s, idx: f.index, containers: map[ast.Node]Mention{}}
	if frontmatter.Get(pc) != nil {
		p.frontMatter()
	}
	p.visit(f.doc)
	p.headings()
	p.paragraphLabels()
	p.scan()
	p.finish()
	return f
}

func firstInvalidUTF8(b []byte) int {
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
	return len(b)
}

// textSeg is one stretch of inline text outside opaque regions.
type textSeg struct {
	start, stop int
	container   Mention
}

type fileParser struct {
	f   *File
	src []byte
	idx *pos.Index

	segs       []textSeg
	code       []pos.Range // code span contents and code block lines, for hashing
	containers map[ast.Node]Mention
	headingsTL []*ast.Heading // top-level headings in source order
	nameRanges map[int]*Anchor
	sectionAs  []*Anchor // section-form anchors, in source order
	opMaxEnd   []int     // prefix maximum of f.Opaque[k].Range.End
}

// ---- generic position helpers ----

func (p *fileParser) clampOff(off int) int {
	if off < 0 {
		return 0
	}
	if off > len(p.src) {
		return len(p.src)
	}
	return off
}

func isBlankLine(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return false
		}
	}
	return true
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == '\v'
}

// trimRight moves end back over ASCII whitespace, not before start.
func (p *fileParser) trimRight(start, end int) int {
	end = p.clampOff(end)
	for end > start && isSpaceByte(p.src[end-1]) {
		end--
	}
	return end
}

// startOf returns the first source offset of n, or -1 when unknown.
func (p *fileParser) startOf(n ast.Node) int {
	if t, ok := n.(*ast.Text); ok {
		return t.Segment.Start
	}
	if n.Pos() >= 0 {
		return n.Pos()
	}
	if n.Type() == ast.TypeBlock {
		if ls := n.Lines(); ls != nil && ls.Len() > 0 {
			return ls.At(0).Start
		}
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if s := p.startOf(c); s >= 0 {
			return s
		}
	}
	return -1
}

// lastLine returns the highest 1-based line touched by block n or its block
// descendants (its lines and its start position), or 0 when unknown.
func (p *fileParser) lastLine(n ast.Node) int {
	best := 0
	if s := p.startOf(n); s >= 0 {
		best = p.idx.Line(s)
	}
	if ls := n.Lines(); ls != nil && ls.Len() > 0 {
		seg := ls.At(ls.Len() - 1)
		end := seg.Stop
		if end > seg.Start {
			end--
		}
		if l := p.idx.Line(end); l > best {
			best = l
		}
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if c.Type() != ast.TypeBlock {
			continue
		}
		if l := p.lastLine(c); l > best {
			best = l
		}
	}
	return best
}

// nextBlockStart returns the start of the block that follows n in document
// order, or len(src).
func (p *fileParser) nextBlockStart(n ast.Node) int {
	for a := n; a != nil && a.Kind() != ast.KindDocument; a = a.Parent() {
		for sib := a.NextSibling(); sib != nil; sib = sib.NextSibling() {
			if s := p.startOf(sib); s >= 0 {
				return s
			}
		}
	}
	return len(p.src)
}

// blockRange returns the source range of block n: from its start to the end
// of its last line, extended over directly following non-blank lines that
// precede the next block (closing code fences, setext underlines), with
// trailing whitespace trimmed.
func (p *fileParser) blockRange(n ast.Node) pos.Range {
	start := p.startOf(n)
	if start < 0 {
		return pos.Range{}
	}
	start = p.clampOff(start)
	last := p.lastLine(n)
	if last == 0 {
		last = p.idx.Line(start)
	}
	next := p.nextBlockStart(n)
	bound := p.idx.LineCount() + 1
	if next < len(p.src) {
		bound = p.idx.Line(next)
	}
	for last+1 < bound {
		lr := p.idx.LineRange(last + 1)
		if isBlankLine(p.src[lr.Start:lr.End]) {
			break
		}
		last++
	}
	end := p.trimRight(start, p.idx.LineRange(last).End)
	if end < start {
		end = start
	}
	return pos.Range{Start: start, End: end}
}

// linesRange returns the range covered by n's own lines, trimmed.
func (p *fileParser) linesRange(n ast.Node) pos.Range {
	ls := n.Lines()
	if ls == nil || ls.Len() == 0 {
		s := p.startOf(n)
		if s < 0 {
			return pos.Range{}
		}
		return pos.Range{Start: s, End: s}
	}
	start := p.clampOff(ls.At(0).Start)
	end := p.trimRight(start, ls.At(ls.Len()-1).Stop)
	return pos.Range{Start: start, End: end}
}

// inlineEnd returns the end of inline node n (its delimiters included),
// computed from the start of its next sibling, or from its parent's end.
// It returns -1 when the end cannot be determined.
func (p *fileParser) inlineEnd(n ast.Node) int {
	if t, ok := n.(*ast.Text); ok {
		return t.Segment.Stop
	}
	start := p.startOf(n)
	if start < 0 {
		return -1
	}
	for sib := n.NextSibling(); sib != nil; sib = sib.NextSibling() {
		if s := p.startOf(sib); s >= 0 {
			return p.trimRight(start, s)
		}
	}
	parent := n.Parent()
	if parent == nil {
		return -1
	}
	if parent.Type() == ast.TypeBlock {
		return p.trimRight(start, p.linesRange(parent).End)
	}
	pe := p.inlineEnd(parent)
	if pe < 0 {
		return -1
	}
	switch q := parent.(type) {
	case *ast.Emphasis:
		pe -= q.Level
	case *east.Strikethrough:
		for k := 0; k < 2 && pe > start && p.src[pe-1] == '~'; k++ {
			pe--
		}
	default:
		return -1
	}
	if pe < start {
		return -1
	}
	return pe
}

// ---- AST walk ----

func (p *fileParser) addOpaque(k OpaqueKind, r pos.Range) {
	if r.Start < 0 || r.End > len(p.src) || r.End < r.Start {
		return
	}
	p.f.Opaque = append(p.f.Opaque, Opaque{Kind: k, Range: r})
}

func (p *fileParser) frontMatter() {
	start := 0
	if p.f.HasBOM {
		start = len(bom)
	}
	lr := p.idx.LineRange(p.idx.Line(start))
	first := bytes.TrimRight(p.src[start:lr.End], " \t")
	end := len(p.src)
	for l := p.idx.Line(start) + 1; l <= p.idx.LineCount(); l++ {
		r := p.idx.LineRange(l)
		if bytes.Equal(p.src[r.Start:r.End], first) {
			end = r.End
			break
		}
	}
	p.addOpaque(OpaqueFrontMatter, pos.Range{Start: start, End: end})
}

func (p *fileParser) visit(n ast.Node) {
	switch v := n.(type) {
	case *ast.CodeSpan:
		p.codeSpan(v)
		return
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		p.addOpaque(OpaqueCodeBlock, p.blockRange(n))
		ls := n.Lines()
		for i := 0; i < ls.Len(); i++ {
			seg := ls.At(i)
			p.code = append(p.code, pos.Range{Start: seg.Start, End: seg.Stop})
		}
		return
	case *ast.HTMLBlock:
		p.addOpaque(OpaqueHTMLBlock, p.blockRange(n))
		return
	case *ast.LinkReferenceDefinition:
		p.addOpaque(OpaqueLinkDest, p.blockRange(n))
		return
	case *ast.RawHTML:
		if v.Segments.Len() > 0 {
			p.addOpaque(OpaqueRawHTML, pos.Range{Start: v.Segments.At(0).Start, End: v.Segments.At(v.Segments.Len() - 1).Stop})
		}
		return
	case *ast.AutoLink:
		start := p.startOf(n)
		end := p.inlineEnd(n)
		if start >= 0 && end >= 0 {
			for start < end && isSpaceByte(p.src[start]) {
				start++
			}
			p.addOpaque(OpaqueAutoLink, pos.Range{Start: start, End: end})
		}
		return
	case *ast.Text:
		if v.Segment.Stop > v.Segment.Start {
			p.segs = append(p.segs, textSeg{start: v.Segment.Start, stop: v.Segment.Stop, container: p.containerOf(n)})
		}
		return
	case *ast.Heading:
		if n.Parent() != nil && n.Parent().Kind() == ast.KindDocument {
			p.headingsTL = append(p.headingsTL, v)
		}
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		p.visit(c)
	}
	switch n.(type) {
	case *ast.Link, *ast.Image:
		p.linkTail(n)
	}
}

// codeSpan records a code span: the whole span is opaque; its content (between
// the backtick runs) is hashed byte-exact.
func (p *fileParser) codeSpan(n *ast.CodeSpan) {
	start := n.Pos()
	if start < 0 || start >= len(p.src) || p.src[start] != '`' {
		// Fall back to the children's extent.
		s, e := -1, -1
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				if s < 0 {
					s = t.Segment.Start
				}
				e = t.Segment.Stop
			}
		}
		if s >= 0 {
			p.addOpaque(OpaqueCodeSpan, pos.Range{Start: s, End: e})
			p.code = append(p.code, pos.Range{Start: s, End: e})
		}
		return
	}
	ticks := 0
	for start+ticks < len(p.src) && p.src[start+ticks] == '`' {
		ticks++
	}
	contentStart := start + ticks
	// The closing run is the next backtick run of exactly the same length.
	i := contentStart
	for i < len(p.src) {
		if p.src[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(p.src) && p.src[j] == '`' {
			j++
		}
		if j-i == ticks {
			p.addOpaque(OpaqueCodeSpan, pos.Range{Start: start, End: j})
			p.code = append(p.code, pos.Range{Start: contentStart, End: i})
			return
		}
		i = j
	}
	end := p.inlineEnd(n)
	if end < contentStart {
		end = contentStart
	}
	p.addOpaque(OpaqueCodeSpan, pos.Range{Start: start, End: end})
	p.code = append(p.code, pos.Range{Start: contentStart, End: end})
}

// linkTail records the destination and title part of a link or image: from
// the end of its text to the end of the node.
func (p *fileParser) linkTail(n ast.Node) {
	end := p.inlineEnd(n)
	if end < 0 {
		return // covered by the enclosing link's tail
	}
	textEnd := -1
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || c == n {
			return ast.WalkContinue, nil
		}
		switch v := c.(type) {
		case *ast.Text:
			if v.Segment.Stop > textEnd {
				textEnd = v.Segment.Stop
			}
		case *ast.CodeSpan, *ast.RawHTML, *ast.AutoLink:
			if e := p.inlineEnd(c); e > textEnd {
				textEnd = e
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	if textEnd < 0 {
		textEnd = p.startOf(n) + 1
		if _, ok := n.(*ast.Image); ok {
			textEnd++
		}
	}
	if textEnd > end {
		return
	}
	p.addOpaque(OpaqueLinkDest, pos.Range{Start: textEnd, End: end})
}

// containerOf returns the mention container for inline node n.
func (p *fileParser) containerOf(n ast.Node) Mention {
	b := n.Parent()
	for b != nil && b.Type() != ast.TypeBlock {
		b = b.Parent()
	}
	if b == nil {
		return Mention{File: p.f.Path}
	}
	if m, ok := p.containers[b]; ok {
		return m
	}
	var m Mention
	switch b.(type) {
	case *ast.Heading:
		m = Mention{File: p.f.Path, Kind: ContainerHeading, Range: p.blockRange(b)}
	case *east.TableCell:
		m = Mention{File: p.f.Path, Kind: ContainerTableCell, Range: p.linesRange(b)}
	default:
		m = Mention{File: p.f.Path, Kind: ContainerParagraph, Range: p.blockRange(b)}
		switch b.Parent().(type) {
		case *ast.ListItem:
			m = Mention{File: p.f.Path, Kind: ContainerListItem, Range: p.blockRange(b.Parent())}
		case *ast.Blockquote:
			m = Mention{File: p.f.Path, Kind: ContainerBlockquote, Range: p.blockRange(b.Parent())}
		}
	}
	p.containers[b] = m
	return m
}

// ---- headings: numbered sections and section-form anchors ----

// headingText returns the trimmed raw text of heading h and its range.
func (p *fileParser) headingText(h *ast.Heading) (string, pos.Range) {
	r := p.linesRange(h)
	if r.Empty() {
		return "", r
	}
	return string(p.src[r.Start:r.End]), r
}

func (p *fileParser) headings() {
	first := map[string]*Section{}
	for i, h := range p.headingsTL {
		txt, tr := p.headingText(h)
		hr := p.blockRange(h)
		bodyEnd := p.sectionEnd(i)
		if m := sectionHeadingRE.FindStringSubmatchIndex(txt); m != nil {
			tok := txt[m[2]:m[3]]
			s := &Section{
				File:         p.f.Path,
				Number:       strings.TrimSuffix(tok, "."),
				Title:        strings.Join(strings.Fields(txt[m[4]:m[5]]), " "),
				Level:        h.Level,
				HeadingRange: hr,
				NumberRange:  pos.Range{Start: tr.Start + m[2], End: tr.Start + m[3]},
				Range:        pos.Range{Start: hr.Start, End: bodyEnd},
			}
			p.f.Sections = append(p.f.Sections, s)
			if prev, ok := first[s.Number]; ok {
				p.f.Diagnostics = append(p.f.Diagnostics, diag.Diagnostic{
					Code: diag.CodeSectionDup, Severity: diag.Error, File: p.f.Path, Range: s.NumberRange,
					Message: fmt.Sprintf("section number %s is used twice in this file; renumber one of them", s.Number),
					Related: []diag.Related{{File: p.f.Path, Range: prev.NumberRange, Message: "first section " + s.Number}},
				})
			} else {
				first[s.Number] = s
			}
			continue
		}
		m := headingLabelRE.FindStringSubmatchIndex(txt)
		if m == nil || !ValidName(txt[m[4]:m[5]]) {
			continue
		}
		a := &Anchor{
			Kind:       KindInvariant,
			Form:       FormSection,
			Name:       txt[m[4]:m[5]],
			File:       p.f.Path,
			LabelRange: hr,
			NameRange:  pos.Range{Start: tr.Start + m[4] - 1, End: tr.Start + m[5]},
			BodyRange:  pos.Range{Start: hr.Start, End: bodyEnd},
		}
		if m[2] >= 0 {
			a.Kind = KindDefinition
		}
		if m[6] >= 0 {
			a.Title = strings.TrimSpace(txt[m[6]:m[7]])
			a.TitleRange = pos.Range{Start: tr.Start + m[6], End: tr.Start + m[7]}
		}
		if outer := p.enclosingSectionAnchor(a.LabelRange.Start); outer != nil {
			p.nested(a.LabelRange, a.Name, fmt.Sprintf(
				"$%s is defined inside the body of section-form anchor $%s; definitions cannot nest. Move it below the next heading of level %d or less (%s or fewer #), which ends $%s's body, or turn $%s into a paragraph-form anchor",
				a.Name, outer.Name, p.levelOf(outer), strings.Repeat("#", p.levelOf(outer)), outer.Name, outer.Name))
			continue
		}
		a.input.hashRange = pos.Range{Start: hr.End, End: bodyEnd}
		if a.input.hashRange.End < a.input.hashRange.Start {
			a.input.hashRange.End = a.input.hashRange.Start
		}
		p.addAnchor(a)
	}
	// Headings nested in containers cannot define anchors.
	_ = ast.Walk(p.f.doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		h, ok := n.(*ast.Heading)
		if !ok || n.Parent() == nil || n.Parent().Kind() == ast.KindDocument {
			return ast.WalkContinue, nil
		}
		txt, _ := p.headingText(h)
		if m := headingLabelRE.FindStringSubmatch(txt); m != nil && ValidName(m[2]) {
			p.nested(p.blockRange(h), m[2], fmt.Sprintf(
				"definitions must be top-level: $%s is defined in a heading inside a %s. Move the heading out to the top level of the file",
				m[2], containerWord(n.Parent())))
		}
		return ast.WalkSkipChildren, nil
	})
}

func (p *fileParser) levelOf(a *Anchor) int {
	for _, h := range p.headingsTL {
		if p.startOf(h) == a.LabelRange.Start {
			return h.Level
		}
	}
	return 1
}

// sectionEnd returns the end of the body of the i-th top-level heading: the
// content before the next top-level heading of the same or a higher level.
func (p *fileParser) sectionEnd(i int) int {
	h := p.headingsTL[i]
	start := p.blockRange(h).End
	limit := len(p.src)
	for _, nh := range p.headingsTL[i+1:] {
		if nh.Level <= h.Level {
			limit = p.idx.LineStart(p.idx.Line(p.startOf(nh)))
			break
		}
	}
	end := p.trimRight(start, limit)
	if end < start {
		end = start
	}
	return end
}

// enclosingSectionAnchor returns the section-form anchor whose body (after
// its heading) contains off, or nil.
func (p *fileParser) enclosingSectionAnchor(off int) *Anchor {
	for _, a := range p.sectionAs {
		if off >= a.LabelRange.End && off < a.BodyRange.End {
			return a
		}
	}
	return nil
}

func containerWord(n ast.Node) string {
	switch n.(type) {
	case *ast.ListItem, *ast.List:
		return "list item"
	case *ast.Blockquote:
		return "blockquote"
	case *east.TableCell:
		return "table cell"
	}
	return "container block"
}

func (p *fileParser) nested(r pos.Range, name, msg string) {
	p.f.Diagnostics = append(p.f.Diagnostics, diag.Diagnostic{
		Code: diag.CodeDefNested, Severity: diag.Error, File: p.f.Path, Range: r, Message: msg,
	})
}

func (p *fileParser) addAnchor(a *Anchor) {
	p.f.Anchors = append(p.f.Anchors, a)
	if a.Form == FormSection {
		p.sectionAs = append(p.sectionAs, a)
	}
	if p.nameRanges == nil {
		p.nameRanges = map[int]*Anchor{}
	}
	p.nameRanges[a.NameRange.Start] = a
}

// ---- paragraph-form labels ----

// strongLabel returns the strong node that opens paragraph-like block b and
// its range, or nil.
func (p *fileParser) strongLabel(b ast.Node) (*ast.Emphasis, pos.Range) {
	first := b.FirstChild()
	em, ok := first.(*ast.Emphasis)
	if !ok || em.Level != 2 {
		return nil, pos.Range{}
	}
	start := em.Pos()
	end := p.inlineEnd(em)
	if start < 0 || end < start+4 || end > len(p.src) {
		return nil, pos.Range{}
	}
	open := p.src[start : start+2]
	if !(bytes.Equal(open, []byte("**")) || bytes.Equal(open, []byte("__"))) || !bytes.Equal(p.src[end-2:end], open) {
		return nil, pos.Range{}
	}
	return em, pos.Range{Start: start, End: end}
}

func (p *fileParser) paragraphLabels() {
	// Top-level paragraphs, in order.
	for b := p.f.doc.FirstChild(); b != nil; b = b.NextSibling() {
		para, ok := b.(*ast.Paragraph)
		if !ok {
			continue
		}
		em, lr := p.strongLabel(para)
		if em == nil {
			continue
		}
		inner := string(p.src[lr.Start+2 : lr.End-2])
		m := labelRE.FindStringSubmatchIndex(inner)
		if m == nil || !ValidName(inner[m[4]:m[5]]) {
			if labelLikeRE.MatchString(inner) {
				p.f.Diagnostics = append(p.f.Diagnostics, diag.Diagnostic{
					Code: diag.CodeDefMalformed, Severity: diag.Error, File: p.f.Path, Range: lr,
					Message: "malformed definition label; write **$Name.**, **$Name (title).** or **Def $Name (title).** (names are dotted CamelCase segments with at least one lowercase letter)",
				})
			}
			continue
		}
		name := inner[m[4]:m[5]]
		base := lr.Start + 2
		a := &Anchor{
			Kind:       KindInvariant,
			Form:       FormParagraph,
			Name:       name,
			File:       p.f.Path,
			LabelRange: lr,
			NameRange:  pos.Range{Start: base + m[4] - 1, End: base + m[5]},
		}
		if m[2] >= 0 {
			a.Kind = KindDefinition
		}
		if m[6] >= 0 {
			a.Title = strings.TrimSpace(inner[m[6]:m[7]])
			a.TitleRange = pos.Range{Start: base + m[6], End: base + m[7]}
		}
		if outer := p.enclosingSectionAnchor(lr.Start); outer != nil {
			p.nested(lr, name, fmt.Sprintf(
				"$%s is defined inside the body of section-form anchor $%s; definitions cannot nest. Move it below the next heading of level %d or less (%s or fewer #), which ends $%s's body, or turn $%s into a paragraph-form anchor",
				name, outer.Name, p.levelOf(outer), strings.Repeat("#", p.levelOf(outer)), outer.Name, outer.Name))
			continue
		}
		p.paragraphBody(a, para)
		p.addAnchor(a)
	}
	// Labels in nested blocks: def-nested.
	_ = ast.Walk(p.f.doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.Paragraph, *ast.TextBlock, *east.TableCell:
		default:
			return ast.WalkContinue, nil
		}
		if n.Parent() != nil && n.Parent().Kind() == ast.KindDocument {
			return ast.WalkSkipChildren, nil
		}
		em, lr := p.strongLabel(n)
		if em == nil {
			return ast.WalkSkipChildren, nil
		}
		inner := string(p.src[lr.Start+2 : lr.End-2])
		if m := labelRE.FindStringSubmatch(inner); m != nil && ValidName(m[2]) {
			where := n.Parent()
			if _, ok := n.(*east.TableCell); ok {
				where = n
			}
			p.nested(lr, m[2], fmt.Sprintf(
				"definitions must be top-level paragraphs: **$%s** opens a paragraph inside a %s, so it is read as a reference. To define $%s, move the label into its own paragraph at the top level of the file (not indented, not in a list, quote or table)",
				m[2], containerWord(where), m[2]))
		}
		return ast.WalkSkipChildren, nil
	})
}

// paragraphBody sets the body and hash range of paragraph-form anchor a.
func (p *fileParser) paragraphBody(a *Anchor, para *ast.Paragraph) {
	last := ast.Node(para)
	paraLast := p.lastLine(para)
	if next := para.NextSibling(); next != nil {
		switch next.(type) {
		case *ast.List, *ast.Blockquote:
			if s := p.startOf(next); s >= 0 && p.idx.Line(s) == paraLast+1 {
				last = next
			}
		}
	}
	end := p.blockRange(last).End
	a.BodyRange = pos.Range{Start: a.LabelRange.Start, End: end}
	if fc, ok := last.NextSibling().(*ast.FencedCodeBlock); ok {
		if s := p.startOf(fc); s >= 0 && p.idx.Line(s) == p.idx.Line(end)+1 {
			a.CodeAfter = true
		}
	}
	h := a.LabelRange.End
	if h < len(p.src) && (p.src[h] == '.' || p.src[h] == ':') {
		h++
	}
	for h < end && isSpaceByte(p.src[h]) {
		h++
	}
	if h > end {
		h = end
	}
	a.input.hashRange = pos.Range{Start: h, End: end}
}

// ---- token scan ----

// runByte returns the byte at j if j continues the text run of segment s,
// with the segment index holding j.
func (p *fileParser) runByte(s, j int) (byte, int, bool) {
	for s < len(p.segs) {
		if j < p.segs[s].stop {
			if j < p.segs[s].start {
				return 0, s, false
			}
			return p.src[j], s, true
		}
		if s+1 < len(p.segs) && p.segs[s+1].start == p.segs[s].stop {
			s++
			continue
		}
		return 0, s, false
	}
	return 0, s, false
}

// prevRunByte returns the byte before i in the text run of segment s.
func (p *fileParser) prevRunByte(s, i int) (byte, int, bool) {
	if i > p.segs[s].start {
		return p.src[i-1], s, true
	}
	if s > 0 && p.segs[s-1].stop == i && p.segs[s-1].stop > p.segs[s-1].start {
		return p.src[i-1], s - 1, true
	}
	return 0, s, false
}

// escaped reports whether the byte at i is preceded by an odd number of
// backslashes in its text run.
func (p *fileParser) escaped(s, i int) bool {
	n := 0
	for {
		c, ns, ok := p.prevRunByte(s, i)
		if !ok || c != '\\' {
			break
		}
		n++
		s, i = ns, i-1
	}
	return n%2 == 1
}

func (p *fileParser) overlapsOpaque(r pos.Range) bool {
	op := p.f.Opaque
	i := sort.Search(len(op), func(k int) bool { return p.opMaxEnd[k] > r.Start })
	for ; i < len(op) && op[i].Range.Start < r.End; i++ {
		if op[i].Range.Overlaps(r) {
			return true
		}
	}
	return false
}

func (p *fileParser) scan() {
	sort.SliceStable(p.segs, func(i, j int) bool { return p.segs[i].start < p.segs[j].start })
	sort.SliceStable(p.f.Opaque, func(i, j int) bool { return p.f.Opaque[i].Range.Start < p.f.Opaque[j].Range.Start })
	// Opaque ranges may nest (a link tail inside another), so End is not
	// monotonic; overlapsOpaque searches a prefix maximum instead.
	p.opMaxEnd = make([]int, len(p.f.Opaque))
	m := 0
	for k, o := range p.f.Opaque {
		m = max(m, o.Range.End)
		p.opMaxEnd[k] = m
	}
	next := 0
	for s := range p.segs {
		seg := p.segs[s]
		i := seg.start
		if i < next {
			i = next
		}
		for i < seg.stop {
			c := p.src[i]
			switch {
			case c == '$':
				i = p.scanDollar(s, i)
			case c == 0xC2 && i+1 < seg.stop && p.src[i+1] == 0xA7:
				i = p.scanSection(s, i)
			case c == '#':
				i = p.scanHash(s, i)
			default:
				i++
			}
		}
		next = i
	}
}

// munchName returns the end of the name run starting at j in segment s.
func (p *fileParser) munchName(s, j int) int {
	for {
		c, ns, ok := p.runByte(s, j)
		if !ok {
			return j
		}
		s = ns
		if isNameByte(c) {
			j++
			continue
		}
		if c == '.' {
			if d, _, ok2 := p.runByte(s, j+1); ok2 && isNameByte(d) {
				j++
				continue
			}
		}
		return j
	}
}

func (p *fileParser) scanDollar(s, i int) int {
	if p.escaped(s, i) {
		return i + 1
	}
	if c, _, ok := p.prevRunByte(s, i); ok && (isNameByte(c) || c == '$') {
		return i + 1
	}
	end := p.munchName(s, i+1)
	if end == i+1 {
		return i + 1
	}
	r := pos.Range{Start: i, End: end}
	if p.overlapsOpaque(r) {
		return end
	}
	name := string(p.src[i+1 : end])
	if !ValidName(name) {
		p.f.Candidates = append(p.f.Candidates, Candidate{Range: r, Text: name})
		return end
	}
	o := &Occurrence{Role: RoleReference, Name: name, File: p.f.Path, Range: r, Container: p.segs[s].container}
	if a, ok := p.nameRanges[i]; ok && a.NameRange == r {
		o.Role = RoleDefinition
		o.Defines = a
		o.Within = a
	}
	p.f.Occurrences = append(p.f.Occurrences, o)
	return end
}

func (p *fileParser) scanSection(s, i int) int {
	j := i + 2
	if c, _, ok := p.runByte(s, j); ok && (c == ' ' || c == '\t') {
		j++
	}
	stem := ""
	// Optional "stem:" qualifier: stem bytes, then ':' and a digit.
	q := j
	for {
		c, _, ok := p.runByte(s, q)
		if !ok || !isStemByte(c) {
			break
		}
		q++
	}
	if c, _, ok := p.runByte(s, q); ok && c == ':' {
		if d, _, ok2 := p.runByte(s, q+1); ok2 && isDigit(d) && validRefStem(p.src[j:q]) {
			stem = string(p.src[j:q])
			j = q + 1
		}
	}
	// Number: N(.N)*[a-z]?
	k := j
	for {
		c, _, ok := p.runByte(s, k)
		if !ok || !isDigit(c) {
			break
		}
		k++
	}
	if k == j {
		return i + 2
	}
	for {
		c, _, ok := p.runByte(s, k)
		d, _, ok2 := p.runByte(s, k+1)
		if !ok || !ok2 || c != '.' || !isDigit(d) {
			break
		}
		k++
		for {
			c3, _, ok3 := p.runByte(s, k)
			if !ok3 || !isDigit(c3) {
				break
			}
			k++
		}
	}
	if c, _, ok := p.runByte(s, k); ok && isLower(c) {
		k++
	}
	if c, _, ok := p.runByte(s, k); ok && isAlnum(c) {
		e := k
		for {
			c2, _, ok2 := p.runByte(s, e)
			if !ok2 || !isAlnum(c2) {
				break
			}
			e++
		}
		r := pos.Range{Start: i, End: e}
		if !p.overlapsOpaque(r) {
			p.f.MalformedSectionRefs = append(p.f.MalformedSectionRefs, r)
		}
		return e
	}
	r := pos.Range{Start: i, End: k}
	if p.overlapsOpaque(r) {
		return k
	}
	p.f.SectionRefs = append(p.f.SectionRefs, &SectionRef{
		File: p.f.Path, Range: r, Stem: stem, Number: string(p.src[j:k]), Container: p.segs[s].container,
	})
	return k
}

func (p *fileParser) scanHash(s, i int) int {
	if p.escaped(s, i) {
		return i + 1
	}
	if c, _, ok := p.prevRunByte(s, i); ok && (isNameByte(c) || c == '#' || c == '&') {
		return i + 1
	}
	j := i + 1
	k := j
	for {
		c, _, ok := p.runByte(s, k)
		if !ok || !isDigit(c) {
			break
		}
		k++
	}
	if k == j {
		return i + 1
	}
	dots := 0
	for {
		c, _, ok := p.runByte(s, k)
		d, _, ok2 := p.runByte(s, k+1)
		if !ok || !ok2 || c != '.' || !isDigit(d) {
			break
		}
		dots++
		k++
		for {
			c3, _, ok3 := p.runByte(s, k)
			if !ok3 || !isDigit(c3) {
				break
			}
			k++
		}
	}
	if c, _, ok := p.runByte(s, k); ok && isLower(c) {
		k++
	}
	if dots == 0 {
		return k
	}
	if c, _, ok := p.runByte(s, k); ok && isAlnum(c) {
		return k
	}
	r := pos.Range{Start: i, End: k}
	if !p.overlapsOpaque(r) {
		p.f.HashRefs = append(p.f.HashRefs, HashRef{Range: r, Number: string(p.src[j:k])})
	}
	return k
}

// ---- finish: within, sections, hashes ----

func (p *fileParser) finish() {
	f := p.f
	sort.SliceStable(f.Anchors, func(i, j int) bool { return f.Anchors[i].LabelRange.Start < f.Anchors[j].LabelRange.Start })
	for _, o := range f.Occurrences {
		if o.Within == nil {
			o.Within = anchorAt(f.Anchors, o.Range.Start)
			o.InTitle = o.Within != nil && o.Within.TitleRange.ContainsRange(o.Range) && !o.Within.TitleRange.Empty()
		}
	}
	for _, r := range f.SectionRefs {
		r.Within = anchorAt(f.Anchors, r.Range.Start)
	}
	sort.Slice(p.code, func(i, j int) bool { return p.code[i].Start < p.code[j].Start })
	for _, a := range f.Anchors {
		a.Section = f.SectionAt(a.LabelRange.Start)
		a.src = f.src
		hr := a.input.hashRange
		for k := sort.Search(len(p.code), func(k int) bool { return p.code[k].End > hr.Start }); k < len(p.code) && p.code[k].Start < hr.End; k++ {
			c := p.code[k]
			a.input.code = append(a.input.code, pos.Range{Start: max(c.Start, hr.Start), End: min(c.End, hr.End)})
		}
		occ := f.Occurrences
		for k := sort.Search(len(occ), func(k int) bool { return occ[k].Range.Start >= hr.Start }); k < len(occ) && occ[k].Range.Start < hr.End; k++ {
			if o := occ[k]; o.Role == RoleReference && hr.ContainsRange(o.Range) {
				a.input.refs = append(a.input.refs, refSpan{r: o.NameRange(), name: o.Name})
			}
		}
		if !a.TitleRange.Empty() {
			tr := a.TitleRange
			for k := sort.Search(len(occ), func(k int) bool { return occ[k].Range.Start >= tr.Start }); k < len(occ) && occ[k].Range.Start < tr.End; k++ {
				if o := occ[k]; o.Role == RoleReference && tr.ContainsRange(o.Range) {
					a.titleRefs = append(a.titleRefs, refSpan{r: o.NameRange(), name: o.Name})
				}
			}
		}
		a.Hash = a.HashWithRenames(nil)
		a.ShortHash = ShortHash(a.Hash)
	}
	diag.Sort(f.Diagnostics)
}

// anchorAt returns the anchor whose body contains off, or nil. Bodies do
// not overlap and as is sorted by start.
func anchorAt(as []*Anchor, off int) *Anchor {
	k := sort.Search(len(as), func(k int) bool { return as[k].BodyRange.Start > off }) - 1
	if k >= 0 && off < as[k].BodyRange.End {
		return as[k]
	}
	return nil
}
