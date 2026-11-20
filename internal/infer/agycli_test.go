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

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
)

// fakeAgyVar selects the test binary's fake-agy behaviour; the provider passes
// it through because the test config names it in pass_env.
const fakeAgyVar = "SPEC_FAKE_AGY"

// agyJSON is a result payload shaped like `agy --output-format json` (1.5.13),
// which is also the payload of the stream-json "result" event.
func agyJSON(response string, mut func(map[string]any)) string {
	m := map[string]any{
		"conversation_id":  "conv-test-123",
		"status":           "SUCCESS",
		"response":         response,
		"duration_seconds": 1.25,
		"num_turns":        1,
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
		m["usage"] = map[string]any{
			"input_tokens":      0,
			"output_tokens":     0,
			"thinking_tokens":   0,
			"cache_read_tokens": 0,
			"total_tokens":      0,
		}
	})
}

// agyStream is stdout shaped like `agy --output-format stream-json` (per
// antigravity.google/docs/cli/headless): an init event, the given step
// updates, and a result event whose payload is result (agyJSON's shape).
func agyStream(result string, steps ...string) string {
	lines := []string{`{"event":"init","conversation_id":"conv-test-123","init":{"cwd":"/tmp/x","tools":["ask_permission","run_command","write_to_file"],"permission_mode":"request-review","model":"gemini-4-neon-medium"}}`,
		`{"event":"step_update","step_update":{"conversation_id":"conv-test-123","step_index":0,"state":"DONE","step_type":"user_input"}}`}
	lines = append(lines, steps...)
	lines = append(lines, `{"event":"result","result":`+result+`}`)
	return strings.Join(lines, "\n")
}

