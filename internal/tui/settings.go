// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/pelletier/go-toml/v2"
)

type settingsSection struct{ scope, title string }

var settingsSections = []settingsSection{{"User", "Connections"}, {"User", "Model routing"}, {"User", "Appearance"}, {"User", "Limits and prompts"}, {"Project", "Project files"}, {"Project", "Lint and formatting"}}

const (
	setupOpenRouter = iota
	setupClaude
	setupCodex
	setupCustom
	setupGemini
	setupContinue
)

var connectionChoices = []struct{ label, desc, name string }{
	setupOpenRouter: {"OpenRouter API", "One key, three models: a decision (gate), a triage and an inference (check) model, each from a short recommended list or any model id.", ""},
	setupClaude:     {"Claude account", "Installed Claude Code CLI; sign in with claude auth login in your terminal.", "claude"},
	setupCodex:      {"ChatGPT / Codex", "Installed Codex CLI; sign in with codex login in your terminal.", "codex"},
	setupCustom:     {"Custom API endpoint", "Any compatible HTTP(S) API, including local servers.", "custom"},
	setupGemini:     {"Google Gemini Decisions", "Use Gemini Decision 1 as the gate with a Google API key.", "gemini"},
	setupContinue:   {"Continue editing", "Setup is optional; local editing remains available.", ""},
}

type settingsField struct {
	path                            []string
	label, help, kind, defaultValue string
	choices                         []string
	choiceLabels                    []string
	action                          string
}

func (f settingsField) id() string { return strings.Join(f.path, "\x00") }

type settingsScreen struct {
	theme                                 *Theme
	schemes                               *colorSchemeCatalog
	schemeDirectory, home                 string
	probeFingerprint                      string
	probeFromRouting                      bool
	credentialGeneration                  map[string]uint64
	probeProgress                         chan settingsProbeOutcome
	probeCompleted                        []settingsProbeOutcome
	user, project                         *config.Document
	section, field, scroll, width, height int
	rail, selector                        bool
	choice                                int
	provider                              string
	text                                  map[string]string
	baseline                              map[string]string
	touched                               map[string]bool
	errors                                map[string]string
	undo, redo                            map[string][]string
	cursor                                int
	confirm, notice                       string
	closeAfter                            bool
	back, close                           func()
	setup                                 bool
	setupChoice                           int
	openrouter                            *openRouterSetup
	invocation                            string
	secrets                               map[string]string
	sessionKeys                           map[string]string
	probes                                []settingsProbePlan
	probe                                 *settingsProbePlan
	probeCancel                           context.CancelFunc
	probeResult                           string
	probePassed                           bool
	probeRunning                          bool
	probeStarted                          time.Time
}
type settingsProbePlan struct {
	name, role, key string
	provider        config.Provider
	getenv          func(string) string
}
type settingsProbeOutcome struct {
	name, role string
	result     infer.ProbeResult
	err        error
}
type settingsProbeResult struct {
	outcomes []settingsProbeOutcome
	screen   *settingsScreen
	result   infer.ProbeResult
	err      error
}
type settingsProbeProgressMsg struct {
	screen  *settingsScreen
	outcome settingsProbeOutcome
}
type settingsProbeTick struct{ screen *settingsScreen }

// settingsSavedMsg reports a save; test is the connection test's verdict
// on what was saved (testVerdict).
type settingsSavedMsg struct{ scope, test string }

func diagnosticsText(ds []diag.Diagnostic) []string {
	var ps []string
	for _, d := range ds {
		ps = append(ps, d.Message)
	}
	return ps
}
func userSettingsValidate(src []byte) []string {
	_, ds := config.Parse("User config", src)
	return diagnosticsText(ds)
}
func projectSettingsValidate(src []byte) []string {
	_, ds := project.ParseConfig(src)
	return diagnosticsText(ds)
}
func newSettingsScreen(a *App) *settingsScreen {
	// No environment was injected, so this App has no user config (see
	// userConfigPath): the draft has no file behind it and cannot be saved.
	getenv := func(string) string { return "" }
	if a.wf != nil && a.wf.Getenv != nil {
		getenv = a.wf.Getenv
	}
	userPath := config.PathFor(getenv("XDG_CONFIG_HOME"), getenv("HOME"))
	u := config.NewDocument("", nil, false, userSettingsValidate)
	if userPath != "" {
		u = config.OpenDocument(userPath, userSettingsValidate)
	}
	p := config.NewDocument(filepath.Join(a.ws.Root, project.ConfigFile), a.ws.Project.ConfigSource(), true, projectSettingsValidate)
	s := newSettingsWithDocuments(u, p)
	s.theme = a.theme
	s.home = getenv("HOME")
	if a.wf != nil {
		s.sessionKeys = a.wf.SessionKeySnapshot()
	}

	s.selectFirstProvider()
	if a.wf != nil {
		var overrides []string
		for _, x := range []struct{ name, value string }{{"provider", a.wf.Provider}, {"model", a.wf.Model}, {"effort", a.wf.Effort}} {
			if x.value != "" {
				overrides = append(overrides, x.name+"="+x.value)
			}
		}
		s.invocation = strings.Join(overrides, ", ")
	}
	if ps := u.Problems(); len(ps) > 0 {
		s.notice = "User config: " + ps[0] + a.theme.uiText(" · Save remains blocked until repaired")
	}
	return s
}
func newSettingsWithDocuments(u, p *config.Document) *settingsScreen {
	home, _ := os.UserHomeDir()
	return &settingsScreen{user: u, project: p, home: home, text: map[string]string{}, baseline: map[string]string{}, touched: map[string]bool{}, errors: map[string]string{}, undo: map[string][]string{}, redo: map[string][]string{}, sessionKeys: map[string]string{}, secrets: map[string]string{}, credentialGeneration: map[string]uint64{}}
}

func (f settingsField) choiceLabel(i int) string {
	if i < len(f.choiceLabels) {
		return f.choiceLabels[i]
	}
	return f.choices[i]
}

