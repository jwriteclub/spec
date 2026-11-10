// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/project"
)

// diagSummary is the part of a diagnostic the tests compare.
type diagSummary struct {
	Range    protocol.Range
	Severity protocol.DiagnosticSeverity
	Code     string
	Message  string
}

func summarize(t *testing.T, ds []protocol.Diagnostic) []diagSummary {
	t.Helper()
	out := []diagSummary{}
	for _, d := range ds {
		code, ok := d.Code.(protocol.String)
		require.True(t, ok, "diagnostic code is a string")
		msg, ok := d.Message.(protocol.String)
		require.True(t, ok, "diagnostic message is a string")
		src, _ := d.Source.Get()
		assert.Equal(t, "spec", src)
		out = append(out, diagSummary{Range: d.Range, Severity: d.Severity, Code: string(code), Message: string(msg)})
	}
	return out
}

// multi copies testdata/multi and returns the directory and its file URIs.
func multi(t *testing.T) (dir string, hls, index, rws uri.URI) {
	t.Helper()
	dir = copyFixture(t, "multi")
	return dir, fileURIOf(t, filepath.Join(dir, "hls.spec.md")), fileURIOf(t, filepath.Join(dir, "index.spec.md")), fileURIOf(t, filepath.Join(dir, "rws.spec.md"))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestInitializeNegotiatesEncoding(t *testing.T) {
	c := newClient(t, clientOptions{})
	r := c.request("initialize", &protocol.InitializeParams{})
	require.NotNil(t, r.Error, "a second initialize is an error")
	assert.Equal(t, codeInvalidRequest, r.Error.Code)

	// The negotiated encoding and the capabilities, decoded.
	for _, tc := range []struct {
		offer []protocol.PositionEncodingKind
		want  protocol.PositionEncodingKind
	}{
		{nil, protocol.PositionEncodingKindUTF16},
		{[]protocol.PositionEncodingKind{protocol.PositionEncodingKindUTF16}, protocol.PositionEncodingKindUTF16},
		{[]protocol.PositionEncodingKind{protocol.PositionEncodingKindUTF16, protocol.PositionEncodingKindUTF8}, protocol.PositionEncodingKindUTF8},
	} {
		srv := New(strings.NewReader(""), io.Discard, Options{})
		res := srv.initialize(&protocol.InitializeParams{Capabilities: protocol.ClientCapabilities{
			General: &protocol.GeneralClientCapabilities{PositionEncodings: tc.offer},
		}})
		assert.Equal(t, tc.want, res.Capabilities.PositionEncoding)
		assert.Equal(t, tc.want == protocol.PositionEncodingKindUTF8, srv.conv.utf8)
		st, ok := res.Capabilities.SemanticTokensProvider.(*protocol.SemanticTokensOptions)
		require.True(t, ok)
		assert.Equal(t, []string{"variable", "type", "namespace"}, st.Legend.TokenTypes)
		assert.Equal(t, []string{"declaration"}, st.Legend.TokenModifiers)
		assert.Equal(t, []string{"$", "§", "#"}, res.Capabilities.CompletionProvider.TriggerCharacters)
		sync, ok := res.Capabilities.TextDocumentSync.(*protocol.TextDocumentSyncOptions)
		require.True(t, ok)
		assert.Equal(t, protocol.TextDocumentSyncKindFull, *sync.Change)
	}
}

func TestRequestsBeforeInitializeAndAfterShutdown(t *testing.T) {
	srv := New(strings.NewReader(""), io.Discard, Options{})
	_, rerr := srv.request(&message{ID: json.RawMessage("1"), Method: "textDocument/hover"})
	require.NotNil(t, rerr)
	assert.Equal(t, codeServerNotInit, rerr.Code)

	c := newClient(t, clientOptions{})
	c.call("shutdown", nil, nil)
	r := c.request("textDocument/hover", &protocol.HoverParams{})
	require.NotNil(t, r.Error)
	assert.Equal(t, codeInvalidRequest, r.Error.Code)
	r = c.request("textDocument/nonsense", nil)
	require.NotNil(t, r.Error)
}

// TestUnsavedEditDiagnostics is acceptance criterion 4: an unsaved edit that
// leaves references dangling produces diagnostics in every affected file,
// open or not, without writing anything; closing the buffer reverts to the
// disk content.
func TestUnsavedEditDiagnostics(t *testing.T) {
	dir, hls, index, rws := multi(t)
	before := readFile(t, filepath.Join(dir, "rws.spec.md"))
	c := newClient(t, clientOptions{})
	c.open(filepath.Join(dir, "rws.spec.md"))
	c.sync()
	for _, u := range []uri.URI{hls, index, rws} {
		assert.Equal(t, 0, c.publishCount(u), "a clean project publishes nothing")
	}

	edited := strings.Replace(before, "**Def $Rws.Session (session).**", "**Def $Rws.Connection (session).**", 1)
	require.NotEqual(t, before, edited)
	c.change(rws, 2, edited)
	c.sync()

	dangling := func(line, start, end uint32) diagSummary {
		return diagSummary{Range: lspRange(line, start, end), Severity: protocol.DiagnosticSeverityError, Code: "ref-dangling",
			Message: "$Rws.Session is not defined anywhere in the project"}
	}
	assert.Equal(t, []diagSummary{dangling(7, 75, 87)}, summarize(t, c.diagnostics(hls)))
	assert.Equal(t, []diagSummary{dangling(8, 0, 12)}, summarize(t, c.diagnostics(index)))
	assert.Equal(t, []diagSummary{
		{Range: lspRange(8, 6, 21), Severity: protocol.DiagnosticSeverityWarning, Code: "def-unreferenced",
			Message: "definition $Rws.Connection is not referenced by any other anchor or text; reference it where it is used, or remove it"},
		dangling(11, 60, 72), dangling(19, 53, 65), dangling(25, 33, 45),
	}, summarize(t, c.diagnostics(rws)))
	c.mu.Lock()
	v, ok := c.diags[rws].Version.Get()
	c.mu.Unlock()
	assert.True(t, ok)
	assert.Equal(t, int32(2), v, "diagnostics carry the buffer version")
	assert.Equal(t, before, readFile(t, filepath.Join(dir, "rws.spec.md")), "the file on disk is untouched")

	// A second identical analysis republishes nothing.
	counts := []int{c.publishCount(hls), c.publishCount(index), c.publishCount(rws)}
	c.change(rws, 3, edited)
	c.sync()
	assert.Equal(t, counts, []int{c.publishCount(hls), c.publishCount(index), c.publishCount(rws)})

	c.close(rws)
	c.sync()
	for _, u := range []uri.URI{hls, index, rws} {
		assert.Empty(t, c.diagnostics(u), "closing the buffer reverts to disk: %s", u)
		assert.Equal(t, 2, c.publishCount(u), "%s: published once with the error, once cleared", u)
	}
}

// TestDebounce checks that an edit is analyzed by the debounce timer alone,
// without any request.
func TestDebounce(t *testing.T) {
	dir, _, _, rws := multi(t)
	c := newClient(t, clientOptions{debounce: 20 * time.Millisecond})
	src := readFile(t, filepath.Join(dir, "rws.spec.md"))
	c.open(filepath.Join(dir, "rws.spec.md"))
	c.change(rws, 2, src+"\nA $Missing.Name here.\n")
	require.True(t, c.waitFor(func() bool { return len(c.diagnostics(rws)) == 1 }), "diagnostics after the debounce")
	assert.Equal(t, "ref-dangling", string(c.diagnostics(rws)[0].Code.(protocol.String)))
}

// TestChangedSinceHEAD checks the information diagnostic on anchors that
// differ from the git baseline.
func TestChangedSinceHEAD(t *testing.T) {
	requireGit(t)
	dir, hls, _, _ := multi(t)
	git(t, dir, "init", "-q")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "fixture")
	c := newClient(t, clientOptions{})
	src := readFile(t, filepath.Join(dir, "hls.spec.md"))
	c.open(filepath.Join(dir, "hls.spec.md"))
	c.sync()
	assert.Equal(t, 0, c.publishCount(hls), "the working tree matches HEAD")

	edited := strings.Replace(src, "at most 600 ms", "at most 500 ms", 1)
	edited += "\n**$Hls.Fresh (fresh).** A new invariant. Checked by: review.\n"
	c.change(hls, 2, edited)
	c.sync()
	assert.Equal(t, []diagSummary{
		{Range: lspRange(15, 0, 38), Severity: protocol.DiagnosticSeverityInformation, Code: codeAnchorChanged,
			Message: "invariant changed since HEAD: needs review (Spec-Change trailer)"},
		{Range: lspRange(31, 0, 23), Severity: protocol.DiagnosticSeverityInformation, Code: codeAnchorChanged,
			Message: "invariant added since HEAD: needs review (Spec-Change trailer)"},
	}, summarize(t, c.diagnostics(hls)))

	c.change(hls, 3, src)
	c.sync()
	assert.Empty(t, c.diagnostics(hls))
}

