// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
)

// cannedFile writes a replies file and returns its path.
func cannedFile(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "replies.toml")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))
	return path
}

func noEnv(string) string { return "" }

const cannedSrc = `
[[check]]
reply = '{"summary": "first", "findings": [], "questions": []}'

[[check]]
reply = '{"summary": "second", "findings": [], "questions": []}'
model = "echoed-model"

[[triage]]
reply = '{"numeric_mismatch": 0.1}'
latency_ms = 250

[[draft]]
reply = '{"subject": "spec: x", "body": ""}'

[[gate]]
scores = { material_change = 0.9, contradiction = 0.62 }
latency_ms = 300
`

// Replies come in order within each purpose; the triage model answers a
// triage request; latency_ms is reported as is.
func TestCannedServesInOrderPerPurpose(t *testing.T) {
	path := cannedFile(t, cannedSrc)
	p, err := Open("c", config.Provider{Kind: config.KindCanned, Path: path, TriageModel: "tri"}, noEnv)
	require.NoError(t, err)
	assert.Equal(t, Info{Name: "c", Kind: config.KindCanned, Model: "canned", TriageModel: "tri", Transport: path}, p.Info())

	ctx := context.Background()
	var v map[string]any
	resp, err := ChatJSON(ctx, p, Request{Purpose: PurposeTriage, Triage: true}, &v)
	require.NoError(t, err)
	assert.Equal(t, 0.1, v["numeric_mismatch"])
	assert.Equal(t, "tri", resp.RequestedModel)
	assert.Equal(t, 250*time.Millisecond, resp.Latency)

	resp, err = p.Chat(ctx, Request{})
	require.NoError(t, err, "no purpose counts as a check")
	assert.Contains(t, resp.Content, "first")
	assert.Equal(t, "canned", resp.Model)
	resp, err = p.Chat(ctx, Request{Purpose: PurposeCheck})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "second")
	assert.Equal(t, "echoed-model", resp.Model)

	// A second provider on the same file goes on where the first stopped.
	again, err := Open("c", config.Provider{Kind: config.KindCanned, Path: path}, noEnv)
	require.NoError(t, err)
	resp, err = again.Chat(ctx, Request{Purpose: PurposeDraft})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "spec: x")

	gate, err := OpenGate("c", config.Provider{Kind: config.KindCanned, Path: path}, noEnv)
	require.NoError(t, err)
	d, err := gate.Decide(ctx, GateRequest{Questions: []Question{{ID: "material_change"}, {ID: "contradiction"}}})
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"material_change": 0.9, "contradiction": 0.62}, d.Scores)
	assert.Equal(t, 300*time.Millisecond, d.Latency)
	assert.Equal(t, "c", d.Provider)
}

// A call with no entry left names the purpose, the entry's number and
// the request's first lines.
func TestCannedMissingEntry(t *testing.T) {
	path := cannedFile(t, cannedSrc)
	p, err := NewCanned("c", config.Provider{Kind: config.KindCanned, Path: path}, noEnv)
	require.NoError(t, err)
	_, err = p.Chat(context.Background(), Request{Purpose: PurposeFix, Messages: []Message{
		System("the system prompt"), User("\nFix W-anchor-label at lensd.spec.md:40\nthe second line\nthird\nfourth"),
	}})
	var ie *Error
	require.ErrorAs(t, err, &ie)
	assert.Contains(t, ie.Error(), "no [[fix]] entry #1 (the file has 0)")
	assert.Contains(t, ie.Error(), "  Fix W-anchor-label at lensd.spec.md:40\n  the second line\n  third\n  …")
	assert.Contains(t, ie.Fix(), "add a [[fix]] entry to "+path)

	_, err = p.Decide(context.Background(), GateRequest{Situation: "x"})
	require.NoError(t, err)
	_, err = p.Decide(context.Background(), GateRequest{Situation: "the edit"})
	require.ErrorAs(t, err, &ie)
	assert.Contains(t, ie.Error(), "no [[gate]] entry #2 (the file has 1)")
}

// error = "timeout", "http_500" and "bad_json" fail the call as a real
// provider would; delay_ms holds the reply and the context cancels it.
func TestCannedErrorsAndDelay(t *testing.T) {
	path := cannedFile(t, `
[[check]]
error = "timeout"
[[check]]
error = "http_500"
[[check]]
error = "bad_json"
[[check]]
reply = "{}"
delay_ms = 5000
[[gate]]
scores = { material_change = 0.9 }
`)
	p, err := NewCanned("c", config.Provider{Kind: config.KindCanned, Path: path}, noEnv)
	require.NoError(t, err)
	ctx := context.Background()

	_, err = p.Chat(ctx, Request{})
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "%v", err)
	var ie *Error
	_, err = p.Chat(ctx, Request{})
	require.ErrorAs(t, err, &ie)
	assert.Equal(t, http.StatusInternalServerError, ie.Status)
	var v map[string]any
	_, err = ChatJSON(ctx, p, Request{}, &v)
	var je *JSONError
	require.ErrorAs(t, err, &je)

	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = p.Chat(cctx, Request{})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second, "the context cut the delay short")

	_, err = p.Decide(ctx, GateRequest{Questions: []Question{{ID: "material_change"}, {ID: "contradiction"}}})
	require.ErrorAs(t, err, &ie)
	assert.Contains(t, ie.Error(), "no score for contradiction")
}

