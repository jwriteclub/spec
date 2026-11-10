// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
)

const testKey = "sk-test-0123456789abcdef"

// fake is an httptest provider. Each request is recorded; reply decides
// the answer for the n-th request (1-based).
type fake struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	reqs  []recorded
	reply func(n int, w http.ResponseWriter, r *http.Request)
}

type recorded struct {
	Method string
	Path   string
	Auth   string
	Body   map[string]any
}

func newFake(t *testing.T, reply func(n int, w http.ResponseWriter, r *http.Request)) *fake {
	t.Helper()
	f := &fake{t: t, reply: reply}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec := recorded{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &rec.Body)
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, rec)
		n := len(f.reqs)
		f.mu.Unlock()
		f.reply(n, w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

// client returns a Client for the fake whose waits are recorded, not slept.
func (f *fake) client(waits *[]time.Duration) *Client {
	c := &Client{Name: "fake", BaseURL: f.srv.URL + "/v1/", Model: "m-main", TriageModel: "m-triage"}
	c.SetAPIKey(testKey)
	c.sleep = func(ctx context.Context, d time.Duration) error {
		if waits != nil {
			*waits = append(*waits, d)
		}
		return ctx.Err()
	}
	return c
}

func ok(content string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeOK(w, content)
	}
}

func writeOK(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "x",
		"model":   "echoed-model",
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 2, "total_tokens": 13},
	})
}

func status(code int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

func f64(v float64) *float64 { return &v }

func TestChatRequestAndResponse(t *testing.T) {
	f := newFake(t, ok("OK"))
	c := f.client(nil)
	resp, err := c.Chat(context.Background(), Request{
		Messages:    []Message{System("be terse"), User("Reply with the single word OK")},
		Temperature: f64(0),
		MaxTokens:   8,
	})
	require.NoError(t, err)
	assert.Equal(t, "OK", resp.Content)
	assert.Equal(t, "fake", resp.Provider)
	assert.Equal(t, "m-main", resp.RequestedModel)
	assert.Equal(t, "echoed-model", resp.Model)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, Usage{PromptTokens: 11, CompletionTokens: 2, TotalTokens: 13}, resp.Usage)
	assert.Equal(t, 1, resp.Attempts)
	assert.Positive(t, resp.Latency)
	assert.False(t, resp.JSONMode)

	reqs := f.requests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, http.MethodPost, r.Method)
	assert.Equal(t, "/v1/chat/completions", r.Path, "a trailing slash on base_url is fine")
	assert.Equal(t, "Bearer "+testKey, r.Auth)
	assert.Equal(t, "m-main", r.Body["model"])
	assert.Equal(t, float64(8), r.Body["max_tokens"])
	assert.Equal(t, float64(0), r.Body["temperature"])
	assert.Equal(t, []any{
		map[string]any{"role": "system", "content": "be terse"},
		map[string]any{"role": "user", "content": "Reply with the single word OK"},
	}, r.Body["messages"])
	assert.NotContains(t, r.Body, "response_format")
	assert.NotContains(t, r.Body, "stream")
}

func TestChatOmitsUnsetFieldsAndKey(t *testing.T) {
	f := newFake(t, ok("hi"))
	c := f.client(nil)
	c.SetAPIKey("")
	_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.NoError(t, err)
	r := f.requests()[0]
	assert.Empty(t, r.Auth, "no key, no Authorization header")
	assert.NotContains(t, r.Body, "temperature")
	assert.NotContains(t, r.Body, "max_tokens")

	_, err = c.Chat(context.Background(), Request{})
	assert.Error(t, err, "a request needs a message")
	assert.Len(t, f.requests(), 1)
}

func TestJSONMode(t *testing.T) {
	f := newFake(t, ok(`{"ok": true}`))
	c := f.client(nil)

	// Off: no response_format even when JSON is asked for.
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}, JSON: true})
	require.NoError(t, err)
	assert.False(t, resp.JSONMode)
	assert.NotContains(t, f.requests()[0].Body, "response_format")

	// On: response_format only for JSON requests.
	c.JSONMode = true
	resp, err = c.Chat(context.Background(), Request{Messages: []Message{User("x")}, JSON: true})
	require.NoError(t, err)
	assert.True(t, resp.JSONMode)
	assert.Equal(t, map[string]any{"type": "json_object"}, f.requests()[1].Body["response_format"])
	_, err = c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.NoError(t, err)
	assert.NotContains(t, f.requests()[2].Body, "response_format")
}

