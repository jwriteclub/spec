// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/workflow"
)

// The per-edit cascade in the UI (notes/plan-step4f-cascade.md): while ^P
// checks an anchor, its card at the top of the sidebar lists the stages
// as they finish (gate, triage, check) and the running one with its time;
// the status line shows the same chain. The editor keeps the keyboard.

// cascadeRun is one anchor's cascade, as the sidebar shows it.
type cascadeRun struct {
	// anchor is the checked anchor, with its $.
	anchor string
	// stages are the finished stages, in order.
	stages []proposal.Stage
	// current is the running stage (nil when none), started at since.
	current *proposal.Stage
	since   time.Time
	// done is set once the job finished; cleared names the stage that
	// cleared the edit, failed why the check failed.
	done    bool
	cleared string
	failed  string
}

// stageMsg carries one stage event of job seq; ch is where the next one
// comes from.
type stageMsg struct {
	seq int
	ev  workflow.StageEvent
	ch  <-chan workflow.StageEvent
}

// waitStage waits for the next stage event of job seq (nil once the job
// closed ch).
func waitStage(seq int, ch <-chan workflow.StageEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return stageMsg{seq: seq, ev: ev, ch: ch}
	}
}

// stageSender returns the OnStage callback of a job: it hands events to
// ch until ctx ends.
func stageSender(ctx context.Context, ch chan<- workflow.StageEvent) func(workflow.StageEvent) {
	return func(ev workflow.StageEvent) {
		select {
		case ch <- ev:
		case <-ctx.Done():
		}
	}
}

// onStage applies a stage event to its anchor's run.
func (a *App) onStage(msg stageMsg) tea.Cmd {
	next := waitStage(msg.seq, msg.ch)
	if a.job == nil || a.job.seq != msg.seq {
		return next // a cancelled job's late event
	}
	r := a.side.run(msg.ev.Anchor)
	if r.done {
		return next
	}
	st := msg.ev.Stage
	if st.Outcome == proposal.OutcomeRunning {
		r.current, r.since = &st, a.now()
		return next
	}
	r.current = nil
	r.stages = append(r.stages, st)
	return next
}

// run returns the run of anchor, adding it when it is new.
func (s *sidebar) run(anchor string) *cascadeRun {
	for _, r := range s.runs {
		if r.anchor == anchor {
			return r
		}
	}
	r := &cascadeRun{anchor: anchor}
	s.runs = append(s.runs, r)
	return r
}

// finishRuns settles the runs once the job is done: the recorded stages
// replace the streamed ones (events may arrive after the result), a
// cleared edit keeps its run, and a filed check drops it (its proposal's
// group lists the stages).
func (a *App) finishRuns(filed, cleared []*proposal.Proposal, failed string) {
	keep := a.side.runs[:0]
	for _, r := range a.side.runs {
		r.done, r.current = true, nil
		gone := false
		for _, p := range filed {
			if p.Primary != nil && p.Primary.Name == r.anchor {
				gone = true
			}
		}
		for _, p := range cleared {
			if p.Primary != nil && p.Primary.Name == r.anchor && p.Inference != nil {
				r.stages, r.cleared = p.Inference.Stages, p.Inference.ClearedBy
			}
		}
		if failed != "" && r.cleared == "" {
			r.failed = failed
		}
		if !gone {
			keep = append(keep, r)
		}
	}
	a.side.runs = keep
}

// runLine is a stage as the sidebar and the status line show it: a
// finished stage's line, or the running one with its time so far (now
// minus since).
func runLine(st proposal.Stage, since, now time.Time, themes ...*Theme) string {
	th := uiTheme(themes)
	if st.Outcome != proposal.OutcomeRunning {
		return workflow.StageLine(st)
	}
	head := st.Name
	if who := workflow.StageWho(st); who != "" {
		head += " " + who
	}
	return fmt.Sprintf(th.uiText("%s running %s…"), head, workflow.Seconds(now.Sub(since)))
}

// activeRun is the run the running job is on (nil when none).
func (a *App) activeRun() *cascadeRun {
	if a.job == nil {
		return nil
	}
	for k := len(a.side.runs) - 1; k >= 0; k-- {
		if r := a.side.runs[k]; !r.done {
			if r.current != nil || k == len(a.side.runs)-1 {
				return r
			}
		}
	}
	return nil
}

// runBox renders compact human-facing activity for a check. Provider and
// inference stages stay in the run detail, not in the ordinary review list.
func (a *App) runBox(r *cascadeRun, inner int) []string {
	th := a.theme
	state := "Checking"
	switch {
	case r.failed != "":
		state = "Check failed"
	case r.cleared != "":
		state = "Check complete"
	case r.done:
		state = "Checked"
	case r.current != nil:
		state += a.theme.uiText(" · ") + workflow.Seconds(a.now().Sub(r.since))
	}
	body := []string{state, th.PopupDim.Render(cardValue(r.anchor, inner, th))}
	add := func(text string, style lipgloss.Style) {
		for _, l := range wrapText(termtext.Clean(text), inner) {
			body = append(body, style.Render(l))
		}
	}
	if r.cleared != "" {
		for _, st := range r.stages {
			if st.Name == r.cleared {
				add(workflow.ClearedText(st), th.Accepted)
				break
			}
		}
	}
	if r.failed != "" && r.cleared == "" {
		add(r.failed, th.Stale)
	}
	if a.height > 36 {
		body = append([]string{""}, body...)
	}
	if a.height >= 30 {
		body = append(body, "")
	}
	return filledCard(body, inner, th.Human, th, false, false)
}

// ---- cleared checks ----

// bodyHash identifies an anchor body's text in the sidebar state.
func bodyHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// noteCleared remembers that the cascade cleared the text of each of ps,
// so the commit flow counts it as checked.
func (a *App) noteCleared(ps []*proposal.Proposal) {
	if len(ps) == 0 {
		return
	}
	st := &a.side.state
	if st.Cleared == nil {
		st.Cleared = map[string]string{}
	}
	for _, p := range ps {
		if p.Primary != nil {
			st.Cleared[p.Primary.Name] = bodyHash(p.Primary.NewText)
		}
	}
	a.saveChecks()
}

// clearedText reports whether the cascade cleared anchor name ("$Name")
// with this text.
func (a *App) clearedText(name, text string) bool {
	h, ok := a.side.state.Cleared[name]
	return ok && h == bodyHash(text)
}

// changedAnchor reports whether anchor name ("$Name") differs from HEAD;
// true too while that is not known yet (no spec or baseline loaded).
func (a *App) changedAnchor(name string) bool {
	if a.ws.Spec == nil || a.ws.Base == nil {
		return true
	}
	for _, an := range a.ws.Spec.Lookup(strings.TrimPrefix(name, "$")) {
		if a.changeState(an) != "" {
			return true
		}
	}
	return false
}
