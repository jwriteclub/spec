// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer"
)

// UserConfigPath is where the user config lives ("" when neither HOME nor
// XDG_CONFIG_HOME is known).
func (w *Workflow) UserConfigPath() string {
	return config.PathFor(w.getenv("XDG_CONFIG_HOME"), w.home())
}

// Home is the user's home directory (HOME through Getenv, else the
// process's).
func (w *Workflow) Home() string { return w.home() }

// home is the user's home directory.
func (w *Workflow) home() string {
	home := w.getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return home
}

// ProviderProblem is why no client could be built, with how to fix it.
type ProviderProblem struct {
	// Warn is true for something not set up yet, false for something
	// wrong.
	Warn bool
	Msg  string
	Fix  string
}

// SelectProvider picks w.Provider (else the default) from a valid user
// config and builds it with w.Model and w.Effort applied. It reads no
// network and runs nothing.
func (w *Workflow) SelectProvider(loaded *config.Loaded) (infer.Provider, *ProviderProblem) {
	cfgPath := loaded.Path
	switch {
	case !loaded.Exists:
		return nil, &ProviderProblem{true, "no user config at " + cfgPath, "create it with a [providers.NAME] table (`spec explain config` has examples)"}
	case len(loaded.Diagnostics) > 0:
		return nil, &ProviderProblem{false, cfgPath + ": " + loaded.Diagnostics[0].Message, "edit " + cfgPath + " (`spec explain config` shows the format)"}
	}
	name, p, err := loaded.Config.Provider(w.Provider)
	if err != nil {
		if w.Provider == "" {
			return nil, &ProviderProblem{true, err.Error(), `add default = "NAME" and a [providers.NAME] table to ` + cfgPath}
		}
		return nil, &ProviderProblem{false, err.Error(), "pass --provider with a name defined in " + cfgPath}
	}
	kind := p.KindOrDefault()
	if e := w.Effort; e != "" && !config.ValidEffort(kind, e) {
		return nil, &ProviderProblem{false, fmt.Sprintf("--effort %q is not a level of provider %s (kind %s)", e, name, kind),
			"pass one of " + strings.Join(config.EffortLevels(kind), ", ")}
	}
	resolved, getenv, err := w.credentialProvider(loaded.Config, cfgPath, name, p)
	if err != nil {
		return nil, envFileProblem(err, cfgPath)
	}
	c, err := infer.Open(name, resolved, getenv)
	if err != nil {
		var ee *config.EnvError
		var se *infer.SetupError
		switch {
		case errors.As(err, &ee):
			return nil, &ProviderProblem{true, err.Error(), ee.Fix()}
		case errors.As(err, &se):
			return nil, &ProviderProblem{true, err.Error(), se.Fix()}
		}
		return nil, &ProviderProblem{false, err.Error(), "edit " + cfgPath}
	}
	if w.Model != "" {
		c.SetModel(w.Model)
	}
	if w.Effort != "" {
		c.SetEffort(w.Effort)
	}
	if hc, ok := c.(*infer.Client); ok && w.TuneClient != nil {
		w.TuneClient(hc)
	}
	return w.wrapProvider(c), nil
}

// envFileProblem is the problem of an env_file that cannot be used.
func envFileProblem(err error, cfgPath string) *ProviderProblem {
	var fe *config.EnvFileError
	if errors.As(err, &fe) {
		return &ProviderProblem{false, err.Error(), fe.Fix()}
	}
	return &ProviderProblem{false, err.Error(), "fix env_file in " + cfgPath}
}

// EnvFile loads the env_file cfg names (cfg read from cfgPath), once per
// EnvFiles cache. It returns nil and no error when cfg sets none.
func (w *Workflow) EnvFile(cfg config.Config, cfgPath string) (*config.EnvFileData, error) {
	if cfg.EnvFile == "" {
		return nil, nil
	}
	cache := w.EnvFiles
	if cache == nil {
		cache = &w.ownEnvFiles
	}
	return cache.Load(config.EnvFilePath(cfg.EnvFile, cfgPath, w.home()))
}

// ProviderEnv returns the lookup for provider p's settings: the process
// environment, then the env_file cfg names. The file is read only when one
// of the provider's variables (config.Provider.EnvNames) is unset or empty
// in the environment, so a command whose provider needs nothing from it
// never reads it. The lookup is for api_key_env and base_url only; a CLI
// provider's environment comes from Getenv alone.
func (w *Workflow) ProviderEnv(cfg config.Config, cfgPath string, p config.Provider) (config.EnvLookup, error) {
	env := config.EnvLookup{Getenv: w.getenv}
	if cfg.EnvFile == "" {
		return env, nil
	}
	need := false
	for _, n := range p.EnvNames() {
		if w.getenv(n) == "" {
			need = true
		}
	}
	if !need {
		return env, nil
	}
	d, err := w.EnvFile(cfg, cfgPath)
	if err != nil {
		return env, err
	}
	env.File = d.Vars
	return env, nil
}

// OpenProviderConfig loads the user config and returns the client for
// w.Provider/w.Model/w.Effort with the loaded config, or an exit 1
// `provider_unavailable` error that says how to fix the configuration.
func (w *Workflow) OpenProviderConfig() (infer.Provider, *config.Loaded, *Error) {
	path := w.UserConfigPath()
	if path == "" {
		return nil, nil, fail(ExitFail, "provider_unavailable", "cannot tell where the user config lives (no HOME or XDG_CONFIG_HOME); set one of them")
	}
	loaded, err := config.Load(path)
	if err != nil {
		return nil, nil, fail(ExitFail, "provider_unavailable", "cannot read %s: %v", path, err)
	}
	c, prob := w.SelectProvider(loaded)
	if prob != nil {
		return nil, nil, fail(ExitFail, "provider_unavailable", "%s; fix: %s", prob.Msg, prob.Fix)
	}
	return c, loaded, nil
}

// ProviderErr turns a failed model call into an exit 1 `provider_failed`
// error.
func ProviderErr(err error) *Error {
	var ie *infer.Error
	if errors.As(err, &ie) {
		return fail(ExitFail, "provider_failed", "%s; fix: %s", ie.Error(), ie.Fix())
	}
	return fail(ExitFail, "provider_failed", "%s", fmt.Sprint(err))
}
