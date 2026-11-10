// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// JSON-RPC 2.0 and LSP error codes.
const (
	codeParseError       = -32700
	codeInvalidRequest   = -32600
	codeMethodNotFound   = -32601
	codeInvalidParams    = -32602
	codeInternalError    = -32603
	codeServerNotInit    = -32002
	codeRequestFailed    = -32803
	maxMessageBytes      = 64 << 20
	headerContentLength  = "content-length"
	jsonrpcVersionString = "2.0"
)

// message is one incoming JSON-RPC message: a request (ID and Method), a
// notification (Method only) or a response to a request we sent (ID, with
// Result or Error).
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// isRequest reports whether m expects a response.
func (m *message) isRequest() bool {
	return len(m.ID) > 0 && !bytes.Equal(m.ID, []byte("null"))
}

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// requestFailed builds the LSP RequestFailed error a handler returns when a
// valid request cannot be carried out (for example a rename onto an existing
// name).
func requestFailed(format string, args ...any) *rpcError {
	return &rpcError{Code: codeRequestFailed, Message: fmt.Sprintf(format, args...)}
}

// response is an outgoing response. Exactly one of Result and Error is set;
// Result holds "null" for an empty result.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// notification is an outgoing notification.
type notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// request is an outgoing request.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// readMessage reads one Content-Length framed message. It returns io.EOF
// when the stream ends cleanly between messages.
func readMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	sawHeader := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && !sawHeader && line == "" {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("reading message header: %w", err)
		}
		sawHeader = true
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("malformed message header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), headerContentLength) {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("bad Content-Length %q", value)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, errors.New("message without Content-Length header")
	}
	if length > maxMessageBytes {
		return nil, fmt.Errorf("message of %d bytes exceeds the %d byte limit", length, maxMessageBytes)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("reading message body: %w", err)
	}
	return body, nil
}

// writer frames and writes outgoing messages; it is safe for concurrent use.
type writer struct {
	mu  sync.Mutex
	out io.Writer
}

// write marshals v with encoding/json (the envelope only; payloads are
// already encoded) and writes it with a Content-Length header.
func (w *writer) write(v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	body := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := fmt.Fprintf(w.out, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err := w.out.Write(body)
	return err
}
