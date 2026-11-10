// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/proposal"
)

// The per-edit cascade (cascade.go) against fake providers: a Jev gate, a
// triage provider and a check provider, each its own server.

const cascadeSpec = "# demo\n\n## 1. Rules\n\n" +
	"**$Net.Timeout (timeout).** A request times out after 30 s.\n\n" +
	"**$Net.Retry (retry).** A request that hits $Net.Timeout is retried twice, 60 s apart.\n\n" +
	"Clients see $Net.Timeout as an error after 30 s.\n"

const (
	timeoutOld = "**$Net.Timeout (timeout).** A request times out after 30 s."
	timeoutNew = "**$Net.Timeout (timeout).** A request times out after 45 s."
)

// fakeServer records request bodies and answers each with reply.
type fakeServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	reply  func(w http.ResponseWriter, body map[string]any)
}

func newFakeServer(t *testing.T, reply func(w http.ResponseWriter, body map[string]any)) *fakeServer {
	t.Helper()
	f := &fakeServer{reply: reply}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		reply := f.reply
		f.mu.Unlock()
		reply(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

// requests returns the bodies seen so far.
func (f *fakeServer) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.bodies...)
}

// chatReply answers like an OpenAI-compatible endpoint.
func chatReply(content string) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, body map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   body["model"],
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110, "cost": 0.0001},
		})
	}
}

// scoresJSON is a triage reply.
func scoresJSON(material, numeric, contradiction, stale float64) string {
	b, _ := json.Marshal(map[string]float64{
		infer.QuestionMaterialChange: material, infer.QuestionNumericMismatch: numeric,
		infer.QuestionContradiction: contradiction, infer.QuestionStaleReference: stale,
	})
	return string(b)
}

// jevReply answers like the Decisions API.
func jevReply(material, numeric, contradiction, stale float64) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, _ map[string]any) {
		answers := map[string]any{}
		for q, v := range map[string]float64{
			infer.QuestionMaterialChange: material, infer.QuestionNumericMismatch: numeric,
			infer.QuestionContradiction: contradiction, infer.QuestionStaleReference: stale,
		} {
			answers[q] = map[string]any{"type": "noul", "noul": v}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "typesafe/jev-1.14-20261014", "provider": "TypeSafe", "answers": answers,
			"usage": map[string]any{"input_tokens": 1500, "output_tokens": 70, "cost": 0.00007},
		})
	}
}

// checkFinding is the check's reply: one prose fix.
var checkFinding = `{"summary": "the prose still says 30 s", "findings": [{"file": "demo.spec.md", "anchor": "", "issue": "numeric_mismatch", "severity": "inconsistent", "explanation": "prose says 30 s", "find": "Clients see $Net.Timeout as an error after 30 s.", "replace": "Clients see $Net.Timeout as an error after 45 s."}], "questions": []}`

// cascadeEnv is a workflow whose user config names the fakes.
type cascadeEnv struct {
	jev, triage, check *fakeServer
	wf                 *Workflow
	cfgPath            string
}

// newCascadeEnv writes a user config: default "check", a "cf" provider
// with a triage model, a jev gate, and extra lines appended.
func newCascadeEnv(t *testing.T, extra string) *cascadeEnv {
	t.Helper()
	e := &cascadeEnv{
		jev:    newFakeServer(t, jevReply(0.99, 0.7, 0.2, 0.1)),
		triage: newFakeServer(t, chatReply(scoresJSON(1, 1, 0, 0))),
		check:  newFakeServer(t, chatReply(checkFinding)),
	}
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"), "JEV_KEY": "k-jev"}
	cfg := "default = \"check\"\n" +
		"[providers.check]\nbase_url = \"" + e.check.URL + "\"\nmodel = \"check-model\"\nstream = false\n" +
		"[providers.cf]\nbase_url = \"" + e.triage.URL + "\"\nmodel = \"cf-model\"\ntriage_model = \"cf-triage\"\nstream = false\n" +
		"[providers.jev]\nkind = \"decisions\"\nbase_url = \"" + e.jev.URL + "\"\nmodel = \"typesafe/jev-1.14\"\napi_key_env = \"JEV_KEY\"\ntimeout = 1\n" +
		extra
	e.cfgPath = filepath.Join(env["XDG_CONFIG_HOME"], "spec", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(e.cfgPath), 0o755))
	require.NoError(t, os.WriteFile(e.cfgPath, []byte(cfg), 0o644))
	e.wf = &Workflow{
		Getenv:     func(k string) string { return env[k] },
		TuneClient: func(c *infer.Client) { c.BaseDelay, c.MaxDelay = time.Millisecond, time.Millisecond },
	}
	return e
}

