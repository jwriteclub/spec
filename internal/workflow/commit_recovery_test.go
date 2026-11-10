// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

func recoveryProposal(t *testing.T, proj *project.Project) *proposal.Proposal {
	t.Helper()
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	a := loaded.Spec.Lookup("A.One")[0]
	old := loaded.Spec.File(a.File).Text(a.BodyRange)
	p := &proposal.Proposal{Kind: proposal.KindChange, SourceKind: "agent", CheckState: "complete", CheckFiles: snapshotHashes(loaded.Spec),
		Primary: &proposal.Primary{N: 1, Action: "change", Name: "$A.One", File: a.File, OldKind: "inv", NewKind: "inv", OldText: old, NewText: strings.Replace(old, "at most 4", "at most 6", 1)}}
	FillBase(p, loaded.Spec)
	require.NoError(t, (&Workflow{}).Store(proj.Root).Create(p))
	return p
}

func TestApprovalPreflightLeavesFilesAndDecisionUntouched(t *testing.T) {
	for _, state := range []string{"detached", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		t.Run(state, func(t *testing.T) {
			proj := commitRepo(t)
			p := recoveryProposal(t, proj)
			original := read(t, proj.Root, proposal.RelPath(p.ID))
			if state == "detached" {
				git(t, proj.Root, "checkout", "--detach", "-q")
			} else {
				write(t, filepath.Join(filepath.Dir(proj.Root), ".git"), state, git(t, proj.Root, "rev-parse", "HEAD"))
			}
			_, e := (&Workflow{}).Apply(proj.Root, p.ID, nil, "reviewer", true)
			require.NotNil(t, e)
			assert.Equal(t, "commit_failed", e.Code)
			assert.Equal(t, commitSpec, read(t, proj.Root, "c.spec.md"))
			assert.Equal(t, original, read(t, proj.Root, proposal.RelPath(p.ID)))
			_, err := os.Stat(filepath.Join(proj.Root, operationPath))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestLegacyAlreadyPresentPrimaryStaysPending(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	// Old receipts lack checked snapshot evidence, as in the crash probe.
	p.SourceKind, p.CheckState, p.CheckFiles = "", "", nil
	w := &Workflow{}
	require.NoError(t, w.Store(proj.Root).Save(p))
	written := strings.Replace(commitSpec, "at most 4", "at most 6", 1)
	write(t, proj.Root, "c.spec.md", written)
	_, e := w.Apply(proj.Root, p.ID, nil, "reviewer", true)
	require.NotNil(t, e)
	assert.Equal(t, "stale", e.Code)
	assert.Contains(t, e.Message, "remains pending")
	stored := readPending(t, proj.Root, p.ID)
	assert.Equal(t, proposal.StatusPending, stored.Status, "replacement equality proves neither approval nor rejection")
	assert.Nil(t, stored.Decision)
	require.NotNil(t, stored.StalePrimary)
	assert.Equal(t, written, read(t, proj.Root, "c.spec.md"))
}

// The child is killed by a short-lived Git hook at the actual production
// boundary; no sleeps, fault-injection globals or orphan watchdogs are needed.
func TestCommitRecoveryProcess(t *testing.T) {
	root := os.Getenv("SPEC_RECOVERY_TEST_ROOT")
	if root == "" {
		return
	}
	require.NoError(t, os.Setenv("SPEC_RECOVERY_TEST_PID", fmt.Sprint(os.Getpid())))
	w := &Workflow{}
	id := os.Getenv("SPEC_RECOVERY_TEST_ID")
	mode := os.Getenv("SPEC_RECOVERY_TEST_MODE")
	if mode == "retry" {
		_, e := w.Apply(root, id, nil, "reviewer", true)
		require.NotNil(t, e)
		require.Equal(t, "recovery_required", e.Code)
		return
	}
	if mode == "commit" {
		proj, err := project.Open(root)
		require.NoError(t, err)
		b, e := w.LoadBaseline(proj, baseline.Source{}, false)
		require.Nil(t, e)
		loaded, err := proj.Load(nil)
		require.NoError(t, err)
		_, e = w.CommitChangesSeen(proj, []string{"$A.One"}, "selected change", b.Commit, snapshotHashes(loaded.Spec))
		t.Fatalf("commit survived kill hook: %v", e)
	}
	_, e := w.Apply(root, id, nil, "reviewer", true)
	t.Fatalf("approval survived kill hook: %v", e)
}

func recoveryChild(t *testing.T, root, id, mode string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommitRecoveryProcess$")
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir(), "SPEC_RECOVERY_TEST_ROOT="+root, "SPEC_RECOVERY_TEST_ID="+id, "SPEC_RECOVERY_TEST_MODE="+mode)
	out, err := cmd.CombinedOutput()
	require.NoError(t, ctx.Err(), "bounded child timed out: %s", out)
	if mode == "retry" {
		require.NoError(t, err, "%s", out)
	}
	return err
}

func TestCommitRecoverySurvivesProcessDeath(t *testing.T) {
	for _, mode := range []string{"approve", "commit"} {
		for _, hook := range []string{"pre-commit", "post-commit"} {
			t.Run(mode+"/"+hook, func(t *testing.T) {
				proj := commitRepo(t)
				p := recoveryProposal(t, proj)
				receipt := read(t, proj.Root, proposal.RelPath(p.ID))
				parent := git(t, proj.Root, "rev-parse", "HEAD")
				if mode == "commit" {
					write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
				}
				hookPath := filepath.Join(filepath.Dir(proj.Root), ".git", "hooks", hook)
				require.NoError(t, os.WriteFile(hookPath, []byte("#!/bin/sh\nkill -KILL \"$SPEC_RECOVERY_TEST_PID\"\n"), 0o755))
				require.Error(t, recoveryChild(t, proj.Root, p.ID, mode))
				require.NoError(t, os.Remove(hookPath))
				w := &Workflow{}
				intent, e := w.Recover(proj.Root, false, "")
				require.Nil(t, e)
				require.NotNil(t, intent)
				assert.Equal(t, receipt, read(t, proj.Root, proposal.RelPath(p.ID)))
				if hook == "post-commit" {
					assert.NotEmpty(t, intent.PreparedCommit)
					assert.Equal(t, intent.PreparedCommit, strings.TrimSpace(git(t, proj.Root, "rev-parse", "HEAD")))
					assert.NotEmpty(t, git(t, proj.Root, "diff", "--cached"), "real index is still the old one")
				} else {
					assert.Equal(t, parent, git(t, proj.Root, "rev-parse", "HEAD"))
				}
				// Stage later work, then restart and acknowledge: neither step
				// may reset the real index or claim that old writes were applied.
				later := strings.Replace(commitSpec, "at most 4", "at most 9", 1)
				write(t, proj.Root, "c.spec.md", later)
				git(t, proj.Root, "add", "c.spec.md")
				index := git(t, proj.Root, "write-tree")
				require.NoError(t, recoveryChild(t, proj.Root, p.ID, "retry"))
				_, e = w.Recover(proj.Root, true, "inspected HEAD and retained later staging")
				require.Nil(t, e)
				assert.Equal(t, index, git(t, proj.Root, "write-tree"))
				assert.Equal(t, later, read(t, proj.Root, "c.spec.md"))
				assert.Equal(t, receipt, read(t, proj.Root, proposal.RelPath(p.ID)))
				require.Nil(t, w.CheckRecovery(proj.Root, ""), "project guard is clear")
				if mode == "approve" {
					require.NotNil(t, w.CheckRecovery(proj.Root, p.ID), "acknowledgment never re-enables ambiguous proposal")
					closed, e := w.Reject(proj.Root, p.ID, "remaining work will be refiled", "reviewer")
					require.Nil(t, e)
					assert.Contains(t, closed.Decision.Reason, "prior writes are not classified")
				}
				git(t, proj.Root, "fsck", "--connectivity-only", "--no-reflogs")
			})
		}
	}
}

func TestPartialWriteIntentFencesFreshWorkflowWithoutOmittingPriorWrite(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	w := &Workflow{}
	outputs := []edit.Output{
		{Path: "c.spec.md", Before: []byte(commitSpec), After: []byte(strings.Replace(commitSpec, "at most 4", "at most 6", 1))},
		{Path: "other.spec.md", Before: []byte("old"), After: []byte("new")},
	}
	write(t, proj.Root, "other.spec.md", "old")
	intent, e := w.beginApproval(proj.Root, p, []int{1, 2}, []int{1, 2}, outputs)
	require.Nil(t, e)
	write(t, proj.Root, "c.spec.md", string(outputs[0].After))
	_, e = (&Workflow{}).Apply(proj.Root, p.ID, nil, "reviewer", false)
	require.NotNil(t, e)
	assert.Equal(t, "recovery_required", e.Code)
	assert.Equal(t, "old", read(t, proj.Root, "other.spec.md"))
	pending, err := w.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Nil(t, pending.Decision)
	recovered, e := w.Recover(proj.Root, true, "inspected partial write")
	require.Nil(t, e)
	assert.Equal(t, intent.PlannedApplied, recovered.PlannedApplied)
	assert.Equal(t, intent.Files, recovered.Files)
	assert.NotNil(t, w.CheckRecovery(proj.Root, p.ID))
	// Even an unrelated persisted revision cannot silently release the fence.
	pending.Summary = "revised metadata"
	require.NoError(t, w.Store(proj.Root).Save(pending))
	assert.NotNil(t, w.CheckRecovery(proj.Root, p.ID))
}

func TestRecoveryAcknowledgmentPreservesDecidedReceipt(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	w := &Workflow{}
	intent, e := w.beginApproval(proj.Root, p, []int{1}, []int{1}, nil)
	require.Nil(t, e)
	p.Status = proposal.StatusApplied
	p.Decision = &proposal.Decision{Accepted: []int{1}, Applied: []int{1}, By: "reviewer"}
	require.NoError(t, w.Store(proj.Root).Save(p))
	receipt := read(t, proj.Root, proposal.RelPath(p.ID))
	_, e = w.Recover(proj.Root, true, "receipt already saved")
	require.Nil(t, e)
	assert.Equal(t, receipt, read(t, proj.Root, proposal.RelPath(p.ID)))
	archive := read(t, proj.Root, recoveryDir+"/"+intent.ID+".json")
	// Repeating acknowledgment with no active intent cannot overwrite evidence.
	_, e = w.Recover(proj.Root, true, "different note")
	require.Nil(t, e)
	assert.Equal(t, archive, read(t, proj.Root, recoveryDir+"/"+intent.ID+".json"))
}

// Receipt persistence is independent of installing the Git ref. Force that
// persistence failure without discarding the old receipt, and retain the real
// outcome in both the committed and uncommitted approval paths.
func TestApprovalReceiptFailurePreservesActualWriteAndCommitOutcome(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprint(commit), func(t *testing.T) {
			proj := commitRepo(t)
			p := recoveryProposal(t, proj)
			original := read(t, proj.Root, proposal.RelPath(p.ID))
			storeDir := filepath.Join(proj.Root, proposal.Dir)
			backup := storeDir + "-before"
			calls := 0
			w := &Workflow{BeforeCommit: func() {
				calls++
				// First callback precedes file writes; second precedes Git.
				// Both are after the pending proposal has been loaded.
				if calls != 1 {
					return
				}
				require.NoError(t, os.Rename(storeDir, backup))
				require.NoError(t, os.WriteFile(storeDir, []byte("block receipt directory"), 0o600))
			}}
			res, e := w.Apply(proj.Root, p.ID, nil, "reviewer", commit)
			assert.Contains(t, read(t, proj.Root, "c.spec.md"), "at most 6")
			if commit {
				require.Nil(t, e, "an installed commit is a success with a warning")
				require.NotNil(t, res)
				assert.Equal(t, strings.TrimSpace(git(t, proj.Root, "rev-parse", "HEAD")), res.Commit)
				assert.Contains(t, res.Warning, "receipt could not be saved")
				assert.Contains(t, res.Warning, "spec recover")
			} else {
				require.NotNil(t, e)
				assert.Equal(t, "recovery_required", e.Code)
				assert.True(t, e.Written)
				require.NotNil(t, e.Proposal)
				assert.Equal(t, proposal.StatusPending, e.Proposal.Status, "unsaved candidate is not a persisted receipt")
				assert.Nil(t, e.Proposal.Decision)
			}
			require.NoError(t, os.Remove(storeDir))
			require.NoError(t, os.Rename(backup, storeDir))
			assert.Equal(t, original, read(t, proj.Root, proposal.RelPath(p.ID)))
			intent, e := w.Recover(proj.Root, false, "")
			require.Nil(t, e)
			require.NotNil(t, intent)
			if commit {
				assert.Equal(t, res.Commit, intent.PreparedCommit)
			}
			assert.NotNil(t, w.CheckRecovery(proj.Root, p.ID))
		})
	}
}

func TestCommittedIndexFailureKeepsHashWarningAndGuard(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	// Simulate an index owned by another Git process after the ref installs.
	top := filepath.Dir(proj.Root)
	hook := filepath.Join(top, ".git", "hooks", "post-commit")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nprintf busy > \"$(git rev-parse --git-path index.lock)\"\n"), 0o755))
	w := &Workflow{}
	res, e := w.Apply(proj.Root, p.ID, nil, "reviewer", true)
	require.Nil(t, e)
	assert.Equal(t, strings.TrimSpace(git(t, proj.Root, "rev-parse", "HEAD")), res.Commit)
	assert.Contains(t, res.Warning, "index entries")
	assert.Contains(t, res.Warning, "spec recover")
	assert.NotNil(t, w.CheckRecovery(proj.Root, ""))
	saved, err := w.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	require.NotNil(t, saved.Decision)
	assert.Equal(t, res.Commit, saved.Decision.Commit)
	require.NoError(t, os.Remove(filepath.Join(top, ".git", "index.lock")))
}

func TestRecoveryDoesNotMislabelOrAcknowledgeLiveOperation(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	w := &Workflow{}
	lock, err := project.AcquireLock(proj.Root, "active approval")
	require.NoError(t, err)
	defer lock.Release()
	_, e := w.beginApproval(proj.Root, p, []int{1}, []int{1}, nil)
	require.Nil(t, e)
	e = w.CheckRecovery(proj.Root, p.ID)
	require.NotNil(t, e)
	assert.Contains(t, e.Message, "unfinished")
	assert.Contains(t, e.Message, "wait for active work")
	assert.NotContains(t, e.Message, "was interrupted")
	_, e = w.Recover(proj.Root, true, "must not override active owner")
	require.NotNil(t, e)
	assert.Equal(t, "lock_held", e.Code)
	active, err := readRecovery(filepath.Join(proj.Root, operationPath))
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Empty(t, active.Acknowledged)
}