func TestExtraMerge(t *testing.T) {
	f := newFake(t, ok("x"))
	c := f.client(nil)
	c.Extra = map[string]any{
		"temperature": 1.0, // a model that only takes its default wins over the request
		"reasoning":   map[string]any{"enabled": true, "effort": "low"},
		"top_p":       0.9,
	}
	c.TriageExtra = map[string]any{
		"reasoning_effort": "none",
		"reasoning":        map[string]any{"enabled": false},
	}

	_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}, Temperature: f64(0)})
	require.NoError(t, err)
	b := f.requests()[0].Body
	assert.Equal(t, "m-main", b["model"])
	assert.Equal(t, 1.0, b["temperature"])
	assert.Equal(t, map[string]any{"enabled": true, "effort": "low"}, b["reasoning"])
	assert.NotContains(t, b, "reasoning_effort", "triage_extra only applies to triage")

	_, err = c.Chat(context.Background(), Request{
		Messages: []Message{User("x")}, Triage: true,
		Extra: map[string]any{"top_p": 0.5, "seed": 7},
	})
	require.NoError(t, err)
	b = f.requests()[1].Body
	assert.Equal(t, "m-triage", b["model"])
	assert.Equal(t, "none", b["reasoning_effort"])
	assert.Equal(t, map[string]any{"enabled": false, "effort": "low"}, b["reasoning"], "nested tables merge key by key")
	assert.Equal(t, 0.5, b["top_p"], "the request's extra wins")
	assert.Equal(t, float64(7), b["seed"])

	// Merging never mutates the provider's tables.
	assert.Equal(t, map[string]any{"enabled": true, "effort": "low"}, c.Extra["reasoning"])

	// An explicit model wins over triage; no triage model falls back.
	assert.Equal(t, "other", c.ModelFor(Request{Model: "other", Triage: true}))
	c.TriageModel = ""
	assert.Equal(t, "m-main", c.ModelFor(Request{Triage: true}))

	body, err := c.Body(Request{Messages: []Message{User("x")}})
	require.NoError(t, err)
	assert.NotContains(t, string(body), testKey)
}

func TestRetryThenSuccess(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		switch n {
		case 1:
			status(503, `{"error":{"message":"overloaded"}}`)(w)
		case 2:
			w.Header().Set("Retry-After", "7")
			status(429, `{"error":{"message":"slow down"}}`)(w)
		default:
			writeOK(w, "done")
		}
	})
	var waits []time.Duration
	c := f.client(&waits)
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.NoError(t, err)
	assert.Equal(t, "done", resp.Content)
	assert.Equal(t, 3, resp.Attempts)
	require.Len(t, waits, 2)
	assert.GreaterOrEqual(t, waits[0], DefaultBaseDelay/2)
	assert.LessOrEqual(t, waits[0], DefaultBaseDelay*3/2)
	assert.Equal(t, 7*time.Second, waits[1], "Retry-After wins when longer than the backoff")
}

func TestRetryableStatuses(t *testing.T) {
	for _, code := range []int{408, 409, 425, 429, 500, 502, 503, 504} {
		f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { status(code, "nope")(w) })
		var waits []time.Duration
		c := f.client(&waits)
		_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
		var e *Error
		require.ErrorAs(t, err, &e, code)
		assert.Equal(t, code, e.Status)
		assert.True(t, e.Retryable, code)
		assert.Equal(t, DefaultAttempts, e.Attempts, code)
		assert.Len(t, f.requests(), 3, code)
		assert.Len(t, waits, 2, code)
		assert.Contains(t, err.Error(), "after 3 attempts")
	}
	for _, code := range []int{400, 401, 403, 404, 422} {
		f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { status(code, "nope")(w) })
		c := f.client(nil)
		_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
		var e *Error
		require.ErrorAs(t, err, &e, code)
		assert.False(t, e.Retryable, code)
		assert.Equal(t, 1, e.Attempts)
		assert.Len(t, f.requests(), 1, code)
	}
}

