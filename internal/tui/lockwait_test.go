// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// A decision in the UI waits for .spec/lock when another spec process
// holds it (`spec approve --commit` running slow git hooks), but never on
// the UI goroutine: the editor stays live and the card says it waits.

// lockedApp opens the UI on dir, with an agent's proposal pending and
// .spec/lock held by "another process" (an approval of it). The UI's
// lock wait is wait.
func lockedApp(t *testing.T, wait time.Duration) (*App, *proposal.Proposal, *project.Lock) {
	t.Helper()
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	held, err := project.AcquireLock(dir, "spec approve "+p.ID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Release() })
	wf := testWorkflow(t, nil)
	wf.LockWait = wait
	a := newTestApp(t, uiOptions(dir, wf))
	t.Cleanup(a.Close)
	return a, p, held
}

// update runs one message through Update and returns its command; it
// fails the test when Update does not return promptly (blocked on the
// lock).
func update(t *testing.T, a *App, msg tea.Msg) tea.Cmd {
	t.Helper()
	got := make(chan tea.Cmd, 1)
	go func() {
		_, cmd := a.Update(msg)
		got <- cmd
	}()
	select {
	case cmd := <-got:
		return cmd
	case <-time.After(2 * time.Second):
		t.Fatal("Update blocked: the UI froze waiting for .spec/lock")
		return nil
	}
}

// lockResult runs cmd (and the commands it batches) and returns the
// background wait's result, the lockMsg; other messages (the redraw
// ticks) are dropped.
func lockResult(t *testing.T, cmd tea.Cmd) lockMsg {
	t.Helper()
	got := make(chan lockMsg, 1)
	var run func(c tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch m := c().(type) {
		case tea.BatchMsg:
			for _, sub := range m {
				go run(sub)
			}
		case lockMsg:
			got <- m
		}
	}
	go run(cmd)
	select {
	case m := <-got:
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("the background wait for .spec/lock never finished")
		return lockMsg{}
	}
}

// buffer is the text of path in the editor.
func buffer(a *App, path string) string {
	return string(a.ws.Docs()[path].Ed.Buffer().Bytes())
}

// Approving an agent's proposal while an approval elsewhere holds the
// lock: Update returns at once, the card and the status line say what it
// waits for, the editor takes typing, and the decision lands once the
// lock is free.
func TestDecisionWaitsForLockInBackground(t *testing.T) {
	a, p, held := lockedApp(t, time.Minute)
	send(a, key("ctrl+r"))
	require.True(t, a.sideFocus)
	cmd := update(t, a, key("y"))
	require.NotNil(t, cmd, "the wait runs as a command")
	require.True(t, a.waitingOn(p.ID))
	assert.Contains(t, a.flash, "Waiting for another spec process (spec approve "+p.ID+") to finish: apply "+p.ID+" runs then; ^C stops waiting")
	a.flash = ""
	frame := plain(a)
	assert.Contains(t, frame, "waiting for another spec process", "the card says it waits")
	assert.Contains(t, a.waitStatus(), "waiting 0 s for spec approve "+p.ID+" · ^C stops")
	assert.Contains(t, frame, "waiting", "the bounded status line retains wait activity")

	// A second decision on it is refused, not run twice.
	update(t, a, key("y"))
	assert.Contains(t, a.flash, p.ID+" already waits for another spec process")
	require.Len(t, a.wait.queue, 1)

	// The editor stays live.
	send(a, key("esc"), key("ctrl+end"))
	require.Equal(t, "demo.spec.md", a.cur.Ed.Path())
	send(a, typeText("Tail.")...)
	assert.Equal(t, demoSpec+"Tail.", buffer(a, "demo.spec.md"), "typing works while the decision waits")
	stored, err := proposal.NewStore(a.ws.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, stored.Status, "nothing decided yet")

	// The other process finishes: the decision lands, over the typing,
	// and leaves the cursor where Chris is typing.
	require.NoError(t, held.Release())
	update(t, a, lockResult(t, cmd))
	assert.Nil(t, a.wait)
	stored, err = proposal.NewStore(a.ws.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, stored.Status, "not recorded until the save")
	want := strings.Replace(strings.Replace(demoSpec, timingOld, timingNew, 1), proseFind, proseReplace, 1) + "Tail."
	assert.Equal(t, want, buffer(a, "demo.spec.md"))
	assert.Equal(t, "Applied · unsaved · $Frame.Timing: ^] to jump", a.flash)
	assert.Equal(t, Location{Path: "demo.spec.md", Off: len(want)}, a.here(), "the cursor stays after the typing")
	saveBuffers(t, a)
	stored, err = proposal.NewStore(a.ws.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, stored.Status)
	assert.Equal(t, []int{1, 2}, stored.Decision.Applied)
	assert.Empty(t, a.side.cards)
	l, err := project.AcquireLock(a.ws.Root, "test")
	require.NoError(t, err, "the UI released the lock")
	require.NoError(t, l.Release())
}

