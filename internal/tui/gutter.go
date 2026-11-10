// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/proposal"
)

// barKind is the background of a line's marker cell
// (plan-step4d-ergonomics.md, part 1; plan-step4e-review-flow.md).
type barKind int

const (
	barNone barKind = iota
	// barAnchor: a line of an anchor that differs from HEAD (new or
	// changed), always shown.
	barAnchor
	// barFix: a line a suggested edit of the shown proposal would modify.
	barFix
	// barFixStale: the same, for an edit that no longer applies.
	barFixStale
	// barProseAdd, barProseDel and barProseBoth: a line of prose (outside
	// the changed anchors) whose words were added, removed, or both: a
	// thin pipe in the marker cell, not a bar.
	barProseAdd
	barProseDel
	barProseBoth
	// barFormat: a line that differs from HEAD in whitespace or wrapping
	// only: a thin cyan pipe.
	barFormat
	// barOther: while a proposal is shown, a line of the checked anchor
	// edited after the check saw it.
	barOther
)

// shownProposal is the proposal the gutter previews: the one under
// review, or the selected card's while the sidebar is visible.
type shownProposal struct {
	p *proposal.Proposal
	// hidden are the hunks no longer shown: skipped in the review, or
	// decided in the sidebar.
	hidden map[int]bool
	// stale are the hunks the review found stale on disk.
	stale map[int]string
}

// shown returns the proposal the gutter previews, or nil.
func (a *App) shown() *shownProposal {
	if r, ok := a.top().(*reviewScreen); ok {
		sp := &shownProposal{p: r.p, hidden: map[int]bool{}, stale: r.stale}
		for n, st := range r.state {
			if st == hunkSkipped {
				sp.hidden[n] = true
			}
		}
		return sp
	}
	if !a.sideShown || len(a.screens) > 1 {
		return nil
	}
	c := a.previewCard()
	if c == nil {
		return nil
	}
	sp := &shownProposal{p: c.p, hidden: map[int]bool{}}
	for _, h := range c.p.Hunks {
		if a.decided(c.p.ID, fmt.Sprintf("h%d", h.N)) != "" {
			sp.hidden[h.N] = true
		}
	}
	return sp
}

// locateHunk finds hunk h in the buffer src: its find text when it occurs
// there once (fresh). Otherwise (stale) it finds it in disk, the file as
// last read or written (or else takes the range the hunk was made
// against), and maps that through the unsaved edits. ok is false when
// there is nothing to show.
func locateHunk(disk, src string, h finding.Hunk) (r pos.Range, fresh, ok bool) {
	if h.Find != "" && strings.Count(src, h.Find) == 1 {
		i := strings.Index(src, h.Find)
		return pos.Range{Start: i, End: i + len(h.Find)}, true, true
	}
	switch {
	case h.Find != "" && strings.Count(disk, h.Find) == 1:
		i := strings.Index(disk, h.Find)
		r = pos.Range{Start: i, End: i + len(h.Find)}
	case h.Range.Start >= 0 && h.Range.Start <= len(disk):
		r = pos.Range{Start: h.Range.Start, End: min(max(h.Range.End, h.Range.Start), len(disk))}
	default:
		return pos.Range{}, false, false
	}
	if disk == src {
		return r, false, true
	}
	start := mapOffset(disk, src, r.Start)
	return pos.Range{Start: start, End: max(mapOffset(disk, src, r.End), start)}, false, true
}

// diskOf is path's content on disk as the UI last saw it (its buffer's
// source when it is not open).
func (a *App) diskOf(path, src string) string {
	if d, ok := a.ws.Docs()[path]; ok {
		return string(d.Disk)
	}
	return src
}

// fixHunks are the shown proposal's suggested edits that still count:
// every hunk but the change itself and the skipped or decided ones.
func (a *App) fixHunks(sp *shownProposal) []finding.Hunk {
	if sp == nil {
		return nil
	}
	var out []finding.Hunk
	for _, h := range sp.p.Hunks {
		if (sp.p.Primary != nil && sp.p.Primary.N == h.N) || sp.hidden[h.N] {
			continue
		}
		out = append(out, h)
	}
	return out
}

// checkedAnchor is the anchor of file f the shown proposal checked (a
// change proposal's anchor, or the one holding a fix's finding), or nil.
func checkedAnchor(sp *shownProposal, f *mdspec.File) *mdspec.Anchor {
	p := sp.p
	switch {
	case p.Primary != nil && p.Primary.File == f.Path:
		for _, an := range f.Anchors {
			if "$"+an.Name == p.Primary.Name {
				return an
			}
		}
	case p.Fix != nil && p.Fix.File == f.Path:
		if off, ok := f.Index().Offset(pos.Position{Line: p.Fix.Line, Col: 1}); ok {
			return f.AnchorAt(off)
		}
	}
	return nil
}

