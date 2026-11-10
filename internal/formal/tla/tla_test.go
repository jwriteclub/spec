// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tla

import (
	"strings"
	"testing"

	"github.com/jwriteclub/prudence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/formal"
)

// wrap puts a body in a module header and footer.
func wrap(body string) string {
	return "---- MODULE M ----\n" + body + "\n====\n"
}

func names(f *formal.File, kind formal.DeclKind) []string {
	var out []string
	for _, d := range f.Decls {
		if d.Kind == kind {
			out = append(out, d.Name)
		}
	}
	return out
}

func scan(body string) *formal.File {
	return Scan("tla/M.tla", []byte(wrap(body)))
}

// boundTo summarizes tags as "ref -> decl".
func boundTo(f *formal.File) []string {
	var out []string
	for _, t := range f.Tags {
		ref := string(t.Kind)
		if t.Ref != nil {
			ref = t.Ref.Written()
		}
		name := "(none)"
		if t.Decl != nil {
			name = t.Decl.Name
		}
		out = append(out, ref+" -> "+name)
	}
	return out
}

func TestModuleName(t *testing.T) {
	f := scan("A == 1")
	assert.Equal(t, "M", f.Module)
	assert.False(t, f.Mutant)
	assert.True(t, Scan("tla/M_Mut1_NoGuard.tla", nil).Mutant)
	assert.Equal(t, "Spaced", Scan("x.tla", []byte("-----   MODULE Spaced   -----\n====\n")).Module)
}

func TestTextOutsideTheModuleIsIgnored(t *testing.T) {
	src := "\\* Spec: §1 \"before\"\nPre == 1\n---- MODULE M ----\nA == 1\n====\n\\* Spec: §2\nPost == 1\n"
	f := Scan("M.tla", []byte(src))
	assert.Equal(t, []string{"A"}, names(f, formal.DeclDefinition))
	assert.Empty(t, f.Tags)
}

// SANY reads nothing before a module header, so a file without one has no
// tags and a syntax error.
func TestNoHeaderHasNothing(t *testing.T) {
	f := Scan("M.tla", []byte("\\* Spec: §1\nA == 1\n"))
	assert.Empty(t, f.Tags)
	assert.Empty(t, f.Decls)
	require.Len(t, f.Syntax, 1)
	assert.Contains(t, f.Syntax[0].Message, "MODULE")
}

func TestDefinitionHeads(t *testing.T) {
	f := scan(strings.Join([]string{
		"Plain == 1",
		"Op(a, b) == a",
		"Fn[x \\in S] == x",
		"Multi(a,",
		"      b) == a",
		"NewLine",
		"  == 1",
		"Commented (* a *) == 1",
		"Nested(f(_), g(_, _)) == 1",
		"LOCAL Hidden == 1",
		"Inv_A_1 == TRUE",
		"1a == 2",
		"Uni ≜ 1",
	}, "\n"))
	assert.Equal(t, []string{"Plain", "Op", "Fn", "Multi", "NewLine", "Commented", "Nested", "Hidden", "Inv_A_1", "1a", "Uni"},
		names(f, formal.DeclDefinition))
}

// Top-level definitions count wherever they start; LET definitions do not.
// (The old scanner took only column-0 names, so it missed Indented.)
func TestTopLevelDefinitions(t *testing.T) {
	f := scan(strings.Join([]string{
		"EXTENDS Naturals",
		"A ==",
		"    LET Inner == 1",
		"    IN Inner",
		" Indented == 2",
		"B == 1",
		"INSTANCE Other",
		"RECURSIVE Later(_)",
		"Later(n) == n",
	}, "\n"))
	assert.Equal(t, []string{"A", "Indented", "B", "Later"}, names(f, formal.DeclDefinition))
	assert.Empty(t, f.Syntax)
}

func TestModuleEndIsNotADefinition(t *testing.T) {
	f := Scan("M.tla", []byte("---- MODULE M ----\nA ==\nB\n====\nC == 1\n"))
	assert.Equal(t, []string{"A"}, names(f, formal.DeclDefinition))
}

