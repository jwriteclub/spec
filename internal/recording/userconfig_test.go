// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
)

const userConfigSrc = `
default = "or"
max_output_tokens = 9000
underline_color = "off"
color_scheme = "synthwave-quiet"
color_scheme_dir = "/Users/someone/colours"
glyph_profile = "ascii"
foreground_profile = "light"
color_capability = "256"

[providers.or]
base_url = "https://openrouter.ai/api/v1"
model = "big-model"
triage_model = "small-model"
api_key_env = "OPENROUTER_API_KEY"
extra = { reasoning = { effort = "low" } }
timeout = 30

[providers.cc]
kind = "claude-cli"
command = "/usr/local/bin/claude"
model = "opus"
effort = "high"
pass_env = ["FOO"]

[providers.jev]
kind = "decisions"
api_key_env = "JEV_KEY"
model = "jev-1"

[gate]
provider = "jev"
escalate_class_at = 0.4

[triage]
provider = "or"

[prompts]
change = "prompts/change.md"
`

// Every provider becomes canned on the token path, with no transport or
// key settings left; the cascade's tables stay.
func TestCannedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(userConfigSrc), 0o644))
	loaded, err := config.Load(path)
	require.NoError(t, err)
	require.Empty(t, loaded.Diagnostics)

	out, err := CannedConfig(loaded)
	require.NoError(t, err)
	src := string(out)
	for _, gone := range []string{"api_key_env", "base_url", "command", "extra", "pass_env", "effort", "timeout", "claude-cli", "decisions\"\n", "prompts"} {
		assert.NotContains(t, src, gone)
	}
	cfg, diags := config.Parse("config.toml", out)
	require.Empty(t, diags, src)
	for name, p := range cfg.Providers {
		assert.Equal(t, config.KindCanned, p.Kind, name)
		assert.Equal(t, CannedPathToken, p.Path, name)
	}
	assert.Equal(t, "small-model", cfg.Providers["or"].TriageModel)
	assert.Equal(t, "jev-1", cfg.Providers["jev"].Model)
	require.NotNil(t, cfg.Gate)
	assert.Equal(t, "jev", cfg.Gate.Provider)
	require.NotNil(t, cfg.Triage)
	assert.Equal(t, "or", cfg.Triage.Provider)
	assert.Equal(t, "or", cfg.Default)
	assert.Equal(t, 9000, cfg.MaxOutputTokens)
	assert.Equal(t, "off", cfg.UnderlineColor)
	assert.Equal(t, "synthwave-quiet", cfg.ColorScheme, "the replay looks like the recording")
	assert.Equal(t, "ascii", cfg.GlyphProfile)
	assert.Equal(t, "light", cfg.ForegroundProfile)
	assert.Equal(t, "256", cfg.ColorCapability)
	assert.Empty(t, cfg.ColorSchemeDir, "a scheme directory is the recording machine's")
	assert.Equal(t, config.Prompts{}, cfg.Prompts, "prompt files are the recording machine's")

	empty, err := CannedConfig(&config.Loaded{Path: path})
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// A custom scheme lives in color_scheme_dir, which a canned copy drops, so
// the scheme goes too: the copy is still a valid config, the replay shows
// Synthwave, and the rest of the recorded appearance applies.
func TestCannedConfigCustomScheme(t *testing.T) {
	src := "color_scheme = \"custom:mine\"\ncolor_scheme_dir = \"/Users/someone/colours\"\nglyph_profile = \"ascii\"\nforeground_profile = \"light\"\ncolor_capability = \"256\"\n"
	_, diags := config.Parse("config.toml", []byte(src))
	require.Empty(t, diags, "the recorded config is valid")

	out, err := cannedCopy([]byte(src), CannedPathToken)
	require.NoError(t, err)
	cfg, diags := config.Parse("config.toml", out)
	require.Empty(t, diags, string(out))
	assert.Empty(t, cfg.ColorScheme, "the custom scheme falls back to the default")
	assert.Empty(t, cfg.ColorSchemeDir)
	assert.Equal(t, "ascii", cfg.GlyphProfile)
	assert.Equal(t, "light", cfg.ForegroundProfile)
	assert.Equal(t, "256", cfg.ColorCapability)
}
