// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/recording"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/workflow"
)

// maxBodyLines caps the body of a hover preview.
const maxBodyLines = 12

// popup is a box drawn over the editor at a token's screen position: a
// hover preview, the diagnostics at the cursor, or a diff.
type popup struct {
	lines []string // styled content lines
	inner int      // content width
	x, y  int      // top-left cell of the box on screen
	// target is where ^T goes from the popup, if anywhere; choices the list it
	// opens instead when several places qualify, titled choicesTitle.
	target       *Location
	choices      []listItem
	choicesTitle string
	// mouse is true for a popup opened by the mouse resting on a token;
	// moving away closes it. tokRow and tokX0..tokX1 are the token's cells.
	mouse          bool
	tokRow         int
	tokX0, tokX1   int
	boxW, boxH     int
	scrollable     bool
	scroll, height int
	// notice is a modal message over any screen: any key closes it.
	notice bool
	// diags are the diagnostics the popup shows; ^P fixes the most severe.
	diags []diag.Diagnostic
}

// box renders the popup's box.
func (p *popup) box(th *Theme) string {
	lines := p.lines
	if p.scrollable && p.height > 0 {
		end := min(p.scroll+p.height, len(lines))
		lines = lines[p.scroll:end]
	}
	padded := make([]string, len(lines))
	for k, l := range lines {
		padded[k] = padTo(l, p.inner)
	}
	return popupFrameStyle(th).Render(strings.Join(padded, "\n"))
}

// popupFrameStyle is shared by token/diagnostic and status-counter hovers.
func popupFrameStyle(th *Theme) lipgloss.Style {
	border := lipgloss.RoundedBorder()
	if th.ASCII {
		border = lipgloss.ASCIIBorder()
	}
	return lipgloss.NewStyle().Border(border).BorderForeground(th.dim).Padding(0, 1)
}

// layer returns the popup as a canvas layer.
func (p *popup) layer(a *App) *lipgloss.Layer {
	return lipgloss.NewLayer(p.box(a.theme)).X(p.x).Y(p.y)
}

// displayWidth is the cells a styled string takes.
func displayWidth(s string) int { return ansi.StringWidth(s) }

// covers reports whether screen cell (x, y) is inside the box.
func (p *popup) covers(x, y int) bool {
	return x >= p.x && x < p.x+p.boxW && y >= p.y && y < p.y+p.boxH
}

// over reports whether (x, y) is over the token that opened the popup.
func (p *popup) over(x, y int) bool {
	return y == p.tokRow && x >= p.tokX0 && x < p.tokX1
}

// place positions a popup of the given content near screen cell (tx, ty):
// below the token's row when it fits, else above.
func (a *App) place(p *popup, tx, ty int) {
	ex, _, ew, eh := a.editorRect()
	p.inner = 0
	for _, l := range p.lines {
		p.inner = max(p.inner, ansi.StringWidth(l))
	}
	p.inner = min(p.inner, max(ew-4, 10))
	maxH := max(eh-2, 3)
	if len(p.lines) > maxH {
		p.scrollable = true
		p.height = maxH
	}
	p.boxW = p.inner + 4
	p.boxH = min(len(p.lines), maxH) + 2
	switch {
	case ty+1+p.boxH <= eh:
		p.y = ty + 1
	case ty-p.boxH >= 0:
		p.y = ty - p.boxH
	default:
		p.y = max(eh-p.boxH, 0)
	}
	p.x = min(max(tx, ex), max(a.width-p.boxW, 0))
}

// wrapText wraps plain text to w cells, keeping its line breaks.
func wrapText(s string, w int) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(line, w, ""), "\n")...)
	}
	return out
}

// reflow joins the soft-wrapped lines of Markdown paragraphs so a preview
// wraps them at its own width. List items, quotes, headings, tables and
// indented code keep their own lines; blank lines stay.
func reflow(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if len(out) > 0 && t != "" && out[len(out)-1] != "" && !startsBlock(line) {
			out[len(out)-1] += " " + t
			continue
		}
		out = append(out, strings.TrimRight(line, " \t\r"))
	}
	return strings.Join(out, "\n")
}

// startsBlock reports whether a source line starts its own block.
func startsBlock(line string) bool {
	if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
		return true
	}
	t := strings.TrimSpace(line)
	for _, p := range []string{"- ", "* ", "+ ", "> ", "#", "|", "```", "~~~"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(t) && (t[i] == '.' || t[i] == ')') && t[i+1] == ' '
}

// kindStyle styles an anchor name by kind.
func (th *Theme) kindStyle(k mdspec.Kind) lipgloss.Style {
	if k == mdspec.KindDefinition {
		return th.OutlineDef.Bold(true)
	}
	return th.OutlineInv.Bold(true)
}

