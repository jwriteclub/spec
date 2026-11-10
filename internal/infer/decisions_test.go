// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
)

// gateQuestions are the four questions of the per-edit gate.
var gateQuestions = []Question{
	{ID: QuestionMaterialChange, Instructions: "changes a claim?"},
	{ID: QuestionNumericMismatch, Instructions: "numbers disagree?"},
	{ID: QuestionContradiction, Instructions: "cannot both hold?"},
	{ID: QuestionStaleReference, Instructions: "citation now wrong?"},
}

// decisionsReplyBody is a Decisions API reply with the given scores.
func decisionsReplyBody(scores map[string]float64) map[string]any {
	answers := map[string]any{}
	for q, v := range scores {
		answers[q] = map[string]any{"type": "noul", "noul": v}
	}
	return map[string]any{
		"model": "typesafe/jev-1.14-20261014", "id": "gen-dec-1", "provider": "TypeSafe",
		"answers": answers,
		"usage":   map[string]any{"input_tokens": 2047, "output_tokens": 77, "cost": 8.5974e-05},
	}
}

func writeJSONReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// decisionsFor returns a Jev gate on the fake whose waits are recorded, not slept.
func decisionsFor(t *testing.T, f *fake, waits *[]time.Duration) *Decisions {
	t.Helper()
	g, err := OpenGate("jev", config.Provider{Kind: config.KindDecisions, Model: "typesafe/jev-1.14",
		BaseURL: f.srv.URL + "/api/alpha", APIKeyEnv: "OPENROUTER_API_KEY"},
		func(n string) string {
			if n == "OPENROUTER_API_KEY" {
				return testKey
			}
			return ""
		})
	require.NoError(t, err)
	j, ok := g.(*Decisions)
	require.True(t, ok)
	j.c.sleep = func(ctx context.Context, d time.Duration) error {
		if waits != nil {
			*waits = append(*waits, d)
		}
		return ctx.Err()
	}
	return j
}

func TestDecisionsDecide(t *testing.T) {
	scores := map[string]float64{QuestionMaterialChange: 0.99, QuestionNumericMismatch: 0.88, QuestionContradiction: 0.66, QuestionStaleReference: 0.2}
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSONReply(w, decisionsReplyBody(scores)) })
	j := decisionsFor(t, f, nil)
	info := j.Info()
	assert.Equal(t, config.KindDecisions, info.Kind)
	assert.Equal(t, "typesafe/jev-1.14", info.Model)

	d, err := j.Decide(context.Background(), GateRequest{Policy: "the policy", Situation: "the edit", Questions: gateQuestions})
	require.NoError(t, err)
	assert.Equal(t, scores, d.Scores)
	assert.Equal(t, "jev", d.Provider)
	assert.Equal(t, "typesafe/jev-1.14", d.RequestedModel)
	assert.Equal(t, "typesafe/jev-1.14-20261014", d.Model)
	assert.Equal(t, "TypeSafe", d.Upstream)
	assert.Equal(t, "gen-dec-1", d.ID)
	assert.Equal(t, Usage{PromptTokens: 2047, CompletionTokens: 77, TotalTokens: 2124}, d.Usage)
	assert.InDelta(t, 8.5974e-05, d.CostUSD, 1e-12)
	assert.Equal(t, 1, d.Attempts)
	m, ok := d.MaxClass()
	require.True(t, ok)
	assert.InDelta(t, 0.88, m, 1e-9)

	reqs := f.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, http.MethodPost, reqs[0].Method)
	assert.Equal(t, "/api/alpha/decisions", reqs[0].Path)
	assert.Equal(t, "Bearer "+testKey, reqs[0].Auth)
	assert.Equal(t, "typesafe/jev-1.14", reqs[0].Body["model"])
	assert.Equal(t, map[string]any{"policy": "the policy", "situation": "the edit"}, reqs[0].Body["state"])
	qs, ok := reqs[0].Body["questions"].(map[string]any)
	require.True(t, ok)
	require.Len(t, qs, 4)
	assert.Equal(t, map[string]any{"type": "noul", "instructions": "numbers disagree?"}, qs[QuestionNumericMismatch])
}

func TestDecisionsRetriesAndRedacts(t *testing.T) {
	scores := map[string]float64{QuestionMaterialChange: 0.1, QuestionNumericMismatch: 0.1, QuestionContradiction: 0.1, QuestionStaleReference: 0.1}
	f := newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		writeJSONReply(w, decisionsReplyBody(scores))
	})
	var waits []time.Duration
	j := decisionsFor(t, f, &waits)
	d, err := j.Decide(context.Background(), GateRequest{Situation: "s", Questions: gateQuestions})
	require.NoError(t, err)
	assert.Equal(t, 2, d.Attempts)
	assert.Len(t, waits, 1)

	// A 401 is not retried, and the key never appears in the error.
	f = newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key `+testKey+`"}}`)
	})
	j = decisionsFor(t, f, nil)
	_, err = j.Decide(context.Background(), GateRequest{Situation: "s", Questions: gateQuestions})
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, http.StatusUnauthorized, e.Status)
	assert.Equal(t, 1, e.Attempts)
	assert.NotContains(t, err.Error(), testKey)
	assert.Contains(t, err.Error(), "[redacted]")
	assert.Len(t, f.requests(), 1)
}

