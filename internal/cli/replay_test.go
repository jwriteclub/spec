// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/recording"
	"github.com/jwriteclub/spec/internal/tui"
)

// recordLensd records a (fake-UI) session of the lensd project opened at
// $Time.Elapsed and returns the project and the recording.
func recordLensd(t *testing.T) (string, string) {
	t.Helper()
	dir := lensdProject(t)
	rec := filepath.Join(t.TempDir(), "rec")
	r, _ := runUI(t, dir, "--record", rec, "Time.Elapsed")
	require.Equal(t, 0, r.code, r.stderr)
	return dir, rec
}

// TestUIReplay: --replay DIR opens the UI on a restored copy of the
// recorded project, where the recording opened, and removes the copy
// afterwards unless --keep.
func TestUIReplay(t *testing.T) {
	dir, rec := recordLensd(t)
	cwd := t.TempDir()
	r, a := runUI(t, cwd, "--replay", rec)
	require.Equal(t, 0, r.code, r.stderr)
	require.NotNil(t, a)
	root := a.Workspace().Root
	assert.NotEqual(t, dir, root)
	assert.Equal(t, readFile(t, dir, "lensd.spec.md"), string(a.Current().Ed.Buffer().Bytes()))
	src := string(a.Current().Ed.Buffer().Bytes())
	assert.Equal(t, strings.Index(src, "**Def $Time.Elapsed"), a.Current().Ed.Cursor(), "opened at the recorded target")
	assert.Empty(t, r.stderr)
	assert.Empty(t, readFile(t, rec, "replay.log"), "nothing diverged")
	_, err := os.Stat(root)
	assert.True(t, os.IsNotExist(err), "the restored project is removed")

	r, a = runUI(t, cwd, "--replay", rec, "--keep")
	require.Equal(t, 0, r.code, r.stderr)
	root = a.Workspace().Root
	assert.Contains(t, r.stderr, "the replayed project is in "+root)
	assert.FileExists(t, filepath.Join(root, "lensd.spec.md"))
	require.NoError(t, os.RemoveAll(filepath.Dir(root)))

	r, _ = runUI(t, cwd, "--replay", rec, "lensd.spec.md")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "--replay takes no FILE")
	r, _ = runUI(t, dir, "--keep")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "go with --replay")
	r, _ = runUI(t, cwd, "--replay", filepath.Join(cwd, "nothing"))
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "cannot replay")
}

// TestUIReplayAppearance: the replay looks like the recording: the recorded
// config's colour scheme and glyphs, and the terminal's colour depth
// (TERM without a truecolor COLORTERM: 256 colours), not the fixed default.
func TestUIReplayAppearance(t *testing.T) {
	dir := lensdProject(t)
	te := isolatedEnv(t)
	te["TERM"] = "xterm-256color"
	cfg := filepath.Join(te["XDG_CONFIG_HOME"], "spec", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfg), 0o755))
	require.NoError(t, os.WriteFile(cfg, []byte("color_scheme = \"synthwave-quiet\"\nglyph_profile = \"ascii\"\n"), 0o644))
	rec := filepath.Join(t.TempDir(), "rec")
	r, recorded := runUIEnv(t, te, dir, "--record", rec, "Time.Elapsed")
	require.Equal(t, 0, r.code, r.stderr)
	want := recorded.Theme()
	require.True(t, want.Reduced, "TERM without COLORTERM=truecolor")
	require.True(t, want.ASCII)

	r, replayed := runUIEnv(t, te, t.TempDir(), "--replay", rec)
	require.Equal(t, 0, r.code, r.stderr)
	got := replayed.Theme()
	assert.Equal(t, want.Dark, got.Dark)
	assert.Equal(t, want.Reduced, got.Reduced)
	assert.Equal(t, want.ASCII, got.ASCII)
	assert.Equal(t, want.PlainUnderline, got.PlainUnderline)
	for _, role := range []string{"ink", "human", "agent", "control", "error"} {
		assert.Equal(t, want.Token(role), got.Token(role), role)
	}
	assert.NotEqual(t, tui.NewThemeProfile(want.Dark, want.PlainUnderline, true, true).Token("human"), got.Token("human"),
		"the recorded colour scheme, not the default")
}

