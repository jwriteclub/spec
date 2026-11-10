// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// The seven bugs that slipped past the tests before (plan-ui-test-driver
// §4), each as the scenario that catches it. notes/plan-ui-test-driver.md
// ("Phase 1 as built") records which fail on the commit before their fix.

// Texts of lensd the scenarios edit.
const (
	pdtFind    = "PROGRAM-DATE-TIME (wall time from NVR keyframe stamps"
	pdtReplace = "PROGRAM-DATE-TIME ($Time.Wall, from NVR keyframe stamps"
)

// TestScenarioRegressProposeUnchanged (item 26): ^P in an unchanged anchor
// is disabled even while another anchor differs; it neither prompts nor
// starts a check.
func TestScenarioRegressProposeUnchanged(t *testing.T) {
	s := Open(t, scenes.Lensd, At("Hub.TranscoderCap"),
		Edits(scenes.Edit{File: "lensd.spec.md", Find: pdtFind, Replace: pdtReplace}))
	s.ExpectAnchor("$Hub.TranscoderCap")
	s.ExpectHelpDim("^P")
	s.Press("ctrl+p")
	s.ExpectContext(tui.CtxEditor)
	s.ExpectFocus("editor")
	flash := s.State().Flash
	s.ExpectFlashText(flash)
	s.Expect("no job and no prompt", func(st tui.State) bool { return st.Job == "" && st.Prompt == "" })
}

// analyzeEdit and analyzeReply: ^N finds a suggested edit, a note and a
// question in the demo project.
var (
	analyzeEdit  = scenes.Edit{File: "other.spec.md", Find: "Checked by: TestLimit.", Replace: "Drained every 30 s. Checked by: TestLimit."}
	analyzeReply = infer.CannedFile{Check: []infer.CannedEntry{scenes.CheckReply("one size disagrees with the drain interval", []string{"Is 30 s the drain interval everywhere?"},
		scenes.Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch", Severity: "inconsistent",
			Explanation: "8 items here, but draining every 30 s needs room for 5 at most", Find: "MUST hold at most 8 items", Replace: "MUST hold at most 5 items"},
		scenes.Finding{File: "demo.spec.md", Issue: "ambiguity", Severity: "cosmetic", Explanation: "the drain interval is not defined anywhere else"},
	),
		// ^R then checks $Extra.Limit, which no check has seen.
		scenes.CheckReply("consistent", nil),
	}}
)

// TestScenarioRegressAnalyzeThenReview (items 20, 30): after ^N lands at a
// normal width, the flash naming the review key is whole on the status
// line (ExpectLayout after every step), and ^R gets into the findings
// (and checks $Extra.Limit, which no check has seen, in the background).
func TestScenarioRegressAnalyzeThenReview(t *testing.T) {
	s := Open(t, scenes.Demo, Size(100, 30), Edits(analyzeEdit), Replies(analyzeReply))
	s.Press("ctrl+n")
	s.WaitFor(tui.Cond{Idle: true, Cards: 3})
	s.ExpectFlash(`^\^R `)
	s.ExpectFocus("editor")
	s.Press("ctrl+r")
	s.ExpectFocus("sidebar")
	s.ExpectContext(tui.CtxChecks)
	s.Idle()
	s.ExpectContext(tui.CtxChecks)
	s.ExpectCards("problem", "fix", "problem", "question")
	s.ExpectHelp("y Accept", "n Dismiss")
}

// formattedEdits are lensd with a real edit inside a long paragraph
// ($Frame.Forward cites $Time.Wall) and a prose edit, then ^F: the
// situation of Chris's 8 nameless ^] stops.
func formattedEdits(t *testing.T, opts ...Opt) *Scenario {
	s := Open(t, scenes.Lensd, append(opts, Edits(
		scenes.Edit{File: "lensd.spec.md", Find: pdtFind, Replace: pdtReplace},
		scenes.Edit{File: "lensd.spec.md", Find: "lensd is a Go web gateway", Replace: "lensd is a Go HTTP gateway"},
	))...)
	s.Press("ctrl+f")
	s.ExpectFlash(`^Formatted: \d+ lines changed$`)
	return s
}

// editLabel is a ^] flash: the stop's number and what kind of edit it is.
var editLabel = regexp.MustCompile(`^Edit (\d+)/(\d+): (format only in .+ \(whitespace or wrapping\)|prose in .+: .+|(inv|def) \$\S+ (changed|new|edited)(: .+)?)$`)

