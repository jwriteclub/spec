// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package config loads and validates the user configuration at
// $XDG_CONFIG_HOME/spec/config.toml (else ~/.config/spec/config.toml): the
// inference providers. Parse validates the file; ResolveBaseURL and Key read
// the environment when a provider is used.
//
// A provider has a kind: "openai" (the default), any OpenAI-compatible
// chat-completions endpoint over HTTP; "claude-cli", the installed Claude
// Code CLI run in print mode on the user's own login; or "codex-cli", the
// installed Codex CLI run with `codex exec` on the user's own ChatGPT login;
// or "decisions", a gate that scores an edit on yes/no questions through
// the OpenRouter or Google Decisions API; or
// "canned", hand-written replies read from a file (UI scenes and tests).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/pos"
)

// Provider kinds.
const (
	// KindOpenAI is an OpenAI-compatible chat-completions endpoint over
	// HTTP (the default when kind is unset).
	KindOpenAI = "openai"
	// KindClaudeCLI runs the installed Claude Code CLI (`claude -p`) on the
	// user's own login, so a Claude subscription needs no API key.
	KindClaudeCLI = "claude-cli"
	// KindCodexCLI runs the installed Codex CLI (`codex exec`) on the
	// user's own login, so a ChatGPT plan that includes Codex needs no API
	// key.
	KindCodexCLI = "codex-cli"
	// KindDecisions scores yes/no questions through a Decisions API.
	// Open it with infer.OpenGate.
	KindDecisions = "decisions"
	// KindCanned answers from a file of hand-written replies, served in
	// order per request purpose (infer.Canned): for UI scenes, tests and
	// demos, never a model. It is a chat provider and a gate.
	KindCanned = "canned"
)

// DecisionsAPI selects the wire protocol of a decisions provider.
type DecisionsAPI string

const (
	DecisionsOpenRouter DecisionsAPI = "openrouter"
	DecisionsGoogle     DecisionsAPI = "google"

	DefaultDecisionsBaseURL = "https://openrouter.ai/api/alpha"
	GoogleDecisionsBaseURL  = "https://generativelanguage.googleapis.com/v1beta"
	GeminiDecisionModel     = "gemini-decision-1"
	GeminiKeyEnv            = "GEMINI_API_KEY"
)

// DefaultBaseURL is the API root when base_url is omitted. An unknown API
// has no root. An omitted API uses OpenRouter for existing configurations.
func (api DecisionsAPI) DefaultBaseURL() string {
	switch api {
	case "", DecisionsOpenRouter:
		return DefaultDecisionsBaseURL
	case DecisionsGoogle:
		return GoogleDecisionsBaseURL
	default:
		return ""
	}
}

// effortLevels are the effort values each kind accepts. For openai they are
// sent as reasoning_effort; for claude-cli as Claude Code's --effort; for
// codex-cli as Codex's model_reasoning_effort. Codex's "ultra" is left out:
// it hands work to sub-agents, which a single turn has no use for, and
// "minimal" is left out because the GPT-6 models reject it.
var effortLevels = map[string][]string{
	KindOpenAI:    {"none", "minimal", "low", "medium", "high", "xhigh"},
	KindClaudeCLI: {"low", "medium", "high", "xhigh", "max"},
	KindCodexCLI:  {"none", "low", "medium", "high", "xhigh", "max"},
}

// EffortLevels returns the effort values a provider kind accepts, in order
// from least to most; nil for an unknown kind.
func EffortLevels(kind string) []string {
	return append([]string(nil), effortLevels[kind]...)
}

// ValidEffort reports whether level is an effort value of kind.
func ValidEffort(kind, level string) bool {
	return slices.Contains(effortLevels[kind], level)
}

