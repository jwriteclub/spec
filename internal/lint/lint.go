// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lint

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// Options configures Run.
type Options struct {
	// Overrides are the severity overrides from spec.toml's [lint] table.
	Overrides Overrides
	// Extra are diagnostics from outside the spec (project loading, user
	// configuration); they are merged in and subject to Overrides too.
	Extra []diag.Diagnostic
	// Covered, when set, reports whether a formal model checks the anchor of
	// that name, which satisfies checked-by-missing.
	Covered func(name string) bool
	// Cited, when set, reports whether a formal model cites the anchor of that
	// name, which counts as a reference for def-unreferenced.
	Cited func(name string) bool
}

// Run returns every diagnostic for spec: those mdspec produced while parsing
// and resolving, opts.Extra, and the findings of the rules in this package.
// Overrides then change or drop diagnostics by code ("off" removes them), and
// the result is sorted with diag.Sort. Opt-in rules (checked-by-missing) run
// only when an override enables them.
func Run(spec *mdspec.Spec, opts Options) []diag.Diagnostic {
	var out []diag.Diagnostic
	out = append(out, spec.Diagnostics...)
	out = append(out, opts.Extra...)

	enabled := func(code string) bool {
		lv, _ := opts.Overrides.level(code)
		return lv != Off
	}
	l := &linter{spec: spec}
	if enabled(diag.CodeDefUnreferenced) {
		out = append(out, l.defUnreferenced(opts.Cited)...)
	}
	if enabled(diag.CodeMathHazard) {
		out = append(out, l.mathHazard()...)
	}
	if enabled(diag.CodeAnchorLong) {
		out = append(out, l.anchorLong()...)
	}
	if enabled(diag.CodeCheckedByMissing) {
		out = append(out, l.checkedByMissing(opts.Covered)...)
	}
	if enabled(diag.CodeSectionHashRef) {
		out = append(out, l.sectionHashRef()...)
	}
	if enabled(diag.CodeSectionRefMalformed) {
		out = append(out, l.sectionRefMalformed()...)
	}
	if enabled(diag.CodeRefNearly) {
		out = append(out, l.refNearly()...)
	}
	if enabled(diag.CodeRefInTitle) {
		out = append(out, l.refInTitle()...)
	}
	if enabled(diag.CodeAnchorCodeAfter) {
		out = append(out, l.anchorCodeAfter()...)
	}

	result := make([]diag.Diagnostic, 0, len(out))
	for _, d := range out {
		if raw, ok := opts.Overrides.raw(d.Code); ok {
			if lv, err := ParseLevel(raw); err == nil {
				if lv == Off {
					continue
				}
				d.Severity = lv
			}
		}
		result = append(result, d)
	}
	diag.Sort(result)
	return result
}

type linter struct {
	spec *mdspec.Spec
}

// files returns the files that took part in Build.
func (l *linter) files() []*mdspec.File {
	var out []*mdspec.File
	for _, f := range l.spec.Files {
		if f.ValidUTF8 {
			out = append(out, f)
		}
	}
	return out
}

func (l *linter) defUnreferenced(cited func(string) bool) []diag.Diagnostic {
	refs := map[string]int{}
	for _, r := range l.spec.Refs {
		if r.Within != nil && r.Within.Name == r.Name {
			continue // a self-reference does not count
		}
		refs[r.Name]++
	}
	var out []diag.Diagnostic
	for _, a := range l.spec.Anchors {
		if a.Kind != mdspec.KindDefinition || refs[a.Name] > 0 || (cited != nil && cited(a.Name)) {
			continue
		}
		out = append(out, diag.Diagnostic{
			Code: diag.CodeDefUnreferenced, Severity: diag.Warning, File: a.File, Range: a.NameRange,
			Message: fmt.Sprintf("definition $%s is not referenced by any other anchor or text; reference it where it is used, or remove it", a.Name),
		})
	}
	return out
}

