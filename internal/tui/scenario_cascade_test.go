// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// The per-edit cascade (manual check items 45 to 50, step 4f): gate,
// triage and check on canned replies.

var (
	gateClear    = scenes.GateReply(0.08, 0.1, 0.05)
	gateEscalate = scenes.GateReply(0.1, 0.62, 0.05)
	triageClear  = scenes.TriageReply(0.1, 0.05, 0)
	triagePass   = scenes.TriageReply(0.7, 0.2, 0)
)

// cascade opens the demo project with the cascade on the given replies,
// bounds $Frame.Timing and runs ^P (saving first).
func cascade(t *testing.T, replies infer.CannedFile, opts ...Opt) *Scenario {
	t.Helper()
	s := Open(t, scenes.Demo, append([]Opt{At("Frame.Timing"), Cascade(), Replies(replies)}, opts...)...)
	s.Replace(aboveZero, atMost5s)
	s.Press("ctrl+p", "y")
	return s
}

// stageOutcomes returns the finished stages of the only run as
// "name:outcome".
func stageOutcomes(st tui.State) []string {
	var out []string
	if len(st.Runs) != 1 {
		return nil
	}
	for _, sg := range st.Runs[0].Stages {
		out = append(out, sg.Name+":"+sg.Outcome)
	}
	return out
}

// TestScenarioGateCleared (item 45): the gate clears an editorial edit:
// the run says no logically significant change was found, nothing is
// filed, and the editor keeps the keyboard.
func TestScenarioGateCleared(t *testing.T) {
	s := cascade(t, infer.CannedFile{Gate: []infer.CannedEntry{gateClear}})
	s.ExpectFocus("editor")
	s.Idle()
	s.ExpectFlashText("$Frame.Timing: No logically significant change found (0.3 s)")
	s.Expect("the gate cleared it", func(st tui.State) bool {
		return len(st.Runs) == 1 && st.Runs[0].Done && st.Runs[0].Cleared == "gate" &&
			strings.Join(stageOutcomes(st), ",") == "gate:clear"
	})
	s.ExpectText("No logically significant change found (0.3 s)")
	if ps := s.Proposals(); len(ps) != 0 {
		s.fail("a cleared edit filed %d proposals", len(ps))
	}
}

// TestScenarioThreeStages (item 46): the gate escalates, triage passes,
// the check runs: each stage shows as it runs, typing goes on meanwhile,
// and the check's finding lands under the three stage lines.
func TestScenarioThreeStages(t *testing.T) {
	one := scenes.CheckReply("the extra queue should follow", nil,
		scenes.Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch", Severity: "inconsistent",
			Explanation: "5 items", Find: limitFind, Replace: limitReplace})
	s := cascade(t, infer.CannedFile{
		Gate:   []infer.CannedEntry{gateEscalate},
		Triage: []infer.CannedEntry{scenes.Slow(triagePass, 200)},
		Check:  []infer.CannedEntry{scenes.Slow(one, 200)},
	})
	s.WaitFor(tui.Cond{Stage: "triage"})
	s.Expect("the gate escalated", func(st tui.State) bool { return strings.Join(stageOutcomes(st), ",") == "gate:pass" })
	s.Expect("the status line shows human check progress", func(st tui.State) bool {
		return strings.Contains(st.Job, "Checking $Frame.Timing") && len(st.Runs) == 1 && st.Runs[0].Current == "triage"
	})
	s.ExpectFocus("editor")
	s.Type("x")
	s.Press("backspace")
	s.WaitFor(tui.Cond{Stage: "check"})
	s.Expect("triage passed", func(st tui.State) bool {
		return strings.Join(stageOutcomes(st), ",") == "gate:pass,triage:pass"
	})
	s.WaitFor(tui.Cond{Idle: true, Cards: 1})
	ps := s.Proposals()
	if len(ps) != 1 || ps[0].Inference == nil {
		s.fail("want one check filed")
	}
	var got []string
	for _, sg := range ps[0].Inference.Stages {
		got = append(got, sg.Name+":"+sg.Outcome)
	}
	if strings.Join(got, ",") != "gate:pass,triage:pass,check:done" || ps[0].Inference.Stages[2].Findings != 1 {
		s.fail("the filed stages: %v", got)
	}
	s.ExpectFlash(`^\^R `)
	s.ExpectCards("problem", "fix")
}

// TestScenarioTriageCleared (item 47): the gate escalates, triage clears.
func TestScenarioTriageCleared(t *testing.T) {
	s := cascade(t, infer.CannedFile{Gate: []infer.CannedEntry{gateEscalate}, Triage: []infer.CannedEntry{triageClear}})
	s.Idle()
	s.ExpectFlash(`^\$Frame\.Timing: No conflict found \(\d+\.\d s\)$`)
	s.Expect("triage cleared it", func(st tui.State) bool {
		return len(st.Runs) == 1 && st.Runs[0].Cleared == "triage" &&
			strings.Join(stageOutcomes(st), ",") == "gate:pass,triage:clear"
	})
	if ps := s.Proposals(); len(ps) != 0 {
		s.fail("a cleared edit filed %d proposals", len(ps))
	}
}