// Provider is one inference provider.
type Provider struct {
	// Kind is KindOpenAI ("" means it too), KindClaudeCLI, KindCodexCLI,
	// KindDecisions or KindCanned.
	Kind string `toml:"kind"`
	// API selects the Decisions API for a gate (default OpenRouter).
	API DecisionsAPI `toml:"api,omitempty"`
	// Path is a canned provider's replies file (kind canned only).
	Path string `toml:"path"`
	// Command is the CLI a CLI provider runs: a name looked up on PATH or a
	// path (default "claude" for claude-cli, "codex" for codex-cli).
	Command string `toml:"command"`
	// Effort is the reasoning effort (see EffortLevels); "" leaves the
	// provider's default. --effort overrides it.
	Effort string `toml:"effort"`
	// PassEnv names environment variables passed to a CLI provider beyond
	// the minimal set it always gets (HOME, PATH, locale, proxies).
	PassEnv []string `toml:"pass_env"`
	// BaseURL is the API root, for example https://openrouter.ai/api/v1.
	BaseURL string `toml:"base_url"`
	// Model is the model used for contradiction checks and analysis.
	Model string `toml:"model"`
	// TriageModel is an optional cheaper model for first-pass triage.
	TriageModel string `toml:"triage_model"`
	// APIKeyEnv names the environment variable holding the API key. The key
	// itself is never stored in the file. It may be empty for a local server
	// that needs no key.
	APIKeyEnv string `toml:"api_key_env"`
	// JSONMode says the provider accepts response_format {"type":
	// "json_object"}. Without it, prompts ask for JSON and the reply is
	// parsed leniently.
	JSONMode bool `toml:"json_mode"`
	// Extra is merged into every request body, over the standard fields
	// (for example reasoning_effort, or temperature for a model that only
	// accepts its default).
	Extra map[string]any `toml:"extra"`
	// TriageExtra is merged over Extra for triage requests only.
	TriageExtra map[string]any `toml:"triage_extra"`
	// Triage = false turns the per-edit cascade's triage stage off for
	// this provider (nil means on). Commit drafts still use triage_model.
	Triage *bool `toml:"triage"`
	// MaxTokensParam names the body field for the reply cap: "max_tokens"
	// (the default) or "max_completion_tokens" (OpenAI's reasoning models
	// reject max_tokens).
	MaxTokensParam string `toml:"max_tokens_param"`
	// Timeout bounds each attempt, in seconds (0: the client's default,
	// 900 when streaming and 120 without). A reasoning model answering a
	// long check can need more.
	Timeout int `toml:"timeout"`
	// IdleTimeout abandons a streamed attempt that receives nothing for
	// this many seconds (0: the client's default, 90).
	IdleTimeout int `toml:"idle_timeout"`
	// Stream asks for streamed replies; nil means true. false keeps one
	// JSON reply per request, for a provider that cannot stream.
	Stream *bool `toml:"stream"`
}

// KindOrDefault returns Kind, or KindOpenAI when it is unset.
func (p Provider) KindOrDefault() string {
	if p.Kind == "" {
		return KindOpenAI
	}
	return p.Kind
}

// IsCLI reports whether the provider runs a local CLI instead of calling
// an HTTP endpoint.
func (p Provider) IsCLI() bool {
	k := p.KindOrDefault()
	return k == KindClaudeCLI || k == KindCodexCLI
}

// IsGate reports whether the provider is a gate (kind decisions): it scores
// edits through infer.OpenGate and cannot answer chat requests.
func (p Provider) IsGate() bool {
	return p.KindOrDefault() == KindDecisions
}

// TriageOn reports whether the provider runs the cascade's triage stage
// (triage is not set to false).
func (p Provider) TriageOn() bool {
	return p.Triage == nil || *p.Triage
}

// Streams reports whether the provider's replies are streamed (the default).
func (p Provider) Streams() bool {
	return p.Stream == nil || *p.Stream
}

// Prompts names files that replace the embedded system prompts. A leading
// "~/" is the home directory; a relative path is relative to the config
// file's directory.
type Prompts struct {
	// Change replaces the prompt of the contradiction check behind `spec
	// change` and `spec propose`.
	Change string `toml:"change"`
	// Analyze replaces the prompt of `spec analyze`.
	Analyze string `toml:"analyze"`
	// Commit replaces the prompt that drafts a commit message in `spec
	// ui`'s commit view.
	Commit string `toml:"commit"`
	// Gate replaces the gate's policy and questions (the per-edit
	// cascade's first stage).
	Gate string `toml:"gate"`
	// Triage replaces the triage stage's policy, questions and reply
	// format.
	Triage string `toml:"triage"`
	// Spell replaces the spell check's policy, Jev question and triage
	// reply format.
	Spell string `toml:"spell"`
}

// Defaults for the inference budgets.
const (
	// DefaultMaxContextTokens is the context budget, estimated as bytes/4.
	DefaultMaxContextTokens = 60000
	// DefaultMaxOutputTokens caps a reply; reasoning models spend part of
	// it thinking.
	DefaultMaxOutputTokens = 16384
)

