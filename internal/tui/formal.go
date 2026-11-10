// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strconv"

	"charm.land/lipgloss/v2"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/termtext"
)

// A formal model opens in the editor beside the spec. The two link through the
// tags in the model (internal/formal): hover and ^T from a tag or a tagged
// declaration reach the spec, and hover and ^T from what a tag cites reach the
// model. A model has no gutter diff, format or commit (modelClosed), and Chris's
// edits of it save directly; an agent's edit arrives as a model proposal card
// (modelprop.go).

// modelClosed says why an action is closed on a model. It stands behind the
// dimmed help bar entries and is what Help shows for them.
const modelClosed = "a TLA+ model is not a spec file: your edits save directly and git reviews them (agents send theirs as proposals), so checks, format and commit stay closed here"

// modelClosedActions are the actions modelClosed covers.
var modelClosedActions = []Action{ActPropose, ActFix, ActFormat, ActAnalyze, ActCommitView}

// modelClosedIn lists the actions a model closes in ctx: in the sidebar only A,
// since ^F there acts on the sidebar; elsewhere a screen has its own rules. ^R
// stays open on a model, since an agent's model proposal arrives as a card;
// only its background check of changed anchors is skipped (commitFlow).
func modelClosedIn(ctx Context) []Action {
	switch ctx {
	case CtxEditor, CtxOutline, CtxPopup, CtxDiagPopup:
		return modelClosedActions
	case CtxChecks:
		return []Action{ActCommitView}
	}
	return nil
}

// maxModelPlaces caps the places a spec-side hover names; ^\ lists them all.
const maxModelPlaces = 6

// maxQuoteLines caps the lines of a tag's quote in a hover.
const maxQuoteLines = 2

// inModel reports whether the current document is a formal model.
func (a *App) inModel() bool {
	return a.cur != nil && a.ws != nil && a.ws.IsModel(a.cur.Ed.Path())
}

// ---- tokens ----

// modelTokenIn is tokenIn on model m: a tag, or a declaration with tags.
func modelTokenIn(m *formal.File, off int, inclusive bool) tokenAt {
	if t := m.TagAt(off); t != nil && (inclusive || off < t.Range.End) {
		return tokenAt{tag: t}
	}
	if d := m.DeclAt(off); d != nil && (inclusive || off < d.NameRange.End) && len(m.TagsOf(d)) > 0 {
		return tokenAt{decl: d}
	}
	return tokenAt{}
}

// citedTokenIn finds what a model cites at off in spec file f. Under the mouse
// that is a heading's number; a label's $Name is an occurrence already. At the
// cursor it is the whole label or heading line, since a jump to an anchor or
// section lands at its start: ^T there goes on to the model.
func (a *App) citedTokenIn(f *mdspec.File, off int, inclusive bool) tokenAt {
	if inclusive {
		if an := f.AnchorAt(off); an != nil && off <= an.LabelRange.End && len(a.ws.Formal.ToAnchor(an.Name)) > 0 {
			return tokenAt{label: an}
		}
	}
	s := f.SectionAt(off)
	if s == nil || s.Number == "" || len(a.ws.Formal.ToSection(s)) == 0 {
		return tokenAt{}
	}
	if inclusive && off >= s.HeadingRange.Start && off <= s.HeadingRange.End {
		return tokenAt{head: s}
	}
	if off >= s.NumberRange.Start && off < s.NumberRange.End {
		return tokenAt{head: s}
	}
	return tokenAt{}
}

// ---- targets ----

// linkLoc is where a jump to a citing tag lands: the declaration it binds to.
func linkLoc(l *formal.Link) Location {
	if l.Tag.Decl != nil {
		return Location{Path: l.File.Path, Off: l.Tag.Decl.NameRange.Start}
	}
	return Location{Path: l.File.Path, Off: l.Tag.Range.Start}
}

// modelItems lists the distinct places links lead to: Spec tags first, then
// Derived-from, with mutants last, as Index orders them.
func (a *App) modelItems(links []*formal.Link) []listItem {
	var out []listItem
	seen := map[Location]bool{}
	for _, l := range links {
		loc := linkLoc(l)
		if seen[loc] {
			continue
		}
		seen[loc] = true
		detail := termtext.Clean(l.Tag.Quote)
		if l.Tag.Kind == formal.TagDerived {
			detail = string(formal.TagDerived)
		}
		where := termtext.Clean(l.Where())
		out = append(out, listItem{key: where, label: a.theme.OutlineDef.Render(where), detail: detail, loc: loc})
	}
	return out
}

