// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/jwriteclub/spec/internal/pos"
)

// edgeProject copies three edge fixtures into a project of their own.
func edgeProject(t *testing.T) string {
	t.Helper()
	dir := copyFixture(t, "edge", "refs.spec.md", "sections.spec.md", "encoding-unicode.spec.md")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spec.toml"), []byte("name = \"edge\"\n"), 0o644))
	return dir
}

// TestUnicodePositions checks every position-carrying feature after 2-, 3-
// and 4-byte characters in both position encodings. In encoding-unicode,
// line 5 is "𝄞𝄞 $Enc.Uni, and §1 after 😀 §1." (𝄞 and 😀 are astral: two
// UTF-16 units, four UTF-8 bytes).
func TestUnicodePositions(t *testing.T) {
	type cols struct{ uni5, sec1a, sec1b, line4a, line4b, line4c, heading uint32 }
	for _, tc := range []struct {
		name string
		utf8 bool
		// start columns: $Enc.Uni on line 5, the two §1 on line 5, the three
		// $Enc.Uni on line 4, and the end of the "## 1. Ünïcode section"
		// heading.
		c cols
		// lengths of "$Enc.Uni" and "§1".
		nameLen, secLen uint32
	}{
		{"utf-16", false, cols{5, 19, 31, 2, 23, 45, 21}, 8, 2},
		{"utf-8", true, cols{9, 23, 38, 3, 26, 50, 23}, 8, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := edgeProject(t)
			c := newClient(t, clientOptions{utf8: tc.utf8})
			u := c.open(filepath.Join(dir, "encoding-unicode.spec.md"))
			n := tc.nameLen

			var h protocol.Hover
			c.call("textDocument/hover", &protocol.HoverParams{TextDocumentPositionParams: at(u, 5, tc.c.uni5+1)}, &h)
			assert.Equal(t, lspRange(5, tc.c.uni5, tc.c.uni5+n), *h.Range)
			// A position between the two halves of 😀's surrogate pair (or
			// inside its UTF-8 bytes) is not on any token.
			assert.Equal(t, "null", string(c.callRaw("textDocument/hover", &protocol.HoverParams{TextDocumentPositionParams: at(u, 5, tc.c.sec1b-2)})))

			var refs []protocol.Location
			c.call("textDocument/references", &protocol.ReferenceParams{TextDocumentPositionParams: at(u, 5, tc.c.uni5),
				Context: protocol.ReferenceContext{IncludeDeclaration: true}}, &refs)
			loc := func(line, start uint32) protocol.Location {
				return protocol.Location{URI: u, Range: lspRange(line, start, start+n)}
			}
			assert.Equal(t, []protocol.Location{loc(2, 2), loc(4, tc.c.line4a), loc(4, tc.c.line4b), loc(4, tc.c.line4c), loc(5, tc.c.uni5)}, refs)

			var defs []protocol.Location
			c.call("textDocument/definition", &protocol.DefinitionParams{TextDocumentPositionParams: at(u, 5, tc.c.sec1b+1)}, &defs)
			assert.Equal(t, []protocol.Location{{URI: u, Range: lspRange(7, 0, tc.c.heading)}}, defs, "§1 after 😀 goes to its heading")

			var hl []protocol.DocumentHighlight
			c.call("textDocument/documentHighlight", &protocol.DocumentHighlightParams{TextDocumentPositionParams: at(u, 7, 3)}, &hl)
			assert.Equal(t, []protocol.DocumentHighlight{
				{Range: lspRange(7, 3, 4), Kind: protocol.DocumentHighlightKindWrite}, // "1" of "## 1."
				{Range: lspRange(5, tc.c.sec1a, tc.c.sec1a+tc.secLen), Kind: protocol.DocumentHighlightKindRead},
				{Range: lspRange(5, tc.c.sec1b, tc.c.sec1b+tc.secLen), Kind: protocol.DocumentHighlightKindRead},
			}, hl)

			var st protocol.SemanticTokens
			c.call("textDocument/semanticTokens/full", &protocol.SemanticTokensParams{TextDocument: protocol.TextDocumentIdentifier{URI: u}}, &st)
			const v, ns, decl = tokenInvariant, tokenSection, modDeclaration
			assert.Equal(t, [][5]uint32{
				{2, 2, n, v, decl},
				{4, tc.c.line4a, n, v, 0},
				{4, tc.c.line4b, n, v, 0},
				{4, tc.c.line4c, n, v, 0},
				{5, tc.c.uni5, n, v, 0},
				{5, tc.c.sec1a, tc.secLen, ns, 0},
				{5, tc.c.sec1b, tc.secLen, ns, 0},
				{7, 3, 1, ns, decl},
				{9, 2, 10, v, decl},
			}, absoluteTokens(t, st.Data))

			var we protocol.WorkspaceEdit
			c.call("textDocument/rename", &protocol.RenameParams{TextDocumentPositionParams: at(u, 5, tc.c.uni5+3), NewName: "Enc.Wide"}, &we)
			got := we.Changes[u]
			edit := func(line, start uint32) protocol.TextEdit {
				return protocol.TextEdit{Range: lspRange(line, start+1, start+n), NewText: "Enc.Wide"}
			}
			assert.Equal(t, []protocol.TextEdit{edit(2, 2), edit(4, tc.c.line4a), edit(4, tc.c.line4b), edit(4, tc.c.line4c), edit(5, tc.c.uni5)}, got)
		})
	}
}