func (s *settingsScreen) schemeChoices() settingsField {
	dir, _ := s.user.Get("color_scheme_dir")
	directory, _ := dir.(string)
	if s.schemes == nil || directory != s.schemeDirectory {
		s.schemes = loadColorSchemes(directory, s.user.Path, s.home)
		s.schemeDirectory = directory
	}
	f := settingsField{path: []string{"color_scheme"}, label: "Colour scheme", kind: "select", defaultValue: s.theme.uiText("Synthwave · built-in"), help: "Applies after saving User settings. Custom files require an explicit folder.", choices: []string{"Use default"}, choiceLabels: []string{s.theme.uiText("Use default · Synthwave")}}
	for _, scheme := range s.schemes.schemes {
		f.choices = append(f.choices, scheme.id)
		label := scheme.name + s.theme.uiText(" · built-in")
		if strings.HasPrefix(scheme.id, "custom:") {
			label = scheme.name + s.theme.uiText(" · ") + scheme.id
		}
		f.choiceLabels = append(f.choiceLabels, label)
	}
	return f
}
func (s *settingsScreen) selectFirstProvider() {
	s.provider = ""
	names := s.providerNames()
	if len(names) > 0 {
		s.provider = names[0]
	}
}
func (s *settingsScreen) providerNames() []string {
	v, _ := s.user.Get("providers")
	m, _ := v.(map[string]any)
	var ns []string
	for n := range m {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return ns
}
func (s *settingsScreen) doc() *config.Document {
	if s.section >= 4 {
		return s.project
	}
	return s.user
}
func (s *settingsScreen) context(*App) Context { return CtxSettings }
func (s *settingsScreen) fields() []settingsField {
	f := func(key, label, kind, def, help string) settingsField {
		return settingsField{path: strings.Split(key, "."), label: label, kind: kind, defaultValue: def, help: help}
	}
	sel := func(key, label, def, help string, choices ...string) settingsField {
		x := f(key, label, "select", def, help)
		x.choices = append([]string{"Use default"}, choices...)
		return x
	}
	switch s.section {
	case 0:
		fields := []settingsField{{label: "Connection", kind: "provider", choices: s.providerNames(), help: "Choose an existing connection; its settings stay in User config."}, {label: "Add connection", kind: "action", action: "add", help: "OpenRouter API, Claude account, ChatGPT / Codex, a custom endpoint, or Google Gemini Decisions."}}
		if s.provider == "" {
			return fields
		}
		prefix := []string{"providers", s.provider}
		p := func(key, label, kind, def, help string) settingsField {
			x := f(key, label, kind, def, help)
			x.path = append(append([]string{}, prefix...), key)
			return x
		}
		kindv, _ := s.user.Get(append(prefix, "kind")...)
		kind, _ := kindv.(string)
		if kind == "" {
			kind = config.KindOpenAI
		}
		fields = append(fields, settingsField{path: []string{"$provider_name"}, label: "Connection name", kind: "name", help: "Renaming also stages updates to routes using this connection."}, p("kind", "Connection type", "readonly", kind, "Choose another setup path by adding a connection; existing advanced values remain intact."))
		fields = append(fields, p("model", "Model ID", "text", "", "Enter a model ID. No discovery or account request runs here."))
		if kind != config.KindDecisions {
			fields = append(fields, p("triage_model", "Triage model", "text", "Same as check model", "Optional faster model for triage and commit message drafts."))
		}
		if kind == config.KindOpenAI || kind == config.KindDecisions {
			endpoint := ""
			if kind == config.KindDecisions {
				v, _ := s.user.Get(append(prefix, "api")...)
				api, _ := v.(string)
				endpoint = config.DecisionsAPI(api).DefaultBaseURL()
				fields = append(fields, p("api", "Decisions API", "readonly", string(config.DecisionsOpenRouter), "The protocol used to ask gate questions."))
			}
			fields = append(fields, p("base_url", "API endpoint", "text", endpoint, "HTTP(S) API root; ${VARIABLE} references resolve only when used."), p("api_key_env", "API key environment variable", "text", "No key required", "Store a variable name only, never the key itself."), settingsField{label: "Key for this session", kind: "secret", help: s.theme.uiText("Session only · masked. Save activates it; never written to config.")})
		}
		if kind == config.KindClaudeCLI || kind == config.KindCodexCLI {
			fields = append(fields, p("command", "CLI command", "text", map[bool]string{true: "codex", false: "claude"}[kind == config.KindCodexCLI], "Uses the CLI's own login. Sign in in your terminal, then choose Test."), p("pass_env", "Additional environment variables", "list", "None", "One variable name per comma-separated entry; no values."))
		}
		if kind != config.KindDecisions && kind != config.KindCanned {
			x := p("effort", "Reasoning effort", "select", "Provider default", "Only supported effort levels for this provider kind.")
			x.choices = append([]string{"Use default"}, config.EffortLevels(kind)...)
			fields = append(fields, x)
		}
		if kind != config.KindCanned {
			fields = append(fields, p("timeout", "Timeout (seconds)", "number", "Provider default", "Zero or blank uses the provider default."))
		}
		if kind == config.KindOpenAI {
			for _, x := range []settingsField{p("stream", "Stream replies", "select", "On", "Blank uses the supported default."), p("json_mode", "JSON mode", "select", "Off", "Enable only when this endpoint accepts JSON response_format."), p("max_tokens_param", "Token limit parameter", "select", "max_tokens", "Name of the endpoint's reply-budget parameter."), p("idle_timeout", "Idle timeout (seconds)", "number", "90", "Maximum time without streamed data; blank uses default."), p("extra", "Advanced request options (TOML table)", "table", "{}", "Structured TOML inline table; reserved model/messages/stream fields are rejected."), p("triage_extra", "Triage request options (TOML table)", "table", "{}", "Merged over request options for triage only.")} {
				switch x.path[len(x.path)-1] {
				case "stream", "json_mode":
					x.choices = []string{"Use default", "On", "Off"}
				case "max_tokens_param":
					x.choices = []string{"Use default", "max_tokens", "max_completion_tokens"}
				}
				fields = append(fields, x)
			}
		}
		if kind == config.KindCanned {
			fields = append(fields, p("path", "Canned replies file", "text", "", "Fixture provider retained without changing its kind."))
		}
		fields = append(fields, settingsField{label: "Forget session key", kind: "action", action: "forget", help: "Clear only this connection's session credential."}, settingsField{label: "Remove connection", kind: "action", action: "remove", help: "Stages removal. Routes naming it must also be changed before Save."})
		return fields
	case 1:
		return []settingsField{f("default", "Default check provider", "text", "None", "Name a chat connection. Invocation --provider/--engine/--effort still take precedence."), f("triage.provider", "Triage provider", "text", "Check provider's triage model", "Optional chat provider for triage and fast requests."), f("gate.provider", "Gate provider", "text", "Gate disabled", "Optional gate-only decisions or canned provider."), f("gate.escalate_class_at", "Gate escalation threshold", "float", "0.45", "0 to 1; zero uses the built-in threshold."), f("gate.skip_material_below", "Gate material threshold", "float", "0 (off)", "0 to 1; zero disables the material skip.")}
	case 2:
		return []settingsField{s.schemeChoices(), sel("foreground_profile", "Foreground profile", s.theme.uiText("Auto · detected by terminal"), "Terminal background stays unchanged.", "auto", "dark", "light"), sel("color_capability", "Colour capability", s.theme.uiText("Auto · terminal detection"), "Foreground colour capability is independent of underline support.", "auto", "truecolor", "256"), sel("glyph_profile", "Symbols and keys", s.theme.uiText("Auto · terminal detection"), "ASCII uses text fallbacks independently of colours.", "auto", "unicode", "ascii"), sel("underline_color", "Coloured underlines", "Auto", "Terminal.app uses plain underlines and text-colour fallback.", "auto", "on", "off"), f("color_scheme_dir", "Custom colour scheme folder", "text", "Disabled", "Opt-in. Relative to User config; ~/ and absolute paths are supported.")}
	case 3:
		fs := []settingsField{f("max_context_tokens", "Context budget (tokens)", "number", "60000", "Approximate bytes / 4. Blank or zero uses built-in default."), f("max_output_tokens", "Reply budget (tokens)", "number", "16384", "CLI provider reply length is not capped by this setting.")}
		for _, p := range []string{"change", "analyze", "commit", "gate", "triage"} {
			fs = append(fs, f("prompts."+p, p+" prompt file", "text", "Built-in prompt", "Relative to User config directory, never the project."))
		}
		return fs
	case 4:
		return []settingsField{f("name", "Project name", "text", "Directory name", "Project-only setting; independent of User config."), f("files", "File patterns", "list", project.DefaultGlob, "Comma-separated relative forward-slash globs. Explicit patterns replace the default.")}
	case 5:
		fs := []settingsField{f("format.width", "Paragraph width (columns)", "number", "100", "Blank or zero uses the default width.")}
		for _, code := range lint.KnownCodes() {
			fs = append(fs, sel("lint."+code, code, "Rule default", "Project lint severity override.", "off", "hint", "warning", "error"))
		}
		return fs
	}
	return nil
}
func (s *settingsScreen) fieldID(f settingsField) string {
	if f.kind == "name" {
		return "User\x00provider-name\x00" + s.provider
	}
	return settingsSections[s.section].scope + "\x00" + f.id()
}
func valueText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "On"
		}
		return "Off"
	case []any:
		var vs []string
		for _, v := range x {
			vs = append(vs, fmt.Sprint(v))
		}
		return strings.Join(vs, ", ")
	case map[string]any:
		return inlineTOML(x)
	default:
		return fmt.Sprint(v)
	}
}
func inlineTOML(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case map[string]any:
		var pairs []string
		for k, v := range x {
			pairs = append(pairs, strconv.Quote(k)+" = "+inlineTOML(v))
		}
		sort.Strings(pairs)
		return "{ " + strings.Join(pairs, ", ") + " }"
	case []any:
		var vs []string
		for _, v := range x {
			vs = append(vs, inlineTOML(v))
		}
		return "[" + strings.Join(vs, ", ") + "]"
	default:
		return fmt.Sprint(v)
	}
}

