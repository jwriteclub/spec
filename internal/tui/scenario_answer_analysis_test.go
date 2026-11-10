// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"strings"
	"testing"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// Answering an analysis's question (notes/plan-step4g-fix-review.md, "As
// built: rethink"), and a clean re-analysis clearing an earlier
// analysis's cards.

const (
	drainQuestion = "Should the queue keep 8 items and drain faster, or hold 5 and keep draining every 30 s?"
	drain15       = "Drained every 15 s."
	drainTest     = "Checked by: TestLimit, TestDrain."
)

var (
	// drainAsk: ^N finds the contradiction and asks which side to change.
	drainAsk = scenes.CheckReply("the limit and the drain interval disagree", []string{drainQuestion},
		scenes.Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "contradiction", Severity: "breaking",
			Explanation: "8 items, but draining every 30 s leaves room for 5 at most"})
	// drainAnswered: the answer keeps 8 items; the drain follows, and
	// its test.
	drainAnswered = scenes.CheckReply("the author keeps 8 items: the queue drains every 15 s", nil,
		scenes.Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch", Severity: "breaking",
			Explanation: "8 items need a 15 s drain", Find: "Drained every 30 s.", Replace: drain15},
		scenes.Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "missing_update", Severity: "inconsistent",
			Explanation: "a test should check the drain", Find: "Checked by: TestLimit.", Replace: drainTest})
	drainClean = scenes.CheckReply("No inconsistencies found.", nil)
)

// selectKind moves the sidebar selection to a card of kind: down from
// the selected card, then up.
func (s *Scenario) selectKind(kind string) {
	s.t.Helper()
	for _, key := range []string{"down", "up"} {
		for range len(s.State().Cards) {
			if s.selected().Kind == kind {
				return
			}
			s.Press(key)
		}
	}
	s.fail("no %s card to select", kind)
}

// ^N asks; f on the question opens the answer box, ^O sends the answer,
// and the model's fixes (in the edited text too) land as a new analysis
// that supersedes the answered one, the notes kept; y on the group
// applies both and ^O records them.
func TestScenarioAnswerAnalysis(t *testing.T) {
	s := Open(t, scenes.Demo, Edits(analyzeEdit), Replies(infer.CannedFile{Check: []infer.CannedEntry{drainAsk, drainAnswered}}))
	s.Press("ctrl+n")
	s.WaitFor(tui.Cond{Idle: true, Cards: 2})
	s.ExpectCards("problem", "question")
	old := s.Proposals()[0]
	s.Press("shift+tab")
	s.ExpectFocus("sidebar")
	s.selectKind("question")
	s.ExpectText("f answers it · n dismisses it")
	s.ExpectNoText("Answer by editing the spec.")
	s.ExpectHelpLive("f")

	s.Press("f")
	s.ExpectContext(tui.CtxHunkEdit)
	s.ExpectText("the model rethinks the analysis")
	s.Type("Keep 8 items and drain faster.")
	s.Press("ctrl+o")
	s.WaitUntil("the rethink", func(st tui.State) bool {
		return st.Job == "" && len(st.CardsOf("fix")) == 2
	})
	s.ExpectNoText(drainQuestion)

	var np *proposal.Proposal
	for _, p := range s.Proposals() {
		switch {
		case p.ID == old.ID:
			if p.Status != proposal.StatusRejected || !strings.Contains(p.Decision.Reason, "superseded by rethink") {
				s.fail("the answered analysis: %s %+v", p.Status, p.Decision)
			}
		case p.Status == proposal.StatusPending:
			np = p
		}
	}
	if np == nil || np.Kind != proposal.KindAnalysis || np.Rethink == nil || np.Rethink.Of != old.ID || np.Rethink.PrimaryHunk != 0 {
		s.fail("the rethink: %+v", np)
	}
	if len(np.Rethink.Notes) != 1 || np.Rethink.Notes[0].Question != drainQuestion {
		s.fail("the notes: %+v", np.Rethink.Notes)
	}
	// Proposed, not applied.
	if strings.Contains(s.ReadFile("other.spec.md"), drain15) {
		s.fail("the fix was applied before y")
	}

	s.selectKind("group")
	s.ExpectText("rethought with your answer")
	s.Press("y")
	s.Press("ctrl+o")
	s.WaitUntil("the rethink recorded", func(tui.State) bool {
		for _, p := range s.Proposals() {
			if p.ID == np.ID {
				return p.Status == proposal.StatusApplied && len(p.Decision.Applied) == 2
			}
		}
		return false
	})
	s.ExpectCards()
	if got := s.ReadFile("other.spec.md"); !strings.Contains(got, drain15) || !strings.Contains(got, drainTest) {
		s.fail("both fixes saved")
	}
}

// A clean re-analysis of the same edits supersedes the earlier analysis's
// cards nobody acted on; the record stays, rejected as superseded.
func TestScenarioReanalysisClearsStaleCards(t *testing.T) {
	s := Open(t, scenes.Demo, Edits(analyzeEdit), Replies(infer.CannedFile{Check: []infer.CannedEntry{drainAsk, drainClean}}))
	s.Press("ctrl+n")
	s.WaitFor(tui.Cond{Idle: true, Cards: 2})
	old := s.Proposals()[0]
	s.Press("ctrl+n")
	s.WaitUntil("the re-analysis", func(st tui.State) bool { return st.Job == "" && len(st.Cards) == 0 })
	s.ExpectFlash("^Analysis: nothing found; cleared the cards of the earlier analysis$")
	ps := s.Proposals()
	if len(ps) != 1 || ps[0].ID != old.ID || ps[0].Status != proposal.StatusRejected || ps[0].Decision.Reason != "superseded by a newer analysis of the same scope" {
		s.fail("the earlier analysis: %+v", ps)
	}
}

// An analysis Chris has decided an item of stays when a re-analysis
// comes back clean.
func TestScenarioReanalysisKeepsActedOnCards(t *testing.T) {
	s := Open(t, scenes.Demo, Edits(analyzeEdit), Replies(infer.CannedFile{Check: []infer.CannedEntry{drainAsk, drainClean}}))
	s.Press("ctrl+n")
	s.WaitFor(tui.Cond{Idle: true, Cards: 2})
	s.Press("shift+tab")
	s.selectKind("question")
	s.Press("n")
	s.Press("esc")
	s.Press("ctrl+n")
	s.WaitUntil("the re-analysis", func(st tui.State) bool { return st.Job == "" && strings.HasPrefix(st.Flash, "Analysis:") })
	s.ExpectFlashText("Analysis: nothing found")
	ps := s.Proposals()
	if len(ps) != 1 || ps[0].Status != proposal.StatusPending {
		s.fail("the decided analysis stays: %+v", ps)
	}
}
