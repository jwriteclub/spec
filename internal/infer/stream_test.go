// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
)

// sseData is one event carrying v as JSON.
func sseData(v any) string {
	b, _ := json.Marshal(v)
	return "data: " + string(b) + "\n\n"
}

// delta is a chunk whose first choice's delta is d.
func delta(d map[string]any) string {
	return sseData(map[string]any{
		"id": "c", "object": "chat.completion.chunk", "model": "echoed-model",
		"choices": []any{map[string]any{"index": 0, "delta": d, "finish_reason": nil}},
	})
}

func contentChunk(s string) string { return delta(map[string]any{"content": s}) }

func finishChunk(reason string) string {
	return sseData(map[string]any{
		"model":   "echoed-model",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": reason}},
	})
}

func usageChunk(prompt, completion int) string {
	return sseData(map[string]any{
		"model": "echoed-model", "choices": []any{},
		"usage": map[string]any{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion},
	})
}

const sseDone = "data: [DONE]\n\n"

// okStream is a whole streamed reply of content.
func okStream(content string) []string {
	return []string{
		delta(map[string]any{"role": "assistant", "content": nil}),
		contentChunk(content), finishChunk("stop"), usageChunk(11, 2), sseDone,
	}
}

// writeSSE writes each part and flushes it, so each is its own TCP write.
func writeSSE(w http.ResponseWriter, parts ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	for _, p := range parts {
		_, _ = io.WriteString(w, p)
		if fl != nil {
			fl.Flush()
		}
	}
}

func streams(parts ...string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) { writeSSE(w, parts...) }
}

// streamClient is the fake's client with streaming on.
func (f *fake) streamClient(waits *[]time.Duration) *Client {
	c := f.client(waits)
	c.Stream = true
	return c
}

// stall blocks until the client goes away (or 5 s pass).
func stall(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

func chat(t *testing.T, c *Client) (*Response, error) {
	t.Helper()
	return c.Chat(context.Background(), Request{Messages: []Message{User("x")}, MaxTokens: 64})
}

func TestStreamRequestBody(t *testing.T) {
	var accept string
	f := newFake(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		writeSSE(w, okStream("OK")...)
	})
	c := f.streamClient(nil)
	c.Extra = map[string]any{"reasoning_effort": "low"}
	resp, err := chat(t, c)
	require.NoError(t, err)
	b := f.requests()[0].Body
	assert.Equal(t, true, b["stream"])
	assert.Equal(t, map[string]any{"include_usage": true}, b["stream_options"])
	assert.Equal(t, "low", b["reasoning_effort"])
	assert.Contains(t, accept, "text/event-stream")
	assert.True(t, resp.Streamed)

	body, err := c.Body(Request{Messages: []Message{User("x")}})
	require.NoError(t, err)
	assert.Contains(t, string(body), `"stream":true`)
}

func TestStreamContentOnly(t *testing.T) {
	f := newFake(t, streams(contentChunk("Hel"), contentChunk("lo, "), contentChunk("world"), finishChunk("stop"), sseDone))
	resp, err := chat(t, f.streamClient(nil))
	require.NoError(t, err)
	assert.Equal(t, "Hello, world", resp.Content)
	assert.Equal(t, "", resp.Reasoning)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, "echoed-model", resp.Model)
	assert.Equal(t, "m-main", resp.RequestedModel)
	assert.Equal(t, Usage{}, resp.Usage, "no usage chunk, no usage")
	assert.Equal(t, 1, resp.Attempts)
	assert.True(t, resp.Streamed)
}

