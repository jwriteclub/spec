// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/spell"
	"github.com/jwriteclub/spec/internal/workflow"
)

// ---- ls ----

type lsAnchor struct {
	anchorJSON
	Change *string `json:"change"`
}

type lsData struct {
	Baseline *baselineJSON `json:"baseline"`
	Anchors  []lsAnchor    `json:"anchors"`
}

func (s *session) lsCommand() *cobra.Command {
	var kind string
	var changed, unreferenced bool
	cmd := &cobra.Command{
		Use:   "ls [--kind inv|def] [--changed] [--unreferenced]",
		Short: "list anchors: kind, name, title, file:line, short hash, reference count",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errTooMany("ls [--kind inv|def] [--changed] [--unreferenced]")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runLs(kind, changed, unreferenced)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "only anchors of this kind: inv or def")
	cmd.Flags().BoolVar(&changed, "changed", false, "only anchors that differ from HEAD")
	cmd.Flags().BoolVar(&unreferenced, "unreferenced", false, "only anchors nothing references")
	return cmd
}

func (s *session) runLs(kind string, changed, unreferenced bool) *outcome {
	if kind != "" && kind != string(mdspec.KindInvariant) && kind != string(mdspec.KindDefinition) {
		return usageErr("--kind must be inv or def, not %q", kind)
	}
	v, o := s.loadView()
	if o != nil {
		return o
	}
	var bl *baselineJSON
	var states map[string]string
	if changed {
		cmp, o := s.compare(v, compareOpts{})
		if o != nil {
			return o
		}
		if cmp.base.State == baseline.StateUnavailable {
			return noBaseline(cmp.base)
		}
		bj := cmp.baselineObj()
		bl = &bj
		states = map[string]string{}
		for _, e := range cmp.diff.Entries {
			if e.New != nil {
				states[e.Name] = string(e.Status)
			}
		}
	}

	data := lsData{Baseline: bl, Anchors: []lsAnchor{}}
	var tw strings.Builder
	w := tabwriter.NewWriter(&tw, 0, 4, 2, ' ', 0)
	for _, a := range v.spec.Anchors {
		if kind != "" && string(a.Kind) != kind {
			continue
		}
		var change *string
		if changed {
			st, ok := states[a.Name]
			if !ok {
				continue
			}
			change = &st
		}
		obj := v.anchorObj(a)
		if unreferenced && obj.RefCount != 0 {
			continue
		}
		data.Anchors = append(data.Anchors, lsAnchor{anchorJSON: obj, Change: change})
		title := a.Title
		if title == "" {
			title = "-"
		}
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s", a.Kind, a.Name, title, locString(a.File, obj.Line, 0), a.ShortHash, plural(obj.RefCount, "ref"))
		if changed {
			row += "\t" + *change
		}
		fmt.Fprintln(w, row)
	}
	w.Flush()
	human := tw.String()
	if len(data.Anchors) == 0 {
		switch {
		case changed:
			human = "no anchors differ from " + bl.From + "\n"
		case len(v.spec.Anchors) == 0:
			human = "no anchors defined yet; `spec explain markup` has the syntax\n"
		default:
			human = "no anchors match\n"
		}
	}
	return &outcome{data: data, diags: v.diagsJSON(), human: human}
}

// ---- show ----

type refByJSON struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
}

type mentionJSON struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Kind string `json:"kind"`
}

type showAnchorData struct {
	Type         string        `json:"type"`
	Anchor       anchorJSON    `json:"anchor"`
	Body         string        `json:"body"`
	ReferencedBy []refByJSON   `json:"referenced_by"`
	MentionedIn  []mentionJSON `json:"mentioned_in"`
	Baseline     baselineJSON  `json:"baseline"`
	Change       *changeJSON   `json:"change"`
}

type showSectionData struct {
	Type    string       `json:"type"`
	Section sectionJSON  `json:"section"`
	Anchors []anchorJSON `json:"anchors"`
}

