// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// FixClass says how a lint finding is fixed.
type FixClass string

// Fix classes.
const (
	// FixModel asks the model, with the rule's instruction.
	FixModel FixClass = "model"
	// FixMechanical computes the edit in Go: no model, no network.
	FixMechanical FixClass = "mechanical"
	// FixManual cannot be fixed by an edit of a spec file; the rule's
	// manual text says what to do by hand.
	FixManual FixClass = "manual"
)

// fixRule is how one lint rule is fixed.
type fixRule struct {
	class FixClass
	// mechanical computes the fix in Go. It reports false when the finding
	// has no single obvious fix; the model is then asked when model is set.
	mechanical func(spec *mdspec.Spec, f *mdspec.File, d diag.Diagnostic) (mechFix, bool)
	// model is true when the model may be asked (always for FixModel).
	model bool
	// what describes the fix for the commit message, given the flagged
	// text ("reference $Time.Wall").
	what func(spec *mdspec.Spec, subject string, d diag.Diagnostic) string
	// manual says what to do by hand (FixManual).
	manual string
}

// mechFix is a computed fix: the range of the finding's file to replace,
// and with what.
type mechFix struct {
	r           pos.Range
	text        string
	explanation string
}

func prefixed(verb string) func(*mdspec.Spec, string, diag.Diagnostic) string {
	return func(_ *mdspec.Spec, s string, _ diag.Diagnostic) string { return verb + " " + s }
}

var fixRules = map[string]fixRule{
	diag.CodeDefUnreferenced:     {class: FixModel, model: true, what: prefixed("reference")},
	diag.CodeAnchorLong:          {class: FixModel, model: true, what: prefixed("split")},
	diag.CodeRefDangling:         {class: FixModel, model: true, mechanical: mechRefDangling, what: prefixed("define or correct")},
	diag.CodeSectionRefDangling:  {class: FixModel, model: true, what: prefixed("correct")},
	diag.CodeSectionRefAmbiguous: {class: FixModel, model: true, what: prefixed("qualify")},
	diag.CodeDefDuplicate:        {class: FixModel, model: true, what: prefixed("one definition of")},
	diag.CodeDefMalformed:        {class: FixModel, model: true, what: prefixed("repair the label")},
	diag.CodeDefNested: {class: FixModel, model: true, what: func(_ *mdspec.Spec, s string, _ diag.Diagnostic) string {
		return "move " + s + " out of its container"
	}},
	diag.CodeSectionDup: {class: FixModel, model: true, what: prefixed("renumber section")},
	diag.CodeRefInTitle: {class: FixModel, model: true, what: func(_ *mdspec.Spec, s string, _ diag.Diagnostic) string {
		return "move " + s + " out of the title"
	}},
	diag.CodeCheckedByMissing: {class: FixModel, model: true, what: prefixed("say what checks")},
	diag.CodeAnchorCodeAfter:  {class: FixModel, model: true, what: prefixed("the code block after")},

	diag.CodeMathHazard: {class: FixMechanical, mechanical: mechMathHazard, what: func(spec *mdspec.Spec, _ string, d diag.Diagnostic) string {
		if f := spec.File(d.File); f != nil && d.Range.Valid(len(f.Source())) {
			return "put " + f.Text(lint.StrayWord(f, d.Range.Start)) + " in backticks"
		}
		return "put the stray $ in backticks"
	}},
	diag.CodeFileBOM: {class: FixMechanical, mechanical: mechFileBOM, what: func(_ *mdspec.Spec, _ string, d diag.Diagnostic) string {
		return "remove the byte order mark from " + d.File
	}},
	diag.CodeSectionHashRef: {class: FixMechanical, model: true, mechanical: mechSectionHashRef, what: func(_ *mdspec.Spec, s string, _ diag.Diagnostic) string {
		return "write §" + strings.TrimPrefix(s, "#") + " for " + s
	}},
	diag.CodeRefNearly:           {class: FixMechanical, model: true, mechanical: mechRefNearly, what: prefixed("correct")},
	diag.CodeSectionRefMalformed: {class: FixMechanical, model: true, mechanical: mechSectionRefMalformed, what: prefixed("correct")},

	diag.CodeProjectNoFiles: {class: FixManual,
		manual: "spec.toml selects no spec file: fix its files patterns (`spec explain config`) or add a *.spec.md file"},
	diag.CodeProjectFileMissing: {class: FixManual,
		manual: "a files entry in spec.toml matches nothing: correct the entry or remove it"},
	diag.CodeFileNotUTF8: {class: FixManual,
		manual: "save the file as UTF-8 (for example `iconv -f LATIN1 -t UTF-8`); spec leaves it out until then"},
	diag.CodeFileSymlink: {class: FixManual,
		manual: "replace the symbolic link with the file itself, or list the real path in spec.toml"},
	diag.CodeConfigInvalid: {class: FixManual,
		manual: "edit the file the message names (`spec explain config` shows the format; `spec doctor` checks the user config)"},
	diag.CodeFileStemDuplicate: {class: FixManual,
		manual: "rename one of the files so each stem is unique, then update the §stem:N references to it"},
	diag.CodeFormalSyntax: {class: FixManual,
		manual: "correct the TLA+ at the place the message names; `spec explain citations` says how models are read"},
	diag.CodeFormalSemantic: {class: FixManual,
		manual: "correct the TLA+ at the place the message names, or the module it extends; `spec explain citations` says how models are checked"},
	diag.CodeFormalRefDangling: {class: FixManual,
		manual: "correct the tag in the model, or define what it cites (`spec explain citations`)"},
	diag.CodeFormalOpUncited: {class: FixManual,
		manual: "add a \\* Spec: tag above the operator, naming the statement it checks (`spec explain citations`)"},
	diag.CodeFormalTagMalformed: {class: FixManual,
		manual: "correct what the message names; a tag reads \\* Spec: $Name \"quote\", \\* Spec: §N \"quote\", \\* Derived-from: $Name \"quote\" or \\* Spec-file: stem (`spec explain citations`)"},
	diag.CodeFormalTagBlock: {class: FixManual,
		manual: "move the tag out of the (* *) comment onto its own \\* line"},
	diag.CodeFormalQuoteStale: {class: FixManual,
		manual: "update the quote from the cited text, or point the tag at the text that says it"},
	diag.CodeFormalCheckLimit: {class: FixManual,
		manual: "raise the limit with --max-acquisition-visits N, or max_acquisition_visits = N under [tla] in spec.toml (`spec explain citations`)"},
	diag.CodeSpell: {class: FixManual,
		manual: "replace the word with the fix the message names (^P in spec ui and the language server's quick fix put it in), or add the word to spec.words"},
}