// Config is the parsed user configuration.
type Config struct {
	// Default names the provider used when none is chosen.
	Default   string              `toml:"default"`
	Providers map[string]Provider `toml:"providers"`
	// MaxContextTokens bounds the text sent with a check (0: the default).
	MaxContextTokens int `toml:"max_context_tokens"`
	// MaxOutputTokens caps each reply (0: the default).
	MaxOutputTokens int `toml:"max_output_tokens"`
	// Prompts overrides the embedded prompts.
	Prompts Prompts `toml:"prompts"`
	// UnderlineColor says whether `spec ui` may colour underlines (SGR
	// 58): "auto" (the default: not in Terminal.app, which draws them
	// blinking), "on" or "off".
	UnderlineColor string `toml:"underline_color"`
	// Appearance preferences leave terminal detection active when omitted or auto.
	ForegroundProfile string `toml:"foreground_profile"`
	ColorCapability   string `toml:"color_capability"`
	GlyphProfile      string `toml:"glyph_profile"`
	// ColorScheme selects an embedded scheme or custom:<filename stem>.
	// ColorSchemeDir is opt-in; relative paths resolve beside this user config.
	ColorScheme    string `toml:"color_scheme"`
	ColorSchemeDir string `toml:"color_scheme_dir"`
	// Gate is the per-edit cascade's gate; nil runs none.
	Gate *Gate `toml:"gate"`
	// Triage names the provider of the triage stage and the fast requests;
	// nil uses the check provider's triage_model.
	Triage *Triage `toml:"triage"`
	// EnvFile names a private file of NAME=VALUE lines that supplies the
	// providers' variables the environment lacks (see EnvFilePath and
	// ReadEnvFile). The config itself still holds names only.
	EnvFile string `toml:"env_file"`
}

// ContextBudget returns MaxContextTokens or its default.
func (c Config) ContextBudget() int {
	if c.MaxContextTokens > 0 {
		return c.MaxContextTokens
	}
	return DefaultMaxContextTokens
}

// OutputBudget returns MaxOutputTokens or its default.
func (c Config) OutputBudget() int {
	if c.MaxOutputTokens > 0 {
		return c.MaxOutputTokens
	}
	return DefaultMaxOutputTokens
}

// PromptPath resolves a [prompts] entry: "" stays "", "~/" is the home
// directory, and a relative path is taken relative to the directory of
// the config file at cfgPath.
func PromptPath(entry, cfgPath, home string) string {
	switch {
	case entry == "":
		return ""
	case entry == "~" || strings.HasPrefix(entry, "~/"):
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(entry, "~"), "/"))
	case filepath.IsAbs(entry):
		return entry
	}
	return filepath.Join(filepath.Dir(cfgPath), entry)
}

// Loaded is the result of Load.
type Loaded struct {
	// Path is the file that was (or would have been) read.
	Path string
	// Exists is false when the file is absent; Config is then empty and
	// there are no diagnostics.
	Exists bool
	Config Config
	// Source is the raw file contents (diagnostic ranges index them).
	Source []byte
	// Diagnostics are config-invalid errors with File set to Path.
	Diagnostics []diag.Diagnostic
}

// Path returns where the user config lives: $XDG_CONFIG_HOME/spec/config.toml
// when XDG_CONFIG_HOME is set and non-empty, else ~/.config/spec/config.toml
// (deliberately not os.UserConfigDir, which differs on macOS). It returns ""
// when neither is known.
func Path() string {
	return PathFor(os.Getenv("XDG_CONFIG_HOME"), homeDir())
}

