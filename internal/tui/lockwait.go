// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

// A decision on a proposal (applying it, rejecting it, keeping an edited
// hunk, recording a check as done) changes its status under .spec/lock,
// which another spec process may hold for seconds: `spec approve
// --commit` holds it while git runs the hooks. The UI never waits for the
// lock on its own goroutine. When the lock is free, the decision runs at
// once. When it is held, a background command waits for it
// (workflow.DecisionLock, up to 10 s) while every pane stays live; the
// decision's card or review, and the status line, say what it waits for.
// Once the lock is taken the decision runs on the UI goroutine, against
// the state as it is then, and the lock is released. ^C stops the wait.
//
// An apply that ran after a wait leaves the cursor where it is: Chris
// has likely moved on and is typing elsewhere. It flashes "Applied X: ^]
// to jump" instead, and ^] as the next key, while that flash shows, goes
// to the applied edit (where an apply at once puts the cursor). Any other
// key ends the offer (onKey), as the edit's place may have moved.

// decision is a change of a proposal's status, run with .spec/lock held.
type decision struct {
	// id is the proposal decided (settleAll for the record sweep).
	id string
	// label says what runs, for the flash ("apply 0003").
	label string
	run   func(a *App, lock *project.Lock) tea.Cmd
}

// lockWait is the background wait for .spec/lock and the decisions
// queued behind it, run in order once it is taken.
type lockWait struct {
	seq     int
	started time.Time
	// holder names the process that held the lock when the wait began.
	holder string
	cancel context.CancelFunc
	queue  []decision
}

// lockMsg carries the result of the background wait seq: the lock, held
// now, or why it was not taken.
type lockMsg struct {
	seq  int
	lock *project.Lock
	err  *workflow.Error
}

// lockTickMsg redraws the waiting state of wait seq.
type lockTickMsg struct{ seq int }

// settleAll is the decision id of recording every fully decided check
// as done (settleIfDone).
const settleAll = "(record)"