func TestHover(t *testing.T) {
	dir, hls, index, rws := multi(t)
	l, err := project.Load(dir, nil)
	require.NoError(t, err)
	short := l.Spec.Anchor("Rws.Session").ShortHash
	c := newClient(t, clientOptions{})
	c.open(filepath.Join(dir, "hls.spec.md"))
	c.open(filepath.Join(dir, "rws.spec.md"))
	_ = index

	hover := func(u uri.URI, line, char uint32) *protocol.Hover {
		raw := c.callRaw("textDocument/hover", &protocol.HoverParams{TextDocumentPositionParams: at(u, line, char)})
		if string(raw) == "null" {
			return nil
		}
		var h protocol.Hover
		require.NoError(t, protocol.Unmarshal(raw, &h))
		return &h
	}
	text := func(h *protocol.Hover) string {
		require.NotNil(t, h)
		mc, ok := h.Contents.(*protocol.MarkupContent)
		require.True(t, ok)
		assert.Equal(t, protocol.MarkupKindMarkdown, mc.Kind)
		return mc.Value
	}

	wantSession := "**definition** `$Rws.Session` (session)\n\n" +
		"**Def $Rws.Session (session).** A session is one RTSP connection from the relay to one camera. Viewers\n" +
		"never open sessions themselves; they attach to an existing one (see §2).\n\n" +
		"`rws.spec.md:9` · `" + short + "` · 5 references"
	for _, char := range []uint32{75, 80, 87} { // first byte, middle, just after the token
		h := hover(hls, 7, char)
		assert.Equal(t, wantSession, text(h), "char %d", char)
		assert.Equal(t, lspRange(7, 75, 87), *h.Range)
	}
	// On the definition site itself.
	assert.Equal(t, wantSession, text(hover(rws, 8, 10)))

	h := hover(rws, 9, 69) // §2 in rws: its own section 2
	assert.Equal(t, "**§2** Relay `rws.spec.md:16`\n\n"+
		"The relay copies frames from a session to every attached viewer and to the part packer described in §3.1.\n\n"+
		"Anchors:\n\n- `$Rws.FanOut` (fan-out)\n- `$Rws.IdleStop` (idle stop)", text(h))
	assert.Equal(t, lspRange(9, 68, 70), *h.Range)

	h = hover(hls, 26, 95) // §index:1
	assert.Equal(t, "**§1** Records `index.spec.md:5`\n\n"+
		"**Def $Index.Record (record).** A record is one motion interval for one camera: a start and an end, both on\n"+
		"the server's monotonic clock, with start <= end. Records come from alarm-log lines; the relay's\n"+
		"$Rws.Session objects play no part in producing them.\n\n"+
		"Anchors:\n\n- `$Index.Record` (record)\n- `$Index.Monotonic` (monotonic records)", text(h))
	assert.Equal(t, lspRange(26, 92, 100), *h.Range)

	h = hover(hls, 18, 4) // the number of "## 3. Renditions"
	assert.Equal(t, "**§3** Renditions `hls.spec.md:19`\n\n"+
		"An ffmpeg rendition is restarted by a watchdog when it stalls; each start is a generation (§3.1).\n\n"+
		"Anchors:\n\n- `$Hls.Generation` (generation)\n- `$Hls.WatchdogKills` (watchdog kills)", text(h))

	assert.Nil(t, hover(hls, 2, 5), "plain prose has no hover")
}