// tagItems lists the spec places a tag points at.
func (a *App) tagItems(m *formal.File, t *formal.Tag) []listItem {
	if t.Kind == formal.TagSpecFile {
		var out []listItem
		for _, f := range a.ws.Spec.FilesWithStem(t.Stem) {
			out = append(out, listItem{key: f.Path, label: a.theme.OutlineFile.Render(termtext.Clean(f.Path)), loc: Location{Path: f.Path}})
		}
		return out
	}
	l := a.ws.Formal.LinkOf(t)
	if l == nil {
		return nil
	}
	var out []listItem
	switch {
	case l.Anchor != nil:
		out = append(out, a.anchorItem(l.Anchor))
	case l.Section != nil:
		out = append(out, a.sectionItem(l.Section))
	}
	for _, an := range l.Anchors {
		out = append(out, a.anchorItem(an))
	}
	for _, s := range l.Sections {
		out = append(out, a.sectionItem(s))
	}
	return out
}

// declItems lists the spec places a declaration's tags point at: Spec tags
// first, since Derived-from is the weaker claim.
func (a *App) declItems(m *formal.File, d *formal.Decl) []listItem {
	var out []listItem
	seen := map[Location]bool{}
	for _, kind := range []formal.TagKind{formal.TagSpec, formal.TagDerived} {
		for _, t := range m.TagsOf(d) {
			if t.Kind != kind {
				continue
			}
			for _, it := range a.tagItems(m, t) {
				if !seen[it.loc] {
					seen[it.loc] = true
					out = append(out, it)
				}
			}
		}
	}
	return out
}

// follow goes to the one place in items, or lists several. It reports whether
// there was anywhere to go.
func (a *App) follow(title string, items []listItem) bool {
	switch len(items) {
	case 0:
		return false
	case 1:
		a.jump(items[0].loc)
	default:
		a.list = newList(title, items, false)
	}
	return true
}

// setTargets makes items what ^T follows from popup p.
func (p *popup) setTargets(title string, items []listItem) {
	switch len(items) {
	case 0:
	case 1:
		loc := items[0].loc
		p.target = &loc
	default:
		p.choices, p.choicesTitle = items, title
	}
}

// citedTitle titles the list of places that cite what.
func citedTitle(what string, n int) string {
	return fmt.Sprintf("%s is cited in %d places", what, n)
}

// followSpec goes from a tag or a tagged declaration to what it cites.
func (a *App) followSpec(t tokenAt) {
	m := a.ws.Model(a.cur.Ed.Path())
	if m == nil {
		return
	}
	var items []listItem
	var title string
	switch {
	case t.tag != nil:
		items, title = a.tagItems(m, t.tag), "This tag matches several places"
	case t.decl != nil:
		items, title = a.declItems(m, t.decl), termtext.Clean(t.decl.Name)+" cites several places"
	}
	if a.follow(title, items) {
		return
	}
	switch {
	case t.tag != nil && t.tag.Problem != "":
		a.setFlash("%s", a.theme.uiText(termtext.Clean(t.tag.Problem)))
	case t.tag != nil && t.tag.Ref != nil:
		a.setFlash("%s does not resolve to anything in the spec", a.refText(t.tag.Ref))
	default:
		a.setFlash("Nothing in the spec to go to from here")
	}
}

// tagRefTarget is what ^\ lists from a tag: the $Name it cites, or the section
// it resolves to.
func (a *App) tagRefTarget(t *formal.Tag) (string, *mdspec.Section) {
	switch l := a.ws.Formal.LinkOf(t); {
	case t.Ref == nil:
	case t.Ref.IsAnchor():
		return t.Ref.Name, nil
	case l != nil:
		return "", l.Section
	}
	return "", nil
}

// declRefTarget is tagRefTarget from a declaration: its first tag with a
// target, Spec tags first, since Derived-from is the weaker claim.
func (a *App) declRefTarget(d *formal.Decl) (string, *mdspec.Section) {
	m := a.ws.Model(a.cur.Ed.Path())
	if m == nil {
		return "", nil
	}
	for _, kind := range []formal.TagKind{formal.TagSpec, formal.TagDerived} {
		for _, t := range m.TagsOf(d) {
			if t.Kind != kind {
				continue
			}
			if name, sec := a.tagRefTarget(t); name != "" || sec != nil {
				return name, sec
			}
		}
	}
	return "", nil
}

