// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package scenes

import (
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/tui"
)

// Step 4g's problem and fix cards (notes/plan-step4g-fix-review.md, "As
// built"): every shape of findings and fixes, on hand-written check
// replies.

// Texts of the TranscoderCap case: Chris changes $Hub.TranscoderCap's
// "$Frame.CleanGeneration's 1 s" to 2 s; the one finding carries its fix
// on $Frame.CleanGeneration.
const (
	cleanFind    = "MUST have exited within 1 s"
	cleanReplace = "MUST have exited within 2 s"
	cleanWhy     = "$Hub.TranscoderCap now bounds an exiting generation by $Frame.CleanGeneration's 2 s, but $Frame.CleanGeneration says it MUST have exited within 1 s."
)

// transcoderReply is the TranscoderCap check: one problem, its fix on
// $Frame.CleanGeneration.
var transcoderReply = CheckReply("$Frame.CleanGeneration still bounds the exit by 1 s", nil,
	Finding{File: "lensd.spec.md", Anchor: "$Frame.CleanGeneration", Issue: "numeric_mismatch", Severity: "breaking",
		Explanation: cleanWhy, Find: cleanFind, Replace: cleanReplace})

// checkTranscoder changes $Hub.TranscoderCap's 1 s to 2 s, checks it
// with ^P (saving first), and focuses the sidebar on the problem.
var checkTranscoder = []Step{
	{Note: "change $Hub.TranscoderCap's 1 s to 2 s", Find: "1 s), so a new", Keys: []string{"shift+right"}, Text: "2"},
	{Note: "^P, save first", Keys: []string{"ctrl+p", "y"}, Wait: tui.Cond{Idle: true, Cards: 2}},
	{Note: "focus the sidebar: the problem is selected", Keys: []string{"ctrl+r"}},
}

// oneResolvesTwo: two problems name the same fix.
var oneResolvesTwo = CheckReply("the prose does not mention the bound", nil,
	Finding{File: "demo.spec.md", Anchor: "$Frame.Timing", Issue: "numeric_mismatch", Severity: "breaking",
		Explanation: "fragments are bounded to 5 s now, but the prose implies no bound", FixedBy: []int{2}},
	Finding{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "stale_reference", Severity: "inconsistent",
		Explanation: "readers of $Frame.Order are pointed to prose that misses the bound", FixedBy: []int{2}},
	Finding{File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent",
		Explanation: "mention the bound in the prose", Find: proseFind, Replace: proseReplace},
)

// twoForOne: one contradiction that needs two fixes.
var twoForOne = CheckReply("the bound needs the prose and the queue to follow", nil,
	Finding{File: "demo.spec.md", Anchor: "$Frame.Timing", Issue: "contradiction", Severity: "breaking",
		Explanation: "a 5 s bound contradicts the prose and the queue size", FixedBy: []int{1, 2}},
	Finding{File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent",
		Explanation: "mention the bound", Find: proseFind, Replace: proseReplace},
	Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch", Severity: "cosmetic",
		Explanation: "align the queue size", Find: limitFind, Replace: limitReplace},
)

// noFix: a problem the model proposed no fix for, and a question.
var noFix = CheckReply("one rule is unclear now", []string{"Should the 5 s bound apply to the last fragment too?"},
	Finding{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "ambiguity", Severity: "inconsistent",
		Explanation: "with fragments bounded, is the order by arrival or by media time?"},
)

// proseFix is one problem with its fix in the prose; proseFixInOrder
// says the fix belongs to $Frame.Order, where the prose is not.
var (
	proseFix = CheckReply("the prose should mention the bound", nil,
		Finding{File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent",
			Explanation: "mention the bound", Find: proseFind, Replace: proseReplace})
	proseFixInOrder = CheckReply("the prose should mention the bound", nil,
		Finding{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "missing_update", Severity: "inconsistent",
			Explanation: "mention the bound", Find: proseFind, Replace: proseReplace})
)

// checkTimingDemo edits and checks $Frame.Timing on the demo (no
// cascade), waits for cards cards, and focuses the sidebar.
func checkTimingDemo(cards int) []Step {
	return steps([]Step{editTiming}, []Step{
		{Note: "^P, save first", Keys: []string{"ctrl+p", "y"}, Wait: tui.Cond{Idle: true, Cards: cards}},
		{Note: "focus the sidebar", Keys: []string{"ctrl+r"}},
	})
}

// byHand types text into the prose line, from the editor.
func byHand(note, text string, wait tui.Cond) Step {
	return Step{Note: note, Find: "for the reader", Text: text, Wait: wait}
}