// The approval that held the lock applied the proposal: the UI's decision
// is refused with a message that says so, and changes nothing.
func TestDecisionAfterWaitNotPending(t *testing.T) {
	a, p, held := lockedApp(t, time.Minute)
	send(a, key("ctrl+r"))
	cmd := update(t, a, key("y"))
	require.True(t, a.waitingOn(p.ID))

	// spec approve records it applied (under the lock it holds).
	st := proposal.NewStore(a.ws.Root)
	ap, err := st.Load(p.ID)
	require.NoError(t, err)
	ap.Status = proposal.StatusApplied
	ap.Decision = &proposal.Decision{Applied: []int{1, 2}, By: "agent-7", At: time.Now()}
	require.NoError(t, st.Save(ap))
	require.NoError(t, held.Release())

	update(t, a, lockResult(t, cmd))
	assert.Equal(t, "Not applied: "+p.ID+" was already applied by another spec process; nothing changed here", a.flash)
	assert.Equal(t, demoSpec, buffer(a, "demo.spec.md"), "nothing applied twice")
	assert.Empty(t, a.pending)
	got, err := st.Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, "agent-7", got.Decision.By, "the approval's record stands")
}

// A rejection from the review waits too; the approval meanwhile wins and
// the rejection says so.
func TestRejectAfterWaitNotPending(t *testing.T) {
	a, p, held := lockedApp(t, time.Minute)
	send(a, key("ctrl+r"), key("enter"))
	require.NotNil(t, reviewOf(a))
	send(a, key("R"))
	send(a, typeText("no")...)
	cmd := update(t, a, key("enter"))
	require.True(t, a.waitingOn(p.ID))
	a.flash = ""
	assert.Contains(t, plain(a), "waiting for another spec process (spec approve "+p.ID+")", "the review says it waits")

	st := proposal.NewStore(a.ws.Root)
	ap, err := st.Load(p.ID)
	require.NoError(t, err)
	ap.Status = proposal.StatusApplied
	ap.Decision = &proposal.Decision{Applied: []int{1, 2}, By: "agent-7", At: time.Now()}
	require.NoError(t, st.Save(ap))
	require.NoError(t, held.Release())

	update(t, a, lockResult(t, cmd))
	assert.Contains(t, a.flash, "Not rejected: proposal "+p.ID+" is already applied (decided by another spec process meanwhile)")
	got, err := st.Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, got.Status)
}

// The lock stays held past the wait: nothing is decided and the flash
// says who held it.
func TestDecisionWaitTimesOut(t *testing.T) {
	a, p, _ := lockedApp(t, time.Millisecond)
	send(a, key("ctrl+r"))
	cmd := update(t, a, key("y"))
	update(t, a, lockResult(t, cmd))
	assert.Nil(t, a.wait)
	assert.Contains(t, a.flash, "Nothing done: another spec process (spec approve "+p.ID+") held .spec/lock")
	assert.Contains(t, a.flash, "apply "+p.ID+" did not run, try again")
	got, err := proposal.NewStore(a.ws.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, got.Status)
	assert.Equal(t, demoSpec, buffer(a, "demo.spec.md"))
}

// ^C stops the wait: the decision does not run when the lock comes free
// later, and the UI holds no lock.
func TestCtrlCStopsLockWait(t *testing.T) {
	a, p, held := lockedApp(t, time.Minute)
	send(a, key("ctrl+r"))
	cmd := update(t, a, key("y"))
	require.True(t, a.waitingOn(p.ID))
	send(a, key("ctrl+c"))
	assert.Nil(t, a.wait)
	assert.Equal(t, "Stopped waiting: apply "+p.ID+" did not run", a.flash)

	require.NoError(t, held.Release())
	m := lockResult(t, cmd)
	update(t, a, m)
	got, err := proposal.NewStore(a.ws.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, got.Status)
	assert.Equal(t, demoSpec, buffer(a, "demo.spec.md"))
	l, err := project.AcquireLock(a.ws.Root, "test")
	require.NoError(t, err, "the UI holds no lock")
	require.NoError(t, l.Release())
}

// Without contention nothing changes: the decision runs within the key.
func TestDecisionWithFreeLockRunsAtOnce(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	send(a, key("ctrl+r"), key("y"))
	assert.Nil(t, a.wait)
	saveBuffers(t, a)
	got, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, got.Status)
}

// An edited hunk kept while the lock is held: the small editor closes at
// once, the review says it waits, and the edit lands once the lock is
// free.
func TestHunkEditWaitsForLock(t *testing.T) {
	a, p, held := lockedApp(t, time.Minute)
	send(a, key("ctrl+r"), key("enter"))
	r := reviewOf(a)
	require.NotNil(t, r)
	require.Equal(t, 2, r.selected())
	send(a, key("e"))
	send(a, typeText(" Edited.")...)
	cmd := update(t, a, key("ctrl+o"))
	require.True(t, a.waitingOn(p.ID))
	assert.Same(t, r, reviewOf(a), "the editor closed; the review waits")

	require.NoError(t, held.Release())
	update(t, a, lockResult(t, cmd))
	assert.Equal(t, proseReplace+" Edited.", r.p.Hunks[0].Replace)
	assert.Equal(t, hunkAccepted, r.state[2])
	assert.Equal(t, "Hunk 2 edited and accepted", a.flash)
	got, err := proposal.NewStore(a.ws.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proseReplace+" Edited.", got.Hunks[0].Replace)
}

