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

// fakeAgyVar selects the test binary's fake-agy behaviour; the provider passes
// it through because the test config names it in pass_env.
const fakeAgyVar = "SPEC_FAKE_AGY"

// agyJSON is a JSON envelope shaped like `agy --output-format json` (1.5.13).
func agyJSON(response string, mut func(map[string]any)) string {
	m := map[string]any{
		"conversation_id":  "conv-test-123",
		"status":           "SUCCESS",
		"response":         response,
		"duration_seconds": 1.25,
		"num_turns":        2,
		"usage": map[string]any{
			"input_tokens":      16054,
			"output_tokens":     63,
			"thinking_tokens":   0,
			"cache_read_tokens": 11451,
			"total_tokens":      16117,
		},
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func agyErrorJSON(msg string) string {
	return agyJSON("", func(m map[string]any) {
		m["conversation_id"] = ""
		m["status"] = "ERROR"
		m["response"] = ""
		m["error"] = msg
		m["duration_seconds"] = 0
		m["num_turns"] = 0
		m["finished_reason"] = "error"
		m["usage"] = map[string]any{
			"input_tokens":      0,
			"output_tokens":     0,
			"thinking_tokens":   0,
			"cache_read_tokens": 0,
			"total_tokens":      0,
		}
	})
}

func fakeAgy(mode string) int {
	in, _ := io.ReadAll(os.Stdin)
	switch mode {
	case "echo":
		cwd, _ := os.Getwd()
		seen, _ := json.Marshal(fakeSeen{Args: os.Args[1:], Stdin: string(in), Cwd: cwd, Env: os.Environ()})
		fmt.Println(agyJSON(string(seen), nil))
	case "fenced":
		fmt.Println(agyJSON("Here it is:\n```json\n{\"answer\": 42}\n```", nil))
	case "prose":
		fmt.Println(agyJSON("I cannot answer in JSON.", nil))
	case "truncated":
		fmt.Println(agyJSON("{\"answer\":", func(m map[string]any) { m["finished_reason"] = "max_tokens" }))
	case "notloggedin":
		fmt.Println(agyErrorJSON("HTTP 401 Unauthenticated: not logged in"))
		return 1
	case "ratelimit":
		fmt.Println(agyErrorJSON("status 429 RESOURCE_EXHAUSTED: rate limit exceeded"))
		return 1
	case "badmodel":
		fmt.Println(agyErrorJSON("invalid model selection (nope): unknown model"))
		return 1
	case "servererr":
		fmt.Println(agyErrorJSON("status 500 internal error: backend unavailable"))
		return 1
	case "unknownflag":
		fmt.Fprintln(os.Stderr, "flags provided but not defined: -disable-slash-commands")
		return 2
	case "unknownflag_singular":
		fmt.Fprintln(os.Stderr, "flag provided but not defined: -sandbox")
		return 2
	case "crash":
		fmt.Fprintln(os.Stderr, "fatal error: runtime panic")
		return 1
	case "garbage":
		fmt.Println("Hello! How can I help?")
	case "nostatus":
		fmt.Println(`{"response":"missing status field"}`)
	case "slow":
		time.Sleep(time.Minute)
	default:
		return 99
	}
	return 0
}

// fakeAgyEnv is the environment spec "runs in" for these tests.
func fakeAgyEnv(mode string) func(string) string {
	vars := map[string]string{
		"HOME":              "/home/tester",
		"PATH":              "/usr/bin:/bin",
		"LANG":              "en_US.UTF-8",
		"XDG_CONFIG_HOME":   "/home/tester/.config-alt",
		"GEMINI_API_KEY":    "AIza-should-not-pass",
		"ANTHROPIC_API_KEY": "sk-ant-should-not-pass",
		"OPENAI_API_KEY":    "sk-should-not-pass",
		fakeAgyVar:          mode,
	}
	return func(k string) string { return vars[k] }
}

func fakeAgyProvider(t *testing.T, mode string) *AgyCLI {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	p, err := Open("agy", config.Provider{
		Kind: config.KindAgyCLI, Command: exe, Model: "gemini-4-neon-medium", TriageModel: "gemini-3.8-flash-low",
		Effort: "low", PassEnv: []string{fakeAgyVar},
	}, fakeAgyEnv(mode))
	require.NoError(t, err)
	c, ok := p.(*AgyCLI)
	require.True(t, ok)
	return c
}

func TestAgyCLIArgvEnvAndResponse(t *testing.T) {
	c := fakeAgyProvider(t, "echo")
	resp, err := c.Chat(context.Background(), Request{
		Messages:    []Message{System("You check specs."), User("the spec text")},
		MaxTokens:   16384,
		JSON:        true,
		Temperature: f64(0),
	})
	require.NoError(t, err)
	var seen fakeSeen
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	expectedPrompt := "<system_instructions>\nYou check specs.\n\nDo not invoke any tools or modify files; answer directly.\n</system_instructions>\n\nthe spec text"
	assert.Equal(t, []string{
		"--output-format", "json",
		"--model", "gemini-4-neon-medium",
		"--disable-slash-commands",
		"--sandbox",
		"--effort", "low",
		"-p=" + expectedPrompt,
	}, seen.Args)
	assert.Empty(t, seen.Stdin, "the prompt goes in -p=..., not stdin")
	assert.Contains(t, seen.Cwd, "spec-cli-", "a fresh temporary directory")
	assert.ElementsMatch(t, []string{
		"HOME=/home/tester", "PATH=/usr/bin:/bin", "LANG=en_US.UTF-8", "XDG_CONFIG_HOME=/home/tester/.config-alt",
		fakeAgyVar + "=echo",
	}, seen.Env, "a minimal environment: no API keys unless in pass_env")

	assert.Equal(t, "agy", resp.Provider)
	assert.Equal(t, "gemini-4-neon-medium", resp.RequestedModel)
	assert.Empty(t, resp.Model, "agy --output-format json echoes no model id")
	// input_tokens (16054) already includes cache_read_tokens (11451); 16054 + 63 == 16117.
	assert.Equal(t, Usage{PromptTokens: 16054, CompletionTokens: 63, TotalTokens: 16117}, resp.Usage)
	assert.Zero(t, resp.CostUSD)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, 1, resp.Attempts)
	assert.False(t, resp.Streamed)
	assert.False(t, resp.JSONMode)
	assert.Positive(t, resp.Latency)
}

func TestAgyCLIArgsDashPromptAndEffort(t *testing.T) {
	c := &AgyCLI{cliBase: cliBase{Name: "agy", Model: "gemini-4-neon-medium"}}
	args := c.Args("gemini-4-neon-medium", "sys", "-leading-dash")
	assert.NotContains(t, args, "--effort", "no effort set: omit --effort")
	last := args[len(args)-1]
	assert.True(t, strings.HasPrefix(last, "-p="), "-p= form prevents leading dash ambiguity")
	assert.Contains(t, last, "-leading-dash")

	c.Effort = "high"
	args = c.Args("gemini-4-neon-medium", "sys", "hi")
	assert.Equal(t, []string{"--effort", "high"}, args[len(args)-3:len(args)-1])
	assert.True(t, strings.HasPrefix(args[len(args)-1], "-p="), "-p= is always the last arg")
}

func TestAgyCLIOverridesAndTriage(t *testing.T) {
	c := fakeAgyProvider(t, "echo")
	assert.Equal(t, Info{
		Name: "agy", Kind: "agy-cli", Model: "gemini-4-neon-medium",
		TriageModel: "gemini-3.8-flash-low", Effort: "low", Transport: c.Path,
	}, c.Info())
	assert.Nil(t, c.RunawayExtra())

	resp, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}, Triage: true})
	require.NoError(t, err)
	var seen fakeSeen
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	assert.Equal(t, "gemini-3.8-flash-low", resp.RequestedModel)
	assert.Contains(t, seen.Args[len(seen.Args)-1], cliDefaultSystem, "default system instructions included when none provided")

	c.SetModel("gemini-3.8-flash-medium")
	c.SetEffort("max")
	c.SetLimits(5*time.Second, 3)
	assert.Equal(t, 5*time.Second, c.Timeout)
	resp, err = c.Chat(context.Background(), Request{Messages: []Message{User("x")}, Triage: true})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	assert.Equal(t, "gemini-3.8-flash-medium", resp.RequestedModel)
	assert.Equal(t, []string{"--effort", "max"}, seen.Args[len(seen.Args)-3:len(seen.Args)-1], "--effort wins over the config")
	assert.Equal(t, "max", c.Info().Effort)

	c.Effort, c.EffortOverride = "", ""
	assert.NotContains(t, c.Args("m", "s", "p"), "--effort", "no effort set: agy's default")
}

