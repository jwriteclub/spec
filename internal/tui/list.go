// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/termtext"
)

// listItem is one entry of a list popup.
type listItem struct {
	// key is what quick open matches against.
	key    string
	label  string // styled
	detail string // plain, shown dim
	loc    Location
}

// listPopup is a centered box with an optional filter line and a list:
// quick open (^Q), references (^\) and ambiguous jump targets.
type listPopup struct {
	title  string
	filter bool
	query  string
	all    []listItem
	items  []listItem
	sel    int
	scroll int

	// geometry of the last render, for clicks
	x, y, w, rows, first int
}

// newList returns a list popup over items.
func newList(title string, items []listItem, filter bool) *listPopup {
	l := &listPopup{title: title, all: items, filter: filter}
	l.refilter()
	return l
}

// refilter applies the query.
func (l *listPopup) refilter() {
	if !l.filter || l.query == "" {
		l.items = l.all
	} else {
		l.items = fuzzyFilter(l.query, l.all)
	}
	l.sel, l.scroll = 0, 0
}

// move moves the selection by n.
func (l *listPopup) move(n int) {
	if len(l.items) == 0 {
		return
	}
	l.sel = min(max(l.sel+n, 0), len(l.items)-1)
}

// selected returns the selected item's location.
func (l *listPopup) selected() (Location, bool) {
	if l.sel < 0 || l.sel >= len(l.items) {
		return Location{}, false
	}
	return l.items[l.sel].loc, true
}

// listKey runs a list-context key.
func (a *App) listKey(msg tea.KeyPressMsg, b Binding, ok bool) tea.Cmd {
	l := a.list
	if !ok {
		k := msg.Key()
		if l.filter && k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
			return a.typeInto(k.Text)
		}
		return nil
	}
	switch b.Action {
	case ActAccept:
		loc, ok := l.selected()
		a.list = nil
		if !ok {
			a.setFlash("Nothing selected")
			return nil
		}
		a.jump(loc)
	case ActCancel:
		a.list = nil
	case ActUp:
		l.move(-1)
	case ActDown:
		l.move(1)
	case ActBackspace:
		if l.filter && l.query != "" {
			_, n := utf8.DecodeLastRuneInString(l.query)
			l.query = l.query[:len(l.query)-n]
			l.refilter()
		}
	}
	return nil
}

// typeInto adds typed or pasted text to the open list filter or prompt.
func (a *App) typeInto(s string) tea.Cmd {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", " ")
	switch {
	case a.prompt != nil:
		a.prompt.input += s
	case a.list != nil && a.list.filter:
		a.list.query += s
		a.list.refilter()
	}
	return nil
}