func fakeAgy(mode string) int {
	in, _ := io.ReadAll(os.Stdin)
	switch mode {
	case "echo":
		cwd, _ := os.Getwd()
		seen, _ := json.Marshal(fakeSeen{Args: os.Args[1:], Stdin: string(in), Cwd: cwd, Env: os.Environ()})
		fmt.Println(agyStream(agyJSON(string(seen), nil),
			`{"event":"step_update","step_update":{"conversation_id":"conv-test-123","step_index":2,"state":"DONE","step_type":"agent_response","text_delta":"ignored"}}`))
	case "fenced":
		fmt.Println(agyStream(agyJSON("Here it is:\n```json\n{\"answer\": 42}\n```", nil)))
	case "prose":
		fmt.Println(agyStream(agyJSON("I cannot answer in JSON.", nil)))
	case "truncated":
		fmt.Println(agyStream(agyJSON("{\"answer\":", nil)))
	case "tool":
		// A tool step reported ACTIVE then DONE, a subagent step, and a
		// soft-deny notice on stderr: the run still succeeds.
		fmt.Fprintln(os.Stderr, "Tool write_to_file was denied: approval is required in headless mode; add write_file(...) to permissions.allow")
		fmt.Println(agyStream(agyJSON("done", nil),
			`{"event":"step_update","step_update":{"conversation_id":"conv-test-123","step_index":3,"state":"ACTIVE","step_type":"tool","tool_name":"run_command"}}`,
			`{"event":"step_update","step_update":{"conversation_id":"conv-test-123","step_index":3,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"echo hi"},"output":"hi"}}}`,
			`{"event":"step_update","step_update":{"conversation_id":"conv-test-123","step_index":4,"state":"DONE","step_type":"invoke","subagent_info":{"subagents":[]}}}`))
	case "notloggedin":
		fmt.Println(agyStream(agyErrorJSON("HTTP 401 Unauthenticated: not logged in")))
		return 1
	case "ratelimit":
		fmt.Println(agyStream(agyErrorJSON("status 429 RESOURCE_EXHAUSTED: rate limit exceeded")))
		return 1
	case "badmodel":
		// The message from antigravity.google/docs/cli/headless, which names
		// --effort and lists "Available models:" but is a model problem.
		fmt.Println(agyStream(agyErrorJSON("invalid model selection (--model \"nope\" --effort \"\"): model nope is not recognized as a known model or custom model in settings\nAvailable models:\n Gemini 3.6 Flash (High)")))
		return 1
	case "noeffort":
		// The per-model effort check from antigravity.google/docs/cli/commands/effort.
		fmt.Println(agyStream(agyErrorJSON(`gemini-4-neon-medium has no "max" effort (available: low, medium, high)`)))
		return 1
	case "servererr":
		fmt.Println(agyStream(agyErrorJSON("status 500 internal error: backend unavailable")))
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
		fmt.Println(`{"event":"result","result":{"response":"missing status field"}}`)
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

// agyLockdown is the argv every run starts with, before --effort.
var agyLockdown = []string{
	"--input-format", "stream-json",
	"--output-format", "stream-json",
	"--model", "gemini-4-neon-medium",
	"--mode", "plan",
	"--disable-slash-commands",
	"--sandbox",
}

// agyStdinPrompt decodes the fake's stdin: exactly one stream-json user event
// on one line. It returns the event's content.
func agyStdinPrompt(t *testing.T, stdin string) string {
	t.Helper()
	require.True(t, strings.HasSuffix(stdin, "\n"), "the event line ends with a newline")
	require.Equal(t, 1, strings.Count(stdin, "\n"), "one event, one line")
	var ev struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdin), &ev))
	assert.Equal(t, "user", ev.Event)
	return ev.Message.Content
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
	assert.Equal(t, append(append([]string{}, agyLockdown...), "--effort", "low"), seen.Args)
	assert.NotContains(t, seen.Args, "-p", "stream-json input takes no -p")
	assert.NotContains(t, seen.Args, "auto", "never auto mode")
	assert.Equal(t, expectedPrompt, agyStdinPrompt(t, seen.Stdin), "the prompt goes on stdin as a user event")
	assert.Contains(t, seen.Stdin, "<system_instructions>", "no HTML escaping of the prompt")
	assert.Contains(t, seen.Cwd, "spec-cli-", "a fresh temporary directory")
	assert.ElementsMatch(t, []string{
		"HOME=/home/tester", "PATH=/usr/bin:/bin", "LANG=en_US.UTF-8", "XDG_CONFIG_HOME=/home/tester/.config-alt",
		fakeAgyVar + "=echo",
	}, seen.Env, "a minimal environment: no API keys unless in pass_env")

	assert.Equal(t, "agy", resp.Provider)
	assert.Equal(t, "gemini-4-neon-medium", resp.RequestedModel)
	assert.Empty(t, resp.Model, "agy echoes only the model it was given")
	// input_tokens (16054) already includes cache_read_tokens (11451); 16054 + 63 == 16117.
	assert.Equal(t, Usage{PromptTokens: 16054, CompletionTokens: 63, TotalTokens: 16117}, resp.Usage)
	assert.Zero(t, resp.CostUSD)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, 1, resp.Attempts)
	assert.False(t, resp.Streamed)
	assert.False(t, resp.JSONMode)
	assert.Positive(t, resp.Latency)
}

// TestAgyCLIHugePromptStaysOffArgv sends a prompt past Linux's 128 KiB limit
// on one argument (MAX_ARG_STRLEN): it all goes on stdin and argv stays small.
func TestAgyCLIHugePromptStaysOffArgv(t *testing.T) {
	c := fakeAgyProvider(t, "echo")
	huge := strings.Repeat("The queue MUST hold at most 8 items. <x> & \"y\"\n", 5000)
	require.Greater(t, len(huge), 128<<10)
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{System("sys"), User(huge)}})
	require.NoError(t, err)
	var seen fakeSeen
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	total := 0
	for _, a := range seen.Args {
		assert.Less(t, len(a), 256, "no prompt text in argv: %.40q", a)
		total += len(a)
	}
	assert.Less(t, total, 1024, "the whole argv stays small")
	got := agyStdinPrompt(t, seen.Stdin)
	assert.True(t, strings.HasSuffix(got, huge), "the whole prompt arrives on stdin")
}

