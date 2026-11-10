// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/config"
)

// gitOut runs git in dir and returns its output.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), testGitEnv...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

// A snapshot restores to the same working tree, HEAD and index, with a
// user config whose providers serve a copy of canned.toml; Cleanup
// removes it all.
func TestPrepareRestores(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	gitIn(t, root, "init", "-q", "-b", "main")
	writeIn(t, root, ".gitignore", ".spec/\n.dev/\n")
	writeIn(t, root, "a.spec.md", "# A\n\nhead a\n")
	writeIn(t, root, "gone.md", "deleted later\n")
	writeIn(t, root, "run.sh", "#!/bin/sh\n")
	require.NoError(t, os.Chmod(filepath.Join(root, "run.sh"), 0o755))
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "init")
	writeIn(t, root, "a.spec.md", "# A\n\nstaged a\n")
	gitIn(t, root, "add", "a.spec.md")
	writeIn(t, root, "a.spec.md", "# A\n\nworking a\n")
	require.NoError(t, os.Remove(filepath.Join(root, "gone.md")))
	writeIn(t, root, "new.md", "untracked\n")
	writeIn(t, root, ".spec/proposals/p1.json", "{}\n")

	dir := filepath.Join(t.TempDir(), "rec")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	info, err := Snapshot(context.Background(), baseline.Loader{Env: testGitEnv}, root, dir)
	require.NoError(t, err)
	start := time.Date(2026, 11, 1, 14, 2, 3, 0, time.UTC)
	require.NoError(t, WriteMeta(dir, Meta{Version: FormatVersion, Start: start, GitHead: info.Head, Cols: 100, Rows: 30}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, SessionFile), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, CannedFile), []byte("[[check]]\nreply = \"ok\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile),
		[]byte("default = \"cf\"\n\n[providers.cf]\nkind = \"canned\"\npath = \""+CannedPathToken+"\"\nmodel = \"m\"\n"), 0o644))

	p, err := Prepare(context.Background(), dir, t.TempDir())
	require.NoError(t, err)
	got := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(p.Root, rel))
		require.NoError(t, err)
		return string(b)
	}
	assert.Equal(t, "# A\n\nworking a\n", got("a.spec.md"))
	assert.Equal(t, "untracked\n", got("new.md"))
	assert.Equal(t, "{}\n", got(".spec/proposals/p1.json"))
	_, err = os.Stat(filepath.Join(p.Root, "gone.md"))
	assert.True(t, os.IsNotExist(err), "deleted in the working tree")
	st, err := os.Stat(filepath.Join(p.Root, "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), st.Mode().Perm())

	assert.Equal(t, "# A\n\nhead a\n", gitOut(t, p.Root, "show", "HEAD:a.spec.md"))
	assert.Equal(t, "deleted later\n", gitOut(t, p.Root, "show", "HEAD:gone.md"))
	assert.Equal(t, "# A\n\nstaged a\n", gitOut(t, p.Root, "show", ":a.spec.md"))
	assert.Equal(t, "a.spec.md\n", gitOut(t, p.Root, "diff", "--cached", "--name-only"))
	assert.Equal(t, fmt.Sprintf("%d\n", start.Unix()), gitOut(t, p.Root, "log", "-1", "--format=%ct"),
		"committed at the recording's start")

	cfgPath := config.PathFor(p.Getenv("XDG_CONFIG_HOME"), p.Getenv("HOME"))
	loaded, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Contains(t, loaded.Config.Providers, "cf")
	assert.Equal(t, config.KindCanned, loaded.Config.Providers["cf"].Kind)
	assert.Equal(t, p.Canned, loaded.Config.Providers["cf"].Path)
	assert.True(t, strings.HasPrefix(p.Canned, p.Tmp))
	b, err := os.ReadFile(p.Canned)
	require.NoError(t, err)
	assert.Equal(t, "[[check]]\nreply = \"ok\"\n", string(b))

	require.NoError(t, p.Cleanup())
	_, err = os.Stat(p.Tmp)
	assert.True(t, os.IsNotExist(err))
}

// Without head.tar the project is a plain directory.
func TestPrepareNoGit(t *testing.T) {
	root := t.TempDir()
	writeIn(t, root, "a.md", "a\n")
	dir := filepath.Join(root, "rec")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	_, err := Snapshot(context.Background(), baseline.Loader{Env: append(testGitEnv, "GIT_CEILING_DIRECTORIES="+filepath.Dir(root))}, root, dir)
	require.NoError(t, err)
	require.NoError(t, WriteMeta(dir, Meta{Version: FormatVersion}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, SessionFile), nil, 0o644))
	p, err := Prepare(context.Background(), dir, t.TempDir())
	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join(p.Root, "a.md"))
	require.NoError(t, err)
	assert.Equal(t, "a\n", string(b))
	_, err = os.Stat(filepath.Join(p.Root, ".git"))
	assert.True(t, os.IsNotExist(err))
}

