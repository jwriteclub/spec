// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/workflow"
)

// changeState says how anchor an differs from HEAD: "changed", "new", or ""
// (unchanged, or no baseline).
func (a *App) changeState(an *mdspec.Anchor) string {
	b := a.ws.Base
	if b == nil || b.State == baseline.StateUnavailable || b.Spec == nil {
		return ""
	}
	if b.State == baseline.StateUnborn {
		return "new"
	}
	old := b.Spec.Lookup(an.Name)
	switch {
	case len(old) == 0:
		return "new"
	case len(old) == 1 && old[0].Hash != an.Hash:
		return "changed"
	}
	return ""
}

// anchorDiffCache is the anchor comparison of one parsed spec with one
// baseline.
type anchorDiffCache struct {
	spec *mdspec.Spec
	base *baseline.Baseline
	diff *baseline.Diff
}

// anchorDiff compares the parsed spec with HEAD by anchor, as spec status
// and spec propose do: renames detected, and HEAD's references renamed to
// match before bodies are compared. Nil without a baseline.
func (a *App) anchorDiff() *baseline.Diff {
	b := a.ws.Base
	if a.ws.Spec == nil || b == nil || b.State == baseline.StateUnavailable || b.Spec == nil && b.State != baseline.StateUnborn {
		return nil
	}
	if c := &a.diffCache; c.spec != a.ws.Spec || c.base != b || c.diff == nil {
		*c = anchorDiffCache{spec: a.ws.Spec, base: b, diff: baseline.Compare(b.Spec, a.ws.Spec)}
	}
	return a.diffCache.diff
}

// needsCheck reports whether anchor an's change is one a check reads: it
// was added, or its body or kind changed. A rename alone is not, nor an
// anchor whose references only follow a rename: the commit records those
// with Spec-Rename lines, and spec propose skips them.
func (a *App) needsCheck(an *mdspec.Anchor) bool {
	d := a.anchorDiff()
	if d == nil {
		return false
	}
	for _, e := range d.Entries {
		if e.New == an {
			return e.NeedsChangeTrailer()
		}
	}
	return false
}

// anyNeedsCheck reports whether any anchor's change is one a check reads.
func (a *App) anyNeedsCheck() bool {
	d := a.anchorDiff()
	if d == nil {
		return false
	}
	for _, e := range d.Entries {
		if e.New != nil && e.NeedsChangeTrailer() {
			return true
		}
	}
	return false
}

// renamesText lists the renames since HEAD ("$Old was renamed to $New,
// ..."), "" when there are none.
func (a *App) renamesText() string {
	d := a.anchorDiff()
	if d == nil || len(d.Renames) == 0 {
		return ""
	}
	olds := slices.Sorted(maps.Keys(d.Renames))
	parts := make([]string, 0, len(olds))
	for _, o := range olds {
		parts = append(parts, fmt.Sprintf("$%s was renamed to $%s", o, d.Renames[o]))
	}
	return strings.Join(parts, ", ")
}

// nothingToCheckRenamed is the flash when the only changes are renames.
func nothingToCheckRenamed(renames string) string {
	return "Nothing to check: " + renames + "; the commit records renames with Spec-Rename lines"
}

type statusKind int

const (
	statusInfo statusKind = iota
	statusSuccess
	statusWarning
	statusFailure
	statusAgent
	statusHuman
	statusFix
	statusPartial
)

type statusExpiryMsg struct{ seq int }

