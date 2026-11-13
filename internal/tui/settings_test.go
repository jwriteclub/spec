// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/workflow"
)

const settingsFixture = `default='fixture'
[providers.fixture]
kind='openai'
base_url='http://localhost/v1'
model='fixture/model'
api_key_env='FIXTURE_API_KEY'
[providers.fixture.extra]
temperature=0.2
[providers.fixture.extra.reasoning]
mode='quiet'
`

func fixtureSettings(t *testing.T) *settingsScreen {
	t.Helper()
	dir := t.TempDir()
	u := config.NewDocument(filepath.Join(dir, "user.toml"), []byte(settingsFixture), false, userSettingsValidate)
	p := config.NewDocument(filepath.Join(dir, "spec.toml"), []byte("name='fixture'\n[format]\nwidth=100\n"), false, projectSettingsValidate)
	s := newSettingsWithDocuments(u, p)
	s.selectFirstProvider()
	return s
}
func settingsFieldNamed(t *testing.T, s *settingsScreen, name string) settingsField {
	t.Helper()
	for i, f := range s.fields() {
		if f.label == name {
			s.field = i
			s.scroll = max(i*4-3, 0)
			return f
		}
	}
	t.Fatalf("missing field %s", name)
	return settingsField{}
}

// realConfig stands in for the user's real config: HOME and XDG_CONFIG_HOME
// name a sentinel file, as they do outside the test binary. It returns the
// file's path and bytes.
func realConfig(t *testing.T) (string, []byte) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	path := filepath.Join(home, ".config", "spec", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	want := []byte("# the user's own\ndefault = 'cf' # route\n[providers.cf]\nbase_url = 'http://localhost/v1'\nmodel = 'real'\n")
	require.NoError(t, os.WriteFile(path, want, 0o600))
	return path, want
}

// A settings rename and save in an App with no injected environment, which
// only a test can build, must not reach the user's config: on 2026-11-03
// something renamed cf to cfy($ in the real file.
func TestSettingsWithoutEnvironmentCannotReachUserConfig(t *testing.T) {
	path, want := realConfig(t)
	a := newTestApp(t, testOptions(lensdProject(t)))
	a.openHelp()
	ref := a.top().(*referenceScreen)
	ref.settingsTab(a)
	s := ref.settings
	s.user.Set("cf", "default")
	s.user.Set(map[string]any{"base_url": "http://localhost/v1", "model": "draft"}, "providers", "cf")
	s.selectFirstProvider()
	s.edit(settingsFieldNamed(t, s, "Connection name"), "cfy($")
	require.True(t, s.stageAll(), s.errors)
	_, renamed := s.user.Get("providers", "cfy($")
	require.True(t, renamed, "the rename reached the draft")
	s.save(a, "User")

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "the user's config is byte for byte as it was")
	assert.Empty(t, s.user.Path, "no file behind the draft")
	assert.Contains(t, s.notice, "Could not save", "says so rather than pretending")
}

// The same App reads nothing from there either.
func TestAppWithoutEnvironmentReadsNoUserConfig(t *testing.T) {
	realConfig(t)
	a := newTestApp(t, testOptions(lensdProject(t)))
	assert.Empty(t, a.userConfigPath())
	loaded, err := a.loadUserConfig()
	require.NoError(t, err)
	assert.Empty(t, loaded.Path)
	assert.Empty(t, loaded.Config.Default)
}

// Production injects its environment (cli.Run defaults Env.Getenv to
// os.Getenv), and settings opens and saves the file that environment names.
func TestSettingsWithEnvironmentOpensItsUserConfig(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "x")}
	path := filepath.Join(home, "x", "spec", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(settingsFixture), 0o600))
	opts := testOptions(lensdProject(t))
	opts.Workflow = &workflow.Workflow{Getenv: func(k string) string { return env[k] }}
	a := newTestApp(t, opts)
	assert.Equal(t, path, a.userConfigPath())
	s := newSettingsScreen(a)
	assert.Equal(t, path, s.user.Path)
	s.edit(settingsFieldNamed(t, s, "Connection name"), "renamed")
	require.True(t, s.stageAll(), s.errors)
	require.NotNil(t, s.save(a, "User"), s.notice)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(got), "renamed")
}

