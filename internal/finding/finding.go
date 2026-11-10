// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package finding is the model's answer to a check and its verification.
//
// A model replies with a Result: a summary, findings and questions. Every
// finding that carries a find/replace pair is verified against the spec
// files: find must occur exactly once in the named file, verbatim or up to
// how its lines are wrapped (and outside any
// protected range, such as the anchor being changed). A verified pair
// becomes a Hunk that can be applied; anything else becomes a Note that is
// shown but never applied.
//
// A finding may list, by their indices in the reply, the other findings
// whose find/replace pairs resolve it (fixed_by): one fix can resolve several
// findings, and one finding can need several fixes. A link survives
// verification only when the finding it names became a hunk; hunks and notes
// carry the linked hunks' numbers.
package finding

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/tenta-browser/polychromatic"

	"github.com/jwriteclub/spec/internal/pos"
)

var lg *logrus.Entry = polychromatic.GetLogger("finding")

// Issue kinds.
const (
	IssueContradiction    = "contradiction"
	IssueNumericMismatch  = "numeric_mismatch"
	IssueStaleReference   = "stale_reference"
	IssueTerminologyDrift = "terminology_drift"
	IssueAmbiguity        = "ambiguity"
	IssueMissingUpdate    = "missing_update"
	// IssueSpelling is a spelling flag on proposed text (spec ui adds it as
	// a note; models never report it).
	IssueSpelling          = "spelling"
	SeverityBreaking       = "breaking"
	SeverityInconsistent   = "inconsistent"
	SeverityCosmetic       = "cosmetic"
	defaultIssue           = IssueAmbiguity
	defaultSeverity        = SeverityInconsistent
	maxFindings            = 100
	maxQuestions           = 50
	maxFieldBytes          = 16 << 10
	noteNoFind             = "no edit proposed"
	notePhantom            = "model quoted text that does not exist"
	noteRepeated           = "the quoted text occurs %d times in the file, so the edit is ambiguous"
	noteNoFile             = "the finding names no spec file the check was given"
	noteProtected          = "the edit overlaps the change under review"
	noteOverlap            = "the edit overlaps hunk %d"
	noteNoop               = "find and replace are identical"
	noteMissingReplacement = "the finding has find but no replace"
)

// Issues and Severities list the valid values.
var (
	Issues     = []string{IssueContradiction, IssueNumericMismatch, IssueStaleReference, IssueTerminologyDrift, IssueAmbiguity, IssueMissingUpdate}
	Severities = []string{SeverityBreaking, SeverityInconsistent, SeverityCosmetic}
)

// Finding is one item of a model's reply.
type Finding struct {
	File        string  `json:"file"`
	Anchor      string  `json:"anchor,omitempty"`
	Issue       string  `json:"issue"`
	Severity    string  `json:"severity"`
	Explanation string  `json:"explanation"`
	Find        *string `json:"find,omitempty"`
	Replace     *string `json:"replace,omitempty"`
	// FixedBy are the 0-based indices, in the same reply's findings, of the
	// findings whose find/replace pairs resolve this one (empty: standalone).
	FixedBy []int `json:"fixed_by,omitempty"`
}

// Result is a model's whole reply.
type Result struct {
	Summary   string    `json:"summary"`
	Findings  []Finding `json:"findings"`
	Questions []string  `json:"questions"`
	// Primary is a rethink's change to the author's own anchor (nil when
	// none, and always nil from a check: only the rethink prompt asks for it).
	Primary *PrimaryEdit `json:"primary,omitempty"`
}

// PrimaryEdit is a find/replace inside the anchor under review: what a
// rethink proposes for the author's own edit when the notes change it.
type PrimaryEdit struct {
	Find        string `json:"find"`
	Replace     string `json:"replace"`
	Explanation string `json:"explanation"`
}

