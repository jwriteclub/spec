// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	orderFind = "Fragments MUST arrive in order"
	orderEdit = "Fragments MUST always arrive in order"
)

// applyTiming files and approves the change of $Frame.Timing without
// committing it, and returns the proposal's id and the file as it now is.
func applyTiming(t *testing.T, is *inferSetup) (string, string) {
	t.Helper()
	write(t, is.dir, "demo.spec.md", strings.Replace(baseSpec, timingOld, timingNew, 1))
	jr := is.run(t, "propose", "Frame.Timing", "--why", "bound it", "--by", "agent-7")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	id := filedOf(t, jr).Proposals[0].ID
	jr = is.run(t, "approve", id)
	require.Equal(t, 0, jr.code, jr.stdout)
	return id, readFile(t, is.dir, "demo.spec.md")
}

func primaries(t *testing.T, jr jsonResult) []string {
	t.Helper()
	names := []string{}
	for _, p := range filedOf(t, jr).Proposals {
		names = append(names, p.Primary.Name)
	}
	return names
}

// The reported bug: a change from an applied proposal is not reverted and
// re-proposed by an agent's no-name propose, which names only what is left.
func TestProposeWithoutNamesLeavesAppliedChange(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	_, applied := applyTiming(t, is)
	edited := strings.Replace(applied, orderFind, orderEdit, 1)
	write(t, is.dir, "demo.spec.md", edited)

	jr := is.run(t, "propose", "--why", "order it", "--by", "agent-7")
	assert.Equal(t, exitFail, jr.code, jr.stdout)
	assert.Equal(t, "names_required", jr.errCode)
	assert.Contains(t, jr.errMessage, "$Frame.Order")
	assert.NotContains(t, jr.errMessage, "$Frame.Timing", "the applied change is accounted for")
	assert.Equal(t, edited, readFile(t, is.dir, "demo.spec.md"), "a refusal changes nothing")
	assert.Equal(t, []string{"spec", "propose", "Frame.Order", "--why", "order it", "--by", "agent-7"}, filedOf(t, jr).Next.Argv)

	// The command it gives files that anchor alone and keeps the applied text.
	jr = is.run(t, "propose", "Frame.Order", "--why", "order it", "--by", "agent-7")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	assert.Equal(t, []string{"$Frame.Order"}, primaries(t, jr))
	assert.Equal(t, applied, readFile(t, is.dir, "demo.spec.md"))
}

// A human's uncommitted edit never moves into an agent's proposal: the
// tree cannot say whose edit is whose, so the agent must name its own.
func TestProposeWithoutNamesLeavesHumanEdit(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))

	// Alone in the tree, the human's edit is as safe as beside the agent's.
	humanOnly := strings.Replace(baseSpec, orderFind, orderEdit, 1)
	write(t, is.dir, "demo.spec.md", humanOnly)
	jr := is.run(t, "propose", "--why", "w", "--by", "agent-7")
	assert.Equal(t, "names_required", jr.errCode, jr.stdout)
	assert.Empty(t, proposalFiles(t, is.dir))
	assert.Equal(t, humanOnly, readFile(t, is.dir, "demo.spec.md"))

	both := strings.Replace(humanOnly, timingOld, timingNew, 1)
	write(t, is.dir, "demo.spec.md", both)
	jr = is.run(t, "propose", "--why", "w", "--by", "agent-7")
	assert.Equal(t, "names_required", jr.errCode, jr.stdout)
	assert.Contains(t, jr.errMessage, "$Frame.Timing")
	assert.Contains(t, jr.errMessage, "$Frame.Order")
	assert.Empty(t, proposalFiles(t, is.dir))
	assert.Equal(t, both, readFile(t, is.dir, "demo.spec.md"))

	jr = is.run(t, "propose", "Frame.Timing", "--why", "w", "--by", "agent-7")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	assert.Equal(t, []string{"$Frame.Timing"}, primaries(t, jr))
	assert.Equal(t, humanOnly, readFile(t, is.dir, "demo.spec.md"), "the human's edit stays in the tree")
}

