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

// The rethink (notes/plan-step4g-fix-review.md, "Rethink"): f on a check's
// question opens the notes box with the question quoted, ^O sends the
// answer, and the model's new check supersedes the old one; a change to
// Chris's own edit is a fix marked "changes your edit", applied only by y.

const (
	rethinkQuestion = "Should the 5 s bound stay, or follow the 3 s that the extra queue of 8 items implies at the usual rate?"
	atMost3s        = "of at most 3 s"
	prose3s         = "Prose that mentions $Frame.Order and the 3 s bound for the reader."
)

var (
	// askReply: the check asks which bound to keep.
	askReply = scenes.CheckReply("the bound may disagree with the extra queue", []string{rethinkQuestion})
	// rethinkReply: after the answer, the bound becomes 3 s (a change to
	// Chris's edit) and the prose follows.
	rethinkReply = rethinkCanned("the author wants 3 s: the bound and the prose change",
		atMost5s, atMost3s, scenes.Finding{File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent",
			Explanation: "the prose should mention the 3 s bound", Find: proseFind, Replace: prose3s})
)

// rethinkCanned is a check reply with a primary block.
func rethinkCanned(summary, find, replace string, fs ...scenes.Finding) infer.CannedEntry {
	e := scenes.CheckReply(summary, nil, fs...)
	e.Reply = strings.TrimSuffix(e.Reply, "}") + `,"primary":{"find":` + quoteJSON(find) + `,"replace":` + quoteJSON(replace) + `,"explanation":"the answer settles on 3 s"}}`
	return e
}

// quoteJSON quotes s as a JSON string (the texts here need no escapes
// beyond quotes).
func quoteJSON(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

// Answer the check's question: the rethink lands as a new check with a fix
// of the prose and a change to Chris's own edit; the old check is
// superseded, the notes kept; y on the group applies both.
func TestScenarioRethinkAnswer(t *testing.T) {
	s := Open(t, scenes.Demo, At("Frame.Timing"), Edits(timingEdit),
		Replies(infer.CannedFile{Check: []infer.CannedEntry{askReply, rethinkReply}}))
	s.Press("ctrl+p")
	s.WaitFor(tui.Cond{Idle: true, Cards: 1})
	s.Press("ctrl+r")
	s.ExpectCards("question")
	old := s.Proposals()[0]
	s.ExpectText("f answers it")
	s.ExpectHelpLive("f")

	s.Press("f")
	s.ExpectContext(tui.CtxHunkEdit)
	s.ExpectText("the model rethinks the check")
	s.Type("Use 3 s across the board.")
	s.Press("ctrl+o")
	s.WaitUntil("the rethink", func(st tui.State) bool {
		return st.Job == "" && len(st.CardsOf("fix")) == 2
	})
	s.ExpectText("changes your edit")

	var np *proposal.Proposal
	for _, p := range s.Proposals() {
		switch {
		case p.ID == old.ID:
			if p.Status != proposal.StatusRejected || !strings.Contains(p.Decision.Reason, "superseded by rethink") {
				s.fail("the old check: %s %+v", p.Status, p.Decision)
			}
		case p.Status == proposal.StatusPending:
			np = p
		}
	}
	if np == nil || np.Rethink == nil || np.Rethink.Of != old.ID || np.Rethink.PrimaryHunk == 0 {
		s.fail("the rethink: %+v", np)
	}
	if len(np.Rethink.Notes) != 1 || np.Rethink.Notes[0].Question != rethinkQuestion || np.Rethink.Notes[0].Text != "Use 3 s across the board." {
		s.fail("the notes: %+v", np.Rethink.Notes)
	}
	// Proposed, not applied.
	s.ExpectBuffer(atMost5s)

	for s.selected().Kind != "group" {
		s.Press("up")
	}
	s.Press("y")
	s.ExpectBuffer(atMost3s)
	s.ExpectBuffer(prose3s)
	s.ExpectNoBuffer(atMost5s)
	s.Press("ctrl+o")
	s.WaitUntil("the rethink recorded", func(tui.State) bool {
		for _, p := range s.Proposals() {
			if p.ID == np.ID {
				return p.Status == proposal.StatusApplied && len(p.Decision.Applied) == 3
			}
		}
		return false
	})
	s.ExpectCards()
}
