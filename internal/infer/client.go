// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package infer is the inference provider layer: one client for any
// OpenAI-compatible chat-completions endpoint (Cloudflare Workers AI,
// DeepSeek, OpenRouter, OpenAI, Gemini's and Anthropic's compatibility
// endpoints, local servers), on net/http with no SDK.
//
// A Client sends one Request and returns a Response with the reply, the
// token usage, the latency and the model id the provider echoed. Replies are
// streamed (server-sent events) unless the provider opts out, so a long
// reasoning reply keeps its connection alive past a gateway's cutoff for a
// silent request; a streamed attempt that goes quiet for IdleTimeout is
// abandoned. Transient failures (408, 409, 425, 429, 5xx, network errors,
// streams cut short and timeouts) are retried with jittered exponential
// backoff. Every error the package returns has the API key redacted.
package infer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/tenta-browser/polychromatic"

	"github.com/jwriteclub/spec/internal/config"
)

var lg *logrus.Entry = polychromatic.GetLogger("infer")

// Defaults for a new Client.
const (
	// DefaultTimeout bounds an attempt that is not streamed.
	DefaultTimeout = 120 * time.Second
	// DefaultStreamTimeout bounds a streamed attempt: a reasoning model
	// that keeps sending may think for many minutes.
	DefaultStreamTimeout = 900 * time.Second
	// DefaultIdleTimeout abandons a streamed attempt that has received no
	// bytes for this long.
	DefaultIdleTimeout = 90 * time.Second
	DefaultAttempts    = 3
	DefaultBaseDelay   = time.Second
	DefaultMaxDelay    = 30 * time.Second
)

// maxResponseBytes bounds how much of a response body is read.
const maxResponseBytes = 16 << 20

// Doer sends an HTTP request; *http.Client implements it.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// System returns a system message.
func System(content string) Message { return Message{Role: "system", Content: content} }

// User returns a user message.
func User(content string) Message { return Message{Role: "user", Content: content} }

// Request is one chat-completions call.
type Request struct {
	// Model overrides the client's model; empty uses TriageModel for a
	// triage request (when set) and Model otherwise.
	Model    string
	Messages []Message
	// Temperature is omitted when nil (the provider's default).
	Temperature *float64
	// MaxTokens caps the reply; 0 omits the field.
	MaxTokens int
	// JSON asks for a JSON object. The client sends response_format
	// json_object only when the provider's json_mode is on, so the prompt
	// must ask for JSON too; ChatJSON parses the reply leniently either way.
	JSON bool
	// Triage selects the triage model and merges the provider's
	// triage_extra over its extra.
	Triage bool
	// Extra is merged into the body last, over the provider's extra.
	Extra map[string]any
	// Purpose says what the request is for (PurposeCheck, PurposeTriage,
	// ...). Only the canned provider reads it; "" counts as a check.
	Purpose string
}

// Usage is the token count the provider reported. Zero when it reported
// none.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Add returns the sum of u and o.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		PromptTokens:     u.PromptTokens + o.PromptTokens,
		CompletionTokens: u.CompletionTokens + o.CompletionTokens,
		TotalTokens:      u.TotalTokens + o.TotalTokens,
	}
}

// Response is a successful reply.
type Response struct {
	// Content is the text of the first choice.
	Content string
	// Provider is the client's name.
	Provider string
	// RequestedModel is the model id sent.
	RequestedModel string
	// Model is the model id the provider echoed ("" when it echoed none).
	Model string
	// Reasoning is the first choice's reasoning text, for providers that
	// return it beside the content (reasoning_content or reasoning).
	Reasoning string
	// FinishReason is the first choice's finish_reason ("stop", "length").
	// A reasoning model cut off by MaxTokens has "length", an empty Content
	// and a non-empty Reasoning.
	FinishReason string
	Usage        Usage
	// Latency is the wall time of the whole call, retries included.
	Latency time.Duration
	// Attempts is how many HTTP requests were sent.
	Attempts int
	// JSONMode is true when response_format json_object was sent.
	JSONMode bool
	// Streamed is true when the reply arrived as an event stream.
	Streamed bool
	// CostUSD is the cost the provider reported, in US dollars (0 when it
	// reported none: most OpenAI-compatible endpoints do not; OpenRouter
	// does, as usage.cost).
	CostUSD float64
}

