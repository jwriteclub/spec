// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/workflow"
	"github.com/stretchr/testify/require"
)

func TestBuiltinColourSchemesKeepApprovedColoursAndReadableProfiles(t *testing.T) {
	require.Len(t, builtinColorSchemes, 2)
	require.Equal(t, "synthwave", builtinColorSchemes[0].id)
	require.Equal(t, "#f786d6", builtinColorSchemes[0].roles["human"].Dark)
	require.Equal(t, "#ec90d0", builtinColorSchemes[1].roles["human"].Dark)
	for _, s := range builtinColorSchemes {
		require.Len(t, s.roles, 40)
		for name, role := range s.roles {
			require.Regexp(t, colorSchemeHex, role.Dark, name)
			require.Regexp(t, colorSchemeHex, role.Light, name)
			require.True(t, role.Dark256 >= 0 && role.Dark256 <= 255, name)
			require.True(t, role.Light256 >= 0 && role.Light256 <= 255, name)
		}
		for _, dark := range []bool{false, true} {
			for _, reduced := range []bool{false, true} {
				th := newThemeScheme(dark, false, reduced, false, s)
				check := func(fg, bg string) {
					t.Helper()
					require.GreaterOrEqual(t, contrast(th.Token(fg), th.Token(bg)), 4.5, "%s dark=%v reduced=%v %s/%s", s.id, dark, reduced, fg, bg)
				}
				for _, bg := range []string{"card", "selectedCard", "childCard", "selectedChildCard"} {
					for _, fg := range []string{"ink", "secondary", "focus", "human", "agent", "fix", "error", "warning", "positive"} {
						check(fg, bg)
					}
				}
				for _, fg := range []string{"ink", "secondary", "focus"} {
					check(fg, "counterHover")
				}
				for _, fg := range []string{"statusInk", "statusHuman", "statusAgent", "statusFix", "statusError", "statusWarning", "statusPositive"} {
					check(fg, "status")
				}
				check("onPositiveKey", "positiveKey")
				check("ink", "keycap")
				check("focus", "note")
				for _, pair := range [][2]string{{"warning", "warningCount"}, {"error", "warningCount"}, {"agent", "proposalCount"}, {"human", "otherCount"}} {
					if dark && reduced {
						pair[0] = "focus"
					}
					check(pair[0], pair[1])
				}
			}
		}
	}
}

func TestCustomColourSchemeInheritanceAndIndexedOverrides(t *testing.T) {
	s, err := decodeColorScheme([]byte(`{"version":1,"name":"My colours","base":"synthwave-quiet","roles":{"human":{"dark":"#123456","light256":0},"agent":{"dark256":86}}}`), "custom:mine", "mine")
	require.NoError(t, err)
	require.Equal(t, "My colours", s.name)
	require.Equal(t, "#123456", s.roles["human"].Dark)
	require.Equal(t, 0, s.roles["human"].Light256, "zero is a real index, not an omitted field")
	require.Equal(t, nearestScheme256("#123456"), s.roles["human"].Dark256)
	require.Equal(t, builtinColorSchemes[1].roles["fix"], s.roles["fix"])
	require.Equal(t, "#ec90d0", builtinColorSchemes[1].roles["human"].Dark, "customisation must not mutate a built-in")
	require.Equal(t, 16, nearestScheme256("#000000"))
	require.Equal(t, 231, nearestScheme256("#ffffff"))
}

