// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/config"
)

// DefaultDecisionsTimeout bounds one Decisions API attempt when the provider sets
// no timeout. A decision takes about 0.3 s.
const DefaultDecisionsTimeout = 60 * time.Second

// Decisions is a gate (kind decisions): OpenRouter's Decisions API (alpha, POST
// BaseURL/decisions), which answers each question with a "noul"
// probability. It shares the chat client's transport rules: the key is
// sent as a bearer token and redacted from every error, and transient
// failures (408, 409, 425, 429, 5xx, network errors, timeouts) are retried
// with jittered exponential backoff.
//
// The request body is
//
//	{"model": M, "state": {"policy": P, "situation": S},
//	 "questions": {"<id>": {"type": "noul", "instructions": "..."}, ...}}
//
// and the reply
//
//	{"model": "<served id>", "provider": "TypeSafe", "id": "...",
//	 "answers": {"<id>": {"type": "noul", "noul": 0.93}, ...},
//	 "usage": {"input_tokens": N, "output_tokens": N, "cost": USD}}
type Decisions struct {
	// c carries the transport: base URL, key, timeouts, retries and
	// redaction. Its chat fields are unused.
	c *Client
}

// NewDecisions builds the decisions gate called name, reading base_url's ${VAR}
// references and the API key from getenv. A missing variable is a
// *config.EnvError.
func NewDecisions(name string, p config.Provider, getenv func(string) string) (*Decisions, error) {
	base, err := p.DecisionsBaseURL(name, getenv)
	if err != nil {
		return nil, err
	}
	key, err := p.Key(name, getenv)
	if err != nil {
		return nil, err
	}
	c := &Client{Name: name, BaseURL: base, Model: p.Model, Timeout: time.Duration(p.Timeout) * time.Second}
	if c.Timeout <= 0 {
		c.Timeout = DefaultDecisionsTimeout
	}
	c.SetAPIKey(key)
	return &Decisions{c: c}, nil
}

// SetHTTP replaces the HTTP client (tests, proxies).
func (g *Decisions) SetHTTP(d Doer) { g.c.HTTP = d }

// Info describes the gate (Gate).
func (g *Decisions) Info() Info {
	transport := g.c.BaseURL
	if u, err := url.Parse(g.c.BaseURL); err == nil && u.Host != "" {
		transport = u.Scheme + "://" + u.Host
	}
	return Info{Name: g.c.Name, Kind: config.KindDecisions, Model: g.c.Model, Transport: transport}
}

// SetLimits bounds each attempt and the number of attempts (Gate).
func (g *Decisions) SetLimits(timeout time.Duration, attempts int) {
	g.c.Timeout = timeout
	g.c.Attempts = attempts
}

// decisionsQuestion is one question of a request body.
type decisionsQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

// decisionsBody is a request body.
type decisionsBody struct {
	Model string `json:"model"`
	State struct {
		Policy    string `json:"policy"`
		Situation string `json:"situation"`
	} `json:"state"`
	Questions map[string]decisionsQuestion `json:"questions"`
}

