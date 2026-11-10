// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
	"github.com/jwriteclub/spec/internal/workflow"
)

// The Checks sidebar, the commit view and agents' proposals (manual check
// items 24 to 44, step 4e), on the demo project with canned replies.

// Texts of the demo project the scenarios edit.
const (
	aboveZero    = "above zero"
	atMost5s     = "of at most 5 s"
	proseFind    = "Prose that mentions $Frame.Order for the reader."
	proseReplace = "Prose that mentions $Frame.Order and the 5 s bound for the reader."
	limitFind    = "MUST hold at most 8 items"
	limitReplace = "MUST hold at most 5 items"
)

var (
	// timingEdit bounds $Frame.Timing in the working tree.
	timingEdit = scenes.Edit{File: "demo.spec.md", Find: aboveZero, Replace: atMost5s}
	// limitEdit changes $Extra.Limit in the working tree.
	limitEdit = scenes.Edit{File: "other.spec.md", Find: "at most 8 items", Replace: "at most 9 items"}
	// boundReply: a check of the bound finds two suggested edits.
	boundReply = scenes.CheckReply("the prose and the extra queue should follow the new bound", nil,
		scenes.Finding{File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent",
			Explanation: "the prose about $Frame.Order should mention the 5 s bound", Find: proseFind, Replace: proseReplace},
		scenes.Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch", Severity: "cosmetic",
			Explanation: "the extra queue holds one fragment per second of the bound: 5 items", Find: limitFind, Replace: limitReplace},
	)
	// cleanReply: a check that finds nothing.
	cleanReply = scenes.CheckReply("consistent", nil)
	// draftReply: a drafted commit message.
	draftReply = scenes.DraftReply("spec: bound fragment duration to 5 s", "$Frame.Timing now caps a fragment at 5 s of media time.")
	// agentTiming: agent-7 bounds $Frame.Timing.
	agentTiming = scenes.AgentChange{Author: "agent-7", Why: "bound fragment length", Edits: []scenes.Edit{timingEdit}}
)

// checked opens the demo project with $Frame.Timing bounded on disk and
// checks it with ^P: two suggested edits land in the sidebar.
func checked(t *testing.T, opts ...Opt) *Scenario {
	t.Helper()
	opts = append([]Opt{At("Frame.Timing"), Edits(timingEdit), Replies(infer.CannedFile{Check: []infer.CannedEntry{boundReply}})}, opts...)
	s := Open(t, scenes.Demo, opts...)
	s.Press("ctrl+p")
	s.WaitFor(tui.Cond{Idle: true, Cards: 2})
	return s
}

// NoProvider gives the UI a user environment with no config at all.
func NoProvider() Opt {
	return Tweak(func(o *tui.Options) {
		env := map[string]string{"HOME": "/nonexistent/spec-scenario-home", "XDG_CONFIG_HOME": "/nonexistent/spec-scenario-home/config"}
		o.Workflow = &workflow.Workflow{
			Getenv: func(k string) string { return env[k] },
			Git:    baseline.Loader{Env: scenes.GitEnv()},
			Now:    func() time.Time { return scenes.Clock },
		}
	})
}

// TestScenarioChangedAnchorState (item 24): an edit in an anchor marks
// every line of that anchor as changed, and the status line says ^P checks
// it.
func TestScenarioChangedAnchorState(t *testing.T) {
	s := Open(t, scenes.Demo, At("Frame.Timing"))
	s.Expect("no gutter marks on a clean tree", func(st tui.State) bool { return len(st.Gutter) == 0 })
	s.Find("in $Time.Media")
	s.Type("only ")
	s.ExpectStatus("$Frame.Timing")
	st := s.State()
	if lines := st.GutterLines("anchor"); len(lines) != 2 || lines[0] != st.Line {
		s.fail("changed-anchor lines %v; want $Frame.Timing's two lines from %d", lines, st.Line)
	}
}