func TestStreamReasoningAndContent(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning"} {
		f := newFake(t, streams(
			delta(map[string]any{"role": "assistant", "content": nil}),
			delta(map[string]any{field: "The user "}),
			delta(map[string]any{field: "wants OK."}),
			delta(map[string]any{"content": "OK", field: nil}),
			finishChunk("stop"), usageChunk(18, 40), sseDone))
		resp, err := chat(t, f.streamClient(nil))
		require.NoError(t, err, field)
		assert.Equal(t, "OK", resp.Content, field)
		assert.Equal(t, "The user wants OK.", resp.Reasoning, field)
		assert.Equal(t, Usage{18, 40, 58}, resp.Usage, field)
	}

	// Cut off while reasoning: no content, finish_reason length, the
	// reasoning kept, as a reply that is not streamed.
	f := newFake(t, streams(delta(map[string]any{"reasoning_content": "Let me check"}), finishChunk("length"), usageChunk(7000, 16384), sseDone))
	resp, err := chat(t, f.streamClient(nil))
	require.NoError(t, err)
	assert.Equal(t, "", resp.Content)
	assert.Equal(t, "Let me check", resp.Reasoning)
	assert.Equal(t, "length", resp.FinishReason)
	assert.Equal(t, 16384, resp.Usage.CompletionTokens)
}

func TestStreamUsageInFinalChunk(t *testing.T) {
	// Usage arrives after finish_reason, in a chunk with no choices, and
	// without total_tokens.
	f := newFake(t, streams(contentChunk("a"), finishChunk("stop"),
		`data: {"model":"echoed-model","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3}}`+"\n\n", sseDone))
	resp, err := chat(t, f.streamClient(nil))
	require.NoError(t, err)
	assert.Equal(t, Usage{5, 3, 8}, resp.Usage)
	assert.Equal(t, "stop", resp.FinishReason)
}

func TestStreamMissingDone(t *testing.T) {
	// A finish_reason without [DONE] is a whole reply.
	f := newFake(t, streams(contentChunk("fine"), finishChunk("stop"), usageChunk(1, 1)))
	resp, err := chat(t, f.streamClient(nil))
	require.NoError(t, err)
	assert.Equal(t, "fine", resp.Content)
	assert.Equal(t, 2, resp.Usage.TotalTokens)

	// Neither: the stream was cut short; retried like a network error.
	var waits []time.Duration
	f = newFake(t, streams(contentChunk("half")))
	_, err = chat(t, f.streamClient(&waits))
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.True(t, e.Retryable)
	assert.Equal(t, 3, e.Attempts)
	assert.Len(t, waits, 2)
	assert.Contains(t, err.Error(), "ended before the reply was complete")

	// Cut short once, whole the second time.
	f = newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			writeSSE(w, contentChunk("half"))
			return
		}
		writeSSE(w, okStream("whole")...)
	})
	resp, err = chat(t, f.streamClient(nil))
	require.NoError(t, err)
	assert.Equal(t, "whole", resp.Content, "the second attempt's text only")
	assert.Equal(t, 2, resp.Attempts)
}

func TestStreamCRLFAndComments(t *testing.T) {
	crlf := strings.ReplaceAll(strings.Join(okStream("crlf"), ""), "\n", "\r\n")
	f := newFake(t, streams(": OPENROUTER PROCESSING\r\n\r\n", crlf))
	resp, err := chat(t, f.streamClient(nil))
	require.NoError(t, err)
	assert.Equal(t, "crlf", resp.Content)
	assert.Equal(t, 13, resp.Usage.TotalTokens)

	cr := strings.ReplaceAll(strings.Join(okStream("cr"), ""), "\n", "\r")
	f = newFake(t, streams(cr))
	resp, err = chat(t, f.streamClient(nil))
	require.NoError(t, err)
	assert.Equal(t, "cr", resp.Content, "a lone CR ends a line too")

	f = newFake(t, streams(": ping\n\n", contentChunk("a"), ": ping\n", "\n", ":\n\n",
		"event: message\n", "id: 7\n", "retry: 100\n", contentChunk("b"), finishChunk("stop"), sseDone))
	resp, err = chat(t, f.streamClient(nil))
	require.NoError(t, err)
	assert.Equal(t, "ab", resp.Content, "comments and other fields are ignored")
}

