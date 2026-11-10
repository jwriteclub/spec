// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"fmt"

	"go.lsp.dev/protocol"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// codeAction offers quick fixes for the section-hash-ref and ref-nearly
// diagnostics that overlap the requested range: "#5.2" becomes "§5.2" (or
// a qualified "§stem:5.2" per candidate when it is ambiguous), and an
// invalid name that differs from a defined one only in case becomes that
// name.
func (s *Server) codeAction(p *protocol.CodeActionParams) (any, *rpcError) {
	v, ok := s.viewOf(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	x := v.file.Index()
	want := pos.Range{Start: s.conv.offset(x, p.Range.Start), End: s.conv.offset(x, p.Range.End)}
	out := []protocol.CommandOrCodeAction{}
	for _, d := range v.snap.diags[v.file.Path] {
		// An empty request range (a cursor) touching the diagnostic counts.
		if !(d.Range.Overlaps(want) || (want.Empty() && want.Start >= d.Range.Start && want.Start <= d.Range.End)) {
			continue
		}
		switch d.Code {
		case diag.CodeSectionHashRef:
			out = append(out, s.hashRefFixes(v, d)...)
		case diag.CodeRefNearly:
			out = append(out, s.nearlyFixes(v, d)...)
		case diag.CodeSpell:
			out = append(out, s.spellFixes(v, d)...)
		}
	}
	return out, nil
}

// quickFix builds a quick fix that replaces one range of the view's file.
func (s *Server) quickFix(v view, d diag.Diagnostic, title string, r pos.Range, text string, preferred bool) *protocol.CodeAction {
	kind := protocol.CodeActionKindQuickFix
	out := v.file.Path
	we := s.workspaceEdit(v.p, []edit.Output{{Path: out, Splices: []edit.Splice{{Range: r, Old: v.file.Text(r), New: text}}}})
	ca := &protocol.CodeAction{
		Title:       title,
		Kind:        &kind,
		Diagnostics: []protocol.Diagnostic{s.lspDiagnostic(v.p, v.file.Index(), d)},
		Edit:        we,
	}
	if preferred {
		ca.IsPreferred = &preferred
	}
	return ca
}

func (s *Server) hashRefFixes(v view, d diag.Diagnostic) []protocol.CommandOrCodeAction {
	var h *mdspec.HashRef
	for i := range v.file.HashRefs {
		if v.file.HashRefs[i].Range == d.Range {
			h = &v.file.HashRefs[i]
			break
		}
	}
	if h == nil {
		return nil
	}
	state, target, cands := v.snap.spec.ResolveSection(v.file.Path, "", h.Number)
	switch state {
	case mdspec.Resolved:
		title := fmt.Sprintf("Write %s%s (section %s)", sectionSign, h.Number, target.Title)
		hash := pos.Range{Start: h.Range.Start, End: h.Range.Start + 1}
		return []protocol.CommandOrCodeAction{s.quickFix(v, d, title, hash, sectionSign, true)}
	case mdspec.Ambiguous:
		var out []protocol.CommandOrCodeAction
		for _, c := range cands {
			q := c.Qualified()
			title := fmt.Sprintf("Write %s (section %s in %s)", q, c.Title, c.File)
			out = append(out, s.quickFix(v, d, title, h.Range, q, false))
		}
		return out
	}
	return nil
}

func (s *Server) nearlyFixes(v view, d diag.Diagnostic) []protocol.CommandOrCodeAction {
	for _, c := range v.file.Candidates {
		if c.Range != d.Range {
			continue
		}
		n, ok := lint.SuggestName(v.snap.spec, c.Text)
		if !ok || n.Prefix {
			// A prefix match ("$Frame.Timing.x") has no safe rewrite: the
			// rest of the candidate may be prose.
			return nil
		}
		title := fmt.Sprintf("Change to $%s", n.Name)
		return []protocol.CommandOrCodeAction{s.quickFix(v, d, title, c.Range, "$"+n.Name, true)}
	}
	return nil
}
