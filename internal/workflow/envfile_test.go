// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer"
)

// envFileSecret stands in for an API key that lives only in the env_file.
const envFileSecret = "sk-wf-envfile-sentinel-31337"

// envFileHome is a temporary home with a user config (cfg) beside its
// env_file, and a Workflow whose environment is env.
type envFileHome struct {
	home, cfgPath, envPath string
	env                    map[string]string
	wf                     *Workflow
}

func newEnvFileHome(t *testing.T, cfg, envText string, mode os.FileMode) *envFileHome {
	t.Helper()
	h := &envFileHome{home: t.TempDir()}
	dir := filepath.Join(h.home, ".config", "spec")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	h.cfgPath = filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(h.cfgPath, []byte(cfg), 0o600))
	h.envPath = filepath.Join(dir, "env")
	if envText != "" {
		require.NoError(t, os.WriteFile(h.envPath, []byte(envText), 0o600))
		require.NoError(t, os.Chmod(h.envPath, mode))
	}
	h.env = map[string]string{"HOME": h.home, "PATH": os.Getenv("PATH")}
	h.wf = &Workflow{Getenv: func(k string) string { return h.env[k] }}
	return h
}

func (h *envFileHome) load(t *testing.T) *config.Loaded {
	t.Helper()
	l, err := config.Load(h.cfgPath)
	require.NoError(t, err)
	require.Empty(t, l.Diagnostics)
	return l
}

// keyServer answers chat requests and records the path and Authorization
// header of each.
type keyServer struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
	auths []string
}

func newKeyServer(t *testing.T) *keyServer {
	t.Helper()
	s := &keyServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "m",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "OK"}, "finish_reason": "stop"}},
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func TestSelectProviderReadsEnvFile(t *testing.T) {
	srv := newKeyServer(t)
	h := newEnvFileHome(t, `default = "p"
env_file = "env"

[providers.p]
base_url = "`+srv.URL+`/accounts/${ACCT}/v1"
model = "m"
api_key_env = "KEY"
stream = false
`, "# keys\nexport KEY='"+envFileSecret+"'\nACCT=from-file\n", 0o600)

	c, prob := h.wf.SelectProvider(h.load(t))
	require.Nil(t, prob)
	_, err := c.Chat(context.Background(), infer.Request{Messages: []infer.Message{infer.User("hi")}, MaxTokens: 4})
	require.NoError(t, err)
	assert.Equal(t, []string{"/accounts/from-file/v1/chat/completions"}, srv.paths)
	assert.Equal(t, []string{"Bearer " + envFileSecret}, srv.auths)

	// The environment wins over the file; an empty variable falls back.
	h.env["ACCT"] = "from-env"
	h.env["KEY"] = ""
	c, prob = h.wf.SelectProvider(h.load(t))
	require.Nil(t, prob)
	_, err = c.Chat(context.Background(), infer.Request{Messages: []infer.Message{infer.User("hi")}, MaxTokens: 4})
	require.NoError(t, err)
	assert.Equal(t, "/accounts/from-env/v1/chat/completions", srv.paths[1])
	assert.Equal(t, "Bearer "+envFileSecret, srv.auths[1])
}

func TestEnvFileTildePath(t *testing.T) {
	h := newEnvFileHome(t, `default = "p"
env_file = "~/.config/spec/env"

[providers.p]
base_url = "http://127.0.0.1:1/v1"
model = "m"
api_key_env = "KEY"
`, "KEY="+envFileSecret+"\n", 0o600)
	l := h.load(t)
	env, err := h.wf.ProviderEnv(l.Config, l.Path, l.Config.Providers["p"])
	require.NoError(t, err)
	assert.Equal(t, envFileSecret, env.Get("KEY"))
	assert.Equal(t, config.SourceEnvFile, env.Source("KEY"))
}

func TestEnvFileRefusedIsAProviderProblem(t *testing.T) {
	h := newEnvFileHome(t, `default = "p"
env_file = "env"

[providers.p]
base_url = "http://127.0.0.1:1/v1"
model = "m"
api_key_env = "KEY"
`, "KEY="+envFileSecret+"\n", 0o644)
	_, prob := h.wf.SelectProvider(h.load(t))
	require.NotNil(t, prob)
	assert.False(t, prob.Warn)
	assert.Contains(t, prob.Msg, h.envPath)
	assert.Contains(t, prob.Msg, "mode 0644")
	assert.Equal(t, "chmod 600 "+h.envPath, prob.Fix)
	assert.NotContains(t, prob.Msg+prob.Fix, envFileSecret)

	// With every variable in the environment the file is never read.
	h.env["KEY"] = "from-env"
	_, prob = h.wf.SelectProvider(h.load(t))
	assert.Nil(t, prob)
}

func TestEnvFileNotReadWithoutNeed(t *testing.T) {
	// A provider that needs no variable, and a missing env_file.
	h := newEnvFileHome(t, `default = "local"
env_file = "missing.env"

[providers.local]
base_url = "http://127.0.0.1:1/v1"
model = "m"
`, "", 0)
	_, prob := h.wf.SelectProvider(h.load(t))
	assert.Nil(t, prob)
	l := h.load(t)
	_, err := h.wf.EnvFile(l.Config, l.Path)
	var fe *config.EnvFileError
	require.ErrorAs(t, err, &fe)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestEnvFileNotPassedToCLI(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	for _, kind := range []string{config.KindClaudeCLI, config.KindCodexCLI} {
		t.Run(kind, func(t *testing.T) {
			h := newEnvFileHome(t, `default = "cli"
env_file = "env"

[providers.cli]
kind = "`+kind+`"
command = "`+script+`"
model = "m"
pass_env = ["KEY", "SHELL_ONLY"]
`, "KEY="+envFileSecret+"\nSHELL_ONLY=file\n", 0o600)
			h.env["SHELL_ONLY"] = "shell"
			c, prob := h.wf.SelectProvider(h.load(t))
			require.Nil(t, prob)
			var env []string
			switch p := c.(type) {
			case *infer.ClaudeCLI:
				env = p.Env
			case *infer.CodexCLI:
				env = p.Env
			default:
				t.Fatalf("unexpected provider %T", c)
			}
			joined := strings.Join(env, "\n")
			assert.NotContains(t, joined, envFileSecret)
			assert.NotContains(t, joined, "KEY=")
			assert.Contains(t, env, "SHELL_ONLY=shell", "pass_env forwards the real environment")
		})
	}
}

func TestEnvFileCascadeAndSessionKey(t *testing.T) {
	h := newEnvFileHome(t, `default = "p"
env_file = "env"

[providers.p]
base_url = "http://127.0.0.1:1/${ACCT}/v1"
model = "m"
api_key_env = "KEY"
`, "ACCT=a1\nKEY=file-key\n", 0o600)
	l := h.load(t)
	h.wf.SetSessionKey("p", "session-key")
	p, get, err := h.wf.credentialProvider(l.Config, l.Path, "p", l.Config.Providers["p"])
	require.NoError(t, err)
	assert.Equal(t, "session-key", get(p.APIKeyEnv))
	assert.Equal(t, "a1", get("ACCT"), "base_url variables still come from the env_file")
}