func TestStreamSplitWrites(t *testing.T) {
	// Every byte its own write: lines, a "data:" prefix and the bytes of
	// multi-byte characters are all split.
	text := "naïve 日本語 ✓ 🙂"
	whole := strings.Join(append([]string{": ping\r\n\r\n"}, okStream(text)...), "")
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		parts := make([]string, 0, len(whole))
		for i := 0; i < len(whole); i++ {
			parts = append(parts, whole[i:i+1])
		}
		writeSSE(w, parts...)
	})
	resp, err := chat(t, f.streamClient(nil))
	require.NoError(t, err)
	assert.Equal(t, text, resp.Content)

	// The parser itself, one byte per read, and splits mid-character.
	r, err := readStream(iotest.OneByteReader(strings.NewReader(whole)))
	require.NoError(t, err)
	assert.Equal(t, text, r.Content)
	for split := 1; split < 9; split++ {
		r, err := readStream(&chunkReader{data: []byte(whole), n: split})
		require.NoError(t, err, split)
		assert.Equal(t, text, r.Content, split)
	}
}

func TestStreamDataLinesWithoutBlankLines(t *testing.T) {
	// A server that sends one chunk per data line with no blank line
	// between them: each line is read on its own.
	s := strings.Join([]string{contentChunk("a"), contentChunk("b"), finishChunk("stop")}, "")
	s = strings.ReplaceAll(s, "\n\n", "\n")
	r, err := readStream(strings.NewReader(s + "data: [DONE]\n"))
	require.NoError(t, err)
	assert.Equal(t, "ab", r.Content)
	assert.Equal(t, "stop", r.FinishReason)
}

func TestStreamPlainJSONReply(t *testing.T) {
	// A server that ignores stream and answers with one JSON object.
	f := newFake(t, ok("whole"))
	resp, err := chat(t, f.streamClient(nil))
	require.NoError(t, err)
	assert.Equal(t, "whole", resp.Content)
	assert.False(t, resp.Streamed)
	assert.Equal(t, 13, resp.Usage.TotalTokens)
}

func TestStreamBrokenReplies(t *testing.T) {
	cases := []struct {
		name      string
		parts     []string
		retryable bool
		contains  string
	}{
		{"no choices", []string{usageChunk(1, 1), sseDone}, false, "no choices"},
		{"not a stream", []string{"<html>gateway</html>"}, false, "neither JSON nor an event stream"},
		{"empty", nil, true, "ended before the reply was complete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, streams(tc.parts...))
			c := f.streamClient(nil)
			_, err := chat(t, c)
			var e *Error
			require.ErrorAs(t, err, &e)
			assert.Equal(t, tc.retryable, e.Retryable)
			assert.Contains(t, err.Error(), tc.contains)
		})
	}
}

func TestStreamIdleTimeout(t *testing.T) {
	// The server sends a little, then stalls: each attempt is abandoned
	// after IdleTimeout and retried.
	f := newFake(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		writeSSE(w, contentChunk("thinking..."))
		stall(r)
	})
	var waits []time.Duration
	c := f.streamClient(&waits)
	c.IdleTimeout = 50 * time.Millisecond
	start := time.Now()
	_, err := chat(t, c)
	assert.Less(t, time.Since(start), 3*time.Second)
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.ErrorIs(t, err, ErrIdleTimeout)
	assert.True(t, e.Retryable)
	assert.Equal(t, 3, e.Attempts)
	assert.Len(t, waits, 2)
	assert.Contains(t, err.Error(), "idle_timeout")
	assert.Contains(t, e.Fix(), "idle_timeout")

	// Content arrived, the connection went quiet, the retry is whole.
	f = newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			writeSSE(w, contentChunk("par"))
			stall(r)
			return
		}
		writeSSE(w, okStream("full")...)
	})
	c = f.streamClient(nil)
	c.IdleTimeout = 50 * time.Millisecond
	resp, err := chat(t, c)
	require.NoError(t, err)
	assert.Equal(t, "full", resp.Content)
	assert.Equal(t, 2, resp.Attempts)

	// No headers at all within IdleTimeout counts too.
	f = newFake(t, func(_ int, _ http.ResponseWriter, r *http.Request) { stall(r) })
	c = f.streamClient(nil)
	c.IdleTimeout = 50 * time.Millisecond
	c.Attempts = 1
	_, err = chat(t, c)
	assert.ErrorIs(t, err, ErrIdleTimeout)

	// Keep-alive comments are bytes: they hold the attempt open.
	f = newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < 6; i++ {
			writeSSE(w, ": ping\n\n")
			time.Sleep(20 * time.Millisecond)
		}
		writeSSE(w, okStream("slow")...)
	})
	c = f.streamClient(nil)
	c.IdleTimeout = 60 * time.Millisecond
	resp, err = chat(t, c)
	require.NoError(t, err)
	assert.Equal(t, "slow", resp.Content)
	assert.Equal(t, 1, resp.Attempts)
}

