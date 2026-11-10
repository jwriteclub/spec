// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
	"github.com/stretchr/testify/require"
)

func TestStatusBandKeepsEndcapsAndCounterFills(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	for _, dark := range []bool{false, true} {
		for _, reduced := range []bool{false, true} {
			a.theme = NewThemeProfile(dark, false, reduced, false)
			for _, w := range []int{80, 100, 140, 220} {
				a.width, a.height = w, 40
				before := ansi.Strip(a.statusLine(w))
				a.cur.Ed.Insert("x")
				a.setStatus(statusWarning, "Unsaved", "Unsaved · an intentionally long receipt that cannot displace either endcap or any counter")
				after := ansi.Strip(a.statusLine(w))
				require.Equal(t, ansi.StringWidth(before[:strings.Index(before, "|")]), ansi.StringWidth(after[:strings.Index(after, "|")]))
				require.Equal(t, ansi.StringWidth(before[:strings.LastIndex(before, "|")]), ansi.StringWidth(after[:strings.LastIndex(after, "|")]))
				require.Equal(t, w, ansi.StringWidth(after))
				lines := a.statusBand(w)
				require.Len(t, lines, 3)
				em := vt.NewEmulator(w, 3)
				for y, line := range lines {
					_, err := fmt.Fprintf(em, "\x1b[%d;1H%s\x1b[0m", y+1, line)
					require.NoError(t, err)
					require.Equal(t, w, ansi.StringWidth(line))
				}
				for x := range w {
					center := em.CellAt(x, 1)
					top := em.CellAt(x, 0)
					bottom := em.CellAt(x, 2)
					require.NotNil(t, center.Style.Bg, "status cell %d is fully filled", x)
					require.Equal(t, color.RGBAModel.Convert(center.Style.Bg), color.RGBAModel.Convert(top.Style.Fg), "top edge at %d", x)
					require.Equal(t, color.RGBAModel.Convert(center.Style.Bg), color.RGBAModel.Convert(bottom.Style.Fg), "bottom edge at %d", x)
					// Reverse video: the fill is drawn as the cell background.
					require.Nil(t, top.Style.Bg, "top edge at %d", x)
					require.Nil(t, bottom.Style.Bg, "bottom edge at %d", x)
					require.NotZero(t, top.Style.Attrs&uv.AttrReverse, "top edge at %d", x)
					require.NotZero(t, bottom.Style.Attrs&uv.AttrReverse, "bottom edge at %d", x)
					require.Equal(t, "▀", top.Content, "top edge at %d leaves its upper half to the terminal", x)
					require.Equal(t, "▄", bottom.Content, "bottom edge at %d leaves its lower half to the terminal", x)
				}
				require.NoError(t, em.Close())
			}
		}
	}
}

// TestStatusEdgesPaintFillAsBackground pins the SGR of the status band's
// half-row edges against its centre row. Both paint each fill through the
// background colour path: the centre with 48, the edges with reverse video
// over a 38 foreground. A bare 38 fill over the default background is text
// colour, which Terminal.app adjusts for minimum contrast.
func TestStatusEdgesPaintFillAsBackground(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	a.width, a.height = 100, 40
	for _, tc := range []struct {
		name    string
		reduced bool
		fills   []string // status, warningCount, proposalCount, otherCount
	}{
		{"truecolor", false, []string{"2;35;28;45", "2;60;49;49", "2;37;54;71", "2;67;40;72"}},
		{"curated256", true, []string{"5;235", "5;58", "5;23", "5;53"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.theme = NewThemeProfile(true, false, tc.reduced, false)
			lines := a.statusBand(a.width)
			require.Len(t, lines, 3)
			top, centre, bottom := lines[0], lines[1], lines[2]
			for _, fill := range tc.fills {
				require.Contains(t, centre, ";48;"+fill+"m", "the centre row paints %s as background", fill)
				require.Contains(t, top, "\x1b[7;38;"+fill+"m▀", "the top edge paints %s as background", fill)
				require.Contains(t, bottom, "\x1b[7;38;"+fill+"m▄", "the bottom edge paints %s as background", fill)
				require.NotContains(t, top, "\x1b[38;"+fill+"m", "no top edge cell draws %s as text colour", fill)
				require.NotContains(t, bottom, "\x1b[38;"+fill+"m", "no bottom edge cell draws %s as text colour", fill)
			}
			// The downsampled frame keeps both paths for Terminal.app.
			var buf bytes.Buffer
			w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ANSI256}
			_, err := w.WriteString(strings.Join(lines, "\n"))
			require.NoError(t, err)
			require.Contains(t, buf.String(), "\x1b[7;38;5;235m▀")
			require.Contains(t, buf.String(), "\x1b[7;38;5;23m▄")
			require.Contains(t, buf.String(), ";48;5;235m")
		})
	}
}
func TestFooterBudgetsAndInheritedBodyBackground(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	for height, footer := range map[int]int{24: 3, 29: 3, 30: 4, 36: 4, 37: 5, 50: 5} {
		a.height = height
		require.Equal(t, footer, a.footerHeight())
		require.Equal(t, height-footer, a.bodyHeight())
	}
	a.width, a.height = 140, 40
	a.showOutline = false
	a.layout()
	frame, _ := a.render()
	em := vt.NewEmulator(a.width, a.height)
	defer em.Close()
	for y, line := range strings.Split(frame, "\n") {
		fmt.Fprintf(em, "\x1b[%d;1H%s\x1b[0m", y+1, line)
	}
	require.Nil(t, em.CellAt(130, 10).Style.Bg, "the editor inherits the terminal's background")
	a.theme.ASCII = true
	for _, line := range a.statusBand(140) {
		require.NotContains(t, line, "▀")
		require.NotContains(t, line, "▄")
		require.Equal(t, 140, ansi.StringWidth(line))
	}
}

