// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
)

// watchRequests returns the requests the server has sent so far, having first
// made one round trip so that anything sent on initialized has arrived.
func watchRequests(c *client) []serverRequest {
	c.t.Helper()
	c.call("workspace/symbol", &protocol.WorkspaceSymbolParams{Query: "x"}, nil)
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]serverRequest(nil), c.serverRequests...)
}

func TestRegistersFileWatches(t *testing.T) {
	c := newClient(t, clientOptions{dynamicWatch: true})
	reqs := watchRequests(c)
	require.Len(t, reqs, 1)
	assert.Equal(t, "client/registerCapability", reqs[0].method)

	var got struct {
		Registrations []struct {
			ID, Method      string
			RegisterOptions struct {
				Watchers []struct {
					GlobPattern string `json:"globPattern"`
				} `json:"watchers"`
			} `json:"registerOptions"`
		} `json:"registrations"`
	}
	require.NoError(t, json.Unmarshal(reqs[0].params, &got), "%s", reqs[0].params)
	require.Len(t, got.Registrations, 1)
	reg := got.Registrations[0]
	assert.Equal(t, "workspace/didChangeWatchedFiles", reg.Method)
	assert.NotEmpty(t, reg.ID)
	var globs []string
	for _, w := range reg.RegisterOptions.Watchers {
		globs = append(globs, w.GlobPattern)
	}
	assert.Equal(t, []string{"**/spec.toml", "**/*.md", "**/*.tla"}, globs)
}

func TestNoRegistrationWithoutClientSupport(t *testing.T) {
	assert.Empty(t, watchRequests(newClient(t, clientOptions{})))
}

// TestRegistrationOnce: a second initialized does not register again.
func TestRegistrationOnce(t *testing.T) {
	c := newClient(t, clientOptions{dynamicWatch: true})
	c.notify("initialized", &protocol.InitializedParams{})
	assert.Len(t, watchRequests(c), 1)
}

// TestRegistrationErrorTolerated: a client refusing the registration costs
// nothing; the session goes on, and a response nobody asked for is ignored.
func TestRegistrationErrorTolerated(t *testing.T) {
	dir, _, _, _ := multi(t)
	c := newClient(t, clientOptions{dynamicWatch: true, reply: func(string) any {
		return &rpcError{Code: codeMethodNotFound, Message: "no thanks"}
	}})
	require.Len(t, watchRequests(c), 1)
	c.send(map[string]any{"jsonrpc": "2.0", "id": 999, "result": nil})
	c.send(map[string]any{"jsonrpc": "2.0", "id": 998, "error": map[string]any{"code": -1, "message": "stray"}})
	u := c.open(filepath.Join(dir, "hls.spec.md"))
	raw := c.callRaw("textDocument/hover", &protocol.HoverParams{TextDocumentPositionParams: at(u, 7, 80)})
	assert.Contains(t, string(raw), "$Rws.Session")
}

// watched sends the client's report of file events.
func watched(c *client, events map[string]protocol.FileChangeType) {
	c.t.Helper()
	p := &protocol.DidChangeWatchedFilesParams{}
	for path, typ := range events {
		p.Changes = append(p.Changes, protocol.FileEvent{URI: fileURIOf(c.t, path), Type: typ})
	}
	c.notify("workspace/didChangeWatchedFiles", p)
}

// hoverText asks for the hover at text in the open file f.
func hoverText(c *client, f, src, text string) string {
	c.t.Helper()
	var h protocol.Hover
	var p protocol.HoverParams
	p.TextDocument = protocol.TextDocumentIdentifier{URI: fileURIOf(c.t, f)}
	p.Position = positionOf(src, text)
	c.call("textDocument/hover", &p, &h)
	raw, err := protocol.Marshal(&h)
	require.NoError(c.t, err)
	return string(raw)
}

// positionOf is the position of the first occurrence of text in src.
func positionOf(src, text string) protocol.Position {
	at := strings.Index(src, text)
	line := strings.Count(src[:at], "\n")
	col := at - strings.LastIndexByte(src[:at], '\n') - 1
	return protocol.Position{Line: uint32(line), Character: uint32(col)}
}

