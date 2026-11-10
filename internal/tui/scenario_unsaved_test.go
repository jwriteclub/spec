// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"io"
	"strings"
	"testing"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/cli"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// An approval edits the buffer; only the save makes it true. These drive
// `^R y` and what can follow it, and read what an agent reads: the store
// and `spec proposal ID --wait`.

// waitExitPending is what `spec proposal --wait` exits with while the
// proposal is undecided; waitExitApplied once it is applied.
const (
	waitExitPending = 2
	waitExitApplied = 0
)

// unsaved opens the demo with agent-7's proposal to bound $Frame.Timing
// (one file, no fixes) and approves it from the sidebar.
func unsaved(t *testing.T, opts ...Opt) *Scenario {
	t.Helper()
	opts = append([]Opt{Agents(agentTiming), Replies(infer.CannedFile{Check: []infer.CannedEntry{cleanReply}})}, opts...)
	s := Open(t, scenes.Demo, opts...)
	s.Press("ctrl+r")
	s.ExpectCards("proposal")
	s.Press("y")
	s.Idle()
	s.ExpectBuffer(atMost5s)
	s.ExpectDirty(true)
	s.ExpectFlash(`\^O saves; nothing is committed`)
	s.ExpectCards()
	return s
}

// status is the only proposal's status in the store.
func status(s *Scenario) string {
	s.t.Helper()
	ps := s.Proposals()
	if len(ps) != 1 {
		s.fail("want one proposal, got %d", len(ps))
	}
	return ps[0].Status
}

// expectPending says the store has not been told anything that a save has
// not made true: an agent still reads pending, and the file is as it was.
func expectPending(s *Scenario) {
	s.t.Helper()
	if got := status(s); got != proposal.StatusPending {
		s.fail("want the proposal pending, got %s", got)
	}
	if d := s.Proposals()[0].Decision; d != nil && len(d.Applied) > 0 {
		s.fail("want no applied receipt, got %v", d.Applied)
	}
	out, code := s.ShellCode("spec", "proposal", s.Proposals()[0].ID, "--wait", "20ms")
	if code != waitExitPending {
		s.fail("a waiter must keep waiting (exit %d), got exit %d:\n%s", waitExitPending, code, out)
	}
}

// expectApplied says the file holds the change and a waiter gets exit 0.
func expectApplied(s *Scenario) {
	s.t.Helper()
	if !strings.Contains(s.ReadFile("demo.spec.md"), atMost5s) {
		s.fail("the file does not hold the change")
	}
	if got := status(s); got != proposal.StatusApplied {
		s.fail("want the proposal applied, got %s", got)
	}
	out, code := s.ShellCode("spec", "proposal", s.Proposals()[0].ID, "--wait", "20ms")
	if code != waitExitApplied {
		s.fail("want a waiter released with exit %d, got %d:\n%s", waitExitApplied, code, out)
	}
}

// y then ^X n: the buffer is thrown away, so nothing was applied. The
// proposal is still pending, and after a restart it can be approved again.
func TestScenarioApproveThenDiscardStaysPending(t *testing.T) {
	s := unsaved(t, NoLayoutCheck())
	expectPending(s)
	s.Press("ctrl+x")
	s.ExpectContext(tui.CtxConfirm)
	s.Press("n")
	if strings.Contains(s.ReadFile("demo.spec.md"), atMost5s) {
		s.fail("the discarded change reached the file")
	}
	expectPending(s)
	s.Reopen()
	s.Press("ctrl+r")
	s.ExpectCards("proposal")
	s.Press("y")
	s.Idle()
	s.Press("ctrl+o")
	expectApplied(s)
}