func TestEdgeHoverStates(t *testing.T) {
	dir := edgeProject(t)
	c := newClient(t, clientOptions{})
	refs := c.open(filepath.Join(dir, "refs.spec.md"))
	sections := c.open(filepath.Join(dir, "sections.spec.md"))
	text := func(u uri.URI, line, char uint32) string {
		var h protocol.Hover
		c.call("textDocument/hover", &protocol.HoverParams{TextDocumentPositionParams: at(u, line, char)}, &h)
		mc, ok := h.Contents.(*protocol.MarkupContent)
		require.True(t, ok, "hover at %d:%d", line, char)
		return mc.Value
	}
	assert.Equal(t, "`$Not.Defined` is not defined anywhere in the project.", text(refs, 25, 14))
	// Line 28: "Ambiguous within this file: §1 (numbered twice)."
	assert.Equal(t, "`§1` is ambiguous; it matches:\n\n- `§sections:1` Frames `sections.spec.md:3`\n- `§sections:1` Frames again `sections.spec.md:19`", text(sections, 27, 29))
	// Line 25: "... §05 versus §5 (dangling)." Six § signs precede it, each two
	// bytes but one UTF-16 unit.
	assert.Equal(t, "`§5` does not match any numbered section.", text(sections, 24, 60))
}

func TestCodeActions(t *testing.T) {
	dir := edgeProject(t)
	c := newClient(t, clientOptions{docChanges: true})
	sections := c.open(filepath.Join(dir, "sections.spec.md"))
	refs := c.open(filepath.Join(dir, "refs.spec.md"))
	actions := func(u uri.URI, r protocol.Range) []*protocol.CodeAction {
		var raw []protocol.CommandOrCodeAction
		c.call("textDocument/codeAction", &protocol.CodeActionParams{TextDocument: protocol.TextDocumentIdentifier{URI: u}, Range: r}, &raw)
		var out []*protocol.CodeAction
		for _, a := range raw {
			ca, ok := a.(*protocol.CodeAction)
			require.True(t, ok)
			out = append(out, ca)
		}
		return out
	}
	one := int32(1)
	single := func(u uri.URI, r protocol.Range, text string) *protocol.WorkspaceEdit {
		return &protocol.WorkspaceEdit{DocumentChanges: []protocol.DocumentChange{&protocol.TextDocumentEdit{
			TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: u}, Version: &one},
			Edits:        []protocol.TextDocumentEditElement{&protocol.TextEdit{Range: r, NewText: text}},
		}}}
	}

	// Line 28: "Hash forms are not references: #5.2a and #1.1; #3 is an issue number."
	got := actions(sections, lspRange(28, 33, 33))
	require.Len(t, got, 1)
	assert.Equal(t, "Write §5.2a (section Deep)", got[0].Title)
	assert.Equal(t, protocol.CodeActionKindQuickFix, *got[0].Kind)
	assert.True(t, *got[0].IsPreferred)
	require.Len(t, got[0].Diagnostics, 1)
	assert.Equal(t, protocol.String("section-hash-ref"), got[0].Diagnostics[0].Code)
	assert.Equal(t, single(sections, lspRange(28, 31, 32), "§"), got[0].Edit)

	got = actions(sections, protocol.Range{Start: lspPos(28, 0), End: lspPos(29, 0)})
	require.Len(t, got, 2, "both hash references on the line")
	assert.Equal(t, "Write §1.1 (section Timing)", got[1].Title)
	assert.Empty(t, actions(sections, lspRange(28, 49, 50)), "#3 is not a section reference")

	// Line 23: "$Frame.timing is plain text, ..."
	got = actions(refs, lspRange(23, 2, 2))
	require.Len(t, got, 1)
	assert.Equal(t, "Change to $Frame.Timing", got[0].Title)
	assert.Equal(t, single(refs, lspRange(23, 0, 13), "$Frame.Timing"), got[0].Edit)
	// Line 24: "... and so is $Frame.Timing.x." starts with a defined name: no
	// safe rewrite.
	assert.Empty(t, actions(refs, lspRange(24, 40, 40)))
}

