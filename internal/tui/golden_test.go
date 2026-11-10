// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

// runFlow drives the App at 100x30, waits until msgs are handled, quits,
// and returns the final model. startFlow also registers failure cleanup.
func runFlow(t *testing.T, opts Options, msgs ...tea.Msg) *App {
	t.Helper()
	f := startFlow(t, opts)
	f.send(msgs...)
	return f.finish()
}

// checkFrame compares the plain and the 256-color renders with goldens.
func checkFrame(t *testing.T, a *App) {
	t.Helper()
	t.Run("plain", func(t *testing.T) { golden.RequireEqual(t, plain(a)) })
	t.Run("styled", func(t *testing.T) { golden.RequireEqual(t, styled(t, a)) })
}

// TestGolden is acceptance criterion 3: snapshots of the lensd fixture at
// 100x30 with a fixed color profile. Regenerate with
// `go test ./internal/tui -run TestGolden -update`.
func TestGolden(t *testing.T) {
	// findAndLook searches for q and opens the preview at the match.
	findAndLook := func(q string) []tea.Msg {
		msgs := []tea.Msg{key("ctrl+w")}
		msgs = append(msgs, typeText(q)...)
		return append(msgs, key("enter"), key("ctrl+l"))
	}

	t.Run("initial", func(t *testing.T) {
		a := runFlow(t, testOptions(lensdProject(t)))
		checkFrame(t, a)
	})

	t.Run("hover", func(t *testing.T) {
		a := runFlow(t, testOptions(lensdProject(t)), findAndLook("$Time.Elapsed)")...)
		require.NotNil(t, a.popup)
		checkFrame(t, a)
	})

	t.Run("jump", func(t *testing.T) {
		msgs := findAndLook("$Time.Elapsed)")
		msgs = append(msgs, key("ctrl+t"))
		a := runFlow(t, testOptions(lensdProject(t)), msgs...)
		require.Nil(t, a.popup)
		line := a.cur.Ed.Buffer().Index().Line(a.cur.Ed.Cursor())
		require.Equal(t, 46, line, "the definition of $Time.Elapsed")
		require.Len(t, a.back, 1)
		checkFrame(t, a)
	})

	t.Run("outline", func(t *testing.T) {
		a := runFlow(t, testOptions(lensdProject(t)), key("f2"), key("down"), key("down"), key("down"))
		require.True(t, a.outlineFocus)
		checkFrame(t, a)
	})

	t.Run("dangling", func(t *testing.T) {
		msgs := []tea.Msg{key("ctrl+w")}
		msgs = append(msgs, typeText("of scope.")...)
		msgs = append(msgs, key("enter"), key("ctrl+e"))
		msgs = append(msgs, typeText(" See $Frame.Timin")...)
		// Typing $ opened the completion list (step 4b); Esc closes it.
		msgs = append(msgs, key("esc"))
		a := runFlow(t, testOptions(lensdProject(t)), msgs...)
		require.True(t, a.cur.Ed.Buffer().Dirty())
		require.Nil(t, a.comp)
		checkFrame(t, a)
	})
}
