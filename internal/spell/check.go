// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package spell is the contextual spell check of spec prose. A classical
// pass (tokenizer, identifier splitter, the system word list, the project's
// own vocabulary and a known-misspellings table) flags known misspellings
// and leaves the unknown words as candidates; a model pass (the Jev gate
// scores the candidates in batches, and the triage chat model names the
// typos and their fixes) judges the candidates in context. Verdicts are
// cached per candidate in .spec/spell/, so a repeated analyze costs
// nothing and the editor and language server show them on open.
package spell

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/sirupsen/logrus"
	"github.com/tenta-browser/polychromatic"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

var lg *logrus.Entry = polychromatic.GetLogger("spell")

// Who flagged a word.
const (
	// ByKnown is the known-misspellings table (no model).
	ByKnown = "known"
	// ByModel is the model pass (Jev, then the triage model).
	ByModel = "model"
)

// minLetters is the shortest word part checked (cspell's rule): shorter
// parts are too often abbreviations.
const minLetters = 4

// maxAcronym is the longest all-capitals part treated as an acronym.
const maxAcronym = 5

// frequentUses and frequentFiles make a word project vocabulary: used this
// many times, or in this many files.
const (
	frequentUses  = 3
	frequentFiles = 2
)

// Flag is one misspelled word.
type Flag struct {
	File  string    `json:"file"`
	Range pos.Range `json:"range"`
	// Line and Col are 1-based (Col in bytes).
	Line int `json:"line"`
	Col  int `json:"col"`
	// Word is the word as written; Fix its correction.
	Word string `json:"word"`
	Fix  string `json:"fix"`
	// By is ByKnown or ByModel.
	By string `json:"by"`
	// key is the word's cache key.
	key string
}

// Candidate is a word the classical pass does not know: the model pass
// judges it.
type Candidate struct {
	File  string
	Range pos.Range
	Word  string
	// Part is the unknown part of a split identifier ("" when the word is
	// one part); PartOff its byte offset in Word.
	Part    string
	PartOff int
	// Sentence is the sentence around the word, white space collapsed;
	// Mark is the word's byte offset in it.
	Sentence string
	Mark     int
	// Uses and Files count the word in the project's prose.
	Uses  int
	Files int
	// Key identifies the candidate in the cache.
	Key string
}

// Scope limits a check to some files, or ranges of them: a file mapped to
// nil is checked whole, a file not in the map is not checked. A nil Scope
// checks every file.
type Scope map[string][]pos.Range

// in reports whether r of file is in scope.
func (s Scope) in(file string, r pos.Range) bool {
	if s == nil {
		return true
	}
	rs, ok := s[file]
	if !ok {
		return false
	}
	if rs == nil {
		return true
	}
	for _, x := range rs {
		if r.Start < x.End && x.Start < r.End {
			return true
		}
	}
	return false
}

// Project is the classical pass's knowledge of one project: the word list,
// the words the project uses and how often, and the words it vouches for.
type Project struct {
	Dict *Dictionary
	// user are the spec.words entries: always accepted, even a known
	// misspelling.
	user map[string]bool
	// explicit are anchor names and their parts, code identifiers and
	// their parts, and the project name.
	explicit map[string]bool
	// uses and files count each lower-case word and identifier part in
	// prose.
	uses  map[string]int
	files map[string]int
	// terms are the vocabulary words for near-term lookups, by length.
	terms map[int][]string
}

// NewProject builds the classical pass over spec: words are the spec.words
// entries, name the project name, dict the word list (nil: System()).
func NewProject(spec *mdspec.Spec, words []string, name string, dict *Dictionary) *Project {
	var fws []*fileWords
	for _, f := range spec.Files {
		if f.ValidUTF8 {
			fws = append(fws, wordsOf(f))
		}
	}
	return buildProject(spec.Anchors, fws, words, name, dict)
}

// fileWords is what one spec file gives the classical pass: its prose
// tokens, the identifiers of its code and their parts (lower case), and
// how often its prose uses each lower-case word and identifier part.
type fileWords struct {
	tokens []Token
	code   map[string]bool
	uses   map[string]int
}

