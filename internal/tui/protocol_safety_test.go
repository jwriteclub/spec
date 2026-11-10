// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

func TestCommitJobCannotLoseCompletionToCancel(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	_ = a.startCommitJob(func(context.Context) func(*App) tea.Cmd { return nil })
	seq := a.job.seq
	assert.True(t, a.cancelJob())
	require.NotNil(t, a.job)
	assert.NotContains(t, a.jobText(), "^C cancels")
	finished := false
	a.onJobDone(jobDoneMsg{seq: seq, finish: func(*App) tea.Cmd { finished = true; return nil }})
	assert.True(t, finished)
	assert.Nil(t, a.job)
}

func TestDisabledPrimaryAcceptAndCommitIncludeHaveNoSideEffects(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	r := &reviewScreen{p: &proposal.Proposal{Primary: &proposal.Primary{N: 1}}, nums: []int{1, 2}, state: map[int]hunkState{1: hunkAccepted}}
	r.accept(a, 1)
	assert.Equal(t, 0, r.sel, "required primary must not advance")
	s := &commitScreen{changes: []workflow.Change{{ID: "one"}}, ticked: map[string]bool{"one": true}}
	cmd := s.key(a, tea.KeyPressMsg{}, Binding{Action: ActYes}, true)
	assert.Nil(t, cmd)
	assert.Equal(t, 0, s.draftSeq, "already included must not restart drafting")
}
