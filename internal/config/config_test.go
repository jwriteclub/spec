// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/explain"
	_ "github.com/jwriteclub/spec/internal/golden"
)

const valid = `default = "cf"

[providers.cf]
base_url = "https://api.cloudflare.com/client/v4/accounts/x/ai/v1"
model = "@cf/zai-org/glm"
api_key_env = "CLOUDFLARE_API_TOKEN"

[providers.local]
base_url = "http://localhost:11434/v1"
model = "qwen"
triage_model = "qwen-small"
`

func TestPathFor(t *testing.T) {
	assert.Equal(t, "/x/spec/config.toml", PathFor("/x", "/home/u"))
	assert.Equal(t, "/home/u/.config/spec/config.toml", PathFor("", "/home/u"))
	assert.Equal(t, "", PathFor("", ""))
}

func TestPathUsesEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	assert.Equal(t, "/xdg/spec/config.toml", Path())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/h")
	assert.Equal(t, "/h/.config/spec/config.toml", Path())
}

func TestParseValid(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte(valid))
	assert.Empty(t, ds)
	assert.Equal(t, "cf", cfg.Default)
	require.Len(t, cfg.Providers, 2)
	assert.Equal(t, "qwen-small", cfg.Providers["local"].TriageModel)
	name, p, ok := cfg.DefaultProvider()
	require.True(t, ok)
	assert.Equal(t, "cf", name)
	assert.Equal(t, "CLOUDFLARE_API_TOKEN", p.APIKeyEnv)
}

func TestParseEmpty(t *testing.T) {
	cfg, ds := Parse("c.toml", nil)
	assert.Empty(t, ds)
	assert.Empty(t, cfg.Providers)
	_, _, ok := cfg.DefaultProvider()
	assert.False(t, ok)
}

func problems(t *testing.T, src string) []diag.Diagnostic {
	t.Helper()
	_, ds := Parse("c.toml", []byte(src))
	for _, d := range ds {
		assert.Equal(t, diag.CodeConfigInvalid, d.Code)
		assert.Equal(t, diag.Error, d.Severity)
		assert.Equal(t, "c.toml", d.File)
	}
	return ds
}

func TestParseUnknownKeys(t *testing.T) {
	src := "default = \"a\"\nbogus = 1\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\nnope = true\n"
	ds := problems(t, src)
	require.Len(t, ds, 2)
	assert.Contains(t, ds[0].Message, `"bogus"`)
	assert.Equal(t, "bogus", src[ds[0].Range.Start:ds[0].Range.End])
	assert.Contains(t, ds[1].Message, "nope")
}

func TestParseProviderProblems(t *testing.T) {
	ds := problems(t, "default = \"a\"\n[providers.a]\n")
	require.Len(t, ds, 2)
	assert.Contains(t, ds[0].Message, "base_url is required")
	assert.Contains(t, ds[1].Message, "model is required")

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"ftp://x\"\nmodel = \"m\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "http or https")

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\napi_key_env = \"sk-abc123!\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "never the key itself")
}

func TestParseDefaultProblems(t *testing.T) {
	ds := problems(t, "[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "default is not set")

	src := "default = \"zzz\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\n"
	ds = problems(t, src)
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "names no provider")
	assert.Equal(t, "default", src[ds[0].Range.Start:ds[0].Range.End])

	ds = problems(t, "default = \"zzz\"\n")
	require.Len(t, ds, 1)
}

func TestParseSyntaxAndTypeErrors(t *testing.T) {
	ds := problems(t, "default = \"a\n")
	require.Len(t, ds, 1)
	ds = problems(t, "[providers.a]\nbase_url = 3\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "base_url")
}

func TestLoadMissingFile(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "nope", "config.toml"))
	require.NoError(t, err)
	assert.False(t, l.Exists)
	assert.Empty(t, l.Diagnostics)
}

func TestLoadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte(valid), 0o600))
	l, err := Load(p)
	require.NoError(t, err)
	assert.True(t, l.Exists)
	assert.Empty(t, l.Diagnostics)
	assert.Equal(t, p, l.Path)
	assert.Equal(t, "cf", l.Config.Default)

	require.NoError(t, os.WriteFile(p, []byte("bogus = 1\n"), 0o600))
	l, err = Load(p)
	require.NoError(t, err)
	assert.True(t, l.Exists)
	require.Len(t, l.Diagnostics, 1)
	assert.Equal(t, p, l.Diagnostics[0].File)
}