func TestStreamOverallTimeout(t *testing.T) {
	// The server keeps the stream alive but never finishes: the attempt
	// ends at Timeout, not IdleTimeout.
	f := newFake(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		for {
			writeSSE(w, delta(map[string]any{"reasoning_content": "hmm "}))
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	})
	c := f.streamClient(nil)
	c.Timeout = 150 * time.Millisecond
	c.IdleTimeout = time.Second
	c.Attempts = 1
	start := time.Now()
	_, err := chat(t, c)
	assert.Less(t, time.Since(start), 3*time.Second)
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.NotErrorIs(t, err, ErrIdleTimeout)
	assert.True(t, e.Retryable)
	assert.Contains(t, err.Error(), "took longer than 150ms (timeout)")
	assert.Contains(t, e.Fix(), "did not finish in time")

	// The caller's own context ending is returned as is.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c.Timeout = time.Minute
	_, err = c.Chat(ctx, Request{Messages: []Message{User("x")}})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, errors.As(err, &e), "not an *Error")
}

func TestStreamErrorEvents(t *testing.T) {
	escaped := `s` + testKey[1:] // the key JSON-escaped: "s" as s
	cases := []struct {
		name      string
		event     string
		status    int
		retryable bool
		contains  string
	}{
		{"openai error object", `data: {"error":{"message":"upstream failed for ` + testKey + `","code":502}}` + "\n\n", 502, true, "upstream failed for [redacted]"},
		{"escaped key", `data: {"error":{"message":"bad key ` + escaped + `","type":"server_error"}}` + "\n\n", 200, true, "bad key [redacted]"},
		{"cloudflare errors array", `data: {"errors":[{"message":"AiError: inference timed out","code":3046}],"success":false}` + "\n\n", 200, true, "AiError: inference timed out"},
		{"event error", "event: error\ndata: {\"message\":\"overloaded; Bearer " + testKey + "\"}\n\n", 200, true, "overloaded; Bearer [redacted]"},
		{"client error code", `data: {"error":{"message":"context length exceeded","code":400}}` + "\n\n", 400, false, "context length exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, streams(contentChunk("partial answer"), tc.event))
			_, err := chat(t, f.streamClient(nil))
			var e *Error
			require.ErrorAs(t, err, &e)
			assert.Equal(t, tc.status, e.Status)
			assert.Equal(t, tc.retryable, e.Retryable)
			assert.Contains(t, err.Error(), tc.contains)
			assert.NotContains(t, err.Error(), testKey)
			assert.NotContains(t, err.Error(), testKey[3:12], "no fragment of the key")
			if tc.retryable {
				assert.Len(t, f.requests(), 3)
			} else {
				assert.Len(t, f.requests(), 1)
			}
		})
	}
}

