// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer/cliproc"
)

// agyVia labels an agy-cli failure: headless (print) mode, which the docs
// name after -p; with stream-json input the prompt comes on stdin instead.
const agyVia = "agy -p"

// agyEnv names the variables passed to agy beyond cliproc.BaseEnv:
// XDG_CONFIG_HOME, so a login or configuration kept outside ~/.config is found.
// API keys stay out unless pass_env names them, so the user's login pays.
var agyEnv = []string{"XDG_CONFIG_HOME"}

// agyMode is the execution mode every run is pinned to. Antigravity has no
// documented mode in which nothing can edit files or run commands: since
// 1.5.7 an unpinned `agy -p` runs in auto mode, which approves workspace edits
// and safe commands by itself. plan is the one mode the docs describe as
// investigating with read-only tools before any edit, and --mode overrides the
// user's agentMode (antigravity.google/docs/cli/modes).
const agyMode = "plan"

// AgyCLI is an agy-cli provider: it runs the installed Antigravity CLI once
// per request, in headless mode with the prompt on stdin, on the user's own
// login (an Antigravity login needs no API key). Each run is pinned to plan
// mode with the terminal sandbox on and slash commands off; Args documents the
// flags and what Antigravity cannot turn off.
//
// Model and TriageModel are Antigravity model ids (`agy models` lists them);
// Effort and EffortOverride are Antigravity --effort levels.
type AgyCLI struct {
	cliBase
}

// NewAgyCLI builds the agy-cli provider called name: it finds the command on
// getenv("PATH") and builds the minimal environment. It does not exec agy
// during Open (selectProvider contract). A missing command is a *SetupError.
func NewAgyCLI(name string, p config.Provider, getenv func(string) string) (*AgyCLI, error) {
	base, err := newCLIBase(name, p, getenv, agyEnv, nil)
	if err != nil {
		return nil, &SetupError{
			Provider: name,
			Msg:      fmt.Sprintf("cannot run Antigravity CLI: %v", err),
			FixText:  "install the Antigravity CLI (`agy`) and log in, or set command to the agy executable's path",
		}
	}
	return &AgyCLI{cliBase: base}, nil
}

// Info describes the provider (Provider).
func (c *AgyCLI) Info() Info { return c.info(config.KindAgyCLI) }

// agyPrompt composes the system instructions and user prompt into the one
// user message agy receives: agy 1.5.x has no --system-prompt flag, so system
// instructions are wrapped explicitly above the prompt and instruct the model
// not to invoke tools.
func agyPrompt(system, prompt string) string {
	return "<system_instructions>\n" + system + "\n\nDo not invoke any tools or modify files; answer directly.\n</system_instructions>\n\n" + prompt
}

// agyUserEvent is one prompt in agy's stream-json input: a "user" event whose
// message content is the prompt text.
type agyUserEvent struct {
	Event   string `json:"event"`
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

// agyInput is the stdin of a run: one stream-json user event carrying the
// composed prompt, then end of input, so agy exits after that one turn.
func agyInput(system, prompt string) ([]byte, error) {
	ev := agyUserEvent{Event: "user"}
	ev.Message.Content = agyPrompt(system, prompt)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ev); err != nil {
		return nil, fmt.Errorf("encoding the agy prompt: %w", err)
	}
	return b.Bytes(), nil
}