// run checks the change of $Net.Timeout and returns the proposal and the
// stage events in order.
func (e *cascadeEnv) run(t *testing.T) (*proposal.Proposal, []StageEvent) {
	t.Helper()
	ck, werr := e.wf.OpenChecker(context.Background(), false)
	require.Nil(t, werr)
	var events []StageEvent
	ck.OnStage = func(ev StageEvent) { events = append(events, ev) }
	spec := mdspec.Build([]*mdspec.File{mdspec.ParseFile("demo.spec.md", []byte(cascadeSpec))})
	ch := &specctx.Change{Action: specctx.ActionChange, Name: "Net.Timeout", File: "demo.spec.md",
		OldKind: mdspec.KindInvariant, NewKind: mdspec.KindInvariant, OldText: timeoutOld, NewText: timeoutNew}
	pr := &proposal.Primary{N: 1, Name: "$Net.Timeout", Action: ch.Action, File: ch.File, OldText: timeoutOld, NewText: timeoutNew}
	p, werr := e.wf.CheckChange(ck, spec, ch, pr, nil, proposal.AuthorHuman)
	require.Nil(t, werr)
	return p, events
}

// names are the stages' names and outcomes: "gate:pass".
func names(stages []proposal.Stage) []string {
	var out []string
	for _, st := range stages {
		out = append(out, st.Name+":"+st.Outcome)
	}
	return out
}

const withGateAndTriage = "[gate]\nprovider = \"jev\"\n[triage]\nprovider = \"cf\"\n"

func TestCascadeGateClears(t *testing.T) {
	e := newCascadeEnv(t, withGateAndTriage)
	e.jev.reply = jevReply(0.9, 0.2, 0.1, 0.3)
	p, events := e.run(t)
	require.NotNil(t, p.Inference)
	assert.Equal(t, proposal.StageGate, p.Inference.ClearedBy)
	assert.Equal(t, []string{"gate:clear"}, names(p.Inference.Stages))
	assert.Empty(t, p.Hunks)
	assert.Empty(t, e.triage.requests(), "triage did not run")
	assert.Empty(t, e.check.requests(), "the check did not run")
	st := p.Inference.Stages[0]
	assert.Equal(t, "jev", st.Provider)
	assert.Equal(t, "typesafe/jev-1.14-20261014", st.Model)
	assert.InDelta(t, 0.3, st.Scores[infer.QuestionStaleReference], 1e-9)
	assert.InDelta(t, 0.00007, st.CostUSD, 1e-12)
	assert.Equal(t, "No logically significant change found ("+Seconds(time.Duration(st.LatencyMS)*time.Millisecond)+"); the check did not run", p.Summary)
	assert.Contains(t, StageLine(st), "gate jev/jev-1.14-20261014 ")
	assert.Contains(t, StageLine(st), ": clear (max class 0.30)")
	require.Len(t, events, 2)
	assert.Equal(t, proposal.OutcomeRunning, events[0].Stage.Outcome)
	assert.Equal(t, "$Net.Timeout", events[0].Anchor)

	// The gate saw the policy, the situation and the four questions.
	req := e.jev.requests()[0]
	assert.Equal(t, "typesafe/jev-1.14", req["model"])
	state := req["state"].(map[string]any)
	assert.Contains(t, state["policy"], "A spec is a Markdown document")
	assert.Contains(t, state["situation"], timeoutNew)
	assert.NotContains(t, state["situation"], "## Anchor index", "no index in a situation")
	assert.Len(t, req["questions"], 4)
}