func TestStreamFallback(t *testing.T) {
	// The provider rejects stream_options, then stream: the same attempt
	// is sent again without each, with no backoff, and succeeds unstreamed.
	var f *fake
	f = newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		b := f.requests()[n-1].Body
		switch {
		case b["stream_options"] != nil:
			status(400, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`)(w)
		case b["stream"] != nil:
			status(400, `{"errors":[{"message":"Streaming is not supported for this model"}],"success":false}`)(w)
		default:
			writeOK(w, "plain")
		}
	})
	var waits []time.Duration
	c := f.streamClient(&waits)
	resp, err := chat(t, c)
	require.NoError(t, err)
	assert.Equal(t, "plain", resp.Content)
	assert.False(t, resp.Streamed)
	assert.Equal(t, 3, resp.Attempts, "three requests sent")
	assert.Empty(t, waits, "a fallback is not a retry")
	reqs := f.requests()
	require.Len(t, reqs, 3)
	assert.Contains(t, reqs[0].Body, "stream_options")
	assert.Equal(t, true, reqs[1].Body["stream"])
	assert.NotContains(t, reqs[1].Body, "stream_options")
	assert.NotContains(t, reqs[2].Body, "stream")

	// Only stream_options rejected: streamed without it (no usage).
	f = newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		b := f.requests()[n-1].Body
		if b["stream_options"] != nil {
			status(400, `{"error":"stream_options: unknown field"}`)(w)
			return
		}
		writeSSE(w, contentChunk("s"), finishChunk("stop"), sseDone)
	})
	resp, err = chat(t, f.streamClient(nil))
	require.NoError(t, err)
	assert.True(t, resp.Streamed)
	assert.Equal(t, 2, resp.Attempts)

	// A 400 about something else is not a fallback.
	f = newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		status(400, `{"error":{"message":"unknown model"}}`)(w)
	})
	_, err = chat(t, f.streamClient(nil))
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 400, e.Status)
	assert.Len(t, f.requests(), 1)

	// Without streaming, a 400 naming stream is just a 400.
	f = newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		status(400, `{"error":{"message":"stream is required"}}`)(w)
	})
	_, err = chat(t, f.client(nil))
	require.ErrorAs(t, err, &e)
	assert.Len(t, f.requests(), 1)
}

func TestNewStreamsByDefault(t *testing.T) {
	get := func(string) string { return "" }
	p := config.Provider{BaseURL: "http://x", Model: "m"}
	c, err := New("p", p, get)
	require.NoError(t, err)
	assert.True(t, c.Stream)
	assert.Equal(t, DefaultStreamTimeout, c.timeout(true))
	assert.Equal(t, 900*time.Second, DefaultStreamTimeout)
	assert.Equal(t, DefaultTimeout, c.timeout(false), "a fallback to no streaming gets the shorter default")
	assert.Equal(t, DefaultIdleTimeout, c.idleTimeout())
	assert.Equal(t, 90*time.Second, DefaultIdleTimeout)

	off := false
	p.Stream = &off
	p.Timeout = 300
	p.IdleTimeout = 20
	c, err = New("p", p, get)
	require.NoError(t, err)
	assert.False(t, c.Stream)
	assert.Equal(t, 300*time.Second, c.timeout(true), "an explicit timeout wins either way")
	assert.Equal(t, 20*time.Second, c.idleTimeout())
}

// chunkReader returns at most n bytes per Read.
type chunkReader struct {
	data []byte
	n    int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	k := min(r.n, len(p), len(r.data))
	copy(p, r.data[:k])
	r.data = r.data[k:]
	return k, nil
}

// FuzzReadStream: the parser never panics, and how the bytes are split
// across reads never changes the result.
func FuzzReadStream(f *testing.F) {
	f.Add([]byte(strings.Join(okStream("hello"), "")), uint8(1))
	f.Add([]byte(strings.ReplaceAll(strings.Join(okStream("crlf é"), ""), "\n", "\r\n")), uint8(2))
	f.Add([]byte(": ping\n\n"+contentChunk("a")+finishChunk("length")), uint8(3))
	f.Add([]byte("event: error\ndata: {\"error\":{\"message\":\"x\",\"code\":502}}\n\n"), uint8(5))
	f.Add([]byte("data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"p\"}]}}]}\ndata: [DONE]"), uint8(7))
	f.Add([]byte("data: {\"a\":1}\r\rdata:\r\n\r\ndata: [DONE]\r"), uint8(4))
	f.Fuzz(func(t *testing.T, data []byte, split uint8) {
		r1, e1 := readStream(bytes.NewReader(data))
		r2, e2 := readStream(&chunkReader{data: data, n: int(split%16) + 1})
		if (e1 == nil) != (e2 == nil) {
			t.Fatalf("whole: %v; split %d: %v", e1, split, e2)
		}
		if e1 != nil {
			if e1.Error() != e2.Error() {
				t.Fatalf("errors differ: %v vs %v", e1, e2)
			}
			return
		}
		if *r1 != *r2 {
			t.Fatalf("replies differ: %+v vs %+v", r1, r2)
		}
	})
}
