// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"slices"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer/cliproc"
)

// DefaultCLITimeout bounds one CLI run when the provider sets no timeout.
const DefaultCLITimeout = 900 * time.Second

// cliDefaultSystem is the system prompt of a request that brings none
// (spec doctor's): a CLI provider always replaces the CLI's own long one.
const cliDefaultSystem = "Answer the user's message directly."

// cliBase is what the CLI providers (claude-cli, codex-cli) share: the
// configuration, the overrides and the Provider methods that only set
// them. Each provider builds its own argv and reads its own output.
type cliBase struct {
	// Name labels errors and responses (the provider's config name).
	Name string
	// Path is the resolved executable.
	Path string
	// Model and TriageModel are the model ids (or the CLI's aliases).
	Model       string
	TriageModel string
	// Effort is the provider's effort setting; EffortOverride (--effort)
	// wins over it. Both are levels of the provider's kind.
	Effort         string
	EffortOverride string
	// Timeout bounds one run; 0 means DefaultCLITimeout.
	Timeout time.Duration
	// Env is the whole environment the CLI runs with.
	Env []string
}

// newCLIBase resolves the provider's command on getenv("PATH") and builds
// its environment: cliproc.BaseEnv, then extraEnv and pass_env, then set.
// A command that is not found returns the lookup error, for the caller's
// *SetupError.
func newCLIBase(name string, p config.Provider, getenv func(string) string, extraEnv []string, set map[string]string) (cliBase, error) {
	path, err := cliproc.Lookup(p.CommandOrDefault(), getenv("PATH"))
	if err != nil {
		return cliBase{}, err
	}
	names := slices.Concat(cliproc.BaseEnv, extraEnv, p.PassEnv)
	return cliBase{
		Name:        name,
		Path:        path,
		Model:       p.Model,
		TriageModel: p.TriageModel,
		Effort:      p.Effort,
		Timeout:     time.Duration(p.Timeout) * time.Second,
		Env:         cliproc.Env(getenv, names, set),
	}, nil
}

// info describes the provider as kind.
func (c *cliBase) info(kind string) Info {
	return Info{Name: c.Name, Kind: kind, Model: c.Model, TriageModel: c.TriageModel, Effort: c.effort(), Transport: c.Path}
}

// SetModel replaces Model and TriageModel (Provider).
func (c *cliBase) SetModel(id string) {
	c.Model = id
	c.TriageModel = id
}

// SetEffort sets EffortOverride (Provider).
func (c *cliBase) SetEffort(level string) { c.EffortOverride = level }

// SetLimits sets Timeout; there is one attempt, since the CLIs retry API
// errors themselves (Provider).
func (c *cliBase) SetLimits(timeout time.Duration, attempts int) { c.Timeout = timeout }

// RunawayExtra is nil: a CLI picks its own reply cap, so the runaway guard
// does not apply (Provider).
func (c *cliBase) RunawayExtra() map[string]any { return nil }

func (c *cliBase) effort() string {
	if c.EffortOverride != "" {
		return c.EffortOverride
	}
	return c.Effort
}

func (c *cliBase) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultCLITimeout
}

// cliPrompt splits messages into the system prompt and the text for stdin.
// A CLI takes one prompt per run, so a conversation (the JSON retry: the
// first prompt, the model's reply and a reminder) is written out in order,
// with the model's own earlier reply marked as such.
func cliPrompt(msgs []Message) (system, prompt string) {
	var sys, parts []string
	for _, m := range msgs {
		switch m.Role {
		case "system":
			sys = append(sys, m.Content)
		case "assistant":
			parts = append(parts, "<your_previous_reply>\n"+m.Content+"\n</your_previous_reply>")
		default:
			parts = append(parts, m.Content)
		}
	}
	system = strings.Join(sys, "\n\n")
	if strings.TrimSpace(system) == "" {
		system = cliDefaultSystem
	}
	return system, strings.Join(parts, "\n\n")
}
