// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package mdspec

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jwriteclub/spec/internal/diag"
)

// Spec is a resolved set of files. It is immutable after Build.
type Spec struct {
	// Files are all files passed to Build, sorted by path, including those
	// excluded for invalid UTF-8.
	Files []*File
	// Anchors are the anchors of included files in project order (file
	// order, then source order). Duplicated names appear once per site.
	Anchors []*Anchor
	// ByName maps a name (without $) to every anchor defining it.
	ByName map[string][]*Anchor
	// Refs are every reference occurrence of included files, in project
	// order, with its resolution.
	Refs []*ResolvedRef
	// SectionRefs are every section reference of included files, in
	// project order, with its resolution.
	SectionRefs []*ResolvedSectionRef
	// Sections are the numbered sections of included files, in project
	// order.
	Sections []*Section
	// Diagnostics are the files' parse diagnostics plus those found while
	// resolving, sorted with diag.Sort.
	Diagnostics []diag.Diagnostic

	byPath   map[string]*File
	byStem   map[string][]*File
	refOf    map[*Occurrence]*ResolvedRef
	secRefOf map[*SectionRef]*ResolvedSectionRef
}

// Build resolves files into a Spec. Files with invalid UTF-8 are kept in
// Files and contribute their diagnostics but nothing else. Build never
// modifies the files, so a File may be shared by several Specs.
func Build(files []*File) *Spec {
	s := &Spec{
		Files:    append([]*File(nil), files...),
		ByName:   map[string][]*Anchor{},
		byPath:   map[string]*File{},
		byStem:   map[string][]*File{},
		refOf:    map[*Occurrence]*ResolvedRef{},
		secRefOf: map[*SectionRef]*ResolvedSectionRef{},
	}
	sort.SliceStable(s.Files, func(i, j int) bool { return s.Files[i].Path < s.Files[j].Path })
	for _, f := range s.Files {
		s.byPath[f.Path] = f
		s.Diagnostics = append(s.Diagnostics, f.Diagnostics...)
		if !f.ValidUTF8 {
			continue
		}
		s.byStem[f.Stem()] = append(s.byStem[f.Stem()], f)
		for _, a := range f.Anchors {
			s.Anchors = append(s.Anchors, a)
			s.ByName[a.Name] = append(s.ByName[a.Name], a)
		}
		s.Sections = append(s.Sections, f.Sections...)
	}
	s.duplicates()
	s.stems()
	s.resolveRefs()
	s.resolveSectionRefs()
	diag.Sort(s.Diagnostics)
	return s
}

func (s *Spec) included() []*File {
	var out []*File
	for _, f := range s.Files {
		if f.ValidUTF8 {
			out = append(out, f)
		}
	}
	return out
}

func (s *Spec) duplicates() {
	for _, a := range s.Anchors {
		all := s.ByName[a.Name]
		if len(all) < 2 {
			continue
		}
		var rel []diag.Related
		for _, b := range all {
			if b != a {
				rel = append(rel, diag.Related{File: b.File, Range: b.NameRange, Message: "also defined here"})
			}
		}
		s.Diagnostics = append(s.Diagnostics, diag.Diagnostic{
			Code: diag.CodeDefDuplicate, Severity: diag.Error, File: a.File, Range: a.NameRange,
			Message: fmt.Sprintf("$%s is defined %d times; names must be unique across the project. Rename all but one", a.Name, len(all)),
			Related: rel,
		})
	}
}

func (s *Spec) stems() {
	stems := make([]string, 0, len(s.byStem))
	for st := range s.byStem {
		stems = append(stems, st)
	}
	sort.Strings(stems)
	for _, st := range stems {
		fs := s.byStem[st]
		if len(fs) < 2 {
			continue
		}
		for _, f := range fs {
			var rel []diag.Related
			for _, g := range fs {
				if g != f {
					rel = append(rel, diag.Related{File: g.Path, Message: "same stem"})
				}
			}
			s.Diagnostics = append(s.Diagnostics, diag.Diagnostic{
				Code: diag.CodeFileStemDuplicate, Severity: diag.Warning, File: f.Path,
				Message: fmt.Sprintf("%d spec files have the stem %q, so §%s:N references are ambiguous; rename one of the files", len(fs), st, st),
				Related: rel,
			})
		}
	}
}

