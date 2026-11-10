// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

const formalSpecText = `# Queue

**$Queue.One (one turn).** There is exactly one active turn per session.

**$Queue.Unmodelled.** Nothing checks this.

See $Queue.One and §4.2.

## 4.2 Mutex

The mutex is released by a deferred unlock.

## 4.3 Cap

A global concurrency cap applies.
`

const formalModelText = `---- MODULE M ----
\* Spec-file: q
VARIABLES
    \* Derived-from: $Queue.One
    state
\* Spec: $Queue.One "exactly one active turn"
\* Spec: §4.2 "deferred unlock"
Inv_One == TRUE

Claim ==
    \* Spec: §4.3
    TRUE

Plain == 1
\* Spec: $Queue.Nope
Dangling == 1
\* Spec: rc-host §10
Malformed == 1
Use == Inv_One
====
`

// formalFixture is an open project with one spec and one model.
type formalFixture struct {
	c           *client
	dir         string
	spec, model uri.URI
}

func newFormalFixture(t *testing.T, o clientOptions) formalFixture {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(text), 0o644))
	}
	write("spec.toml", "formal = [\"tla/*.tla\"]\n")
	write("q.spec.md", formalSpecText)
	write("tla/M.tla", formalModelText)
	write("tla/M_Mut1_Bad.tla", "---- MODULE M_Mut1_Bad ----\n\\* Spec: $Queue.One\nInv_Mut == FALSE\n====\n")
	c := newClient(t, o)
	return formalFixture{
		c: c, dir: dir,
		spec:  c.open(filepath.Join(dir, "q.spec.md")),
		model: c.open(filepath.Join(dir, "tla", "M.tla")),
	}
}

// lineOf returns the zero-based line holding needle and the column of its
// first byte (fixture text is ASCII before the needle unless stated).
func lineOf(t *testing.T, text, needle string) (uint32, uint32) {
	t.Helper()
	i := strings.Index(text, needle)
	require.GreaterOrEqual(t, i, 0, needle)
	line := strings.Count(text[:i], "\n")
	col := i - (strings.LastIndexByte(text[:i], '\n') + 1)
	return uint32(line), uint32(utf16Len(text[i-col : i]))
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}

func (f formalFixture) hover(t *testing.T, u uri.URI, line, char uint32) string {
	t.Helper()
	raw := f.c.callRaw("textDocument/hover", &protocol.HoverParams{TextDocumentPositionParams: at(u, line, char)})
	if string(raw) == "null" {
		return ""
	}
	var h protocol.Hover
	require.NoError(t, protocol.Unmarshal(raw, &h))
	mc, ok := h.Contents.(*protocol.MarkupContent)
	require.True(t, ok)
	return mc.Value
}

func (f formalFixture) locations(t *testing.T, method string, params any) []protocol.Location {
	t.Helper()
	raw := f.c.callRaw(method, params)
	if string(raw) == "null" {
		return nil
	}
	var out []protocol.Location
	require.NoError(t, protocol.Unmarshal(raw, &out), string(raw))
	return out
}

func (f formalFixture) definition(t *testing.T, u uri.URI, line, char uint32) []protocol.Location {
	t.Helper()
	return f.locations(t, "textDocument/definition", &protocol.DefinitionParams{TextDocumentPositionParams: at(u, line, char)})
}

func (f formalFixture) implementation(t *testing.T, u uri.URI, line, char uint32) []protocol.Location {
	t.Helper()
	return f.locations(t, "textDocument/implementation", &protocol.ImplementationParams{TextDocumentPositionParams: at(u, line, char)})
}

func (f formalFixture) references(t *testing.T, u uri.URI, line, char uint32) []protocol.Location {
	t.Helper()
	return f.locations(t, "textDocument/references", &protocol.ReferenceParams{
		TextDocumentPositionParams: at(u, line, char), Context: protocol.ReferenceContext{IncludeDeclaration: true},
	})
}

// where renders locations as "file:line" (1-based), relative to the project.
func (f formalFixture) where(locs []protocol.Location) []string {
	var out []string
	for _, l := range locs {
		abs, _ := pathOf(l.URI)
		rel, _ := filepath.Rel(f.dir, abs)
		out = append(out, filepath.ToSlash(rel)+":"+itoa(int(l.Range.Start.Line)+1))
	}
	return out
}

