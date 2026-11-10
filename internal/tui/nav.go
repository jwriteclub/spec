// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/termtext"
)

// maxJumps bounds the back and forward stacks.
const maxJumps = 100

// jump goes to loc, recording the current place on the back stack.
func (a *App) jump(loc Location) {
	from := a.here()
	if err := a.open(loc); err != nil {
		a.setFlash("%s", err.Error())
		return
	}
	a.cur.Ed.CenterCursor()
	if from != a.here() {
		a.back = append(a.back, from)
		if len(a.back) > maxJumps {
			a.back = a.back[1:]
		}
		a.fwd = nil
	}
}

// goBack returns to the previous place on the jump stack.
func (a *App) goBack() {
	if len(a.back) == 0 {
		a.setFlash("No earlier location")
		return
	}
	loc := a.back[len(a.back)-1]
	a.back = a.back[:len(a.back)-1]
	a.fwd = append(a.fwd, a.here())
	if err := a.open(loc); err != nil {
		a.setFlash("%s", err.Error())
		return
	}
	a.cur.Ed.CenterCursor()
}

// goForward undoes a goBack.
func (a *App) goForward() {
	if len(a.fwd) == 0 {
		a.setFlash("No later location")
		return
	}
	loc := a.fwd[len(a.fwd)-1]
	a.fwd = a.fwd[:len(a.fwd)-1]
	a.back = append(a.back, a.here())
	if err := a.open(loc); err != nil {
		a.setFlash("%s", err.Error())
		return
	}
	a.cur.Ed.CenterCursor()
}

// tokenAt is what can be followed at an offset of the current file: a $Name or
// § token. Where a model cites it, also a label or a heading's number (label
// and head). In a model, a tag or a declaration with tags (tag and decl).
type tokenAt struct {
	occ   *mdspec.Occurrence
	sec   *mdspec.SectionRef
	label *mdspec.Anchor
	head  *mdspec.Section
	tag   *formal.Tag
	decl  *formal.Decl
}

// tokenUnder returns the token at off in the current file. With inclusive,
// the offset just after a token counts (the cursor after typing a name).
func (a *App) tokenUnder(off int, inclusive bool) tokenAt {
	a.fresh()
	return a.tokenIn(off, inclusive)
}

// tokenIn is tokenUnder without bringing the analysis up to date: the help
// bar asks on every frame, and a reparse there would undo the debounce.
func (a *App) tokenIn(off int, inclusive bool) tokenAt {
	if m := a.ws.Model(a.cur.Ed.Path()); m != nil {
		return modelTokenIn(m, off, inclusive)
	}
	f := a.ws.File(a.cur.Ed.Path())
	if f == nil {
		return tokenAt{}
	}
	if o := f.OccurrenceAt(off); o != nil && (inclusive || off < o.Range.End) {
		return tokenAt{occ: o}
	}
	if r := f.SectionRefAt(off); r != nil && (inclusive || off < r.Range.End) {
		return tokenAt{sec: r}
	}
	return a.citedTokenIn(f, off, inclusive)
}

// anchorLoc is where a jump to an anchor lands.
func anchorLoc(an *mdspec.Anchor) Location {
	return Location{Path: an.File, Off: an.LabelRange.Start}
}

// sectionLoc is where a jump to a section lands.
func sectionLoc(s *mdspec.Section) Location {
	return Location{Path: s.File, Off: s.HeadingRange.Start}
}

// toTarget jumps to the definition or section under the cursor.
func (a *App) toTarget() {
	t := a.tokenUnder(a.cur.Ed.Cursor(), true)
	spec := a.ws.Spec
	switch {
	case t.occ != nil:
		as := spec.Lookup(t.occ.Name)
		switch {
		case t.occ.Role == mdspec.RoleDefinition:
			links := a.ws.Formal.ToAnchor(t.occ.Name)
			items := a.modelItems(links)
			if !a.follow(citedTitle("$"+termtext.Clean(t.occ.Name), len(items)), items) {
				a.setFlash("$%s is defined here; ^\\ lists its references", t.occ.Name)
			}
		case len(as) == 0:
			a.setFlash("$%s is not defined anywhere in the project", t.occ.Name)
		case len(as) == 1:
			a.jump(anchorLoc(as[0]))
		default:
			var items []listItem
			for _, an := range as {
				items = append(items, a.anchorItem(an))
			}
			a.list = newList(fmt.Sprintf("$%s is defined %d times", t.occ.Name, len(as)), items, false)
		}
	case t.sec != nil:
		st, sec, cands := spec.ResolveSection(t.sec.File, t.sec.Stem, t.sec.Number)
		switch st {
		case mdspec.Resolved:
			a.jump(sectionLoc(sec))
		case mdspec.Ambiguous:
			var items []listItem
			for _, c := range cands {
				items = append(items, a.sectionItem(c))
			}
			a.list = newList(fmt.Sprintf(a.theme.uiText("§%s is ambiguous"), t.sec.Number), items, false)
		default:
			a.setFlash(a.theme.uiText("§%s does not match any numbered section"), t.sec.Number)
		}
	case t.label != nil:
		items := a.modelItems(a.ws.Formal.ToAnchor(t.label.Name))
		a.follow(citedTitle("$"+termtext.Clean(t.label.Name), len(items)), items)
	case t.head != nil:
		items := a.modelItems(a.ws.Formal.ToSection(t.head))
		a.follow(citedTitle(a.theme.uiText("§")+t.head.Number, len(items)), items)
	case t.tag != nil || t.decl != nil:
		a.followSpec(t)
	case a.inModel():
		a.setFlash("No tag or tagged declaration under the cursor")
	default:
		a.setFlash("%s", a.theme.uiText("No $Name or § reference under the cursor"))
	}
}