func (s *session) showCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME|SECTION",
		Short: "show an anchor (body, references, change state) or a section (the anchors in it)",
		Long: "NAME is an anchor name without the sigil (Frame.Timing). SECTION is a number (5.2a), optionally\n" +
			"qualified with the file stem (hls:5.2a); § and # prefixes are accepted.",
		Args: needArgs(1, "show NAME|SECTION"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runShow(args[0])
			return nil
		},
	}
}

func (s *session) runShow(arg string) *outcome {
	t, o := parseTarget(arg)
	if o != nil {
		return o
	}
	v, o := s.loadView()
	if o != nil {
		return o
	}
	a, sec, o := v.resolveTarget(t)
	if o != nil {
		return o
	}
	if sec != nil {
		return v.showSection(sec)
	}
	return s.showAnchor(v, a)
}

func (v *view) showSection(sec *mdspec.Section) *outcome {
	data := showSectionData{Type: "section", Section: v.sectionObj(sec), Anchors: []anchorJSON{}}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", sec.Qualified(), sec.Title)
	fmt.Fprintf(&b, "%s (level %d)\n", locString(sec.File, data.Section.Line, 0), sec.Level)
	as := v.spec.AnchorsIn(sec)
	if len(as) == 0 {
		b.WriteString("\nno anchors in this section\n")
	} else {
		b.WriteString("\nanchors:\n")
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		for _, a := range as {
			obj := v.anchorObj(a)
			data.Anchors = append(data.Anchors, obj)
			title := a.Title
			if title == "" {
				title = "-"
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", a.Kind, a.Name, title, locString(a.File, obj.Line, 0), a.ShortHash)
		}
		w.Flush()
	}
	return &outcome{data: data, diags: v.diagsJSON(), human: b.String()}
}

func (s *session) showAnchor(v *view, a *mdspec.Anchor) *outcome {
	obj := v.anchorObj(a)
	f := v.spec.File(a.File)
	data := showAnchorData{
		Type: "anchor", Anchor: obj, Body: f.Text(a.BodyRange),
		ReferencedBy: []refByJSON{}, MentionedIn: []mentionJSON{},
	}
	for _, r := range v.spec.ReferencedBy(a.Name) {
		data.ReferencedBy = append(data.ReferencedBy, refByJSON{Name: "$" + r.Name, Kind: string(r.Kind), File: r.File, Line: v.line(r.File, r.LabelRange.Start)})
	}
	for _, m := range v.spec.Mentions(a.Name) {
		data.MentionedIn = append(data.MentionedIn, mentionJSON{File: m.File, Line: v.line(m.File, m.Range.Start), Kind: string(m.Kind)})
	}

	// Change state against HEAD; a baseline that cannot be read only
	// degrades this part.
	var changeLine string
	cmp, o := s.compare(v, compareOpts{lenient: true})
	if o != nil {
		return o
	}
	data.Baseline = cmp.baselineObj()
	switch cmp.base.State {
	case baseline.StateUnavailable:
		changeLine = "baseline unavailable: " + cmp.base.Reason
	default:
		changeLine = "unchanged vs " + data.Baseline.From
		for _, e := range cmp.diff.Entries {
			if e.New != nil && e.Name == a.Name {
				cj := cmp.changeObj(e)
				data.Change = &cj
				changeLine = fmt.Sprintf("%s vs %s", cj.State, data.Baseline.From)
				if cj.RenamedFrom != nil {
					changeLine += " (was " + strings.TrimPrefix(*cj.RenamedFrom, "$") + ")"
				}
			}
		}
		if cmp.base.State == baseline.StateUnborn {
			changeLine += " (no commits yet)"
		}
	}

	var b strings.Builder
	title := ""
	if a.Title != "" {
		title = "  (" + a.Title + ")"
	}
	fmt.Fprintf(&b, "%s  %s%s\n", a.Name, a.Kind, title)
	where := locString(a.File, obj.Line, 0)
	if a.Section != nil {
		where += "  in " + a.Section.Qualified()
	}
	fmt.Fprintf(&b, "%s\nhash %s  (%s)\n", where, a.ShortHash, a.Hash)
	fmt.Fprintf(&b, "%s\n\n", changeLine)
	b.WriteString(strings.TrimRight(data.Body, "\n") + "\n\n")
	if len(data.ReferencedBy) == 0 {
		b.WriteString("referenced by: no anchors\n")
	} else {
		b.WriteString("referenced by:\n")
		for _, r := range data.ReferencedBy {
			fmt.Fprintf(&b, "  %s  %s\n", strings.TrimPrefix(r.Name, "$"), locString(r.File, r.Line, 0))
		}
	}
	if len(data.MentionedIn) == 0 {
		b.WriteString("mentioned in: no prose outside anchor bodies\n")
	} else {
		b.WriteString("mentioned in:\n")
		for _, m := range data.MentionedIn {
			fmt.Fprintf(&b, "  %s  (%s)\n", locString(m.File, m.Line, 0), m.Kind)
		}
	}
	return &outcome{data: data, diags: v.diagsJSON(), human: b.String()}
}

// ---- refs ----

type refsTarget struct {
	Type    string       `json:"type"`
	Name    string       `json:"name"`
	Section *sectionJSON `json:"section"`
}

type refSite struct {
	File    string    `json:"file"`
	Line    int       `json:"line"`
	Col     int       `json:"col"`
	Range   pos.Range `json:"range"`
	Text    string    `json:"text"`
	Within  *string   `json:"within"`
	InTitle bool      `json:"in_title"`
	State   string    `json:"state"`
}

type refsData struct {
	Target refsTarget `json:"target"`
	Refs   []refSite  `json:"refs"`
}

func (s *session) refsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "refs NAME|SECTION",
		Short: "list every reference to an anchor or section, as file:line:col with the line's text",
		Args:  needArgs(1, "refs NAME|SECTION"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runRefs(args[0])
			return nil
		},
	}
}