func TestFormalCapability(t *testing.T) {
	res := New(strings.NewReader(""), &strings.Builder{}, Options{}).initialize(&protocol.InitializeParams{})
	assert.Equal(t, protocol.Boolean(true), res.Capabilities.ImplementationProvider)
}

// $Name, model to spec: hover and definition on the tag.
func TestFormalTagToSpecAnchor(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	line, char := lineOf(t, formalModelText, `$Queue.One "exactly`)

	md := f.hover(t, f.model, line, char+2)
	assert.Contains(t, md, "*spec* · Spec tag", "labelled, to tell it from a plugin's")
	assert.Contains(t, md, "`$Queue.One` (one turn)")
	assert.Contains(t, md, "exactly one active turn per session")
	assert.NotContains(t, md, "not in this text")

	specLine, _ := lineOf(t, formalSpecText, "**$Queue.One")
	assert.Equal(t, []string{"q.spec.md:" + itoa(int(specLine)+1)}, f.where(f.definition(t, f.model, line, char+2)))
	assert.Equal(t, []string{"q.spec.md:" + itoa(int(specLine)+1)}, f.where(f.definition(t, f.model, line, 0)),
		"anywhere on the tag's line")
}

// §, model to spec, with the Spec-file tag in play.
func TestFormalTagToSpecSection(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	line, _ := lineOf(t, formalModelText, `\* Spec: §4.2`)

	md := f.hover(t, f.model, line, 10)
	assert.Contains(t, md, "**§4.2** Mutex")
	assert.Contains(t, md, "released by a deferred unlock")

	specLine, _ := lineOf(t, formalSpecText, "## 4.2")
	assert.Equal(t, []string{"q.spec.md:" + itoa(int(specLine)+1)}, f.where(f.definition(t, f.model, line, 10)))
}

// Hover and definition on a tagged declaration, Derived-from dimmed.
func TestFormalDeclaration(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})

	line, char := lineOf(t, formalModelText, "Inv_One ==")
	md := f.hover(t, f.model, line, char+1)
	assert.Contains(t, md, "`Inv_One` cites")
	assert.Contains(t, md, "`$Queue.One`")
	assert.Contains(t, md, "**§4.2** Mutex")
	assert.Len(t, f.definition(t, f.model, line, char+1), 2)

	line, char = lineOf(t, formalModelText, "    state")
	md = f.hover(t, f.model, line, char+5)
	assert.Contains(t, md, "*Derived-from `$Queue.One`")
	assert.NotContains(t, md, "exactly one active turn", "a Derived-from is one dim line, not the full text")

	line, char = lineOf(t, formalModelText, "Claim ==")
	assert.Contains(t, f.hover(t, f.model, line, char), "**§4.3** Cap", "a tag in the body binds to the definition")
}

// The narrow rule: nothing elsewhere, so a TLA+ plugin keeps its job.
func TestFormalStaysQuietElsewhere(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	for _, needle := range []string{"Plain ==", "Use ==", "Inv_One\n====", "---- MODULE", "TRUE\n\nClaim"} {
		line, char := lineOf(t, formalModelText, needle)
		assert.Empty(t, f.hover(t, f.model, line, char), needle)
		assert.Nil(t, f.definition(t, f.model, line, char), needle)
		assert.Nil(t, f.references(t, f.model, line, char), needle)
		assert.Nil(t, f.implementation(t, f.model, line, char), needle)
	}
	assert.JSONEq(t, `{"data":[]}`, string(f.c.callRaw("textDocument/semanticTokens/full",
		&protocol.SemanticTokensParams{TextDocument: protocol.TextDocumentIdentifier{URI: f.model}})))
	assert.Equal(t, "null", string(f.c.callRaw("textDocument/documentSymbol",
		&protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: f.model}})))
}

