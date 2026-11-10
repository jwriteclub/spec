// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer/cliproc"
)

// claudeVia labels a claude-cli failure.
const claudeVia = "claude -p"

// claudeEnv names the variables passed to claude beyond cliproc.BaseEnv:
// CLAUDE_CONFIG_DIR, so a login kept outside ~/.claude is found.
var claudeEnv = []string{"CLAUDE_CONFIG_DIR"}

// claudeSetEnv is set for every run. CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC
// turns off Claude Code's autoupdater, telemetry and error reporting, which a
// one-shot call has no use for.
var claudeSetEnv = map[string]string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}

// ClaudeCLI is a claude-cli provider: it runs the installed Claude Code CLI
// once per request, in print mode, on the user's own login (a Claude
// subscription needs no API key). Each run is one model turn and nothing
// else; Args documents the flags that make it so.
//
// Model and TriageModel are aliases (haiku, sonnet, opus) or full ids;
// Effort and EffortOverride are Claude Code --effort levels.
type ClaudeCLI struct {
	cliBase
}

// NewClaudeCLI builds the claude-cli provider called name: it finds the
// command on getenv("PATH") and builds the minimal environment. A missing
// command is a *SetupError.
func NewClaudeCLI(name string, p config.Provider, getenv func(string) string) (*ClaudeCLI, error) {
	base, err := newCLIBase(name, p, getenv, claudeEnv, claudeSetEnv)
	if err != nil {
		return nil, &SetupError{
			Provider: name,
			Msg:      fmt.Sprintf("cannot run Claude Code: %v", err),
			FixText:  "install Claude Code (https://claude.com/claude-code) and log in with `claude auth login`, or set command to the claude executable's path",
		}
	}
	return &ClaudeCLI{cliBase: base}, nil
}

// Info describes the provider (Provider).
func (c *ClaudeCLI) Info() Info { return c.info(config.KindClaudeCLI) }

// Args returns the argv (after the program name) for a request to model
// with the given system prompt. The user prompt goes on stdin. Every flag
// is in `claude --help` (2.1.x) except --max-turns, which the CLI
// reference documents for print mode:
//
//	-p                          print mode: one non-interactive run, then exit
//	--output-format json        one JSON result: text, usage, cost, model, is_error
//	--model MODEL               the provider's model (alias or full id)
//	--system-prompt=TEXT        our prompt replaces Claude Code's default one
//	                            (the = form, so a prompt starting with "-" is
//	                            never read as a flag)
//	--tools ""                  no built-in tools at all
//	--strict-mcp-config         ignore every MCP server the user configured...
//	--mcp-config {"mcpServers":{}}  ...and load none in their place
//	--setting-sources ""        no user, project or local settings: no hooks,
//	                            plugins, skills, permissions or env from them
//	--disable-slash-commands    no skills or custom commands
//	--no-session-persistence    nothing saved to disk, nothing to resume
//	--max-turns 1               one model turn, never an agent loop
//	--effort LEVEL              only when an effort is set
//
// Run in an empty temporary directory, Claude Code finds no CLAUDE.md or
// project settings; measured, the request then carries about 640 tokens
// besides ours (Claude Code's environment notes). json_mode has no
// counterpart: the prompt asks for JSON and the reply is parsed leniently.
// max_tokens and temperature are not passed (Claude Code has no flags for
// them).
func (c *ClaudeCLI) Args(model, system string) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--model", model,
		"--system-prompt=" + system,
		"--tools", "",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
		"--setting-sources", "",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--max-turns", "1",
	}
	if e := c.effort(); e != "" {
		args = append(args, "--effort", e)
	}
	return args
}