// refs lists the references to the anchor or section under the cursor.
func (a *App) refs() {
	off := a.cur.Ed.Cursor()
	t := a.tokenUnder(off, true)
	spec := a.ws.Spec
	var name string
	var sec *mdspec.Section
	switch {
	case t.occ != nil:
		name = t.occ.Name
	case t.sec != nil:
		_, sec, _ = spec.ResolveSection(t.sec.File, t.sec.Stem, t.sec.Number)
	case t.label != nil:
		name = t.label.Name
	case t.head != nil:
		sec = t.head
	case t.tag != nil:
		name, sec = a.tagRefTarget(t.tag)
	case t.decl != nil:
		name, sec = a.declRefTarget(t.decl)
	default:
		if f := a.ws.File(a.cur.Ed.Path()); f != nil {
			if an := f.AnchorAt(off); an != nil && off <= an.LabelRange.End {
				name = an.Name
			} else if s := f.SectionAt(off); s != nil && off <= s.HeadingRange.End {
				sec = s
			}
		}
	}
	var items []listItem
	var title string
	mentionTags := func(links []*formal.Link) {
		for _, l := range links {
			items = append(items, a.mentionItem(l.File.Path, l.Tag.Range.Start))
		}
	}
	switch {
	case name != "":
		for _, r := range spec.ReferencesTo(name) {
			items = append(items, a.mentionItem(r.File, r.Range.Start))
		}
		mentionTags(a.ws.Formal.ToAnchor(name))
		title = fmt.Sprintf("References to $%s (%d)", name, len(items))
	case sec != nil:
		for _, r := range spec.SectionReferencesTo(sec) {
			items = append(items, a.mentionItem(r.File, r.Range.Start))
		}
		mentionTags(a.ws.Formal.ToSection(sec))
		title = fmt.Sprintf(a.theme.uiText("References to §%s (%d)"), sec.Number, len(items))
	case a.inModel():
		a.setFlash("No tag with a resolved reference under the cursor")
		return
	default:
		a.setFlash("%s", a.theme.uiText("No $Name, § reference, label or numbered heading under the cursor"))
		return
	}
	if len(items) == 0 {
		a.setFlash("%s: none", strings.TrimSuffix(title, " (0)"))
		return
	}
	a.list = newList(title, items, false)
}

// findNext searches for the last query after the cursor.
func (a *App) findNext() {
	if a.query == "" {
		a.setFlash("No current search pattern")
		return
	}
	ed := a.cur.Ed
	r, ok := ed.Find(a.query, ed.Cursor()+1)
	if !ok {
		a.setFlash("\"%s\" not found", a.query)
		return
	}
	if r.Start <= ed.Cursor() {
		a.setFlash("Search Wrapped")
	}
	ed.SetCursor(r.Start)
	ed.SetMatch(r)
}

// save writes the current file. With thenQuit it exits after a good save.
func (a *App) save(thenQuit bool) tea.Cmd {
	d := a.cur
	r, err := a.saveDoc(d)
	switch {
	case errors.Is(err, ErrConflict):
		a.startConflict(d.Ed.Path())
		return nil
	case err != nil:
		lg.WithError(err).Warn("save failed")
		a.setStatus(statusFailure, "Save failed", a.theme.uiText("Save failed: %s · %s"), d.Ed.Path(), errorText(err))
		return nil
	}
	a.savedStatus(d.Ed.Path(), r)
	if thenQuit {
		return a.exit()
	}
	return nil
}

// savedStatus says path was saved, and what that recorded.
func (a *App) savedStatus(path string, r *receipts) {
	kind := statusSuccess
	if r.warned {
		kind = statusWarning
	}
	if len(r.notes) == 0 {
		a.setStatus(kind, "Saved", "Saved %s", path)
		return
	}
	a.setStatus(kind, "Saved", a.theme.uiText("Saved %s · %s"), path, strings.Join(r.notes, "; "))
}

// exit quits, first asking to save a modified buffer (one at a time).
func (a *App) exit() tea.Cmd {
	if a.job != nil && a.job.irreversible {
		return nil
	}
	for _, p := range a.ws.OpenPaths() {
		d, ok := a.ws.Docs()[p]
		if ok && d.Ed.Buffer().Dirty() && !a.discard[p] {
			if d != a.cur {
				_ = a.open(Location{Path: p, Off: d.Ed.Cursor()})
			}
			a.prompt = &prompt{kind: promptSave, label: "Save modified buffer " + p + "? "}
			return nil
		}
	}
	return a.quit()
}