func TestAgyCLIArgsLockdownAndEffort(t *testing.T) {
	c := &AgyCLI{cliBase: cliBase{Name: "agy", Model: "gemini-4-neon-medium"}}
	args := c.Args("gemini-4-neon-medium")
	assert.Equal(t, agyLockdown, args, "no effort set: omit --effort")

	c.Effort = "high"
	args = c.Args("gemini-4-neon-medium")
	assert.Equal(t, append(append([]string{}, agyLockdown...), "--effort", "high"), args)
}

func TestAgyCLIInputLeadingDash(t *testing.T) {
	in, err := agyInput("sys", "-leading-dash")
	require.NoError(t, err)
	assert.Contains(t, agyStdinPrompt(t, string(in)), "-leading-dash", "a prompt starting with - is data on stdin, never a flag")
}

func TestAgyCLIToolUseWarns(t *testing.T) {
	// polychromatic logs nothing below panic by default: raise the level to
	// warn for this test, discard the text, and read the entries from a hook.
	oldLevel, oldOut, oldHooks := lg.Logger.GetLevel(), lg.Logger.Out, lg.Logger.ReplaceHooks(logrus.LevelHooks{})
	lg.Logger.SetLevel(logrus.WarnLevel)
	lg.Logger.SetOutput(io.Discard)
	hook := logtest.NewLocal(lg.Logger)
	t.Cleanup(func() {
		lg.Logger.SetLevel(oldLevel)
		lg.Logger.SetOutput(oldOut)
		lg.Logger.ReplaceHooks(oldHooks)
	})
	warnings := func() string {
		var out []string
		for _, e := range hook.AllEntries() {
			if e.Level == logrus.WarnLevel {
				out = append(out, e.Message)
			}
		}
		return strings.Join(out, "\n")
	}

	resp, err := fakeAgyProvider(t, "tool").Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.NoError(t, err)
	assert.Equal(t, "done", resp.Content, "a tool step is warned about, not an error")
	out := warnings()
	assert.Contains(t, out, "agy ran 1 turns and 2 tool steps (run_command, subagent) with 1 permission notices")
	assert.Contains(t, out, "agy notice: Tool write_to_file was denied")

	hook.Reset()
	_, err = fakeAgyProvider(t, "fenced").Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.NoError(t, err)
	assert.NotContains(t, warnings(), "tool steps", "a plain reply warns about nothing")
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
	assert.Contains(t, seen.Args, "gemini-3.8-flash-low")
	assert.Contains(t, agyStdinPrompt(t, seen.Stdin), cliDefaultSystem, "default system instructions included when none provided")

	c.SetModel("gemini-3.8-flash-medium")
	c.SetEffort("max")
	c.SetLimits(5*time.Second, 3)
	assert.Equal(t, 5*time.Second, c.Timeout)
	resp, err = c.Chat(context.Background(), Request{Messages: []Message{User("x")}, Triage: true})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	assert.Equal(t, "gemini-3.8-flash-medium", resp.RequestedModel)
	assert.Equal(t, []string{"--effort", "max"}, seen.Args[len(seen.Args)-2:], "--effort wins over the config")
	assert.Equal(t, "max", c.Info().Effort)

	c.Effort, c.EffortOverride = "", ""
	assert.NotContains(t, c.Args("m"), "--effort", "no effort set: agy's default")
}

func TestAgyCLIConversationIsFlattened(t *testing.T) {
	c := fakeAgyProvider(t, "echo")
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{
		System("sys one"), User("first"), {Role: "assistant", Content: "not json"}, User("answer in JSON"), System("sys two"),
	}})
	require.NoError(t, err)
	var seen fakeSeen
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &seen))
	expected := "<system_instructions>\nsys one\n\nsys two\n\nDo not invoke any tools or modify files; answer directly.\n</system_instructions>\n\nfirst\n\n<your_previous_reply>\nnot json\n</your_previous_reply>\n\nanswer in JSON"
	assert.Equal(t, expected, agyStdinPrompt(t, seen.Stdin))
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
	assert.Equal(t, "stop", resp.FinishReason, "agy documents no finish reason, so a cut-off reply is not reported as length")
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
		{"noeffort", 0, `has no "max" effort`, "does not offer that effort level"},
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
