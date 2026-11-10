// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/jwriteclub/spec/internal/baseline"
)

// testTimeout bounds every wait in the in-process tests.
const testTimeout = 10 * time.Second

// client is an in-process LSP client driving a Server over pipes.
type client struct {
	t      *testing.T
	srv    *Server
	in     *io.PipeWriter
	nextID int
	done   chan error

	mu      sync.Mutex
	pending map[string]chan *response
	diags   map[uri.URI]*protocol.PublishDiagnosticsParams
	// published counts publishDiagnostics notifications per URI.
	published map[uri.URI]int
	notified  chan struct{}
	// serverRequests are the requests the server sent; reply answers them.
	serverRequests []serverRequest
	reply          func(method string) any
}

// serverRequest is a request the server sent to the client.
type serverRequest struct {
	id     json.RawMessage
	method string
	params json.RawMessage
}

// clientOptions configures newClient.
type clientOptions struct {
	utf8       bool // offer the utf-8 position encoding
	docChanges bool // accept documentChanges in workspace edits
	debounce   time.Duration
	git        baseline.Loader
	// asyncBaseline loads the git baseline in the background, as the real
	// server does, instead of inline.
	asyncBaseline bool
	// dynamicWatch declares dynamic registration of didChangeWatchedFiles.
	dynamicWatch bool
	// reply answers a request from the server: an *rpcError fails it; nil
	// answers with a null result.
	reply func(method string) any
	// spell runs the spell check's model pass (Options.Spell).
	spell func(ctx context.Context, root string) error
}

// newClient starts a server and initializes it.
func newClient(t *testing.T, o clientOptions) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	debounce := o.debounce
	if debounce == 0 {
		debounce = time.Hour // tests flush through requests unless they ask for the timer
	}
	srv := New(inR, outW, Options{Debounce: debounce, Git: o.git, Version: "test", Spell: o.spell})
	srv.syncBaseline = !o.asyncBaseline
	c := &client{
		t: t, srv: srv, in: inW, done: make(chan error, 1),
		pending:   map[string]chan *response{},
		diags:     map[uri.URI]*protocol.PublishDiagnosticsParams{},
		published: map[uri.URI]int{},
		notified:  make(chan struct{}, 1),
		reply:     o.reply,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		err := srv.Run(ctx)
		_ = outW.Close()
		c.done <- err
	}()
	go c.readLoop(bufio.NewReader(outR))
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		select {
		case <-c.done:
		case <-time.After(testTimeout):
			t.Error("server did not stop")
		}
	})

	caps := protocol.ClientCapabilities{}
	if o.utf8 {
		caps.General = &protocol.GeneralClientCapabilities{PositionEncodings: []protocol.PositionEncodingKind{protocol.PositionEncodingKindUTF8, protocol.PositionEncodingKindUTF16}}
	}
	if o.docChanges {
		yes := true
		caps.Workspace = &protocol.WorkspaceClientCapabilities{WorkspaceEdit: &protocol.WorkspaceEditClientCapabilities{DocumentChanges: &yes}}
	}
	if o.dynamicWatch {
		yes := true
		if caps.Workspace == nil {
			caps.Workspace = &protocol.WorkspaceClientCapabilities{}
		}
		caps.Workspace.DidChangeWatchedFiles = &protocol.DidChangeWatchedFilesClientCapabilities{DynamicRegistration: &yes}
	}
	var res protocol.InitializeResult
	c.call("initialize", &protocol.InitializeParams{Capabilities: caps}, &res)
	c.notify("initialized", &protocol.InitializedParams{})
	return c
}