func (s *settingsScreen) value(f settingsField) string {
	if f.kind == "name" {
		id := s.fieldID(f)
		if v, ok := s.text[id]; ok {
			return v
		}
		s.text[id] = s.provider
		s.baseline[id] = s.provider
		return s.provider
	}
	if f.kind == "provider" {
		return s.provider
	}
	if f.kind == "secret" {
		if s.secrets[s.provider] != "" || s.sessionKeys[s.provider] != "" {
			return "********"
		}
		return ""
	}
	if f.kind == "action" {
		return f.label
	}
	id := s.fieldID(f)
	if v, ok := s.text[id]; ok {
		return v
	}
	v, ok := s.doc().Get(f.path...)
	text := ""
	if ok {
		text = valueText(v)
	}
	s.text[id] = text
	s.baseline[id] = text
	return text
}
func (s *settingsScreen) stage(f settingsField) bool {
	if f.kind == "action" || f.kind == "provider" || f.kind == "secret" || f.kind == "readonly" {
		return true
	}
	id := s.fieldID(f)
	text := s.value(f)
	if f.kind == "name" {
		return s.renameProvider(text, id)
	}
	if text == s.baseline[id] && !s.touched[id] {
		return true
	}
	var value any = text
	var err error
	if text == "" || text == "Use default" {
		value = nil
	} else {
		switch f.kind {
		case "number":
			value, err = strconv.Atoi(text)
			if err == nil && value.(int) < 0 {
				err = fmt.Errorf("use zero, a positive number, or blank for default")
			}
		case "float":
			value, err = strconv.ParseFloat(text, 64)
			if err == nil {
				v := value.(float64)
				if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
					err = fmt.Errorf("enter a finite number from 0 to 1")
				}
			}
		case "list":
			var vs []string
			for _, v := range strings.Split(text, ",") {
				vs = append(vs, strings.TrimSpace(v))
			}
			value = vs
		case "table":
			var m map[string]any
			err = toml.Unmarshal([]byte("value = "+text), &m)
			if err == nil {
				var ok bool
				value, ok = m["value"].(map[string]any)
				if !ok {
					err = fmt.Errorf("enter a TOML inline table, such as { temperature = 0.2 }")
				}
			}
		case "select":
			if text == "On" {
				value = true
			} else if text == "Off" {
				value = false
			}
		}
	}
	if err != nil {
		s.errors[id] = "Error: " + err.Error()
		return false
	}
	delete(s.errors, id)
	s.doc().Set(value, f.path...)
	// Removing the final optional route removes its empty table too.
	if value == nil && len(f.path) == 2 && (f.path[0] == "triage" || f.path[0] == "gate") {
		if v, _ := s.doc().Get(f.path[0]); v != nil && len(v.(map[string]any)) == 0 {
			s.doc().Set(nil, f.path[0])
		}
	}
	return true
}
func (s *settingsScreen) stageAll() bool {
	if s.provider != "" {
		f := settingsField{kind: "name"}
		id := s.fieldID(f)
		if name, seen := s.text[id]; seen && !s.renameProvider(name, id) {
			return false
		}
	}
	old := s.section
	ok := true
	for sec := range settingsSections {
		s.section = sec
		for _, f := range s.fields() {
			id := s.fieldID(f)
			if _, seen := s.text[id]; seen && !s.stage(f) {
				ok = false
			}
		}
	}
	s.section = old
	return ok
}
func (s *settingsScreen) dirty() int {
	n := len(s.user.Changes()) + len(s.project.Changes())
	for id, txt := range s.text {
		if txt != s.baseline[id] {
			n++
		}
	}
	return n
}
func (s *settingsScreen) changeCount() int {
	s.stageAll()
	return len(s.user.Changes()) + len(s.project.Changes()) + len(s.errors)
}
func (s *settingsScreen) current() *settingsField {
	fs := s.fields()
	if len(fs) == 0 {
		return nil
	}
	s.field = min(max(s.field, 0), len(fs)-1)
	return &fs[s.field]
}
func (s *settingsScreen) moveField(delta int) {
	if f := s.current(); f != nil {
		s.stage(*f)
	}
	fs := s.fields()
	if len(fs) == 0 {
		return
	}
	s.field = (s.field + delta + len(fs)) % len(fs)
	s.cursor = len([]rune(s.value(fs[s.field])))
	s.scroll = max(s.field*4-3, 0)
}
func (s *settingsScreen) edit(f settingsField, value string) {
	id := s.fieldID(f)
	s.touched[id] = true
	s.undo[id] = append(s.undo[id], s.value(f))
	s.redo[id] = nil
	s.text[id] = value
	delete(s.errors, id)
}
func (s *settingsScreen) revert() {
	s.user.Revert()
	s.project.Revert()
	s.text = map[string]string{}
	s.baseline = map[string]string{}
	s.touched = map[string]bool{}
	s.errors = map[string]string{}
	for name := range s.secrets {
		s.bumpCredential(name)
	}
	s.confirm = ""
	s.secrets = map[string]string{}
	s.notice = "Settings changes discarded"
	s.selectFirstProvider()
}
func (s *settingsScreen) openSave(closeAfter bool) {
	if !s.stageAll() {
		s.notice = "Fields need attention; draft retained"
		return
	}
	s.closeAfter = closeAfter
	s.confirm = "save"
	s.notice = ""
}
func (s *settingsScreen) save(a *App, scope string) tea.Cmd {
	if scope == "User" && len(s.user.Changes()) == 0 && len(s.secrets) == 0 {
		return nil
	}
	if scope == "Project" && len(s.project.Changes()) == 0 {
		return nil
	}
	d := s.user
	if scope == "Project" {
		d = s.project
	}
	if ps := d.Problems(); len(ps) > 0 {
		s.notice = "Cannot save " + scope + ": " + strings.Join(ps, "; ")
		return nil
	}
	if scope == "User" {
		id, _ := s.user.Get("color_scheme")
		selected, _ := id.(string)
		s.schemes = nil
		s.schemeChoices()
		if _, err := s.schemes.find(selected); err != nil {
			s.notice = "Cannot save User: " + err.Error()
			return nil
		}
	}
	if err := d.Save(); err != nil {
		s.notice = "Could not save " + scope + a.theme.uiText(" · ") + err.Error() + a.theme.uiText(" · settings remain unsaved")
		return nil
	}
	if scope == "User" {
		for name, key := range s.secrets {
			if key != "" {
				s.sessionKeys[name] = key
				if a.wf != nil {
					a.wf.SetSessionKey(name, key)
				}
			}
		}
		s.secrets = map[string]string{}
	}

	s.text = map[string]string{}
	s.baseline = map[string]string{}
	s.touched = map[string]bool{}
	test := "not tested"
	if scope == "User" {
		test = s.testVerdict()
	}
	s.notice = scope + a.theme.uiText(" saved · "+test)
	if len(s.user.Changes())+len(s.project.Changes()) == 0 {
		s.confirm = ""
		if s.closeAfter && s.close != nil {
			s.close()
		}
	}
	return func() tea.Msg { return settingsSavedMsg{scope: scope, test: test} }
}
func (s *settingsScreen) key(a *App, msg tea.KeyPressMsg, _ Binding, _ bool) tea.Cmd {
	k := msg.String()
	if k == "ctrl+g" || k == "f1" {
		if s.back != nil {
			s.back()
		}
		return nil
	}
	if s.probeRunning {
		if k == "ctrl+c" {
			s.probeCancel()
			s.notice = a.theme.uiText("Cancelling test…")
		}
		return nil
	}
	if s.confirm != "" {
		return s.confirmKey(a, msg)
	}
	if s.openrouter != nil {
		return s.openRouterKey(msg)
	}
	if s.setup {
		return s.setupKey(msg)
	}
	if s.selector {
		f := s.current()
		if f == nil {
			return nil
		}
		switch k {
		case "esc":
			s.selector = false
		case "up":
			s.choice = max(s.choice-1, 0)
		case "down":
			s.choice = min(s.choice+1, len(f.choices)-1)
		case "enter":
			if len(f.choices) > 0 {
				v := f.choices[s.choice]
				if f.kind == "provider" {
					s.provider = v
				} else {
					s.edit(*f, v)
					s.stage(*f)
				}
			}
			s.selector = false
		}
		return nil
	}

	if s.rail {
		switch k {
		case "esc", "ctrl+q":
			s.rail = false
		case "up":
			s.section = max(s.section-1, 0)
			s.field = 0
			s.scroll = 0
		case "down":
			s.section = min(s.section+1, len(settingsSections)-1)
			s.field = 0
			s.scroll = 0
		case "enter":
			s.rail = false
		}
		return nil
	}
	switch k {
	case "esc", "ctrl+x":
		if s.changeCount() > 0 || len(s.secrets) > 0 {
			s.confirm = "close"
		} else if s.close != nil {
			s.close()
		}
		return nil
	case "ctrl+q":
		s.rail = true
		return nil
	case "ctrl+o":
		if s.changeCount() == 0 && len(s.secrets) == 0 {
			return nil
		}
		s.openSave(false)
		return nil
	case "ctrl+r":
		if s.dirty() > 0 {
			s.confirm = "revert"
		}
		return nil
	case "ctrl+t":
		if s.section != 0 && s.section != 1 {
			return nil
		}
		s.openTest(a)
		return nil
	case "tab", "enter":
		f := s.current()
		if k == "enter" && f != nil && (f.kind == "select" || f.kind == "provider" || f.kind == "action") {
			return s.activate(a, *f)
		}
		s.moveField(1)
		return nil
	case "shift+tab":
		s.moveField(-1)
		return nil
	case "up":
		s.scroll = max(s.scroll-1, 0)
		return nil
	case "down":
		s.scroll++
		return nil
	case "pgup":
		s.scroll = max(s.scroll-max(s.height-6, 1), 0)
		return nil
	case "pgdown", "ctrl+v":
		s.scroll += max(s.height-6, 1)
		return nil
	}
	f := s.current()
	if f == nil {
		return nil
	}
	if f.kind == "action" || f.kind == "provider" || f.kind == "select" || f.kind == "readonly" {
		if k == "space" {
			return s.activate(a, *f)
		}
		return nil
	}
	if f.kind == "secret" {
		switch k {
		case "backspace", "delete", "ctrl+u":
			delete(s.secrets, s.provider)
			s.bumpCredential(s.provider)
		default:
			if msg.Key().Text != "" && msg.Key().Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
				s.secrets[s.provider] += msg.Key().Text
				s.bumpCredential(s.provider)
			}
		}
		return nil
	}
	text := []rune(s.value(*f))
	s.cursor = min(max(s.cursor, 0), len(text))
	switch k {
	case "left":
		s.cursor = max(s.cursor-1, 0)
	case "right":
		s.cursor = min(s.cursor+1, len(text))
	case "home", "ctrl+a":
		s.cursor = 0
	case "end", "ctrl+e":
		s.cursor = len(text)
	case "backspace":
		if s.cursor > 0 {
			s.edit(*f, string(append(text[:s.cursor-1], text[s.cursor:]...)))
			s.cursor--
		}
	case "delete":
		if s.cursor < len(text) {
			s.edit(*f, string(append(text[:s.cursor], text[s.cursor+1:]...)))
		}
	case "ctrl+u":
		s.edit(*f, "")
		s.cursor = 0
	case "ctrl+z", "ctrl+y":
		id := s.fieldID(*f)
		from, to := s.undo, s.redo
		if k == "ctrl+y" {
			from, to = s.redo, s.undo
		}
		if stack := from[id]; len(stack) > 0 {
			to[id] = append(to[id], s.value(*f))
			s.text[id] = stack[len(stack)-1]
			from[id] = stack[:len(stack)-1]
			s.cursor = len([]rune(s.text[id]))
		}
	default:
		if t := msg.Key().Text; t != "" && msg.Key().Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
			r := []rune(t)
			s.edit(*f, string(append(append(append([]rune{}, text[:s.cursor]...), r...), text[s.cursor:]...)))
			s.cursor += len(r)
		}
	}
	return nil
}
func (s *settingsScreen) activate(a *App, f settingsField) tea.Cmd {
	switch f.kind {
	case "select", "provider":
		if len(f.path) == 1 && f.path[0] == "color_scheme" {
			s.schemes = nil // Refresh opt-in files whenever the picker is opened.
			f = s.schemeChoices()
			if len(s.schemes.problems) > 0 {
				s.notice = s.schemes.problems[0]
			}
		}
		s.selector = true
		s.choice = 0
		for i, choice := range f.choices {
			if choice == s.value(f) {
				s.choice = i
			}
		}
	case "action":
		switch f.action {
		case "add":
			s.setup = true
			s.setupChoice = setupOpenRouter
		case "forget":
			delete(s.sessionKeys, s.provider)
			s.bumpCredential(s.provider)
			if a.wf != nil {
				a.wf.SetSessionKey(s.provider, "")
			}
			delete(s.secrets, s.provider)
			s.notice = "Session key forgotten"
		case "remove":
			s.confirm = "remove"
		}
	}
	return nil
}
func (s *settingsScreen) confirmKey(a *App, m tea.KeyPressMsg) tea.Cmd {
	k := m.String()
	if k == "esc" {
		s.confirm = ""
		return nil
	}
	switch s.confirm {
	case "close":
		switch k {
		case "d":
			s.revert()
			if s.close != nil {
				s.close()
			}
		case "ctrl+o":
			s.openSave(true)
		}
	case "revert":
		if k == "d" {
			s.revert()
		}
	case "remove":
		if k == "d" {
			s.user.Set(nil, "providers", s.provider)
			s.provider = ""
			s.selectFirstProvider()
			s.text = map[string]string{}
			s.baseline = map[string]string{}
			s.touched = map[string]bool{}
			s.confirm = ""
			s.notice = "Connection removal staged; update routes before saving"
		}
	case "save":
		switch k {
		case "u":
			return s.save(a, "User")
		case "p":
			return s.save(a, "Project")
		case "ctrl+o":
			if len(s.user.Changes()) > 0 || len(s.secrets) > 0 {
				return s.save(a, "User")
			}
			if len(s.project.Changes()) > 0 {
				return s.save(a, "Project")
			}
			s.confirm = ""
		}
	case "result":
		if k == "ctrl+t" {
			s.confirm = "test"
		}
	case "test":
		if k == "ctrl+t" {
			return s.startTest()
		}
	}
	return nil
}
func (s *settingsScreen) setupKey(m tea.KeyPressMsg) tea.Cmd {
	switch m.String() {
	case "esc":
		s.setup = false
	case "up":
		s.setupChoice = max(s.setupChoice-1, 0)
	case "down":
		s.setupChoice = min(s.setupChoice+1, len(connectionChoices)-1)
	case "enter":
		s.setup = false
		switch s.setupChoice {
		case setupOpenRouter:
			s.openOpenRouterSetup()
		case setupContinue:
		default:
			s.addPreset(s.setupChoice)
		}
	}
	return nil
}