func (a *App) setStatus(kind statusKind, accent, format string, args ...any) {
	// Background arrivals cannot replace an unresolved failure.
	if a.flash != "" && (a.flashKind == statusFailure || a.flashKind == statusPartial) && (kind == statusAgent || kind == statusHuman || kind == statusFix || kind == statusInfo || kind == statusWarning) {
		return
	}
	a.statusHover = ""
	a.statusTip = nil
	a.flash = termtext.Clean(fmt.Sprintf(format, args...))
	a.flashAccent, a.flashKind = termtext.Clean(accent), kind
	a.flashSeq++
	duration := 4 * time.Second
	if kind == statusAgent || kind == statusHuman || kind == statusWarning {
		duration = 6 * time.Second
	}
	a.flashUntil = a.now().Add(duration)
	a.keepFlash = true
}
func (a *App) statusExpiry() tea.Cmd {
	if a.flash == "" || (a.flashKind == statusFailure || a.flashKind == statusPartial) {
		return nil
	}
	seq, d := a.flashSeq, a.flashUntil.Sub(a.now())
	if d < 0 {
		d = 0
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return statusExpiryMsg{seq} })
}
func (a *App) footerHeight() int {
	n := 1
	if a.showHelpBar {
		n += 2
		if a.height >= 30 {
			n++
		}
		if a.height > 36 {
			n++
		}
	}
	return n
}
func (a *App) persistence() string {
	if a.cur.Ed.Buffer().Dirty() {
		return "Unsaved"
	}
	if a.inModel() {
		return "Saved" // HEAD's baseline holds only spec files
	}
	b := a.ws.Base
	if b == nil || b.State == baseline.StateUnavailable || b.Spec == nil {
		return "No baseline"
	}
	old := b.Spec.File(a.cur.Ed.Path())
	if old == nil || !bytes.Equal(old.Source(), a.cur.Disk) {
		return "Uncommitted"
	}
	return "Clean"
}
func (a *App) statusTotals() (errors, warnings, hints, proposals, other int) {
	ds := a.ws.FileDiags(a.cur.Ed.Path())
	errors, warnings, hints = diag.Count(ds, diag.Error), diag.Count(ds, diag.Warning), diag.Count(ds, diag.Hint)
	// A proposal counts once, even if it owns several suggested fixes.
	seen := map[string]bool{}
	for _, p := range a.pending {
		if !suggestions(p) || p.SourceKind == "agent" {
			if !seen[p.ID] {
				proposals++
				seen[p.ID] = true
			}
		}
	}
	for _, c := range a.side.cards {
		if (c.kind == cardProblem || c.kind == cardQuestion) && a.openCard(c) {
			other++
		} else if c.kind == cardEdit && (c.cl == nil || len(c.cl.problems) == 0) && a.fixState(c.p, c.hunk) == fixPending {
			other++
		}
	}
	return
}
func countText(n int) string {
	if n > 99 {
		return "99+"
	}
	return fmt.Sprint(n)
}

type statusSegment struct{ text, background string }

