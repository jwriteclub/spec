// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
)

// fakeClaudeVar selects the test binary's fake-claude behaviour; the
// provider passes it through because the test config names it in pass_env.
const fakeClaudeVar = "SPEC_FAKE_CLAUDE"

// TestMain lets the test binary stand in for `claude` and `codex`: with
// SPEC_FAKE_CLAUDE (SPEC_FAKE_CODEX) set it runs fakeClaude (fakeCodex)
// instead of the tests.
func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeClaudeVar); mode != "" {
		os.Exit(fakeClaude(mode))
	}
	if mode := os.Getenv(fakeCodexVar); mode != "" {
		os.Exit(fakeCodex(mode))
	}
	os.Exit(m.Run())
}

// fakeSeen is what the fake reports, as its result text, about how it ran.
type fakeSeen struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
	Cwd   string   `json:"cwd"`
	Env   []string `json:"env"`
}

// claudeJSON is a result object shaped like Claude Code 2.1's.
func claudeJSON(result string, mut func(map[string]any)) string {
	m := map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "num_turns": 1,
		"result": result, "stop_reason": "end_turn", "total_cost_usd": 0.000123,
		"api_error_status": nil, "permission_denials": []any{},
		"usage": map[string]any{
			"input_tokens": 2, "cache_creation_input_tokens": 604, "cache_read_input_tokens": 30, "output_tokens": 4,
		},
		"modelUsage": map[string]any{
			"claude-haiku-5-5":   map[string]any{"outputTokens": 4, "costUSD": 0.0001},
			"claude-helper-tiny": map[string]any{"outputTokens": 1},
		},
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func apiError(status any, msg string) func(map[string]any) {
	return func(m map[string]any) {
		m["is_error"] = true
		m["api_error_status"] = status
		m["result"] = msg
		m["total_cost_usd"] = 0
		m["modelUsage"] = map[string]any{}
		m["stop_reason"] = "stop_sequence"
	}
}

func fakeClaude(mode string) int {
	in, _ := io.ReadAll(os.Stdin)
	switch mode {
	case "echo":
		cwd, _ := os.Getwd()
		seen, _ := json.Marshal(fakeSeen{Args: os.Args[1:], Stdin: string(in), Cwd: cwd, Env: os.Environ()})
		fmt.Println(claudeJSON(string(seen), nil))
	case "fenced":
		fmt.Println(claudeJSON("Here it is:\n```json\n{\"answer\": 42}\n```", nil))
	case "prose":
		fmt.Println(claudeJSON("I cannot answer in JSON.", nil))
	case "truncated":
		fmt.Println(claudeJSON("{\"answer\":", func(m map[string]any) { m["stop_reason"] = "max_tokens" }))
	case "notloggedin":
		fmt.Println(claudeJSON("", apiError(nil, "Not logged in · Please run /login")))
		return 1
	case "ratelimit":
		fmt.Println(claudeJSON("", apiError(429, "You've hit your limit · resets 5pm")))
		return 1
	case "overloaded":
		fmt.Println(claudeJSON("", apiError(529, "API Error: 529 Overloaded")))
		return 1
	case "badmodel":
		fmt.Println(claudeJSON("", apiError(404, "There's an issue with the selected model (nope). It may not exist or you may not have access to it.")))
		fmt.Fprintln(os.Stderr, `[claude-code:unrecognized_model] {"model":"nope"}`)
		return 1
	case "maxturns":
		fmt.Println(claudeJSON("", func(m map[string]any) {
			m["subtype"] = "error_max_turns"
			delete(m, "result")
			m["num_turns"] = 2
		}))
	case "unknownflag":
		fmt.Fprintln(os.Stderr, "error: unknown option '--max-turns'")
		return 1
	case "crash":
		fmt.Fprintln(os.Stderr, "Segmentation fault")
		return 139
	case "garbage":
		fmt.Println("Hello! How can I help?")
	case "slow":
		time.Sleep(time.Minute)
	default:
		return 99
	}
	return 0
}

// fakeEnv is the environment spec "runs in" for these tests.
func fakeEnv(mode string) func(string) string {
	vars := map[string]string{
		"HOME":              "/home/tester",
		"PATH":              "/usr/bin:/bin",
		"LANG":              "en_US.UTF-8",
		"CLAUDE_CONFIG_DIR": "/home/tester/.claude-alt",
		"ANTHROPIC_API_KEY": "sk-ant-should-not-pass",
		"CLAUDECODE":        "1",
		"CLAUDE_EFFORT":     "max",
		fakeClaudeVar:       mode,
	}
	return func(k string) string { return vars[k] }
}

func fakeClaudeProvider(t *testing.T, mode string) *ClaudeCLI {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	p, err := Open("claude", config.Provider{
		Kind: config.KindClaudeCLI, Command: exe, Model: "haiku", TriageModel: "sonnet",
		Effort: "low", PassEnv: []string{fakeClaudeVar},
	}, fakeEnv(mode))
	require.NoError(t, err)
	c, ok := p.(*ClaudeCLI)
	require.True(t, ok)
	return c
}

func TestClaudeCLIArgvStdinEnvAndResponse(t *testing.T) {
	c := fakeClaudeProvider(t, "echo")
	resp, err := c.Chat(context.Background(), Request{
		Messages:    []Message{System("You check specs."), User("the spec text")},
		MaxTokens:   16384,
		JSON:        true,
		Temperature: f64(0),
	})
	require.NoError(t, err)
	var seen fakeSeen
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	assert.Equal(t, []string{
		"-p", "--output-format", "json", "--model", "haiku", "--system-prompt=You check specs.",
		"--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "",
		"--disable-slash-commands", "--no-session-persistence", "--max-turns", "1", "--effort", "low",
	}, seen.Args)
	assert.Equal(t, "the spec text", seen.Stdin, "the user prompt goes on stdin, never argv")
	assert.Contains(t, seen.Cwd, "spec-cli-", "a fresh temporary directory")
	assert.ElementsMatch(t, []string{
		"HOME=/home/tester", "PATH=/usr/bin:/bin", "LANG=en_US.UTF-8", "CLAUDE_CONFIG_DIR=/home/tester/.claude-alt",
		fakeClaudeVar + "=echo", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	}, seen.Env, "a minimal environment: no API key, no parent session variables")

	assert.Equal(t, "claude", resp.Provider)
	assert.Equal(t, "haiku", resp.RequestedModel)
	assert.Equal(t, "claude-haiku-5-5", resp.Model, "the model that wrote most of the reply")
	assert.Equal(t, Usage{PromptTokens: 636, CompletionTokens: 4, TotalTokens: 640}, resp.Usage)
	assert.InDelta(t, 0.000123, resp.CostUSD, 1e-9)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, 1, resp.Attempts)
	assert.False(t, resp.Streamed)
	assert.False(t, resp.JSONMode)
	assert.Positive(t, resp.Latency)
}