func TestNestedComments(t *testing.T) {
	f := scan(strings.Join([]string{
		"(* outer (* inner *) still comment",
		"Hidden == 1",
		"*)",
		"Seen == 1",
		"(* \\* a line comment means nothing here *)",
		"AlsoSeen == 1",
		"(* a string \" means nothing here *)",
		"Third == 1",
	}, "\n"))
	assert.Equal(t, []string{"Seen", "AlsoSeen", "Third"}, names(f, formal.DeclDefinition))
	assert.Empty(t, f.Syntax)
}

// An unterminated comment runs to the end and is a lexical error; the
// declarations before it that prudence completed stay.
func TestUnterminatedComment(t *testing.T) {
	f := scan("Seen == 1\nAlso == 2\n(* unterminated (* nesting *)\nLost == 1")
	assert.Equal(t, []string{"Seen", "Also"}, names(f, formal.DeclDefinition))
	require.NotEmpty(t, f.Syntax)
	assert.Equal(t, prudence.CodeLexical, f.Syntax[0].Code)
}

func TestStrings(t *testing.T) {
	f := scan(strings.Join([]string{
		`A == "\\* not a comment"`,
		`B == "(* not a comment"`,
		`C == "esc \" \\* still a string"`,
		`D == "\\" \* Spec: §1 "real comment"`,
		`F == 1`,
	}, "\n"))
	assert.Equal(t, []string{"A", "B", "C", "D", "F"}, names(f, formal.DeclDefinition))
	assert.Equal(t, []string{"§1 -> D"}, boundTo(f))
	assert.Empty(t, f.Syntax)
}

func TestOperatorsAreNotComments(t *testing.T) {
	f := scan("A == x /\\* comment after a conjunction\nB == y \\/* comment after a disjunction\nC == 1")
	assert.NotEmpty(t, f.Syntax, "a * with no operand")
	assert.Len(t, f.LineComments, 0, "/\\ and \\/ take the backslash, so no \\* starts")
}

func TestBinding(t *testing.T) {
	f := scan(strings.Join([]string{
		"\\* Spec: §1",
		"\\* Spec: §2 \"stacked\"",
		"Above == 1",
		"",
		"Body ==",
		"    \\* Spec: §3",
		"    /\\ a",
		"    /\\ IF b THEN \\* Spec: §4",
		"       c ELSE d",
		"\\* Spec: §5",
		"Next == 1 \\* Spec: §6",
		"(* Comment above.",
		"   Spec: §7 \"block\"",
		"*)",
		"Blocked == 1",
		"Indented ==",
		"    (* Spec: §8 *)",
		"    TRUE",
	}, "\n"))
	assert.Equal(t, []string{
		"§1 -> Above", "§2 -> Above", "§3 -> Body", "§4 -> Body", "§5 -> Next", "§6 -> Next",
		"§7 -> Blocked", "§8 -> Indented",
	}, boundTo(f))
}

// Prudence's draft outline lists no CONSTANT declarations; withConstants
// reads them from the tokens meanwhile.
func TestBindingInConstantLists(t *testing.T) {
	f := scan(strings.Join([]string{
		"CONSTANTS",
		"    Sessions,  \\* Spec: §1",
		"    Op(_, _),",
		"    \\* Spec: §2",
		"    Cap",
		"Init == 1",
	}, "\n"))
	assert.Equal(t, []string{"Sessions", "Op", "Cap"}, names(f, formal.DeclConstant))
	assert.Equal(t, []string{"§1 -> Sessions", "§2 -> Cap"}, boundTo(f))
	assert.Equal(t, []string{"Init"}, names(f, formal.DeclDefinition))
	f = scan("CONSTANT Procs \\* Spec: §1\nVARIABLE x\nTHEOREM T == ASSUME NEW CONSTANT c PROVE TRUE")
	assert.Equal(t, []string{"Procs"}, names(f, formal.DeclConstant), "only a CONSTANT that opens a line")
	assert.Equal(t, []string{"§1 -> Procs"}, boundTo(f))
}

func TestBindingInDeclarationLists(t *testing.T) {
	f := scan(strings.Join([]string{
		"VARIABLES",
		"    (* -- state -- *)",
		"    \\* Derived-from: §3 \"x\"",
		"    state,     \\* a plain comment",
		"    \\* Spec: §4",
		"    queue, other",
		"VARIABLE lone \\* Spec: §5",
		"\\* Spec: §6",
		"Init == 1",
	}, "\n"))
	assert.Equal(t, []string{"state", "queue", "other", "lone"}, names(f, formal.DeclVariable))
	assert.Equal(t, []string{"§3 -> state", "§4 -> queue", "§5 -> lone", "§6 -> Init"}, boundTo(f))
}

