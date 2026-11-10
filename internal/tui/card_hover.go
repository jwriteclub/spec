// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import "github.com/jwriteclub/spec/internal/finding"

// Hover is an ephemeral preview, independent of the persistent selection. A
// pointer crossing the queue never changes keyboard focus, cursor or history.
func (a *App) updateSideHover(x, y int) {
	a.sideHoverID = ""
	if !a.sideShown || len(a.screens) != 1 || a.prompt != nil || a.popup != nil && a.popup.notice {
		return
	}
	sx, sw := a.sideRect()
	if x < sx || x >= sx+sw || y < 0 || y >= a.bodyHeight() {
		return
	}
	row := y + a.side.scroll
	for k, c := range a.side.cards {
		if k < len(a.side.starts) && row >= a.side.starts[k] && row < a.side.ends[k] {
			a.sideHoverID = c.id()
			return
		}
	}
}
func (a *App) previewCard() *card {
	c := a.side.selected()
	if a.sideHoverID != "" {
		for _, candidate := range a.side.cards {
			if candidate.id() == a.sideHoverID {
				c = candidate
				break
			}
		}
	}
	if c != nil && c.kind == cardProposal && c.p.Primary != nil && !c.p.Primary.InWorktree {
		// Use the existing inline old -> new renderer for an external primary as
		// well. The copy is presentation only; the required primary stays a parent.
		pr := c.p.Primary
		copy := *c
		copy.kind = cardEdit
		copy.hunk = finding.Hunk{N: pr.N, File: pr.File, Find: pr.OldText, Replace: pr.NewText}
		return &copy
	}
	return c
}