// FixClassOf returns how lint rule code is fixed, and false for an unknown
// code.
func FixClassOf(code string) (FixClass, bool) {
	r, ok := fixRules[code]
	return r.class, ok
}

// FixUsesModel reports whether a fix of code may ask the model: always for
// a model rule, and for a mechanical one when the finding has no single
// obvious fix.
func FixUsesModel(code string) bool {
	return fixRules[code].model
}

// FixManualText says what to do by hand about a finding of code that
// cannot be fixed here ("" for the others).
func FixManualText(code string) string {
	return fixRules[code].manual
}

// DefaultWhy is the commit message a fix of d starts with ("Fix
// def-unreferenced: reference $Time.Wall"); the author may edit it.
func DefaultWhy(spec *mdspec.Spec, d diag.Diagnostic) string {
	r, ok := fixRules[d.Code]
	if !ok || r.what == nil {
		return "Fix " + d.Code
	}
	return "Fix " + d.Code + ": " + r.what(spec, subjectOf(spec, d), d)
}

// subjectOf is the flagged text, shortened to one line for a message.
func subjectOf(spec *mdspec.Spec, d diag.Diagnostic) string {
	f := spec.File(d.File)
	if f == nil || !d.Range.Valid(len(f.Source())) {
		return d.File
	}
	s := strings.Join(strings.Fields(f.Text(d.Range)), " ")
	if utf8.RuneCountInString(s) > 48 {
		s = string([]rune(s)[:47]) + "…"
	}
	if s == "" {
		return d.File
	}
	return s
}

