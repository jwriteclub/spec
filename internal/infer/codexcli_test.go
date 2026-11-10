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

// fakeCodexVar selects the test binary's fake-codex behaviour; the provider
// passes it through because the test config names it in pass_env.
const fakeCodexVar = "SPEC_FAKE_CODEX"

// codexSeen is what the fake codex reports, as its reply, about how it ran.
type codexSeen struct {
	Args         []string `json:"args"`
	Stdin        string   `json:"stdin"`
	Cwd          string   `json:"cwd"`
	Files        []string `json:"files"`
	Instructions string   `json:"instructions"`
	Env          []string `json:"env"`
}

// codexEvents prints a run's JSONL events the way Codex 0.155 does: the
// thread, the turn, the items, then the turn's end.
func codexEvents(items []map[string]any, end map[string]any) {
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]any{"type": "thread.started", "thread_id": "01a1-test"})
	_ = enc.Encode(map[string]any{"type": "turn.started"})
	for i, it := range items {
		it["id"] = fmt.Sprintf("item_%d", i)
		_ = enc.Encode(map[string]any{"type": "item.completed", "item": it})
	}
	if end != nil {
		_ = enc.Encode(end)
	}
}

func codexMessage(text string) map[string]any {
	return map[string]any{"type": "agent_message", "text": text}
}

func codexDone() map[string]any {
	return map[string]any{"type": "turn.completed", "usage": map[string]any{
		"input_tokens": 1688, "cached_input_tokens": 1024, "cache_write_input_tokens": 0, "output_tokens": 5, "reasoning_output_tokens": 0,
	}}
}

// codexFailed ends a turn with the error text Codex reports.
func codexFailed(text string) map[string]any {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "error", "message": text})
	return map[string]any{"type": "turn.failed", "error": map[string]any{"message": text}}
}

func fakeCodex(mode string) int {
	in, _ := io.ReadAll(os.Stdin)
	switch mode {
	case "echo":
		cwd, _ := os.Getwd()
		entries, _ := os.ReadDir(".")
		files := []string{}
		for _, e := range entries {
			files = append(files, e.Name())
		}
		instr, _ := os.ReadFile(codexInstructionsFile)
		seen, _ := json.Marshal(codexSeen{Args: os.Args[1:], Stdin: string(in), Cwd: cwd, Files: files, Instructions: string(instr), Env: os.Environ()})
		codexEvents([]map[string]any{
			{"type": "error", "message": "Model metadata for `x` not found. Defaulting to fallback metadata."},
			{"type": "reasoning", "text": "thinking"},
			codexMessage(string(seen)),
		}, codexDone())
	case "fenced":
		codexEvents([]map[string]any{codexMessage("Here it is:\n```json\n{\"answer\": 42}\n```")}, codexDone())
	case "prose":
		codexEvents([]map[string]any{codexMessage("I cannot answer in JSON.")}, codexDone())
	case "tools":
		codexEvents([]map[string]any{
			{"type": "command_execution", "command": "ls", "status": "declined"},
			codexMessage("first"), codexMessage("final"),
		}, codexDone())
	case "notloggedin":
		codexEvents(nil, codexFailed("unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: https://api.openai.com/v1/responses"))
		return 1
	case "ratelimit":
		codexEvents(nil, codexFailed(`{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"You've hit your usage limit. Try again at 5:00 PM."}}`))
		return 1
	case "badmodel":
		codexEvents([]map[string]any{{"type": "error", "message": "Model metadata for `nope` not found."}},
			codexFailed(`{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"The 'nope' model is not supported when using Codex with a ChatGPT account.","code":"model_not_found"}}`))
		return 1
	case "badeffort":
		codexEvents(nil, codexFailed(`{"type":"error","error":{"message":"Unsupported value: 'minimal' is not supported with the 'gpt-6-luna' model.","type":"invalid_request_error","param":"reasoning.effort","code":"unsupported_value"},"status":400}`))
		return 1
	case "overloaded":
		codexEvents(nil, codexFailed("unexpected status 503 Service Unavailable: overloaded"))
		return 1
	case "strict":
		fmt.Fprintln(os.Stderr, "Error loading config.toml: unknown configuration field `agents.enabled` in -c/--config override")
		return 1
	case "unknownflag":
		fmt.Fprintln(os.Stderr, "error: unexpected argument '--strict-config' found")
		return 2
	case "crash":
		fmt.Fprintln(os.Stderr, "thread 'main' panicked")
		return 101
	case "garbage":
		fmt.Println("Hello! How can I help?")
	case "slow":
		time.Sleep(time.Minute)
	default:
		return 99
	}
	return 0
}

// fakeCodexEnv is the environment spec "runs in" for these tests.
func fakeCodexEnv(mode string) func(string) string {
	vars := map[string]string{
		"HOME":           "/home/tester",
		"PATH":           "/usr/bin:/bin",
		"LANG":           "en_US.UTF-8",
		"CODEX_HOME":     "/home/tester/.codex-alt",
		"OPENAI_API_KEY": "sk-should-not-pass",
		"CODEX_API_KEY":  "sk-should-not-pass-either",
		"CLAUDECODE":     "1",
		fakeCodexVar:     mode,
	}
	return func(k string) string { return vars[k] }
}

