// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/proposal"
)

// The per-edit cascade on the command line: a gate that clears files
// nothing for --keep (exit 0, data.cleared_by) and files a finding-free
// proposal for an agent; stage lines in the human output.

// fakeJev answers the Decisions API with fixed scores.
func fakeJev(t *testing.T, material, class float64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		answers := map[string]any{}
		for q, v := range map[string]float64{"material_change": material, "numeric_mismatch": class, "contradiction": 0.05, "stale_reference": 0.05} {
			answers[q] = map[string]any{"type": "noul", "noul": v}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "typesafe/jev-1.14", "answers": answers,
			"usage": map[string]any{"input_tokens": 900, "output_tokens": 70, "cost": 0.00007}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// cascadeSetup is setupInfer with a jev gate in front of the check.
func cascadeSetup(t *testing.T, material, class float64) (*inferSetup, *httptest.Server) {
	t.Helper()
	is := setupInfer(t, okReply(modelReply{Summary: "the prose", Findings: []modelFinding{proseFix}}.json(t)))
	jev := fakeJev(t, material, class)
	writeUserConfig(t, is.env, "default = \"fake\"\n[providers.fake]\nbase_url = \""+is.srv.URL+"\"\nmodel = \"check-model\"\n"+
		"[providers.jev]\nkind = \"decisions\"\nbase_url = \""+jev.URL+"\"\nmodel = \"typesafe/jev-1.14\"\n[gate]\nprovider = \"jev\"\n")
	write(t, is.dir, "demo.spec.md", strings.Replace(baseSpec, timingOld, timingNew, 1))
	return is, jev
}

func TestProposeKeepClearedByGate(t *testing.T) {
	requireGit(t)
	is, _ := cascadeSetup(t, 0.9, 0.1)
	jr := is.run(t, "propose", "--keep", "--source", "human", "--why", "mine")
	require.Equal(t, 0, jr.code, jr.stdout)
	d := filedOf(t, jr)
	assert.Empty(t, d.Proposals, "nothing filed")
	assert.Equal(t, "gate", d.ClearedBy)
	require.Len(t, d.Cleared, 1)
	assert.Equal(t, "$Frame.Timing", d.Cleared[0].Anchor)
	assert.Equal(t, "", d.Cleared[0].ID)
	assert.Contains(t, d.Cleared[0].Reason, "No logically significant change found (")
	require.Len(t, d.Cleared[0].Stages, 1)
	assert.Equal(t, proposal.OutcomeClear, d.Cleared[0].Stages[0].Outcome)
	assert.Empty(t, proposalFiles(t, is.dir))
	bodies, _ := is.srv.requests()
	assert.Empty(t, bodies, "the check did not run")
	assert.Equal(t, []string{"spec", "status"}, d.Next.Argv)

	h := is.runHuman(t, "", "propose", "--keep", "--source", "human", "--why", "mine")
	require.Equal(t, 0, h.code, h.stderr)
	assert.Regexp(t, `(?m)^\$Frame\.Timing: No logically significant change found \([0-9.]+ s\); nothing filed$`, h.stdout)
	assert.Contains(t, h.stdout, "  gate jev/jev-1.14 ")
	assert.Contains(t, h.stdout, ": clear (max class 0.10), $0.00007")
	assert.Contains(t, h.stdout, "next: nothing to check; commit the change")
}

func TestProposeAgentClearedStillFiled(t *testing.T) {
	requireGit(t)
	is, _ := cascadeSetup(t, 0.9, 0.1)
	jr := is.run(t, "propose", "Frame.Timing", "--why", "agent edit")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	d := filedOf(t, jr)
	require.Len(t, d.Proposals, 1)
	p := d.Proposals[0]
	assert.NotEmpty(t, p.ID)
	assert.Empty(t, p.Hunks)
	assert.Equal(t, "gate", p.Inference.ClearedBy)
	assert.Contains(t, p.Summary, "No logically significant change found (")
	assert.Equal(t, "gate", d.ClearedBy)
	require.Len(t, d.Cleared, 1)
	assert.Equal(t, p.ID, d.Cleared[0].ID)
	assert.Equal(t, baseSpec, readFile(t, is.dir, "demo.spec.md"), "the agent's edit went into the proposal")
	assert.Len(t, proposalFiles(t, is.dir), 1)
}

// An edit the gate escalates and triage clears: the human output leads
// with "No conflict found", the stage lines below it.
func TestProposeKeepClearedByTriage(t *testing.T) {
	requireGit(t)
	is, _ := cascadeSetup(t, 0.99, 0.8)
	triage := fakeProvider(t, okReply("```json\n{\"material_change\": 1, \"numeric_mismatch\": 0.1, \"contradiction\": 0.05, \"stale_reference\": 0}\n```"))
	jev := fakeJev(t, 0.99, 0.8)
	writeUserConfig(t, is.env, "default = \"fake\"\n[providers.fake]\nbase_url = \""+is.srv.URL+"\"\nmodel = \"check-model\"\n"+
		"[providers.tri]\nbase_url = \""+triage.URL+"\"\nmodel = \"glm\"\n"+
		"[providers.jev]\nkind = \"decisions\"\nbase_url = \""+jev.URL+"\"\nmodel = \"typesafe/jev-1.14\"\n[gate]\nprovider = \"jev\"\n[triage]\nprovider = \"tri\"\n")
	h := is.runHuman(t, "", "propose", "--keep", "--source", "human", "--why", "mine")
	require.Equal(t, 0, h.code, h.stdout+h.stderr)
	assert.Regexp(t, `(?m)^\$Frame\.Timing: No conflict found \([0-9.]+ s\); nothing filed$`, h.stdout)
	assert.Contains(t, h.stdout, "  gate jev/jev-1.14 ")
	assert.Contains(t, h.stdout, ": escalate (numeric 0.80)")
	assert.Contains(t, h.stdout, "  triage tri/glm ")
	bodies, _ := is.srv.requests()
	assert.Empty(t, bodies, "the check did not run")
	assert.Empty(t, proposalFiles(t, is.dir))
}

func TestProposeEscalatedRunsCheck(t *testing.T) {
	requireGit(t)
	is, _ := cascadeSetup(t, 0.99, 0.8)
	h := is.runHuman(t, "", "propose", "--keep", "--source", "human", "--why", "mine")
	require.Equal(t, exitPending, h.code, h.stdout+h.stderr)
	assert.Contains(t, h.stdout, "  gate jev/jev-1.14 ")
	assert.Contains(t, h.stdout, ": escalate (numeric 0.80)")
	assert.Contains(t, h.stdout, "  check fake/check-model ")
	assert.Contains(t, h.stdout, ": 1 finding")
	ps := proposalFiles(t, is.dir)
	require.Len(t, ps, 1)
}

func TestDoctorGateAndTriage(t *testing.T) {
	is := setupInfer(t, okReply("OK"))
	jev := fakeJev(t, 0.1, 0.1)
	writeUserConfig(t, is.env, "default = \"fake\"\n[providers.fake]\nbase_url = \""+is.srv.URL+"\"\nmodel = \"check-model\"\ntriage_model = \"cheap\"\n"+
		"[providers.jev]\nkind = \"decisions\"\nbase_url = \""+jev.URL+"\"\nmodel = \"typesafe/jev-1.14\"\napi_key_env = \"SPEC_JEV_KEY\"\n[gate]\nprovider = \"jev\"\n[triage]\nprovider = \"fake\"\n")
	jr := is.run(t, "doctor")
	_, c := doctorChecks(t, jr)
	assert.Equal(t, "warn", c["gate"].Status, c["gate"].Detail)
	assert.Contains(t, c["gate"].Detail, "SPEC_JEV_KEY")
	assert.Contains(t, c["gate"].Detail, "fails open")
	assert.Equal(t, "ok", c["triage"].Status, c["triage"].Detail)
	assert.Contains(t, c["triage"].Detail, "provider fake, model cheap: answered in")

	is.env["SPEC_JEV_KEY"] = "k"
	jr = is.run(t, "doctor")
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "ok", c["gate"].Status, c["gate"].Detail)
	assert.Contains(t, c["gate"].Detail, "provider jev, model typesafe/jev-1.14: answered in")
	assert.Contains(t, c["gate"].Detail, "escalate_class_at 0.45")
}

func TestDoctorGateWithoutReportedCost(t *testing.T) {
	is := setupInfer(t, okReply("OK"))
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]any{"material_change": map[string]any{"type": "noul", "noul": 0.01}},
			"usage":   map[string]any{"total_input_tokens": 90, "total_output_tokens": 0},
		})
	}))
	t.Cleanup(gate.Close)
	writeUserConfig(t, is.env, "[providers.gemini]\nkind = 'decisions'\napi = 'google'\nmodel = 'gemini-decision-1'\nbase_url = '"+gate.URL+"'\n[gate]\nprovider = 'gemini'\n")
	_, checks := doctorChecks(t, is.run(t, "doctor"))
	assert.Equal(t, "ok", checks["gate"].Status)
	assert.Contains(t, checks["gate"].Detail, "cost not reported")
	assert.NotContains(t, checks["gate"].Detail, "$0")
}
