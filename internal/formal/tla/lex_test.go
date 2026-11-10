// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tla

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jwriteclub/prudence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
)

var kindNames = map[formal.TokenKind]string{
	formal.TokenKeyword: "kw", formal.TokenOperator: "op", formal.TokenString: "str", formal.TokenNumber: "num",
	formal.TokenComment: "com", formal.TokenDefinition: "def",
}

// lexed writes src's tokens as "kind:text".
func lexed(src string) []string {
	var out []string
	for _, t := range Lex([]byte(src)) {
		out = append(out, fmt.Sprintf("%s:%s", kindNames[t.Kind], src[t.Range.Start:t.Range.End]))
	}
	return out
}

// body lexes a line in a module, dropping the header and footer tokens.
func body(line string) []string {
	out := lexed("---- MODULE M ----\n" + line + "\n====\n")
	return out[4 : len(out)-1]
}

func TestLexHeaderAndFooter(t *testing.T) {
	assert.Equal(t, []string{"op:----", "kw:MODULE", "def:M", "op:----", "op:===="}, lexed("---- MODULE M ----\n====\n"))
}

func TestLexOutsideTheModuleIsComment(t *testing.T) {
	assert.Equal(t,
		[]string{"com:before == 1\n", "op:----", "kw:MODULE", "def:M", "op:----", "op:====", "com:\nafter == \"2\"\n"},
		lexed("before == 1\n---- MODULE M ----\n====\nafter == \"2\"\n"))
}

func TestLexNestedModule(t *testing.T) {
	src := "---- MODULE A ----\n---- MODULE B ----\nIF\n====\nTHEN\n====\nELSE\n"
	assert.Equal(t, []string{
		"op:----", "kw:MODULE", "def:A", "op:----", "op:----", "kw:MODULE", "op:----", "kw:IF", "op:====",
		"kw:THEN", "op:====", "com:\nELSE\n",
	}, lexed(src))
}

// SANY reads nothing before a module header.
func TestLexNoHeaderIsNothing(t *testing.T) {
	assert.Empty(t, lexed("EXTENDS Naturals ==\n"))
}