// apiKey holds the key behind a pointer, so printing a Client with %v or
// %+v shows an address, never the key.
type apiKey struct{ value string }

// Client talks to one OpenAI-compatible provider. Configure the exported
// fields before the first call; a Client is safe for concurrent use after
// that.
type Client struct {
	// Name labels errors and responses (the provider's config name).
	Name string
	// BaseURL is the API root; requests go to BaseURL + "/chat/completions".
	BaseURL string
	// Model and TriageModel are the default model ids.
	Model       string
	TriageModel string
	// JSONMode: the provider accepts response_format json_object.
	JSONMode bool
	// Extra and TriageExtra are merged into request bodies (see Request).
	Extra       map[string]any
	TriageExtra map[string]any
	// MaxTokensParam is the body field for Request.MaxTokens; "" means
	// "max_tokens".
	MaxTokensParam string
	// Effort is the provider's effort setting, sent as reasoning_effort
	// under the extras (an extra that sets reasoning_effort wins). ""
	// sends none.
	Effort string
	// EffortOverride (--effort) is sent as reasoning_effort over
	// everything, extras and the runaway guard's included.
	EffortOverride string
	// Stream asks for the reply as server-sent events (stream: true, with
	// stream_options include_usage). New sets it unless the provider's
	// config says stream = false.
	Stream bool
	// Timeout bounds each attempt; 0 means DefaultStreamTimeout for a
	// streamed attempt and DefaultTimeout otherwise.
	Timeout time.Duration
	// IdleTimeout abandons a streamed attempt that receives no bytes for
	// this long (it is retried like a network error); 0 means
	// DefaultIdleTimeout. It does not apply without streaming.
	IdleTimeout time.Duration
	// Attempts is the total number of tries, at least 1.
	Attempts int
	// BaseDelay is the first retry's backoff before jitter; each further
	// retry doubles it, up to MaxDelay. A Retry-After header, capped at
	// MaxDelay, wins when longer.
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// HTTP sends the requests; nil uses a plain *http.Client.
	HTTP Doer

	key *apiKey
	// sleep waits between attempts; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
}

// New builds a client for the provider called name, reading base_url's
// ${VAR} references and the API key from getenv. A missing variable is a
// *config.EnvError.
func New(name string, p config.Provider, getenv func(string) string) (*Client, error) {
	base, err := p.ResolveBaseURL(name, getenv)
	if err != nil {
		return nil, err
	}
	key, err := p.Key(name, getenv)
	if err != nil {
		return nil, err
	}
	c := &Client{
		Name:           name,
		BaseURL:        base,
		Model:          p.Model,
		TriageModel:    p.TriageModel,
		JSONMode:       p.JSONMode,
		Extra:          p.Extra,
		TriageExtra:    p.TriageExtra,
		MaxTokensParam: p.MaxTokensParam,
		Effort:         p.Effort,
		Stream:         p.Streams(),
		Timeout:        time.Duration(p.Timeout) * time.Second,
		IdleTimeout:    time.Duration(p.IdleTimeout) * time.Second,
	}
	c.SetAPIKey(key)
	return c, nil
}

// SetAPIKey sets the bearer token ("" sends no Authorization header).
func (c *Client) SetAPIKey(key string) {
	c.key = &apiKey{value: key}
}

func (c *Client) apiKey() string {
	if c.key == nil {
		return ""
	}
	return c.key.value
}

// ModelFor returns the model id req would be sent with.
func (c *Client) ModelFor(req Request) string {
	return modelFor(req, c.Model, c.TriageModel)
}

// Info describes the client (Provider).
func (c *Client) Info() Info {
	effort := c.EffortOverride
	if effort == "" {
		effort = c.Effort
	}
	transport := c.BaseURL
	if u, err := url.Parse(c.BaseURL); err == nil && u.Host != "" {
		transport = u.Scheme + "://" + u.Host
	}
	return Info{Name: c.Name, Kind: config.KindOpenAI, Model: c.Model, TriageModel: c.TriageModel, Effort: effort, Transport: transport}
}

