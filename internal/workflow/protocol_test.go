// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExternalProposalPublishedBeforeCheckAndRetainedOnFailure(t *testing.T) {
	proj := commitRepo(t)
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
	w := &Workflow{Getenv: func(string) string { return "" }}
	res, e := w.Propose(context.Background(), proj, ProposeRequest{Names: []string{"A.One"}, SourceKind: "agent", Why: "raise capacity"})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 1)
	p := res.Proposals[0]
	assert.NotEmpty(t, p.ID)
	assert.Empty(t, p.Author, "unnamed agent remains agent")
	assert.Equal(t, "agent", p.SourceKind)
	assert.Equal(t, "failed", p.CheckState)
	assert.NotEmpty(t, p.CheckMessage)
	assert.Equal(t, commitSpec, read(t, proj.Root, "c.spec.md"), "author text is retained in proposal before restore")
	assert.Contains(t, p.Primary.NewText, "at most 6")
	_, e = w.Apply(proj.Root, p.ID, nil, "human", false)
	require.NotNil(t, e)
	assert.Equal(t, "check_required", e.Code)
}

func TestExternalProposalVisibleWhileCannedCheckRuns(t *testing.T) {
	proj := commitRepo(t)
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
	env := newCascadeEnv(t, "")
	env.check.reply = chatReply(`{"summary":"consistent","findings":[],"questions":[]}`)
	seen := false
	res, e := env.wf.Propose(context.Background(), proj, ProposeRequest{Names: []string{"A.One"}, SourceKind: "agent", Why: "raise capacity", OnStage: func(StageEvent) {
		ps, err := env.wf.Store(proj.Root).List()
		require.NoError(t, err)
		require.Len(t, ps, 1)
		assert.Equal(t, "running", ps[0].CheckState)
		assert.Equal(t, commitSpec, read(t, proj.Root, "c.spec.md"))
		seen = true
	}})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 1)
	assert.True(t, seen)
	assert.Equal(t, "complete", res.Proposals[0].CheckState)
	assert.Greater(t, res.Proposals[0].Revision, 1)
}

func TestCheckRetryUsesSameIdentityAndRejectsChangedSnapshot(t *testing.T) {
	proj := commitRepo(t)
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
	w := &Workflow{Getenv: func(string) string { return "" }}
	res, e := w.Propose(context.Background(), proj, ProposeRequest{Names: []string{"A.One"}, SourceKind: "agent", Why: "raise capacity"})
	require.Nil(t, e)
	p := res.Proposals[0]
	env := newCascadeEnv(t, "")
	env.check.reply = chatReply(`{"summary":"consistent","findings":[],"questions":[]}`)
	done, e := env.wf.CheckPublished(context.Background(), proj, p.ID, false, nil)
	require.Nil(t, e)
	assert.Equal(t, p.ID, done.ID)
	assert.Equal(t, "complete", done.CheckState)
	done.CheckState = "failed"
	require.NoError(t, env.wf.Store(proj.Root).Save(done))
	write(t, proj.Root, "c.spec.md", commitSpec+"\nDifferent context.\n")
	stale, e := env.wf.CheckPublished(context.Background(), proj, p.ID, false, nil)
	require.NotNil(t, e)
	assert.Equal(t, "stale", e.Code)
	assert.Equal(t, "stale", stale.CheckState)
	assert.Len(t, env.check.requests(), 1, "stale retry never sends project text")
}

func TestPartialApplicationRetainsResidualAndNeverReplaysSuccess(t *testing.T) {
	proj := commitRepo(t)
	w := &Workflow{}
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	p := &proposal.Proposal{Kind: proposal.KindAnalysis, CheckState: "complete", CheckFiles: snapshotHashes(loaded.Spec), Hunks: []finding.Hunk{
		{N: 1, File: "c.spec.md", Find: "One MUST hold at most 4 items.", Replace: "One MUST hold at most 6 items."},
		{N: 2, File: "c.spec.md", Find: "missing phrase", Replace: "replacement"},
	}}
	require.NoError(t, w.Store(proj.Root).Create(p))
	first, e := w.Apply(proj.Root, p.ID, nil, "human", false)
	require.Nil(t, e)
	assert.Equal(t, proposal.StatusPending, first.Proposal.Status)
	assert.Equal(t, []int{1}, first.Proposal.Decision.Applied)
	require.Len(t, first.Proposal.Decision.Failed, 1)
	h := first.Proposal.Hunks[1]
	h.Find = "Two MUST answer within 5 s."
	h.Replace = "Two MUST answer within 6 s."
	revised, e := w.SetHunkSeen(proj.Root, first.Proposal, h)
	require.Nil(t, e)
	second, e := w.ApplySeen(proj.Root, revised, nil, "human", false)
	require.Nil(t, e)
	assert.Equal(t, proposal.StatusApplied, second.Proposal.Status)
	assert.Equal(t, []int{1, 2}, second.Proposal.Decision.Applied)
	assert.Empty(t, second.Proposal.Decision.Failed)
	assert.Contains(t, read(t, proj.Root, "c.spec.md"), "at most 6")
	assert.Contains(t, read(t, proj.Root, "c.spec.md"), "within 6")
}