// `spec proposal ID --wait` acknowledges the receipt, the documented path
// of a waiting agent. The applied text is still accounted for after it.
func TestProposeAccountsForAppliedAfterAck(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	id, applied := applyTiming(t, is)
	jr := is.run(t, "proposal", id, "--wait", "1s")
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Empty(t, proposalFiles(t, is.dir), "acknowledged: no receipt is left")
	assert.Equal(t, "not_found", is.run(t, "proposal", id).errCode)
	assert.FileExists(t, filepath.Join(is.dir, ".spec", "proposals", "acked", id+".json"))

	jr = is.run(t, "propose", "--why", "w", "--by", "agent-7")
	assert.Equal(t, 0, jr.code, jr.stdout)
	assert.Empty(t, filedOf(t, jr).Proposals)
	assert.Contains(t, strings.Join(filedOf(t, jr).Skipped, "\n"), "$Frame.Timing (applied by proposal "+id)
	assert.Equal(t, applied, readFile(t, is.dir, "demo.spec.md"))

	jr = is.run(t, "propose", "Frame.Timing", "--why", "w", "--by", "agent-7")
	assert.Equal(t, 0, jr.code, jr.stdout)
	assert.Empty(t, filedOf(t, jr).Proposals, "named too: it is never reverted and re-proposed")
	assert.Equal(t, applied, readFile(t, is.dir, "demo.spec.md"))
}

// An explicit --ack does the same; a committed approval leaves nothing.
func TestAcknowledgeKeepsOnlyUncommittedEvidence(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	id, _ := applyTiming(t, is)
	require.Equal(t, 0, is.run(t, "proposal", id, "--ack").code)
	assert.FileExists(t, filepath.Join(is.dir, ".spec", "proposals", "acked", id+".json"))

	git(t, is.dir, "checkout", "--", "demo.spec.md")
	write(t, is.dir, "demo.spec.md", strings.Replace(baseSpec, orderFind, orderEdit, 1))
	jr := is.run(t, "propose", "Frame.Order", "--why", "w", "--by", "agent-7")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	second := filedOf(t, jr).Proposals[0].ID
	jr = is.run(t, "approve", second, "--commit")
	require.Equal(t, 0, jr.code, jr.stdout)
	require.Equal(t, 0, is.run(t, "proposal", second, "--ack").code)
	assert.NoFileExists(t, filepath.Join(is.dir, ".spec", "proposals", "acked", second+".json"))
}

// An applied change with a further edit on top cannot be proposed against
// HEAD: restoring HEAD would revert the applied change. --keep reverts
// nothing, so it may.
func TestProposeRefusesFurtherEditOfApplied(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	id, applied := applyTiming(t, is)
	further := strings.Replace(applied, "at most 5 s", "at most 4 s", 1)
	require.NotEqual(t, applied, further)
	write(t, is.dir, "demo.spec.md", further)

	jr := is.run(t, "propose", "Frame.Timing", "--why", "w", "--by", "agent-7")
	assert.Equal(t, exitFail, jr.code, jr.stdout)
	assert.Equal(t, "applied_not_committed", jr.errCode)
	assert.Contains(t, jr.errMessage, "commit the applied change first")
	assert.Contains(t, jr.errMessage, id)
	assert.Equal(t, further, readFile(t, is.dir, "demo.spec.md"))

	jr = is.run(t, "propose", "Frame.Timing", "--keep", "--source", "human", "--why", "w")
	assert.Equal(t, exitPending, jr.code, jr.stdout)
	assert.Equal(t, further, readFile(t, is.dir, "demo.spec.md"))
}

// Once the applied change is committed, its evidence no longer applies: the
// same anchor can change again, even while the receipt is still on disk.
func TestProposeAfterAppliedChangeIsCommitted(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	_, applied := applyTiming(t, is)
	git(t, is.dir, "add", "demo.spec.md")
	_, sd := statusOf(t, is.dir)
	git(t, is.dir, "commit", "-q", "-m", "bound it\n\n"+strings.Join(sd.Trailers, "\n"))

	write(t, is.dir, "demo.spec.md", strings.Replace(applied, "at most 5 s", "at most 4 s", 1))
	jr := is.run(t, "propose", "Frame.Timing", "--why", "w", "--by", "agent-7")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	assert.Equal(t, applied, readFile(t, is.dir, "demo.spec.md"), "back to the committed text")
}