func fakeCodexProvider(t *testing.T, mode string) *CodexCLI {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	p, err := Open("codex", config.Provider{
		Kind: config.KindCodexCLI, Command: exe, Model: "gpt-6.1-terra", TriageModel: "gpt-6-luna",
		Effort: "low", PassEnv: []string{fakeCodexVar},
	}, fakeCodexEnv(mode))
	require.NoError(t, err)
	c, ok := p.(*CodexCLI)
	require.True(t, ok)
	return c
}

func TestCodexCLIArgvStdinEnvAndResponse(t *testing.T) {
	c := fakeCodexProvider(t, "echo")
	resp, err := c.Chat(context.Background(), Request{
		Messages:    []Message{System("You check specs."), User("the spec text")},
		MaxTokens:   16384,
		JSON:        true,
		Temperature: f64(0),
	})
	require.NoError(t, err)
	var seen codexSeen
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	assert.Equal(t, []string{
		"exec", "--json", "--color", "never", "--strict-config", "--ignore-user-config", "--ignore-rules",
		"--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "-m", "gpt-6.1-terra",
		"-c", `model_instructions_file="spec-instructions.md"`,
		"-c", `approval_policy="never"`,
		"-c", `web_search="disabled"`,
		"-c", "project_doc_max_bytes=0",
		"-c", `history.persistence="none"`,
		"-c", "analytics.enabled=false",
		"-c", "check_for_update_on_startup=false",
		"-c", `model_reasoning_summary="none"`,
		"-c", "hide_agent_reasoning=true",
		"-c", "include_permissions_instructions=false",
		"-c", "include_apps_instructions=false",
		"-c", "include_collaboration_mode_instructions=false",
		"-c", "include_environment_context=false",
		"-c", "skills.include_instructions=false",
		"-c", "skills.bundled.enabled=false",
		"-c", "tools.experimental_request_user_input.enabled=false",
		"-c", "agents.enabled=false",
		"--disable", "shell_tool", "--disable", "multi_agent", "--disable", "apps", "--disable", "plugins",
		"--disable", "remote_plugin", "--disable", "hooks", "--disable", "image_generation", "--disable", "view_image",
		"--disable", "browser_use", "--disable", "computer_use", "--disable", "goals", "--disable", "skill_search",
		"--disable", "tool_suggest", "--disable", "sleep_tool", "--disable", "shell_snapshot",
		"-c", `model_reasoning_effort="low"`,
		"-",
	}, seen.Args)
	assert.Equal(t, "the spec text", seen.Stdin, "the user prompt goes on stdin, never argv")
	assert.Equal(t, "You check specs.", seen.Instructions, "the system prompt is the instructions file")
	assert.Equal(t, []string{codexInstructionsFile}, seen.Files, "nothing else in the working directory")
	assert.Contains(t, seen.Cwd, "spec-cli-", "a fresh temporary directory")
	assert.ElementsMatch(t, []string{
		"HOME=/home/tester", "PATH=/usr/bin:/bin", "LANG=en_US.UTF-8", "CODEX_HOME=/home/tester/.codex-alt",
		fakeCodexVar + "=echo",
	}, seen.Env, "a minimal environment: no API keys, no parent session variables")

	assert.Equal(t, "codex", resp.Provider)
	assert.Equal(t, "gpt-6.1-terra", resp.RequestedModel)
	assert.Empty(t, resp.Model, "codex exec echoes no model id")
	assert.Equal(t, Usage{PromptTokens: 1688, CompletionTokens: 5, TotalTokens: 1693}, resp.Usage)
	assert.Zero(t, resp.CostUSD)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, 1, resp.Attempts)
	assert.False(t, resp.Streamed)
	assert.False(t, resp.JSONMode)
	assert.Positive(t, resp.Latency)
}

func TestCodexCLIOverridesAndTriage(t *testing.T) {
	c := fakeCodexProvider(t, "echo")
	assert.Equal(t, Info{Name: "codex", Kind: "codex-cli", Model: "gpt-6.1-terra", TriageModel: "gpt-6-luna", Effort: "low", Transport: c.Path}, c.Info())
	assert.Nil(t, c.RunawayExtra())

	resp, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}, Triage: true})
	require.NoError(t, err)
	var seen codexSeen
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	assert.Equal(t, "gpt-6-luna", resp.RequestedModel)
	assert.Equal(t, cliDefaultSystem, seen.Instructions, "Codex's built-in instructions are always replaced")

	c.SetModel("gpt-6-sol")
	c.SetEffort("none")
	c.SetLimits(5*time.Second, 3)
	assert.Equal(t, 5*time.Second, c.Timeout)
	resp, err = c.Chat(context.Background(), Request{Messages: []Message{User("x")}, Triage: true})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	assert.Equal(t, "gpt-6-sol", resp.RequestedModel)
	assert.Equal(t, []string{"-c", `model_reasoning_effort="none"`, "-"}, seen.Args[len(seen.Args)-3:], "--effort wins over the config")
	assert.Equal(t, "none", c.Info().Effort)

	c.Effort, c.EffortOverride = "", ""
	assert.NotContains(t, strings.Join(c.Args("m"), " "), "model_reasoning_effort", "no effort set: the model's default")
}

