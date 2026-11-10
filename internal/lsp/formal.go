// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"fmt"
	"strings"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// A TLA+ plugin usually runs beside this server, so on a model it answers
// narrowly: hover and definition only on tags and declaration names, nothing
// for semantic tokens or symbols. The spec-to-model direction has its own
// request, textDocument/implementation, so `gd` keeps meaning the spec.

// hoverLabel opens every hover on a model, to tell it apart from a plugin's.
const hoverLabel = "*spec*"

// formalView is an open model in its project's current analysis.
type formalView struct {
	p    *projectState
	snap *snapshot
	file *formal.File
}

// formalViewOf returns the analysis of an open model, re-analyzing first when
// edits are pending. ok is false for anything else.
func (s *Server) formalViewOf(u uri.URI) (formalView, bool) {
	abs, err := pathOf(u)
	if err != nil {
		return formalView{}, false
	}
	d := s.docs[abs]
	if d == nil || d.proj == nil {
		return formalView{}, false
	}
	s.fresh(d.proj)
	p := d.proj
	if p == nil || p.snap == nil {
		return formalView{}, false
	}
	f := p.snap.formal.File(d.rel)
	if f == nil || !f.ValidUTF8 {
		return formalView{}, false
	}
	return formalView{p: p, snap: p.snap, file: f}, true
}

func (fv formalView) offset(s *Server, at protocol.Position) int {
	return s.conv.offset(fv.file.Index(), at)
}

// modelLines is what a spec-side hover adds: one line per distinct place that
// cites the target, with Derived-from dimmed. The result starts with its own
// paragraph break, or is empty.
func modelLines(links []*formal.Link) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, l := range links {
		place := l.Where()
		key := string(l.Tag.Kind) + " " + place
		if seen[key] {
			continue
		}
		seen[key] = true
		switch {
		case l.Tag.Kind == formal.TagDerived:
			fmt.Fprintf(&b, "\n\n*Derived-from in `%s`*", place)
		case l.File.Mutant:
			fmt.Fprintf(&b, "\n\nModel (mutant): `%s`", place)
		default:
			fmt.Fprintf(&b, "\n\nModel: `%s`", place)
		}
	}
	return b.String()
}

// ---- hover ----

func (s *Server) formalHover(p *protocol.HoverParams) (any, *rpcError) {
	fv, ok := s.formalViewOf(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	off := fv.offset(s, p.Position)
	var md string
	var at pos.Range
	if t := fv.file.TagAt(off); t != nil {
		md, at = hoverLabel+" · "+string(t.Kind)+" tag\n\n"+fv.tagMarkdown(t), t.Range
	} else if d := fv.file.DeclAt(off); d != nil {
		tags := fv.file.TagsOf(d)
		if len(tags) == 0 {
			return nil, nil
		}
		md, at = fv.declMarkdown(d, tags), d.NameRange
	} else {
		return nil, nil
	}
	r := s.conv.rng(fv.file.Index(), at)
	return &protocol.Hover{Contents: &protocol.MarkupContent{Kind: protocol.MarkupKindMarkdown, Value: md}, Range: &r}, nil
}

// tagMarkdown is the text a tag cites, or what is wrong with it.
func (fv formalView) tagMarkdown(t *formal.Tag) string {
	if t.Kind == formal.TagSpecFile {
		return fv.specFileMarkdown(t)
	}
	if t.Ref == nil {
		return t.Problem
	}
	l := fv.snap.formal.LinkOf(t)
	out := fv.targetMarkdown(l)
	if fv.snap.formal.QuoteStale(l) {
		out += "\n\nThe tag's quote is not in this text."
	}
	if t.Problem != "" {
		out += "\n\n" + t.Problem
	}
	return out
}

func (fv formalView) specFileMarkdown(t *formal.Tag) string {
	if t.Stem == "" {
		return t.Problem
	}
	files := fv.snap.spec.FilesWithStem(t.Stem)
	if len(files) == 0 {
		return fmt.Sprintf("No spec file has the stem `%s`.", t.Stem)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, "`"+f.Path+"`")
	}
	return fmt.Sprintf("Bare section numbers in this module look in %s first.", strings.Join(paths, ", "))
}

// targetMarkdown is the text of a link's target.
func (fv formalView) targetMarkdown(l *formal.Link) string {
	spec := fv.snap.spec
	written := l.Tag.Ref.Written()
	switch {
	case l.Anchor != nil:
		return anchorMarkdown(spec, l.Anchor)
	case l.Section != nil:
		return sectionMarkdown(spec, l.Section)
	case len(l.Anchors) > 0:
		var b strings.Builder
		fmt.Fprintf(&b, "`%s` is defined %d times; names must be unique:\n", written, len(l.Anchors))
		for _, a := range l.Anchors {
			fmt.Fprintf(&b, "\n- %s `%s`", kindWord(a.Kind), where(spec, a.File, a.LabelRange.Start))
		}
		return b.String()
	case len(l.Sections) > 0:
		var b strings.Builder
		fmt.Fprintf(&b, "`%s` is ambiguous; it matches:\n", written)
		for _, c := range l.Sections {
			fmt.Fprintf(&b, "\n- `%s` %s `%s`", c.Qualified(), c.Title, where(spec, c.File, c.HeadingRange.Start))
		}
		return b.String()
	}
	return fmt.Sprintf("`%s` does not resolve to anything in the spec.", written)
}