// member is one entry of a hand-made tar.
type member struct {
	name, link string
	body       string
	mode       int64
	kind       byte
}

// writeTar writes a tar of members and returns its path.
func writeTar(t *testing.T, members ...member) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, m := range members {
		kind, mode := m.kind, m.mode
		if kind == 0 {
			kind = tar.TypeReg
		}
		if mode == 0 {
			mode = 0o644
		}
		require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: kind, Name: m.name, Linkname: m.link, Mode: mode, Size: int64(len(m.body)), Format: tar.FormatPAX}))
		_, err := tw.Write([]byte(m.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	p := filepath.Join(t.TempDir(), "t.tar")
	require.NoError(t, os.WriteFile(p, buf.Bytes(), 0o644))
	return p
}

func symlink(name, target string) member {
	return member{name: name, link: target, kind: tar.TypeSymlink, mode: 0o777}
}

// sandbox is a project root with a sibling directory outside it.
func sandbox(t *testing.T) (root, outside string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	root, outside = filepath.Join(base, "project"), filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.MkdirAll(outside, 0o755))
	return root, outside
}

// A member never lands outside the root or in .git, nor through a link inside
// the root, whatever links the tar or the root hold on the way there.
func TestExtractTarNeverFollowsALink(t *testing.T) {
	cases := []struct {
		name    string
		pre     map[string]string // symlinks already in the root
		members func(outside string) []member
		// outside is where the attack would have written, relative to the
		// outside directory; "" for inside the root.
		outsideFile string
		rootFile    string
	}{
		{
			name: "absolute directory link in the tar",
			members: func(o string) []member {
				return []member{symlink("esc", o), {name: "esc/pwned", body: "x"}}
			},
			outsideFile: "pwned",
		},
		{
			name: "relative directory link in the tar",
			members: func(string) []member {
				return []member{symlink("esc", "../outside"), {name: "esc/pwned", body: "x"}}
			},
			outsideFile: "pwned",
		},
		{
			name: "directory link already in the root",
			pre:  map[string]string{"pre": "OUTSIDE"},
			members: func(string) []member {
				return []member{{name: "pre/pwned", body: "x"}}
			},
			outsideFile: "pwned",
		},
		{
			name: "file link already in the root",
			pre:  map[string]string{"f.md": "OUTSIDE/pwned"},
			members: func(string) []member {
				return []member{{name: "f.md", body: "x"}}
			},
			outsideFile: "pwned",
		},
		{
			name: "file link earlier in the tar",
			members: func(o string) []member {
				return []member{symlink("f.md", filepath.Join(o, "pwned")), {name: "f.md", body: "x"}}
			},
			outsideFile: "pwned",
		},
		{
			name: "a link through a link",
			members: func(string) []member {
				return []member{
					{name: "sub/keep", body: "k"},
					symlink("sub/up", ".."),
					symlink("esc", "sub/up/../outside"),
					{name: "esc/pwned", body: "x"},
				}
			},
			outsideFile: "pwned",
		},
		{
			name: "a link inside the root, on the way to .git",
			members: func(string) []member {
				return []member{symlink("g", ".git"), {name: "g/hooks/pre-commit", body: "x", mode: 0o755}}
			},
			rootFile: ".git/hooks/pre-commit",
		},
		{
			name: "a harmless-looking link inside the root",
			members: func(string) []member {
				return []member{{name: "real/a.md", body: "a"}, symlink("l", "real"), {name: "l/pwned", body: "x"}}
			},
			rootFile: "real/pwned",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, outside := sandbox(t)
			for name, target := range tc.pre {
				require.NoError(t, os.Symlink(strings.ReplaceAll(target, "OUTSIDE", outside), filepath.Join(root, name)))
			}
			_, _ = extractTar(writeTar(t, tc.members(outside)...), root)
			if tc.outsideFile != "" {
				assert.NoFileExists(t, filepath.Join(outside, tc.outsideFile))
			}
			if tc.rootFile != "" {
				assert.NoFileExists(t, filepath.Join(root, tc.rootFile))
			}
		})
	}
}

