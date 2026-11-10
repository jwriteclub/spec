// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const orderOld = "**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, see $Frame.Timing.\nChecked by: TestOrder."

// Human-origin optional checks exercise placement independently of external
// required-check snapshot invalidation.
// Removing two adjacent anchors: propose puts both back where they were,
// byte for byte, and approving both reproduces the edit.
func TestProposeRestoresAdjacentRemovals(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	edited := strings.Replace(baseSpec, timingOld+"\n\n"+orderOld+"\n\n", "", 1)
	edited = strings.Replace(edited, "Prose that mentions $Frame.Order for the reader.", "Prose for the reader.", 1)
	write(t, is.dir, "demo.spec.md", edited)

	jr := is.run(t, "propose", "--source", "human", "--why", "drop both")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	d := filedOf(t, jr)
	require.Len(t, d.Proposals, 2)
	want := strings.Replace(baseSpec, "Prose that mentions $Frame.Order for the reader.", "Prose for the reader.", 1)
	assert.Equal(t, want, readFile(t, is.dir, "demo.spec.md"), "both anchors are back where they were")

	for _, p := range d.Proposals {
		jr = is.run(t, "approve", p.ID)
		require.Equal(t, 0, jr.code, jr.stdout)
	}
	assert.Equal(t, edited, readFile(t, is.dir, "demo.spec.md"))
}

// Adding two adjacent anchors: approving them in either order reproduces
// the edit byte for byte.
func TestProposeAdjacentAddsApproveInAnyOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		requireGit(t)
		is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
		gapA := "**$Frame.Gap (frame gap).** Two fragments are at most 2 s apart.\nChecked by: TestGap."
		gapB := "**$Frame.Jitter (frame jitter).** Arrival jitter is at most 50 ms.\nChecked by: TestJitter."
		edited := strings.Replace(baseSpec, orderOld+"\n", orderOld+"\n\n"+gapA+"\n\n"+gapB+"\n", 1)
		write(t, is.dir, "demo.spec.md", edited)

		jr := is.run(t, "propose", "--source", "human", "--why", "two new rules")
		require.Equal(t, exitPending, jr.code, jr.stdout)
		ps := filedOf(t, jr).Proposals
		require.Len(t, ps, 2)
		assert.Equal(t, baseSpec, readFile(t, is.dir, "demo.spec.md"))
		byName := map[string]string{}
		for _, p := range ps {
			byName[p.Primary.Name] = p.ID
		}
		order := []string{"$Frame.Gap", "$Frame.Jitter"}
		if reverse {
			order = []string{"$Frame.Jitter", "$Frame.Gap"}
		}
		for _, n := range order {
			jr = is.run(t, "approve", byName[n])
			require.Equal(t, 0, jr.code, jr.stdout)
		}
		assert.Equal(t, edited, readFile(t, is.dir, "demo.spec.md"), "reverse=%v", reverse)
	}
}

// When the proposal cannot be filed, propose must not restore the HEAD
// text: the agent's edit would exist nowhere.
func TestProposeKeepsTheEditWhenFilingFails(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	edited := strings.Replace(baseSpec, timingOld, timingNew, 1)
	write(t, is.dir, "demo.spec.md", edited)
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory whatever its mode")
	}
	dir := filepath.Join(is.dir, ".spec", "proposals")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o500)) // readable, so the filing is what fails
	t.Cleanup(func() { assert.NoError(t, os.Chmod(dir, 0o755)) })

	jr := is.run(t, "propose", "Frame.Timing", "--why", "w")
	assert.Equal(t, exitFail, jr.code)
	assert.Equal(t, "write_failed", jr.errCode)
	assert.Equal(t, edited, readFile(t, is.dir, "demo.spec.md"), "the edit stays in the working tree")
}

// Filing a second proposal for an anchor that already has a pending one
// is allowed, but the filer is told which one is waiting.
func TestProposeWarnsOfPendingProposalForTheAnchor(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	write(t, is.dir, "demo.spec.md", strings.Replace(baseSpec, timingOld, timingNew, 1))

	jr := is.run(t, "propose", "--keep", "--source", "human", "--why", "first")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	first := filedOf(t, jr)
	require.Len(t, first.Proposals, 1)
	assert.Equal(t, []string{}, first.Warnings, "nothing pending before: no warning, and never null")

	h := is.runHuman(t, "", "propose", "--keep", "--source", "human", "--why", "second")
	require.Equal(t, exitPending, h.code, h.stdout+h.stderr)
	want := "$Frame.Timing already has pending proposal " + first.Proposals[0].ID
	assert.Contains(t, h.stdout, "warning: "+want)

	jr = is.run(t, "propose", "--keep", "--source", "human", "--why", "third")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	d := filedOf(t, jr)
	require.Len(t, d.Proposals, 1)
	assert.NotEqual(t, first.Proposals[0].ID, d.Proposals[0].ID, "filed anyway")
	require.Len(t, d.Warnings, 1)
	// Two are pending now, usually filed in the same second (timestamps
	// are to the second), so the warning may name either one.
	prefix := "$Frame.Timing already has pending proposal "
	require.True(t, strings.HasPrefix(d.Warnings[0], prefix), d.Warnings[0])
	named := strings.TrimPrefix(d.Warnings[0], prefix)
	assert.NotEqual(t, d.Proposals[0].ID, named, "names an earlier proposal, not the new one")
	assert.Len(t, proposalFiles(t, is.dir), 3, "all three are filed")
}