// PathFor is Path with the environment passed in.
func PathFor(xdgConfigHome, home string) string {
	switch {
	case xdgConfigHome != "":
		return filepath.Join(xdgConfigHome, "spec", "config.toml")
	case home != "":
		return filepath.Join(home, ".config", "spec", "config.toml")
	}
	return ""
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// Load reads and validates the file at path (Path() when path is empty). A
// missing file is not an error: Loaded.Exists is false. Invalid content
// becomes config-invalid diagnostics; an I/O error other than "not found" is
// returned.
func Load(path string) (*Loaded, error) {
	if path == "" {
		path = Path()
	}
	l := &Loaded{Path: path}
	if path == "" {
		return l, nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return l, nil
		}
		return nil, err
	}
	l.Exists = true
	l.Source = src
	l.Config, l.Diagnostics = Parse(path, src)
	return l, nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var customColorScheme = regexp.MustCompile(`^custom:[a-z0-9][a-z0-9_-]{0,63}$`)

// Parse parses and validates config text. file labels the diagnostics.
func Parse(file string, src []byte) (Config, []diag.Diagnostic) {
	var cfg Config
	var ds []diag.Diagnostic
	index := pos.NewIndex(src)
	bad := func(r pos.Range, format string, args ...any) {
		ds = append(ds, diag.Diagnostic{
			Code: diag.CodeConfigInvalid, Severity: diag.Error, File: file,
			Range: r, Message: fmt.Sprintf(format, args...),
		})
	}
	at := func(row, col, length int) pos.Range {
		off, ok := index.Offset(pos.Position{Line: row, Col: col})
		if !ok {
			return pos.Range{}
		}
		return pos.Range{Start: off, End: min(off+length, len(src))}
	}

	dec := toml.NewDecoder(bytes.NewReader(src)).DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		var strict *toml.StrictMissingError
		var derr *toml.DecodeError
		switch {
		case errors.As(err, &strict):
			for _, e := range strict.Errors {
				row, col := e.Position()
				key := e.Key()
				length := 0
				if len(key) > 0 {
					length = len(key[len(key)-1])
				}
				bad(at(row, col, length), "unknown key %q (known: default, env_file, max_context_tokens, max_output_tokens, underline_color, foreground_profile, color_capability, glyph_profile, color_scheme, color_scheme_dir, [prompts] with change, analyze, commit, gate and triage, [gate] with provider, escalate_class_at and skip_material_below, [triage] with provider, [providers.<name>] with kind, model, triage_model, triage, effort, timeout; for kind openai also base_url, api_key_env, json_mode, extra, triage_extra, max_tokens_param, idle_timeout, stream; for kinds claude-cli and codex-cli also command, pass_env; kind decisions takes only api, model, base_url, api_key_env, timeout; kind canned takes path, model, triage_model, triage)", strings.Join(key, "."))
			}
		case errors.As(err, &derr):
			row, col := derr.Position()
			msg := strings.TrimPrefix(derr.Error(), "toml: ")
			if k := strings.Join(derr.Key(), "."); k != "" {
				msg = k + ": " + msg
			}
			bad(at(row, col, 0), "%s", msg)
			return Config{}, ds
		default:
			bad(pos.Range{}, "%v", err)
			return Config{}, ds
		}
	}

	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := cfg.Providers[n]
		r := findProvider(src, n)
		for _, msg := range checkProvider(n, p) {
			bad(r, "%s", msg)
		}
	}
	if cfg.MaxContextTokens < 0 {
		bad(findKey(src, "max_context_tokens"), "max_context_tokens must be positive (default %d)", DefaultMaxContextTokens)
	}
	if cfg.MaxOutputTokens < 0 {
		bad(findKey(src, "max_output_tokens"), "max_output_tokens must be positive (default %d)", DefaultMaxOutputTokens)
	}
	if cfg.Gate != nil {
		for _, msg := range cfg.Gate.Check(cfg.Providers) {
			bad(findTable(src, "gate"), "%s", msg)
		}
	}
	if cfg.Triage != nil {
		for _, msg := range cfg.Triage.Check(cfg.Providers) {
			bad(findTable(src, "triage"), "%s", msg)
		}
	}
	if cfg.ColorScheme != "" && cfg.ColorScheme != "synthwave" && cfg.ColorScheme != "synthwave-quiet" && !customColorScheme.MatchString(cfg.ColorScheme) {
		bad(findKey(src, "color_scheme"), "color_scheme must be synthwave, synthwave-quiet or custom:<filename stem>")
	}
	if customColorScheme.MatchString(cfg.ColorScheme) && cfg.ColorSchemeDir == "" {
		bad(findKey(src, "color_scheme"), "custom color_scheme requires an explicit color_scheme_dir in User config")
	}
	switch {
	case cfg.EnvFile != "" && strings.TrimSpace(cfg.EnvFile) == "":
		bad(findKey(src, "env_file"), "env_file is blank; name the file of NAME=VALUE lines, or remove the key")
	case strings.ContainsRune(cfg.EnvFile, 0):
		bad(findKey(src, "env_file"), "env_file must not contain NUL")
	}
	if strings.ContainsRune(cfg.ColorSchemeDir, 0) {
		bad(findKey(src, "color_scheme_dir"), "color_scheme_dir must not contain NUL")
	}
	for _, pref := range []struct {
		key, value string
		allowed    []string
	}{
		{"foreground_profile", cfg.ForegroundProfile, []string{"", "auto", "dark", "light"}},
		{"color_capability", cfg.ColorCapability, []string{"", "auto", "truecolor", "256"}},
		{"glyph_profile", cfg.GlyphProfile, []string{"", "auto", "unicode", "ascii"}},
	} {
		if !slices.Contains(pref.allowed, pref.value) {
			bad(findKey(src, pref.key), "%s = %q must be %s", pref.key, pref.value, strings.Join(pref.allowed[1:], ", "))
		}
	}
	switch cfg.UnderlineColor {
	case "", "auto", "on", "off":
	default:
		bad(findKey(src, "underline_color"), "underline_color = %q must be \"auto\" (the default), \"on\" or \"off\"", cfg.UnderlineColor)
	}
	switch {
	case cfg.Default == "" && len(names) > 0:
		for _, name := range names {
			if !cfg.Providers[name].IsGate() {
				bad(pos.Range{}, "default is not set; add default = %q (or another provider name)", name)
				break
			}
		}
	case cfg.Default != "" && len(names) == 0:
		bad(findKey(src, "default"), "default = %q names a provider, but no [providers.%s] table exists", cfg.Default, cfg.Default)
	case cfg.Default != "":
		if p, ok := cfg.Providers[cfg.Default]; !ok {
			bad(findKey(src, "default"), "default = %q names no provider (defined: %s)", cfg.Default, strings.Join(names, ", "))
		} else if p.IsGate() {
			bad(findKey(src, "default"), "default = %q names a gate (kind %s), which cannot run checks; name a chat provider", cfg.Default, KindDecisions)
		}
	}
	return cfg, ds
}

