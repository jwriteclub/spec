// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

// Step 4b review: decisions and jobs. Each test failed before its fix.

// The review must apply what Chris saw: a hunk revised elsewhere (`spec
// review` in another terminal) after the review opened is not applied
// unseen.
func TestApproveRefusesUnseenRevision(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	wf := testWorkflow(t, nil)
	a := newTestApp(t, uiOptions(dir, wf))
	send(a, key("ctrl+r"), key("enter"))
	require.NotNil(t, reviewOf(a))

	elsewhere := "Prose that mentions $Frame.Order and SOMETHING ELSE for the reader."
	h := p.Hunks[0]
	h.Replace = elsewhere
	_, e := wf.SetHunk(dir, p.ID, h)
	require.Nil(t, e)

	send(a, key("y"), key("A"))
	assert.Equal(t, demoSpec, string(a.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()), "nothing applied unseen")
	r := reviewOf(a)
	require.NotNil(t, r, "the review stays open")
	assert.Equal(t, elsewhere, r.p.Hunks[0].Replace, "it shows the proposal as it is now")
	assert.Equal(t, hunkUndecided, r.state[2], "the changed hunk needs a new decision")
	assert.Contains(t, plain(a), "+ "+elsewhere)
	stored, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, stored.Status)

	// Seen now: it applies (into the buffer).
	a.popup = nil
	send(a, key("y"), key("A"))
	assert.Equal(t, strings.Replace(strings.Replace(demoSpec, timingOld, timingNew, 1), proseFind, elsewhere, 1),
		string(a.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()))
}

// The status line says "^C cancels" while a check runs; it must on the
// review screen too (where f runs one).
func TestCtrlCCancelsJobOnReview(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	m := newFakeModel(t, modelJSON(t, map[string]string{"find": proseFind, "replace": "Prose.", "explanation": "e"}))
	gate := make(chan struct{})
	m.gate = gate
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.send(key("ctrl+r"), key("enter"), key("f"))
	f.typeIn("shorter")
	f.send(key("ctrl+o"))
	f.until("the request", func(a *App) bool { return a.job != nil && len(m.requests()) == 1 })
	assert.Contains(t, f.frame(), "^C cancels")
	f.send(key("ctrl+c"))
	f.do(func(a *App) {
		assert.Nil(t, a.job, "^C cancels the revision")
		assert.NotNil(t, reviewOf(a), "and leaves the review open")
		assert.Contains(t, a.flash, "Cancelled: revising hunk 2")
	})
	close(gate)
	time.Sleep(100 * time.Millisecond)
	stored, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proseReplace, stored.Hunks[0].Replace, "the cancelled revision is not saved")
	f.finish()
}

// A reject prompt answers for the proposal it was opened on, even when a
// ^P check finishes meanwhile.
func TestRejectPromptKeepsItsProposal(t *testing.T) {
	dir := demoRepo(t)
	agent := agentProposal(t, dir, false)
	writeFile(t, dir, "other.spec.md", strings.Replace(otherSpec, "at most 8", "at most 9", 1))
	m := newFakeModel(t, modelJSON(t, map[string]any{"summary": "fine", "findings": []any{}, "questions": []string{}}))
	gate := make(chan struct{})
	m.gate = gate
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	f.send(key("ctrl+p"))
	f.until("the request", func(a *App) bool { return a.job != nil && len(m.requests()) == 1 })

	// Meanwhile: review the agent's proposal and start rejecting it.
	f.send(key("ctrl+r"), key("enter"), key("R"))
	f.typeIn("no")
	close(gate)
	f.until("the check", func(a *App) bool { return a.job == nil })
	f.send(key("enter"))

	got, err := proposal.NewStore(dir).Load(agent.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusRejected, got.Status, "the reason was typed for the agent's proposal")
	ps, err := proposal.NewStore(dir).List()
	require.NoError(t, err)
	require.Len(t, ps, 2)
	for _, p := range ps {
		if p.ID != agent.ID {
			assert.Equal(t, proposal.StatusApplied, p.Status, "Chris's own check found nothing: done, not rejected")
		}
	}
	f.finish()
}

// blockingHook is a commit-msg hook that signals it runs, then waits for
// a release file (so a test can act inside approve's commit).
const blockingHook = "#!/bin/sh\ngd=\"$(git rev-parse --git-dir)\"\n: > \"$gd/hook-running\"\nn=0\nwhile [ ! -f \"$gd/hook-release\" ] && [ $n -lt 200 ]; do sleep 0.05; n=$((n+1)); done\nexit 0\n"

// A rejection that lands while an approval is committing (`spec approve
// --commit`; the UI no longer commits on approval) must not be reported as
// done while the approval then records "applied" over it.
// The rejection waits for the lock approve holds, then is refused.
func TestRejectDuringApprovalCommit(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	hook := filepath.Join(dir, ".git", "hooks", "commit-msg")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o755))
	require.NoError(t, os.WriteFile(hook, []byte(blockingHook), 0o755))

	type rejected struct {
		p *proposal.Proposal
		e *workflow.Error
	}
	got := make(chan rejected, 1)
	go func() {
		running := filepath.Join(dir, ".git", "hook-running")
		for range 400 {
			if _, err := os.Stat(running); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		// Another process's reject, during the commit.
		res := make(chan rejected, 1)
		go func() {
			rp, re := (&workflow.Workflow{}).Reject(dir, p.ID, "too late", "chris-cli")
			res <- rejected{rp, re}
		}()
		time.Sleep(200 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(dir, ".git", "hook-release"), nil, 0o644)
		got <- <-res
	}()

	_, _ = testWorkflow(t, nil).Apply(dir, p.ID, nil, proposal.AuthorHuman, true)
	r := <-got
	stored, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, stored.Status, "the files were written and committed")
	require.NotNil(t, r.e, "the late rejection is refused, not reported as done")
	assert.Equal(t, "not_pending", r.e.Code)
}