// popupWidth is the content width hover previews wrap at.
func (a *App) popupWidth() int {
	_, _, ew, _ := a.editorRect()
	// Fit the box (border and padding: 4 cells) beside the gutter.
	return min(72, max(ew-a.cur.Ed.GutterWidth()-5, 20))
}

// anchorPreview builds the hover lines for an anchor; withModels adds the
// places in formal models that cite it.
func (a *App) anchorPreview(an *mdspec.Anchor, withModels bool) []string {
	th := a.theme
	w := a.popupWidth()
	head := string(an.Kind) + " " + th.kindStyle(an.Kind).Render("$"+an.Name)
	if an.Title != "" {
		head += " (" + termtext.Clean(an.Title) + ")"
	}
	lines := []string{head}
	f := a.ws.File(an.File)
	body := ""
	if f != nil {
		body = strings.TrimSpace(termtext.Clean(f.Text(pos.Range{Start: an.LabelRange.End, End: an.BodyRange.End})))
	}
	if body != "" {
		bl := wrapText(reflow(body), w)
		if len(bl) > maxBodyLines {
			bl = append(bl[:maxBodyLines-1], a.theme.uiText("…"))
		}
		lines = append(lines, bl...)
	}
	if withModels {
		lines = append(lines, a.modelLines(a.ws.Formal.ToAnchor(an.Name))...)
	}
	refs := len(a.ws.Spec.ReferencesTo(an.Name))
	foot := fmt.Sprintf(a.theme.uiText("%s:%d · %s · %s"), termtext.Clean(an.File), a.ws.Line(an.File, an.LabelRange.Start), plural(refs, "ref"), an.ShortHash)
	if st := a.changeState(an); st != "" {
		foot += a.theme.uiText(" · ") + st + " vs HEAD"
	}
	return append(lines, th.PopupDim.Render(foot))
}

// sectionPreview builds the hover lines for a section; withModels adds the
// places in formal models that cite it.
func (a *App) sectionPreview(s *mdspec.Section, withModels bool) []string {
	th := a.theme
	w := a.popupWidth()
	lines := []string{th.OutlineSec.Bold(true).Render(a.theme.uiText("§")+s.Number) + " " + termtext.Clean(s.Title)}
	if f := a.ws.File(s.File); f != nil {
		rest := termtext.Clean(f.Text(pos.Range{Start: s.HeadingRange.End, End: s.Range.End}))
		para := firstParagraph(rest)
		if para != "" {
			pl := wrapText(reflow(para), w)
			if len(pl) > maxBodyLines {
				pl = append(pl[:maxBodyLines-1], a.theme.uiText("…"))
			}
			lines = append(lines, pl...)
		}
	}
	if as := a.ws.Spec.AnchorsIn(s); len(as) > 0 {
		var names []string
		for _, an := range as {
			names = append(names, th.kindStyle(an.Kind).UnsetBold().Render("$"+an.Name))
		}
		lines = append(lines, wrapStyled("anchors: "+strings.Join(names, ", "), w)...)
	}
	if withModels {
		lines = append(lines, a.modelLines(a.ws.Formal.ToSection(s))...)
	}
	refs := len(a.ws.Spec.SectionReferencesTo(s))
	lines = append(lines, th.PopupDim.Render(fmt.Sprintf(a.theme.uiText("%s:%d · %s"), termtext.Clean(s.File), a.ws.Line(s.File, s.HeadingRange.Start), plural(refs, "ref"))))
	return lines
}

// wrapStyled wraps a styled line to w cells.
func wrapStyled(s string, w int) []string {
	return strings.Split(ansi.Wrap(s, w, ""), "\n")
}

// firstParagraph returns the first non-blank paragraph of s.
func firstParagraph(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			if len(out) > 0 {
				break
			}
			continue
		}
		out = append(out, t)
	}
	return strings.Join(out, "\n")
}

// diagsAt returns the diagnostics of the current file at off, the end
// included (as at a cursor).
func (a *App) diagsAt(off int) []diag.Diagnostic {
	a.fresh()
	var out []diag.Diagnostic
	for _, d := range a.ws.FileDiags(a.cur.Ed.Path()) {
		if d.Range.End <= d.Range.Start || off < d.Range.Start || off > d.Range.End {
			continue
		}
		out = append(out, d)
	}
	return out
}

// diagsIn returns the diagnostics of the current file that overlap r.
func (a *App) diagsIn(r pos.Range) []diag.Diagnostic {
	a.fresh()
	var out []diag.Diagnostic
	for _, d := range a.ws.FileDiags(a.cur.Ed.Path()) {
		if d.Range.End > d.Range.Start && d.Range.Start < r.End && d.Range.End > r.Start {
			out = append(out, d)
		}
	}
	return out
}