// CheckProvider returns what is wrong with the provider called n, one
// message per problem (none when it is valid). Parse reports the same
// messages as diagnostics; other files that hold [providers.<name>] tables
// in the user config's schema (the eval runner's) validate them with it.
func CheckProvider(n string, p Provider) []string {
	return checkProvider(n, p)
}

// checkProvider returns what is wrong with the provider called n.
func checkProvider(n string, p Provider) []string {
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, fmt.Sprintf("provider %q: ", n)+fmt.Sprintf(format, args...))
	}
	kind := p.KindOrDefault()
	if kind == KindDecisions {
		return checkDecisions(n, p)
	}
	if p.API != "" {
		bad("api only applies to kind %s", KindDecisions)
	}
	if kind == KindCanned {
		return append(msgs, checkCanned(n, p)...)
	}
	if _, ok := effortLevels[kind]; !ok {
		bad("kind %q must be %q (the default), %q, %q, %q or %q", p.Kind, KindOpenAI, KindClaudeCLI, KindCodexCLI, KindDecisions, KindCanned)
		return msgs
	}
	if kind == KindOpenAI {
		msgs = append(msgs, checkHTTP(n, p)...)
	} else {
		msgs = append(msgs, checkCLI(n, p)...)
	}
	if strings.TrimSpace(p.Model) == "" {
		bad("model is required")
	}
	if p.Effort != "" && !ValidEffort(kind, p.Effort) {
		bad("effort %q is not a level of kind %s (%s)", p.Effort, kind, strings.Join(effortLevels[kind], ", "))
	}
	if p.Timeout < 0 {
		bad("timeout must be a positive number of seconds")
	}
	return msgs
}

// checkHTTP checks the keys of an openai provider. Its base_url problem
// comes first, as before kinds existed.
func checkHTTP(n string, p Provider) []string {
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, fmt.Sprintf("provider %q: ", n)+fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		bad("base_url is required")
	} else if _, err := envRefs(p.BaseURL); err != nil {
		bad("base_url %q: %v", p.BaseURL, err)
	} else {
		// Validate the shape with every ${VAR} stood in by a plain
		// word: the variables are read only when the provider is used.
		shape := envRef.ReplaceAllString(p.BaseURL, "x")
		if u, err := url.Parse(shape); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad("base_url %q must be an http or https URL", p.BaseURL)
		}
	}
	if p.APIKeyEnv != "" && !envName.MatchString(p.APIKeyEnv) {
		bad("api_key_env %q is not an environment variable name; put the variable's name there, never the key itself", p.APIKeyEnv)
	}
	switch p.MaxTokensParam {
	case "", "max_tokens", "max_completion_tokens":
	default:
		bad("max_tokens_param %q must be \"max_tokens\" or \"max_completion_tokens\"", p.MaxTokensParam)
	}
	if p.IdleTimeout < 0 {
		bad("idle_timeout must be a positive number of seconds")
	}
	for _, t := range []struct {
		name  string
		table map[string]any
	}{{"extra", p.Extra}, {"triage_extra", p.TriageExtra}} {
		for _, k := range reservedExtra {
			if _, ok := t.table[k]; ok {
				bad("%s may not set %q (spec sets it on every request)", t.name, k)
			}
		}
	}
	if p.Command != "" {
		bad("command applies only to kinds %s and %s", KindClaudeCLI, KindCodexCLI)
	}
	if p.Path != "" {
		bad("path applies only to kind %s", KindCanned)
	}
	if p.PassEnv != nil {
		bad("pass_env applies only to kinds %s and %s", KindClaudeCLI, KindCodexCLI)
	}
	return msgs
}

// checkCLI checks the keys of a CLI provider: the HTTP keys do not apply.
func checkCLI(n string, p Provider) []string {
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, fmt.Sprintf("provider %q: ", n)+fmt.Sprintf(format, args...))
	}
	var unused []string
	for _, k := range []struct {
		key string
		set bool
	}{
		{"base_url", p.BaseURL != ""},
		{"api_key_env", p.APIKeyEnv != ""},
		{"json_mode", p.JSONMode},
		{"extra", p.Extra != nil},
		{"triage_extra", p.TriageExtra != nil},
		{"max_tokens_param", p.MaxTokensParam != ""},
		{"idle_timeout", p.IdleTimeout != 0},
		{"stream", p.Stream != nil},
		{"path", p.Path != ""},
	} {
		if k.set {
			unused = append(unused, k.key)
		}
	}
	if len(unused) > 0 {
		bad("%s %s not apply to kind %s (it runs the CLI on your own login; `spec explain config` lists its keys)",
			strings.Join(unused, ", "), map[bool]string{true: "does", false: "do"}[len(unused) == 1], p.Kind)
	}
	if p.Command != "" && strings.TrimSpace(p.Command) == "" {
		bad("command is blank")
	}
	for _, v := range p.PassEnv {
		if !envName.MatchString(v) {
			bad("pass_env entry %q is not an environment variable name", v)
		}
	}
	return msgs
}

