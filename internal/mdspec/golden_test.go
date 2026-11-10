// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package mdspec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/golden"
	"github.com/jwriteclub/spec/internal/pos"
)

const testdata = "../../testdata"

// Golden JSON shapes. Ranges are written "line:col-line:col" (1-based, byte
// columns) so the files are reviewable; byte offsets are in "bytes".

type gAnchor struct {
	Name      string `json:"name"`
	Kind      Kind   `json:"kind"`
	Form      Form   `json:"form"`
	Title     string `json:"title,omitempty"`
	Label     string `json:"label"`
	NameAt    string `json:"name_at"`
	TitleAt   string `json:"title_at,omitempty"`
	Body      string `json:"body"`
	BodyBytes string `json:"body_bytes"`
	BodyText  string `json:"body_text,omitempty"`
	HashInput string `json:"hash_input,omitempty"`
	Hash      string `json:"hash"`
	ShapeHash string `json:"shape_hash,omitempty"`
	CodeAfter bool   `json:"code_after,omitempty"`
	Section   string `json:"section,omitempty"`
	FirstLine string `json:"first_line,omitempty"`
	BodyLines int    `json:"body_lines"`
}

type gOcc struct {
	Role      Role       `json:"role"`
	Text      string     `json:"text"`
	At        string     `json:"at"`
	LSP       string     `json:"lsp"`
	InTitle   bool       `json:"in_title,omitempty"`
	Container string     `json:"container"`
	Within    string     `json:"within,omitempty"`
	State     Resolution `json:"state,omitempty"`
	Target    string     `json:"target,omitempty"`
}

type gSection struct {
	Number  string `json:"number"`
	Title   string `json:"title"`
	Level   int    `json:"level"`
	Heading string `json:"heading"`
	Range   string `json:"range"`
}

type gSecRef struct {
	Text      string     `json:"text"`
	At        string     `json:"at"`
	Stem      string     `json:"stem,omitempty"`
	Number    string     `json:"number"`
	Container string     `json:"container"`
	Within    string     `json:"within,omitempty"`
	State     Resolution `json:"state"`
	Target    string     `json:"target,omitempty"`
}

type gSpan struct {
	Kind string `json:"kind,omitempty"`
	Text string `json:"text"`
	At   string `json:"at"`
}

type gDiag struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	File     string   `json:"file,omitempty"`
	At       string   `json:"at"`
	Message  string   `json:"message"`
	Related  []string `json:"related,omitempty"`
}

type gFile struct {
	File        string     `json:"file"`
	ValidUTF8   bool       `json:"valid_utf8"`
	BOM         bool       `json:"bom"`
	Anchors     []gAnchor  `json:"anchors"`
	Occurrences []gOcc     `json:"occurrences"`
	Sections    []gSection `json:"sections"`
	SectionRefs []gSecRef  `json:"section_refs"`
	Candidates  []gSpan    `json:"candidates"`
	Malformed   []gSpan    `json:"malformed_section_refs"`
	HashRefs    []gSpan    `json:"hash_refs"`
	Opaque      []gSpan    `json:"opaque"`
	Diagnostics []gDiag    `json:"diagnostics"`
}

