// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// enteredWaitContext signals after the observer has completed its first pass,
// exactly when Store.Wait selects for a change. No scheduling sleep is needed.
type enteredWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *enteredWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestWaitObservesSourceChangeWithoutApproval(t *testing.T) {
	for _, checked := range []bool{false, true} {
		t.Run(map[bool]string{false: "optional human", true: "required check"}[checked], func(t *testing.T) {
			proj := commitRepo(t)
			p := recoveryProposal(t, proj)
			wf := &Workflow{}
			if !checked {
				p.SourceKind, p.CheckState, p.CheckFiles = "human", "", nil
				require.NoError(t, wf.Store(proj.Root).Save(p))
			}
			base, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx := &enteredWaitContext{Context: base, entered: make(chan struct{})}
			type answer struct {
				ps  []*proposal.Proposal
				err error
			}
			done := make(chan answer, 1)
			go func() {
				ps, err := wf.Wait(ctx, proj.Root, []string{p.ID}, 10*time.Millisecond)
				done <- answer{ps, err}
			}()
			select {
			case <-ctx.entered:
			case <-base.Done():
				t.Fatal("wait did not start")
			}
			write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 8", 1))
			got := <-done
			require.NoError(t, got.err)
			require.Len(t, got.ps, 1)
			require.NotNil(t, got.ps[0].StalePrimary)
			assert.Contains(t, got.ps[0].StalePrimary.Reason, "$A.One")
			assert.Equal(t, proposal.StatusPending, got.ps[0].Status)
			assert.Nil(t, got.ps[0].Decision)
			assert.False(t, got.ps[0].Decided())
			assert.Equal(t, got.ps[0].StalePrimary, readPending(t, proj.Root, p.ID).StalePrimary)
			// Later waiters receive the same durable result, even after a revert.
			write(t, proj.Root, "c.spec.md", commitSpec)
			late, err := wf.Wait(base, proj.Root, []string{p.ID}, time.Hour)
			require.NoError(t, err)
			assert.Equal(t, got.ps[0].StalePrimary, late[0].StalePrimary)
		})
	}
}

func TestWaitDoesNotConfuseSnapshotOrAppliedPrimaryWithStaleness(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	wf := &Workflow{}
	write(t, proj.Root, "other.spec.md", "# Other\n\nChanged context.\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	got, err := wf.Wait(ctx, proj.Root, []string{p.ID}, 5*time.Millisecond)
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, got[0].StalePrimary)
	loaded, err := project.Load(proj.Root, nil)
	require.NoError(t, err)
	snapshotErr := CheckSnapshot(got[0], loaded.Spec)
	require.NotNil(t, snapshotErr)
	assert.Equal(t, "stale_check", snapshotErr.Code)
	p.Decision = &proposal.Decision{Applied: []int{1}}
	require.NoError(t, wf.Store(proj.Root).Save(p))
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	got, err = wf.Wait(ctx, proj.Root, []string{p.ID}, 5*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, got[0].StalePrimary, "applied primary must not match old text again")
}

func TestStaleMarkerClearsOnlyAfterSuccessfulExplicitRecheck(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	p.Hunks = []finding.Hunk{{N: 2, File: "c.spec.md", Find: "Prose between", Replace: "Text between"}}
	wf := &Workflow{Getenv: func(string) string { return "" }}
	require.NoError(t, wf.Store(proj.Root).Save(p))
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 8", 1))
	_, e := wf.Apply(proj.Root, p.ID, nil, "human", false)
	require.NotNil(t, e)
	assert.Equal(t, "stale", e.Code, "primary gate precedes snapshot gate")
	p = readPending(t, proj.Root, p.ID)
	require.NotNil(t, p.StalePrimary)
	h := p.Hunks[0]
	h.Replace = "Revised prose between"
	p, e = wf.SetHunk(proj.Root, p.ID, h)
	require.Nil(t, e)
	require.NotNil(t, p.StalePrimary, "child revision does not validate primary")
	write(t, proj.Root, "c.spec.md", commitSpec)
	_, e = wf.RefreshForAutoApprove(context.Background(), proj, p.ID, false)
	require.NotNil(t, e)
	assert.Equal(t, "stale", e.Code, "automatic context refresh cannot clear primary evidence")
	_, e = wf.CheckPublished(context.Background(), proj, p.ID, false, nil)
	require.NotNil(t, e)
	assert.Equal(t, "stale", e.Code, "retry cannot clear prior primary evidence")
	_, e = wf.RecheckPublished(context.Background(), proj, p.ID, false, nil, nil)
	require.NotNil(t, e, "missing provider is a failed explicit recheck")
	require.NotNil(t, readPending(t, proj.Root, p.ID).StalePrimary)
	env := newCascadeEnv(t, "")
	env.check.reply = chatReply(`{"summary":"fresh","findings":[],"questions":[]}`)
	got, e := env.wf.RecheckPublished(context.Background(), proj, p.ID, true, nil, nil)
	require.Nil(t, e)
	assert.Nil(t, got.StalePrimary)
	assert.Equal(t, "complete", got.CheckState)
	assert.Equal(t, p.ID, got.ID)
}

