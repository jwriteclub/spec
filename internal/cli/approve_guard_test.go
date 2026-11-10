// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/proposal"
)

// A reject that lands while approve is writing waits for the lock that
// approve holds, then is refused: approve writes and records "applied", and
// the rejection is never reported as done.
func TestRejectDuringApproveWaits(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	write(t, is.dir, "body.md", timingNew)
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	id := filedOf(t, jr).Proposals[0].ID

	var out, errb bytes.Buffer
	env := is.fastEnv(&out, &errb, "")
	var rejected <-chan result
	env.beforeCommit = func() { rejected = is.runAsync("reject", id, "--why", "no way") }
	code := Run([]string{"approve", id, "--json"}, env)
	ajr := decodeEnvelope(t, result{code: code, stdout: out.String(), stderr: errb.String()}, []string{"approve"})
	assert.Equal(t, 0, ajr.code, ajr.stdout)
	assert.Equal(t, strings.Replace(baseSpec, timingOld, timingNew, 1), readFile(t, is.dir, "demo.spec.md"), "written")

	require.NotNil(t, rejected)
	rjr := decodeEnvelope(t, <-rejected, []string{"reject"})
	assert.Equal(t, exitFail, rjr.code)
	assert.Equal(t, "not_pending", rjr.errCode)

	jr = is.run(t, "proposal", id)
	var pd proposalData
	jr.decode(t, &pd)
	assert.Equal(t, proposal.StatusApplied, pd.Proposal.Status)
}

// A finding hunk whose replace would break the spec (a second definition
// of a name, or a label edit that leaves references dangling) fails alone;
// the rest of the proposal applies.
func TestHunkIntroducingLintErrorsFailsAlone(t *testing.T) {
	requireGit(t)
	dup := modelFinding{File: "demo.spec.md", Issue: "missing_update", Severity: "cosmetic",
		Find: "Prose that mentions $Frame.Order for the reader.", Replace: "**$Frame.Order (again).** A second definition."}
	relabel := modelFinding{File: "demo.spec.md", Issue: "terminology_drift", Severity: "cosmetic",
		Find: "**$Frame.Order (frame order).** Fragments", Replace: "**$Frame.Sequence (frame order).** Fragments"}
	good := modelFinding{File: "other.spec.md", Issue: "missing_update", Severity: "cosmetic",
		Find: "at most 8 items", Replace: "at most 8 items of any size"}
	var reply atomic.Value
	reply.Store(modelReply{Summary: "ok", Findings: []modelFinding{dup, good}}.json(t))
	is := setupInfer(t, func(w http.ResponseWriter, r *http.Request) { okReply(reply.Load().(string))(w, r) })
	write(t, is.dir, "body.md", timingNew)

	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	require.Len(t, p.Hunks, 2)

	jr = is.run(t, "approve", p.ID, "--commit")
	require.Equal(t, 0, jr.code, jr.stdout)
	var ad approveData
	jr.decode(t, &ad)
	assert.Equal(t, []int{1, 3}, ad.Proposal.Decision.Applied)
	require.Len(t, ad.Proposal.Decision.Failed, 1)
	assert.Equal(t, 2, ad.Proposal.Decision.Failed[0].N)
	assert.Contains(t, ad.Proposal.Decision.Failed[0].Reason, "lint error")
	assert.Equal(t, strings.Replace(baseSpec, timingOld, timingNew, 1), readFile(t, is.dir, "demo.spec.md"))
	assert.Contains(t, readFile(t, is.dir, "other.spec.md"), "of any size")
	assert.Equal(t, 0, is.run(t, "lint").code)

	// A label edit that leaves $Frame.Order dangling in prose fails too;
	// an analysis-style proposal with only that hunk applies nothing.
	reply.Store(modelReply{Summary: "ok", Findings: []modelFinding{relabel}}.json(t))
	write(t, is.dir, "body2.md", timingOld)
	jr = is.run(t, "change", "Frame.Timing", "--body-file", "body2.md", "--why", "back")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p = filedOf(t, jr).Proposals[0]
	require.Len(t, p.Hunks, 1)
	jr = is.run(t, "approve", p.ID)
	require.Equal(t, 0, jr.code, jr.stdout)
	jr.decode(t, &ad)
	assert.Equal(t, []int{1}, ad.Proposal.Decision.Applied)
	require.Len(t, ad.Proposal.Decision.Failed, 1)
	assert.Contains(t, ad.Proposal.Decision.Failed[0].Reason, "lint error")
	assert.Contains(t, readFile(t, is.dir, "demo.spec.md"), "**$Frame.Order (frame order).**")
}

