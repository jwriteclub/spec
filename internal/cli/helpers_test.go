// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/jwriteclub/spec/internal/golden"
)

// TestMain isolates git from the machine's configuration (plan section 9).
func TestMain(m *testing.M) {
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME":     "Spec Test",
		"GIT_AUTHOR_EMAIL":    "spec@example.com",
		"GIT_COMMITTER_NAME":  "Spec Test",
		"GIT_COMMITTER_EMAIL": "spec@example.com",
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
	} {
		os.Setenv(k, v)
	}
	os.Exit(m.Run())
}

// result is one Run.
type result struct {
	code   int
	stdout string
	stderr string
}

// testEnv is the environment variables a Run sees; tests override entries.
type testEnv map[string]string

func (e testEnv) get(k string) string {
	if v, ok := e[k]; ok {
		return v
	}
	return os.Getenv(k)
}

// isolatedEnv points HOME and XDG_CONFIG_HOME at an empty directory, so the
// developer's own user config never leaks into a test.
func isolatedEnv(t *testing.T) testEnv {
	t.Helper()
	home := t.TempDir()
	return testEnv{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config")}
}

// runIn runs spec with args in dir with the given environment.
func runIn(t *testing.T, env testEnv, dir string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, Env{Stdout: &out, Stderr: &errb, Cwd: dir, Getenv: env.get})
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

// run is runIn with an isolated environment.
func run(t *testing.T, dir string, args ...string) result {
	t.Helper()
	return runIn(t, isolatedEnv(t), dir, args...)
}

// jsonResult is a decoded --json run.
type jsonResult struct {
	result
	ok          bool
	data        json.RawMessage
	errCode     string
	errMessage  string
	diagnostics []map[string]any
	raw         map[string]json.RawMessage
}

// runJSON runs spec with --json and checks the envelope contract: stderr
// empty, stdout exactly one JSON object with the five keys, ok matching the
// exit code, and error null exactly on success.
func runJSON(t *testing.T, dir string, args ...string) jsonResult {
	t.Helper()
	return runJSONEnv(t, isolatedEnv(t), dir, args...)
}

func runJSONEnv(t *testing.T, env testEnv, dir string, args ...string) jsonResult {
	t.Helper()
	r := runIn(t, env, dir, append(append([]string{}, args...), "--json")...)
	return decodeEnvelope(t, r, args)
}

func decodeEnvelope(t *testing.T, r result, args []string) jsonResult {
	t.Helper()
	require.Empty(t, r.stderr, "--json writes nothing to stderr: spec %v", args)
	require.True(t, strings.HasSuffix(r.stdout, "\n"), "one object and a newline: %q", r.stdout)
	dec := json.NewDecoder(strings.NewReader(r.stdout))
	var raw map[string]json.RawMessage
	require.NoError(t, dec.Decode(&raw), "stdout is a JSON object: spec %v: %q", args, r.stdout)
	var extra json.RawMessage
	assert.Error(t, dec.Decode(&extra), "exactly one JSON value on stdout")

	keys := []string{}
	for k := range raw {
		keys = append(keys, k)
	}
	require.ElementsMatch(t, []string{"schema", "ok", "data", "error", "diagnostics"}, keys)

	jr := jsonResult{result: r, raw: raw, data: raw["data"]}
	var schema string
	require.NoError(t, json.Unmarshal(raw["schema"], &schema))
	assert.Equal(t, "spec/v1", schema)
	require.NoError(t, json.Unmarshal(raw["ok"], &jr.ok))
	assert.Equal(t, r.code == 0, jr.ok, "ok is true exactly when the exit code is 0")
	var e *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(raw["error"], &e))
	if r.code == 0 {
		assert.Nil(t, e, "error is null on success")
	} else if assert.NotNil(t, e, "error is set on failure (exit %d): %s", r.code, r.stdout) {
		jr.errCode, jr.errMessage = e.Code, e.Message
		assert.NotEmpty(t, e.Code)
		assert.NotEmpty(t, e.Message)
	}
	require.True(t, bytes.HasPrefix(raw["diagnostics"], []byte("[")), "diagnostics is an array: %s", raw["diagnostics"])
	require.NoError(t, json.Unmarshal(raw["diagnostics"], &jr.diagnostics))
	assertNoStrayNulls(t, args, r.stdout)
	return jr
}

// nullableKeys are the only JSON keys that may hold null.
var nullableKeys = map[string]bool{
	"data": true, "error": true, "section": true, "renamed_from": true, "old": true, "new": true,
	"change": true, "within": true, "baseline": true, "message_file": true, "topic": true,
	"inference": true, "primary": true, "placement": true, "decision": true, "proposal": true, "fix": true,
}

// assertNoStrayNulls fails when an array or string is null: arrays are never
// null and absent strings are "".
func assertNoStrayNulls(t *testing.T, args []string, stdout string) {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal([]byte(stdout), &v))
	var walk func(path string, key string, v any)
	walk = func(path, key string, v any) {
		switch x := v.(type) {
		case nil:
			assert.True(t, nullableKeys[key], "spec %v: %s is null but %q may not be", args, path, key)
		case map[string]any:
			for k, c := range x {
				walk(path+"."+k, k, c)
			}
		case []any:
			for i, c := range x {
				walk(path+"["+strconvItoa(i)+"]", key, c)
			}
		}
	}
	walk("$", "", v)
}

func strconvItoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// decode unmarshals the result's data into out.
func (jr jsonResult) decode(t *testing.T, out any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(jr.data, out), "data: %s", jr.data)
}

// write creates dir/rel with content.
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(b)
}

// copyDir copies a directory tree.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	}))
}

// requireGit skips the test when git is not installed.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// git runs git in dir with the test configuration and returns its output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

// gitInit makes dir a repository on branch main.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	requireGit(t)
	git(t, dir, "init", "-q")
}

// commitAll stages everything and commits.
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", msg)
}

// repoWith creates a repository holding files (path to content), with one
// commit.
func repoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir)
	for p, c := range files {
		write(t, dir, p, c)
	}
	commitAll(t, dir, "base")
	return dir
}

// execCommand builds an exec.Cmd running in dir.
func execCommand(t *testing.T, dir, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd
}