// Args returns the argv (after the program name) for a request to model. The
// prompt goes on stdin as a stream-json user event, never in the arguments.
// The flags are in Antigravity's CLI reference and headless docs
// (antigravity.google/docs/cli/reference, /docs/cli/headless); the
// stream-json input, --mode and the effort check were not run against a live
// agy, while --output-format, --model, --disable-slash-commands, --sandbox and
// --effort were verified by the contributor on agy 1.5.13:
//
//	--input-format stream-json   read prompts as JSON lines on stdin; agy exits
//	                             once stdin closes and the turn ends
//	--output-format stream-json  JSON events on stdout: init, step updates
//	                             (tool steps included) and the result; the
//	                             stream-json input requires it
//	--model MODEL                the provider's model id (`agy models`)
//	--mode plan                  pin the execution mode over the user's
//	                             agentMode, so the auto default (workspace
//	                             edits and safe commands approved by
//	                             themselves) never applies
//	--disable-slash-commands     no skills or custom commands
//	--sandbox                    terminal commands, should any run, are
//	                             contained by agy's terminal sandbox (no effect
//	                             where that sandbox is unavailable)
//	--effort LEVEL               only when an effort is set
//
// What agy cannot turn off: there is no documented flag or config-dir variable
// to skip ~/.gemini/antigravity-cli/settings.json (toolPermission, permission
// rules, agentMode), the MCP servers, hooks and plugins under ~/.gemini/config,
// or custom agents; no flag removes the tool catalog; and every run is saved
// in agy's conversation history (resumable by conversation_id), since agy has
// no documented flag to skip it. A tool needing approval it cannot get is
// soft-denied with a notice on stderr; parse warns about tool steps and such
// notices.
//
// Run in an empty temporary directory, agy finds no project files. The
// contributor measured about 16k input tokens per call besides ours on agy
// 1.5.13 (Antigravity's agent prompt and tool catalog); that was before the
// stream-json input and --mode plan and has not been re-measured. json_mode,
// max_tokens and temperature have no counterpart: the prompt asks for JSON and
// the reply is parsed leniently.
func (c *AgyCLI) Args(model string) []string {
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--model", model,
		"--mode", agyMode,
		"--disable-slash-commands",
		"--sandbox",
	}
	if e := c.effort(); e != "" {
		args = append(args, "--effort", e)
	}
	return args
}

// Chat runs agy once for req (Provider). req.JSON, MaxTokens, Temperature and
// Extra have no agy counterpart and are ignored.
func (c *AgyCLI) Chat(ctx context.Context, req Request) (*Response, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("infer: a request needs at least one message")
	}
	model := modelFor(req, c.Model, c.TriageModel)
	system, prompt := cliPrompt(req.Messages)
	stdin, err := agyInput(system, prompt)
	if err != nil {
		return nil, err
	}
	lg.Debugf("%s: running %s with model %s, effort %q, a %d-byte system prompt and %d bytes on stdin", c.Name, c.Path, model, c.effort(), len(system), len(stdin))
	start := time.Now()
	res, err := cliproc.Run(ctx, cliproc.Command{
		Path:    c.Path,
		Args:    c.Args(model),
		Stdin:   stdin,
		Env:     c.Env,
		Timeout: c.timeout(),
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, c.runError(model, err)
	}
	return c.parse(model, res, time.Since(start))
}

// runError is a run that did not finish: a timeout, or agy would not start.
func (c *AgyCLI) runError(model string, err error) *Error {
	e := &Error{Provider: c.Name, Model: model, Via: agyVia, Attempts: 1, cause: err}
	switch {
	case errors.Is(err, cliproc.ErrTimeout):
		e.fix = fmt.Sprintf("the model did not finish within %s: lower the effort (effort = \"low\" in the provider, or --effort low), or raise the provider's timeout (seconds)", c.timeout())
	case errors.Is(err, cliproc.ErrOutputTooLarge):
		e.fix = "Antigravity CLI wrote far more than one reply; run `agy -p=hello` in a terminal to see what it prints"
	default:
		e.fix = "check that " + c.Path + " runs (`agy --version`), or set command to the agy executable's path"
	}
	return e
}

// agyResult is the payload of the stream's "result" event, the same shape
// as the `agy --output-format json` envelope.
type agyResult struct {
	ConversationID  string  `json:"conversation_id"`
	Status          string  `json:"status"`
	Response        string  `json:"response"`
	Error           string  `json:"error"`
	DurationSeconds float64 `json:"duration_seconds"`
	NumTurns        int     `json:"num_turns"`
	Usage           struct {
		InputTokens     int `json:"input_tokens"`
		OutputTokens    int `json:"output_tokens"`
		ThinkingTokens  int `json:"thinking_tokens"`
		CacheReadTokens int `json:"cache_read_tokens"`
		TotalTokens     int `json:"total_tokens"`
	} `json:"usage"`
}

