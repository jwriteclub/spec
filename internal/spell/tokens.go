// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// rightSingleQuote is U+2019, the typographic apostrophe.
const rightSingleQuote = '\u2019'

// Token is one word or identifier of a spec file's prose.
type Token struct {
	// Range is the token's byte range in the file's source.
	Range pos.Range
	// Text is the token as written, without surrounding punctuation or a
	// possessive 's.
	Text string
}

// Part is one piece of a split identifier: its text and its byte offset
// within the token.
type Part struct {
	Text string
	Off  int
}

// skipped returns the ranges of f that are never checked, sorted by start:
// opaque regions (code, HTML, autolinks, link destinations, front matter),
// $Name occurrences, § references, #N references, near-miss $ candidates
// and malformed section references.
func skipped(f *mdspec.File) []pos.Range {
	var out []pos.Range
	for _, o := range f.Opaque {
		out = append(out, o.Range)
	}
	for _, o := range f.Occurrences {
		out = append(out, o.Range)
	}
	for _, r := range f.SectionRefs {
		out = append(out, r.Range)
	}
	for _, h := range f.HashRefs {
		out = append(out, h.Range)
	}
	for _, c := range f.Candidates {
		out = append(out, c.Range)
	}
	out = append(out, f.MalformedSectionRefs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// codeRanges returns the code spans and code blocks of f: their
// identifiers are project vocabulary.
func codeRanges(f *mdspec.File) []pos.Range {
	var out []pos.Range
	for _, o := range f.Opaque {
		if o.Kind == mdspec.OpaqueCodeSpan || o.Kind == mdspec.OpaqueCodeBlock {
			out = append(out, o.Range)
		}
	}
	return out
}

// runChar reports whether r continues a word run: letters, digits and the
// punctuation identifiers and URLs are made of.
func runChar(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	switch r {
	case '_', '.', '-', '\'', rightSingleQuote, '/', ':', '@', '\\', '~', '%', '+', '=', '#', '&', '?':
		return true
	}
	return false
}

// trimSet is what is stripped from both ends of a run.
const trimSet = "_.-'/:@\\~%+=#&?\u2019"

// Tokenize returns the prose tokens of f outside the skipped regions.
// Runs that look like paths, URLs, e-mail addresses or hashes, and runs
// with letters outside ASCII, are dropped.
func Tokenize(f *mdspec.File) []Token {
	return tokenize(f.Source(), skipped(f))
}

// tokenize scans src for word runs outside skip (sorted by start).
func tokenize(src []byte, skip []pos.Range) []Token {
	var out []Token
	k := 0
	for i := 0; i < len(src); {
		for k < len(skip) && skip[k].End <= i {
			k++
		}
		if k < len(skip) && skip[k].Start <= i {
			i = skip[k].End
			continue
		}
		r, w := utf8.DecodeRune(src[i:])
		if !runChar(r) {
			i += w
			continue
		}
		start := i
		end := i
		for end < len(src) {
			if k < len(skip) && skip[k].Start <= end {
				break
			}
			r, w := utf8.DecodeRune(src[end:])
			if !runChar(r) {
				break
			}
			end += w
		}
		i = end
		if t, ok := tokenOf(src, start, end); ok {
			out = append(out, t)
		}
	}
	return out
}

// tokenOf trims the run src[start:end] and decides whether it is a word.
func tokenOf(src []byte, start, end int) (Token, bool) {
	run := string(src[start:end])
	lead := len(run) - len(strings.TrimLeft(run, trimSet))
	run = strings.TrimLeft(run, trimSet)
	run = strings.TrimRight(run, trimSet)
	if run == "" {
		return Token{}, false
	}
	start += lead
	// A possessive.
	for _, s := range []string{"'s", "\u2019s"} {
		if strings.HasSuffix(run, s) && len(run) > len(s) {
			run = strings.TrimSuffix(run, s)
		}
	}
	if strings.ContainsAny(run, "/:@\\~%+=#&?") {
		return Token{}, false // a path, URL, e-mail address or expression
	}
	letters := 0
	for _, r := range run {
		switch {
		case r > unicode.MaxASCII:
			return Token{}, false
		case unicode.IsLetter(r):
			letters++
		}
	}
	if letters == 0 || hexLike(run) {
		return Token{}, false
	}
	return Token{Range: pos.Range{Start: start, End: start + len(run)}, Text: run}, true
}

// hexLike reports a run that is a hash or hexadecimal number: at least 7
// characters, all hex digits, with at least one digit.
func hexLike(s string) bool {
	if len(s) < 7 {
		return false
	}
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
		default:
			return false
		}
	}
	return digits > 0
}

// Split splits an identifier into its words: on _ . - and apostrophes, on
// digits (which are dropped), on lower-to-upper case changes, and before
// the last capital of an acronym followed by a lower-case letter
// ("parseHTMLTokens" is parse, HTML, Tokens).
func Split(word string) []Part {
	var out []Part
	rs := []rune(word)
	offs := make([]int, len(rs)+1)
	o := 0
	for i, r := range rs {
		offs[i] = o
		o += utf8.RuneLen(r)
	}
	offs[len(rs)] = o
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			out = append(out, Part{Text: string(rs[start:end]), Off: offs[start]})
		}
		start = -1
	}
	for i, r := range rs {
		if !unicode.IsLetter(r) {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		prev := rs[i-1]
		switch {
		case unicode.IsLower(prev) && unicode.IsUpper(r):
			flush(i)
			start = i
		case unicode.IsUpper(prev) && unicode.IsUpper(r) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
			flush(i)
			start = i
		}
	}
	flush(len(rs))
	return out
}

// sentence returns the sentence of src around r: from the previous
// sentence end, blank line or block start to the next, at most limit bytes
// either side, with whitespace collapsed, and r's offset within it.
func sentence(src []byte, r pos.Range, limit int) (string, int) {
	lo := r.Start
	for lo > 0 && r.Start-lo < limit {
		c := src[lo-1]
		if c == '\n' && lo >= 2 && src[lo-2] == '\n' {
			break
		}
		if (c == ' ' || c == '\n') && lo >= 2 && strings.IndexByte(".!?", src[lo-2]) >= 0 {
			break
		}
		lo--
	}
	hi := r.End
	for hi < len(src) && hi-r.End < limit {
		c := src[hi]
		if c == '\n' && hi+1 < len(src) && src[hi+1] == '\n' {
			break
		}
		hi++
		if strings.IndexByte(".!?", c) >= 0 && (hi == len(src) || src[hi] == ' ' || src[hi] == '\n') {
			break
		}
	}
	before := collapse(string(src[lo:r.Start]))
	word := string(src[r.Start:r.End])
	after := collapse(string(src[r.End:hi]))
	before = strings.TrimLeft(before, " #*->|")
	text := before + word + after
	return strings.TrimSpace(text), len(before)
}

// collapse replaces every run of white space with one space.
func collapse(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}