func TestAgyCLIConversationIsFlattened(t *testing.T) {
	c := fakeAgyProvider(t, "echo")
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{
		System("sys one"), User("first"), {Role: "assistant", Content: "not json"}, User("answer in JSON"), System("sys two"),
	}})
	require.NoError(t, err)
	var seen fakeSeen
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	expected := "-p=<system_instructions>\nsys one\n\nsys two\n\nDo not invoke any tools or modify files; answer directly.\n</system_instructions>\n\nfirst\n\n<your_previous_reply>\nnot json\n</your_previous_reply>\n\nanswer in JSON"
	assert.Equal(t, expected, seen.Args[len(seen.Args)-1])
}

func TestAgyCLIChatJSON(t *testing.T) {
	var v struct {
		Answer int `json:"answer"`
	}
	resp, err := ChatJSON(context.Background(), fakeAgyProvider(t, "fenced"), Request{Messages: []Message{User("x")}}, &v)
	require.NoError(t, err)
	assert.Equal(t, 42, v.Answer, "the lenient extractor finds the object in a fence")
	assert.NotNil(t, resp)

	resp, err = ChatJSON(context.Background(), fakeAgyProvider(t, "prose"), Request{Messages: []Message{User("x")}}, &v)
	var je *JSONError
	require.ErrorAs(t, err, &je)
	require.NotNil(t, resp, "the response comes back for its usage")
	assert.Equal(t, 63, resp.Usage.CompletionTokens)

	resp, err = ChatJSON(context.Background(), fakeAgyProvider(t, "truncated"), Request{Messages: []Message{User("x")}}, &v)
	require.ErrorAs(t, err, &je)
	assert.Equal(t, "length", resp.FinishReason, "max_tokens maps to length")
}

