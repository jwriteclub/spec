// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// sourceItem owns offsets and decoded paths copied from the parser. Parser
// nodes are ephemeral and must never escape NextExpression.
type sourceItem struct {
	path                      []string
	start, end, valueStart    int
	table, arrayTable, inline bool
	comments                  []unstable.Range
}
type sourceEdit struct {
	start, end int
	text       string
}

func sourceKey(n *unstable.Node) []string {
	var out []string
	it := n.Key()
	for it.Next() {
		out = append(out, string(it.Node().Data))
	}
	return out
}
func pathPrefix(path, prefix []string) bool {
	if len(path) < len(prefix) {
		return false
	}
	for i := range prefix {
		if path[i] != prefix[i] {
			return false
		}
	}
	return true
}
func sourceComments(n *unstable.Node) []unstable.Range {
	var out []unstable.Range
	for c := n.Child(); c != nil; c = c.Next() {
		if c.Kind == unstable.Comment {
			out = append(out, c.Raw)
		}
		out = append(out, sourceComments(c)...)
	}
	return out
}
func indexSource(src []byte) ([]sourceItem, error) {
	var p unstable.Parser
	p.KeepComments = true
	p.Reset(src)
	var items []sourceItem
	var scope []string
	var inArray bool
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			scope = sourceKey(n)
			inArray = n.Kind == unstable.ArrayTable
			it := n.Key()
			it.Next()
			first := it.Node()
			start := int(first.Raw.Offset)
			for start > 0 && src[start-1] != '\n' {
				start--
			}
			end := start
			// Table keys cannot contain a newline. Retain any trailing comment.
			for end < len(src) && src[end] != '\n' && src[end] != '\r' {
				end++
			}
			if c := n.Next(); c != nil && c.Kind == unstable.Comment {
				end = int(c.Raw.Offset)
			}
			for end > start && (src[end-1] == ' ' || src[end-1] == '\t') {
				end--
			}
			items = append(items, sourceItem{path: append([]string(nil), scope...), start: start, end: end, table: true, arrayTable: inArray})
		case unstable.KeyValue:
			var add func(*unstable.Node, []string, bool)
			add = func(kv *unstable.Node, prefix []string, inline bool) {
				path := append(append([]string(nil), prefix...), sourceKey(kv)...)
				start, end := int(kv.Raw.Offset), int(kv.Raw.Offset+kv.Raw.Length)
				// The final key token locates '=' without interpreting string contents.
				keys := kv.Key()
				keyEnd := start
				for keys.Next() {
					k := keys.Node()
					keyEnd = int(k.Raw.Offset + k.Raw.Length)
				}
				valueStart := keyEnd
				for valueStart < end && src[valueStart] != '=' {
					valueStart++
				}
				valueStart++
				for valueStart < end && (src[valueStart] == ' ' || src[valueStart] == '\t') {
					valueStart++
				}
				items = append(items, sourceItem{path: path, start: start, end: end, valueStart: valueStart, inline: inline, arrayTable: inArray, comments: sourceComments(kv.Value())})
				if kv.Value().Kind == unstable.InlineTable {
					for c := kv.Value().Child(); c != nil; c = c.Next() {
						if c.Kind == unstable.KeyValue {
							add(c, path, true)
						}
					}
				}
			}
			add(n, scope, false)
		}
	}
	return items, p.Error()
}
func quotedPath(path []string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = strconv.Quote(p)
	}
	return strings.Join(parts, ".")
}
func sourceValue(v any) (string, error) {
	b, err := toml.Marshal(map[string]any{"value": v})
	if err != nil {
		return "", err
	}
	items, err := indexSource(b)
	if err != nil {
		return "", err
	}
	if len(items) == 1 && !items[0].table {
		return string(b[items[0].valueStart:items[0].end]), nil
	}
	return "", fmt.Errorf("value requires a table")
}