// UnmarshalJSON accepts questions as strings or as objects with a
// "question" field, and tolerates null lists.
func (r *Result) UnmarshalJSON(b []byte) error {
	var raw struct {
		Summary   json.RawMessage   `json:"summary"`
		Findings  []json.RawMessage `json:"findings"`
		Questions []json.RawMessage `json:"questions"`
		Primary   json.RawMessage   `json:"primary"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw.Findings == nil && raw.Summary == nil && raw.Questions == nil {
		return fmt.Errorf("the object has none of summary, findings, questions")
	}
	r.Summary = loose(raw.Summary)
	r.Primary = primaryEdit(raw.Primary)
	r.Findings = nil
	// kept maps an index in the reply to its index in r.Findings, so
	// fixed_by still names the right findings after junk entries are dropped.
	kept := map[int]int{}
	for i, f := range raw.Findings {
		var fi struct {
			File        json.RawMessage `json:"file"`
			Anchor      json.RawMessage `json:"anchor"`
			Issue       json.RawMessage `json:"issue"`
			Severity    json.RawMessage `json:"severity"`
			Explanation json.RawMessage `json:"explanation"`
			Find        json.RawMessage `json:"find"`
			Replace     json.RawMessage `json:"replace"`
			FixedBy     json.RawMessage `json:"fixed_by"`
		}
		if err := json.Unmarshal(f, &fi); err != nil {
			continue // a finding that is not an object is dropped
		}
		out := Finding{
			File: loose(fi.File), Anchor: loose(fi.Anchor), Issue: loose(fi.Issue),
			Severity: loose(fi.Severity), Explanation: loose(fi.Explanation),
		}
		out.Find = optional(fi.Find)
		out.Replace = optional(fi.Replace)
		out.FixedBy = indices(fi.FixedBy)
		kept[i] = len(r.Findings)
		r.Findings = append(r.Findings, out)
	}
	for i := range r.Findings {
		var fb []int
		for _, x := range r.Findings[i].FixedBy {
			if k, ok := kept[x]; ok {
				fb = append(fb, k)
			} else {
				lg.WithField("finding", i).WithField("fixed_by", x).Debug("dropping a fixed_by index that names no finding")
			}
		}
		r.Findings[i].FixedBy = fb
	}
	r.Questions = nil
	for _, q := range raw.Questions {
		if s := loose(q); s != "" {
			r.Questions = append(r.Questions, s)
			continue
		}
		var obj struct {
			Question string `json:"question"`
		}
		if json.Unmarshal(q, &obj) == nil && obj.Question != "" {
			r.Questions = append(r.Questions, obj.Question)
		}
	}
	return nil
}

// primaryEdit decodes a rethink's primary block: nil when it is absent,
// null, not an object, or has no find.
func primaryEdit(raw json.RawMessage) *PrimaryEdit {
	var pe struct {
		Find        json.RawMessage `json:"find"`
		Replace     json.RawMessage `json:"replace"`
		Explanation json.RawMessage `json:"explanation"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &pe) != nil {
		return nil
	}
	out := &PrimaryEdit{Find: loose(pe.Find), Replace: loose(pe.Replace), Explanation: loose(pe.Explanation)}
	if out.Find == "" {
		return nil
	}
	return out
}

// loose decodes a JSON string; null, absent and non-strings give "".
func loose(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// optional decodes a JSON string that may be absent or null.
func optional(raw json.RawMessage) *string {
	var s *string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return s
}

// indices decodes fixed_by: a list of non-negative integer indices (a bare
// number or numeric string is a list of one). Entries that are not such an
// index are dropped.
func indices(raw json.RawMessage) []int {
	if len(raw) == 0 {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		items = []json.RawMessage{raw}
	}
	var out []int
	for _, it := range items {
		if n, ok := index(it); ok {
			out = append(out, n)
		}
	}
	return out
}

// index decodes one non-negative integer, given as a number or a numeric
// string.
func index(raw json.RawMessage) (int, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return 0, false // Unmarshal leaves f at 0 for null
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return 0, false
		}
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &f); err != nil {
			return 0, false
		}
	}
	if f < 0 || f > maxFindings || f != float64(int(f)) {
		return 0, false
	}
	return int(f), true
}

// Hunk is a verified edit: Find occurs exactly once in File at Range.
type Hunk struct {
	// N numbers the hunk within its proposal (1-based).
	N           int       `json:"n"`
	File        string    `json:"file"`
	Anchor      string    `json:"anchor"`
	Issue       string    `json:"issue"`
	Severity    string    `json:"severity"`
	Explanation string    `json:"explanation"`
	Find        string    `json:"find"`
	Replace     string    `json:"replace"`
	Range       pos.Range `json:"range"`
	// FixedBy are the numbers of the other hunks in the same proposal that
	// this finding also needs (empty: this hunk alone resolves it).
	FixedBy []int `json:"fixed_by,omitempty"`
}

