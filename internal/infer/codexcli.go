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

// codexVia labels a codex-cli failure.
const codexVia = "codex exec"

// codexInstructionsFile is the file, in the run's working directory, that
// holds the system prompt; model_instructions_file names it by this
// relative path (Codex resolves it against the working directory).
const codexInstructionsFile = "spec-instructions.md"

// codexEnv names the variables passed to codex beyond cliproc.BaseEnv:
// CODEX_HOME, so a login kept outside ~/.codex is found. OPENAI_API_KEY and
// CODEX_API_KEY stay out unless pass_env names them, so the plan pays.
var codexEnv = []string{"CODEX_HOME"}

// codexOff are the Codex features that add a tool, or run something, which
// spec turns off with --disable: command execution, sub-agents, apps and
// plugins (with their MCP servers), lifecycle hooks, image generation and
// viewing, browser and computer use, goals, skill search, tool suggestions,
// the sleep tool and shell snapshots. Each name is in `codex features list`
// (0.155).
var codexOff = []string{
	"shell_tool", "multi_agent", "apps", "plugins", "remote_plugin", "hooks", "image_generation", "view_image",
	"browser_use", "computer_use", "goals", "skill_search", "tool_suggest", "sleep_tool", "shell_snapshot",
}

// codexSettings are the -c overrides that strip a run down to one answer.
// Every key is in Codex's configuration reference, or (the include_*,
// skills.* and tools.experimental_request_user_input keys) accepted by
// --strict-config, which rejects any key it does not know:
//
//	model_instructions_file              our system prompt replaces Codex's built-in instructions
//	approval_policy="never"              never stop to ask; anything needing approval is refused
//	web_search="disabled"                no web search tool
//	project_doc_max_bytes=0              read no AGENTS.md
//	history.persistence="none"           write nothing to history.jsonl
//	analytics.enabled=false              send no analytics events
//	check_for_update_on_startup=false    no update check
//	model_reasoning_summary="none"       no reasoning summaries (never shown)
//	hide_agent_reasoning=true            no reasoning events on stdout
//	include_permissions_instructions=false        no sandbox and approval notes in the prompt
//	include_apps_instructions=false               no apps notes
//	include_collaboration_mode_instructions=false no collaboration-mode notes
//	include_environment_context=false             no cwd, shell, date and timezone block
//	skills.include_instructions=false    no skills catalog in the prompt
//	skills.bundled.enabled=false         no bundled skills
//	tools.experimental_request_user_input.enabled=false  no question-asking tool
//	agents.enabled=false                 no sub-agent tools or their instructions
var codexSettings = []string{
	`model_instructions_file="` + codexInstructionsFile + `"`,
	`approval_policy="never"`,
	`web_search="disabled"`,
	`project_doc_max_bytes=0`,
	`history.persistence="none"`,
	`analytics.enabled=false`,
	`check_for_update_on_startup=false`,
	`model_reasoning_summary="none"`,
	`hide_agent_reasoning=true`,
	`include_permissions_instructions=false`,
	`include_apps_instructions=false`,
	`include_collaboration_mode_instructions=false`,
	`include_environment_context=false`,
	`skills.include_instructions=false`,
	`skills.bundled.enabled=false`,
	`tools.experimental_request_user_input.enabled=false`,
	`agents.enabled=false`,
}

// CodexCLI is a codex-cli provider: it runs the installed Codex CLI once
// per request with `codex exec`, on the user's own login (a ChatGPT plan
// that includes Codex needs no API key). Each run is one model turn with
// every tool Codex lets a user turn off turned off; Args documents how.
//
// Model and TriageModel are Codex model ids (`codex debug models` lists
// them); Effort and EffortOverride are model_reasoning_effort levels.
type CodexCLI struct {
	cliBase
}

