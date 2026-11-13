// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/workflow"
)

// openRouterSettings opens Settings on a user config with src and starts
// the OpenRouter setup.
func openRouterSettings(t *testing.T, src string) *settingsScreen {
	t.Helper()
	dir := t.TempDir()
	u := config.NewDocument(filepath.Join(dir, "user.toml"), []byte(src), src != "", userSettingsValidate)
	p := config.NewDocument(filepath.Join(dir, "spec.toml"), nil, false, projectSettingsValidate)
	s := newSettingsWithDocuments(u, p)
	s.selectFirstProvider()
	s.openOpenRouterSetup()
	return s
}

// stagedConfig parses the staged user draft, failing on any diagnostic.
func stagedConfig(t *testing.T, s *settingsScreen) (config.Config, string) {
	t.Helper()
	b, err := s.user.Bytes()
	require.NoError(t, err)
	cfg, ds := config.Parse("draft", b)
	require.Empty(t, ds, string(b))
	return cfg, string(b)
}

func pressAll(s *settingsScreen, keys ...string) {
	for _, k := range keys {
		s.openRouterKey(key(k))
	}
}

// chooseOption opens the picker on role row and picks the option whose
// label contains text.
func chooseOption(t *testing.T, s *settingsScreen, row int, text string) {
	t.Helper()
	s.openrouter.row = row
	pressAll(s, "enter")
	require.True(t, s.openrouter.picking)
	for i, opt := range s.openrouter.roles[row].options {
		if strings.Contains(opt.text(nil), text) {
			s.openrouter.choice = i
			pressAll(s, "enter")
			return
		}
	}
	t.Fatalf("no option %q on row %d", text, row)
}

func TestOpenRouterSetupStagesAllThreeRoles(t *testing.T) {
	s := openRouterSettings(t, "")
	a := &App{theme: NewTheme(true, false)}
	screen := strings.Join(s.view(a, 100, 40), "\n")
	for _, want := range []string{"decision (gate) model", "triage model", "inference (check) model", "Jev 1.14", "GLM 5.3 Flash", "DeepSeek V4.1 Flash", "OPENROUTER_API_KEY"} {
		assert.Contains(t, screen, want)
	}

	s.openrouter.row = s.openrouter.stageRow()
	pressAll(s, "enter")
	require.Nil(t, s.openrouter, s.notice)
	cfg, src := stagedConfig(t, s)

	assert.Equal(t, "openrouter", cfg.Default)
	check := cfg.Providers["openrouter"]
	assert.Equal(t, config.KindOpenAI, check.Kind)
	assert.Equal(t, config.OpenRouterBaseURL, check.BaseURL)
	assert.Equal(t, "OPENROUTER_API_KEY", check.APIKeyEnv)
	assert.Equal(t, "deepseek/deepseek-v4.1-flash-1017", check.Model)
	assert.True(t, check.JSONMode, "DeepSeek keeps thinking on with json_mode")
	assert.Equal(t, map[string]any{"effort": "low"}, check.Extra["reasoning"])
	assert.Equal(t, 1800, check.Timeout)
	assert.Equal(t, 120, check.IdleTimeout)

	require.NotNil(t, cfg.Triage)
	assert.Equal(t, "openrouter-triage", cfg.Triage.Provider)
	triage := cfg.Providers["openrouter-triage"]
	assert.Equal(t, "z-ai/glm-5.3-flash", triage.Model)
	assert.Equal(t, map[string]any{"effort": "low"}, triage.Extra["reasoning"], "GLM needs effort low")
	assert.True(t, triage.JSONMode)

	require.NotNil(t, cfg.Gate)
	assert.Equal(t, "openrouter-gate", cfg.Gate.Provider)
	assert.Equal(t, 0.45, cfg.Gate.EscalateClassAt)
	assert.Equal(t, 0.0, cfg.Gate.SkipMaterialBelow)
	gate := cfg.Providers["openrouter-gate"]
	assert.Equal(t, config.KindDecisions, gate.Kind)
	assert.Equal(t, "typesafe/jev-1.14", gate.Model)
	assert.Equal(t, "OPENROUTER_API_KEY", gate.APIKeyEnv)
	assert.Empty(t, gate.BaseURL, "the Decisions API root is the default")

	assert.Equal(t, 1, s.section, "lands on Model routing, where ^T tests every role")
	assert.Contains(t, s.notice, "Staged gate openrouter-gate, triage openrouter-triage, check openrouter")
	assert.NotContains(t, src, "key =")
}