// declMarkdown is the hover of a tagged declaration: the full text of what its
// Spec tags cite, and one dimmed line per Derived-from tag.
func (fv formalView) declMarkdown(d *formal.Decl, tags []*formal.Tag) string {
	parts := []string{fmt.Sprintf("%s · `%s` cites", hoverLabel, d.Name)}
	var derived []string
	for _, t := range tags {
		if t.Kind == formal.TagDerived {
			derived = append(derived, "*"+fv.derivedLine(t)+"*")
			continue
		}
		parts = append(parts, fv.tagMarkdown(t))
	}
	if len(derived) > 0 {
		parts = append(parts, strings.Join(derived, "\n\n"))
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func (fv formalView) derivedLine(t *formal.Tag) string {
	if t.Ref == nil {
		return "Derived-from: " + t.Problem
	}
	l := fv.snap.formal.LinkOf(t)
	spec := fv.snap.spec
	line := "Derived-from `" + t.Ref.Written() + "`"
	switch {
	case l.Anchor != nil && l.Anchor.Title != "":
		line += " " + l.Anchor.Title
	case l.Section != nil:
		line += " " + l.Section.Title + " `" + where(spec, l.Section.File, l.Section.HeadingRange.Start) + "`"
	case l.Anchor != nil:
		line += " `" + where(spec, l.Anchor.File, l.Anchor.LabelRange.Start) + "`"
	}
	return line
}

// ---- navigation ----

// tagTargets returns the spec locations a tag points at.
func (s *Server) tagTargets(fv formalView, t *formal.Tag) []protocol.Location {
	var out []protocol.Location
	add := func(file string, r pos.Range) {
		if loc, ok := s.location(fv.p, file, r); ok {
			out = append(out, loc)
		}
	}
	if t.Kind == formal.TagSpecFile {
		for _, f := range fv.snap.spec.FilesWithStem(t.Stem) {
			add(f.Path, pos.Range{})
		}
		return out
	}
	l := fv.snap.formal.LinkOf(t)
	if l == nil {
		return nil
	}
	switch {
	case l.Anchor != nil:
		add(l.Anchor.File, l.Anchor.NameRange)
	case l.Section != nil:
		add(l.Section.File, l.Section.HeadingRange)
	}
	for _, a := range l.Anchors {
		add(a.File, a.NameRange)
	}
	for _, c := range l.Sections {
		add(c.File, c.HeadingRange)
	}
	return out
}

func (s *Server) formalDefinition(p *protocol.DefinitionParams) (any, *rpcError) {
	fv, ok := s.formalViewOf(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	off := fv.offset(s, p.Position)
	var tags []*formal.Tag
	if t := fv.file.TagAt(off); t != nil {
		tags = []*formal.Tag{t}
	} else if d := fv.file.DeclAt(off); d != nil {
		// Spec tags first: Derived-from is the weaker claim.
		for _, want := range []formal.TagKind{formal.TagSpec, formal.TagDerived} {
			for _, t := range fv.file.TagsOf(d) {
				if t.Kind == want {
					tags = append(tags, t)
				}
			}
		}
	}
	var out protocol.LocationSlice
	seen := map[protocol.Location]bool{}
	for _, t := range tags {
		for _, loc := range s.tagTargets(fv, t) {
			if !seen[loc] {
				seen[loc] = true
				out = append(out, loc)
			}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// implementation jumps from the spec to the models that cite it: from a $Name,
// a § reference or a section heading's number to each declaration its tags
// bind to, or to the tag when it binds to none.
func (s *Server) implementation(p *protocol.ImplementationParams) (any, *rpcError) {
	v, ok := s.viewOf(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	t, ok := targetAt(v, s.offset(v, p.Position))
	if !ok {
		return nil, nil
	}
	var links []*formal.Link
	switch {
	case t.occ != nil:
		links = v.snap.formal.ToAnchor(t.occ.Name)
	case t.sec != nil:
		links = v.snap.formal.ToSection(t.sec)
	}
	var out protocol.LocationSlice
	seen := map[protocol.Location]bool{}
	for _, l := range links {
		r := l.Tag.Range
		if l.Tag.Decl != nil {
			r = l.Tag.Decl.NameRange
		}
		if loc, ok := s.location(v.p, l.File.Path, r); ok && !seen[loc] {
			seen[loc] = true
			out = append(out, loc)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// formalReferences answers on a tag's line, with everything that cites the
// same target. Elsewhere in a model it stays quiet for a TLA+ plugin.
func (s *Server) formalReferences(p *protocol.ReferenceParams) (any, *rpcError) {
	fv, ok := s.formalViewOf(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	t := fv.file.TagAt(fv.offset(s, p.Position))
	if t == nil {
		return nil, nil
	}
	l := fv.snap.formal.LinkOf(t)
	if l == nil {
		return []protocol.Location{}, nil
	}
	var sts []site
	if t.Ref.IsAnchor() {
		sts = sites(fv.snap, t.Ref.Name, nil)
	} else {
		sts = sites(fv.snap, "", l.Section)
	}
	return s.siteLocations(fv.p, sts, p.Context.IncludeDeclaration), nil
}

// ---- completion ----

// formalCompletion completes $ and § where a tag's reference goes.
func (s *Server) formalCompletion(p *protocol.CompletionParams) (any, *rpcError) {
	fv, ok := s.formalViewOf(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	x := fv.file.Index()
	tr, ok := completionTrigger(fv.file.Source(), x, fv.offset(s, p.Position))
	if !ok || tr.sigil == completionHash || !fv.file.RefSlot(tr.rng.Start) {
		return nil, nil
	}
	rng := s.conv.rng(x, tr.rng)
	spec := fv.snap.spec
	list := &protocol.CompletionList{}
	if tr.sigil == completionDollar {
		list.Items = anchorItems(spec, rng)
		return list, nil
	}
	list.Items = sectionItems(spec, "", tr, rng, func(number string) (mdspec.Resolution, *mdspec.Section) {
		state, tgt, _ := fv.snap.formal.ResolveSection(fv.file, "", number)
		return state, tgt
	})
	return list, nil
}
