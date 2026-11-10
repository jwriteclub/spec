// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
)

func TestTaskReadingRegionAndReviewMouseOffsets(t *testing.T) {
	for _, size := range []struct{ width, x, inner int }{{80, 1, 78}, {100, 1, 98}, {140, 10, 120}, {220, 30, 160}} {
		t.Run(fmt.Sprint(size.width), func(t *testing.T) {
			x, w := taskReadingRegion(size.width)
			assert.Equal(t, size.x, x)
			assert.Equal(t, size.inner, w)
			a := newTestApp(t, testOptions(demoRepo(t)))
			a.width, a.height = size.width, 80
			r := &reviewScreen{p: &proposal.Proposal{ID: "test", Primary: &proposal.Primary{N: 1, Name: "$Primary", OldText: "old", NewText: "new"}, Hunks: []finding.Hunk{{N: 2, File: "demo.spec.md", Find: "before", Replace: "after"}}}, nums: []int{1, 2}, state: map[int]hunkState{1: hunkAccepted}, stale: map[int]string{}}
			lines := r.view(a, size.width, 70)
			for _, line := range lines {
				assert.LessOrEqual(t, ansi.StringWidth(line), size.width)
				if line != "" {
					assert.True(t, strings.HasPrefix(line, strings.Repeat(" ", size.x)))
				}
			}
			assert.Contains(t, strings.Join(lines, "\n"), "Required with Apply")
			y := r.starts[2]
			r.mouse(a, tea.MouseClickMsg{X: size.x - 1, Y: y, Button: tea.MouseLeft})
			assert.Equal(t, 0, r.sel, "outside reading region is inert")
			r.mouse(a, tea.MouseClickMsg{X: size.x + 3, Y: y, Button: tea.MouseLeft})
			assert.Equal(t, 1, r.sel, "centered click selects the correct hunk")
		})
	}
}

func TestLongReviewScrollKeepsSelectionAndSummary(t *testing.T) {
	a := newTestApp(t, testOptions(demoRepo(t)))
	a.width, a.height = 80, 24
	r := &reviewScreen{p: &proposal.Proposal{ID: "long", Primary: &proposal.Primary{N: 1, Name: "$Long", OldText: strings.Repeat("before line\n", 60), NewText: strings.Repeat("after line\n", 60)}, Hunks: []finding.Hunk{{N: 2, File: "demo.spec.md", Find: "old fix", Replace: "new fix"}}}, nums: []int{1, 2}, state: map[int]hunkState{1: hunkAccepted, 2: hunkAccepted}, stale: map[int]string{}}
	before := r.view(a, 80, 20)
	require.Greater(t, len(r.lines), 20)
	r.mouse(a, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	assert.Greater(t, r.scroll, 0)
	after := r.view(a, 80, 20)
	assert.Equal(t, before[:reviewHeaderRows], after[:reviewHeaderRows], "task title, selection summary and retry stay visible")
	assert.Equal(t, 0, r.sel, "reading does not select a different hunk")
	r.sel = 1
	r.reveal(a)
	after = r.view(a, 80, 20)
	assert.Contains(t, strings.Join(after, "\n"), "Selected for application")
	assert.Equal(t, 1, r.sel)
}

func TestCommitCenteredMouseDoesNotToggleMargin(t *testing.T) {
	dir := demoRepo(t)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "above zero", "of at most 5 s", 1))
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.width, a.height = 140, 40
	a.side.state.Message = "spec: selected change"
	a.openCommit()
	s, ok := a.top().(*commitScreen)
	require.True(t, ok)
	s.view(a, 140, 36)
	require.NotEmpty(t, s.changes)
	id := s.changes[0].ID
	require.True(t, s.ticked[id])
	s.mouse(a, tea.MouseClickMsg{X: 0, Y: s.starts[0], Button: tea.MouseLeft})
	assert.True(t, s.ticked[id])
	s.mouse(a, tea.MouseClickMsg{X: 13, Y: s.starts[0], Button: tea.MouseLeft})
	assert.False(t, s.ticked[id])
}