func TestDefinitionReferencesHighlight(t *testing.T) {
	dir, hls, index, rws := multi(t)
	c := newClient(t, clientOptions{})
	c.open(filepath.Join(dir, "hls.spec.md"))
	c.open(filepath.Join(dir, "rws.spec.md"))
	loc := func(u uri.URI, line, start, end uint32) protocol.Location {
		return protocol.Location{URI: u, Range: lspRange(line, start, end)}
	}

	var defs []protocol.Location
	c.call("textDocument/definition", &protocol.DefinitionParams{TextDocumentPositionParams: at(hls, 7, 80)}, &defs)
	assert.Equal(t, []protocol.Location{loc(rws, 8, 6, 18)}, defs)
	c.call("textDocument/definition", &protocol.DefinitionParams{TextDocumentPositionParams: at(hls, 26, 95)}, &defs)
	assert.Equal(t, []protocol.Location{loc(index, 4, 0, 13)}, defs, "§index:1 goes to the heading")
	c.call("textDocument/definition", &protocol.DefinitionParams{TextDocumentPositionParams: at(hls, 20, 92)}, &defs)
	assert.Equal(t, []protocol.Location{loc(hls, 22, 0, 19)}, defs, "§3.1 goes to the heading")
	assert.Equal(t, "null", string(c.callRaw("textDocument/definition", &protocol.DefinitionParams{TextDocumentPositionParams: at(hls, 2, 3)})))

	refs := func(u uri.URI, line, char uint32, decl bool) []protocol.Location {
		var out []protocol.Location
		c.call("textDocument/references", &protocol.ReferenceParams{TextDocumentPositionParams: at(u, line, char),
			Context: protocol.ReferenceContext{IncludeDeclaration: decl}}, &out)
		return out
	}
	sessionRefs := []protocol.Location{loc(hls, 7, 75, 87), loc(index, 8, 0, 12), loc(rws, 11, 60, 72), loc(rws, 19, 53, 65), loc(rws, 25, 33, 45)}
	assert.Equal(t, sessionRefs, refs(hls, 7, 80, false))
	assert.Equal(t, append([]protocol.Location{loc(rws, 8, 6, 18)}, sessionRefs...), refs(rws, 8, 7, true))
	assert.Equal(t, []protocol.Location{loc(hls, 22, 4, 7), loc(hls, 20, 91, 95), loc(index, 23, 44, 48), loc(rws, 17, 100, 104)},
		refs(hls, 20, 92, true), "§3.1 from its reference")
	assert.Equal(t, []protocol.Location{loc(hls, 20, 91, 95), loc(index, 23, 44, 48), loc(rws, 17, 100, 104)},
		refs(hls, 22, 5, false), "§3.1 from its heading")

	var hl []protocol.DocumentHighlight
	c.call("textDocument/documentHighlight", &protocol.DocumentHighlightParams{TextDocumentPositionParams: at(rws, 19, 55)}, &hl)
	assert.Equal(t, []protocol.DocumentHighlight{
		{Range: lspRange(8, 6, 18), Kind: protocol.DocumentHighlightKindWrite},
		{Range: lspRange(11, 60, 72), Kind: protocol.DocumentHighlightKindRead},
		{Range: lspRange(19, 53, 65), Kind: protocol.DocumentHighlightKindRead},
		{Range: lspRange(25, 33, 45), Kind: protocol.DocumentHighlightKindRead},
	}, hl)
}

