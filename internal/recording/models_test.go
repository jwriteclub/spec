// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer"
)

// fakeProvider answers each request by its last message: "slow:" and
// "fast:" replies after a delay, "cancel" waits for the context, "timeout"
// and "500" fail.
type fakeProvider struct {
	release chan struct{}
}

func (f *fakeProvider) Chat(ctx context.Context, req infer.Request) (*infer.Response, error) {
	msg := req.Messages[len(req.Messages)-1].Content
	switch msg {
	case "cancel":
		<-ctx.Done()
		return nil, ctx.Err()
	case "timeout":
		return nil, fmt.Errorf("fake: %w", context.DeadlineExceeded)
	case "500":
		return nil, &infer.Error{Provider: "fake", Status: 500, Message: "upstream down"}
	case "slow":
		<-f.release
	}
	return &infer.Response{
		Content: "reply to " + msg, Provider: "fake", RequestedModel: "m1", Model: "m1-echo",
		FinishReason: map[bool]string{true: "length", false: "stop"}[msg == "long"],
		Usage:        infer.Usage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13},
		CostUSD:      0.002, Latency: 42 * time.Millisecond, Attempts: 1,
	}, nil
}

func (f *fakeProvider) Info() infer.Info {
	return infer.Info{Name: "fake", Kind: config.KindOpenAI, Model: "m1", TriageModel: "m1-tri"}
}
func (f *fakeProvider) SetModel(string)              {}
func (f *fakeProvider) SetEffort(string)             {}
func (f *fakeProvider) SetLimits(time.Duration, int) {}
func (f *fakeProvider) RunawayExtra() map[string]any { return nil }
func (f *fakeGate) Info() infer.Info {
	return infer.Info{Name: "jev", Kind: config.KindDecisions, Model: "g1"}
}
func (f *fakeGate) SetLimits(time.Duration, int) {}
func (f *fakeGate) Decide(ctx context.Context, req infer.GateRequest) (*infer.Decision, error) {
	if req.Situation == "deadline" {
		return nil, fmt.Errorf("gate: %w", context.DeadlineExceeded)
	}
	if req.Situation == "zero" || req.Situation == "missing" {
		return &infer.Decision{CostReported: req.Situation == "zero"}, nil
	}
	return &infer.Decision{Scores: map[string]float64{infer.QuestionContradiction: 0.7, infer.QuestionMaterialChange: 0.25},
		Provider: "jev", Model: "g1", Latency: 300 * time.Millisecond, CostUSD: 0.0001}, nil
}

type fakeGate struct{}

func chat(msg, purpose string) infer.Request {
	return infer.Request{Purpose: purpose, Messages: []infer.Message{infer.System("sys"), infer.User(msg)}}
}