// sourceBytes edits only affected expressions; it never rewrites the document
// from its map. Unsupported edits fail before Save can touch the destination.
func (d *Document) sourceBytes() ([]byte, error) {
	if d.LoadError != nil {
		return nil, d.LoadError
	}
	if len(d.Review()) == 0 {
		return bytes.Clone(d.source), nil
	}
	items, err := indexSource(d.source)
	if err != nil {
		return nil, err
	}
	newline := "\n"
	if bytes.Contains(d.source, []byte("\r\n")) {
		newline = "\r\n"
	}
	var edits []sourceEdit
	insertions := map[int]string{}
	refuse := func(path []string, why string) ([]byte, error) {
		target := "document " + d.Path
		if len(path) > 0 {
			target = quotedPath(path) + " in " + d.Path
		}
		return nil, fmt.Errorf("cannot safely edit %s: %s; edit this value in the TOML source, then reload Settings (draft retained)", target, why)
	}
	for _, change := range d.Review() {
		var exact *sourceItem
		for i := range items {
			it := &items[i]
			if !it.table && len(it.path) == len(change.Path) && pathPrefix(it.path, change.Path) {
				exact = it
				break
			}
		}
		if exact != nil && change.AfterSet {
			if exact.arrayTable {
				return refuse(change.Path, "array-of-tables editing is unsupported")
			}
			value, e := sourceValue(change.After)
			if e != nil {
				return refuse(change.Path, "changing an inline value into a table is unsupported")
			}
			if len(exact.comments) > 0 {
				return refuse(change.Path, "the replaced value contains comments")
			}
			edits = append(edits, sourceEdit{exact.valueStart, exact.end, value})
			continue
		}
		if change.BeforeSet {
			found := false
			for _, it := range items {
				if !pathPrefix(it.path, change.Path) || it.inline && exact == nil {
					continue
				}
				if it.arrayTable || it.inline {
					return refuse(change.Path, "removing an array-of-tables or inline-table member is unsupported")
				}
				found = true
				// Preserve comments inside a removed multiline value as standalone lines;
				// trailing and preceding comments already live outside the edited span.
				var retained strings.Builder
				for _, c := range it.comments {
					retained.Write(d.source[c.Offset : c.Offset+c.Length])
					retained.WriteString(newline)
				}
				edits = append(edits, sourceEdit{it.start, it.end, retained.String()})
			}
			if !found {
				return refuse(change.Path, "no independent source expression was found")
			}
		}
		if !change.AfterSet {
			continue
		}
		if m, ok := change.After.(map[string]any); ok {
			// A newly added table is serialized in isolation; existing tables, values,
			// comments and spelling remain untouched.
			var nested any = m
			for i := len(change.Path) - 1; i >= 0; i-- {
				nested = map[string]any{change.Path[i]: nested}
			}
			b, e := toml.Marshal(nested)
			if e != nil {
				return nil, e
			}
			// The wrapping ancestors already exist (Review descends only
			// into tables present on both sides), and each wrapper has no
			// keys of its own, so Marshal opens the text with one bare header
			// per ancestor. Re-declaring an ancestor such as [providers] is
			// an error when the source declares it, or when a second new
			// table repeats it, so drop those headers; the subtable header
			// that follows names the full path.
			text := string(b)
			for range len(change.Path) - 1 {
				line, rest, ok := strings.Cut(text, "\n")
				if !ok || !strings.HasPrefix(line, "[") {
					break
				}
				text = rest
			}
			insertions[len(d.source)] += newline + strings.ReplaceAll(text, "\n", newline)
			continue
		}
		value, e := sourceValue(change.After)
		if e != nil {
			return refuse(change.Path, e.Error())
		}
		// Use the most specific explicit table; root assignments precede all tables.
		at, depth := len(d.source), 0
		for _, it := range items {
			if it.table {
				at = it.start
				break
			}
		}
		for i, it := range items {
			if !it.table || it.arrayTable || len(it.path) >= len(change.Path) || !pathPrefix(change.Path, it.path) || len(it.path) <= depth {
				continue
			}
			depth = len(it.path)
			at = len(d.source)
			for _, next := range items[i+1:] {
				if next.table {
					at = next.start
					break
				}
			}
		}
		insertions[at] += quotedPath(change.Path[depth:]) + " = " + value + newline
	}
	for at, text := range insertions {
		if at > 0 && d.source[at-1] != '\n' {
			text = newline + text
		}
		edits = append(edits, sourceEdit{at, at, text})
	}
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].start == edits[j].start {
			return edits[i].end < edits[j].end
		}
		return edits[i].start < edits[j].start
	})
	var out bytes.Buffer
	pos := 0
	for _, e := range edits {
		if e.start < pos {
			return refuse(nil, "overlapping edits")
		}
		out.Write(d.source[pos:e.start])
		out.WriteString(e.text)
		pos = e.end
	}
	out.Write(d.source[pos:])
	candidate := out.Bytes()
	var decoded map[string]any
	if err := toml.Unmarshal(candidate, &decoded); err != nil {
		return refuse(nil, "the source layout cannot represent these edits: "+err.Error())
	}
	// Removing the last child can erase an implicit parent from TOML even
	// though the staged map deliberately retains that empty table. Materialize
	// only those surviving empty ancestors of removals. Explicit empty tables
	// and every other semantic difference still pass the exact equality guard.
	for _, change := range d.Review() {
		if change.AfterSet {
			continue
		}
		for depth := 1; depth < len(change.Path); depth++ {
			path := change.Path[:depth]
			value, exists := d.Get(path...)
			table, isTable := value.(map[string]any)
			if !exists || !isTable || len(table) != 0 {
				continue
			}
			var current any = decoded
			present := true
			for _, key := range path {
				parent, ok := current.(map[string]any)
				if !ok {
					break // a conflicting scalar must fail equality, not be repaired
				}
				current, present = parent[key]
				if !present {
					break
				}
			}
			if present {
				continue
			}
			out.WriteString(newline + "[" + quotedPath(path) + "]" + newline)
			candidate = out.Bytes()
			if err := toml.Unmarshal(candidate, &decoded); err != nil {
				return refuse(nil, "the source layout cannot preserve an emptied parent table: "+err.Error())
			}
		}
	}
	if !sameMeaning(decoded, d.Values) {
		return refuse(nil, "edited source does not exactly match the staged values")
	}
	return candidate, nil
}