func TestWaitRecoveryPriorityAndLiveWriterClassification(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	p.StalePrimary = &proposal.StalePrimary{Reason: "stale source"}
	wf := &Workflow{}
	require.NoError(t, wf.Store(proj.Root).Save(p))
	lock, err := project.AcquireLock(proj.Root, "live approval")
	require.NoError(t, err)
	defer lock.Release()
	intent, e := wf.beginApproval(proj.Root, p, []int{1}, []int{1}, nil)
	require.Nil(t, e)
	for _, invoke := range []func() *Error{
		func() *Error { return wf.CheckRecoveryBeforeLock(proj.Root, p.ID) },
		func() *Error {
			_, e := wf.Propose(context.Background(), proj, ProposeRequest{SourceKind: "agent", Why: "new"})
			return e
		},
		func() *Error {
			_, e := wf.RecheckPublished(context.Background(), proj, p.ID, false, nil, nil)
			return e
		},
	} {
		e := invoke()
		require.NotNil(t, e)
		assert.Equal(t, "lock_held", e.Code)
		assert.Equal(t, ExitLock, e.Exit)
	}
	_, err = wf.Wait(context.Background(), proj.Root, []string{p.ID}, time.Second)
	var we *Error
	require.True(t, errors.As(err, &we))
	assert.Equal(t, "lock_held", we.Code)
	require.NoError(t, lock.Release())
	for _, archived := range []bool{false, true} {
		if archived {
			_, e = wf.Recover(proj.Root, true, "inspected")
			require.Nil(t, e)
		}
		_, err = wf.Wait(context.Background(), proj.Root, []string{p.ID}, time.Second)
		require.True(t, errors.As(err, &we))
		assert.Equal(t, "recovery_required", we.Code)
		assert.NotNil(t, readPending(t, proj.Root, p.ID).StalePrimary)
	}
	require.NoError(t, os.Remove(filepath.Join(proj.Root, recoveryDir, intent.ID+".json")))
	require.Nil(t, wf.CheckRecoveryBeforeLock(proj.Root, p.ID), "finished owner no longer leaves interruption evidence")
}

func TestPrimaryMarkerSaveFailureIsNotReportedAsDurableStale(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	wf := &Workflow{}
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 8", 1))
	loaded, err := project.Load(proj.Root, nil)
	require.NoError(t, err)
	lock, err := project.AcquireLock(proj.Root, "test observation")
	require.NoError(t, err)
	defer lock.Release()
	path := wf.Store(proj.Root).Path(p.ID)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o755))
	e := wf.checkPrimaryLocked(proj.Root, p, loaded)
	require.NotNil(t, e)
	assert.Equal(t, "internal", e.Code)
	assert.Nil(t, p.StalePrimary)
}

func TestRejectedBatchCheckContinuesToLaterPublishedCard(t *testing.T) {
	proj := commitRepo(t)
	env := newCascadeEnv(t, "")
	var calls atomic.Int32
	rejected := make(chan string, 1)
	env.check.reply = func(w http.ResponseWriter, body map[string]any) {
		if calls.Add(1) == 1 {
			ps, err := env.wf.Store(proj.Root).List()
			require.NoError(t, err)
			require.Len(t, ps, 2)
			for _, p := range ps {
				if p.CheckState == "running" {
					rejected <- p.ID
					_, e := env.wf.Reject(proj.Root, p.ID, "declined during check", "Chris")
					require.Nil(t, e)
				}
			}
		}
		chatReply(`{"summary":"checked","findings":[],"questions":[]}`)(w, body)
	}
	write(t, proj.Root, "c.spec.md", strings.ReplaceAll(strings.Replace(commitSpec, "at most 4", "at most 6", 1), "within 5 s", "within 7 s"))
	res, e := env.wf.Propose(context.Background(), proj, ProposeRequest{Names: []string{"A.One", "B.Two"}, SourceKind: "agent", Why: "update", Full: true})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 2)
	assert.Equal(t, int32(2), calls.Load())
	rejectedID := <-rejected
	for _, p := range res.Proposals {
		if p.ID == rejectedID {
			assert.Equal(t, proposal.StatusRejected, p.Status)
			assert.Equal(t, "declined during check", p.Decision.Reason)
			assert.False(t, Eligible(p))
		} else {
			assert.Equal(t, "complete", p.CheckState)
		}
	}
}