// Follow-on hunks of an applied proposal can change other anchors; those
// changes are accounted for too.
func TestProposeAccountsForAppliedFollowOnHunk(t *testing.T) {
	requireGit(t)
	fix := modelFinding{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "order", Severity: "breaking", Explanation: "x",
		Find: orderFind, Replace: orderEdit}
	is := setupInfer(t, okReply(modelReply{Summary: "ok", Findings: []modelFinding{fix}}.json(t)))
	_, applied := applyTiming(t, is)
	require.Contains(t, applied, orderEdit)

	jr := is.run(t, "propose", "--why", "w", "--by", "agent-7")
	assert.Equal(t, 0, jr.code, jr.stdout)
	assert.Empty(t, filedOf(t, jr).Proposals)
	assert.Equal(t, applied, readFile(t, is.dir, "demo.spec.md"))
	skipped := strings.Join(filedOf(t, jr).Skipped, "\n")
	assert.Contains(t, skipped, "$Frame.Timing")
	assert.Contains(t, skipped, "$Frame.Order")
}

// Applied additions and removals are accounted for like changes.
func TestProposeAccountsForAppliedAddAndRemove(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	added := "**$Frame.Gap (frame gap).** Two fragments are at most 2 s apart.\nChecked by: TestGap."
	edited := strings.Replace(baseSpec, "\n\nProse that mentions", "\n\n"+added+"\n\nProse that mentions", 1)
	edited = strings.Replace(edited, orderOld+"\n\n", "", 1)
	edited = strings.Replace(edited, "Prose that mentions $Frame.Order for the reader.\n", "Prose for the reader.\n", 1)
	write(t, is.dir, "demo.spec.md", edited)
	jr := is.run(t, "propose", "Frame.Gap", "Frame.Order", "--source", "human", "--why", "w")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	for _, p := range filedOf(t, jr).Proposals {
		require.Equal(t, 0, is.run(t, "approve", p.ID).code)
	}
	applied := readFile(t, is.dir, "demo.spec.md")
	require.Equal(t, edited, applied)

	jr = is.run(t, "propose", "--why", "w", "--by", "agent-7")
	assert.Equal(t, 0, jr.code, jr.stdout)
	assert.Empty(t, filedOf(t, jr).Proposals)
	assert.Equal(t, applied, readFile(t, is.dir, "demo.spec.md"))
}

// `spec fmt` after an approval rewraps the anchor without changing its hash:
// still the applied change.
func TestProposeAppliedSurvivesReflow(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	_, applied := applyTiming(t, is)
	reflowed := strings.Replace(applied, "Every fragment MUST", "Every fragment\nMUST", 1)
	require.NotEqual(t, applied, reflowed)
	write(t, is.dir, "demo.spec.md", reflowed)

	jr := is.run(t, "propose", "Frame.Timing", "--why", "w", "--by", "agent-7")
	assert.Equal(t, 0, jr.code, jr.stdout)
	assert.Empty(t, filedOf(t, jr).Proposals)
	assert.Equal(t, reflowed, readFile(t, is.dir, "demo.spec.md"))
}

// A human's sweep still covers what is changed, minus applied changes; so
// does --keep, which restores nothing.
func TestProposeHumanSweepSkipsApplied(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	_, applied := applyTiming(t, is)
	write(t, is.dir, "demo.spec.md", strings.Replace(applied, orderFind, orderEdit, 1))

	jr := is.run(t, "propose", "--keep", "--why", "w", "--by", "agent-7")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	assert.Equal(t, []string{"$Frame.Order"}, primaries(t, jr))

	jr = is.run(t, "propose", "--source", "human", "--why", "w")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	assert.Equal(t, []string{"$Frame.Order"}, primaries(t, jr))
	assert.Equal(t, applied, readFile(t, is.dir, "demo.spec.md"))
}

// spec status names the anchors in its next line, and tells a tree holding
// only applied changes to commit them.
func TestStatusNextNamesAnchors(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	_, applied := applyTiming(t, is)

	_, sd := statusOf(t, is.dir)
	assert.Equal(t, "anchors_changed", sd.State)
	assert.NotContains(t, sd.Next.Text, "agents: run `spec propose")
	assert.Contains(t, sd.Next.Text, "commit")
	assert.Empty(t, sd.Next.Argv)

	write(t, is.dir, "demo.spec.md", strings.Replace(applied, orderFind, orderEdit, 1))
	_, sd = statusOf(t, is.dir)
	assert.Contains(t, sd.Next.Text, "agents: run `spec propose Frame.Order --why WHY`")
	assert.Equal(t, []string{"spec", "propose", "Frame.Order", "--why", "WHY"}, sd.Next.Argv)
}