func TestClaudeCLIOverridesAndTriage(t *testing.T) {
	c := fakeClaudeProvider(t, "echo")
	assert.Equal(t, Info{Name: "claude", Kind: "claude-cli", Model: "haiku", TriageModel: "sonnet", Effort: "low", Transport: c.Path}, c.Info())
	assert.Nil(t, c.RunawayExtra())

	resp, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}, Triage: true})
	require.NoError(t, err)
	var seen fakeSeen
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	assert.Equal(t, "sonnet", resp.RequestedModel)
	assert.Contains(t, seen.Args, "--system-prompt="+cliDefaultSystem, "Claude Code's long default prompt is always replaced")

	c.SetModel("opus")
	c.SetEffort("max")
	c.SetLimits(5*time.Second, 3)
	assert.Equal(t, 5*time.Second, c.Timeout)
	resp, err = c.Chat(context.Background(), Request{Messages: []Message{User("x")}, Triage: true})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	assert.Equal(t, "opus", resp.RequestedModel)
	assert.Equal(t, []string{"--effort", "max"}, seen.Args[len(seen.Args)-2:], "--effort wins over the config")
	assert.Equal(t, "max", c.Info().Effort)

	c.Effort, c.EffortOverride = "", ""
	assert.NotContains(t, c.Args("m", "s"), "--effort", "no effort set: Claude Code's default")
}

func TestClaudeCLIConversationIsFlattened(t *testing.T) {
	c := fakeClaudeProvider(t, "echo")
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{
		System("sys one"), User("first"), {Role: "assistant", Content: "not json"}, User("answer in JSON"), System("sys two"),
	}})
	require.NoError(t, err)
	var seen fakeSeen
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	assert.Equal(t, "first\n\n<your_previous_reply>\nnot json\n</your_previous_reply>\n\nanswer in JSON", seen.Stdin)
	assert.Contains(t, seen.Args, "--system-prompt=sys one\n\nsys two")
}

func TestClaudeCLIChatJSON(t *testing.T) {
	var v struct {
		Answer int `json:"answer"`
	}
	resp, err := ChatJSON(context.Background(), fakeClaudeProvider(t, "fenced"), Request{Messages: []Message{User("x")}}, &v)
	require.NoError(t, err)
	assert.Equal(t, 42, v.Answer, "the lenient extractor finds the object in a fence")
	assert.NotNil(t, resp)

	resp, err = ChatJSON(context.Background(), fakeClaudeProvider(t, "prose"), Request{Messages: []Message{User("x")}}, &v)
	var je *JSONError
	require.ErrorAs(t, err, &je)
	require.NotNil(t, resp, "the response comes back for its usage")
	assert.Equal(t, 4, resp.Usage.CompletionTokens)

	resp, err = ChatJSON(context.Background(), fakeClaudeProvider(t, "truncated"), Request{Messages: []Message{User("x")}}, &v)
	require.ErrorAs(t, err, &je)
	assert.Equal(t, "length", resp.FinishReason, "max_tokens maps to length")
}