// TestIncrementalChange checks that a range change (sent by clients that
// ignore the full-sync capability) is applied at the right bytes after an
// astral character.
func TestIncrementalChange(t *testing.T) {
	dir := edgeProject(t)
	c := newClient(t, clientOptions{})
	u := c.open(filepath.Join(dir, "encoding-unicode.spec.md"))
	// Line 5: "𝄞𝄞 $Enc.Uni, ...": replace "Uni" (UTF-16 columns 10-13) with "Gone".
	c.notify("textDocument/didChange", &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: u}, Version: 2},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{&protocol.TextDocumentContentChangePartial{
			Range: lspRange(5, 10, 13), Text: "Gone",
		}},
	})
	c.sync()
	ds := c.diagnostics(u)
	require.Len(t, ds, 1)
	assert.Equal(t, protocol.String("$Enc.Gone is not defined anywhere in the project"), ds[0].Message)
	assert.Equal(t, lspRange(5, 5, 14), ds[0].Range)
}

// utf8BOM is the UTF-8 byte order mark.
const utf8BOM = "\xef\xbb\xbf"

// TestByteOrderMarkUnopened checks positions on the first line of a file
// that starts with a byte order mark. Editors drop the mark when they load
// a file, so the server must not count it either: a rename of a file the
// editor has not opened would otherwise land one character (UTF-16) or
// three bytes (UTF-8) too far right.
func TestByteOrderMarkUnopened(t *testing.T) {
	for _, utf8 := range []bool{false, true} {
		t.Run(map[bool]string{false: "utf-16", true: "utf-8"}[utf8], func(t *testing.T) {
			dir, hls, _, rws := multi(t)
			path := filepath.Join(dir, "hls.spec.md")
			src := "$Rws.Session " + readFile(t, path)
			require.NoError(t, os.WriteFile(path, []byte(utf8BOM+src), 0o644))
			c := newClient(t, clientOptions{utf8: utf8})
			c.open(filepath.Join(dir, "rws.spec.md"))

			var we protocol.WorkspaceEdit
			c.call("textDocument/rename", &protocol.RenameParams{TextDocumentPositionParams: at(rws, 8, 7), NewName: "Rws.Link"}, &we)
			require.NotEmpty(t, we.Changes[hls])
			assert.Equal(t, protocol.TextEdit{Range: lspRange(0, 1, 12), NewText: "Rws.Link"}, we.Changes[hls][0])

			c.sync()
			assert.Equal(t, []diagSummary{{Range: lspRange(0, 0, 0), Severity: protocol.DiagnosticSeverityWarning, Code: "file-bom",
				Message: "file starts with a byte order mark; remove it"}}, summarize(t, c.diagnostics(hls)))

			// Opened the way editors open it (mark dropped), the same
			// reference sits at the same position.
			c.openText(path, src)
			var refs []protocol.Location
			c.call("textDocument/references", &protocol.ReferenceParams{TextDocumentPositionParams: at(hls, 0, 3),
				Context: protocol.ReferenceContext{IncludeDeclaration: false}}, &refs)
			assert.Contains(t, refs, protocol.Location{URI: hls, Range: lspRange(0, 0, 12)})
		})
	}
}

// TestConvByteOrderMarkRoundTrip checks that offsets after a byte order
// mark survive a round trip through LSP positions in both encodings.
func TestConvByteOrderMarkRoundTrip(t *testing.T) {
	src := []byte(utf8BOM + "$A.b 𝄞 x\nnext\n")
	x := pos.NewIndex(src)
	for _, c := range []conv{{utf8: false}, {utf8: true}} {
		assert.Equal(t, protocol.Position{}, c.position(x, 0), "utf8=%v: the mark is invisible", c.utf8)
		assert.Equal(t, protocol.Position{}, c.position(x, 3), "utf8=%v", c.utf8)
		for _, off := range []int{3, 4, 8, 12, 13, 14, 15, 19, 20} {
			assert.Equal(t, off, c.offset(x, c.position(x, off)), "utf8=%v off=%d", c.utf8, off)
		}
	}
}