// addPreset stages a single connection selected in Add connection.
func (s *settingsScreen) addPreset(choice int) {
	p := map[string]any{}
	switch choice {
	case setupClaude:
		p["kind"] = config.KindClaudeCLI
	case setupCodex:
		p["kind"] = config.KindCodexCLI
	case setupCustom:
		p["kind"] = config.KindOpenAI
	case setupGemini:
		p["kind"] = config.KindDecisions
		p["api"] = string(config.DecisionsGoogle)
		p["model"] = config.GeminiDecisionModel
		p["api_key_env"] = config.GeminiKeyEnv
	default:
		return
	}
	name := s.freeProviderName(connectionChoices[choice].name)
	s.user.Set(p, "providers", name)
	if choice == setupGemini {
		s.stageGateRoute(name)
	} else if _, ok := s.user.Get("default"); !ok {
		s.user.Set(name, "default")
	}
	s.provider = name
	s.field = 4
	s.cursor = 0
	if choice == setupGemini {
		s.notice = "Gate staged: " + name + ". Review before Save."
	} else {
		s.notice = s.theme.uiText("Connection staged · enter its model, then Review before Save. No test has run.")
	}
	s.text = map[string]string{}
	s.baseline = map[string]string{}
	s.touched = map[string]bool{}
}

