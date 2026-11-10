// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/workflow"
)

// A model proposal is an agent's edit of one formal model (`spec change
// --tla`). Its card is decided as a whole, like a change: y applies every
// hunk into the model's buffer and the save records it, n rejects it. Chris's
// own edits of a model save directly and need no proposal.

// modelMaxDiff caps the diff lines a selected model card shows; Enter opens the
// review with all of it.
const modelMaxDiff = 12

// modelConfirm is the model proposal a second y applies despite its check
// errors: its id, revision and the check it was asked about.
type modelConfirm struct {
	id       string
	revision int
	hash     string
}

// bufferOverlay is the open documents' text, laid over the files on disk.
func (a *App) bufferOverlay() map[string][]byte {
	overlay := map[string][]byte{}
	for p, d := range a.ws.Docs() {
		overlay[p] = d.Ed.Buffer().Bytes()
	}
	return overlay
}

// modelGate runs before y applies model proposal p: when the model moved since
// p was checked, it is checked again as approving would leave it (p records the
// result, which the save's receipt keeps), and a model left with check errors
// needs a second y. It reports whether to apply now.
func (a *App) modelGate(p *proposal.Proposal) bool {
	if !p.IsModel() {
		return true
	}
	if _, e := a.wf.RecheckModel(context.Background(), a.ws.Project, p, a.bufferOverlay()); e != nil {
		a.notice("Cannot check the model", e)
		return false
	}
	warn := workflow.ModelErrorsWarning(p)
	if warn == "" {
		a.modelConfirm = nil
		return true
	}
	want := modelConfirm{id: p.ID, revision: p.Revision, hash: p.Model.Check.Hash}
	if a.modelConfirm != nil && *a.modelConfirm == want {
		a.modelConfirm = nil
		return true
	}
	a.modelConfirm = &want
	a.setFlash("%s; y again applies it anyway", warn)
	return false
}

// modelCheckBadge says in a few words how a model proposal's check went. A
// check that did not cover semantics says "Syntax checked".
func modelCheckBadge(c proposal.ModelCheck) string {
	switch c.State {
	case proposal.ModelClean:
		if !c.FullCheck() {
			return "Syntax checked, clean" + checkerSuffix(c)
		}
		return "Check clean" + checkerSuffix(c)
	case proposal.ModelErrors:
		if !c.FullCheck() {
			return "Syntax checked, " + plural(c.Errors(), "error") + checkerSuffix(c)
		}
		return plural(c.Errors(), "check error") + checkerSuffix(c)
	case proposal.ModelCheckFailed:
		return "Check failed to run: " + oneLine(termtext.Clean(c.Message))
	}
	return "Not checked"
}

// checkerSuffix names the checker of c, after a separator ("" when unnamed).
func checkerSuffix(c proposal.ModelCheck) string {
	if c.Checker == "" {
		return ""
	}
	s := " · " + c.Checker
	if c.Rechecked {
		s += ", rechecked"
	}
	return s
}

// modelDecls lists the definitions a model proposal touches, at most three
// with a count of the rest.
func modelDecls(m *proposal.Model) string {
	if len(m.Decls) == 0 {
		return ""
	}
	shown := m.Decls
	more := ""
	if len(shown) > 3 {
		more = fmt.Sprintf(" (+%d)", len(shown)-3)
		shown = shown[:3]
	}
	return strings.Join(shown, ", ") + more
}

// modelCardBody is the body of model proposal p's card below its label and
// author: the model, the definitions touched, the check, the tag links it
// breaks, whether it still applies and, when selected, the diff.
func (a *App) modelCardBody(p *proposal.Proposal, inner int, sel bool) []string {
	th := a.theme
	m := p.Model
	var body []string
	body = append(body, th.HunkHead.Render(settingsPath(oneLine(termtext.Clean(m.File)), inner, th)))
	if ds := modelDecls(m); ds != "" {
		body = append(body, th.PopupDim.Render(cardValue("Touches "+ds, inner, th)))
	}
	badge := modelCheckBadge(m.Check)
	switch m.Check.State {
	case proposal.ModelErrors, proposal.ModelCheckFailed:
		body = append(body, th.Stale.Render(cardValue(badge, inner, th)))
		for _, d := range m.Check.Diagnostics {
			if d.Severity == "error" {
				line := fmt.Sprintf("L%d %s", d.Start.Line, d.Message)
				body = append(body, th.PopupDim.Render(cardValue(line, inner, th)))
				break
			}
		}
	default:
		body = append(body, th.PopupDim.Render(cardValue(badge, inner, th)))
	}
	for _, l := range m.Links {
		text := fmt.Sprintf("L%d %s [%s]", l.Line, l.Message, l.Code)
		style := th.PopupDim
		if l.Severity == "error" {
			style = th.Stale
		}
		body = append(body, style.Render(cardValue(text, inner, th)))
	}
	if p.StalePrimary != nil {
		for _, l := range wrapText(oneLine(termtext.Clean("No longer applies: "+p.StalePrimary.Reason)), inner) {
			body = append(body, th.Stale.Render(l))
		}
	} else {
		body = append(body, "Ready to review")
	}
	if !sel {
		return body
	}
	var diff []string
	for _, h := range p.Hunks {
		diff = append(diff, diffLines(th, h.Find, h.Replace, inner, "")...)
	}
	if len(diff) > modelMaxDiff {
		diff = append(diff[:modelMaxDiff-1], th.PopupDim.Render(fmt.Sprintf(th.uiText("… %d more lines; Enter reviews it all"), len(diff)-modelMaxDiff+1)))
	}
	for k, l := range diff {
		if ansi.StringWidth(l) > inner {
			diff[k] = ansi.Truncate(l, inner, "")
		}
	}
	return append(body, diff...)
}

// modelLoc is where model proposal p edits: its model, at its first hunk when
// that is still in the buffer.
func (a *App) modelLoc(p *proposal.Proposal) (Location, bool) {
	file := p.Model.File
	if !a.ws.IsModel(file) {
		return Location{}, false
	}
	loc := Location{Path: file}
	src := ""
	if d, ok := a.ws.Docs()[file]; ok {
		src = string(d.Ed.Buffer().Bytes())
	} else if m := a.ws.Model(file); m != nil {
		src = string(m.Source())
	}
	if len(p.Hunks) > 0 {
		if at := strings.Index(src, p.Hunks[0].Find); at >= 0 {
			loc.Off = at
		}
	}
	return loc, true
}