// severityRank orders severities, the most severe first.
func severityRank(s diag.Severity) int {
	switch s {
	case diag.Error:
		return 0
	case diag.Warning:
		return 1
	case diag.Hint:
		return 2
	}
	return 3
}

// MostSevere returns the most severe of ds (the first of the most severe
// ones), and false when ds is empty.
func MostSevere(ds []diag.Diagnostic) (diag.Diagnostic, bool) {
	if len(ds) == 0 {
		return diag.Diagnostic{}, false
	}
	best := ds[0]
	for _, d := range ds[1:] {
		if severityRank(d.Severity) < severityRank(best.Severity) {
			best = d
		}
	}
	return best, true
}

// FixRequest says which lint finding `spec fix` (or ^P in `spec ui`) fixes.
type FixRequest struct {
	// File is project-relative, with slashes.
	File string
	// Line is 1-based. Col is the 1-based byte column `spec lint` prints;
	// 0 means any finding on the line.
	Line, Col int
	// Code picks the finding by rule when several are there; "" takes the
	// most severe.
	Code string
	// Why is the commit message; "" uses DefaultWhy.
	Why    string
	Author string
	Full   bool
	// Overlay replaces files' contents (the editor's unsaved buffers); nil
	// reads the files on disk.
	Overlay map[string][]byte
	// NoModel makes a fix that needs the model fail with exit 1
	// `needs_model` instead of asking it (the UI applies a mechanical fix
	// at once and runs a model fix in the background).
	NoModel bool
}

// FixResult is a fix proposal, not filed yet.
type FixResult struct {
	Diagnostic diag.Diagnostic
	Class      FixClass
	Proposal   *proposal.Proposal
}

// Fix makes the proposal that fixes the lint finding req names, from the
// files on disk. A mechanical rule's edit is computed without a model; a
// model rule asks the model with the rule's instruction. A finding that
// cannot be fixed by editing a spec file is exit 1 `not_fixable`, with
// what to do by hand; a model reply without an edit is exit 1 `no_fix`,
// with the reply as an unfiled proposal. The caller files the proposal.
func (w *Workflow) Fix(ctx context.Context, proj *project.Project, req FixRequest) (*FixResult, *Error) {
	loaded, err := proj.Load(req.Overlay)
	if err != nil {
		return nil, internalErr(err)
	}
	spec := loaded.Spec
	diags := lint.Run(spec, loaded.LintOptions())
	req.Code = lint.Canonical(req.Code)
	d, e := findDiagnostic(placesOf(loaded, req.File), diags, req)
	if e != nil {
		return nil, e
	}
	rule, known := fixRules[d.Code]
	if !known {
		return nil, fail(ExitFail, "not_fixable", "%s has no fix", d.Code)
	}
	if rule.class == FixManual {
		return nil, fail(ExitFail, "not_fixable", "%s cannot be fixed by editing a spec file: %s", d.Code, rule.manual)
	}
	why := strings.TrimSpace(req.Why)
	if why == "" {
		why = DefaultWhy(spec, d)
	}
	f := spec.File(d.File)
	if f == nil {
		return nil, fail(ExitFail, "not_fixable", "%s is not about a spec file, so it cannot be fixed by editing one", d.Code)
	}
	at := f.Index().Position(d.Range.Start)
	p := &proposal.Proposal{
		Created: w.stamp(), Author: ByOr(req.Author), Kind: proposal.KindFix, Why: why,
		Fix:    &proposal.Fix{Code: d.Code, File: d.File, Line: at.Line, Col: at.Col, Message: d.Message},
		Status: proposal.StatusPending,
	}
	res := &FixResult{Diagnostic: d, Proposal: p}

	if rule.mechanical != nil {
		if mf, ok := rule.mechanical(spec, f, d); ok {
			h, ok := lineHunk(f, mf.r, mf.text)
			if !ok {
				return nil, internalErr(fmt.Errorf("the %s fix of %s:%d does not change the file", d.Code, d.File, at.Line))
			}
			h.Issue, h.Severity, h.Explanation = d.Code, fixSeverity(d), mf.explanation
			p.Hunks = []finding.Hunk{h}
			p.Summary = mf.explanation
			res.Class = FixMechanical
			p.Fix.Class = string(FixMechanical)
			FillBase(p, spec)
			p.Normalize()
			return res, nil
		}
	}
	if !rule.model {
		return nil, internalErr(fmt.Errorf("no fix for %s at %s:%d", d.Code, d.File, at.Line))
	}
	if req.NoModel {
		return nil, fail(ExitFail, "needs_model", "the %s fix at %s:%d needs the model", d.Code, d.File, at.Line)
	}

	ck, e := w.OpenChecker(ctx, req.Full)
	if e != nil {
		return nil, e
	}
	result, e := ck.Fix(fixInput(f, d, at), fixContext(spec, d.File, ck.options()))
	if e != nil {
		return nil, e
	}
	hunks, notes := finding.Verify(result, SourcesOf(spec), finding.Options{FirstN: 1})
	for i := range hunks {
		hunks[i].Issue, hunks[i].Severity = d.Code, fixSeverity(d)
	}
	for i := range notes {
		notes[i].Issue, notes[i].Severity = d.Code, fixSeverity(d)
	}
	rec := ck.Record
	p.Summary, p.Hunks, p.Notes, p.Questions, p.Inference = strings.TrimSpace(result.Summary), hunks, notes, result.Questions, &rec
	res.Class = FixModel
	p.Fix.Class = string(FixModel)
	FillBase(p, spec)
	p.Normalize()
	if len(hunks) == 0 {
		msg := fmt.Sprintf("the model proposed no edit for %s at %s:%d", d.Code, d.File, at.Line)
		if p.Summary != "" {
			msg += ": " + p.Summary
		}
		for _, n := range notes {
			if n.Explanation != "" {
				msg += "; note: " + n.Explanation
			}
		}
		for _, q := range p.Questions {
			msg += "; question: " + q
		}
		e := fail(ExitFail, "no_fix", "%s; nothing was filed", msg)
		e.Proposal = p
		return nil, e
	}
	return res, nil
}

