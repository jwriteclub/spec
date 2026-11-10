// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package formal links formal models to the spec they check. A model is not
// a spec file: it is read as a separate set, never hashed or formatted. A
// person edits it directly; an agent files a model proposal (`spec change
// --tla`). The link is authored only in the model, as comment tags:
//
//	\* Spec: $Queue.OneActiveTurn "exactly one active turn per session"
//	\* Derived-from: §4.1 "ephemeral queue"
//	\* Spec-file: 04-queue
//
// The tag grammar here is language-neutral. A language contributes a Scanner
// that finds comments, declarations and syntax errors; see package formal/tla,
// which parses TLA+ with prudence. Join resolves
// tags against a mdspec.Spec, and the result answers both directions.
package formal

import (
	"path"
	"strings"
	"unicode/utf8"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// Scanner reads a model's source into a File. It keeps nothing but what the
// File holds, must not fail, and must tolerate any bytes: an editor buffer is
// often mid-edit.
type Scanner func(path string, src []byte) *File

// mutantMark is what marks a deliberately broken variant in a model's name.
const mutantMark = "_Mut"

// DeclKind says what a declaration declares.
type DeclKind string

// Declaration kinds.
const (
	DeclDefinition DeclKind = "definition"
	DeclVariable   DeclKind = "variable"
	DeclConstant   DeclKind = "constant"
	DeclTheorem    DeclKind = "theorem"
	// DeclSymbolic is an operator defined with a symbol, such as `a ++ b ==`.
	// Tags do not bind to it usefully, so a tag on one draws a hint.
	DeclSymbolic DeclKind = "symbolic"
)

// Decl is one top-level declaration of a model.
type Decl struct {
	// Name is the declared name; for a symbolic operator, its symbol.
	Name string
	Kind DeclKind
	// NameRange covers the name or the symbol.
	NameRange pos.Range
	// Range covers the whole declaration, from its first token to its last.
	Range pos.Range
}

// Problem is a syntax error, or a warning, that a language's parser found in
// a model.
type Problem struct {
	Severity diag.Severity
	// Code is the parser's own class of problem, such as "lexical".
	Code    string
	Message string
	Range   pos.Range
}

// TagKind says which keyword a tag used.
type TagKind string

// Tag kinds, spelled as written in a model.
const (
	TagSpec     TagKind = "Spec"
	TagDerived  TagKind = "Derived-from"
	TagSpecFile TagKind = "Spec-file"
)

// Ref is the reference of a tag: an anchor (Name) or a section (Number, with
// an optional Stem).
type Ref struct {
	Name   string
	Stem   string
	Number string
	// Range covers the token from its sigil to its end.
	Range pos.Range
}

// IsAnchor reports whether the reference names an anchor.
func (r *Ref) IsAnchor() bool {
	return r.Name != ""
}

// Written returns the reference as a tag writes it.
func (r *Ref) Written() string {
	switch {
	case r.Name != "":
		return "$" + r.Name
	case r.Stem != "":
		return "§" + r.Stem + ":" + r.Number
	}
	return "§" + r.Number
}

// NameRange covers the name of an anchor reference without its $.
func (r *Ref) NameRange() pos.Range {
	return pos.Range{Start: r.Range.Start + 1, End: r.Range.End}
}

// Tag is one tag in a model's comments.
type Tag struct {
	Kind TagKind
	// Block is true for a tag inside a (* *) comment.
	Block bool
	// Range covers the tag: the comment from its opening marker for a line
	// comment, from the keyword for a block comment.
	Range pos.Range
	// Ref is the reference of a Spec or Derived-from tag; nil when it has
	// none or it does not parse.
	Ref *Ref
	// Stem and StemRange are the stem of a Spec-file tag.
	Stem      string
	StemRange pos.Range
	// Quote is the quoted text without its quotes; QuoteRange covers it.
	Quote      string
	QuoteRange pos.Range
	// Problem says what is malformed about the tag; "" when nothing is.
	Problem string
	// Decl is the declaration the tag binds to; nil when it binds to none.
	Decl *Decl
}

// File is one scanned model.
type File struct {
	Path string
	// Module is the module's name, "" when it has no module header.
	Module string
	Decls  []*Decl
	// Stale holds declarations kept from an earlier, error-free scan (see
	// Recover) whose place in this text is unknown: an edit since then
	// overlaps them. Their ranges are only near where they were. They are for
	// display only: no lookup, tag, link or jump uses them.
	Stale []*Decl
	Tags  []*Tag
	// Tokens are the highlight tokens of the source, in order; nil when the
	// language gives none.
	Tokens []Token
	// LineComments are the single-line comments, after their opening marker.
	LineComments []pos.Range
	// Syntax holds what the parser found wrong with the model's text. When it
	// holds an error, Decls may lack the declarations after it.
	Syntax []Problem
	// Mutant is true for a deliberately broken variant of a model; see
	// IsMutant. Mutants are indexed but do not count as coverage.
	Mutant bool
	// ValidUTF8 is false for a file that is not UTF-8; such a file holds no
	// declarations and no tags.
	ValidUTF8 bool
	// ReadError says why the file could not be read; "" when it was. Such a
	// file holds no declarations and no tags, and draws a hint.
	ReadError string

	src   []byte
	index *pos.Index
}

// NewFile returns an empty File for a scanner to fill.
func NewFile(p string, src []byte) *File {
	return &File{
		Path: p, Mutant: IsMutant(p), ValidUTF8: utf8.Valid(src),
		src: src, index: pos.NewIndex(src),
	}
}

// IsMutant reports whether a model's path is a mutant's: its base name holds
// _Mut, not followed by a lowercase letter. Queue_Mut1_NoLock.tla is a mutant;
// Queue_Mutex.tla is a model about a mutex.
func IsMutant(p string) bool {
	base := path.Base(p)
	for at := 0; ; {
		i := strings.Index(base[at:], mutantMark)
		if i < 0 {
			return false
		}
		at += i + len(mutantMark)
		if at == len(base) || base[at] < 'a' || base[at] > 'z' {
			return true
		}
	}
}

// HasError reports whether the parser found a syntax error in the file.
func (f *File) HasError() bool {
	for _, p := range f.Syntax {
		if p.Severity == diag.Error {
			return true
		}
	}
	return false
}

// Source returns the file's bytes.
func (f *File) Source() []byte { return f.src }

// Index returns the file's position index.
func (f *File) Index() *pos.Index { return f.index }

// Text returns the bytes of a range as a string.
func (f *File) Text(r pos.Range) string {
	if !r.Valid(len(f.src)) {
		return ""
	}
	return string(f.src[r.Start:r.End])
}

// TagAt returns the tag whose range holds off (its end included, like the
// other point lookups), or nil.
func (f *File) TagAt(off int) *Tag {
	for _, t := range f.Tags {
		if off >= t.Range.Start && off <= t.Range.End {
			return t
		}
	}
	return nil
}

// DeclAt returns the declaration whose name holds off, or nil.
func (f *File) DeclAt(off int) *Decl {
	for _, d := range f.Decls {
		if off >= d.NameRange.Start && off <= d.NameRange.End {
			return d
		}
	}
	return nil
}

// TagsOf returns the tags bound to d, in file order.
func (f *File) TagsOf(d *Decl) []*Tag {
	var out []*Tag
	for _, t := range f.Tags {
		if t.Decl == d {
			out = append(out, t)
		}
	}
	return out
}

// RefSlot reports whether a token starting at start sits where a tag's
// reference goes: in a line comment, right after Spec: or Derived-from:.
func (f *File) RefSlot(start int) bool {
	for _, c := range f.LineComments {
		if start < c.Start || start > c.End {
			continue
		}
		before := strings.TrimSpace(string(f.src[c.Start:start]))
		return before == string(TagSpec)+":" || before == string(TagDerived)+":"
	}
	return false
}

// Mentions returns the ranges of "$name" (sigil included) in the file's bytes,
// whole names only: the rule of `spec explain markup`.
func (f *File) Mentions(name string) []pos.Range {
	var out []pos.Range
	needle := "$" + name
	text := string(f.src)
	for at := 0; ; {
		i := strings.Index(text[at:], needle)
		if i < 0 {
			return out
		}
		start := at + i
		end := start + len(needle)
		at = end
		if start > 0 && (isNameByte(text[start-1]) || text[start-1] == '$' || text[start-1] == '\\') {
			continue
		}
		if munchName(f.src, start+1) != end {
			continue
		}
		out = append(out, pos.Range{Start: start, End: end})
	}
}

func isNameByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
}

