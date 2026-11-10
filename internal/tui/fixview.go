// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/tui/editor"
)

// How problems and fixes are drawn (fixcards.go has what they are): the
// group's title, the problem and fix boxes in the sidebar, and the
// selected fix in place in the editor.

// ---- the editor: the selected fix in place ----

// fixPreview is the selected fix shown in the text of path while the
// sidebar is visible: the words it replaces struck through, the words it
// puts in as ghost text after them.
func (a *App) fixPreview(path string) ([]editor.Span, []editor.Ghost) {
	if !a.sideShown || len(a.screens) > 1 {
		return nil, nil
	}
	c := a.previewCard()
	if c == nil || c.kind != cardEdit || c.hunk.File != path {
		return nil, nil
	}
	if a.fixState(c.p, c.hunk) != fixPending || a.checkOutdated(c.p) != "" {
		return nil, nil
	}
	src := string(a.ws.Source(path))
	at := strings.Index(src, c.hunk.Find)
	if at < 0 {
		return nil, nil
	}
	var spans []editor.Span
	var ghosts []editor.Ghost
	arrow := a.theme.uiText("→")
	if a.theme.ASCII {
		arrow = "->"
	}
	// The buffer holds find; compared with replace, each run of find that
	// differs is what the fix removes, and the words of replace it lacks
	// (removed, from replace's side) are what the fix puts in.
	for _, e := range contentEdits(c.hunk.Replace, c.hunk.Find) {
		in := strings.Join(e.removed, " ")
		if e.end > e.start {
			spans = append(spans, editor.Span{Start: at + e.start, End: at + e.end, Flags: editor.FFixOld})
			if in != "" {
				ghosts = append(ghosts, editor.Ghost{Off: at + e.end, Text: arrow + in, Flags: editor.FFixNew})
			}
			continue
		}
		if e.ghost != "" {
			ghosts = append(ghosts, editor.Ghost{Off: at + e.ghostAt, Text: e.ghost, Flags: editor.FFixNew})
		}
	}
	return spans, ghosts
}

// sortGhosts orders gs by offset (SetGhosts wants them so).
func sortGhosts(gs []editor.Ghost) []editor.Ghost {
	sort.SliceStable(gs, func(i, j int) bool { return gs[i].Off < gs[j].Off })
	return gs
}

// ---- the view ----

// groupHead renders a check's title band: "Your edit to $Name" (the id
// small and dim) for a check of Chris's edit, else what the proposal is.
// The group card uses the proposal's provenance as its source accent.
func (a *App) groupHead(p *proposal.Proposal, w int, sel bool) []string {
	th := a.theme
	body := []string{th.Focus.Bold(true).Render(proposalLabel(p))}
	if p.Primary != nil {
		body = append(body, th.PopupDim.Render(cardValue(p.Primary.Name, w-4, th)))
	} else if p.Fix != nil {
		body = append(body, th.PopupDim.Render(cardValue(p.Fix.Code, w-4, th)))
	}
	if p.Rethink != nil {
		body = append(body, th.PopupDim.Render(ansi.Truncate(th.uiText("rethought with your answer · was ")+p.Rethink.Of, max(w-4, 1), th.uiText("…"))))
	}
	return filledCard(body, max(w-4, 1), cardOriginStyle(th, p.SourceKind), th, false, sel)
}

