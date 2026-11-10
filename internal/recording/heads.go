// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jwriteclub/spec/internal/infer"
)

// HeadChecker watches a replay's model calls against the recording's: it
// counts the calls of each purpose in the order they start (the order the
// canned provider serves them) and reports, through warn, a call whose
// request begins differently from the recorded call of the same place,
// and a call the recording does not have. The replay goes on either way:
// the canned reply is served (or its "no entry" error surfaces) as usual.
// It is safe for concurrent use.
type HeadChecker struct {
	mu   sync.Mutex
	want map[string][]string
	seen map[string]int
	warn func(string)
}

// NewHeadChecker checks against the recorded requests.
func NewHeadChecker(reqs []RequestRecord, warn func(string)) *HeadChecker {
	h := &HeadChecker{want: map[string][]string{}, seen: map[string]int{}, warn: warn}
	for _, r := range reqs {
		hs := h.want[r.Purpose]
		for len(hs) <= r.Index {
			hs = append(hs, "")
		}
		hs[r.Index] = r.Head
		h.want[r.Purpose] = hs
	}
	return h
}

// check counts a call of purpose whose request begins with head.
func (h *HeadChecker) check(purpose, head string) {
	h.mu.Lock()
	k := h.seen[purpose]
	h.seen[purpose]++
	want := h.want[purpose]
	h.mu.Unlock()
	switch {
	case k >= len(want):
		h.warn(fmt.Sprintf("model call %s #%d is not in the recording (it has %d); the request begins:\n%s", purpose, k+1, len(want), head))
	case want[k] != "" && want[k] != head:
		h.warn(fmt.Sprintf("model call %s #%d differs from the recording's; recorded:\n%s\nnow:\n%s", purpose, k+1, want[k], head))
	}
}

// Unused lists the recorded calls the replay never made, as "purpose: n
// of m" (empty when every one was made).
func (h *HeadChecker) Unused() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for purpose, want := range h.want {
		if n := h.seen[purpose]; n < len(want) {
			out = append(out, fmt.Sprintf("%s: %d of %d recorded model calls made", purpose, n, len(want)))
		}
	}
	sort.Strings(out)
	return out
}

// WrapProvider returns p with its calls checked (Workflow.WrapProvider).
func (h *HeadChecker) WrapProvider(p infer.Provider) infer.Provider {
	return &headProvider{inner: p, h: h}
}

// WrapGate returns g with its decisions checked (Workflow.WrapGate).
func (h *HeadChecker) WrapGate(g infer.Gate) infer.Gate {
	return &headGate{inner: g, h: h}
}

type headProvider struct {
	inner infer.Provider
	h     *HeadChecker
}

func (p *headProvider) Chat(ctx context.Context, req infer.Request) (*infer.Response, error) {
	purpose := req.Purpose
	if purpose == "" {
		purpose = infer.PurposeCheck
	}
	p.h.check(purpose, infer.RequestHead(req))
	return p.inner.Chat(ctx, req)
}

func (p *headProvider) Info() infer.Info                         { return p.inner.Info() }
func (p *headProvider) SetModel(id string)                       { p.inner.SetModel(id) }
func (p *headProvider) SetEffort(level string)                   { p.inner.SetEffort(level) }
func (p *headProvider) SetLimits(timeout time.Duration, att int) { p.inner.SetLimits(timeout, att) }
func (p *headProvider) RunawayExtra() map[string]any             { return p.inner.RunawayExtra() }

type headGate struct {
	inner infer.Gate
	h     *HeadChecker
}

func (g *headGate) Decide(ctx context.Context, req infer.GateRequest) (*infer.Decision, error) {
	g.h.check(gatePurpose(req), infer.GateHead(req))
	return g.inner.Decide(ctx, req)
}

func (g *headGate) Info() infer.Info                         { return g.inner.Info() }
func (g *headGate) SetLimits(timeout time.Duration, att int) { g.inner.SetLimits(timeout, att) }