func TestOpenRouterSetupStagesGeminiGate(t *testing.T) {
	s := openRouterSettings(t, "")
	chooseOption(t, s, 0, "Gemini Decision 1")
	s.stageOpenRouter()
	require.Nil(t, s.openrouter, s.notice)
	cfg, _ := stagedConfig(t, s)
	require.NotNil(t, cfg.Gate)
	gate := cfg.Providers[cfg.Gate.Provider]
	assert.Equal(t, config.KindDecisions, gate.Kind)
	assert.Equal(t, "google/gemini-decision-1", gate.Model)
	assert.Equal(t, config.OpenRouterKeyEnv, gate.APIKeyEnv)
	assert.Empty(t, gate.API, "the default API is OpenRouter")
	assert.Empty(t, gate.BaseURL, "the Decisions API root is the default")
}

func TestOpenRouterSetupKeepsExistingRoutes(t *testing.T) {
	const existing = `default = 'fixture'
[providers.fixture]
base_url = 'http://localhost/v1'
model = 'fixture/model'
[providers.jev]
kind = 'decisions'
model = 'typesafe/jev-1.13'
[gate]
provider = 'jev'
escalate_class_at = 0.6
[triage]
provider = 'fixture'
`
	s := openRouterSettings(t, existing)
	for _, r := range s.openrouter.roles {
		require.Equal(t, pickKeep, r.chosen().pick, "%s defaults to keep", r.role)
		assert.NotEmpty(t, r.existing)
	}
	a := &App{theme: NewTheme(true, false)}
	screen := strings.Join(s.view(a, 120, 40), "\n")
	assert.Contains(t, screen, `Keep existing · [gate] provider = "jev"`)
	assert.Contains(t, screen, `Keep existing · default = "fixture"`)

	s.stageOpenRouter()
	assert.Empty(t, s.user.Changes(), "keeping every role stages nothing")
	assert.Contains(t, s.notice, "Nothing staged")

	// Replacing one role takes an explicit choice; the others stay.
	s.openOpenRouterSetup()
	chooseOption(t, s, 0, "Morph Reflex")
	s.stageOpenRouter()
	cfg, _ := stagedConfig(t, s)
	assert.Equal(t, "openrouter-gate", cfg.Gate.Provider)
	assert.Equal(t, 0.6, cfg.Gate.EscalateClassAt, "an existing gate keeps its thresholds")
	assert.Equal(t, "morph/reflex", cfg.Providers["openrouter-gate"].Model)
	assert.Equal(t, "fixture", cfg.Default)
	assert.Equal(t, "fixture", cfg.Triage.Provider)
	assert.Contains(t, cfg.Providers, "jev", "the replaced gate's connection is left in place")
	assert.NotContains(t, cfg.Providers, "openrouter")
	assert.NotContains(t, cfg.Providers, "openrouter-triage")
	assert.Contains(t, s.notice, "Staged gate openrouter-gate; kept triage, check")
}

func TestOpenRouterSetupTriageModelCountsAsExistingTriage(t *testing.T) {
	s := openRouterSettings(t, "default = 'cf'\n[providers.cf]\nbase_url = 'http://localhost/v1'\nmodel = 'big'\ntriage_model = 'small'\n")
	triage := s.openrouter.roles[1]
	require.Equal(t, config.RoleTriage, triage.role)
	assert.Equal(t, pickKeep, triage.chosen().pick)
	assert.Contains(t, triage.existing, `triage_model "small" on cf`)
	assert.Equal(t, pickRecommended, s.openrouter.roles[0].chosen().pick, "no gate yet: the recommended one")
}

func TestOpenRouterSetupFreeEntry(t *testing.T) {
	s := openRouterSettings(t, "")
	chooseOption(t, s, 2, "Other model ID")
	// Nothing typed yet: staging refuses and stages nothing.
	s.stageOpenRouter()
	require.NotNil(t, s.openrouter)
	assert.Contains(t, s.notice, "enter a model id")
	assert.Empty(t, s.user.Changes())

	pressAll(s, "a", "c", "m", "e", "/", "x", "x", "backspace")
	s.openRouterPaste("-model ")
	assert.Equal(t, "acme/x-model", s.openrouter.roles[2].custom)
	chooseOption(t, s, 1, "Other model ID")
	s.openRouterPaste("vendor/triage-1")
	chooseOption(t, s, 0, "Other model ID")
	s.openRouterPaste("vendor/decider")
	s.stageOpenRouter()
	require.Nil(t, s.openrouter, s.notice)
	cfg, _ := stagedConfig(t, s)
	check := cfg.Providers["openrouter"]
	assert.Equal(t, "acme/x-model", check.Model)
	assert.False(t, check.JSONMode, "a pasted model gets no tuned settings")
	assert.Nil(t, check.Extra)
	assert.Equal(t, "vendor/triage-1", cfg.Providers["openrouter-triage"].Model)
	assert.Equal(t, "vendor/decider", cfg.Providers["openrouter-gate"].Model)
	assert.Equal(t, config.KindDecisions, cfg.Providers["openrouter-gate"].Kind)
}

