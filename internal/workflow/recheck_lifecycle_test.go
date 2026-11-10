// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

func pendingExternal(t *testing.T, proj *project.Project) *proposal.Proposal {
	t.Helper()
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
	res, e := (&Workflow{Getenv: func(string) string { return "" }}).Propose(context.Background(), proj, ProposeRequest{Names: []string{"A.One"}, SourceKind: "agent", Why: "raise capacity"})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 1)
	return res.Proposals[0]
}

func TestRecheckRefreshesFullContextPreservesIdentityAndPrimary(t *testing.T) {
	proj := commitRepo(t)
	p := pendingExternal(t, proj)
	env := newCascadeEnv(t, "")
	env.check.reply = chatReply(`{"summary":"fresh","findings":[],"questions":[]}`)
	write(t, proj.Root, "other.spec.md", "# Extra\n\nSurrounding context changed.\n")
	overlay := map[string][]byte{"c.spec.md": []byte("Intro moved.\n\n" + commitSpec), "unsaved.spec.md": []byte("# Unsaved\n\nFresh editor context.\n")}
	got, e := env.wf.RecheckPublished(context.Background(), proj, p.ID, true, nil, overlay)
	require.Nil(t, e)
	assert.Equal(t, p.ID, got.ID)
	assert.Equal(t, p.Created, got.Created)
	assert.Equal(t, p.Primary, got.Primary)
	assert.Greater(t, got.CheckGeneration, p.CheckGeneration)
	assert.Len(t, got.CheckFiles, 3)
	raw, _ := json.Marshal(env.check.requests())
	assert.Contains(t, string(raw), "Fresh editor context")
	assert.Contains(t, string(raw), "Surrounding context changed")
	_, e = env.wf.Apply(proj.Root, p.ID, nil, "human", false)
	require.NotNil(t, e)
	assert.Equal(t, "stale_check", e.Code, "disk cannot use a check of unsaved buffers")
	prep, e := env.wf.PrepareInto(proj, got, []int{1}, overlay)
	require.Nil(t, e)
	require.Len(t, prep.Edits, 1)
	assert.Greater(t, prep.Edits[0].Splices[0].Range.Start, 0)
	overlay["unsaved.spec.md"] = []byte("changed while reviewing")
	_, e = env.wf.PrepareInto(proj, got, []int{1}, overlay)
	require.NotNil(t, e)
	assert.Equal(t, "stale_check", e.Code)
}

func TestRecheckRefusesChangedPrimaryAndAppliedReceipts(t *testing.T) {
	proj := commitRepo(t)
	p := pendingExternal(t, proj)
	env := newCascadeEnv(t, "")
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 8", 1))
	got, e := env.wf.RecheckPublished(context.Background(), proj, p.ID, false, nil, nil)
	require.NotNil(t, e)
	assert.Equal(t, "stale", e.Code)
	assert.Equal(t, p.Primary.NewText, got.Primary.NewText)
	assert.Equal(t, proposal.StatusPending, got.Status)
	assert.Empty(t, env.check.requests())
	got.Decision = &proposal.Decision{Applied: []int{1}}
	require.NoError(t, env.wf.Store(proj.Root).Save(got))
	_, e = env.wf.RecheckPublished(context.Background(), proj, p.ID, false, nil, nil)
	require.NotNil(t, e)
	assert.Equal(t, "retry_unavailable", e.Code)
	stored, err := env.wf.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, []int{1}, stored.Decision.Applied)
}

func TestCheckProgressContentionDoesNotDiscardCompletedWork(t *testing.T) {
	proj := commitRepo(t)
	p := pendingExternal(t, proj)
	env := newCascadeEnv(t, "")
	env.wf.LockWait = time.Millisecond
	env.check.reply = chatReply(`{"summary":"paid result","findings":[],"questions":[]}`)
	var held *project.Lock
	resultReady := make(chan struct{})
	env.wf.BeforeCommit = func() { close(resultReady) }
	done := make(chan *Error, 1)
	go func() {
		_, e := env.wf.CheckPublished(context.Background(), proj, p.ID, false, func(ev StageEvent) {
			if held == nil {
				var err error
				held, err = project.AcquireLock(proj.Root, "test contention")
				assert.NoError(t, err)
			}
		})
		done <- e
	}()
	select {
	case <-resultReady:
	case <-time.After(5 * time.Second):
		t.Fatal("check did not reach checkpoint")
	}
	// The ready checkpoint exists before the completion wait; no sleeps are
	// needed to keep the lock longer than the configured interactive timeout.
	data, err := os.ReadFile(checkpointPath(proj.Root, p.ID, readPending(t, proj.Root, p.ID).CheckOwner))
	require.NoError(t, err)
	assert.Contains(t, string(data), "paid result")
	require.NoError(t, held.Release())
	select {
	case e := <-done:
		require.Nil(t, e)
	case <-time.After(5 * time.Second):
		t.Fatal("completion stranded")
	}
	got := readPending(t, proj.Root, p.ID)
	assert.Equal(t, "complete", got.CheckState)
	assert.Equal(t, "paid result", got.Summary)
	assert.Len(t, env.check.requests(), 1)
}