// checkDecisions validates the protocol and settings of a gate provider.
func checkDecisions(n string, p Provider) []string {
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, fmt.Sprintf("provider %q: ", n)+fmt.Sprintf(format, args...))
	}
	if p.API.DefaultBaseURL() == "" {
		bad("api %q must be %q or %q", p.API, DecisionsOpenRouter, DecisionsGoogle)
	}
	if strings.TrimSpace(p.Model) == "" {
		bad("model is required (for example \"typesafe/jev-1.14\")")
	}
	if p.BaseURL != "" {
		if _, err := envRefs(p.BaseURL); err != nil {
			bad("base_url %q: %v", p.BaseURL, err)
		} else if u, err := url.Parse(envRef.ReplaceAllString(p.BaseURL, "x")); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad("base_url %q must be an http or https URL", p.BaseURL)
		}
	}
	if p.APIKeyEnv != "" && !envName.MatchString(p.APIKeyEnv) {
		bad("api_key_env %q is not an environment variable name; put the variable's name there, never the key itself", p.APIKeyEnv)
	}
	if p.Timeout < 0 {
		bad("timeout must be a positive number of seconds")
	}
	var unused []string
	for _, k := range []struct {
		key string
		set bool
	}{
		{"command", p.Command != ""},
		{"effort", p.Effort != ""},
		{"pass_env", p.PassEnv != nil},
		{"triage_model", p.TriageModel != ""},
		{"triage", p.Triage != nil},
		{"json_mode", p.JSONMode},
		{"extra", p.Extra != nil},
		{"triage_extra", p.TriageExtra != nil},
		{"max_tokens_param", p.MaxTokensParam != ""},
		{"idle_timeout", p.IdleTimeout != 0},
		{"stream", p.Stream != nil},
		{"path", p.Path != ""},
	} {
		if k.set {
			unused = append(unused, k.key)
		}
	}
	if len(unused) > 0 {
		bad("%s %s not apply to kind %s (a gate takes api, model, base_url, api_key_env and timeout)",
			strings.Join(unused, ", "), map[bool]string{true: "does", false: "do"}[len(unused) == 1], KindDecisions)
	}
	return msgs
}

// checkCanned checks a canned provider: path is required; model,
// triage_model and triage apply; nothing else does.
func checkCanned(n string, p Provider) []string {
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, fmt.Sprintf("provider %q: ", n)+fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(p.Path) == "" {
		bad("path is required (the file of canned replies)")
	}
	var unused []string
	for _, k := range []struct {
		key string
		set bool
	}{
		{"command", p.Command != ""},
		{"effort", p.Effort != ""},
		{"pass_env", p.PassEnv != nil},
		{"base_url", p.BaseURL != ""},
		{"api_key_env", p.APIKeyEnv != ""},
		{"json_mode", p.JSONMode},
		{"extra", p.Extra != nil},
		{"triage_extra", p.TriageExtra != nil},
		{"max_tokens_param", p.MaxTokensParam != ""},
		{"timeout", p.Timeout != 0},
		{"idle_timeout", p.IdleTimeout != 0},
		{"stream", p.Stream != nil},
	} {
		if k.set {
			unused = append(unused, k.key)
		}
	}
	if len(unused) > 0 {
		bad("%s %s not apply to kind %s (it takes path, model, triage_model and triage)",
			strings.Join(unused, ", "), map[bool]string{true: "does", false: "do"}[len(unused) == 1], KindCanned)
	}
	return msgs
}

// DecisionsBaseURL returns the API root of a decisions provider: BaseURL with its
// ${VAR} references resolved, or the default for the selected API.
func (p Provider) DecisionsBaseURL(name string, getenv func(string) string) (string, error) {
	root := p.API.DefaultBaseURL()
	if root == "" {
		return "", fmt.Errorf("provider %q: unknown decisions api %q", name, p.API)
	}
	if p.BaseURL == "" {
		return root, nil
	}
	return p.ResolveBaseURL(name, getenv)
}

