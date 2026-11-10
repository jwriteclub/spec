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

// Step 4g's problem and fix cards (notes/plan-step4g-fix-review.md, "As
// built"; notes/plan-ui-test-driver.md §4, "Findings and fixes are
// many-to-many"): every shape of findings and fixes on hand-written check
// replies, asserting the card model, the decisions and the buffer, never
// the look.

// replies is a canned file whose checks answer reply.
func replies(reply infer.CannedEntry) Opt {
	return Replies(infer.CannedFile{Check: []infer.CannedEntry{reply}})
}

// card returns the first card of kind, failing when there is none.
func (s *Scenario) card(kind string) tui.CardState {
	s.t.Helper()
	cs := s.State().CardsOf(kind)
	if len(cs) == 0 {
		s.fail("no %s card", kind)
	}
	return cs[0]
}

// selected returns the selected card.
func (s *Scenario) selected() tui.CardState {
	s.t.Helper()
	for _, c := range s.State().Cards {
		if c.Selected {
			return c
		}
	}
	s.fail("no card selected")
	return tui.CardState{}
}

// recorded saves the current file, waits until the only proposal is recorded
// as applied, and expects n hunks applied (the check's own change counts as one).
func (s *Scenario) recorded(n int) {
	s.t.Helper()
	s.Press("ctrl+o")
	var ps []*proposal.Proposal
	s.WaitUntil("the check recorded", func(tui.State) bool {
		ps = s.Proposals()
		return len(ps) == 1 && ps[0].Status == proposal.StatusApplied
	})
	if got := len(ps[0].Decision.Applied); got != n {
		s.fail("want %d hunks applied, got %d (%v)", n, got, ps[0].Decision.Applied)
	}
}

// pendingCheck expects the only proposal still pending.
func (s *Scenario) pendingCheck() {
	s.t.Helper()
	if ps := s.Proposals(); len(ps) != 1 || ps[0].Status != proposal.StatusPending {
		s.fail("want the check pending")
	}
}

// Replies of each shape.
var (
	// noFixReply: a problem with no fix, and a question.
	noFixReply = scenes.CheckReply("one rule is unclear now", []string{"Should the 5 s bound apply to the last fragment too?"},
		scenes.Finding{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "ambiguity", Severity: "inconsistent",
			Explanation: "with fragments bounded, is the order by arrival or by media time?"})
	// oneResolvesTwoReply: two problems name the same fix.
	oneResolvesTwoReply = scenes.CheckReply("the prose does not mention the bound", nil,
		scenes.Finding{File: "demo.spec.md", Anchor: "$Frame.Timing", Issue: "numeric_mismatch", Severity: "breaking",
			Explanation: "fragments are bounded to 5 s now, but the prose implies no bound", FixedBy: []int{2}},
		scenes.Finding{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "stale_reference", Severity: "inconsistent",
			Explanation: "readers of $Frame.Order are pointed to prose that misses the bound", FixedBy: []int{2}},
		scenes.Finding{File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent",
			Explanation: "mention the bound in the prose", Find: proseFind, Replace: proseReplace})
	// twoForOneReply: a contradiction that needs two fixes.
	twoForOneReply = scenes.CheckReply("the bound needs the prose and the queue to follow", nil,
		scenes.Finding{File: "demo.spec.md", Anchor: "$Frame.Timing", Issue: "contradiction", Severity: "breaking",
			Explanation: "a 5 s bound contradicts the prose and the queue size", FixedBy: []int{1, 2}},
		scenes.Finding{File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent",
			Explanation: "mention the bound", Find: proseFind, Replace: proseReplace},
		scenes.Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch", Severity: "cosmetic",
			Explanation: "align the queue size", Find: limitFind, Replace: limitReplace})
	// proseFixReply: one problem, its fix in the prose.
	proseFixReply = scenes.CheckReply("the prose should mention the bound", nil,
		scenes.Finding{File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent",
			Explanation: "mention the bound", Find: proseFind, Replace: proseReplace})
)

// fix-none: the problem says no fix was proposed; y applies nothing; the
// problem stays open (and counts on its anchor) until n; the question
// says f answers it (TestScenarioRethinkAnswer answers one).
func TestScenarioFixNone(t *testing.T) {
	s := checked(t, replies(noFixReply))
	s.ExpectCards("problem", "question")
	if c := s.card("problem"); c.State != "no-fix" || len(c.Fixes) != 0 || c.Anchor != "$Frame.Order" {
		s.fail("the problem: %+v", c)
	}
	s.Press("ctrl+r")
	s.ExpectHelpDim("y")
	flash := s.State().Flash
	s.Press("y")
	s.ExpectHelpDim("y")
	s.ExpectFlashText(flash)
	s.ExpectNoBuffer(proseReplace)
	s.ExpectBuffer(atMost5s)
	s.ExpectCards("problem", "question")
	s.ExpectText("$Frame.Order 1")
	s.pendingCheck()
	s.Press("down")
	s.ExpectText("f answers it · n dismisses it")
	s.Press("y")
	s.ExpectHelpDim("y")
	s.ExpectFlashText(flash)
	s.Press("up", "n", "n")
	s.ExpectCards()
	s.recorded(1)
}

