// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package mdspec parses *.spec.md files into a model of anchors (invariants
// and definitions), `$Name` references, numbered sections and `§` section
// references, and resolves references across files.
//
// The model is the contract shared by lint, the CLI, the language server and
// the TUI:
//
//   - ParseFile parses one file and never fails; problems become diagnostics.
//   - Build resolves a set of parsed files into a Spec.
//   - Every range is a half-open [Start, End) byte range into the file's raw
//     source (including a leading BOM); convert with File.Index (package pos).
//   - A File and everything it points to is immutable after ParseFile and may
//     be shared by several Specs (the language server rebuilds a Spec when one
//     file changes and reuses the other Files). Resolution state lives in the
//     Spec, not in the File.
//
// Diagnostics produced here: ParseFile emits file-not-utf8, file-bom,
// def-malformed, def-nested and section-dup; Build emits ref-dangling,
// section-ref-dangling, section-ref-ambiguous, def-duplicate and
// file-stem-duplicate. All other rules belong to package lint, which uses the
// model fields documented below (Occurrence.InTitle, Anchor.CodeAfter,
// File.Candidates, File.MalformedSectionRefs, File.HashRefs).
package mdspec

import (
	"sort"

	"github.com/yuin/goldmark/ast"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/pos"
)

// Kind is the kind of an anchor.
type Kind string

// Anchor kinds.
const (
	// KindInvariant is an invariant: a label without the Def prefix.
	KindInvariant Kind = "inv"
	// KindDefinition is a definition: a label with the Def prefix.
	KindDefinition Kind = "def"
)

// Form is the syntactic form of an anchor's definition site.
type Form string

// Anchor forms.
const (
	// FormParagraph is a bold label opening a top-level paragraph.
	FormParagraph Form = "paragraph"
	// FormSection is a heading whose whole text is the label.
	FormSection Form = "section"
)

// Role says whether an occurrence of $Name defines or references the name.
type Role string

// Occurrence roles.
const (
	// RoleDefinition is the $Name token of a valid definition label.
	RoleDefinition Role = "definition"
	// RoleReference is every other $Name token, including those inside
	// titles and inside malformed or nested labels.
	RoleReference Role = "reference"
)

// Resolution is the state of a resolved reference.
type Resolution string

// Resolution states.
const (
	// Resolved means exactly one target.
	Resolved Resolution = "resolved"
	// Dangling means no target.
	Dangling Resolution = "dangling"
	// Ambiguous means more than one candidate target.
	Ambiguous Resolution = "ambiguous"
)

// ContainerKind is the kind of block that holds a reference, so that `spec
// show` and hover agree on "the paragraph that mentions X".
type ContainerKind string

// Container kinds.
const (
	ContainerParagraph  ContainerKind = "paragraph"
	ContainerListItem   ContainerKind = "list_item"
	ContainerHeading    ContainerKind = "heading"
	ContainerTableCell  ContainerKind = "table_cell"
	ContainerBlockquote ContainerKind = "blockquote"
)

// OpaqueKind is the kind of an opaque region, where no token is produced.
type OpaqueKind string

// Opaque region kinds.
const (
	OpaqueCodeSpan    OpaqueKind = "code_span"
	OpaqueCodeBlock   OpaqueKind = "code_block"
	OpaqueHTMLBlock   OpaqueKind = "html_block"
	OpaqueRawHTML     OpaqueKind = "raw_html"
	OpaqueAutoLink    OpaqueKind = "autolink"
	OpaqueLinkDest    OpaqueKind = "link_destination"
	OpaqueFrontMatter OpaqueKind = "front_matter"
)

// Mention is the block that contains a reference.
//
// Kind is heading for references in a heading, table_cell for a table cell,
// list_item when the reference's paragraph sits directly in a list item (the
// range is the whole item, nested lists included), blockquote when it sits
// directly in a blockquote (the range is the whole blockquote), and paragraph
// otherwise.
type Mention struct {
	File  string        `json:"file"`
	Range pos.Range     `json:"range"`
	Kind  ContainerKind `json:"kind"`
}