// wordsOf reads the words of f, a file of valid UTF-8.
func wordsOf(f *mdspec.File) *fileWords {
	src := f.Source()
	fw := &fileWords{tokens: Tokenize(f), code: map[string]bool{}, uses: map[string]int{}}
	for _, r := range codeRanges(f) {
		for _, t := range tokenize(src[r.Start:r.End], nil) {
			addExplicit(fw.code, t.Text)
		}
	}
	for _, t := range fw.tokens {
		lw := strings.ToLower(t.Text)
		fw.uses[lw]++
		for _, part := range Split(t.Text) {
			if lp := strings.ToLower(part.Text); lp != lw {
				fw.uses[lp]++
			}
		}
	}
	return fw
}

// buildProject builds the classical pass from the spec's anchors and its
// files' words.
func buildProject(anchors []*mdspec.Anchor, fws []*fileWords, words []string, name string, dict *Dictionary) *Project {
	p := emptyProject(words, dict)
	for w := range baseWords(name, anchors) {
		p.explicit[w] = true
	}
	for _, fw := range fws {
		for w := range fw.code {
			p.explicit[w] = true
		}
		p.count(fw, 1)
	}
	return p
}

// emptyProject is the classical pass over no files: words are the
// spec.words entries, dict the word list (nil: System()).
func emptyProject(words []string, dict *Dictionary) *Project {
	if dict == nil {
		dict = System()
	}
	p := &Project{Dict: dict, user: map[string]bool{}, explicit: map[string]bool{}, uses: map[string]int{}, files: map[string]int{}}
	for _, w := range words {
		p.user[strings.ToLower(w)] = true
	}
	return p
}

// baseWords are the identifiers every project vouches for besides its
// code: its name and its anchors' names, with their parts, in lower case.
func baseWords(name string, anchors []*mdspec.Anchor) map[string]bool {
	set := map[string]bool{}
	addExplicit(set, name)
	for _, a := range anchors {
		addExplicit(set, a.Name)
	}
	return set
}

// count adds a file's word counts to p (sign 1) or takes them away (-1).
func (p *Project) count(fw *fileWords, sign int) {
	for w, n := range fw.uses {
		p.uses[w] += sign * n
		p.files[w] += sign
		if p.files[w] <= 0 {
			delete(p.uses, w)
			delete(p.files, w)
		}
	}
}

// standing is what the project knows of lw (lower case) besides
// spec.words, as far as the classical pass and the cache key can tell:
// whether it is an identifier, its use count up to frequentUses and its
// file count up to frequentFiles. A file's flags depend only on the
// standing of its words and their parts.
func (p *Project) standing(lw string) uint8 {
	s := uint8(min(p.uses[lw], frequentUses)) | uint8(min(p.files[lw], frequentFiles))<<2
	if p.explicit[lw] {
		s |= 1 << 4
	}
	return s
}

// addExplicit adds an identifier (or dotted name) and its parts to set,
// in lower case.
func addExplicit(set map[string]bool, id string) {
	if id == "" {
		return
	}
	set[strings.ToLower(id)] = true
	for _, seg := range strings.Split(id, ".") {
		set[strings.ToLower(seg)] = true
	}
	for _, part := range Split(id) {
		set[strings.ToLower(part.Text)] = true
	}
}

// vocabulary reports whether lw (lower case) is a project word: named in
// spec.words, an identifier, or frequent.
func (p *Project) vocabulary(lw string) bool {
	return p.user[lw] || p.explicit[lw] || p.uses[lw] >= frequentUses || p.files[lw] >= frequentFiles
}

// good reports whether lw (lower case) is spelled right as far as the
// classical pass can tell.
func (p *Project) good(lw string) bool {
	return p.vocabulary(lw) || p.Dict.has(lw) || corrections()[lw]
}

// Valid reports whether a fix's word (or identifier part) is acceptable:
// a dictionary word, a project term (used anywhere in the project), or a
// word the known-misspellings table corrects to, and not itself a known
// misspelling.
func (p *Project) Valid(w string) bool {
	lw := strings.ToLower(w)
	if p.user[lw] {
		return true
	}
	if _, bad := known()[lw]; bad {
		return false
	}
	return p.explicit[lw] || p.uses[lw] > 0 || p.Dict.has(lw) || corrections()[lw]
}

// skipPart reports a part too short or acronym-like to check.
func skipPart(s string) bool {
	if len(s) < minLetters {
		return true
	}
	if len(s) <= maxAcronym && strings.ToUpper(s) == s {
		return true
	}
	return false
}

// verdict is what the classical pass makes of one token.
type verdict struct {
	known   bool
	fix     string
	unknown bool
	part    Part
}