func TestRename(t *testing.T) {
	dir, hls, index, rws := multi(t)
	before := map[string]string{}
	for _, n := range []string{"hls.spec.md", "index.spec.md", "rws.spec.md"} {
		before[n] = readFile(t, filepath.Join(dir, n))
	}
	c := newClient(t, clientOptions{docChanges: true})
	c.open(filepath.Join(dir, "hls.spec.md"))
	// An unsaved reference in the open buffer is renamed at its buffer
	// position.
	c.change(hls, 2, before["hls.spec.md"]+"\nSee $Rws.Session again.\n")

	var prep protocol.PrepareRenamePlaceholder
	c.call("textDocument/prepareRename", &protocol.PrepareRenameParams{TextDocumentPositionParams: at(hls, 31, 6)}, &prep)
	assert.Equal(t, protocol.PrepareRenamePlaceholder{Range: lspRange(31, 5, 16), Placeholder: "Rws.Session"}, prep)
	assert.Equal(t, "null", string(c.callRaw("textDocument/prepareRename", &protocol.PrepareRenameParams{TextDocumentPositionParams: at(hls, 2, 4)})))

	var we protocol.WorkspaceEdit
	c.call("textDocument/rename", &protocol.RenameParams{TextDocumentPositionParams: at(hls, 7, 80), NewName: "$Rws.Connection"}, &we)
	assert.Nil(t, we.Changes)
	require.Len(t, we.DocumentChanges, 3)
	edit := func(line, start, end uint32) protocol.TextDocumentEditElement {
		return &protocol.TextEdit{Range: lspRange(line, start, end), NewText: "Rws.Connection"}
	}
	two := int32(2)
	want := []*protocol.TextDocumentEdit{
		{TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: hls}, Version: &two},
			Edits: []protocol.TextDocumentEditElement{edit(7, 76, 87), edit(31, 5, 16)}},
		{TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: index}},
			Edits: []protocol.TextDocumentEditElement{edit(8, 1, 12)}},
		{TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: rws}},
			Edits: []protocol.TextDocumentEditElement{edit(8, 7, 18), edit(11, 61, 72), edit(19, 54, 65), edit(25, 34, 45)}},
	}
	for i, dc := range we.DocumentChanges {
		got, ok := dc.(*protocol.TextDocumentEdit)
		require.True(t, ok)
		assert.Equal(t, want[i], got)
	}
	for n, b := range before {
		assert.Equal(t, b, readFile(t, filepath.Join(dir, n)), "rename writes nothing: %s", n)
	}

	r := c.request("textDocument/rename", &protocol.RenameParams{TextDocumentPositionParams: at(hls, 7, 80), NewName: "Rws.FanOut"})
	require.NotNil(t, r.Error)
	assert.Equal(t, codeRequestFailed, r.Error.Code)
	assert.Contains(t, r.Error.Message, "$Rws.FanOut is already defined")
	r = c.request("textDocument/rename", &protocol.RenameParams{TextDocumentPositionParams: at(hls, 7, 80), NewName: "rws.session"})
	require.NotNil(t, r.Error)
	assert.Contains(t, r.Error.Message, "is not a valid name")

	// Without documentChanges support the edit comes as a changes map.
	c2 := newClient(t, clientOptions{})
	c2.open(filepath.Join(dir, "hls.spec.md"))
	var we2 protocol.WorkspaceEdit
	c2.call("textDocument/rename", &protocol.RenameParams{TextDocumentPositionParams: at(hls, 6, 8), NewName: "Hls.Piece"}, &we2)
	assert.Nil(t, we2.DocumentChanges)
	assert.Equal(t, map[uri.URI][]protocol.TextEdit{
		hls: {{Range: lspRange(6, 7, 15), NewText: "Hls.Piece"}, {Range: lspRange(15, 42, 50), NewText: "Hls.Piece"}},
	}, we2.Changes)
}