func readPending(t *testing.T, root, id string) *proposal.Proposal {
	t.Helper()
	p, err := proposal.NewStore(root).Load(id)
	require.NoError(t, err)
	return p
}

func TestCompletedCheckpointSurvivesSaveFailureAndIsFenced(t *testing.T) {
	proj := commitRepo(t)
	p := pendingExternal(t, proj)
	env := newCascadeEnv(t, "")
	env.check.reply = chatReply(`{"summary":"retained answer","findings":[],"questions":[]}`)
	path := env.wf.Store(proj.Root).Path(p.ID)
	var saved []byte
	env.wf.BeforeCommit = func() {
		var err error
		saved, err = os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Mkdir(path, 0o755))
	}
	_, e := env.wf.CheckPublished(context.Background(), proj, p.ID, false, nil)
	require.NotNil(t, e)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, saved, 0o644))
	require.Nil(t, env.wf.ReconcileChecks(proj.Root))
	got := readPending(t, proj.Root, p.ID)
	assert.Equal(t, "complete", got.CheckState)
	assert.Equal(t, "retained answer", got.Summary)
	again, e := env.wf.CheckPublished(context.Background(), proj, p.ID, false, nil)
	require.Nil(t, e)
	assert.Equal(t, got.Revision, again.Revision)
	assert.Len(t, env.check.requests(), 1, "checkpoint recovery never reruns a model")
}

func TestCheckLateResultCannotOverwriteDecision(t *testing.T) {
	proj := commitRepo(t)
	p := pendingExternal(t, proj)
	env := newCascadeEnv(t, "")
	env.check.reply = func(w http.ResponseWriter, body map[string]any) {
		_, e := env.wf.Reject(proj.Root, p.ID, "no longer wanted", "human")
		require.Nil(t, e)
		chatReply(`{"summary":"late","findings":[],"questions":[]}`)(w, body)
	}
	got, e := env.wf.CheckPublished(context.Background(), proj, p.ID, false, nil)
	require.NotNil(t, e)
	assert.Equal(t, "changed", e.Code)
	assert.Equal(t, proposal.StatusRejected, got.Status)
}