func TestColumnZeroListNames(t *testing.T) {
	f := scan("VARIABLES\nx,\ny\nInit == 1")
	assert.Equal(t, []string{"x", "y"}, names(f, formal.DeclVariable))
	assert.Equal(t, []string{"Init"}, names(f, formal.DeclDefinition))
}

func TestTheorems(t *testing.T) {
	f := scan(strings.Join([]string{
		"\\* Spec: §1",
		"THEOREM Named == TRUE",
		"\\* Spec: §2",
		"THEOREM Spec => []Inv",
		"    \\* Spec: §3",
		"ASSUME Cap > 0",
		"A == 1",
	}, "\n"))
	assert.Equal(t, []string{"Named"}, names(f, formal.DeclTheorem))
	assert.Equal(t, []string{"§1 -> Named", "§2 -> (none)", "§3 -> (none)"}, boundTo(f))
}

func TestSymbolicOperators(t *testing.T) {
	f := scan(strings.Join([]string{
		"\\* Spec: §1",
		"a ++ b == a",
		"\\* Spec: §2",
		"a (+) b == a",
		"\\* Spec: §3",
		"-. a == a",
		"\\* Spec: §4",
		"Plain == 1",
	}, "\n"))
	assert.Equal(t, []string{"++", "(+)", "-."}, names(f, formal.DeclSymbolic))
	assert.Equal(t, []string{"§1 -> ++", "§2 -> (+)", "§3 -> -.", "§4 -> Plain"}, boundTo(f))
	for _, d := range f.Decls {
		assert.Equal(t, d.Name, f.Text(d.NameRange))
	}
}

func TestBlockTags(t *testing.T) {
	f := scan(strings.Join([]string{
		"(* Spec: §1 \"same line\" *)",
		"A == 1",
		"(* prose Spec: §2 is mentioned mid-line",
		"   \\* Spec: §3 is commented out",
		"   (* Spec: §4 is nested *)",
		"   Spec: §5 \"wrapped over",
		"      two lines\"",
		"   Spec: §6",
		"*)",
		"B == 1",
	}, "\n"))
	assert.Equal(t, []string{"§1 -> A", "§5 -> B", "§6 -> B"}, boundTo(f))
	for _, tag := range f.Tags {
		assert.True(t, tag.Block)
		assert.Empty(t, tag.Problem)
	}
	assert.Equal(t, "same line", f.Tags[0].Quote)
	assert.Equal(t, "wrapped over\n      two lines", f.Tags[1].Quote)
	assert.Equal(t, f.Text(f.Tags[1].QuoteRange), f.Tags[1].Quote)
}

func TestQuoteHoldingCommentEndClosesTheBlock(t *testing.T) {
	f := scan("(* Spec: §1 \"closes *) early\" *)\nA == 1")
	require.Len(t, f.Tags, 1)
	assert.Contains(t, f.Tags[0].Problem, "not closed", "the comment ended inside the quote, as in SANY")
}

func TestLineTagForms(t *testing.T) {
	f := scan(strings.Join([]string{
		`\* Spec: $Frame.Timing "decode time" trailing words`,
		`\* Derived-from: §hls:1.2`,
		`\* Spec: § 4.2 (SCR-004) "after a parenthesis"`,
		`\* Spec: §04-q:4.2.`,
		`\* Spec: §4.4 "first" / §4.8 "second"`,
		`\*Spec: §9`,
		`\*   Spec:   §10   `,
		`A == 1`,
	}, "\n"))
	require.Len(t, f.Tags, 7)
	assert.Equal(t, "Frame.Timing", f.Tags[0].Ref.Name)
	assert.Equal(t, "$Frame.Timing", f.Text(f.Tags[0].Ref.Range))
	assert.Equal(t, "Frame.Timing", f.Text(f.Tags[0].Ref.NameRange()))
	assert.Equal(t, "decode time", f.Tags[0].Quote)
	assert.Equal(t, formal.TagDerived, f.Tags[1].Kind)
	assert.Equal(t, "hls", f.Tags[1].Ref.Stem)
	assert.Equal(t, "1.2", f.Tags[1].Ref.Number)
	assert.Equal(t, "§ 4.2", f.Text(f.Tags[2].Ref.Range))
	assert.Equal(t, "after a parenthesis", f.Tags[2].Quote)
	assert.Equal(t, "04-q", f.Tags[3].Ref.Stem)
	assert.Equal(t, "4.2", f.Tags[3].Ref.Number)
	assert.Equal(t, "first", f.Tags[4].Quote)
	assert.Equal(t, "9", f.Tags[5].Ref.Number)
	assert.Equal(t, "10", f.Tags[6].Ref.Number)
	for _, tag := range f.Tags {
		assert.Empty(t, tag.Problem, f.Text(tag.Range))
		assert.True(t, strings.HasPrefix(f.Text(tag.Range), `\*`), "a line tag's range covers its marker")
	}
}