func TestCompletion(t *testing.T) {
	dir, hls, _, _ := multi(t)
	src := readFile(t, filepath.Join(dir, "hls.spec.md"))
	c := newClient(t, clientOptions{})
	c.open(filepath.Join(dir, "hls.spec.md"))
	version := int32(1)
	// complete appends line to the buffer and completes at its end, or
	// before the first "|" when there is one.
	complete := func(line string) (*protocol.CompletionList, uint32) {
		version++
		char := len(line)
		if i := strings.Index(line, "|"); i >= 0 {
			char = i
			line = line[:i] + line[i+1:]
		}
		c.change(hls, version, src+"\n"+line+"\n")
		raw := c.callRaw("textDocument/completion", &protocol.CompletionParams{TextDocumentPositionParams: at(hls, 31, uint32(char))})
		if string(raw) == "null" {
			return nil, 31
		}
		var cl protocol.CompletionList
		require.NoError(t, protocol.Unmarshal(raw, &cl))
		return &cl, 31
	}
	item := func(cl *protocol.CompletionList, label string) protocol.CompletionItem {
		for _, it := range cl.Items {
			if it.Label == label {
				return it
			}
		}
		t.Fatalf("no completion item %q", label)
		return protocol.CompletionItem{}
	}
	opt := protocol.NewOptional[string]

	cl, line := complete("Dollar $Rws.O")
	require.NotNil(t, cl)
	require.Len(t, cl.Items, 13, "every anchor once")
	assert.Equal(t, "$Hls.Part", cl.Items[0].Label, "project order")
	assert.Equal(t, protocol.CompletionItemKindClass, cl.Items[0].Kind)
	assert.Equal(t, opt("definition: part"), cl.Items[0].Detail)
	it := item(cl, "$Rws.OnePerCamera")
	assert.Equal(t, protocol.CompletionItemKindVariable, it.Kind)
	assert.Equal(t, opt("invariant: one session per camera"), it.Detail)
	assert.Equal(t, opt("$Rws.OnePerCamera"), it.FilterText)
	assert.Equal(t, &protocol.TextEdit{Range: lspRange(line, 7, 13), NewText: "$Rws.OnePerCamera"}, it.TextEdit)

	cl, line = complete("Hash #3.")
	require.NotNil(t, cl)
	var labels []string
	for _, it := range cl.Items {
		labels = append(labels, it.Label)
	}
	assert.Equal(t, []string{"§1", "§2", "§3", "§3.1", "§index:1", "§index:2", "§rws:1", "§rws:2", "§2.1"}, labels)
	it = item(cl, "§3.1")
	assert.Equal(t, opt("#3.1"), it.FilterText, "filters on what was typed")
	assert.Equal(t, opt("Generations"), it.Detail)
	assert.Equal(t, &protocol.TextEdit{Range: lspRange(line, 5, 8), NewText: "§3.1"}, it.TextEdit, "replaces the # with §")
	it = item(cl, "§2.1")
	assert.Equal(t, opt("Idle stop (rws.spec.md)"), it.Detail)

	cl, line = complete("Sign §ind")
	require.NotNil(t, cl)
	it = item(cl, "§index:2")
	assert.Equal(t, opt("§index:2"), it.FilterText)
	assert.Equal(t, &protocol.TextEdit{Range: lspRange(line, 5, 9), NewText: "§index:2"}, it.TextEdit, "§ is one UTF-16 unit")

	cl, _ = complete("Spaced § 2")
	require.NotNil(t, cl)
	assert.Equal(t, opt("§ 2.1"), item(cl, "§2.1").FilterText)

	for _, line := range []string{"Code `$Rws|` here", "#", "## 4", "word#3", "price$5", "no sigil"} {
		cl, _ = complete(line)
		assert.Nil(t, cl, "no completion for %q", line)
	}
}