// An apply that lands after a wait does not move the cursor away from
// where Chris typed meanwhile; ^] pressed right after goes to the applied
// edit, where an apply at once puts the cursor.
func TestApplyAfterWaitKeepsCursor(t *testing.T) {
	a, p, held := lockedApp(t, time.Minute)
	send(a, key("ctrl+r"))
	cmd := update(t, a, key("y"))
	require.True(t, a.waitingOn(p.ID))
	send(a, key("esc"), key("ctrl+end"))
	send(a, typeText("Tail.")...)
	require.Equal(t, Location{Path: "demo.spec.md", Off: len(demoSpec + "Tail.")}, a.here())

	require.NoError(t, held.Release())
	update(t, a, lockResult(t, cmd))
	applied := strings.Replace(strings.Replace(demoSpec, timingOld, timingNew, 1), proseFind, proseReplace, 1) + "Tail."
	require.Equal(t, applied, buffer(a, "demo.spec.md"))
	assert.Equal(t, Location{Path: "demo.spec.md", Off: len(applied)}, a.here(), "the cursor did not move")
	assert.Equal(t, "Applied · unsaved · $Frame.Timing: ^] to jump", a.flash)

	send(a, key("ctrl+]"))
	assert.Equal(t, appliedAt(), a.here(), "^] goes to the applied edit")
	assert.Equal(t, "At the applied $Frame.Timing (^] goes on to the next edit)", a.flash)
}

// ^] after another key is the usual next edit: the offer lasts one key
// (the key may have moved the edit's place).
func TestApplyAfterWaitOfferLastsOneKey(t *testing.T) {
	a, p, held := lockedApp(t, time.Minute)
	send(a, key("ctrl+r"))
	cmd := update(t, a, key("y"))
	require.True(t, a.waitingOn(p.ID))
	send(a, key("esc"), key("ctrl+end"))
	require.NoError(t, held.Release())
	update(t, a, lockResult(t, cmd))
	require.Equal(t, "Applied · unsaved · $Frame.Timing: ^] to jump", a.flash)
	send(a, key("ctrl+home"))
	assert.Nil(t, a.appliedAt, "the next key ends the offer")
	send(a, key("ctrl+]"))
	assert.NotContains(t, a.flash, "At the applied")
}

// appliedAt is where applying agentProposal puts the cursor: its first
// edit, the $Frame.Timing text.
func appliedAt() Location {
	return Location{Path: "demo.spec.md", Off: strings.Index(demoSpec, timingOld)}
}

// An apply at once (the lock free) moves the cursor to the applied edit,
// as it always has.
func TestApplyAtOnceMovesCursor(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	send(a, key("ctrl+end"))
	send(a, key("ctrl+r"), key("y"))
	assert.Nil(t, a.wait)
	assert.Nil(t, a.appliedAt)
	assert.Equal(t, appliedAt(), a.here())
	assert.Contains(t, a.flash, "Applied · unsaved · "+p.ID+" by agent-7 into the buffer")
}

// A suggested edit edited on its card and kept after a wait: applied
// without moving the cursor, and ^] goes to it.
func TestEditCardAfterWaitKeepsCursor(t *testing.T) {
	dir := demoProject(t)
	fixedProposals(t, dir)
	held, err := project.AcquireLock(dir, "spec approve k3x7a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Release() })
	wf := testWorkflow(t, nil)
	wf.LockWait = time.Minute
	a := newTestApp(t, uiOptions(dir, wf))
	t.Cleanup(a.Close)

	send(a, key("ctrl+r"), key("down"), key("e"))
	require.Equal(t, CtxHunkEdit, a.context())
	send(a, key("backspace"), key("backspace"), key("backspace"), key("backspace"), key("backspace"))
	send(a, typeText("ITEMS")...)
	cmd := update(t, a, key("ctrl+o"))
	require.True(t, a.waitingOn("m7qd2"))
	send(a, key("esc"))
	require.NoError(t, a.open(Location{Path: "demo.spec.md"}))
	send(a, key("ctrl+end"))
	send(a, typeText("Tail.")...)
	here := a.here()
	require.Equal(t, "demo.spec.md", here.Path)

	require.NoError(t, held.Release())
	update(t, a, lockResult(t, cmd))
	assert.Equal(t, strings.Replace(otherSpec, limitFind, "MUST hold at most 5 ITEMS", 1), buffer(a, "other.spec.md"))
	assert.Equal(t, here, a.here(), "the cursor did not move")
	assert.Equal(t, "Applied · unsaved · $Extra.Limit: ^] to jump", a.flash)

	send(a, key("ctrl+]"))
	assert.Equal(t, Location{Path: "other.spec.md", Off: strings.Index(otherSpec, limitFind)}, a.here())
}