// fix-one-resolves-two: two problems stack over one fix; accepting it
// settles both.
func TestScenarioFixOneResolvesTwo(t *testing.T) {
	s := checked(t, replies(oneResolvesTwoReply))
	s.ExpectCards("problem", "problem", "fix")
	st := s.State()
	fix := st.Cards[2]
	for _, c := range st.Cards {
		if c.Head != "1 fix resolves 2 problems" || len(c.Problems) != 2 {
			s.fail("one cluster of two problems: %+v", c)
		}
	}
	for _, p := range st.Cards[:2] {
		if len(p.Fixes) != 1 || p.Fixes[0] != fix.Key || p.State != "open" {
			s.fail("each problem names the one fix %s: %+v", fix.Key, p)
		}
	}
	s.Press("ctrl+r", "down", "down")
	if c := s.selected(); c.Kind != "fix" {
		s.fail("want the fix selected, got %+v", c)
	}
	s.Press("y")
	s.ExpectBuffer(proseReplace)
	s.ExpectCards()
	s.recorded(2)
}

// fix-two-for-one: one problem over two fixes. One by one, the problem
// stays open until the other fix is decided; on the title, y accepts both.
func TestScenarioFixTwoForOne(t *testing.T) {
	t.Run("one by one", func(t *testing.T) {
		s := checked(t, replies(twoForOneReply))
		s.ExpectCards("group", "problem", "fix", "fix")
		p := s.card("problem")
		if !p.Selected || p.Head != "2 fixes for this problem" || len(p.Fixes) != 2 {
			s.fail("the problem selected, over two fixes: %+v", p)
		}
		s.Press("ctrl+r", "down", "y")
		s.ExpectBuffer(proseReplace)
		s.ExpectCards("group", "problem", "fix", "fix")
		st := s.State()
		if st.Cards[1].State != "open" || st.Cards[2].State != "accepted" || !st.Cards[3].Selected {
			s.fail("one accepted, the problem open, on to the other fix: %+v", st.Cards)
		}
		s.ExpectText("1 of 2 fixes decided")
		s.pendingCheck()
		s.Press("n")
		s.ExpectCards()
		s.recorded(2)
		if disk := s.ReadFile("other.spec.md"); strings.Contains(disk, limitReplace) {
			s.fail("the skipped fix was written")
		}
	})
	t.Run("accept all", func(t *testing.T) {
		s := checked(t, replies(twoForOneReply))
		s.Press("ctrl+r", "up")
		if c := s.selected(); c.Kind != "group" {
			s.fail("want the title selected, got %+v", c)
		}
		s.ExpectNoText("y accepts all")
		s.ExpectHelp("y Accept All")
		s.Press("y")
		s.ExpectFlash(`Applied · unsaved · 2 fixes to 2 files`)
		s.ExpectBuffer(proseReplace)
		s.ExpectCards()
	})
}

// segSpec cites the segment length in three other places.
const segSpec = `# seg

## 1. Segments

**$Seg.Length (segment length).** Every segment MUST be 4 s long. Checked by: TestLength.

**$Seg.Window (live window).** The live window MUST hold 3 segments, 12 s in all (3 × 4 s). Checked by:
TestWindow.

**$Seg.Buffer (player buffer).** A player MUST buffer at least one 4 s segment before it starts. Checked
by: TestBuffer.

Prose: a 4 s segment is about 100 frames at 25 fps.
`