func (a *App) statusSegments(w int) []statusSegment {
	th := a.theme
	base := lipgloss.NewStyle().Foreground(th.Token("statusInk")).Background(th.Token("status"))
	role := func(name, text string) string { return base.Foreground(th.Token(name)).Render(text) }
	if w < 48 {
		return []statusSegment{{base.Render(padTo(" "+a.cur.Ed.Path()+" "+a.persistence(), w)), "status"}}
	}
	lw := 32
	if w >= 140 {
		lw = 48
	} else if w >= 100 {
		lw = 36
	}
	rw := 16
	mw := w - lw - rw - 2
	p := a.cur.Ed.Buffer().Index().Position(a.cur.Ed.Cursor())
	pos := fmt.Sprintf("%d:%d", p.Line, p.Col)
	state := a.persistence()
	// Filename gives up room first. State and the delimiter stay in fixed cells.
	pathw := max(lw-1-1-11-1-len(pos)-1, 1)
	pathText := termtext.Clean(a.cur.Ed.Path())
	if ansi.StringWidth(pathText) > pathw {
		pathText = path.Base(pathText)
	}
	path := ansi.Truncate(pathText, pathw, th.uiText("…"))
	left := base.Render(" " + padTo(path, pathw) + " ")
	stateText := padTo(state, 11)
	if state == "Unsaved" {
		left += role("statusWarning", stateText)
	} else {
		left += base.Render(stateText)
	}
	left += base.Render(" " + pos + " ")
	left = base.Render(padTo(left, lw))
	mid := ""
	if a.statusHover != "" {
		mid = a.statusHover
	} else if a.flash != "" && (a.flashKind == statusFailure || a.flashKind == statusPartial || a.now().Before(a.flashUntil)) {
		mid = a.flash
	}
	if mid == "" {
		if a.wait != nil {
			mid = a.waitStatus()
		} else if a.job != nil {
			mid = a.jobText()
		} else if f := a.ws.File(a.cur.Ed.Path()); f != nil {
			if an := f.AnchorAt(a.cur.Ed.Cursor()); an != nil {
				mid = "$" + an.Name
			}
		} else if m := a.ws.Model(a.cur.Ed.Path()); m != nil {
			mid = modelPlaceName(m, a.cur.Ed.Cursor())
			if note := a.ws.ModelCheck(a.cur.Ed.Path()).Note(); note != "" {
				mid = strings.TrimSpace(mid + "  " + note)
			}
		}
	} else if a.job != nil { // Keep the busy signal visible while a receipt is on screen.
		mid = th.uiText("▪ ") + mid
	}
	mid = ansi.Truncate(termtext.Clean(mid), max(mw-2, 0), th.uiText("…"))
	styled := base.Render(mid)
	if a.statusHover == "" && a.flashAccent != "" && strings.Contains(mid, a.flashAccent) {
		token := map[statusKind]string{statusSuccess: "statusPositive", statusWarning: "statusWarning", statusFailure: "statusError", statusAgent: "statusAgent", statusHuman: "statusHuman", statusFix: "statusFix", statusPartial: "statusWarning"}[a.flashKind]
		if token != "" {
			before, after, _ := strings.Cut(mid, a.flashAccent)
			styled = base.Render(before) + role(token, a.flashAccent) + base.Render(after)
		}
	}
	middle := base.Render(" ") + styled + base.Render(strings.Repeat(" ", max(mw-1-ansi.StringWidth(mid), 0)))
	e, wr, h, ps, o := a.statusTotals()
	diagFg := "warning"
	if e > 0 {
		diagFg = "error"
	}
	badge := func(n, width int, bg, fg string) statusSegment {
		ink := th.Token(fg)
		// The curated badge fills need light ink on dark terminals.
		if th.Dark && th.Reduced {
			ink = th.Token("focus")
		}
		st := lipgloss.NewStyle().Foreground(ink).Background(th.Token(bg))
		return statusSegment{st.Render(padCenter(countText(n), width)), bg}
	}
	rule := role("statusRule", "|")
	return []statusSegment{{left, "status"}, {rule, "status"}, {middle, "status"}, {rule, "status"}, badge(e+wr+h, 6, "warningCount", diagFg), badge(ps, 5, "proposalCount", "agent"), badge(o, 5, "otherCount", "human")}

}
func padCenter(s string, w int) string {
	extra := max(w-ansi.StringWidth(s), 0)
	return strings.Repeat(" ", extra/2) + s + strings.Repeat(" ", extra-extra/2)
}
func (a *App) statusLine(w int) string {
	var b strings.Builder
	for _, s := range a.statusSegments(w) {
		b.WriteString(s.text)
	}
	return b.String()
}