func TestPartialResidualGuidancePreservesAppliedReceipt(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	p.Decision = &proposal.Decision{Applied: []int{1, 2}}
	p.Hunks = []finding.Hunk{{N: 2, File: "c.spec.md", Find: "already", Replace: "done"}, {N: 3, File: "c.spec.md", Find: "Prose between", Replace: "Text between"}}
	wf := &Workflow{}
	require.NoError(t, wf.Store(proj.Root).Save(p))
	applied := strings.Replace(commitSpec, "at most 4", "at most 6", 1)
	write(t, proj.Root, "c.spec.md", applied)
	write(t, proj.Root, "other.spec.md", "# New context\n")
	_, e := wf.Apply(proj.Root, p.ID, nil, "human", false)
	require.NotNil(t, e)
	assert.Equal(t, "stale_check", e.Code)
	assert.Contains(t, e.Message, "spec reject "+p.ID)
	assert.Contains(t, e.Message, "new proposal")
	assert.NotContains(t, e.Message, "recheck proposal")
	_, e = wf.RecheckPublished(context.Background(), proj, p.ID, false, nil, nil)
	require.NotNil(t, e)
	assert.Equal(t, "retry_unavailable", e.Code)
	assert.Equal(t, ResidualGuidance(p), e.Message)
	h := p.Hunks[1]
	h.Replace = "Fresh text between"
	_, e = wf.SetHunk(proj.Root, p.ID, h)
	require.Nil(t, e)
	_, e = wf.Apply(proj.Root, p.ID, nil, "human", false)
	require.NotNil(t, e)
	assert.Equal(t, "stale_check", e.Code)
	closed, e := wf.Reject(proj.Root, p.ID, "refile residual", "Chris")
	require.Nil(t, e)
	assert.Equal(t, proposal.StatusApplied, closed.Status)
	assert.Equal(t, []int{1, 2}, closed.Decision.Applied)
	assert.Contains(t, closed.Decision.Reason, "Remaining edits rejected: refile residual")
	assert.Equal(t, applied, read(t, proj.Root, "c.spec.md"))
}

// Source replacement occurs exactly during publication, before the first check.
// Unique deterministic IDs avoid entropy or timer dependencies in this boundary.
type changingIDReader struct {
	calls  byte
	change func()
}

func (r *changingIDReader) Read(b []byte) (int, error) {
	if r.calls == 0 {
		r.change()
	}
	r.calls++
	for i := range b {
		b[i] = r.calls
	}
	return len(b), nil
}