func TestCheckWorkerDeathReconciles(t *testing.T) {
	if root := os.Getenv("LOGEDIT_CHECK_DEATH_ROOT"); root != "" {
		wf := &Workflow{}
		loaded, err := project.Load(root, nil)
		if err != nil {
			os.Exit(20)
		}
		token, owner, e := newCheckOwner(root)
		if e != nil {
			os.Exit(21)
		}
		defer owner.Release()
		p := &proposal.Proposal{Kind: proposal.KindChange, Primary: &proposal.Primary{N: 1}, CheckState: "running", CheckOwner: token, CheckFiles: snapshotHashes(loaded.Spec)}
		if wf.Store(root).Create(p) != nil {
			os.Exit(22)
		}
		if os.Getenv("LOGEDIT_CHECK_DEATH_RESULT") == "yes" {
			if saveCheckpoint(root, checkCheckpoint{Seen: p, State: "complete", Message: "Check finished", Result: &proposal.Proposal{Summary: "completed before death"}}) != nil {
				os.Exit(23)
			}
		}
		if os.WriteFile(filepath.Join(root, "ready"), []byte(p.ID), 0o600) != nil {
			os.Exit(24)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	for _, result := range []string{"no", "yes"} {
		t.Run(result, func(t *testing.T) {
			proj := commitRepo(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestCheckWorkerDeathReconciles$")
			cmd.Env = append(os.Environ(), "LOGEDIT_CHECK_DEATH_ROOT="+proj.Root, "LOGEDIT_CHECK_DEATH_RESULT="+result)
			require.NoError(t, cmd.Start())
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			var data []byte
			require.Eventually(t, func() bool { data, _ = os.ReadFile(filepath.Join(proj.Root, "ready")); return len(data) > 0 }, 5*time.Second, 10*time.Millisecond)
			id := string(data)
			wf := &Workflow{}
			require.Nil(t, wf.ReconcileChecks(proj.Root))
			assert.Equal(t, "running", readPending(t, proj.Root, id).CheckState, "live owner cannot be stolen")
			require.NoError(t, cmd.Process.Kill())
			_ = cmd.Wait()
			require.Nil(t, wf.ReconcileChecks(proj.Root))
			got := readPending(t, proj.Root, id)
			if result == "yes" {
				assert.Equal(t, "complete", got.CheckState)
				assert.Equal(t, "completed before death", got.Summary)
			} else {
				assert.Equal(t, "failed", got.CheckState)
				assert.Contains(t, got.CheckMessage, "interrupted")
			}
		})
	}
}

func TestMalformedCheckOwnerBecomesActionableFailure(t *testing.T) {
	proj := commitRepo(t)
	p := pendingExternal(t, proj)
	p.CheckOwner = "../../invalid"
	p.CheckState = "running"
	require.NoError(t, proposal.NewStore(proj.Root).Save(p))
	require.Nil(t, (&Workflow{}).ReconcileChecks(proj.Root))
	got := readPending(t, proj.Root, p.ID)
	assert.Equal(t, "failed", got.CheckState)
	assert.Contains(t, got.CheckMessage, "owner record is invalid")
	assert.Empty(t, got.CheckOwner)
}

func TestCheckClaimRecoversCheckpointAfterReconciliationLockContention(t *testing.T) {
	proj := commitRepo(t)
	p := pendingExternal(t, proj)
	env := newCascadeEnv(t, "")
	token, owner, e := newCheckOwner(proj.Root)
	require.Nil(t, e)
	p.CheckOwner, p.CheckState = token, "running"
	require.NoError(t, env.wf.Store(proj.Root).Save(p))
	require.NoError(t, saveCheckpoint(proj.Root, checkCheckpoint{Seen: p, State: "complete", Message: "Check finished", Result: &proposal.Proposal{Summary: "already paid"}}))
	require.NoError(t, owner.Release())
	lock, err := project.AcquireLock(proj.Root, "hold claim")
	require.NoError(t, err)
	defer lock.Release()
	type answer struct {
		p *proposal.Proposal
		e *Error
	}
	done := make(chan answer, 1)
	go func() {
		p, e := env.wf.CheckPublished(context.Background(), proj, p.ID, false, nil)
		done <- answer{p, e}
	}()
	// pendingExternal and the saved attempt have already created two owners.
	// A third owner proves reconciliation returned and the new claimant is waiting.
	require.Eventually(t, func() bool {
		entries, _ := os.ReadDir(filepath.Join(proj.Root, ".spec", "checks", "owners"))
		return len(entries) >= 4
	}, 3*time.Second, 5*time.Millisecond)
	require.NoError(t, lock.Release())
	select {
	case got := <-done:
		require.Nil(t, got.e)
		assert.Equal(t, "already paid", got.p.Summary)
		assert.Equal(t, "complete", got.p.CheckState)
	case <-time.After(5 * time.Second):
		t.Fatal("claim did not finish")
	}
	assert.Empty(t, env.check.requests(), "claim must consume checkpoint before replacing its token")
}

func TestProposeCannotRestoreBeforeInterruptedOperationAcknowledgment(t *testing.T) {
	proj := commitRepo(t)
	w := &Workflow{}
	intended := strings.Replace(commitSpec, "at most 4", "at most 6", 1)
	write(t, proj.Root, "c.spec.md", intended)
	intent, err := newRecovery("approve")
	require.NoError(t, err)
	require.NoError(t, saveRecovery(proj.Root, intent))
	_, e := w.Propose(context.Background(), proj, ProposeRequest{SourceKind: "agent", Why: "new work"})
	require.NotNil(t, e)
	assert.Equal(t, "recovery_required", e.Code)
	assert.Equal(t, intended, read(t, proj.Root, "c.spec.md"))
	ps, err := w.Store(proj.Root).List()
	require.NoError(t, err)
	assert.Empty(t, ps)
	_, e = w.Recover(proj.Root, true, "inspected worktree and index")
	require.Nil(t, e)
	res, e := w.Propose(context.Background(), proj, ProposeRequest{Names: []string{"A.One"}, SourceKind: "agent", Why: "new work"})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 1)
	assert.Equal(t, commitSpec, read(t, proj.Root, "c.spec.md"))
}

func TestProposeStopsWhenAppliedEvidenceCannotBeRead(t *testing.T) {
	proj := commitRepo(t)
	w := &Workflow{}
	intended := strings.Replace(commitSpec, "at most 4", "at most 6", 1)
	write(t, proj.Root, "c.spec.md", intended)
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	// Writable but not readable: filing would succeed, and the restore would
	// revert any applied change that the store could not show.
	dir := filepath.Join(proj.Root, ".spec", "proposals")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o300))
	t.Cleanup(func() { assert.NoError(t, os.Chmod(dir, 0o755)) })
	_, e := w.Propose(context.Background(), proj, ProposeRequest{Names: []string{"A.One"}, SourceKind: "agent", Why: "new work"})
	require.NotNil(t, e)
	assert.Equal(t, "proposals_unreadable", e.Code)
	assert.Equal(t, intended, read(t, proj.Root, "c.spec.md"), "nothing is restored")
}
