// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/project"
)

// resolveRoot finds the project directory from --project or the working
// directory.
func (s *session) resolveRoot() (string, *outcome) {
	dir := s.project
	if dir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(s.env.Cwd, dir)
	}
	root, err := project.Resolve(dir, s.env.Cwd)
	if err != nil {
		return "", s.projectErr(err)
	}
	return root, nil
}

// projectErr maps a project discovery or open error to an outcome.
func (s *session) projectErr(err error) *outcome {
	nf, ok := err.(*project.NotFoundError)
	if !ok {
		return internalErr(err)
	}
	// Suggestions are relative to the directory searched from; report them
	// relative to the working directory.
	cands := []string{}
	for _, c := range nf.Suggestions {
		abs := filepath.Join(nf.Start, filepath.FromSlash(c))
		if rel, err := filepath.Rel(s.env.Cwd, abs); err == nil {
			c = filepath.ToSlash(rel)
		}
		cands = append(cands, c)
	}
	var msg string
	if nf.Explicit {
		msg = "no spec.toml in " + nf.Start + "; run `spec init` there, or pass --project the directory that holds spec.toml"
	} else {
		msg = "no spec.toml found in " + nf.Start + " or any parent directory; run `spec init` here, or pass --project DIR"
	}
	if len(cands) > 0 {
		msg += "\nprojects in this git work tree (pass one to --project):\n  " + strings.Join(cands, "\n  ")
	}
	o := fail(exitFail, "no_project", "%s", msg)
	o.data = map[string]any{"candidates": cands}
	// In JSON the data is null on exit 1 except where stated: no_project
	// carries candidates.
	return o
}

// openProject resolves and opens the project (config and file list only).
func (s *session) openProject() (*project.Project, *outcome) {
	root, o := s.resolveRoot()
	if o != nil {
		return nil, o
	}
	return s.openRoot(root)
}

func (s *session) openRoot(root string) (*project.Project, *outcome) {
	p, err := project.Open(root)
	if err != nil {
		return nil, s.projectErr(err)
	}
	return p, nil
}

// view is a project's spec as one side of a comparison, with its
// diagnostics: what almost every command needs.
type view struct {
	proj  *project.Project
	spec  *mdspec.Spec
	diags []diag.Diagnostic
	// models are the formal models joined to spec; nil for a historical side
	// (a commit or the index), since models are read from the working tree
	// only and would not match it.
	models *formal.Index

	cfgIndex *pos.Index
}

// newView lints a spec that is not the working tree, using proj's
// configuration. Models take no part.
func newView(proj *project.Project, spec *mdspec.Spec) *view {
	v := &view{proj: proj, spec: spec}
	v.diags = lint.Run(spec, lint.Options{Overrides: proj.Config.Lint, Extra: proj.Diagnostics})
	return v
}

// newLoadedView lints the working tree, its formal models included.
func newLoadedView(loaded *project.Loaded) *view {
	v := &view{proj: loaded.Project, spec: loaded.Spec, models: loaded.Formal}
	v.diags = lint.Run(loaded.Spec, loaded.LintOptions())
	return v
}

// loadView reads the working tree.
func (s *session) loadView() (*view, *outcome) {
	proj, o := s.openProject()
	if o != nil {
		return nil, o
	}
	return s.loadViewOf(proj)
}

func (s *session) loadViewOf(proj *project.Project) (*view, *outcome) {
	loaded, err := proj.Load(nil)
	if err != nil {
		return nil, internalErr(err)
	}
	return newLoadedView(loaded), nil
}

// index returns the position index of a file named in a diagnostic, or nil.
func (v *view) index(file string) *pos.Index {
	if file == project.ConfigFile {
		if v.cfgIndex == nil {
			v.cfgIndex = pos.NewIndex(v.proj.ConfigSource())
		}
		return v.cfgIndex
	}
	if f := v.spec.File(file); f != nil {
		return f.Index()
	}
	if f := v.models.File(file); f != nil {
		return f.Index()
	}
	return nil
}

