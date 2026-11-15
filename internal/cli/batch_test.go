// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/proposal"
)

// batchEdit changes $Frame.Timing and adds $Frame.Gap, which references it.
func batchEdit() string {
	gap := "**$Frame.Gap (frame gap).** Two fragments are at most 2 s apart, see $Frame.Timing.\nChecked by: TestGap."
	edited := strings.Replace(baseSpec, timingOld, timingNew, 1)
	return strings.Replace(edited, orderOld+"\n", orderOld+"\n\n"+gap+"\n", 1)
}

func TestProposeBatchApproveDefaultSkipsFollowOns(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok", Findings: []modelFinding{proseFix}}.json(t)))
	edited := batchEdit()
	write(t, is.dir, "demo.spec.md", edited)

	jr := is.run(t, "propose", "Frame.Timing", "Frame.Gap", "--why", "bound and gap", "--by", "d1")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	d := filedOf(t, jr)
	require.Len(t, d.Proposals, 1)
	p := d.Proposals[0]
	assert.Equal(t, []string{"$Frame.Timing", "$Frame.Gap"}, p.Names())
	require.Len(t, p.Hunks, 1)
	assert.Equal(t, 3, p.Hunks[0].N)
	bodies, _ := is.srv.requests()
	assert.Len(t, bodies, 1, "one check for the batch")
	assert.Contains(t, userMessage(t, is.srv, 0), "one batch of 2")

	h := is.runHuman(t, "", "proposals")
	assert.Contains(t, h.stdout, "$Frame.Timing +1")
	h = is.runHuman(t, "", "proposal", p.ID)
	assert.Contains(t, h.stdout, "batch of 2 anchors (hunks 1,2)")
	assert.Contains(t, h.stdout, "[2] add $Frame.Gap")

	jr = is.run(t, "approve", p.ID, "--by", "reviewer")
	require.Equal(t, 0, jr.code, jr.stdout)
	var ad approveData
	jr.decode(t, &ad)
	assert.Equal(t, []int{1, 2}, ad.Proposal.Decision.Applied)
	assert.Equal(t, []int{3}, ad.Skipped)
	assert.Equal(t, edited, readFile(t, is.dir, "demo.spec.md"), "the follow-on prose edit is not applied")
}

func TestApproveDefaultPrintsSkippedHunks(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok", Findings: []modelFinding{proseFix}}.json(t)))
	write(t, is.dir, "body.md", timingNew)
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "bound it")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	h := is.runHuman(t, "", "approve", p.ID)
	require.Equal(t, 0, h.code, h.stderr)
	assert.Contains(t, h.stdout, "skipped follow-on hunks 2")
	assert.NotContains(t, readFile(t, is.dir, "demo.spec.md"), "5 s bound for the reader")

	h = is.runHuman(t, "", "approve", p.ID, "--all", "--hunks", "1")
	assert.Equal(t, exitFail, h.code)
}

func TestProposeBatchWaitAndWithdraw(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	write(t, is.dir, "demo.spec.md", batchEdit())
	jr := is.run(t, "propose", "Frame.Timing", "Frame.Gap", "--why", "bound and gap", "--by", "d1")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	id := filedOf(t, jr).Proposals[0].ID

	// Still pending: one waiter, one id, exit 2 on timeout.
	jr = is.run(t, "proposal", id, "--wait", "50ms")
	assert.Equal(t, exitPending, jr.code, jr.stdout)

	// Withdrawn by its author: the waiter reports the batch's one outcome.
	jr = is.run(t, "reject", id, "--why", "withdrawn: redo the section", "--by", "d1")
	require.Equal(t, 0, jr.code, jr.stdout)
	jr = is.run(t, "proposal", id, "--wait", "1s")
	assert.Equal(t, exitRejected, jr.code, jr.stdout)
	assert.Contains(t, jr.errMessage, "withdrawn: redo the section")
	assert.Equal(t, baseSpec, readFile(t, is.dir, "demo.spec.md"))

	// Refiling works and makes a new batch.
	write(t, is.dir, "demo.spec.md", batchEdit())
	jr = is.run(t, "propose", "Frame.Timing", "Frame.Gap", "--why", "bound and gap, again", "--by", "d1")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	again := filedOf(t, jr).Proposals[0]
	assert.NotEqual(t, id, again.ID)
	assert.Equal(t, proposal.StatusPending, again.Status)
}

func TestFiledWithoutWaitDoesNotSayWaiting(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	write(t, is.dir, "body.md", timingNew)
	h := is.runHuman(t, "", "change", "Frame.Timing", "--body-file", "body.md", "--why", "bound it")
	assert.Equal(t, exitPending, h.code)
	assert.NotContains(t, h.stderr, "waiting for review")
	assert.Contains(t, h.stderr, "not waiting")
}

func TestChangeAddWithoutNameSaysSo(t *testing.T) {
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	h := is.runHuman(t, "", "change", "--add", "--section", "2", "--body-file", "body.md", "--why", "w")
	assert.NotEqual(t, exitOK, h.code)
	assert.Contains(t, h.stderr, "--add needs the new anchor's NAME")
	assert.NotContains(t, h.stderr, "shell expand")
}

func TestAmbiguousSectionListsQualifiedCandidates(t *testing.T) {
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	write(t, is.dir, "other.spec.md", "# other\n\n## 2. More rules\n\nText.\n")
	write(t, is.dir, "body.md", "**$Frame.Gap (frame gap).** Two fragments are at most 2 s apart.\nChecked by: TestGap.")
	h := is.runHuman(t, "", "change", "Frame.Gap", "--add", "--section", "2", "--body-file", "body.md", "--why", "w")
	assert.Equal(t, exitFail, h.code)
	assert.Contains(t, h.stderr, "qualify it as FILE:2")
	assert.Contains(t, h.stderr, "demo:2")
	assert.Contains(t, h.stderr, "other:2")
}