func TestCodexCLIConversationIsFlattened(t *testing.T) {
	c := fakeCodexProvider(t, "echo")
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{
		System("sys one"), User("first"), {Role: "assistant", Content: "not json"}, User("answer in JSON"), System("sys two"),
	}})
	require.NoError(t, err)
	var seen codexSeen
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	assert.Equal(t, "first\n\n<your_previous_reply>\nnot json\n</your_previous_reply>\n\nanswer in JSON", seen.Stdin)
	assert.Equal(t, "sys one\n\nsys two", seen.Instructions)
}

func TestCodexCLIReplyAndToolItems(t *testing.T) {
	resp, err := fakeCodexProvider(t, "tools").Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.NoError(t, err)
	assert.Equal(t, "final", resp.Content, "the last agent message is the reply")

	r, events := readCodexEvents([]byte("not json\n{\"type\":\"turn.started\"}\n{\"type\":\"error\",\"message\":\"Reconnecting... 1/5\"}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"mcp_tool_call\"}}\n{\"type\":\"turn.completed\"}\n"))
	assert.Equal(t, 4, events, "a line that is not an event is skipped")
	assert.Equal(t, []string{"Reconnecting... 1/5"}, r.notices, "an error event is a notice")
	assert.Equal(t, []string{"mcp_tool_call"}, r.tools)
	assert.True(t, r.completed)
	assert.Equal(t, 1, r.turns)
}

func TestCodexCLIChatJSON(t *testing.T) {
	var v struct {
		Answer int `json:"answer"`
	}
	resp, err := ChatJSON(context.Background(), fakeCodexProvider(t, "fenced"), Request{Messages: []Message{User("x")}}, &v)
	require.NoError(t, err)
	assert.Equal(t, 42, v.Answer, "the lenient extractor finds the object in a fence")
	assert.NotNil(t, resp)

	resp, err = ChatJSON(context.Background(), fakeCodexProvider(t, "prose"), Request{Messages: []Message{User("x")}}, &v)
	var je *JSONError
	require.ErrorAs(t, err, &je)
	require.NotNil(t, resp, "the response comes back for its usage")
	assert.Equal(t, 5, resp.Usage.CompletionTokens)
}

func TestCodexCLIErrors(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		status  int
		message string
		fix     string
	}{
		{"notloggedin", 401, "Missing bearer", "codex login"},
		{"ratelimit", 429, "hit your usage limit", "usage or rate limit"},
		{"badmodel", 400, "model is not supported", "codex debug models"},
		{"badeffort", 400, "'minimal' is not supported", "effort level"},
		{"overloaded", 503, "Service Unavailable", "retry later"},
		{"strict", 0, "unknown configuration field `agents.enabled`", "codex update"},
		{"unknownflag", 0, "unexpected argument '--strict-config'", "does not accept a flag"},
		{"crash", 0, "panicked", "run `codex exec` in a terminal"},
		{"garbage", 0, "Hello! How can I help?", "codex exec --json"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			_, err := fakeCodexProvider(t, tc.mode).Chat(context.Background(), Request{Messages: []Message{User("x")}})
			var e *Error
			require.ErrorAs(t, err, &e)
			assert.Equal(t, tc.status, e.Status)
			assert.Contains(t, e.Message, tc.message)
			assert.Contains(t, e.Fix(), tc.fix)
			assert.False(t, e.Retryable, "Codex retries API errors itself")
			assert.Equal(t, 1, e.Attempts)
			assert.True(t, strings.HasPrefix(e.Error(), "provider codex (model gpt-6.1-terra): codex exec"), e.Error())
		})
	}
}

func TestCodexCLITimeoutAndCancel(t *testing.T) {
	c := fakeCodexProvider(t, "slow")
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

func TestCodexCLISetup(t *testing.T) {
	_, err := Open("codex", config.Provider{Kind: config.KindCodexCLI, Model: "gpt-6-luna", Command: "no-such-codex"}, fakeCodexEnv("echo"))
	var se *SetupError
	require.ErrorAs(t, err, &se)
	assert.Contains(t, se.Error(), `provider "codex": cannot run Codex: no-such-codex was not found on PATH`)
	assert.Contains(t, se.Fix(), "codex login")

	_, err = NewCodexCLI("codex", config.Provider{Kind: config.KindCodexCLI, Model: "gpt-6-luna"}, func(string) string { return "" })
	require.ErrorAs(t, err, &se, "the default command, codex, on an empty PATH")

	_, err = fakeCodexProvider(t, "echo").Chat(context.Background(), Request{})
	assert.ErrorContains(t, err, "at least one message")
}