// agyEvent is the part of an `agy --output-format stream-json` event we read.
type agyEvent struct {
	Event string `json:"event"`
	Init  *struct {
		Tools          []string `json:"tools"`
		PermissionMode string   `json:"permission_mode"`
		Model          string   `json:"model"`
	} `json:"init"`
	StepUpdate *struct {
		StepIndex    int             `json:"step_index"`
		StepType     string          `json:"step_type"`
		ToolName     string          `json:"tool_name"`
		SubagentInfo json.RawMessage `json:"subagent_info"`
	} `json:"step_update"`
	Result *agyResult `json:"result"`
}

// agyRun is what a run's events say.
type agyRun struct {
	// result is the last "result" event (nil when there was none).
	result *agyResult
	// permissionMode, tools and model are from the "init" event.
	permissionMode string
	tools          int
	model          string
	// toolSteps names each step that ran a tool or a subagent, once per step
	// index. spec asks for none.
	toolSteps []string
}

// readAgyEvents reads the JSON lines on stdout; a line that is not an event
// is skipped.
func readAgyEvents(out []byte) agyRun {
	var r agyRun
	seen := map[int]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), cliproc.MaxStdout)
	for sc.Scan() {
		var ev agyEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Event == "" {
			continue
		}
		switch {
		case ev.Event == "init" && ev.Init != nil:
			r.permissionMode = ev.Init.PermissionMode
			r.tools = len(ev.Init.Tools)
			r.model = ev.Init.Model
		case ev.Event == "step_update" && ev.StepUpdate != nil:
			st := ev.StepUpdate
			isSubagent := len(st.SubagentInfo) > 0 && string(st.SubagentInfo) != "null"
			if (st.StepType != "tool" && st.ToolName == "" && !isSubagent) || seen[st.StepIndex] {
				continue
			}
			seen[st.StepIndex] = true
			name := st.ToolName
			switch {
			case name == "" && isSubagent:
				name = "subagent"
			case name == "":
				name = st.StepType
			}
			r.toolSteps = append(r.toolSteps, name)
		case ev.Event == "result" && ev.Result != nil:
			r.result = ev.Result
		}
	}
	return r
}

// agyNotices returns the stderr lines that look like a permission notice: in
// headless mode a tool needing approval is soft-denied, and agy prints a
// notice naming the tool and how to allow it (the exact wording is not
// documented, so this matches loosely).
func agyNotices(stderr string) []string {
	var out []string
	for line := range strings.SplitSeq(stderr, "\n") {
		t := strings.TrimSpace(line)
		low := strings.ToLower(t)
		if t != "" && (strings.Contains(low, "denied") || strings.Contains(low, "permission") || strings.Contains(low, "not allowed")) {
			out = append(out, t)
		}
	}
	return out
}

// parse turns a finished run into a Response or an *Error.
func (c *AgyCLI) parse(model string, res *cliproc.Result, latency time.Duration) (*Response, error) {
	run := readAgyEvents(res.Stdout)
	lg.Debugf("%s: agy ran in permission mode %q with %d tools offered (model %q)", c.Name, run.permissionMode, run.tools, run.model)
	r := run.result
	if r == nil || r.Status == "" {
		return nil, c.outputError(model, res)
	}
	if r.Status != "SUCCESS" || res.ExitCode != 0 {
		return nil, c.resultError(model, r, res)
	}
	notices := agyNotices(res.Stderr)
	if len(run.toolSteps) > 0 || r.NumTurns > 1 || len(notices) > 0 {
		lg.Warnf("%s: agy ran %d turns and %d tool steps (%s) with %d permission notices; spec asks for one turn without tools", c.Name, r.NumTurns, len(run.toolSteps), strings.Join(run.toolSteps, ", "), len(notices))
		for _, n := range notices {
			lg.Warnf("%s: agy notice: %s", c.Name, shorten(redactBearer(n)))
		}
	}
	// usage.input_tokens already includes cache_read_tokens
	// (input_tokens + output_tokens == total_tokens); do not add cache_read_tokens again.
	u := Usage{
		PromptTokens:     r.Usage.InputTokens,
		CompletionTokens: r.Usage.OutputTokens,
	}
	return &Response{
		Content:        r.Response,
		Provider:       c.Name,
		RequestedModel: model,
		FinishReason:   "stop",
		Usage:          u.filled(),
		Latency:        latency,
		Attempts:       1,
	}, nil
}