// judge applies the classical pass to one token.
func (p *Project) judge(word string) verdict {
	lw := strings.ToLower(word)
	if p.user[lw] || contractions[lw] {
		return verdict{}
	}
	if fix, ok := known()[lw]; ok {
		return verdict{known: true, fix: MatchCase(word, fix)}
	}
	if p.vocabulary(lw) {
		return verdict{}
	}
	parts := Split(word)
	var out verdict
	for _, part := range parts {
		if skipPart(part.Text) {
			continue
		}
		lp := strings.ToLower(part.Text)
		if p.user[lp] {
			continue
		}
		if fix, ok := known()[lp]; ok {
			return verdict{known: true, fix: word[:part.Off] + MatchCase(part.Text, fix) + word[part.Off+len(part.Text):]}
		}
		if p.good(lp) {
			continue
		}
		if !out.unknown {
			out = verdict{unknown: true, part: part}
		}
	}
	if out.unknown && len(parts) == 1 {
		out.part = Part{}
	}
	return out
}

// Check runs the classical pass over files in scope: the known
// misspellings, with their fixes, and the candidates for the model pass.
func (p *Project) Check(files []*mdspec.File, scope Scope) ([]Flag, []Candidate) {
	var flags []Flag
	var cands []Candidate
	for _, f := range files {
		if !f.ValidUTF8 {
			continue
		}
		if scope != nil {
			if _, ok := scope[f.Path]; !ok {
				continue
			}
		}
		fl, cs := p.checkFile(f, Tokenize(f), scope)
		flags = append(flags, fl...)
		cands = append(cands, cs...)
	}
	return flags, cands
}

// checkFile runs the classical pass over the tokens of f in scope.
func (p *Project) checkFile(f *mdspec.File, tokens []Token, scope Scope) ([]Flag, []Candidate) {
	var flags []Flag
	var cands []Candidate
	src := f.Source()
	x := f.Index()
	for _, t := range tokens {
		if !scope.in(f.Path, t.Range) {
			continue
		}
		v := p.judge(t.Text)
		if !v.known && !v.unknown {
			continue
		}
		lw := strings.ToLower(t.Text)
		sent, mark := sentence(src, t.Range, 240)
		c := Candidate{File: f.Path, Range: t.Range, Word: t.Text, Part: v.part.Text, PartOff: v.part.Off,
			Sentence: sent, Mark: mark, Uses: p.uses[lw], Files: p.files[lw]}
		c.Key = key(c)
		if v.known {
			fl := newFlag(f.Path, x, t, v.fix, ByKnown)
			fl.key = c.Key
			flags = append(flags, fl)
			continue
		}
		cands = append(cands, c)
	}
	return flags, cands
}

// newFlag builds the flag of token t of a file indexed by x.
func newFlag(file string, x *pos.Index, t Token, fix, by string) Flag {
	at := x.Position(t.Range.Start)
	return Flag{File: file, Range: t.Range, Line: at.Line, Col: at.Col, Word: t.Text, Fix: fix, By: by}
}

// key is a candidate's cache key: the word, its sentence and how often the
// project uses it (1, 2, or 3 or more).
func key(c Candidate) string {
	bucket := min(c.Uses, frequentUses)
	h := sha256.Sum256([]byte(strings.ToLower(c.Word) + "\x00" + c.Sentence + "\x00" + fmt.Sprint(bucket)))
	return hex.EncodeToString(h[:8])
}

// MatchCase writes fix in the case of orig: all capitals, a leading
// capital, or as given (a fix that has capitals of its own keeps them).
func MatchCase(orig, fix string) string {
	if fix == "" || orig == "" {
		return fix
	}
	if strings.ToLower(fix) != fix {
		return fix
	}
	letters := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return r
		}
		return -1
	}, orig)
	if len(letters) > 1 && strings.ToUpper(letters) == letters {
		return strings.ToUpper(fix)
	}
	if r := []rune(orig)[0]; unicode.IsUpper(r) {
		rs := []rune(fix)
		rs[0] = unicode.ToUpper(rs[0])
		return string(rs)
	}
	return fix
}