func TestSettingsScopesDefaultsAndNestedTables(t *testing.T) {
	s := fixtureSettings(t)
	a := &App{theme: NewTheme(true, false)}
	_ = s.view(a, 100, 27)
	if !s.stageAll() {
		t.Fatal(s.errors)
	}
	if len(s.user.Changes()) != 0 {
		t.Fatal("viewing settings rewrites loaded values", s.user.Changes())
	}
	if len(s.user.Problems()) != 0 {
		t.Fatal(s.user.Problems())
	}
	s.section = 2
	f := settingsFieldNamed(t, s, "Foreground profile")
	s.edit(f, "dark")
	if !s.stage(f) {
		t.Fatal(s.errors)
	}
	if v, _ := s.user.Get("foreground_profile"); v != "dark" {
		t.Fatal(v)
	}
	if _, ok := s.project.Get("foreground_profile"); ok {
		t.Fatal("User preference entered Project scope")
	}
	s.edit(f, "Use default")
	s.stage(f)
	if _, ok := s.user.Get("foreground_profile"); ok {
		t.Fatal("Use default persisted duplicate default")
	}
	v, _ := s.user.Get("providers", "fixture", "extra", "reasoning", "mode")
	if v != "quiet" {
		t.Fatal("nested advanced options lost")
	}
}
func TestSettingsInvalidNumberRetainsTextAndDraft(t *testing.T) {
	s := fixtureSettings(t)
	s.section = 3
	f := settingsFieldNamed(t, s, "Context budget (tokens)")
	s.edit(f, "not-a-number")
	if s.stage(f) {
		t.Fatal("invalid number staged")
	}
	s.openSave(false)
	if s.confirm == "save" {
		t.Fatal("invalid draft advanced to save")
	}
	if s.value(f) != "not-a-number" {
		t.Fatal("invalid text lost")
	}
	if len(s.errors) == 0 {
		t.Fatal("no local reason")
	}
}
func TestSettingsSecretMaskAndExplicitActivation(t *testing.T) {
	s := fixtureSettings(t)
	a := &App{theme: NewTheme(true, false), wf: &workflow.Workflow{}}
	settingsFieldNamed(t, s, "Key for this session")
	s.paste("fixture-session-credential")
	rendered := strings.Join(s.view(a, 100, 27), "\n")
	if strings.Contains(rendered, "fixture-session-credential") || !strings.Contains(rendered, "********") {
		t.Fatal("secret was exposed or not masked")
	}
	b, _ := s.user.Bytes()
	if strings.Contains(string(b), "fixture-session-credential") {
		t.Fatal("session key entered persistent draft")
	}
	if a.wf.SessionKeySnapshot()["fixture"] != "" {
		t.Fatal("typing activated a key")
	}
	// Match the explicit missing snapshot used by this fixture before Save.
	if err := os.WriteFile(s.user.Path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.user.Path); err != nil {
		t.Fatal(err)
	}
	s.user = config.NewDocument(s.user.Path, nil, false, userSettingsValidate)
	s.openOpenRouterSetup()
	s.stageOpenRouter()
	for _, f := range s.fields() {
		if f.label == "Model ID" {
			s.edit(f, "fixture/model")
			s.stage(f)
		}
	}
	s.secrets[s.provider] = "fixture-session-credential"
	s.stageAll()
	cmd := s.save(a, "User")
	if cmd == nil {
		t.Fatal(s.notice)
	}
	disk, _ := os.ReadFile(s.user.Path)
	if strings.Contains(string(disk), "fixture-session-credential") {
		t.Fatal("session key persisted")
	}
	if a.wf.SessionKeySnapshot()[s.provider] != "fixture-session-credential" {
		t.Fatal("explicit Save did not activate session key")
	}
}
func TestSettingsRenamePreservesAdvancedOptionsAndRoutes(t *testing.T) {
	s := fixtureSettings(t)
	f := settingsFieldNamed(t, s, "Connection name")
	s.edit(f, "renamed.with.dot")
	if !s.stageAll() {
		t.Fatal(s.errors)
	}
	if s.provider != "renamed.with.dot" {
		t.Fatal(s.provider)
	}
	v, _ := s.user.Get("providers", s.provider, "extra", "reasoning", "mode")
	if v != "quiet" {
		t.Fatal(v)
	}
	v, _ = s.user.Get("default")
	if v != s.provider {
		t.Fatal("route not updated", v)
	}
	if _, ok := s.user.Get("providers", "fixture"); ok {
		t.Fatal("old table retained")
	}
}
func TestSettingsPasteLiteralSingleLineAndUndoStaysLocal(t *testing.T) {
	s := fixtureSettings(t)
	s.section = 4
	f := settingsFieldNamed(t, s, "Project name")
	s.cursor = 0
	s.paste("literal ${text}")
	if !strings.Contains(s.value(f), "${text}") {
		t.Fatal(s.value(f))
	}
	before := s.value(f)
	s.paste("line\nbreak")
	if s.value(f) != before {
		t.Fatal("multiline paste changed field")
	}
	s.key(&App{}, key("ctrl+z"), Binding{}, false)
	if s.value(f) != "fixture" {
		t.Fatal("field undo failed", s.value(f))
	}
}
func TestSettingsAndHelpResponsiveBounds(t *testing.T) {
	a := &App{theme: NewTheme(true, false)}
	for _, size := range [][2]int{{80, 21}, {100, 26}, {140, 35}, {220, 45}, {40, 10}} {
		s := fixtureSettings(t)
		s.section = 2
		lines := s.view(a, size[0], size[1])
		if len(lines) != size[1] {
			t.Fatal(len(lines))
		}
		for _, ln := range lines {
			if ansi.StringWidth(ln) > size[0] {
				t.Fatalf("settings overflow %dx%d: %q", size[0], size[1], ln)
			}
		}
		h := &referenceScreen{caller: CtxEditor, match: -1}
		lines = h.view(a, size[0], size[1])
		if len(lines) != size[1] {
			t.Fatal(len(lines))
		}
		for _, ln := range lines {
			if ansi.StringWidth(ln) > size[0] {
				t.Fatalf("Help overflow: %q", ln)
			}
		}
	}
}
func TestHelpFindRetainsReferenceAndTypingQ(t *testing.T) {
	a := &App{theme: NewTheme(true, false)}
	s := &referenceScreen{caller: CtxEditor, match: -1}
	s.view(a, 100, 26)
	total := 0
	for _, c := range s.columns {
		total += len(c)
	}
	s.find = true
	s.key(a, key("q"), Binding{}, false)
	if s.query != "q" || !s.find {
		t.Fatal("q closed Find")
	}
	s.query = "Ctrl"
	s.view(a, 100, 26)
	after := 0
	for _, c := range s.columns {
		after += len(c)
	}
	if total != after {
		t.Fatal("search filtered the reference")
	}
	s.visitMatch(false)
	if s.match < 0 {
		t.Fatal("search found no key aliases")
	}
	s.key(a, key("esc"), Binding{}, false)
	if s.find || s.query != "Ctrl" {
		t.Fatal("Find escape lost query")
	}
}
func TestUniversalHelpPreservesPromptPopupAndJob(t *testing.T) {
	root := lensdProject(t)
	opts := testOptions(root)
	opts.Workflow = &workflow.Workflow{Getenv: func(string) string { return "" }}
	a := newTestApp(t, opts)
	p := newTextPrompt(promptFind, "Find", "keep this draft")
	a.prompt = p
	pop := &popup{notice: true, lines: []string{"notice remains"}}
	a.popup = pop
	cancelled := false
	a.job = &job{label: "fixture background check", cancel: func() { cancelled = true }}
	depth := len(a.screens)
	send(a, key("ctrl+g"))
	ref, ok := a.top().(*referenceScreen)
	if !ok || a.prompt != nil || a.popup != nil {
		t.Fatal("Help failed to suspend caller overlays")
	}
	send(a, key("ctrl+g"))
	if len(a.screens) != depth+1 || a.top() != ref {
		t.Fatal("nested Help instance")
	}
	send(a, key("esc"))
	if a.prompt != p || a.popup != pop || p.input != "keep this draft" || cancelled || a.job == nil {
		t.Fatal("Help mutated caller draft, popup, or background operation")
	}
}
func TestUniversalHelpPreservesSettingsFieldAndFindQuery(t *testing.T) {
	root := lensdProject(t)
	opts := testOptions(root)
	opts.Workflow = &workflow.Workflow{Getenv: func(string) string { return "" }}
	a := newTestApp(t, opts)
	a.openHelp()
	ref := a.top().(*referenceScreen)
	ref.settings = fixtureSettings(t)
	ref.settings.back = func() { ref.referenceFocus() }
	ref.showSettings = true
	ref.settings.section = 4
	f := settingsFieldNamed(t, ref.settings, "Project name")
	ref.settings.edit(f, "unfinished settings draft")
	ref.settings.cursor = 7
	send(a, key("ctrl+g"))
	if ref.showSettings {
		t.Fatal("Ctrl+G did not show Help")
	}
	send(a, key("ctrl+w"), key("q"), key("esc"))
	if ref.query != "q" || ref.find {
		t.Fatal("Find scope was not preserved")
	}
	send(a, key("esc"))
	if !ref.showSettings || ref.settings.value(f) != "unfinished settings draft" || ref.settings.cursor != 7 {
		t.Fatal("Help did not return to the exact Settings field")
	}
}
func TestStatusDetailsContainFullMessageAndCounts(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	a.flash = "Save failed: " + strings.Repeat("long full explanation ", 12)
	s := &statusReferenceScreen{}
	lines := s.view(a, 80, 40)
	text := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(text, "long full explanation") || !strings.Contains(text, "Current-file diagnostics:") || !strings.Contains(text, "Review awaiting:") {
		t.Fatal(text)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 80 {
			t.Fatal("status detail overflow")
		}
	}
}
func TestSettingsReturningToLoadedValueRemovesChange(t *testing.T) {
	s := fixtureSettings(t)
	s.section = 4
	f := settingsFieldNamed(t, s, "Project name")
	s.edit(f, "changed")
	s.stage(f)
	s.edit(f, "fixture")
	s.stage(f)
	if len(s.project.Changes()) != 0 {
		t.Fatal("returning to loaded value stayed dirty", s.project.Changes())
	}
	s.section = 5
	f = settingsFieldNamed(t, s, "Paragraph width (columns)")
	s.edit(f, "120")
	s.stage(f)
	s.edit(f, "100")
	s.stage(f)
	if len(s.project.Changes()) != 0 {
		t.Fatal("numeric representation created false changes", s.project.Changes())
	}
}
func TestSettingsNewPresetSurvivesReflowAndRevert(t *testing.T) {
	u := config.NewDocument("fixture", nil, false, userSettingsValidate)
	p := config.NewDocument("project", nil, false, projectSettingsValidate)
	s := newSettingsWithDocuments(u, p)
	s.openOpenRouterSetup()
	s.stageOpenRouter()
	a := &App{theme: NewTheme(true, false)}
	s.view(a, 100, 26)
	s.stageAll()
	if value, _ := u.Get("providers", "openrouter", "base_url"); value != "https://openrouter.ai/api/v1" {
		t.Fatal("display discarded staged preset", value)
	}
	s.revert()
	if s.provider != "" || len(s.providerNames()) != 0 {
		t.Fatal("discard retained a nonexistent provider")
	}
}
func TestHelpPrintableKeyCaseAndASCII(t *testing.T) {
	for input, want := range map[string]string{"y": "y", "Y": "Y", "f": "f", "ctrl+p": "Ctrl+P"} {
		if got := referenceKeyName(input); got != want {
			t.Fatalf("key %s rendered %s", input, got)
		}
	}
	a := &App{theme: NewThemeProfile(true, false, true, true)}
	s := fixtureSettings(t)
	s.section = 2
	for _, line := range s.view(a, 80, 21) {
		for _, r := range ansi.Strip(line) {
			if r > 127 {
				t.Fatalf("ASCII preference emitted %q", r)
			}
		}
	}
	h := &referenceScreen{caller: CtxChecks, match: -1}
	for _, line := range h.view(a, 140, 35) {
		for _, r := range ansi.Strip(line) {
			if r > 127 {
				t.Fatalf("ASCII Help emitted %q", r)
			}
		}
	}
}
func TestSettingsProbeResultTracksExactRelevantDraft(t *testing.T) {
	s := fixtureSettings(t)
	a := &App{theme: NewTheme(true, false), wf: &workflow.Workflow{Getenv: func(string) string { return "" }}}
	s.openTest(a)
	if s.probe == nil {
		t.Fatal(s.notice)
	}
	frozen := s.probe.provider.Model
	s.probeDone(settingsProbeResult{outcomes: []settingsProbeOutcome{{name: s.provider, role: "check", result: infer.ProbeResult{Summary: "Test passed"}}}})
	s.confirm = ""
	if !strings.Contains(s.probeState(), "Test passed") {
		t.Fatal(s.probeState())
	}
	f := settingsFieldNamed(t, s, "Model ID")
	s.edit(f, "changed/model")
	s.stage(f)
	s.view(a, 100, 26)
	if !strings.Contains(s.probeState(), "Changed since test") || strings.Contains(s.notice, "Test passed") {
		t.Fatal("stale success remained visible", s.notice, s.probeState())
	}
	if s.probe.provider.Model != frozen {
		t.Fatal("draft editing mutated frozen test snapshot")
	}
	s.edit(f, frozen)
	s.stage(f)
	if !strings.Contains(s.probeState(), "Test passed") {
		t.Fatal("restoring exact draft did not restore matching test scope")
	}
	s.section = 2
	appearance := settingsFieldNamed(t, s, "Foreground profile")
	s.edit(appearance, "light")
	s.stage(appearance)
	if !strings.Contains(s.probeState(), "Test passed") {
		t.Fatal("unrelated appearance edit invalidated provider test")
	}
	s.section = 0
	settingsFieldNamed(t, s, "Key for this session")
	s.paste("fixture-new-session-credential")
	if !strings.Contains(s.probeState(), "Changed since test") {
		t.Fatal("credential generation did not invalidate test")
	}
}