// lineCol returns the 1-based line and byte column of offset off in file,
// or 0, 0 when the file is unknown.
func (v *view) lineCol(file string, off int) (int, int) {
	idx := v.index(file)
	if idx == nil {
		return 0, 0
	}
	p := idx.Position(off)
	return p.Line, p.Col
}

// line returns the 1-based line of offset off in file.
func (v *view) line(file string, off int) int {
	l, _ := v.lineCol(file, off)
	return l
}

// counts returns the diagnostics by severity.
func (v *view) counts() lintCounts {
	return lintCounts{
		Error:   diag.Count(v.diags, diag.Error),
		Warning: diag.Count(v.diags, diag.Warning),
		Hint:    diag.Count(v.diags, diag.Hint),
	}
}

// lintCounts is the {error, warning, hint} object.
type lintCounts struct {
	Error   int `json:"error"`
	Warning int `json:"warning"`
	Hint    int `json:"hint"`
}

// diagJSON is the diagnostic object of the JSON contract.
type diagJSON struct {
	Code     string        `json:"code"`
	Severity diag.Severity `json:"severity"`
	File     string        `json:"file"`
	Line     int           `json:"line"`
	Col      int           `json:"col"`
	Range    pos.Range     `json:"range"`
	Message  string        `json:"message"`
	Related  []relatedJSON `json:"related"`
}

type relatedJSON struct {
	File    string    `json:"file"`
	Line    int       `json:"line"`
	Col     int       `json:"col"`
	Range   pos.Range `json:"range"`
	Message string    `json:"message"`
}

// diagsJSON converts the view's diagnostics.
func (v *view) diagsJSON() []diagJSON {
	out := make([]diagJSON, 0, len(v.diags))
	for _, d := range v.diags {
		line, col := v.lineCol(d.File, d.Range.Start)
		dj := diagJSON{
			Code: d.Code, Severity: d.Severity, File: d.File, Line: line, Col: col,
			Range: d.Range, Message: d.Message, Related: []relatedJSON{},
		}
		for _, r := range d.Related {
			rl, rc := v.lineCol(r.File, r.Range.Start)
			dj.Related = append(dj.Related, relatedJSON{File: r.File, Line: rl, Col: rc, Range: r.Range, Message: r.Message})
		}
		out = append(out, dj)
	}
	return out
}

// formatDiag renders one diagnostic for humans:
//
//	hls.spec.md:12:5: error: message [ref-dangling]
func formatDiag(d diagJSON) string {
	var b strings.Builder
	switch {
	case d.File == "":
	case d.Line == 0:
		b.WriteString(d.File + ": ")
	default:
		b.WriteString(locString(d.File, d.Line, d.Col) + ": ")
	}
	b.WriteString(string(d.Severity) + ": " + d.Message + " [" + d.Code + "]\n")
	for _, r := range d.Related {
		b.WriteString("    ")
		if r.Line > 0 {
			b.WriteString(locString(r.File, r.Line, r.Col) + ": ")
		} else if r.File != "" {
			b.WriteString(r.File + ": ")
		}
		b.WriteString(r.Message + "\n")
	}
	return b.String()
}

func locString(file string, line, col int) string {
	if col > 0 {
		return file + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(col)
	}
	return file + ":" + strconv.Itoa(line)
}

// anchorJSON is the anchor object of the JSON contract.
type anchorJSON struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Form      string  `json:"form"`
	Title     string  `json:"title"`
	File      string  `json:"file"`
	Line      int     `json:"line"`
	Section   *string `json:"section"`
	Hash      string  `json:"hash"`
	ShortHash string  `json:"short_hash"`
	RefCount  int     `json:"ref_count"`
}

// anchorObj builds the anchor object for a in this view.
func (v *view) anchorObj(a *mdspec.Anchor) anchorJSON {
	o := anchorJSON{
		Name: "$" + a.Name, Kind: string(a.Kind), Form: string(a.Form), Title: a.Title,
		File: a.File, Line: v.line(a.File, a.LabelRange.Start),
		Hash: a.Hash, ShortHash: a.ShortHash, RefCount: v.refCount(a),
	}
	if a.Section != nil {
		q := a.Section.Qualified()
		o.Section = &q
	}
	return o
}

