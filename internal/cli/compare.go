// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/project"
)

// compareOpts selects the two sides of a comparison.
type compareOpts struct {
	// ref is the baseline revision; "" means HEAD.
	ref string
	// staged says the new side is the index rather than the working tree.
	staged bool
	// lenient turns a baseline that cannot be read into an unavailable one
	// instead of an error (for commands where the baseline is a side
	// dish).
	lenient bool
}

// comparison is the anchor diff between a baseline and the new side.
type comparison struct {
	opts    compareOpts
	base    *baseline.Baseline
	oldSpec *mdspec.Spec
	newView *view
	// diff is nil when the baseline is unavailable.
	diff *baseline.Diff
}

// loadBaseline reads a baseline, mapping errors to outcomes.
func (s *session) loadBaseline(proj *project.Project, src baseline.Source, lenient bool) (*baseline.Baseline, *outcome) {
	b, err := s.env.Git.Load(context.Background(), proj, src)
	if err != nil {
		var bad *baseline.BadRefError
		if errors.As(err, &bad) {
			return nil, fail(exitFail, "bad_ref", "%s", bad.Error())
		}
		if lenient {
			return &baseline.Baseline{State: baseline.StateUnavailable, Source: src, Reason: err.Error()}, nil
		}
		return nil, internalErr(err)
	}
	return b, nil
}

// stagedView reads the index as a view.
func (s *session) stagedView(proj *project.Project) (*view, *outcome) {
	b, o := s.loadBaseline(proj, baseline.Source{Index: true}, false)
	if o != nil {
		return nil, o
	}
	if b.State == baseline.StateUnavailable {
		return nil, noBaseline(b)
	}
	return newView(proj, b.Spec), nil
}

// noBaseline is the exit 1 outcome for a missing baseline.
func noBaseline(b *baseline.Baseline) *outcome {
	return fail(exitFail, "no_baseline", "no baseline to compare against: %s (diff, ls --changed and check need a git repository and git on PATH)", b.Reason)
}

// compare loads the baseline and diffs it against the view.
func (s *session) compare(v *view, opts compareOpts) (*comparison, *outcome) {
	b, o := s.loadBaseline(v.proj, baseline.Source{Ref: opts.ref}, opts.lenient)
	if o != nil {
		return nil, o
	}
	c := &comparison{opts: opts, base: b, oldSpec: b.Spec, newView: v}
	if b.State != baseline.StateUnavailable {
		c.diff = baseline.Compare(b.Spec, v.spec)
	}
	return c, nil
}

// baselineJSON is the baseline object of the JSON contract.
type baselineJSON struct {
	From   string `json:"from"`
	To     string `json:"to"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

func (c *comparison) baselineObj() baselineJSON {
	from := c.opts.ref
	if from == "" {
		from = "HEAD"
	}
	to := "worktree"
	if c.opts.staged {
		to = "index"
	}
	return baselineJSON{From: from, To: to, State: string(c.base.State), Reason: c.base.Reason}
}

// sideJSON is one side (old or new) of a change.
type sideJSON struct {
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Hash      string `json:"hash"`
	ShortHash string `json:"short_hash"`
	File      string `json:"file"`
	Line      int    `json:"line"`
}

// changeJSON is the change object of the JSON contract.
type changeJSON struct {
	Name        string    `json:"name"`
	State       string    `json:"state"`
	RenamedFrom *string   `json:"renamed_from"`
	Old         *sideJSON `json:"old"`
	New         *sideJSON `json:"new"`
	BodyChanged bool      `json:"body_changed"`
	KindChanged bool      `json:"kind_changed"`
	Retitled    bool      `json:"retitled"`
	Trailers    []string  `json:"trailers"`
}

func lineIn(spec *mdspec.Spec, a *mdspec.Anchor) int {
	if spec == nil || a == nil {
		return 0
	}
	f := spec.File(a.File)
	if f == nil {
		return 0
	}
	return f.Index().Position(a.LabelRange.Start).Line
}

func (c *comparison) changeObj(e baseline.Entry) changeJSON {
	cj := changeJSON{
		Name: "$" + e.Name, State: string(e.Status),
		BodyChanged: e.Changed, KindChanged: e.KindChanged, Retitled: e.Retitled,
		Trailers: (&baseline.Diff{Entries: []baseline.Entry{e}}).Required().Lines(),
	}
	if e.Status == baseline.Renamed {
		old := "$" + e.OldName
		cj.RenamedFrom = &old
	}
	if e.Old != nil {
		cj.Old = &sideJSON{Kind: string(e.OldKind), Title: e.OldTitle, Hash: e.OldHash, ShortHash: e.OldShort, File: e.OldFile, Line: lineIn(c.oldSpec, e.Old)}
	}
	if e.New != nil {
		cj.New = &sideJSON{Kind: string(e.NewKind), Title: e.NewTitle, Hash: e.NewHash, ShortHash: e.NewShort, File: e.NewFile, Line: lineIn(c.newView.spec, e.New)}
	}
	return cj
}

// changes returns the change objects sorted by name.
func (c *comparison) changes() []changeJSON {
	out := []changeJSON{}
	if c.diff == nil {
		return out
	}
	for _, e := range c.diff.Entries {
		out = append(out, c.changeObj(e))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// skipped returns the names defined more than once, as "$Name", sorted.
func (c *comparison) skipped() []string {
	out := []string{}
	if c.diff == nil {
		return out
	}
	seen := map[string]bool{}
	for _, sk := range c.diff.Skipped {
		if !seen[sk.Name] {
			seen[sk.Name] = true
			out = append(out, "$"+sk.Name)
		}
	}
	sort.Strings(out)
	return out
}

// side formats one side of a trailer-like transition: "inv:8b77d0a4" or "-".
func sideString(kind, short string) string {
	if kind == "" {
		return "-"
	}
	return kind + ":" + short
}

// renderChanges is the human list of changes, one anchor per line.
func renderChanges(changes []changeJSON) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	for _, c := range changes {
		name := strings.TrimPrefix(c.Name, "$")
		var detail, where string
		os, ns := "-", "-"
		if c.Old != nil {
			os = sideString(c.Old.Kind, c.Old.ShortHash)
		}
		if c.New != nil {
			ns = sideString(c.New.Kind, c.New.ShortHash)
			where = locString(c.New.File, c.New.Line, 0)
		} else if c.Old != nil {
			where = "was " + locString(c.Old.File, c.Old.Line, 0)
		}
		switch c.State {
		case "retitled":
			detail = fmt.Sprintf("%q -> %q", c.Old.Title, c.New.Title)
		case "renamed":
			detail = "was " + strings.TrimPrefix(*c.RenamedFrom, "$")
			if c.BodyChanged || c.KindChanged {
				detail += "; " + os + " -> " + ns
			}
		default:
			detail = os + " -> " + ns
		}
		var also []string
		if c.State != "changed" && c.BodyChanged {
			also = append(also, "body changed")
		}
		if c.State != "kind_changed" && c.KindChanged {
			also = append(also, "kind changed")
		}
		if c.State != "retitled" && c.Retitled {
			also = append(also, "retitled")
		}
		if len(also) > 0 {
			detail += " (also " + strings.Join(also, ", ") + ")"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.State, name, detail, where)
	}
	w.Flush()
	return b.String()
}

func renderSkipped(skipped []string) string {
	if len(skipped) == 0 {
		return ""
	}
	return "defined more than once, not compared: " + strings.Join(skipped, ", ") + " (spec lint shows where)\n"
}