// stageGateRoute preserves thresholds on an existing gate.
func (s *settingsScreen) stageGateRoute(name string) {
	if _, ok := s.user.Get("gate"); ok {
		s.user.Set(name, "gate", "provider")
	} else {
		s.user.Set(map[string]any{"provider": name, "escalate_class_at": config.DefaultEscalateClassAt, "skip_material_below": 0.0}, "gate")
	}
}

func (s *settingsScreen) paste(text string) {
	if s.openrouter != nil && s.confirm == "" && !s.probeRunning {
		s.openRouterPaste(text)
		return
	}
	if s.confirm != "" || s.probeRunning || s.setup || s.rail || s.selector {
		return
	}
	f := s.current()
	if f == nil {
		return
	}
	if strings.ContainsAny(text, "\r\n") {
		s.notice = "Single-line field: paste must not contain a newline"
		return
	}
	if f.kind == "secret" {
		s.secrets[s.provider] += text
		s.bumpCredential(s.provider)
		return
	}
	if f.kind != "text" && f.kind != "name" && f.kind != "number" && f.kind != "float" && f.kind != "list" && f.kind != "table" {
		return
	}
	r := []rune(s.value(*f))
	s.cursor = min(max(s.cursor, 0), len(r))
	ins := []rune(text)
	s.edit(*f, string(append(append(append([]rune{}, r[:s.cursor]...), ins...), r[s.cursor:]...)))
	s.cursor += len(ins)
}
func (s *settingsScreen) openTest(a *App) {
	if s.section != 0 && s.section != 1 {
		s.notice = "Test is available in Connections and Model routing"
		return
	}
	if !s.stageAll() {
		s.notice = "Correct the invalid fields before testing"
		return
	}
	b, err := s.user.Bytes()
	if err != nil {
		s.notice = "Could not snapshot connection"
		return
	}
	cfg, ds := config.Parse("User draft", b)
	if len(ds) > 0 {
		s.notice = "Cannot test: " + ds[0].Message
		return
	}
	type route struct{ name, role string }
	var plans []route
	if s.section == 1 {
		if cfg.Default != "" {
			plans = append(plans, route{cfg.Default, "check"})
		}
		if cfg.Triage != nil {
			plans = append(plans, route{cfg.Triage.Provider, "triage"})
		} else if cfg.Default != "" && cfg.Providers[cfg.Default].TriageOn() {
			plans = append(plans, route{cfg.Default, "triage"})
		}
		if cfg.Gate != nil {
			plans = append(plans, route{cfg.Gate.Provider, "gate"})
		}
	} else if p, ok := cfg.Providers[s.provider]; ok {
		role := "check"
		if p.IsGate() {
			role = "gate"
		}
		plans = append(plans, route{s.provider, role})
	}
	if len(plans) == 0 {
		s.notice = "Choose a connection before testing"
		return
	}
	getenv := os.Getenv
	if a.wf != nil && a.wf.Getenv != nil {
		getenv = a.wf.Getenv
	}
	var probes []settingsProbePlan
	for _, plan := range plans {
		key := s.sessionKeys[plan.name]
		if staged := s.secrets[plan.name]; staged != "" {
			key = staged
		}
		provider := cfg.Providers[plan.name]
		probeEnv := getenv
		if a.wf != nil && !provider.IsCLI() {
			// api_key_env and base_url read the environment, then the
			// draft's env_file; a session key stands in for api_key_env.
			lookup := provider
			if key != "" {
				lookup.APIKeyEnv = ""
			}
			env, err := a.wf.ProviderEnv(cfg, a.userConfigPath(), lookup)
			if err != nil {
				s.confirm = ""
				s.probeResult = ""
				s.notice = "Cannot test: " + err.Error()
				var fe *config.EnvFileError
				if errors.As(err, &fe) {
					s.notice += "; fix: " + fe.Fix()
				}
				return
			}
			probeEnv = env.Get
		}
		probes = append(probes, settingsProbePlan{name: plan.name, role: plan.role, key: key, provider: provider, getenv: probeEnv})
	}
	s.probes = probes
	s.probe = &s.probes[0]
	s.probeFromRouting = s.section == 1
	s.probeFingerprint = s.currentProbeFingerprint()

	s.confirm = "test"
	s.probeResult = ""
	s.probePassed = false
}
func (s *settingsScreen) startTest() tea.Cmd {
	p := s.probe
	if p == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.probeCancel = cancel
	s.probeRunning = true
	s.probeStarted = time.Now()
	s.probeCompleted = nil
	s.probeProgress = make(chan settingsProbeOutcome, len(s.probes))
	s.confirm = ""
	plans := append([]settingsProbePlan(nil), s.probes...)
	progress := s.probeProgress
	work := func() tea.Msg {
		defer close(progress)
		var outcomes []settingsProbeOutcome
		for _, plan := range plans {
			result, err := infer.ProbeProvider(ctx, plan.name, plan.provider, plan.role, plan.key, plan.getenv)
			outcome := settingsProbeOutcome{name: plan.name, role: plan.role, result: result, err: err}
			outcomes = append(outcomes, outcome)
			progress <- outcome
			if ctx.Err() != nil {
				break
			}
		}
		return settingsProbeResult{screen: s, outcomes: outcomes}
	}
	return tea.Batch(work, s.probeTick(), s.probeProgressNext())

}
func (s *settingsScreen) probeDone(m settingsProbeResult) {
	if s.probeCancel != nil {
		s.probeCancel()
	}
	s.probeRunning = false
	var summaries []string
	if len(m.outcomes) == 0 {
		m.outcomes = []settingsProbeOutcome{{result: m.result, err: m.err}}
	}
	s.probePassed = len(m.outcomes) >= len(s.probes)
	for _, o := range m.outcomes {
		if o.err != nil {
			s.probePassed = false
		}
		summary := o.result.Summary
		if o.err != nil {
			summary = s.theme.uiText("Test failed · ") + o.err.Error()
			if errors.Is(o.err, context.Canceled) {
				summary = s.theme.uiText("Test cancelled · no successful result")
			} else if errors.Is(o.err, context.DeadlineExceeded) {
				summary = s.theme.uiText("Test timed out · no successful result")
			}
		}
		if o.role != "" {
			summary = o.role + " / " + o.name + ": " + summary
		}
		summaries = append(summaries, summary)
	}
	if len(m.outcomes) < len(s.probes) {
		summaries = append(summaries, fmt.Sprintf("%d roles were not run", len(s.probes)-len(m.outcomes)))
	}
	s.probeResult = strings.Join(summaries, "; ")

	s.notice = s.probeResult
	s.confirm = "result"
}
func (s *settingsScreen) mouse(a *App, msg tea.MouseMsg) tea.Cmd {
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		if wheel.Button == tea.MouseWheelUp {
			s.scroll = max(s.scroll-3, 0)
		} else if wheel.Button == tea.MouseWheelDown {
			s.scroll += 3
		}
		return nil
	}
	m, ok := msg.(tea.MouseClickMsg)
	if !ok {
		return nil
	}
	if m.Y == 0 && m.X < 10 {
		if s.back != nil {
			s.back()
		}
		return nil
	}
	if s.setup || s.openrouter != nil {
		return nil // the choosers are keyboard-only; a click must not reach the fields beneath
	}
	railw := 0
	if s.width >= 100 {
		railw = 26
		if s.width >= 140 {
			railw = 30
		}
	}
	if railw > 0 && m.X < railw && m.Y >= 4 && m.Y < 4+len(settingsSections)*2 {
		s.stageAll()
		s.section = (m.Y - 4) / 2
		s.field = 0
		s.scroll = 0
		s.rail = false
		return nil
	}
	if s.confirm != "" || s.probeRunning {
		return nil
	}
	if s.rail && m.Y >= 4 && m.Y < 4+len(settingsSections)*2 {
		s.section = (m.Y - 4) / 2
		s.field = 0
		s.scroll = 0
		s.rail = false
		return nil
	}
	i := (m.Y - 4 + s.scroll) / 4
	if m.Y >= 4 && i < len(s.fields()) {
		if f := s.current(); f != nil {
			s.stage(*f)
		}
		s.field = i
		f := s.current()
		s.cursor = len([]rune(s.value(*f)))
		if f.kind == "provider" || f.kind == "select" || f.kind == "action" {
			return s.activate(a, *f)
		}
	}
	return nil
}
func (s *settingsScreen) view(a *App, w, h int) []string {
	s.theme = a.theme
	s.width = w
	s.height = h
	out := make([]string, h)
	if h == 0 {
		return out
	}
	changes := s.changeCount()
	testState := s.probeState()
	if s.probeResult != "" && (s.notice == s.probeResult || strings.HasPrefix(s.notice, "Changed since test")) {
		s.notice = testState
	}
	state := "Saved"
	if changes > 0 {
		state = fmt.Sprintf("%d unsaved changes", changes)
		if changes == 1 {
			state = "1 unsaved change"
		}
	}
	out[0] = "  Help    " + a.theme.PopupTitle.Render("Settings") + a.theme.uiText("  · ") + state
	if h > 1 {
		sec := settingsSections[s.section]
		out[1] = "  " + a.theme.PopupDim.Render(sec.scope+" / "+sec.title)
	}
	if h > 2 {
		detail := settingsSections[s.section].scope + a.theme.uiText(" config · ") + settingsPath(s.doc().Path, max(min(w-20, 104), 1), a.theme)
		if s.invocation != "" && s.section < 2 {
			detail = a.theme.uiText("Invocation override · ") + s.invocation
		}
		if s.notice != "" {
			detail = s.notice
		}
		out[2] = "  " + a.theme.PopupDim.Render(detail)
	}
	if h > 3 && testState != "" && !s.probeRunning {
		out[3] = "  " + a.theme.PopupDim.Render(a.theme.uiText("Connection test · ")+testState)
	}
	draw := func(y int, text string) {
		if y >= 0 && y < h {
			out[y] = text
		}
	}
	if s.probeRunning {
		frames := spinFrames
		if a.theme.ASCII {
			frames = []string{"|", "/", "-", "\\"}
		}
		frame := frames[int(time.Since(s.probeStarted)/spinEvery)%len(frames)]
		draw(4, "  "+frame+fmt.Sprintf(a.theme.uiText(" Testing connections · %d of %d roles complete · %s"), len(s.probeCompleted), len(s.probes), time.Since(s.probeStarted).Round(time.Second)))
		row := 6
		for i, plan := range s.probes {
			text := a.theme.uiText("Waiting · ") + plan.role + " / " + plan.name
			if i < len(s.probeCompleted) {
				text = probeOutcomeSummary(s.probeCompleted[i], a.theme)
			} else if i == len(s.probeCompleted) {
				text = a.theme.uiText("Testing · ") + plan.role + " / " + plan.name + a.theme.uiText(" · ") + plan.provider.Model
			}
			for _, line := range helpWrap(text, max(w-4, 1)) {
				draw(row, "  "+line)
				row++
			}
			row++
		}
		draw(min(row+1, h-2), "  Synthetic text only. Ctrl+C cancels this test.")
		return settingsClip(out, w)
	}

	if s.confirm != "" {
		s.confirmView(a, out, w)
		return settingsClip(out, w)
	}
	if s.openrouter != nil {
		if h > 4 {
			s.openRouterView(a, out, w, h)
		}
		return settingsClip(out, w)
	}
	if s.setup {
		draw(2, a.theme.uiText("  Add a connection · setup is optional"))
		for i, c := range connectionChoices {
			style := a.theme.HelpLabel.Background(a.theme.Token("card"))
			if i == s.setupChoice {
				style = a.theme.PopupTitle.Foreground(a.theme.Token("control")).Background(a.theme.Token("selectedCard"))
			}
			draw(4+i*3, "  "+style.Render(padTo(c.label, max(w-4, 1))))
			for line, txt := range helpWrap(c.desc, max(w-6, 1)) {
				if line > 1 {
					break
				}
				draw(5+i*3+line, "    "+a.theme.PopupDim.Render(txt))
			}
		}

		return settingsClip(out, w)
	}
	railw := 0
	if w >= 100 {
		railw = 26
		if w >= 140 {
			railw = 30
		}
	}
	if railw > 0 || s.rail {
		for i, sec := range settingsSections {
			txt := sec.scope + " / " + sec.title
			sty := a.theme.PopupDim
			if i == s.section {
				sty = a.theme.PopupTitle
			}
			draw(4+i*2, "  "+sty.Render(ansi.Truncate(txt, map[bool]int{true: railw - 4, false: w - 4}[railw > 0], "")))
		}
		if s.rail {
			return settingsClip(out, w)
		}
	}
	x := 2
	if railw > 0 {
		x = railw + 2
	}
	fw := max(min(w-x-2, 104), 1)
	fs := s.fields()
	s.scroll = min(max(s.scroll, 0), max(len(fs)*4-max(h-4, 1), 0))
	appendAt := func(y int, txt string) {
		if y < 4 || y >= h {
			return
		}
		prefix := ""
		if railw > 0 {
			prefix = padTo(out[y], x)
		} else {
			prefix = strings.Repeat(" ", x)
		}
		out[y] = prefix + txt
	}
	for i, f := range fs {
		y := 4 + i*4 - s.scroll
		if y+2 < 4 || y >= h {
			continue
		}
		value := s.value(f)
		source := "Built-in default"
		if _, ok := s.doc().Get(f.path...); ok && len(f.path) > 0 {
			source = settingsSections[s.section].scope + " config"
		}
		if f.kind == "secret" {
			source = "Session only"
		}
		label := f.label + a.theme.uiText(" · ") + source
		labelStyle := a.theme.HelpLabel
		if i == s.field {
			labelStyle = a.theme.PopupTitle.Foreground(a.theme.Token("control"))
		}
		appendAt(y, labelStyle.Render(ansi.Truncate(label, fw, "")))
		shown := value
		for j, choice := range f.choices {
			if shown == choice && shown != "" {
				shown = f.choiceLabel(j)
				break
			}
		}
		if shown == "" {
			shown = f.defaultValue
			if len(f.path) == 1 {
				switch f.path[0] {
				case "foreground_profile":
					detected := "light"
					if a.theme.Dark {
						detected = "dark"
					}
					shown = a.theme.uiText("Auto · current ") + detected
				case "color_capability":
					detected := "truecolor"
					if a.theme.Reduced {
						detected = "256 colours"
					}
					shown = a.theme.uiText("Auto · current ") + detected
				case "glyph_profile":
					detected := "Unicode"
					if a.theme.ASCII {
						detected = "ASCII"
					}
					shown = a.theme.uiText("Auto · current ") + detected
				}
			}
		}
		switch f.kind {
		case "provider", "select":
			shown += "  v"
		case "action":
			shown = a.theme.uiText("Enter · ") + shown
		case "readonly":
		case "secret":
			if shown == "" {
				shown = "        "
			}
		default:
			if i == s.field {
				r := []rune(shown)
				pos := min(s.cursor, len(r))
				shown = string(r[:pos]) + "_" + string(r[pos:])
			}
		}
		fieldText := "[ " + ansi.Truncate(shown, max(fw-4, 1), "") + " ]"
		appendAt(y+1, a.theme.HelpLabel.Background(a.theme.Token("card")).Render(fieldText))
		support := f.help
		if err := s.errors[s.fieldID(f)]; err != "" {
			support = err
		}
		appendAt(y+2, a.theme.PopupDim.Render(ansi.Truncate(support, fw, "")))
	}
	if s.selector {
		f := s.current()
		if f != nil {
			visible := max(h-6, 1)
			first := max(0, min(s.choice-visible+1, len(f.choices)-visible))
			start := min(5, max(h-min(len(f.choices), visible)-1, 3))
			for i := first; i < min(first+visible, len(f.choices)); i++ {
				txt := "  " + f.choiceLabel(i)
				if i == s.choice {
					txt = a.theme.PopupTitle.Render(txt)
				}
				draw(start+i-first, strings.Repeat(" ", x)+txt)
			}
		}
	}
	return settingsClip(out, w)
}

