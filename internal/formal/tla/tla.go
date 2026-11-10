// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package tla reads TLA+ models with prudence, a Go port of SANY's parser:
// their tokens for highlighting, their module and top-level declarations,
// their syntax errors, and the comments that hold logedit's tags. Tags and
// what they bind to are logedit's own; prudence knows nothing of them.
//
// SANY ignores text before the first module header and after its closing
// ====, and so does this package. A model with no header has no tokens.
package tla

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jwriteclub/prudence"
	"github.com/sirupsen/logrus"
	"github.com/tenta-browser/polychromatic"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/pos"
)

var lg *logrus.Entry = polychromatic.GetLogger("tla")

// timeout bounds one prudence call, since a Scanner and a Lexer take no
// context. A model parses in milliseconds.
const timeout = 10 * time.Second

// theoremWords open an assumption or a theorem. One with no name declares
// nothing, and a tag in its body binds to nothing.
var theoremWords = map[string]bool{
	"ASSUME": true, "ASSUMPTION": true, "AXIOM": true,
	"THEOREM": true, "LEMMA": true, "PROPOSITION": true, "COROLLARY": true,
}

// declKinds maps prudence's declaration kinds to logedit's. The others (the
// module, a submodule, a RECURSIVE forward declaration) are not declarations
// a tag binds to.
var declKinds = map[prudence.DeclKind]formal.DeclKind{
	prudence.DeclDefinition: formal.DeclDefinition,
	prudence.DeclVariable:   formal.DeclVariable,
	prudence.DeclConstant:   formal.DeclConstant,
	prudence.DeclTheorem:    formal.DeclTheorem,
	prudence.DeclAssumption: formal.DeclTheorem,
	prudence.DeclOperator:   formal.DeclSymbolic,
}