func (s *Spec) resolveRefs() {
	for _, f := range s.included() {
		for _, o := range f.Occurrences {
			if o.Role != RoleReference {
				continue
			}
			rr := &ResolvedRef{Occurrence: o}
			switch as := s.ByName[o.Name]; len(as) {
			case 0:
				rr.State = Dangling
				s.Diagnostics = append(s.Diagnostics, diag.Diagnostic{
					Code: diag.CodeRefDangling, Severity: diag.Error, File: o.File, Range: o.Range,
					Message: fmt.Sprintf("$%s is not defined anywhere in the project", o.Name),
				})
			case 1:
				rr.State = Resolved
				rr.Target = as[0]
			default:
				rr.State = Ambiguous
				rr.Candidates = append([]*Anchor(nil), as...)
			}
			s.Refs = append(s.Refs, rr)
			s.refOf[o] = rr
		}
	}
}

// Resolve returns the resolution of a reference occurrence of one of the
// included files (for example from File.OccurrenceAt), or nil for a
// definition, an occurrence of an excluded file, or one from a File this
// Spec was not built from.
func (s *Spec) Resolve(o *Occurrence) *ResolvedRef {
	return s.refOf[o]
}

// ResolveSectionRef returns the resolution of a section reference of one of
// the included files (for example from File.SectionRefAt), or nil for one
// from an excluded file or a File this Spec was not built from.
func (s *Spec) ResolveSectionRef(r *SectionRef) *ResolvedSectionRef {
	return s.secRefOf[r]
}

// sectionsIn returns the sections of f numbered number.
func sectionsIn(f *File, number string) []*Section {
	var out []*Section
	for _, sec := range f.Sections {
		if sec.Number == number {
			out = append(out, sec)
		}
	}
	return out
}

// ResolveSection resolves a section reference made from file from (plan
// 1.6). A qualified reference (stem != "") resolves in the file with that
// stem, and is ambiguous when several files share the stem. An unqualified
// one resolves in from first, then project-wide when exactly one file has
// the number.
func (s *Spec) ResolveSection(from, stem, number string) (Resolution, *Section, []*Section) {
	var cands []*Section
	if stem != "" {
		fs := s.byStem[stem]
		for _, f := range fs {
			cands = append(cands, sectionsIn(f, number)...)
		}
		if len(fs) > 1 && len(cands) > 0 {
			return Ambiguous, nil, cands
		}
	} else {
		if f := s.byPath[from]; f != nil && f.ValidUTF8 {
			cands = sectionsIn(f, number)
		}
		if len(cands) == 0 {
			for _, f := range s.included() {
				cands = append(cands, sectionsIn(f, number)...)
			}
		}
	}
	switch len(cands) {
	case 0:
		return Dangling, nil, nil
	case 1:
		return Resolved, cands[0], nil
	}
	return Ambiguous, nil, cands
}

