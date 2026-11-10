// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package scenes

import (
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/tui"
)

// Texts the demo scenes edit.
const (
	aboveZero    = "above zero"
	atMost5s     = "of at most 5 s"
	proseFind    = "Prose that mentions $Frame.Order for the reader."
	proseReplace = "Prose that mentions $Frame.Order and the 5 s bound for the reader."
	limitFind    = "MUST hold at most 8 items"
	limitReplace = "MUST hold at most 5 items"
	pdtFind      = "PROGRAM-DATE-TIME (wall time from NVR keyframe stamps"
	pdtReplace   = "PROGRAM-DATE-TIME ($Time.Wall, from NVR keyframe stamps"
)

// timingEdit bounds $Frame.Timing in the working tree.
var timingEdit = Edit{File: "demo.spec.md", Find: aboveZero, Replace: atMost5s}

// boundReply is a check of the $Frame.Timing bound: the prose and the
// extra queue should follow it.
var boundReply = CheckReply("the prose and the extra queue should follow the new bound", nil,
	Finding{File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent",
		Explanation: "the prose about $Frame.Order should mention the 5 s bound", Find: proseFind, Replace: proseReplace},
	Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch", Severity: "cosmetic",
		Explanation: "the extra queue holds one fragment per second of the bound: 5 items", Find: limitFind, Replace: limitReplace},
)

// cleanReply is a check that found nothing.
var cleanReply = CheckReply("the bound is consistent with every rule that cites $Frame.Timing", nil)

// editTiming bounds $Frame.Timing in the UI: select "above zero", type
// over it.
var editTiming = Step{Note: "bound $Frame.Timing", Find: aboveZero, Keys: []string{
	"shift+right", "shift+right", "shift+right", "shift+right", "shift+right",
	"shift+right", "shift+right", "shift+right", "shift+right", "shift+right",
}, Text: atMost5s}

// checkTiming edits $Frame.Timing and checks it with ^P, saving first.
var checkTiming = []Step{
	editTiming,
	{Note: "^P asks to save first", Keys: []string{"ctrl+p"}, Capture: "save-first"},
	{Note: "save and check", Keys: []string{"y"}},
}

// agentTiming is agent-7's proposal to bound $Frame.Timing, with the two
// follow-on fixes of boundReply.
var agentTiming = AgentChange{Author: "agent-7", Why: "bound fragment length", Edits: []Edit{timingEdit}}