// Note is a finding that is shown but not applied.
type Note struct {
	File        string `json:"file"`
	Anchor      string `json:"anchor"`
	Issue       string `json:"issue"`
	Severity    string `json:"severity"`
	Explanation string `json:"explanation"`
	Find        string `json:"find"`
	Replace     string `json:"replace"`
	// Reason says why it is a note ("model quoted text that does not
	// exist").
	Reason string `json:"reason"`
	// FixedBy are the numbers (Hunk.N) of the hunks in the same proposal
	// that resolve this note (empty: no hunk does).
	FixedBy []int `json:"fixed_by,omitempty"`
}

// Options control Verify.
type Options struct {
	// Protected are ranges no hunk may touch, by file (the anchor under
	// review).
	Protected map[string][]pos.Range
	// FirstN is the number given to the first hunk.
	FirstN int
}

// Verify checks every finding against sources (project-relative path to
// file bytes, the text the hunks will be applied to). A finding's file is
// matched exactly, else by a unique base name. It returns the hunks, the
// notes, and the questions, all cleaned.
func Verify(r *Result, sources map[string][]byte, o Options) ([]Hunk, []Note) {
	hunks := []Hunk{}
	notes := []Note{}
	n := o.FirstN
	if n <= 0 {
		n = 1
	}
	fs := r.Findings
	if len(fs) > maxFindings {
		fs = fs[:maxFindings]
	}
	// hunkOf maps a finding's index to the number of the hunk it became;
	// hunkSrc and noteSrc hold, per hunk and per note, its finding's index.
	hunkOf := map[int]int{}
	hunkSrc := []int{}
	noteSrc := []int{}
	for i, f := range fs {
		f = clean(f)
		// The file is resolved first, so a note names it as a hunk would
		// (the UI jumps to a note's file).
		file, fileOK := resolveFile(f.File, sources)
		if fileOK {
			f.File = file
		}
		note := func(reason string) {
			noteSrc = append(noteSrc, i)
			notes = append(notes, Note{
				File: f.File, Anchor: f.Anchor, Issue: f.Issue, Severity: f.Severity,
				Explanation: f.Explanation, Find: deref(f.Find), Replace: deref(f.Replace), Reason: reason,
			})
		}
		if f.Find == nil || *f.Find == "" {
			note(noteNoFind)
			continue
		}
		if f.Replace == nil {
			note(noteMissingReplacement)
			continue
		}
		if *f.Find == *f.Replace {
			note(noteNoop)
			continue
		}
		if !fileOK {
			note(noteNoFile)
			continue
		}
		src := string(sources[file])
		find, replace := *f.Find, *f.Replace
		var r pos.Range
		switch c := strings.Count(src, find); {
		case c > 1:
			note(fmt.Sprintf(noteRepeated, c))
			continue
		case c == 1:
			start := strings.Index(src, find)
			r = pos.Range{Start: start, End: start + len(find)}
		default:
			// Not verbatim: try again with whitespace runs matching loosely
			// (a wrapped line quoted with a space).
			trimmed := strings.Trim(find, " \t\r\n")
			m := looseMatch(src, trimmed)
			if len(m) > 1 {
				note(fmt.Sprintf(noteRepeated, len(m)))
				continue
			}
			if len(m) == 0 {
				note(notePhantom)
				continue
			}
			actual := src[m[0][0]:m[0][1]]
			rewritten := reflow(trimmed, strings.Trim(replace, " \t\r\n"), actual)
			if rewritten == "" || rewritten == actual {
				note(notePhantom)
				continue
			}
			find, replace = actual, rewritten
			r = pos.Range{Start: m[0][0], End: m[0][1]}
		}
		if overlapsAny(r, o.Protected[file]) {
			note(noteProtected)
			continue
		}
		if prev := overlapping(hunks, file, r); prev > 0 {
			note(fmt.Sprintf(noteOverlap, prev))
			continue
		}
		hunks = append(hunks, Hunk{
			N: n, File: file, Anchor: f.Anchor, Issue: f.Issue, Severity: f.Severity,
			Explanation: f.Explanation, Find: find, Replace: replace, Range: r,
		})
		hunkOf[i] = n
		hunkSrc = append(hunkSrc, i)
		n++
	}
	for k, i := range hunkSrc {
		hunks[k].FixedBy = linkedHunks(i, fs[i].FixedBy, hunkOf)
	}
	for k, i := range noteSrc {
		notes[k].FixedBy = linkedHunks(i, fs[i].FixedBy, hunkOf)
	}
	return hunks, notes
}

