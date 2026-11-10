// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package lsp is the language server behind `spec lsp`: a thin layer that
// translates the spec model (mdspec, project, lint, baseline, edit) into the
// Language Server Protocol over stdio.
//
// Dependency choice: the protocol types come from go.lsp.dev/protocol
// (v1, generated from the LSP 3.18 meta-model, with a reflection-free codec),
// and the JSON-RPC transport is the small hand-written Content-Length loop in
// rpc.go. That adds three small modules (go.lsp.dev/jsonrpc2 and
// go.lsp.dev/uri, which the protocol package imports, and
// github.com/go-json-experiment/json for its codec). The alternative,
// github.com/tliron/glsp, was last released in 2024 and pulls in commonlog,
// kutil, termenv, gorilla/websocket and sourcegraph/jsonrpc2. Owning the loop
// also keeps the debounce and the request ordering in one goroutine, which
// makes the server deterministic to test.
//
// Concurrency: one goroutine (Run) owns all state. A reader goroutine feeds
// it decoded messages; debounce timers and baseline loads post events back
// to it. Requests are answered in order, and a request about a project with
// pending edits re-analyzes it first, so answers always reflect the buffers.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/tenta-browser/polychromatic"
	"go.lsp.dev/protocol"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/project"
)

var lg *logrus.Entry = polychromatic.GetLogger("lsp")

// DefaultDebounce is how long the server waits after the last edit before it
// re-analyzes a project and republishes diagnostics.
const DefaultDebounce = 150 * time.Millisecond

// baselineTTL is how old a project's git baseline may get before the next
// analysis reloads it in the background (it also reloads on save).
const baselineTTL = 10 * time.Second

// ErrExitWithoutShutdown is returned by Run when the client sends exit
// without a preceding shutdown request (the process should exit with 1).
var ErrExitWithoutShutdown = errors.New("exit notification without a shutdown request")

// Options configures a Server.
type Options struct {
	// Debounce overrides DefaultDebounce when positive.
	Debounce time.Duration
	// Git loads the HEAD baseline for the "changed since HEAD" diagnostics.
	// The zero value runs git from PATH.
	Git baseline.Loader
	// Version is reported as serverInfo.version.
	Version string
	// Spell runs the spell check's model pass over the project at root
	// and caches its verdicts (the spec.spellCheck command); nil turns
	// the command off. The server only shows cached verdicts otherwise.
	Spell func(ctx context.Context, root string) error
}

// Server is one language server session.
type Server struct {
	opts     Options
	debounce time.Duration
	in       *bufio.Reader
	w        *writer
	conv     conv

	// docChanges is true when the client accepts versioned documentChanges
	// in a WorkspaceEdit.
	docChanges bool
	// dynamicWatch is true when the client lets the server register for
	// workspace/didChangeWatchedFiles.
	dynamicWatch bool
	initialized  bool
	shutdown     bool

	// sent holds the method of each request sent to the client and not yet
	// answered, by id. The client's answer is only logged: no request
	// the server sends has a result it needs.
	sent   map[string]string
	nextID int

	docs     map[string]*document     // open documents by absolute path
	projects map[string]*projectState // by absolute root

	incoming chan *message
	readErr  error
	fire     chan fireEvent
	baseCh   chan baseResult
	spellCh  chan spellDone
	done     chan struct{}
	ctx      context.Context
	wg       sync.WaitGroup

	// syncBaseline loads baselines inline instead of in the background
	// (tests only), so diagnostics are deterministic.
	syncBaseline bool
	// onAnalyze, when set, runs after every analysis with its duration
	// (benchmarks only).
	onAnalyze func(time.Duration)
}

// New returns a server reading requests from in and writing to out.
func New(in io.Reader, out io.Writer, opts Options) *Server {
	d := opts.Debounce
	if d <= 0 {
		d = DefaultDebounce
	}
	return &Server{
		opts:     opts,
		debounce: d,
		in:       bufio.NewReaderSize(in, 64<<10),
		w:        &writer{out: out},
		docs:     map[string]*document{},
		projects: map[string]*projectState{},
		sent:     map[string]string{},
		incoming: make(chan *message),
		fire:     make(chan fireEvent),
		baseCh:   make(chan baseResult),
		spellCh:  make(chan spellDone),
		done:     make(chan struct{}),
		ctx:      context.Background(),
	}
}