// TestScenarioRegressNextEditKinds (item 38): ^] after ^F stops on every
// edit and names its kind; the real change inside a reflowed paragraph is
// its own stop.
func TestScenarioRegressNextEditKinds(t *testing.T) {
	s := formattedEdits(t)
	n := len(s.State().Edits)
	if n < 3 {
		s.fail("want at least 3 ^] stops, got %d", n)
	}
	s.Press("ctrl+home")
	kinds := map[string]int{}
	for range n {
		s.Press("ctrl+]")
		m := editLabel.FindStringSubmatch(s.State().Flash)
		if m == nil {
			s.fail("^] flash %q names no kind", s.State().Flash)
		}
		switch {
		case strings.HasPrefix(m[3], "format only"):
			kinds["format"]++
		case strings.HasPrefix(m[3], "prose"):
			kinds["prose"]++
		default:
			kinds["anchor"]++
			if !strings.Contains(m[3], "$Frame.Forward changed") {
				s.fail("the anchor stop is not $Frame.Forward's change: %q", m[3])
			}
		}
	}
	if kinds["anchor"] != 1 || kinds["prose"] != 1 || kinds["format"] == 0 {
		s.fail("stops by kind: %v; want one anchor, one prose and some format-only", kinds)
	}
}

// forwardNote is a check of $Frame.Forward that files one note.
var forwardNote = infer.CannedFile{Check: []infer.CannedEntry{scenes.CheckReply("one note", nil,
	scenes.Finding{File: "lensd.spec.md", Anchor: "$Frame.Forward", Issue: "ambiguity", Severity: "cosmetic",
		Explanation: "say which clock the stamps use"})}}

// TestScenarioRegressFormatGutter (items 22, 23, 38): after ^F reflowed
// the whole file and a check of the changed anchor is shown, the marks
// for changed anchors and for edits since the check cover only
// $Frame.Forward, the prose marks only the prose edit; every other changed
// line is format-only. (Asserted on the gutter's edit-kind model, not on
// colours.)
func TestScenarioRegressFormatGutter(t *testing.T) {
	s := formattedEdits(t, Replies(forwardNote))
	s.Find("**$Frame.Forward")
	fwd := s.State().Line
	s.Press("ctrl+p", "y")
	s.WaitFor(tui.Cond{Idle: true, Cards: 1})
	st := s.State()
	marked := append(st.GutterLines("anchor"), st.GutterLines("other")...)
	if len(marked) == 0 || len(marked) > 8 {
		s.fail("lines marked as a changed anchor or an edit since the check: %d (%v); want $Frame.Forward's few lines", len(marked), marked)
	}
	for _, l := range marked {
		if l < fwd || l > fwd+8 {
			s.fail("line %d is marked as changed, outside $Frame.Forward (line %d)", l, fwd)
		}
	}
	if n := st.GutterCount("format"); n < 20 {
		s.fail("format-only lines: %d; want the reflowed paragraphs", n)
	}
	prose := st.GutterCount("prose-add") + st.GutterCount("prose-del") + st.GutterCount("prose-both")
	if prose == 0 || prose > 2 {
		s.fail("prose-edit lines: %d; want the one edited line", prose)
	}
}

// TestScenarioRegressNoTilde (item 42): past the end of a short file the
// editor shows nothing (no vim `~` lines).
func TestScenarioRegressNoTilde(t *testing.T) {
	s := Open(t, scenes.Demo)
	if strings.Count(s.State().Text, "\n") > 30 {
		s.fail("the demo file should be shorter than the editor")
	}
	for k, row := range s.Frame() {
		if strings.Contains(row, "~") {
			s.fail("row %d shows a ~: %q", k, row)
		}
	}
}

// TestScenarioRegressPaneFocus (items 35, 36): which pane has the keyboard
// follows ^R, ⇧Tab and Esc, and the help bar names ⇧Tab Next Pane in each.
func TestScenarioRegressPaneFocus(t *testing.T) {
	s := Open(t, scenes.Demo)
	s.ExpectHelp("⇧Tab Next Pane")
	s.Press("ctrl+r")
	s.ExpectFocus("sidebar")
	s.ExpectHelp("⇧Tab Next Pane")
	s.Press("esc")
	s.ExpectFocus("editor")
	s.Expect("the sidebar stays shown", func(st tui.State) bool { return st.SideShown })
	s.Press("shift+tab")
	s.ExpectFocus("sidebar")
	s.Press("shift+tab")
	s.ExpectFocus("outline")
	s.ExpectHelp("⇧Tab Next Pane")
	s.Press("shift+tab")
	s.ExpectFocus("editor")
}

// TestScenarioRegressHelpBarLabels (item 37): ^R stays Checks and ^P
// becomes live once the anchor under the cursor is edited.
func TestScenarioRegressHelpBarLabels(t *testing.T) {
	s := Open(t, scenes.Demo, At("Frame.Timing"))
	s.ExpectHelp("^R Checks", "^P Propose")
	s.ExpectNoHelp("^R Commit")
	s.ExpectHelpDim("^P")
	s.Replace("above zero", "of at most 5 s")
	s.ExpectHelp("^R Checks")
	s.ExpectHelpLive("^P")
	s.ExpectStatus("$Frame.Timing")
}