// watchFixture is a project with an open file whose $Later.Def dangles.
func watchFixture(t *testing.T, config string) (c *client, root, main, src string) {
	t.Helper()
	root = t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "spec.toml"), []byte(config), 0o644))
	src = "# main\n\n**$Main.One (one).** It MUST hold after $Later.Def.\n"
	main = filepath.Join(root, "main.spec.md")
	require.NoError(t, os.WriteFile(main, []byte(src), 0o644))
	c = newClient(t, clientOptions{dynamicWatch: true})
	c.open(main)
	return c, root, main, src
}

const watchLater = "# later\n\n**$Later.Def (later).** It MUST arrive.\n"

// danglingLater reports whether main's diagnostics name $Later.Def as dangling,
// after flushing through a request.
func danglingLater(c *client, main, src string) bool {
	c.t.Helper()
	_ = hoverText(c, main, src, "$Main.One")
	return diagnosticsMention(c, main, "Later.Def")
}

func diagnosticsMention(c *client, file, text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.diags[fileURIOf(c.t, file)]
	if p == nil {
		return false
	}
	raw, err := protocol.Marshal(p)
	return err == nil && strings.Contains(string(raw), text)
}

// TestWatchedNewFileResolves: a file created after the project was analyzed
// resolves on the event, and a deleted one goes.
func TestWatchedNewFileResolves(t *testing.T) {
	c, root, main, src := watchFixture(t, "")
	require.True(t, danglingLater(c, main, src))

	later := filepath.Join(root, "later.spec.md")
	require.NoError(t, os.WriteFile(later, []byte(watchLater), 0o644))
	assert.True(t, danglingLater(c, main, src), "nothing told the server yet")
	watched(c, map[string]protocol.FileChangeType{later: protocol.FileChangeTypeCreated})
	assert.False(t, danglingLater(c, main, src), "the new file resolves")
	assert.Contains(t, hoverText(c, main, src, "$Later.Def"), "MUST arrive")

	require.NoError(t, os.Remove(later))
	watched(c, map[string]protocol.FileChangeType{later: protocol.FileChangeTypeDeleted})
	assert.True(t, danglingLater(c, main, src), "the deleted file went")
}