// Anchor is one definition site: an invariant or a definition.
type Anchor struct {
	Kind  Kind   `json:"kind"`
	Form  Form   `json:"form"`
	Name  string `json:"name"`  // without the $ sigil
	Title string `json:"title"` // "" when the label has no title
	File  string `json:"file"`

	// LabelRange is the bold label (paragraph form, delimiters included) or
	// the heading (section form, from the first '#' to the end of the heading
	// text, or through the underline of a setext heading).
	LabelRange pos.Range `json:"label_range"`
	// NameRange is the $Name token, sigil included.
	NameRange pos.Range `json:"name_range"`
	// TitleRange is the title text without its parentheses or ": " prefix;
	// empty (Start == End == 0) when there is no title.
	TitleRange pos.Range `json:"title_range"`
	// BodyRange starts at the label and ends at the end of the last body
	// line, trailing whitespace excluded. Paragraph form: the paragraph plus
	// an immediately following list or blockquote. Section form: the heading
	// through the content before the next top-level heading of the same or
	// a higher level.
	BodyRange pos.Range `json:"body_range"`

	// Hash is the v1 body hash (64 hex characters); ShortHash is its first 8.
	Hash      string `json:"hash"`
	ShortHash string `json:"short_hash"`

	// CodeAfter is true when a fenced code block starts on the line right
	// after the body (lint hint anchor-code-after); it is not part of the body.
	CodeAfter bool `json:"code_after"`

	// Section is the innermost numbered section containing the label, or nil.
	Section *Section `json:"-"`

	src       []byte
	input     hashInput
	titleRefs []refSpan // references inside TitleRange, names without $
}

// Occurrence is one $Name token: a definition or a reference.
type Occurrence struct {
	Role Role   `json:"role"`
	Name string `json:"name"` // without the $ sigil
	File string `json:"file"`
	// Range covers the token, $ included.
	Range pos.Range `json:"range"`
	// InTitle is true for a reference inside a label's title (lint hint
	// ref-in-title: titles are not hashed).
	InTitle bool `json:"in_title"`
	// Container is the block holding the token.
	Container Mention `json:"container"`
	// Defines is the anchor this token defines (definitions only).
	Defines *Anchor `json:"-"`
	// Within is the anchor whose body contains the token, or nil (for a
	// definition, the anchor itself).
	Within *Anchor `json:"-"`
}

// NameRange returns the token's range without the $ sigil.
func (o *Occurrence) NameRange() pos.Range {
	return pos.Range{Start: o.Range.Start + 1, End: o.Range.End}
}

// Section is a numbered heading such as "## 5.2a. Retry".
type Section struct {
	File   string `json:"file"`
	Number string `json:"number"` // without a trailing period, e.g. "5.2a"
	Title  string `json:"title"`
	Level  int    `json:"level"` // heading level, 1 to 6
	// HeadingRange is the heading line (and setext underline).
	HeadingRange pos.Range `json:"heading_range"`
	// NumberRange is the number token in the heading, trailing period
	// included.
	NumberRange pos.Range `json:"number_range"`
	// Range runs from the heading to the end of the content before the next
	// top-level heading of the same or a higher level.
	Range pos.Range `json:"range"`
}

// Qualified returns the file-qualified reference form, e.g. "§hls:5.2a".
func (s *Section) Qualified() string {
	return "§" + StemOf(s.File) + ":" + s.Number
}

// SectionRef is one § section reference, e.g. "§5.2a", "§ 5.2" or
// "§hls:1.2".
type SectionRef struct {
	File string `json:"file"`
	// Range covers the token from § through the number.
	Range pos.Range `json:"range"`
	// Stem is the file qualifier ("" when unqualified).
	Stem string `json:"stem"`
	// Number is the section number, e.g. "5.2a".
	Number    string  `json:"number"`
	Container Mention `json:"container"`
	// Within is the anchor whose body contains the reference, or nil.
	Within *Anchor `json:"-"`
}

// Candidate is a $ token candidate that is not a valid name, such as
// "$Frame.timing" or "$5.00". lint uses these for ref-nearly.
type Candidate struct {
	// Range covers the candidate, $ included.
	Range pos.Range `json:"range"`
	// Text is the candidate without the $.
	Text string `json:"text"`
}

// HashRef is "#N.N…" text outside opaque regions (lint hint section-hash-ref
// when it would resolve as a section).
type HashRef struct {
	Range  pos.Range `json:"range"` // "#" included
	Number string    `json:"number"`
}

// Opaque is a region where no token is ever produced.
type Opaque struct {
	Kind  OpaqueKind `json:"kind"`
	Range pos.Range  `json:"range"`
}

// File is one parsed spec file. All fields are read-only after ParseFile.
type File struct {
	// Path is the project-relative slash path given to ParseFile.
	Path string
	// ValidUTF8 is false when the source is not valid UTF-8; such a file
	// carries a file-not-utf8 error and Build excludes it.
	ValidUTF8 bool
	// HasBOM is true when the source starts with a UTF-8 byte order mark.
	HasBOM bool

	// Anchors are the valid definition sites, in source order (a name
	// defined twice in one file appears twice).
	Anchors []*Anchor
	// Occurrences are all $Name tokens, definitions and references, in
	// source order.
	Occurrences []*Occurrence
	// Sections are the numbered top-level headings, in source order.
	Sections []*Section
	// SectionRefs are all § references, in source order.
	SectionRefs []*SectionRef
	// Candidates are invalid $ candidates, in source order.
	Candidates []Candidate
	// MalformedSectionRefs are §-number tokens followed by a letter or
	// digit, such as "§5.2ab" (lint hint section-ref-malformed).
	MalformedSectionRefs []pos.Range
	// HashRefs are "#N.N…" tokens outside opaque regions.
	HashRefs []HashRef
	// Opaque lists the opaque regions, sorted by start.
	Opaque []Opaque
	// Diagnostics are the parse-level diagnostics for this file.
	Diagnostics []diag.Diagnostic

	src   []byte
	doc   ast.Node
	index *pos.Index
}