// linkedHunks turns finding self's fixed_by indices into hunk numbers,
// dropping (with a debug log) an index that is self or names a finding that
// did not become a hunk, and repeats.
func linkedHunks(self int, fixedBy []int, hunkOf map[int]int) []int {
	var out []int
	seen := map[int]bool{}
	for _, x := range fixedBy {
		hn, ok := hunkOf[x]
		if x == self || !ok {
			lg.WithField("finding", self).WithField("fixed_by", x).Debug("dropping a fixed_by index: it is the finding itself or names no finding that became a hunk")
			continue
		}
		if !seen[hn] {
			seen[hn] = true
			out = append(out, hn)
		}
	}
	return out
}

// CleanQuestions trims, deduplicates and bounds questions.
func CleanQuestions(qs []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, q := range qs {
		q = strings.TrimSpace(clip(q))
		if q == "" || seen[q] {
			continue
		}
		seen[q] = true
		out = append(out, q)
		if len(out) == maxQuestions {
			break
		}
	}
	return out
}

// clean normalizes a finding's fields: known issue and severity values
// (lowercased; unknown ones become ambiguity and inconsistent), an anchor
// with its $, and bounded lengths.
func clean(f Finding) Finding {
	f.File = strings.TrimSpace(f.File)
	f.Anchor = strings.TrimSpace(f.Anchor)
	if f.Anchor != "" && !strings.HasPrefix(f.Anchor, "$") {
		f.Anchor = "$" + f.Anchor
	}
	f.Issue = pick(f.Issue, Issues, defaultIssue)
	if alias, ok := severityAlias[strings.ToLower(strings.TrimSpace(f.Severity))]; ok {
		f.Severity = alias
	}
	f.Severity = pick(f.Severity, Severities, defaultSeverity)
	f.Explanation = strings.TrimSpace(clip(f.Explanation))
	return f
}

// severityAlias maps common severity words to ours.
var severityAlias = map[string]string{
	"critical": SeverityBreaking, "high": SeverityBreaking, "major": SeverityBreaking, "error": SeverityBreaking,
	"medium": SeverityInconsistent, "moderate": SeverityInconsistent, "warning": SeverityInconsistent,
	"low": SeverityCosmetic, "minor": SeverityCosmetic, "info": SeverityCosmetic, "style": SeverityCosmetic,
}

func pick(v string, valid []string, def string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, " ", "_")
	for _, x := range valid {
		if v == x {
			return x
		}
	}
	return def
}

func clip(s string) string {
	if len(s) <= maxFieldBytes {
		return s
	}
	cut := maxFieldBytes
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// resolveFile matches a model-given file name against the sources: exactly,
// then without a leading "./", then by a unique base name.
func resolveFile(name string, sources map[string][]byte) (string, bool) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "./")
	if _, ok := sources[name]; ok {
		return name, true
	}
	if name == "" {
		return "", false
	}
	var hits []string
	for p := range sources {
		if path.Base(p) == path.Base(name) {
			hits = append(hits, p)
		}
	}
	sort.Strings(hits)
	if len(hits) == 1 {
		return hits[0], true
	}
	return "", false
}

func overlapsAny(r pos.Range, rs []pos.Range) bool {
	for _, p := range rs {
		if r.Start < p.End && p.Start < r.End {
			return true
		}
	}
	return false
}

// overlapping returns the number of an earlier hunk in file that overlaps
// r, or 0.
func overlapping(hs []Hunk, file string, r pos.Range) int {
	for _, h := range hs {
		if h.File == file && r.Start < h.Range.End && h.Range.Start < r.End {
			return h.N
		}
	}
	return 0
}

// Breaking reports whether any hunk or note is breaking.
func Breaking(hs []Hunk, ns []Note) bool {
	for _, h := range hs {
		if h.Severity == SeverityBreaking {
			return true
		}
	}
	for _, n := range ns {
		if n.Severity == SeverityBreaking {
			return true
		}
	}
	return false
}