// SetModel replaces Model and TriageModel (Provider).
func (c *Client) SetModel(id string) {
	c.Model = id
	c.TriageModel = id
}

// SetEffort sets EffortOverride (Provider).
func (c *Client) SetEffort(level string) { c.EffortOverride = level }

// SetLimits sets Timeout and Attempts (Provider).
func (c *Client) SetLimits(timeout time.Duration, attempts int) {
	c.Timeout = timeout
	c.Attempts = attempts
}

// RunawayExtra returns TriageExtra (Provider).
func (c *Client) RunawayExtra() map[string]any { return c.TriageExtra }

// Body returns the JSON body req would be sent with (for tests and
// debugging; it holds no key).
func (c *Client) Body(req Request) ([]byte, error) {
	b, _ := c.body(req, c.streamMode())
	return json.Marshal(b)
}

// streamMode is how an attempt asks for its reply.
type streamMode int

const (
	// streamOff sends no stream field: one JSON reply.
	streamOff streamMode = iota
	// streamPlain sends stream: true without stream_options (a provider
	// that rejected stream_options).
	streamPlain
	// streamFull sends stream: true and stream_options include_usage.
	streamFull
)

func (m streamMode) String() string {
	switch m {
	case streamFull:
		return "streamed with usage"
	case streamPlain:
		return "streamed without stream_options"
	}
	return "not streamed"
}

// streamMode is the mode a call starts in.
func (c *Client) streamMode() streamMode {
	if c.Stream {
		return streamFull
	}
	return streamOff
}

