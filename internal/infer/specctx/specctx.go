// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package specctx assembles the spec text sent to a model with a check: the
// change or the edited regions, the neighborhood of anchors around them
// (two hops of references, with bodies), the prose that mentions the target,
// an index of every anchor, and the text of the touched files within a
// token budget. Everything is ordered deterministically, so the rendered
// text is golden-testable.
package specctx

import (
	"sort"
	"strings"

	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// Actions of a Change.
const (
	ActionChange = "change"
	ActionAdd    = "add"
	ActionRemove = "remove"
	ActionKind   = "kind"
)

// Change is one proposed anchor change.
type Change struct {
	// Action is change, add, remove or kind (a kind change only).
	Action string
	// Name is the anchor name, without $.
	Name string
	// File is where the anchor is (or will be).
	File    string
	OldKind mdspec.Kind
	NewKind mdspec.Kind
	// OldText is the anchor's text, label included ("" for add).
	OldText string
	// NewText is the proposed text ("" for remove).
	NewText string
	// Why is the author's reason.
	Why string
	// Where describes the place of an added anchor ("after $X").
	Where string
}

// Region is an edited byte range of a file, for analysis. Old is the text
// it replaced ("" when unknown or for a whole file).
type Region struct {
	File  string
	Range pos.Range
	Old   string
}

// Target is what a check is about: a change, or regions or whole files for
// analysis.
type Target struct {
	Change  *Change
	Regions []Region
	// Files are checked whole: they are sent in full (budget permitting)
	// and their anchors' neighbors in other files come along.
	Files []string
}

// Options tune Build.
type Options struct {
	// MaxTokens is the budget, estimated as bytes/4 (0: 60000).
	MaxTokens int
	// Full sends every spec file whole, ignoring the budget.
	Full bool
}

// DefaultMaxTokens is the budget when Options.MaxTokens is 0.
const DefaultMaxTokens = 60000

// RegionText is an edited region as sent.
type RegionText struct {
	File      string
	StartLine int
	EndLine   int
	Text      string
	Old       string
}

// Neighbor is an anchor near the target.
type Neighbor struct {
	Name string
	Kind mdspec.Kind
	File string
	Line int
	// Hops is 1 for direct references either way, 2 for one step further.
	Hops int
	Text string
}

// MentionText is a prose block that references the target.
type MentionText struct {
	File string
	Line int
	Kind mdspec.ContainerKind
	Text string
}

// IndexEntry is one line of the anchor index.
type IndexEntry struct {
	Name    string
	Kind    mdspec.Kind
	File    string
	Summary string
}

// FileText is the text sent for one file: the whole file, or the listed
// sections.
type FileText struct {
	Path string
	Full bool
	// Parts are (heading, text) pieces when Full is false.
	Parts []Part
	Text  string
}

// Part is one excerpt of a file.
type Part struct {
	Label string
	Line  int
	Text  string
}

// Context is the assembled text.
type Context struct {
	Change    *Change
	Regions   []RegionText
	Neighbors []Neighbor
	Mentions  []MentionText
	Index     []IndexEntry
	Files     []FileText
	// Omitted names what the budget left out ("file" or "file §N").
	Omitted []string
	// Budget is the token budget used (0 with Full).
	Budget int

	whole []string
}

// Build assembles the context for t from spec.
func Build(spec *mdspec.Spec, t Target, o Options) *Context {
	c := &Context{Change: t.Change, whole: t.Files}
	budget := o.MaxTokens
	if budget <= 0 {
		budget = DefaultMaxTokens
	}
	if !o.Full {
		c.Budget = budget
	}
	order := map[*mdspec.Anchor]int{}
	for i, a := range spec.Anchors {
		order[a] = i
	}

	// Seeds: the hop-0 anchors and the names referenced from the target.
	seed := map[string]bool{}
	direct := map[string]bool{}
	touched := map[string]bool{}
	if ch := t.Change; ch != nil {
		seed[ch.Name] = true
		touched[ch.File] = true
		for _, txt := range []string{ch.OldText, ch.NewText} {
			for _, n := range refNames(txt) {
				if n != ch.Name {
					direct[n] = true
				}
			}
		}
	}
	for _, path := range t.Files {
		f := spec.File(path)
		if f == nil || !f.ValidUTF8 {
			continue
		}
		touched[path] = true
		for _, a := range f.Anchors {
			seed[a.Name] = true
		}
		for _, oc := range f.Occurrences {
			if oc.Role == mdspec.RoleReference {
				direct[oc.Name] = true
			}
		}
	}
	for _, r := range t.Regions {
		f := spec.File(r.File)
		if f == nil {
			continue
		}
		touched[r.File] = true
		start, end := lineBounds(f.Source(), r.Range)
		c.Regions = append(c.Regions, RegionText{
			File: r.File, StartLine: f.Index().Position(start).Line, EndLine: f.Index().Position(max(start, end-1)).Line,
			Text: f.Text(pos.Range{Start: start, End: end}), Old: r.Old,
		})
		for _, a := range f.Anchors {
			if a.BodyRange.Start < r.Range.End && r.Range.Start < a.BodyRange.End || r.Range.Len() == 0 && a.BodyRange.Contains(r.Range.Start) {
				seed[a.Name] = true
			}
		}
		for _, oc := range f.Occurrences {
			if oc.Role == mdspec.RoleReference && r.Range.ContainsRange(oc.Range) {
				direct[oc.Name] = true
			}
		}
	}

	// Two hops of references, both directions.
	hops := map[string]int{}
	for n := range direct {
		if !seed[n] {
			hops[n] = 1
		}
	}
	for _, n := range sortedKeys(seed) {
		for _, m := range neighbors(spec, n) {
			if !seed[m] {
				hops[m] = 1
			}
		}
	}
	var first []string
	for n := range hops {
		first = append(first, n)
	}
	sort.Strings(first)
	for _, n := range first {
		for _, m := range neighbors(spec, n) {
			if _, ok := hops[m]; !ok && !seed[m] {
				hops[m] = 2
			}
		}
	}
	for n, h := range hops {
		as := spec.Lookup(n)
		if len(as) == 0 {
			continue
		}
		for _, a := range as {
			f := spec.File(a.File)
			c.Neighbors = append(c.Neighbors, Neighbor{
				Name: a.Name, Kind: a.Kind, File: a.File, Line: f.Index().Position(a.LabelRange.Start).Line,
				Hops: h, Text: f.Text(a.BodyRange),
			})
			touched[a.File] = true
		}
	}
	sort.SliceStable(c.Neighbors, func(i, j int) bool {
		a, b := c.Neighbors[i], c.Neighbors[j]
		if a.Hops != b.Hops {
			return a.Hops < b.Hops
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})

	// Prose that mentions the target.
	if ch := t.Change; ch != nil {
		for _, m := range spec.Mentions(ch.Name) {
			f := spec.File(m.File)
			c.Mentions = append(c.Mentions, MentionText{
				File: m.File, Line: f.Index().Position(m.Range.Start).Line, Kind: m.Kind, Text: f.Text(m.Range),
			})
			touched[m.File] = true
		}
	}

	for _, a := range spec.Anchors {
		f := spec.File(a.File)
		c.Index = append(c.Index, IndexEntry{Name: a.Name, Kind: a.Kind, File: a.File, Summary: firstSentence(f, a)})
	}

	c.files(spec, touched, o.Full, budget)
	return c
}

// files fills Files: every file with Full, else the touched files whole if
// they fit the budget, else the sections holding the neighborhood.
func (c *Context) files(spec *mdspec.Spec, touched map[string]bool, full bool, budget int) {
	var paths []string
	for _, f := range spec.Files {
		if f.ValidUTF8 && (full || touched[f.Path]) {
			paths = append(paths, f.Path)
		}
	}
	if full {
		for _, p := range paths {
			c.Files = append(c.Files, FileText{Path: p, Full: true, Text: string(spec.File(p).Source())})
		}
		return
	}
	left := budget - c.fixedTokens()
	total := 0
	for _, p := range paths {
		total += tokens(len(spec.File(p).Source()))
	}
	if total <= left {
		for _, p := range paths {
			c.Files = append(c.Files, FileText{Path: p, Full: true, Text: string(spec.File(p).Source())})
		}
		return
	}
	// Over budget: the sections that hold the regions, neighbors and
	// mentions, in file order, until the budget runs out.
	want := map[string][]int{} // path -> offsets of interest
	for _, r := range c.Regions {
		f := spec.File(r.File)
		if off, ok := f.Index().Offset(pos.Position{Line: r.StartLine, Col: 1}); ok {
			want[r.File] = append(want[r.File], off)
		}
	}
	if ch := c.Change; ch != nil {
		for _, a := range spec.Lookup(ch.Name) {
			want[a.File] = append(want[a.File], a.LabelRange.Start)
		}
	}
	for _, n := range c.Neighbors {
		for _, a := range spec.Lookup(n.Name) {
			want[a.File] = append(want[a.File], a.LabelRange.Start)
		}
	}
	for _, m := range c.Mentions {
		f := spec.File(m.File)
		if off, ok := f.Index().Offset(pos.Position{Line: m.Line, Col: 1}); ok {
			want[m.File] = append(want[m.File], off)
		}
	}
	for _, path := range c.whole {
		if f := spec.File(path); f != nil {
			for _, sec := range f.Sections {
				want[path] = append(want[path], sec.Range.Start)
			}
		}
	}
	for _, p := range paths {
		f := spec.File(p)
		ft := FileText{Path: p}
		seen := map[*mdspec.Section]bool{}
		offs := want[p]
		sort.Ints(offs)
		var secs []*mdspec.Section
		for _, off := range offs {
			s := outermost(f, off)
			if s == nil || seen[s] {
				continue
			}
			seen[s] = true
			secs = append(secs, s)
		}
		for _, s := range secs {
			text := f.Text(s.Range)
			if tokens(len(text)) > left {
				c.Omitted = append(c.Omitted, p+" §"+s.Number)
				continue
			}
			left -= tokens(len(text))
			ft.Parts = append(ft.Parts, Part{Label: "§" + s.Number + " " + s.Title, Line: f.Index().Position(s.Range.Start).Line, Text: text})
		}
		if len(ft.Parts) == 0 {
			c.Omitted = append(c.Omitted, p)
			continue
		}
		c.Files = append(c.Files, ft)
	}
}

// fixedTokens estimates everything but the files.
func (c *Context) fixedTokens() int {
	n := 0
	if ch := c.Change; ch != nil {
		n += len(ch.OldText) + len(ch.NewText) + len(ch.Why)
	}
	for _, r := range c.Regions {
		n += len(r.Text) + len(r.Old)
	}
	for _, nb := range c.Neighbors {
		n += len(nb.Text) + 40
	}
	for _, m := range c.Mentions {
		n += len(m.Text) + 40
	}
	for _, e := range c.Index {
		n += len(e.Name) + len(e.File) + len(e.Summary) + 12
	}
	return tokens(n)
}

func tokens(bytes int) int {
	return (bytes + 3) / 4
}

// outermost returns the top-level numbered section containing off: the one
// with the lowest heading level.
func outermost(f *mdspec.File, off int) *mdspec.Section {
	var best *mdspec.Section
	for _, s := range f.Sections {
		if off >= s.Range.Start && off < s.Range.End || off == s.Range.End && off == s.Range.Start {
			if best == nil || s.Level < best.Level {
				best = s
			}
		}
	}
	return best
}

// neighbors returns the names that anchor name references and the names of
// the anchors that reference it, sorted.
func neighbors(spec *mdspec.Spec, name string) []string {
	set := map[string]bool{}
	for _, a := range spec.ReferencedBy(name) {
		set[a.Name] = true
	}
	for _, r := range spec.Refs {
		if r.Within != nil && r.Within.Name == name && r.Name != name {
			set[r.Name] = true
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// refNames returns the $Name references in a standalone piece of text, in
// order of first occurrence.
func refNames(text string) []string {
	if text == "" {
		return nil
	}
	f := mdspec.ParseFile("change.spec.md", []byte(text))
	seen := map[string]bool{}
	var out []string
	for _, o := range f.Occurrences {
		if o.Role == mdspec.RoleReference && !seen[o.Name] {
			seen[o.Name] = true
			out = append(out, o.Name)
		}
	}
	return out
}

// lineBounds widens r to whole lines (without the final line ending).
func lineBounds(src []byte, r pos.Range) (int, int) {
	s, e := r.Start, r.End
	if s > len(src) {
		s = len(src)
	}
	if e > len(src) {
		e = len(src)
	}
	for s > 0 && src[s-1] != '\n' {
		s--
	}
	if e > s && src[e-1] == '\n' {
		e--
	}
	for e < len(src) && src[e] != '\n' {
		e++
	}
	if e > s && src[e-1] == '\r' {
		e--
	}
	return s, e
}

// firstSentence returns the start of a's body after its label: up to the
// first sentence end, whitespace collapsed, at most 160 bytes.
func firstSentence(f *mdspec.File, a *mdspec.Anchor) string {
	text := strings.Join(strings.Fields(f.Text(pos.Range{Start: a.LabelRange.End, End: a.BodyRange.End})), " ")
	for i := 0; i+1 < len(text); i++ {
		if (text[i] == '.' || text[i] == ';') && text[i+1] == ' ' && i > 0 {
			text = text[:i+1]
			break
		}
	}
	const limit = 160
	if len(text) > limit {
		cut := limit
		for cut > 0 && text[cut]&0xC0 == 0x80 {
			cut--
		}
		text = text[:cut] + "..."
	}
	return text
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
