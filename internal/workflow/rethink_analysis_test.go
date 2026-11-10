// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/proposal"
)

// analysisQuestion is the analysis's question in these tests.
const analysisQuestion = "Should Two answer within 5 s, or within the 6 s the new capacity needs?"

// analyzedWithQuestion edits $A.One (at most 4 becomes at most 6) and
// analyzes the edits with the first reply: a question and no fix, filed
// all the same.
func analyzedWithQuestion(t *testing.T, replies ...string) (*Workflow, *Analysis, func() *proposal.Proposal) {
	t.Helper()
	proj := commitRepo(t)
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
	w := cannedWorkflow(t, replies...)
	an, e := w.Analyze(context.Background(), proj, AnalyzeRequest{Mode: ModeChanged, Author: proposal.AuthorHuman})
	require.Nil(t, e)
	require.True(t, an.Saved, "a question alone is filed: it can be answered")
	rethink := func() *proposal.Proposal {
		np, e := w.Rethink(context.Background(), proj, RethinkRequest{
			Of: an.Proposal, Note: proposal.RethinkNote{Question: analysisQuestion, Text: " Two answers within 6 s. "}, Author: proposal.AuthorHuman,
		})
		require.Nil(t, e)
		return np
	}
	return w, an, rethink
}

// An analysis's question is answered: the rethink proposes fixes anywhere
// in the spec (here inside the edited anchor's neighbour and the prose),
// keeps the analysis's scope and the notes, and changes no own edit.
func TestRethinkAnalysisProposesFixes(t *testing.T) {
	first := replyJSON(t, map[string]any{"summary": "the capacity and the answer time disagree", "findings": []map[string]any{{
		"file": "c.spec.md", "anchor": "$B.Two", "issue": "contradiction", "severity": "breaking",
		"explanation": "6 items need 6 s, but Two answers within 5 s",
	}}, "questions": []string{analysisQuestion}})
	second := replyJSON(t, map[string]any{
		"summary": "the author settles on 6 s",
		"findings": []map[string]any{
			{"file": "c.spec.md", "anchor": "$B.Two", "issue": "numeric_mismatch", "severity": "breaking",
				"explanation": "Two must answer within 6 s", "find": "Two MUST answer within 5 s.", "replace": "Two MUST answer within 6 s."},
			{"file": "c.spec.md", "issue": "missing_update", "severity": "cosmetic",
				"explanation": "the prose names the bound", "find": "Prose between the anchors.", "replace": "Prose between the anchors, with a 6 s bound."},
		},
		"questions": []string{},
	})
	_, an, rethink := analyzedWithQuestion(t, first, second)
	old := an.Proposal
	require.NotNil(t, old.Scope)
	assert.Equal(t, ModeChanged, old.Scope.Mode)
	assert.NotEmpty(t, old.Scope.Head, "a changed analysis records the HEAD it compared against")

	np := rethink()
	assert.Equal(t, proposal.KindAnalysis, np.Kind)
	assert.Nil(t, np.Primary)
	require.NotNil(t, np.Rethink)
	assert.Equal(t, old.ID, np.Rethink.Of)
	assert.Zero(t, np.Rethink.PrimaryHunk)
	assert.Equal(t, []proposal.RethinkNote{{Question: analysisQuestion, Text: "Two answers within 6 s."}}, np.Rethink.Notes)
	assert.Equal(t, old.Scope, np.Scope)
	require.Len(t, np.Hunks, 2)
	assert.Equal(t, []int{1, 2}, []int{np.Hunks[0].N, np.Hunks[1].N})
	assert.Equal(t, "$B.Two", np.Hunks[0].Anchor)
	assert.Empty(t, np.Questions)
	assert.NotEmpty(t, np.Base.Files)
}

// Only a pending analysis with notes is rethought.
func TestRethinkAnalysisRefusals(t *testing.T) {
	first := replyJSON(t, map[string]any{"summary": "unclear", "findings": []any{}, "questions": []string{analysisQuestion}})
	w, an, _ := analyzedWithQuestion(t, first)
	p := an.Proposal
	_, e := w.Rethink(context.Background(), nil, RethinkRequest{Of: p, Note: proposal.RethinkNote{Text: " "}})
	require.NotNil(t, e)
	assert.Equal(t, "bad_change", e.Code)
	done := *p
	done.Status = proposal.StatusRejected
	_, e = w.Rethink(context.Background(), nil, RethinkRequest{Of: &done, Note: proposal.RethinkNote{Text: "6 s"}})
	require.NotNil(t, e)
	assert.Equal(t, "not_pending", e.Code)
}

// The analysis rethink's messages name the analysis, not a review of a
// change, and its prompt has no primary block.
func TestRethinkAnalysisMessages(t *testing.T) {
	in := prompt.RethinkInput{Summary: "first", Questions: []prompt.RethinkQuestion{{Text: "q?", Decision: "answered in the author's notes below"}},
		Notes: []prompt.RethinkNote{{Question: "q?", Text: "a"}}}
	msgs := prompt.RethinkAnalysisMessages("system", &specctx.Context{}, in)
	require.Len(t, msgs, 2)
	u := msgs[1].Content
	assert.True(t, strings.HasPrefix(u, "Analyze this specification again in light of the author's notes."))
	assert.Contains(t, u, "## The first analysis")
	assert.Contains(t, u, "The analysis asked: q?\nThe author answered:\n<notes>\na\n</notes>")
	sys, err := prompt.Embedded(prompt.RethinkAnalysis)
	require.NoError(t, err)
	assert.NotContains(t, sys, `"primary"`)
	assert.Contains(t, sys, "in an edited region or not")
	assert.NotContains(t, sys, "Copyright")
}