// A changed file starts over; a bad file is a *SetupError.
func TestCannedFileProblems(t *testing.T) {
	path := cannedFile(t, "[[check]]\nreply = \"one\"\n")
	p, err := NewCanned("c", config.Provider{Kind: config.KindCanned, Path: path}, noEnv)
	require.NoError(t, err)
	_, err = p.Chat(context.Background(), Request{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("[[check]]\nreply = \"two\"\n"), 0o644))
	p, err = NewCanned("c", config.Provider{Kind: config.KindCanned, Path: path}, noEnv)
	require.NoError(t, err)
	resp, err := p.Chat(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "two", resp.Content)

	for src, want := range map[string]string{
		"[[check]]\nreply = 1\n":                   "reply",
		"[[check]]\nbogus = 1\n":                   "bogus",
		"[[nope]]\nreply = \"x\"\n":                "nope",
		"[[check]]\nerror = \"boom\"\n":            `error "boom" must be`,
		"[[gate]]\nreply = \"x\"\n":                "a gate entry takes scores",
		"[[check]]\nscores = { a = 1.0 }\n":        "scores are for [[gate]] entries",
		"[[triage]]\nreply = \"x\"\ndelay_ms = -1": "must not be negative",
	} {
		_, err := NewCanned("c", config.Provider{Kind: config.KindCanned, Path: cannedFile(t, src)}, noEnv)
		var se *SetupError
		require.ErrorAs(t, err, &se, src)
		assert.Contains(t, se.Error(), want, src)
	}
	_, err = NewCanned("c", config.Provider{Kind: config.KindCanned, Path: filepath.Join(t.TempDir(), "absent.toml")}, noEnv)
	var se *SetupError
	require.ErrorAs(t, err, &se)

	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "r.toml"), []byte("[[check]]\nreply = \"home\"\n"), 0o644))
	p, err = NewCanned("c", config.Provider{Kind: config.KindCanned, Path: "~/r.toml"}, func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	})
	require.NoError(t, err)
	resp, err = p.Chat(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "home", resp.Content)
}

// The usage keys round-trip: a file written with them parses, and the
// replies report them; a file without them still reports "stop" and no
// usage.
func TestCannedUsageKeys(t *testing.T) {
	src := `
[[check]]
reply = "cut off"
finish_reason = "length"
cost_usd = 0.0123
prompt_tokens = 1200
completion_tokens = 340

[[check]]
reply = "plain"

[[gate]]
scores = { contradiction = 0.2 }
cost_usd = 0.0001
prompt_tokens = 50

[[gate]]
scores = { contradiction = 0.2 }

[[gate]]
scores = { contradiction = 0.2 }
cost_usd = 0.0
cost_reported = true
`
	f, err := ParseCanned([]byte(src))
	require.NoError(t, err)
	assert.Equal(t, "length", f.Check[0].FinishReason)
	path := cannedFile(t, src)
	p, err := NewCanned("c", config.Provider{Kind: config.KindCanned, Path: path}, noEnv)
	require.NoError(t, err)
	ctx := context.Background()
	resp, err := p.Chat(ctx, Request{})
	require.NoError(t, err)
	assert.Equal(t, "length", resp.FinishReason)
	assert.InDelta(t, 0.0123, resp.CostUSD, 1e-9)
	assert.Equal(t, Usage{PromptTokens: 1200, CompletionTokens: 340, TotalTokens: 1540}, resp.Usage)
	resp, err = p.Chat(ctx, Request{})
	require.NoError(t, err)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, Usage{}, resp.Usage)
	assert.Zero(t, resp.CostUSD)
	d, err := p.Decide(ctx, GateRequest{Questions: []Question{{ID: QuestionContradiction}}})
	require.NoError(t, err)
	assert.InDelta(t, 0.0001, d.CostUSD, 1e-9)
	assert.True(t, d.CostReported)
	assert.Equal(t, 50, d.Usage.PromptTokens)
	d, err = p.Decide(ctx, GateRequest{Questions: []Question{{ID: QuestionContradiction}}})
	require.NoError(t, err)
	assert.Zero(t, d.CostUSD)
	assert.False(t, d.CostReported)
	d, err = p.Decide(ctx, GateRequest{Questions: []Question{{ID: QuestionContradiction}}})
	require.NoError(t, err)
	assert.Zero(t, d.CostUSD)
	assert.True(t, d.CostReported)

	_, err = ParseCanned([]byte("[[check]]\nbogus = 1\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finish_reason, cost_usd, cost_reported, prompt_tokens, completion_tokens")
	_, err = ParseCanned([]byte("[[gate]]\nscores = { a = 1.0 }\nfinish_reason = \"stop\"\n"))
	require.Error(t, err)
	_, err = ParseCanned([]byte("[[check]]\nreply = \"x\"\ncost_usd = -1.0\n"))
	require.Error(t, err)
}

// ForgetCanned starts a file over even when its content is unchanged.
func TestForgetCanned(t *testing.T) {
	path := cannedFile(t, "[[check]]\nreply = \"one\"\n")
	open := func() *Canned {
		p, err := NewCanned("c", config.Provider{Kind: config.KindCanned, Path: path}, noEnv)
		require.NoError(t, err)
		return p
	}
	_, err := open().Chat(context.Background(), Request{})
	require.NoError(t, err)
	_, err = open().Chat(context.Background(), Request{})
	require.Error(t, err, "the only entry is served")
	ForgetCanned(path)
	resp, err := open().Chat(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "one", resp.Content)
}

// RequestHead and GateHead are the first lines of the last message and of
// the situation.
func TestHeads(t *testing.T) {
	assert.Equal(t, "  a\n  b\n  c\n  …", RequestHead(Request{Messages: []Message{System("s"), User("a\n\nb\nc\nd")}}))
	assert.Equal(t, "(no messages)", RequestHead(Request{}))
	assert.Equal(t, "  x", GateHead(GateRequest{Situation: "x\n"}))
}