// Saving names the connection test's verdict on what was saved: a save of
// exactly the tested snapshot keeps the pass (it read "not tested").
func TestSettingsSaveNamesTestVerdict(t *testing.T) {
	passed := settingsProbeOutcome{name: "fixture", role: "check", result: infer.ProbeResult{Summary: "Test passed · 9 ms · reasoning model"}}
	failed := settingsProbeOutcome{name: "fixture", role: "check", err: errors.New("HTTP 401")}
	for _, tc := range []struct {
		name    string
		outcome *settingsProbeOutcome
		edit    bool
		key     string
		want    string
	}{
		{"untested", nil, false, "", "not tested"},
		{"passed", &passed, false, "", "test passed"},
		{"passed with session key", &passed, false, "fixture-session-credential", "test passed"},
		{"failed", &failed, false, "", "test did not pass"},
		{"changed", &passed, true, "", "changed since test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "user.toml")
			require.NoError(t, os.WriteFile(path, []byte(settingsFixture), 0o600))
			u := config.OpenDocument(path, userSettingsValidate)
			p := config.NewDocument(filepath.Join(dir, "spec.toml"), []byte("name='fixture'\n"), false, projectSettingsValidate)
			s := newSettingsWithDocuments(u, p)
			s.selectFirstProvider()
			a := &App{theme: NewTheme(true, false), wf: &workflow.Workflow{Getenv: func(string) string { return "" }}}
			model := settingsFieldNamed(t, s, "Model ID")
			s.edit(model, "fixture/model-2")
			s.stage(model)
			if tc.key != "" {
				settingsFieldNamed(t, s, "Key for this session")
				s.paste(tc.key)
			}
			if tc.outcome != nil {
				s.openTest(a)
				require.NotNil(t, s.probe, s.notice)
				s.probeDone(settingsProbeResult{outcomes: []settingsProbeOutcome{*tc.outcome}})
				s.confirm = ""
			}
			if tc.edit {
				s.edit(model, "fixture/model-3")
				s.stage(model)
			}
			cmd := s.save(a, "User")
			require.NotNil(t, cmd, s.notice)
			assert.Equal(t, settingsSavedMsg{scope: "User", test: tc.want}, cmd())
			assert.Equal(t, "User saved · "+tc.want, s.notice)
			assert.NotContains(t, s.notice+a.flash, "fixture-session-credential")
		})
	}
}
func TestSettingsRoutingAndCredentialDiscardInvalidateTest(t *testing.T) {
	s := fixtureSettings(t)
	a := &App{theme: NewTheme(true, false), wf: &workflow.Workflow{Getenv: func(string) string { return "" }}}
	s.section = 1
	s.openTest(a)
	s.probeDone(settingsProbeResult{outcomes: []settingsProbeOutcome{{name: s.provider, role: "check", result: infer.ProbeResult{Summary: "Test passed"}}}})
	s.confirm = ""
	s.user.Set("missing-provider", "triage", "provider")
	if !strings.Contains(s.probeState(), "Changed since test") {
		t.Fatal("routing mutation retained success")
	}
	s = fixtureSettings(t)
	settingsFieldNamed(t, s, "Key for this session")
	s.paste("fixture-staged-credential")
	s.openTest(a)
	s.probeDone(settingsProbeResult{outcomes: []settingsProbeOutcome{{name: s.provider, role: "check", result: infer.ProbeResult{Summary: "Test passed"}}}})
	s.confirm = ""
	s.revert()
	if !strings.Contains(s.probeState(), "Changed since test") {
		t.Fatal("discarded tested session credential retained success")
	}
}
func TestSettingsProbeProgressReportsCompletedRoles(t *testing.T) {
	s := fixtureSettings(t)
	a := &App{theme: NewTheme(true, false)}
	s.probes = []settingsProbePlan{{name: "fixture", role: "check", provider: config.Provider{Model: "first/model"}}, {name: "fixture", role: "triage", provider: config.Provider{Model: "second/model"}}}
	s.probe = &s.probes[0]
	s.probeRunning = true
	s.probeStarted = time.Now()
	s.probeProgress = make(chan settingsProbeOutcome, 2)
	s.probeProgressDone(settingsProbeProgressMsg{screen: s, outcome: settingsProbeOutcome{name: "fixture", role: "check", result: infer.ProbeResult{Summary: "Test passed"}}})
	text := ansi.Strip(strings.Join(s.view(a, 100, 26), "\n"))
	if !strings.Contains(text, "1 of 2 roles complete") || !strings.Contains(text, "check / fixture: Test passed") || !strings.Contains(text, "Testing · triage / fixture") {
		t.Fatal(text)
	}
	if s.probes[1].provider.Model != "second/model" {
		t.Fatal("progress mutated the frozen snapshot")
	}
}
func TestSettingsProbeReadsEnvFile(t *testing.T) {
	const secret = "sk-tui-envfile-sentinel-77"
	home := t.TempDir()
	envPath := filepath.Join(home, "keys.env")
	require.NoError(t, os.WriteFile(envPath, []byte("FIXTURE_API_KEY="+secret+"\n"), 0o600))
	env := map[string]string{"HOME": home}
	a := &App{theme: NewTheme(true, false), wf: &workflow.Workflow{Getenv: func(k string) string { return env[k] }}}

	s := fixtureSettings(t)
	s.user.Set("~/keys.env", "env_file")
	s.openTest(a)
	require.NotNil(t, s.probe, s.notice)
	assert.Equal(t, secret, s.probe.getenv("FIXTURE_API_KEY"), "the probe resolves api_key_env through env_file")

	require.NoError(t, os.Chmod(envPath, 0o644))
	s = fixtureSettings(t)
	s.user.Set("~/keys.env", "env_file")
	s.openTest(a)
	assert.Nil(t, s.probe)
	assert.Contains(t, s.notice, "chmod 600")
	assert.NotContains(t, s.notice, secret)
}

