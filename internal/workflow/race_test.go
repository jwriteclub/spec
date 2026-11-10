// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// capProposal files a pending proposal that raises $A.One's cap to 6.
func capProposal(t *testing.T, proj *project.Project) *proposal.Proposal {
	t.Helper()
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	find := "One MUST hold at most 4 items."
	src := string(loaded.Spec.File("c.spec.md").Source())
	h := finding.Hunk{N: 1, File: "c.spec.md", Anchor: "$A.One", Issue: "numeric_mismatch", Severity: "inconsistent",
		Explanation: "align", Find: find, Replace: "One MUST hold at most 6 items."}
	h.Range.Start = strings.Index(src, find)
	h.Range.End = h.Range.Start + len(find)
	p := &proposal.Proposal{Author: "agent", Kind: proposal.KindAnalysis, Summary: "align the cap",
		Hunks: []finding.Hunk{h}, Status: proposal.StatusPending}
	FillBase(p, loaded.Spec)
	require.NoError(t, (&Workflow{}).Store(proj.Root).Create(p))
	return p
}

// A rejection from another process that lands after approve's last status
// check (the files are written, the commit is under way) must not be
// reported as done while the approval then records "applied" over it. The
// fake clock runs the rejection at the moment approve stamps its decision,
// after it wrote the file.
func TestRejectWhileApproveWritesIsRefused(t *testing.T) {
	proj := commitRepo(t)
	p := capProposal(t, proj)

	other := &Workflow{LockWait: time.Nanosecond}
	var rejectErr *Error
	fired := false
	w := &Workflow{Now: func() time.Time {
		if !fired && strings.Contains(read(t, proj.Root, "c.spec.md"), "at most 6 items") {
			fired = true
			_, rejectErr = other.Reject(proj.Root, p.ID, "no", "agent-9")
		}
		return time.Now()
	}}
	res, e := w.Apply(proj.Root, p.ID, nil, "human", true)
	require.Nil(t, e)
	require.True(t, fired, "the rejection ran after the file was written")
	assert.NotEmpty(t, res.Commit)

	stored, err := w.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, stored.Status, "the change was written and committed")
	require.NotNil(t, rejectErr, "the rejection is refused, not reported as done")
	assert.Equal(t, "lock_held", rejectErr.Code)
}

// The same for the UI recording its decision (Settle) while a rejection
// lands between Settle's status check and its save.
func TestRejectWhileSettleIsRefused(t *testing.T) {
	proj := commitRepo(t)
	p := capProposal(t, proj)

	other := &Workflow{LockWait: time.Nanosecond}
	var rejectErr *Error
	fired := false
	w := &Workflow{Now: func() time.Time {
		if !fired {
			fired = true
			_, rejectErr = other.Reject(proj.Root, p.ID, "no", "agent-9")
		}
		return time.Now()
	}}
	got, e := w.Settle(proj.Root, p.ID, proposal.Decision{Accepted: []int{1}, Applied: []int{1}, By: "human"})
	require.Nil(t, e)
	require.True(t, fired)
	assert.Equal(t, proposal.StatusApplied, got.Status)

	stored, err := w.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, stored.Status)
	require.NotNil(t, rejectErr, "the rejection is refused, not reported as done")
	assert.Equal(t, "lock_held", rejectErr.Code)
}
