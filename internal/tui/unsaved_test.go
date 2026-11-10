// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
)

// A second y on a proposal whose approval is unsaved must not apply it twice
// or record anything: the card it acts on may be from before the first.
func TestSecondApprovalWhileUnsaved(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.loadPending()
	require.NotEmpty(t, a.side.cards)
	c := a.side.cards[0]
	a.acceptCard(c)
	once := buffer(a, "demo.spec.md")
	require.Contains(t, once, timingNew)
	assert.Empty(t, a.pending, "the approved proposal has left the queue")

	a.acceptCard(c)
	assert.Contains(t, a.flash, "already in the buffer")
	assert.Equal(t, once, buffer(a, "demo.spec.md"))
	stored, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, stored.Status)

	saveBuffers(t, a)
	stored, err = proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, stored.Status)
	assert.Equal(t, []int{1, 2}, stored.Decision.Applied)
}

// Saving one file of a two-file approval records that file's hunks at once,
// so a waiter does not find the primary's new text and call it stale, and
// the proposal is applied only when the other file is saved too.
func TestApprovalAcrossFilesLandsPerFile(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, true)
	wf := testWorkflow(t, nil)
	a := newTestApp(t, uiOptions(dir, wf))
	a.loadPending()
	require.NotEmpty(t, a.side.cards)
	a.acceptCard(a.side.cards[0])

	demo, other := a.ws.Docs()["demo.spec.md"], a.ws.Docs()["other.spec.md"]
	require.True(t, demo.Ed.Buffer().Dirty())
	require.True(t, other.Ed.Buffer().Dirty())
	_, err := a.saveDoc(demo)
	require.NoError(t, err)
	assert.Contains(t, readFile(t, dir, "demo.spec.md"), timingNew)
	stored, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, stored.Status, "other.spec.md is not saved")
	assert.Equal(t, []int{1, 2}, stored.Decision.Applied, "what reached the disk")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _ = wf.Wait(ctx, dir, []string{p.ID}, 5*time.Millisecond)
	stored, err = proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Nil(t, stored.StalePrimary, "the primary is applied, not stale")

	_, err = a.saveDoc(other)
	require.NoError(t, err)
	stored, err = proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, stored.Status)
	assert.Equal(t, []int{1, 2, 3}, stored.Decision.Applied)
	assert.Empty(t, a.buffered)
}

// A hunk that fails keeps the proposal pending, shown as partly applied, and
// nothing is on record until the save. A retry before the save is judged with
// the first apply in the buffer: the primary's new text there is not staleness.
func TestPartialApprovalRetriedWhileUnsaved(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.loadPending()
	d := a.ws.Docs()["demo.spec.md"]
	d.Ed.ReplaceAll(strings.Replace(demoSpec, proseFind, "Prose changed by hand.", 1), 0)
	a.ws.Reparse()
	a.loadPending()
	require.NotEmpty(t, a.side.cards)
	a.acceptCard(a.side.cards[0])
	require.Contains(t, buffer(a, "demo.spec.md"), timingNew)
	require.NotEmpty(t, a.pending, "hunk 2 did not apply")
	assert.True(t, a.pending[0].AppliedHunk(1), "the UI shows the primary as applied")
	stored, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Nil(t, stored.Decision, "the store has heard nothing")

	require.NotEmpty(t, a.side.cards)
	a.acceptCard(a.side.cards[0])
	stored, err = proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Nil(t, stored.StalePrimary, "the retry does not call its own primary stale")
	assert.Equal(t, 1, strings.Count(buffer(a, "demo.spec.md"), timingNew))

	saveBuffers(t, a)
	stored, err = proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, stored.Status)
	assert.Equal(t, []int{1}, stored.Decision.Applied)
	assert.Nil(t, stored.StalePrimary)
}

// reviseElsewhere changes the text of hunk n of proposal id, as another spec
// process would.
func reviseElsewhere(t *testing.T, dir, id string, n int) {
	t.Helper()
	st := proposal.NewStore(dir)
	p, err := st.Load(id)
	require.NoError(t, err)
	for i := range p.Hunks {
		if p.Hunks[i].N == n {
			p.Hunks[i].Replace += " Revised."
		}
	}
	require.NoError(t, st.Save(p))
}

// A receipt refused because the proposal was revised elsewhere ends the
// approval: the proposal comes back for review, and the next save does not
// retry a receipt that can never be recorded.
func TestRefusedReceiptReturnsProposalForReview(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, true)
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.loadPending()
	require.NotEmpty(t, a.side.cards)
	a.acceptCard(a.side.cards[0])
	require.Empty(t, a.pending, "the approved proposal has left the queue")
	reviseElsewhere(t, dir, p.ID, 3)

	r, err := a.saveDoc(a.ws.Docs()["other.spec.md"])
	require.NoError(t, err)
	assert.True(t, r.warned)
	assert.Contains(t, strings.Join(r.notes, "; "), "changed meanwhile")
	assert.Empty(t, a.buffered)
	require.Len(t, a.pending, 1, "the changed proposal is back for review")
	assert.Equal(t, p.ID, a.pending[0].ID)

	r, err = a.saveDoc(a.ws.Docs()["demo.spec.md"])
	require.NoError(t, err)
	assert.False(t, r.warned, "nothing is left to retry")
	stored, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, stored.Status)
	assert.Nil(t, stored.Decision)
}

// The sidebar decisions a refused apply took go with it: the fix is asked
// again, and no later settle records it from the leftover key.
func TestRefusedReceiptDropsSidebarDecisions(t *testing.T) {
	dir := demoRepo(t)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, timingOld, timingNew, 1))
	p := agentProposal(t, dir, true)
	p.Primary.InWorktree = true
	require.NoError(t, proposal.NewStore(dir).Save(p))
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.loadPending()
	require.Len(t, a.pending, 1)
	var fix finding.Hunk
	for _, h := range a.pending[0].Hunks {
		if h.N == 2 {
			fix = h
		}
	}
	a.acceptFixes(a.pending[0], []finding.Hunk{fix}, decAccepted, false, "")
	require.Equal(t, decAccepted, a.decided(p.ID, hunkKey(2)))
	reviseElsewhere(t, dir, p.ID, 3)

	r, err := a.saveDoc(a.ws.Docs()["demo.spec.md"])
	require.NoError(t, err)
	assert.True(t, r.warned)
	assert.Empty(t, a.buffered)
	assert.Empty(t, a.decided(p.ID, hunkKey(2)), "the fix is asked again")
	require.Len(t, a.pending, 1)
	stored, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Nil(t, stored.Decision)
}

// A receipt refused for any other reason may yet be recorded: the approval is
// kept, so the proposal stays out of the queue.
func TestTransientReceiptFailureKeepsApproval(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, true)
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.loadPending()
	require.NotEmpty(t, a.side.cards)
	a.acceptCard(a.side.cards[0])
	path := proposal.NewStore(dir).Path(p.ID)
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))

	r, err := a.saveDoc(a.ws.Docs()["other.spec.md"])
	require.NoError(t, err)
	assert.True(t, r.warned)
	assert.Len(t, a.bufferedOf(p.ID), 1)
}