// NewCodexCLI builds the codex-cli provider called name: it finds the
// command on getenv("PATH") and builds the minimal environment. A missing
// command is a *SetupError.
func NewCodexCLI(name string, p config.Provider, getenv func(string) string) (*CodexCLI, error) {
	base, err := newCLIBase(name, p, getenv, codexEnv, nil)
	if err != nil {
		return nil, &SetupError{
			Provider: name,
			Msg:      fmt.Sprintf("cannot run Codex: %v", err),
			FixText:  "install the Codex CLI (https://developers.openai.com/codex) and log in with `codex login`, or set command to the codex executable's path",
		}
	}
	return &CodexCLI{cliBase: base}, nil
}

// Info describes the provider (Provider).
func (c *CodexCLI) Info() Info { return c.info(config.KindCodexCLI) }

// Args returns the argv (after the program name) for a request to model.
// The system prompt goes in the instructions file and the user prompt on
// stdin. Every flag is in `codex exec --help` (0.155):
//
//	exec                      one non-interactive run, then exit
//	--json                    JSONL events on stdout: the reply, usage, failures
//	--color never             no ANSI codes
//	--strict-config           an override key this Codex does not know is an
//	                          error, so a lockdown setting can never silently lapse
//	--ignore-user-config      skip ~/.codex/config.toml: none of the user's MCP
//	                          servers, plugins, notify hooks or defaults (the
//	                          login in CODEX_HOME is still used)
//	--ignore-rules            no execpolicy .rules files
//	--ephemeral               no session files written
//	--skip-git-repo-check     the empty working directory is not a git repository
//	--sandbox read-only       should anything run after all, it can write nothing
//	-m MODEL                  the provider's model
//	-c KEY=VALUE              each of codexSettings
//	--disable FEATURE         each of codexOff
//	-c model_reasoning_effort="LEVEL"  only when an effort is set
//	-                         read the prompt from stdin
//
// Measured with Codex 0.155 and 0.160 on GPT-6 Luna and Terra, a one-word
// request then carries about 1690 input tokens besides ours: the model's
// catalog entry runs it in code mode, which keeps an `exec` (a JavaScript
// sandbox with no file system or network) and a `wait` tool that no
// setting removes. With sub-agents left on it was about 3280. json_mode,
// max_tokens and temperature have no counterpart.
func (c *CodexCLI) Args(model string) []string {
	args := []string{
		"exec", "--json", "--color", "never", "--strict-config", "--ignore-user-config", "--ignore-rules",
		"--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "-m", model,
	}
	for _, s := range codexSettings {
		args = append(args, "-c", s)
	}
	for _, f := range codexOff {
		args = append(args, "--disable", f)
	}
	if e := c.effort(); e != "" {
		args = append(args, "-c", `model_reasoning_effort="`+e+`"`)
	}
	return append(args, "-")
}

// Chat runs codex once for req (Provider). req.JSON, MaxTokens,
// Temperature and Extra have no codex counterpart and are ignored.
func (c *CodexCLI) Chat(ctx context.Context, req Request) (*Response, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("infer: a request needs at least one message")
	}
	model := modelFor(req, c.Model, c.TriageModel)
	system, prompt := cliPrompt(req.Messages)
	lg.Debugf("%s: running %s with model %s, effort %q, a %d-byte system prompt and %d bytes on stdin", c.Name, c.Path, model, c.effort(), len(system), len(prompt))
	start := time.Now()
	res, err := cliproc.Run(ctx, cliproc.Command{
		Path:    c.Path,
		Args:    c.Args(model),
		Stdin:   []byte(prompt),
		Env:     c.Env,
		Timeout: c.timeout(),
		Files:   map[string][]byte{codexInstructionsFile: []byte(system)},
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, c.runError(model, err)
	}
	return c.parse(model, res, time.Since(start))
}