// Run serves until the client sends exit, the input ends, or ctx is
// cancelled. It returns nil after a clean shutdown and exit or at the end of
// the input, ErrExitWithoutShutdown for an exit without shutdown, and the
// read error when the input cannot be parsed.
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.ctx = ctx
	defer func() {
		cancel()
		close(s.done)
		for _, p := range s.projects {
			p.stopTimer()
		}
		s.wg.Wait()
	}()
	go s.readLoop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-s.incoming:
			if !ok {
				if s.readErr != nil && !errors.Is(s.readErr, io.EOF) {
					return s.readErr
				}
				return nil
			}
			if m.Method == "exit" {
				if !s.shutdown {
					return ErrExitWithoutShutdown
				}
				return nil
			}
			s.handle(m)
		case ev := <-s.fire:
			s.onFire(ev)
		case r := <-s.baseCh:
			s.applyBaseline(r)
		case r := <-s.spellCh:
			s.spellFinished(r)
		}
	}
}

// readLoop decodes messages and hands them to Run. It closes incoming when
// the input ends or cannot be framed.
func (s *Server) readLoop() {
	defer close(s.incoming)
	for {
		body, err := readMessage(s.in)
		if err != nil {
			s.readErr = err
			return
		}
		m := &message{}
		if err := json.Unmarshal(body, m); err != nil {
			lg.WithError(err).Warn("dropping a message that is not JSON")
			_ = s.w.write(response{JSONRPC: jsonrpcVersionString, ID: json.RawMessage("null"),
				Error: &rpcError{Code: codeParseError, Message: err.Error()}})
			continue
		}
		select {
		case s.incoming <- m:
		case <-s.done:
			return
		}
	}
}

// handle dispatches one request or notification.
func (s *Server) handle(m *message) {
	if m.Method == "" {
		s.onResponse(m)
		return
	}
	if !m.isRequest() {
		s.notify(m)
		return
	}
	result, rerr := s.request(m)
	resp := response{JSONRPC: jsonrpcVersionString, ID: m.ID}
	if rerr == nil {
		raw, err := encodeResult(result)
		if err != nil {
			rerr = &rpcError{Code: codeInternalError, Message: err.Error()}
		} else {
			resp.Result = raw
		}
	}
	if rerr != nil {
		resp.Error = rerr
	}
	if err := s.w.write(resp); err != nil {
		lg.WithError(err).Warn("writing a response failed")
	}
}