// Terminal.app draws a flat bar: no half-block edges, the same three rows
// as ASCII (blank edges), and the body keeps its height.
func TestStatusBandIsFlatInTerminalApp(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	a.width, a.height = 140, 40
	env := map[string]string{}
	a.wf = &workflow.Workflow{Getenv: func(k string) string { return env[k] }}
	body := a.bodyHeight()
	lines := a.statusBand(140)
	require.Len(t, lines, 3)
	require.Contains(t, lines[0], "▀")
	require.Contains(t, lines[2], "▄")
	env["TERM_PROGRAM"] = "Apple_Terminal"
	lines = a.statusBand(140)
	require.Len(t, lines, 3)
	for _, i := range []int{0, 2} {
		require.NotContains(t, lines[i], "▀")
		require.NotContains(t, lines[i], "▄")
		require.Equal(t, "", strings.TrimSpace(ansi.Strip(lines[i])))
		require.Equal(t, 140, ansi.StringWidth(lines[i]))
	}
	require.Equal(t, body, a.bodyHeight())
}

func TestStatusReceiptsPersistThroughKeysAndFailureBeatsArrival(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	now := time.Unix(100, 0)
	a.opts.Now = func() time.Time { return now }
	a.setStatus(statusSuccess, "Saved", "Saved fixture")
	send(a, key("right"))
	require.Equal(t, "Saved fixture", a.flash, "ordinary cursor movement does not erase a receipt")
	now = now.Add(5 * time.Second)
	a.Update(statusExpiryMsg{a.flashSeq})
	require.Empty(t, a.flash)
	a.setStatus(statusFailure, "Save failed", "Save failed · disk unavailable")
	a.setStatus(statusAgent, "Agent proposal", "Agent proposal arrived")
	require.Equal(t, "Save failed · disk unavailable", a.flash)
	now = now.Add(time.Hour)
	a.Update(statusExpiryMsg{a.flashSeq})
	require.Contains(t, a.flash, "Save failed", "a failure remains available until inspected or acted on")
}

func TestAutoAppearanceRestoresDetectedTerminalAndHelpSuspendsLateNotice(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	a.detectedDark = true
	a.applyAppearance(config.Config{ForegroundProfile: "light"})
	require.False(t, a.theme.Dark)
	a.applyAppearance(config.Config{ForegroundProfile: "auto"})
	require.True(t, a.theme.Dark)
	a.openHelp()
	ref := a.top().(*referenceScreen)
	a.notice("Check failed", fmt.Errorf("fixture error"))
	notice := a.popup
	require.NotNil(t, notice)
	a.openHelp()
	require.Nil(t, a.popup, "a late failure cannot obscure the universal Help route")
	require.Same(t, ref, a.top())
	send(a, key("esc"))
	require.Same(t, notice, a.popup, "returning restores the late notice")
}
func TestFileConflictDoesNotStealHelpFindInput(t *testing.T) {
	dir := lensdProject(t)
	a := newTestApp(t, testOptions(dir))
	send(a, typeText("local draft ")...)
	a.openHelp()
	ref := a.top().(*referenceScreen)
	ref.find = true
	ref.query = "save"
	writeFile(t, dir, "lensd.spec.md", "external edit\n")
	a.onFileChanged([]string{"lensd.spec.md"})
	require.Nil(t, a.prompt)
	require.Equal(t, CtxHelp, a.context())
	require.Equal(t, "save", ref.query)
	require.True(t, a.cur.Ed.Buffer().Dirty())
	a.setStatus(statusFailure, "Save failed", "Save failed · fixture")
	a.setFlash("Reloaded another file")
	require.Equal(t, "Save failed · fixture", a.flash, "routine reload feedback cannot erase an unacknowledged failure")
	a.statusHover = "old tooltip"
	a.setStatus(statusSuccess, "Saved", "Saved fixture")
	require.Empty(t, a.statusHover, "a new receipt is visible immediately")
}

func TestExternalCheckingAnimationAndStickyPartialReceipt(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	p := &proposal.Proposal{ID: "external", CheckState: "running"}
	a.pending = []*proposal.Proposal{p}
	require.Nil(t, a.job)
	require.NotNil(t, a.ensureProposalSpinner(), "external checks animate without an internal model job")
	require.Nil(t, a.ensureProposalSpinner(), "only one ticker chain may be active")
	_, cmd := a.Update(proposalSpinMsg{})
	require.NotNil(t, cmd)
	p.CheckState = "complete"
	_, cmd = a.Update(proposalSpinMsg{})
	require.Nil(t, cmd)
	require.False(t, a.proposalSpinning)
	a.setStatus(statusPartial, "Partly applied", "Partly applied · unsaved · one edit needs review")
	a.setFlash("Routine reload")
	require.Contains(t, a.flash, "Partly applied")
	require.Nil(t, a.statusExpiry())
}