func TestCommitSeenRefusesUnseenSameAnchorEdit(t *testing.T) {
	proj := commitRepo(t)
	w := &Workflow{}
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
	b, e := w.LoadBaseline(proj, baseline.Source{}, true)
	require.Nil(t, e)
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	groups := Changes(b.Spec, loaded.Spec)
	require.Len(t, groups, 1)
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 8", 1))
	_, e = w.CommitChangesSeen(proj, []string{groups[0].ID}, "change", b.Commit, snapshotHashes(loaded.Spec))
	require.NotNil(t, e)
	assert.Equal(t, "changed", e.Code)
	assert.Equal(t, b.Commit, strings.TrimSpace(git(t, proj.Root, "rev-parse", "HEAD")))
}

func TestPrepareRefusesMetadataRevisionAndSetHunkCannotRewriteApplied(t *testing.T) {
	proj := commitRepo(t)
	w := &Workflow{}
	p := &proposal.Proposal{Kind: proposal.KindAnalysis, Hunks: []finding.Hunk{{N: 1, File: "c.spec.md", Find: "at most 4", Replace: "at most 6"}}}
	require.NoError(t, w.Store(proj.Root).Create(p))
	cur, err := w.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	cur.Why = "revised intent"
	require.NoError(t, w.Store(proj.Root).Save(cur))
	_, e := w.PrepareInto(proj, p, []int{1}, nil)
	require.NotNil(t, e)
	assert.Equal(t, "changed", e.Code)
	cur.Decision = &proposal.Decision{Applied: []int{1}}
	require.NoError(t, w.Store(proj.Root).Save(cur))
	_, e = w.SetHunkSeen(proj.Root, cur, cur.Hunks[0])
	require.NotNil(t, e)
	assert.Equal(t, "already_applied", e.Code)
}

// A cancellation arriving after the model reply but while another process
// holds the proposal lock must not mutate the proposal later.
func TestReviseCancelledWhileWaitingForLock(t *testing.T) {
	proj := commitRepo(t)
	env := newCascadeEnv(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replied := make(chan struct{})
	env.check.reply = func(w http.ResponseWriter, body map[string]any) {
		chatReply(`{"find":"at most 4", "replace":"at most 7", "explanation":"new bound"}`)(w, body)
		close(replied)
	}
	p := &proposal.Proposal{Kind: proposal.KindAnalysis, Hunks: []finding.Hunk{{N: 1, File: "c.spec.md", Find: "at most 4", Replace: "at most 6"}}}
	require.NoError(t, env.wf.Store(proj.Root).Create(p))
	lock, err := project.AcquireLock(proj.Root, "another reviewer")
	require.NoError(t, err)
	defer lock.Release()
	ck, e := env.wf.OpenChecker(ctx, false)
	require.Nil(t, e)
	done := make(chan *Error, 1)
	go func() { _, e := env.wf.ReviseHunk(ck, proj.Root, p, p.Hunks[0], "raise to seven"); done <- e }()
	select {
	case <-replied:
	case <-time.After(2 * time.Second):
		t.Fatal("canned revision never replied")
	}
	// Allow the response to reach the decision lock, which stays held.
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case e := <-done:
		require.NotNil(t, e)
		assert.Equal(t, "cancelled", e.Code)
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled revision kept waiting")
	}
	stored, err := env.wf.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.Revision, stored.Revision)
	assert.Equal(t, "at most 6", stored.Hunks[0].Replace)
}