// problemBox renders problem card c: severity and issue, the sides it is
// between, the explanation and where it stands. The card keeps its origin
// accent while severity colors only its small badge.
func (a *App) problemBox(c *card, sel bool, inner int) []string {
	th := a.theme
	pr := c.prob
	st, open := a.problemState(c.p, pr)
	outdated := a.checkOutdated(c.p) != ""
	col := th.sevColor(pr.severity)
	text := lipgloss.NewStyle()
	if outdated || st == probResolved || st == probHand || st == probDismissed {
		col = th.dim
	}
	if outdated {
		text = th.PopupDim
	}
	var body []string
	add := func(ls ...string) { body = append(body, ls...) }
	wrapped := func(s string, style lipgloss.Style, maxLines int) {
		ls := wrapText(termtext.Clean(oneLine(s)), inner)
		if maxLines > 0 && len(ls) > maxLines {
			ls = append(ls[:maxLines-1], ansi.Truncate(ls[maxLines-1], max(inner-ansi.StringWidth(a.theme.uiText("…")), 0), "")+a.theme.uiText("…"))
		}
		for _, l := range ls {
			add(style.Render(l))
		}
	}
	badge := lipgloss.NewStyle().Foreground(col).Bold(true).Render(pr.severity)
	if outdated {
		badge += " " + th.Stale.Render("OUTDATED")
	}
	head := badge
	if pr.issue != "" {
		head += " " + th.HunkHead.Render(ansi.Truncate(termtext.Clean(pr.issue), max(inner-ansi.StringWidth(badge)-1, 1), a.theme.uiText("…")))
	}
	add(head)
	if rethinkPrimary(c.p, pr.own) {
		add(th.Stale.Render("changes your edit"))
	}
	if a.waitingOn(c.p.ID) {
		wrapped(a.waitText(), th.Stale, 0)
	}
	limit := 2
	if sel {
		limit = 0
	}
	for _, scope := range a.problemScopeFields(c) {
		display := cardValue(scope, inner, th)
		if !strings.HasPrefix(scope, "$") {
			display = settingsPath(oneLine(termtext.Clean(scope)), inner, th)
		}
		add(th.PopupDim.Render(display))
	}
	wrapped(pr.explanation, text, limit)
	if sel && pr.own == 0 && pr.find != "" && pr.reason != "" {
		wrapped("its own edit was dropped: "+pr.reason, th.PopupDim, 0)
	}
	n := len(pr.fixes)
	switch st {
	case probNoFix:
		wrapped("No fix proposed", th.Stale, 0)
		wrapped("Edit by hand", th.PopupDim, 0)
	case probOpen:
		if n > 1 && n-open > 0 {
			wrapped(fmt.Sprintf("%d of %d fixes decided", n-open, n), th.PopupDim, 0)
		}
	case probSkipped:
		s := "Fix skipped"
		if n > 1 {
			s = "Fixes skipped"
		}
		wrapped(s, th.Stale, 0)
	case probResolved:
		applied := false
		for _, n := range pr.fixes {
			applied = applied || c.p.AppliedHunk(n)
		}
		if applied {
			wrapped("Fix applied", th.Accepted, 0)
			wrapped("Check again to verify", th.PopupDim, 0)
		} else {
			wrapped("settled", th.PopupDim, 0)
		}
	case probHand:
		wrapped(a.theme.uiText("✓ resolved by hand"), th.Accepted, 0)
	case probDismissed:
		wrapped("dismissed", th.Skipped, 0)
	}
	return filledCard(body, inner, cardOriginStyle(th, c.p.SourceKind), th, false, sel)
}

// fixBox renders fix card c: where it edits, the compact word diff, its
// state. Its left accent is teal, and it sits inset beneath the problem it
// addresses.
func (a *App) fixBox(c *card, sel bool, inner int) []string {
	th := a.theme
	fi := max(inner-2, 6)
	h := c.hunk
	st := a.fixState(c.p, h)
	why := a.checkOutdated(c.p)
	var body []string
	add := func(ls ...string) { body = append(body, ls...) }
	wrapped := func(s string, style lipgloss.Style) {
		for _, l := range wrapText(termtext.Clean(oneLine(s)), fi) {
			add(style.Render(l))
		}
	}
	title := "Suggested fix"
	loc := h.File
	if l, ok := a.hunkLoc(h); ok {
		loc = fmt.Sprintf("%s:%d", l.Path, a.ws.Line(l.Path, l.Off))
	}
	add(th.FixHead.Render(title))
	if rethinkPrimary(c.p, h.N) {
		add(th.Stale.Render("changes your edit"))
	}
	if h.Anchor != "" {
		add(th.PopupDim.Render(cardValue(h.Anchor, fi, th)))
	}
	add(th.PopupDim.Render(settingsPath(oneLine(termtext.Clean(loc)), fi, th)))
	if a.waitingOn(c.p.ID) {
		wrapped(a.waitText(), th.Stale)
	}
	if c.cl != nil && !slices.ContainsFunc(c.cl.problems, func(pr *problem) bool { return pr.own == h.N }) {
		// A fix only (its finding is no problem of its own): why this
		// edit, in its finding's words.
		ls := wrapText(termtext.Clean(oneLine(h.Explanation)), fi)
		if !sel && len(ls) > 2 {
			ls = append(ls[:1], ansi.Truncate(ls[1], max(fi-ansi.StringWidth(a.theme.uiText("…")), 0), "")+a.theme.uiText("…"))
		}
		for _, l := range ls {
			add(th.PopupDim.Render(l))
		}
	}
	if sel && a.side.format[c.id()] && st == fixPending && why == "" {
		add(a.formattedPreview(c, fi)...)
	} else {
		add(compactDiff(th, h.Find, h.Replace, fi)...)
	}
	switch {
	case c.p.AppliedHunk(h.N):
		wrapped("applied to buffer", th.Accepted)
	case st == fixAccepted:
		wrapped(a.theme.uiText("✓ accepted"), th.Accepted)
	case st == fixEdited:
		wrapped(a.theme.uiText("✓ edited and accepted"), th.Accepted)
	case st == fixSkipped:
		wrapped("skipped", th.Skipped)
	case st == fixByHand:
		wrapped(a.theme.uiText("✓ done by hand"), th.Accepted)
	case why != "":
		wrapped(a.theme.uiText("Outdated · ")+why, th.Stale)
	case st == fixHand:
		wrapped("done by hand", th.Accepted) // recorded on the reparse
	case st == fixHandUnsure:
		wrapped(a.theme.uiText("Done by hand · confirm"), th.Accepted)
		wrapped("unsure: "+a.handDoubt(h, string(a.ws.Source(h.File))), th.PopupDim)
	case st == fixGone:
		wrapped("No longer applies", th.PopupDim)
	case st == fixStale:
		wrapped(a.theme.uiText("Stale · ")+a.cardStale(c), th.Stale)
	default:
		if c.p.ChecksBlocking() {
			wrapped("checking", th.PopupDim)
		} else {
			add(lipgloss.NewStyle().Foreground(th.fix).Render("pending"))
		}
	}
	box := a.insetFix(body, fi, sel)
	for k := range box {
		box[k] = "  " + box[k]
	}
	return box
}