func (s *session) runRefs(arg string) *outcome {
	t, o := parseTarget(arg)
	if o != nil {
		return o
	}
	v, o := s.loadView()
	if o != nil {
		return o
	}
	data := refsData{Refs: []refSite{}}
	if t.kind == targetAnchor {
		data.Target = refsTarget{Type: "anchor", Name: "$" + t.name}
		as := v.spec.Lookup(t.name)
		refs := v.spec.ReferencesTo(t.name)
		if len(as) == 0 && len(refs) == 0 && len(v.models.ToAnchor(t.name)) == 0 {
			return fail(exitFail, "not_found", "no anchor named %s in this project and nothing references it (spec ls lists the anchors)", t.name)
		}
		if len(as) == 1 && as[0].Section != nil {
			sj := v.sectionObj(as[0].Section)
			data.Target.Section = &sj
		}
		for _, r := range refs {
			data.Refs = append(data.Refs, v.refSite(r.File, r.Range.Start, r.Range.End, r.Within, r.InTitle, string(r.State)))
		}
		for _, l := range v.models.ToAnchor(t.name) {
			data.Refs = append(data.Refs, v.tagSite(l))
		}
	} else {
		_, sec, o := v.resolveTarget(t)
		if o != nil {
			return o
		}
		sj := v.sectionObj(sec)
		data.Target = refsTarget{Type: "section", Name: "", Section: &sj}
		for _, r := range v.spec.SectionRefs {
			hit := r.State == mdspec.Resolved && r.Target == sec
			if r.State == mdspec.Ambiguous {
				for _, c := range r.Candidates {
					if c == sec {
						hit = true
					}
				}
			}
			if hit {
				data.Refs = append(data.Refs, v.refSite(r.File, r.Range.Start, r.Range.End, r.Within, false, string(r.State)))
			}
		}
		for _, l := range v.models.ToSection(sec) {
			data.Refs = append(data.Refs, v.tagSite(l))
		}
	}

	var b strings.Builder
	if len(data.Refs) == 0 {
		fmt.Fprintf(&b, "no references to %s\n", t.display())
	}
	for _, r := range data.Refs {
		fmt.Fprintf(&b, "%s: %s", locString(r.File, r.Line, r.Col), strings.TrimSpace(r.Text))
		var notes []string
		if r.Within != nil {
			notes = append(notes, "in "+strings.TrimPrefix(*r.Within, "$"))
		}
		if r.InTitle {
			notes = append(notes, "in a title")
		}
		if r.State != string(mdspec.Resolved) {
			notes = append(notes, r.State)
		}
		if len(notes) > 0 {
			b.WriteString("  [" + strings.Join(notes, ", ") + "]")
		}
		b.WriteString("\n")
	}
	return &outcome{data: data, diags: v.diagsJSON(), human: b.String()}
}