func TestPublishedCheckCancellationKeepsHonestFailureMessage(t *testing.T) {
	proj := commitRepo(t)
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
	env := newCascadeEnv(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env.check.reply = func(w http.ResponseWriter, body map[string]any) {
		cancel()
		chatReply(`{"summary":"consistent","findings":[],"questions":[]}`)(w, body)
	}
	res, e := env.wf.Propose(ctx, proj, ProposeRequest{Names: []string{"A.One"}, SourceKind: "agent", Why: "raise capacity"})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 1)
	p := res.Proposals[0]
	assert.Equal(t, "failed", p.CheckState)
	assert.Equal(t, "Check cancelled; proposal remains pending", p.CheckMessage)
	assert.Equal(t, proposal.StatusPending, p.Status)
	assert.NotContains(t, p.CheckMessage, "nothing was filed")
}

func TestCheckRetryDiscoversNewSpecFile(t *testing.T) {
	proj := commitRepo(t)
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
	w := &Workflow{Getenv: func(string) string { return "" }}
	res, e := w.Propose(context.Background(), proj, ProposeRequest{Names: []string{"A.One"}, SourceKind: "agent", Why: "raise capacity"})
	require.Nil(t, e)
	env := newCascadeEnv(t, "")
	write(t, proj.Root, "new.spec.md", "# New context\n\nAn additional rule.\n")
	p, e := env.wf.CheckPublished(context.Background(), proj, res.Proposals[0].ID, false, nil)
	require.NotNil(t, e)
	assert.Equal(t, "stale", e.Code)
	assert.Equal(t, "stale", p.CheckState)
	assert.Empty(t, env.check.requests(), "a cached Project cannot omit new files on retry")
}

func TestCheckCompletionDiscoversNewFileUnderLock(t *testing.T) {
	proj := commitRepo(t)
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
	env := newCascadeEnv(t, "")
	env.check.reply = chatReply(`{"summary":"consistent","findings":[],"questions":[]}`)
	env.wf.BeforeCommit = func() {
		if len(env.check.requests()) > 0 {
			write(t, proj.Root, "late.spec.md", "# Late context\n\nAn additional rule.\n")
		}
	}
	res, e := env.wf.Propose(context.Background(), proj, ProposeRequest{Names: []string{"A.One"}, SourceKind: "agent", Why: "raise capacity"})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 1)
	assert.Equal(t, "stale", res.Proposals[0].CheckState)
	assert.True(t, res.Proposals[0].ChecksBlocking())
	assert.Len(t, env.check.requests(), 1)
}

func TestCommitSeenCannotRebaseStaleSelectionOntoConcurrentCommit(t *testing.T) {
	for _, duringHook := range []bool{false, true} {
		t.Run(map[bool]string{false: "after planning", true: "during commit hook"}[duringHook], func(t *testing.T) {
			proj := commitRepo(t)
			w := &Workflow{}
			base := strings.TrimSpace(git(t, proj.Root, "rev-parse", "HEAD"))
			externalText := strings.Replace(commitSpec, "within 5", "within 99", 1)
			write(t, proj.Root, "c.spec.md", externalText)
			git(t, proj.Root, "add", "c.spec.md")
			git(t, proj.Root, "commit", "-q", "-m", "concurrent change")
			external := strings.TrimSpace(git(t, proj.Root, "rev-parse", "HEAD"))
			git(t, proj.Root, "reset", "--hard", base)
			write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
			b, e := w.LoadBaseline(proj, baseline.Source{}, true)
			require.Nil(t, e)
			loaded, err := proj.Load(nil)
			require.NoError(t, err)
			groups := Changes(b.Spec, loaded.Spec)
			require.Len(t, groups, 1)
			if duringHook {
				hook := filepath.Join(filepath.Dir(proj.Root), ".git", "hooks", "commit-msg")
				require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\ngit update-ref HEAD "+external+" "+base+"\n"), 0o755))
			} else {
				w.BeforeCommit = func() { git(t, proj.Root, "update-ref", "HEAD", external, base) }
			}
			_, e = w.CommitChangesSeen(proj, []string{groups[0].ID}, "selected change", base, snapshotHashes(loaded.Spec))
			require.NotNil(t, e)
			assert.Equal(t, "changed", e.Code)
			assert.Equal(t, external, strings.TrimSpace(git(t, proj.Root, "rev-parse", "HEAD")))
			assert.Equal(t, externalText, git(t, proj.Root, "show", "HEAD:spec/c.spec.md"))
			assert.Equal(t, "2", strings.TrimSpace(git(t, proj.Root, "rev-list", "--count", "HEAD")), "no stale commit was installed")
		})
	}
}