// runError is a run that did not finish: a timeout, or codex would not
// start.
func (c *CodexCLI) runError(model string, err error) *Error {
	e := &Error{Provider: c.Name, Model: model, Via: codexVia, Attempts: 1, cause: err}
	switch {
	case errors.Is(err, cliproc.ErrTimeout):
		e.fix = fmt.Sprintf("the model did not finish within %s: lower the effort (effort = \"low\" in the provider, or --effort low), or raise the provider's timeout (seconds)", c.timeout())
	case errors.Is(err, cliproc.ErrOutputTooLarge):
		e.fix = "Codex wrote far more than one reply; run `codex exec` in a terminal to see what it prints"
	default:
		e.fix = "check that " + c.Path + " runs (`codex --version`), or set command to the codex executable's path"
	}
	return e
}

// codexEvent is the part of a `codex exec --json` event we read.
type codexEvent struct {
	Type string `json:"type"`
	// Message is an "error" event's text (a retry notice, or the failure
	// that ends the turn).
	Message string `json:"message"`
	Item    *struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Message string `json:"message"`
	} `json:"item"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// codexRun is what a run's events say.
type codexRun struct {
	turns     int
	reply     string
	completed bool
	failed    string
	usage     Usage
	// tools are the items that were not the reply, reasoning or a notice:
	// a command, a file change, a tool call. spec turns them all off.
	tools []string
	// notices are Codex's non-fatal errors: "error" events (a retry notice)
	// and error items (a missing model catalog entry, a transport fallback).
	notices []string
}

// readCodexEvents reads the JSONL on stdout; a line that is not an event is
// skipped.
func readCodexEvents(out []byte) (codexRun, int) {
	var r codexRun
	events := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), cliproc.MaxStdout)
	for sc.Scan() {
		var ev codexEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Type == "" {
			continue
		}
		events++
		switch ev.Type {
		case "turn.started":
			r.turns++
		case "error":
			if ev.Message != "" {
				r.notices = append(r.notices, ev.Message)
			}
		case "turn.completed":
			r.completed = true
			if ev.Usage != nil {
				r.usage = r.usage.Add(Usage{PromptTokens: ev.Usage.InputTokens, CompletionTokens: ev.Usage.OutputTokens})
			}
		case "turn.failed":
			if ev.Error != nil {
				r.failed = ev.Error.Message
			}
			if r.failed == "" {
				r.failed = "the turn failed"
			}
		case "item.completed":
			switch {
			case ev.Item == nil:
			case ev.Item.Type == "agent_message":
				r.reply = ev.Item.Text
			case ev.Item.Type == "error":
				r.notices = append(r.notices, ev.Item.Message)
			case ev.Item.Type != "reasoning":
				r.tools = append(r.tools, ev.Item.Type)
			}
		}
	}
	return r, events
}

// parse turns a finished run into a Response or an *Error.
func (c *CodexCLI) parse(model string, res *cliproc.Result, latency time.Duration) (*Response, error) {
	r, events := readCodexEvents(res.Stdout)
	for _, n := range r.notices {
		lg.Debugf("%s: codex notice: %s", c.Name, shorten(n))
	}
	switch {
	case events == 0 || (!r.completed && r.failed == ""):
		return nil, c.outputError(model, res)
	case r.failed != "":
		return nil, c.turnError(model, r)
	case res.ExitCode != 0:
		return nil, c.outputError(model, res)
	}
	if r.turns > 1 || len(r.tools) > 0 {
		lg.Warnf("%s: codex ran %d turns and %d tool items (%s); spec asks for one turn without tools", c.Name, r.turns, len(r.tools), strings.Join(r.tools, ", "))
	}
	return &Response{
		Content:        r.reply,
		Provider:       c.Name,
		RequestedModel: model,
		FinishReason:   "stop",
		Usage:          r.usage.filled(),
		Latency:        latency,
		Attempts:       1,
	}, nil
}

// outputError is a run with no turn result on stdout: Codex refused its
// arguments or configuration, or printed something else.
func (c *CodexCLI) outputError(model string, res *cliproc.Result) *Error {
	e := &Error{Provider: c.Name, Model: model, Via: codexVia, Attempts: 1}
	line := res.StderrLine()
	low := strings.ToLower(line)
	switch {
	case res.ExitCode != 0 && strings.Contains(low, "unknown configuration field"):
		e.cause = fmt.Errorf("exit status %d", res.ExitCode)
		e.Message = shorten(redactBearer(line))
		e.fix = "this Codex does not know a setting spec passes to turn its tools off (spec runs it with --strict-config, so none can lapse silently): update Codex (`codex update`); `spec explain config` lists the settings"
	case res.ExitCode != 0 && strings.Contains(low, "unexpected argument"):
		e.cause = fmt.Errorf("exit status %d", res.ExitCode)
		e.Message = shorten(redactBearer(line))
		e.fix = "this Codex does not accept a flag spec passes; update it (`codex update`); `spec explain config` lists the flags"
	case res.ExitCode != 0:
		e.cause = fmt.Errorf("exit status %d", res.ExitCode)
		e.Message = shorten(redactBearer(line))
		e.fix = "run `codex exec` in a terminal to see Codex's own error"
	default:
		e.cause = errors.New("the output has no Codex turn result")
		e.Message = snippet([]byte(redactBearer(string(res.Stdout))))
		e.fix = "update Codex (`codex update`); spec reads the events of `codex exec --json`"
	}
	return e
}

// codexStatus finds an HTTP status in a Codex failure text.
var codexStatus = regexp.MustCompile(`(?i)(?:unexpected )?status (\d{3})`)

// turnError is a turn that failed: the API refused the request or the
// retries ran out. The text is often the API's error body as JSON.
func (c *CodexCLI) turnError(model string, r codexRun) *Error {
	msg, status, code := codexFailure(r.failed)
	e := &Error{Provider: c.Name, Model: model, Via: codexVia, Attempts: 1, Status: status, Message: shorten(redactBearer(msg))}
	e.fix = codexFix(status, code, msg)
	return e
}

// codexFailure extracts the message, status and error code from a failed
// turn's text: {"status": 400, "error": {"message", "code", "param"}} or a
// line such as "unexpected status 401 Unauthorized: ...".
func codexFailure(text string) (msg string, status int, code string) {
	var body struct {
		Status int `json:"status"`
		Error  struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(text), &body) == nil && body.Error.Message != "" {
		code = body.Error.Code
		if body.Error.Param != "" && code == "" {
			code = body.Error.Param
		}
		return body.Error.Message, body.Status, code
	}
	if m := codexStatus.FindStringSubmatch(text); m != nil {
		status, _ = strconv.Atoi(m[1])
	}
	return text, status, ""
}

// codexFix says how to resolve a failure Codex reported.
func codexFix(status int, code, msg string) string {
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
	case status == 401 || status == 403 || has("unauthorized", "not logged in", "login", "authentication"):
		return "Codex is not logged in, or its login expired: run `codex login` (sign in with ChatGPT) and retry; `codex login status` shows the login"
	case status == 429 || has("usage limit", "rate limit", "hit your limit"):
		return "your ChatGPT plan's Codex usage or rate limit was reached: wait for the reset the message names, or use another provider with --provider NAME"
	case code == "model_not_found" || status == 404 || has("model is not supported", "does not exist"):
		return "Codex does not offer this model on your login, or this Codex is too old to know it: `codex debug models` lists the ids it knows (a newer model can need `codex update`); set model or pass --engine"
	case code == "unsupported_value" || code == "reasoning.effort" || has("reasoning.effort", "supported values"):
		return "the model does not accept this effort level: set another (effort in the provider, or --effort)"
	case status >= 500 || has("overloaded"):
		return "OpenAI's API is failing and Codex's own retries ran out: retry later, or use another provider with --provider NAME"
	}
	return "run `codex exec` in a terminal to see Codex's own error"
}
