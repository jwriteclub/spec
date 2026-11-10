// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// A check (^P, `spec propose --keep`) judged its anchor's text as it was
// then. Once the buffer moves on, its findings are about text that is no
// longer there:
//
//   - while the anchor differs from the text the check saw (allowing for
//     the check's own accepted edits and for whitespace), the check is
//     outdated: its group says so, its cards are greyed, and y does not
//     apply them; ^P rechecks (and supersedes it), n dismisses;
//   - once the anchor is back to its HEAD text, there is no change left to
//     check: Chris's check is withdrawn (rejected as superseded, under
//     .spec/lock as every decision is), on the reparse after the edit.
//
// An agent's proposal is a change not yet in the files; it is never
// withdrawn here (the review finds its stale hunks).

// checkOf reports whether p is a check of an edit in the files (not an
// agent's proposal, a fix or an analysis).
func checkOf(p *proposal.Proposal) bool {
	return p.Primary != nil && p.Primary.InWorktree && p.Primary.Name != ""
}

// foldSpace collapses runs of whitespace, so rewrapping is no change.
func foldSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// anchorBody returns the current text of anchor name ("$Name") in the
// buffers and how many anchors have that name.
func (a *App) anchorBody(name string) (string, int) {
	if a.ws.Spec == nil {
		return "", 0
	}
	ans := a.ws.Spec.Lookup(strings.TrimPrefix(name, "$"))
	if len(ans) != 1 {
		return "", len(ans)
	}
	an := ans[0]
	f := a.ws.Spec.File(an.File)
	if f == nil || !an.BodyRange.Valid(len(f.Source())) {
		return "", 0
	}
	return f.Text(an.BodyRange), 1
}

// checkOutdated says why check p no longer matches its anchor ("" when it
// does, or p is not a check): the anchor's text differs from the text p
// checked with p's accepted edits applied, whitespace aside.
func (a *App) checkOutdated(p *proposal.Proposal) string {
	if !checkOf(p) || a.ws.Spec == nil {
		return ""
	}
	name := p.Primary.Name
	text, n := a.anchorBody(name)
	switch {
	case n > 1:
		return "" // a duplicate name: lint says so
	case n == 0 && p.Primary.NewText == "":
		return "" // a removal, still removed
	case n == 0:
		return name + " is no longer in the spec"
	}
	want := foldSpace(p.Primary.NewText)
	for _, h := range p.Hunks {
		if !applied(a.decided(p.ID, fmt.Sprintf("h%d", h.N))) {
			continue
		}
		if find := foldSpace(h.Find); find != "" && strings.Count(want, find) == 1 {
			want = strings.Replace(want, find, foldSpace(h.Replace), 1)
		}
	}
	if foldSpace(text) == want {
		return ""
	}
	return name + " changed since this check"
}

// atHead reports whether anchor name ("$Name") exists once and matches
// HEAD; false while the baseline is not known.
func (a *App) atHead(name string) bool {
	b := a.ws.Base
	if a.ws.Spec == nil || b == nil || b.State != baseline.StateOK || b.Spec == nil {
		return false
	}
	ans := a.ws.Spec.Lookup(strings.TrimPrefix(name, "$"))
	return len(ans) == 1 && a.changeState(ans[0]) == ""
}

// withdrawReverted withdraws Chris's pending checks whose anchor is back
// to its HEAD text, and flashes which.
func (a *App) withdrawReverted() tea.Cmd {
	var cmds []tea.Cmd
	for _, p := range a.pending {
		if !checkOf(p) || !p.HumanSource() || a.waitingOn(p.ID) || !a.atHead(p.Primary.Name) {
			continue
		}
		id, name := p.ID, p.Primary.Name
		cmds = append(cmds, a.underLock(id, "withdraw "+id, func(a *App, lock *project.Lock) tea.Cmd {
			if e := a.wf.CheckRecovery(a.ws.Root, id); e != nil {
				return nil
			}
			if _, e := a.wf.RejectLocked(lock, a.ws.Root, id, "superseded: "+name+" is back to its committed text", proposal.AuthorHuman); e != nil {
				if e.Code != "not_pending" {
					lg.WithError(e).Warn("cannot withdraw " + id)
				}
				a.loadPending()
				return nil
			}
			a.loadPending()
			a.setFlash("%s is back to its committed text; its check was withdrawn", name)
			return nil
		}))
	}
	return tea.Batch(cmds...)
}
