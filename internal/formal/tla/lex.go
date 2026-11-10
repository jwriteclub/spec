// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tla

import (
	"bytes"
	"context"
	"strings"

	"github.com/jwriteclub/prudence"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/pos"
)

// utf8BOM is the byte order mark.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// constantWords are the built-in values prudence lexes as identifiers; they
// are drawn as keywords, as TLA+ editors draw them.
var constantWords = map[string]bool{"TRUE": true, "FALSE": true, "BOOLEAN": true, "STRING": true}

// Lex splits TLA+ source into highlight tokens with prudence's recovering
// lexer. It implements formal.Lexer. SANY ignores text before the first
// module header and after its closing ====, so that text is a comment here.
// When prudence fails to run, it logs why and returns nil: plain text.
func Lex(src []byte) []formal.Token {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	raw, _, err := tokenize(ctx, "", src)
	if err != nil {
		lg.WithError(err).Warn("prudence could not lex a model; it is drawn as plain text")
		return nil
	}
	return highlight(src, moduleTokens(src, raw))
}

// tokenize and parse are prudence's entry points; tests replace them to see
// what an operational failure does.
var (
	tokenize = prudence.Tokens
	parse    = prudence.Parse
)

// token is one prudence token cut to the module, with its range in bytes.
type token struct {
	kind prudence.TokenKind
	r    pos.Range
	// lineStart is true for a token that opens its line.
	lineStart bool
}

// isComment reports whether t is a comment.
func (t token) isComment() bool {
	switch t.kind {
	case prudence.TokenComment, prudence.TokenLineComment, prudence.TokenBlockComment:
		return true
	}
	return false
}

// module holds the tokens SANY reads.
type module struct {
	toks []token
	// start and end bound the module: its header and the end of its
	// closing ==== (or of the source).
	start, end int
}

// moduleTokens converts prudence's tokens to byte ranges and keeps those of
// the first module, through its closing ====. The pieces of a nested (* *)
// comment become one token, as does an unterminated one (whose rest is an
// invalid token to prudence), and a line comment stops before its line break.
func moduleTokens(src []byte, raw []prudence.Token) module {
	m := module{end: len(src)}
	depth := 0
	for _, rt := range raw {
		t := token{kind: rt.Kind, r: pos.Range{Start: clamp(rt.Span.Start.ByteOffset, src), End: clamp(rt.Span.End.ByteOffset, src)}}
		if t.r.End <= t.r.Start {
			continue
		}
		text := src[t.r.Start:t.r.End]
		switch {
		case t.kind == prudence.TokenInvalid && bytes.HasPrefix(text, []byte("(*")):
			t.kind = prudence.TokenBlockComment
		case t.kind == prudence.TokenLineComment:
			t.r.End = t.r.Start + len(bytes.TrimRight(text, "\r\n"))
		}
		if n := len(m.toks); n > 0 && (t.kind == prudence.TokenComment || t.kind == prudence.TokenInvalid) {
			last := &m.toks[n-1]
			if last.kind == prudence.TokenBlockComment && last.r.End == t.r.Start && unclosed(src[last.r.Start:last.r.End]) {
				last.r.End = t.r.End
				continue
			}
		}
		t.lineStart = lineStart(src, t.r.Start)
		if len(m.toks) == 0 {
			m.start = t.r.Start
		}
		m.toks = append(m.toks, t)
		switch {
		case t.kind == prudence.TokenDelimiter && isHeader(text):
			depth++
		case t.kind == prudence.TokenDelimiter && len(text) >= 4 && len(bytes.Trim(text, "=")) == 0:
			if depth--; depth <= 0 {
				m.end = t.r.End
				return m
			}
		}
	}
	return m
}

// unclosed reports whether a (* *) comment's text opens more than it closes.
func unclosed(text []byte) bool {
	depth := 0
	for i := 0; i+1 < len(text); i++ {
		switch {
		case text[i] == '(' && text[i+1] == '*':
			depth++
			i++
		case text[i] == '*' && text[i+1] == ')':
			depth--
			i++
		}
	}
	return depth > 0
}

// isHeader reports whether a delimiter's text opens a module: "---- MODULE".
func isHeader(text []byte) bool {
	return bytes.HasPrefix(text, []byte("----")) && bytes.HasSuffix(text, []byte("MODULE"))
}

// clamp keeps an offset within src.
func clamp(off int, src []byte) int {
	return max(0, min(off, len(src)))
}

// lineStart reports whether off opens its line; a leading byte order mark
// takes no room.
func lineStart(src []byte, off int) bool {
	return off == 0 || src[off-1] == '\n' || (off == len(utf8BOM) && bytes.HasPrefix(src, utf8BOM))
}

// highlight maps the module's tokens to logedit's classes. The module's name
// is a definition; the other declarations come from File.Decls.
func highlight(src []byte, m module) []formal.Token {
	var out []formal.Token
	emit := func(k formal.TokenKind, start, end int) {
		if end > start {
			out = append(out, formal.Token{Kind: k, Range: pos.Range{Start: start, End: end}})
		}
	}
	lead := 0
	if bytes.HasPrefix(src, utf8BOM) {
		lead = len(utf8BOM)
	}
	if len(m.toks) > 0 && len(bytes.TrimSpace(src[lead:m.start])) > 0 {
		emit(formal.TokenComment, lead, m.start)
	}
	named := false
	for i, t := range m.toks {
		text := string(src[t.r.Start:t.r.End])
		switch t.kind {
		case prudence.TokenKeyword, prudence.TokenProofKeyword:
			emit(formal.TokenKeyword, t.r.Start, t.r.End)
		case prudence.TokenIdentifier:
			switch {
			case !named && i > 0 && isHeader(src[m.toks[i-1].r.Start:m.toks[i-1].r.End]):
				named = true
				emit(formal.TokenDefinition, t.r.Start, t.r.End)
			case constantWords[text]:
				emit(formal.TokenKeyword, t.r.Start, t.r.End)
			}
		case prudence.TokenOperator, prudence.TokenDelimiter:
			if t.kind == prudence.TokenDelimiter && isHeader([]byte(text)) {
				dashes := strings.LastIndexByte(text, '-') + 1
				emit(formal.TokenOperator, t.r.Start, t.r.Start+dashes)
				emit(formal.TokenKeyword, t.r.End-len("MODULE"), t.r.End)
				continue
			}
			kind := formal.TokenOperator
			if isWord(text) {
				kind = formal.TokenKeyword // ENABLED, UNCHANGED, DOMAIN, SUBSET, UNION
			}
			emit(kind, t.r.Start, t.r.End)
		case prudence.TokenNumber:
			emit(formal.TokenNumber, t.r.Start, t.r.End)
		case prudence.TokenString:
			emit(formal.TokenString, t.r.Start, t.r.End)
		case prudence.TokenComment, prudence.TokenLineComment, prudence.TokenBlockComment:
			emit(formal.TokenComment, t.r.Start, t.r.End)
		}
	}
	if len(m.toks) > 0 && len(bytes.TrimSpace(src[m.end:])) > 0 {
		emit(formal.TokenComment, m.end, len(src))
	}
	return out
}

// isWord reports whether an operator is spelled with capital letters only.
func isWord(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] < 'A' || text[i] > 'Z' {
			return false
		}
	}
	return text != ""
}
