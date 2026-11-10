// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import "github.com/jwriteclub/spec/internal/config"

func (w *Workflow) SetSessionKey(name, key string) {
	w.sessionMu.Lock()
	defer w.sessionMu.Unlock()
	if w.sessionKeys == nil {
		w.sessionKeys = map[string]string{}
	}
	if key == "" {
		delete(w.sessionKeys, name)
	} else {
		w.sessionKeys[name] = key
	}
}
func (w *Workflow) SessionKeySnapshot() map[string]string {
	w.sessionMu.RLock()
	defer w.sessionMu.RUnlock()
	out := map[string]string{}
	for n, k := range w.sessionKeys {
		out[n] = k
	}
	return out
}

// credentialProvider captures one provider's key without modifying the process
// environment or exposing it to an account CLI's pass_env. The returned
// getenv reads the environment, then the user config's env_file (cfg, at
// cfgPath), for the provider's settings; a CLI provider gets the
// environment alone, so pass_env never forwards an env_file value.
func (w *Workflow) credentialProvider(cfg config.Config, cfgPath, name string, p config.Provider) (config.Provider, func(string) string, error) {
	if p.IsCLI() {
		return p, w.getenv, nil
	}
	w.sessionMu.RLock()
	key := w.sessionKeys[name]
	w.sessionMu.RUnlock()
	if key == "" {
		env, err := w.ProviderEnv(cfg, cfgPath, p)
		if err != nil {
			return p, nil, err
		}
		return p, env.Get, nil
	}
	// The session key replaces api_key_env: only base_url's variables may
	// still need the env_file.
	p.APIKeyEnv = ""
	env, err := w.ProviderEnv(cfg, cfgPath, p)
	if err != nil {
		return p, nil, err
	}
	const variable = "__SPEC_SESSION_CREDENTIAL"
	p.APIKeyEnv = variable
	return p, func(k string) string {
		if k == variable {
			return key
		}
		return env.Get(k)
	}, nil
}
