// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/stretchr/testify/require"
)

func TestFilledCardPreservesSidebarCellBudget(t *testing.T) {
	th := NewTheme(true, false)
	for _, selected := range []bool{false, true} {
		for _, child := range []bool{false, true} {
			lines := filledCard([]string{"a title", "a description"}, 31, th.FixOrigin, th, child, selected)
			require.Len(t, lines, 2)
			for _, line := range lines {
				require.Equal(t, 35, ansi.StringWidth(line), "card line must retain its reserved width")
			}
		}
	}
}

func TestCardOriginNeedsExplicitSourceKind(t *testing.T) {
	th := NewTheme(true, false)
	require.Equal(t, th.Human.GetForeground(), cardOriginStyle(th, "human").GetForeground())
	require.Equal(t, th.Agent.GetForeground(), cardOriginStyle(th, "agent").GetForeground())
	require.Equal(t, th.Structure.GetForeground(), cardOriginStyle(th, "").GetForeground())

	p := &proposal.Proposal{Kind: "change", Author: "Chris", SourceKind: "human"}
	require.Equal(t, "Your change", groupOriginTitle(p))
	p.SourceKind = "agent"
	require.Equal(t, "Agent proposal · Chris", groupOriginTitle(p))
	p.SourceKind = ""
	require.Equal(t, "Proposal · change", groupOriginTitle(p))
	p.SourceKind, p.Author = "agent", "agent\u202e7"
	require.NotContains(t, groupOriginTitle(p), "\u202e", "source labels must not allow bidi reordering")
}

func TestAgentChildrenDoNotSettleUnappliedParent(t *testing.T) {
	a := &App{side: &sidebar{state: checksState{Decided: map[string]map[string]string{
		"agent": {hunkKey(2): decAccepted},
	}}}}
	p := &proposal.Proposal{ID: "agent", SourceKind: "agent", Primary: &proposal.Primary{N: 1}, Hunks: []finding.Hunk{{N: 2}}}
	require.False(t, a.proposalSettled(p), "deciding a generated child fix does not apply the parent")
	require.Equal(t, []int{2}, a.acceptedOf(p), "the parent primary is not recorded as applied")
	p.Decision = &proposal.Decision{Applied: []int{1}}
	require.True(t, a.proposalSettled(p), "once the parent primary is applied, settled children may be recorded")
	require.Equal(t, []int{1, 2}, a.acceptedOf(p))
}

func TestAgentChildCannotApplyBeforeParent(t *testing.T) {
	a := &App{}
	p := &proposal.Proposal{ID: "agent", SourceKind: "agent", Primary: &proposal.Primary{N: 1}, Hunks: []finding.Hunk{{N: 2}}}
	c := &card{kind: cardEdit, p: p, hunk: finding.Hunk{N: 2}}
	require.True(t, a.agentChildNeedsParent(c))
	p.Decision = &proposal.Decision{Applied: []int{1}}
	require.False(t, a.agentChildNeedsParent(c), "child is available after the parent has applied")
	p.Decision.Applied = nil
	p.Primary.InWorktree = true
	require.False(t, a.agentChildNeedsParent(c), "a primary already in the working tree permits its child")
}

func TestAgentParentApplyHonorsSkippedChildren(t *testing.T) {
	a := &App{side: &sidebar{state: checksState{Decided: map[string]map[string]string{
		"agent": {hunkKey(3): decDismissed},
	}}}}
	p := &proposal.Proposal{
		ID: "agent", SourceKind: "agent", Primary: &proposal.Primary{N: 1},
		Hunks: []finding.Hunk{{N: 2}, {N: 3}},
	}
	require.Equal(t, []int{1, 2}, a.proposalHunksToApply(p), "a skipped child must not be replayed with parent acceptance")
	p.Decision = &proposal.Decision{Applied: []int{1, 2}}
	require.Empty(t, a.proposalHunksToApply(p), "recorded successes must not be replayed")
}
