// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/jwriteclub/spec/internal/infer"
)

// cancelledDelay is added to the delay of a call the user cancelled, and
// is the delay of a call still running when recording ended: on replay the
// replayed ^C (or the end) cancels it at the same point, long before the
// delay runs out.
const cancelledDelay = 600000

// ModelRecorder records every model call made through the providers and
// gates it wraps: each reply (or failure) and its real time go into
// canned.toml, in the order the calls started within each purpose (the
// order the canned provider serves them), and the whole request into
// requests.jsonl. It is safe for concurrent use.
type ModelRecorder struct {
	dir   string
	start time.Time

	mu    sync.Mutex
	keys  func() int
	slots map[string][]*slot
	calls int
	reqs  *os.File
	err   error
}

// slot is one call's canned entry; done is false while the call runs.
type slot struct {
	entry infer.CannedEntry
	done  bool
}

// NewModelRecorder records into dir (canned.toml and requests.jsonl);
// times are taken relative to start (time.Since, so monotonic).
func NewModelRecorder(dir string, start time.Time) (*ModelRecorder, error) {
	f, err := os.OpenFile(filepath.Join(dir, RequestsFile), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	r := &ModelRecorder{dir: dir, start: start, slots: map[string][]*slot{}, reqs: f}
	if err := r.writeCanned(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return r, nil
}

// SetKeys sets where the key count of each call comes from (the session
// recorder's).
func (r *ModelRecorder) SetKeys(fn func() int) {
	r.mu.Lock()
	r.keys = fn
	r.mu.Unlock()
}

// Calls is the number of calls recorded so far (finished or not).
func (r *ModelRecorder) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// Err is the first error writing the recording.
func (r *ModelRecorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Close writes canned.toml a last time (calls still running get the long
// delay) and closes requests.jsonl.
func (r *ModelRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.writeCannedLocked(); err != nil && r.err == nil {
		r.err = err
	}
	if r.reqs != nil {
		if err := r.reqs.Close(); err != nil && r.err == nil {
			r.err = err
		}
		r.reqs = nil
	}
	return r.err
}

// WrapProvider returns p recording its calls (Workflow.WrapProvider).
func (r *ModelRecorder) WrapProvider(p infer.Provider) infer.Provider {
	return &recProvider{inner: p, r: r}
}

// WrapGate returns g recording its decisions (Workflow.WrapGate).
func (r *ModelRecorder) WrapGate(g infer.Gate) infer.Gate {
	return &recGate{inner: g, r: r}
}

// call is one call in progress.
type call struct {
	purpose string
	index   int
	t       time.Duration
	keys    int
	slot    *slot
}

// reserve takes the next slot of purpose at the start of a call.
func (r *ModelRecorder) reserve(purpose string) call {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &slot{}
	r.slots[purpose] = append(r.slots[purpose], s)
	r.calls++
	c := call{purpose: purpose, index: len(r.slots[purpose]) - 1, t: time.Since(r.start), slot: s}
	if r.keys != nil {
		c.keys = r.keys()
	}
	return c
}

// finish fills c's slot and writes canned.toml and the request's line.
func (r *ModelRecorder) finish(c call, e infer.CannedEntry, rr RequestRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c.slot.entry, c.slot.done = e, true
	rr.Purpose, rr.Index, rr.T, rr.Keys = c.purpose, c.index, c.t.Milliseconds(), c.keys
	if err := r.writeCannedLocked(); err != nil && r.err == nil {
		r.err = err
	}
	if r.reqs == nil {
		return
	}
	b, err := json.Marshal(rr)
	if err == nil {
		_, err = r.reqs.Write(append(b, '\n'))
	}
	if err != nil && r.err == nil {
		r.err = err
	}
}

// classify fills the failure fields of e and rr from err after elapsed.
func classify(err error, elapsed time.Duration, e *infer.CannedEntry, rr *RequestRecord) {
	ms := int(elapsed.Milliseconds())
	e.DelayMS = ms
	rr.Error = err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		e.Error, rr.Outcome = infer.CannedTimeout, OutcomeTimeout
	case errors.Is(err, context.Canceled):
		e.DelayMS, rr.Outcome = ms+cancelledDelay, OutcomeCancelled
	default:
		e.Error, rr.Outcome = infer.CannedHTTP500, OutcomeError
	}
}

func (r *ModelRecorder) writeCanned() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeCannedLocked()
}

// writeCannedLocked rewrites canned.toml from the slots.
func (r *ModelRecorder) writeCannedLocked() error {
	var f infer.CannedFile
	for _, purpose := range infer.CannedPurposes {
		var es []infer.CannedEntry
		for _, s := range r.slots[purpose] {
			if s.done {
				es = append(es, s.entry)
			} else {
				es = append(es, infer.CannedEntry{DelayMS: cancelledDelay})
			}
		}
		switch purpose {
		case infer.PurposeCheck:
			f.Check = es
		case infer.PurposeTriage:
			f.Triage = es
		case infer.PurposeGate:
			f.Gate = es
		case infer.PurposeDraft:
			f.Draft = es
		case infer.PurposeFix:
			f.Fix = es
		case infer.PurposeRevise:
			f.Revise = es
		case infer.PurposeSpellGate:
			f.SpellGate = es
		case infer.PurposeSpell:
			f.Spell = es
		}
	}
	b, err := toml.Marshal(f)
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(r.dir, CannedFile), b)
}

// recProvider records the chat calls of inner.
type recProvider struct {
	inner infer.Provider
	r     *ModelRecorder
}

func (p *recProvider) Chat(ctx context.Context, req infer.Request) (*infer.Response, error) {
	purpose := req.Purpose
	if purpose == "" {
		purpose = infer.PurposeCheck
	}
	c := p.r.reserve(purpose)
	began := time.Now()
	resp, err := p.inner.Chat(ctx, req)
	elapsed := time.Since(began)
	info := p.inner.Info()
	raw, _ := json.Marshal(req)
	rr := RequestRecord{Provider: info.Name, Model: requested(req, info), Head: infer.RequestHead(req),
		Request: raw, ElapsedMS: elapsed.Milliseconds()}
	var e infer.CannedEntry
	if err != nil {
		classify(err, elapsed, &e, &rr)
	} else {
		rr.Outcome = OutcomeOK
		e = infer.CannedEntry{
			Reply: resp.Content, Model: resp.Model, DelayMS: int(elapsed.Milliseconds()),
			LatencyMS: int(resp.Latency.Milliseconds()), CostUSD: resp.CostUSD,
			PromptTokens: resp.Usage.PromptTokens, CompletionTokens: resp.Usage.CompletionTokens,
		}
		if resp.FinishReason != "stop" {
			e.FinishReason = resp.FinishReason
		}
		if resp.RequestedModel != "" {
			rr.Model = resp.RequestedModel
		}
	}
	p.r.finish(c, e, rr)
	return resp, err
}

// requested is the model a request goes to, as far as the wrapper can
// tell (the reply says exactly).
func requested(req infer.Request, info infer.Info) string {
	switch {
	case req.Model != "":
		return req.Model
	case req.Triage && info.TriageModel != "":
		return info.TriageModel
	}
	return info.Model
}

func (p *recProvider) Info() infer.Info                         { return p.inner.Info() }
func (p *recProvider) SetModel(id string)                       { p.inner.SetModel(id) }
func (p *recProvider) SetEffort(level string)                   { p.inner.SetEffort(level) }
func (p *recProvider) SetLimits(timeout time.Duration, att int) { p.inner.SetLimits(timeout, att) }
func (p *recProvider) RunawayExtra() map[string]any             { return p.inner.RunawayExtra() }

// recGate records the decisions of inner.
type recGate struct {
	inner infer.Gate
	r     *ModelRecorder
}

func (g *recGate) Decide(ctx context.Context, req infer.GateRequest) (*infer.Decision, error) {
	c := g.r.reserve(gatePurpose(req))
	began := time.Now()
	d, err := g.inner.Decide(ctx, req)
	elapsed := time.Since(began)
	info := g.inner.Info()
	model := info.Model
	if req.Model != "" {
		model = req.Model
	}
	raw, _ := json.Marshal(req)
	rr := RequestRecord{Provider: info.Name, Model: model, Head: infer.GateHead(req),
		Request: raw, ElapsedMS: elapsed.Milliseconds()}
	var e infer.CannedEntry
	if err != nil {
		classify(err, elapsed, &e, &rr)
	} else {
		rr.Outcome = OutcomeOK
		scores := make(map[string]float64, len(d.Scores))
		for k, v := range d.Scores {
			scores[k] = v
		}
		e = infer.CannedEntry{
			Scores: scores, Model: d.Model, DelayMS: int(elapsed.Milliseconds()),
			LatencyMS: int(d.Latency.Milliseconds()), CostUSD: d.CostUSD, CostReported: d.CostReported,
			PromptTokens: d.Usage.PromptTokens, CompletionTokens: d.Usage.CompletionTokens,
		}
	}
	g.r.finish(c, e, rr)
	return d, err
}

func (g *recGate) Info() infer.Info                         { return g.inner.Info() }
func (g *recGate) SetLimits(timeout time.Duration, att int) { g.inner.SetLimits(timeout, att) }

// gatePurpose is the canned table a decision is served from.
func gatePurpose(req infer.GateRequest) string {
	if req.Purpose != "" {
		return req.Purpose
	}
	return infer.PurposeGate
}