// TestScenarioCheckNoPrompt (item 27): ^P in a changed anchor with an
// unsaved buffer asks only to save; the check runs in the background with
// the editor keeping the keyboard, and its findings open the sidebar.
func TestScenarioCheckNoPrompt(t *testing.T) {
	s := Open(t, scenes.Demo, At("Frame.Timing"),
		Replies(infer.CannedFile{Check: []infer.CannedEntry{scenes.Slow(boundReply, 300)}}))
	s.Replace(aboveZero, atMost5s)
	s.Press("ctrl+p")
	s.ExpectContext(tui.CtxSaveFirst)
	s.Expect("the save-first question", func(st tui.State) bool {
		return strings.HasPrefix(st.Prompt, "Check reads the files: save demo.spec.md first?")
	})
	s.Press("y")
	s.ExpectFocus("editor")
	s.ExpectDirty(false)
	s.Expect("the check runs", func(st tui.State) bool { return strings.Contains(st.Job, "$Frame.Timing") })
	s.Type("x")
	s.ExpectDirty(true)
	s.Press("backspace")
	s.WaitFor(tui.Cond{Idle: true, Cards: 2})
	s.ExpectFlashText("^R 2 problems, 2 fixes")
	s.ExpectFocus("editor")
	s.Expect("the sidebar shows as a column", func(st tui.State) bool { return st.SideShown && st.SideColumn })
	if ps := s.Proposals(); len(ps) != 1 || ps[0].Status != proposal.StatusPending {
		s.fail("want one pending check filed, got %d", len(ps))
	}
}

// TestScenarioCards (item 28): ^R focuses the sidebar and its help bar;
// y applies a card into the buffer without committing; ^Z in the editor
// takes it back; n dismisses; ^R twice hides the sidebar.
func TestScenarioCards(t *testing.T) {
	s := checked(t)
	s.Press("ctrl+r")
	s.ExpectFocus("sidebar")
	s.ExpectHelp("y Accept", "n Dismiss", "e Edit", "f Feedback", "Ret Go To", "c Message", "A Commit", "^R Hide")
	s.ExpectCards("group", "problem", "fix", "problem", "fix")
	s.Press("y")
	s.ExpectBuffer(proseReplace)
	s.ExpectDirty(true)
	if n := strings.TrimSpace(s.Shell("git", "rev-list", "--count", "HEAD")); n != "1" {
		s.fail("y committed: %s commits", n)
	}
	s.Press("esc")
	s.ExpectFocus("editor")
	s.Press("ctrl+z")
	s.ExpectNoBuffer(proseReplace)
	s.WaitUntil("the fix undone: its problem is back", func(st tui.State) bool { return len(st.CardsOf("problem")) == 2 })
	s.Press("ctrl+r")
	s.Expect("a problem selected", func(st tui.State) bool {
		for _, c := range st.Cards {
			if c.Selected {
				return c.Kind == "problem"
			}
		}
		return false
	})
	s.Press("n")
	s.Expect("one problem dismissed with its fix", func(st tui.State) bool { return len(st.CardsOf("problem")) == 1 })
	s.Press("ctrl+r")
	s.Expect("the sidebar hidden", func(st tui.State) bool { return !st.SideShown })
	s.ExpectFocus("editor")
}

// TestScenarioCardSave: the flash after y says ^O saves, and it does, with
// the sidebar keeping the keyboard.
func TestScenarioCardSave(t *testing.T) {
	s := checked(t)
	s.Press("ctrl+r", "y")
	s.ExpectFlash(`\^O saves`)
	s.ExpectDirty(true)
	s.Press("ctrl+o")
	s.ExpectFocus("sidebar")
	s.ExpectDirty(false)
	s.ExpectFlash(`^Saved demo\.spec\.md`)
	if disk := s.ReadFile("demo.spec.md"); !strings.Contains(disk, proseReplace) {
		s.fail("^O did not write the fix")
	}
}

// TestScenarioCardStale (item 28): editing the text a fix replaces makes
// it no longer apply, and y refuses it.
func TestScenarioCardStale(t *testing.T) {
	s := checked(t)
	s.Replace("for the reader", "for readers")
	s.Press("ctrl+r", "down")
	s.Expect("the prose fix is selected and stale", func(st tui.State) bool {
		for _, c := range st.Cards {
			if c.Kind == "fix" && c.Selected && c.Stale != "" && c.State == "gone" {
				return true
			}
		}
		return false
	})
	flash := s.State().Flash
	s.Press("y")
	s.ExpectHelpDim("y")
	s.ExpectFlashText(flash)
	s.ExpectNoBuffer(proseReplace)
}

// TestScenarioCardGoTo (item 28): Enter on a card puts the editor on its
// place and gives it the keyboard.
func TestScenarioCardGoTo(t *testing.T) {
	s := checked(t)
	s.Press("ctrl+r", "down", "down", "down")
	s.Expect("the $Extra.Limit fix selected", func(st tui.State) bool {
		return len(st.Cards) == 5 && st.Cards[4].Selected && st.Cards[4].Kind == "fix" && st.Cards[4].Anchor == "$Extra.Limit"
	})
	s.Press("enter")
	s.ExpectFocus("editor")
	s.ExpectCursor("other.spec.md", 5)
}