// Scan reads a TLA+ module, with its highlight tokens from the same prudence
// call. It implements formal.Scanner. When prudence fails to run, it logs why
// and returns the file with that as a problem.
func Scan(path string, src []byte) *formal.File {
	f := formal.NewFile(path, src)
	if !f.ValidUTF8 {
		f.Tokens = Lex(src)
		return f
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := parse(ctx, path, src, prudence.Options{IncludeTokens: true})
	if err != nil {
		lg.WithError(err).WithField("path", path).Warn("prudence could not parse a model")
		f.Syntax = []formal.Problem{{Severity: diag.Error, Code: prudence.CodeInternal, Message: "prudence could not parse this model: " + err.Error()}}
		return f
	}
	m := moduleTokens(src, res.Tokens)
	f.Tokens = highlight(src, m)
	f.Module = res.Outline.Module
	f.Decls = decls(src, res.Outline.Decls)
	f.Decls = withConstants(src, m.toks, f.Decls)
	f.Syntax = Problems(src, res.Diagnostics)
	(&binder{f: f, src: src, toks: m.toks}).readComments()
	return f
}

// decls converts prudence's top-level declarations, in file order.
func decls(src []byte, in []prudence.Decl) []*formal.Decl {
	var out []*formal.Decl
	for _, d := range in {
		kind, ok := declKinds[d.Kind]
		if !ok {
			continue
		}
		out = append(out, &formal.Decl{Name: d.Name, Kind: kind, NameRange: Range(src, d.NameSpan), Range: Range(src, d.FullSpan)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].NameRange.Start < out[j].NameRange.Start })
	return out
}

// withConstants adds the names of CONSTANT lists, read from the tokens, when
// prudence's outline has none: the draft outline omits them (reported to its
// author). It does nothing once prudence lists constants, and can then go.
func withConstants(src []byte, toks []token, ds []*formal.Decl) []*formal.Decl {
	for _, d := range ds {
		if d.Kind == formal.DeclConstant {
			return ds
		}
	}
	added := false
	for i := 0; i < len(toks); i++ {
		w := string(src[toks[i].r.Start:toks[i].r.End])
		if !toks[i].lineStart || (w != "CONSTANT" && w != "CONSTANTS") {
			continue
		}
		start := toks[i].r.Start
		var names []pos.Range
		depth, expect := 0, true
	list:
		for i++; i < len(toks); i++ {
			k := toks[i]
			if k.isComment() {
				continue
			}
			switch text := string(src[k.r.Start:k.r.End]); {
			case text == "(":
				depth++
			case text == ")":
				depth--
			case depth > 0:
			case text == ",":
				expect = true
			case expect && k.kind == prudence.TokenIdentifier:
				names = append(names, k.r)
				expect = false
			default:
				break list
			}
		}
		i--
		end := toks[i].r.End
		for _, r := range names {
			ds = append(ds, &formal.Decl{Name: string(src[r.Start:r.End]), Kind: formal.DeclConstant, NameRange: r, Range: pos.Range{Start: start, End: end}})
			added = true
		}
	}
	if added {
		sort.SliceStable(ds, func(i, j int) bool { return ds[i].NameRange.Start < ds[j].NameRange.Start })
	}
	return ds
}

// Range converts a prudence span to a byte range of src. Prudence's spans
// are byte offsets already; this only keeps them within src.
func Range(src []byte, s prudence.Span) pos.Range {
	start := clamp(s.Start.ByteOffset, src)
	return pos.Range{Start: start, End: max(start, clamp(s.End.ByteOffset, src))}
}

// Problems converts prudence's diagnostics. An empty span is widened to the
// character at it, so an editor can underline it.
func Problems(src []byte, ds []prudence.Diagnostic) []formal.Problem {
	var out []formal.Problem
	for _, d := range ds {
		r := Range(src, d.Span)
		if r.Empty() && r.Start < len(src) && src[r.Start] != '\n' && src[r.Start] != '\r' {
			r.End = r.Start + 1
			for r.End < len(src) && src[r.End]&0xC0 == 0x80 {
				r.End++
			}
		}
		out = append(out, formal.Problem{Severity: severity(d.Severity), Code: d.Code, Message: Message(d.Message), Range: r})
	}
	return out
}

// severity maps a prudence severity to a lint severity.
func severity(s prudence.Severity) diag.Severity {
	switch s {
	case prudence.SeverityError:
		return diag.Error
	case prudence.SeverityWarning:
		return diag.Warning
	}
	return diag.Hint
}

// Message puts a SANY message on one line: without the "***Parse Error***"
// banner and the residual stack trace, which name the enclosing constructs.
func Message(msg string) string {
	if i := strings.Index(msg, "Residual stack trace follows:"); i >= 0 {
		msg = msg[:i]
	}
	msg = strings.TrimPrefix(strings.TrimSpace(msg), "***Parse Error***")
	return strings.Join(strings.Fields(msg), " ")
}

// binder reads the tags in a module's comments and binds each to the
// declaration it is about.
type binder struct {
	f    *formal.File
	src  []byte
	toks []token
}

// readComments reads the line comments and the tags of every comment.
func (b *binder) readComments() {
	for _, t := range b.toks {
		switch t.kind {
		case prudence.TokenLineComment:
			b.lineComment(t.r)
		case prudence.TokenBlockComment, prudence.TokenComment:
			b.blockComment(t.r)
		}
	}
}

func (b *binder) lineComment(r pos.Range) {
	text := r.Start + len(`\*`)
	b.f.LineComments = append(b.f.LineComments, pos.Range{Start: text, End: r.End})
	if t := formal.ParseTag(b.src, text, r.End, r.End); t != nil {
		t.Range.Start = r.Start
		b.bind(t, r.Start)
	}
}

// blockComment reads the tags that open a line of the outermost level of a
// (* *) comment. In SANY the comment nests, and nothing else means anything in
// it: a quote holding *) ends it.
func (b *binder) blockComment(r pos.Range) {
	src := b.src
	depth := 1
	j := r.Start + 2
	lines := []int{j}
	body := r.End
scan:
	for j < r.End {
		switch {
		case j+1 < r.End && src[j] == '(' && src[j+1] == '*':
			depth++
			j += 2
		case j+1 < r.End && src[j] == '*' && src[j+1] == ')':
			depth--
			j += 2
			if depth == 0 {
				body = j - 2
				break scan
			}
		default:
			if src[j] == '\n' && depth == 1 {
				lines = append(lines, j+1)
			}
			j++
		}
	}
	covered := r.Start
	for _, line := range lines {
		if line < covered {
			continue // inside the quote of the previous tag
		}
		if line >= body {
			break
		}
		lineEnd := body
		if nl := bytes.IndexByte(src[line:body], '\n'); nl >= 0 {
			lineEnd = line + nl
		}
		if t := formal.ParseTag(src, line, lineEnd, body); t != nil {
			t.Block = true
			covered = t.Range.End
			b.bind(t, r.Start)
		}
	}
}

// bind records a tag and decides what it is about, from where its comment
// starts (at):
//
//   - a declaration whose name is earlier on the same line, as in
//     `x, \* Spec: §1` in a VARIABLES list;
//   - when the comment is indented, the definition whose body it is in: the
//     latest one, when no line has started in column 0 since;
//   - nothing, when indented in the body of an unnamed ASSUME or THEOREM;
//   - otherwise the next declaration, unless EXTENDS, INSTANCE or an unnamed
//     theorem comes before it: a tag there is about the module.
func (b *binder) bind(t *formal.Tag, at int) {
	b.f.Tags = append(b.f.Tags, t)
	if t.Kind == formal.TagSpecFile {
		return
	}
	var prev, next *formal.Decl
	for _, d := range b.f.Decls {
		if d.NameRange.Start >= at {
			next = d
			break
		}
		prev = d
	}
	ix := b.f.Index()
	if prev != nil && ix.Line(prev.NameRange.Start) == ix.Line(at) {
		t.Decl = prev
		return
	}
	if !lineStart(b.src, at) {
		k := b.lastLineStart(at)
		inList := prev != nil && (prev.Kind == formal.DeclVariable || prev.Kind == formal.DeclConstant)
		if prev != nil && !inList && (k == nil || k.r.Start <= prev.Range.Start) {
			t.Decl = prev
			return
		}
		if k != nil && theoremWords[b.text(k)] {
			return
		}
	}
	if next == nil {
		return
	}
	limit := min(next.Range.Start, next.NameRange.Start)
	for i := range b.toks {
		k := &b.toks[i]
		if k.r.Start <= at || k.r.Start >= limit || k.isComment() {
			continue
		}
		if w := b.text(k); w == "EXTENDS" || w == "INSTANCE" || theoremWords[w] {
			return
		}
	}
	t.Decl = next
}

// lastLineStart returns the last token before at that opens its line and is
// not a comment, or nil.
func (b *binder) lastLineStart(at int) *token {
	var out *token
	for i := range b.toks {
		k := &b.toks[i]
		if k.r.Start >= at {
			break
		}
		if k.lineStart && !k.isComment() {
			out = k
		}
	}
	return out
}

func (b *binder) text(k *token) string {
	return string(b.src[k.r.Start:k.r.End])
}
