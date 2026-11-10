// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/termtext"
)

// Outline glyphs: invariants and definitions differ by glyph and color.
const (
	glyphInv = "•"
	glyphDef = "◦"
)

// entryKind is what an outline row shows.
type entryKind int

const (
	entryFile entryKind = iota
	entrySection
	entryAnchor
	// entryDecl is a declaration of a formal model.
	entryDecl
)

// outlineEntry is one row of the outline.
type outlineEntry struct {
	kind   entryKind
	depth  int
	text   string
	anchor mdspec.Kind
	loc    Location
	// counts are the file's diagnostics (file rows only).
	errs, warns, hints int
	// an is the anchor of an anchor row.
	an *mdspec.Anchor
	// declKind and tagged are a declaration row's kind, and whether it has tags.
	declKind formal.DeclKind
	tagged   bool
	// stale marks a declaration row kept from the model's last good scan
	// whose place in the text is unknown (formal.File.Stale): it is drawn
	// dimmed, and selecting it jumps nowhere.
	stale bool
}

// outline is the left pane: files, each with its sections and anchors.
type outline struct {
	entries []outlineEntry
	sel     int
	scroll  int
	// shown is the selection the view last scrolled to; the view follows
	// the selection only when it changed, so the wheel can scroll it away.
	shown int
	// shownH is the height of the last view: a shorter pane (a resize, a
	// taller footer) brings the selection into view again.
	shownH int
}

func newOutline() *outline {
	return &outline{}
}

// rebuild recomputes the entries from the workspace, keeping the selection
// on the same location where possible.
func (o *outline) rebuild(ws *Workspace) {
	var keep, top Location
	if o.sel < len(o.entries) {
		keep = o.entries[o.sel].loc
	}
	hadTop := o.scroll < len(o.entries)
	if hadTop {
		top = o.entries[o.scroll].loc
	}
	wasSel := o.sel
	o.entries = o.entries[:0]
	for _, p := range ws.Paths() {
		f := ws.File(p)
		if f == nil {
			continue
		}
		ds := ws.FileDiags(p)
		o.entries = append(o.entries, outlineEntry{
			kind: entryFile, text: p, loc: Location{Path: p},
			errs: diag.Count(ds, diag.Error), warns: diag.Count(ds, diag.Warning), hints: diag.Count(ds, diag.Hint),
		})
		type item struct {
			off int
			e   outlineEntry
		}
		var items []item
		for _, s := range f.Sections {
			items = append(items, item{s.HeadingRange.Start, outlineEntry{
				kind: entrySection, depth: max(s.Level-1, 1), text: s.Number + " " + s.Title, loc: sectionLoc(s),
			}})
		}
		for _, an := range f.Anchors {
			depth := 1
			if s := f.SectionAt(an.LabelRange.Start); s != nil {
				depth = max(s.Level-1, 1) + 1
			}
			text := "$" + an.Name
			if an.Form == mdspec.FormSection {
				// A section-form anchor's heading is its own entry.
				depth = max(depth-1, 1)
			}
			items = append(items, item{an.LabelRange.Start, outlineEntry{
				kind: entryAnchor, depth: depth, text: text, anchor: an.Kind, loc: anchorLoc(an), an: an,
			}})
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].off < items[j].off })
		for _, it := range items {
			o.entries = append(o.entries, it.e)
		}
	}
	for _, p := range ws.ModelPaths() {
		m := ws.Model(p)
		if m == nil {
			continue
		}
		ds := ws.FileDiags(p)
		o.entries = append(o.entries, outlineEntry{
			kind: entryFile, text: p, loc: Location{Path: p},
			errs: diag.Count(ds, diag.Error), warns: diag.Count(ds, diag.Warning), hints: diag.Count(ds, diag.Hint),
		})
		tagged := cites(m)
		var rows []outlineEntry
		for _, d := range m.Decls {
			rows = append(rows, outlineEntry{
				kind: entryDecl, depth: 1, text: d.Name, loc: Location{Path: p, Off: d.NameRange.Start},
				declKind: d.Kind, tagged: tagged[d],
			})
		}
		for _, d := range m.Stale {
			rows = append(rows, outlineEntry{
				kind: entryDecl, depth: 1, text: d.Name, loc: Location{Path: p, Off: d.NameRange.Start},
				declKind: d.Kind, stale: true,
			})
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].loc.Off < rows[j].loc.Off })
		o.entries = append(o.entries, rows...)
	}
	o.sel = 0
	for k, e := range o.entries {
		if e.loc == keep && !e.stale {
			o.sel = k
			if o.shown == wasSel {
				o.shown = k // the same row: no reason to scroll to it
			}
			break
		}
	}
	// Rows came or went above the view: it goes with its top row.
	if k := slices.IndexFunc(o.entries, func(e outlineEntry) bool { return e.loc == top }); hadTop && k >= 0 {
		o.scroll = k
	}
}