// mathHazard flags a $ that closes GitHub inline math opened by an
// earlier $Name on the same line. Measured against GitHub's renderer
// (plan-step4d-ergonomics.md, part 4): a $ closes a span only when the
// character before it is not a space and the one after it is not a letter,
// digit or _. A $Name's own $ is followed by a letter, so it never closes
// one; a backslash does not escape a $, and code, HTML and link destinations
// hold no math. The closing $ pairs with the nearest opener before it.
func (l *linter) mathHazard() []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, f := range l.files() {
		src := f.Source()
		x := f.Index()
		starts := map[int]bool{}
		for _, o := range f.Occurrences {
			starts[o.Range.Start] = true
		}
		opener, openerLine := -1, 0
		for i := 0; i < len(src); i++ {
			if src[i] != '$' || inOpaque(f, i) {
				continue
			}
			line := x.Line(i)
			if starts[i] {
				opener, openerLine = i, line
				continue
			}
			if opener < 0 || openerLine != line || !closesMath(src, i, x.LineStart(line)) {
				continue
			}
			o := pos.Range{Start: opener, End: opener + 1}
			name := occurrenceAt(f, opener)
			word := f.Text(StrayWord(f, i))
			out = append(out, diag.Diagnostic{
				Code: diag.CodeMathHazard, Severity: diag.Warning, File: f.Path, Range: pos.Range{Start: i, End: i + 1},
				Message: fmt.Sprintf("this $ closes math that %s opens earlier on the line: GitHub renders the text between them as math. Put `%s` in backticks or reword; a backslash does not escape a $", name, word),
				Related: []diag.Related{{File: f.Path, Range: o, Message: "opens the math"}},
			})
			opener = -1
		}
	}
	return out
}

// closesMath reports whether the $ at src[i] can close GitHub inline math:
// not at the line start (lineStart), not after a space, and not before a
// letter, digit or _.
func closesMath(src []byte, i, lineStart int) bool {
	if i == lineStart {
		return false
	}
	if prev, _ := utf8.DecodeLastRune(src[lineStart:i]); unicode.IsSpace(prev) {
		return false
	}
	if i+1 < len(src) {
		next, _ := utf8.DecodeRune(src[i+1:])
		if next == '_' || unicode.IsLetter(next) || unicode.IsDigit(next) {
			return false
		}
	}
	return true
}

// occurrenceAt returns the $Name token that starts at off, $ included.
func occurrenceAt(f *mdspec.File, off int) string {
	for _, o := range f.Occurrences {
		if o.Range.Start == off {
			return "$" + o.Name
		}
	}
	return "$"
}

// inOpaque reports whether off is inside an opaque region (code, HTML,
// link destination), where GitHub renders no math.
func inOpaque(f *mdspec.File, off int) bool {
	for _, op := range f.Opaque {
		if op.Range.Start > off {
			return false // sorted by start
		}
		if off < op.Range.End {
			return true
		}
	}
	return false
}

// StrayWord returns the range of the word the stray $ at off ends, the $
// included: the letters, digits, '_', '.' and backslashes before it, stopping at a
// $Name token or an opaque region. A math-hazard fix puts it in backticks
// ("5$" becomes "`5$`").
func StrayWord(f *mdspec.File, off int) pos.Range {
	src := f.Source()
	start := off
	for start > 0 {
		r, n := utf8.DecodeLastRune(src[:start])
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '\\') {
			break
		}
		if inOpaque(f, start-n) || inOccurrence(f, start-n) {
			break
		}
		start -= n
	}
	return pos.Range{Start: start, End: off + 1}
}

// inOccurrence reports whether off is inside a $Name token.
func inOccurrence(f *mdspec.File, off int) bool {
	for _, o := range f.Occurrences {
		if o.Range.Start <= off && off < o.Range.End {
			return true
		}
	}
	return false
}

// bodyLines returns the number of lines the body of a spans.
func bodyLines(f *mdspec.File, a *mdspec.Anchor) int {
	last := a.BodyRange.End - 1
	if last < a.BodyRange.Start {
		last = a.BodyRange.Start
	}
	return f.Index().Line(last) - f.Index().Line(a.BodyRange.Start) + 1
}

func (l *linter) anchorLong() []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, f := range l.files() {
		for _, a := range f.Anchors {
			if n := bodyLines(f, a); n > MaxBodyLines {
				out = append(out, diag.Diagnostic{
					Code: diag.CodeAnchorLong, Severity: diag.Warning, File: f.Path, Range: a.NameRange,
					Message: fmt.Sprintf("the body of $%s is %d lines (limit %d); split it into several anchors", a.Name, n, MaxBodyLines),
				})
			}
		}
	}
	return out
}

func (l *linter) checkedByMissing(covered func(string) bool) []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, f := range l.files() {
		for _, a := range f.Anchors {
			if a.Kind != mdspec.KindInvariant {
				continue
			}
			if strings.Contains(strings.ToLower(f.Text(a.BodyRange)), "checked by:") {
				continue
			}
			if covered != nil && covered(a.Name) {
				continue
			}
			out = append(out, diag.Diagnostic{
				Code: diag.CodeCheckedByMissing, Severity: diag.Warning, File: f.Path, Range: a.NameRange,
				Message: fmt.Sprintf("invariant $%s says nothing about what checks it; add a \"Checked by:\" line to its body, or cite it from a formal model with a \\* Spec: tag", a.Name),
			})
		}
	}
	return out
}