// A waiter started before the save is released by it, and not earlier.
func TestScenarioWaitSucceedsOnlyAtSave(t *testing.T) {
	s := unsaved(t)
	expectPending(s)
	id := s.Proposals()[0].ID
	code := make(chan int, 1)
	go func() {
		code <- cli.Run([]string{"proposal", id, "--wait", "9s"}, cli.Env{
			Stdout: io.Discard, Stderr: io.Discard, Cwd: s.sess.Dir, Getenv: s.sess.Getenv,
			Git: baseline.Loader{Env: scenes.GitEnv()},
		})
	}()
	s.Press("ctrl+o")
	if got := <-code; got != waitExitApplied {
		s.fail("want the waiter released with exit %d by the save, got %d", waitExitApplied, got)
	}
	s.ExpectDirty(false)
	if !strings.Contains(s.ReadFile("demo.spec.md"), atMost5s) {
		s.fail("the waiter was released without the change on disk")
	}
}

// ^O from the sidebar, where the approval leaves the keyboard, saves.
func TestScenarioSaveFromSidebar(t *testing.T) {
	s := unsaved(t)
	s.ExpectFocus("sidebar")
	s.Press("ctrl+o")
	s.ExpectFocus("sidebar")
	s.ExpectDirty(false)
	s.ExpectFlash(`^Saved demo\.spec\.md`)
	expectApplied(s)
}

// ^Z of the approval takes the decision back: the card returns, a save
// records nothing, and y works again.
func TestScenarioUndoApprovalStaysPending(t *testing.T) {
	s := unsaved(t)
	s.Press("esc", "ctrl+z")
	s.ExpectNoBuffer(atMost5s)
	s.Idle()
	s.WaitUntil("the card is back", func(st tui.State) bool { return len(st.CardsOf("proposal")) == 1 })
	s.Press("ctrl+o")
	expectPending(s)
	s.Press("ctrl+r")
	s.ExpectCards("proposal")
	s.Press("y")
	s.Idle()
	s.ExpectBuffer(atMost5s)
	s.Press("ctrl+o")
	expectApplied(s)
}

// ^Y after ^Z makes the approval true again: the save records it.
func TestScenarioRedoApprovalAppliesAtSave(t *testing.T) {
	s := unsaved(t)
	s.Press("esc", "ctrl+z")
	s.WaitUntil("the card is back", func(st tui.State) bool { return len(st.CardsOf("proposal")) == 1 })
	s.Press("ctrl+y")
	s.ExpectBuffer(atMost5s)
	s.WaitUntil("the card is gone", func(st tui.State) bool { return len(st.CardsOf("proposal")) == 0 })
	expectPending(s)
	s.Press("ctrl+o")
	expectApplied(s)
}

// Reworking the approved text before the save is still an approval: what
// the save writes is the change with Chris's edit on top.
func TestScenarioEditAfterApprovalAppliesAtSave(t *testing.T) {
	s := unsaved(t)
	s.Press("esc")
	s.Replace("Prose that mentions", "Text that mentions")
	s.Press("ctrl+o")
	expectApplied(s)
	s.ExpectBuffer("Text that mentions")
}

// A failed save records nothing, and the next good one does.
func TestScenarioSaveFailureStaysPending(t *testing.T) {
	s := unsaved(t)
	lock, err := project.AcquireLock(s.Dir(), "another spec process")
	if err != nil {
		s.fail("cannot hold the lock: %v", err)
	}
	s.Press("ctrl+o")
	s.ExpectDirty(true)
	s.ExpectFlash(`Save failed`)
	if err := lock.Release(); err != nil {
		s.fail("cannot release the lock: %v", err)
	}
	expectPending(s)
	s.Press("ctrl+o")
	expectApplied(s)
}

// The file changes while the approval is unsaved. Taking theirs drops the
// buffer and with it the approval; the proposal is pending.
func TestScenarioExternalChangeTakeTheirsStaysPending(t *testing.T) {
	s := unsaved(t)
	disk := s.ReadFile("demo.spec.md")
	s.WriteFile("demo.spec.md", disk+"\nAn external line.\n", true)
	s.Press("ctrl+o")
	s.ExpectContext(tui.CtxConflict)
	s.Press("t")
	s.ExpectDirty(false)
	s.ExpectNoBuffer(atMost5s)
	s.WaitUntil("the card is back", func(st tui.State) bool { return len(st.CardsOf("proposal")) == 1 })
	expectPending(s)
}