func TestCustomColourSchemeRejectsMalformedDefinitions(t *testing.T) {
	for _, src := range []string{
		`{`, `null`, `{"version":2,"roles":{"ink":{"dark":"#123456"}}}`,
		`{"version":1,"base":"custom:cycle","roles":{"ink":{"dark":"#123456"}}}`,
		`{"version":1,"roles":{"typo":{"dark":"#123456"}}}`,
		`{"version":1,"roles":{"ink":{"dark":"red"}}}`,
		`{"version":1,"roles":{"ink":{"light":"#12"}}}`,
		`{"version":1,"roles":{"ink":{"dark256":256}}}`,
		`{"version":1,"roles":{"ink":{"light256":-1}}}`,
		`{"version":1,"roles":{"ink":{"drak":"#123456"}}}`,
		`{"version":1,"roles":{"ink":{}}}`,
		`{"version":1,"roles":{}}`,
		`{"version":1,"name":"bad\u001b[2J","roles":{"ink":{"dark":"#123456"}}}`,
		`{"version":1,"roles":{"ink":{"dark":"#123456"}}} {}`,
	} {
		t.Run(src, func(t *testing.T) {
			_, err := decodeColorScheme([]byte(src), "custom:bad", "bad")
			require.Error(t, err)
		})
	}
}

func writeSchemeFixture(t *testing.T, path, contents string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
}

func TestCustomColourSchemeDiscoveryRequiresExplicitPersonalFolder(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "spec", "config.toml")
	dir := filepath.Join(filepath.Dir(path), "colours")
	writeSchemeFixture(t, filepath.Join(dir, "mine.json"), `{"version":1,"name":"Mine","roles":{"human":{"dark":"#f080c0"}}}`)
	writeSchemeFixture(t, filepath.Join(dir, "broken.json"), `{"version":1,"roles":{"oops":{"dark":"red"}}}`)
	writeSchemeFixture(t, filepath.Join(dir, "ignore.txt"), "not a scheme")
	writeSchemeFixture(t, filepath.Join(dir, "nested", "hidden.json"), `{"version":1,"roles":{"ink":{"dark":"#123456"}}}`)
	noOptIn := loadColorSchemes("", path, home)
	require.Len(t, noOptIn.schemes, 2)
	require.Empty(t, noOptIn.problems)
	for _, configured := range []string{"colours", dir, "~/.config/spec/colours"} {
		c := loadColorSchemes(configured, path, home)
		require.Len(t, c.schemes, 3)
		found, err := c.find("custom:mine")
		require.NoError(t, err)
		require.Equal(t, "Mine", found.name)
		require.Len(t, c.problems, 1)
		_, err = c.find("custom:broken")
		require.ErrorContains(t, err, "unknown colour role")
	}
	writeSchemeFixture(t, filepath.Join(dir, "bad\x1b[2J.json"), `{}`)
	for _, problem := range loadColorSchemes("colours", path, home).problems {
		require.NotContains(t, problem, "\x1b", "invalid filenames must not inject terminal controls into Settings")
	}
	writeSchemeFixture(t, filepath.Join(dir, "huge.json"), strings.Repeat(" ", (1<<20)+1))
	_, err := readColorScheme(filepath.Join(dir, "huge.json"), "custom:huge", "huge")
	require.ErrorContains(t, err, "1 MiB")
	c := loadColorSchemes("missing", path, home)
	require.Len(t, c.schemes, 2)
	require.NotEmpty(t, c.problems)
}

func TestColourSchemePickerPersistsAndReloadsWithoutWritingBuiltins(t *testing.T) {
	s := fixtureSettings(t)
	writeSchemeFixture(t, s.user.Path, settingsFixture)
	s.user = config.OpenDocument(s.user.Path, userSettingsValidate)
	s.section = 2
	f := settingsFieldNamed(t, s, "Colour scheme")
	require.Equal(t, []string{"Use default", "synthwave", "synthwave-quiet"}, f.choices)
	a := newTestApp(t, testOptions(lensdProject(t)))
	s.activate(a, f)
	s.key(a, key("down"), Binding{}, false)
	s.key(a, key("down"), Binding{}, false)
	s.key(a, key("enter"), Binding{}, false)
	v, _ := s.user.Get("color_scheme")
	require.Equal(t, "synthwave-quiet", v)
	require.Equal(t, defaultColorScheme, a.theme.scheme.id, "the picker stages until Save")
	cmd := s.save(a, "User")
	require.NotNil(t, cmd, s.notice)
	loaded, err := config.Load(s.user.Path)
	require.NoError(t, err)
	require.Empty(t, loaded.Diagnostics)
	require.Equal(t, "synthwave-quiet", loaded.Config.ColorScheme)
	require.NoError(t, a.applyAppearance(loaded.Config))
	require.Equal(t, "synthwave-quiet", a.theme.scheme.id)
	files, err := os.ReadDir(filepath.Dir(s.user.Path))
	require.NoError(t, err)
	require.Len(t, files, 1, "only User config is written; palettes stay embedded")
	s.activate(a, f)
	require.Equal(t, 2, s.choice, "reopening selects the existing value")
	s.key(a, key("esc"), Binding{}, false)
}