// fixSeverity is the severity of a fix hunk: cosmetic for a hint.
func fixSeverity(d diag.Diagnostic) string {
	if d.Severity == diag.Hint {
		return finding.SeverityCosmetic
	}
	return finding.SeverityInconsistent
}

// placesOf returns the position index of a spec file or a model, and nil for
// any other file a finding can name (spec.toml, a symbolic link).
func placesOf(loaded *project.Loaded, file string) *pos.Index {
	if f := loaded.Spec.File(file); f != nil {
		return f.Index()
	}
	if m := loaded.Formal.File(file); m != nil {
		return m.Index()
	}
	return nil
}

// findDiagnostic returns the finding req names: in req.File at req.Line
// (and req.Col), of req.Code when given, else the most severe there. x is
// req.File's position index; nil means its findings have no place to match.
func findDiagnostic(x *pos.Index, diags []diag.Diagnostic, req FixRequest) (diag.Diagnostic, *Error) {
	where := fmt.Sprintf("%s:%d", req.File, req.Line)
	if req.Col > 0 {
		where += fmt.Sprintf(":%d", req.Col)
	}
	var at []diag.Diagnostic
	for _, d := range diags {
		if d.File != req.File || req.Code != "" && d.Code != req.Code {
			continue
		}
		if x == nil || covers(x, d.Range, req.Line, req.Col) {
			at = append(at, d)
		}
	}
	if x == nil && len(at) == 0 {
		return diag.Diagnostic{}, fail(ExitFail, "not_found", "%s is not a spec file of this project with a lint finding (spec lint lists them)", req.File)
	}
	d, ok := MostSevere(at)
	if !ok {
		what := "lint finding"
		if req.Code != "" {
			what = req.Code + " finding"
		}
		return diag.Diagnostic{}, fail(ExitFail, "not_found", "no %s at %s (spec lint lists them, with their FILE:LINE:COL)", what, where)
	}
	return d, nil
}