// TestScenarioModelFixCard (item 29): ^P on a warning asks the model for a
// fix that lands as a card; e edits its replacement in a box, ^O keeps it
// and applies it.
func TestScenarioModelFixCard(t *testing.T) {
	s := FromScene(t, "lint-fix-card")
	s.Play()
	s.ExpectFocus("sidebar")
	s.ExpectCards("problem", "fix")
	s.Press("e")
	s.ExpectFocus("text-edit")
	s.ExpectContext(tui.CtxHunkEdit)
	s.Press("ctrl+e")
	s.Type(" (own)")
	s.Press("ctrl+o")
	s.ExpectBuffer("PROGRAM-DATE-TIME ($Time.Wall, from NVR keyframe stamps (own)")
}

// TestScenarioAnalyzeCards (item 30): ^N's suggested edit, note and
// question arrive as cards on the anchors they name.
func TestScenarioAnalyzeCards(t *testing.T) {
	s := Open(t, scenes.Demo, Edits(analyzeEdit), Replies(analyzeReply))
	s.Press("ctrl+n")
	s.WaitFor(tui.Cond{Idle: true, Cards: 3})
	s.ExpectCards("problem", "fix", "problem", "question")
	s.Expect("the edit names $Extra.Limit", func(st tui.State) bool { return st.Cards[0].Anchor == "$Extra.Limit" })
	s.ExpectFlashText("^R 2 problems, 1 fix, 1 question")
	s.ExpectText("$Extra.Limit 1")
}

// TestScenarioCommitTicked (item 31): the commit view lists every change,
// all ticked; n unticks one; A commits only the ticked change, with its
// Spec-Change trailer, and leaves the other modified and nothing staged.
func TestScenarioCommitTicked(t *testing.T) {
	s := Open(t, scenes.Demo, Edits(timingEdit, limitEdit), Replies(infer.CannedFile{
		Check: []infer.CannedEntry{cleanReply, cleanReply},
		Draft: []infer.CannedEntry{draftReply, draftReply, draftReply},
	}))
	s.Press("ctrl+r")
	s.Idle()
	s.ExpectFocus("sidebar")
	s.Press("A")
	s.Idle()
	s.ExpectFocus("commit")
	s.Expect("two changes, both ticked", func(st tui.State) bool {
		c := st.Commit
		return len(c.Items) == 2 && c.Items[0].Ticked && c.Items[1].Ticked
	})
	s.Press("down", "n")
	s.Idle()
	s.Expect("$Extra.Limit unticked", func(st tui.State) bool {
		c := st.Commit
		return c.Items[0].Ticked && !c.Items[1].Ticked && strings.Contains(c.Items[1].ID, "Extra.Limit")
	})
	s.Press("A")
	s.Idle()
	s.ExpectFocus("editor")
	show := s.Shell("git", "show", "--stat", "--format=%B", "HEAD")
	if !strings.Contains(show, "Spec-Change: $Frame.Timing") || strings.Contains(show, "other.spec.md") || strings.Contains(show, "Extra.Limit") {
		s.fail("the commit holds more than the ticked change:\n%s", show)
	}
	if st := s.Shell("git", "status", "--porcelain"); !strings.Contains(st, " M other.spec.md") || strings.Contains(st, "demo.spec.md") {
		s.fail("git status after the commit:\n%s", st)
	}
	if cached := s.Shell("git", "diff", "--cached", "--name-only"); cached != "" {
		s.fail("left staged: %s", cached)
	}
}

// TestScenarioSurvivesRestart (item 32): a dismissed card stays dismissed
// after a restart; the other cards come back.
func TestScenarioSurvivesRestart(t *testing.T) {
	s := checked(t)
	s.Press("ctrl+r", "n")
	s.ExpectCards("group", "problem", "fix")
	kept := s.State().Cards[1].Key
	s.Reopen()
	s.ExpectCards("group", "problem", "fix")
	s.Expect("the same problem is back", func(st tui.State) bool { return st.Cards[1].Key == kept })
}

