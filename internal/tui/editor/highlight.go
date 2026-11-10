// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package editor

import (
	"sort"

	"github.com/yuin/goldmark/ast"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// Flag is a set of highlight classes for a byte. Several may apply at once
// (a $Name inside a label line with a diagnostic on it).
type Flag uint32

// Highlight classes.
const (
	// FInv is a $Name that refers to an invariant.
	FInv Flag = 1 << iota
	// FInvSite is the $Name of an invariant's definition site.
	FInvSite
	// FDef is a $Name that refers to a definition.
	FDef
	// FDefSite is the $Name of a definition's definition site.
	FDefSite
	// FSection is a heading, a section number or a § reference.
	FSection
	// FBadRef is a dangling or ambiguous reference.
	FBadRef
	// FCode is a code span or block (and other opaque text).
	FCode
	// FLabel is a paragraph label (**$Name (title).**).
	FLabel
	// FDiagError, FDiagWarn and FDiagHint underline a diagnostic's range.
	FDiagError
	FDiagWarn
	FDiagHint
	// FSelect is the selection.
	FSelect
	// FMatch is the current find match.
	FMatch
	// FAdded is a word added since HEAD (SetExtra).
	FAdded
	// FRemoved is ghost text: words removed since HEAD (SetGhosts).
	FRemoved
	// FFixOld is text the selected suggested fix would replace (SetExtra).
	FFixOld
	// FFixNew is ghost text: the words the selected suggested fix would
	// put in (SetGhosts).
	FFixNew
	// The classes of a formal model's source (Analysis.Syntax). A tag's
	// reference takes the class a spec gives it (FInv, FDef, FSection or
	// FBadRef) over FModelComment.
	FModelKeyword
	FModelOperator
	FModelString
	FModelNumber
	FModelComment
	// FModelDecl is a declared name.
	FModelDecl
	// FModelTagKey is the keyword of a tag: "Spec:".
	FModelTagKey
	// FModelLink is what follows a link to the spec: a tag's reference, or a
	// declaration that has tags.
	FModelLink
	// FSpell underlines a word the spell check flagged.
	FSpell
)

// Span is a highlighted byte range.
type Span struct {
	Start, End int
	Flags      Flag
}

// Analysis is what the editor needs from a parse: the file as parsed, the
// spec it was resolved in, and the diagnostics of this file.
type Analysis struct {
	File  *mdspec.File
	Spec  *mdspec.Spec
	Diags []diag.Diagnostic
	// Path and Syntax stand in for File and Spec on a file that is not a spec
	// (a formal model): its path, and its highlight, which the host computes.
	// Spec stays nil there, so nothing that reads the spec's markup runs: #
	// means "not equal" in TLA+, and must not turn into a §.
	Path   string
	Syntax []Span
}

// Spans computes the highlight spans of a parsed file, sorted by start.
func Spans(a Analysis) []Span {
	f := a.File
	if f == nil {
		return otherSpans(a)
	}
	var out []Span
	add := func(r pos.Range, fl Flag) {
		if r.End > r.Start {
			out = append(out, Span{Start: r.Start, End: r.End, Flags: fl})
		}
	}
	for _, o := range f.Opaque {
		switch o.Kind {
		case mdspec.OpaqueLinkDest, mdspec.OpaqueAutoLink:
			continue
		}
		add(o.Range, FCode)
	}
	for _, r := range headingRanges(f) {
		add(r, FSection)
	}
	for _, an := range f.Anchors {
		if an.Form == mdspec.FormParagraph {
			add(an.LabelRange, FLabel)
		}
	}
	for _, o := range f.Occurrences {
		add(o.Range, occurrenceFlag(a.Spec, o))
	}
	for _, r := range f.SectionRefs {
		fl := FSection
		if a.Spec != nil {
			if rr := a.Spec.ResolveSectionRef(r); rr != nil && rr.State != mdspec.Resolved {
				fl = FBadRef
			}
		}
		add(r.Range, fl)
	}
	for _, d := range a.Diags {
		if d.File != f.Path {
			continue
		}
		add(d.Range, diagFlagOf(d))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// otherSpans is Spans for a file that is not a spec: its syntax and its
// diagnostics.
func otherSpans(a Analysis) []Span {
	if a.Path == "" {
		return nil
	}
	out := append([]Span(nil), a.Syntax...)
	for _, d := range a.Diags {
		if d.File == a.Path && d.Range.End > d.Range.Start {
			out = append(out, Span{Start: d.Range.Start, End: d.Range.End, Flags: diagFlagOf(d)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// occurrenceFlag classifies a $Name token.
func occurrenceFlag(s *mdspec.Spec, o *mdspec.Occurrence) Flag {
	if o.Role == mdspec.RoleDefinition && o.Defines != nil {
		if o.Defines.Kind == mdspec.KindDefinition {
			return FDefSite
		}
		return FInvSite
	}
	if s == nil {
		return FInv
	}
	if rr := s.Resolve(o); rr != nil {
		if rr.State != mdspec.Resolved {
			return FBadRef
		}
		if rr.Target.Kind == mdspec.KindDefinition {
			return FDef
		}
		return FInv
	}
	// Not resolved in this spec (a definition token of a malformed label):
	// classify by name.
	switch as := s.Lookup(o.Name); {
	case len(as) == 0:
		return FBadRef
	case as[0].Kind == mdspec.KindDefinition:
		return FDef
	}
	return FInv
}

// diagFlagOf is a diagnostic's underline class: FSpell for a spelling
// flag, else its severity's.
func diagFlagOf(d diag.Diagnostic) Flag {
	if d.Code == diag.CodeSpell {
		return FSpell
	}
	return diagFlag(d.Severity)
}

// diagFlag maps a severity to its underline class.
func diagFlag(s diag.Severity) Flag {
	switch s {
	case diag.Error:
		return FDiagError
	case diag.Warning:
		return FDiagWarn
	}
	return FDiagHint
}

// headingRanges returns the ranges of every heading in the file, from the
// first byte of its first line to the end of its last line.
func headingRanges(f *mdspec.File) []pos.Range {
	doc := f.Document()
	if doc == nil {
		return nil
	}
	idx := f.Index()
	var out []pos.Range
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		h, ok := n.(*ast.Heading)
		if !ok {
			return ast.WalkContinue, nil
		}
		lines := h.Lines()
		if lines.Len() == 0 {
			return ast.WalkSkipChildren, nil
		}
		first := lines.At(0)
		last := lines.At(lines.Len() - 1)
		startLine := idx.Line(first.Start)
		endLine := idx.Line(max(last.Stop-1, first.Start))
		if !isATX(f.Source(), idx.LineStart(startLine)) {
			// Setext: the underline is the next line.
			endLine++
		}
		out = append(out, pos.Range{Start: idx.LineStart(startLine), End: idx.LineRange(endLine).End})
		return ast.WalkSkipChildren, nil
	})
	return out
}

// isATX reports whether the line starting at off begins (after up to three
// spaces) with '#'.
func isATX(src []byte, off int) bool {
	for k := 0; k < 4 && off+k < len(src); k++ {
		switch src[off+k] {
		case ' ':
			continue
		case '#':
			return true
		}
		return false
	}
	return false
}

// shiftSpans adjusts spans for an edit so a stale highlight stays aligned
// with the text until the next parse. Spans after the edit move; spans the
// edit cuts into are clipped; empty spans are dropped.
func shiftSpans(spans []Span, e Edit) []Span {
	delta := len(e.New) - len(e.Old)
	es, ee := e.Off, e.End()
	out := spans[:0]
	for _, s := range spans {
		switch {
		case s.End <= es:
			// Entirely before the edit.
		case s.Start >= ee:
			s.Start += delta
			s.End += delta
		default:
			if s.Start >= es {
				s.Start = es + len(e.New)
			}
			if s.End > ee {
				s.End += delta
			} else {
				s.End = es
				if s.Start > es {
					s.End = s.Start
				}
			}
		}
		if s.End > s.Start {
			out = append(out, s)
		}
	}
	return out
}