func (s *Spec) resolveSectionRefs() {
	for _, f := range s.included() {
		for _, r := range f.SectionRefs {
			rr := &ResolvedSectionRef{SectionRef: r}
			rr.State, rr.Target, rr.Candidates = s.ResolveSection(r.File, r.Stem, r.Number)
			s.SectionRefs = append(s.SectionRefs, rr)
			s.secRefOf[r] = rr
			written := "§" + r.Number
			if r.Stem != "" {
				written = "§" + r.Stem + ":" + r.Number
			}
			switch rr.State {
			case Dangling:
				msg := fmt.Sprintf("%s does not match any numbered section", written)
				if r.Stem != "" && len(s.byStem[r.Stem]) == 0 {
					msg = fmt.Sprintf("%s: no spec file has the stem %q", written, r.Stem)
				}
				s.Diagnostics = append(s.Diagnostics, diag.Diagnostic{
					Code: diag.CodeSectionRefDangling, Severity: diag.Error, File: r.File, Range: r.Range, Message: msg,
				})
			case Ambiguous:
				var forms, files []string
				var rel []diag.Related
				seen := map[string]bool{}
				seenFile := map[string]bool{}
				for _, c := range rr.Candidates {
					rel = append(rel, diag.Related{File: c.File, Range: c.HeadingRange, Message: "candidate " + c.Qualified()})
					if q := c.Qualified(); !seen[q] {
						seen[q] = true
						forms = append(forms, q)
					}
					if !seenFile[c.File] {
						seenFile[c.File] = true
						files = append(files, c.File)
					}
				}
				var msg string
				switch {
				case len(files) == 1:
					// One file numbers two sections the same (section-dup).
					msg = fmt.Sprintf("%s is ambiguous: %s numbers more than one section %s. Renumber the duplicate section", written, files[0], r.Number)
				case len(forms) < len(files):
					// Some candidates share a stem, so qualifying cannot help.
					msg = fmt.Sprintf("%s is ambiguous: it matches sections in %s, and spec files that share a stem cannot be told apart by §stem:N. Rename one of those files so each stem is unique",
						written, strings.Join(files, ", "))
				default:
					msg = fmt.Sprintf("%s is ambiguous: it matches %s. Write the file-qualified form instead, e.g. %s",
						written, strings.Join(forms, ", "), forms[0])
				}
				s.Diagnostics = append(s.Diagnostics, diag.Diagnostic{
					Code: diag.CodeSectionRefAmbiguous, Severity: diag.Error, File: r.File, Range: r.Range,
					Message: msg, Related: rel,
				})
			}
		}
	}
}

// File returns the file with project-relative path p, or nil.
func (s *Spec) File(p string) *File {
	return s.byPath[p]
}

// FilesWithStem returns the included files whose stem is stem.
func (s *Spec) FilesWithStem(stem string) []*File {
	return s.byStem[stem]
}

// Lookup returns every anchor named name (without $).
func (s *Spec) Lookup(name string) []*Anchor {
	return s.ByName[name]
}

// Anchor returns the anchor named name when exactly one exists, else nil.
func (s *Spec) Anchor(name string) *Anchor {
	if as := s.ByName[name]; len(as) == 1 {
		return as[0]
	}
	return nil
}

// ReferencesTo returns every reference whose name is name (resolved,
// dangling or ambiguous), in project order.
func (s *Spec) ReferencesTo(name string) []*ResolvedRef {
	var out []*ResolvedRef
	for _, r := range s.Refs {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

// ReferencedBy returns the distinct anchors whose bodies reference name,
// excluding anchors named name (self-references do not count), in project
// order.
func (s *Spec) ReferencedBy(name string) []*Anchor {
	var out []*Anchor
	seen := map[*Anchor]bool{}
	for _, r := range s.Refs {
		if r.Name != name || r.Within == nil || r.Within.Name == name || seen[r.Within] {
			continue
		}
		seen[r.Within] = true
		out = append(out, r.Within)
	}
	return out
}

// Mentions returns the distinct prose containers (outside every anchor
// body) holding a reference to name, in project order.
func (s *Spec) Mentions(name string) []Mention {
	var out []Mention
	seen := map[Mention]bool{}
	for _, r := range s.Refs {
		if r.Name != name || r.Within != nil || seen[r.Container] {
			continue
		}
		seen[r.Container] = true
		out = append(out, r.Container)
	}
	return out
}

// FindSections returns every section numbered number, in project order.
func (s *Spec) FindSections(number string) []*Section {
	var out []*Section
	for _, sec := range s.Sections {
		if sec.Number == number {
			out = append(out, sec)
		}
	}
	return out
}

// AnchorsIn returns the anchors whose labels lie inside sec's range.
func (s *Spec) AnchorsIn(sec *Section) []*Anchor {
	var out []*Anchor
	for _, a := range s.Anchors {
		if a.File == sec.File && a.LabelRange.Start >= sec.Range.Start && a.LabelRange.Start < sec.Range.End {
			out = append(out, a)
		}
	}
	return out
}

// SectionReferencesTo returns the section references that resolve to sec.
func (s *Spec) SectionReferencesTo(sec *Section) []*ResolvedSectionRef {
	var out []*ResolvedSectionRef
	for _, r := range s.SectionRefs {
		if r.Target == sec {
			out = append(out, r)
		}
	}
	return out
}