// covers reports whether range r of the file indexed by x touches line (and
// the byte column col, when not 0), the end included as at a cursor.
func covers(x *pos.Index, r pos.Range, line, col int) bool {
	if line < 1 || line > x.LineCount() {
		return false
	}
	if col > 0 {
		off, ok := x.Offset(pos.Position{Line: line, Col: col})
		return ok && r.Start <= off && off <= r.End
	}
	lr := x.LineRange(line)
	return r.Start <= lr.End && (r.End > lr.Start || r.Start == lr.Start)
}

// fixInput is the finding as the model is told it.
func fixInput(f *mdspec.File, d diag.Diagnostic, at pos.Position) prompt.FixInput {
	src := f.Source()
	lo := lineStart(src, d.Range.Start)
	hi := lineEnd(src, d.Range.End)
	return prompt.FixInput{
		Code: d.Code, Severity: string(d.Severity), File: d.File, Line: at.Line, Col: at.Col, Message: d.Message,
		Text: f.Text(d.Range), Lines: string(src[lo:hi]),
	}
}

// fixContext is the spec text sent with a fix: every file when they fit the
// budget, else the finding's file and its neighborhood.
func fixContext(spec *mdspec.Spec, file string, o specctx.Options) *specctx.Context {
	var all []string
	for _, f := range spec.Files {
		if f.ValidUTF8 {
			all = append(all, f.Path)
		}
	}
	c := specctx.Build(spec, specctx.Target{Files: all}, o)
	for _, ft := range c.Files {
		if ft.Path == file && ft.Full {
			return c
		}
	}
	return specctx.Build(spec, specctx.Target{Files: []string{file}}, o)
}

// lineHunk turns the replacement of r in f by text into a hunk whose find
// is the whole lines around r, widened line by line until it occurs once
// in the file.
func lineHunk(f *mdspec.File, r pos.Range, text string) (finding.Hunk, bool) {
	src := f.Source()
	if !r.Valid(len(src)) {
		return finding.Hunk{}, false
	}
	lo, hi := lineStart(src, r.Start), lineEnd(src, r.End)
	s := string(src)
	for lo > 0 || hi < len(src) {
		if hi > lo && strings.Count(s, s[lo:hi]) == 1 {
			break
		}
		if hi < len(src) {
			hi = lineEnd(src, hi+1)
		} else {
			lo = lineStart(src, lo-1)
		}
	}
	find := s[lo:hi]
	replace := s[lo:r.Start] + text + s[r.End:hi]
	if find == replace {
		return finding.Hunk{}, false
	}
	h := finding.Hunk{N: 1, File: f.Path, Find: find, Replace: replace, Range: pos.Range{Start: lo, End: hi}}
	if a := f.AnchorAt(r.Start); a != nil {
		h.Anchor = "$" + a.Name
	}
	return h, true
}

// lineStart returns the start of the line holding off.
func lineStart(src []byte, off int) int {
	off = min(max(off, 0), len(src))
	for off > 0 && src[off-1] != '\n' {
		off--
	}
	return off
}

// lineEnd returns the end of the line holding off, before its newline.
func lineEnd(src []byte, off int) int {
	off = min(max(off, 0), len(src))
	for off < len(src) && src[off] != '\n' {
		off++
	}
	return off
}

// ---- mechanical fixes ----

// mechMathHazard puts the stray $ and the word it ends in backticks
// ("5$" becomes "`5$`"), dropping a backslash right before the $: it does
// not escape a $ for GitHub, and in code it would show.
func mechMathHazard(_ *mdspec.Spec, f *mdspec.File, d diag.Diagnostic) (mechFix, bool) {
	r := lint.StrayWord(f, d.Range.Start)
	word := f.Text(r)
	code := strings.TrimSuffix(strings.TrimSuffix(word, "$"), "\\") + "$"
	return mechFix{r: r, text: "`" + code + "`",
		explanation: fmt.Sprintf("Put %s in backticks, so GitHub does not render the text between it and the earlier $Name as math.", word)}, true
}