func TestSettingsAddGoogleDecisions(t *testing.T) {
	dir := t.TempDir()
	user := config.NewDocument(filepath.Join(dir, "user.toml"), nil, false, userSettingsValidate)
	project := config.NewDocument(filepath.Join(dir, "project.toml"), nil, false, projectSettingsValidate)
	s := newSettingsWithDocuments(user, project)
	a := &App{theme: NewTheme(true, false)}
	s.setup = true
	assert.Contains(t, strings.Join(s.view(a, 100, 30), "\n"), "Google Gemini Decisions")
	for s.setupChoice < setupGemini {
		s.setupKey(key("down"))
	}
	s.setupKey(key("enter"))
	require.False(t, s.setup)
	assert.Equal(t, config.GoogleDecisionsBaseURL, settingsFieldNamed(t, s, "API endpoint").defaultValue)
	secret := settingsFieldNamed(t, s, "Key for this session")
	s.paste("session-fixture")
	assert.Equal(t, "********", s.value(secret))

	s.openSave(false)
	require.Equal(t, "save", s.confirm)
	assert.NotContains(t, strings.Join(s.view(a, 100, 30), "\n"), "session-fixture")
	require.NotNil(t, s.save(a, "User"), s.notice)
	b, err := os.ReadFile(user.Path)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "session-fixture")
	assert.Equal(t, "session-fixture", s.sessionKeys["gemini"])
	cfg, ds := config.Parse(user.Path, b)
	require.Empty(t, ds)
	assert.Empty(t, cfg.Default)
	assert.Nil(t, cfg.Triage)
	require.NotNil(t, cfg.Gate)
	assert.Equal(t, "gemini", cfg.Gate.Provider)
	assert.Equal(t, config.DefaultEscalateClassAt, cfg.Gate.EscalateAt())
	assert.Equal(t, config.Provider{Kind: config.KindDecisions, API: config.DecisionsGoogle,
		Model: config.GeminiDecisionModel, APIKeyEnv: config.GeminiKeyEnv}, cfg.Providers["gemini"])
	s.section = 1
	s.openTest(a)
	require.Equal(t, "test", s.confirm)
	require.Len(t, s.probes, 1)
	assert.Equal(t, "gate", s.probes[0].role)
	assert.Equal(t, "gemini", s.probes[0].name)
	assert.Contains(t, strings.Join(s.view(a, 120, 30), "\n"), config.GoogleDecisionsBaseURL)
}

func TestSettingsGoogleDecisionsReplacesOnlyGate(t *testing.T) {
	s := fixtureSettings(t)
	s.user.Set("fixture", "triage", "provider")
	s.user.Set(map[string]any{"kind": config.KindDecisions, "model": "existing"}, "providers", "gemini")
	s.user.Set(map[string]any{"provider": "gemini", "escalate_class_at": 0.6, "skip_material_below": 0.2}, "gate")
	s.addPreset(setupGemini)
	cfg, _ := stagedConfig(t, s)
	assert.Equal(t, "fixture", cfg.Default)
	assert.Equal(t, "fixture", cfg.Triage.Provider)
	assert.Equal(t, "existing", cfg.Providers["gemini"].Model)
	assert.NotEqual(t, "gemini", cfg.Gate.Provider)
	assert.Equal(t, config.DecisionsGoogle, cfg.Providers[cfg.Gate.Provider].API)
	assert.Equal(t, 0.6, cfg.Gate.EscalateClassAt)
	assert.Equal(t, 0.2, cfg.Gate.SkipMaterialBelow)
}