// fix-propagate: Chris changes a value cited in three places; ^P yields
// one group with a fix per place, and accepting the group leaves no
// mention of the old value.
func TestScenarioFixPropagate(t *testing.T) {
	place := func(anchor, find, replace string) scenes.Finding {
		return scenes.Finding{File: "demo.spec.md", Anchor: anchor, Issue: "missing_update", Severity: "inconsistent",
			Explanation: "still cites 4 s segments", Find: find, Replace: replace}
	}
	s := Open(t, scenes.Demo, replies(scenes.CheckReply("three places cite the old length", nil,
		place("$Seg.Window", "12 s in all (3 × 4 s)", "18 s in all (3 × 6 s)"),
		place("$Seg.Buffer", "one 4 s segment", "one 6 s segment"),
		place("", "a 4 s segment is about 100 frames", "a 6 s segment is about 150 frames"),
	)))
	s.WriteFile("demo.spec.md", segSpec, true)
	s.Shell("git", "commit", "-q", "-am", "seg")
	s.Reopen()
	s.Replace("4 s long", "6 s long")
	s.Press("ctrl+p", "y")
	s.WaitFor(tui.Cond{Idle: true, Cards: 7})
	s.ExpectCards("group", "problem", "fix", "problem", "fix", "problem", "fix")
	s.Press("ctrl+r", "up")
	s.ExpectNoText("y accepts all")
	s.ExpectHelp("y Accept All")
	s.Press("y")
	s.ExpectFlash(`Applied · unsaved · 3 fixes to 1 file`)
	s.ExpectBuffer("18 s in all (3 × 6 s)")
	s.Expect("no mention of the old value is left", func(st tui.State) bool { return !strings.Contains(st.Text, "4 s") })
	s.ExpectCards()
	s.recorded(4)
}

// fix-invalid-link: fixed_by naming the finding itself, an index out of
// range and a finding without an edit: the links are dropped and every
// finding stands alone.
func TestScenarioFixInvalidLink(t *testing.T) {
	s := checked(t, replies(scenes.CheckReply("links gone wrong", nil,
		scenes.Finding{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "ambiguity", Severity: "cosmetic", Explanation: "self", FixedBy: []int{0}},
		scenes.Finding{File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent", Explanation: "out of range",
			Find: proseFind, Replace: proseReplace, FixedBy: []int{7}},
		scenes.Finding{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "stale_reference", Severity: "cosmetic", Explanation: "to a note", FixedBy: []int{3}},
		scenes.Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "ambiguity", Severity: "cosmetic", Explanation: "no edit"},
	)))
	s.ExpectCards("problem", "fix", "problem", "problem", "problem")
	noFix := 0
	for _, c := range s.State().Cards {
		if c.Head != "" {
			s.fail("no cluster heads: %+v", c)
		}
		if c.State == "no-fix" {
			noFix++
		}
	}
	if noFix != 3 {
		s.fail("want 3 problems with no fix, got %d", noFix)
	}
	s.Press("ctrl+r", "down", "y")
	s.ExpectBuffer(proseReplace)
	s.ExpectCards("problem", "problem", "problem")
}

// fix-done-by-hand: Chris makes the fix's change himself; the reparse
// records it with no key, flashes once, and the group goes away; the
// check is recorded with only its own change applied.
func TestScenarioFixDoneByHand(t *testing.T) {
	s := checked(t, replies(proseFixReply))
	if c := s.card("fix"); c.State != "pending" {
		s.fail("the fix pending: %+v", c)
	}
	s.Find("for the reader")
	s.Type("and the 5 s bound ")
	s.ExpectBuffer(proseReplace)
	s.WaitUntil("recorded by hand", func(st tui.State) bool { return len(st.Cards) == 0 })
	s.ExpectFlashText("Done by hand: $Frame.Timing fix recorded")
	s.ExpectFocus("editor")
	s.recorded(1)
	flash := s.State().Flash
	s.Type("x")
	s.ExpectFlashText(flash)
	s.ExpectCards()
}

// The same with the reparse debounced and a jump right after the edit
// (fresh() leaves the pending parse alone): still recorded once the
// debounce has passed.
func TestScenarioFixDoneByHandAfterJump(t *testing.T) {
	const delay = 40 * time.Millisecond
	s := checked(t, replies(proseFixReply), Tweak(func(o *tui.Options) { o.ParseDelay = delay }))
	s.Find("for the reader")
	s.Type("and the 5 s bound ")
	s.Press("ctrl+t")
	time.Sleep(5 * delay)
	s.Idle()
	s.WaitUntil("recorded by hand", func(st tui.State) bool { return len(st.Cards) == 0 })
	s.recorded(1)
}

// fix-done-by-hand, in doubt: the text says what the fix would, but not
// in the anchor the fix names. Nothing is recorded on its own, the
// problem stays open, and y records it.
func TestScenarioFixDoneByHandUnsure(t *testing.T) {
	s := checked(t, replies(scenes.CheckReply("the prose should mention the bound", nil,
		scenes.Finding{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "missing_update", Severity: "inconsistent",
			Explanation: "mention the bound", Find: proseFind, Replace: proseReplace})))
	s.Find("for the reader")
	s.Type("and the 5 s bound ")
	s.WaitUntil("the fix in doubt", func(st tui.State) bool {
		f := st.CardsOf("fix")
		return len(f) == 1 && f[0].State == "hand-unsure"
	})
	if p := s.card("problem"); p.State != "open" {
		s.fail("the problem stays open until y: %+v", p)
	}
	s.Expect("no flash of its own", func(st tui.State) bool { return !strings.Contains(st.Flash, "Done by hand") })
	s.pendingCheck()
	s.Press("ctrl+r", "down")
	s.ExpectText("unsure: it is not in $Frame.Order")
	s.Press("y")
	s.ExpectFlashText("Already done by hand: recorded, nothing applied")
	s.ExpectCards()
	s.recorded(1)
}