// selectNear selects the last entry at or before loc.
func (o *outline) selectNear(loc Location) {
	best := -1
	for k, e := range o.entries {
		if e.loc.Path != loc.Path || e.stale {
			continue
		}
		if e.loc.Off <= loc.Off {
			best = k
		}
	}
	if best >= 0 {
		o.sel = best
	}
}

// move moves the selection by n rows.
func (o *outline) move(n int) {
	if len(o.entries) == 0 {
		return
	}
	o.sel = min(max(o.sel+n, 0), len(o.entries)-1)
}

// scrollBy scrolls the pane by n rows.
func (o *outline) scrollBy(n, h int) {
	o.scroll = min(max(o.scroll+n, 0), max(len(o.entries)-h, 0))
}

// selected returns the selected entry's location.
func (o *outline) selected() (Location, bool) {
	if o.sel < 0 || o.sel >= len(o.entries) || o.entries[o.sel].stale {
		return Location{}, false
	}
	return o.entries[o.sel].loc, true
}

// click selects the entry on screen row y.
func (o *outline) click(y int) (Location, bool) {
	k := o.scroll + y
	if k < 0 || k >= len(o.entries) {
		return Location{}, false
	}
	o.sel = k
	if o.entries[k].stale {
		return Location{}, false
	}
	return o.entries[k].loc, true
}

// view renders h rows of w cells.
func (o *outline) view(a *App, w, h int) []string {
	th := a.theme
	if o.sel != o.shown || h < o.shownH {
		if o.sel < o.scroll {
			o.scroll = o.sel
		}
		if o.sel >= o.scroll+h {
			o.scroll = o.sel - h + 1
		}
		o.shown = o.sel
	}
	o.shownH = h
	o.scroll = min(max(o.scroll, 0), max(len(o.entries)-h, 0))
	// Outstanding sidebar cards per anchor.
	open := map[string]int{}
	for _, c := range a.side.cards {
		if an := c.anchor(); an != "" && a.openCard(c) {
			open[an]++
		}
	}
	out := make([]string, 0, h)
	for k := o.scroll; k < len(o.entries) && len(out) < h; k++ {
		e := o.entries[k]
		indent := strings.Repeat(" ", e.depth)
		var line string
		switch e.kind {
		case entryFile:
			counts := ""
			if e.errs+e.warns+e.hints > 0 {
				counts = fmt.Sprintf(" E%d W%d H%d", e.errs, e.warns, e.hints)
			}
			style := th.OutlineFile
			if k == o.sel && a.outlineFocus {
				style = style.Bold(true)
			}
			line = style.Render(e.text) + th.PopupDim.Render(counts)
		case entrySection:
			style := th.OutlineSec
			if k == o.sel && a.outlineFocus {
				style = style.Bold(true)
			}
			line = indent + style.Render(th.uiText("§")+e.text)
		case entryAnchor:
			glyph, st := glyphInv, th.OutlineInv
			if e.anchor == mdspec.KindDefinition {
				glyph, st = glyphDef, th.OutlineDef
			}
			// A changed anchor gets the gutter's change bar in front;
			// one with open cards a count.
			lead := indent
			if e.an != nil && a.changeState(e.an) != "" {
				lead = indent[:len(indent)-1] + th.Bar[barAnchor].Render(" ")
			}
			if k == o.sel && a.outlineFocus {
				st = st.Bold(true)
			}
			line = lead + st.Render(th.uiText(glyph)+" "+e.text)
			if n := open[e.text]; n > 0 {
				line += " " + th.SideHeadFocus.Render(fmt.Sprintf("%d", n))
			}
		case entryDecl:
			// Filled for a declaration tied to the spec by a tag, hollow for
			// one that is not; colour says what it declares.
			glyph, mark := glyphDef, th.Structure
			if e.tagged {
				glyph, mark = glyphInv, th.FocusEdge
			}
			st := th.OutlineDef
			if e.declKind == formal.DeclVariable || e.declKind == formal.DeclConstant {
				st = th.Secondary
			}
			if e.stale {
				mark, st = th.PopupDim, th.PopupDim
			}
			if k == o.sel && a.outlineFocus {
				st = st.Bold(true)
			}
			line = indent + mark.Render(th.uiText(glyph)) + " " + st.Render(termtext.Clean(e.text))
		}
		line = padTo(line, w)
		if k == o.sel && a.outlineFocus {
			line = th.SideCardFocus.Render(line)
		}
		out = append(out, line)
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out
}
