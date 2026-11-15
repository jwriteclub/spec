// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// batchWork is commitSpec with $A.One changed and two anchors added at the
// end, the first referencing the second.
var batchWork = strings.Replace(commitSpec, "at most 4", "at most 6", 1) +
	"\n**$C.New (new).** New MUST use $D.New.\nChecked by: TestNew.\n\n" +
	"**Def $D.New (dnew).** A dnew is a thing with a name.\n"

// batchFinding is a check reply with one follow-on edit of the prose.
const batchFinding = `{"summary":"one prose fix","findings":[{"file":"c.spec.md","anchor":"","issue":"missing_update","severity":"cosmetic","explanation":"prose","find":"Prose between the anchors.","replace":"Prose between the two anchors."}],"questions":[]}`

// fileBatch files batchWork's three anchors with one spec propose run and a
// check that answers reply.
func fileBatch(t *testing.T, reply string) (*project.Project, *cascadeEnv, *proposal.Proposal) {
	t.Helper()
	proj := commitRepo(t)
	write(t, proj.Root, "c.spec.md", batchWork)
	env := newCascadeEnv(t, "")
	env.check.reply = chatReply(reply)
	res, e := env.wf.Propose(context.Background(), proj, ProposeRequest{Names: []string{"C.New", "D.New", "A.One"}, SourceKind: "agent", Author: "d1", Why: "section 1"})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 1, "one run files one proposal")
	return proj, env, res.Proposals[0]
}

func TestBatchProposeFilesOneProposalWithOneCheck(t *testing.T) {
	proj, env, p := fileBatch(t, batchFinding)
	assert.Equal(t, []string{"$A.One", "$C.New", "$D.New"}, p.Names(), "primaries in file order")
	assert.Equal(t, []int{1, 2, 3}, p.PrimaryNumbers())
	require.Len(t, p.Hunks, 1)
	assert.Equal(t, 4, p.Hunks[0].N, "follow-on hunks are numbered after the batch")
	assert.Equal(t, "complete", p.CheckState)
	assert.NotEmpty(t, p.CheckDeps)
	assert.Empty(t, p.CheckFiles)
	assert.Equal(t, commitSpec, read(t, proj.Root, "c.spec.md"), "every named anchor's HEAD text is restored")

	reqs := env.check.requests()
	require.Len(t, reqs, 1, "one check covers the batch")
	raw, _ := json.Marshal(reqs[0])
	body := string(raw)
	assert.Contains(t, body, "one batch of 3")
	assert.Contains(t, body, "(added by this batch)", "batch-mates are in the anchor index")
	assert.Contains(t, body, "$D.New")

	stored, err := env.wf.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Len(t, stored.Batch, 2)
}

func TestBatchApproveDefaultSkipsFollowOns(t *testing.T) {
	proj, env, p := fileBatch(t, batchFinding)
	res, e := env.wf.Apply(proj.Root, p.ID, nil, "reviewer", false)
	require.Nil(t, e)
	assert.Equal(t, proposal.StatusApplied, res.Proposal.Status)
	assert.Equal(t, []int{1, 2, 3}, res.Proposal.Decision.Applied)
	assert.Equal(t, []int{1, 2, 3}, res.Proposal.Decision.Accepted)
	assert.Equal(t, []int{4}, res.Skipped)
	assert.Equal(t, batchWork, read(t, proj.Root, "c.spec.md"), "the batch lands as written, adjacent adds in order")
}

func TestBatchApproveAllAppliesFollowOns(t *testing.T) {
	proj, env, p := fileBatch(t, batchFinding)
	res, e := env.wf.Apply(proj.Root, p.ID, p.Numbers(), "reviewer", false)
	require.Nil(t, e)
	assert.Equal(t, []int{1, 2, 3, 4}, res.Proposal.Decision.Applied)
	assert.Empty(t, res.Skipped)
	assert.Contains(t, read(t, proj.Root, "c.spec.md"), "Prose between the two anchors.")
}

func TestBatchCannotLeaveOutOneOfItsChanges(t *testing.T) {
	proj, env, p := fileBatch(t, batchFinding)
	_, e := env.wf.Apply(proj.Root, p.ID, []int{1, 2, 4}, "reviewer", false)
	require.NotNil(t, e)
	assert.Equal(t, "usage", e.Code)
	assert.Contains(t, e.Message, "batch")
	assert.Equal(t, commitSpec, read(t, proj.Root, "c.spec.md"))
}

