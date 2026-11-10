// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// spinFrames are the frames of the busy spinner in the status line.
var spinFrames = []string{"▖", "▘", "▝", "▗"}

// spinEvery is how often the spinner turns.
const spinEvery = 100 * time.Millisecond

// job is one background operation. Model work is cancellable; a dispatched
// commit retains its result until completion.
type job struct {
	seq     int
	label   string
	started time.Time
	cancel  context.CancelFunc
	// irreversible jobs retain their completion even when cancellation is requested.
	irreversible bool
}

// spinMsg turns the spinner of job seq.
type spinMsg struct{ seq int }

// jobDoneMsg carries a finished job's result: finish runs on the UI
// goroutine and returns the next command.
type jobDoneMsg struct {
	seq    int
	finish func(a *App) tea.Cmd
}

// startJob runs work in the background with a spinner labelled label. work
// gets a context cancelled by ^C and returns what to do with its result on
// the UI goroutine.
func (a *App) startJob(label string, work func(ctx context.Context) func(a *App) tea.Cmd) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	a.jobSeq++
	seq := a.jobSeq
	a.job = &job{seq: seq, label: label, started: a.now(), cancel: cancel}
	lg.WithField("job", label).Info("started")
	run := func() tea.Msg {
		return jobDoneMsg{seq: seq, finish: work(ctx)}
	}
	return tea.Batch(run, spinTick(seq))
}

// spinTick schedules the next spinner frame of job seq.
func spinTick(seq int) tea.Cmd {
	return tea.Tick(spinEvery, func(time.Time) tea.Msg { return spinMsg{seq: seq} })
}

// onJobDone applies a finished job's result, unless it was cancelled.
func (a *App) onJobDone(msg jobDoneMsg) tea.Cmd {
	if a.job == nil || a.job.seq != msg.seq {
		return nil // cancelled: drop the result
	}
	lg.WithField("job", a.job.label).WithField("seconds", time.Since(a.job.started).Seconds()).Info("finished")
	a.job.cancel()
	a.job = nil
	if msg.finish == nil {
		return nil
	}
	return msg.finish(a)
}

// cancelJob stops the running job, if any, and reports whether there was
// one.
func (a *App) cancelJob() bool {
	if a.job == nil {
		return false
	}
	if a.job.irreversible {
		a.setFlash("%s is running; it cannot be cancelled safely", a.job.label)
		return true
	}
	lg.WithField("job", a.job.label).Info("cancelled")
	a.job.cancel()
	a.setFlash("Cancelled: %s", strings.ToLower(a.job.label[:1])+a.job.label[1:])
	a.job = nil
	a.side.runs = nil
	return true
}

// busy says a job is running and flashes so when one is.
func (a *App) busy() bool {
	if a.job == nil {
		return false
	}
	if a.job.irreversible {
		a.setFlash("%s is still running", a.job.label)
	} else {
		a.setFlash("%s is still running (^C cancels it)", a.job.label)
	}
	return true
}

// jobText is the status line's text for the running job.
func (a *App) jobText() string {
	if a.job == nil {
		return ""
	}
	el := a.now().Sub(a.job.started)
	frames := spinFrames
	if a.theme.ASCII {
		frames = []string{"|", "/", "-", "\\"}
	}
	frame := frames[int(el/spinEvery)%len(frames)]
	label := a.job.label
	if r := a.activeRun(); r != nil {
		label = "Checking " + r.anchor
	}
	text := fmt.Sprintf(a.theme.uiText("%s %s · %.1f s"), frame, label, el.Seconds())
	if !a.job.irreversible {
		text += "  ^C cancels"
	}
	return text
}

// startCommitJob deliberately cannot advertise cancellation after dispatch:
// git hooks and reference updates must finish and their outcome be reconciled.
func (a *App) startCommitJob(work func(context.Context) func(*App) tea.Cmd) tea.Cmd {
	cmd := a.startJob("Committing", work)
	a.job.irreversible = true
	return cmd
}
