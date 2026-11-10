// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestMain points HOME and XDG_CONFIG_HOME at a temp directory for the whole
// test binary, so that no test, parallel or not, can read or write the user's
// real spec config. It covers App, Driver and Workflow alike, and any process
// they start.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "spec-tui-test-home-")
	if err != nil {
		panic(err)
	}
	if err := isolateUserConfig(home); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// isolateUserConfig makes home the process's home and its config directory.
func isolateUserConfig(home string) error {
	if err := os.Setenv("HOME", home); err != nil {
		return err
	}
	// The host terminal must not change rendering (Terminal.app gets a flat status bar).
	if err := os.Unsetenv("TERM_PROGRAM"); err != nil {
		return err
	}
	return os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
}

// Frame size of the golden snapshots (acceptance criterion 3).
const (
	frameW = 100
	frameH = 30
)

// copyDir copies the regular files of src into dst.
func copyDir(t testing.TB, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644))
	}
}

// lensdProject copies testdata/lensd into a temp project.
func lensdProject(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	copyDir(t, "../../testdata/lensd", dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spec.toml"), []byte("name = \"lift\"\n"), 0o644))
	return dir
}

// testOptions are deterministic options: fixed dark theme, parse at once,
// no watcher, no git baseline.
func testOptions(root string) Options {
	return Options{Root: root, HashToSection: true, FixedTheme: true}
}

// newTestApp opens root in an App sized to the golden frame.
func newTestApp(t testing.TB, opts Options) *App {
	t.Helper()
	a, err := New(opts)
	require.NoError(t, err)
	a.Update(tea.WindowSizeMsg{Width: frameW, Height: frameH})
	return a
}

// send feeds messages to the app synchronously, running no commands.
func send(a *App, msgs ...tea.Msg) {
	for _, m := range msgs {
		a.Update(m)
	}
}

// key builds a key press from a Bubble Tea key string such as "ctrl+t",
// "alt+," or "enter".
func key(s string) tea.KeyPressMsg {
	var k tea.Key
	parts := strings.Split(s, "+")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		// "alt++" style: the key itself is '+'.
		parts = append(parts[:len(parts)-2], "+")
	}
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl":
			k.Mod |= tea.ModCtrl
		case "alt":
			k.Mod |= tea.ModAlt
		case "shift":
			k.Mod |= tea.ModShift
		}
	}
	last := parts[len(parts)-1]
	switch last {
	case "enter":
		k.Code = tea.KeyEnter
	case "esc":
		k.Code = tea.KeyEscape
	case "tab":
		k.Code = tea.KeyTab
	case "backspace":
		k.Code = tea.KeyBackspace
	case "delete":
		k.Code = tea.KeyDelete
	case "up":
		k.Code = tea.KeyUp
	case "down":
		k.Code = tea.KeyDown
	case "left":
		k.Code = tea.KeyLeft
	case "right":
		k.Code = tea.KeyRight
	case "home":
		k.Code = tea.KeyHome
	case "end":
		k.Code = tea.KeyEnd
	case "pgup":
		k.Code = tea.KeyPgUp
	case "pgdown":
		k.Code = tea.KeyPgDown
	case "f12":
		k.Code = tea.KeyF12
	case "f1":
		k.Code = tea.KeyF1
	case "f2":
		k.Code = tea.KeyF2
	case "f8":
		k.Code = tea.KeyF8
	case "space":
		k.Code = tea.KeySpace
		if k.Mod == 0 {
			k.Text = " "
		}
	default:
		r := []rune(last)
		k.Code = r[0]
		if k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
			k.Text = last
		}
	}
	return tea.KeyPressMsg(k)
}

// typeText returns key presses that type s.
func typeText(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		switch r {
		case '\n':
			out = append(out, key("enter"))
		case ' ':
			out = append(out, key("space"))
		default:
			out = append(out, tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
		}
	}
	return out
}

// plain returns the frame as plain text, each line padded to the frame
// width (Lip Gloss's canvas trims trailing spaces).
func plain(a *App) string {
	content, _ := a.render()
	lines := strings.Split(ansi.Strip(content), "\n")
	for len(lines) < frameH {
		lines = append(lines, "")
	}
	for k, l := range lines {
		lines[k] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

// styled returns the frame downsampled to the 256-color profile, so the
// golden is stable across terminals.
func styled(t testing.TB, a *App) string {
	t.Helper()
	content, _ := a.render()
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ANSI256}
	_, err := w.WriteString(content)
	require.NoError(t, err)
	return buf.String()
}

// saveBuffers saves every modified buffer, as ^O in each would: what makes
// an approval true.
func saveBuffers(t *testing.T, a *App) {
	t.Helper()
	for _, d := range a.ws.Docs() {
		if d.Ed.Buffer().Dirty() {
			_, err := a.saveDoc(d)
			require.NoError(t, err)
		}
	}
}