func TestCascadeEscalatesThroughTriageToCheck(t *testing.T) {
	e := newCascadeEnv(t, withGateAndTriage)
	e.jev.reply = jevReply(0.99, 0.2, 0.62, 0.1)
	e.triage.reply = chatReply(scoresJSON(1, 0.71, 0.2, 0))
	p, events := e.run(t)
	assert.Empty(t, p.Inference.ClearedBy)
	assert.Equal(t, []string{"gate:pass", "triage:pass", "check:done"}, names(p.Inference.Stages))
	require.Len(t, p.Hunks, 1, "the check's finding is a hunk")
	assert.Equal(t, 1, p.Inference.Stages[2].Findings)
	assert.Equal(t, "escalate (contradiction 0.62)", StageVerdict(p.Inference.Stages[0]))
	assert.Equal(t, "pass (numeric 0.71)", StageVerdict(p.Inference.Stages[1]))
	assert.Equal(t, "1 finding", StageVerdict(p.Inference.Stages[2]))
	assert.Equal(t, "cf", p.Inference.Stages[1].Provider)
	assert.Equal(t, "cf-triage", p.Inference.Stages[1].Model)
	assert.Equal(t, "check", p.Inference.Stages[2].Provider)

	var seq []string
	for _, ev := range events {
		seq = append(seq, ev.Stage.Name+":"+ev.Stage.Outcome)
	}
	assert.Equal(t, []string{"gate:running", "gate:pass", "triage:running", "triage:pass", "check:running", "check:done"}, seq)

	// Routing: triage went to cf with its triage model, the check to the
	// default provider.
	tr := e.triage.requests()
	require.Len(t, tr, 1)
	assert.Equal(t, "cf-triage", tr[0]["model"])
	msgs := tr[0]["messages"].([]any)
	assert.Contains(t, msgs[0].(map[string]any)["content"], "Reply with ONLY a JSON object")
	assert.Contains(t, msgs[1].(map[string]any)["content"], timeoutNew)
	cr := e.check.requests()
	require.Len(t, cr, 1)
	assert.Equal(t, "check-model", cr[0]["model"])
}

func TestCascadeTriageClears(t *testing.T) {
	e := newCascadeEnv(t, withGateAndTriage)
	e.triage.reply = chatReply("```json\n" + scoresJSON(1, 0.1, 0.05, 0) + "\n```")
	p, _ := e.run(t)
	assert.Equal(t, proposal.StageTriage, p.Inference.ClearedBy)
	assert.Equal(t, []string{"gate:pass", "triage:clear"}, names(p.Inference.Stages))
	assert.Empty(t, e.check.requests())
	assert.Contains(t, p.Summary, "No conflict found (")
	assert.InDelta(t, 0.0001, p.Inference.Stages[1].CostUSD, 1e-12, "OpenRouter's usage.cost is recorded")
}

func TestCascadeNoGate(t *testing.T) {
	e := newCascadeEnv(t, "[triage]\nprovider = \"cf\"\n")
	p, _ := e.run(t)
	assert.Equal(t, []string{"triage:pass", "check:done"}, names(p.Inference.Stages))
	assert.Empty(t, e.jev.requests())
}

func TestCascadeNotConfigured(t *testing.T) {
	// No [gate], no [triage], and the check provider has no triage model:
	// today's check, with no stages recorded.
	e := newCascadeEnv(t, "")
	p, events := e.run(t)
	assert.Nil(t, p.Inference.Stages)
	assert.Len(t, p.Hunks, 1)
	assert.Empty(t, e.jev.requests())
	assert.Empty(t, e.triage.requests())
	require.Len(t, events, 2, "the check still reports its start and end")
}