func mechFileBOM(_ *mdspec.Spec, f *mdspec.File, d diag.Diagnostic) (mechFix, bool) {
	if !f.HasBOM {
		return mechFix{}, false
	}
	return mechFix{r: pos.Range{Start: 0, End: len(byteOrderMark)}, explanation: "Remove the byte order mark at the start of " + f.Path + "."}, true
}

// byteOrderMark is UTF-8's byte order mark.
const byteOrderMark = "\xef\xbb\xbf"

func mechSectionHashRef(spec *mdspec.Spec, f *mdspec.File, d diag.Diagnostic) (mechFix, bool) {
	for _, h := range f.HashRefs {
		if h.Range != d.Range {
			continue
		}
		state, target, _ := spec.ResolveSection(f.Path, "", h.Number)
		if state != mdspec.Resolved {
			return mechFix{}, false
		}
		return mechFix{
			r: pos.Range{Start: h.Range.Start, End: h.Range.Start + 1}, text: "§",
			explanation: fmt.Sprintf("Write §%s, a reference to section %s (%s), instead of #%s.", h.Number, target.Qualified(), target.Title, h.Number),
		}, true
	}
	return mechFix{}, false
}

func mechRefNearly(spec *mdspec.Spec, f *mdspec.File, d diag.Diagnostic) (mechFix, bool) {
	for _, c := range f.Candidates {
		if c.Range != d.Range {
			continue
		}
		n, ok := lint.SuggestName(spec, c.Text)
		if !ok || n.Prefix {
			// A defined prefix ("$Frame.Timing_v2") has no safe rewrite:
			// the rest may be prose.
			return mechFix{}, false
		}
		return mechFix{r: c.Range, text: "$" + n.Name,
			explanation: fmt.Sprintf("$%s is not a valid name; write the defined name $%s.", c.Text, n.Name)}, true
	}
	return mechFix{}, false
}

func mechRefDangling(spec *mdspec.Spec, f *mdspec.File, d diag.Diagnostic) (mechFix, bool) {
	name := strings.TrimPrefix(f.Text(d.Range), "$")
	near := NearNames(spec, name)
	if len(near) != 1 {
		return mechFix{}, false
	}
	return mechFix{r: d.Range, text: "$" + near[0],
		explanation: fmt.Sprintf("$%s is not defined; the only defined name close to it is $%s.", name, near[0])}, true
}

// NearNames returns the defined names close to name, sorted: equal to it
// ignoring case, or within an edit distance of 2 that is at most a quarter
// of its length.
func NearNames(spec *mdspec.Spec, name string) []string {
	low := strings.ToLower(name)
	var out []string
	for n := range spec.ByName {
		if n == name || len(spec.ByName[n]) == 0 {
			continue
		}
		dist := editDistance(low, strings.ToLower(n))
		if dist == 0 || dist <= 2 && dist*4 <= len(name) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// editDistance is the Levenshtein distance between a and b, in bytes.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func mechSectionRefMalformed(spec *mdspec.Spec, f *mdspec.File, d diag.Diagnostic) (mechFix, bool) {
	text := f.Text(d.Range)
	rest, ok := strings.CutPrefix(text, "§")
	if !ok {
		return mechFix{}, false
	}
	base := d.Range.Start + len("§")
	stem := ""
	if s, num, qualified := strings.Cut(rest, ":"); qualified {
		stem, rest, base = s, num, base+len(s)+1
	}
	// The readings: each prefix that is a section number of an existing
	// section. Exactly one is an obvious fix: a space after it.
	var cut []int
	for k := len(rest) - 1; k >= 1; k-- {
		p := rest[:k]
		if !mdspec.ValidSectionNumber(p) {
			continue
		}
		if state, _, _ := spec.ResolveSection(f.Path, stem, p); state == mdspec.Resolved {
			cut = append(cut, k)
		}
	}
	if len(cut) != 1 {
		return mechFix{}, false
	}
	at := base + cut[0]
	return mechFix{r: pos.Range{Start: at, End: at}, text: " ",
		explanation: fmt.Sprintf("%q is not a section reference; a space after %s makes it one.", text, f.Text(pos.Range{Start: d.Range.Start, End: at}))}, true
}