// steps joins step lists.
func steps(parts ...[]Step) []Step {
	var out []Step
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// transcoderCap opens lensd at $Hub.TranscoderCap.
var transcoderCap = tui.Target{Name: "Hub.TranscoderCap"}

// All returns every scene, in gallery order.
func All() []Scene {
	return append([]Scene{
		{
			Name:        "editor",
			Description: "The editor alone on a formatted spec: anchors, definitions, references and section headings in their colours, the anchor under the cursor ticked in the gutter, the outline hidden.",
			Fixture:     LensdFormatted,
			Open:        transcoderCap,
			Steps:       []Step{{Note: "hide the outline (it shows by default)", Keys: []string{"alt+o", "alt+o"}}},
		},
		{
			Name:        "outline",
			Description: "The outline focused beside the editor: files, sections, definitions and invariants, the focus edge on its border.",
			Fixture:     LensdFormatted,
			Open:        transcoderCap,
			Steps:       []Step{{Note: "focus the outline and move down", Keys: []string{"alt+o", "down", "down", "down"}}},
		},
		{
			Name:        "quick-open",
			Description: "^Q quick open: fuzzy-find anchors, sections and files, as a list over the editor.",
			Fixture:     LensdFormatted,
			Open:        transcoderCap,
			Steps:       []Step{{Note: "quick open, filter", Keys: []string{"ctrl+q"}, Text: "frame"}},
		},
		{
			Name:        "hover-popup",
			Description: "^L on a reference: the popup previews the anchor it names.",
			Fixture:     LensdFormatted,
			Open:        tui.Target{Name: "Frame.Forward"},
			Steps:       []Step{{Note: "look at $Frame.Timing", Find: "$Frame.Timing).", Keys: []string{"ctrl+l"}}},
		},
		{
			Name:        "diag-popup",
			Description: "^L on a warning: the diagnostic popup with its fix key (the def-unreferenced warning on $Time.Wall).",
			Fixture:     Lensd,
			Open:        tui.Target{Name: "Time.Wall"},
			Steps:       []Step{{Note: "look at the warning", Find: "$Time.Wall (wall", Keys: []string{"ctrl+l"}}},
		},
		{
			Name:        "help-screen",
			Description: "^G: every key binding, by context.",
			Fixture:     Demo,
			Steps:       []Step{{Keys: []string{"ctrl+g"}}},
		},
		{
			Name:        "helpbar-states",
			Description: "The help bar follows the state: on a clean tree ^R says Checks and ^P is dimmed; once an anchor is edited ^R says Commit and ^P Propose is live.",
			Fixture:     Demo,
			Open:        tui.Target{Name: "Frame.Timing"},
			Steps: []Step{
				{Note: "a clean tree", Capture: "clean"},
				editTiming,
			},
		},
		{
			Name:        "gutter-marks",
			Description: "The gutter after ^F reflowed the whole file: a thin cyan pipe on format-only lines, a thin green pipe on a prose edit, the thick bar on the changed anchor; ^] steps through the edits and its flash names each kind.",
			Fixture:     Lensd,
			Edits: []Edit{
				{File: "lensd.spec.md", Find: pdtFind, Replace: pdtReplace},
				{File: "lensd.spec.md", Find: "lensd is a Go web gateway", Replace: "lensd is a Go HTTP gateway"},
			},
			Steps: []Step{
				{Note: "format the buffer", Keys: []string{"ctrl+f"}, Capture: "formatted"},
				{Note: "next edit: format only", Keys: []string{"ctrl+]"}, Capture: "next-format"},
				{Note: "next edit: the prose change", Keys: []string{"ctrl+]", "ctrl+]"}, Capture: "next-prose"},
				{Note: "to the changed anchor", Find: "## 1. Frames", Keys: []string{"ctrl+]", "ctrl+]", "ctrl+]", "ctrl+]", "ctrl+]", "ctrl+]", "ctrl+]"}},
			},
		},
		{
			Name:        "lint-fix-card",
			Description: "^P on a warning asks the model for a fix; the suggested edit lands in the Checks sidebar as a card.",
			Fixture:     Lensd,
			Sizes:       []Size{Size80, Size140, Size220},
			Open:        tui.Target{Name: "Time.Wall"},
			Replies: infer.CannedFile{Fix: []infer.CannedEntry{CheckReply("cite $Time.Wall where wall time is used", nil,
				Finding{File: "lensd.spec.md", Anchor: "$Frame.Forward", Issue: "def-unreferenced", Severity: "cosmetic",
					Explanation: "PROGRAM-DATE-TIME is wall time: cite $Time.Wall there", Find: pdtFind, Replace: pdtReplace})}},
			Steps: []Step{
				{Note: "on the warning, ^P fixes it", Find: "$Time.Wall (wall", Keys: []string{"ctrl+p"}, Wait: tui.Cond{Idle: true, Cards: 1}},
				{Note: "focus the sidebar", Keys: []string{"ctrl+r"}},
			},
		},
		{
			Name:        "agent-proposal",
			Description: "An agent's proposal (agent-7 bounds $Frame.Timing, with two follow-on fixes) waiting in the Checks sidebar; the outline and the help bar count it.",
			Fixture:     Demo,
			Sizes:       []Size{Size80, Size140, Size220},
			Agents:      []AgentChange{agentTiming},
			Replies:     infer.CannedFile{Check: []infer.CannedEntry{boundReply}},
			Steps:       []Step{{Note: "show the sidebar", Keys: []string{"ctrl+r"}}},
		},
		{
			Name:        "review",
			Description: "The review screen of an agent's proposal: the change and each follow-on hunk as a diff, accepted or skipped one by one.",
			Fixture:     Demo,
			Agents:      []AgentChange{agentTiming},
			Replies:     infer.CannedFile{Check: []infer.CannedEntry{boundReply}},
			Steps: []Step{
				{Note: "open the proposal", Keys: []string{"ctrl+r", "enter"}},
				{Note: "y accepts the selected hunk and moves to the next", Keys: []string{"y"}},
			},
		},
		{
			Name:        "cascade-running",
			Description: "^P on an edited invariant while the cascade runs: the gate escalated, triage is running; the card and the status line show the chain and the editor keeps the keyboard.",
			Fixture:     Demo,
			Sizes:       []Size{Size80, Size140, Size220},
			Cascade:     true,
			Replies: infer.CannedFile{
				Gate:   []infer.CannedEntry{GateReply(0.1, 0.62, 0.05)},
				Triage: []infer.CannedEntry{Slow(TriageReply(0.7, 0.2, 0), 60000)},
			},
			Steps: steps(checkTiming, []Step{{Note: "wait for triage to start", Wait: tui.Cond{Stage: "triage"}}}),
		},
		{
			Name:        "gate-cleared",
			Description: "The gate clears the edit (every class score low): no check runs, the card says why.",
			Fixture:     Demo,
			Cascade:     true,
			Replies:     infer.CannedFile{Gate: []infer.CannedEntry{GateReply(0.08, 0.1, 0.05)}},
			Steps:       steps(checkTiming, []Step{{Note: "wait for the gate", Wait: tui.Cond{Idle: true}}}),
		},
		{
			Name:        "triage-cleared",
			Description: "The gate escalates, triage clears the edit: no conflict found, the stage lines under the headline.",
			Fixture:     Demo,
			Cascade:     true,
			Replies: infer.CannedFile{
				Gate:   []infer.CannedEntry{GateReply(0.1, 0.62, 0.05)},
				Triage: []infer.CannedEntry{TriageReply(0.1, 0.05, 0)},
			},
			Steps: steps(checkTiming, []Step{{Note: "wait for triage", Wait: tui.Cond{Idle: true}}}),
		},
		{
			Name:        "check-finding",
			Description: "Gate and triage escalate and the check files two suggested edits: the sidebar lists the stages, the summary and a card per edit, the selected one expanded.",
			Fixture:     Demo,
			Sizes:       []Size{Size80, Size140, Size220},
			Cascade:     true,
			Replies: infer.CannedFile{
				Gate:   []infer.CannedEntry{GateReply(0.1, 0.62, 0.05)},
				Triage: []infer.CannedEntry{TriageReply(0.7, 0.2, 0)},
				Check:  []infer.CannedEntry{boundReply},
			},
			Steps: steps(checkTiming, []Step{
				{Note: "wait for the check", Wait: tui.Cond{Idle: true, Cards: 2}},
				{Note: "focus the sidebar", Keys: []string{"ctrl+r"}},
			}),
		},
		{
			Name:        "check-outdated",
			Description: "A further edit of the checked anchor: the check is marked outdated and its cards cannot be accepted until ^P rechecks.",
			Fixture:     Demo,
			Cascade:     true,
			Replies: infer.CannedFile{
				Gate:   []infer.CannedEntry{GateReply(0.1, 0.62, 0.05)},
				Triage: []infer.CannedEntry{TriageReply(0.7, 0.2, 0)},
				Check:  []infer.CannedEntry{boundReply},
			},
			Steps: steps(checkTiming, []Step{
				{Note: "wait for the check", Wait: tui.Cond{Idle: true, Cards: 2}},
				{Note: "change 5 s to 7 s", Find: "5 s", Keys: []string{"shift+right"}, Text: "7", Wait: tui.Cond{Text: "Needs refresh"}},
				{Note: "focus the sidebar (^R would recheck the outdated anchor)", Keys: []string{"shift+tab"}},
			}),
		},
		{
			Name:        "analyze",
			Description: "^N analyzes the edits since HEAD: a suggested edit, a note without a fix and a question for the author.",
			Fixture:     Demo,
			Edits:       []Edit{{File: "other.spec.md", Find: "Checked by: TestLimit.", Replace: "Drained every 30 s. Checked by: TestLimit."}},
			Replies: infer.CannedFile{Check: []infer.CannedEntry{CheckReply("one size disagrees with the drain interval", []string{"Is 30 s the drain interval everywhere?"},
				Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch", Severity: "inconsistent",
					Explanation: "8 items here, but draining every 30 s needs room for 5 at most", Find: limitFind, Replace: limitReplace},
				Finding{File: "demo.spec.md", Issue: "ambiguity", Severity: "cosmetic", Explanation: "the drain interval is not defined anywhere else"},
			)}},
			Steps: []Step{
				{Note: "analyze", Keys: []string{"ctrl+n"}, Wait: tui.Cond{Idle: true, Cards: 3}},
				{Note: "focus the sidebar (^R would check the edited anchor)", Keys: []string{"shift+tab"}},
			},
		},
		{
			Name:        "commit-view",
			Description: "The commit view: the changes since HEAD, ticked, with the message the fast model drafted.",
			Fixture:     Demo,
			Edits:       []Edit{timingEdit},
			Replies: infer.CannedFile{
				Check: []infer.CannedEntry{cleanReply},
				Draft: []infer.CannedEntry{DraftReply("spec: bound fragment duration to 5 s", "$Frame.Timing now caps a fragment at 5 s of media time instead of\nonly requiring it to be positive.")},
			},
			Steps: []Step{
				{Note: "^R checks the unchecked edit", Keys: []string{"ctrl+r"}, Wait: tui.Cond{Idle: true}},
				{Note: "open the commit view", Keys: []string{"A"}, Wait: tui.Cond{Idle: true}},
			},
		},
		{
			Name:        "commit-message-box",
			Description: "The centred editor box: c in the commit view edits the drafted message.",
			Fixture:     Demo,
			Edits:       []Edit{timingEdit},
			Replies: infer.CannedFile{
				Check: []infer.CannedEntry{cleanReply},
				Draft: []infer.CannedEntry{DraftReply("spec: bound fragment duration to 5 s", "$Frame.Timing now caps a fragment at 5 s of media time instead of\nonly requiring it to be positive.")},
			},
			Steps: []Step{
				{Note: "^R checks the unchecked edit", Keys: []string{"ctrl+r"}, Wait: tui.Cond{Idle: true}},
				{Note: "open the commit view", Keys: []string{"A"}, Wait: tui.Cond{Idle: true}},
				{Note: "edit the message", Keys: []string{"c"}},
			},
		},
		{
			Name:        "narrow-sidebar",
			Description: "At 100 columns the sidebar has no room for a column and is drawn over the text.",
			Fixture:     Demo,
			Sizes:       []Size{Size100},
			Agents:      []AgentChange{agentTiming},
			Replies:     infer.CannedFile{Check: []infer.CannedEntry{boundReply}},
			Steps:       []Step{{Note: "show the sidebar", Keys: []string{"ctrl+r"}}},
		},
	}, append(append(fixScenes(), settingsScenes()...), formalScenes()...)...)
}