// TestScenarioClearedCountsAsChecked (item 48): after triage cleared the
// edit, ^R runs no check and the commit view calls it checked; a further
// edit, saved, is checked again.
func TestScenarioClearedCountsAsChecked(t *testing.T) {
	s := cascade(t, infer.CannedFile{
		Gate:   []infer.CannedEntry{gateEscalate, gateClear},
		Triage: []infer.CannedEntry{triageClear},
		Draft:  []infer.CannedEntry{draftReply},
	})
	s.Idle()
	s.Press("ctrl+r")
	s.ExpectFlashText("Every changed anchor is checked")
	s.Expect("no check runs", func(st tui.State) bool { return st.Job == "" })
	s.Press("A")
	s.Idle()
	s.Expect("the anchor is checked", func(st tui.State) bool {
		return len(st.Commit.Items) == 1 && st.Commit.Items[0].State == "checked"
	})
	s.Press("esc", "esc")
	s.ExpectFocus("editor")
	s.Replace(atMost5s, "of at most 6 s")
	s.Press("ctrl+o", "ctrl+r")
	s.Expect("a check runs", func(st tui.State) bool { return strings.Contains(st.Job, "$Frame.Timing") })
	s.Idle()
	s.ExpectFlashText("$Frame.Timing: No logically significant change found (0.3 s)")
}

// TestScenarioFailOpen (item 49): a gate that fails, fails open: its line
// says so and triage runs as usual.
func TestScenarioFailOpen(t *testing.T) {
	s := cascade(t, infer.CannedFile{
		Gate:   []infer.CannedEntry{{Error: infer.CannedHTTP500}},
		Triage: []infer.CannedEntry{triageClear},
	})
	s.Idle()
	s.Expect("the gate failed open, triage cleared", func(st tui.State) bool {
		return strings.Join(stageOutcomes(st), ",") == "gate:failed_open,triage:clear" &&
			strings.Contains(st.Runs[0].Stages[0].Line, "failed open")
	})
	s.ExpectFlash(`^\$Frame\.Timing: No conflict found`)
}

// TestScenarioCancelStage (item 49): ^C during a running stage cancels the
// check; its run goes away.
func TestScenarioCancelStage(t *testing.T) {
	s := cascade(t, infer.CannedFile{
		Gate:   []infer.CannedEntry{gateEscalate},
		Triage: []infer.CannedEntry{scenes.Slow(triagePass, 60000)},
	})
	s.WaitFor(tui.Cond{Stage: "triage"})
	s.Press("ctrl+c")
	s.Idle()
	s.ExpectFlashText("Cancelled: checking $Frame.Timing")
	s.Expect("no run, no card", func(st tui.State) bool { return len(st.Runs) == 0 && len(st.Cards) == 0 })
	if ps := s.Proposals(); len(ps) != 0 {
		s.fail("a cancelled check filed %d proposals", len(ps))
	}
}

// TestScenarioOutdatedThenWithdrawn (item 50): a further edit makes the
// check outdated and its cards refuse y; back at the committed text the
// check is withdrawn as superseded.
func TestScenarioOutdatedThenWithdrawn(t *testing.T) {
	s := FromScene(t, "check-outdated")
	s.Play()
	s.ExpectFocus("sidebar")
	s.Expect("every card outdated", func(st tui.State) bool {
		for _, c := range st.Cards {
			if c.Outdated == "" {
				return false
			}
		}
		return len(st.Cards) == 5
	})
	flash := s.State().Flash
	s.ExpectHelpDim("y")
	s.Press("y")
	s.ExpectFlashText(flash)
	s.ExpectNoBuffer(proseReplace)
	s.Press("esc")
	s.Replace("of at most 7 s", aboveZero)
	s.Idle()
	s.ExpectFlashText("$Frame.Timing is back to its committed text; its check was withdrawn")
	s.ExpectCards()
	ps := s.Proposals()
	if len(ps) != 1 || ps[0].Status != proposal.StatusRejected || !strings.HasPrefix(ps[0].Decision.Reason, "superseded: ") {
		s.fail("want the check rejected as superseded, got %d proposals", len(ps))
	}
}

// TestScenarioWithdrawNoJump is TestScenarioWithdrawAfterJump's control:
// with no key after the revert, the debounced parse withdraws the check.
func TestScenarioWithdrawNoJump(t *testing.T) {
	const delay = 40 * time.Millisecond
	s := checked(t, Tweak(func(o *tui.Options) { o.ParseDelay = delay }))
	s.Replace(atMost5s, aboveZero)
	time.Sleep(5 * delay)
	s.Idle()
	s.WaitUntil("the check is withdrawn", func(st tui.State) bool { return len(st.Cards) == 0 })
}

// TestScenarioWithdrawAfterJump: reverting the anchor to its committed
// text and jumping at once (before the reparse debounce fires) still
// withdraws the check once the debounce has passed.
func TestScenarioWithdrawAfterJump(t *testing.T) {
	const delay = 40 * time.Millisecond
	s := checked(t, Tweak(func(o *tui.Options) { o.ParseDelay = delay }))
	s.Replace(atMost5s, aboveZero)
	s.Press("ctrl+t")
	time.Sleep(5 * delay)
	s.Idle()
	s.WaitUntil("the check is withdrawn", func(st tui.State) bool { return len(st.Cards) == 0 })
}