func TestLoadDirectoryIsAnError(t *testing.T) {
	_, err := Load(t.TempDir())
	assert.Error(t, err)
}

func TestAPIKey(t *testing.T) {
	env := map[string]string{"SET": "secret", "EMPTY": ""}
	get := func(k string) string { return env[k] }

	key, name, ok := Provider{APIKeyEnv: "SET"}.APIKey(get)
	assert.True(t, ok)
	assert.Equal(t, "secret", key)
	assert.Equal(t, "SET", name)

	_, name, ok = Provider{APIKeyEnv: "EMPTY"}.APIKey(get)
	assert.False(t, ok)
	assert.Equal(t, "EMPTY", name)

	_, name, ok = Provider{}.APIKey(get)
	assert.False(t, ok)
	assert.Equal(t, "", name)
}

const cloudflare = `default = "cf"

[providers.cf]
base_url = "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/ai/v1"
model = "@cf/zai-org/glm-5.3-flash"
triage_model = "@cf/zai-org/glm-5.3-flash"
api_key_env = "CLOUDFLARE_AI_TOKEN"
json_mode = true
extra = { max_completion_tokens = 4, nested = { a = "b" } }
triage_extra = { reasoning_effort = "none" }
`

func TestParseProviderOptions(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte(cloudflare))
	require.Empty(t, ds)
	p := cfg.Providers["cf"]
	assert.True(t, p.JSONMode)
	assert.Equal(t, int64(4), p.Extra["max_completion_tokens"])
	assert.Equal(t, map[string]any{"a": "b"}, p.Extra["nested"])
	assert.Equal(t, map[string]any{"reasoning_effort": "none"}, p.TriageExtra)
}

func TestParseBaseURLVariables(t *testing.T) {
	// A variable in the host still validates: the shape is checked with a
	// stand-in, and the variable is read only when the provider is used.
	_, ds := Parse("c.toml", []byte("default = \"a\"\n[providers.a]\nbase_url = \"https://${HOST}/v1\"\nmodel = \"m\"\n"))
	assert.Empty(t, ds)

	for _, bad := range []string{"https://x/${1BAD}/v1", "https://x/${}/v1", "https://x/${OPEN/v1"} {
		ds := problems(t, "default = \"a\"\n[providers.a]\nbase_url = \""+bad+"\"\nmodel = \"m\"\n")
		require.Len(t, ds, 1, bad)
		assert.Contains(t, ds[0].Message, "base_url", bad)
	}
	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"ftp://${HOST}/v1\"\nmodel = \"m\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "http or https")
}

func TestParseReservedExtra(t *testing.T) {
	ds := problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\nextra = { model = \"n\" }\ntriage_extra = { stream = true }\n")
	require.Len(t, ds, 2)
	assert.Contains(t, ds[0].Message, `extra may not set "model"`)
	assert.Contains(t, ds[1].Message, `triage_extra may not set "stream"`)
}

func TestResolveBaseURL(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte(cloudflare))
	require.Empty(t, ds)
	p := cfg.Providers["cf"]
	env := map[string]string{"CLOUDFLARE_ACCOUNT_ID": "acct123"}
	get := func(k string) string { return env[k] }

	u, err := p.ResolveBaseURL("cf", get)
	require.NoError(t, err)
	assert.Equal(t, "https://api.cloudflare.com/client/v4/accounts/acct123/ai/v1", u)

	delete(env, "CLOUDFLARE_ACCOUNT_ID")
	_, err = p.ResolveBaseURL("cf", get)
	var ee *EnvError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, EnvError{Provider: "cf", Field: "base_url", Var: "CLOUDFLARE_ACCOUNT_ID"}, *ee)
	assert.Contains(t, err.Error(), "${CLOUDFLARE_ACCOUNT_ID}")
	assert.Contains(t, ee.Fix(), "export CLOUDFLARE_ACCOUNT_ID=")

	// Only ${VAR} expands; a bare $ stays.
	u, err = Provider{BaseURL: "http://x/$HOME/v1"}.ResolveBaseURL("p", get)
	require.NoError(t, err)
	assert.Equal(t, "http://x/$HOME/v1", u)
}