// underLock runs decision run on proposal id with .spec/lock held: at
// once when the lock is free, else once a background wait takes it. A
// second decision on a proposal that already waits is refused; one on
// another proposal is queued behind the wait.
func (a *App) underLock(id, label string, run func(a *App, lock *project.Lock) tea.Cmd) tea.Cmd {
	d := decision{id: id, label: label, run: run}
	if a.heldLock != nil {
		// A decision made by a decision (an edited hunk, then applied).
		return d.run(a, a.heldLock)
	}
	if w := a.wait; w != nil {
		if a.waitingOn(id) {
			a.setFlash("Already waiting for another spec process (%s) to %s; ^C stops waiting", w.holder, label)
			return nil
		}
		w.queue = append(w.queue, d)
		a.setFlash("Queued: %s runs when another spec process (%s) releases %s; ^C stops waiting", label, w.holder, project.LockFile)
		return nil
	}
	lock, err := project.AcquireLock(a.ws.Root, "spec ui")
	if err == nil {
		return a.runHeld(lock, []decision{d})
	}
	var held *project.LockHeldError
	if !errors.As(err, &held) {
		a.notice("Cannot "+label, workflow.LockErr(err))
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.waitSeq++
	seq := a.waitSeq
	a.wait = &lockWait{seq: seq, started: time.Now(), holder: holderName(held.Record), cancel: cancel, queue: []decision{d}}
	lg.WithField("holder", held.Record.String()).WithField("decision", label).Info("waiting for " + project.LockFile)
	a.setFlash("Waiting for another spec process (%s) to finish: %s runs then; ^C stops waiting", a.wait.holder, label)
	wf, root := a.wf, a.ws.Root
	acquire := func() tea.Msg {
		l, e := wf.DecisionLock(ctx, root, "spec ui")
		return lockMsg{seq: seq, lock: l, err: e}
	}
	return tea.Batch(acquire, lockTick(seq))
}

// runHeld runs ds in order with lock held, then releases it.
func (a *App) runHeld(lock *project.Lock, ds []decision) tea.Cmd {
	a.heldLock = lock
	defer func() {
		a.heldLock = nil
		if err := lock.Release(); err != nil {
			lg.WithError(err).Warn("cannot release " + project.LockFile)
		}
	}()
	var cmds []tea.Cmd
	for _, d := range ds {
		cmds = append(cmds, d.run(a, lock))
	}
	return tea.Batch(cmds...)
}

// onLock ends the wait msg.seq: it runs the queued decisions with the
// lock now held, or says why nothing was decided. A wait stopped by ^C
// meanwhile drops the lock it took.
func (a *App) onLock(msg lockMsg) tea.Cmd {
	w := a.wait
	if w == nil || w.seq != msg.seq {
		if msg.lock != nil {
			_ = msg.lock.Release()
		}
		return nil
	}
	a.wait = nil
	w.cancel()
	lg.WithField("seconds", time.Since(w.started).Seconds()).Info("done waiting for " + project.LockFile)
	if msg.err != nil {
		holder := w.holder
		if msg.err.Lock != nil {
			holder = holderName(msg.err.Lock.Record)
		}
		if msg.err.Code == "lock_held" {
			a.setFlash("Nothing done: another spec process (%s) held %s for %.0f s; %s did not run, try again", holder, project.LockFile, time.Since(w.started).Seconds(), w.labels())
		} else {
			a.notice("Cannot "+w.labels(), msg.err)
		}
		return nil
	}
	a.afterWait, a.appliedAt = true, nil
	defer func() { a.afterWait = false }()
	return a.runHeld(msg.lock, w.queue)
}

// landing is where an apply after a wait put its edit, offered to ^]
// while flash, the flash that says so, shows.
type landing struct {
	loc   Location
	name  string
	flash string
}

// appliedFlash flashes msg after proposal name was applied. After a wait
// the apply did not move the cursor (applyInto): the flash then offers ^]
// instead, followed by failed (what did not apply).
func (a *App) appliedFlash(name, failed, msg string) {
	kind := statusWarning
	prefix := a.theme.uiText("Applied · unsaved · ")
	if failed != "" {
		kind = statusPartial
		prefix = a.theme.uiText("Partly applied · unsaved · ")
	}
	if !a.afterWait || a.appliedAt == nil {
		a.setStatus(kind, "unsaved", "%s%s", prefix, strings.TrimPrefix(msg, "Applied "))
		return
	}
	a.setStatus(kind, "unsaved", "%s%s: ^] to jump%s", prefix, name, failed)
	a.appliedAt.name, a.appliedAt.flash = name, a.flash
}

// jumpToLanded is ^] (dir 1) as the next key after an apply after a
// wait: it goes to the applied edit and reports true.
func (a *App) jumpToLanded(dir int) bool {
	l := a.appliedAt
	if l == nil || dir < 0 || l.flash == "" || a.flash != l.flash {
		return false
	}
	a.jump(l.loc)
	a.setFlash("At the applied %s (^] goes on to the next edit)", l.name)
	return true
}

// failedText says which hunks did not apply ("; hunk 3 failed: ...").
func failedText(fs []proposal.Failure) string {
	var out string
	for _, f := range fs {
		out += fmt.Sprintf("; hunk %d failed: %s", f.N, f.Reason)
	}
	return out
}

// proposalName names p in a flash: the anchor it changes, else its id.
func proposalName(p *proposal.Proposal) string {
	if p.Primary != nil && p.Primary.Name != "" {
		return p.Primary.Name
	}
	return p.ID
}

// cancelWait stops the wait for .spec/lock, if any: the queued decisions
// do not run. It reports whether there was one.
func (a *App) cancelWait() bool {
	w := a.wait
	if w == nil {
		return false
	}
	lg.WithField("decisions", w.labels()).Info("stopped waiting for " + project.LockFile)
	w.cancel()
	a.wait = nil
	a.setFlash("Stopped waiting: %s did not run", w.labels())
	return true
}

// waitingOn reports whether a decision on proposal id waits for the lock.
func (a *App) waitingOn(id string) bool {
	if a.wait == nil {
		return false
	}
	for _, d := range a.wait.queue {
		if d.id == id {
			return true
		}
	}
	return false
}

// refuseWhileWaiting flashes and reports true when a decision on proposal
// id already waits for the lock (a second one would decide it twice).
func (a *App) refuseWhileWaiting(id string) bool {
	if !a.waitingOn(id) {
		return false
	}
	a.setFlash("%s already waits for another spec process (%s) to finish; ^C stops waiting", id, a.wait.holder)
	return true
}

// labels lists the queued decisions.
func (w *lockWait) labels() string {
	var out []string
	for _, d := range w.queue {
		out = append(out, d.label)
	}
	return strings.Join(out, ", ")
}

// waitText is the waiting state of a card or a review: what holds the
// lock and for how long it has been waited for.
func (a *App) waitText() string {
	w := a.wait
	if w == nil {
		return ""
	}
	el := time.Since(w.started)
	return fmt.Sprintf(a.theme.uiText("%s waiting for another spec process (%s)… %.0f s; ^C stops waiting"), spinFrame(el, a.theme), w.holder, el.Seconds())
}

// waitStatus is the waiting state in the status line (shorter).
func (a *App) waitStatus() string {
	w := a.wait
	if w == nil {
		return ""
	}
	el := time.Since(w.started)
	return fmt.Sprintf(a.theme.uiText("%s waiting %.0f s for %s · ^C stops"), spinFrame(el, a.theme), el.Seconds(), w.holder)
}

// spinFrame is the spinner's frame after el.
func spinFrame(el time.Duration, themes ...*Theme) string {
	frames := spinFrames
	if th := uiTheme(themes); th != nil && th.ASCII {
		frames = []string{"|", "/", "-", "\\"}
	}
	return frames[int(el/spinEvery)%len(frames)]
}

// lockTick schedules the next redraw of wait seq.
func lockTick(seq int) tea.Cmd {
	return tea.Tick(spinEvery, func(time.Time) tea.Msg { return lockTickMsg{seq: seq} })
}

// holderName names the process that holds the lock: its command line
// ("spec approve 0003"), else its pid.
func holderName(r project.LockRecord) string {
	switch {
	case r.Command != "":
		return r.Command
	case r.PID != 0:
		return fmt.Sprintf("pid %d", r.PID)
	}
	return "unknown"
}
