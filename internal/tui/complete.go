// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/termtext"
)

// completionRows is the most entries the completion popup shows.
const completionRows = 8

// sectionSign is § (U+00A7).
const sectionSign = "§"

// completion is the popup that completes a $Name (anchors with their kind
// and title) or a §N / #N (sections with their titles) while typing.
type completion struct {
	// sigil is "$", "§" or "#"; start is its offset in the buffer.
	sigil string
	start int
	path  string
	all   []listItem
	items []listItem
	// names are what each item inserts, by key.
	insert map[string]string
	sel    int
	scroll int
}

// isWordChar reports whether c continues a word: a sigil typed right
// after one (a$b, PR#3) opens no completion.
func isWordChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// maybeComplete opens the completion popup when typed (just typed at the
// cursor) is a sigil after a non-word character, in prose.
func (a *App) maybeComplete(typed string) {
	if typed != "$" && typed != "#" && typed != sectionSign {
		return
	}
	ed := a.cur.Ed
	start := ed.Cursor() - len(typed)
	src := ed.Buffer().Bytes()
	if start < 0 || string(src[start:ed.Cursor()]) != typed {
		return
	}
	if start > 0 && isWordChar(src[start-1]) {
		return
	}
	m := a.ws.Model(ed.Path())
	if m != nil {
		// In a model, only where a tag's reference goes, and never for #: it
		// means "not equal" in TLA+, and $ and § in other comments are prose.
		if typed == "#" {
			return
		}
		a.fresh()
		if m = a.ws.Model(ed.Path()); m == nil || !m.RefSlot(start) {
			return
		}
	} else if !ed.InProse(start) {
		return
	}
	c := &completion{sigil: typed, start: start, path: ed.Path(), insert: map[string]string{}}
	if typed == "$" {
		for _, an := range a.ws.Spec.Anchors {
			it := listItem{
				key:    an.Name,
				label:  a.theme.kindStyle(an.Kind).UnsetBold().Render("$" + an.Name),
				detail: termtext.Clean(strings.TrimSpace(string(an.Kind) + "  " + an.Title)),
			}
			if _, dup := c.insert[it.key]; dup {
				continue
			}
			c.insert[it.key] = "$" + an.Name
			c.all = append(c.all, it)
		}
	} else {
		// A reference must reach the pick as the text stands now.
		a.fresh()
		from := ed.Path()
		resolve := func(stem, number string) (mdspec.Resolution, *mdspec.Section, []*mdspec.Section) {
			return a.ws.Spec.ResolveSection(from, stem, number)
		}
		if m != nil {
			resolve = func(stem, number string) (mdspec.Resolution, *mdspec.Section, []*mdspec.Section) {
				return a.ws.Formal.ResolveSection(m, stem, number)
			}
		}
		for _, s := range a.ws.Spec.Sections {
			if s.Number == "" {
				continue
			}
			ref, addressable := sectionRef(s, resolve)
			if !addressable {
				continue
			}
			shown := termtext.Clean(ref)
			key := strings.TrimPrefix(shown, sectionSign) + " " + s.Title
			c.insert[key] = ref
			c.all = append(c.all, listItem{
				key:   key,
				label: a.theme.OutlineSec.Render(shown) + " " + termtext.Clean(s.Title),
			})
		}
	}
	if len(c.all) == 0 {
		return
	}
	c.items = c.all
	a.comp = c
}

// sectionRef returns the shortest reference that resolve takes to s (§N, else
// §stem:N), and false when neither reaches it: a duplicate number or stem.
func sectionRef(s *mdspec.Section, resolve func(stem, number string) (mdspec.Resolution, *mdspec.Section, []*mdspec.Section)) (string, bool) {
	if st, tgt, _ := resolve("", s.Number); st == mdspec.Resolved && tgt == s {
		return sectionSign + s.Number, true
	}
	st, tgt, _ := resolve(mdspec.StemOf(s.File), s.Number)
	return s.Qualified(), st == mdspec.Resolved && tgt == s
}