func (v *view) refSite(file string, start, end int, within *mdspec.Anchor, inTitle bool, state string) refSite {
	line, col := v.lineCol(file, start)
	r := refSite{File: file, Line: line, Col: col, Range: pos.Range{Start: start, End: end}, Text: v.lineText(file, start), InTitle: inTitle, State: state}
	if within != nil {
		w := "$" + within.Name
		r.Within = &w
	}
	return r
}

// tagSite is the reference site of a formal model's tag.
func (v *view) tagSite(l *formal.Link) refSite {
	r := l.Tag.Ref.Range
	return v.refSite(l.File.Path, r.Start, r.End, nil, false, string(l.State))
}

// ---- lint ----

type lintData struct {
	Files    int        `json:"files"`
	Anchors  int        `json:"anchors"`
	Sections int        `json:"sections"`
	Counts   lintCounts `json:"counts"`
}

func (s *session) lintCommand() *cobra.Command {
	var spelling bool
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "check the spec files against every lint rule; exit 4 on errors",
		Long: "Checks every spec file against the lint rules. --spell adds the spell check's flags without\n" +
			"calling a model: known misspellings and the verdicts `spec analyze` cached (`spec explain spell`).",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errTooMany("lint")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runLint(spelling)
			return nil
		},
	}
	cmd.Flags().BoolVar(&spelling, "spell", false, "add known misspellings and cached spell-check verdicts (no model call)")
	return cmd
}

func (s *session) runLint(spelling bool) *outcome {
	v, o := s.loadView()
	if o != nil {
		return o
	}
	if spelling && !workflow.SpellOff(v.proj) {
		var files []*mdspec.File
		for _, f := range v.spec.Files {
			if f.ValidUTF8 {
				files = append(files, f)
			}
		}
		p := s.workflow().SpellProject(v.proj, v.spec)
		ds := spell.Diagnostics(spell.Cached(p, files, nil, spell.OpenCache(v.proj.Root)))
		if lv, err := lint.ParseLevel(v.proj.Config.Lint[diag.CodeSpell]); err == nil {
			for i := range ds {
				ds[i].Severity = lv
			}
		}
		v.diags = append(v.diags, ds...)
		diag.Sort(v.diags)
	}
	c := v.counts()
	data := lintData{Files: len(v.spec.Files), Anchors: len(v.spec.Anchors), Sections: len(v.spec.Sections), Counts: c}
	dj := v.diagsJSON()
	var b strings.Builder
	for _, d := range dj {
		b.WriteString(formatDiag(d))
	}
	fmt.Fprintf(&b, "%s, %s, %s in %s, %s, %s\n",
		plural(c.Error, "error"), plural(c.Warning, "warning"), plural(c.Hint, "hint"),
		plural(data.Files, "file"), plural(data.Anchors, "anchor"), plural(data.Sections, "section"))
	out := &outcome{data: data, diags: dj, human: b.String()}
	if diag.HasErrors(v.diags) {
		out.exit = exitCheck
		out.code = "lint_errors"
		out.message = plural(c.Error, "lint error")
	}
	return out
}