// $Name, spec to model: hover names the model, implementation jumps there.
func TestFormalSpecAnchorToModel(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	line, char := lineOf(t, formalSpecText, "See $Queue.One")
	md := f.hover(t, f.spec, line, char+6)
	assert.Contains(t, md, "Model: `tla/M.tla:Inv_One`")
	assert.Contains(t, md, "*Derived-from in `tla/M.tla:state`*")
	assert.Contains(t, md, "Model (mutant): `tla/M_Mut1_Bad.tla:Inv_Mut`")
	assert.Less(t, strings.Index(md, "Model: "), strings.Index(md, "Derived-from"))
	assert.Less(t, strings.Index(md, "Derived-from"), strings.Index(md, "mutant"), "mutants last")

	inv, _ := lineOf(t, formalModelText, "Inv_One ==")
	state, _ := lineOf(t, formalModelText, "    state")
	got := f.where(f.implementation(t, f.spec, line, char+6))
	assert.Equal(t, []string{
		"tla/M.tla:" + itoa(int(inv)+1), "tla/M.tla:" + itoa(int(state)+1), "tla/M_Mut1_Bad.tla:3",
	}, got)

	// The label's own name answers the same.
	line, char = lineOf(t, formalSpecText, "$Queue.One (one")
	assert.Len(t, f.implementation(t, f.spec, line, char+1), 3)
	assert.Contains(t, f.hover(t, f.spec, line, char+1), "Model: `tla/M.tla:Inv_One`")

	// An anchor no model cites has no Model line and no jump.
	line, char = lineOf(t, formalSpecText, "$Queue.Unmodelled")
	assert.NotContains(t, f.hover(t, f.spec, line, char+1), "Model")
	assert.Nil(t, f.implementation(t, f.spec, line, char+1))
}

// §, spec to model: from a reference and from the heading's number.
func TestFormalSpecSectionToModel(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	inv, _ := lineOf(t, formalModelText, "Inv_One ==")
	want := []string{"tla/M.tla:" + itoa(int(inv)+1)}

	line, _ := lineOf(t, formalSpecText, "See $Queue.One and §4.2")
	char := uint32(utf16Len("See $Queue.One and §4"))
	assert.Contains(t, f.hover(t, f.spec, line, char), "Model: `tla/M.tla:Inv_One`")
	assert.Equal(t, want, f.where(f.implementation(t, f.spec, line, char)))

	line, _ = lineOf(t, formalSpecText, "## 4.2")
	assert.Contains(t, f.hover(t, f.spec, line, 4), "Model: `tla/M.tla:Inv_One`")
	assert.Equal(t, want, f.where(f.implementation(t, f.spec, line, 4)))

	claim, _ := lineOf(t, formalModelText, "Claim ==")
	line, _ = lineOf(t, formalSpecText, "## 4.3")
	assert.Equal(t, []string{"tla/M.tla:" + itoa(int(claim)+1)}, f.where(f.implementation(t, f.spec, line, 3)),
		"a tag in a body jumps to its definition")
}

// References include tags, from either side.
func TestFormalReferences(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	tagLine, tagChar := lineOf(t, formalModelText, `$Queue.One "exactly`)
	line, char := lineOf(t, formalSpecText, "See $Queue.One")
	fromSpec := f.where(f.references(t, f.spec, line, char+6))
	assert.Contains(t, fromSpec, "tla/M.tla:"+itoa(int(tagLine)+1))
	assert.Contains(t, fromSpec, "tla/M_Mut1_Bad.tla:2")
	assert.Contains(t, fromSpec, "q.spec.md:"+itoa(int(line)+1))
	assert.Equal(t, fromSpec, f.where(f.references(t, f.model, tagLine, tagChar)), "the same list from the tag")

	secLine, _ := lineOf(t, formalSpecText, "## 4.2")
	refs := f.where(f.references(t, f.spec, secLine, 4))
	s42, _ := lineOf(t, formalModelText, `\* Spec: §4.2`)
	assert.Contains(t, refs, "tla/M.tla:"+itoa(int(s42)+1))
	assert.Equal(t, refs, f.where(f.references(t, f.model, s42, 10)))

	// Highlights stay within the file.
	raw := f.c.callRaw("textDocument/documentHighlight", &protocol.DocumentHighlightParams{TextDocumentPositionParams: at(f.spec, line, char+6)})
	var hs []protocol.DocumentHighlight
	require.NoError(t, protocol.Unmarshal(raw, &hs))
	assert.Len(t, hs, 2)
}

