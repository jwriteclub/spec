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
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
)

// The connectivity harness: one tiny request and one JSON-mode request per
// provider, to prove the endpoint, the key and the response shape. It runs
// only with SPEC_INTEGRATION=1 (scripts/integration.sh sets it and loads the
// keys from SPEC_ENV_FILE); a provider whose key variable is unset is
// skipped. SPEC_IT_<NAME>_MODEL overrides a provider's model, for example
// SPEC_IT_GEMINI_MODEL, and SPEC_IT_<NAME>_EXTRA (a JSON object) replaces its
// extra; SPEC_IT_DEBUG=1 logs each response body. Keys are never logged: only model ids, token counts,
// latency and the first characters of each reply.

type liveProvider struct {
	name string
	cfg  config.Provider
	// maxTokens caps each reply: 16, or more for a model whose reasoning
	// cannot be turned off and spends its first tokens thinking.
	maxTokens int
}

// liveProviders mirrors the examples in `spec explain config`.
var liveProviders = []liveProvider{
	{"cloudflare", config.Provider{
		BaseURL:   "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/ai/v1",
		Model:     "@cf/zai-org/glm-5.3-flash",
		APIKeyEnv: "CLOUDFLARE_AI_TOKEN",
		JSONMode:  true,
		Extra:     map[string]any{"reasoning_effort": "none"},
	}, 256},
	{"deepseek", config.Provider{
		BaseURL:   "https://api.deepseek.com",
		Model:     "deepseek-flash",
		APIKeyEnv: "DEEPSEEK_API_KEY",
		JSONMode:  true,
		// Thinking stays on: with {"thinking": {"type": "disabled"}},
		// json_object replies came back broken in 3 of 4 runs
		// ({"ok":Hi}, {"ok":1}, a leaked end-of-thinking token).
	}, 128},
	{"openrouter", config.Provider{
		BaseURL:   "https://openrouter.ai/api/v1",
		Model:     "z-ai/glm-5.3-flash",
		APIKeyEnv: "OPENROUTER_API_KEY",
		JSONMode:  true,
		Extra:     map[string]any{"reasoning": map[string]any{"effort": "low"}},
	}, 64},
	{"openai", config.Provider{
		BaseURL:        "https://api.openai.com/v1",
		Model:          "gpt-5.4-nano",
		APIKeyEnv:      "OPENAI_API_KEY",
		JSONMode:       true,
		MaxTokensParam: "max_completion_tokens",
		Extra:          map[string]any{"reasoning_effort": "none"},
	}, 16},
	{"gemini", config.Provider{
		BaseURL:   "https://generativelanguage.googleapis.com/v1beta/openai",
		Model:     "gemini-flash-lite-latest",
		APIKeyEnv: "GEMINI_API_KEY",
		JSONMode:  true,
	}, 16},
	{"anthropic", config.Provider{
		BaseURL:   "https://api.anthropic.com/v1",
		Model:     "claude-haiku-5-5",
		APIKeyEnv: "ANTHROPIC_API_KEY",
	}, 16},
}

func TestIntegrationProviders(t *testing.T) {
	if os.Getenv("SPEC_INTEGRATION") != "1" {
		t.Skip("set SPEC_INTEGRATION=1 (scripts/integration.sh) to call real providers")
	}
	var mu sync.Mutex
	var total Usage
	t.Cleanup(func() {
		t.Logf("total tokens spent: %d (%d prompt + %d completion)", total.TotalTokens, total.PromptTokens, total.CompletionTokens)
	})
	for _, lp := range liveProviders {
		t.Run(lp.name, func(t *testing.T) {
			p := lp.cfg
			if m := os.Getenv("SPEC_IT_" + strings.ToUpper(lp.name) + "_MODEL"); m != "" {
				p.Model = m
			}
			if x := os.Getenv("SPEC_IT_" + strings.ToUpper(lp.name) + "_EXTRA"); x != "" {
				p.Extra = nil
				require.NoError(t, json.Unmarshal([]byte(x), &p.Extra), "SPEC_IT_%s_EXTRA is a JSON object", strings.ToUpper(lp.name))
			}
			if os.Getenv(p.APIKeyEnv) == "" {
				t.Skipf("%s is not set", p.APIKeyEnv)
			}
			c, err := New(lp.name, p, os.Getenv)
			require.NoError(t, err)
			c.Timeout = 60 * time.Second
			if os.Getenv("SPEC_IT_DEBUG") == "1" {
				c.HTTP = debugDoer{t: t, c: c}
			}
			ctx := context.Background()
			maxTokens := lp.maxTokens
			if n, err := strconv.Atoi(os.Getenv("SPEC_IT_MAX_TOKENS")); err == nil && n > 0 {
				maxTokens = n
			}
			spent := func(r *Response) {
				mu.Lock()
				total = total.Add(r.Usage)
				mu.Unlock()
			}

			if lp.name == "openai" {
				if ids, err := c.Models(ctx); err == nil {
					var nano []string
					for _, id := range ids {
						if strings.Contains(id, "nano") {
							nano = append(nano, id)
						}
					}
					t.Logf("openai nano-class models: %s", strings.Join(nano, ", "))
				} else {
					t.Logf("openai model list failed: %v", err)
				}
			}

			resp, err := c.Chat(ctx, Request{
				Messages:  []Message{User("Reply with the single word OK")},
				MaxTokens: maxTokens,
			})
			require.NoError(t, err)
			spent(resp)
			t.Logf("plain: model %s (echoed %q), streamed %v, %d+%d tokens, %s, %d attempt(s), finish %q, reply %q, reasoning %d bytes",
				resp.RequestedModel, resp.Model, resp.Streamed, resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
				resp.Latency.Round(time.Millisecond), resp.Attempts, resp.FinishReason, preview(c, resp.Content), len(resp.Reasoning))
			assert.Contains(t, strings.ToUpper(resp.Content), "OK")
			assert.Positive(t, resp.Usage.TotalTokens, "usage is reported")
			assert.True(t, resp.Streamed, "the reply was streamed (no fallback)")

			// The shape is what is under test: a JSON object with the asked
			// key. Small models sometimes answer {"ok": 1}; that still passes.
			var v map[string]any
			resp, err = c.ChatJSON(ctx, Request{
				Messages:  []Message{User(`Return exactly this JSON object and nothing else: {"ok": true}`)},
				MaxTokens: maxTokens,
			}, &v)
			if resp != nil {
				spent(resp)
				t.Logf("json: json_mode %v, streamed %v, %d+%d tokens, %s, finish %q, reply %q",
					resp.JSONMode, resp.Streamed, resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
					resp.Latency.Round(time.Millisecond), resp.FinishReason, preview(c, resp.Content))
			}
			require.NoError(t, err)
			assert.Contains(t, v, "ok")
		})
	}
}