// Chat runs claude once for req (Provider). req.JSON, MaxTokens,
// Temperature and Extra have no claude counterpart and are ignored.
func (c *ClaudeCLI) Chat(ctx context.Context, req Request) (*Response, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("infer: a request needs at least one message")
	}
	model := modelFor(req, c.Model, c.TriageModel)
	system, prompt := cliPrompt(req.Messages)
	args := c.Args(model, system)
	lg.Debugf("%s: running %s with model %s, effort %q, a %d-byte system prompt and %d bytes on stdin", c.Name, c.Path, model, c.effort(), len(system), len(prompt))
	start := time.Now()
	res, err := cliproc.Run(ctx, cliproc.Command{
		Path:    c.Path,
		Args:    args,
		Stdin:   []byte(prompt),
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

// runError is a run that did not finish: a timeout, or claude would not
// start.
func (c *ClaudeCLI) runError(model string, err error) *Error {
	e := &Error{Provider: c.Name, Model: model, Via: claudeVia, Attempts: 1, cause: err}
	switch {
	case errors.Is(err, cliproc.ErrTimeout):
		e.fix = fmt.Sprintf("the model did not finish within %s: lower the effort (effort = \"low\" in the provider, or --effort low), or raise the provider's timeout (seconds)", c.timeout())
	case errors.Is(err, cliproc.ErrOutputTooLarge):
		e.fix = "Claude Code wrote far more than one reply; run `claude -p` in a terminal to see what it prints"
	default:
		e.fix = "check that " + c.Path + " runs (`claude --version`), or set command to the claude executable's path"
	}
	return e
}

// claudeResult is the part of `claude -p --output-format json` we read.
type claudeResult struct {
	Type           string  `json:"type"`
	Subtype        string  `json:"subtype"`
	IsError        bool    `json:"is_error"`
	Result         string  `json:"result"`
	StopReason     string  `json:"stop_reason"`
	NumTurns       int     `json:"num_turns"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	APIErrorStatus *int    `json:"api_error_status"`
	Usage          struct {
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		OutputTokens int `json:"outputTokens"`
	} `json:"modelUsage"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
	Errors            []string          `json:"errors"`
}

// parse turns a finished run into a Response or an *Error.
func (c *ClaudeCLI) parse(model string, res *cliproc.Result, latency time.Duration) (*Response, error) {
	var r claudeResult
	out := bytes.TrimSpace(res.Stdout)
	if err := json.Unmarshal(out, &r); err != nil || r.Type != "result" {
		return nil, c.outputError(model, res)
	}
	if r.IsError || r.Subtype != "success" || res.ExitCode != 0 {
		return nil, c.resultError(model, &r, res)
	}
	if r.NumTurns > 1 || len(r.PermissionDenials) > 0 {
		lg.Warnf("%s: claude ran %d turns with %d permission denials; spec asks for one turn without tools", c.Name, r.NumTurns, len(r.PermissionDenials))
	}
	u := Usage{
		PromptTokens:     r.Usage.InputTokens + r.Usage.CacheCreationInputTokens + r.Usage.CacheReadInputTokens,
		CompletionTokens: r.Usage.OutputTokens,
	}
	return &Response{
		Content:        r.Result,
		Provider:       c.Name,
		RequestedModel: model,
		Model:          echoedModel(r.ModelUsage),
		FinishReason:   finishReason(r.StopReason),
		Usage:          u.filled(),
		Latency:        latency,
		Attempts:       1,
		CostUSD:        r.TotalCostUSD,
	}, nil
}

// echoedModel is the model that wrote most of the reply: Claude Code
// reports usage per full model id.
func echoedModel(usage map[string]struct {
	OutputTokens int `json:"outputTokens"`
}) string {
	best, most := "", -1
	for id, u := range usage {
		if u.OutputTokens > most || (u.OutputTokens == most && id < best) {
			best, most = id, u.OutputTokens
		}
	}
	return best
}

// finishReason maps an Anthropic stop_reason to the OpenAI finish_reason
// the workflow reads ("length" when the reply cap cut it off).
func finishReason(stop string) string {
	switch stop {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	}
	return stop
}

// outputError is a run whose stdout is not Claude Code's JSON result.
func (c *ClaudeCLI) outputError(model string, res *cliproc.Result) *Error {
	e := &Error{Provider: c.Name, Model: model, Via: claudeVia, Attempts: 1}
	line := res.StderrLine()
	switch {
	case res.ExitCode != 0 && strings.Contains(strings.ToLower(line), "unknown option"):
		e.cause = fmt.Errorf("exit status %d", res.ExitCode)
		e.Message = shorten(redactBearer(line))
		e.fix = "this Claude Code does not accept a flag spec passes; update it (`claude update`); `spec explain config` lists the flags"
	case res.ExitCode != 0:
		e.cause = fmt.Errorf("exit status %d", res.ExitCode)
		e.Message = shorten(redactBearer(line))
		e.fix = "run `claude -p` in a terminal to see Claude Code's own error"
	default:
		e.cause = errors.New("the output is not Claude Code's JSON result")
		e.Message = snippet([]byte(redactBearer(string(res.Stdout))))
		e.fix = "update Claude Code (`claude update`); spec reads the result of --output-format json"
	}
	return e
}

// resultError is a JSON result that reports a failure.
func (c *ClaudeCLI) resultError(model string, r *claudeResult, res *cliproc.Result) *Error {
	msg := r.Result
	if msg == "" && len(r.Errors) > 0 {
		msg = strings.Join(r.Errors, "; ")
	}
	if msg == "" {
		msg = r.Subtype
	}
	e := &Error{Provider: c.Name, Model: model, Via: claudeVia, Attempts: 1, Message: shorten(redactBearer(msg))}
	if r.APIErrorStatus != nil {
		e.Status = *r.APIErrorStatus
	}
	e.fix = claudeFix(e.Status, r.Subtype, msg)
	if e.Status == 0 && res.ExitCode != 0 {
		e.cause = fmt.Errorf("exit status %d", res.ExitCode)
	}
	return e
}

// claudeFix says how to resolve a failure Claude Code reported.
func claudeFix(status int, subtype, msg string) string {
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
	case status == 401 || status == 403 || has("not logged in", "/login", "invalid api key", "oauth token"):
		return "Claude Code is not logged in, or its login expired: run `claude auth login` (or `claude`, then /login) and retry; `claude auth status` shows the login"
	case status == 429 || has("usage limit", "rate limit", "hit your limit"):
		return "your Claude plan's usage or rate limit was reached: wait for the reset the message names, or use another provider with --provider NAME"
	case status == 529 || has("overloaded"):
		return "Anthropic's API is overloaded and Claude Code's own retries ran out: retry later, or use another provider with --provider NAME"
	case status == 404 || has("selected model"):
		return "Claude Code does not know the model, or your plan cannot use it: set model to an alias (haiku, sonnet, opus) or a full model id, or pass --engine"
	case status >= 500:
		return "Anthropic's API is failing and Claude Code's own retries ran out: retry later, or use another provider with --provider NAME"
	case subtype == "error_max_turns":
		return "the model tried to use a tool, which spec turns off, and ran out of turns: run it again, or try another model (--engine)"
	}
	return "run `claude -p` in a terminal to see Claude Code's own error"
}
