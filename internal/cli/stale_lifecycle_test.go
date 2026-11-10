// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

func TestRejectedDuringRequiredCheckReachesNormalVerdict(t *testing.T) {
	requireGit(t)
	started, release := make(chan struct{}), make(chan struct{})
	reply := okReply(modelReply{Summary: "late result"}.json(t))
	is := setupInfer(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-release; reply(w, r) })
	write(t, is.dir, "body.md", timingNew)
	done := is.runAsync("change", "Frame.Timing", "--body-file", "body.md", "--why", "update", "--auto-approve", "--wait", "20s")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("check did not start")
	}
	id := waitForProposal(t, is.dir)
	rejected := is.run(t, "reject", id, "--why", "no longer wanted")
	close(release)
	require.Equal(t, 0, rejected.code)
	select {
	case got := <-done:
		jr := decodeEnvelope(t, got, nil)
		assert.Equal(t, exitRejected, jr.code)
		assert.Equal(t, "rejected", jr.errCode)
		assert.Contains(t, jr.errMessage, "no longer wanted")
		fd := filedOf(t, jr)
		require.Len(t, fd.Proposals, 1)
		assert.Equal(t, proposal.StatusRejected, fd.Proposals[0].Status)
		assert.NotNil(t, fd.Skipped)
		assert.Empty(t, fd.Skipped)
		assert.Empty(t, proposalFiles(t, is.dir), "decided receipt acknowledged after delivery")
	case <-time.After(5 * time.Second):
		t.Fatal("late reply did not deliver rejection")
	}
}

func TestWaitStalePrimaryRetainsPendingReceiptAndFullData(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "clear"}.json(t)))
	write(t, is.dir, "body.md", timingNew)
	filed := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "update")
	p := filedOf(t, filed).Proposals[0]
	replaceIn(t, is.dir, "demo.spec.md", "above zero", "strictly above zero")
	for attempt := 0; attempt < 2; attempt++ {
		got := is.run(t, "proposal", p.ID, "--wait", "20s")
		assert.Equal(t, exitStale, got.code)
		assert.Equal(t, "stale", got.errCode)
		fd := filedOf(t, got)
		require.Len(t, fd.Proposals, 1)
		assert.Equal(t, p.ID, fd.Proposals[0].ID)
		assert.Equal(t, proposal.StatusPending, fd.Proposals[0].Status)
		assert.Nil(t, fd.Proposals[0].Decision)
		require.NotNil(t, fd.Proposals[0].StalePrimary)
		assert.Contains(t, fd.Proposals[0].StalePrimary.Reason, "$Frame.Timing")
		assert.Contains(t, fd.Next.Text, "ask the user")
		assert.NotNil(t, fd.Skipped)
		assert.NotNil(t, fd.Warnings)
		assert.Len(t, proposalFiles(t, is.dir), 1)
	}
	ack := is.run(t, "proposal", p.ID, "--ack")
	assert.Equal(t, exitFail, ack.code)
	assert.Len(t, proposalFiles(t, is.dir), 1, "pending stale receipt cannot be acknowledged")
}

func TestMixedStaleBatchVerdictRetainsAllIDsAndRejectionPrecedence(t *testing.T) {
	st := proposal.NewStore(t.TempDir())
	stale := &proposal.Proposal{Kind: proposal.KindChange, StalePrimary: &proposal.StalePrimary{Reason: "anchor changed"}}
	pending := &proposal.Proposal{Kind: proposal.KindAnalysis}
	rejected := &proposal.Proposal{Kind: proposal.KindAnalysis, Status: proposal.StatusRejected, Decision: &proposal.Decision{Reason: "declined"}}
	for _, p := range []*proposal.Proposal{stale, pending, rejected} {
		require.NoError(t, st.Create(p))
	}
	s := &session{}
	out := s.verdict(st, []*proposal.Proposal{pending, stale}, nil, true, "")
	assert.Equal(t, exitStale, out.exit)
	data := out.data.(*filedData)
	require.Len(t, data.Proposals, 2)
	assert.Equal(t, []string{pending.ID, stale.ID}, []string{data.Proposals[0].ID, data.Proposals[1].ID})
	out = s.verdict(st, []*proposal.Proposal{stale, pending, rejected}, nil, true, "")
	assert.Equal(t, exitRejected, out.exit)
	ps, err := st.List()
	require.NoError(t, err)
	assert.Len(t, ps, 2, "only decided receipt removed")
}

func TestFiledFailureUsesEmptySkippedArray(t *testing.T) {
	out := filedFailure([]*proposal.Proposal{}, nil, fail(exitFail, "changed", "changed while checking"))
	data, err := json.Marshal(out.data)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"skipped":[]`)
}

func TestWaitLiveOperationRetainsStructuredLockAndProposal(t *testing.T) {
	dir := demoProject(t)
	st := proposal.NewStore(dir)
	p := &proposal.Proposal{Kind: proposal.KindChange, StalePrimary: &proposal.StalePrimary{Reason: "stale source"}}
	require.NoError(t, st.Create(p))
	lock, err := project.AcquireLock(dir, "test active approval")
	require.NoError(t, err)
	defer lock.Release()
	intent, err := json.Marshal(workflow.RecoveryIntent{ID: "0123456789abcdef0123456789abcdef", Kind: "approve", Proposal: p})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".spec", "operation.json"), intent, 0o600))
	jr := runJSON(t, dir, "proposal", p.ID, "--wait", "10s")
	assert.Equal(t, exitLock, jr.code)
	assert.Equal(t, "lock_held", jr.errCode)
	fd := filedOf(t, jr)
	require.Len(t, fd.Proposals, 1)
	assert.Equal(t, p.ID, fd.Proposals[0].ID)
	require.NotNil(t, fd.Lock)
	assert.Equal(t, os.Getpid(), fd.Lock.PID)
	assert.Equal(t, "test active approval", fd.Lock.Command)
	assert.NotEmpty(t, fd.Lock.Host)
	assert.NotEmpty(t, fd.Lock.Started)
	assert.Len(t, proposalFiles(t, dir), 1)
}

func TestAutoApproveRetainsDurablePrimaryMarker(t *testing.T) {
	dir := demoProject(t)
	st := proposal.NewStore(dir)
	p := &proposal.Proposal{Kind: proposal.KindChange, CheckState: "complete", CheckFiles: map[string]string{"old.spec.md": "old"}, StalePrimary: &proposal.StalePrimary{Reason: "primary previously changed"}}
	require.NoError(t, st.Create(p))
	s := &session{}
	out := s.fileProposals(dir, []*proposal.Proposal{p}, nil, fileFlags{autoApprove: true}, "")
	assert.Equal(t, exitStale, out.exit)
	got, err := st.Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.StalePrimary, got.StalePrimary)
	assert.Equal(t, proposal.StatusPending, got.Status)
	assert.Nil(t, got.Decision)
}