// debugDoer logs each response body (redacted, shortened); never request
// headers.
type debugDoer struct {
	t *testing.T
	c *Client
}

func (d debugDoer) Do(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return resp, err
	}
	raw, rerr := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if rerr == nil {
		d.t.Logf("debug: HTTP %d: %s", resp.StatusCode, d.c.bodySnippet(raw))
	}
	return resp, nil
}

// preview is the start of a reply, redacted, for the log.
func preview(c *Client, s string) string {
	s = c.redact(s)
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}

// TestIntegrationClaudeCLI sends the same two tiny requests through the
// installed Claude Code CLI on the developer's own login, at low effort. It
// is skipped when claude is not on PATH. SPEC_IT_CLAUDE_MODEL overrides the
// model (default haiku).
func TestIntegrationClaudeCLI(t *testing.T) {
	if os.Getenv("SPEC_INTEGRATION") != "1" {
		t.Skip("set SPEC_INTEGRATION=1 (scripts/integration.sh) to call real providers")
	}
	model := os.Getenv("SPEC_IT_CLAUDE_MODEL")
	if model == "" {
		model = "haiku"
	}
	c, err := NewClaudeCLI("claude", config.Provider{Kind: config.KindClaudeCLI, Model: model, Effort: "low"}, os.Getenv)
	var se *SetupError
	if errors.As(err, &se) {
		t.Skipf("%v", se)
	}
	require.NoError(t, err)
	c.Timeout = 60 * time.Second
	ctx := context.Background()

	resp, err := c.Chat(ctx, Request{Messages: []Message{User("Reply with the single word OK")}})
	require.NoError(t, err)
	t.Logf("plain: model %s (echoed %q), %d+%d tokens, $%.6f, %s, finish %q, reply %q",
		resp.RequestedModel, resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.CostUSD,
		resp.Latency.Round(time.Millisecond), resp.FinishReason, resp.Content)
	assert.Contains(t, strings.ToUpper(resp.Content), "OK")
	assert.Positive(t, resp.Usage.TotalTokens)
	assert.NotEmpty(t, resp.Model)

	var v map[string]any
	resp, err = ChatJSON(ctx, c, Request{Messages: []Message{User(`Return exactly this JSON object and nothing else: {"ok": true}`)}}, &v)
	require.NoError(t, err)
	t.Logf("json: %d+%d tokens, $%.6f, %s, reply %q", resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.CostUSD,
		resp.Latency.Round(time.Millisecond), resp.Content)
	assert.Contains(t, v, "ok")
}

// TestIntegrationCodexCLI sends the same two tiny requests through the
// installed Codex CLI on the developer's own ChatGPT login, at effort
// none. It is skipped when codex is not on PATH. SPEC_IT_CODEX_MODEL
// overrides the model (default gpt-6-luna) and SPEC_IT_CODEX_COMMAND the
// executable (for a newer Codex that knows gpt-6.1-terra).
func TestIntegrationCodexCLI(t *testing.T) {
	if os.Getenv("SPEC_INTEGRATION") != "1" {
		t.Skip("set SPEC_INTEGRATION=1 (scripts/integration.sh) to call real providers")
	}
	model := os.Getenv("SPEC_IT_CODEX_MODEL")
	if model == "" {
		model = "gpt-6-luna"
	}
	p := config.Provider{Kind: config.KindCodexCLI, Model: model, Effort: "none", Command: os.Getenv("SPEC_IT_CODEX_COMMAND")}
	c, err := NewCodexCLI("codex", p, os.Getenv)
	var se *SetupError
	if errors.As(err, &se) {
		t.Skipf("%v", se)
	}
	require.NoError(t, err)
	c.Timeout = 60 * time.Second
	ctx := context.Background()

	resp, err := c.Chat(ctx, Request{Messages: []Message{User("Reply with the single word OK")}})
	require.NoError(t, err)
	t.Logf("plain: model %s, %d+%d tokens, %s, reply %q",
		resp.RequestedModel, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Latency.Round(time.Millisecond), resp.Content)
	assert.Contains(t, strings.ToUpper(resp.Content), "OK")
	assert.Positive(t, resp.Usage.TotalTokens)

	var v map[string]any
	resp, err = ChatJSON(ctx, c, Request{Messages: []Message{User(`Return exactly this JSON object and nothing else: {"ok": true}`)}}, &v)
	require.NoError(t, err)
	t.Logf("json: %d+%d tokens, %s, reply %q", resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		resp.Latency.Round(time.Millisecond), resp.Content)
	assert.Contains(t, v, "ok")
}