func TestNotTags(t *testing.T) {
	f := scan(strings.Join([]string{
		`\* The Spec: operator is below`,
		`\* Spec:§1`,
		`\* spec: §1`,
		`\* Specification: §1`,
		`Spec == 1`,
	}, "\n"))
	assert.Empty(t, f.Tags)
	assert.Equal(t, []string{"Spec"}, names(f, formal.DeclDefinition))
}

func TestMalformedTags(t *testing.T) {
	cases := []struct{ line, problem string }{
		{`\* Spec:`, "no reference"},
		{`\* Derived-from: model bookkeeping`, "no reference"},
		{`\* Spec: rc-host §10 INV-4`, "rc-host"},
		{`\* Spec: $frame.timing`, "not a valid anchor name"},
		{`\* Spec: $Frame_x "q"`, "not a valid anchor name"},
		{`\* Spec: §5.2ab`, "does not parse"},
		{`\* Spec: §x`, "does not parse"},
		{`\* Spec: §1 "open`, "not closed"},
		{`\* Spec-file:`, "names no spec file"},
		{`\* Spec-file: a b`, "one stem"},
	}
	for _, c := range cases {
		f := scan(c.line + "\nA == 1")
		require.Len(t, f.Tags, 1, c.line)
		assert.Contains(t, f.Tags[0].Problem, c.problem, c.line)
	}
}

func TestSpecFile(t *testing.T) {
	f := scan("\\* Spec-file: 04-queue\n\\* Spec-file: hls\nA == 1")
	require.Len(t, f.Tags, 2)
	assert.Equal(t, "04-queue", f.Tags[0].Stem)
	assert.Equal(t, "04-queue", f.Text(f.Tags[0].StemRange))
	assert.Nil(t, f.Tags[0].Decl, "a Spec-file tag belongs to the module")
	assert.Equal(t, "hls", f.Tags[1].Stem)
}

func TestUnicode(t *testing.T) {
	src := wrap("\\* Spec: §4.2 \"café → naïve\" — note\nA == 1\nB == \"é\" \\* Spec: §5 \"日本語\"")
	f := Scan("M.tla", []byte(src))
	require.Len(t, f.Tags, 2)
	assert.Equal(t, "café → naïve", f.Tags[0].Quote)
	assert.Equal(t, f.Tags[0].Quote, f.Text(f.Tags[0].QuoteRange))
	assert.Equal(t, "日本語", f.Tags[1].Quote)
	assert.Equal(t, []string{"§4.2 -> A", "§5 -> B"}, boundTo(f))
}

func TestByteOrderMarkAndCRLF(t *testing.T) {
	f := Scan("M.tla", []byte("\xEF\xBB\xBF---- MODULE M ----\r\n\\* Spec: §1\r\nA == 1\r\nB ==\r\n  \\* Spec: §2 \"q\"\r\n  1\r\n====\r\n"))
	assert.Equal(t, []string{"§1 -> A", "§2 -> B"}, boundTo(f))
	assert.Equal(t, "q", f.Tags[1].Quote)
	assert.NotContains(t, f.Text(f.Tags[1].Range), "\r")
	assert.Empty(t, f.Syntax)
}

func TestInvalidUTF8(t *testing.T) {
	f := Scan("M.tla", []byte("---- MODULE M ----\n\\* Spec: \xA7 1\nA == 1\n====\n"))
	assert.False(t, f.ValidUTF8)
	assert.Empty(t, f.Decls)
	assert.Empty(t, f.Tags)
}