func TestCommitSeenRefusesHookBranchSwitchAndDetachedHEAD(t *testing.T) {
	for _, detach := range []bool{false, true} {
		t.Run(map[bool]string{false: "branch switched in hook", true: "detached"}[detach], func(t *testing.T) {
			proj := commitRepo(t)
			w := &Workflow{}
			base := strings.TrimSpace(git(t, proj.Root, "rev-parse", "HEAD"))
			git(t, proj.Root, "branch", "other", base)
			if detach {
				git(t, proj.Root, "checkout", "--detach", "-q", base)
			} else {
				hook := filepath.Join(filepath.Dir(proj.Root), ".git", "hooks", "commit-msg")
				require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\ngit symbolic-ref HEAD refs/heads/other\n"), 0o755))
			}
			write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
			b, e := w.LoadBaseline(proj, baseline.Source{}, true)
			require.Nil(t, e)
			loaded, err := proj.Load(nil)
			require.NoError(t, err)
			groups := Changes(b.Spec, loaded.Spec)
			_, e = w.CommitChangesSeen(proj, []string{groups[0].ID}, "selected", base, snapshotHashes(loaded.Spec))
			require.NotNil(t, e)
			if detach {
				assert.Contains(t, e.Message, "create or switch to a branch")
			} else {
				assert.Equal(t, "changed", e.Code)
			}
			assert.Equal(t, base, strings.TrimSpace(git(t, proj.Root, "rev-parse", "main")))
			assert.Equal(t, base, strings.TrimSpace(git(t, proj.Root, "rev-parse", "other")))
			assert.Equal(t, base, strings.TrimSpace(git(t, proj.Root, "rev-parse", "HEAD")))
		})
	}
}

func TestCompletedRequiredCheckRefusesChangedApplicationContext(t *testing.T) {
	for _, mode := range []string{"disk", "new file", "buffer"} {
		t.Run(mode, func(t *testing.T) {
			proj := commitRepo(t)
			write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
			env := newCascadeEnv(t, "")
			env.check.reply = chatReply(`{"summary":"consistent","findings":[],"questions":[]}`)
			res, e := env.wf.Propose(context.Background(), proj, ProposeRequest{Names: []string{"A.One"}, SourceKind: "agent", Why: "raise capacity"})
			require.Nil(t, e)
			p := res.Proposals[0]
			require.Equal(t, "complete", p.CheckState)
			overlay := map[string][]byte{}
			changed := strings.Replace(commitSpec, "within 5", "within 99", 1)
			switch mode {
			case "disk":
				write(t, proj.Root, "c.spec.md", changed)
			case "new file":
				write(t, proj.Root, "new.spec.md", "# New context\n")
			case "buffer":
				overlay["c.spec.md"] = []byte(changed)
			}
			_, e = env.wf.PrepareInto(proj, p, []int{p.Primary.N}, overlay)
			require.NotNil(t, e)
			assert.Equal(t, "stale_check", e.Code)
			if mode != "buffer" {
				_, e = env.wf.Apply(proj.Root, p.ID, nil, "human", false)
				require.NotNil(t, e)
				assert.Equal(t, "stale_check", e.Code)
			}
			stored, err := env.wf.Store(proj.Root).Load(p.ID)
			require.NoError(t, err)
			assert.Equal(t, proposal.StatusPending, stored.Status)
			assert.Nil(t, stored.Decision)
		})
	}
}

func TestSelectiveCommitUnbornBranch(t *testing.T) {
	proj := commitRepo(t)
	git(t, proj.Root, "symbolic-ref", "HEAD", "refs/heads/unborn")
	w := &Workflow{}
	b, e := w.LoadBaseline(proj, baseline.Source{}, true)
	require.Nil(t, e)
	require.Equal(t, baseline.StateUnborn, b.State)
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	var ids []string
	for _, c := range Changes(b.Spec, loaded.Spec) {
		ids = append(ids, c.ID)
	}
	// spec.toml is new too: it stays uncommitted, but the snapshot covers it.
	others, err := OtherChanges(proj, b)
	require.NoError(t, err)
	res, e := w.CommitChangesSeen(proj, ids, "initial spec", "", CommitSnapshot(loaded.Spec, others))
	require.Nil(t, e)
	assert.Equal(t, res.Commit, strings.TrimSpace(git(t, proj.Root, "rev-parse", "HEAD")))
	assert.Equal(t, "1", strings.TrimSpace(git(t, proj.Root, "rev-list", "--count", "HEAD")))
}
