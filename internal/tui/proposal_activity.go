// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

type proposalSpinMsg struct{}

func proposalSpinTick() tea.Cmd {
	return tea.Tick(spinEvery, func(time.Time) tea.Msg { return proposalSpinMsg{} })
}
func (a *App) checksInFlight() bool {
	for _, p := range a.pending {
		if p.CheckState == "queued" || p.CheckState == "running" {
			return true
		}
	}
	return false
}
func (a *App) ensureProposalSpinner() tea.Cmd {
	if a.proposalSpinning || !a.checksInFlight() {
		return nil
	}
	a.proposalSpinning = true
	return proposalSpinTick()
}
func (a *App) progressMark() string {
	frames := spinFrames
	if a.theme.ASCII {
		frames = []string{"|", "/", "-", "\\"}
	}
	index := a.now().UnixNano() / int64(spinEvery) % int64(len(frames))
	if index < 0 {
		index = -index
	}
	return frames[index]
}