func TestRetryNetworkError(t *testing.T) {
	f := newFake(t, ok("x"))
	url := f.srv.URL
	f.srv.Close()
	var waits []time.Duration
	c := &Client{Name: "down", BaseURL: url, Model: "m"}
	c.sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 0, e.Status)
	assert.True(t, e.Retryable)
	assert.Equal(t, 3, e.Attempts)
	assert.Len(t, waits, 2)
	assert.Contains(t, e.Fix(), "could not be reached")
}

func TestAttemptTimeoutIsRetried(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		writeOK(w, "late but fine")
	})
	c := f.client(nil)
	c.Timeout = 50 * time.Millisecond
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.NoError(t, err)
	assert.Equal(t, 2, resp.Attempts)
}

func TestTimeoutFix(t *testing.T) {
	f := newFake(t, func(_ int, _ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	c := f.client(nil)
	c.Timeout = 20 * time.Millisecond
	_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 3, e.Attempts)
	assert.Contains(t, e.Fix(), "did not finish in time")
	assert.Contains(t, e.Fix(), "reasoning_effort")

	// Cloudflare's own limit on a reply is a 408.
	assert.Contains(t, (&Error{Status: http.StatusRequestTimeout}).Fix(), "did not finish in time")
	assert.Contains(t, (&Error{Status: 0}).Fix(), "could not be reached")
}

func TestContextCancelStopsRetrying(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { status(503, "busy")(w) })
	c := f.client(nil)
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	_, err := c.Chat(ctx, Request{Messages: []Message{User("x")}})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Len(t, f.requests(), 1)

	// Already cancelled: no *Error, the context's error.
	_, err = c.Chat(ctx, Request{Messages: []Message{User("x")}})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRealSleepHonoursContext(t *testing.T) {
	c := &Client{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	assert.ErrorIs(t, c.wait(ctx, time.Hour), context.Canceled)
	assert.Less(t, time.Since(start), time.Second)
	assert.NoError(t, c.wait(context.Background(), time.Millisecond))
}

func TestBackoff(t *testing.T) {
	c := &Client{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}
	for i := 0; i < 200; i++ {
		d1 := c.backoff(1, 0)
		assert.GreaterOrEqual(t, d1, 50*time.Millisecond)
		assert.LessOrEqual(t, d1, 150*time.Millisecond)
		d3 := c.backoff(3, 0)
		assert.GreaterOrEqual(t, d3, 200*time.Millisecond)
		assert.LessOrEqual(t, d3, 600*time.Millisecond)
		assert.LessOrEqual(t, c.backoff(30, 0), time.Second, "capped")
		assert.Equal(t, time.Second, c.backoff(1, time.Hour), "Retry-After is capped too")
	}
	assert.Equal(t, 2*time.Second, retryAfter("2"))
	assert.Equal(t, 1500*time.Millisecond, retryAfter("1.5"))
	assert.Zero(t, retryAfter(""))
	assert.Zero(t, retryAfter("soon"))
	assert.Positive(t, retryAfter(time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)))
}

func TestMalformedReplies(t *testing.T) {
	cases := []struct {
		name      string
		write     func(http.ResponseWriter)
		retryable bool
		contains  string
	}{
		{"not json", status(200, "<html>gateway</html>"), false, "not JSON"},
		{"no choices", status(200, `{"choices": []}`), false, "no choices"},
		{"200 with an upstream error", status(200, `{"error":{"code":502,"message":"upstream died"}}`), true, "upstream died"},
		{"cloudflare errors array", status(400, `{"errors":[{"message":"bad model"}],"success":false}`), false, "bad model"},
		{"plain error string", status(400, `{"error":"nope"}`), false, "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { tc.write(w) })
			c := f.client(nil)
			_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
			var e *Error
			require.ErrorAs(t, err, &e)
			assert.Equal(t, tc.retryable, e.Retryable)
			assert.Contains(t, err.Error(), tc.contains)
		})
	}
}

func TestContentParts(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":[{"type":"text","text":"a"},{"type":"image","url":"x"},{"type":"text","text":"b"}]},"finish_reason":"length"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`)
	})
	resp, err := f.client(nil).Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.NoError(t, err)
	assert.Equal(t, "ab", resp.Content)
	assert.Equal(t, "length", resp.FinishReason)
	assert.Equal(t, "", resp.Model, "no model echoed")
	assert.Equal(t, 7, resp.Usage.TotalTokens, "total filled in when absent")

	f = newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":null,"reasoning_content":"hmm"},"finish_reason":"length"}]}`)
	})
	resp, err = f.client(nil).Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.NoError(t, err)
	assert.Equal(t, "", resp.Content)
	assert.Equal(t, Usage{}, resp.Usage)
}

