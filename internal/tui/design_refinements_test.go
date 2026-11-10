// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/stretchr/testify/require"
)

func refineCardApp(t *testing.T) *App {
	a := newTestApp(t, testOptions(demoProject(t)))
	a.width, a.height = 140, 40
	p := &proposal.Proposal{ID: "fixture", SourceKind: "agent", CheckState: "running", Primary: &proposal.Primary{N: 1, Name: "$Frame.Timing", File: "demo.spec.md"}, Hunks: []finding.Hunk{{N: 2, File: "demo.spec.md", Find: "above zero", Replace: "of at most 5 s", Issue: "missing_update", Severity: "inconsistent", Explanation: "Keep the bound consistent."}}}
	a.pending = []*proposal.Proposal{p}
	a.rebuildCards()
	a.sideShown = true
	a.layout()
	return a
}
func emulateRefinement(t *testing.T, rows []string, width int) *vt.Emulator {
	t.Helper()
	em := vt.NewEmulator(width, len(rows))
	t.Cleanup(func() { em.Close() })
	for y, line := range rows {
		_, err := fmt.Fprintf(em, "\x1b[%d;1H%s\x1b[0m", y+1, line)
		require.NoError(t, err)
	}
	return em
}
func TestProposalSurfaceSurroundsInsetFix(t *testing.T) {
	a := refineCardApp(t)
	rows := a.sideView(54, 70)
	em := emulateRefinement(t, rows, 54)
	fixIndex := -1
	for i, c := range a.side.cards {
		if c.kind == cardEdit {
			fixIndex = i
			break
		}
	}
	require.GreaterOrEqual(t, fixIndex, 0)
	parent := color.RGBAModel.Convert(a.theme.SideCardFocus.GetBackground())
	child := color.RGBAModel.Convert(a.theme.SideChild.GetBackground())
	for y := a.side.starts[fixIndex]; y < a.side.ends[fixIndex]; y++ {
		require.Equal(t, "│", em.CellAt(1, y).Content, "parent rail continues alongside child row %d", y)
		require.Equal(t, parent, color.RGBAModel.Convert(em.CellAt(2, y).Style.Bg), "indent belongs to parent")
		require.Equal(t, child, color.RGBAModel.Convert(em.CellAt(7, y).Style.Bg), "fix retains its own surface")
	}
}
func TestProposalPaddingProfilesAndSeparation(t *testing.T) {
	a := refineCardApp(t)
	counts := map[int]int{}
	for _, height := range []int{24, 30, 37} {
		a.height = height
		em := emulateRefinement(t, a.sideView(54, 70), 54)
		count := 0
		for y := range 70 {
			if em.CellAt(1, y).Content == "│" && strings.TrimSpace(em.CellAt(5, y).Content) == "" && em.CellAt(5, y).Style.Bg != nil {
				count++
			}
		}
		counts[height] = count
	}
	require.Equal(t, counts[24]+2, counts[30], "one parent inset precedes the first fix, and one trailing row belongs to the fix at 30 rows")
	require.Equal(t, counts[30]+1, counts[37], "top padding is added to the parent only above 36 rows")
	// Independent proposals always keep an unfilled separating row.
	a.height = 24
	p := &proposal.Proposal{ID: "second", SourceKind: "agent"}
	a.side.cards = []*card{{kind: cardProposal, p: a.pending[0]}, {kind: cardProposal, p: p}}
	rows := a.sideView(54, 70)
	require.Equal(t, "", strings.TrimSpace(ansi.Strip(ansi.Cut(rows[a.side.starts[1]-1], 1, 54))))
}
func TestStatusSectionsAreContinuousAndHoverDoesNotMoveFocus(t *testing.T) {
	a := refineCardApp(t)
	a.width, a.height = 80, 24
	a.sideShown = false
	a.layout()
	em := emulateRefinement(t, []string{a.statusLine(80)}, 80)
	for x := 64; x < 80; x++ {
		token := "warningCount"
		if x >= 70 {
			token = "proposalCount"
		}
		if x >= 75 {
			token = "otherCount"
		}
		require.Equal(t, color.RGBAModel.Convert(a.theme.Token(token)), color.RGBAModel.Convert(em.CellAt(x, 0).Style.Bg))
	}
	here, ctx := a.here(), a.context()
	a.onMouse(tea.MouseMotionMsg{X: 65, Y: a.bodyHeight()})
	require.NotNil(t, a.statusTip)
	require.Equal(t, here, a.here())
	require.Equal(t, ctx, a.context())
	require.Empty(t, a.statusHover, "hover explanation no longer replaces the status message")
	frame, _ := a.render()
	require.Contains(t, ansi.Strip(frame), "Current-file diagnostics")
	a.onMouse(tea.MouseMotionMsg{X: a.statusTip.x + 1, Y: a.statusTip.y + 1})
	require.NotNil(t, a.statusTip)
	x, y := a.statusTip.x+1, a.statusTip.y+1
	a.onMouse(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	a.onMouse(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelDown})
	require.Equal(t, here, a.here(), "tooltip clicks and scrolls never reach the document behind it")
	require.Equal(t, ctx, a.context())
	a.onMouse(tea.MouseMotionMsg{X: 0, Y: 0})
	require.Nil(t, a.statusTip)
}
func TestNoteStripFillsEveryCell(t *testing.T) {
	a := refineCardApp(t)
	a.width, a.height = 80, 30
	a.prompt = &prompt{kind: promptSaveFirst, label: "Check reads the files: save modified buffers?"}
	rows := strings.Split(a.View().Content, "\n")
	em := emulateRefinement(t, rows, 80)
	for x := range 80 {
		require.Equal(t, color.RGBAModel.Convert(a.theme.Token("note")), color.RGBAModel.Convert(em.CellAt(x, a.bodyHeight()-1).Style.Bg), "note fill at %d", x)
	}
}
func TestReviewRetryLivesOnlyInFooter(t *testing.T) {
	a := refineCardApp(t)
	r := a.newReview(a.pending[0])
	a.push(r)
	body := ansi.Strip(strings.Join(r.view(a, 100, 30), "\n"))
	require.NotContains(t, body, "[r] Retry")
	footer := strings.Join(strings.Fields(ansi.Strip(strings.Join(a.helpBar(CtxReview, 100), " "))), " ")
	require.Contains(t, footer, "[r] Recheck")
	require.NotContains(t, footer, "Format")
	_, bound := Lookup(CtxReview, "ctrl+f")
	require.False(t, bound)
}
func TestSettingsSaveReviewUsesFooterForActions(t *testing.T) {
	a := refineCardApp(t)
	s := fixtureSettings(t)
	s.confirm = "save"
	s.user.Set("dark", "foreground_profile")
	rows := make([]string, 30)
	s.confirmView(a, rows, 100)
	body := strings.Join(rows, "\n")
	require.NotContains(t, body, "Ctrl+O")
	require.NotContains(t, body, "u Save user")
	var labels []string
	for _, b := range s.helperBindings(a) {
		labels = append(labels, b.Label)
	}
	require.Contains(t, labels, "Save user")
}
func TestCardIdentifiersDoNotBreakUIOwnedLabels(t *testing.T) {
	a := refineCardApp(t)
	p := a.pending[0]
	p.Author = strings.Repeat("very-long-agent-name", 5)
	p.Primary.Name = "$" + strings.Repeat("Long.Identifier.", 8)
	rows := a.cardBox(a.side.cards[0], true, 37)
	require.Contains(t, ansi.Strip(rows[0]), "Agent proposal")
	for _, line := range rows {
		require.Equal(t, 41, ansi.StringWidth(line))
	}
	require.Contains(t, ansi.Strip(strings.Join(rows, "\n")), "…")
}