// Info is the line of facts the models see with a candidate: how often the
// project uses it, the unknown part of an identifier, near dictionary
// words and near project terms.
func (p *Project) Info(c Candidate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s in the project", c.Word, plural(c.Uses, "use"))
	if c.Files > 1 {
		fmt.Fprintf(&b, ", %d files", c.Files)
	}
	target := c.Word
	if c.Part != "" {
		fmt.Fprintf(&b, "; split: %s; %q unknown", strings.Join(partTexts(c.Word), " "), c.Part)
		target = c.Part
	}
	if s := p.Suggest(target, 3); len(s) > 0 {
		fmt.Fprintf(&b, "; dictionary suggestions: %s", strings.Join(s, ", "))
	} else {
		b.WriteString("; no dictionary suggestion")
	}
	if n := p.Near(c.Word, 3); len(n) > 0 {
		var ts []string
		for _, t := range n {
			ts = append(ts, fmt.Sprintf("'%s' %s", t, plural(p.uses[t], "use")))
		}
		fmt.Fprintf(&b, "; similar project terms: %s", strings.Join(ts, ", "))
	}
	return b.String()
}

// partTexts returns the parts of word.
func partTexts(word string) []string {
	var out []string
	for _, p := range Split(word) {
		out = append(out, p.Text)
	}
	return out
}

// plural writes n and noun, pluralized.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// Suggest returns up to n dictionary words one edit from w (lower case,
// sorted), from the word list, the project vocabulary and the known
// corrections.
func (p *Project) Suggest(w string, n int) []string {
	lw := strings.ToLower(w)
	seen := map[string]bool{}
	var out []string
	for _, e := range edits1(lw) {
		if seen[e] || e == lw {
			continue
		}
		seen[e] = true
		if p.Dict.words[e] || p.explicit[e] || p.uses[e] >= frequentUses || corrections()[e] {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// edits1 returns every string one deletion, transposition, substitution
// or insertion (of a-z) from w.
func edits1(w string) []string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	var out []string
	for i := 0; i <= len(w); i++ {
		a, b := w[:i], w[i:]
		if len(b) > 0 {
			out = append(out, a+b[1:])
		}
		if len(b) > 1 {
			out = append(out, a+string(b[1])+string(b[0])+b[2:])
		}
		for _, c := range letters {
			if len(b) > 0 {
				out = append(out, a+string(c)+b[1:])
			}
			out = append(out, a+string(c)+b)
		}
	}
	return out
}

// Near returns up to n project terms (frequent words and identifiers, at
// least 5 letters) within edit distance 2 of w, most used first.
func (p *Project) Near(w string, n int) []string {
	if p.terms == nil {
		p.terms = map[int][]string{}
		add := func(t string) {
			if len(t) >= 5 {
				p.terms[len(t)] = append(p.terms[len(t)], t)
			}
		}
		for t := range p.explicit {
			add(t)
		}
		for t, c := range p.uses {
			if !p.explicit[t] && (c >= frequentUses || p.files[t] >= frequentFiles) {
				add(t)
			}
		}
		for _, ts := range p.terms {
			sort.Strings(ts)
		}
	}
	lw := strings.ToLower(w)
	var out []string
	for l := len(lw) - 2; l <= len(lw)+2; l++ {
		for _, t := range p.terms[l] {
			if t != lw && Distance(lw, t) <= 2 {
				out = append(out, t)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if p.uses[out[i]] != p.uses[out[j]] {
			return p.uses[out[i]] > p.uses[out[j]]
		}
		return out[i] < out[j]
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Distance is the optimal-string-alignment Damerau-Levenshtein distance
// between a and b (in bytes): insertions, deletions, substitutions and
// transpositions of adjacent characters each cost 1.
func Distance(a, b string) int {
	n, m := len(a), len(b)
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
		d[i][0] = i
	}
	for j := 0; j <= m; j++ {
		d[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[n][m]
}

// Code is the diagnostic code of a spelling flag.
const Code = diag.CodeSpell

// Diagnostics turns flags into diagnostics: code spell, severity hint.
func Diagnostics(flags []Flag) []diag.Diagnostic {
	out := make([]diag.Diagnostic, 0, len(flags))
	for _, f := range flags {
		out = append(out, diag.Diagnostic{Code: Code, Severity: diag.Hint, File: f.File, Range: f.Range, Message: Message(f)})
	}
	return out
}

// Message is a flag's diagnostic message.
func Message(f Flag) string {
	return fmt.Sprintf("%q may be misspelled: did you mean %q?", f.Word, f.Fix)
}