func TestSemanticTokens(t *testing.T) {
	dir, hls, _, _ := multi(t)
	c := newClient(t, clientOptions{})
	c.open(filepath.Join(dir, "hls.spec.md"))
	var st protocol.SemanticTokens
	c.call("textDocument/semanticTokens/full", &protocol.SemanticTokensParams{TextDocument: protocol.TextDocumentIdentifier{URI: hls}}, &st)
	const v, ty, ns, decl = tokenInvariant, tokenDefinition, tokenSection, modDeclaration
	assert.Equal(t, [][5]uint32{
		{4, 3, 1, ns, decl},   // ## 1.
		{6, 6, 9, ty, decl},   // **Def $Hls.Part
		{7, 75, 12, ty, 0},    // $Rws.Session (a definition)
		{9, 2, 18, v, decl},   // **$Hls.KeyframeStart
		{11, 0, 2, ns, 0},     // §2
		{11, 21, 2, ns, 0},    // §3
		{13, 3, 1, ns, decl},  // ## 2.
		{15, 2, 17, v, decl},  // **$Hls.PartDuration
		{15, 41, 9, ty, 0},    // $Hls.Part
		{18, 3, 1, ns, decl},  // ## 3.
		{20, 91, 4, ns, 0},    // §3.1
		{22, 4, 3, ns, decl},  // ### 3.1
		{24, 6, 15, ty, decl}, // **Def $Hls.Generation
		{26, 50, 13, ty, 0},   // $Index.Record
		{26, 92, 8, ns, 0},    // §index:1
		{28, 2, 18, v, decl},  // **$Hls.WatchdogKills
		{28, 62, 15, ty, 0},   // $Hls.Generation
		{29, 83, 8, ns, 0},    // §rws:2.1
	}, absoluteTokens(t, st.Data))
}