// The kind mapping: prudence's keywords and proof keywords are keywords, as
// are the operators spelled in capitals and the built-in constants; its
// operators and delimiters are operators; identifiers are plain.
func TestLexClasses(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{`Inv == x`, []string{"op:=="}},
		{`IF a THEN b ELSE c`, []string{"kw:IF", "kw:THEN", "kw:ELSE"}},
		{`/\ x = 1`, []string{`op:/\`, "op:=", "num:1"}},
		{`x \in S`, []string{`op:\in`}},
		{`\A s \in S : TRUE`, []string{`kw:\A`, `op:\in`, "op::", "kw:TRUE"}},
		{`a \o b`, []string{`op:\o`}},
		{`\b101 + \o17 + \h1F`, []string{`num:\b101`, "op:+", `num:\o17`, "op:+", `num:\h1F`}},
		{`1..5`, []string{"num:1", "op:..", "num:5"}},
		{`"a b"`, []string{`str:"a b"`}},
		{`"a\"b" c`, []string{`str:"a\"b"`}},
		{`WF_vars(Next)`, []string{"kw:WF_", "op:(", "op:)"}},
		{`[][Next]_vars`, []string{"op:[]", "op:[", "op:]_"}},
		{`<<a, b>>`, []string{"op:<<", "op:,", "op:>>"}},
		{`Next' = x'`, []string{"op:'", "op:=", "op:'"}},
		{"x ≜ 1", []string{"op:≜", "num:1"}},
		{`UNCHANGED x /\ ENABLED y`, []string{"kw:UNCHANGED", `op:/\`, "kw:ENABLED"}},
		{`THEOREM BOOLEAN`, []string{"kw:THEOREM", "kw:BOOLEAN"}},
		{`IFF`, nil},
		{`Inv_IF`, nil},
	} {
		got := body(tc.line)
		if len(got) == 0 {
			got = nil
		}
		assert.Equal(t, tc.want, got, tc.line)
	}
}

func TestLexComments(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{`\* IF "x"`, []string{`com:\* IF "x"`}},
		{`(* IF *) THEN`, []string{"com:(* IF *)", "kw:THEN"}},
		{`(* a (* b *) c *) IF`, []string{"com:(* a (* b *) c *)", "kw:IF"}},
		{"(* a\nb *) IF", []string{"com:(* a\nb *)", "kw:IF"}},
		// \* and strings mean nothing inside (* *), so this closes.
		{`(* \* *) IF`, []string{`com:(* \* *)`, "kw:IF"}},
		{`(* " *) IF "x"`, []string{`com:(* " *)`, "kw:IF", `str:"x"`}},
		// And (* means nothing inside \* or a string.
		{`\* (* IF`, []string{`com:\* (* IF`}},
		{`"(*" IF`, []string{`str:"(*"`, "kw:IF"}},
		// A * after a conjunction opens no comment.
		{`a /\* b`, []string{`op:/\`, "op:*"}},
		// Unterminated: to the end of the file.
		{`(* a (* b *) c`, []string{"com:(* a (* b *) c\n====\n"}},
	} {
		got := lexed("---- MODULE M ----\n" + tc.line + "\n====\n")[4:]
		if want := tc.want[len(tc.want)-1]; len(want) < 5 || want[len(want)-5:] != "====\n" {
			got = got[:len(got)-1]
		}
		assert.Equal(t, tc.want, got, tc.line)
	}
}

func TestLexKeepsCRLFOutOfALineComment(t *testing.T) {
	assert.Equal(t, []string{`com:\* x`, "kw:IF"}, body("\\* x\r\nIF"))
}

func TestLexByteOrderMark(t *testing.T) {
	assert.Equal(t, []string{"op:----", "kw:MODULE", "def:M", "op:----"}, lexed(string(utf8BOM)+"---- MODULE M ----\n"))
}

// Prudence lexes any bytes; so does Lex, though Scan reads no tags in them.
func TestLexInvalidUTF8(t *testing.T) {
	assert.Equal(t, []string{"op:----", "kw:MODULE", "def:M", "op:----", "op:===="}, lexed("---- MODULE M ----\n\xff\n====\n"))
}

// When prudence fails to run, the model is drawn as plain text and scanned
// as having no declarations, with the failure as its problem.
func TestFallbackOnOperationalError(t *testing.T) {
	oldTokenize, oldParse := tokenize, parse
	t.Cleanup(func() { tokenize, parse = oldTokenize, oldParse })
	boom := errors.New("boom")
	tokenize = func(context.Context, string, []byte) ([]prudence.Token, []prudence.Diagnostic, error) {
		return nil, nil, boom
	}
	parse = func(context.Context, string, []byte, prudence.Options) (prudence.Result, error) {
		return prudence.Result{}, boom
	}
	src := wrap("\\* Spec: §1\nA == 1")
	assert.Nil(t, Lex([]byte(src)))
	f := Scan("M.tla", []byte(src))
	assert.Empty(t, f.Decls)
	assert.Empty(t, f.Tags)
	require.Len(t, f.Syntax, 1)
	assert.Equal(t, diag.Error, f.Syntax[0].Severity)
	assert.Contains(t, f.Syntax[0].Message, "boom")
}

// Span conversion: prudence's byte offsets are logedit's, multi-byte text
// included, and an empty span widens to the whole character at it.
func TestRangeAndProblems(t *testing.T) {
	src := []byte("é€x")
	at := func(off int) prudence.Position { return prudence.Position{ByteOffset: off} }
	r := Range(src, prudence.Span{Start: at(2), End: at(5)})
	assert.Equal(t, "€", string(src[r.Start:r.End]))
	r = Range(src, prudence.Span{Start: at(4), End: at(99)})
	assert.Equal(t, 6, r.End, "clamped to the source")
	ps := Problems(src, []prudence.Diagnostic{
		{Severity: prudence.SeverityError, Code: prudence.CodeSyntax, Message: "***Parse Error***\nEncountered \"x\"\n\nResidual stack trace follows:\nDefinition starting at line 1.\n", Span: prudence.Span{Start: at(2), End: at(2)}},
		{Severity: prudence.SeverityWarning, Code: prudence.CodeLexical, Message: "w", Span: prudence.Span{Start: at(6), End: at(6)}},
	})
	require.Len(t, ps, 2)
	assert.Equal(t, "€", string(src[ps[0].Range.Start:ps[0].Range.End]))
	assert.Equal(t, `Encountered "x"`, ps[0].Message)
	assert.Equal(t, diag.Error, ps[0].Severity)
	assert.True(t, ps[1].Range.Empty(), "at the end there is nothing to widen to")
	assert.Equal(t, diag.Warning, ps[1].Severity)
}

// A syntax error in a module with multi-byte text lands on the right bytes.
func TestSyntaxErrorAfterUnicode(t *testing.T) {
	src := wrap("A == \"日本語\" \\* café\nB == 1 ` 2")
	f := Scan("M.tla", []byte(src))
	require.NotEmpty(t, f.Syntax)
	assert.Equal(t, "`", f.Text(f.Syntax[0].Range))
}

// TestLexAgreesWithScan: a tag the scanner reads lies in a comment token, and a
// declaration in no comment, for every prefix of a module with everything in
// it: an editor sees each of them. Tokens are ordered and do not overlap.
func TestLexAgreesWithScan(t *testing.T) {
	src := "(* pre *)\n---- MODULE M ----\nEXTENDS Naturals\n\\* Spec: $A.B \"quote\"\nInv_A == /\\ x \\in {1, 2}\n    \\* Derived-from: §1.2\n    /\\ \"s\" = \"t\"\n(* Spec: §3 \"block *) done\nVARIABLES a, \\* Spec: $C.D\n    b\nOp(x) == (* n (* m *) *) x\n====\nafter\n"
	for n := 0; n <= len(src); n++ {
		prefix := []byte(src[:n])
		toks := Lex(prefix)
		end := 0
		for _, tk := range toks {
			assert.True(t, tk.Range.Start >= end && tk.Range.End > tk.Range.Start && tk.Range.End <= n, "prefix %d: %v", n, tk)
			end = tk.Range.End
		}
		f := Scan("M.tla", prefix)
		for _, tag := range f.Tags {
			hit := false
			for _, tk := range toks {
				hit = hit || (tk.Kind == formal.TokenComment && tk.Range.Start <= tag.Range.Start && tag.Range.End <= tk.Range.End)
			}
			assert.True(t, hit, "prefix %d: tag %q is in no comment token", n, f.Text(tag.Range))
		}
		for _, d := range f.Decls {
			for _, tk := range toks {
				overlap := tk.Range.Start < d.NameRange.End && d.NameRange.Start < tk.Range.End
				assert.False(t, overlap && tk.Kind == formal.TokenComment, "prefix %d: decl %s in a comment", n, d.Name)
			}
		}
		for _, p := range f.Syntax {
			assert.True(t, p.Range.Valid(n), "prefix %d: problem %v", n, p.Range)
		}
	}
}