func TestDecisionsBadReplies(t *testing.T) {
	// A question left unanswered, or a score outside [0, 1], is an error
	// that is not retried.
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeJSONReply(w, decisionsReplyBody(map[string]float64{QuestionMaterialChange: 0.5, QuestionNumericMismatch: 1.7, QuestionContradiction: 0.1}))
	})
	j := decisionsFor(t, f, nil)
	_, err := j.Decide(context.Background(), GateRequest{Situation: "s", Questions: gateQuestions})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no probability in [0, 1] for numeric_mismatch, stale_reference")
	assert.Len(t, f.requests(), 1)

	f = newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "not json") })
	j = decisionsFor(t, f, nil)
	_, err = j.Decide(context.Background(), GateRequest{Situation: "s", Questions: gateQuestions})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not JSON")

	_, err = j.Decide(context.Background(), GateRequest{Situation: "s"})
	assert.Error(t, err)
	_, err = j.Decide(context.Background(), GateRequest{Situation: "s", Questions: []Question{{ID: "a"}, {ID: "a"}}})
	assert.Error(t, err)
}

func TestOpenGateAndOpen(t *testing.T) {
	env := func(string) string { return "" }
	_, err := OpenGate("cf", config.Provider{BaseURL: "http://x", Model: "m"}, env)
	assert.ErrorContains(t, err, "not a gate")
	_, err = OpenGate("jev", config.Provider{Kind: config.KindDecisions, Model: "m", APIKeyEnv: "OPENROUTER_API_KEY"}, env)
	var ee *config.EnvError
	assert.True(t, errors.As(err, &ee))
	g, err := OpenGate("jev", config.Provider{Kind: config.KindDecisions, Model: "m"}, env)
	require.NoError(t, err)
	assert.Equal(t, "https://openrouter.ai", g.Info().Transport)

	_, err = Open("jev", config.Provider{Kind: config.KindDecisions, Model: "m"}, env)
	var se *SetupError
	assert.True(t, errors.As(err, &se))
}

func TestEscalates(t *testing.T) {
	s := func(mat, num, con, stale float64) map[string]float64 {
		return map[string]float64{QuestionMaterialChange: mat, QuestionNumericMismatch: num, QuestionContradiction: con, QuestionStaleReference: stale}
	}
	def := config.Gate{Provider: "jev"}
	assert.True(t, Escalates(def, s(0.9, 0.1, 0.45, 0.1)).Escalate, "at the threshold escalates")
	assert.False(t, Escalates(def, s(0.9, 0.1, 0.44, 0.1)).Escalate)
	assert.True(t, Escalates(def, s(0.1, 0.9, 0.1, 0.1)).Escalate, "the material skip is off by default")

	skip := config.Gate{Provider: "jev", SkipMaterialBelow: 0.3}
	o := Escalates(skip, s(0.1, 0.9, 0.1, 0.1))
	assert.False(t, o.Escalate)
	assert.Contains(t, o.Reason, "editorial")
	assert.True(t, Escalates(skip, s(0.3, 0.9, 0.1, 0.1)).Escalate)

	old := config.Gate{Provider: "jev", EscalateClassAt: 0.15, SkipMaterialBelow: 0.3}
	assert.True(t, Escalates(old, s(0.5, 0.16, 0, 0)).Escalate)

	o = Escalates(def, map[string]float64{QuestionMaterialChange: 0.1})
	assert.True(t, o.Escalate, "no class score fails open")
}

func TestChatReportsOpenRouterCost(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeJSONReply(w, map[string]any{
			"model":   "anthropic/claude-opus-5.5",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "OK"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 2, "total_tokens": 13, "cost": 0.0123},
		})
	})
	resp, err := f.client(nil).Chat(context.Background(), Request{Messages: []Message{User("hi")}})
	require.NoError(t, err)
	assert.InDelta(t, 0.0123, resp.CostUSD, 1e-12)
	assert.Equal(t, 13, resp.Usage.TotalTokens)

	f = newFake(t, streams(contentChunk("OK"), finishChunk("stop"), sseData(map[string]any{
		"model": "m", "choices": []any{},
		"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 1, "cost": 0.5},
	}), sseDone))
	resp, err = f.streamClient(nil).Chat(context.Background(), Request{Messages: []Message{User("hi")}})
	require.NoError(t, err)
	assert.InDelta(t, 0.5, resp.CostUSD, 1e-12)
	assert.Equal(t, 6, resp.Usage.TotalTokens)
}
