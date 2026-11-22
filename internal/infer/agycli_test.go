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

// agyJSON is a result payload shaped like `agy --output-format json`, which
// is also the payload of the stream-json "result" event. The usage is from a
// live agy 1.5.14 run whose model turn read 7358 tokens from the cache:
// input_tokens excludes them, and total_tokens is input + output.
func agyJSON(response string, mut func(map[string]any)) string {
	m := map[string]any{
		"conversation_id":  "conv-test-123",
		"status":           "SUCCESS",
		"response":         response,
		"duration_seconds": 1.25,
		"num_turns":        1,
		"usage": map[string]any{
			"input_tokens":      2127,
			"output_tokens":     67,
			"thinking_tokens":   0,
			"cache_read_tokens": 7358,
			"total_tokens":      2194,
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

// agyStep is one step_update line of a stream-json run.
func agyStep(index int, state, stepType, extra string) string {
	return fmt.Sprintf(`{"event":"step_update","step_update":{"conversation_id":"conv-test-123","step_index":%d,"state":%q,"step_type":%q%s}}`, index, state, stepType, extra)
}

// agyStepUsage is the usage field of the live step agyJSON's usage came from.
const agyStepUsage = `,"usage":{"input_tokens":2127,"output_tokens":67,"thinking_tokens":0,"cache_read_tokens":7358,"total_tokens":2194}`

// agyHang prints lines, then waits as agy 1.5.13 did for an approval it
// could not get in headless mode: only a kill ends it.
func agyHang(lines ...string) {
	fmt.Println(strings.Join(lines, "\n"))
	time.Sleep(time.Minute)
}

func fakeAgy(mode string) int {
	in, _ := io.ReadAll(os.Stdin)
	switch mode {
	case "echo":
		cwd, _ := os.Getwd()
		seen, _ := json.Marshal(fakeSeen{Args: os.Args[1:], Stdin: string(in), Cwd: cwd, Env: os.Environ()})
		fmt.Println(agyStream(agyJSON(string(seen), nil),
			agyStep(1, "ACTIVE", "agent_response", `,"text_delta":"ignored"`),
			agyStep(1, "DONE", "agent_response", `,"text_delta":"\n"`+agyStepUsage)))
	case "fenced":
		fmt.Println(agyStream(agyJSON("Here it is:\n```json\n{\"answer\": 42}\n```", nil)))
	case "prose":
		fmt.Println(agyStream(agyJSON("I cannot answer in JSON.", nil)))
	case "truncated":
		fmt.Println(agyStream(agyJSON("{\"answer\":", nil)))
	case "tool":
		// The live agy 1.5.13 hang: a model turn, a tool step that starts,
		// and then nothing while agy waits for an approval.
		agyHang(`{"event":"init","conversation_id":"conv-test-123","init":{"tools":["run_command"],"permission_mode":"request-review"}}`,
			agyStep(0, "DONE", "user_input", ""),
			agyStep(1, "DONE", "agent_response", agyStepUsage),
			agyStep(2, "ACTIVE", "tool", `,"tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"ls -la","BypassSandbox":true}}`))
	case "subagent":
		agyHang(agyStep(0, "DONE", "user_input", ""), agyStep(1, "ACTIVE", "invoke", `,"subagent_info":{"subagents":[]}`))
	case "waiting":
		agyHang(agyStep(0, "DONE", "user_input", ""), agyStep(1, "WAITING", "agent_response", ""))
	case "secondturn":
		// Two model turns with no tool step between them, as a Stop hook
		// that asks for a continuation would cause.
		agyHang(agyStep(0, "DONE", "user_input", ""),
			agyStep(1, "DONE", "agent_response", `,"text_delta":"{}"`+agyStepUsage),
			agyStep(2, "ACTIVE", "agent_response", `,"text_delta":"more"`))
	case "concat":
		// Live agy 1.5.14 (no hang this time): text before a tool call, the
		// refused tool, and a second turn; the result's response joins both
		// turns' text into something that is not one JSON object.
		fmt.Println(agyStream(agyJSON("{\"part\": \"ALPHA\"\n{\"part\": \"BETA\"}\n", nil),
			agyStep(1, "ACTIVE", "agent_response", `,"text_delta":"{\"part\": \"ALPHA\""`),
			agyStep(1, "DONE", "agent_response", `,"text_delta":"\n","usage":{"input_tokens":9448,"output_tokens":41,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":9489}`),
			agyStep(2, "ACTIVE", "tool", `,"tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"ls"}}`),
			agyStep(2, "ERROR", "tool", `,"tool_name":"run_command"`),
			agyStep(3, "DONE", "agent_response", `,"text_delta":"{\"part\": \"BETA\"}\n","usage":{"input_tokens":2127,"output_tokens":7,"thinking_tokens":0,"cache_read_tokens":7405,"total_tokens":2134}`)))
	case "denied":
		// Live agy 1.5.14 soft deny without the deny rules: the turn ends
		// with an empty response and the refusal in denied_actions.
		fmt.Fprintln(os.Stderr, "jetski: no output produced \u2014 a tool required the \"command\" permission that headless mode cannot prompt for, so it was auto-denied.")
		fmt.Println(agyStream(agyJSON("", func(m map[string]any) {
			m["denied_actions"] = []map[string]string{{"action": "command", "display_name": "Bash"}}
		})))
	case "empty":
		fmt.Println(agyStream(agyJSON("", nil)))
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
	case "effortconflict":
		// Live agy 1.5.14: a model id that names its effort, with another --effort.
		fmt.Fprintln(os.Stderr, `error: invalid model selection (--model "gemini-3.8-flash-low" --effort "high"): --model gemini-3.8-flash-low conflicts with --effort=high`)
		fmt.Println(`{"event":"result","result":{"conversation_id":"","status":"ERROR","response":"","error":"invalid model selection (--model \"gemini-3.8-flash-low\" --effort \"high\"): --model gemini-3.8-flash-low conflicts with --effort=high","duration_seconds":0,"num_turns":0,"usage":{"input_tokens":0,"output_tokens":0,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":0},"finished_reason":"error"}}`)
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
	"--settings", `{"agentMode":"","toolPermission":"strict","allowNonWorkspaceAccess":false,"permissions":{"deny":["command(*)","read_file(*)","write_file(*)","read_url(*)","execute_url(*)","mcp(*)"]}}`,
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
	// From the step's usage, reported once on DONE: input 2127 plus 7358 cache reads.
	assert.Equal(t, Usage{PromptTokens: 9485, CompletionTokens: 67, TotalTokens: 9552}, resp.Usage)
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
	var settings struct {
		AgentMode      string `json:"agentMode"`
		ToolPermission string `json:"toolPermission"`
		Permissions    struct {
			Deny  []string `json:"deny"`
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal([]byte(agySettings), &settings), "--settings takes a JSON object")
	assert.Empty(t, settings.AgentMode, "default mode: never auto or accept-edits")
	assert.Equal(t, "strict", settings.ToolPermission)
	assert.ElementsMatch(t, []string{"command(*)", "read_file(*)", "write_file(*)", "read_url(*)", "execute_url(*)", "mcp(*)"}, settings.Permissions.Deny)
	assert.Empty(t, settings.Permissions.Allow)
	assert.NotContains(t, args, "--mode", "plan mode does nothing with slash commands disabled")

	c.Effort = "high"
	args = c.Args("gemini-4-neon-medium")
	assert.Equal(t, append(append([]string{}, agyLockdown...), "--effort", "high"), args)
}

func TestAgyCLIInputLeadingDash(t *testing.T) {
	in, err := agyInput("sys", "-leading-dash")
	require.NoError(t, err)
	assert.Contains(t, agyStdinPrompt(t, string(in)), "-leading-dash", "a prompt starting with - is data on stdin, never a flag")
}

// TestAgyCLIStopsAtOnce covers the live findings: a tool step that waits
// for an approval, a subagent, a waiting step and a second model turn each
// get the run killed as soon as the step starts, long before the timeout.
func TestAgyCLIStopsAtOnce(t *testing.T) {
	for _, tc := range []struct{ mode, what string }{
		{"tool", "a run_command tool step"},
		{"subagent", "a subagent step"},
		{"waiting", "a step that waits for input (agent_response)"},
		{"secondturn", "a second model turn"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			c := fakeAgyProvider(t, tc.mode)
			c.Timeout = 30 * time.Second
			start := time.Now()
			_, err := c.Chat(context.Background(), Request{Messages: []Message{User("x")}})
			var e *Error
			require.ErrorAs(t, err, &e)
			var stop *agyStop
			require.ErrorAs(t, err, &stop)
			assert.Less(t, time.Since(start), 10*time.Second, "killed at once, not at the timeout")
			assert.NotErrorIs(t, err, context.DeadlineExceeded)
			assert.Contains(t, e.Error(), "agy -p: agy started "+tc.what+", and spec allows one model turn and no tools, so spec stopped it")
			assert.Contains(t, e.Fix(), "never allows")
			assert.Contains(t, e.Fix(), "hooks.json")
			assert.Equal(t, 1, e.Attempts)
		})
	}
}

// TestAgyCLIConcatenatedTurnsNeverParse is the reply-assembly finding: when a
// run took a tool step, agy's result joins the text of every model turn,
// and spec's lenient JSON extraction then picked an inner object. The run is
// now an error at the tool step instead of a reply.
func TestAgyCLIConcatenatedTurnsNeverParse(t *testing.T) {
	var v map[string]any
	_, err := ChatJSON(context.Background(), fakeAgyProvider(t, "concat"), Request{Messages: []Message{User("x")}}, &v)
	var stop *agyStop
	require.ErrorAs(t, err, &stop)
	var je *JSONError
	assert.False(t, errors.As(err, &je), "never reaches the JSON parse")
	assert.Nil(t, v)
}

func TestAgyCLIEmptyOrDenied(t *testing.T) {
	_, err := fakeAgyProvider(t, "denied").Chat(context.Background(), Request{Messages: []Message{User("x")}})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Contains(t, e.Message, "agy refused 1 tool calls and ended the turn: jetski: no output produced")
	assert.Contains(t, e.Fix(), "never allows")

	_, err = fakeAgyProvider(t, "empty").Chat(context.Background(), Request{Messages: []Message{User("x")}})
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "agy reported success with an empty reply", e.Message)
}

// TestAgyCLIUsageSumsSteps uses the step usages of a live agy 1.5.14 run
// with two model turns (a refused tool between them): the result's
// input_tokens (11528) is the sum of the steps' input_tokens, and cache reads
// (7358, on the second turn) come on top.
func TestAgyCLIUsageSumsSteps(t *testing.T) {
	r := &agyRun{}
	for _, line := range []string{
		agyStep(1, "DONE", "agent_response", `,"usage":{"input_tokens":9401,"output_tokens":37,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":9438}`),
		agyStep(3, "ACTIVE", "agent_response", `,"text_delta":"x"`),
		agyStep(3, "DONE", "agent_response", `,"usage":{"input_tokens":2127,"output_tokens":67,"thinking_tokens":0,"cache_read_tokens":7358,"total_tokens":2194}`),
		`{"event":"result","result":{"status":"SUCCESS","response":"x","usage":{"input_tokens":11528,"output_tokens":104,"thinking_tokens":0,"cache_read_tokens":7358,"total_tokens":11632}}}`,
	} {
		// The second turn stops a real run; record its usage regardless.
		if err := r.event([]byte(line)); err != nil {
			var ev agyEvent
			require.NoError(t, json.Unmarshal([]byte(line), &ev))
			if ev.StepUpdate.Usage != nil {
				r.usage[ev.StepUpdate.StepIndex] = *ev.StepUpdate.Usage
			}
		}
	}
	assert.Equal(t, Usage{PromptTokens: 9401 + 2127 + 7358, CompletionTokens: 104, TotalTokens: 18990}, r.tokens())
	assert.Equal(t, r.result.Usage.InputTokens+r.result.Usage.CacheReadTokens, r.tokens().PromptTokens, "the steps add up to the result")

	// No step reported usage: the result's, with its cache reads.
	r = &agyRun{}
	require.NoError(t, r.event([]byte(`{"event":"result","result":{"status":"SUCCESS","response":"x","usage":{"input_tokens":2127,"output_tokens":67,"cache_read_tokens":7358}}}`)))
	assert.Equal(t, Usage{PromptTokens: 9485, CompletionTokens: 67, TotalTokens: 9552}, r.tokens())

	// Thinking is part of output_tokens (live: 671 output, 669 thinking, total 10078 = 9407 + 671).
	r = &agyRun{}
	require.NoError(t, r.event([]byte(agyStep(1, "DONE", "agent_response", `,"usage":{"input_tokens":9407,"output_tokens":671,"thinking_tokens":669,"cache_read_tokens":0,"total_tokens":10078}`))))
	assert.Equal(t, Usage{PromptTokens: 9407, CompletionTokens: 671, TotalTokens: 10078}, r.tokens())
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
	assert.Equal(t, 67, resp.Usage.CompletionTokens)

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
		{"effortconflict", 0, "conflicts with --effort=high", "already names its effort level"},
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