func TestBatchUnrelatedEditLeavesCheckFresh(t *testing.T) {
	proj, env, p := fileBatch(t, batchFinding)
	// $B.Two is in no batch anchor's neighborhood, and a new file that
	// references nothing of it is not either.
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "within 5 s", "within 9 s", 1))
	write(t, proj.Root, "other.spec.md", "# Other\n\n**$Z.Other (other).** Other MUST be quiet.\nChecked by: TestOther.\n")
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	stored, err := env.wf.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Nil(t, CheckSnapshot(stored, loaded.Spec))
	res, e := env.wf.Apply(proj.Root, p.ID, nil, "reviewer", false)
	require.Nil(t, e)
	assert.Equal(t, proposal.StatusApplied, res.Proposal.Status)
	assert.Contains(t, read(t, proj.Root, "c.spec.md"), "within 9 s")
}

func TestBatchRelatedEditStalesCheck(t *testing.T) {
	for name, edit := range map[string]func(root string){
		"target's neighbor": func(root string) {
			write(t, root, "c.spec.md", strings.Replace(commitSpec, "within 5 s.", "within 5 s, as $A.One allows.", 1))
		},
		"prose mention": func(root string) {
			write(t, root, "c.spec.md", strings.Replace(commitSpec, "Prose between the anchors.", "Prose about $C.New.", 1))
		},
		"a new anchor references a target": func(root string) {
			write(t, root, "other.spec.md", "# Other\n\n**$Z.Uses (uses).** Uses MUST respect $A.One.\nChecked by: TestUses.\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			proj, env, p := fileBatch(t, batchFinding)
			edit(proj.Root)
			_, e := env.wf.Apply(proj.Root, p.ID, nil, "reviewer", false)
			require.NotNil(t, e)
			assert.Equal(t, "stale_check", e.Code)
		})
	}
}

func TestBatchCheckToleratesUnrelatedConcurrentEdit(t *testing.T) {
	proj := commitRepo(t)
	write(t, proj.Root, "c.spec.md", batchWork)
	env := newCascadeEnv(t, "")
	env.check.reply = chatReply(batchFinding)
	env.wf.BeforeCommit = func() {
		if len(env.check.requests()) > 0 {
			write(t, proj.Root, "late.spec.md", "# Late\n\nAn unrelated rule, saved while checking.\n")
		}
	}
	res, e := env.wf.Propose(context.Background(), proj, ProposeRequest{Names: []string{"C.New", "D.New", "A.One"}, SourceKind: "agent", Why: "section 1"})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 1)
	assert.Equal(t, "complete", res.Proposals[0].CheckState)
}

func TestBatchWaitReportsOneOutcomeAndWithdrawWorks(t *testing.T) {
	proj, env, p := fileBatch(t, batchFinding)
	done := make(chan []*proposal.Proposal, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ps, err := env.wf.Wait(ctx, proj.Root, []string{p.ID}, 50*time.Millisecond)
		assert.NoError(t, err)
		done <- ps
	}()
	time.Sleep(100 * time.Millisecond)
	_, e := env.wf.Reject(proj.Root, p.ID, "withdrawn: splitting the section", "d1")
	require.Nil(t, e)
	ps := <-done
	require.Len(t, ps, 1)
	assert.Equal(t, proposal.StatusRejected, ps[0].Status)
	assert.Equal(t, "d1", ps[0].Decision.By)
	assert.Equal(t, commitSpec, read(t, proj.Root, "c.spec.md"), "a withdrawn batch writes nothing")

	// Refiling the same anchors files a new batch.
	write(t, proj.Root, "c.spec.md", batchWork)
	res, e := env.wf.Propose(context.Background(), proj, ProposeRequest{Names: []string{"C.New", "D.New", "A.One"}, SourceKind: "agent", Author: "d1", Why: "section 1, again"})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 1)
	assert.NotEqual(t, p.ID, res.Proposals[0].ID)
	assert.Equal(t, "complete", res.Proposals[0].CheckState)
}

func TestLegacyWholeTreeSnapshotStillReads(t *testing.T) {
	proj := commitRepo(t)
	p := recoveryProposal(t, proj)
	require.NotEmpty(t, p.CheckFiles)
	require.Empty(t, p.CheckDeps)
	// A legacy check stays fresh while no spec file changed, and goes stale
	// on any change, as before.
	_, e := (&Workflow{}).Apply(proj.Root, p.ID, nil, "human", false)
	require.Nil(t, e)

	proj2 := commitRepo(t)
	q := recoveryProposal(t, proj2)
	write(t, proj2.Root, "other.spec.md", "# Other\n")
	_, e = (&Workflow{}).Apply(proj2.Root, q.ID, nil, "human", false)
	require.NotNil(t, e)
	assert.Equal(t, "stale_check", e.Code)
}