func TestFormalUnresolvedTags(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	line, char := lineOf(t, formalModelText, "$Queue.Nope")
	assert.Contains(t, f.hover(t, f.model, line, char), "does not resolve")
	assert.Nil(t, f.definition(t, f.model, line, char))

	line, char = lineOf(t, formalModelText, "rc-host")
	md := f.hover(t, f.model, line, char)
	assert.Contains(t, md, "rc-host")
	assert.Contains(t, md, "not a reference")
	assert.Nil(t, f.definition(t, f.model, line, char))
	refs := f.references(t, f.model, line, char)
	assert.Empty(t, refs)
}

func TestFormalSpecFileTag(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	line, _ := lineOf(t, formalModelText, `\* Spec-file: q`)
	assert.Contains(t, f.hover(t, f.model, line, 15), "`q.spec.md`")
	assert.Equal(t, []string{"q.spec.md:1"}, f.where(f.definition(t, f.model, line, 15)))
}

func TestFormalDiagnostics(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	f.c.sync()
	byCode := map[string]protocol.Diagnostic{}
	for _, d := range f.c.diagnostics(f.model) {
		code, _ := d.Code.(protocol.String)
		byCode[string(code)] = d
	}
	require.Contains(t, byCode, "formal-ref-dangling")
	assert.Equal(t, protocol.DiagnosticSeverityError, byCode["formal-ref-dangling"].Severity)
	line, char := lineOf(t, formalModelText, "$Queue.Nope")
	assert.Equal(t, lspRange(line, char, char+uint32(len("$Queue.Nope"))), byCode["formal-ref-dangling"].Range)
	require.Contains(t, byCode, "formal-tag-malformed")
	assert.Equal(t, protocol.DiagnosticSeverityHint, byCode["formal-tag-malformed"].Severity)
	assert.NotContains(t, byCode, "formal-op-uncited")
	assert.NotContains(t, byCode, "formal-quote-stale")
}

// Edits re-index at once, from either side, with diagnostics to match.
func TestFormalLiveReindex(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})

	// A new tag in the buffer shows from the spec before any save.
	line, char := lineOf(t, formalSpecText, "$Queue.Unmodelled")
	assert.Nil(t, f.implementation(t, f.spec, line, char+1))
	edited := strings.Replace(formalModelText, "Plain == 1", "\\* Spec: $Queue.Unmodelled\nPlain == 1", 1)
	f.c.change(f.model, 2, edited)
	assert.Contains(t, f.hover(t, f.spec, line, char+1), "Model: `tla/M.tla:Plain`")
	assert.Len(t, f.implementation(t, f.spec, line, char+1), 1)

	// Defining the dangling name in the spec clears the model's error.
	code := func() []string {
		var out []string
		for _, d := range f.c.diagnostics(f.model) {
			c, _ := d.Code.(protocol.String)
			out = append(out, string(c))
		}
		return out
	}
	f.c.sync()
	assert.Contains(t, code(), "formal-ref-dangling")
	f.c.change(f.spec, 2, formalSpecText+"\n**$Queue.Nope.** Defined now.\n")
	f.c.sync()
	assert.NotContains(t, code(), "formal-ref-dangling")

	// Closing the buffer falls back to the disk's model.
	f.c.close(f.model)
	f.c.sync()
	assert.NotContains(t, f.hover(t, f.spec, line, char+1), "Model")
}

func completionLabels(t *testing.T, f formalFixture, u uri.URI, line, char uint32) []string {
	t.Helper()
	raw := f.c.callRaw("textDocument/completion", &protocol.CompletionParams{TextDocumentPositionParams: at(u, line, char)})
	if string(raw) == "null" {
		return nil
	}
	var list protocol.CompletionList
	require.NoError(t, protocol.Unmarshal(raw, &list))
	out := []string{}
	for _, it := range list.Items {
		out = append(out, it.Label)
	}
	return out
}

