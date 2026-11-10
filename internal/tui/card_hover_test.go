// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSidebarHoverPreviewsWithoutMovingPersistentState(t *testing.T) {
	a := newTestApp(t, testOptions(demoProject(t)))
	a.width, a.height = 140, 40
	p := &proposal.Proposal{ID: "fixture"}
	first := &card{kind: cardEdit, p: p, key: "h1", hunk: finding.Hunk{N: 1, File: "demo.spec.md", Find: "above zero", Replace: "at most 5 s"}}
	second := &card{kind: cardEdit, p: p, key: "h2", hunk: finding.Hunk{N: 2, File: "demo.spec.md", Find: "Every fragment", Replace: "Each fragment"}}
	a.side.cards = []*card{first, second}
	a.side.starts, a.side.ends = []int{1, 5}, []int{4, 8}
	a.sideShown = true
	a.layout()
	here, history := a.here(), len(a.back)
	sx, _ := a.sideRect()
	a.onMouse(tea.MouseMotionMsg{X: sx + 2, Y: 6})
	require.Equal(t, 0, a.side.sel)
	require.False(t, a.sideFocus)
	require.Equal(t, here, a.here())
	require.Equal(t, history, len(a.back))
	require.Equal(t, second.id(), a.previewCard().id())
	old, new := a.fixPreview("demo.spec.md")
	require.NotEmpty(t, old)
	require.NotEmpty(t, new)
	a.onMouse(tea.MouseMotionMsg{X: 20, Y: 0})
	require.Empty(t, a.sideHoverID)
	require.Equal(t, first.id(), a.previewCard().id(), "leaving restores the selected preview")
}