// TestWatchedNewDirectory: a new directory is no different, for a default glob
// or a model's.
func TestWatchedNewDirectory(t *testing.T) {
	c, root, main, src := watchFixture(t, "formal = [\"tla/*.tla\"]\n")
	later := filepath.Join(root, "deep", "er", "later.spec.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(later), 0o755))
	require.NoError(t, os.WriteFile(later, []byte(watchLater), 0o644))
	watched(c, map[string]protocol.FileChangeType{later: protocol.FileChangeTypeCreated})
	assert.False(t, danglingLater(c, main, src))

	model := filepath.Join(root, "tla", "N.tla")
	require.NoError(t, os.MkdirAll(filepath.Dir(model), 0o755))
	require.NoError(t, os.WriteFile(model, []byte("---- MODULE N ----\n\\* Spec: $Main.One \"MUST hold\"\nInv == TRUE\n====\n"), 0o644))
	watched(c, map[string]protocol.FileChangeType{model: protocol.FileChangeTypeCreated})
	assert.Contains(t, hoverText(c, main, src, "$Main.One"), "N.tla", "the model's tag reaches the hover")
}

// TestWatchedClosedModelEdited: a closed .tla edited on disk is read again, the
// gap plan-tla.md listed.
func TestWatchedClosedModelEdited(t *testing.T) {
	c, root, main, src := watchFixture(t, "formal = [\"N.tla\"]\n")
	model := filepath.Join(root, "N.tla")
	require.NoError(t, os.WriteFile(model, []byte("---- MODULE N ----\nInv == TRUE\n====\n"), 0o644))
	watched(c, map[string]protocol.FileChangeType{model: protocol.FileChangeTypeCreated})
	assert.NotContains(t, hoverText(c, main, src, "$Main.One"), "N.tla")
	require.NoError(t, os.WriteFile(model, []byte("---- MODULE N ----\n\\* Spec: $Main.One \"MUST hold\"\nInv == TRUE\n====\n"), 0o644))
	watched(c, map[string]protocol.FileChangeType{model: protocol.FileChangeTypeChanged})
	assert.Contains(t, hoverText(c, main, src, "$Main.One"), "N.tla")
}

// TestWatchedOverlayWins: an open document's buffer beats its file on disk,
// through a change and a deletion.
func TestWatchedOverlayWins(t *testing.T) {
	c, root, main, src := watchFixture(t, "")
	later := filepath.Join(root, "later.spec.md")
	require.NoError(t, os.WriteFile(later, []byte(watchLater), 0o644))
	c.openText(later, "# later\n\n**$Later.Def (buffer).** It MUST come from the buffer.\n")

	require.NoError(t, os.WriteFile(later, []byte("# later\n\nnothing here\n"), 0o644))
	watched(c, map[string]protocol.FileChangeType{later: protocol.FileChangeTypeChanged})
	assert.Contains(t, hoverText(c, main, src, "$Later.Def"), "from the buffer")

	require.NoError(t, os.Remove(later))
	watched(c, map[string]protocol.FileChangeType{later: protocol.FileChangeTypeDeleted})
	assert.Contains(t, hoverText(c, main, src, "$Later.Def"), "from the buffer")
}

// TestWatchedSpecTomlChange: spec.toml is watched too: a glob change takes effect.
func TestWatchedSpecTomlChange(t *testing.T) {
	c, root, main, src := watchFixture(t, "files = [\"main.spec.md\"]\n")
	later := filepath.Join(root, "later.txt")
	require.NoError(t, os.WriteFile(later, []byte(watchLater), 0o644))
	assert.True(t, danglingLater(c, main, src))
	cfg := filepath.Join(root, "spec.toml")
	require.NoError(t, os.WriteFile(cfg, []byte("files = [\"main.spec.md\", \"later.txt\"]\n"), 0o644))
	watched(c, map[string]protocol.FileChangeType{cfg: protocol.FileChangeTypeChanged})
	assert.False(t, danglingLater(c, main, src))
}

// TestWatchedPublishesOnItsOwn: without a request, the debounce publishes.
func TestWatchedPublishesOnItsOwn(t *testing.T) {
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "spec.toml"), nil, 0o644))
	src := "# main\n\n**$Main.One (one).** It MUST hold after $Later.Def.\n"
	main := filepath.Join(root, "main.spec.md")
	require.NoError(t, os.WriteFile(main, []byte(src), 0o644))
	c := newClient(t, clientOptions{debounce: 5 * time.Millisecond, dynamicWatch: true})
	u := c.open(main)
	waitDiags := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(testTimeout)
		for diagnosticsMention(c, main, "Later.Def") != want {
			if time.Now().After(deadline) {
				t.Fatalf("diagnostics of %s never reached %v", u, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitDiags(true)
	later := filepath.Join(root, "later.spec.md")
	require.NoError(t, os.WriteFile(later, []byte(watchLater), 0o644))
	watched(c, map[string]protocol.FileChangeType{later: protocol.FileChangeTypeCreated})
	waitDiags(false)
}

// nestedFixture is a project holding sub/s.spec.md, with its own spec.toml or not,
// and the file open.
func nestedFixture(t *testing.T, nested bool) (c *client, toml, sub, src string) {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "spec.toml"), nil, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	toml = filepath.Join(root, "sub", "spec.toml")
	if nested {
		require.NoError(t, os.WriteFile(toml, nil, 0o644))
	}
	sub = filepath.Join(root, "sub", "s.spec.md")
	src = "# s\n\n**$S.One (one).** It MUST hold.\n"
	require.NoError(t, os.WriteFile(sub, []byte(src), 0o644))
	c = newClient(t, clientOptions{dynamicWatch: true})
	c.open(sub)
	require.Contains(t, hoverText(c, sub, src, "$S.One"), "MUST hold")
	return c, toml, sub, src
}

// TestNestedSpecTomlCreated: an open document moves to the project that now owns it.
func TestNestedSpecTomlCreated(t *testing.T) {
	c, toml, sub, src := nestedFixture(t, false)
	require.NoError(t, os.WriteFile(toml, nil, 0o644))
	watched(c, map[string]protocol.FileChangeType{toml: protocol.FileChangeTypeCreated})
	assert.Contains(t, hoverText(c, sub, src, "$S.One"), "MUST hold")
}

// TestNestedSpecTomlDeleted: an open document moves to the project that takes it in.
func TestNestedSpecTomlDeleted(t *testing.T) {
	c, toml, sub, src := nestedFixture(t, true)
	require.NoError(t, os.Remove(toml))
	watched(c, map[string]protocol.FileChangeType{toml: protocol.FileChangeTypeDeleted})
	assert.Contains(t, hoverText(c, sub, src, "$S.One"), "MUST hold")
	assert.Contains(t, hoverText(c, sub, src, "$S.One"), "MUST hold", "and stays")
}