// absoluteTokens decodes relative semantic token data into absolute
// (line, char, length, type, modifiers) tuples.
func absoluteTokens(t *testing.T, data []uint32) [][5]uint32 {
	t.Helper()
	require.Zero(t, len(data)%5)
	out := [][5]uint32{}
	var line, char uint32
	for i := 0; i < len(data); i += 5 {
		if data[i] > 0 {
			line += data[i]
			char = data[i+1]
		} else {
			char += data[i+1]
		}
		out = append(out, [5]uint32{line, char, data[i+2], data[i+3], data[i+4]})
	}
	return out
}

func TestSymbols(t *testing.T) {
	dir, hls, _, _ := multi(t)
	c := newClient(t, clientOptions{})
	c.open(filepath.Join(dir, "hls.spec.md"))

	var syms []protocol.DocumentSymbol
	c.call("textDocument/documentSymbol", &protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: hls}}, &syms)
	var outline func(ss []protocol.DocumentSymbol, depth int) []string
	outline = func(ss []protocol.DocumentSymbol, depth int) []string {
		var out []string
		for _, s := range ss {
			d := ""
			if s.Detail != nil {
				d = " " + *s.Detail
			}
			out = append(out, strings.Repeat("  ", depth)+s.Name+d)
			out = append(out, outline(s.Children, depth+1)...)
		}
		return out
	}
	assert.Equal(t, []string{
		"§1 Playlists",
		"  $Hls.Part part",
		"  $Hls.KeyframeStart segments start on keyframes",
		"§2 Parts",
		"  $Hls.PartDuration part duration",
		"§3 Renditions",
		"  §3.1 Generations",
		"    $Hls.Generation generation",
		"    $Hls.WatchdogKills watchdog kills",
	}, outline(syms, 0))
	require.Len(t, syms, 3)
	assert.Equal(t, protocol.SymbolKindNamespace, syms[0].Kind)
	assert.Equal(t, protocol.Range{Start: lspPos(4, 0), End: lspPos(11, 24)}, syms[0].Range)
	assert.Equal(t, lspRange(4, 0, 15), syms[0].SelectionRange)
	assert.Equal(t, protocol.SymbolKindClass, syms[0].Children[0].Kind)
	assert.Equal(t, lspRange(6, 6, 15), syms[0].Children[0].SelectionRange)
	assert.Equal(t, protocol.SymbolKindVariable, syms[0].Children[1].Kind)

	var ws []protocol.WorkspaceSymbol
	c.call("workspace/symbol", &protocol.WorkspaceSymbolParams{Query: "watchdog"}, &ws)
	require.Len(t, ws, 1)
	assert.Equal(t, "$Hls.WatchdogKills", ws[0].Name)
	assert.Equal(t, protocol.SymbolKindVariable, ws[0].Kind)
	assert.Equal(t, "watchdog kills (hls.spec.md)", *ws[0].ContainerName)
	assert.Equal(t, &protocol.Location{URI: hls, Range: lspRange(28, 2, 20)}, ws[0].Location)
	c.call("workspace/symbol", &protocol.WorkspaceSymbolParams{Query: ""}, &ws)
	assert.Len(t, ws, 13)
	c.call("workspace/symbol", &protocol.WorkspaceSymbolParams{Query: "$rws."}, &ws)
	assert.Len(t, ws, 4, "case-insensitive, sigil optional")
}

// TestSeveralProjectsAndOutsiders opens files of two projects and one file
// outside any project in one session.
func TestSeveralProjectsAndOutsiders(t *testing.T) {
	dir, hls, _, _ := multi(t)
	edge := edgeProject(t)
	lone := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(lone, "lone.spec.md"), []byte("# Lone\n\nA $Dangling.Ref here.\n"), 0o644))

	c := newClient(t, clientOptions{})
	c.open(filepath.Join(dir, "hls.spec.md"))
	refs := c.open(filepath.Join(edge, "refs.spec.md"))
	loneURI := c.open(filepath.Join(lone, "lone.spec.md"))
	c.sync()

	var ws []protocol.WorkspaceSymbol
	c.call("workspace/symbol", &protocol.WorkspaceSymbolParams{Query: ""}, &ws)
	assert.Len(t, ws, 13+4, "anchors of both projects")
	assert.NotEmpty(t, c.diagnostics(refs), "the edge project has findings")
	assert.Empty(t, c.diagnostics(hls))

	assert.Equal(t, 0, c.publishCount(loneURI), "nothing for a file outside any project")
	for _, m := range []string{"textDocument/hover", "textDocument/definition", "textDocument/completion", "textDocument/documentSymbol", "textDocument/prepareRename"} {
		assert.Equal(t, "null", string(c.callRaw(m, &protocol.HoverParams{TextDocumentPositionParams: at(loneURI, 2, 4)})), m)
	}
	var st protocol.SemanticTokens
	c.call("textDocument/semanticTokens/full", &protocol.SemanticTokensParams{TextDocument: protocol.TextDocumentIdentifier{URI: loneURI}}, &st)
	assert.Empty(t, st.Data)
}

