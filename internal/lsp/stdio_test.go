// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
)

// buildSpec builds cmd/spec into a temp dir and returns the binary's path.
func buildSpec(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "spec")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "build", "-o", bin, "github.com/jwriteclub/spec/cmd/spec")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build: %s", out)
	return bin
}

// lockedBuffer is a bytes.Buffer safe to read while the process writes it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// stdioSession is the real binary running `spec lsp`.
type stdioSession struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	stderr *lockedBuffer
	cancel context.CancelFunc
}

func startStdio(t *testing.T, bin string) *stdioSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, bin, "lsp")
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	require.NoError(t, cmd.Start())
	s := &stdioSession{t: t, cmd: cmd, stdin: stdin, out: bufio.NewReader(stdout), stderr: stderr, cancel: cancel}
	t.Cleanup(func() {
		cancel() // kills the process if a test left it running
		_ = cmd.Wait()
	})
	return s
}

func (s *stdioSession) send(id int, method string, params any) {
	s.t.Helper()
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		m["params"] = encodeParams(s.t, params)
	}
	if id > 0 {
		m["id"] = id
	}
	body, err := json.Marshal(m)
	require.NoError(s.t, err)
	_, err = s.stdin.Write([]byte("Content-Length: " + itoa(len(body)) + "\r\n\r\n" + string(body)))
	require.NoError(s.t, err)
}

// response reads messages until the response to id, skipping
// notifications.
func (s *stdioSession) response(id int) json.RawMessage {
	s.t.Helper()
	for {
		body, err := readMessage(s.out)
		require.NoError(s.t, err, "stderr: %s", s.stderr.String())
		var m struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		require.NoError(s.t, json.Unmarshal(body, &m))
		if m.Method != "" || m.ID == nil || *m.ID != id {
			continue
		}
		require.Nil(s.t, m.Error)
		return m.Result
	}
}

// wait closes stdin and waits for the process, returning its exit code.
func (s *stdioSession) wait() int {
	s.t.Helper()
	_ = s.stdin.Close()
	err := s.cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	require.NoError(s.t, err)
	return s.cmd.ProcessState.ExitCode()
}

// TestStdioBinary is acceptance criterion 3: the built binary answers
// initialize, didOpen, hover, shutdown and exit over stdio and exits 0.
func TestStdioBinary(t *testing.T) {
	bin := buildSpec(t)
	dir, hls, _, _ := multi(t)

	s := startStdio(t, bin)
	s.send(1, "initialize", &protocol.InitializeParams{})
	var init protocol.InitializeResult
	require.NoError(t, protocol.Unmarshal(s.response(1), &init))
	assert.Equal(t, protocol.PositionEncodingKindUTF16, init.Capabilities.PositionEncoding)
	assert.Equal(t, "spec", init.ServerInfo.Name)
	s.send(0, "initialized", &protocol.InitializedParams{})
	s.send(0, "textDocument/didOpen", &protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{
		URI: hls, LanguageID: "markdown", Version: 1, Text: readFile(t, filepath.Join(dir, "hls.spec.md")),
	}})
	s.send(2, "textDocument/hover", &protocol.HoverParams{TextDocumentPositionParams: at(hls, 7, 80)})
	var h protocol.Hover
	require.NoError(t, protocol.Unmarshal(s.response(2), &h))
	mc, ok := h.Contents.(*protocol.MarkupContent)
	require.True(t, ok)
	assert.Contains(t, mc.Value, "**definition** `$Rws.Session` (session)")
	s.send(3, "shutdown", nil)
	assert.Equal(t, "null", string(s.response(3)))
	s.send(0, "exit", nil)
	assert.Equal(t, 0, s.wait(), "clean exit; stderr: %s", s.stderr.String())
	assert.Empty(t, s.stderr.String())

	// exit without shutdown exits 1, as the protocol asks.
	s = startStdio(t, bin)
	s.send(1, "initialize", &protocol.InitializeParams{})
	s.response(1)
	s.send(0, "exit", nil)
	assert.Equal(t, 1, s.wait())
}
