// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/proposal"
)

func analyzeOf(t *testing.T, jr jsonResult) analyzeData {
	t.Helper()
	var d analyzeData
	jr.decode(t, &d)
	return d
}

func TestAnalyzeChanged(t *testing.T) {
	requireGit(t)
	fix := modelFinding{
		File: "other.spec.md", Anchor: "Extra.Limit", Issue: "numeric_mismatch", Severity: "inconsistent",
		Explanation: "16 here, 8 there", Find: "The extra queue MUST hold at most 8 items.", Replace: "The extra queue MUST hold at most 16 items.",
	}
	note := modelFinding{File: "demo.spec.md", Issue: "ambiguity", Severity: "cosmetic", Explanation: "unclear"}
	is := setupInfer(t, okReply(modelReply{Summary: "one mismatch", Findings: []modelFinding{fix, note}, Questions: []string{"which?"}}.json(t)))

	// Nothing differs: nothing sent.
	jr := is.run(t, "analyze")
	require.Equal(t, 0, jr.code, jr.stdout)
	d := analyzeOf(t, jr)
	assert.Equal(t, "changed", d.Mode)
	assert.Nil(t, d.Proposal)
	assert.Equal(t, []string{"spec", "analyze", "--all"}, d.Next.Argv)
	bodies, _ := is.srv.requests()
	assert.Empty(t, bodies)

	replaceIn(t, is.dir, "demo.spec.md", "Prose that mentions $Frame.Order for the reader.", "The extra queue holds 16 items; see $Frame.Order.")
	jr = is.run(t, "analyze")
	require.Equal(t, 0, jr.code, jr.stdout)
	d = analyzeOf(t, jr)
	assert.Equal(t, []string{"demo.spec.md"}, d.Files)
	assert.Equal(t, 1, d.Regions)
	require.True(t, d.Saved)
	p := d.Proposal
	assert.Equal(t, proposal.KindAnalysis, p.Kind)
	assert.Nil(t, p.Primary)
	require.Len(t, p.Hunks, 1)
	assert.Equal(t, 1, p.Hunks[0].N)
	assert.Equal(t, "$Extra.Limit", p.Hunks[0].Anchor)
	require.Len(t, p.Notes, 1)
	assert.Equal(t, []string{"which?"}, p.Questions)
	assert.Equal(t, []string{"spec", "review", p.ID}, d.Next.Argv)

	msg := userMessage(t, is.srv, 0)
	assert.Contains(t, msg, "## Edited regions")
	assert.Contains(t, msg, "<now>\nThe extra queue holds 16 items; see $Frame.Order.\n</now>")
	assert.Contains(t, msg, "<before>\nProse that mentions $Frame.Order for the reader.\n</before>")
	bodies, _ = is.srv.requests()
	assert.Contains(t, bodies[0]["messages"].([]any)[0].(map[string]any)["content"], "QUANTITIES")

	jr = is.run(t, "approve", p.ID, "--hunks", "1")
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Contains(t, readFile(t, is.dir, "other.spec.md"), "at most 16 items")

	// --all and FILE... modes.
	jr = is.run(t, "analyze", "--all")
	require.Equal(t, 0, jr.code, jr.stdout)
	d = analyzeOf(t, jr)
	assert.Equal(t, "all", d.Mode)
	assert.Equal(t, []string{"demo.spec.md", "other.spec.md"}, d.Files)
	require.True(t, d.Saved, "filed for its question")
	assert.Empty(t, d.Proposal.Hunks, "the fix no longer applies: its text changed")
	assert.Equal(t, []string{"spec", "proposal", d.Proposal.ID}, d.Next.Argv)
	assert.Empty(t, d.Superseded, "the changed analysis was approved")
	allID := d.Proposal.ID
	assert.Contains(t, userMessage(t, is.srv, 1), "Check the whole specification")

	jr = is.run(t, "analyze", "other.spec.md", "--full")
	require.Equal(t, 0, jr.code, jr.stdout)
	d = analyzeOf(t, jr)
	assert.Equal(t, []string{"other.spec.md"}, d.Files)
	assert.Empty(t, d.Superseded, "one file does not cover all of them")
	jr = is.run(t, "analyze", "notes/README.md")
	assert.Equal(t, "not_found", jr.errCode)
	jr = is.run(t, "analyze", "--all", "--changed")
	assert.Equal(t, "usage", jr.errCode)

	// Human output; a second --all supersedes the first.
	h := is.runHuman(t, "", "analyze", "--all")
	assert.Equal(t, 0, h.code)
	_, line, _ := strings.Cut(h.stdout, "superseded earlier analyses of the same scope: ")
	line, _, _ = strings.Cut(line, "\n")
	assert.Contains(t, line, allID, "and the files analysis, which --all covers")
	assert.Contains(t, h.stdout, "analyzed 2 files with fake/check-model")
	assert.Contains(t, h.stdout, "note: cosmetic ambiguity in demo.spec.md (no edit proposed)")
	assert.Contains(t, h.stdout, "question: which?")
}

func TestAnalyzeProviderFailures(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	jr := is.run(t, "analyze", "--all")
	assert.Equal(t, 1, jr.code)
	assert.Equal(t, "provider_failed", jr.errCode)
	bodies, _ := is.srv.requests()
	assert.Len(t, bodies, 3, "retried")

	// No user config at all.
	is.env = isolatedEnv(t)
	jr = is.run(t, "analyze", "--all")
	assert.Equal(t, "provider_unavailable", jr.errCode)
	assert.Empty(t, proposalFiles(t, is.dir))
}

