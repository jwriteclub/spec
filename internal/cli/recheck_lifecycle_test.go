// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/proposal"
)

func TestDefaultAgentBatchRechecksBeforeSecondAutoApproval(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "clean"}.json(t)))
	desired := strings.Replace(strings.Replace(baseSpec, timingOld, timingNew, 1), "Fragments MUST arrive in order", "Fragments MUST always arrive in order", 1)
	write(t, is.dir, "demo.spec.md", desired)
	jr := is.run(t, "propose", "Frame.Timing", "Frame.Order", "--why", "two changes", "--auto-approve")
	require.Equal(t, 0, jr.code, jr.stdout)
	ps := filedOf(t, jr).Proposals
	require.Len(t, ps, 2)
	for _, p := range ps {
		assert.Equal(t, "agent", p.SourceKind)
		assert.Equal(t, proposal.StatusApplied, p.Status)
	}
	assert.Greater(t, ps[1].CheckGeneration, ps[0].CheckGeneration)
	assert.Equal(t, desired, readFile(t, is.dir, "demo.spec.md"))
}

func TestRecheckCLIUsesSameIDAfterContextChange(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "clean"}.json(t)))
	write(t, is.dir, "body.md", timingNew)
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "bound it")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	write(t, is.dir, "extra.spec.md", "# Extra\n\nNew surrounding context.\n")
	jr = is.run(t, "recheck", p.ID)
	require.Equal(t, 0, jr.code, jr.stdout)
	var data proposalData
	jr.decode(t, &data)
	assert.Equal(t, p.ID, data.Proposal.ID)
	assert.Equal(t, p.Primary, data.Proposal.Primary)
	assert.Equal(t, "complete", data.Proposal.CheckState)
	jr = is.run(t, "approve", p.ID)
	require.Equal(t, 0, jr.code, jr.stdout)
}

func TestDefaultAgentAutoApproveLeavesFreshQuestionsPending(t *testing.T) {
	requireGit(t)
	var calls atomic.Int32
	clean := modelReply{Summary: "clean"}.json(t)
	question := modelReply{Summary: "needs clarification", Questions: []string{"Is the combined behavior intended?"}}.json(t)
	is := setupInfer(t, func(w http.ResponseWriter, r *http.Request) {
		reply := clean
		if calls.Add(1) == 3 {
			reply = question
		}
		okReply(reply)(w, r)
	})
	desired := strings.Replace(strings.Replace(baseSpec, timingOld, timingNew, 1), "Fragments MUST arrive in order", "Fragments MUST always arrive in order", 1)
	write(t, is.dir, "demo.spec.md", desired)
	jr := is.run(t, "propose", "Frame.Timing", "Frame.Order", "--why", "two changes", "--auto-approve")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	ps := filedOf(t, jr).Proposals
	require.Len(t, ps, 2)
	assert.NotEmpty(t, ps[0].ID)
	assert.NotEmpty(t, ps[1].ID)
	assert.Equal(t, proposal.StatusApplied, ps[0].Status)
	assert.Equal(t, proposal.StatusPending, ps[1].Status)
	assert.Equal(t, []string{"Is the combined behavior intended?"}, ps[1].Questions)
	assert.Contains(t, readFile(t, is.dir, "demo.spec.md"), ps[0].Primary.NewText)
	assert.Contains(t, readFile(t, is.dir, "demo.spec.md"), ps[1].Primary.OldText)
	assert.Equal(t, int32(3), calls.Load())
}

func TestHumanCLIWithoutKeepRetainsMandatoryPrimary(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "clean"}.json(t)))
	write(t, is.dir, "demo.spec.md", strings.Replace(baseSpec, timingOld, timingNew, 1))
	jr := is.run(t, "propose", "--source", "human", "--by", "Chris", "--why", "bound it")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	assert.Equal(t, "human", p.SourceKind)
	assert.Equal(t, "Chris", p.Author)
	assert.False(t, p.Primary.InWorktree)
	assert.Equal(t, baseSpec, readFile(t, is.dir, "demo.spec.md"))
	jr = is.run(t, "approve", p.ID)
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Contains(t, readFile(t, is.dir, "demo.spec.md"), timingNew)
}

func TestInteractiveReviewOmitsEmptyAuthorClause(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "clean"}.json(t)))
	write(t, is.dir, "body.md", timingNew)
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "bound it")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	assert.Empty(t, p.Author)
	out := is.runHuman(t, "q\n", "review", p.ID)
	assert.Contains(t, out.stdout, "proposal "+p.ID+" (change)")
	assert.NotContains(t, out.stdout, ", by ")
	stored, err := proposal.NewStore(is.dir).Load(p.ID)
	require.NoError(t, err)
	assert.Empty(t, stored.Author)
}