// TestProjectAppearsAfterOpen checks that a document opened before its
// spec.toml existed (or while it was gone) joins the project on its next
// edit or save, without being reopened.
func TestProjectAppearsAfterOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.spec.md")
	require.NoError(t, os.WriteFile(path, []byte("# A\n"), 0o644))
	c := newClient(t, clientOptions{})
	u := c.open(path)
	c.sync()
	assert.Equal(t, 0, c.publishCount(u), "outside any project")

	// `spec init` while the file is open.
	cfg := filepath.Join(dir, project.ConfigFile)
	require.NoError(t, os.WriteFile(cfg, []byte("name = \"late\"\n"), 0o644))
	c.change(u, 2, "# A\n\nSee $Not.Here.\n")
	c.sync()
	require.Len(t, c.diagnostics(u), 1, "the edit is analyzed in the new project")
	assert.Equal(t, protocol.String("ref-dangling"), c.diagnostics(u)[0].Code)

	// spec.toml goes away (a branch switch) and the project is dropped...
	require.NoError(t, os.Remove(cfg))
	c.notify("textDocument/didSave", &protocol.DidSaveTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: u}})
	c.sync()
	assert.Empty(t, c.diagnostics(u))

	// ...and comes back: the document rejoins on save.
	require.NoError(t, os.WriteFile(cfg, []byte("name = \"late\"\n"), 0o644))
	c.notify("textDocument/didSave", &protocol.DidSaveTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: u}})
	c.sync()
	require.Len(t, c.diagnostics(u), 1, "the document rejoins the recreated project")
}

// TestBaselineArrivesDuringEdits checks the background baseline load: its
// diagnostics carry the version of the text they were computed from, not
// the version of a newer edit that is still waiting for its debounce.
func TestBaselineArrivesDuringEdits(t *testing.T) {
	requireGit(t)
	dir, hls, _, _ := multi(t)
	git(t, dir, "init", "-q")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "fixture")
	path := filepath.Join(dir, "hls.spec.md")
	src := strings.Replace(readFile(t, path), "at most 600 ms", "at most 500 ms", 1)
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))

	// A slow git, so the baseline arrives after the edit below.
	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)
	slow := filepath.Join(t.TempDir(), "slowgit")
	require.NoError(t, os.WriteFile(slow, []byte("#!/bin/sh\nsleep 0.1\nexec "+gitPath+" \"$@\"\n"), 0o755))
	c := newClient(t, clientOptions{asyncBaseline: true, git: baseline.Loader{Git: slow, Env: gitEnv()}})
	c.open(path)
	c.sync() // analyzes version 1 and starts the baseline load
	c.change(hls, 2, "\n\n\n"+src)

	require.True(t, c.waitFor(func() bool { return len(c.diagnostics(hls)) == 1 }), "the baseline's note")
	c.mu.Lock()
	first := *c.diags[hls]
	c.mu.Unlock()
	v, _ := first.Version.Get()
	assert.Equal(t, int32(1), v, "computed from version 1")
	assert.Equal(t, uint32(15), first.Diagnostics[0].Range.Start.Line)

	c.sync()
	c.mu.Lock()
	last := *c.diags[hls]
	c.mu.Unlock()
	v, _ = last.Version.Get()
	assert.Equal(t, int32(2), v)
	require.Len(t, last.Diagnostics, 1)
	assert.Equal(t, uint32(18), last.Diagnostics[0].Range.Start.Line)
}