// munchName returns the end of the name run at i: the longest run of name
// bytes, taking a '.' only when a name byte follows it.
func munchName(b []byte, i int) int {
	j := i
	for j < len(b) {
		switch {
		case isNameByte(b[j]):
			j++
		case b[j] == '.' && j+1 < len(b) && isNameByte(b[j+1]):
			j++
		default:
			return j
		}
	}
	return j
}

// sectionSign is "§" (U+00A7) in UTF-8.
const sectionSign = "§"

// isBlank reports whether c is a space or tab.
func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// skipBlanks returns the first offset in [i, end) that is not a blank.
func skipBlanks(src []byte, i, end int) int {
	for i < end && isBlank(src[i]) {
		i++
	}
	return i
}

// trimEnd returns end moved back over blanks and carriage returns.
func trimEnd(src []byte, start, end int) int {
	for end > start && (isBlank(src[end-1]) || src[end-1] == '\r') {
		end--
	}
	return end
}

// ParseTag reads a tag from the comment text src[start:lineEnd], which begins
// at the keyword after optional blanks. limit is how far a quote may run: past
// lineEnd only in a block comment, where existing tags wrap their quotes. It
// returns nil when the text is no tag. The caller sets Block, Decl and, when
// the tag should cover its comment marker, Range.Start.
func ParseTag(src []byte, start, lineEnd, limit int) *Tag {
	i := skipBlanks(src, start, lineEnd)
	var kind TagKind
	for _, k := range []TagKind{TagSpecFile, TagDerived, TagSpec} {
		w := string(k) + ":"
		if lineEnd-i >= len(w) && string(src[i:i+len(w)]) == w {
			after := i + len(w)
			if after == lineEnd || isBlank(src[after]) || src[after] == '\r' {
				kind = k
				i = after
				break
			}
		}
	}
	if kind == "" {
		return nil
	}
	t := &Tag{Kind: kind, Range: pos.Range{Start: skipBlanks(src, start, lineEnd), End: trimEnd(src, start, lineEnd)}}
	i = skipBlanks(src, i, t.Range.End)
	if kind == TagSpecFile {
		parseSpecFile(t, src, i, t.Range.End)
		return t
	}
	refEnd := parseRef(t, src, i, t.Range.End)
	if t.Ref != nil {
		parseQuote(t, src, refEnd, lineEnd, limit)
	}
	return t
}