// statusBand is the status line plus its optional half-row edges. An edge
// cell is reverse video: the band's fill is the foreground, so the terminal
// paints it as the cell background, exactly as the status line's own cells,
// and the glyph covers the other half in the terminal's background. A plain
// foreground half block would go through the terminal's text colour path
// instead: Terminal.app applies a minimum contrast to 256-colour text over
// its default background, so 38;5;235 on that background draws lighter than
// 48;5;235 does.
//
// Terminal.app draws a flat bar: its font's half blocks do not fill the line
// height, so the cell background would show as a thin line at the outer edge.
// The edge rows stay reserved and blank, as for the ASCII profile, so the
// layout does not change.
func (a *App) statusBand(w int) []string {
	var out []string
	edge := func(air string) string {
		var b strings.Builder
		for _, s := range a.statusSegments(w) {
			n := ansi.StringWidth(s.text)
			if a.theme.ASCII || a.flatBand() {
				b.WriteString(strings.Repeat(" ", n))
			} else {
				b.WriteString(lipgloss.NewStyle().Foreground(a.theme.Token(s.background)).Reverse(true).Render(strings.Repeat(air, n)))
			}
		}
		return b.String()
	}
	// The glyph names the half left to the terminal background.
	if a.showHelpBar && a.height > 36 {
		out = append(out, edge("▀"))
	}
	out = append(out, a.statusLine(w))
	if a.showHelpBar && a.height >= 30 {
		out = append(out, edge("▄"))
	}
	return out
}

// diagUnderCursor returns the most severe diagnostic covering the cursor
// (what ^P fixes), without reparsing.
func (a *App) diagUnderCursor() (diag.Diagnostic, bool) {
	ed := a.cur.Ed
	off := ed.Cursor()
	var ds []diag.Diagnostic
	for _, d := range a.ws.FileDiags(ed.Path()) {
		if d.Range.End > d.Range.Start && off >= d.Range.Start && off <= d.Range.End {
			ds = append(ds, d)
		}
	}
	return workflow.MostSevere(ds)
}

