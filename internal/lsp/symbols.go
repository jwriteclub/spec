// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"sort"
	"strings"

	"go.lsp.dev/protocol"

	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// ---- semantic tokens ----

// token is one semantic token before encoding.
type token struct {
	rng  pos.Range
	typ  uint32
	mods uint32
}

// anchorToken returns the token type for an anchor kind.
func anchorToken(k mdspec.Kind) uint32 {
	if k == mdspec.KindDefinition {
		return tokenDefinition
	}
	return tokenInvariant
}

// fileTokens lists the semantic tokens of f: $Name tokens colored by the
// kind of anchor they name (dangling ones are left alone), § references and
// section numbers, with the declaration modifier on definition sites.
func fileTokens(spec *mdspec.Spec, f *mdspec.File) []token {
	var out []token
	for _, o := range f.Occurrences {
		if o.Role == mdspec.RoleDefinition {
			out = append(out, token{rng: o.Range, typ: anchorToken(o.Defines.Kind), mods: modDeclaration})
			continue
		}
		rr := spec.Resolve(o)
		if rr == nil {
			continue
		}
		switch rr.State {
		case mdspec.Resolved:
			out = append(out, token{rng: o.Range, typ: anchorToken(rr.Target.Kind)})
		case mdspec.Ambiguous:
			out = append(out, token{rng: o.Range, typ: anchorToken(rr.Candidates[0].Kind)})
		}
	}
	for _, r := range f.SectionRefs {
		if rr := spec.ResolveSectionRef(r); rr != nil && rr.State != mdspec.Dangling {
			out = append(out, token{rng: r.Range, typ: tokenSection})
		}
	}
	for _, sec := range f.Sections {
		out = append(out, token{rng: numberRange(spec, sec), typ: tokenSection, mods: modDeclaration})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].rng.Start < out[j].rng.Start })
	return out
}

func (s *Server) semanticTokens(p *protocol.SemanticTokensParams) (any, *rpcError) {
	v, ok := s.viewOf(p.TextDocument.URI)
	if !ok {
		return &protocol.SemanticTokens{Data: []uint32{}}, nil
	}
	x := v.file.Index()
	data := []uint32{}
	var prevLine, prevChar uint32
	end := -1
	for _, t := range fileTokens(v.snap.spec, v.file) {
		if t.rng.Start < end {
			continue // overlapping tokens are not allowed
		}
		r := s.conv.rng(x, t.rng)
		if r.Start.Line != r.End.Line || r.End.Character <= r.Start.Character {
			continue // tokens never span lines
		}
		end = t.rng.End
		dl := r.Start.Line - prevLine
		dc := r.Start.Character
		if dl == 0 {
			dc -= prevChar
		}
		data = append(data, dl, dc, r.End.Character-r.Start.Character, t.typ, t.mods)
		prevLine, prevChar = r.Start.Line, r.Start.Character
	}
	return &protocol.SemanticTokens{Data: data}, nil
}

// ---- document and workspace symbols ----

// anchorSymbolKind maps an anchor kind to a symbol kind, matching the
// semantic token types (variable and type).
func anchorSymbolKind(k mdspec.Kind) protocol.SymbolKind {
	if k == mdspec.KindDefinition {
		return protocol.SymbolKindClass
	}
	return protocol.SymbolKindVariable
}

func (s *Server) documentSymbol(p *protocol.DocumentSymbolParams) (any, *rpcError) {
	v, ok := s.viewOf(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	return s.outline(v.file), nil
}

// symNode is a document symbol under construction.
type symNode struct {
	sym      protocol.DocumentSymbol
	rng      pos.Range
	level    int // heading level; 0 for anchors
	children []*symNode
}

func (n *symNode) build() protocol.DocumentSymbol {
	out := n.sym
	for _, c := range n.children {
		out.Children = append(out.Children, c.build())
	}
	return out
}

// outline nests numbered sections by heading level and places each anchor
// under the innermost section that contains its body.
func (s *Server) outline(f *mdspec.File) []protocol.DocumentSymbol {
	x := f.Index()
	var nodes []*symNode
	for _, sec := range f.Sections {
		detail := sec.Title
		name := "§" + sec.Number
		nodes = append(nodes, &symNode{
			sym: protocol.DocumentSymbol{
				Name: name, Detail: &detail, Kind: protocol.SymbolKindNamespace,
				Range: s.conv.rng(x, sec.Range), SelectionRange: s.conv.rng(x, sec.HeadingRange),
			},
			rng: sec.Range, level: sec.Level,
		})
	}
	for _, a := range f.Anchors {
		n := &symNode{
			sym: protocol.DocumentSymbol{
				Name: "$" + a.Name, Kind: anchorSymbolKind(a.Kind),
				Range: s.conv.rng(x, a.BodyRange), SelectionRange: s.conv.rng(x, a.NameRange),
			},
			rng: a.BodyRange,
		}
		if a.Title != "" {
			t := a.Title
			n.sym.Detail = &t
		}
		nodes = append(nodes, n)
	}
	// Source order; a section sorts before an anchor starting at the same
	// byte (a section-form anchor's heading).
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].rng.Start != nodes[j].rng.Start {
			return nodes[i].rng.Start < nodes[j].rng.Start
		}
		return nodes[i].level > nodes[j].level
	})
	var roots []*symNode
	var stack []*symNode
	for _, n := range nodes {
		for len(stack) > 0 {
			top := stack[len(stack)-1]
			if top.rng.ContainsRange(n.rng) && (n.level == 0 || n.level > top.level) {
				break
			}
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, n)
		} else {
			top := stack[len(stack)-1]
			top.children = append(top.children, n)
		}
		if n.level > 0 {
			stack = append(stack, n)
		}
	}
	out := []protocol.DocumentSymbol{}
	for _, n := range roots {
		out = append(out, n.build())
	}
	return out
}

func (s *Server) workspaceSymbol(p *protocol.WorkspaceSymbolParams) (any, *rpcError) {
	q := strings.ToLower(strings.TrimPrefix(p.Query, "$"))
	roots := make([]string, 0, len(s.projects))
	for root := range s.projects {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	out := []protocol.WorkspaceSymbol{}
	for _, root := range roots {
		ps := s.projects[root]
		s.fresh(ps)
		if s.projects[root] == nil || ps.snap == nil {
			continue
		}
		for _, a := range ps.snap.spec.Anchors {
			if q != "" && !strings.Contains(strings.ToLower(a.Name), q) {
				continue
			}
			loc, ok := s.location(ps, a.File, a.NameRange)
			if !ok {
				continue
			}
			ws := protocol.WorkspaceSymbol{Location: &loc}
			ws.Name = "$" + a.Name
			ws.Kind = anchorSymbolKind(a.Kind)
			container := a.File
			if a.Title != "" {
				container = a.Title + " (" + a.File + ")"
			}
			ws.ContainerName = &container
			out = append(out, ws)
		}
	}
	return out, nil
}