func TestRedaction(t *testing.T) {
	// The provider echoes the Authorization header and the key, the key
	// straddling the 300-byte cut, in a 401.
	f := newFake(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		msg := strings.Repeat("x", 290) + testKey + " header was " + r.Header.Get("Authorization")
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg}})
	})
	c := f.client(nil)
	_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.Error(t, err)
	for _, s := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		assert.NotContains(t, s, testKey)
		assert.NotContains(t, s, testKey[:12], "no fragment of the key either")
	}
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Contains(t, e.Fix(), "refused the API key")

	// Short of the cut, the redaction marker shows where the key was.
	f = newFake(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key `+testKey+`; got `+r.Header.Get("Authorization")+`"}}`)
	})
	_, err = f.client(nil).Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testKey)
	assert.Contains(t, err.Error(), "bad key [redacted]; got Bearer [redacted]")

	// A key in the URL (a provider that takes ?key=) is redacted from
	// network errors.
	down := newFake(t, ok("x"))
	url := down.srv.URL
	down.srv.Close()
	c = &Client{Name: "q", BaseURL: url + "/v1?key=" + testKey, Model: "m", Attempts: 1}
	c.SetAPIKey(testKey)
	_, err = c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testKey)

	// Printing the client shows no key.
	c = f.client(nil)
	for _, s := range []string{fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%v", *c), fmt.Sprintf("%+v", *c)} {
		assert.NotContains(t, s, testKey)
	}

	// A model reply quoting the key is redacted in a JSONError.
	f = newFake(t, ok("your key is "+testKey))
	resp, err := f.client(nil).ChatJSON(context.Background(), Request{Messages: []Message{User("x")}}, &map[string]any{})
	require.NotNil(t, resp)
	var je *JSONError
	require.ErrorAs(t, err, &je)
	assert.NotContains(t, err.Error(), testKey)

	assert.Equal(t, "Authorization: Bearer [redacted]", redactBearer("Authorization: Bearer abc.def-123"))
}

// A provider that echoes the key JSON-escaped (a base64 key's "/" written
// as "\/", or a \u escape) must not leak it once the message is decoded.
func TestRedactionOfEscapedKey(t *testing.T) {
	const key = "ab/cd+ef0123456789/xyz=="
	escaped := strings.ReplaceAll(key, "/", `\/`)
	unicode := `a` + key[1:]
	for _, echo := range []string{escaped, unicode} {
		f := newFake(t, func(_ int, w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":{"message":"bad key `+echo+`; `+strings.Repeat("x", 249)+` again `+echo+`"}}`)
		})
		c := f.client(nil)
		c.SetAPIKey(key)
		_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), key)
		assert.NotContains(t, err.Error(), key[:8], "no fragment of the key at the cut either")
		assert.Contains(t, err.Error(), "bad key [redacted]")
	}
}

func TestNewFromConfig(t *testing.T) {
	p := config.Provider{
		BaseURL:     "https://api.example.com/accounts/${ACCOUNT}/v1",
		Model:       "m",
		TriageModel: "t",
		APIKeyEnv:   "EX_KEY",
		JSONMode:    true,
		Extra:       map[string]any{"a": 1},
		TriageExtra: map[string]any{"reasoning_effort": "none"},
	}
	env := map[string]string{"ACCOUNT": "acct", "EX_KEY": testKey}
	get := func(k string) string { return env[k] }
	c, err := New("ex", p, get)
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.com/accounts/acct/v1", c.BaseURL)
	assert.Equal(t, "ex", c.Name)
	assert.Equal(t, "t", c.TriageModel)
	assert.True(t, c.JSONMode)
	assert.Equal(t, testKey, c.apiKey())
	assert.Equal(t, DefaultTimeout, c.timeout(false), "no timeout key: the default")

	p.Timeout = 240
	c, err = New("ex", p, get)
	require.NoError(t, err)
	assert.Equal(t, 240*time.Second, c.timeout(false))
	p.Timeout = 0

	delete(env, "ACCOUNT")
	_, err = New("ex", p, get)
	var ee *config.EnvError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, "ACCOUNT", ee.Var)

	env["ACCOUNT"] = "acct"
	delete(env, "EX_KEY")
	_, err = New("ex", p, get)
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, "EX_KEY", ee.Var)
	assert.Equal(t, "api_key_env", ee.Field)
}