// body builds the request body: the standard fields and the provider's
// effort, then the provider's extra, its triage_extra for a triage request,
// and the request's extra, each merged over what came before; then an
// --effort override and the stream fields (the config may not set them).
func (c *Client) body(req Request, mode streamMode) (map[string]any, bool) {
	b := map[string]any{
		"model":    c.ModelFor(req),
		"messages": req.Messages,
	}
	if req.Temperature != nil {
		b["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		field := c.MaxTokensParam
		if field == "" {
			field = "max_tokens"
		}
		b[field] = req.MaxTokens
	}
	if req.JSON && c.JSONMode {
		b["response_format"] = map[string]any{"type": "json_object"}
	}
	if c.Effort != "" {
		b["reasoning_effort"] = c.Effort
	}
	merge(b, c.Extra)
	if req.Triage {
		merge(b, c.TriageExtra)
	}
	merge(b, req.Extra)
	if c.EffortOverride != "" {
		b["reasoning_effort"] = c.EffortOverride
	}
	switch mode {
	case streamFull:
		b["stream"] = true
		b["stream_options"] = map[string]any{"include_usage": true}
	case streamPlain:
		b["stream"] = true
	}
	_, jsonMode := b["response_format"]
	return b, jsonMode
}

// merge copies src into dst; nested tables merge key by key.
func merge(dst, src map[string]any) {
	for k, v := range src {
		sv, sok := v.(map[string]any)
		dv, dok := dst[k].(map[string]any)
		if sok && dok {
			cp := make(map[string]any, len(dv))
			merge(cp, dv)
			merge(cp, sv)
			dst[k] = cp
			continue
		}
		if sok {
			cp := make(map[string]any, len(sv))
			merge(cp, sv)
			dst[k] = cp
			continue
		}
		dst[k] = v
	}
}

func (c *Client) attempts() int {
	if c.Attempts < 1 {
		return DefaultAttempts
	}
	return c.Attempts
}

// timeout bounds one attempt.
func (c *Client) timeout(streamed bool) time.Duration {
	switch {
	case c.Timeout > 0:
		return c.Timeout
	case streamed:
		return DefaultStreamTimeout
	}
	return DefaultTimeout
}

func (c *Client) idleTimeout() time.Duration {
	if c.IdleTimeout <= 0 {
		return DefaultIdleTimeout
	}
	return c.IdleTimeout
}

func (c *Client) doer() Doer {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

func (c *Client) endpoint(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + path
}

// backoff returns the wait before retry number n (1 for the first retry):
// BaseDelay doubled n-1 times, jittered to between half and one and a half
// times that, capped at MaxDelay; retryAfter wins when longer.
func (c *Client) backoff(n int, retryAfter time.Duration) time.Duration {
	base := c.BaseDelay
	if base <= 0 {
		base = DefaultBaseDelay
	}
	maxDelay := c.MaxDelay
	if maxDelay <= 0 {
		maxDelay = DefaultMaxDelay
	}
	d := base << min(n-1, 20)
	d = time.Duration(float64(d) * (0.5 + rand.Float64()))
	if retryAfter > d {
		d = retryAfter
	}
	return min(d, maxDelay)
}

func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Chat sends req and returns the reply. A provider or network failure is an
// *Error; the context's own cancellation is returned as is.
//
// A streamed call that the provider rejects with HTTP 400 naming stream or
// stream_options is sent again at once without stream_options, then without
// streaming; that does not use up a retry.
func (c *Client) Chat(ctx context.Context, req Request) (*Response, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("infer: a request needs at least one message")
	}
	mode := c.streamMode()
	payload, jsonMode, err := c.payload(req, mode)
	if err != nil {
		return nil, err
	}
	model := c.ModelFor(req)
	start := time.Now()
	attempts := c.attempts()
	sent := 0
	var last *Error
	for n := 1; n <= attempts; n++ {
		sent++
		r, retryAfter, err := c.post(ctx, payload, mode != streamOff)
		if err == nil {
			return &Response{
				Content:        r.Content,
				Provider:       c.Name,
				RequestedModel: model,
				Model:          r.Model,
				Reasoning:      r.Reasoning,
				FinishReason:   r.FinishReason,
				Usage:          r.Usage,
				CostUSD:        r.CostUSD,
				Latency:        time.Since(start),
				Attempts:       sent,
				JSONMode:       jsonMode,
				Streamed:       r.Streamed,
			}, nil
		}
		var e *Error
		if !errors.As(err, &e) {
			return nil, err // the caller's context ended
		}
		e.Provider, e.Model, e.Attempts = c.Name, model, sent
		last = e
		if next, ok := streamFallback(e, mode); ok {
			lg.Warnf("%s: the provider rejected the streamed request (%s); sending it %s (stream = false in the provider's config skips this)", c.Name, e.short(), next)
			mode = next
			if payload, jsonMode, err = c.payload(req, mode); err != nil {
				return nil, err
			}
			n-- // the same attempt again, not a retry
			continue
		}
		if !e.Retryable || n == attempts {
			break
		}
		d := c.backoff(n, retryAfter)
		lg.Debugf("%s: attempt %d of %d failed (%s); retrying in %s", c.Name, n, attempts, e.short(), d)
		if err := c.wait(ctx, d); err != nil {
			return nil, err
		}
	}
	return nil, last
}

// payload encodes req's body for mode.
func (c *Client) payload(req Request, mode streamMode) ([]byte, bool, error) {
	body, jsonMode := c.body(req, mode)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, false, fmt.Errorf("infer: encoding the request: %w", err)
	}
	return raw, jsonMode, nil
}

// streamFallback returns the mode to try after e: a streamed request that
// the provider rejected with a 400 (or 422) naming stream drops
// stream_options first, then streaming.
func streamFallback(e *Error, mode streamMode) (streamMode, bool) {
	if mode == streamOff || (e.Status != http.StatusBadRequest && e.Status != http.StatusUnprocessableEntity) {
		return mode, false
	}
	if !strings.Contains(strings.ToLower(e.Message), "stream") {
		return mode, false
	}
	return mode - 1, true
}

// ErrIdleTimeout is the cause of a streamed attempt that received nothing
// for IdleTimeout.
var ErrIdleTimeout = errors.New("the provider sent nothing for longer than idle_timeout")

// idleWatch cancels a streamed attempt that has received no bytes for d.
type idleWatch struct {
	d time.Duration
	t *time.Timer
}

func watchIdle(d time.Duration, cancel context.CancelCauseFunc) *idleWatch {
	cause := fmt.Errorf("%w (%s)", ErrIdleTimeout, d)
	return &idleWatch{d: d, t: time.AfterFunc(d, func() { cancel(cause) })}
}

func (w *idleWatch) stop() { w.t.Stop() }