// decisionsReply is the part of a reply Decide reads.
type decisionsReply struct {
	Model    string `json:"model"`
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Answers  map[string]struct {
		Type string   `json:"type"`
		Noul *float64 `json:"noul"`
	} `json:"answers"`
	Usage *struct {
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
		Cost         float64 `json:"cost"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// Decide sends one decision request (Gate).
func (g *Decisions) Decide(ctx context.Context, req GateRequest) (*Decision, error) {
	if len(req.Questions) == 0 {
		return nil, errors.New("infer: a gate request needs at least one question")
	}
	model := req.Model
	if model == "" {
		model = g.c.Model
	}
	var body decisionsBody
	body.Model = model
	body.State.Policy = req.Policy
	body.State.Situation = req.Situation
	body.Questions = make(map[string]decisionsQuestion, len(req.Questions))
	for _, q := range req.Questions {
		if q.ID == "" {
			return nil, errors.New("infer: a gate question needs an id")
		}
		if _, dup := body.Questions[q.ID]; dup {
			return nil, fmt.Errorf("infer: gate question %q is asked twice", q.ID)
		}
		body.Questions[q.ID] = decisionsQuestion{Type: "noul", Instructions: q.Instructions}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("infer: encoding the gate request: %w", err)
	}

	c := g.c
	start := time.Now()
	attempts := c.attempts()
	var last *Error
	for n := 1; n <= attempts; n++ {
		raw, wait, err := c.postJSON(ctx, "/decisions", payload)
		if err == nil {
			d, derr := g.decode(raw, req.Questions)
			if derr == nil {
				d.RequestedModel, d.Latency, d.Attempts = model, time.Since(start), n
				return d, nil
			}
			err = derr
		}
		var e *Error
		if !errors.As(err, &e) {
			return nil, err // the caller's context ended
		}
		e.Provider, e.Model, e.Attempts = c.Name, model, n
		last = e
		if !e.Retryable || n == attempts {
			break
		}
		d := c.backoff(n, wait)
		lg.Debugf("%s: gate attempt %d of %d failed (%s); retrying in %s", c.Name, n, attempts, e.short(), d)
		if err := c.wait(ctx, d); err != nil {
			return nil, err
		}
	}
	return nil, last
}

// decode reads a reply: every question asked must have a probability.
func (g *Decisions) decode(raw []byte, qs []Question) (*Decision, error) {
	c := g.c
	var r decisionsReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, c.newError(http.StatusOK, c.bodySnippet(raw), fmt.Errorf("the reply is not JSON: %w", err), false)
	}
	if isError(r.Error) {
		return nil, c.newError(http.StatusOK, c.bodyMessage(raw), errors.New("the reply carried an error"), false)
	}
	d := &Decision{Provider: c.Name, Model: r.Model, Upstream: r.Provider, ID: r.ID, Scores: make(map[string]float64, len(qs))}
	var missing []string
	for _, q := range qs {
		a, ok := r.Answers[q.ID]
		if !ok || a.Noul == nil || *a.Noul < 0 || *a.Noul > 1 {
			missing = append(missing, q.ID)
			continue
		}
		d.Scores[q.ID] = *a.Noul
	}
	if len(missing) > 0 {
		return nil, c.newError(http.StatusOK, c.bodySnippet(raw),
			fmt.Errorf("the reply has no probability in [0, 1] for %s (asked: %s)", strings.Join(missing, ", "), strings.Join(questionIDs(qs), ", ")), false)
	}
	if r.Usage != nil {
		d.Usage = Usage{PromptTokens: r.Usage.InputTokens, CompletionTokens: r.Usage.OutputTokens}.filled()
		d.CostUSD = r.Usage.Cost
	}
	return d, nil
}

// postJSON sends one non-streamed POST of payload to BaseURL + path and
// returns the body of a 200 reply, or an *Error (with how long the provider
// asked us to wait), or the context's error when ctx itself ended.
func (c *Client) postJSON(ctx context.Context, path string, payload []byte) ([]byte, time.Duration, error) {
	limit := c.timeout(false)
	actx, cancel := context.WithTimeoutCause(ctx, limit,
		fmt.Errorf("the attempt took longer than %s (timeout): %w", limit, context.DeadlineExceeded))
	defer cancel()
	hreq, err := http.NewRequestWithContext(actx, http.MethodPost, c.endpoint(path), bytes.NewReader(payload))
	if err != nil {
		return nil, 0, c.newError(0, "", err, false)
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	if k := c.apiKey(); k != "" {
		hreq.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := c.doer().Do(hreq)
	if err != nil {
		return nil, 0, c.transportError(ctx, actx, 0, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, c.transportError(ctx, actx, resp.StatusCode, fmt.Errorf("reading the response: %w", err))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, retryAfter(resp.Header.Get("Retry-After")), c.newError(resp.StatusCode, c.bodyMessage(raw), nil, retryableStatus(resp.StatusCode))
	}
	return raw, 0, nil
}