func TestClaudeCLIErrors(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		status  int
		message string
		fix     string
	}{
		{"notloggedin", 0, "Not logged in", "claude auth login"},
		{"ratelimit", 429, "hit your limit", "usage or rate limit"},
		{"overloaded", 529, "Overloaded", "overloaded"},
		{"badmodel", 404, "selected model", "alias (haiku, sonnet, opus)"},
		{"maxturns", 0, "error_max_turns", "tried to use a tool"},
		{"unknownflag", 0, "unknown option '--max-turns'", "claude update"},
		{"crash", 0, "Segmentation fault", "run `claude -p` in a terminal"},
		{"garbage", 0, "Hello! How can I help?", "--output-format json"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			_, err := fakeClaudeProvider(t, tc.mode).Chat(context.Background(), Request{Messages: []Message{User("x")}})
			var e *Error
			require.ErrorAs(t, err, &e)
			assert.Equal(t, tc.status, e.Status)
			assert.Contains(t, e.Message, tc.message)
			assert.Contains(t, e.Fix(), tc.fix)
			assert.False(t, e.Retryable, "Claude Code retries API errors itself")
			assert.Equal(t, 1, e.Attempts)
			assert.True(t, strings.HasPrefix(e.Error(), "provider claude (model haiku): claude -p"), e.Error())
			if tc.status != 0 {
				assert.Contains(t, e.Error(), fmt.Sprintf("API error %d", tc.status))
			}
		})
	}
}

func TestClaudeCLITimeoutAndCancel(t *testing.T) {
	c := fakeClaudeProvider(t, "slow")
	c.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, e.Fix(), "--effort low")
	assert.Less(t, time.Since(start), 10*time.Second)

	c.Timeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err = c.Chat(ctx, Request{Messages: []Message{User("x")}})
	assert.True(t, errors.Is(err, context.Canceled), "the caller's cancellation comes back as is: %v", err)
	assert.False(t, errors.As(err, &e))
}

func TestClaudeCLISetup(t *testing.T) {
	_, err := Open("claude", config.Provider{Kind: config.KindClaudeCLI, Model: "haiku", Command: "no-such-claude"}, fakeEnv("echo"))
	var se *SetupError
	require.ErrorAs(t, err, &se)
	assert.Contains(t, se.Error(), `provider "claude": cannot run Claude Code: no-such-claude was not found on PATH`)
	assert.Contains(t, se.Fix(), "install Claude Code")

	_, err = NewClaudeCLI("claude", config.Provider{Kind: config.KindClaudeCLI, Model: "haiku"}, func(string) string { return "" })
	require.ErrorAs(t, err, &se, "the default command, claude, on an empty PATH")

	_, err = fakeClaudeProvider(t, "echo").Chat(context.Background(), Request{})
	assert.ErrorContains(t, err, "at least one message")
}

func TestClientEffort(t *testing.T) {
	cl := &Client{Name: "x", Model: "m", BaseURL: "https://api.example.com/v1", Effort: "low"}
	body := func(req Request) map[string]any {
		raw, err := cl.Body(req)
		require.NoError(t, err)
		var b map[string]any
		require.NoError(t, json.Unmarshal(raw, &b))
		return b
	}
	req := Request{Messages: []Message{User("x")}}
	assert.Equal(t, "low", body(req)["reasoning_effort"], "the config's effort is sent as reasoning_effort")

	cl.Extra = map[string]any{"reasoning_effort": "none"}
	assert.Equal(t, "none", body(req)["reasoning_effort"], "an extra that sets it wins over effort")

	cl.TriageExtra = map[string]any{"reasoning_effort": "minimal"}
	cl.SetEffort("high")
	assert.Equal(t, "high", body(req)["reasoning_effort"], "--effort wins over extra")
	assert.Equal(t, "high", body(Request{Messages: req.Messages, Triage: true, Extra: map[string]any{"reasoning_effort": "low"}})["reasoning_effort"], "and over triage_extra and the request's extra")

	cl.Effort, cl.EffortOverride, cl.Extra = "", "", nil
	_, set := body(req)["reasoning_effort"]
	assert.False(t, set, "no effort anywhere: no field")

	assert.Equal(t, Info{Name: "x", Kind: "openai", Model: "m", Transport: "https://api.example.com"}, cl.Info())
	cl.SetModel("n")
	cl.SetLimits(time.Second, 5)
	assert.Equal(t, "n", cl.TriageModel)
	assert.Equal(t, 5, cl.Attempts)
	assert.Equal(t, map[string]any{"reasoning_effort": "minimal"}, cl.RunawayExtra())
}
