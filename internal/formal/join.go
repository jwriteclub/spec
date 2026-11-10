// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package formal

import (
	"sort"
	"strconv"

	"github.com/jwriteclub/spec/internal/mdspec"
)

// Link is a tag resolved against a spec.
type Link struct {
	File *File
	Tag  *Tag
	// State is Resolved when exactly one target exists.
	State mdspec.Resolution
	// Anchor or Section is the target when State is Resolved.
	Anchor  *mdspec.Anchor
	Section *mdspec.Section
	// Anchors and Sections are every candidate when State is Ambiguous.
	Anchors  []*mdspec.Anchor
	Sections []*mdspec.Section
}

// Index is a set of models joined to a spec. A nil *Index is an empty one.
type Index struct {
	Spec *mdspec.Spec
	// Files are the models with mutants last, in path order otherwise.
	Files []*File

	byPath    map[string]*File
	links     map[*Tag]*Link
	byAnchor  map[string][]*Link
	bySection map[*mdspec.Section][]*Link
}

// Join resolves every tag of files against spec.
func Join(spec *mdspec.Spec, files []*File) *Index {
	ix := &Index{
		Spec: spec, Files: append([]*File(nil), files...),
		byPath: map[string]*File{}, links: map[*Tag]*Link{},
		byAnchor: map[string][]*Link{}, bySection: map[*mdspec.Section][]*Link{},
	}
	sort.SliceStable(ix.Files, func(i, j int) bool {
		a, b := ix.Files[i], ix.Files[j]
		if a.Mutant != b.Mutant {
			return b.Mutant
		}
		return a.Path < b.Path
	})
	for _, f := range ix.Files {
		ix.byPath[f.Path] = f
		for _, t := range f.Tags {
			if t.Ref != nil {
				ix.join(f, t)
			}
		}
	}
	return ix
}

func (ix *Index) join(f *File, t *Tag) {
	l := &Link{File: f, Tag: t, State: mdspec.Dangling}
	ix.links[t] = l
	if t.Ref.IsAnchor() {
		as := ix.Spec.Lookup(t.Ref.Name)
		ix.byAnchor[t.Ref.Name] = append(ix.byAnchor[t.Ref.Name], l)
		switch len(as) {
		case 0:
		case 1:
			l.State, l.Anchor = mdspec.Resolved, as[0]
		default:
			l.State, l.Anchors = mdspec.Ambiguous, as
		}
		return
	}
	l.State, l.Section, l.Sections = ix.ResolveSection(f, t.Ref.Stem, t.Ref.Number)
	if l.Section != nil {
		ix.bySection[l.Section] = append(ix.bySection[l.Section], l)
	}
	for _, sec := range l.Sections {
		ix.bySection[sec] = append(ix.bySection[sec], l)
	}
}

// ResolveSection resolves a section reference from model f: qualified, then
// the module's Spec-file list, then a section unique in the project.
func (ix *Index) ResolveSection(f *File, stem, number string) (mdspec.Resolution, *mdspec.Section, []*mdspec.Section) {
	if stem != "" {
		return ix.Spec.ResolveSection("", stem, number)
	}
	var cands []*mdspec.Section
	seen := map[string]bool{}
	for _, t := range f.Tags {
		if t.Kind != TagSpecFile || t.Stem == "" || seen[t.Stem] {
			continue
		}
		seen[t.Stem] = true
		for _, sf := range ix.Spec.FilesWithStem(t.Stem) {
			for _, sec := range sf.Sections {
				if sec.Number == number {
					cands = append(cands, sec)
				}
			}
		}
	}
	switch len(cands) {
	case 0:
		return ix.Spec.ResolveSection("", "", number)
	case 1:
		return mdspec.Resolved, cands[0], nil
	}
	return mdspec.Ambiguous, nil, cands
}

// File returns the model at a project-relative path, or nil.
func (ix *Index) File(p string) *File {
	if ix == nil {
		return nil
	}
	return ix.byPath[p]
}

// LinkOf returns the resolution of a tag; nil for a tag with no reference.
func (ix *Index) LinkOf(t *Tag) *Link {
	if ix == nil {
		return nil
	}
	return ix.links[t]
}

// order puts Spec tags before Derived-from tags and mutants last, keeping
// file order otherwise.
func order(ls []*Link) []*Link {
	out := append([]*Link(nil), ls...)
	rank := func(l *Link) int {
		r := 0
		if l.File.Mutant {
			r += 2
		}
		if l.Tag.Kind == TagDerived {
			r++
		}
		return r
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// ToAnchor returns the links citing the anchor name: Spec tags first, then
// Derived-from, with mutants last.
func (ix *Index) ToAnchor(name string) []*Link {
	if ix == nil {
		return nil
	}
	return order(ix.byAnchor[name])
}

// ToSection returns the links citing sec, ordered as ToAnchor.
func (ix *Index) ToSection(sec *mdspec.Section) []*Link {
	if ix == nil {
		return nil
	}
	return order(ix.bySection[sec])
}

// Covered reports whether a Spec tag outside the mutants cites the anchor
// name, which is what satisfies checked-by-missing. A Derived-from tag says
// where a model element came from, not that anything checks the anchor.
func (ix *Index) Covered(name string) bool {
	if ix == nil {
		return false
	}
	for _, l := range ix.byAnchor[name] {
		if !l.File.Mutant && l.Tag.Kind == TagSpec {
			return true
		}
	}
	return false
}

// Cited reports whether any tag outside the mutants cites the anchor name, so
// that a definition used only by a model does not count as unreferenced.
func (ix *Index) Cited(name string) bool {
	if ix == nil {
		return false
	}
	for _, l := range ix.byAnchor[name] {
		if !l.File.Mutant {
			return true
		}
	}
	return false
}

// Where names the place of a link for humans: path:Op when the tag binds to a
// declaration, else path:line.
func (l *Link) Where() string {
	if l.Tag.Decl != nil {
		return l.File.Path + ":" + l.Tag.Decl.Name
	}
	return l.File.Path + ":" + strconv.Itoa(l.File.Index().Line(l.Tag.Range.Start))
}