// diagLines formats diagnostics for a popup.
func (a *App) diagLines(ds []diag.Diagnostic) []string {
	th := a.theme
	var out []string
	for _, d := range ds {
		head := th.Mark(d.Severity).Render(string(d.Severity)) + " " + th.PopupDim.Render(d.Code)
		out = append(out, head)
		out = append(out, wrapText(termtext.Clean(d.Message), a.popupWidth())...)
	}
	return out
}

// preview builds the popup for the token t (with the diagnostics at off).
func (a *App) preview(t tokenAt, off int) *popup {
	spec := a.ws.Spec
	p := &popup{}
	ds := a.diagsAt(off)
	if t.tag != nil {
		// A tag is one thing: what lint says about its reference or quote shows
		// wherever on the tag the pointer rests.
		ds = a.diagsIn(t.tag.Range)
	}
	switch {
	case t.occ != nil:
		as := spec.Lookup(t.occ.Name)
		switch len(as) {
		case 0:
			p.lines = []string{a.theme.Mark(diag.Error).Render("$"+t.occ.Name) + " is not defined anywhere in the project"}
		case 1:
			p.lines = a.anchorPreview(as[0], true)
			if t.occ.Role != mdspec.RoleDefinition {
				loc := anchorLoc(as[0])
				p.target = &loc
			} else {
				items := a.modelItems(a.ws.Formal.ToAnchor(t.occ.Name))
				p.setTargets(citedTitle("$"+termtext.Clean(t.occ.Name), len(items)), items)
			}
		default:
			p.lines = []string{fmt.Sprintf("$%s is defined %d times:", t.occ.Name, len(as))}
			for _, an := range as {
				p.lines = append(p.lines, fmt.Sprintf("  %s:%d", termtext.Clean(an.File), a.ws.Line(an.File, an.LabelRange.Start)))
			}
		}
	case t.sec != nil:
		st, sec, cands := spec.ResolveSection(t.sec.File, t.sec.Stem, t.sec.Number)
		switch st {
		case mdspec.Resolved:
			p.lines = a.sectionPreview(sec, true)
			loc := sectionLoc(sec)
			p.target = &loc
		case mdspec.Ambiguous:
			p.lines = []string{fmt.Sprintf(a.theme.uiText("§%s is ambiguous:"), t.sec.Number)}
			for _, c := range cands {
				p.lines = append(p.lines, "  "+termtext.Clean(c.Qualified()+" "+c.Title))
			}
		default:
			p.lines = []string{a.theme.Mark(diag.Error).Render(a.theme.uiText("§")+t.sec.Number) + " does not match any numbered section"}
		}
	case t.label != nil:
		p.lines = a.anchorPreview(t.label, true)
		items := a.modelItems(a.ws.Formal.ToAnchor(t.label.Name))
		p.setTargets(citedTitle("$"+termtext.Clean(t.label.Name), len(items)), items)
	case t.head != nil:
		p.lines = a.sectionPreview(t.head, true)
		items := a.modelItems(a.ws.Formal.ToSection(t.head))
		p.setTargets(citedTitle(a.theme.uiText("§")+t.head.Number, len(items)), items)
	case t.tag != nil:
		if m := a.ws.Model(a.cur.Ed.Path()); m != nil {
			a.tagPreview(p, m, t.tag, len(ds) == 0)
		}
	case t.decl != nil:
		if m := a.ws.Model(a.cur.Ed.Path()); m != nil {
			a.declPreview(p, m, t.decl)
		}
	}
	if len(ds) > 0 {
		if len(p.lines) > 0 {
			p.lines = append(p.lines, "")
		}
		p.lines = append(p.lines, a.diagLines(ds)...)
		p.diags = ds
	}
	if len(p.lines) == 0 {
		return nil
	}
	return p
}

// look opens the preview at the cursor (^L).
func (a *App) look() {
	ed := a.cur.Ed
	off := ed.Cursor()
	t := a.tokenUnder(off, true)
	p := a.preview(t, off)
	if p == nil {
		if a.inModel() {
			a.setFlash("Nothing to look at here: put the cursor on a tag, a tagged declaration or a diagnostic")
			return
		}
		a.setFlash("%s", a.theme.uiText("Nothing to look at here: put the cursor on a $Name, a § reference or a diagnostic"))
		return
	}
	start := off
	if r, ok := t.span(); ok {
		start = r.Start
	}
	x, y, ok := ed.CellOf(start)
	if !ok {
		x, y, _ = ed.CursorCell()
	}
	ex, ey, _, _ := a.editorRect()
	a.place(p, ex+x, ey+y)
	a.popup = p
}