// ---- hover ----

// dimItalic draws what is weaker than a citation: a Derived-from.
func (th *Theme) dimItalic() lipgloss.Style {
	return th.PopupDim.Italic(true)
}

// modelPlace is one place that cites a spec target, with the quotes its tags
// give it there.
type modelPlace struct {
	head   string
	style  lipgloss.Style
	quotes []string
}

// modelLines is what a spec-side hover adds: "Model: path:Op" with the tag's
// quote for each place that cites the target, Derived-from dimmed.
func (a *App) modelLines(links []*formal.Link) []string {
	th := a.theme
	var places []*modelPlace
	byHead := map[string]*modelPlace{}
	for _, l := range links {
		where := termtext.Clean(l.Where())
		var head string
		st := th.Ink
		switch {
		case l.Tag.Kind == formal.TagDerived:
			head, st = "Derived-from in "+where, th.dimItalic()
		case l.File.Mutant:
			head = "Model (mutant): " + where
		default:
			head = "Model: " + where
		}
		p := byHead[head]
		if p == nil {
			p = &modelPlace{head: head, style: st}
			byHead[head] = p
			places = append(places, p)
		}
		if l.Tag.Quote != "" {
			p.quotes = append(p.quotes, termtext.Clean(l.Tag.Quote))
		}
	}
	if len(places) == 0 {
		return nil
	}
	w := a.popupWidth()
	var out []string
	for k, p := range places {
		if k == maxModelPlaces {
			out = append(out, th.PopupDim.Render(fmt.Sprintf(th.uiText("…and %d more; ^\\ lists them"), len(places)-k)))
			break
		}
		for _, line := range wrapText(p.head, w) {
			out = append(out, p.style.Render(line))
		}
		for _, q := range p.quotes {
			out = append(out, a.quoteLines(q, w)...)
		}
	}
	return out
}

// quoteLines draws a tag's quote, indented and capped.
func (a *App) quoteLines(q string, w int) []string {
	lines := wrapText("\""+reflow(q)+"\"", max(w-2, 10))
	if len(lines) > maxQuoteLines {
		lines = append(lines[:maxQuoteLines-1], a.theme.uiText("…"))
	}
	out := make([]string, len(lines))
	for k, l := range lines {
		out[k] = "  " + a.theme.PopupDim.Render(l)
	}
	return out
}

// tagLines is the text a tag cites; with explain, also what is wrong with it,
// for a popup that does not show the diagnostics saying so.
func (a *App) tagLines(m *formal.File, t *formal.Tag, explain bool) []string {
	th := a.theme
	w := a.popupWidth()
	wrapped := func(text string, st lipgloss.Style) []string {
		var out []string
		for _, l := range wrapText(termtext.Clean(text), w) {
			out = append(out, st.Render(l))
		}
		return out
	}
	if t.Kind == formal.TagSpecFile {
		return a.specFileLines(t)
	}
	if t.Ref == nil {
		if !explain {
			return nil
		}
		return wrapped(th.uiText(t.Problem), th.Ink)
	}
	l := a.ws.Formal.LinkOf(t)
	if l == nil {
		return wrapped(m.Path+" is not in the project's formal list, so its tags are not read", th.PopupDim)
	}
	written := a.refText(t.Ref)
	var out []string
	switch {
	case l.Anchor != nil:
		out = a.anchorPreview(l.Anchor, false)
	case l.Section != nil:
		out = a.sectionPreview(l.Section, false)
	case len(l.Anchors) > 0:
		out = []string{fmt.Sprintf("%s is defined %d times:", written, len(l.Anchors))}
		for _, an := range l.Anchors {
			out = append(out, fmt.Sprintf("  %s:%d", termtext.Clean(an.File), a.ws.Line(an.File, an.LabelRange.Start)))
		}
	case len(l.Sections) > 0:
		out = []string{written + " is ambiguous:"}
		for _, c := range l.Sections {
			out = append(out, "  "+th.uiText(termtext.Clean(c.Qualified()))+" "+termtext.Clean(c.Title))
		}
	case explain:
		out = []string{th.Mark(diag.Error).Render(written) + " does not resolve to anything in the spec"}
	}
	if explain && a.ws.Formal.QuoteStale(l) {
		out = append(out, th.Stale.Render("The tag's quote is not in this text."))
	}
	if explain && t.Problem != "" {
		out = append(out, wrapped(th.uiText(t.Problem), th.Ink)...)
	}
	return out
}

