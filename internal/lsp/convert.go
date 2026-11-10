// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"errors"
	"path/filepath"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/pos"
)

// conv converts byte offsets to LSP positions and back in the negotiated
// position encoding: UTF-8 when the client offers it, UTF-16 otherwise.
type conv struct {
	utf8 bool
}

// bomWidth is how many code units a leading byte order mark takes in the
// negotiated encoding. Editors drop the mark when they load a file, so
// positions on the first line of a source that starts with one are shifted
// left by this much (and offsets right): a rename or diagnostic in a file the
// editor has not opened then lands where the editor sees the text.
func (c conv) bomWidth(x *pos.Index) int {
	if !x.HasBOM() {
		return 0
	}
	if c.utf8 {
		return 3
	}
	return 1
}

// position converts a byte offset in the source indexed by x.
func (c conv) position(x *pos.Index, off int) protocol.Position {
	var p pos.LSPPosition
	if c.utf8 {
		p = x.LSPUTF8(off)
	} else {
		p = x.LSP(off)
	}
	if p.Line == 0 {
		p.Character = max(p.Character-c.bomWidth(x), 0)
	}
	return protocol.Position{Line: uint32(p.Line), Character: uint32(p.Character)}
}

// rng converts a byte range.
func (c conv) rng(x *pos.Index, r pos.Range) protocol.Range {
	return protocol.Range{Start: c.position(x, r.Start), End: c.position(x, r.End)}
}

// offset converts an LSP position to a byte offset (clamped to the source).
func (c conv) offset(x *pos.Index, p protocol.Position) int {
	lp := pos.LSPPosition{Line: int(p.Line), Character: int(p.Character)}
	if lp.Line == 0 {
		lp.Character += c.bomWidth(x)
	}
	if c.utf8 {
		return x.FromLSPUTF8(lp)
	}
	return x.FromLSP(lp)
}

// errNotFile says a URI does not name a local file.
var errNotFile = errors.New("not a file URI")

// pathOf returns the cleaned absolute file system path of a file URI.
func pathOf(u uri.URI) (string, error) {
	if u.Scheme() != "file" {
		return "", errNotFile
	}
	p := u.FsPath()
	if p == "" || !filepath.IsAbs(p) {
		return "", errNotFile
	}
	return filepath.Clean(p), nil
}

// fileURI returns the file URI of an absolute path.
func fileURI(abs string) (uri.URI, error) {
	return uri.From(uri.Components{Scheme: "file", Path: filepath.ToSlash(abs)})
}

// severity maps a diagnostic severity to the LSP's.
func severity(s diag.Severity) protocol.DiagnosticSeverity {
	switch s {
	case diag.Error:
		return protocol.DiagnosticSeverityError
	case diag.Warning:
		return protocol.DiagnosticSeverityWarning
	case diag.Hint:
		return protocol.DiagnosticSeverityHint
	}
	return protocol.DiagnosticSeverityInformation
}