// A recorded session's calls come back from a canned provider opened on
// canned.toml with the same content, model, finish reason, usage, cost and
// latency, in call-start order even when a later call finished first.
func TestModelRecorderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	mr, err := NewModelRecorder(dir, time.Now())
	require.NoError(t, err)
	keys := 0
	mr.SetKeys(func() int { return keys })
	fake := &fakeProvider{release: make(chan struct{})}
	p := mr.WrapProvider(fake)
	g := mr.WrapGate(&fakeGate{})
	ctx := context.Background()

	// Two concurrent checks: "slow" starts first and finishes second.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := p.Chat(ctx, chat("slow", ""))
		assert.NoError(t, err)
	}()
	require.Eventually(t, func() bool { return mr.Calls() == 1 }, 2*time.Second, time.Millisecond)
	keys = 3
	_, err = p.Chat(ctx, chat("fast", infer.PurposeCheck))
	require.NoError(t, err)
	close(fake.release)
	wg.Wait()

	_, err = p.Chat(ctx, chat("long", infer.PurposeTriage))
	require.NoError(t, err)
	_, err = p.Chat(ctx, chat("timeout", infer.PurposeTriage))
	require.Error(t, err)
	_, err = p.Chat(ctx, chat("500", infer.PurposeDraft))
	require.Error(t, err)
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err = p.Chat(cctx, chat("cancel", infer.PurposeFix))
	require.ErrorIs(t, err, context.Canceled)
	gq := []infer.Question{{ID: infer.QuestionContradiction}, {ID: infer.QuestionMaterialChange}}
	_, err = g.Decide(ctx, infer.GateRequest{Situation: "edit of $A\nmore", Questions: gq})
	require.NoError(t, err)
	_, err = g.Decide(ctx, infer.GateRequest{Situation: "deadline", Questions: gq})
	require.Error(t, err)
	require.NoError(t, mr.Close())

	src, err := os.ReadFile(filepath.Join(dir, CannedFile))
	require.NoError(t, err)
	f, err := infer.ParseCanned(src)
	require.NoError(t, err, string(src))
	require.Len(t, f.Check, 2)
	assert.Equal(t, "reply to slow", f.Check[0].Reply, "call-start order")
	assert.Equal(t, "reply to fast", f.Check[1].Reply)
	assert.GreaterOrEqual(t, f.Check[0].DelayMS, f.Check[1].DelayMS)
	require.Len(t, f.Triage, 2)
	assert.Equal(t, "length", f.Triage[0].FinishReason)
	assert.Equal(t, infer.CannedTimeout, f.Triage[1].Error)
	require.Len(t, f.Draft, 1)
	assert.Equal(t, infer.CannedHTTP500, f.Draft[0].Error)
	require.Len(t, f.Fix, 1)
	assert.GreaterOrEqual(t, f.Fix[0].DelayMS, cancelledDelay+15)
	assert.Empty(t, f.Fix[0].Error)
	require.Len(t, f.Gate, 2)
	assert.Equal(t, infer.CannedTimeout, f.Gate[1].Error)

	rec, err := loadRequests(dir)
	require.NoError(t, err)
	require.Len(t, rec, 8)
	byOutcome := map[string]int{}
	for _, r := range rec {
		byOutcome[r.Outcome]++
	}
	assert.Equal(t, map[string]int{OutcomeOK: 4, OutcomeTimeout: 2, OutcomeError: 1, OutcomeCancelled: 1}, byOutcome)
	for _, r := range rec {
		if r.Purpose == infer.PurposeCheck && r.Index == 1 {
			assert.Equal(t, 3, r.Keys)
			assert.Equal(t, "  fast", r.Head)
		}
		if r.Purpose == infer.PurposeDraft {
			assert.Contains(t, r.Error, "upstream down")
		}
	}

	// Serve it back.
	canned, err := infer.NewCanned("rec", config.Provider{Kind: config.KindCanned, Path: filepath.Join(dir, CannedFile), Model: "m1"}, func(string) string { return "" })
	require.NoError(t, err)
	r1, err := canned.Chat(ctx, chat("slow", ""))
	require.NoError(t, err)
	assert.Equal(t, "reply to slow", r1.Content)
	assert.Equal(t, "m1-echo", r1.Model)
	assert.Equal(t, "stop", r1.FinishReason)
	assert.Equal(t, 42*time.Millisecond, r1.Latency)
	assert.InDelta(t, 0.002, r1.CostUSD, 1e-9)
	assert.Equal(t, infer.Usage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13}, r1.Usage)
	r2, err := canned.Chat(ctx, chat("fast", ""))
	require.NoError(t, err)
	assert.Equal(t, "reply to fast", r2.Content)
	r3, err := canned.Chat(ctx, chat("long", infer.PurposeTriage))
	require.NoError(t, err)
	assert.Equal(t, "length", r3.FinishReason)
	_, err = canned.Chat(ctx, chat("timeout", infer.PurposeTriage))
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "a recorded timeout replays as one: %v", err)
	_, err = canned.Chat(ctx, chat("500", infer.PurposeDraft))
	var ie *infer.Error
	require.ErrorAs(t, err, &ie)
	assert.Equal(t, 500, ie.Status)
	d, err := canned.Decide(ctx, infer.GateRequest{Questions: gq})
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{infer.QuestionContradiction: 0.7, infer.QuestionMaterialChange: 0.25}, d.Scores)
	assert.Equal(t, 300*time.Millisecond, d.Latency)
	assert.Equal(t, "g1", d.Model)
	// The cancelled fix waits until the replay cancels it too.
	cctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	_, err = canned.Chat(cctx, chat("cancel", infer.PurposeFix))
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestGateCostPresenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	mr, err := NewModelRecorder(dir, time.Now())
	require.NoError(t, err)
	gate := mr.WrapGate(&fakeGate{})
	for _, situation := range []string{"zero", "missing"} {
		_, err = gate.Decide(context.Background(), infer.GateRequest{Situation: situation})
		require.NoError(t, err)
	}
	require.NoError(t, mr.Close())
	canned, err := infer.NewCanned("rec", config.Provider{Kind: config.KindCanned, Path: filepath.Join(dir, CannedFile)}, func(string) string { return "" })
	require.NoError(t, err)
	for _, reported := range []bool{true, false} {
		d, err := canned.Decide(context.Background(), infer.GateRequest{})
		require.NoError(t, err)
		assert.Zero(t, d.CostUSD)
		assert.Equal(t, reported, d.CostReported)
	}
}

// A call still running when recording closes keeps its index with a long
// delay.
func TestModelRecorderUnfinished(t *testing.T) {
	dir := t.TempDir()
	mr, err := NewModelRecorder(dir, time.Now())
	require.NoError(t, err)
	fake := &fakeProvider{release: make(chan struct{})}
	p := mr.WrapProvider(fake)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Chat(context.Background(), chat("slow", ""))
	}()
	require.Eventually(t, func() bool { return mr.Calls() == 1 }, 2*time.Second, time.Millisecond)
	require.NoError(t, mr.Close())
	src, err := os.ReadFile(filepath.Join(dir, CannedFile))
	require.NoError(t, err)
	f, err := infer.ParseCanned(src)
	require.NoError(t, err)
	require.Len(t, f.Check, 1)
	assert.Equal(t, cancelledDelay, f.Check[0].DelayMS)
	close(fake.release)
	<-done
}

// loadRequests reads requests.jsonl.
func loadRequests(dir string) ([]RequestRecord, error) {
	var out []RequestRecord
	err := readJSONL(filepath.Join(dir, RequestsFile), func(b []byte) error {
		var r RequestRecord
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}