// compactDiff renders the change from find to replace as its changed
// words, each with the word before it: "within 1 → within 2". A change
// too wide for one line takes two ("- …", "+ …"); after four changes the
// rest are counted.
func compactDiff(th *Theme, find, replace string, w int) []string {
	es := contentEdits(find, replace)
	if len(es) == 0 {
		return []string{th.PopupDim.Render("whitespace only")}
	}
	ow := words(find)
	var out []string
	for k, e := range es {
		if k == 4 {
			out = append(out, th.PopupDim.Render(fmt.Sprintf(th.uiText("… and %d more changes"), len(es)-k)))
			break
		}
		ctx := ""
		for _, x := range ow {
			if x[1] <= e.oldStart {
				ctx = find[x[0]:x[1]]
			}
		}
		ctx = termtext.Clean(ctx)
		old := termtext.Clean(strings.Join(e.removed, " "))
		cur := termtext.Clean(strings.Join(e.added, " "))
		side := func(words string, st lipgloss.Style) string {
			switch {
			case ctx == "":
				return st.Render(words)
			case words == "":
				return ctx
			}
			return ctx + " " + st.Render(words)
		}
		plainLen := func(words string) int {
			if ctx == "" || words == "" {
				return ansi.StringWidth(ctx + words)
			}
			return ansi.StringWidth(ctx) + 1 + ansi.StringWidth(words)
		}
		if plainLen(old)+3+plainLen(cur) <= w {
			arrow := th.uiText(" → ")
			if th.ASCII {
				arrow = " -> "
			}
			out = append(out, side(old, th.DiffDel)+th.PopupDim.Render(arrow)+side(cur, th.DiffAdd))
			continue
		}
		two := func(sign, words string, st lipgloss.Style) {
			text := strings.TrimSpace(ctx + " " + words)
			ls := wrapText(text, max(w-2, 4))
			if len(ls) > 2 {
				ls = append(ls[:1], ansi.Truncate(ls[1], max(w-2-ansi.StringWidth(th.uiText("…")), 0), "")+th.uiText("…"))
			}
			for j, l := range ls {
				lead := "  "
				if j == 0 {
					lead = sign + " "
				}
				out = append(out, st.Render(lead+l))
			}
		}
		two("-", old, th.DiffDel)
		two("+", cur, th.DiffAdd)
	}
	return out
}

// Scope metadata is rendered separately from application-owned sentences.
func (a *App) problemScopeFields(c *card) []string {
	var fields []string
	seen := map[string]bool{}
	add := func(anchor, file string, loc Location, found bool) {
		if found {
			file = fmt.Sprintf("%s:%d", loc.Path, a.ws.Line(loc.Path, loc.Off))
		}
		key := anchor + "\x00" + file
		if seen[key] {
			return
		}
		seen[key] = true
		if anchor != "" && (c.p.Primary == nil || anchor != c.p.Primary.Name) {
			fields = append(fields, anchor)
		}
		if file != "" {
			fields = append(fields, file)
		}
	}
	if c.prob.own > 0 {
		h, _ := hunkOf(c.p, c.prob.own)
		loc, ok := a.hunkLoc(h)
		add(h.Anchor, h.File, loc, ok)
	} else {
		loc, ok := a.noteLoc(c.prob)
		add(c.prob.anchor, c.prob.file, loc, ok && (c.prob.find != "" || c.prob.anchor != ""))
	}
	for _, n := range c.prob.fixes {
		h, _ := hunkOf(c.p, n)
		loc, ok := a.hunkLoc(h)
		add(h.Anchor, h.File, loc, ok)
	}
	return fields
}