func at(f *File, r pos.Range) string {
	a := f.Index().Position(r.Start)
	b := f.Index().Position(r.End)
	return fmt.Sprintf("%d:%d-%d:%d", a.Line, a.Col, b.Line, b.Col)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func container(f *File, m Mention) string {
	return fmt.Sprintf("%s %s", m.Kind, at(f, m.Range))
}

// describe renders f as resolved in s. full adds body texts and hash inputs.
func describe(s *Spec, f *File, full bool) gFile {
	g := gFile{File: f.Path, ValidUTF8: f.ValidUTF8, BOM: f.HasBOM,
		Anchors: []gAnchor{}, Occurrences: []gOcc{}, Sections: []gSection{}, SectionRefs: []gSecRef{},
		Candidates: []gSpan{}, Malformed: []gSpan{}, HashRefs: []gSpan{}, Opaque: []gSpan{}, Diagnostics: []gDiag{}}
	for _, a := range f.Anchors {
		ga := gAnchor{
			Name: a.Name, Kind: a.Kind, Form: a.Form, Title: a.Title,
			Label: at(f, a.LabelRange), NameAt: at(f, a.NameRange),
			Body: at(f, a.BodyRange), BodyBytes: a.BodyRange.String(),
			Hash: a.Hash, CodeAfter: a.CodeAfter,
			BodyLines: f.Index().Line(a.BodyRange.End) - f.Index().Line(a.BodyRange.Start) + 1,
		}
		if !a.TitleRange.Empty() {
			ga.TitleAt = at(f, a.TitleRange)
		}
		if a.Section != nil {
			ga.Section = a.Section.Number
		}
		if full {
			ga.BodyText = f.Text(a.BodyRange)
			ga.HashInput = a.HashInput()
			ga.ShapeHash = a.ShapeHash()
		} else {
			body := f.Text(a.BodyRange)
			if i := strings.IndexByte(body, '\n'); i >= 0 {
				body = body[:i]
			}
			ga.FirstLine = clip(body, 100)
			ga.Hash = a.ShortHash
		}
		g.Anchors = append(g.Anchors, ga)
	}
	resolved := map[*Occurrence]*ResolvedRef{}
	if s != nil {
		for _, r := range s.Refs {
			resolved[r.Occurrence] = r
		}
	}
	for _, o := range f.Occurrences {
		lp := f.Index().LSP(o.Range.Start)
		go_ := gOcc{Role: o.Role, Text: f.Text(o.Range), At: at(f, o.Range), LSP: fmt.Sprintf("%d:%d", lp.Line, lp.Character),
			InTitle: o.InTitle, Container: container(f, o.Container)}
		if o.Within != nil {
			go_.Within = o.Within.Name
		}
		if r := resolved[o]; r != nil {
			go_.State = r.State
			if r.Target != nil {
				go_.Target = fmt.Sprintf("%s:%d", r.Target.File, f.Index().Line(r.Target.NameRange.Start))
			}
			if r.State == Ambiguous {
				go_.Target = fmt.Sprintf("%d candidates", len(r.Candidates))
			}
		}
		g.Occurrences = append(g.Occurrences, go_)
	}
	for _, sec := range f.Sections {
		g.Sections = append(g.Sections, gSection{Number: sec.Number, Title: sec.Title, Level: sec.Level,
			Heading: at(f, sec.HeadingRange), Range: at(f, sec.Range)})
	}
	sres := map[*SectionRef]*ResolvedSectionRef{}
	if s != nil {
		for _, r := range s.SectionRefs {
			sres[r.SectionRef] = r
		}
	}
	for _, r := range f.SectionRefs {
		gr := gSecRef{Text: f.Text(r.Range), At: at(f, r.Range), Stem: r.Stem, Number: r.Number, Container: container(f, r.Container)}
		if r.Within != nil {
			gr.Within = r.Within.Name
		}
		if rr := sres[r]; rr != nil {
			gr.State = rr.State
			if rr.Target != nil {
				gr.Target = rr.Target.Qualified()
			}
		}
		g.SectionRefs = append(g.SectionRefs, gr)
	}
	for _, c := range f.Candidates {
		g.Candidates = append(g.Candidates, gSpan{Text: f.Text(c.Range), At: at(f, c.Range)})
	}
	for _, r := range f.MalformedSectionRefs {
		g.Malformed = append(g.Malformed, gSpan{Text: f.Text(r), At: at(f, r)})
	}
	for _, h := range f.HashRefs {
		g.HashRefs = append(g.HashRefs, gSpan{Text: f.Text(h.Range), At: at(f, h.Range)})
	}
	for _, o := range f.Opaque {
		g.Opaque = append(g.Opaque, gSpan{Kind: string(o.Kind), Text: clip(f.Text(o.Range), 60), At: at(f, o.Range)})
	}
	ds := f.Diagnostics
	if s != nil {
		ds = nil
		for _, d := range s.Diagnostics {
			if d.File == f.Path {
				ds = append(ds, d)
			}
		}
	}
	for _, d := range ds {
		gd := gDiag{Code: d.Code, Severity: string(d.Severity), At: at(f, d.Range), Message: d.Message}
		for _, r := range d.Related {
			where := r.File
			if r.File == f.Path {
				where = r.File + " " + at(f, r.Range)
			}
			gd.Related = append(gd.Related, where+": "+r.Message)
		}
		g.Diagnostics = append(g.Diagnostics, gd)
	}
	return g
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	require.NoError(t, enc.Encode(v))
	return buf.Bytes()
}

// TestGoldenEdge parses every testdata/edge/*.spec.md on its own and compares
// the model with <name>.golden.json.
func TestGoldenEdge(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(testdata, "edge", "*.spec.md"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, p := range paths {
		name := filepath.Base(p)
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(p)
			require.NoError(t, err)
			f := ParseFile(name, src)
			s := Build([]*File{f})
			checkFileInvariants(t, f)
			golden.Check(t, strings.TrimSuffix(p, ".spec.md")+".golden.json", marshal(t, describe(s, f, true)))
		})
	}
}

// TestGoldenLensd parses the converted lensd spec and checks it against the
// golden file and the NAMES.md table.
func TestGoldenLensd(t *testing.T) {
	dir := filepath.Join(testdata, "lensd")
	src, err := os.ReadFile(filepath.Join(dir, "lensd.spec.md"))
	require.NoError(t, err)
	f := ParseFile("lensd.spec.md", src)
	s := Build([]*File{f})
	checkFileInvariants(t, f)
	golden.Check(t, filepath.Join(dir, "lensd.golden.json"), marshal(t, describe(s, f, false)))

	// No errors.
	for _, d := range s.Diagnostics {
		require.NotEqual(t, diag.Error, d.Severity, "%s %s %s", d.Code, at(f, d.Range), d.Message)
	}

	// Every row of NAMES.md is an anchor of the right kind, and nothing else is.
	names := readNameTable(t, filepath.Join(dir, "NAMES.md"))
	require.Len(t, s.Anchors, len(names))
	for _, row := range names {
		a := s.Anchor(row.name)
		require.NotNil(t, a, "anchor %s (%s) missing", row.name, row.old)
		require.Equal(t, row.kind, a.Kind, row.name)
	}
}

type nameRow struct {
	old  string
	name string
	kind Kind
}

// readNameTable reads the "| old | new |" rows of NAMES.md, where new is
// "$Name" (an invariant) or "Def $Name" (a definition), in backticks.
func readNameTable(t *testing.T, path string) []nameRow {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var rows []nameRow
	for _, line := range strings.Split(string(b), "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) < 2 {
			continue
		}
		old := strings.Trim(strings.TrimSpace(cells[0]), "`")
		nw := strings.Trim(strings.TrimSpace(cells[1]), "`")
		if !strings.HasPrefix(old, "INV-") && !strings.HasPrefix(old, "DEF-") {
			continue
		}
		row := nameRow{old: old, kind: KindInvariant}
		if strings.HasPrefix(nw, "Def ") {
			row.kind = KindDefinition
			nw = strings.TrimPrefix(nw, "Def ")
		}
		row.name = strings.TrimPrefix(nw, "$")
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].old < rows[j].old })
	return rows
}