// helperTemplate depends on the visible screen and available width, never on
// hidden check state. An unavailable command keeps its slot and becomes inert.
func helperTemplate(ctx Context, w int) []Binding {
	bs := BarBindings(ctx)
	var actions []Action
	switch ctx {
	case CtxEditor:
		actions = []Action{ActHelp, ActExit, ActWriteOut, ActQuickOpen, ActWhereIs, ActPropose, ActCut, ActPaste, ActUndo, ActRedo, ActToTarget, ActReview, ActBack, ActFormat, ActCopy, ActAnalyze, ActOutline, ActNextPane}
		if w < 100 {
			actions = []Action{ActHelp, ActExit, ActWriteOut, ActQuickOpen, ActWhereIs, ActPropose, ActUndo, ActReview, ActOutline, ActNextPane}
		}
	case CtxChecks:
		actions = []Action{ActAcceptHunk, ActSkipHunk, ActEditHunk, ActFeedback, ActCommitView, ActMessage, ActFormat, ActShowRemoved, ActUp, ActDown, ActAccept, ActReview, ActCancel, ActNextPane}
		if w < 100 {
			actions = []Action{ActAcceptHunk, ActSkipHunk, ActEditHunk, ActFeedback, ActCommitView, ActMessage, ActAccept, ActReview, ActCancel, ActNextPane}
		}
	case CtxReview:
		actions = []Action{ActAcceptHunk, ActSkipHunk, ActEditHunk, ActFeedback, ActApprove, ActRejectAll, ActRetryCheck, ActCancel, ActUp, ActDown}
	case CtxCommit:
		actions = []Action{ActYes, ActNo, ActToggle, ActMessage, ActCommitView, ActCancel, ActUp, ActDown}
	}
	if len(actions) == 0 {
		return bs
	}
	out := make([]Binding, 0, len(actions))
	for _, action := range actions {
		for _, b := range bs {
			if b.Action == action {
				out = append(out, b)
				break
			}
		}
	}
	return out
}
func displayKey(k string, ascii bool) string {
	switch k {
	case "Enter", "Ret":
		return "Ret"
	case "Space":
		return "Spc"
	case "Any key":
		return "Any"
	case "⇧Tab":
		if ascii {
			return "S-Tab"
		}
		return "⇧Tab"
	case "Up":
		if !ascii {
			return "↑"
		}
	case "Down":
		if !ascii {
			return "↓"
		}
	}
	return k
}
func keycapWidth(k string) int {
	if strings.HasPrefix(k, "^") {
		return max(3, ansi.StringWidth(k))
	}
	if len(k) == 1 && unicode.IsLower([]rune(k)[0]) {
		return 3
	}
	return max(3, ansi.StringWidth(k))
}
func (a *App) renderKeycap(k string, w int, available bool) string {
	th := a.theme
	fg := th.Token("control")
	if !available {
		fg = th.Token("disabled")
	}
	st := lipgloss.NewStyle().Foreground(fg)
	if strings.HasPrefix(k, "^") {
		return st.Render(padCenter(k, w))
	}
	if len(k) == 1 && unicode.IsLower([]rune(k)[0]) && k != "y" {
		bracket := lipgloss.NewStyle().Foreground(th.Token("structure"))
		return bracket.Render("[") + st.Render(padCenter(k, w-2)) + bracket.Render("]")
	}
	bg := th.Token("keycap")
	fg = th.Token("ink")
	if k == "y" {
		bg = th.Token("positiveKey")
		fg = th.Token("onPositiveKey")
	}
	if k == "Esc" {
		fg = th.Token("error")
	}
	if !available {
		fg = th.Token("disabled")
		bg = th.Token("card")
	}
	return lipgloss.NewStyle().Foreground(fg).Background(bg).Render(padCenter(k, w))
}
func (a *App) helpBar(ctx Context, w int) []string {
	bs := helperTemplate(ctx, w)
	if custom, ok := a.top().(interface{ helperBindings(*App) []Binding }); ok {
		bs = custom.helperBindings(a)
	}
	for i := range bs {
		bs[i].Label = a.labelOf(ctx, bs[i])
		bs[i].Key = displayKey(bs[i].Key, a.theme.ASCII)
	}
	out := []string{"", ""}
	if len(bs) == 0 {
		return out
	}
	cols := (len(bs) + 1) / 2
	needs := make([]int, cols)
	caps := make([]int, cols)
	sum := 0
	for i, b := range bs {
		caps[i/2] = max(caps[i/2], keycapWidth(b.Key))
	}
	for i, b := range bs {
		needs[i/2] = max(needs[i/2], caps[i/2]+1+ansi.StringWidth(b.Label)+1)
	}
	for _, n := range needs {
		sum += n
	}
	widths := append([]int(nil), needs...)
	for c := range cols {
		if sum <= w {
			widths[c] += (w - sum) / cols
			if c < (w-sum)%cols {
				widths[c]++
			}
		} else {
			widths[c] = w / cols
			if c < w%cols {
				widths[c]++
			}
		}
	}
	for row := range 2 {
		var line strings.Builder
		for col := range cols {
			i := col*2 + row
			if i >= len(bs) {
				line.WriteString(strings.Repeat(" ", widths[col]))
				continue
			}
			b := bs[i]
			avail := a.canRun(ctx, b)
			if custom, ok := a.top().(interface{ helperEnabled(*App, Binding) bool }); ok {
				avail = avail && custom.helperEnabled(a, b)
			}
			key := a.renderKeycap(b.Key, caps[col], avail)
			label := " " + ansi.Truncate(b.Label, max(widths[col]-caps[col]-2, 0), "")
			style := a.theme.HelpLabel
			if !avail {
				style = a.theme.HelpLabelDim
			}
			line.WriteString(key + style.Render(label))
			line.WriteString(strings.Repeat(" ", max(widths[col]-caps[col]-ansi.StringWidth(label), 0)))
		}
		out[row] = line.String()
	}
	return out
}

// flatBand reports whether the status band omits its half-block edges:
// Terminal.app, read through the environment the CLI (or replay) injected.
func (a *App) flatBand() bool {
	if a.wf == nil || a.wf.Getenv == nil {
		return false // No injected environment: tests must not depend on the host terminal.
	}
	return a.wf.Getenv("TERM_PROGRAM") == "Apple_Terminal"
}