func (l *linter) sectionHashRef() []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, f := range l.files() {
		for _, h := range f.HashRefs {
			state, target, cands := l.spec.ResolveSection(f.Path, "", h.Number)
			var msg string
			switch state {
			case mdspec.Resolved:
				msg = fmt.Sprintf("#%s matches section %s; write §%s to reference it (this hint never rewrites, since #%s may mean an issue number)", h.Number, target.Qualified(), h.Number, h.Number)
			case mdspec.Ambiguous:
				var forms []string
				for _, c := range cands {
					forms = append(forms, c.Qualified())
				}
				msg = fmt.Sprintf("#%s matches several sections (%s); write the qualified form, e.g. %s, to reference one", h.Number, strings.Join(forms, ", "), forms[0])
			default:
				continue
			}
			out = append(out, diag.Diagnostic{
				Code: diag.CodeSectionHashRef, Severity: diag.Hint, File: f.Path, Range: h.Range, Message: msg,
			})
		}
	}
	return out
}

func (l *linter) sectionRefMalformed() []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, f := range l.files() {
		for _, r := range f.MalformedSectionRefs {
			out = append(out, diag.Diagnostic{
				Code: diag.CodeSectionRefMalformed, Severity: diag.Hint, File: f.Path, Range: r,
				Message: fmt.Sprintf("%q is not a section reference: a section number is digits and dots with at most one trailing lowercase letter, such as §5.2a", f.Text(r)),
			})
		}
	}
	return out
}

// Nearly is the defined name an invalid $ candidate most likely meant.
type Nearly struct {
	// Name is the defined name, without $.
	Name string
	// Prefix is false when the candidate differs from Name only in letter
	// case (replacing the candidate with Name fixes it), and true when Name
	// is only the longest defined prefix of the candidate.
	Prefix bool
}

// SuggestName returns the defined name that the invalid $ candidate text
// (without $) most likely meant: the first, in byte order, of the names equal
// to it ignoring case, or else the longest valid prefix of it that is
// defined. It reports false when there is none. This is the rule behind
// ref-nearly; the language server's quick fix uses it too.
func SuggestName(spec *mdspec.Spec, text string) (Nearly, bool) {
	var folded []string
	for name := range spec.ByName {
		if strings.EqualFold(name, text) {
			folded = append(folded, name)
		}
	}
	if len(folded) > 0 {
		sort.Strings(folded)
		return Nearly{Name: folded[0]}, true
	}
	for i := len(text) - 1; i >= 1; i-- {
		p := text[:i]
		if mdspec.ValidName(p) && len(spec.ByName[p]) > 0 {
			return Nearly{Name: p, Prefix: true}, true
		}
	}
	return Nearly{}, false
}

func (l *linter) refNearly() []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, f := range l.files() {
		for _, c := range f.Candidates {
			n, ok := SuggestName(l.spec, c.Text)
			if !ok {
				continue
			}
			msg := fmt.Sprintf("$%s is not a valid name, so it is plain text; did you mean `$%s`?", c.Text, n.Name)
			if n.Prefix {
				msg = fmt.Sprintf("$%s is not a valid name, so it is plain text; it starts with the defined name `$%s`. Names are dot-separated segments that start with a capital letter and contain only letters and digits", c.Text, n.Name)
			}
			out = append(out, diag.Diagnostic{
				Code: diag.CodeRefNearly, Severity: diag.Hint, File: f.Path, Range: c.Range, Message: msg,
			})
		}
	}
	return out
}

func (l *linter) refInTitle() []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, f := range l.files() {
		for _, o := range f.Occurrences {
			if o.Role != mdspec.RoleReference || !o.InTitle {
				continue
			}
			out = append(out, diag.Diagnostic{
				Code: diag.CodeRefInTitle, Severity: diag.Hint, File: f.Path, Range: o.Range,
				Message: fmt.Sprintf("$%s appears in a title; titles are not hashed, so a dependency stated here is invisible to change detection. State it in the body instead", o.Name),
			})
		}
	}
	return out
}

func (l *linter) anchorCodeAfter() []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, f := range l.files() {
		for _, a := range f.Anchors {
			if !a.CodeAfter {
				continue
			}
			out = append(out, diag.Diagnostic{
				Code: diag.CodeAnchorCodeAfter, Severity: diag.Hint, File: f.Path, Range: a.NameRange,
				Message: fmt.Sprintf("a fenced code block follows the body of $%s but is not part of it, so editing the code does not change the anchor's hash. Put the code inside a list item or quote, or accept that it is not hashed", a.Name),
			})
		}
	}
	return out
}
