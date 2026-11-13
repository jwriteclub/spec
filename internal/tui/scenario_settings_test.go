// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// Settings → Add connection → OpenRouter API sets up a decision (gate), a
// triage and an inference (check) model on one key. The scene's config
// already has a default check provider, so the check role is kept; the
// triage model is typed in, the gate keeps its recommended default. Save
// writes both new connections and their routes and leaves default alone.
func TestScenarioOpenRouterSetupSelectors(t *testing.T) {
	s := Open(t, scenes.Demo)
	s.Press("ctrl+g", "ctrl+t", "tab", "enter")
	s.ExpectText("One key, three models")
	s.Press("enter")
	s.ExpectText("This sets up a decision (gate) model, a triage model and an inference (check) model")
	s.ExpectText("Jev 1.14 · typesafe/jev-1.14 · recommended")
	s.ExpectText("GLM 5.3 Flash, low effort · z-ai/glm-5.3-flash · recommended")
	s.ExpectText(`Keep existing · default = "cf"`)
	s.ExpectHelp("Esc Back", "Ret Choose")

	// The triage picker: the recommended list, then free entry.
	s.Press("down", "enter")
	s.ExpectText("Choose the triage model")
	s.ExpectText("DeepSeek V4.1 Flash, low effort · deepseek/deepseek-v4.1-flash-1017")
	s.ExpectText("Qwen3.8 27B on Cerebras, low effort · qwen/qwen3.8-27b")
	s.Press("down", "down", "down", "enter")
	s.Type("vendor/triage-1")
	s.ExpectText("vendor/triage-1_")

	// The check picker offers the recommended list beside Keep existing.
	s.Press("down", "enter")
	s.ExpectText("Choose the inference (check) model")
	s.ExpectText("DeepSeek V4.1 Flash on Fireworks ultrafast")
	s.Press("esc")

	s.Press("down", "down", "enter")
	s.ExpectText("Staged gate openrouter-gate, triage openrouter-triage; kept check")
	s.Press("ctrl+o")
	s.ExpectText("providers.openrouter-gate")
	s.ExpectText("providers.openrouter-triage")
	s.Press("u")
	s.ExpectText("User saved")

	path := filepath.Join(s.sess.Env["XDG_CONFIG_HOME"], "spec", "config.toml")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	cfg, ds := config.Parse(path, b)
	require.Empty(t, ds, string(b))
	assert.Equal(t, "cf", cfg.Default)
	require.NotNil(t, cfg.Gate)
	assert.Equal(t, "openrouter-gate", cfg.Gate.Provider)
	assert.Equal(t, 0.45, cfg.Gate.EscalateClassAt)
	assert.Equal(t, "typesafe/jev-1.14", cfg.Providers["openrouter-gate"].Model)
	require.NotNil(t, cfg.Triage)
	assert.Equal(t, "openrouter-triage", cfg.Triage.Provider)
	assert.Equal(t, "vendor/triage-1", cfg.Providers["openrouter-triage"].Model)
	assert.NotContains(t, cfg.Providers, "openrouter")
}

// Settings → edit a connection → ^T, which passes → Save: the save status
// names the passing test, as the exact configuration saved is the one
// tested. A recording on 2026-11-08 showed "Saved user settings · not
// tested" right after every role passed.
func TestScenarioSettingsTestThenSaveKeepsPass(t *testing.T) {
	s := Open(t, scenes.Demo, Replies(infer.CannedFile{Check: []infer.CannedEntry{{Reply: "OK"}}}))
	s.Press("ctrl+g", "ctrl+t", "tab", "tab", "tab", "tab")
	s.Type("-v2")
	s.Press("ctrl+t")
	s.ExpectText("Connection test plan · not started")
	s.Press("ctrl+t")
	s.WaitUntil("the test result", func(tui.State) bool { return strings.Contains(strings.Join(s.Frame(), "\n"), "Connection test result") })
	s.ExpectText("check / cf: Test passed")
	s.Press("esc", "ctrl+o", "u")
	s.ExpectText("User saved · test passed")
	// The save writes the config synchronously; its command delivers the flash later.
	s.WaitUntil("the saved settings flash", func(st tui.State) bool {
		return st.Flash == "Saved user settings · test passed"
	})
	s.ExpectNoText("not tested")
}
