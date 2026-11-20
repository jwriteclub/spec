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

// agyVia labels an agy-cli failure.
const agyVia = "agy -p"

// agyEnv names the variables passed to agy beyond cliproc.BaseEnv:
// XDG_CONFIG_HOME, so a login or configuration kept outside ~/.config is found.
// API keys stay out unless pass_env names them, so the user's login pays.
var agyEnv = []string{"XDG_CONFIG_HOME"}

// AgyCLI is an agy-cli provider: it runs the installed Antigravity CLI once
// per request, in print mode (`agy -p=...`), on the user's own login (an
// Antigravity login needs no API key). Each run is bounded with --sandbox and
// --disable-slash-commands; Args documents the flags.
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

// agyPrompt composes the system instructions and user prompt for `agy -p=`:
// agy 1.5.x has no --system-prompt flag, so system instructions are wrapped
// explicitly above the prompt and instruct the model not to invoke tools.
func agyPrompt(system, prompt string) string {
	return "<system_instructions>\n" + system + "\n\nDo not invoke any tools or modify files; answer directly.\n</system_instructions>\n\n" + prompt
}

// Args returns the argv (after the program name) for a request to model with
// the given system and user prompts. Every flag is verified on `agy` 1.5.13:
//
//	--output-format json        one JSON result: status, response, usage, error
//	--model MODEL               the provider's model id (`agy models`)
//	--disable-slash-commands    no skills or custom commands
//	--sandbox                   restrict any unexpected execution to the sandbox
//	--effort LEVEL              only when an effort is set
//	-p=PROMPT                   print mode with the composed prompt (the = form,
//	                            because -p is a string flag taking the prompt and
//	                            so a prompt starting with "-" is never parsed as a flag)
//
// Run in an empty temporary directory, agy finds no project files. json_mode,
// max_tokens and temperature have no counterpart: the prompt asks for JSON and
// the reply is parsed leniently.
func (c *AgyCLI) Args(model, system, prompt string) []string {
	args := []string{
		"--output-format", "json",
		"--model", model,
		"--disable-slash-commands",
		"--sandbox",
	}
	if e := c.effort(); e != "" {
		args = append(args, "--effort", e)
	}
	return append(args, "-p="+agyPrompt(system, prompt))
}

// Chat runs agy once for req (Provider). req.JSON, MaxTokens, Temperature and
// Extra have no agy counterpart and are ignored.
func (c *AgyCLI) Chat(ctx context.Context, req Request) (*Response, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("infer: a request needs at least one message")
	}
	model := modelFor(req, c.Model, c.TriageModel)
	system, prompt := cliPrompt(req.Messages)
	args := c.Args(model, system, prompt)
	lg.Debugf("%s: running %s with model %s, effort %q, a %d-byte system prompt and %d-byte prompt", c.Name, c.Path, model, c.effort(), len(system), len(prompt))
	start := time.Now()
	res, err := cliproc.Run(ctx, cliproc.Command{
		Path:    c.Path,
		Args:    args,
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

// agyResult is the JSON envelope printed by `agy --output-format json`.
type agyResult struct {
	ConversationID  string  `json:"conversation_id"`
	Status          string  `json:"status"`
	Response        string  `json:"response"`
	Error           string  `json:"error"`
	DurationSeconds float64 `json:"duration_seconds"`
	NumTurns        int     `json:"num_turns"`
	FinishedReason  string  `json:"finished_reason"`
	Usage           struct {
		InputTokens     int `json:"input_tokens"`
		OutputTokens    int `json:"output_tokens"`
		ThinkingTokens  int `json:"thinking_tokens"`
		CacheReadTokens int `json:"cache_read_tokens"`
		TotalTokens     int `json:"total_tokens"`
	} `json:"usage"`
}

// parse turns a finished run into a Response or an *Error.
func (c *AgyCLI) parse(model string, res *cliproc.Result, latency time.Duration) (*Response, error) {
	var r agyResult
	out := bytes.TrimSpace(res.Stdout)
	if err := json.Unmarshal(out, &r); err != nil || r.Status == "" {
		return nil, c.outputError(model, res)
	}
	if r.Status != "SUCCESS" || res.ExitCode != 0 {
		return nil, c.resultError(model, &r, res)
	}
	// usage.input_tokens already includes cache_read_tokens
	// (input_tokens + output_tokens == total_tokens); do not add cache_read_tokens again.
	u := Usage{
		PromptTokens:     r.Usage.InputTokens,
		CompletionTokens: r.Usage.OutputTokens,
	}
	finish := "stop"
	switch strings.ToLower(r.FinishedReason) {
	case "max_tokens", "length":
		finish = "length"
	}
	return &Response{
		Content:        r.Response,
		Provider:       c.Name,
		RequestedModel: model,
		FinishReason:   finish,
		Usage:          u.filled(),
		Latency:        latency,
		Attempts:       1,
	}, nil
}

// outputError is a run whose stdout is not agy's JSON result.
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
		e.cause = errors.New("the output is not Antigravity CLI's JSON result")
		e.Message = snippet([]byte(redactBearer(string(res.Stdout))))
		e.fix = "update Antigravity CLI (`agy update`); spec reads the result of --output-format json"
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