// reader wraps r so that every read that returns bytes restarts the clock.
func (w *idleWatch) reader(r io.Reader) io.Reader { return idleReader{r: r, w: w} }

type idleReader struct {
	r io.Reader
	w *idleWatch
}

func (r idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.w.t.Reset(r.w.d)
	}
	return n, err
}

// post sends one attempt. It returns the decoded reply, or an *Error (with
// how long the provider asked us to wait), or the context's error when ctx
// itself ended.
func (c *Client) post(ctx context.Context, payload []byte, streamed bool) (*reply, time.Duration, error) {
	limit := c.timeout(streamed)
	actx, cancel := context.WithTimeoutCause(ctx, limit,
		fmt.Errorf("the attempt took longer than %s (timeout): %w", limit, context.DeadlineExceeded))
	defer cancel()
	var idle *idleWatch
	if streamed {
		ictx, icancel := context.WithCancelCause(actx)
		defer icancel(nil)
		idle = watchIdle(c.idleTimeout(), icancel)
		defer idle.stop()
		actx = ictx
	}
	hreq, err := http.NewRequestWithContext(actx, http.MethodPost, c.endpoint("/chat/completions"), bytes.NewReader(payload))
	if err != nil {
		return nil, 0, c.newError(0, "", err, false)
	}
	hreq.Header.Set("Content-Type", "application/json")
	if streamed {
		hreq.Header.Set("Accept", "text/event-stream, application/json")
	} else {
		hreq.Header.Set("Accept", "application/json")
	}
	if k := c.apiKey(); k != "" {
		hreq.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := c.doer().Do(hreq)
	if err != nil {
		return nil, 0, c.transportError(ctx, actx, 0, err)
	}
	defer resp.Body.Close()
	var body io.Reader = resp.Body
	if idle != nil {
		body = idle.reader(body)
	}
	if resp.StatusCode != http.StatusOK || !streamed {
		raw, err := io.ReadAll(io.LimitReader(body, maxResponseBytes))
		if err != nil {
			return nil, 0, c.transportError(ctx, actx, resp.StatusCode, fmt.Errorf("reading the response: %w", err))
		}
		if resp.StatusCode != http.StatusOK {
			return nil, retryAfter(resp.Header.Get("Retry-After")), c.newError(resp.StatusCode, c.bodyMessage(raw), nil, retryableStatus(resp.StatusCode))
		}
		r, err := c.decode(resp.StatusCode, raw)
		return r, 0, err
	}

	// A streamed request may still get one JSON object back (a server that
	// ignores stream): tell by the first byte.
	br := bufio.NewReaderSize(body, 64<<10)
	first, err := firstByte(br)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, 0, c.newError(resp.StatusCode, "", errStreamCut, true)
		}
		return nil, 0, c.transportError(ctx, actx, resp.StatusCode, fmt.Errorf("reading the stream: %w", err))
	}
	if first == '{' {
		raw, err := io.ReadAll(io.LimitReader(br, maxResponseBytes))
		if err != nil {
			return nil, 0, c.transportError(ctx, actx, resp.StatusCode, fmt.Errorf("reading the response: %w", err))
		}
		lg.Debugf("%s: asked for a stream, got one JSON reply", c.Name)
		r, err := c.decode(resp.StatusCode, raw)
		return r, 0, err
	}
	r, err := readStream(br)
	if err != nil {
		return nil, 0, c.streamFailure(ctx, actx, resp.StatusCode, err)
	}
	return r, 0, nil
}

// firstByte returns the first byte of br that is not white space, leaving
// it unread.
func firstByte(br *bufio.Reader) (byte, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, err
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return b, br.UnreadByte()
	}
}

// decode reads a whole (not streamed) chat-completions reply.
func (c *Client) decode(status int, raw []byte) (*reply, error) {
	var w wireResponse
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, c.newError(status, c.bodySnippet(raw), errors.New("the response is not JSON"), false)
	}
	if len(w.Choices) == 0 {
		// OpenRouter reports some upstream failures as 200 with an error.
		return nil, c.newError(status, c.bodyMessage(raw), errNoChoices, retryableStatus(codeOf(w.Error)))
	}
	return w.reply(), nil
}