func TestStatusCounterHitAreasIncludeHalfEdges(t *testing.T) {
	a := refineCardApp(t)
	for _, height := range []int{24, 30, 37, 50} {
		a.width, a.height = 140, height
		rows := a.footerHeight() - 2
		for y := a.bodyHeight(); y < a.bodyHeight()+rows; y++ {
			for x := a.width - 16; x < a.width; x++ {
				want := 0
				if x >= a.width-10 {
					want = 1
				}
				if x >= a.width-5 {
					want = 2
				}
				require.Equal(t, want, a.statusBadgeAt(x, y), "counter cell %d,%d at height %d", x, y, height)
			}
		}
		require.Equal(t, -1, a.statusBadgeAt(a.width-1, a.bodyHeight()-1))
		require.Equal(t, -1, a.statusBadgeAt(a.width-1, a.bodyHeight()+rows))
	}
}

func TestFindingAndFixShareTrailingPadding(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		for _, height := range []int{24, 30, 40} {
			t.Run(fmt.Sprintf("%d-fixes-%d-rows", count, height), func(t *testing.T) {
				a := refineCardApp(t)
				a.height = height
				p := a.pending[0]
				p.SourceKind, p.CheckState = "human", "complete"
				p.Primary.InWorktree = true
				if count == 0 {
					p.Hunks = nil
					p.Notes = []finding.Note{{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "ambiguity", Severity: "inconsistent", Explanation: "The rule needs clarification."}}
				} else if count == 2 {
					h := p.Hunks[0]
					h.FixedBy = []int{3}
					second := h
					second.N, second.FixedBy = 3, nil
					second.Find, second.Replace = "for the reader", "for each reader"
					p.Hunks = []finding.Hunk{h, second}
				}
				a.rebuildCards()
				rows := a.sideView(54, 70)
				em := emulateRefinement(t, rows, 54)
				var edits []int
				for i, c := range a.side.cards {
					if c.kind == cardEdit {
						edits = append(edits, i)
					}
				}
				require.Len(t, edits, count)
				last := len(a.side.cards) - 1
				end := a.side.ends[last]
				if height < 30 {
					require.Nil(t, em.CellAt(7, end).Style.Bg, "compact cards add no trailing padding")
					return
				}
				if count == 0 {
					require.Equal(t, "│", em.CellAt(1, end).Content)
					require.NotNil(t, em.CellAt(7, end).Style.Bg, "a finding without fixes owns a trailing row")
					require.Nil(t, em.CellAt(7, end+1).Style.Bg, "exactly one trailing row")
					return
				}
				first := edits[0]
				gap := a.side.starts[first] - 1
				require.Equal(t, "", strings.TrimSpace(ansi.Strip(ansi.Cut(rows[gap], 2, 54))))
				require.Equal(t, em.CellAt(7, gap-1).Style.Bg, em.CellAt(7, gap).Style.Bg, "the first inset uses its finding's fill")
				require.Equal(t, "", strings.TrimSpace(em.CellAt(7, end-1).Content))
				require.Equal(t, color.RGBAModel.Convert(a.theme.SideChild.GetBackground()), color.RGBAModel.Convert(em.CellAt(7, end-1).Style.Bg))
				require.Nil(t, em.CellAt(7, end).Style.Bg, "the finding ends with its last fix, without a second bottom row")
				if count == 2 {
					require.Equal(t, a.side.ends[edits[0]], a.side.starts[edits[1]], "only the first fix gets a parent-coloured inset")
				}
			})
		}
	}
}

func TestStatusHoverUsesDistinctOutlinedSurface(t *testing.T) {
	a := refineCardApp(t)
	for _, dark := range []bool{false, true} {
		for _, reduced := range []bool{false, true} {
			for _, ascii := range []bool{false, true} {
				a.theme = NewThemeProfile(dark, false, reduced, ascii)
				p := &statusTooltip{kind: 1}
				p.place(a)
				rows := p.rendered(a)
				em := emulateRefinement(t, rows, p.w)
				corner := "╭"
				if ascii {
					corner = "+"
				}
				require.Equal(t, corner, em.CellAt(0, 0).Content)
				background := color.RGBAModel.Convert(a.theme.Token("counterHover"))
				for _, role := range []string{"card", "selectedCard", "childCard", "selectedChildCard"} {
					require.NotEqual(t, background, color.RGBAModel.Convert(a.theme.Token(role)), "hover differs from %s", role)
				}
				for y, row := range rows {
					require.Equal(t, p.w, ansi.StringWidth(row))
					for x := range p.w {
						require.Equal(t, background, color.RGBAModel.Convert(em.CellAt(x, y).Style.Bg), "continuous hover fill at %d,%d", x, y)
					}
				}
			}
		}
	}
}
