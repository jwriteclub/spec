// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cellOfText returns the screen cell of the first byte of the first
// occurrence of s in the current buffer, scrolling it into view.
func cellOfText(t *testing.T, a *App, s string) (int, int, int) {
	t.Helper()
	off := strings.Index(string(a.cur.Ed.Buffer().Bytes()), s)
	require.GreaterOrEqual(t, off, 0, s)
	x, y, ok := a.cur.Ed.CellOf(off)
	require.True(t, ok, "%q is on screen", s)
	ex, ey, _, _ := a.editorRect()
	return ex + x, ey + y, off
}

func click(x, y int, mod tea.KeyMod) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft, Mod: mod}
}

// TestMouseClickPlacesCursor is acceptance criterion 4: a click lands on a
// known byte.
func TestMouseClickPlacesCursor(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	x, y, off := cellOfText(t, a, "lensd is a Go web gateway")
	send(a, click(x, y, 0), tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	assert.Equal(t, off, a.cur.Ed.Cursor())

	// Three cells to the right is three bytes on (ASCII text).
	send(a, click(x+3, y, 0))
	assert.Equal(t, off+3, a.cur.Ed.Cursor())

	// A click in the gutter goes to the start of that row.
	ex, _, _, _ := a.editorRect()
	send(a, click(ex+1, y, 0))
	assert.Equal(t, off, a.cur.Ed.Cursor(), "the line starts with the clicked text")

	// A drag selects.
	send(a, click(x, y, 0), tea.MouseMotionMsg{X: x + 5, Y: y, Button: tea.MouseLeft}, tea.MouseReleaseMsg{X: x + 5, Y: y})
	sel, ok := a.cur.Ed.Selection()
	require.True(t, ok)
	assert.Equal(t, "lensd", a.cur.Ed.Buffer().Text(sel))
}

// TestMouseCtrlClickJumps: ctrl+click (and alt+click) on a reference jumps
// to its definition.
func TestMouseCtrlClickJumps(t *testing.T) {
	for _, mod := range []tea.KeyMod{tea.ModCtrl, tea.ModAlt} {
		a := newTestApp(t, testOptions(lensdProject(t)))
		x, y, _ := cellOfText(t, a, "$Time.Elapsed),")
		send(a, click(x+2, y, mod))
		src := string(a.cur.Ed.Buffer().Bytes())
		def := strings.Index(src, "**Def $Time.Elapsed")
		assert.Equal(t, def, a.cur.Ed.Cursor())
		require.Len(t, a.back, 1)
		send(a, key("ctrl+b"))
		assert.Equal(t, strings.Index(src, "$Time.Elapsed),")+2, a.cur.Ed.Cursor(), "back returns to the click")
	}
}

// TestMouseWheelScrolls: the wheel scrolls three rows per notch.
func TestMouseWheelScrolls(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	ex, _, _, _ := a.editorRect()
	before := a.cur.Ed.TopRow()
	send(a, tea.MouseWheelMsg{X: ex + 10, Y: 5, Button: tea.MouseWheelDown})
	after := a.cur.Ed.TopRow()
	assert.NotEqual(t, before, after)
	// Three display rows: line 1, 2, then line 3's first row (wrapped).
	assert.Equal(t, 3, after.Line)
	_, _, ok := a.cur.Ed.CursorCell()
	assert.True(t, ok, "the cursor follows onto the screen")
	send(a, tea.MouseWheelMsg{X: ex + 10, Y: 5, Button: tea.MouseWheelUp})
	assert.Equal(t, before, a.cur.Ed.TopRow())

	// Over the outline, the wheel scrolls the outline.
	a.outlineFocus = true // a narrow terminal gives the outline space on request
	a.layout()
	send(a, tea.MouseWheelMsg{X: 2, Y: 5, Button: tea.MouseWheelDown})
	assert.Equal(t, 3, a.outline.scroll)
	assert.Equal(t, before, a.cur.Ed.TopRow())
}

// TestMouseHover: resting over a token opens its preview; moving away
// closes it.
func TestMouseHover(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	x, y, _ := cellOfText(t, a, "$Time.Elapsed),")
	send(a, tea.MouseMotionMsg{X: x + 1, Y: y})
	require.Nil(t, a.popup, "not before the delay")
	send(a, hoverMsg{seq: a.hoverSeq})
	require.NotNil(t, a.popup)
	assert.True(t, a.popup.mouse)
	assert.Contains(t, plain(a), "def $Time.Elapsed (elapsed time)")
	// A stale timer does nothing.
	send(a, hoverMsg{seq: a.hoverSeq - 1})
	// Moving off the token and the box closes it.
	send(a, tea.MouseMotionMsg{X: x + 1, Y: 0})
	assert.Nil(t, a.popup)
}

// TestOutlineClick: a click on an outline entry jumps there.
func TestOutlineClick(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	a.outlineFocus = true // make the pane explicitly visible under responsive sizing
	a.layout()
	k := -1
	for i, e := range a.outline.entries {
		if e.text == "$Interval.Gap" {
			k = i
		}
	}
	require.GreaterOrEqual(t, k, 0)
	send(a, click(3, k, 0))
	src := string(a.cur.Ed.Buffer().Bytes())
	assert.Equal(t, strings.Index(src, "**Def $Interval.Gap"), a.cur.Ed.Cursor())
	assert.False(t, a.outlineFocus)
}