func TestOpenRouterSetupFireworksPinStaysOnTheCheck(t *testing.T) {
	s := openRouterSettings(t, "")
	chooseOption(t, s, 2, "Fireworks ultrafast")
	s.stageOpenRouter()
	cfg, _ := stagedConfig(t, s)
	assert.Equal(t, map[string]any{"order": []any{"fireworks/ultrafast"}, "allow_fallbacks": false}, cfg.Providers["openrouter"].Extra["provider"])
	_, pinned := cfg.Providers["openrouter-triage"].Extra["provider"]
	assert.False(t, pinned, "triage is its own connection, so the check's endpoint pin does not reach GLM")
}

func TestOpenRouterSetupSessionKeyCoversEveryNewConnection(t *testing.T) {
	s := openRouterSettings(t, "")
	a := &App{theme: NewTheme(true, false), wf: &workflow.Workflow{}}
	s.openrouter.row = s.openrouter.keyRow()
	s.openRouterPaste("fixture-openrouter-credential")
	screen := strings.Join(s.view(a, 100, 40), "\n")
	assert.NotContains(t, screen, "fixture-openrouter-credential")
	assert.Contains(t, screen, "********")
	s.stageOpenRouter()
	for _, name := range []string{"openrouter", "openrouter-triage", "openrouter-gate"} {
		assert.Equal(t, "fixture-openrouter-credential", s.secrets[name], name)
	}
	_, src := stagedConfig(t, s)
	assert.NotContains(t, src, "fixture-openrouter-credential")
}

func TestOpenRouterSetupAvoidsNameCollisions(t *testing.T) {
	s := openRouterSettings(t, "[providers.openrouter]\nbase_url = 'http://localhost/v1'\nmodel = 'old'\n")
	s.stageOpenRouter()
	cfg, _ := stagedConfig(t, s)
	assert.Equal(t, "openrouter-2", cfg.Default)
	assert.Equal(t, "old", cfg.Providers["openrouter"].Model)
}

func TestOpenRouterSetupFromChooserAndBack(t *testing.T) {
	s := openRouterSettings(t, "")
	s.openrouter = nil
	a := &App{theme: NewTheme(true, false)}
	s.setup = true
	s.key(a, key("enter"), Binding{}, false)
	require.NotNil(t, s.openrouter, "choice 0 opens the three-role setup")
	assert.Empty(t, s.user.Changes(), "opening the setup stages nothing")
	s.key(a, key("esc"), Binding{}, false)
	assert.Nil(t, s.openrouter)
	assert.True(t, s.setup, "Esc returns to the connection chooser")
}

func TestOpenRouterModelsTable(t *testing.T) {
	for _, role := range []config.Role{config.RoleGate, config.RoleTriage, config.RoleCheck} {
		ms := config.OpenRouterChoices(role)
		require.NotEmpty(t, ms, role)
		for _, m := range ms {
			assert.NotEmpty(t, m.Label)
			assert.NotEmpty(t, m.Note)
			assert.NotContains(t, m.Model, " ")
		}
	}
	gates := config.OpenRouterChoices(config.RoleGate)
	require.GreaterOrEqual(t, len(gates), 2)
	assert.Equal(t, "typesafe/jev-1.14", gates[0].Model)
	assert.Equal(t, "google/gemini-decision-1", gates[1].Model)
	assert.Equal(t, "z-ai/glm-5.3-flash", config.OpenRouterChoices(config.RoleTriage)[0].Model)
	assert.Equal(t, "deepseek/deepseek-v4.1-flash-1017", config.OpenRouterChoices(config.RoleCheck)[0].Model)
	// Provider must hand out copies: editing a staged table cannot change the table.
	p := config.OpenRouterChoices(config.RoleCheck)[0].Provider()
	p["extra"].(map[string]any)["reasoning"] = "changed"
	assert.Equal(t, map[string]any{"effort": "low"}, config.OpenRouterChoices(config.RoleCheck)[0].Extra["reasoning"])
}