// settingsClip only clips cells; generated glyphs are converted before layout.
func settingsClip(out []string, w int) []string {
	for i := range out {
		out[i] = ansi.Truncate(out[i], w, "")
	}
	return out
}
func (s *settingsScreen) confirmView(a *App, out []string, w int) {
	y := 4
	add := func(text string) {
		for _, ln := range helpWrap(text, max(w-4, 1)) {
			if y < len(out) {
				out[y] = "  " + ln
				y++
			}
		}
	}
	switch s.confirm {
	case "close":
		add("Unsaved settings changes")
	case "revert":
		add("Discard all unsaved settings changes?")
	case "remove":
		add("Remove connection " + s.provider + "?")
		add("Routes naming this provider must also be updated before Save.")
	case "save":
		add("Review settings before saving")
		add("Only the selected configuration file is written. Document buffers stay unchanged.")
		for _, scope := range []struct {
			name string
			d    *config.Document
		}{{"User", s.user}, {"Project", s.project}} {
			changes := scope.d.Changes()
			if len(changes) == 0 {
				continue
			}
			add("")
			add(scope.name + " settings")
			add(settingsPath(scope.d.Path, max(w-4, 1), a.theme))
			for _, change := range scope.d.Review() {
				add("  " + strings.Join(change.Path, ".") + ": " + settingsReviewValue(change.Before, change.BeforeSet) + " -> " + settingsReviewValue(change.After, change.AfterSet))
			}
			if ps := scope.d.Problems(); len(ps) > 0 {
				add("Error: " + strings.Join(ps, "; "))
			}
		}
		if len(s.secrets) > 0 {
			add(a.theme.uiText("Session key entered · activated on Save user · never persisted"))
		}
		add("")
		add("Saving preserves comments and untouched TOML. Unsupported source edits or schema errors block writes; the draft is retained.")
	case "result":
		add("Connection test result")
		for _, summary := range strings.Split(s.probeResult, "; ") {
			add("")
			add(summary)
		}
		add("")
	case "test":
		if s.probe == nil {
			return
		}
		p := s.probe
		add(a.theme.uiText("Connection test plan · not started"))
		for _, plan := range s.probes {
			add("Provider: " + plan.name + a.theme.uiText(" · Role: ") + plan.role + a.theme.uiText(" · Model: ") + plan.provider.Model)
		}
		if p.provider.IsCLI() {
			add("Runs the configured CLI using its own login; no browser or login starts here.")
		} else {
			endpoint := p.provider.BaseURL
			if endpoint == "" && p.provider.IsGate() {
				endpoint = p.provider.API.DefaultBaseURL()
			}
			add("Endpoint: " + endpoint)
		}
		add("Each listed role runs one logical test using fixed synthetic text. No project files, prompts or proposal content are sent.")
		add("API chat: 30 seconds per attempt, up to 2 attempts. CLI: one bounded request. Test can incur provider charges.")
	}
}
func (s *settingsScreen) renameProvider(name, id string) bool {
	name = strings.TrimSpace(name)
	if name == s.provider {
		delete(s.errors, id)
		return true
	}
	if name == "" {
		s.errors[id] = "Error: connection name is required"
		return false
	}
	if _, exists := s.user.Get("providers", name); exists {
		s.errors[id] = "Error: this connection name already exists"
		return false
	}
	old := s.provider
	value, exists := s.user.Get("providers", old)
	if !exists {
		return false
	}
	s.user.Set(value, "providers", name)
	s.user.Set(nil, "providers", old)
	for _, route := range [][]string{{"default"}, {"triage", "provider"}, {"gate", "provider"}} {
		if value, _ := s.user.Get(route...); value == old {
			s.user.Set(name, route...)
			id := "User\x00" + strings.Join(route, "\x00")
			if _, seen := s.text[id]; seen {
				s.text[id] = name
				s.touched[id] = true
			}
		}
	}
	for key, v := range s.text {
		oldPrefix := "User\x00providers\x00" + old + "\x00"
		if strings.HasPrefix(key, oldPrefix) {
			newKey := "User\x00providers\x00" + name + "\x00" + strings.TrimPrefix(key, oldPrefix)
			s.text[newKey] = v
			s.baseline[newKey] = s.baseline[key]
			s.touched[newKey] = s.touched[key]
			delete(s.text, key)
			delete(s.baseline, key)
		}
	}
	if key := s.secrets[old]; key != "" {
		s.secrets[name] = key
		delete(s.secrets, old)
	} else if key := s.sessionKeys[old]; key != "" {
		s.secrets[name] = key
	}
	delete(s.text, id)
	delete(s.baseline, id)
	delete(s.errors, id)
	s.provider = name
	s.notice = "Connection rename staged; related routes updated"
	return true
}
func (s *settingsScreen) probeTick() tea.Cmd {
	if !s.probeRunning {
		return nil
	}
	return tea.Tick(spinEvery, func(time.Time) tea.Msg { return settingsProbeTick{screen: s} })
}
func displayBinding(key, label string) Binding {
	return Binding{Key: key, Label: label, Action: ActNone, Bar: true}
}
func (s *settingsScreen) helperBindings(*App) []Binding {
	pairs := func(items ...string) []Binding {
		var bs []Binding
		for i := 0; i+1 < len(items); i += 2 {
			bs = append(bs, displayBinding(items[i], items[i+1]))
		}
		return bs
	}
	if s.probeRunning {
		return pairs("^G", "Help", "^C", "Cancel test")
	}
	switch s.confirm {
	case "close":
		return pairs("^G", "Help", "Esc", "Keep editing", "^O", "Save & close", "d", "Discard")
	case "revert", "remove":
		return pairs("^G", "Help", "Esc", "Keep editing", "d", "Discard")
	case "save":
		return pairs("^G", "Help", "Esc", "Back", "u", "Save user", "p", "Save project", "^O", "Save next scope")
	case "result":
		return pairs("^G", "Help", "Esc", "Edit connection", "^T", "Retry plan")
	case "test":
		return pairs("^G", "Help", "Esc", "Edit connection", "^T", "Start test")
	}
	if s.rail || s.selector || s.setup || s.openrouter != nil {
		return pairs("^G", "Help", "Esc", "Back", "Ret", "Choose", "Up", "Previous", "Down", "Next")
	}
	return pairs("^G", "Help", "Esc", "Close", "^O", "Save", "^R", "Revert all", "^Q", "Sections", "^T", "Test", "PgUp", "Page up", "^V", "Page down", "Tab", "Next", "S-Tab", "Previous")
}
func (s *settingsScreen) helperEnabled(_ *App, b Binding) bool {
	switch b.Label {
	case "Save", "Revert all":
		return s.changeCount() > 0 || (b.Label == "Save" && len(s.secrets) > 0)
	case "Test":
		return s.section == 0 || s.section == 1
	case "Save user":
		return len(s.user.Changes()) > 0 || len(s.secrets) > 0
	case "Save project":
		return len(s.project.Changes()) > 0
	case "Page up":
		return s.scroll > 0
	case "Page down":
		return s.scroll < max(len(s.fields())*4-max(s.height-4, 1), 0)
	case "Choose":
		if s.selector {
			f := s.current()
			return f != nil && len(f.choices) > 0
		}
	}
	return true
}
func settingsPath(path string, width int, themes ...*Theme) string {
	th := uiTheme(themes)
	if ansi.StringWidth(path) <= width {
		return path
	}
	if width < 8 {
		return ansi.Truncate(path, width, "")
	}
	tail := max(width-18, 4)
	suffix := th.uiText("…")
	head := max(width-tail-ansi.StringWidth(suffix), 0)
	cells := ansi.StringWidth(path)
	return ansi.Truncate(path, head, "") + suffix + ansi.Cut(path, cells-tail, cells)
}
func settingsReviewValue(v any, present bool) string {
	if !present {
		return "Use default / unset"
	}
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case map[string]any:
		return fmt.Sprintf("table (%d fields)", len(x))
	default:
		return valueText(v)
	}
}
func (s *settingsScreen) bumpCredential(name string) {
	if s.credentialGeneration == nil {
		s.credentialGeneration = map[string]uint64{}
	}
	s.credentialGeneration[name]++
}
func (s *settingsScreen) currentProbeFingerprint() string {
	s.stageAll()
	providers := map[string]any{}
	generations := map[string]uint64{}
	invalid := map[string]string{}
	for _, plan := range s.probes {
		providers[plan.name], _ = s.user.Get("providers", plan.name)
		generations[plan.name] = s.credentialGeneration[plan.name]
		prefix := "User\x00providers\x00" + plan.name + "\x00"
		for id := range s.errors {
			if strings.HasPrefix(id, prefix) || id == "User\x00provider-name\x00"+plan.name {
				invalid[id] = s.text[id]
			}
		}
	}
	routes := map[string]any{}
	if s.probeFromRouting {
		for _, name := range []string{"default", "triage", "gate"} {
			routes[name], _ = s.user.Get(name)
		}
		for id := range s.errors {
			for _, prefix := range []string{"User\x00default", "User\x00triage\x00", "User\x00gate\x00"} {
				if strings.HasPrefix(id, prefix) {
					invalid[id] = s.text[id]
				}
			}
		}
	}
	selection := ""
	if !s.probeFromRouting {
		selection = s.provider
	}
	payload := struct {
		Selection   string
		Providers   map[string]any
		Routes      map[string]any
		Credentials map[string]uint64
		Invalid     map[string]string
	}{selection, providers, routes, generations, invalid}
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(fmt.Sprintf("%#v", payload))
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func (s *settingsScreen) probeState() string {
	if s.probeResult == "" {
		return ""
	}
	if s.currentProbeFingerprint() != s.probeFingerprint {
		return s.theme.uiText("Changed since test · current draft has not been tested")
	}
	return s.probeResult
}

// testVerdict is the connection test's verdict on the current draft, for
// the save notices: a save of exactly the tested snapshot keeps its result.
func (s *settingsScreen) testVerdict() string {
	switch {
	case s.probeResult == "":
		return "not tested"
	case s.currentProbeFingerprint() != s.probeFingerprint:
		return "changed since test"
	case s.probePassed:
		return "test passed"
	}
	return "test did not pass"
}
func (s *settingsScreen) probeProgressNext() tea.Cmd {
	ch := s.probeProgress
	if ch == nil || !s.probeRunning {
		return nil
	}
	return func() tea.Msg {
		outcome, open := <-ch
		if !open {
			return nil
		}
		return settingsProbeProgressMsg{screen: s, outcome: outcome}
	}
}
func (s *settingsScreen) probeProgressDone(m settingsProbeProgressMsg) tea.Cmd {
	if !s.probeRunning {
		return nil
	}
	s.probeCompleted = append(s.probeCompleted, m.outcome)
	return s.probeProgressNext()
}
func probeOutcomeSummary(o settingsProbeOutcome, themes ...*Theme) string {
	th := uiTheme(themes)
	summary := o.result.Summary
	if o.err != nil {
		summary = th.uiText("Test failed · ") + o.err.Error()
		if errors.Is(o.err, context.Canceled) {
			summary = th.uiText("Test cancelled · no successful result")
		} else if errors.Is(o.err, context.DeadlineExceeded) {
			summary = th.uiText("Test timed out · no successful result")
		}
	}
	if o.role != "" {
		summary = o.role + " / " + o.name + ": " + summary
	}
	return summary
}