func TestChatJSON(t *testing.T) {
	f := newFake(t, ok("Sure!\n```json\n{\"ok\": true, \"n\": 2}\n```\n"))
	var v struct {
		OK bool `json:"ok"`
		N  int  `json:"n"`
	}
	resp, err := f.client(nil).ChatJSON(context.Background(), Request{Messages: []Message{User("x")}}, &v)
	require.NoError(t, err)
	assert.True(t, v.OK)
	assert.Equal(t, 2, v.N)
	assert.Equal(t, 13, resp.Usage.TotalTokens)

	f = newFake(t, ok("I cannot do that"))
	resp, err = f.client(nil).ChatJSON(context.Background(), Request{Messages: []Message{User("x")}}, &v)
	require.NotNil(t, resp, "usage is still reported")
	var je *JSONError
	require.ErrorAs(t, err, &je)
	assert.ErrorIs(t, err, ErrNoJSON)
	assert.Contains(t, err.Error(), "I cannot do that")

	f = newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { status(401, "no")(w) })
	resp, err = f.client(nil).ChatJSON(context.Background(), Request{Messages: []Message{User("x")}}, &v)
	assert.Nil(t, resp)
	var e *Error
	assert.ErrorAs(t, err, &e)
}

func TestModels(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"a"},{"id":"b-nano"}]}`)
	})
	ids, err := f.client(nil).Models(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b-nano"}, ids)
	r := f.requests()[0]
	assert.Equal(t, http.MethodGet, r.Method)
	assert.Equal(t, "/v1/models", r.Path)
	assert.Equal(t, "Bearer "+testKey, r.Auth)

	f = newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		status(401, `{"error":{"message":"`+testKey+`"}}`)(w)
	})
	_, err = f.client(nil).Models(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testKey)
}

func TestUsageAdd(t *testing.T) {
	assert.Equal(t, Usage{3, 5, 8}, Usage{1, 2, 3}.Add(Usage{2, 3, 5}))
}

func TestErrorTexts(t *testing.T) {
	e := &Error{Provider: "p", Model: "m", Status: 404, Message: "no such model", Attempts: 1}
	assert.Equal(t, "provider p (model m): HTTP 404 Not Found: no such model", e.Error())
	assert.Contains(t, e.Fix(), "model id")
	for _, s := range []int{400, 429, 500, 0, 418} {
		assert.NotEmpty(t, (&Error{Status: s}).Fix())
	}
	assert.Equal(t, "provider p: request failed", (&Error{Provider: "p"}).Error())
	cause := errors.New("dial tcp: refused")
	assert.ErrorIs(t, &Error{cause: cause}, cause)
	assert.Equal(t, strings.Repeat("é", 150)+"...", shorten(strings.Repeat("é", 200)), "cut on a rune boundary")
}

func TestMaxTokensParam(t *testing.T) {
	f := newFake(t, ok("x"))
	c := f.client(nil)
	c.MaxTokensParam = "max_completion_tokens"
	_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}, MaxTokens: 8})
	require.NoError(t, err)
	b := f.requests()[0].Body
	assert.Equal(t, float64(8), b["max_completion_tokens"])
	assert.NotContains(t, b, "max_tokens")

	c, err = New("p", config.Provider{BaseURL: "http://x", Model: "m", MaxTokensParam: "max_completion_tokens"}, func(string) string { return "" })
	require.NoError(t, err)
	assert.Equal(t, "max_completion_tokens", c.MaxTokensParam)
}

func TestReasoningReply(t *testing.T) {
	// A reasoning model cut off by max_tokens: empty content, reasoning set.
	for _, field := range []string{"reasoning_content", "reasoning"} {
		f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"model":"glm","choices":[{"message":{"content":"","`+field+`":"The user asks"},"finish_reason":"length"}],"usage":{"prompt_tokens":18,"completion_tokens":8}}`)
		})
		resp, err := f.client(nil).Chat(context.Background(), Request{Messages: []Message{User("x")}, MaxTokens: 8})
		require.NoError(t, err)
		assert.Equal(t, "", resp.Content)
		assert.Equal(t, "The user asks", resp.Reasoning, field)
		assert.Equal(t, "length", resp.FinishReason)
	}
}