// hoverAtCell opens a mouse preview for the token (or the diagnostic's
// underline) at editor cell (x, y), or, over the gutter, for the line's
// diagnostic marker.
func (a *App) hoverAtCell(x, y int) {
	ed := a.cur.Ed
	if x < ed.GutterWidth() {
		a.hoverGutter(y)
		return
	}
	off, ok := ed.OffsetAtCell(x, y)
	if !ok {
		return
	}
	t := a.tokenUnder(off, false)
	var start, end int
	if r, ok := t.span(); ok {
		start, end = r.Start, r.End
	} else {
		// An underlined diagnostic without a token ("#5.2", "§5.2for").
		var under []diag.Diagnostic
		for _, d := range a.diagsAt(off) {
			if off < d.Range.End {
				under = append(under, d)
			}
		}
		d, ok := workflow.MostSevere(under)
		if !ok {
			return
		}
		row, _ := ed.RowAtCell(y)
		start, end = max(d.Range.Start, row.Start), min(d.Range.End, row.End)
	}
	p := a.preview(t, off)
	if p == nil {
		return
	}
	ex, ey, _, _ := a.editorRect()
	sx, sy, ok := ed.CellOf(start)
	if !ok || sy != y {
		sx, sy = x, y
	}
	exx, eyy, ok := ed.CellOf(end)
	if !ok || eyy != sy {
		exx = sx + ansi.StringWidth(string(ed.Buffer().Bytes()[start:end]))
	}
	p.mouse = true
	p.tokRow = ey + sy
	p.tokX0, p.tokX1 = ex+sx, ex+exx
	a.place(p, ex+sx, ey+sy)
	a.popup = p
}

// hoverGutter opens the popup of the diagnostic marked in the gutter at
// editor row y: the most severe diagnostic on that line, as resting over its
// underline shows it.
func (a *App) hoverGutter(y int) {
	ed := a.cur.Ed
	row, ok := ed.RowAtCell(y)
	if !ok || row.Sub != 0 {
		return // the marker is on a line's first row
	}
	rows := ed.Rows(row.Line)
	line := pos.Range{Start: row.Start, End: rows[len(rows)-1].End}
	a.fresh()
	var on []diag.Diagnostic
	for _, d := range a.ws.FileDiags(ed.Path()) {
		if d.Range.End > d.Range.Start && d.Range.Start < max(line.End, line.Start+1) && d.Range.End > line.Start {
			on = append(on, d)
		}
	}
	d, ok := workflow.MostSevere(on)
	if !ok {
		return
	}
	off := max(d.Range.Start, line.Start)
	p := a.preview(a.tokenUnder(off, false), off)
	if p == nil {
		return
	}
	ex, ey, _, _ := a.editorRect()
	p.mouse = true
	p.tokRow = ey + y
	p.tokX0, p.tokX1 = ex, ex+ed.GutterWidth()
	a.place(p, ex, ey+y)
	a.popup = p
}

// plural returns "1 ref" or "2 refs".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// hoverTarget opens the mouse preview a recorded hover opened, found by
// what it showed rather than by its cell (a replay, whose layout may
// differ): the occurrence of the anchor, the § reference or the
// diagnostic nearest the recorded place, or the gutter marker of the
// recorded line, in the current file. It reports false, opening nothing,
// when the target is not in the current file or not on screen; the
// caller then falls back to the recorded cell.
func (a *App) hoverTarget(t recording.MouseTarget) bool {
	ed := a.cur.Ed
	if t.Pos != nil && t.Pos.File != ed.Path() {
		return false
	}
	a.fresh()
	near := 0
	if t.Pos != nil {
		near = offsetOf(ed, *t.Pos)
	}
	start := -1
	closer := func(off int) {
		if start < 0 || absInt(off-near) < absInt(start-near) {
			start = off
		}
	}
	f := a.ws.File(ed.Path())
	switch t.Kind {
	case "gutter":
		if t.Line <= 0 {
			return false
		}
		_, y, ok := ed.CellOf(ed.Buffer().Index().LineStart(t.Line))
		if !ok {
			return false
		}
		a.hoverAt = [2]int{0, y}
		a.hoverGutter(y)
		return a.popup != nil
	case "anchor":
		if f == nil {
			return false
		}
		for _, o := range f.Occurrences {
			if o.Name == t.Name {
				closer(o.Range.Start)
			}
		}
	case "section":
		if f == nil {
			return false
		}
		for _, r := range f.SectionRefs {
			if r.Number == t.Number {
				closer(r.Range.Start)
			}
		}
	case "diag":
		for _, d := range a.ws.FileDiags(ed.Path()) {
			if d.Range.End > d.Range.Start && (t.Code == "" || d.Code == t.Code) {
				closer(d.Range.Start)
			}
		}
	}
	if start < 0 {
		return false
	}
	x, y, ok := ed.CellOf(start)
	if !ok {
		return false
	}
	a.hoverAt = [2]int{x, y}
	a.hoverAtCell(x, y)
	return a.popup != nil
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
