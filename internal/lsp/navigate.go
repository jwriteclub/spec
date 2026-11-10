// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"fmt"
	"strings"

	"go.lsp.dev/protocol"

	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// target is what the cursor is on: a $Name token, a § reference, or the
// number of a section heading.
type target struct {
	occ    *mdspec.Occurrence
	secRef *mdspec.SectionRef
	// sec is the section the cursor names: the heading under the cursor, or
	// the target of a resolved § reference.
	sec *mdspec.Section
	rng pos.Range // the token under the cursor
}

// numberRange is a section heading's number without its trailing period.
func numberRange(spec *mdspec.Spec, sec *mdspec.Section) pos.Range {
	r := sec.NumberRange
	if f := spec.File(sec.File); f != nil && r.End > r.Start && f.Text(pos.Range{Start: r.End - 1, End: r.End}) == "." {
		r.End--
	}
	return r
}

// targetAt finds the token at off.
func targetAt(v view, off int) (target, bool) {
	if o := v.file.OccurrenceAt(off); o != nil {
		return target{occ: o, rng: o.Range}, true
	}
	if r := v.file.SectionRefAt(off); r != nil {
		t := target{secRef: r, rng: r.Range}
		if rr := v.snap.spec.ResolveSectionRef(r); rr != nil && rr.State == mdspec.Resolved {
			t.sec = rr.Target
		}
		return t, true
	}
	for _, sec := range v.file.Sections {
		if off >= sec.NumberRange.Start && off <= sec.NumberRange.End {
			return target{sec: sec, rng: sec.NumberRange}, true
		}
	}
	return target{}, false
}

// ---- hover ----

func (s *Server) hover(p *protocol.HoverParams) (any, *rpcError) {
	v, ok := s.viewOf(p.TextDocument.URI)
	if !ok {
		return s.formalHover(p)
	}
	t, ok := targetAt(v, s.offset(v, p.Position))
	if !ok {
		return nil, nil
	}
	var md string
	switch {
	case t.occ != nil:
		md = s.nameHover(v, t.occ)
	case t.secRef != nil:
		md = s.sectionRefHover(v, t.secRef)
	default:
		md = s.sectionHover(v, t.sec)
	}
	r := s.conv.rng(v.file.Index(), t.rng)
	return &protocol.Hover{Contents: &protocol.MarkupContent{Kind: protocol.MarkupKindMarkdown, Value: md}, Range: &r}, nil
}

// where formats a file:line location of a byte offset.
func where(spec *mdspec.Spec, file string, off int) string {
	line := 0
	if f := spec.File(file); f != nil {
		line = f.Index().Line(off)
	}
	return fmt.Sprintf("%s:%d", file, line)
}

// plural returns "1 reference" or "2 references".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func (s *Server) nameHover(v view, o *mdspec.Occurrence) string {
	spec := v.snap.spec
	var a *mdspec.Anchor
	if o.Role == mdspec.RoleDefinition {
		a = o.Defines
	} else if rr := spec.Resolve(o); rr != nil {
		switch rr.State {
		case mdspec.Resolved:
			a = rr.Target
		case mdspec.Ambiguous:
			var b strings.Builder
			fmt.Fprintf(&b, "`$%s` is defined %d times; names must be unique:\n", o.Name, len(rr.Candidates))
			for _, c := range rr.Candidates {
				fmt.Fprintf(&b, "\n- %s `%s`", kindWord(c.Kind), where(spec, c.File, c.LabelRange.Start))
			}
			return b.String()
		}
	}
	if a == nil {
		return fmt.Sprintf("`$%s` is not defined anywhere in the project.", o.Name)
	}
	return anchorMarkdown(spec, a) + modelLines(v.snap.formal.ToAnchor(a.Name))
}

