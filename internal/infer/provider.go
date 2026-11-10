// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"context"
	"fmt"
	"time"

	"github.com/jwriteclub/spec/internal/config"
)

// Provider is one inference backend, whatever its kind: the workflow sends
// it a Request and gets back a Response (content, model, usage, cost when
// reported, latency and finish reason) or an error. *Client (kind openai),
// *ClaudeCLI (kind claude-cli), *CodexCLI (kind codex-cli) and *Canned
// (kind canned) implement it.
type Provider interface {
	// Chat sends one request and returns the reply. A provider failure is
	// an *Error; the context's own cancellation is returned as is.
	Chat(ctx context.Context, req Request) (*Response, error)
	// Info describes the provider as configured, overrides included.
	Info() Info
	// SetModel replaces the model and the triage model (--engine).
	SetModel(id string)
	// SetEffort replaces the reasoning effort for every request (--effort);
	// it wins over the provider's own settings. The caller has checked the
	// level with config.ValidEffort.
	SetEffort(level string)
	// SetLimits bounds each attempt and the number of attempts (spec
	// doctor's quick check). A CLI provider makes one attempt: the CLI
	// retries API errors itself.
	SetLimits(timeout time.Duration, attempts int)
	// RunawayExtra is what the runaway guard merges into a request that
	// spent its whole reply budget thinking (the provider's triage_extra);
	// nil when the provider has none, which turns the guard off.
	RunawayExtra() map[string]any
}

// Info describes a configured provider.
type Info struct {
	// Name is the provider's config name.
	Name string
	// Kind is config.KindOpenAI, config.KindClaudeCLI,
	// config.KindCodexCLI, config.KindDecisions or config.KindCanned.
	Kind string
	// Model and TriageModel are the model ids requests use.
	Model       string
	TriageModel string
	// Effort is the effort level spec sets ("" when it sets none and the
	// provider's default, or an extra, decides).
	Effort string
	// Transport says how requests travel, for spec doctor: the endpoint's
	// host for openai, the CLI's path for a CLI kind.
	Transport string
}

// SetupError is a provider that cannot be used as configured: the CLI it
// runs is missing, for example. It is a configuration problem, not a call
// that failed.
type SetupError struct {
	Provider string
	Msg      string
	FixText  string
}

func (e *SetupError) Error() string { return fmt.Sprintf("provider %q: %s", e.Provider, e.Msg) }

// Fix says how to resolve the problem.
func (e *SetupError) Fix() string { return e.FixText }

// Open builds the provider called name from its config, by kind. It reads
// the environment through getenv (base_url variables and the API key for
// openai; PATH and the variables passed to a CLI). A missing variable is a
// *config.EnvError and a missing CLI a *SetupError.
func Open(name string, p config.Provider, getenv func(string) string) (Provider, error) {
	switch p.KindOrDefault() {
	case config.KindOpenAI:
		return New(name, p, getenv)
	case config.KindClaudeCLI:
		return NewClaudeCLI(name, p, getenv)
	case config.KindCodexCLI:
		return NewCodexCLI(name, p, getenv)
	case config.KindCanned:
		return NewCanned(name, p, getenv)
	case config.KindDecisions:
		return nil, &SetupError{
			Provider: name,
			Msg:      "kind decisions is a gate: it scores edits and cannot run checks",
			FixText:  "pick a chat provider (kind openai, claude-cli or codex-cli) with --provider, or name one as default",
		}
	}
	return nil, fmt.Errorf("provider %q: unknown kind %q", name, p.Kind)
}

// modelFor is the model a request goes to: its own, else the triage model
// for a triage request (when set), else the main model.
func modelFor(req Request, model, triage string) string {
	switch {
	case req.Model != "":
		return req.Model
	case req.Triage && triage != "":
		return triage
	}
	return model
}