// encodeResult encodes a handler's result; nil encodes as null.
func encodeResult(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	b, err := protocol.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// sendNotification writes a server-to-client notification.
func (s *Server) sendNotification(method string, params any) {
	raw, err := encodeResult(params)
	if err != nil {
		lg.WithError(err).WithField("method", method).Warn("encoding a notification failed")
		return
	}
	if err := s.w.write(notification{JSONRPC: jsonrpcVersionString, Method: method, Params: raw}); err != nil {
		lg.WithError(err).Warn("writing a notification failed")
	}
}

// sendRequest writes a server-to-client request; the answer reaches onResponse.
func (s *Server) sendRequest(method string, params any) {
	raw, err := encodeResult(params)
	if err != nil {
		lg.WithError(err).WithField("method", method).Warn("encoding a request failed")
		return
	}
	s.nextID++
	if err := s.w.write(request{JSONRPC: jsonrpcVersionString, ID: s.nextID, Method: method, Params: raw}); err != nil {
		lg.WithError(err).Warn("writing a request failed")
		return
	}
	s.sent[strconv.Itoa(s.nextID)] = method
}

// onResponse takes the client's answer to a request. An error is no problem for
// the session: the server goes on without what it asked for.
func (s *Server) onResponse(m *message) {
	method, ok := s.sent[string(m.ID)]
	if !ok {
		return
	}
	delete(s.sent, string(m.ID))
	if m.Error != nil {
		lg.WithField("method", method).WithField("code", m.Error.Code).Info(m.Error.Message)
	}
}

// decode unmarshals request or notification params.
func decode[T any](raw json.RawMessage) (*T, *rpcError) {
	v := new(T)
	if len(raw) == 0 {
		return v, nil
	}
	if err := protocol.Unmarshal(raw, v); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	return v, nil
}

// request answers one request.
func (s *Server) request(m *message) (any, *rpcError) {
	if m.Method == "initialize" {
		if s.initialized {
			return nil, &rpcError{Code: codeInvalidRequest, Message: "initialize was already called"}
		}
		p, rerr := decode[protocol.InitializeParams](m.Params)
		if rerr != nil {
			return nil, rerr
		}
		return s.initialize(p), nil
	}
	if !s.initialized {
		return nil, &rpcError{Code: codeServerNotInit, Message: "the server is not initialized"}
	}
	if s.shutdown {
		return nil, &rpcError{Code: codeInvalidRequest, Message: "the server is shutting down"}
	}
	switch m.Method {
	case "shutdown":
		s.shutdown = true
		return nil, nil
	case "textDocument/hover":
		return handle(m, s.hover)
	case "textDocument/definition":
		return handle(m, s.definition)
	case "textDocument/references":
		return handle(m, s.references)
	case "textDocument/implementation":
		return handle(m, s.implementation)
	case "textDocument/documentHighlight":
		return handle(m, s.documentHighlight)
	case "textDocument/prepareRename":
		return handle(m, s.prepareRename)
	case "textDocument/rename":
		return handle(m, s.rename)
	case "textDocument/completion":
		return handle(m, s.completion)
	case "textDocument/semanticTokens/full":
		return handle(m, s.semanticTokens)
	case "textDocument/documentSymbol":
		return handle(m, s.documentSymbol)
	case "workspace/symbol":
		return handle(m, s.workspaceSymbol)
	case "textDocument/codeAction":
		return handle(m, s.codeAction)
	case "workspace/executeCommand":
		return handle(m, s.executeCommand)
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "method not supported: " + m.Method}
}

// handle decodes a request's params and calls f.
func handle[T any](m *message, f func(*T) (any, *rpcError)) (any, *rpcError) {
	p, rerr := decode[T](m.Params)
	if rerr != nil {
		return nil, rerr
	}
	return f(p)
}

// notify handles one notification.
func (s *Server) notify(m *message) {
	if !s.initialized {
		return
	}
	var rerr *rpcError
	switch m.Method {
	case "initialized":
		s.registerWatchers()
	case "textDocument/didOpen":
		var p *protocol.DidOpenTextDocumentParams
		if p, rerr = decode[protocol.DidOpenTextDocumentParams](m.Params); rerr == nil {
			s.didOpen(p)
		}
	case "textDocument/didChange":
		var p *protocol.DidChangeTextDocumentParams
		if p, rerr = decode[protocol.DidChangeTextDocumentParams](m.Params); rerr == nil {
			s.didChange(p)
		}
	case "textDocument/didClose":
		var p *protocol.DidCloseTextDocumentParams
		if p, rerr = decode[protocol.DidCloseTextDocumentParams](m.Params); rerr == nil {
			s.didClose(p)
		}
	case "textDocument/didSave":
		var p *protocol.DidSaveTextDocumentParams
		if p, rerr = decode[protocol.DidSaveTextDocumentParams](m.Params); rerr == nil {
			s.didSave(p)
		}
	case "workspace/didChangeWatchedFiles":
		var p *protocol.DidChangeWatchedFilesParams
		if p, rerr = decode[protocol.DidChangeWatchedFilesParams](m.Params); rerr == nil {
			s.didChangeWatchedFiles(p)
		}
	}
	if rerr != nil {
		lg.WithField("method", m.Method).Warn(rerr.Message)
	}
}

// Semantic token legend: standard types, so themes color them without
// configuration.
var (
	tokenTypes     = []string{string(protocol.SemanticTokenTypesVariable), string(protocol.SemanticTokenTypesType), string(protocol.SemanticTokenTypesNamespace)}
	tokenModifiers = []string{string(protocol.SemanticTokenModifiersDeclaration)}
)

// Indexes into tokenTypes and bits of tokenModifiers.
const (
	tokenInvariant   = 0 // variable
	tokenDefinition  = 1 // type
	tokenSection     = 2 // namespace
	modDeclaration   = 1 << 0
	completionDollar = "$"
	completionSign   = "§"
	completionHash   = "#"
)

// watchRegistrationID names the one file watch registration.
const watchRegistrationID = "spec-watched-files"

// registerWatchers asks a client that allows it to report changes to spec files,
// models and spec.toml. Without this a client sends workspace/didChangeWatchedFiles
// only if its own configuration says to, and a closed or new file stays unseen
// until the next edit.
func (s *Server) registerWatchers() {
	if !s.dynamicWatch {
		return
	}
	s.dynamicWatch = false // once
	var opts protocol.DidChangeWatchedFilesRegistrationOptions
	for _, glob := range project.ClientWatchGlobs() {
		opts.Watchers = append(opts.Watchers, protocol.FileSystemWatcher{GlobPattern: protocol.Pattern(glob)})
	}
	raw, err := protocol.Marshal(&opts)
	if err != nil {
		lg.WithError(err).Warn("encoding the file watch registration failed")
		return
	}
	s.sendRequest("client/registerCapability", &protocol.RegistrationParams{Registrations: []protocol.Registration{
		{ID: watchRegistrationID, Method: protocol.MethodWorkspaceDidChangeWatchedFiles, RegisterOptions: raw},
	}})
}

// initialize negotiates the position encoding and announces capabilities.
func (s *Server) initialize(p *protocol.InitializeParams) *protocol.InitializeResult {
	s.initialized = true
	enc := protocol.PositionEncodingKindUTF16
	if g := p.Capabilities.General; g != nil {
		for _, e := range g.PositionEncodings {
			if e == protocol.PositionEncodingKindUTF8 {
				enc = protocol.PositionEncodingKindUTF8
				s.conv.utf8 = true
				break
			}
		}
	}
	if w := p.Capabilities.Workspace; w != nil && w.WorkspaceEdit != nil && w.WorkspaceEdit.DocumentChanges != nil {
		s.docChanges = *w.WorkspaceEdit.DocumentChanges
	}
	if w := p.Capabilities.Workspace; w != nil && w.DidChangeWatchedFiles != nil && w.DidChangeWatchedFiles.DynamicRegistration != nil {
		s.dynamicWatch = *w.DidChangeWatchedFiles.DynamicRegistration
	}
	yes := true
	full := protocol.TextDocumentSyncKindFull
	res := &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			PositionEncoding: enc,
			TextDocumentSync: &protocol.TextDocumentSyncOptions{
				OpenClose: &yes,
				Change:    &full,
				Save:      protocol.Boolean(true),
			},
			HoverProvider:             protocol.Boolean(true),
			DefinitionProvider:        protocol.Boolean(true),
			ImplementationProvider:    protocol.Boolean(true),
			ReferencesProvider:        protocol.Boolean(true),
			DocumentHighlightProvider: protocol.Boolean(true),
			DocumentSymbolProvider:    protocol.Boolean(true),
			WorkspaceSymbolProvider:   protocol.Boolean(true),
			RenameProvider:            &protocol.RenameOptions{PrepareProvider: &yes},
			CompletionProvider: &protocol.CompletionOptions{
				TriggerCharacters: []string{completionDollar, completionSign, completionHash},
			},
			SemanticTokensProvider: &protocol.SemanticTokensOptions{
				Legend: protocol.SemanticTokensLegend{TokenTypes: tokenTypes, TokenModifiers: tokenModifiers},
				Full:   protocol.Boolean(true),
			},
			CodeActionProvider: &protocol.CodeActionOptions{
				CodeActionKinds: []protocol.CodeActionKind{protocol.CodeActionKindQuickFix},
			},
			ExecuteCommandProvider: protocol.ExecuteCommandOptions{Commands: s.commands()},
		},
		ServerInfo: protocol.ServerInfo{Name: "spec"},
	}
	if s.opts.Version != "" {
		res.ServerInfo.Version = protocol.NewOptional(s.opts.Version)
	}
	return res
}
