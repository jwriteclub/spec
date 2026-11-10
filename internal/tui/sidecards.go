// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"path"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/termtext"
)

// cardOriginStyle uses explicit provenance only. Author and text location
// remain display details and never turn unknown work into a human or agent.
func cardOriginStyle(th *Theme, sourceKind string) lipgloss.Style {
	switch sourceKind {
	case "human":
		return th.Human
	case "agent":
		return th.Agent
	default:
		return th.Structure
	}
}

func cardSurface(th *Theme, child, selected bool) lipgloss.Style {
	switch {
	case child && selected:
		return th.SideChildFocus
	case child:
		return th.SideChild
	case selected:
		return th.SideCardFocus
	default:
		return th.SideCard
	}
}

func cardAccentGlyph(th *Theme) string {
	if th.ASCII {
		return "|"
	}
	return th.uiText("│")
}

// filledCard replaces the old perimeter box with a local solid fill and a
// single origin/fix accent on the left. The body width is retained so existing
// wrapping remains stable; the card occupies inner+4 cells, matching the
// former border and padding geometry.
func filledCard(body []string, inner int, accent lipgloss.Style, th *Theme, child, selected bool) []string {
	surface := cardSurface(th, child, selected)
	accentStyle := accent.Background(surface.GetBackground()).Bold(true)
	out := make([]string, 0, len(body))
	for _, line := range body {
		line = padTo(line, inner+1)
		out = append(out, accentStyle.Render(cardAccentGlyph(th))+renderFilledText(surface, " "+line+" "))
	}
	return out
}

// renderFilledText reapplies the surface background after embedded text
// styles reset their SGR state. Without that, colored words leave dark holes
// in the otherwise continuous card fill.
func renderFilledText(surface lipgloss.Style, text string) string {
	rendered := surface.Render(text)
	probe := surface.Render("x")
	end := strings.IndexByte(probe, 'm')
	if end < 0 {
		return rendered
	}
	background := probe[:end+1]
	for _, reset := range []string{"\x1b[0m", "\x1b[m"} {
		parts := strings.Split(rendered, reset)
		if len(parts) < 2 {
			continue
		}
		var b strings.Builder
		for i, part := range parts {
			b.WriteString(part)
			if i < len(parts)-1 {
				b.WriteString(reset)
				if i < len(parts)-2 {
					b.WriteString(background)
				}
			}
		}
		rendered = b.String()
	}
	return rendered
}

func groupOriginTitle(p *proposal.Proposal, themes ...*Theme) string {
	th := uiTheme(themes)
	switch p.SourceKind {
	case "human":
		if p.Primary != nil {
			return th.uiText("Your edit · ") + p.Primary.Name
		}
		if p.Fix != nil {
			return th.uiText("Your fix · ") + p.Fix.Code
		}
		if p.IsModel() {
			return th.uiText("Your model edit · ") + path.Base(p.Model.File)
		}
		return "Your " + p.Kind
	case "agent":
		title := "Agent proposal"
		if p.Author != "" {
			title += th.uiText(" · ") + oneLine(termtext.Clean(p.Author))
		}
		if p.Primary != nil {
			title += th.uiText(" · ") + p.Primary.Name
		}
		if p.IsModel() {
			title += th.uiText(" · ") + path.Base(p.Model.File)
		}
		return title
	default:
		if p.Primary != nil && p.Primary.InWorktree {
			return th.uiText("Working-tree edit · ") + p.Primary.Name
		}
		if p.Fix != nil {
			return th.uiText("Fix suggestion · ") + p.Fix.Code
		}
		if p.IsModel() {
			return th.uiText("Model proposal · ") + path.Base(p.Model.File)
		}
		return th.uiText("Proposal · ") + p.Kind
	}
}

func filledParentLine(text string, width int, accent lipgloss.Style, th *Theme, selected bool) string {
	return filledCard([]string{text}, max(width-4, 1), accent, th, false, selected)[0]
}

// Proposal padding follows the same height breakpoints as the status edges.
// Fixes share their trailing row with the enclosing finding; the parent
// inserts one parent-coloured row before the first fix only.
func (a *App) insetFix(body []string, inner int, selected bool) []string {
	if a.height >= 30 {
		body = append(body, "")
	}
	return filledCard(body, inner, a.theme.FixOrigin, a.theme, true, selected)
}

func proposalLabel(p *proposal.Proposal) string {
	switch p.SourceKind {
	case "agent":
		if p.IsModel() {
			return "Agent model proposal"
		}
		return "Agent proposal"
	case "human":
		if p.Primary != nil {
			return "Your edit"
		}
		if p.IsModel() {
			return "Your model edit"
		}
		if p.Fix != nil {
			return "Your fix"
		}
		if p.Kind == "analyze" || p.Kind == "analysis" {
			return "Your analysis"
		}
		return "Your check"
	default:
		if p.Primary != nil && p.Primary.InWorktree {
			return "Working-tree edit"
		}
		if p.Fix != nil {
			return "Fix suggestion"
		}
		return "Proposal"
	}
}

// Identifiers and paths are data fields, not fragments of UI sentences. Keep
// each on its own bounded line; full values remain in Status details/review.
func cardValue(value string, width int, themes ...*Theme) string {
	th := uiTheme(themes)
	return ansi.Truncate(oneLine(termtext.Clean(value)), max(width, 1), th.uiText("…"))
}
func (a *App) parentRow(line string, p *proposal.Proposal, width int, selected bool) string {
	surface := cardSurface(a.theme, false, selected)
	line = padTo(ansi.Truncate(line, width, a.theme.uiText("…")), width)
	rail := cardOriginStyle(a.theme, p.SourceKind).Background(surface.GetBackground())
	return rail.Render(cardAccentGlyph(a.theme)) + renderFilledText(surface, ansi.Cut(line, 1, width))
}
