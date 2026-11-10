// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/jwriteclub/spec/internal/termtext"
)

// Counter sections share one hit-test for the text row and both half edges.
// They consume the entire fixed right endcap: six, five and five cells.
func (a *App) statusBadgeAt(x, y int) int {
	if a.width < 48 || x < a.width-16 || x >= a.width {
		return -1
	}
	rows := a.footerHeight()
	if a.showHelpBar {
		rows -= 2
	}
	if y < a.bodyHeight() || y >= a.bodyHeight()+rows {
		return -1
	}
	x -= a.width - 16
	if x < 6 {
		return 0
	}
	if x < 11 {
		return 1
	}
	return 2
}

type statusTooltip struct{ kind, x, y, w, h int }

func (p *statusTooltip) covers(x, y int) bool {
	return x >= p.x && x < p.x+p.w && y >= p.y && y < p.y+p.h
}
func (p *statusTooltip) content(a *App) []string {
	e, w, h, proposals, other := a.statusTotals()
	switch p.kind {
	case 0:
		return []string{"Current-file diagnostics", a.cur.Ed.Path(), "", fmt.Sprintf(a.theme.uiText("%d errors · %d warnings · %d hints"), e, w, h), "Counts apply to the current document."}
	case 1:
		checking, blocked := 0, 0
		for _, pr := range a.pending {
			if suggestions(pr) && pr.SourceKind != "agent" {
				continue
			}
			switch pr.CheckState {
			case "queued", "running":
				checking++
			case "failed", "stale":
				blocked++
			}
		}
		return []string{"Pending proposals", plural(proposals, "whole proposal"), fmt.Sprintf(a.theme.uiText("%d checking · %d need attention"), checking, blocked), "", "Generated fixes do not add to this count."}
	default:
		return []string{"Other review work", fmt.Sprintf("%d unsettled review items", other), "", "Independent problems, questions and standalone fixes.", "Child fixes count with their owning problem."}
	}
}
func (p *statusTooltip) rendered(a *App) []string {
	width := max(p.w-4, 1)
	surface := lipgloss.NewStyle().Foreground(a.theme.Token("ink")).Background(a.theme.Token("counterHover"))
	var rows []string
	for i, text := range p.content(a) {
		for _, line := range wrapText(termtext.Clean(text), width) {
			style := a.theme.Ink
			if i == 0 {
				style = a.theme.Focus.Bold(true)
			}
			rows = append(rows, style.Render(padTo(line, width)))
		}
	}
	frame := (&popup{lines: rows, inner: width}).box(a.theme)
	return strings.Split(renderFilledText(surface, frame), "\n")
}
func (p *statusTooltip) place(a *App) {
	p.w = max(min(52, a.width-2), 1)
	p.h = len(p.rendered(a))
	p.x = max(a.width-p.w-1, 0)
	bottom := a.bodyHeight()
	if a.prompt != nil {
		bottom--
	}
	p.y = max(bottom-p.h, 0)
}
func (p *statusTooltip) layer(a *App) *lipgloss.Layer {
	return lipgloss.NewLayer(strings.Join(p.rendered(a), "\n")).X(p.x).Y(p.y)
}

// Keep the note strip independent of the status band's blue-grey surface.
// Reapply its fill after nested prompt/key styles reset SGR state.
func (a *App) promptBand(line string, width int) string {
	surface := lipgloss.NewStyle().Foreground(a.theme.Token("statusInk")).Background(a.theme.Token("note"))
	return renderFilledText(surface, padTo(line, width))
}