// anchorMarkdown is the hover text of an anchor: kind, name and title, the
// body, then where it is, its short hash and how often it is referenced.
func anchorMarkdown(spec *mdspec.Spec, a *mdspec.Anchor) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** `$%s`", kindWord(a.Kind), a.Name)
	if a.Title != "" {
		fmt.Fprintf(&b, " (%s)", a.Title)
	}
	b.WriteString("\n\n")
	if f := spec.File(a.File); f != nil {
		b.WriteString(strings.ReplaceAll(f.Text(a.BodyRange), "\r\n", "\n"))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "`%s` · `%s` · %s", where(spec, a.File, a.LabelRange.Start), a.ShortHash, plural(len(spec.ReferencesTo(a.Name)), "reference"))
	return b.String()
}

func (s *Server) sectionRefHover(v view, r *mdspec.SectionRef) string {
	spec := v.snap.spec
	written := v.file.Text(r.Range)
	rr := spec.ResolveSectionRef(r)
	if rr == nil {
		return ""
	}
	switch rr.State {
	case mdspec.Resolved:
		return s.sectionHover(v, rr.Target)
	case mdspec.Ambiguous:
		var b strings.Builder
		fmt.Fprintf(&b, "`%s` is ambiguous; it matches:\n", written)
		for _, c := range rr.Candidates {
			fmt.Fprintf(&b, "\n- `%s` %s `%s`", c.Qualified(), c.Title, where(spec, c.File, c.HeadingRange.Start))
		}
		return b.String()
	}
	return fmt.Sprintf("`%s` does not match any numbered section.", written)
}

// sectionHover is the hover text of a section with the models citing it.
func (s *Server) sectionHover(v view, sec *mdspec.Section) string {
	return sectionMarkdown(v.snap.spec, sec) + modelLines(v.snap.formal.ToSection(sec))
}

// sectionMarkdown is the text of a section: number and title, its first
// paragraph, and the anchors inside it.
func sectionMarkdown(spec *mdspec.Spec, sec *mdspec.Section) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**§%s** %s `%s`", sec.Number, sec.Title, where(spec, sec.File, sec.HeadingRange.Start))
	if f := spec.File(sec.File); f != nil {
		if para := firstParagraph(f.Text(pos.Range{Start: sec.HeadingRange.End, End: sec.Range.End})); para != "" {
			b.WriteString("\n\n" + para)
		}
	}
	if as := spec.AnchorsIn(sec); len(as) > 0 {
		b.WriteString("\n\nAnchors:\n")
		for _, a := range as {
			fmt.Fprintf(&b, "\n- `$%s`", a.Name)
			if a.Title != "" {
				fmt.Fprintf(&b, " (%s)", a.Title)
			}
		}
	}
	return b.String()
}