// TestReplayTape: spec replay DIR writes the tape that launches the
// replay, beside the recording or at -o.
func TestReplayTape(t *testing.T) {
	_, rec := recordLensd(t)
	cwd := t.TempDir()
	r := run(t, cwd, "replay", rec)
	require.Equal(t, 0, r.code, r.stderr)
	tape := readFile(t, rec, "replay.tape")
	assert.Contains(t, r.stdout, "wrote "+filepath.Join(rec, "replay.tape"))
	assert.Contains(t, tape, " ui --replay '"+rec+"'")
	assert.Contains(t, tape, `Output "`+filepath.Join(rec, "replay.mp4")+`"`)
	loaded, err := recording.Load(rec)
	require.NoError(t, err)
	fs := recording.FitFontSize(loaded.Meta.Cols, loaded.Meta.Rows, recording.DefaultWidth, recording.DefaultHeight, recording.DefaultPadding)
	assert.Contains(t, tape, fmt.Sprintf("Set FontSize %d\n", fs))
	assert.Contains(t, tape, "Set Width 3840\nSet Height 2160\nSet Framerate 30\n")

	r = run(t, cwd, "replay", rec, "-o", "x.tape", "--video", "x.gif", "--font-size", "20", "--width", "1600", "--height", "900", "--framerate", "60")
	require.Equal(t, 0, r.code, r.stderr)
	tape = readFile(t, cwd, "x.tape")
	assert.Contains(t, tape, `Output "`+filepath.Join(cwd, "x.gif")+`"`)
	assert.Contains(t, tape, "Set FontSize 20\nSet LineHeight 1.2\nSet Padding 10\nSet Width 1600\nSet Height 900\nSet Framerate 60\n")

	r = run(t, cwd, "replay", rec, "-o", "nopad.tape", "--padding", "0")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, readFile(t, cwd, "nopad.tape"), "Set Padding 0\n", "--padding 0 means none")

	r = run(t, cwd, "replay", filepath.Join(cwd, "nothing"))
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "cannot read the recording")
	r = run(t, cwd, "replay")
	assert.Equal(t, 1, r.code)
}

// plantLink makes rec/name a link to a file outside the recording and returns
// that file, whose bytes must survive.
func plantLink(t *testing.T, rec, name string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(target, []byte("mine\n"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(rec, name)))
	require.NoError(t, os.Symlink(target, filepath.Join(rec, name)))
	return target
}

// TestReplayNeverWritesThroughALink: what spec ui --replay and spec replay write
// into a recording replaces a link there, or refuses, and never follows it out.
func TestReplayNeverWritesThroughALink(t *testing.T) {
	_, rec := recordLensd(t)
	cwd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(rec, recording.ReplayLog), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(rec, recording.ReplayFinal), nil, 0o644))
	log, final := plantLink(t, rec, recording.ReplayLog), plantLink(t, rec, recording.ReplayFinal)
	r, _ := runUI(t, cwd, "--replay", rec)
	require.Equal(t, 0, r.code, r.stderr)
	assert.Equal(t, "mine\n", readFile(t, filepath.Dir(log), "victim"), "replay.log")
	assert.Equal(t, "mine\n", readFile(t, filepath.Dir(final), "victim"), "the final frame")

	require.NoError(t, os.WriteFile(filepath.Join(rec, "replay.tape"), nil, 0o644))
	tape := plantLink(t, rec, "replay.tape")
	r = run(t, cwd, "replay", rec)
	require.Equal(t, 0, r.code, r.stderr)
	assert.Equal(t, "mine\n", readFile(t, filepath.Dir(tape), "victim"), "replay.tape")
	st, err := os.Lstat(filepath.Join(rec, "replay.tape"))
	require.NoError(t, err)
	assert.True(t, st.Mode().IsRegular())

	// VHS would write the video through a link: refused, as it runs later.
	require.NoError(t, os.WriteFile(filepath.Join(rec, "replay.mp4"), nil, 0o644))
	plantLink(t, rec, "replay.mp4")
	r = run(t, cwd, "replay", rec)
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "symbolic link")
	r = run(t, cwd, "replay", rec, "--video", filepath.Join(cwd, "v.mp4"))
	require.Equal(t, 0, r.code, r.stderr)
}

// TestReplayRefusesControlCharactersInDir: the tape types DIR into a shell, where
// a control character is a key press.
func TestReplayRefusesControlCharactersInDir(t *testing.T) {
	_, rec := recordLensd(t)
	bad := filepath.Join(filepath.Dir(rec), "a\x03touch b\n")
	require.NoError(t, os.Rename(rec, bad))
	r := run(t, t.TempDir(), "replay", bad)
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "control character")
}