// layer renders the list popup centered over the main screen.
func (l *listPopup) layer(a *App) *lipgloss.Layer {
	th := a.theme
	inner := min(max(a.width*3/4, 40), a.width-4)
	maxRows := max(a.bodyHeight()-6, 3)
	rows := min(max(len(l.items), 1), maxRows)
	if l.sel < l.scroll {
		l.scroll = l.sel
	}
	if l.sel >= l.scroll+rows {
		l.scroll = l.sel - rows + 1
	}
	var lines []string
	lines = append(lines, th.PopupTitle.Render(padTo(l.title, inner)))
	if l.filter {
		lines = append(lines, th.Prompt.Render("> ")+l.query)
	}
	head := len(lines)
	if len(l.items) == 0 {
		lines = append(lines, th.PopupDim.Render("no matches"))
	}
	for k := l.scroll; k < len(l.items) && k < l.scroll+rows; k++ {
		it := l.items[k]
		text := it.label
		if it.detail != "" {
			text += "  " + th.PopupDim.Render(it.detail)
		}
		text = padTo(text, inner)
		if k == l.sel {
			text = th.Selected.Render(ansi.Strip(text))
		}
		lines = append(lines, text)
	}
	if n := len(l.items); n > rows {
		lines = append(lines, th.PopupDim.Render(fmt.Sprintf("%d of %d", l.sel+1, n)))
	}
	for k := range lines {
		lines[k] = padTo(lines[k], inner)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(th.dim).Padding(0, 1).
		Render(strings.Join(lines, "\n"))
	bw := inner + 4
	l.x = max((a.width-bw)/2, 0)
	l.y = max((a.bodyHeight()-(len(lines)+2))/3, 0)
	l.w = bw
	l.first = l.y + 1 + head
	l.rows = rows
	return lipgloss.NewLayer(box).X(l.x).Y(l.y)
}

// click handles a click at screen cell (x, y): hit is true inside the box,
// and loc is set when an item was clicked.
func (l *listPopup) click(_ *App, x, y int) (*Location, bool) {
	if x < l.x || x >= l.x+l.w || y < l.y {
		return nil, false
	}
	k := y - l.first
	if k >= 0 && k < l.rows && l.scroll+k < len(l.items) {
		loc := l.items[l.scroll+k].loc
		return &loc, true
	}
	return nil, y < l.first+l.rows+2
}

// ---- items ----

// anchorItem is a list entry for an anchor.
func (a *App) anchorItem(an *mdspec.Anchor) listItem {
	detail := fmt.Sprintf("%s:%d", an.File, a.ws.Line(an.File, an.LabelRange.Start))
	if an.Title != "" {
		detail = an.Title + "  " + detail
	}
	return listItem{
		key:    an.Name + " " + an.Title,
		label:  a.theme.kindStyle(an.Kind).UnsetBold().Render("$" + an.Name),
		detail: detail,
		loc:    anchorLoc(an),
	}
}

// sectionItem is a list entry for a section.
func (a *App) sectionItem(s *mdspec.Section) listItem {
	return listItem{
		key:    s.Number + " " + s.Title,
		label:  a.theme.OutlineSec.Render(a.theme.uiText("§")+s.Number) + " " + s.Title,
		detail: fmt.Sprintf("%s:%d", s.File, a.ws.Line(s.File, s.HeadingRange.Start)),
		loc:    sectionLoc(s),
	}
}

// mentionItem is a list entry for a reference at off in path: the line's
// text.
func (a *App) mentionItem(path string, off int) listItem {
	line := a.ws.Line(path, off)
	text := strings.TrimSpace(termtext.Clean(a.ws.LineText(path, line)))
	return listItem{
		label:  fmt.Sprintf("%s:%d", path, line),
		detail: text,
		loc:    Location{Path: path, Off: off},
	}
}

// openQuickOpen opens the fuzzy finder over anchors, sections and files.
func (a *App) openQuickOpen() {
	var items []listItem
	for _, an := range a.ws.Spec.Anchors {
		items = append(items, a.anchorItem(an))
	}
	for _, s := range a.ws.Spec.Sections {
		items = append(items, a.sectionItem(s))
	}
	for _, p := range a.ws.Paths() {
		items = append(items, listItem{key: p, label: a.theme.OutlineFile.Render(p), loc: Location{Path: p}})
	}
	for _, p := range a.ws.ModelPaths() {
		m := a.ws.Model(p)
		if m == nil {
			continue
		}
		items = append(items, listItem{key: p, label: a.theme.OutlineFile.Render(termtext.Clean(p)), loc: Location{Path: p}})
		for _, d := range m.Decls {
			name := termtext.Clean(d.Name)
			items = append(items, listItem{
				key: d.Name + " " + p, label: a.theme.OutlineDef.Render(name), detail: declDetail(m, d),
				loc: Location{Path: p, Off: d.NameRange.Start},
			})
		}
	}
	a.list = newList(a.theme.uiText("Quick open: $anchors, §sections, files"), items, true)
}

// ---- fuzzy matching ----

// fuzzyFilter returns the items matching query, best first. A leading $, §
// or # in the query is ignored, so "§5.2", "#5.2" and "5.2" all find
// section 5.2.
func fuzzyFilter(query string, items []listItem) []listItem {
	q := strings.ToLower(strings.TrimSpace(query))
	q = strings.TrimLeft(q, "$#")
	q = strings.TrimPrefix(q, "§")
	if q == "" {
		return items
	}
	type scored struct {
		it    listItem
		score int
		idx   int
	}
	var hits []scored
	for i, it := range items {
		if s, ok := fuzzyScore(q, strings.ToLower(it.key)); ok {
			hits = append(hits, scored{it, s, i})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].idx < hits[j].idx
	})
	out := make([]listItem, len(hits))
	for k, h := range hits {
		out[k] = h.it
	}
	return out
}

// fuzzyScore matches q as a subsequence of s. Contiguous runs, a match at
// the start and matches after a separator score higher; a prefix match
// scores highest.
func fuzzyScore(q, s string) (int, bool) {
	if strings.HasPrefix(s, q) {
		return 1000 - len(s), true
	}
	if i := strings.Index(s, q); i >= 0 {
		return 500 - i - len(s)/4, true
	}
	score := 0
	qi := 0
	prev := -2
	for i := 0; i < len(s) && qi < len(q); i++ {
		if s[i] != q[qi] {
			continue
		}
		switch {
		case i == prev+1:
			score += 5
		case i == 0 || strings.ContainsRune(" ._-/:", rune(s[i-1])):
			score += 3
		default:
			score++
		}
		prev = i
		qi++
	}
	if qi < len(q) {
		return 0, false
	}
	return score - len(s)/8, true
}