// A syntax error in an edited model is published as formal-syntax, its
// range in UTF-16 after a supplementary character, and goes when fixed.
func TestFormalSyntaxDiagnostics(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	broken := strings.Replace(formalModelText, "Plain == 1", "Plain == \"😀\" ` 1", 1)
	f.c.change(f.model, 2, broken)
	f.c.sync()
	var got []protocol.Diagnostic
	for _, d := range f.c.diagnostics(f.model) {
		if c, _ := d.Code.(protocol.String); c == "formal-syntax" {
			got = append(got, d)
		}
	}
	require.Len(t, got, 1)
	assert.Equal(t, protocol.DiagnosticSeverityError, got[0].Severity)
	line, _ := lineOf(t, broken, "Plain ==")
	char := uint32(utf16Len("Plain == \"😀\" "))
	assert.Equal(t, lspRange(line, char, char+1), got[0].Range)
	assert.Contains(t, got[0].Message, "Lexical error")

	f.c.change(f.model, 3, formalModelText)
	f.c.sync()
	for _, d := range f.c.diagnostics(f.model) {
		c, _ := d.Code.(protocol.String)
		assert.NotEqual(t, protocol.String("formal-syntax"), c)
	}
}

func TestFormalCompletion(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	text := strings.Replace(formalModelText, "Plain == 1", "\\* Spec: $Qu\n\\* Derived-from: §4\n\\* Spec: §4.2 \"$Qu\n\\* See $Qu\n(* Spec: $Qu *)\nPlain == \"\\\\* Spec: $Qu\"", 1)
	f.c.change(f.model, 2, text)

	line, _ := lineOf(t, text, `\* Spec: $Qu`)
	assert.Equal(t, []string{"$Queue.One", "$Queue.Unmodelled"}, completionLabels(t, f, f.model, line, 12))

	line, _ = lineOf(t, text, `\* Derived-from: §4`)
	assert.Equal(t, []string{"§4.2", "§4.3"}, completionLabels(t, f, f.model, line, uint32(utf16Len(`\* Derived-from: §4`))))

	for _, needle := range []string{`\* Spec: §4.2 "$Qu`, `\* See $Qu`, `(* Spec: $Qu`, `Plain == "\\* Spec: $Qu`} {
		line, _ = lineOf(t, text, needle)
		assert.Nil(t, completionLabels(t, f, f.model, line, uint32(utf16Len(needle))), needle)
	}
}

// A section number two spec files share completes qualified, unless a
// Spec-file tag settles it. The stem starts with a digit on purpose.
func TestFormalCompletionQualifies(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tla"), 0o755))
	put := func(rel, text string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(text), 0o644))
	}
	put("spec.toml", "files = [\"*.md\"]\nformal = [\"tla/*.tla\"]\n")
	put("04-q.md", "# Q\n\n## 4.2 Mutex\n\nA.\n")
	put("05-r.md", "# R\n\n## 4.2 Other\n\nB.\n")
	put("tla/M.tla", "---- MODULE M ----\n\\* Spec: §\nA == 1\n====\n")
	c := newClient(t, clientOptions{})
	u := c.open(filepath.Join(dir, "tla", "M.tla"))
	f := formalFixture{c: c, dir: dir}
	char := uint32(utf16Len(`\* Spec: §`))
	assert.Equal(t, []string{"§04-q:4.2", "§05-r:4.2"}, completionLabels(t, f, u, 1, char))

	c.change(u, 2, "---- MODULE M ----\n\\* Spec-file: 04-q\n\\* Spec: §\nA == 1\n====\n")
	assert.Equal(t, []string{"§4.2", "§05-r:4.2"}, completionLabels(t, f, u, 2, char))

	// The qualified forms it offers resolve.
	c.change(u, 3, "---- MODULE M ----\n\\* Spec: §04-q:4.2 \"A.\"\nA == 1\n====\n")
	c.sync()
	assert.Empty(t, c.diagnostics(u))
	assert.Equal(t, []string{"04-q.md:3"}, f.where(f.definition(t, u, 1, 12)))
}