// What leaves the root by a link is dropped, however the link gets there:
// a replay's editor or watcher would read and write through it later.
func TestExtractTarDropsLinksThatLeave(t *testing.T) {
	root, outside := sandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644))
	names, err := extractTar(writeTar(t,
		member{name: "a.md", body: "a"},
		symlink("abs", filepath.Join(outside, "secret")),
		symlink("rel", "../outside/secret"),
		symlink("sub/deep", "../../outside/secret"),
		symlink("sub/up", ".."),
		symlink("chain", "sub/up/../outside/secret"),
		symlink("git", ".git/config"),
		symlink("sub/gitup", "../.GIT"),
		symlink("self", "self"),
		symlink("in.md", "a.md"),
		symlink("sub/in.md", "../a.md"),
		symlink("dangling", "not-yet"),
	), root)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.md", "dangling", "in.md", "sub/in.md", "sub/up"}, names)
	for _, gone := range []string{"abs", "rel", "sub/deep", "chain", "git", "sub/gitup", "self"} {
		_, err := os.Lstat(filepath.Join(root, gone))
		assert.ErrorIs(t, err, fs.ErrNotExist, gone)
	}
	b, err := os.ReadFile(filepath.Join(root, "sub/in.md"))
	require.NoError(t, err)
	assert.Equal(t, "a", string(b), "a link inside the root still works")
	target, err := os.Readlink(filepath.Join(root, "dangling"))
	require.NoError(t, err)
	assert.Equal(t, "not-yet", target)
}

// Only files and symbolic links are recordable; anything else fails loudly and
// creates nothing, and a bad name fails before it touches the disk.
func TestExtractTarRefusesMembers(t *testing.T) {
	cases := map[string]member{
		"hard link":        {name: "h", link: "a.md", kind: tar.TypeLink},
		"fifo":             {name: "p", kind: tar.TypeFifo},
		"char device":      {name: "c", kind: tar.TypeChar},
		"block device":     {name: "b", kind: tar.TypeBlock},
		"directory":        {name: "d/", kind: tar.TypeDir, mode: 0o755},
		"parent":           {name: "../pwned", body: "x"},
		"parent later":     {name: "a/../../pwned", body: "x"},
		"absolute":         {name: "/tmp/pwned-logedit", body: "x"},
		"the root":         {name: ".", body: "x"},
		"nameless":         {name: "./", kind: tar.TypeDir},
		"git hook":         {name: ".git/hooks/pre-commit", body: "x", mode: 0o755},
		"git, other case":  {name: ".GIT/config", body: "x"},
		"git, unclean":     {name: "a/../.git/config", body: "x"},
		"nested git":       {name: "vendor/x/.git/config", body: "x"},
		"git as a file":    {name: ".git", body: "gitdir: /tmp/x"},
		"git as a symlink": symlink(".git", "elsewhere"),
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			root, outside := sandbox(t)
			keep := filepath.Join(root, "a.md")
			require.NoError(t, os.WriteFile(keep, []byte("a"), 0o644))
			names, err := extractTar(writeTar(t, m), root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), strings.TrimRight(m.name, "/"), "names the member")
			assert.Empty(t, names)
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			require.Len(t, entries, 1, "nothing created")
			assert.Equal(t, "a.md", entries[0].Name())
			entries, err = os.ReadDir(outside)
			require.NoError(t, err)
			assert.Empty(t, entries)
			entries, err = os.ReadDir(filepath.Dir(root))
			require.NoError(t, err)
			assert.Len(t, entries, 2, "nothing beside the root")
		})
	}
}

// A file's mode is 644 or 755, whatever the tar says.
func TestExtractTarModes(t *testing.T) {
	root, _ := sandbox(t)
	_, err := extractTar(writeTar(t,
		member{name: "suid", body: "x", mode: 0o6777},
		member{name: "world", body: "x", mode: 0o666},
		member{name: "tool", body: "x", mode: 0o755},
		member{name: "private", body: "x", mode: 0o600},
	), root)
	require.NoError(t, err)
	want := map[string]os.FileMode{"suid": 0o755, "world": 0o644, "tool": 0o755, "private": 0o644}
	for name, mode := range want {
		st, err := os.Lstat(filepath.Join(root, name))
		require.NoError(t, err)
		assert.Equal(t, mode, st.Mode(), name)
	}
}

// recordingWith is a minimal recording holding the given tars.
func recordingWith(t *testing.T, tars map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, WriteMeta(dir, Meta{Version: FormatVersion}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, SessionFile), nil, 0o644))
	for name, src := range tars {
		b, err := os.ReadFile(src)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o644))
	}
	return dir
}

// A hook in a recording never runs: restoring commits, and git would run it.
func TestPrepareRefusesGitHooks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	for _, tarName := range []string{HeadTar, WorkspaceTar} {
		t.Run(tarName, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "ran")
			hook := "#!/bin/sh\necho ran > '" + marker + "'\n"
			src := writeTar(t,
				member{name: "a.md", body: "a"},
				member{name: ".git/hooks/pre-commit", body: hook, mode: 0o755},
				member{name: ".git/hooks/post-commit", body: hook, mode: 0o755},
			)
			parent := t.TempDir()
			_, err := Prepare(context.Background(), recordingWith(t, map[string]string{tarName: src}), parent)
			assert.NoFileExists(t, marker, "the hook ran")
			require.Error(t, err)
			assert.Contains(t, err.Error(), ".git/hooks")
			entries, err := os.ReadDir(parent)
			require.NoError(t, err)
			assert.Empty(t, entries, "the temp directory is gone")
		})
	}
}

