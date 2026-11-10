// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/workflow"
)

// providerFlags are --provider, --engine and --effort, shared by every
// command that calls a model. --engine is the LLM model id; it is not
// called --model so that it is never confused with a TLA+ model (--tla).
type providerFlags struct {
	name   string
	model  string
	effort string
}

// addProviderFlags registers --provider, --engine and --effort on cmd, bound
// to s.
func (s *session) addProviderFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&s.provider.name, "provider", "", "inference provider from the user config (default: its `default`)")
	cmd.Flags().StringVar(&s.provider.model, "engine", "", "LLM model id to use instead of the provider's model and triage_model")
	cmd.Flags().StringVar(&s.provider.effort, "effort", "", "reasoning effort instead of the provider's effort: "+effortHelp())
}

// effortHelp lists the --effort levels per provider kind.
func effortHelp() string {
	return fmt.Sprintf("%s for kind %s (sent as reasoning_effort), %s for kind %s (claude --effort), %s for kind %s (codex model_reasoning_effort)",
		strings.Join(config.EffortLevels(config.KindOpenAI), "|"), config.KindOpenAI,
		strings.Join(config.EffortLevels(config.KindClaudeCLI), "|"), config.KindClaudeCLI,
		strings.Join(config.EffortLevels(config.KindCodexCLI), "|"), config.KindCodexCLI)
}

// userConfigPath is where the user config lives in s's environment ("" when
// neither HOME nor XDG_CONFIG_HOME is known).
func (s *session) userConfigPath() string {
	return s.workflow().UserConfigPath()
}

// selectProvider picks --provider (else the default) from a valid user
// config and builds it with --engine and --effort applied. It reads no
// network and runs nothing.
func (s *session) selectProvider(loaded *config.Loaded) (infer.Provider, *workflow.ProviderProblem) {
	return s.workflow().SelectProvider(loaded)
}

// openProvider loads the user config and returns the client for
// --provider/--engine, or an exit 1 `provider_unavailable` outcome that says
// how to fix the configuration.
func (s *session) openProvider() (infer.Provider, *outcome) {
	c, _, e := s.workflow().OpenProviderConfig()
	if e != nil {
		return nil, fromWF(e)
	}
	return c, nil
}

// providerErr turns a failed model call into an exit 1 `provider_failed`
// outcome.
func providerErr(err error) *outcome {
	return fromWF(workflow.ProviderErr(err))
}
