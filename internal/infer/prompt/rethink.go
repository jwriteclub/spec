// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package prompt

import (
	"fmt"
	"strings"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/infer/specctx"
)

// Rethink redoes a check of the author's edit with the author's notes
// (prompts/rethink.md): the reply has a check's shape plus an optional
// primary block, a change to the author's own anchor.
const Rethink = "rethink"

// RethinkAnalysis redoes a consistency analysis with the author's notes
// (prompts/rethink-analysis.md): the reply has an analysis's shape.
const RethinkAnalysis = "rethink-analysis"

// RethinkItem is one finding of the first check, as the rethink is told
// about it.
type RethinkItem struct {
	File, Anchor, Issue, Severity, Explanation string
	// Find and Replace are its suggested fix ("" for a finding without
	// one).
	Find, Replace string
	// Decision is what the author did with it, in words ("accepted: the
	// files already have it", "skipped", "undecided").
	Decision string
}

// RethinkQuestion is one question of the first check and what became of
// it.
type RethinkQuestion struct {
	Text     string
	Decision string
}

// RethinkNote is one of the author's notes: the question it answers ("" for
// none) and the note.
type RethinkNote struct {
	Question string
	Text     string
}

// RethinkInput is the first check and the author's notes.
type RethinkInput struct {
	Summary   string
	Items     []RethinkItem
	Questions []RethinkQuestion
	Notes     []RethinkNote
}

// RethinkMessages renders the messages that redo a check over c (the
// context the check had) with the first check and the author's notes.
func RethinkMessages(system string, c *specctx.Context, in RethinkInput) []infer.Message {
	return rethinkMessages(system, "Review this proposed change again in light of the author's notes.", "review", c, in)
}

// RethinkAnalysisMessages renders the messages that redo an analysis over
// c (what an analysis of the same scope sends now) with the first
// analysis and the author's notes.
func RethinkAnalysisMessages(system string, c *specctx.Context, in RethinkInput) []infer.Message {
	return rethinkMessages(system, "Analyze this specification again in light of the author's notes.", "analysis", c, in)
}

// rethinkMessages renders a rethink: lead, the context c, the first run
// (a review or an analysis: noun) and the notes.
func rethinkMessages(system, lead, noun string, c *specctx.Context, in RethinkInput) []infer.Message {
	var u strings.Builder
	u.WriteString(lead + " Answer with the JSON object only.\n\n")
	u.WriteString(c.Render())
	fmt.Fprintf(&u, "\n## The first %s\n\n", noun)
	fmt.Fprintf(&u, "summary: %s\n", oneLine(in.Summary))
	if len(in.Items) == 0 {
		u.WriteString("\nIt found nothing to report.\n")
	} else {
		u.WriteString("\n### Its findings and suggested fixes\n")
	}
	for k, it := range in.Items {
		fmt.Fprintf(&u, "\n%d. %s %s in %s", k+1, it.Severity, it.Issue, it.File)
		if it.Anchor != "" {
			fmt.Fprintf(&u, " (%s)", it.Anchor)
		}
		fmt.Fprintf(&u, ": %s\n", oneLine(it.Explanation))
		if it.Find != "" {
			u.WriteString("Suggested fix, replacing:\n")
			writeTag(&u, "find", it.Find)
			u.WriteString("with:\n")
			writeTag(&u, "replace", it.Replace)
		}
		fmt.Fprintf(&u, "The author's decision: %s\n", it.Decision)
	}
	if len(in.Questions) > 0 {
		u.WriteString("\n### Its questions\n\n")
		for _, q := range in.Questions {
			fmt.Fprintf(&u, "- %s (%s)\n", oneLine(q.Text), q.Decision)
		}
	}
	u.WriteString("\n## The author's notes\n")
	for _, n := range in.Notes {
		u.WriteString("\n")
		if n.Question != "" {
			fmt.Fprintf(&u, "The %s asked: %s\nThe author answered:\n", noun, oneLine(n.Question))
		} else {
			u.WriteString("The author wrote:\n")
		}
		writeTag(&u, "notes", strings.TrimSpace(n.Text))
	}
	return []infer.Message{infer.System(system), infer.User(u.String())}
}

// oneLine joins the lines and runs of space of s.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