func TestKey(t *testing.T) {
	env := map[string]string{"SET": "secret-value"}
	get := func(k string) string { return env[k] }

	k, err := Provider{APIKeyEnv: "SET"}.Key("p", get)
	require.NoError(t, err)
	assert.Equal(t, "secret-value", k)

	k, err = Provider{}.Key("p", get)
	require.NoError(t, err)
	assert.Empty(t, k)

	_, err = Provider{APIKeyEnv: "UNSET"}.Key("p", get)
	var ee *EnvError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, "api_key_env", ee.Field)
	assert.Contains(t, err.Error(), "UNSET")
}

func TestConfigProvider(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte(valid))
	require.Empty(t, ds)
	name, p, err := cfg.Provider("")
	require.NoError(t, err)
	assert.Equal(t, "cf", name)
	assert.Equal(t, "@cf/zai-org/glm", p.Model)

	name, _, err = cfg.Provider("local")
	require.NoError(t, err)
	assert.Equal(t, "local", name)

	_, _, err = cfg.Provider("nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "defined: cf, local")

	_, _, err = Config{}.Provider("")
	assert.ErrorContains(t, err, "no default provider")
	_, _, err = Config{}.Provider("x")
	assert.ErrorContains(t, err, "defines no providers")
}

func TestParseMaxTokensParam(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte("default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\nmax_tokens_param = \"max_completion_tokens\"\n"))
	require.Empty(t, ds)
	assert.Equal(t, "max_completion_tokens", cfg.Providers["a"].MaxTokensParam)
	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\nmax_tokens_param = \"tokens\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "max_tokens_param")
}

func TestParseTimeout(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte("default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\ntimeout = 240\n"))
	require.Empty(t, ds)
	assert.Equal(t, 240, cfg.Providers["a"].Timeout)
	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\ntimeout = -1\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "timeout must be a positive number of seconds")
}

// TestExplainConfigExamples parses every provider example in `spec explain
// config`, so the documented keys cannot drift from the parser.
func TestExplainConfigExamples(t *testing.T) {
	text, ok := explain.Topic("config")
	require.True(t, ok)
	_, after, found := strings.Cut(text, "## User config")
	require.True(t, found)
	var blocks []string
	var cur []string
	inFence := false
	for _, line := range strings.Split(after, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(line, "    ") {
			cur = append(cur, strings.TrimPrefix(line, "    "))
			continue
		}
		if len(cur) > 0 && strings.TrimSpace(line) != "" {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	if len(cur) > 0 {
		blocks = append(blocks, strings.Join(cur, "\n"))
	}
	// A block that starts with "$ " is a command line, and one that starts
	// with "[[" a canned replies file, not config.
	blocks = slices.DeleteFunc(blocks, func(b string) bool { return strings.HasPrefix(b, "$ ") || strings.HasPrefix(b, "[[") })
	all := strings.Join(blocks, "\n")
	require.GreaterOrEqual(t, strings.Count(all, "[providers."), 8)
	cfg, ds := Parse("config.md", []byte(all+"\n"))
	assert.Empty(t, ds)
	assert.Equal(t, "cf", cfg.Default)
	for _, name := range []string{"cf", "deepseek", "openrouter", "openai", "gemini", "anthropic", "local", "claude"} {
		assert.Contains(t, cfg.Providers, name)
	}
	assert.Equal(t, KindClaudeCLI, cfg.Providers["claude"].Kind)
	assert.Equal(t, "haiku", cfg.Providers["claude"].TriageModel)
	assert.Equal(t, map[string]any{"reasoning_effort": "low"}, cfg.Providers["cf"].TriageExtra)
}

func TestBudgetsAndPrompts(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte("max_context_tokens = 8000\nmax_output_tokens = 4000\n\n[prompts]\nchange = \"~/p/change.md\"\nanalyze = \"analyze.md\"\n"))
	require.Empty(t, ds)
	assert.Equal(t, 8000, cfg.ContextBudget())
	assert.Equal(t, 4000, cfg.OutputBudget())
	assert.Equal(t, "/home/u/p/change.md", PromptPath(cfg.Prompts.Change, "/cfg/spec/config.toml", "/home/u"))
	assert.Equal(t, "/cfg/spec/analyze.md", PromptPath(cfg.Prompts.Analyze, "/cfg/spec/config.toml", "/home/u"))
	assert.Equal(t, "/abs.md", PromptPath("/abs.md", "/cfg/spec/config.toml", "/home/u"))
	assert.Equal(t, "", PromptPath("", "/cfg/spec/config.toml", "/home/u"))

	var empty Config
	assert.Equal(t, DefaultMaxContextTokens, empty.ContextBudget())
	assert.Equal(t, DefaultMaxOutputTokens, empty.OutputBudget())

	_, ds = Parse("c.toml", []byte("max_context_tokens = -1\n"))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "max_context_tokens must be positive")
	_, ds = Parse("c.toml", []byte("[prompts]\nreview = \"x\"\n"))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, `unknown key "prompts.review"`)
}