func TestCascadeGateFailsOpen(t *testing.T) {
	for name, reply := range map[string]func(http.ResponseWriter, map[string]any){
		"timeout": func(w http.ResponseWriter, body map[string]any) {
			time.Sleep(1500 * time.Millisecond)
			jevReply(0, 0, 0, 0)(w, body)
		},
		"http 500": func(w http.ResponseWriter, _ map[string]any) {
			http.Error(w, `{"error":{"message":"upstream broke"}}`, http.StatusInternalServerError)
		},
		"bad json": func(w http.ResponseWriter, _ map[string]any) { _, _ = io.WriteString(w, "not json") },
		"no scores": func(w http.ResponseWriter, _ map[string]any) {
			_, _ = io.WriteString(w, `{"answers": {}}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCascadeEnv(t, withGateAndTriage)
			e.jev.reply = reply
			p, _ := e.run(t)
			require.Equal(t, []string{"gate:failed_open", "triage:pass", "check:done"}, names(p.Inference.Stages))
			assert.NotEmpty(t, p.Inference.Stages[0].Reason)
			assert.Contains(t, StageLine(p.Inference.Stages[0]), "failed open: ")
			assert.Len(t, e.jev.requests(), 1, "one attempt: a failing gate is not retried")
		})
	}
}

func TestCascadeGateKeyMissingFailsOpen(t *testing.T) {
	e := newCascadeEnv(t, withGateAndTriage)
	e.wf.Getenv = func(k string) string {
		if k == "JEV_KEY" {
			return ""
		}
		return map[string]string{"HOME": filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(e.cfgPath)))), "XDG_CONFIG_HOME": filepath.Dir(filepath.Dir(e.cfgPath))}[k]
	}
	p, _ := e.run(t)
	require.Equal(t, []string{"gate:failed_open", "triage:pass", "check:done"}, names(p.Inference.Stages))
	assert.Contains(t, p.Inference.Stages[0].Reason, "JEV_KEY")
	assert.Equal(t, "gate jev/jev-1.14 0.0 s: failed open: cannot open the gate: provider \"jev\": the API key variable JEV_KEY (api_key_env) is not set", StageLine(p.Inference.Stages[0]))
}

func TestCascadeTriageFailsOpen(t *testing.T) {
	for name, reply := range map[string]func(http.ResponseWriter, map[string]any){
		"http 500":      func(w http.ResponseWriter, _ map[string]any) { http.Error(w, "boom", http.StatusInternalServerError) },
		"not json":      chatReply("I think it is fine."),
		"missing score": chatReply(`{"material_change": 1, "numeric_mismatch": 0}`),
		"out of range":  chatReply(scoresJSON(1, 7, 0, 0)),
	} {
		t.Run(name, func(t *testing.T) {
			e := newCascadeEnv(t, withGateAndTriage)
			e.triage.reply = reply
			p, _ := e.run(t)
			require.Equal(t, []string{"gate:pass", "triage:failed_open", "check:done"}, names(p.Inference.Stages))
			assert.NotEmpty(t, p.Inference.Stages[1].Reason)
			assert.Len(t, p.Hunks, 1)
		})
	}
}

func TestCascadeSkipMaterialBelow(t *testing.T) {
	// material 0.1 but a class score of 0.6: off, the gate escalates; at
	// 0.3, it clears the edit as not logically significant.
	off := newCascadeEnv(t, withGateAndTriage)
	off.jev.reply = jevReply(0.1, 0.6, 0, 0)
	p, _ := off.run(t)
	assert.Equal(t, "gate:pass", names(p.Inference.Stages)[0])

	on := newCascadeEnv(t, "[gate]\nprovider = \"jev\"\nskip_material_below = 0.3\n[triage]\nprovider = \"cf\"\n")
	on.jev.reply = jevReply(0.1, 0.6, 0, 0)
	p, _ = on.run(t)
	assert.Equal(t, []string{"gate:clear"}, names(p.Inference.Stages))
	assert.Equal(t, "clear (material 0.10)", StageVerdict(p.Inference.Stages[0]))
	assert.Contains(t, p.Summary, "No logically significant change found (")
}

func TestCascadeEscalateClassAt(t *testing.T) {
	e := newCascadeEnv(t, "[gate]\nprovider = \"jev\"\nescalate_class_at = 0.7\n")
	e.jev.reply = jevReply(0.9, 0.6, 0, 0)
	p, _ := e.run(t)
	assert.Equal(t, []string{"gate:clear"}, names(p.Inference.Stages))
}

func TestCascadeTriageOnCheckProvider(t *testing.T) {
	// Without [triage], the check provider's triage_model answers triage;
	// triage = false turns the stage off.
	e := newCascadeEnv(t, "")
	require.NoError(t, os.WriteFile(e.cfgPath, []byte("default = \"cf\"\n[providers.cf]\nbase_url = \""+e.triage.URL+"\"\nmodel = \"cf-model\"\ntriage_model = \"cf-triage\"\nstream = false\n"), 0o644))
	e.triage.reply = func(w http.ResponseWriter, body map[string]any) {
		if body["model"] == "cf-triage" {
			chatReply(scoresJSON(1, 0.9, 0, 0))(w, body)
			return
		}
		chatReply(checkFinding)(w, body)
	}
	p, _ := e.run(t)
	assert.Equal(t, []string{"triage:pass", "check:done"}, names(p.Inference.Stages))
	reqs := e.triage.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "cf-triage", reqs[0]["model"])
	assert.Equal(t, "cf-model", reqs[1]["model"])

	require.NoError(t, os.WriteFile(e.cfgPath, []byte("default = \"cf\"\n[providers.cf]\nbase_url = \""+e.triage.URL+"\"\nmodel = \"cf-model\"\ntriage_model = \"cf-triage\"\ntriage = false\nstream = false\n"), 0o644))
	p, _ = e.run(t)
	assert.Nil(t, p.Inference.Stages, "triage = false: no stage")
	assert.Len(t, e.triage.requests(), 3, "only the check")
}

func TestCascadeTriageProviderTriageOff(t *testing.T) {
	// [triage] names cf, whose triage = false: no triage stage, but the
	// fast requests still go to cf.
	e := newCascadeEnv(t, "")
	cfg, err := os.ReadFile(e.cfgPath)
	require.NoError(t, err)
	cfg = []byte(strings.Replace(string(cfg), "triage_model = \"cf-triage\"\n", "triage_model = \"cf-triage\"\ntriage = false\n", 1) + "[triage]\nprovider = \"cf\"\n")
	require.NoError(t, os.WriteFile(e.cfgPath, cfg, 0o644))
	p, _ := e.run(t)
	assert.Nil(t, p.Inference.Stages)
	assert.Empty(t, e.triage.requests())

	e.triage.reply = chatReply(`{"subject": "spec: raise the timeout", "body": ""}`)
	ck, werr := e.wf.OpenChecker(context.Background(), false)
	require.Nil(t, werr)
	d, werr := ck.DraftCommit(prompt.CommitInput{Changes: []prompt.CommitChange{{What: "inv $Net.Timeout changed", Before: timeoutOld, After: timeoutNew}}})
	require.Nil(t, werr)
	assert.Equal(t, "spec: raise the timeout", d.Subject)
	reqs := e.triage.requests()
	require.Len(t, reqs, 1, "the commit draft went to the [triage] provider")
	assert.Equal(t, "cf-triage", reqs[0]["model"])
	assert.Len(t, e.check.requests(), 1, "only the check, earlier")
}

// TestCommitDraftRefusesFalseRename: a draft that names a rename the
// changes do not make (here an anchor renamed to itself) is bad model
// output, so the commit view falls back to its template.
func TestCommitDraftRefusesFalseRename(t *testing.T) {
	e := newCascadeEnv(t, "")
	e.check.reply = chatReply(`{"subject": "spec: raise the timeout", "body": "- ` + "`$Net.Timeout`" + ` is longer, and is renamed ` + "`$Net.Timeout`" + ` to ` + "`$Net.Timeout`" + `."}`)
	ck, werr := e.wf.OpenChecker(context.Background(), false)
	require.Nil(t, werr)
	_, werr = ck.DraftCommit(prompt.CommitInput{Changes: []prompt.CommitChange{{What: "inv $Net.Timeout changed", Before: timeoutOld, After: timeoutNew}}})
	require.NotNil(t, werr)
	assert.Equal(t, "bad_model_output", werr.Code)
	assert.Contains(t, werr.Message, "$Net.Timeout to $Net.Timeout")
}

// TestCommitDraftRefusesFalseFix: a draft that claims more fixes than the
// commit applies is bad model output, so the commit view falls back to its
// template; as many as applied pass.
func TestCommitDraftRefusesFalseFix(t *testing.T) {
	e := newCascadeEnv(t, "")
	e.check.reply = chatReply(`{"subject": "spec: raise the timeout", "body": "- Fix the prose to name the timeout.\n- Fix the retry count."}`)
	ck, werr := e.wf.OpenChecker(context.Background(), false)
	require.Nil(t, werr)
	in := prompt.CommitInput{Changes: []prompt.CommitChange{{What: "inv $Net.Timeout changed", Before: timeoutOld, After: timeoutNew}}, Accepted: []string{"name the timeout"}}
	_, werr = ck.DraftCommit(in)
	require.NotNil(t, werr)
	assert.Equal(t, "bad_model_output", werr.Code)
	assert.Contains(t, werr.Message, "claims 2 fixes, but the commit applies 1")

	in.Accepted = append(in.Accepted, "raise the retry count")
	d, werr := ck.DraftCommit(in)
	require.Nil(t, werr)
	assert.Equal(t, "spec: raise the timeout", d.Subject)
}

func TestCascadeCommitDraftRouting(t *testing.T) {
	// Without [triage] the draft goes to the check provider; with it, to
	// the triage provider's triage model.
	e := newCascadeEnv(t, "")
	e.check.reply = chatReply(`{"subject": "spec: x", "body": ""}`)
	ck, werr := e.wf.OpenChecker(context.Background(), false)
	require.Nil(t, werr)
	_, werr = ck.DraftCommit(prompt.CommitInput{})
	require.Nil(t, werr)
	assert.Len(t, e.check.requests(), 1)
	assert.Empty(t, e.triage.requests())

	e2 := newCascadeEnv(t, "[triage]\nprovider = \"cf\"\n")
	e2.triage.reply = chatReply(`{"subject": "spec: x", "body": ""}`)
	ck, werr = e2.wf.OpenChecker(context.Background(), false)
	require.Nil(t, werr)
	_, werr = ck.DraftCommit(prompt.CommitInput{})
	require.Nil(t, werr)
	assert.Empty(t, e2.check.requests())
	require.Len(t, e2.triage.requests(), 1)
	assert.Equal(t, "cf-triage", e2.triage.requests()[0]["model"])
}

func TestCascadeRunawayRetryGoesToTriageProvider(t *testing.T) {
	e := newCascadeEnv(t, "[triage]\nprovider = \"cf\"\n")
	// cf's triage is off for this test: only the guard is under test.
	cfg := strings.Replace(string(mustRead(t, e.cfgPath)), "triage_model = \"cf-triage\"\n", "triage_model = \"cf-triage\"\ntriage = false\n", 1)
	require.NoError(t, os.WriteFile(e.cfgPath, []byte(cfg), 0o644))
	e.check.reply = func(w http.ResponseWriter, _ map[string]any) {
		_, _ = io.WriteString(w, `{"model":"check-model","choices":[{"message":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":16384}}`)
	}
	e.triage.reply = chatReply(checkFinding)
	p, _ := e.run(t)
	assert.True(t, p.Inference.RunawayGuard)
	assert.Len(t, e.check.requests(), 1)
	require.Len(t, e.triage.requests(), 1, "the retry went to the [triage] provider")
	assert.Equal(t, "cf-triage", e.triage.requests()[0]["model"])
	assert.Len(t, p.Hunks, 1)
}

func TestCascadeSkipsAddsAndRemovals(t *testing.T) {
	e := newCascadeEnv(t, withGateAndTriage)
	ck, werr := e.wf.OpenChecker(context.Background(), false)
	require.Nil(t, werr)
	spec := mdspec.Build([]*mdspec.File{mdspec.ParseFile("demo.spec.md", []byte(cascadeSpec))})
	ch := &specctx.Change{Action: specctx.ActionRemove, Name: "Net.Retry", File: "demo.spec.md", OldKind: mdspec.KindInvariant,
		OldText: "**$Net.Retry (retry).** A request that hits $Net.Timeout is retried twice, 60 s apart."}
	p, werr := e.wf.CheckChange(ck, spec, ch, &proposal.Primary{N: 1, Name: "$Net.Retry", Action: ch.Action}, nil, proposal.AuthorHuman)
	require.Nil(t, werr)
	assert.Nil(t, p.Inference.Stages, "only body changes are screened")
	assert.Empty(t, e.jev.requests())
	assert.Empty(t, e.triage.requests())
}

func TestCascadeCancelled(t *testing.T) {
	e := newCascadeEnv(t, withGateAndTriage)
	ctx, cancel := context.WithCancel(context.Background())
	e.jev.reply = func(w http.ResponseWriter, body map[string]any) {
		cancel()
		time.Sleep(50 * time.Millisecond)
		jevReply(0, 0, 0, 0)(w, body)
	}
	ck, werr := e.wf.OpenChecker(ctx, false)
	require.Nil(t, werr)
	spec := mdspec.Build([]*mdspec.File{mdspec.ParseFile("demo.spec.md", []byte(cascadeSpec))})
	ch := &specctx.Change{Action: specctx.ActionChange, Name: "Net.Timeout", File: "demo.spec.md",
		OldKind: mdspec.KindInvariant, NewKind: mdspec.KindInvariant, OldText: timeoutOld, NewText: timeoutNew}
	_, werr = e.wf.CheckChange(ck, spec, ch, &proposal.Primary{N: 1, Name: "$Net.Timeout"}, nil, proposal.AuthorHuman)
	require.NotNil(t, werr)
	assert.Equal(t, "cancelled", werr.Code)
	assert.Empty(t, e.check.requests())
}

func TestStageText(t *testing.T) {
	assert.Equal(t, "glm-5.3-flash", ShortModel("@cf/zai-org/glm-5.3-flash"))
	assert.Equal(t, "deepseek-flash", ShortModel("deepseek-flash"))
	assert.Equal(t, "0.3 s", Seconds(300*time.Millisecond))
	assert.Equal(t, "14 s", Seconds(14*time.Second))
	assert.Equal(t, "4 min 2 s", Seconds(242*time.Second))
	st := proposal.Stage{Name: proposal.StageTriage, Provider: "cf", Model: "@cf/zai-org/glm-5.3-flash", LatencyMS: 1900,
		Outcome: proposal.OutcomeClear, Scores: map[string]float64{infer.QuestionNumericMismatch: 0.1}}
	assert.Equal(t, "triage cf/glm-5.3-flash 1.9 s: clear (max class 0.10)", StageLine(st))
	assert.Equal(t, "No conflict found (1.9 s)", ClearedText(st))
	gate := proposal.Stage{Name: proposal.StageGate, LatencyMS: 300, Outcome: proposal.OutcomeClear}
	assert.Equal(t, "No logically significant change found (0.3 s)", ClearedText(gate))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}