// parseSpecFile reads the stem of a Spec-file tag.
func parseSpecFile(t *Tag, src []byte, i, end int) {
	j := i
	for j < end && !isBlank(src[j]) {
		j++
	}
	if j == i {
		t.Problem = "Spec-file: names no spec file; write its stem, the file name without .spec.md or its extension"
		return
	}
	t.Stem = string(src[i:j])
	t.StemRange = pos.Range{Start: i, End: j}
	if skipBlanks(src, j, end) != end {
		t.Problem = "Spec-file: takes one stem per tag; put each file on its own line"
	}
}

// parseRef reads a tag's reference at i and returns where it ends; on failure
// it sets t.Problem and returns i.
func parseRef(t *Tag, src []byte, i, end int) int {
	text := string(src[i:end])
	switch {
	case text == "":
		t.Problem = string(t.Kind) + ": has no reference; write $Name or §N"
		return i
	case text[0] == '$':
		e := munchName(src[:end], i+1)
		name := string(src[i+1 : e])
		if !mdspec.ValidName(name) {
			t.Problem = "$" + name + " is not a valid anchor name, so the tag has no reference"
			return i
		}
		t.Ref = &Ref{Name: name, Range: pos.Range{Start: i, End: e}}
		return e
	case strings.HasPrefix(text, sectionSign):
		j := i + len(sectionSign)
		if j < end && isBlank(src[j]) {
			j++
		}
		stem, number, n, ok := mdspec.ScanSectionRef(src[j:end])
		if !ok {
			t.Problem = "the section reference does not parse; write §N.N or §stem:N.N, as in §4.2 or §hls:4.2"
			return i
		}
		t.Ref = &Ref{Stem: stem, Number: number, Range: pos.Range{Start: i, End: j + n}}
		return j + n
	}
	if word, _, ok := strings.Cut(text, sectionSign); ok {
		t.Problem = "`" + strings.TrimSpace(word) + " §…` is not a reference; a file is named by its stem, as in §stem:4.2"
		return i
	}
	t.Problem = string(t.Kind) + ": has no reference; write $Name or §N, or use a plain comment"
	return i
}

// parseQuote reads the first quoted text after a reference. It ends at the
// next '"', so a quote cannot hold one. Other trailing text is commentary.
func parseQuote(t *Tag, src []byte, i, lineEnd, limit int) {
	open := -1
	for j := i; j < lineEnd; j++ {
		if src[j] == '"' {
			open = j
			break
		}
	}
	if open < 0 {
		return
	}
	for j := open + 1; j < limit; j++ {
		if src[j] == '"' {
			t.Quote = string(src[open+1 : j])
			t.QuoteRange = pos.Range{Start: open + 1, End: j}
			if j >= lineEnd {
				t.Range.End = j + 1
			}
			return
		}
	}
	t.Problem = "the quote is not closed; a quote cannot hold a \""
}