// gutterBars returns the bar of each 1-based line of path's buffer: the
// anchors changed vs HEAD and the prose whose words changed, always; the
// shown proposal's edits while one is shown. Whitespace and wrapping
// changes (a ^F reflow) are not marked. It reads the current analysis
// without reparsing: it runs on every frame.
func (a *App) gutterBars(path string) map[int]barKind {
	f := a.ws.File(path)
	if f == nil || a.ws.Base == nil {
		return nil
	}
	src := string(a.ws.Source(path))
	x := pos.NewIndex([]byte(src))
	bars := map[int]barKind{}
	mark := func(r pos.Range, k barKind) {
		last := x.Line(max(r.End-1, r.Start))
		for l := x.Line(r.Start); l <= last; l++ {
			bars[l] = k
		}
	}
	// Green: every anchor that differs from HEAD, label through body.
	for _, an := range f.Anchors {
		if r := (pos.Range{Start: an.LabelRange.Start, End: an.BodyRange.End}); a.changeState(an) != "" && r.Valid(len(src)) {
			mark(r, barAnchor)
		}
	}
	// Thin pipes: prose whose words changed (green added, red removed,
	// yellow both), and lines only reflowed (cyan).
	if fe, ok := a.fileEditsOf(path, src); ok {
		kinds := map[int]barKind{}
		for _, e := range fe.content {
			k := barProseAdd
			switch {
			case len(e.added) > 0 && len(e.removed) > 0:
				k = barProseBoth
			case len(e.added) == 0:
				k = barProseDel
			}
			r := pos.Range{Start: min(e.start, len(src)), End: min(e.end, len(src))}
			for l := x.Line(r.Start); l <= x.Line(max(r.End-1, r.Start)); l++ {
				switch old := kinds[l]; {
				case old == barNone || old == k:
					kinds[l] = k
				default:
					kinds[l] = barProseBoth
				}
			}
		}
		for l, k := range kinds {
			if bars[l] == barNone {
				bars[l] = k
			}
		}
		for _, l := range fe.format {
			if bars[l] == barNone {
				bars[l] = barFormat
			}
		}
	}
	sp := a.shown()
	if sp == nil {
		return bars
	}
	// Purple: words of the checked anchor edited since the check saw them.
	if an := checkedAnchor(sp, f); an != nil {
		if r := (pos.Range{Start: an.LabelRange.Start, End: an.BodyRange.End}); r.Valid(len(src)) && sp.p.Primary != nil {
			seen := sp.p.Primary.OldText
			if sp.p.Primary.InWorktree {
				seen = sp.p.Primary.NewText
			}
			for _, e := range contentEdits(seen, src[r.Start:r.End]) {
				mark(pos.Range{Start: r.Start + e.start, End: r.Start + e.end}, barOther)
			}
		}
	}
	// Cyan: the suggested edits, over everything else.
	for _, h := range a.fixHunks(sp) {
		if h.File != path {
			continue
		}
		r, fresh, ok := locateHunk(a.diskOf(path, src), src, h)
		if !ok {
			continue
		}
		_, stale := sp.stale[h.N]
		k := barFix
		if !fresh || stale {
			k = barFixStale
		}
		mark(r, k)
	}
	return bars
}

// gutterChangedLines identifies the actual changed lines inside anchors
// whose marker cells carry a broad changed-extent fill. Callers combine this
// with gutterBars so the thin centered pipe can sit over that fill; a
// diagnostic still owns the foreground cell.
func (a *App) gutterChangedLines(path string) map[int]bool {
	f := a.ws.File(path)
	if f == nil || a.ws.Base == nil {
		return nil
	}
	src := string(a.ws.Source(path))
	fe, ok := a.fileEditsOf(path, src)
	if !ok {
		return nil
	}
	x := pos.NewIndex([]byte(src))
	var anchors []pos.Range
	for _, an := range f.Anchors {
		r := pos.Range{Start: an.LabelRange.Start, End: an.BodyRange.End}
		if a.changeState(an) != "" && r.Valid(len(src)) {
			anchors = append(anchors, r)
		}
	}
	lines := map[int]bool{}
	for _, edit := range fe.content {
		r := pos.Range{Start: min(edit.start, len(src)), End: min(edit.end, len(src))}
		inside := false
		for _, anchor := range anchors {
			if r.Start <= anchor.End && r.End >= anchor.Start {
				inside = true
				break
			}
		}
		if !inside {
			continue
		}
		for line := x.Line(r.Start); line <= x.Line(max(r.End-1, r.Start)); line++ {
			lines[line] = true
		}
	}
	return lines
}

// fixStop is a suggested fix ^] stops on.
type fixStop struct {
	path  string
	start int
	line  int
	label string
}

// fixStops are the shown proposal's suggested edits in project order,
// where ^] stops besides the edits.
func (a *App) fixStops() []fixStop {
	var out []fixStop
	hunks := a.fixHunks(a.shown())
	for _, p := range a.ws.Paths() {
		src := string(a.ws.Source(p))
		x := pos.NewIndex([]byte(src))
		for _, h := range hunks {
			if h.File != p {
				continue
			}
			r, _, ok := locateHunk(a.diskOf(p, src), src, h)
			if !ok {
				continue
			}
			label := h.Anchor
			if label == "" {
				label = fmt.Sprintf("line %d", x.Line(r.Start))
			}
			out = append(out, fixStop{path: p, start: r.Start, line: x.Line(r.Start), label: label})
		}
	}
	paths := a.ws.Paths()
	slices.SortStableFunc(out, func(x, y fixStop) int {
		if c := cmp.Compare(slices.Index(paths, x.path), slices.Index(paths, y.path)); c != 0 {
			return c
		}
		return cmp.Compare(x.start, y.start)
	})
	return out
}