// TestScenarioAgentProposal (item 33): an agent's proposal is one card
// counted in the badge; Enter reviews it hunk by hunk; A applies it into
// the buffer without writing the file.
func TestScenarioAgentProposal(t *testing.T) {
	s := Open(t, scenes.Demo, Agents(agentTiming), Replies(infer.CannedFile{Check: []infer.CannedEntry{boundReply}}))
	s.Expect("the badge counts the proposal and its suggested fixes", func(st tui.State) bool { return st.Badge == "[1 proposal, 2 checks]" })
	s.ExpectHelp("^R Checks")
	s.Press("ctrl+r")
	s.ExpectCards("proposal", "problem", "fix", "problem", "fix")
	s.Press("enter")
	s.ExpectFocus("review")
	s.ExpectContext(tui.CtxReview)
	s.Press("A")
	s.Idle()
	s.ExpectFocus("sidebar")
	s.ExpectBuffer("duration of at most 5 s")
	s.ExpectDirty(true)
	if disk := s.ReadFile("demo.spec.md"); strings.Contains(disk, atMost5s) {
		s.fail("A wrote the file")
	}
}

// TestScenarioAgentReject (item 33): n on an agent's card asks for a
// reason and rejects the proposal with it.
func TestScenarioAgentReject(t *testing.T) {
	s := Open(t, scenes.Demo, Agents(agentTiming), Replies(infer.CannedFile{Check: []infer.CannedEntry{boundReply}}))
	s.Press("ctrl+r", "n")
	s.ExpectContext(tui.CtxPrompt)
	s.Type("not now\n")
	s.Idle()
	s.ExpectCards()
	ps := s.Proposals()
	if len(ps) != 1 {
		s.fail("want one stored proposal, got %d", len(ps))
	}
	if ps[0].Status != proposal.StatusRejected || ps[0].Decision == nil || ps[0].Decision.Reason != "not now" {
		s.fail("want the proposal rejected with the reason, got %+v", ps[0].Decision)
	}
}

// TestScenarioCommitFlowKey (item 37): with an unsaved edit ^R asks to
// save, checks in the background and focuses the sidebar; A opens the
// commit view; once checked, ^R says every changed anchor is checked.
func TestScenarioCommitFlowKey(t *testing.T) {
	s := Open(t, scenes.Demo, At("Frame.Timing"), Replies(infer.CannedFile{
		Check: []infer.CannedEntry{cleanReply},
		Draft: []infer.CannedEntry{draftReply},
	}))
	s.Replace(aboveZero, atMost5s)
	s.ExpectHelp("^R Checks")
	s.Press("ctrl+r")
	s.ExpectContext(tui.CtxSaveFirst)
	s.Press("y")
	s.ExpectFocus("sidebar")
	s.Idle()
	s.Press("A")
	s.Idle()
	s.ExpectFocus("commit")
	s.Press("esc")
	s.Press("esc")
	s.ExpectFocus("editor")
	s.Press("ctrl+r")
	s.ExpectFlashText("Every changed anchor is checked")
	s.Expect("no check runs", func(st tui.State) bool { return st.Job == "" })
}

// TestScenarioFormatWhileAccepting (item 40): ^F on a card previews the
// edit formatted; y applies edit and format; one ^Z undoes both.
func TestScenarioFormatWhileAccepting(t *testing.T) {
	trailing := scenes.Edit{File: "demo.spec.md", Find: "for the reader.", Replace: "for the reader.   "}
	s := checked(t, Edits(trailing))
	s.Press("ctrl+r")
	s.Expect("the prose problem selected", func(st tui.State) bool { return st.Cards[1].Kind == "problem" && st.Cards[1].Selected })
	s.Press("ctrl+f")
	s.ExpectFlashText("Formatted preview: y applies the edit and the format, one ^Z undoes both")
	s.Press("y")
	s.ExpectBuffer(proseReplace + "\n")
	s.Press("esc", "ctrl+z")
	s.ExpectBuffer("for the reader.   \n")
}

// TestScenarioDraftedMessage (item 41): the commit view shows the model's
// draft and says so; a message written with c replaces it.
func TestScenarioDraftedMessage(t *testing.T) {
	s := FromScene(t, "commit-view")
	s.Play()
	s.ExpectFocus("commit")
	s.Expect("the drafted message", func(st tui.State) bool {
		return st.Commit.Note == "drafted by the model" && strings.HasPrefix(st.Commit.Message, "spec: bound fragment duration to 5 s\n\n")
	})
	s.ExpectText("(drafted by the model; c edits it)")
	s.Press("c")
	s.ExpectFocus("text-edit")
	s.Press("ctrl+home")
	s.Type("mine: ")
	s.Press("ctrl+o")
	s.ExpectFocus("commit")
	s.Expect("Chris's message", func(st tui.State) bool { return strings.HasPrefix(st.Commit.Message, "mine: spec: bound") })
	s.ExpectText("(yours; c edits it)")
}