// query returns what was typed after the sigil, and false when the
// completion no longer applies (the cursor left the token).
func (c *completion) query(a *App) (string, bool) {
	ed := a.cur.Ed
	if ed.Path() != c.path {
		return "", false
	}
	src := ed.Buffer().Bytes()
	cur := ed.Cursor()
	from := c.start + len(c.sigil)
	if cur < from || from > len(src) || string(src[c.start:from]) != c.sigil {
		return "", false
	}
	q := string(src[from:cur])
	for k := 0; k < len(q); k++ {
		ch := q[k]
		ok := isWordChar(ch) || ch == '.' || ch == '-'
		if !ok {
			return "", false
		}
	}
	return q, true
}

// update refilters after an edit and closes when nothing matches.
func (a *App) updateCompletion() {
	c := a.comp
	if c == nil {
		return
	}
	q, ok := c.query(a)
	if !ok {
		a.comp = nil
		return
	}
	c.items = fuzzyFilter(q, c.all)
	if len(c.items) == 0 {
		a.comp = nil
		return
	}
	c.sel, c.scroll = 0, 0
}

// completionKey runs a key while the completion popup is open.
func (a *App) completionKey(msg tea.KeyPressMsg, b Binding, ok bool) tea.Cmd {
	c := a.comp
	ed := a.cur.Ed
	if ok {
		switch b.Action {
		case ActAccept:
			if c.sel < len(c.items) {
				text := c.insert[c.items[c.sel].key]
				ed.ReplaceRange(pos.Range{Start: c.start, End: ed.Cursor()}, text)
				a.comp = nil
				return a.edited()
			}
			a.comp = nil
			return nil
		case ActCancel:
			a.comp = nil
			return nil
		case ActUp:
			c.sel = max(c.sel-1, 0)
			return nil
		case ActDown:
			c.sel = min(c.sel+1, len(c.items)-1)
			return nil
		case ActBackspace:
			ed.Backspace()
			cmd := a.edited()
			a.updateCompletion()
			return cmd
		}
	}
	k := msg.Key()
	if !ok && k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper|tea.ModHyper|tea.ModMeta) == 0 {
		a.lastCut = false
		ed.Type(k.Text)
		cmd := a.edited()
		a.updateCompletion()
		if a.comp == nil {
			// A sigil typed here starts a completion of its own.
			a.maybeComplete(k.Text)
		}
		return cmd
	}
	// Any other key closes the list and acts in the editor.
	a.comp = nil
	eb, eok := lookupKey(CtxEditor, msg)
	return a.editorKey(msg, eb, eok)
}

// layer renders the completion popup under the cursor.
func (c *completion) layer(a *App) *lipgloss.Layer {
	th := a.theme
	rows := min(len(c.items), completionRows)
	if c.sel < c.scroll {
		c.scroll = c.sel
	}
	if c.sel >= c.scroll+rows {
		c.scroll = c.sel - rows + 1
	}
	inner := 0
	var lines []string
	for k := c.scroll; k < c.scroll+rows && k < len(c.items); k++ {
		it := c.items[k]
		text := it.label
		if it.detail != "" {
			text += "  " + th.PopupDim.Render(it.detail)
		}
		lines = append(lines, text)
		inner = max(inner, ansi.StringWidth(text))
	}
	if n := len(c.items); n > rows {
		more := fmt.Sprintf("%d of %d", c.sel+1, n)
		lines = append(lines, th.PopupDim.Render(more))
		inner = max(inner, len(more))
	}
	inner = min(inner, max(a.width-4, 10), 70)
	for k := range lines {
		lines[k] = padTo(lines[k], inner)
		if c.scroll+k == c.sel && k < rows {
			lines[k] = th.Selected.Render(ansi.Strip(lines[k]))
		}
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(th.dim).Padding(0, 1).
		Render(strings.Join(lines, "\n"))
	bw, bh := inner+4, len(lines)+2
	ex, ey, _, eh := a.editorRect()
	x, y := ex, ey
	if cx, cy, ok := a.cur.Ed.CursorCell(); ok {
		x, y = ex+cx, ey+cy+1
		if y+bh > ey+eh && cy-bh >= 0 {
			y = ey + cy - bh
		}
	}
	x = min(max(x, 0), max(a.width-bw, 0))
	y = max(y, 0)
	return lipgloss.NewLayer(box).X(x).Y(y)
}
