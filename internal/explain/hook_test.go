// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package explain

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commitMsgHook returns the commit-msg hook script of the integrations topic.
func commitMsgHook(t *testing.T) string {
	t.Helper()
	text, ok := Topic("integrations")
	require.True(t, ok)
	start := strings.Index(text, "    #!/bin/sh\n")
	require.GreaterOrEqual(t, start, 0, "integrations shows the hook as an indented block")
	var lines []string
	for _, l := range strings.SplitAfter(text[start:], "\n") {
		if !strings.HasPrefix(l, "    ") {
			break
		}
		lines = append(lines, strings.TrimPrefix(l, "    "))
	}
	return strings.Join(lines, "")
}

// TestCommitMsgHookSkipsCheckoutsWithoutSpec installs the documented hook and
// commits with a stand-in spec that always refuses: a checkout with no
// spec.toml commits anyway (the hook is shared by every worktree), one with
// spec.toml runs spec check.
func TestCommitMsgHookSkipsCheckoutsWithoutSpec(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "spec"), []byte("#!/bin/sh\necho refused >&2\nexit 4\n"), 0o755))

	repo := t.TempDir()
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	_, err := git("init", "-q")
	require.NoError(t, err)
	hooks := filepath.Join(repo, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooks, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "commit-msg"), []byte(commitMsgHook(t)), 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644))
	_, err = git("add", "main.go")
	require.NoError(t, err)
	out, err := git("commit", "-q", "-m", "no spec project here")
	assert.NoError(t, err, "no spec.toml: the hook lets the commit through: %s", out)

	require.NoError(t, os.WriteFile(filepath.Join(repo, "spec.toml"), []byte("name = \"x\"\n"), 0o644))
	_, err = git("add", "spec.toml")
	require.NoError(t, err)
	out, err = git("commit", "-q", "-m", "spec project")
	assert.Error(t, err, "spec.toml: the hook runs spec check")
	assert.Contains(t, out, "refused")
}