// firstParagraph returns the first block of text, lines joined with "\n",
// or "" when the first block is a heading.
func firstParagraph(text string) string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		if len(lines) == 0 && strings.HasPrefix(strings.TrimSpace(line), "#") {
			return ""
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// ---- definition, references, highlights ----

func (s *Server) definition(p *protocol.DefinitionParams) (any, *rpcError) {
	v, ok := s.viewOf(p.TextDocument.URI)
	if !ok {
		return s.formalDefinition(p)
	}
	t, ok := targetAt(v, s.offset(v, p.Position))
	if !ok {
		return nil, nil
	}
	spec := v.snap.spec
	var out protocol.LocationSlice
	add := func(file string, r pos.Range) {
		if loc, ok := s.location(v.p, file, r); ok {
			out = append(out, loc)
		}
	}
	switch {
	case t.occ != nil:
		for _, a := range spec.Lookup(t.occ.Name) {
			add(a.File, a.NameRange)
		}
	case t.sec != nil:
		add(t.sec.File, t.sec.HeadingRange)
	default:
		if rr := spec.ResolveSectionRef(t.secRef); rr != nil {
			for _, c := range rr.Candidates {
				add(c.File, c.HeadingRange)
			}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// site is one occurrence of the symbol under the cursor.
type site struct {
	file string
	rng  pos.Range
	decl bool
}

// sites returns the declaration and references of the anchor name or of the
// section sec (one of them is set), in project order with declarations first
// and the tags of formal models last.
func sites(sn *snapshot, name string, sec *mdspec.Section) []site {
	spec := sn.spec
	var out []site
	var links []*formal.Link
	switch {
	case name != "":
		for _, a := range spec.Lookup(name) {
			out = append(out, site{file: a.File, rng: a.NameRange, decl: true})
		}
		for _, r := range spec.ReferencesTo(name) {
			out = append(out, site{file: r.File, rng: r.Range})
		}
		links = sn.formal.ToAnchor(name)
	case sec != nil:
		out = append(out, site{file: sec.File, rng: numberRange(spec, sec), decl: true})
		for _, r := range spec.SectionReferencesTo(sec) {
			out = append(out, site{file: r.File, rng: r.Range})
		}
		links = sn.formal.ToSection(sec)
	}
	for _, l := range links {
		out = append(out, site{file: l.File.Path, rng: l.Tag.Ref.Range})
	}
	return out
}

// targetSites returns the sites of the cursor's symbol.
func targetSites(sn *snapshot, t target) []site {
	if t.occ != nil {
		return sites(sn, t.occ.Name, nil)
	}
	return sites(sn, "", t.sec)
}

func (s *Server) references(p *protocol.ReferenceParams) (any, *rpcError) {
	v, ok := s.viewOf(p.TextDocument.URI)
	if !ok {
		return s.formalReferences(p)
	}
	t, ok := targetAt(v, s.offset(v, p.Position))
	if !ok {
		return nil, nil
	}
	return s.siteLocations(v.p, targetSites(v.snap, t), p.Context.IncludeDeclaration), nil
}

// siteLocations converts sites to locations, with or without declarations.
func (s *Server) siteLocations(p *projectState, sts []site, withDecl bool) []protocol.Location {
	out := []protocol.Location{}
	for _, st := range sts {
		if st.decl && !withDecl {
			continue
		}
		if loc, ok := s.location(p, st.file, st.rng); ok {
			out = append(out, loc)
		}
	}
	return out
}

func (s *Server) documentHighlight(p *protocol.DocumentHighlightParams) (any, *rpcError) {
	v, ok := s.viewOf(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	t, ok := targetAt(v, s.offset(v, p.Position))
	if !ok {
		return nil, nil
	}
	out := []protocol.DocumentHighlight{}
	x := v.file.Index()
	for _, st := range targetSites(v.snap, t) {
		if st.file != v.file.Path {
			continue
		}
		kind := protocol.DocumentHighlightKindRead
		if st.decl {
			kind = protocol.DocumentHighlightKindWrite
		}
		out = append(out, protocol.DocumentHighlight{Range: s.conv.rng(x, st.rng), Kind: kind})
	}
	return out, nil
}

// ---- rename ----

// renameTarget returns the $Name token under the cursor and checks that its
// name is defined exactly once.
func renameTarget(v view, off int) (*mdspec.Occurrence, *rpcError) {
	o := v.file.OccurrenceAt(off)
	if o == nil {
		return nil, nil
	}
	switch n := len(v.snap.spec.Lookup(o.Name)); n {
	case 0:
		return nil, requestFailed("$%s is not defined in the project, so there is nothing to rename", o.Name)
	case 1:
		return o, nil
	default:
		return nil, requestFailed("$%s is defined %d times; fix the duplicates first", o.Name, n)
	}
}

func (s *Server) prepareRename(p *protocol.PrepareRenameParams) (any, *rpcError) {
	v, ok := s.viewOf(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	o, rerr := renameTarget(v, s.offset(v, p.Position))
	if o == nil {
		return nil, rerr
	}
	return &protocol.PrepareRenamePlaceholder{Range: s.conv.rng(v.file.Index(), o.NameRange()), Placeholder: o.Name}, nil
}

func (s *Server) rename(p *protocol.RenameParams) (any, *rpcError) {
	v, ok := s.viewOf(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	o, rerr := renameTarget(v, s.offset(v, p.Position))
	if o == nil {
		if rerr == nil {
			rerr = requestFailed("rename works on a $Name; the cursor is not on one")
		}
		return nil, rerr
	}
	res, err := edit.PlanRename(v.p.root, v.snap.spec, o.Name, p.NewName, v.snap.formal.Files...)
	if err != nil {
		return nil, requestFailed("%s", err.Error())
	}
	return s.workspaceEdit(v.p, res.Plan.Outputs), nil
}
