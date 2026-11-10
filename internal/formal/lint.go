// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package formal

import (
	"fmt"
	"strings"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// uncitedPrefix marks the operators that must carry a tag: invariants. Most
// other operators are model machinery with nothing to cite.
const uncitedPrefix = "Inv_"

// Lint returns the findings about the models, with default severities;
// lint.Run applies a project's overrides when given them as Options.Extra.
func (ix *Index) Lint() []diag.Diagnostic {
	if ix == nil {
		return nil
	}
	var out []diag.Diagnostic
	bodies := map[any]string{}
	for _, f := range ix.Files {
		if f.ReadError != "" {
			out = append(out, diag.Diagnostic{
				Code: diag.CodeFormalTagMalformed, Severity: diag.Hint, File: f.Path,
				Message: fmt.Sprintf("%s could not be read, so its tags were not read: %s", f.Path, f.ReadError),
			})
			continue
		}
		if !f.ValidUTF8 {
			out = append(out, diag.Diagnostic{
				Code: diag.CodeFormalTagMalformed, Severity: diag.Hint, File: f.Path,
				Message: fmt.Sprintf("%s is not valid UTF-8, so its tags were not read", f.Path),
			})
			continue
		}
		for _, p := range f.Syntax {
			out = append(out, diag.Diagnostic{Code: diag.CodeFormalSyntax, Severity: p.Severity, File: f.Path, Range: p.Range, Message: p.Message})
		}
		for _, t := range f.Tags {
			out = append(out, ix.lintTag(f, t, bodies)...)
		}
		if !f.Mutant {
			out = append(out, lintUncited(f)...)
		}
	}
	return out
}

func (ix *Index) lintTag(f *File, t *Tag, bodies map[any]string) []diag.Diagnostic {
	var out []diag.Diagnostic
	add := func(code string, sev diag.Severity, r pos.Range, format string, args ...any) {
		out = append(out, diag.Diagnostic{Code: code, Severity: sev, File: f.Path, Range: r, Message: fmt.Sprintf(format, args...)})
	}
	if t.Problem != "" {
		add(diag.CodeFormalTagMalformed, diag.Hint, t.Range, "%s", t.Problem)
	}
	if t.Block {
		add(diag.CodeFormalTagBlock, diag.Hint, t.Range, "this tag is in a (* *) comment, where quote text can end the comment; write it as a \\* line")
	}
	if t.Decl != nil && t.Decl.Kind == DeclSymbolic {
		add(diag.CodeFormalTagMalformed, diag.Hint, t.Range, "%s is a symbolic operator, which a tag cannot name; the tag still resolves", t.Decl.Name)
	}
	if t.Kind == TagSpecFile {
		if t.Stem != "" && len(ix.Spec.FilesWithStem(t.Stem)) == 0 {
			add(diag.CodeFormalRefDangling, diag.Error, t.StemRange, "no spec file has the stem %q", t.Stem)
		}
		return out
	}
	l := ix.links[t]
	if l == nil {
		return out
	}
	switch l.State {
	case mdspec.Dangling:
		add(diag.CodeFormalRefDangling, diag.Error, t.Ref.Range, "%s", danglingMessage(ix.Spec, t.Ref))
	case mdspec.Ambiguous:
		add(diag.CodeFormalRefDangling, diag.Error, t.Ref.Range, "%s", ambiguousMessage(l))
	case mdspec.Resolved:
		if ix.quoteStale(l, bodies) {
			add(diag.CodeFormalQuoteStale, diag.Hint, t.QuoteRange, "this quote is not in %s; update it, or point the tag at the text that says it", t.Ref.Written())
		}
	}
	return out
}

// QuoteStale reports whether a resolved tag quotes text its target lacks.
func (ix *Index) QuoteStale(l *Link) bool {
	return l.State == mdspec.Resolved && ix.quoteStale(l, map[any]string{})
}

func (ix *Index) quoteStale(l *Link, bodies map[any]string) bool {
	return l.Tag.Quote != "" && !strings.Contains(ix.body(l, bodies), collapse(l.Tag.Quote))
}

func danglingMessage(spec *mdspec.Spec, r *Ref) string {
	switch {
	case r.IsAnchor():
		return fmt.Sprintf("$%s is not defined in the spec", r.Name)
	case r.Stem != "" && len(spec.FilesWithStem(r.Stem)) == 0:
		return fmt.Sprintf("%s: no spec file has the stem %q", r.Written(), r.Stem)
	}
	return fmt.Sprintf("%s does not match any numbered section", r.Written())
}

func ambiguousMessage(l *Link) string {
	if l.Tag.Ref.IsAnchor() {
		return fmt.Sprintf("%s is defined %d times in the spec", l.Tag.Ref.Written(), len(l.Anchors))
	}
	var forms []string
	seen := map[string]bool{}
	for _, c := range l.Sections {
		if q := c.Qualified(); !seen[q] {
			seen[q] = true
			forms = append(forms, q)
		}
	}
	return fmt.Sprintf("%s is ambiguous: it matches %s. Qualify it, or name the file once with a \\* Spec-file: tag", l.Tag.Ref.Written(), strings.Join(forms, ", "))
}

// lintUncited flags the invariants of a model that carry no tag at all. A
// malformed tag counts: its own hint already points there.
func lintUncited(f *File) []diag.Diagnostic {
	tagged := map[*Decl]bool{}
	for _, t := range f.Tags {
		if t.Decl != nil {
			tagged[t.Decl] = true
		}
	}
	var out []diag.Diagnostic
	for _, d := range f.Decls {
		if d.Kind != DeclDefinition || !strings.HasPrefix(d.Name, uncitedPrefix) || tagged[d] {
			continue
		}
		out = append(out, diag.Diagnostic{
			Code: diag.CodeFormalOpUncited, Severity: diag.Warning, File: f.Path, Range: d.NameRange,
			Message: fmt.Sprintf("invariant %s cites nothing; add a \\* Spec: tag above it, naming the statement it checks", d.Name),
		})
	}
	return out
}

// body returns the collapsed text a quote must appear in: an anchor's body or
// a section's text. bodies caches it, since many tags cite the same section.
func (ix *Index) body(l *Link, bodies map[any]string) string {
	var key any
	var file string
	var r pos.Range
	switch {
	case l.Anchor != nil:
		key, file, r = l.Anchor, l.Anchor.File, l.Anchor.BodyRange
	case l.Section != nil:
		key, file, r = l.Section, l.Section.File, l.Section.Range
	default:
		return ""
	}
	if b, ok := bodies[key]; ok {
		return b
	}
	f := ix.Spec.File(file)
	if f == nil {
		return ""
	}
	b := collapse(f.Text(r))
	bodies[key] = b
	return b
}

// collapse puts text in the form quotes are compared in. A quote is about the
// words, so line breaks, indentation, letter case and inline Markdown emphasis
// do not count: a tag writes "then advance last_distilled_index" for a spec's
// "Then advance `last_distilled_index`".
func collapse(s string) string {
	s = strings.NewReplacer("*", "", "`", "").Replace(s)
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