// TestScenarioDraftOneBlankLine: the drafted message is shown with one
// blank line between its subject and its body, as the commit gets it.
func TestScenarioDraftOneBlankLine(t *testing.T) {
	s := FromScene(t, "commit-view")
	s.Play()
	rows := s.Frame()
	for k, r := range rows {
		if strings.TrimSpace(r) != "spec: bound fragment duration to 5 s" {
			continue
		}
		if k+2 >= len(rows) || strings.TrimSpace(rows[k+1]) != "" || !strings.HasPrefix(strings.TrimSpace(rows[k+2]), "$Frame.Timing now caps") {
			s.fail("want one blank row between the subject (row %d) and the body", k)
		}
		return
	}
	s.fail("no subject row")
}

// TestScenarioDraftWithoutProvider (item 41): without a provider ^R says
// so and the commit view falls back to the template.
func TestScenarioDraftWithoutProvider(t *testing.T) {
	s := Open(t, scenes.Demo, Edits(timingEdit), NoProvider())
	s.Press("ctrl+r")
	s.ExpectFlash(`^No model to check \$Frame\.Timing with \(spec doctor says why\)$`)
	s.Press("A")
	s.Idle()
	s.ExpectFocus("commit")
	s.Expect("the template", func(st tui.State) bool {
		return st.Commit.Note == "a template: no model provider (spec doctor)" && strings.Contains(st.Commit.Message, "$Frame.Timing")
	})
}

// TestScenarioNoProviderNotice (item 20): without a provider ^N shows a
// notice that says what spec doctor would; any key closes it.
func TestScenarioNoProviderNotice(t *testing.T) {
	s := Open(t, scenes.Demo, Edits(timingEdit), NoProvider())
	s.Press("ctrl+n")
	s.Idle()
	s.ExpectContext(tui.CtxNotice)
	s.ExpectText("spec doctor")
	s.Press("x")
	s.ExpectContext(tui.CtxEditor)
}

// TestScenarioModalBoxes (item 42): e and f on a card and c in the commit
// view each open the text box; Esc cancels it.
func TestScenarioModalBoxes(t *testing.T) {
	s := checked(t, Replies(infer.CannedFile{Check: []infer.CannedEntry{boundReply}, Draft: []infer.CannedEntry{draftReply}}))
	s.Press("ctrl+r", "e")
	s.ExpectFocus("text-edit")
	s.ExpectHelp("^O Keep edit")
	s.Press("esc")
	s.ExpectFocus("sidebar")
	s.Press("f")
	s.ExpectFocus("text-edit")
	s.Press("esc")
	s.ExpectFocus("sidebar")
	s.Press("A")
	s.Idle()
	s.Press("c")
	s.ExpectFocus("text-edit")
	s.Press("esc")
	s.ExpectFocus("commit")
}

// TestScenarioAgentProposesWhileOpen (item 44): an agent files a proposal
// while the UI is open: it flashes and is counted without opening the
// sidebar or taking the keyboard.
func TestScenarioAgentProposesWhileOpen(t *testing.T) {
	s := Open(t, scenes.Demo, Watch(), Replies(infer.CannedFile{Check: []infer.CannedEntry{boundReply}}))
	disk := s.ReadFile("demo.spec.md")
	s.WriteFile("demo.spec.md", strings.Replace(disk, aboveZero, atMost5s, 1), true)
	if out, code := s.ShellCode("spec", "propose", "Frame.Timing", "--why", "agent test", "--by", "agent-7"); code != 2 {
		s.fail("spec propose exited %d, want 2 (filed with findings):\n%s", code, out)
	}
	s.WaitUntil("the proposal arrives and the buffer reloads", func(st tui.State) bool {
		return len(st.Cards) == 5 && strings.Contains(st.Text, aboveZero)
	})
	// The proposal and the reload (propose restored the HEAD text) arrive
	// in either order; the agent's flash wins either way
	// (TestScenarioAgentFlashWinsOverReload fixes each order).
	s.ExpectFlashText("Agent proposal from agent-7 for $Frame.Timing")
	s.ExpectHelp("^R Checks")
	s.ExpectFocus("editor")
	s.Expect("the sidebar stays hidden, the badge counts it", func(st tui.State) bool {
		return !st.SideShown && st.Badge == "[1 proposal, 2 checks]"
	})
	s.Type("z")
	s.ExpectDirty(true)
}
