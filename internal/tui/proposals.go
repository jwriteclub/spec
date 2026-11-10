// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

// proposalsChangedMsg says .spec/proposals changed: a proposal was filed,
// decided or removed.
type proposalsChangedMsg struct{ err *workflow.Error }

// propWatcher watches .spec/proposals with fsnotify and also polls it (the
// fallback where events are unavailable, as `--wait` does).
type propWatcher struct {
	dir  string
	ch   chan proposalsChangedMsg
	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

// newPropWatcher starts watching the proposals of the project at root.
func newPropWatcher(root string) *propWatcher {
	w := &propWatcher{
		dir: filepath.Join(root, filepath.FromSlash(proposal.Dir)),
		ch:  make(chan proposalsChangedMsg, 1), done: make(chan struct{}),
	}
	var events <-chan fsnotify.Event
	var errs <-chan error
	var fw *fsnotify.Watcher
	if err := os.MkdirAll(w.dir, 0o755); err == nil {
		if f, err := fsnotify.NewWatcher(); err == nil {
			if err := f.Add(w.dir); err == nil {
				fw, events, errs = f, f.Events, f.Errors
			} else {
				_ = f.Close()
			}
		}
	}
	if fw == nil {
		lg.Info("fsnotify unavailable for proposals; polling")
	}
	w.wg.Add(1)
	go w.run(fw, events, errs)
	return w
}

// stampOf summarizes the directory for polling: names, sizes and times.
func (w *propWatcher) stampOf() string {
	ents, err := os.ReadDir(w.dir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s %d %d\n", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
}

func (w *propWatcher) run(fw *fsnotify.Watcher, events <-chan fsnotify.Event, errs <-chan error) {
	defer w.wg.Done()
	if fw != nil {
		defer func() { _ = fw.Close() }()
	}
	last := w.stampOf()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	settle := time.NewTimer(time.Hour)
	settle.Stop()
	for {
		select {
		case <-w.done:
			settle.Stop()
			return
		case _, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			settle.Reset(watchSettle)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			lg.WithError(err).Debug("proposal watcher")
		case <-settle.C:
			last = w.stampOf()
			w.signal()
		case <-tick.C:
			// Owner death produces no fsnotify event. Reconciliation is read/try-lock
			// only for live workers, and runs off the UI goroutine.
			if e := (&workflow.Workflow{}).ReconcileChecks(filepath.Dir(filepath.Dir(w.dir))); e != nil {
				w.signal(e)
			}
			if s := w.stampOf(); s != last {
				last = s
				w.signal()
			}
		}
	}
}

// signal reports a change, coalescing with an undelivered one.
func (w *propWatcher) signal(errs ...*workflow.Error) {
	msg := proposalsChangedMsg{}
	if len(errs) > 0 {
		msg.err = errs[0]
	}
	select {
	case w.ch <- msg:
	default:
	}
}

// next returns a command that waits for the next change.
func (w *propWatcher) next() tea.Cmd {
	if w == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case msg := <-w.ch:
			return msg
		case <-w.done:
			return nil
		}
	}
}

// Close stops the watcher and waits for its goroutine.
func (w *propWatcher) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() { close(w.done) })
	w.wg.Wait()
}

// ---- pending proposals ----

// loadPending reads the pending proposals, newest first, and rebuilds the
// sidebar's cards.
func (a *App) loadPending() {
	if e := a.wf.ReconcileChecks(a.ws.Root); e != nil {
		lg.WithError(e).Warn("cannot recover interrupted checks")
		a.setStatus(statusFailure, "Check recovery", "Check recovery failed: %s", e.Message)
	}
	ps, err := a.wf.Store(a.ws.Root).List()
	if err != nil {
		lg.WithError(err).Warn("cannot list proposals")
		return
	}
	previous := map[string]*proposal.Proposal{}
	for _, p := range a.pending {
		previous[p.ID] = p
	}
	if a.side.state.Generations == nil {
		a.side.state.Generations = map[string]int{}
	}
	var out []*proposal.Proposal
	for _, p := range ps {
		if p.Status == proposal.StatusPending {
			if a.side.state.Generations[p.ID] != p.CheckGeneration {
				delete(a.side.state.Decided, p.ID)
			}
			a.side.state.Generations[p.ID] = p.CheckGeneration
			if old := previous[p.ID]; old != nil {
				if old.CheckGeneration != p.CheckGeneration {
					delete(a.side.state.Decided, p.ID)
				}
				for _, n := range workflow.Changed(old, p, nil) {
					if p.AppliedHunk(n) {
						continue
					}
					if p.Primary != nil && p.Primary.N == n {
						delete(a.side.state.Decided, p.ID)
						break
					}
					delete(a.side.state.Decided[p.ID], fmt.Sprintf("h%d", n))
					delete(a.side.state.Decided[p.ID], fmt.Sprintf("p%d", n))
				}
			}
			if a.withBuffered(p) {
				continue
			}
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].ID > out[j].ID
	})
	a.pending = out
	a.bufferedShown = a.bufferedShape()
	a.rebuildCards()
}

// onProposalsChanged refreshes the badge, the sidebar and an open review.
// An agent's new proposal gets a card without taking the keyboard: the
// sidebar retains it without opening, and a brief receipt names the arrival.
func (a *App) onProposalsChanged() {
	before := map[string]bool{}
	for _, p := range a.pending {
		before[p.ID] = true
	}
	a.loadPending()
	var arrived, agents []*proposal.Proposal
	for _, p := range a.pending {
		if before[p.ID] {
			continue
		}
		arrived = append(arrived, p)
		if p.SourceKind == "agent" {
			agents = append(agents, p)
		}
	}
	for _, s := range a.screens {
		if r, ok := s.(*reviewScreen); ok {
			r.onStoreChanged(a)
		}
	}
	switch {
	case len(agents) > 0:
		p := agents[0]
		msg := "Agent proposal"
		if p.Author != "" {
			msg += " from " + p.Author
		}
		if p.Primary != nil {
			msg += " for " + p.Primary.Name
		}
		if a.now().Before(a.arrivalUntil) {
			a.arrivalCount += len(agents)
		} else {
			a.arrivalCount = len(agents)
		}
		a.arrivalUntil = a.now().Add(time.Second)
		accent := "Agent proposal"
		if a.arrivalCount > 1 {
			msg = fmt.Sprintf("%d proposals arrived", a.arrivalCount)
			accent = "proposals"
		}
		a.setStatus(statusAgent, accent, "%s", msg)
		a.agentFlash = a.flash
	case len(arrived) > 0 && a.flash == "":
		a.setFlash("%s arrived in Review", plural(len(arrived), "proposal"))
	}

}

// agentProposals counts the cards of agents' proposals.
func (a *App) agentProposals() int {
	n := 0
	for _, c := range a.side.cards {
		if c.kind == cardProposal {
			n++
		}
	}
	return n
}

// badge is the status line's count of the sidebar's cards: agents'
// proposals first, then the checks' findings and edits.
func (a *App) badge() string {
	n := a.agentProposals()
	var parts []string
	if n > 0 {
		parts = append(parts, plural(n, "proposal"))
	}
	m := 0
	for _, c := range a.side.cards {
		if c.kind != cardProposal && a.openCard(c) {
			m++
		}
	}
	if m > 0 {
		parts = append(parts, plural(m, "check"))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
