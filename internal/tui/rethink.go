// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

// The rethink (notes/plan-step4g-fix-review.md, "Rethink"): f on the
// question of a check or an analysis opens the notes box with the question
// quoted; ^O sends the answer, and the model redoes the check (or the
// analysis) with it. The new one supersedes the old one (its accepted
// fixes recorded, the rest rejected as superseded) and carries the notes.
// A change to Chris's own edit shows as a fix on his anchor, marked
// "changes your edit", proposed like any fix; an analysis has no own edit,
// and its fixes may land anywhere.

// answerable reports whether a question of p can be answered with f: p is
// a check of Chris's edit or an analysis.
func answerable(p *proposal.Proposal) bool {
	return p != nil && (checkOf(p) || p.Kind == proposal.KindAnalysis)
}

// rethinkWhat names what a rethink of p redoes: "the check" or "the
// analysis".
func rethinkWhat(p *proposal.Proposal) string {
	if p.Kind == proposal.KindAnalysis {
		return "the analysis"
	}
	return "the check"
}

// answerCard is f on a question: ask for the answer, then rethink the
// check or analysis with it.
func (a *App) answerCard(c *card) tea.Cmd {
	if c == nil || c.kind != cardQuestion {
		return nil
	}
	if !answerable(c.p) {
		a.setFlash("Only a question of a check of your edit or of an analysis can be answered here; n dismisses it")
		return nil
	}
	if a.busy() {
		return nil
	}
	p, question := c.p, c.text
	what := rethinkWhat(p)
	a.push(newTextEditScreen(a, textEdit{
		title:     "Answer " + what + "'s question: the model rethinks " + what,
		keepLabel: "Send answer",
		quote:     question,
		prompt:    "Your answer (add any other notes; ^O sends):",
		status:    " Answer for the model",
		submit: func(a *App, text string) tea.Cmd {
			if strings.TrimSpace(text) == "" {
				a.setFlash("No answer written: Esc cancels")
				return nil
			}
			a.pop()
			note := proposal.RethinkNote{Question: question, Text: strings.TrimSpace(text)}
			return a.saveFirst("Rethink", func() tea.Cmd { return a.runRethink(p, note) })
		},
		cancel: "No answer sent",
	}))
	return nil
}

// runRethink redoes check (or analysis) p with note in the background,
// files the new one and supersedes p.
func (a *App) runRethink(p *proposal.Proposal, note proposal.RethinkNote) tea.Cmd {
	if a.busy() {
		return nil
	}
	wf, proj := a.wf, a.ws.Project
	decisions := maps.Clone(a.side.state.Decided[p.ID])
	name := rethinkWhat(p)
	if checkOf(p) {
		name = p.Primary.Name
	}
	return a.startJob("Rethinking "+name, func(ctx context.Context) func(*App) tea.Cmd {
		np, e := wf.Rethink(ctx, proj, workflow.RethinkRequest{Of: p, Decisions: decisions, Note: note, Author: proposal.AuthorHuman})
		if e == nil && ctx.Err() != nil {
			return nil // cancelled after the model answered: file nothing
		}
		if e == nil {
			if err := wf.Store(proj.Root).Create(np); err != nil {
				e = &workflow.Error{Exit: workflow.ExitFail, Code: "write_failed", Message: fmt.Sprintf("cannot write %s: %v", proposal.Dir, err)}
			}
		}
		return func(a *App) tea.Cmd {
			if e != nil {
				a.notice("Rethink failed", e)
				return nil
			}
			a.loadPending()
			cmds := []tea.Cmd{a.supersedeRethought(p, np)}
			if !hasItems(np) {
				cmds = append(cmds, a.settleIfDone(np))
			}
			a.loadPending()
			a.landed([]*proposal.Proposal{np}, "Rethought "+name)
			return tea.Batch(cmds...)
		}
	})
}

// supersedeRethought closes check (or analysis) old now that np redoes
// it: the fixes Chris accepted from it are recorded as applied (they are
// in the files), and the rest is rejected as superseded by np. Other
// pending checks of the same anchor are superseded as ^P supersedes them.
func (a *App) supersedeRethought(old, np *proposal.Proposal) tea.Cmd {
	id := old.ID
	why := "superseded by rethink " + np.ID
	cmd := a.underLock(id, "supersede "+id, func(a *App, lock *project.Lock) tea.Cmd {
		if e := a.wf.CheckRecovery(a.ws.Root, id); e != nil {
			return nil
		}
		var e *workflow.Error
		if acc := a.acceptedFixesOf(old); len(acc) > 0 {
			d := proposal.Decision{Accepted: a.acceptedOf(old), Applied: a.acceptedOf(old), Reason: why, By: proposal.AuthorHuman}
			_, e = a.wf.SettleSeenLocked(lock, a.ws.Root, old, d)
		} else {
			_, e = a.wf.RejectLocked(lock, a.ws.Root, id, why, proposal.AuthorHuman)
		}
		if e != nil && e.Code != "not_pending" {
			lg.WithError(e).Warn("cannot supersede " + id)
		}
		a.loadPending()
		return nil
	})
	return tea.Batch(cmd, a.supersede(np))
}

// acceptedFixesOf are the fixes of check p that Chris put into the buffer
// (accepted or edited) or that p records as applied.
func (a *App) acceptedFixesOf(p *proposal.Proposal) []int {
	var out []int
	for _, h := range fixList(p) {
		if p.AppliedHunk(h.N) || applied(a.decided(p.ID, hunkKey(h.N))) {
			out = append(out, h.N)
		}
	}
	return out
}

// rethinkPrimary reports whether fix h of p changes Chris's own edit (a
// rethink's primary change).
func rethinkPrimary(p *proposal.Proposal, n int) bool {
	return p != nil && p.Rethink != nil && p.Rethink.PrimaryHunk != 0 && p.Rethink.PrimaryHunk == n
}