func TestAgyCLIErrors(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		status  int
		message string
		fix     string
	}{
		{"notloggedin", 401, "not logged in", "sign in"},
		{"ratelimit", 429, "rate limit", "usage or rate limit"},
		{"badmodel", 0, "invalid model selection", "agy models"},
		{"servererr", 500, "internal error", "retry later"},
		{"unknownflag", 0, "flags provided but not defined: -disable-slash-commands", "agy update"},
		{"unknownflag_singular", 0, "flag provided but not defined: -sandbox", "agy update"},
		{"crash", 0, "runtime panic", "run `agy -p=hello` in a terminal"},
		{"garbage", 0, "Hello! How can I help?", "agy update"},
		{"nostatus", 0, "missing status field", "agy update"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			_, err := fakeAgyProvider(t, tc.mode).Chat(context.Background(), Request{Messages: []Message{User("x")}})
			var e *Error
			require.ErrorAs(t, err, &e)
			assert.Equal(t, tc.status, e.Status)
			assert.Contains(t, e.Message, tc.message)
			assert.Contains(t, e.Fix(), tc.fix)
			assert.False(t, e.Retryable, "Antigravity CLI retries API errors itself")
			assert.Equal(t, 1, e.Attempts)
			assert.True(t, strings.HasPrefix(e.Error(), "provider agy (model gemini-4-neon-medium): agy -p"), e.Error())
			if tc.status != 0 {
				assert.Contains(t, e.Error(), fmt.Sprintf("API error %d", tc.status))
			}
		})
	}
}

func TestAgyCLITimeoutAndCancel(t *testing.T) {
	c := fakeAgyProvider(t, "slow")
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

func TestAgyCLISetup(t *testing.T) {
	_, err := Open("agy", config.Provider{Kind: config.KindAgyCLI, Model: "gemini-4-neon-medium", Command: "no-such-agy"}, fakeAgyEnv("echo"))
	var se *SetupError
	require.ErrorAs(t, err, &se)
	assert.Contains(t, se.Error(), `provider "agy": cannot run Antigravity CLI: no-such-agy was not found on PATH`)
	assert.Contains(t, se.Fix(), "Antigravity CLI")

	_, err = NewAgyCLI("agy", config.Provider{Kind: config.KindAgyCLI, Model: "gemini-4-neon-medium"}, func(string) string { return "" })
	require.ErrorAs(t, err, &se, "the default command, agy, on an empty PATH")

	_, err = fakeAgyProvider(t, "echo").Chat(context.Background(), Request{})
	assert.ErrorContains(t, err, "at least one message")
}
