// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
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

// agySettings is the session-only settings object every run passes to
// --settings (agy applies it over settings.json and never saves it). Deny
// rules win over every allow and ask rule and over toolPermission, and block
// a call at once instead of waiting for an approval headless mode cannot
// give: no command (in or out of the terminal sandbox), no file read or
// write anywhere, no web page and no MCP tool. agentMode "" is the default
// mode, so neither the auto nor the accept-edits mode approves anything, and
// toolPermission strict asks before any other tool action (which headless
// mode denies). Verified live on agy 1.5.14 (antigravity.google/docs/cli/permissions).
const agySettings = `{"agentMode":"","toolPermission":"strict","allowNonWorkspaceAccess":false,` +
	`"permissions":{"deny":["command(*)","read_file(*)","write_file(*)","read_url(*)","execute_url(*)","mcp(*)"]}}`

// AgyCLI is an agy-cli provider: it runs the installed Antigravity CLI once
// per request, in headless mode with the prompt on stdin, on the user's own
// login (an Antigravity login needs no API key). Each run denies every tool
// through session-only settings, and Chat kills it the moment a tool step
// starts; Args documents the flags and what Antigravity cannot turn off.
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
// Every flag below was checked live on agy 1.5.14 (`agy --help`, and
// antigravity.google/docs/cli/reference, /docs/cli/headless,
// /docs/cli/permissions):
//
//	--input-format stream-json   read prompts as JSON lines on stdin; agy runs
//	                             one turn per line and exits once stdin closes
//	--output-format stream-json  JSON events on stdout: init, step updates
//	                             (tool steps included, as they start) and the
//	                             result; the stream-json input requires it
//	--model MODEL                the provider's model id (`agy models`)
//	--settings JSON              agySettings, for this session only: deny every
//	                             command, file read and write, web page and MCP
//	                             tool, default agent mode, strict tool
//	                             permission
//	--disable-slash-commands     a prompt starting with / is text, not a skill
//	--sandbox                    terminal commands, were any allowed, would run
//	                             in agy's terminal sandbox
//	--effort LEVEL               only when an effort is set; a model id that
//	                             already names a level (gemini-3.8-flash-low)
//	                             accepts only that level
//
// --mode is not passed: plan mode has no effect with slash commands disabled
// (agy says so on stderr), and the deny rules make it unnecessary.
//
// The deny rules block a tool call, but the model may still try one, and a
// tool outside them would wait for an approval headless mode cannot give.
// So Chat watches the event stream and kills agy the moment a tool or
// subagent step starts, a step waits, or a second model turn begins.
//
// What agy cannot turn off: no flag or variable skips the user's global
// hooks (~/.gemini/config/hooks.json), which still run and may push the model
// toward tools (the watch then stops the run), plugins, or custom agents; no
// flag removes the tool catalog (about 60 tools are offered); and every run
// is saved in agy's conversation history (resumable by conversation_id). A
// separate HOME loses the login, so that is no way out either.
//
// Run in an empty temporary directory, agy finds no project files. Measured
// on agy 1.5.14 with gemini-3.8-flash-low, a run carries about 9,400 input
// tokens besides ours (Antigravity's agent prompt and tool catalog), against
// about 640 for claude-cli and 1,690 for codex-cli, and takes 3 to 6 s for a
// one-word reply. json_mode, max_tokens and temperature have no counterpart:
// the prompt asks for JSON and the reply is parsed leniently.
func (c *AgyCLI) Args(model string) []string {
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--model", model,
		"--settings", agySettings,
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
	run := &agyRun{}
	start := time.Now()
	res, err := cliproc.Run(ctx, cliproc.Command{
		Path:    c.Path,
		Args:    c.Args(model),
		Stdin:   stdin,
		Env:     c.Env,
		Timeout: c.timeout(),
		Watch:   run.event,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var stop *agyStop
		if errors.As(err, &stop) {
			return nil, c.stopError(model, stop)
		}
		return nil, c.runError(model, err)
	}
	return c.parse(model, res, run, time.Since(start))
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

// agyStop is why Chat killed a run early: the stream showed a step spec
// never allows, which could otherwise wait for an approval until the timeout.
type agyStop struct {
	// what describes the step ("a run_command tool step").
	what string
}

func (s *agyStop) Error() string {
	return "agy started " + s.what + ", and spec allows one model turn and no tools, so spec stopped it"
}

// stopError is a run Chat killed because of an agyStop.
func (c *AgyCLI) stopError(model string, stop *agyStop) *Error {
	return &Error{
		Provider: c.Name, Model: model, Via: agyVia, Attempts: 1,
		cause: stop,
		fix: "the model tried to use a tool, which spec never allows (agy is run with every tool denied): retry, or use another model (--engine); " +
			"a hook in ~/.gemini/config/hooks.json or a plugin that tells the agent to run commands causes this too (`agy plugin list`, /hooks in agy)",
	}
}

// agyUsage is a token count as agy reports it, on a step and on the
// result. Measured on agy 1.5.14: input_tokens excludes cache_read_tokens,
// output_tokens includes thinking_tokens, and total_tokens is
// input_tokens + output_tokens.
type agyUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

// agyResult is the payload of the stream's "result" event, the same shape
// as the `agy --output-format json` envelope.
type agyResult struct {
	ConversationID  string   `json:"conversation_id"`
	Status          string   `json:"status"`
	Response        string   `json:"response"`
	Error           string   `json:"error"`
	DurationSeconds float64  `json:"duration_seconds"`
	NumTurns        int      `json:"num_turns"`
	Usage           agyUsage `json:"usage"`
	// DeniedActions lists the tools agy refused for want of an approval.
	DeniedActions []json.RawMessage `json:"denied_actions"`
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
		State        string          `json:"state"`
		StepType     string          `json:"step_type"`
		ToolName     string          `json:"tool_name"`
		SubagentInfo json.RawMessage `json:"subagent_info"`
		Usage        *agyUsage       `json:"usage"`
	} `json:"step_update"`
	Result *agyResult `json:"result"`
}

// agyRun is what a run's events say, built line by line as agy writes them.
type agyRun struct {
	// result is the last "result" event (nil when there was none).
	result *agyResult
	// permissionMode, tools and model are from the "init" event.
	permissionMode string
	tools          int
	model          string
	// responseStep is the step index of the one model turn, once started.
	responseStep int
	started      bool
	// usage is each step's usage by step index (the last report wins).
	usage map[int]agyUsage
}

// event reads one line of stdout (cliproc.Command.Watch). It returns an
// *agyStop when the step it reports is one spec never allows: a tool or
// subagent step, a step that waits (for an approval or an answer), or a
// second model turn. Each would mean more than one model turn, and a tool
// needing an approval can hang in headless mode instead of being denied (seen
// on agy 1.5.13), so the run is stopped as soon as the step starts. A line
// that is not an event is skipped.
func (r *agyRun) event(line []byte) error {
	var ev agyEvent
	if json.Unmarshal(line, &ev) != nil || ev.Event == "" {
		return nil
	}
	switch {
	case ev.Event == "init" && ev.Init != nil:
		r.permissionMode = ev.Init.PermissionMode
		r.tools = len(ev.Init.Tools)
		r.model = ev.Init.Model
	case ev.Event == "step_update" && ev.StepUpdate != nil:
		st := ev.StepUpdate
		isSubagent := len(st.SubagentInfo) > 0 && string(st.SubagentInfo) != "null"
		switch {
		case st.StepType == "tool" || st.ToolName != "":
			what := "a tool step"
			if st.ToolName != "" {
				what = "a " + st.ToolName + " tool step"
			}
			return &agyStop{what: what}
		case isSubagent:
			return &agyStop{what: "a subagent step"}
		case strings.EqualFold(st.State, "WAITING"):
			return &agyStop{what: fmt.Sprintf("a step that waits for input (%s)", st.StepType)}
		case st.StepType == "agent_response":
			if r.started && st.StepIndex != r.responseStep {
				return &agyStop{what: "a second model turn"}
			}
			r.started, r.responseStep = true, st.StepIndex
		}
		if st.Usage != nil {
			if r.usage == nil {
				r.usage = map[int]agyUsage{}
			}
			r.usage[st.StepIndex] = *st.Usage
		}
	case ev.Event == "result" && ev.Result != nil:
		r.result = ev.Result
	}
	return nil
}

// tokens is the run's usage: the sum over its steps, or the result's usage
// when no step reported one. The prompt counts cache reads too (agy reports
// them apart from input_tokens on every step after the first), as claude-cli
// counts its cache reads; the completion is output_tokens, which includes
// thinking.
func (r *agyRun) tokens() Usage {
	var sum agyUsage
	if len(r.usage) == 0 && r.result != nil {
		sum = r.result.Usage
	}
	for _, u := range r.usage {
		sum.InputTokens += u.InputTokens
		sum.CacheReadTokens += u.CacheReadTokens
		sum.OutputTokens += u.OutputTokens
	}
	u := Usage{
		PromptTokens:     sum.InputTokens + sum.CacheReadTokens,
		CompletionTokens: sum.OutputTokens,
	}
	return u.filled()
}

// parse turns a finished run into a Response or an *Error.
func (c *AgyCLI) parse(model string, res *cliproc.Result, run *agyRun, latency time.Duration) (*Response, error) {
	lg.Debugf("%s: agy ran in permission mode %q with %d tools offered (model %q)", c.Name, run.permissionMode, run.tools, run.model)
	r := run.result
	if r == nil || r.Status == "" {
		return nil, c.outputError(model, res)
	}
	if r.Status != "SUCCESS" || res.ExitCode != 0 {
		return nil, c.resultError(model, r, res)
	}
	if len(r.DeniedActions) > 0 || strings.TrimSpace(r.Response) == "" {
		return nil, c.emptyError(model, r, res)
	}
	return &Response{
		Content:        r.Response,
		Provider:       c.Name,
		RequestedModel: model,
		FinishReason:   "stop",
		Usage:          run.tokens(),
		Latency:        latency,
		Attempts:       1,
	}, nil
}

// emptyError is a run that reported success with no reply, or with tools
// it refused: in headless mode agy ends the turn after a refused approval
// and leaves the response empty.
func (c *AgyCLI) emptyError(model string, r *agyResult, res *cliproc.Result) *Error {
	e := &Error{Provider: c.Name, Model: model, Via: agyVia, Attempts: 1}
	msg := "agy reported success with an empty reply"
	if len(r.DeniedActions) > 0 {
		msg = fmt.Sprintf("agy refused %d tool calls and ended the turn", len(r.DeniedActions))
	}
	if line := res.StderrLine(); line != "" {
		msg += ": " + line
	}
	e.Message = shorten(redactBearer(msg))
	e.fix = "the model tried to use a tool instead of answering, which spec never allows: retry, or use another model (--engine)"
	return e
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
	case has("conflicts with --effort"):
		return "this model id already names its effort level (the -low, -medium or -high at its end), and --effort asked for another: remove effort from the provider (and --effort), or pick the model id with the level you want from `agy models`"
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