// transportError is a failure to send or read: the caller's own context
// ending is returned as is; an attempt that hit its timeout or went idle
// carries that as the cause. Both are retryable.
func (c *Client) transportError(ctx, actx context.Context, status int, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if actx.Err() != nil {
		err = context.Cause(actx)
	}
	return c.newError(status, "", err, true)
}

// streamFailure classifies a stream that did not finish. A connection that
// failed, went quiet or ended early is retried like a network error, even
// after content arrived; an error event is retried unless its code is a
// client error.
func (c *Client) streamFailure(ctx, actx context.Context, status int, err error) error {
	var se *streamError
	switch {
	case errors.As(err, &se):
		st, retry := status, true
		if se.code >= 100 && se.code <= 599 {
			st = se.code
			retry = se.code >= 500 || retryableStatus(se.code)
		}
		return c.newError(st, c.bodyMessage(se.raw), errors.New("the stream carried an error"), retry)
	case errors.Is(err, errStreamCut):
		return c.newError(status, "", err, true)
	case errors.Is(err, errNoChoices), errors.Is(err, errNotStream), errors.Is(err, errStreamTooLarge), errors.Is(err, errSSELineTooLong):
		return c.newError(status, "", err, false)
	}
	return c.transportError(ctx, actx, status, fmt.Errorf("reading the stream: %w", err))
}

// retryableStatus reports whether an HTTP status is worth retrying.
func retryableStatus(s int) bool {
	switch s {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return s >= 500 && s <= 599
}

// retryAfter parses a Retry-After header (seconds or an HTTP date).
func retryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(h, 64); err == nil && s > 0 {
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// Models lists the model ids the provider serves (GET BaseURL/models). It
// makes one attempt.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	actx, cancel := context.WithTimeout(ctx, c.timeout(false))
	defer cancel()
	hreq, err := http.NewRequestWithContext(actx, http.MethodGet, c.endpoint("/models"), nil)
	if err != nil {
		return nil, c.newError(0, "", err, false)
	}
	hreq.Header.Set("Accept", "application/json")
	if k := c.apiKey(); k != "" {
		hreq.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := c.doer().Do(hreq)
	if err != nil {
		return nil, c.newError(0, "", err, true)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, c.newError(resp.StatusCode, "", err, true)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.newError(resp.StatusCode, c.bodyMessage(raw), nil, retryableStatus(resp.StatusCode))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, c.newError(resp.StatusCode, c.bodySnippet(raw), errors.New("the model list is not JSON"), false)
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// reply is one decoded reply, streamed or not.
type reply struct {
	Model        string
	Content      string
	Reasoning    string
	FinishReason string
	Usage        Usage
	CostUSD      float64
	Streamed     bool
}

// wireResponse is the subset of a chat-completions reply we read.
type wireResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			Reasoning        string          `json:"reasoning"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage      `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// wireUsage is a reply's usage: the token counts, and the cost in US
// dollars that OpenRouter adds (0 when the provider reports none).
type wireUsage struct {
	Usage
	Cost float64 `json:"cost"`
}

// reply returns the first choice; w has at least one.
func (w *wireResponse) reply() *reply {
	ch := w.Choices[0]
	r := &reply{
		Model:        w.Model,
		Content:      contentText(ch.Message.Content),
		Reasoning:    ch.Message.ReasoningContent,
		FinishReason: ch.FinishReason,
	}
	if r.Reasoning == "" {
		r.Reasoning = ch.Message.Reasoning
	}
	if w.Usage != nil {
		r.Usage = w.Usage.filled()
		r.CostUSD = w.Usage.Cost
	}
	return r
}

// contentText returns a message's text: a string, or the text parts of a
// content array.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "" || p.Type == "text" || p.Type == "output_text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// filled returns u with TotalTokens computed when the provider left it out.
func (u Usage) filled() Usage {
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u
}

// codeOf returns a numeric code from an error object ({"code": 502} or
// {"code": "502"}), or 0.
func codeOf(raw json.RawMessage) int {
	var e struct {
		Code json.RawMessage `json:"code"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.Trim(string(e.Code), `"`))
	if err != nil {
		return 0
	}
	return n
}