// refCount counts the reference tokens of a's name outside the bodies of
// anchors of that name.
func (v *view) refCount(a *mdspec.Anchor) int {
	n := 0
	for _, r := range v.spec.ReferencesTo(a.Name) {
		if r.Within != nil && r.Within.Name == a.Name {
			continue
		}
		n++
	}
	return n
}

// sectionJSON is the section object of the JSON contract.
type sectionJSON struct {
	Number    string `json:"number"`
	Qualified string `json:"qualified"`
	Title     string `json:"title"`
	Level     int    `json:"level"`
	File      string `json:"file"`
	Line      int    `json:"line"`
}

func (v *view) sectionObj(sec *mdspec.Section) sectionJSON {
	return sectionJSON{
		Number: sec.Number, Qualified: sec.Qualified(), Title: sec.Title, Level: sec.Level,
		File: sec.File, Line: v.line(sec.File, sec.HeadingRange.Start),
	}
}

// resolveTarget finds the anchor or section a parsed target names.
// Exactly one of the results is non-nil on success.
func (v *view) resolveTarget(t target) (*mdspec.Anchor, *mdspec.Section, *outcome) {
	if t.kind == targetAnchor {
		as := v.spec.Lookup(t.name)
		switch len(as) {
		case 0:
			return nil, nil, fail(exitFail, "not_found", "no anchor named %s in this project (spec ls lists them)", t.name)
		case 1:
			return as[0], nil, nil
		}
		cands := make([]string, 0, len(as))
		for _, a := range as {
			cands = append(cands, locString(a.File, v.line(a.File, a.LabelRange.Start), 0))
		}
		o := fail(exitFail, "ambiguous", "%s is defined %d times (%s); fix the duplicates first (spec lint)", t.name, len(as), strings.Join(cands, ", "))
		o.data = map[string]any{"candidates": cands}
		return nil, nil, o
	}
	if !sectionNumberOK(t.number) {
		return nil, nil, fail(exitFail, "not_found", "%q is not a section number (examples: 5, 5.2a, hls:5.2a)", t.number)
	}
	var secs []*mdspec.Section
	if t.stem == "" {
		secs = v.spec.FindSections(t.number)
	} else {
		for _, f := range v.spec.FilesWithStem(t.stem) {
			for _, sec := range f.Sections {
				if sec.Number == t.number {
					secs = append(secs, sec)
				}
			}
		}
	}
	switch len(secs) {
	case 0:
		return nil, nil, fail(exitFail, "not_found", "no section %s in this project", t.display())
	case 1:
		return nil, secs[0], nil
	}
	cands := make([]string, 0, len(secs))
	distinct := map[string]bool{}
	for _, sec := range secs {
		cands = append(cands, sec.Qualified())
		distinct[sec.Qualified()] = true
	}
	sort.Strings(cands)
	msg := fmt.Sprintf("section %s exists in several files (%s); write one of them", t.display(), strings.Join(cands, ", "))
	if len(distinct) < len(secs) {
		// Some matches share a qualified form (a duplicate number in one
		// file, or files sharing a stem): name them by location instead.
		cands = cands[:0]
		for _, sec := range secs {
			cands = append(cands, locString(sec.File, v.line(sec.File, sec.HeadingRange.Start), 0))
		}
		msg = fmt.Sprintf("section %s matches %d sections that §stem:N cannot tell apart (%s); renumber the duplicate section, or rename one of the files so each stem is unique (spec lint shows where)",
			t.display(), len(secs), strings.Join(cands, ", "))
	}
	o := fail(exitFail, "ambiguous", "%s", msg)
	o.data = map[string]any{"candidates": cands}
	return nil, nil, o
}

// lineText returns the source line holding offset off, without its line
// ending.
func (v *view) lineText(file string, off int) string {
	if f := v.spec.File(file); f != nil {
		idx := f.Index()
		return f.Text(idx.LineRange(idx.Line(off)))
	}
	if f := v.models.File(file); f != nil {
		idx := f.Index()
		return f.Text(idx.LineRange(idx.Line(off)))
	}
	return ""
}