func TestQueuedStalePrimaryDoesNotAbortPublishedBatch(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	require.NoError(t, proposal.NewStore(proj.Root).Remove(p.ID))
	loaded, err := project.Load(proj.Root, nil)
	require.NoError(t, err)
	anchor := loaded.Spec.Lookup("B.Two")[0]
	old := loaded.Spec.File(anchor.File).Text(anchor.BodyRange)
	other := &proposal.Primary{N: 1, Action: specctx.ActionChange, Name: "$B.Two", File: anchor.File, OldText: old, NewText: strings.Replace(old, "within 5 s", "within 7 s", 1)}
	wf := &Workflow{IDs: &changingIDReader{change: func() { write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 8", 1)) }}}
	res, e := wf.proposeExternal(context.Background(), proj, ProposeRequest{SourceKind: "agent", Why: "update"}, loaded.Spec, []*pendingEdit{
		{primary: p.Primary, change: &specctx.Change{Name: "A.One"}},
		{primary: other, change: &specctx.Change{Name: "B.Two"}},
	}, nil, &ProposeResult{})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 2)
	assert.Equal(t, "queued", res.Proposals[0].CheckState)
	require.NotNil(t, res.Proposals[0].StalePrimary)
	assert.Equal(t, "stale", res.Proposals[1].CheckState, "later published check is processed instead of stranded queued")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ps, err := wf.Wait(ctx, proj.Root, []string{res.Proposals[0].ID, res.Proposals[1].ID}, time.Second)
	require.NoError(t, err)
	require.Len(t, ps, 2)
	assert.Equal(t, proposal.StatusPending, ps[0].Status)
}

func TestWaitDeliversExplicitDecisionAfterRecoveryAcknowledgment(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	p.StalePrimary = &proposal.StalePrimary{Reason: "source changed"}
	wf := &Workflow{}
	require.NoError(t, wf.Store(proj.Root).Save(p))
	_, e := wf.beginApproval(proj.Root, p, []int{1}, []int{1}, nil)
	require.Nil(t, e)
	_, e = wf.Recover(proj.Root, true, "inspected HEAD and files")
	require.Nil(t, e)
	closed, e := wf.Reject(proj.Root, p.ID, "close ambiguous proposal", "Chris")
	require.Nil(t, e)
	ps, err := wf.Wait(context.Background(), proj.Root, []string{p.ID}, time.Second)
	require.NoError(t, err)
	require.Len(t, ps, 1)
	assert.Equal(t, proposal.StatusRejected, ps[0].Status)
	assert.Equal(t, closed.Decision, ps[0].Decision)
	assert.NotNil(t, wf.CheckRecovery(proj.Root, p.ID), "archived mutation fence remains intact")
}

func TestWaitRelocatedPrimaryDoesNotInventUnsavedMetadata(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	require.NoError(t, os.Rename(filepath.Join(proj.Root, "c.spec.md"), filepath.Join(proj.Root, "moved.spec.md")))
	wf := &Workflow{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	ps, err := wf.Wait(ctx, proj.Root, []string{p.ID}, 5*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Len(t, ps, 1)
	assert.Nil(t, ps[0].StalePrimary)
	stored := readPending(t, proj.Root, p.ID)
	assert.Equal(t, stored, ps[0], "observer returns exactly the durable revision")
}

// A stale primary found while preparing against buffers persists only the
// marker: what the buffers hold has had no save and no receipt.
func TestPrepareStaleMarkerLeavesBufferedStateOffDisk(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	p.Hunks = []finding.Hunk{{N: 2, File: "c.spec.md", Find: "Prose between", Replace: "Text between"}}
	wf := &Workflow{}
	require.NoError(t, wf.Store(proj.Root).Save(p))
	seen, err := p.Clone()
	require.NoError(t, err)
	seen.Decision = &proposal.Decision{Accepted: []int{2}, Applied: []int{2}}
	seen.CheckFiles = map[string]string{"c.spec.md": "hash of the buffer"}
	overlay := map[string][]byte{"c.spec.md": []byte(strings.Replace(commitSpec, "at most 4", "at most 8", 1))}

	_, e := wf.PrepareInto(proj, seen, []int{1}, overlay)
	require.NotNil(t, e)
	assert.Equal(t, "stale", e.Code)

	stored := readPending(t, proj.Root, p.ID)
	require.NotNil(t, stored.StalePrimary)
	assert.Nil(t, stored.Decision, "no receipt without a save")
	assert.Equal(t, p.CheckFiles, stored.CheckFiles, "no buffer hashes without a save")
	assert.Equal(t, p.Revision+1, stored.Revision)
	require.NotNil(t, e.Proposal)
	assert.Equal(t, stored.Revision, e.Proposal.Revision)
}

func TestPreparePersistsPrimaryFailureUnderDecisionLock(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	wf := &Workflow{LockWait: time.Hour}
	lock, err := project.AcquireLock(proj.Root, "live writer")
	require.NoError(t, err)
	_, e := wf.PrepareInto(proj, p, []int{1}, nil)
	require.NotNil(t, e)
	assert.Equal(t, "lock_held", e.Code)
	require.NoError(t, lock.Release())
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 8", 1))
	_, e = wf.PrepareInto(proj, p, []int{1}, nil)
	require.NotNil(t, e)
	assert.Equal(t, "stale", e.Code)
	stored := readPending(t, proj.Root, p.ID)
	require.NotNil(t, stored.StalePrimary)
	assert.Nil(t, stored.Decision)
	assert.Equal(t, proposal.StatusPending, stored.Status)
}

// A CLI approval writes its receipt while it holds .spec/lock and its
// interruption evidence: a waiter that reads the receipt mid-write, pending
// or already decided, must deliver the decision once the approval lets go,
// not exit lock_held.
func TestWaitDeliversDecisionOfLiveApproval(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	wf := &Workflow{}
	lock, err := project.AcquireLock(proj.Root, "spec approve "+p.ID)
	require.NoError(t, err)
	defer lock.Release()
	_, e := wf.beginApproval(proj.Root, p, []int{1}, []int{1}, nil)
	require.Nil(t, e)

	type waited struct {
		ps  []*proposal.Proposal
		err error
	}
	done := make(chan waited, 1)
	go func() {
		ps, err := wf.Wait(context.Background(), proj.Root, []string{p.ID}, 20*time.Millisecond)
		done <- waited{ps, err}
	}()
	quiet := func(what string) {
		t.Helper()
		select {
		case got := <-done:
			t.Fatalf("the wait returned while the approval held the lock (%s): %v", what, got.err)
		case <-time.After(300 * time.Millisecond):
		}
	}
	quiet("receipt pending")
	p.Status = proposal.StatusApplied
	p.Decision = &proposal.Decision{Accepted: []int{1}, Applied: []int{1}, By: "human", At: time.Now().UTC()}
	require.NoError(t, wf.Store(proj.Root).Save(p))
	quiet("receipt decided")
	require.NoError(t, finishRecovery(proj.Root))
	require.NoError(t, lock.Release())
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Len(t, got.ps, 1)
		assert.Equal(t, proposal.StatusApplied, got.ps[0].Status)
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not deliver the decision after the approval finished")
	}
}
