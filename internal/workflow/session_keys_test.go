// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
)

func TestSessionCredentialScopeAndSnapshots(t *testing.T) {
	w := &Workflow{Getenv: func(k string) string {
		if k == "KEY" {
			return "environment"
		}
		return ""
	}}
	w.SetSessionKey("first", "session-first")
	p := config.Provider{Kind: "openai", APIKeyEnv: "KEY"}
	first, get, err := w.credentialProvider(config.Config{}, "", "first", p)
	require.NoError(t, err)
	require.Equal(t, "session-first", get(first.APIKeyEnv))
	second, getSecond, err := w.credentialProvider(config.Config{}, "", "second", p)
	require.NoError(t, err)
	require.Equal(t, "environment", getSecond(second.APIKeyEnv))
	cli, getCLI, err := w.credentialProvider(config.Config{}, "", "first", config.Provider{Kind: "claude-cli", APIKeyEnv: "KEY"})
	require.NoError(t, err)
	require.Equal(t, "KEY", cli.APIKeyEnv)
	require.Equal(t, "environment", getCLI("KEY"), "session credentials cannot spill into account CLIs")
	snapshot := w.SessionKeySnapshot()
	snapshot["first"] = "mutated"
	require.Equal(t, "session-first", w.SessionKeySnapshot()["first"])
	w.SetSessionKey("first", "")
	require.Empty(t, w.SessionKeySnapshot())
	require.Equal(t, "session-first", get(first.APIKeyEnv), "a running operation holds its own credential snapshot")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				w.SetSessionKey("race", "value")
				w.SessionKeySnapshot()
				_, _, _ = w.credentialProvider(config.Config{}, "", "race", p)
			}
		}()
	}
	wg.Wait()
}