func TestCustomSchemePickerRefreshAndMissingSelectionBlocksSave(t *testing.T) {
	s := fixtureSettings(t)
	s.section = 2
	s.user.Set("colours", "color_scheme_dir")
	f := settingsFieldNamed(t, s, "Colour scheme")
	require.Len(t, f.choices, 3)
	path := filepath.Join(filepath.Dir(s.user.Path), "colours", "fresh.json")
	writeSchemeFixture(t, path, `{"version":1,"name":"Fresh","roles":{"human":{"dark":"#ff00ff"}}}`)
	a := newTestApp(t, testOptions(lensdProject(t)))
	s.activate(a, f)
	f = *s.current()
	require.Contains(t, f.choices, "custom:fresh")
	s.selector = false
	s.edit(f, "custom:fresh")
	require.True(t, s.stage(f))
	require.NoError(t, os.Remove(path))
	require.Nil(t, s.save(a, "User"))
	require.Contains(t, s.notice, "unavailable")
	_, err := os.Stat(s.user.Path)
	require.True(t, os.IsNotExist(err), "invalid selection must not write config")
}

func TestColourSchemeSurvivesTerminalDetectionAndBadCustomFallback(t *testing.T) {
	home := t.TempDir()
	configPath := config.PathFor("", home)
	writeSchemeFixture(t, configPath, "color_scheme='synthwave-quiet'\n")
	a := newTestApp(t, testOptions(lensdProject(t)))
	a.opts.FixedTheme = false
	a.wf = &workflow.Workflow{Getenv: func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}}
	require.NoError(t, a.applyAppearance(config.Config{ColorScheme: "synthwave-quiet"}))
	a.Update(tea.BackgroundColorMsg{Color: color.White})
	require.False(t, a.theme.Dark)
	require.Equal(t, "synthwave-quiet", a.theme.scheme.id)
	err := a.applyAppearance(config.Config{ColorScheme: "custom:missing", ColorSchemeDir: "colours"})
	require.Error(t, err)
	require.Equal(t, defaultColorScheme, a.theme.scheme.id)
	require.Contains(t, a.flash, "unavailable")
	require.Equal(t, statusFailure, a.flashKind)
}

func TestLongCustomSchemePickerKeepsSelectedOptionVisible(t *testing.T) {
	s := fixtureSettings(t)
	s.section = 2
	s.user.Set("colours", "color_scheme_dir")
	for i := range 28 {
		writeSchemeFixture(t, filepath.Join(filepath.Dir(s.user.Path), "colours", fmt.Sprintf("scheme-%02d.json", i)), fmt.Sprintf(`{"version":1,"name":"Scheme %02d","roles":{"ink":{"dark":"#abcdef"}}}`, i))
	}
	f := settingsFieldNamed(t, s, "Colour scheme")
	a := &App{theme: NewTheme(true, false)}
	s.activate(a, f)
	for range 30 {
		s.key(a, key("down"), Binding{}, false)
	}
	view := strings.Join(s.view(a, 80, 21), "\n")
	require.Contains(t, ansi.Strip(view), "Scheme 27")
	for _, line := range strings.Split(view, "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), 80)
	}
}