// fixScenes are the scenes of the problem and fix cards.
func fixScenes() []Scene {
	return []Scene{
		{
			Name: "fix-transcodercap",
			Description: "The user changes $Hub.TranscoderCap's 1 s to 2 s and presses ^P: under 'Your edit to $Hub.TranscoderCap' the problem box names both sides " +
				"(the edit ↔ $Frame.CleanGeneration) and the teal fix box sits under it. Selected, the fix moves the editor to $Frame.CleanGeneration " +
				"and draws the edit in place: the old word struck through, the new one as ghost text.",
			Fixture: Lensd,
			Open:    transcoderCap,
			Replies: infer.CannedFile{Check: []infer.CannedEntry{transcoderReply}},
			Steps: steps(checkTranscoder, []Step{
				{Note: "the problem selected: the editor shows the edit", Capture: "problem"},
				{Note: "select the fix: the editor shows it in place", Keys: []string{"down"}},
			}),
		},
		{
			Name:        "fix-one-resolves-two",
			Description: "Two problems name the same fix: they stack over one fix box, titled '1 fix resolves 2 problems'; accepting it settles both.",
			Fixture:     Demo,
			Open:        tui.Target{Name: "Frame.Timing"},
			Replies:     infer.CannedFile{Check: []infer.CannedEntry{oneResolvesTwo}},
			Steps: steps(checkTimingDemo(3), []Step{
				{Note: "the first problem selected", Capture: "problem"},
				{Note: "select the fix", Keys: []string{"down", "down"}},
			}),
		},
		{
			Name: "fix-two-for-one",
			Description: "One contradiction needs two fixes: '2 fixes for this problem' under the problem, and the check's title becomes a card " +
				"(y is labelled Accept All in the footer). After one fix is accepted the problem reads '1 of 2 fixes decided'.",
			Fixture: Demo,
			Open:    tui.Target{Name: "Frame.Timing"},
			Replies: infer.CannedFile{Check: []infer.CannedEntry{twoForOne}},
			Steps: steps(checkTimingDemo(4), []Step{
				{Note: "the problem selected", Capture: "problem"},
				{Note: "the title selected: y accepts all", Keys: []string{"up"}, Capture: "title"},
				{Note: "accept the first fix", Keys: []string{"down", "down", "y"}},
			}),
		},
		{
			Name:        "fix-none",
			Description: "A problem without a fix has its own trailing padding. The question stays readable, with the short instruction to answer by editing the spec.",
			Fixture:     Demo,
			Open:        tui.Target{Name: "Frame.Timing"},
			Replies:     infer.CannedFile{Check: []infer.CannedEntry{noFix}},
			Steps: steps(checkTimingDemo(2), []Step{
				{Note: "the problem selected", Capture: "problem"},
				{Note: "select the question", Keys: []string{"down"}},
			}),
		},
		{
			Name: "fix-done-by-hand",
			Description: "The user makes the fix's change by hand in the editor: the reparse records it as done by hand with no key, " +
				"the flash says so once and the group goes away.",
			Fixture: Demo,
			Open:    tui.Target{Name: "Frame.Timing"},
			Replies: infer.CannedFile{Check: []infer.CannedEntry{proseFix}},
			Steps: steps(checkTimingDemo(2), []Step{
				{Note: "back to the editor", Keys: []string{"esc"}, Capture: "pending"},
				byHand("type the fix by hand", "and the 5 s bound ", tui.Cond{Text: "Done by hand"}),
			}),
		},
		{
			Name: "fix-done-by-hand-unsure",
			Description: "The text now says what the fix would, but not inside $Frame.Order where the fix belongs: nothing is recorded on its own; " +
				"the fix reads 'done by hand: the text already says this · y records it' and an 'unsure:' line says why.",
			Fixture: Demo,
			Open:    tui.Target{Name: "Frame.Timing"},
			Replies: infer.CannedFile{Check: []infer.CannedEntry{proseFixInOrder}},
			Steps: steps(checkTimingDemo(2), []Step{
				{Note: "back to the editor", Keys: []string{"esc"}},
				byHand("type the fix by hand", "and the 5 s bound ", tui.Cond{Text: "Done by hand"}),
				{Note: "select the fix", Keys: []string{"ctrl+r", "down"}},
			}),
		},
		{
			Name: "fix-no-longer-applies",
			Description: "The text the fix replaces was changed some other way: the fix reads 'no longer applies'; n skips it and the problem " +
				"stays open ('open: fix skipped · n dismisses it, ^P rechecks').",
			Fixture: Demo,
			Open:    tui.Target{Name: "Frame.Timing"},
			Replies: infer.CannedFile{Check: []infer.CannedEntry{proseFix}},
			Steps: steps(checkTimingDemo(2), []Step{
				{Note: "back to the editor", Keys: []string{"esc"}},
				byHand("change the prose another way", "and its bound ", tui.Cond{Text: "No longer applies"}),
				{Note: "select the fix", Keys: []string{"ctrl+r", "down"}, Capture: "gone"},
				{Note: "n skips it", Keys: []string{"n"}},
			}),
		},
	}
}