// A revision saved by `spec review` must not undo a decision made
// elsewhere while the reviewer was typing feedback.
func TestReviewReviseKeepsDecisionMadeElsewhere(t *testing.T) {
	requireGit(t)
	var calls atomic.Int32
	var id atomic.Value
	var is *inferSetup
	check := modelReply{Summary: "ok", Findings: []modelFinding{proseFix}}.json(t)
	is = setupInfer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 2 {
			okReply(check)(w, r)
			return
		}
		// While the model revises, someone rejects the proposal.
		jr := is.run(t, "reject", id.Load().(string), "--why", "decided elsewhere")
		require.Equal(t, 0, jr.code, jr.stdout)
		b, _ := json.Marshal(map[string]string{"find": proseFix.Find, "replace": "Prose that mentions $Frame.Order, bounded.", "explanation": "revised"})
		okReply(string(b))(w, r)
	})
	write(t, is.dir, "body.md", timingNew)
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	id.Store(filedOf(t, jr).Proposals[0].ID)

	h := is.runHuman(t, "y\ne\nbound it\ny\n", "review", id.Load().(string))
	assert.Equal(t, exitFail, h.code, h.stdout+h.stderr)
	assert.Equal(t, baseSpec, readFile(t, is.dir, "demo.spec.md"), "nothing applied")
	jr = is.run(t, "proposal", id.Load().(string))
	var pd proposalData
	jr.decode(t, &pd)
	assert.Equal(t, proposal.StatusRejected, pd.Proposal.Status)
	assert.Equal(t, "decided elsewhere", pd.Proposal.Decision.Reason)
}

func TestParseHunks(t *testing.T) {
	got, o := parseHunks("1, 3-4,3")
	require.Nil(t, o)
	assert.Equal(t, []int{1, 3, 4}, got)
	for _, bad := range []string{"0", "3-1", "x", "1-", "1-1000000000", "999999999"} {
		_, o := parseHunks(bad)
		assert.NotNil(t, o, bad)
	}
}

// A revision from `spec review` obeys the rule the first check did: it may
// not edit the anchor under review (here the reviewer's own kept edit).
func TestReviewRevisionMayNotEditTheChange(t *testing.T) {
	requireGit(t)
	var calls atomic.Int32
	check := modelReply{Summary: "ok", Findings: []modelFinding{proseFix}}.json(t)
	is := setupInfer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 2 {
			okReply(check)(w, r)
			return
		}
		b, _ := json.Marshal(map[string]string{"find": "duration of at most 5 s", "replace": "duration of at most 9 s", "explanation": "revised"})
		okReply(string(b))(w, r)
	})
	edited := strings.Replace(baseSpec, timingOld, timingNew, 1)
	write(t, is.dir, "demo.spec.md", edited)
	jr := is.run(t, "propose", "--keep", "--source", "human", "--why", "mine")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	id := filedOf(t, jr).Proposals[0].ID

	h := is.runHuman(t, "y\ne\nchange the bound\ny\n", "review", id)
	require.Equal(t, 0, h.code, h.stdout+h.stderr)
	assert.Contains(t, h.stdout, "could not revise")
	got := readFile(t, is.dir, "demo.spec.md")
	assert.Contains(t, got, "at most 5 s", "the kept edit is untouched")
	assert.Contains(t, got, proseFix.Replace, "the original hunk applied")
}