// Keeping mine after an external change overwrites the file with the
// buffer: the change reaches the disk, so it is applied.
func TestScenarioExternalChangeKeepMineAppliesAtSave(t *testing.T) {
	s := unsaved(t)
	disk := s.ReadFile("demo.spec.md")
	s.WriteFile("demo.spec.md", disk+"\nAn external line.\n", true)
	s.Press("ctrl+o")
	s.ExpectContext(tui.CtxConflict)
	s.Press("k")
	expectPending(s)
	s.Press("ctrl+o")
	s.ExpectDirty(false)
	expectApplied(s)
}

// The agent withdraws while the approval is unsaved. The save still writes
// the buffer, but the proposal stays rejected and the save says so.
func TestScenarioWithdrawnWhileUnsavedStaysRejected(t *testing.T) {
	s := unsaved(t)
	id := s.Proposals()[0].ID
	s.Shell("spec", "reject", id, "--by", "agent-7", "--why", "withdrawn: resent with more")
	s.Press("ctrl+o")
	s.ExpectFlash(id + ` .*rejected.*not recorded`)
	if got := status(s); got != proposal.StatusRejected {
		s.fail("want the proposal to stay rejected, got %s", got)
	}
}

// A fix accepted from a check's cards settles the check at the save.
func TestScenarioFixSettlesAtSave(t *testing.T) {
	s := checked(t)
	s.Press("ctrl+r", "y")
	s.ExpectBuffer(proseReplace)
	s.Press("n")
	s.ExpectCards()
	if got := status(s); got != proposal.StatusPending {
		s.fail("want the check pending before the save, got %s", got)
	}
	s.Press("ctrl+o")
	s.WaitUntil("the record", func(st tui.State) bool { return status(s) == proposal.StatusApplied })
}

// A fix accepted and then thrown away with the buffer is not remembered as
// accepted: after a restart its card asks again.
func TestScenarioFixDiscardedIsAskedAgain(t *testing.T) {
	s := checked(t)
	s.Press("ctrl+r")
	before := len(s.State().Cards)
	s.Press("y")
	s.ExpectBuffer(proseReplace)
	s.Reopen()
	s.Press("ctrl+r")
	s.Expect("every card back", func(st tui.State) bool { return len(st.Cards) == before })
	s.ExpectNoBuffer(proseReplace)
	if got := status(s); got != proposal.StatusPending {
		s.fail("want the check pending, got %s", got)
	}
}

// An agent withdraws its pending proposal and resends: the reviewer sees the
// old card go and a new one arrive, and a waiter on the old ID exits 3.
func TestScenarioWithdrawAndResend(t *testing.T) {
	s := Open(t, scenes.Demo, Agents(agentTiming), Replies(infer.CannedFile{Check: []infer.CannedEntry{cleanReply, cleanReply}}))
	s.Press("ctrl+r")
	s.ExpectCards("proposal")
	old := s.Proposals()[0].ID
	s.Shell("spec", "reject", old, "--why", "withdrawn: resending with more", "--by", "agent-7")
	s.WriteFile("demo.spec.md", strings.Replace(s.ReadFile("demo.spec.md"), aboveZero, atMost5s, 1), true)
	out, code := s.ShellCode("spec", "propose", "Frame.Timing", "--why", "bound and more", "--by", "agent-7")
	if code != waitExitPending {
		s.fail("want the resend pending, got exit %d:\n%s", code, out)
	}
	s.Deliver("the store changed", tui.ProposalsChanged())
	s.Expect("one card, for the new proposal", func(st tui.State) bool {
		cards := st.CardsOf("proposal")
		return len(cards) == 1 && len(st.Cards) == 1
	})
	s.Expect("the old proposal is gone from the queue", func(st tui.State) bool { return st.Badge == "[1 proposal]" })
	waited, code := s.ShellCode("spec", "proposal", old, "--wait", "20ms")
	if code != 3 || !strings.Contains(waited, "rejected by agent-7: withdrawn: resending with more") {
		s.fail("want the waiter on the old ID to exit 3 with the reason, got %d:\n%s", code, waited)
	}
}