// A real project's links, staged type changes and all, come back as they were.
func TestPrepareRoundTripsLinksAndTypeChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	gitIn(t, root, "init", "-q", "-b", "main")
	writeIn(t, root, ".gitignore", ".spec/\n.dev/\n")
	writeIn(t, root, "a.spec.md", "# A\n\na\n")
	writeIn(t, root, "real/y.md", "y\n")
	writeIn(t, root, "tofile/x.md", "x\n")
	require.NoError(t, os.Symlink("a.spec.md", filepath.Join(root, "link.md")))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	require.NoError(t, os.Symlink("../a.spec.md", filepath.Join(root, "docs", "up.md")))
	require.NoError(t, os.Symlink("real", filepath.Join(root, "dirlink")))
	require.NoError(t, os.Symlink("real", filepath.Join(root, "todir")))
	require.NoError(t, os.Symlink("../outside/thing", filepath.Join(root, "leaves")))
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "init")

	// Staged: a link becomes a directory, a directory becomes a file.
	gitIn(t, root, "rm", "-q", "--cached", "todir")
	require.NoError(t, os.Remove(filepath.Join(root, "todir")))
	writeIn(t, root, "todir/z.md", "z\n")
	gitIn(t, root, "add", "todir/z.md")
	gitIn(t, root, "rm", "-q", "-r", "tofile")
	writeIn(t, root, "tofile", "a file now\n")
	gitIn(t, root, "add", "tofile")
	// Working tree: a new link.
	require.NoError(t, os.Symlink("real/y.md", filepath.Join(root, "new-link.md")))

	dir := filepath.Join(t.TempDir(), "rec")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	_, err := Snapshot(context.Background(), baseline.Loader{Env: testGitEnv}, root, dir)
	require.NoError(t, err)
	require.NoError(t, WriteMeta(dir, Meta{Version: FormatVersion}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, SessionFile), nil, 0o644))

	p, err := Prepare(context.Background(), dir, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Cleanup() })

	links := map[string]string{"link.md": "a.spec.md", "docs/up.md": "../a.spec.md", "dirlink": "real", "new-link.md": "real/y.md"}
	for name, target := range links {
		got, err := os.Readlink(filepath.Join(p.Root, name))
		require.NoError(t, err, name)
		assert.Equal(t, target, got, name)
	}
	st, err := os.Lstat(filepath.Join(p.Root, "todir"))
	require.NoError(t, err)
	assert.True(t, st.IsDir(), "a staged link turned directory")
	b, err := os.ReadFile(filepath.Join(p.Root, "todir", "z.md"))
	require.NoError(t, err)
	assert.Equal(t, "z\n", string(b))
	b, err = os.ReadFile(filepath.Join(p.Root, "tofile"))
	require.NoError(t, err)
	assert.Equal(t, "a file now\n", string(b))
	_, err = os.Lstat(filepath.Join(p.Root, "leaves"))
	assert.ErrorIs(t, err, fs.ErrNotExist, "a link out of the project is not restored")
	assert.Contains(t, gitOut(t, p.Root, "ls-files", "-s", "--", "todir/z.md"), "todir/z.md", "still staged")
	assert.Contains(t, gitOut(t, p.Root, "ls-tree", "-r", "HEAD", "--name-only"), "dirlink")
}

// A nested repository in a project without git is not recorded: it would be
// a .git member, which a restore refuses.
func TestSnapshotNoGitSkipsNestedRepo(t *testing.T) {
	root := t.TempDir()
	writeIn(t, root, "a.md", "a\n")
	writeIn(t, root, "vendor/x/.git/config", "[core]\n")
	writeIn(t, root, "vendor/x/b.md", "b\n")
	writeIn(t, root, "worktree/.git", "gitdir: /elsewhere\n")
	dir := filepath.Join(root, "rec")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	_, err := Snapshot(context.Background(), baseline.Loader{Env: append(testGitEnv, "GIT_CEILING_DIRECTORIES="+filepath.Dir(root))}, root, dir)
	require.NoError(t, err)
	ws, err := ReadTar(filepath.Join(dir, WorkspaceTar))
	require.NoError(t, err)
	assert.Equal(t, []string{"a.md", "vendor/x/b.md"}, keysOf(ws))
	require.NoError(t, WriteMeta(dir, Meta{Version: FormatVersion}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, SessionFile), nil, 0o644))
	p, err := Prepare(context.Background(), dir, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, p.Cleanup())
}