// Gate configures the per-edit gate: the [gate] table. The gate scores an
// edit on four yes/no questions; the class questions are numeric_mismatch,
// contradiction and stale_reference, and material_change is the fourth.
type Gate struct {
	// Provider names a [providers.<name>] table of kind decisions (or canned).
	Provider string `toml:"provider"`
	// EscalateClassAt: an edit goes on to the next stage when any class
	// question scores at or above it (0: DefaultEscalateClassAt).
	EscalateClassAt float64 `toml:"escalate_class_at"`
	// SkipMaterialBelow also clears an edit whose material_change score is
	// below it, whatever the class questions say. 0 turns this off (the
	// default).
	SkipMaterialBelow float64 `toml:"skip_material_below"`
}

// DefaultEscalateClassAt is the class threshold of a gate that sets none:
// the highest that let every real problem through on both measured sets
// (notes/evals/2026-10-31-corpus-eval.md).
const DefaultEscalateClassAt = 0.45

// EscalateAt returns EscalateClassAt, or its default when it is 0.
func (g Gate) EscalateAt() float64 {
	if g.EscalateClassAt > 0 {
		return g.EscalateClassAt
	}
	return DefaultEscalateClassAt
}

// Check returns what is wrong with the gate, given the providers it may
// name.
func (g Gate) Check(providers map[string]Provider) []string {
	var msgs []string
	switch p, ok := providers[g.Provider]; {
	case g.Provider == "":
		msgs = append(msgs, "gate: provider is required (a [providers.<name>] table of kind decisions)")
	case !ok:
		msgs = append(msgs, fmt.Sprintf("gate: provider %q names no [providers.%s] table", g.Provider, g.Provider))
	case !p.IsGate() && p.KindOrDefault() != KindCanned:
		msgs = append(msgs, fmt.Sprintf("gate: provider %q is kind %s; a gate needs kind %s (or %s)", g.Provider, p.KindOrDefault(), KindDecisions, KindCanned))
	}
	if g.EscalateClassAt < 0 || g.EscalateClassAt > 1 {
		msgs = append(msgs, fmt.Sprintf("gate: escalate_class_at %v must be between 0 and 1", g.EscalateClassAt))
	}
	if g.SkipMaterialBelow < 0 || g.SkipMaterialBelow > 1 {
		msgs = append(msgs, fmt.Sprintf("gate: skip_material_below %v must be between 0 and 1 (0 is off)", g.SkipMaterialBelow))
	}
	return msgs
}

// Triage configures the cascade's triage stage: the [triage] table.
type Triage struct {
	// Provider names the chat provider that answers triage and the fast
	// requests (commit message drafts, the runaway guard's retry), with
	// its triage_model (else its model) and its triage_extra.
	Provider string `toml:"provider"`
}

// Check returns what is wrong with the triage table, given the providers
// it may name.
func (t Triage) Check(providers map[string]Provider) []string {
	switch p, ok := providers[t.Provider]; {
	case t.Provider == "":
		return []string{"triage: provider is required (a chat provider's [providers.<name>] table)"}
	case !ok:
		return []string{fmt.Sprintf("triage: provider %q names no [providers.%s] table", t.Provider, t.Provider)}
	case p.IsGate():
		return []string{fmt.Sprintf("triage: provider %q is a gate (kind %s); triage needs a chat provider", t.Provider, KindDecisions)}
	}
	return nil
}

// findTable returns the range of a "[name]" table header in src, or the
// empty range.
func findTable(src []byte, name string) pos.Range {
	form := "[" + name + "]"
	for off := 0; off < len(src); {
		end := bytes.IndexByte(src[off:], '\n')
		line := src[off:]
		if end >= 0 {
			line = src[off : off+end]
		}
		trim := bytes.TrimLeft(line, " \t")
		if bytes.HasPrefix(trim, []byte(form)) {
			s := off + (len(line) - len(trim))
			return pos.Range{Start: s, End: s + len(form)}
		}
		if end < 0 {
			break
		}
		off += end + 1
	}
	return pos.Range{}
}

// reservedExtra are request fields that extra and triage_extra may not set:
// the client owns them.
var reservedExtra = []string{"messages", "model", "stream", "stream_options"}

// envRef matches a ${VAR} reference.
var envRef = regexp.MustCompile(`\$\{([^}]*)\}`)

// envRefs returns the variable names referenced as ${VAR} in s, in order,
// or an error for a malformed reference.
func envRefs(s string) ([]string, error) {
	var names []string
	for _, m := range envRef.FindAllStringSubmatch(s, -1) {
		if !envName.MatchString(m[1]) {
			return nil, fmt.Errorf("${%s} is not an environment variable reference", m[1])
		}
		names = append(names, m[1])
	}
	if strings.Contains(envRef.ReplaceAllString(s, ""), "${") {
		return nil, errors.New("unterminated ${")
	}
	return names, nil
}