func TestPromptOverrideAndBudget(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	cfgDir := filepath.Join(is.env["XDG_CONFIG_HOME"], "spec")
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "analyze.md"), []byte("Custom analysis prompt.\n"), 0o644))
	srvURL := is.srv.URL
	writeUserConfig(t, is.env, "default = \"fake\"\nmax_context_tokens = 50\nmax_output_tokens = 1234\n\n[prompts]\nanalyze = \"analyze.md\"\n\n[providers.fake]\nbase_url = \""+srvURL+"\"\nmodel = \"m\"\n")
	jr := is.run(t, "analyze", "--all")
	require.Equal(t, 0, jr.code, jr.stdout)
	bodies, _ := is.srv.requests()
	require.Len(t, bodies, 1)
	assert.Equal(t, "Custom analysis prompt.\n", bodies[0]["messages"].([]any)[0].(map[string]any)["content"])
	assert.Equal(t, float64(1234), bodies[0]["max_tokens"])
	assert.Contains(t, userMessage(t, is.srv, 0), "## Left out to fit the context budget")

	writeUserConfig(t, is.env, "default = \"fake\"\n[prompts]\nanalyze = \"missing.md\"\n[providers.fake]\nbase_url = \""+srvURL+"\"\nmodel = \"m\"\n")
	jr = is.run(t, "analyze", "--all")
	assert.Equal(t, "provider_unavailable", jr.errCode)
	assert.Contains(t, jr.errMessage, "missing.md")
}

func TestReviewInteractive(t *testing.T) {
	requireGit(t)
	second := modelFinding{File: "other.spec.md", Issue: "missing_update", Severity: "cosmetic", Explanation: "x",
		Find: "Checked by: TestLimit.", Replace: "Checked by: TestLimitBound."}
	var calls atomic.Int32
	check := modelReply{Summary: "ok", Findings: []modelFinding{proseFix, second}}.json(t)
	is := setupInfer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 2 {
			okReply(check)(w, r)
			return
		}
		// The revision of hunk 2.
		b, _ := json.Marshal(map[string]string{
			"find": proseFix.Find, "replace": "Prose that mentions $Frame.Order and its bound for the reader.", "explanation": "revised",
		})
		okReply(string(b))(w, r)
	})
	write(t, is.dir, "body.md", timingNew)
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w", "--by", "agent")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	id := filedOf(t, jr).Proposals[0].ID

	// --json is refused.
	jr = is.run(t, "review", id)
	assert.Equal(t, "usage", jr.errCode)

	// q leaves it pending.
	h := is.runHuman(t, "y\nq\n", "review", id)
	assert.Equal(t, 0, h.code)
	assert.Contains(t, h.stdout, "is still pending")

	// y the change, e (feedback) then y hunk 2, n hunk 3, with --commit.
	h = is.runHuman(t, "y\ne\nsay bound, not 5 s\ny\nn\n", "review", id, "--commit")
	require.Equal(t, 0, h.code, h.stdout+h.stderr)
	assert.Contains(t, h.stdout, "apply hunk 2? [y/n/e/q]")
	assert.Contains(t, h.stdout, "+ Prose that mentions $Frame.Order and its bound for the reader.")
	assert.Contains(t, h.stdout, "committed ")
	got := readFile(t, is.dir, "demo.spec.md")
	assert.Contains(t, got, timingNew)
	assert.Contains(t, got, "Prose that mentions $Frame.Order and its bound for the reader.")
	assert.Equal(t, otherSpec, readFile(t, is.dir, "other.spec.md"))
	assert.Contains(t, userMessage(t, is.srv, 1), "feedback: say bound, not 5 s")
	assert.Contains(t, git(t, is.dir, "log", "-1", "--format=%B"), "Spec-Change: $Frame.Timing")

	// Declining the change itself rejects with the reason given.
	jr = is.run(t, "change", "Frame.Order", "--def", "--why", "w")
	id = filedOf(t, jr).Proposals[0].ID
	h = is.runHuman(t, "n\nnot vocabulary\n", "review", id)
	assert.Equal(t, 0, h.code)
	assert.Contains(t, h.stdout, "rejected by human: not vocabulary")
	assert.True(t, strings.HasPrefix(h.stdout, "proposal "), h.stdout)
}

func TestReadOnlyProposalCommands(t *testing.T) {
	requireGit(t)
	dir := demoProject(t)
	for _, args := range [][]string{{"proposals"}, {"proposal", "abcd"}} {
		run(t, dir, args...)
	}
	_, err := os.Stat(filepath.Join(dir, ".spec"))
	assert.True(t, os.IsNotExist(err), "listing and showing proposals create nothing")
	jr := runJSON(t, dir, "proposal", "abcd")
	assert.Equal(t, "not_found", jr.errCode)
	jr = runJSON(t, dir, "approve", "abcd", "--hunks", "x")
	assert.Equal(t, "usage", jr.errCode)
	jr = runJSON(t, dir, "reject", "abcd")
	assert.Equal(t, "usage", jr.errCode)
}
