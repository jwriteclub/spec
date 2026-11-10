// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

func TestPartialResidualBlockersOfferExplicitCloseAndRefile(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	loaded, err := project.Load(dir, nil)
	require.NoError(t, err)
	p.CheckState = "complete"
	p.CheckFiles = map[string]string{}
	for _, file := range loaded.Spec.Files {
		p.CheckFiles[file.Path] = proposal.FileHash(file.Source())
	}
	p.Decision = &proposal.Decision{Applied: []int{1}}
	require.NoError(t, proposal.NewStore(dir).Save(p))
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.cur.Ed.ReplaceAll(strings.Replace(demoSpec, timingOld, timingNew, 1)+"\nChanged surrounding context.\n", 0)
	a.ws.Reparse()
	a.openReview(p.ID)
	r := reviewOf(a)
	require.NotNil(t, r)
	guidance := workflow.ResidualGuidance(p)
	assert.Contains(t, r.approveBlocked(a), guidance)
	assert.Equal(t, guidance, r.retryBlocked(a))
	assert.Contains(t, guidance, "spec reject "+p.ID)
	assert.NotContains(t, guidance, "revise")
	assert.Empty(t, r.primaryStale, "already-applied primary isn't stale because its old text disappeared")
}

func TestDurablePrimaryBlockerPrecedesRefreshableSnapshot(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	p.CheckState = "running"
	p.CheckFiles = map[string]string{"different.spec.md": "old"}
	p.StalePrimary = &proposal.StalePrimary{Reason: "$Frame.Timing changed; inspect the primary"}
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	r := a.newReview(p)
	assert.Equal(t, p.StalePrimary.Reason, r.approveBlocked(a))
}
