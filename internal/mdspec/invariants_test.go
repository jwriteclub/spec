// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package mdspec

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jwriteclub/spec/internal/pos"
)

// checkFileInvariants asserts the parser properties of acceptance criterion
// 2 on f: every range is in bounds; for valid UTF-8 input, ranges fall on
// rune boundaries, every reference starts with $ (or §), and no token
// overlaps an opaque region.
func checkFileInvariants(t testing.TB, f *File) {
	t.Helper()
	src := f.Source()
	n := len(src)
	type named struct {
		what string
		r    pos.Range
	}
	var rs []named
	add := func(what string, r pos.Range) { rs = append(rs, named{what, r}) }
	for _, a := range f.Anchors {
		add("anchor label "+a.Name, a.LabelRange)
		add("anchor name "+a.Name, a.NameRange)
		add("anchor title "+a.Name, a.TitleRange)
		add("anchor body "+a.Name, a.BodyRange)
		add("anchor hash "+a.Name, a.HashRange())
		if !a.BodyRange.ContainsRange(a.LabelRange) && a.Form == FormParagraph {
			t.Errorf("anchor %s: label %v outside body %v", a.Name, a.LabelRange, a.BodyRange)
		}
		if !a.BodyRange.ContainsRange(a.HashRange()) {
			t.Errorf("anchor %s: hash range %v outside body %v", a.Name, a.HashRange(), a.BodyRange)
		}
		if got := f.Text(a.NameRange); got != "$"+a.Name {
			t.Errorf("anchor %s: name range covers %q", a.Name, got)
		}
		if len(a.Hash) != 64 || a.ShortHash != a.Hash[:8] {
			t.Errorf("anchor %s: bad hash %q / %q", a.Name, a.Hash, a.ShortHash)
		}
		_ = a.ShapeHash()
	}
	for _, o := range f.Occurrences {
		add("occurrence "+o.Name, o.Range)
		add("occurrence container "+o.Name, o.Container.Range)
	}
	for _, s := range f.Sections {
		add("section heading "+s.Number, s.HeadingRange)
		add("section number "+s.Number, s.NumberRange)
		add("section range "+s.Number, s.Range)
	}
	for _, r := range f.SectionRefs {
		add("section ref "+r.Number, r.Range)
		add("section ref container "+r.Number, r.Container.Range)
	}
	for _, c := range f.Candidates {
		add("candidate", c.Range)
	}
	for _, r := range f.MalformedSectionRefs {
		add("malformed section ref", r)
	}
	for _, h := range f.HashRefs {
		add("hash ref", h.Range)
	}
	for _, o := range f.Opaque {
		add("opaque "+string(o.Kind), o.Range)
	}
	for _, d := range f.Diagnostics {
		add("diagnostic "+d.Code, d.Range)
		for _, r := range d.Related {
			if r.File == f.Path {
				add("related "+d.Code, r.Range)
			}
		}
	}
	for _, x := range rs {
		if !x.r.Valid(n) {
			t.Errorf("%s: range %v out of bounds (len %d)", x.what, x.r, n)
		}
	}
	if !f.ValidUTF8 {
		return
	}
	for _, x := range rs {
		if !x.r.Valid(n) {
			continue
		}
		if x.r.Start < n && !utf8.RuneStart(src[x.r.Start]) {
			t.Errorf("%s: range %v starts inside a rune", x.what, x.r)
		}
		if x.r.End < n && !utf8.RuneStart(src[x.r.End]) {
			t.Errorf("%s: range %v ends inside a rune", x.what, x.r)
		}
	}
	var tokens []named
	for _, o := range f.Occurrences {
		txt := f.Text(o.Range)
		if txt != "$"+o.Name {
			t.Errorf("occurrence %s covers %q", o.Name, txt)
		}
		if o.Role == RoleDefinition && (o.Defines == nil || o.Defines.NameRange != o.Range) {
			t.Errorf("definition occurrence %s does not match its anchor", o.Name)
		}
		tokens = append(tokens, named{"occurrence " + o.Name, o.Range})
	}
	for _, r := range f.SectionRefs {
		if !strings.HasPrefix(f.Text(r.Range), "§") {
			t.Errorf("section ref %q does not start with §", f.Text(r.Range))
		}
		tokens = append(tokens, named{"section ref " + r.Number, r.Range})
	}
	for _, c := range f.Candidates {
		if !strings.HasPrefix(f.Text(c.Range), "$") {
			t.Errorf("candidate %q does not start with $", f.Text(c.Range))
		}
		tokens = append(tokens, named{"candidate", c.Range})
	}
	for _, h := range f.HashRefs {
		tokens = append(tokens, named{"hash ref", h.Range})
	}
	for _, tok := range tokens {
		for _, o := range f.Opaque {
			if tok.r.Overlaps(o.Range) {
				t.Errorf("%s %v overlaps opaque %s %v", tok.what, tok.r, o.Kind, o.Range)
			}
		}
	}
}