// Source returns the file's raw source. The caller must not modify it.
func (f *File) Source() []byte {
	return f.src
}

// Document returns the goldmark AST of the file, for the TUI renderer. It is
// the one goldmark type in the API. Segments index into Source(); a leading
// BOM is skipped by the parser, so no node starts inside it. The caller must
// not modify the tree.
func (f *File) Document() ast.Node {
	return f.doc
}

// Index returns the position index for Source().
func (f *File) Index() *pos.Index {
	return f.index
}

// Stem returns the file's stem: the base name minus ".spec.md".
func (f *File) Stem() string {
	return StemOf(f.Path)
}

// Text returns the source bytes of r as a string. Out-of-bounds parts of r
// are clipped.
func (f *File) Text(r pos.Range) string {
	s, e := r.Start, r.End
	if s < 0 {
		s = 0
	}
	if e > len(f.src) {
		e = len(f.src)
	}
	if s >= e {
		return ""
	}
	return string(f.src[s:e])
}

// AnchorAt returns the anchor whose body contains off, or nil. Bodies do not
// overlap.
func (f *File) AnchorAt(off int) *Anchor {
	return anchorAt(f.Anchors, off)
}

// OccurrenceAt returns the $Name token at off, or nil. Tokens never touch,
// so off may be anywhere from the token's first byte to the byte just after
// it (where an editor cursor sits after typing the name).
func (f *File) OccurrenceAt(off int) *Occurrence {
	occ := f.Occurrences
	k := sort.Search(len(occ), func(k int) bool { return occ[k].Range.Start > off }) - 1
	if k >= 0 && off <= occ[k].Range.End {
		return occ[k]
	}
	return nil
}

// SectionRefAt returns the § reference at off, or nil, with the same
// inclusive end as OccurrenceAt.
func (f *File) SectionRefAt(off int) *SectionRef {
	refs := f.SectionRefs
	k := sort.Search(len(refs), func(k int) bool { return refs[k].Range.Start > off }) - 1
	if k >= 0 && off <= refs[k].Range.End {
		return refs[k]
	}
	return nil
}

// SectionAt returns the innermost numbered section whose range contains off,
// or nil.
func (f *File) SectionAt(off int) *Section {
	var best *Section
	for _, s := range f.Sections {
		if off >= s.Range.Start && off <= s.Range.End {
			if best == nil || s.Level > best.Level {
				best = s
			}
		}
	}
	return best
}

// ResolvedRef is a reference occurrence with its resolution in a Spec.
type ResolvedRef struct {
	*Occurrence
	State Resolution `json:"state"`
	// Target is set when State is Resolved.
	Target *Anchor `json:"-"`
	// Candidates is set when State is Ambiguous: every anchor with the name.
	Candidates []*Anchor `json:"-"`
}

// ResolvedSectionRef is a section reference with its resolution in a Spec.
type ResolvedSectionRef struct {
	*SectionRef
	State Resolution `json:"state"`
	// Target is set when State is Resolved.
	Target *Section `json:"-"`
	// Candidates is set when State is Ambiguous.
	Candidates []*Section `json:"-"`
}

// OpaqueMentions returns the ranges of "$name" written inside opaque regions
// (code, HTML, link destinations, front matter), where it is not a token.
// It applies the token rules to the raw bytes (not preceded by a backslash
// or [A-Za-z0-9_$], maximal munch) and matches whole names only, so for
// name "Frame" it does not report "$Frame.Timing". `spec rename` uses it to
// report the occurrences it leaves alone.
func (f *File) OpaqueMentions(name string) []pos.Range {
	var out []pos.Range
	for _, o := range f.Opaque {
		b := f.src[o.Range.Start:o.Range.End]
		for i := 0; i < len(b); i++ {
			if b[i] != '$' {
				continue
			}
			if i > 0 && (isNameByte(b[i-1]) || b[i-1] == '$' || b[i-1] == '\\') {
				continue
			}
			j := i + 1
			for j < len(b) {
				if isNameByte(b[j]) || (b[j] == '.' && j+1 < len(b) && isNameByte(b[j+1])) {
					j++
					continue
				}
				break
			}
			if string(b[i+1:j]) == name {
				out = append(out, pos.Range{Start: o.Range.Start + i, End: o.Range.Start + j})
			}
			i = j - 1
		}
	}
	return out
}