// specFileLines says what a Spec-file tag does.
func (a *App) specFileLines(t *formal.Tag) []string {
	th := a.theme
	if t.Stem == "" {
		return wrapText(th.uiText(termtext.Clean(t.Problem)), a.popupWidth())
	}
	files := a.ws.Spec.FilesWithStem(t.Stem)
	if len(files) == 0 {
		return []string{th.Mark(diag.Error).Render(termtext.Clean(t.Stem)) + " is the stem of no spec file"}
	}
	out := []string{th.uiText("Bare § numbers in this module look here first:")}
	for _, f := range files {
		out = append(out, "  "+th.OutlineFile.Render(termtext.Clean(f.Path)))
	}
	return out
}

// tagPreview is the popup of a tag; explain as for tagLines.
func (a *App) tagPreview(p *popup, m *formal.File, t *formal.Tag, explain bool) {
	if t.Kind == formal.TagDerived {
		p.lines = append(p.lines, a.theme.dimItalic().Render(string(formal.TagDerived)))
	}
	p.lines = append(p.lines, a.tagLines(m, t, explain)...)
	p.setTargets("This tag matches several places", a.tagItems(m, t))
}

// declPreview is the popup of a tagged declaration: the full text of what its
// Spec tags cite, and a dimmed line per Derived-from tag.
func (a *App) declPreview(p *popup, m *formal.File, d *formal.Decl) {
	th := a.theme
	name := termtext.Clean(d.Name)
	p.lines = append(p.lines, th.OutlineDef.Bold(true).Render(name)+" cites")
	for _, t := range m.TagsOf(d) {
		if t.Kind == formal.TagDerived {
			continue
		}
		p.lines = append(p.lines, "")
		p.lines = append(p.lines, a.tagLines(m, t, true)...)
	}
	for _, t := range m.TagsOf(d) {
		if t.Kind == formal.TagDerived {
			p.lines = append(p.lines, "")
			for _, line := range wrapText(termtext.Clean(a.derivedLine(t)), a.popupWidth()) {
				p.lines = append(p.lines, th.dimItalic().Render(line))
			}
		}
	}
	p.setTargets(name+" cites several places", a.declItems(m, d))
}

// refText is a tag's reference as generated text: its § follows the profile.
func (a *App) refText(r *formal.Ref) string {
	return a.theme.uiText(termtext.Clean(r.Written()))
}

// derivedLine names what a Derived-from tag cites, in one line.
func (a *App) derivedLine(t *formal.Tag) string {
	if t.Ref == nil {
		return "Derived-from: " + a.theme.uiText(t.Problem)
	}
	line := "Derived-from " + a.refText(t.Ref)
	l := a.ws.Formal.LinkOf(t)
	if l == nil {
		return line
	}
	switch {
	case l.Anchor != nil && l.Anchor.Title != "":
		line += " " + l.Anchor.Title
	case l.Section != nil:
		line += " " + l.Section.Title
	}
	return line
}

// modelPlaceName names where the cursor is in model m: the declaration it is
// in, for the status line.
func modelPlaceName(m *formal.File, off int) string {
	name := ""
	for _, d := range m.Decls {
		if d.NameRange.Start > off {
			break
		}
		name = d.Name
	}
	return name
}

// cites returns the declarations of m that carry a tag.
func cites(m *formal.File) map[*formal.Decl]bool {
	out := map[*formal.Decl]bool{}
	for _, t := range m.Tags {
		if t.Decl != nil {
			out[t.Decl] = true
		}
	}
	return out
}

// declDetail is the quick-open detail of a declaration: its place.
func declDetail(m *formal.File, d *formal.Decl) string {
	return termtext.Clean(m.Path) + ":" + strconv.Itoa(m.Index().Line(d.NameRange.Start))
}

// span is the bytes token t covers; ok is false for no token.
func (t tokenAt) span() (pos.Range, bool) {
	switch {
	case t.occ != nil:
		return t.occ.Range, true
	case t.sec != nil:
		return t.sec.Range, true
	case t.label != nil:
		return t.label.LabelRange, true
	case t.head != nil:
		return t.head.NumberRange, true
	case t.tag != nil:
		return t.tag.Range, true
	case t.decl != nil:
		return t.decl.NameRange, true
	}
	return pos.Range{}, false
}

// any reports whether there is a token.
func (t tokenAt) any() bool {
	_, ok := t.span()
	return ok
}
