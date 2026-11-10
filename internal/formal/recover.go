// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package formal

import (
	"sort"

	"github.com/jwriteclub/spec/internal/pos"
)

// Recover fills in what a syntax error hid from cur, a scan of text being
// edited, from good, the last error-free scan of the same file. A parser that
// stops at the first error (SANY does) drops every declaration after it, so a
// moment of typing would empty the outline below the cursor.
//
// The declarations cur has stay as they are, with the tags before the end of
// its last one. After that end come good's declarations and tags, moved by the
// length of the edit: everything outside the span where good's text and cur's
// differ keeps its bytes, so its ranges map exactly. A declaration whose name
// that span touches, or whose bounds it cuts, goes to Stale; one that holds
// the whole span is kept, edited inside. A tag the span touches is dropped,
// and cur's own reading of that text stays, bound to nothing. A tag bound to
// a stale declaration is kept unbound.
//
// It returns cur itself when cur has no error, or good is nil or has one. It
// changes neither file: a scan may be shared.
func Recover(cur, good *File) *File {
	if good == nil || !cur.HasError() || good.HasError() || good.Path != cur.Path {
		return cur
	}
	m := diffOf(good.src, cur.src)
	cut := 0
	for _, d := range cur.Decls {
		cut = max(cut, d.Range.End)
	}
	out := *cur
	out.Decls = append([]*Decl(nil), cur.Decls...)
	out.Stale = nil
	moved := map[*Decl]*Decl{}
	for _, d := range cur.Decls {
		moved[d] = d
	}
	// good's declarations before cut that cur also has, so a tag after cut
	// keeps its binding to one.
	for _, d := range good.Decls {
		if nr, ok := m.exact(d.NameRange); ok {
			if nd := cur.DeclAt(nr.Start); nd != nil && nd.NameRange == nr && nd.Name == d.Name {
				moved[d] = nd
			}
		}
	}
	for _, d := range good.Decls {
		if _, have := moved[d]; have {
			continue
		}
		nr, okName := m.exact(d.NameRange)
		r, okRange := m.around(d.Range)
		if okName && okRange {
			if nr.Start < cut {
				continue // where cur's own declarations are
			}
			nd := &Decl{Name: d.Name, Kind: d.Kind, NameRange: nr, Range: r}
			out.Decls = append(out.Decls, nd)
			moved[d] = nd
			continue
		}
		near := m.near(d.NameRange.Start)
		if near < cut {
			continue
		}
		out.Stale = append(out.Stale, &Decl{Name: d.Name, Kind: d.Kind, NameRange: pos.Range{Start: near, End: near}, Range: pos.Range{Start: near, End: near}})
	}
	out.Tags = nil
	for _, t := range cur.Tags {
		if t.Range.Start < cut {
			out.Tags = append(out.Tags, t)
		}
	}
	placed := map[int]bool{}
	for _, t := range good.Tags {
		nt, ok := m.mapTag(t)
		if !ok || nt.Range.Start < cut {
			continue
		}
		nt.Decl = moved[t.Decl] // nil when unbound or stale
		out.Tags = append(out.Tags, nt)
		placed[nt.Range.Start] = true
	}
	for _, t := range cur.Tags {
		if t.Range.Start >= cut && !placed[t.Range.Start] {
			nt := *t
			nt.Decl = nil // bound against the declarations the error hid
			out.Tags = append(out.Tags, &nt)
		}
	}
	sort.SliceStable(out.Tags, func(i, j int) bool { return out.Tags[i].Range.Start < out.Tags[j].Range.Start })
	byName := func(ds []*Decl) {
		sort.SliceStable(ds, func(i, j int) bool { return ds[i].NameRange.Start < ds[j].NameRange.Start })
	}
	byName(out.Decls)
	byName(out.Stale)
	return &out
}

// diff maps offsets of an old text to a new one that differs from it in one
// span: old[prefix:oldEnd] became new[prefix:oldEnd+delta].
type diff struct {
	prefix, oldEnd, delta int
}

// diffOf finds the span where old and new differ: the bytes before it and
// after it are the same in both.
func diffOf(old, new []byte) diff {
	n := min(len(old), len(new))
	p := 0
	for p < n && old[p] == new[p] {
		p++
	}
	s := 0
	for s < n-p && old[len(old)-1-s] == new[len(new)-1-s] {
		s++
	}
	return diff{prefix: p, oldEnd: len(old) - s, delta: len(new) - len(old)}
}

// exact maps r when it lies wholly before or wholly after the changed span,
// so its bytes are the same in both texts.
func (m diff) exact(r pos.Range) (pos.Range, bool) {
	switch {
	case r.End <= m.prefix:
		return r, true
	case r.Start >= m.oldEnd:
		return pos.Range{Start: r.Start + m.delta, End: r.End + m.delta}, true
	}
	return pos.Range{}, false
}

// around maps r as exact does, or when it holds the whole changed span with
// unchanged bytes at both ends: its bounds then still bound the same
// construct, edited inside.
func (m diff) around(r pos.Range) (pos.Range, bool) {
	if out, ok := m.exact(r); ok {
		return out, true
	}
	if r.Start < m.prefix && r.End > m.oldEnd {
		return pos.Range{Start: r.Start, End: r.End + m.delta}, true
	}
	return pos.Range{}, false
}

// near returns an offset of the new text close to old offset off: off itself
// before the changed span, moved after it, and the span's start inside it.
func (m diff) near(off int) int {
	switch {
	case off < m.prefix:
		return off
	case off >= m.oldEnd:
		return off + m.delta
	}
	return m.prefix
}

// mapTag returns a copy of t with every range mapped, or false when one does
// not map. The copy keeps t's Decl for the caller to replace.
func (m diff) mapTag(t *Tag) (*Tag, bool) {
	nt := *t
	var ok bool
	if nt.Range, ok = m.exact(t.Range); !ok {
		return nil, false
	}
	if t.Ref != nil {
		ref := *t.Ref
		if ref.Range, ok = m.exact(t.Ref.Range); !ok {
			return nil, false
		}
		nt.Ref = &ref
	}
	if t.Stem != "" {
		if nt.StemRange, ok = m.exact(t.StemRange); !ok {
			return nil, false
		}
	}
	if t.QuoteRange != (pos.Range{}) {
		if nt.QuoteRange, ok = m.exact(t.QuoteRange); !ok {
			return nil, false
		}
	}
	return &nt, true
}