// Scanning any prefix of a real module must not panic or lose ranges: an
// editor sends every keystroke.
func TestEveryPrefix(t *testing.T) {
	src := wrap("\\* Spec: $A.Bc \"quote\"\nA(x, y) == \"s\\\"\" (* c (* d *) *)\nVARIABLES a, \\* Spec: §1\n b\n(* Spec: §2 \"x\n y\" *)\nF[z \\in S] == 1")
	for i := 0; i <= len(src); i++ {
		f := Scan("M.tla", []byte(src[:i]))
		for _, tag := range f.Tags {
			assert.True(t, tag.Range.Valid(i), "prefix %d", i)
		}
		for _, d := range f.Decls {
			assert.True(t, d.NameRange.Valid(i), "prefix %d", i)
		}
	}
}

func FuzzScan(f *testing.F) {
	f.Add(wrap("\\* Spec: §1 \"q\"\nA == 1"))
	f.Add("(* (* *) \" \\ \\* VARIABLES , ( [ ---- MODULE ====")
	f.Fuzz(func(t *testing.T, src string) {
		out := Scan("M.tla", []byte(src))
		for _, tag := range out.Tags {
			if !tag.Range.Valid(len(src)) {
				t.Fatalf("tag range %v outside %d bytes", tag.Range, len(src))
			}
		}
	})
}

// A tag above EXTENDS or INSTANCE is about the module; RECURSIVE announces the
// definition below it, so a tag there still reaches that definition.
func TestModuleLevelTagsBindToNothing(t *testing.T) {
	assert.Equal(t, []string{"§1 -> (none)"}, boundTo(scan("\\* Spec: §1\nEXTENDS Naturals\nVARIABLE x")))
	assert.Equal(t, []string{"§1 -> (none)", "§2 -> (none)"},
		boundTo(scan("(*\n Spec: §1\n Spec: §2\n*)\nEXTENDS Naturals\nVARIABLE x")))
	assert.Equal(t, []string{"§1 -> (none)"}, boundTo(scan("\\* Spec: §1\nLOCAL INSTANCE Foo\nBar == 1")))
	assert.Equal(t, []string{"§1 -> Foo"}, boundTo(scan("\\* Spec: §1\nRECURSIVE Foo(_)\nFoo(x) == 1")))
}

// Comments between a definition's name and its == are read, not skipped.
func TestTagsInADefinitionHead(t *testing.T) {
	for _, body := range []string{
		"Foo \\* Spec: §1\n == 1",
		"Foo (* Spec: §1 *) == 1",
		"Foo(a, \\* Spec: §1\n    b) == 1",
		"Foo[a \\in S, \\* Spec: §1\n    b \\in S] == 1",
		"Foo(a, \\* Spec: §1\nb) == 1",
		"THEOREM Foo \\* Spec: §1\n == TRUE",
	} {
		f := scan(body)
		assert.Equal(t, []string{"§1 -> Foo"}, boundTo(f), body)
		require.Len(t, f.Decls, 1, body)
		assert.Equal(t, "Foo", f.Decls[0].Name, body)
	}
	assert.Equal(t, []string{"§1 -> Bar"}, boundTo(scan("Foo\n==\n1\n\\* Spec: §1\nBar == 2")), "a == in column 0 still ends the head")
}

// A backslash at the end of an unclosed string does not hide the next line's
// comment, though the string is a syntax error.
func TestStringEscapeStopsAtTheLineEnd(t *testing.T) {
	f := scan("Foo == \"abc\\\n\\* Spec: §1\nBar == 2")
	require.Len(t, f.Tags, 1)
	assert.Equal(t, 3, f.Index().Line(f.Tags[0].Range.Start), "the line after the string")
	assert.NotEmpty(t, f.Syntax)
}

// Set difference is \, so in S \\* a comment follows the operator, as in
// SANY's lexer; there is no \\ operator.
func TestSetDifferenceBeforeAComment(t *testing.T) {
	assert.Equal(t, []string{"§1 -> Foo"}, boundTo(scan("Foo == S \\ T \\* Spec: §1")))
	assert.Len(t, scan("Foo == S \\\\* Spec: §1").Tags, 1)
}