// EnvError reports an environment variable a provider needs that is unset
// or empty. It carries the variable's name, never a value.
type EnvError struct {
	// Provider is the provider's name.
	Provider string
	// Field is the config key that needs the variable: base_url or
	// api_key_env.
	Field string
	// Var is the variable's name.
	Var string
}

func (e *EnvError) Error() string {
	if e.Field == "api_key_env" {
		return fmt.Sprintf("provider %q: the API key variable %s (api_key_env) is not set", e.Provider, e.Var)
	}
	return fmt.Sprintf("provider %q: %s uses ${%s}, which is not set", e.Provider, e.Field, e.Var)
}

// Fix says how to resolve the error.
func (e *EnvError) Fix() string {
	return "export " + e.Var + "=... in the shell that runs spec, or add " + e.Var + "=... to the user config's env_file (the config file names the variable; the value comes from the environment or env_file)"
}

// CommandOrDefault returns Command, or the kind's CLI when it sets none:
// "codex" for codex-cli, "claude" otherwise.
func (p Provider) CommandOrDefault() string {
	switch {
	case p.Command != "":
		return p.Command
	case p.KindOrDefault() == KindCodexCLI:
		return "codex"
	}
	return "claude"
}

// ResolveBaseURL returns BaseURL with each ${VAR} replaced from getenv. An
// unset or empty variable is an *EnvError naming the provider.
func (p Provider) ResolveBaseURL(name string, getenv func(string) string) (string, error) {
	if _, err := envRefs(p.BaseURL); err != nil {
		return "", fmt.Errorf("provider %q: base_url: %w", name, err)
	}
	var missing string
	out := envRef.ReplaceAllStringFunc(p.BaseURL, func(ref string) string {
		v := ref[2 : len(ref)-1]
		val := getenv(v)
		if val == "" && missing == "" {
			missing = v
		}
		return val
	})
	if missing != "" {
		return "", &EnvError{Provider: name, Field: "base_url", Var: missing}
	}
	return out, nil
}

// Key returns the API key from the variable named by api_key_env. A
// provider with no api_key_env needs no key and returns "". An unset or
// empty variable is an *EnvError.
func (p Provider) Key(name string, getenv func(string) string) (string, error) {
	key, env, ok := p.APIKey(getenv)
	switch {
	case env == "":
		return "", nil
	case !ok:
		return "", &EnvError{Provider: name, Field: "api_key_env", Var: env}
	}
	return key, nil
}

// Provider returns the provider called name, or the default provider when
// name is empty. The error says what is defined.
func (c Config) Provider(name string) (string, Provider, error) {
	if name == "" {
		if c.Default == "" {
			return "", Provider{}, errors.New("no default provider is set")
		}
		name = c.Default
	}
	p, ok := c.Providers[name]
	if !ok {
		names := make([]string, 0, len(c.Providers))
		for n := range c.Providers {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return "", Provider{}, fmt.Errorf("no provider %q: the user config defines no providers", name)
		}
		return "", Provider{}, fmt.Errorf("no provider %q (defined: %s)", name, strings.Join(names, ", "))
	}
	return name, p, nil
}

// findKey returns the range of a top-level key in src, or the empty range.
func findKey(src []byte, key string) pos.Range {
	for off := 0; off < len(src); {
		end := bytes.IndexByte(src[off:], '\n')
		line := src[off:]
		if end >= 0 {
			line = src[off : off+end]
		}
		trim := bytes.TrimLeft(line, " \t")
		if bytes.HasPrefix(trim, []byte(key)) {
			rest := bytes.TrimLeft(trim[len(key):], " \t")
			if bytes.HasPrefix(rest, []byte("=")) {
				s := off + (len(line) - len(trim))
				return pos.Range{Start: s, End: s + len(key)}
			}
		}
		if end < 0 {
			break
		}
		off += end + 1
	}
	return pos.Range{}
}

// findProvider returns the range of the "[providers.name" header.
func findProvider(src []byte, name string) pos.Range {
	for _, form := range []string{"[providers." + name, `[providers."` + name + `"`} {
		if i := bytes.Index(src, []byte(form)); i >= 0 {
			return pos.Range{Start: i, End: i + len(form)}
		}
	}
	return pos.Range{}
}

// DefaultProvider returns the provider named by Default.
func (c Config) DefaultProvider() (name string, p Provider, ok bool) {
	p, ok = c.Providers[c.Default]
	return c.Default, p, ok
}

// APIKey returns the value of the provider's api_key_env variable using
// getenv. ok is false when the provider names no variable or it is unset or
// empty; env is the variable's name ("" when none is configured).
func (p Provider) APIKey(getenv func(string) string) (key, env string, ok bool) {
	if p.APIKeyEnv == "" {
		return "", "", false
	}
	v := getenv(p.APIKeyEnv)
	return v, p.APIKeyEnv, v != ""
}
