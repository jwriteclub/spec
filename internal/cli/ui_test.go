// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/tui"
)

// runUI runs `spec ui args` in dir with a fake terminal UI that records
// where the app opened.
func runUI(t *testing.T, dir string, args ...string) (result, *tui.App) {
	t.Helper()
	return runUIEnv(t, isolatedEnv(t), dir, args...)
}

// runUIEnv is runUI with the environment te.
func runUIEnv(t *testing.T, te testEnv, dir string, args ...string) (result, *tui.App) {
	t.Helper()
	var out, errb bytes.Buffer
	var opened *tui.App
	env := Env{Stdout: &out, Stderr: &errb, Cwd: dir, Getenv: te.get,
		runUI: func(a *tui.App) error {
			opened = a
			// The UI lock is held while the UI runs.
			_, err := project.AcquireUILock(a.Workspace().Root, "test")
			var held *project.LockHeldError
			if !errors.As(err, &held) {
				return errors.New("the UI lock is not held while the UI runs")
			}
			return nil
		}}
	code := Run(append([]string{"ui"}, args...), env)
	return result{code: code, stdout: out.String(), stderr: errb.String()}, opened
}

func TestUIOpensTargets(t *testing.T) {
	dir := lensdProject(t)
	src := readFile(t, dir, "lensd.spec.md")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "# lensd specification"},
		{[]string{"lensd.spec.md"}, "# lensd specification"},
		{[]string{"lensd.spec.md:17"}, "## Context"},
		{[]string{"Time.Elapsed"}, "**Def $Time.Elapsed"},
		{[]string{"§0.2"}, "### 0.2 Intervals"},
		{[]string{"#1"}, "## 1. Frames"},
		{[]string{"lensd:1"}, "## 1. Frames"},
	} {
		r, a := runUI(t, dir, tc.args...)
		require.Equal(t, 0, r.code, "%v: %s", tc.args, r.stderr)
		require.NotNil(t, a, "%v", tc.args)
		assert.Equal(t, "lensd.spec.md", a.Current().Ed.Path())
		assert.Equal(t, strings.Index(src, tc.want), a.Current().Ed.Cursor(), "%v", tc.args)
	}

	// From a subdirectory, a file path is relative to the working directory.
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	r, a := runUI(t, sub, "../lensd.spec.md:17")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Equal(t, strings.Index(src, "## Context"), a.Current().Ed.Cursor())
}

// TestUIPlainUnderline: in Terminal.app (TERM_PROGRAM=Apple_Terminal),
// which reads an underline colour as blink, the UI underlines plainly.
func TestUIPlainUnderline(t *testing.T) {
	dir := lensdProject(t)
	for term, want := range map[string]bool{"Apple_Terminal": true, "iTerm.app": false, "": false} {
		te := isolatedEnv(t)
		te["TERM_PROGRAM"] = term
		r, a := runUIEnv(t, te, dir)
		require.Equal(t, 0, r.code, r.stderr)
		require.NotNil(t, a)
		assert.Equal(t, want, a.Theme().PlainUnderline, "TERM_PROGRAM=%q", term)
	}
	// The user config's underline_color overrides the guess.
	for _, tc := range []struct {
		term, setting string
		want          bool
	}{{"Apple_Terminal", "on", false}, {"iTerm.app", "off", true}, {"Apple_Terminal", "auto", true}} {
		te := isolatedEnv(t)
		te["TERM_PROGRAM"] = tc.term
		cfg := filepath.Join(te["XDG_CONFIG_HOME"], "spec", "config.toml")
		require.NoError(t, os.MkdirAll(filepath.Dir(cfg), 0o755))
		require.NoError(t, os.WriteFile(cfg, []byte("underline_color = \""+tc.setting+"\"\n"), 0o644))
		r, a := runUIEnv(t, te, dir)
		require.Equal(t, 0, r.code, r.stderr)
		assert.Equal(t, tc.want, a.Theme().PlainUnderline, "%s with underline_color = %q", tc.term, tc.setting)
	}
}

func TestUIErrors(t *testing.T) {
	dir := lensdProject(t)
	for _, tc := range []struct {
		args []string
		msg  string
	}{
		{[]string{"Nope.Missing"}, "no anchor named Nope.Missing"},
		{[]string{"§42"}, "no section numbered 42"},
		{[]string{"missing.spec.md"}, "no file missing.spec.md"},
		{[]string{".Timing"}, "did your shell expand"},
		{[]string{"a", "b"}, "too many arguments"},
	} {
		r, a := runUI(t, dir, tc.args...)
		assert.Equal(t, 1, r.code, "%v", tc.args)
		assert.Nil(t, a)
		assert.Contains(t, r.stderr, tc.msg, "%v", tc.args)
	}
	r := run(t, dir, "ui", "--json")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stdout, "interactive")

	// Without a terminal, spec ui refuses to start.
	r = run(t, dir, "ui")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "needs a terminal")
}

// TestUISingleInstance: a second spec ui exits 5 and names the holder.
func TestUISingleInstance(t *testing.T) {
	dir := lensdProject(t)
	lock, err := project.AcquireUILock(dir, "spec ui")
	require.NoError(t, err)
	defer func() { _ = lock.Release() }()
	r, a := runUI(t, dir)
	assert.Equal(t, 5, r.code)
	assert.Nil(t, a)
	assert.Contains(t, r.stderr, "another spec ui is open on this project")
	assert.Contains(t, r.stderr, "pid ")
	assert.Contains(t, r.stderr, project.UILockFile)

	// The writer lock is separate: agents can write while the UI is open.
	w, err := project.AcquireLock(dir, "spec rename")
	require.NoError(t, err)
	require.NoError(t, w.Release())
}

// TestUIRecord: --record DIR writes the snapshot, the events and the
// canned replies, documents itself in the help, and refuses a directory
// that holds anything.
func TestUIRecord(t *testing.T) {
	dir := lensdProject(t)
	rec := filepath.Join(t.TempDir(), "rec")
	r, a := runUI(t, dir, "--record", rec)
	require.Equal(t, 0, r.code, r.stderr)
	require.NotNil(t, a)
	assert.Contains(t, r.stderr, "recorded to "+rec+" (")
	for _, f := range []string{"meta.json", "workspace.tar", "session.jsonl", "canned.toml", "requests.jsonl"} {
		_, err := os.Stat(filepath.Join(rec, f))
		assert.NoError(t, err, f)
	}
	meta := readFile(t, rec, "meta.json")
	assert.Contains(t, meta, `"version": 1`)
	assert.Contains(t, meta, `"ids_seed": "`)

	r, _ = runUI(t, dir, "--record", rec)
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "is not empty")

	var out bytes.Buffer
	Run([]string{"ui", "--help"}, Env{Stdout: &out, Stderr: &out, Cwd: dir})
	assert.Contains(t, out.String(), "--record DIR")
	assert.Contains(t, out.String(), ".dev/recordings/NAME")
}
