// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"fmt"
	"sort"

	"go.lsp.dev/protocol"

	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// sectionSign is "§" (U+00A7) in UTF-8.
const sectionSign = "§"

// trigger is the completion context before the cursor.
type trigger struct {
	sigil string    // completionDollar, completionSign or completionHash
	rng   pos.Range // from the sigil to the cursor: what the item replaces
	space bool      // "§ 5": a space follows the sign
}

func isNameByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
}

// completionTrigger finds a $, § or # sigil before off on the same line,
// followed only by characters that can continue a name or section number.
func completionTrigger(src []byte, x *pos.Index, off int) (trigger, bool) {
	lineStart := x.LineStart(x.Line(off))
	i := off
	for i > lineStart && (isNameByte(src[i-1]) || src[i-1] == '.' || src[i-1] == ':' || src[i-1] == '-') {
		i--
	}
	prevOK := func(at int) bool {
		if at <= lineStart {
			return true
		}
		c := src[at-1]
		return !isNameByte(c) && c != '$' && c != '\\'
	}
	if i > lineStart && src[i-1] == '$' && prevOK(i-1) {
		return trigger{sigil: completionDollar, rng: pos.Range{Start: i - 1, End: off}}, true
	}
	if i > lineStart && src[i-1] == '#' {
		h := i - 1
		before := src[lineStart:h]
		onlySpace := true
		for _, c := range before {
			if c != ' ' && c != '\t' {
				onlySpace = false
				break
			}
		}
		// "#" opening a line is a heading in the making; "##", "&#" and
		// "word#" are not section references either.
		if !onlySpace && src[h-1] != '#' && src[h-1] != '&' && !isNameByte(src[h-1]) && src[h-1] != '\\' {
			return trigger{sigil: completionHash, rng: pos.Range{Start: h, End: off}}, true
		}
		return trigger{}, false
	}
	j, space := i, false
	if j > lineStart && src[j-1] == ' ' {
		j--
		space = true
	}
	if j-len(sectionSign) >= lineStart && string(src[j-len(sectionSign):j]) == sectionSign {
		return trigger{sigil: completionSign, rng: pos.Range{Start: j - len(sectionSign), End: off}, space: space}, true
	}
	return trigger{}, false
}

// inOpaque reports whether off lies inside code, HTML or another region
// where no token is produced.
func inOpaque(f *mdspec.File, off int) bool {
	op := f.Opaque
	k := sort.Search(len(op), func(k int) bool { return op[k].Range.Start > off }) - 1
	return k >= 0 && op[k].Range.Contains(off)
}

func (s *Server) completion(p *protocol.CompletionParams) (any, *rpcError) {
	v, ok := s.viewOf(p.TextDocument.URI)
	if !ok {
		return s.formalCompletion(p)
	}
	off := s.offset(v, p.Position)
	tr, ok := completionTrigger(v.file.Source(), v.file.Index(), off)
	if !ok || inOpaque(v.file, tr.rng.Start) {
		return nil, nil
	}
	rng := s.conv.rng(v.file.Index(), tr.rng)
	list := &protocol.CompletionList{Items: []protocol.CompletionItem{}}
	if tr.sigil == completionDollar {
		list.Items = anchorItems(v.snap.spec, rng)
	} else {
		spec := v.snap.spec
		list.Items = sectionItems(spec, v.file.Path, tr, rng, func(number string) (mdspec.Resolution, *mdspec.Section) {
			state, tgt, _ := spec.ResolveSection(v.file.Path, "", number)
			return state, tgt
		})
	}
	return list, nil
}

// anchorItems offers every defined name once, in project order.
func anchorItems(spec *mdspec.Spec, rng protocol.Range) []protocol.CompletionItem {
	items := []protocol.CompletionItem{}
	seen := map[string]bool{}
	for _, a := range spec.Anchors {
		if seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		label := "$" + a.Name
		kind := protocol.CompletionItemKindVariable
		if a.Kind == mdspec.KindDefinition {
			kind = protocol.CompletionItemKindClass
		}
		detail := kindWord(a.Kind)
		if a.Title != "" {
			detail += ": " + a.Title
		}
		items = append(items, protocol.CompletionItem{
			Label:      label,
			Kind:       kind,
			Detail:     protocol.NewOptional(detail),
			SortText:   protocol.NewOptional(fmt.Sprintf("%05d", len(items))),
			FilterText: protocol.NewOptional(label),
			TextEdit:   &protocol.TextEdit{Range: rng, NewText: label},
		})
	}
	return items
}

// sectionItems offers every numbered section, in project order, in the
// shortest form that resolves to it from the current file ("§5.2a", else
// "§stem:5.2a"); unqualified says what a bare number resolves to there. The
// inserted text always uses §, so picking an item after "#" replaces the "#".
func sectionItems(spec *mdspec.Spec, from string, tr trigger, rng protocol.Range, unqualified func(number string) (mdspec.Resolution, *mdspec.Section)) []protocol.CompletionItem {
	items := []protocol.CompletionItem{}
	for _, sec := range spec.Sections {
		ref := sec.Number
		if state, tgt := unqualified(sec.Number); state != mdspec.Resolved || tgt != sec {
			ref = mdspec.StemOf(sec.File) + ":" + sec.Number
			if st, t2, _ := spec.ResolveSection(from, mdspec.StemOf(sec.File), sec.Number); st != mdspec.Resolved || t2 != sec {
				continue // not addressable (duplicate number or stem)
			}
		}
		label := sectionSign + ref
		filter := label
		switch {
		case tr.sigil == completionHash:
			filter = "#" + ref
		case tr.space:
			filter = sectionSign + " " + ref
		}
		detail := sec.Title
		if sec.File != from {
			detail += " (" + sec.File + ")"
		}
		items = append(items, protocol.CompletionItem{
			Label:      label,
			Kind:       protocol.CompletionItemKindModule,
			Detail:     protocol.NewOptional(detail),
			SortText:   protocol.NewOptional(fmt.Sprintf("%05d", len(items))),
			FilterText: protocol.NewOptional(filter),
			TextEdit:   &protocol.TextEdit{Range: rng, NewText: label},
		})
	}
	return items
}