func (c *client) readLoop(r *bufio.Reader) {
	for {
		body, err := readMessage(r)
		if err != nil {
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			c.t.Errorf("server sent invalid JSON: %v: %s", err, body)
			return
		}
		if m.Method == "textDocument/publishDiagnostics" {
			var p protocol.PublishDiagnosticsParams
			if err := protocol.Unmarshal(m.Params, &p); err != nil {
				c.t.Errorf("bad publishDiagnostics: %v", err)
				continue
			}
			c.mu.Lock()
			c.diags[p.URI] = &p
			c.published[p.URI]++
			c.mu.Unlock()
			select {
			case c.notified <- struct{}{}:
			default:
			}
			continue
		}
		if m.Method != "" {
			if len(m.ID) > 0 {
				r := serverRequest{id: m.ID, method: m.Method, params: m.Params}
				c.mu.Lock()
				c.serverRequests = append(c.serverRequests, r)
				c.mu.Unlock()
				go c.answer(r) // not here: the server may be writing to this loop
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[string(m.ID)]
		delete(c.pending, string(m.ID))
		c.mu.Unlock()
		if ch != nil {
			ch <- &response{ID: m.ID, Result: m.Result, Error: m.Error}
		}
	}
}

// answer replies to a request from the server.
func (c *client) answer(r serverRequest) {
	reply := map[string]any{"jsonrpc": "2.0", "id": r.id, "result": nil}
	if c.reply != nil {
		switch v := c.reply(r.method).(type) {
		case nil:
		case *rpcError:
			delete(reply, "result")
			reply["error"] = v
		default:
			reply["result"] = v
		}
	}
	body, err := json.Marshal(reply)
	if err != nil {
		c.t.Errorf("encoding a reply: %v", err)
		return
	}
	_, _ = io.WriteString(c.in, "Content-Length: "+itoa(len(body))+"\r\n\r\n"+string(body)) // the server may be gone
}

// send writes one framed message.
func (c *client) send(v any) {
	c.t.Helper()
	body, err := json.Marshal(v)
	require.NoError(c.t, err)
	_, err = io.WriteString(c.in, "Content-Length: "+itoa(len(body))+"\r\n\r\n"+string(body))
	require.NoError(c.t, err)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func encodeParams(t *testing.T, params any) json.RawMessage {
	t.Helper()
	raw, err := protocol.Marshal(params)
	require.NoError(t, err)
	return raw
}

// request sends a request and returns its response.
func (c *client) request(method string, params any) *response {
	c.t.Helper()
	c.nextID++
	id := json.RawMessage(itoa(c.nextID))
	ch := make(chan *response, 1)
	c.mu.Lock()
	c.pending[string(id)] = ch
	c.mu.Unlock()
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": encodeParams(c.t, params)})
	select {
	case r := <-ch:
		return r
	case <-time.After(testTimeout):
		c.t.Fatalf("no response to %s", method)
	}
	return nil
}

// call sends a request, requires success and decodes the result into out
// (out may be nil).
func (c *client) call(method string, params, out any) {
	c.t.Helper()
	r := c.request(method, params)
	require.Nil(c.t, r.Error, "%s failed", method)
	if out != nil {
		require.NoError(c.t, protocol.Unmarshal(r.Result, out), "%s result %s", method, r.Result)
	}
}

// callRaw sends a request and returns its raw result.
func (c *client) callRaw(method string, params any) json.RawMessage {
	c.t.Helper()
	r := c.request(method, params)
	require.Nil(c.t, r.Error, "%s failed", method)
	return r.Result
}

func (c *client) notify(method string, params any) {
	c.t.Helper()
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": encodeParams(c.t, params)})
}

// sync makes sure every pending edit was analyzed and its diagnostics
// published, by sending a request (requests flush pending edits first).
func (c *client) sync() {
	c.t.Helper()
	c.callRaw("workspace/symbol", &protocol.WorkspaceSymbolParams{Query: "\x00none"})
}

// diagnostics returns the last diagnostics published for u (nil when none
// were published).
func (c *client) diagnostics(u uri.URI) []protocol.Diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.diags[u]; p != nil {
		return p.Diagnostics
	}
	return nil
}

// publishCount returns how many times diagnostics were published for u.
func (c *client) publishCount(u uri.URI) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.published[u]
}

// waitFor waits until cond holds after a publishDiagnostics notification.
func (c *client) waitFor(cond func() bool) bool {
	deadline := time.After(testTimeout)
	for !cond() {
		select {
		case <-c.notified:
		case <-deadline:
			return false
		}
	}
	return true
}

// open sends didOpen with the file's disk content and returns its URI.
func (c *client) open(path string) uri.URI {
	c.t.Helper()
	src, err := os.ReadFile(path)
	require.NoError(c.t, err)
	return c.openText(path, string(src))
}

// openText sends didOpen with the given text.
func (c *client) openText(path, text string) uri.URI {
	c.t.Helper()
	u := fileURIOf(c.t, path)
	c.notify("textDocument/didOpen", &protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{
		URI: u, LanguageID: "markdown", Version: 1, Text: text,
	}})
	return u
}

// change replaces a document's text (full sync).
func (c *client) change(u uri.URI, version int32, text string) {
	c.t.Helper()
	c.notify("textDocument/didChange", &protocol.DidChangeTextDocumentParams{
		TextDocument:   protocol.VersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: u}, Version: version},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{&protocol.TextDocumentContentChangeWholeDocument{Text: text}},
	})
}

func (c *client) close(u uri.URI) {
	c.t.Helper()
	c.notify("textDocument/didClose", &protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: u}})
}

func fileURIOf(t *testing.T, path string) uri.URI {
	t.Helper()
	u, err := fileURI(path)
	require.NoError(t, err)
	return u
}

// copyFixture copies files from testdata/<dir> into a new temp dir and
// returns it; names nil copies every file.
func copyFixture(t *testing.T, dir string, names ...string) string {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", dir)
	if names == nil {
		entries, err := os.ReadDir(src)
		require.NoError(t, err)
		for _, e := range entries {
			if !e.IsDir() && !strings.HasSuffix(e.Name(), ".golden.json") {
				names = append(names, e.Name())
			}
		}
	}
	dst := t.TempDir()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(src, n))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dst, n), b, 0o644))
	}
	return dst
}

// pos is a protocol position.
func lspPos(line, char uint32) protocol.Position {
	return protocol.Position{Line: line, Character: char}
}

// rng is a protocol range on one line.
func lspRange(line, start, end uint32) protocol.Range {
	return protocol.Range{Start: lspPos(line, start), End: lspPos(line, end)}
}

// at builds TextDocumentPositionParams.
func at(u uri.URI, line, char uint32) protocol.TextDocumentPositionParams {
	return protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: u}, Position: lspPos(line, char)}
}

// requireGit skips the test when git is missing.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// gitEnv isolates git from the user's configuration.
func gitEnv() []string {
	return []string{
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	}
}

// git runs git in dir.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main", "-c", "core.autocrlf=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv()...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
}
