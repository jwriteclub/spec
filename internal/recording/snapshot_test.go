// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/baseline"
)

var testGitEnv = []string{
	"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
	"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
	"GIT_AUTHOR_DATE=2026-11-01T12:00:00Z", "GIT_COMMITTER_DATE=2026-11-01T12:00:00Z",
	"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), testGitEnv...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func writeIn(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
}

func keysOf(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The snapshot holds the working tree (tracked, untracked not ignored,
// .spec without locks and log), HEAD's versions, and only the staged
// paths in index.tar.
func TestSnapshotGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	gitIn(t, root, "init", "-q", "-b", "main")
	writeIn(t, root, ".gitignore", "ignored.md\n.spec/\n.dev/\n")
	writeIn(t, root, "a.spec.md", "# A\n\nhead a\n")
	writeIn(t, root, "b.spec.md", "# B\n\nhead b\n")
	writeIn(t, root, "gone.md", "deleted later\n")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "init")
	writeIn(t, root, "a.spec.md", "# A\n\nstaged a\n")
	gitIn(t, root, "add", "a.spec.md")
	writeIn(t, root, "a.spec.md", "# A\n\nworking a\n")
	require.NoError(t, os.Remove(filepath.Join(root, "gone.md")))
	writeIn(t, root, "new.md", "untracked\n")
	writeIn(t, root, "ignored.md", "ignored\n")
	writeIn(t, root, ".spec/ui.lock", "pid")
	writeIn(t, root, ".spec/lock", "pid")
	writeIn(t, root, ".spec/ui.log", "log")
	writeIn(t, root, ".spec/proposals/p1.json", "{}\n")
	writeIn(t, root, ".spec/proposals/.tmp-123.json", "{")
	writeIn(t, root, ".dev/play/x.md", "dev")
	dir := filepath.Join(root, ".dev", "recordings", "r1")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	info, err := Snapshot(context.Background(), baseline.Loader{Env: testGitEnv}, root, dir)
	require.NoError(t, err)
	assert.True(t, info.Git)
	assert.Len(t, info.Head, 40)

	ws, err := ReadTar(filepath.Join(dir, WorkspaceTar))
	require.NoError(t, err)
	assert.Equal(t, []string{".gitignore", ".spec/proposals/p1.json", "a.spec.md", "b.spec.md", "new.md"}, keysOf(ws))
	assert.Equal(t, "# A\n\nworking a\n", string(ws["a.spec.md"]))
	assert.Equal(t, info.Paths, keysOf(ws))

	head, err := ReadTar(filepath.Join(dir, HeadTar))
	require.NoError(t, err)
	assert.Equal(t, []string{".gitignore", "a.spec.md", "b.spec.md", "gone.md"}, keysOf(head))
	assert.Equal(t, "# A\n\nhead a\n", string(head["a.spec.md"]))

	idx, err := ReadTar(filepath.Join(dir, IndexTar))
	require.NoError(t, err)
	assert.Equal(t, []string{"a.spec.md"}, keysOf(idx), "gone.md is still in the index: unstaged deletion")
	assert.Equal(t, "# A\n\nstaged a\n", string(idx["a.spec.md"]))

	// The same tree snapshots to the same bytes.
	dir2 := t.TempDir()
	_, err = Snapshot(context.Background(), baseline.Loader{Env: testGitEnv}, root, dir2)
	require.NoError(t, err)
	b1, _ := os.ReadFile(filepath.Join(dir, WorkspaceTar))
	b2, _ := os.ReadFile(filepath.Join(dir2, WorkspaceTar))
	assert.Equal(t, b1, b2)
}

// Without git the tree is walked; .git-less projects get no head.tar.
func TestSnapshotNoGit(t *testing.T) {
	root := t.TempDir()
	writeIn(t, root, "a.md", "a\n")
	writeIn(t, root, "sub/b.md", "b\n")
	writeIn(t, root, ".spec/ui.lock", "pid")
	writeIn(t, root, ".spec/checks.json", "{}")
	writeIn(t, root, ".dev/x", "x")
	require.NoError(t, os.Symlink("a.md", filepath.Join(root, "link.md")))
	dir := filepath.Join(root, "rec")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	info, err := Snapshot(context.Background(), baseline.Loader{Env: append(testGitEnv, "GIT_CEILING_DIRECTORIES="+filepath.Dir(root))}, root, dir)
	require.NoError(t, err)
	assert.False(t, info.Git)
	ws, err := ReadTar(filepath.Join(dir, WorkspaceTar))
	require.NoError(t, err)
	assert.Equal(t, []string{".spec/checks.json", "a.md", "link.md", "sub/b.md"}, keysOf(ws))
	assert.Equal(t, "-> a.md", string(ws["link.md"]))
	_, err = os.Stat(filepath.Join(dir, HeadTar))
	assert.True(t, os.IsNotExist(err))
}
