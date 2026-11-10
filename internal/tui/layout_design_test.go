// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/proposal"
)

func TestResponsiveRegionBudgets(t *testing.T) {
	for _, tc := range []struct {
		width, outline, editor, side int
	}{
		{80, 0, 80, 0},
		{100, 0, 100, 0},
		{140, 24, 62, 54},
		{220, 28, 124, 68},
	} {
		a := &App{width: tc.width, showOutline: true, sideShown: true}
		outline, editor, side := a.regionWidths()
		require.Equal(t, tc.outline, outline, "%d columns: outline", tc.width)
		require.Equal(t, tc.editor, editor, "%d columns: editor", tc.width)
		require.Equal(t, tc.side, side, "%d columns: sidebar column", tc.width)
		if side > 0 {
			require.GreaterOrEqual(t, editor, minEditorWidth)
		}
	}

	focused := &App{width: 80, showOutline: true, outlineFocus: true, sideShown: true}
	outline, editor, side := focused.regionWidths()
	require.Equal(t, outlineMinWidth, outline, "explicit outline focus keeps its minimum pane")
	require.Equal(t, 60, editor)
	require.Zero(t, side, "the sidebar becomes a deliberate overlay when both panes cannot fit")
}

func TestSidebarWheelScrollDoesNotSnapBackToSelection(t *testing.T) {
	a := newTestApp(t, testOptions(demoProject(t)))
	a.width, a.height = 140, 24
	a.sideShown = true
	p := &proposal.Proposal{ID: "fixture", Kind: "analysis", SourceKind: "human"}
	for i := 0; i < 12; i++ {
		a.side.cards = append(a.side.cards, &card{kind: cardQuestion, p: p, key: fmt.Sprint(i), text: fmt.Sprintf("Question %d about the requirement and its assumptions", i)})
	}
	a.sideView(54, a.bodyHeight())
	sx, _ := a.sideRect()
	a.onMouse(tea.MouseWheelMsg{X: sx + 2, Y: 5, Button: tea.MouseWheelDown})
	scrolled := a.side.scroll
	require.Greater(t, scrolled, 0)
	a.sideView(54, a.bodyHeight())
	require.Equal(t, scrolled, a.side.scroll, "rendering preserves manual scrolling")
	require.Equal(t, 0, a.side.sel, "wheel scrolling does not change the keyboard selection")
	a.side.sel = 8
	a.sideView(54, a.bodyHeight())
	require.Less(t, a.side.starts[8], a.side.scroll+a.bodyHeight(), "keyboard navigation reveals the new selection")
}