// outputError is a run whose stdout has no result event: agy refused its
// arguments, or printed something else.
func (c *AgyCLI) outputError(model string, res *cliproc.Result) *Error {
	e := &Error{Provider: c.Name, Model: model, Via: agyVia, Attempts: 1}
	line := res.StderrLine()
	low := strings.ToLower(line)
	switch {
	case res.ExitCode != 0 && (strings.Contains(low, "flag provided but not defined") || strings.Contains(low, "flags provided but not defined") || strings.Contains(low, "unknown flag")):
		e.cause = fmt.Errorf("exit status %d", res.ExitCode)
		e.Message = shorten(redactBearer(line))
		e.fix = "this Antigravity CLI does not accept a flag spec passes; update it (`agy update`); `spec explain config` lists the flags"
	case res.ExitCode != 0:
		e.cause = fmt.Errorf("exit status %d", res.ExitCode)
		e.Message = shorten(redactBearer(line))
		e.fix = "run `agy -p=hello` in a terminal to see Antigravity CLI's own error"
	default:
		e.cause = errors.New("the output has no Antigravity CLI result event")
		e.Message = snippet([]byte(redactBearer(string(res.Stdout))))
		e.fix = "update Antigravity CLI (`agy update`); spec reads the events of --output-format stream-json"
	}
	return e
}

// agyStatus finds an HTTP status code in an agy error message.
var agyStatus = regexp.MustCompile(`(?i)(?:status|http|code|error)[:\s]+(\d{3})\b`)

func agyStatusCode(msg string) int {
	m := agyStatus.FindStringSubmatch(msg)
	if m == nil {
		return 0
	}
	code, err := strconv.Atoi(m[1])
	if err != nil || code < 400 || code >= 600 {
		return 0
	}
	return code
}

// resultError is a JSON result that reports a failure.
func (c *AgyCLI) resultError(model string, r *agyResult, res *cliproc.Result) *Error {
	msg := r.Error
	if msg == "" {
		msg = r.Response
	}
	if msg == "" {
		msg = res.StderrLine()
	}
	if msg == "" {
		msg = r.Status
	}
	status := agyStatusCode(msg)
	e := &Error{
		Provider: c.Name,
		Model:    model,
		Via:      agyVia,
		Attempts: 1,
		Status:   status,
		Message:  shorten(redactBearer(msg)),
	}
	e.fix = agyFix(status, msg)
	if e.Status == 0 && res.ExitCode != 0 {
		e.cause = fmt.Errorf("exit status %d", res.ExitCode)
	}
	return e
}

// agyFix says how to resolve a failure Antigravity CLI reported.
func agyFix(status int, msg string) string {
	low := strings.ToLower(msg)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(low, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("effort") && has("has no", "(available:"):
		return "this model does not offer that effort level: set effort (or --effort) to one the message lists, or pick a model that has it; `agy -p /effort` shows the levels of agy's current model"
	case status == 401 || status == 403 || has("unauthorized", "unauthenticated", "not logged in", "login", "authentication", "credential"):
		return "Antigravity CLI is not logged in, or its login expired: run `agy` to sign in and retry"
	case status == 429 || has("rate limit", "usage limit", "quota", "resource_exhausted", "too many requests"):
		return "your Antigravity usage or rate limit was reached: wait and retry, or use another provider with --provider NAME"
	case status == 404 || has("invalid model", "model not found", "unknown model", "does not exist"):
		return "Antigravity CLI does not know this model, or your account cannot use it: run `agy models` to list available model ids (a newer model may need `agy update`), or pass --engine"
	case status >= 500 || has("overloaded", "unavailable", "internal error", "server error"):
		return "Antigravity's API is failing and the CLI's own retries ran out: retry later, or use another provider with --provider NAME"
	}
	return "run `agy -p=hello` in a terminal to see Antigravity CLI's own error"
}
