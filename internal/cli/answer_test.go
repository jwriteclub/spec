// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/proposal"
)

// replySequence answers the k-th request with contents[k] (the last one
// after that).
func replySequence(contents ...string) http.HandlerFunc {
	var mu sync.Mutex
	n := 0
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		k := min(n, len(contents)-1)
		n++
		mu.Unlock()
		okReply(contents[k])(w, r)
	}
}

// spec proposal ID --answer: the model rethinks the check with the answer;
// the new check (with its change to the edit itself) is filed and the old
// one is superseded.
func TestProposalAnswerRethinks(t *testing.T) {
	q := "Should the bound be 5 s or 3 s?"
	first := modelReply{Summary: "unclear bound", Questions: []string{q}}.json(t)
	second := `{"summary":"3 s","primary":{"find":"of at most 5 s","replace":"of at most 3 s","explanation":"the answer says 3 s"},"findings":[],"questions":[]}`
	is := setupInfer(t, replySequence(first, second))
	write(t, is.dir, "demo.spec.md", strings.Replace(baseSpec, timingOld, timingNew, 1))
	jr := is.run(t, "propose", "Frame.Timing", "--keep", "--source", "human", "--why", "mine")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	old := filedOf(t, jr).Proposals[0]

	jr = is.run(t, "proposal", old.ID, "--answer", "3 s, please", "--question", "2")
	assert.Equal(t, exitFail, jr.code, "only one question")

	jr = is.run(t, "proposal", old.ID, "--answer", "3 s, please")
	require.Equal(t, 0, jr.code, jr.stdout)
	var d proposalData
	jr.decode(t, &d)
	np := d.Proposal
	require.NotNil(t, np.Rethink)
	assert.Equal(t, old.ID, np.Rethink.Of)
	require.Len(t, np.Hunks, 1)
	assert.Equal(t, np.Hunks[0].N, np.Rethink.PrimaryHunk)
	assert.Equal(t, "of at most 3 s", np.Hunks[0].Replace)
	u := userMessage(t, is.srv, 1)
	assert.Contains(t, u, "The review asked: "+q+"\nThe author answered:\n<notes>\n3 s, please\n</notes>")

	human := is.runHuman(t, "", "proposal", np.ID)
	assert.Contains(t, human.stdout, "rethink of "+old.ID)
	assert.Contains(t, human.stdout, "(changes the edit itself)")
	jr = is.run(t, "proposal", old.ID)
	var od proposalData
	jr.decode(t, &od)
	assert.Equal(t, "rejected", od.Proposal.Status)
	assert.Equal(t, "superseded by rethink "+np.ID, od.Proposal.Decision.Reason)
}

// A check decided between loading it and taking the decision lock makes
// --answer fail without leaving the new check filed.
func TestProposalAnswerLeavesNoStrayCheck(t *testing.T) {
	first := modelReply{Summary: "unclear bound", Questions: []string{"5 s or 3 s?"}}.json(t)
	second := `{"summary":"3 s","findings":[],"questions":[]}`
	var decide func()
	seq := replySequence(first, second)
	var mu sync.Mutex
	calls := 0
	is := setupInfer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 2 && decide != nil {
			decide() // the rethink request: the old check is decided now
		}
		seq(w, r)
	})
	write(t, is.dir, "demo.spec.md", strings.Replace(baseSpec, timingOld, timingNew, 1))
	jr := is.run(t, "propose", "Frame.Timing", "--keep", "--source", "human", "--why", "mine")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	old := filedOf(t, jr).Proposals[0]
	st := proposal.NewStore(is.dir)
	decide = func() {
		p, err := st.Load(old.ID)
		require.NoError(t, err)
		p.Status = proposal.StatusRejected
		p.Decision = &proposal.Decision{Reason: "decided meanwhile", By: proposal.AuthorHuman}
		require.NoError(t, st.Save(p))
	}

	jr = is.run(t, "proposal", old.ID, "--answer", "3 s, please")
	assert.Equal(t, exitFail, jr.code, jr.stdout)
	all, err := st.List()
	require.NoError(t, err)
	for _, p := range all {
		assert.Equal(t, old.ID, p.ID, "no new check is left filed")
	}
}

// spec proposal ID --answer on an analysis: spec analyze files its
// question, the model rethinks the analysis with the answer and proposes
// fixes anywhere; the analysis is superseded. A later clean analysis of
// the same changes supersedes an earlier one nobody acted on, but not the
// answer.
func TestProposalAnswerAnalysis(t *testing.T) {
	requireGit(t)
	q := "Should the queue hold 8 items or 16?"
	ask := modelReply{Summary: "the sizes disagree", Questions: []string{q}}.json(t)
	fix := modelFinding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch", Severity: "breaking",
		Explanation: "16 items", Find: "The extra queue MUST hold at most 8 items.", Replace: "The extra queue MUST hold at most 16 items."}
	answered := modelReply{Summary: "16 everywhere", Findings: []modelFinding{fix}}.json(t)
	clean := modelReply{Summary: "No inconsistencies found."}.json(t)
	is := setupInfer(t, replySequence(ask, answered, ask, clean))
	replaceIn(t, is.dir, "demo.spec.md", "Prose that mentions $Frame.Order for the reader.", "The extra queue holds 16 items; see $Frame.Order.")

	jr := is.run(t, "analyze")
	require.Equal(t, 0, jr.code, jr.stdout)
	d := analyzeOf(t, jr)
	require.True(t, d.Saved)
	old := d.Proposal
	assert.Contains(t, d.Next.Text, "spec proposal "+old.ID+" --answer")

	jr = is.run(t, "proposal", old.ID, "--answer", "16 everywhere")
	require.Equal(t, 0, jr.code, jr.stdout)
	var pd proposalData
	jr.decode(t, &pd)
	np := pd.Proposal
	assert.Equal(t, proposal.KindAnalysis, np.Kind)
	require.NotNil(t, np.Rethink)
	assert.Equal(t, old.ID, np.Rethink.Of)
	require.Len(t, np.Hunks, 1)
	assert.Equal(t, "other.spec.md", np.Hunks[0].File)
	u := userMessage(t, is.srv, 1)
	assert.Contains(t, u, "## The first analysis")
	assert.Contains(t, u, "The analysis asked: "+q+"\nThe author answered:\n<notes>\n16 everywhere\n</notes>")
	jr = is.run(t, "proposal", old.ID)
	var od proposalData
	jr.decode(t, &od)
	assert.Equal(t, "rejected", od.Proposal.Status)
	assert.Equal(t, "superseded by rethink "+np.ID, od.Proposal.Decision.Reason)

	// Asked again, then clean: the earlier question goes, the answer stays.
	jr = is.run(t, "analyze")
	again := analyzeOf(t, jr).Proposal
	jr = is.run(t, "analyze")
	require.Equal(t, 0, jr.code, jr.stdout)
	d = analyzeOf(t, jr)
	assert.False(t, d.Saved)
	assert.Equal(t, []string{again.ID}, d.Superseded)
	jr = is.run(t, "proposal", again.ID)
	jr.decode(t, &od)
	assert.Equal(t, "rejected", od.Proposal.Status)
	assert.Equal(t, "superseded by a newer analysis of the same scope", od.Proposal.Decision.Reason)
	jr = is.run(t, "proposal", np.ID)
	jr.decode(t, &od)
	assert.Equal(t, "pending", od.Proposal.Status)
}