// Rename from the spec reaches the tags, through the client's edit.
func TestFormalRename(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	line, char := lineOf(t, formalSpecText, "See $Queue.One")
	raw := f.c.callRaw("textDocument/rename", &protocol.RenameParams{
		TextDocumentPositionParams: at(f.spec, line, char+6), NewName: "Queue.Single",
	})
	var we protocol.WorkspaceEdit
	require.NoError(t, protocol.Unmarshal(raw, &we))
	edits := we.Changes[f.model]
	require.Len(t, edits, 2, "the Derived-from and the Spec tag")
	tagLine, tagChar := lineOf(t, formalModelText, `$Queue.One "exactly`)
	assert.Contains(t, []protocol.Range{edits[0].Range, edits[1].Range}, lspRange(tagLine, tagChar+1, tagChar+uint32(len("$Queue.One"))))
	assert.Equal(t, "Queue.Single", edits[0].NewText)
	mut := fileURIOf(t, filepath.Join(f.dir, "tla", "M_Mut1_Bad.tla"))
	assert.Len(t, we.Changes[mut], 1, "a model that is not open too")
}

// A .tla file the formal key does not select gets nothing.
func TestFormalUnselectedModel(t *testing.T) {
	f := newFormalFixture(t, clientOptions{})
	p := filepath.Join(f.dir, "other", "N.tla")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("---- MODULE N ----\n\\* Spec: $Queue.One\nA == 1\n====\n"), 0o644))
	u := f.c.open(p)
	assert.Empty(t, f.hover(t, u, 1, 10))
	assert.Nil(t, f.definition(t, u, 1, 10))
	f.c.sync()
	assert.Empty(t, f.c.diagnostics(u))
}

// Positions in UTF-16 and UTF-8 both land on the tag after a non-ASCII byte.
func TestFormalPositionEncodings(t *testing.T) {
	for _, utf8 := range []bool{false, true} {
		f := newFormalFixture(t, clientOptions{utf8: utf8})
		line, _ := lineOf(t, formalModelText, `\* Spec: §4.2`)
		// Just inside "4.2", past the two-byte sign.
		char := uint32(len(`\* Spec: `)) + 1
		if utf8 {
			char++
		}
		assert.Len(t, f.definition(t, f.model, line, char), 1)
	}
}

// Each analysis reloads the project, but a model whose text did not change
// keeps its scan: an edit to the spec parses no model, and an edit to a
// model parses that model only.
func TestAnalysisReusesUnchangedModelScans(t *testing.T) {
	dir := t.TempDir()
	for rel, text := range map[string]string{
		"spec.toml": "formal = [\"tla/*.tla\"]\n", "q.spec.md": formalSpecText,
		"tla/M.tla": formalModelText, "tla/N.tla": "---- MODULE N ----\nB == 1\n====\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(text), 0o644))
	}
	srv := New(strings.NewReader(""), io.Discard, Options{})
	srv.syncBaseline = true
	srv.initialize(&protocol.InitializeParams{})
	open := func(rel, text string) uri.URI {
		u, err := fileURI(filepath.Join(dir, filepath.FromSlash(rel)))
		require.NoError(t, err)
		srv.didOpen(&protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: u, Version: 1, Text: text}})
		return u
	}
	spec := open("q.spec.md", formalSpecText)
	model := open("tla/M.tla", formalModelText)
	p := srv.docs[filepath.Join(dir, "q.spec.md")].proj
	require.NotNil(t, p)
	t.Cleanup(func() {
		p.stopTimer()
		close(srv.done)
	})
	srv.flush(p)
	m0, n0 := p.snap.formal.File("tla/M.tla"), p.snap.formal.File("tla/N.tla")
	require.NotNil(t, m0)
	require.NotNil(t, n0)

	change(srv, spec, 2, formalSpecText+"\nMore prose.\n")
	srv.flush(p)
	assert.Same(t, m0, p.snap.formal.File("tla/M.tla"), "a spec edit parses no model")
	assert.Same(t, n0, p.snap.formal.File("tla/N.tla"))

	change(srv, model, 2, formalModelText+"\\* trailing\n")
	srv.flush(p)
	assert.NotSame(t, m0, p.snap.formal.File("tla/M.tla"), "the edited model is parsed again")
	assert.Same(t, n0, p.snap.formal.File("tla/N.tla"), "the other is not")
}