func TestParseStreamAndIdleTimeout(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte("default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\n[providers.b]\nbase_url = \"http://x\"\nmodel = \"m\"\nstream = false\nidle_timeout = 30\n"))
	require.Empty(t, ds)
	assert.True(t, cfg.Providers["a"].Streams(), "streaming is the default")
	assert.Zero(t, cfg.Providers["a"].IdleTimeout)
	assert.False(t, cfg.Providers["b"].Streams())
	assert.Equal(t, 30, cfg.Providers["b"].IdleTimeout)

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\nidle_timeout = -5\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "idle_timeout must be a positive number of seconds")

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\nextra = { stream_options = { include_usage = false } }\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, `extra may not set "stream_options"`)

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\nstreaming = true\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "idle_timeout, stream;", "the known keys list the new ones")
	assert.Contains(t, ds[0].Message, "for kinds claude-cli and codex-cli also command, pass_env; kind decisions takes only model, base_url, api_key_env, timeout; kind canned takes path, model, triage_model, triage)")
}

func TestParseKinds(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte(`default = "claude"

[providers.claude]
kind = "claude-cli"
model = "sonnet"
triage_model = "haiku"
effort = "max"
timeout = 600
pass_env = ["AWS_PROFILE"]

[providers.codex]
kind = "codex-cli"
model = "gpt-6-luna"
effort = "none"

[providers.cf]
base_url = "http://x"
model = "m"
effort = "low"
`))
	require.Empty(t, ds)
	c := cfg.Providers["claude"]
	assert.Equal(t, KindClaudeCLI, c.KindOrDefault())
	assert.True(t, c.IsCLI())
	assert.Equal(t, "claude", c.CommandOrDefault())
	assert.Equal(t, []string{"AWS_PROFILE"}, c.PassEnv)
	x := cfg.Providers["codex"]
	assert.Equal(t, KindCodexCLI, x.KindOrDefault())
	assert.True(t, x.IsCLI())
	assert.Equal(t, "codex", x.CommandOrDefault())
	x.Command = "/opt/codex"
	assert.Equal(t, "/opt/codex", x.CommandOrDefault())
	assert.Equal(t, KindOpenAI, cfg.Providers["cf"].KindOrDefault())
	assert.False(t, cfg.Providers["cf"].IsCLI())
	assert.Equal(t, "low", cfg.Providers["cf"].Effort)

	ds = problems(t, "default = \"a\"\n[providers.a]\nkind = \"gemini-cli\"\nmodel = \"m\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, `kind "gemini-cli" must be "openai" (the default), "claude-cli", "codex-cli", "decisions" or "canned"`)

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\n[providers.j]\nkind = \"jev\"\nmodel = \"m\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, `kind "jev" must be`, "the kind's old name is no name")

	ds = problems(t, "default = \"a\"\n[providers.a]\nkind = \"claude-cli\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "model is required")

	ds = problems(t, "default = \"a\"\n[providers.a]\nkind = \"claude-cli\"\nmodel = \"haiku\"\nbase_url = \"http://x\"\napi_key_env = \"K\"\nstream = false\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "base_url, api_key_env, stream do not apply to kind claude-cli")

	ds = problems(t, "default = \"a\"\n[providers.a]\nkind = \"codex-cli\"\nmodel = \"gpt-6-luna\"\njson_mode = true\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "json_mode does not apply to kind codex-cli")

	ds = problems(t, "default = \"a\"\n[providers.a]\nkind = \"claude-cli\"\nmodel = \"haiku\"\npass_env = [\"not a name\"]\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, `pass_env entry "not a name"`)

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\ncommand = \"claude\"\npass_env = []\n")
	require.Len(t, ds, 2)
	assert.Contains(t, ds[0].Message, "command applies only to kinds claude-cli and codex-cli")
	assert.Contains(t, ds[1].Message, "pass_env applies only to kinds claude-cli and codex-cli")
}

func TestParseEffort(t *testing.T) {
	ds := problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\neffort = \"max\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, `effort "max" is not a level of kind openai (none, minimal, low, medium, high, xhigh)`)

	ds = problems(t, "default = \"a\"\n[providers.a]\nkind = \"claude-cli\"\nmodel = \"m\"\neffort = \"none\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, `effort "none" is not a level of kind claude-cli (low, medium, high, xhigh, max)`)

	ds = problems(t, "default = \"a\"\n[providers.a]\nkind = \"codex-cli\"\nmodel = \"m\"\neffort = \"ultra\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, `effort "ultra" is not a level of kind codex-cli (none, low, medium, high, xhigh, max)`)

	assert.True(t, ValidEffort(KindClaudeCLI, "xhigh"))
	assert.True(t, ValidEffort(KindCodexCLI, "none"))
	assert.False(t, ValidEffort(KindCodexCLI, "minimal"))
	assert.False(t, ValidEffort("nope", "low"))
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, EffortLevels(KindClaudeCLI))
	assert.Nil(t, EffortLevels("nope"))
}

func TestUnderlineColor(t *testing.T) {
	for _, v := range []string{"auto", "on", "off"} {
		cfg, ds := Parse("c.toml", []byte("underline_color = \""+v+"\"\n"))
		assert.Empty(t, ds, v)
		assert.Equal(t, v, cfg.UnderlineColor)
	}
	_, ds := Parse("c.toml", []byte("underline_color = \"blink\"\n"))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, `must be "auto" (the default), "on" or "off"`)
}

func TestParseDecisions(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte(`default = "cf"

[providers.cf]
base_url = "http://x"
model = "m"

[providers.jev]
kind = "decisions"
api_key_env = "OPENROUTER_API_KEY"
model = "typesafe/jev-1.14"
timeout = 30
`))
	require.Empty(t, ds)
	j := cfg.Providers["jev"]
	assert.True(t, j.IsGate())
	assert.False(t, j.IsCLI())
	assert.False(t, cfg.Providers["cf"].IsGate())
	u, err := j.DecisionsBaseURL("jev", func(string) string { return "" })
	require.NoError(t, err)
	assert.Equal(t, DefaultDecisionsBaseURL, u)
	j.BaseURL = "https://${HOST}/api/alpha"
	u, err = j.DecisionsBaseURL("jev", func(string) string { return "h.test" })
	require.NoError(t, err)
	assert.Equal(t, "https://h.test/api/alpha", u)

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\n[providers.j]\nkind = \"decisions\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "model is required")

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\n[providers.j]\nkind = \"decisions\"\nmodel = \"m\"\neffort = \"low\"\nstream = false\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "effort, stream do not apply to kind decisions")

	ds = problems(t, "default = \"j\"\n[providers.j]\nkind = \"decisions\"\nmodel = \"m\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "names a gate")

	assert.Empty(t, CheckProvider("j", Provider{Kind: KindDecisions, Model: "m"}))
	assert.NotEmpty(t, CheckProvider("j", Provider{Kind: KindDecisions}))
}

func TestGateCheck(t *testing.T) {
	providers := map[string]Provider{
		"jev": {Kind: KindDecisions, Model: "typesafe/jev-1.14"},
		"cf":  {BaseURL: "http://x", Model: "m"},
	}
	g := Gate{Provider: "jev"}
	assert.Empty(t, g.Check(providers))
	assert.InDelta(t, DefaultEscalateClassAt, g.EscalateAt(), 1e-9)
	g.EscalateClassAt = 0.6
	assert.InDelta(t, 0.6, g.EscalateAt(), 1e-9)

	assert.Contains(t, Gate{}.Check(providers)[0], "provider is required")
	assert.Contains(t, Gate{Provider: "nope"}.Check(providers)[0], "names no")
	assert.Contains(t, Gate{Provider: "cf"}.Check(providers)[0], "needs kind decisions (or canned)")
	msgs := Gate{Provider: "jev", EscalateClassAt: 1.5, SkipMaterialBelow: -1}.Check(providers)
	require.Len(t, msgs, 2)
	assert.Contains(t, msgs[0], "escalate_class_at")
	assert.Contains(t, msgs[1], "skip_material_below")
}

func TestParseCascade(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte(`default = "deepseek"

[providers.deepseek]
base_url = "https://api.deepseek.com"
model = "deepseek-flash"

[providers.cf]
base_url = "http://x"
model = "m"
triage_model = "t"
triage = false

[providers.jev]
kind = "decisions"
model = "typesafe/jev-1.14"

[gate]
provider = "jev"
escalate_class_at = 0.5
skip_material_below = 0.3

[triage]
provider = "cf"

[prompts]
gate = "gate.md"
triage = "triage.md"
`))
	require.Empty(t, ds)
	require.NotNil(t, cfg.Gate)
	assert.Equal(t, "jev", cfg.Gate.Provider)
	assert.InDelta(t, 0.5, cfg.Gate.EscalateAt(), 1e-9)
	assert.InDelta(t, 0.3, cfg.Gate.SkipMaterialBelow, 1e-9)
	require.NotNil(t, cfg.Triage)
	assert.Equal(t, "cf", cfg.Triage.Provider)
	assert.False(t, cfg.Providers["cf"].TriageOn())
	assert.True(t, cfg.Providers["deepseek"].TriageOn(), "triage is on unless set to false")
	assert.Equal(t, "gate.md", cfg.Prompts.Gate)
	assert.Equal(t, "triage.md", cfg.Prompts.Triage)

	cfg, ds = Parse("c.toml", []byte("default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\n"))
	require.Empty(t, ds)
	assert.Nil(t, cfg.Gate, "no [gate]: no gate")
	assert.Nil(t, cfg.Triage)

	base := "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\n[providers.j]\nkind = \"decisions\"\nmodel = \"m\"\n"
	ds = problems(t, base+"[gate]\nprovider = \"a\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "needs kind decisions")
	assert.Equal(t, "[gate]", string([]byte(base + "[gate]\nprovider = \"a\"\n")[ds[0].Range.Start:ds[0].Range.End]))

	ds = problems(t, base+"[triage]\nprovider = \"j\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "triage: provider \"j\" is a gate")
	ds = problems(t, base+"[triage]\nprovider = \"nope\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "names no [providers.nope] table")
	ds = problems(t, base+"[triage]\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "triage: provider is required")

	ds = problems(t, base+"[triage]\nprovider = \"a\"\nmodel = \"x\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "[triage] with provider")

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\n[providers.j]\nkind = \"decisions\"\nmodel = \"m\"\ntriage = false\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "triage does not apply to kind decisions")
}

func TestParseCanned(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte(`default = "scenes"

[providers.scenes]
kind = "canned"
path = "/tmp/replies.toml"
triage_model = "canned-triage"

[gate]
provider = "scenes"

[triage]
provider = "scenes"
`))
	require.Empty(t, ds)
	p := cfg.Providers["scenes"]
	assert.False(t, p.IsGate(), "a chat provider, which may be default")
	assert.False(t, p.IsCLI())
	assert.Equal(t, "/tmp/replies.toml", p.Path)

	ds = problems(t, "default = \"c\"\n[providers.c]\nkind = \"canned\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "path is required")

	ds = problems(t, "default = \"c\"\n[providers.c]\nkind = \"canned\"\npath = \"r.toml\"\nbase_url = \"http://x\"\neffort = \"low\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "effort, base_url do not apply to kind canned")

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\npath = \"r.toml\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "path applies only to kind canned")

	ds = problems(t, "default = \"a\"\n[providers.a]\nkind = \"claude-cli\"\nmodel = \"m\"\npath = \"r.toml\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "path does not apply to kind claude-cli")

	ds = problems(t, "default = \"a\"\n[providers.a]\nbase_url = \"http://x\"\nmodel = \"m\"\n[providers.j]\nkind = \"decisions\"\nmodel = \"m\"\npath = \"r.toml\"\n")
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "path does not apply to kind decisions")

	assert.Empty(t, Gate{Provider: "c"}.Check(map[string]Provider{"c": {Kind: KindCanned, Path: "r"}}))
}