// The text the fix replaces was changed some other way: the fix no
// longer applies, y refuses it, n skips it and the problem stays open
// until n dismisses it.
func TestScenarioFixNoLongerApplies(t *testing.T) {
	s := checked(t, replies(proseFixReply))
	s.Find("for the reader")
	s.Type("and its bound ")
	s.WaitUntil("the fix no longer applies", func(st tui.State) bool {
		f := st.CardsOf("fix")
		return len(f) == 1 && f[0].State == "gone"
	})
	s.Press("ctrl+r", "down", "n")
	s.ExpectCards("problem", "fix")
	if p := s.card("problem"); p.State != "skipped" {
		s.fail("the problem open, its fix skipped: %+v", p)
	}
	s.Press("up", "n")
	s.ExpectCards()
	s.recorded(1)
}

// Chris's report (#26): after a commit from the sidebar the help bar
// stayed the sidebar's and Esc was needed to type. A commit returns the
// keyboard to the editor with its help bar.
func TestScenarioCommitReturnsToEditor(t *testing.T) {
	s := checked(t, Replies(infer.CannedFile{Check: []infer.CannedEntry{proseFixReply}, Draft: []infer.CannedEntry{draftReply}}))
	s.Press("ctrl+r", "y")
	s.ExpectCards()
	s.ExpectFocus("sidebar")
	s.Press("A")
	s.ExpectContext(tui.CtxSaveFirst)
	s.Press("y")
	s.Idle()
	s.ExpectFocus("commit")
	s.Press("A")
	s.Idle()
	if subj := s.Shell("git", "log", "-1", "--format=%s"); !strings.HasPrefix(subj, "spec: bound fragment duration to 5 s") {
		s.fail("the commit: %q", subj)
	}
	s.ExpectFlash("^Committed ")
	s.ExpectFocus("editor")
	s.ExpectContext(tui.CtxEditor)
	s.ExpectHelp("^O Save", "^P Propose")
	s.ExpectNoHelp("y Accept", "n Skip")
	s.Type("Z")
	s.ExpectDirty(true)
}

// Chris's principle: it must be obvious when an external process sent
// something. When an agent's `spec propose` lands, its flash wins over
// the reload of the file it touched, whichever the watchers report first;
// subsequent reloads cannot hide the agent's proposal receipt.
func TestScenarioAgentFlashWinsOverReload(t *testing.T) {
	for _, proposalFirst := range []bool{true, false} {
		name := map[bool]string{true: "proposal then reload", false: "reload then proposal"}[proposalFirst]
		t.Run(name, func(t *testing.T) {
			s := Open(t, scenes.Demo, replies(boundReply))
			disk := s.ReadFile("demo.spec.md")
			s.WriteFile("demo.spec.md", strings.Replace(disk, aboveZero, atMost5s, 1), true)
			if out, code := s.ShellCode("spec", "propose", "Frame.Timing", "--why", "agent test", "--by", "agent-7"); code != 2 {
				s.fail("spec propose exited %d, want 2 (filed with findings):\n%s", code, out)
			}
			// What the UI saw on disk changes after the restore, as a
			// reload: the agent's edit is in the buffer until then.
			s.WriteFile("demo.spec.md", strings.Replace(disk, "for the reader", "for readers", 1), true)
			if proposalFirst {
				s.Deliver("the proposal", tui.ProposalsChanged())
				s.Deliver("the reload", tui.FilesChanged("demo.spec.md"))
			} else {
				s.Deliver("the reload", tui.FilesChanged("demo.spec.md"))
				s.Deliver("the proposal", tui.ProposalsChanged())
			}
			s.ExpectBuffer("for readers")
			s.ExpectCards("proposal", "problem", "fix", "problem", "fix")
			s.ExpectFlashText("Agent proposal from agent-7 for $Frame.Timing")
			s.ExpectFocus("editor")

			s.Press("right", "left")
			s.ExpectFlashText("Agent proposal from agent-7 for $Frame.Timing")
			s.WriteFile("demo.spec.md", disk, true)
			s.Deliver("a later reload", tui.FilesChanged("demo.spec.md"))
			s.ExpectFlashText("Agent proposal from agent-7 for $Frame.Timing")
		})
	}
}
