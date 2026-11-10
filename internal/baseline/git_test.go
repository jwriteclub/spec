// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/jwriteclub/spec/internal/golden"
	"github.com/jwriteclub/spec/internal/project"
)

// repo is a scripted git repository in a temp dir.
type repo struct {
	t    *testing.T
	dir  string
	env  []string
	proj string // project directory relative to dir ("" = the repo root)
}

func gitEnv() []string {
	return []string{
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// newRepo creates an initialised repository (no commits).
func newRepo(t *testing.T) *repo {
	t.Helper()
	requireGit(t)
	r := &repo{t: t, dir: t.TempDir(), env: gitEnv()}
	r.git("init", "-q")
	return r
}

// plainDir is a directory that is not a git repository.
func plainDir(t *testing.T) *repo {
	t.Helper()
	return &repo{t: t, dir: t.TempDir(), env: gitEnv()}
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main", "-c", "core.autocrlf=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), r.env...)
	out, err := cmd.CombinedOutput()
	require.NoError(r.t, err, "git %s: %s", strings.Join(args, " "), out)
	return string(out)
}

func (r *repo) path(rel string) string {
	return filepath.Join(r.dir, filepath.FromSlash(rel))
}

func (r *repo) write(rel, content string) {
	r.t.Helper()
	require.NoError(r.t, os.MkdirAll(filepath.Dir(r.path(rel)), 0o755))
	require.NoError(r.t, os.WriteFile(r.path(rel), []byte(content), 0o644))
}

func (r *repo) remove(rel string) {
	r.t.Helper()
	require.NoError(r.t, os.Remove(r.path(rel)))
}

func (r *repo) commitAll(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "-m", msg)
}

func (r *repo) root() string {
	if r.proj == "" {
		return r.dir
	}
	return r.path(r.proj)
}

func (r *repo) loader() Loader { return Loader{Env: r.env} }

func (r *repo) project() *project.Project {
	r.t.Helper()
	p, err := project.Open(r.root())
	require.NoError(r.t, err)
	return p
}

func (r *repo) load(src Source) *Baseline {
	r.t.Helper()
	b, err := r.loader().Load(context.Background(), r.project(), src)
	require.NoError(r.t, err)
	return b
}

const (
	baseA = "# A\n\n**Def $Time.Media (media time).** Media time is a count of ticks.\n\n**$Frame.Timing (timing).** Uses $Time.Media for ticks. Checked by: test.\n\nA prose paragraph.\n"
	baseB = "# B\n\n**$Hub.Idle (idle).** Uses $Frame.Timing to stop. Checked by: test.\n"
)

// seeded creates a repo with spec.toml, a.spec.md and b.spec.md committed.
func seeded(t *testing.T) *repo {
	r := newRepo(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", baseA)
	r.write("b.spec.md", baseB)
	r.commitAll("initial")
	return r
}

func TestLoadHeadReadsCommittedFiles(t *testing.T) {
	r := seeded(t)
	r.write("a.spec.md", "edited, not committed\n")
	b := r.load(Source{})
	assert.Equal(t, StateOK, b.State)
	assert.Len(t, b.Commit, 40)
	require.NotNil(t, b.Spec)
	assert.Len(t, b.Spec.Files, 2)
	assert.Equal(t, []byte(baseA), b.Spec.File("a.spec.md").Source())
	require.NotNil(t, b.Spec.Anchor("Frame.Timing"))
}

func TestLoadSpecificRefAndTag(t *testing.T) {
	r := seeded(t)
	r.git("tag", "v1")
	r.write("a.spec.md", baseA+"\n**$New.One.** Added later.\n")
	r.commitAll("second")

	assert.NotNil(t, r.load(Source{}).Spec.Anchor("New.One"))
	assert.Nil(t, r.load(Source{Ref: "HEAD~1"}).Spec.Anchor("New.One"))
	assert.Nil(t, r.load(Source{Ref: "v1"}).Spec.Anchor("New.One"))
	assert.Nil(t, r.load(Source{Ref: "main~1"}).Spec.Anchor("New.One"))
}

func TestLoadBadRef(t *testing.T) {
	r := seeded(t)
	for _, ref := range []string{"no-such-ref", "HEAD~50", "-x"} {
		_, err := r.loader().Load(context.Background(), r.project(), Source{Ref: ref})
		var be *BadRefError
		require.ErrorAs(t, err, &be, ref)
		assert.Equal(t, ref, be.Ref)
	}
}

func TestLoadUnbornHead(t *testing.T) {
	r := newRepo(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", baseA)
	b := r.load(Source{})
	assert.Equal(t, StateUnborn, b.State)
	assert.Equal(t, "no commits yet", b.Reason)
	require.NotNil(t, b.Spec)
	assert.Empty(t, b.Spec.Anchors)

	// An explicit HEAD behaves the same; any other ref is a bad ref.
	assert.Equal(t, StateUnborn, r.load(Source{Ref: "HEAD"}).State)
	_, err := r.loader().Load(context.Background(), r.project(), Source{Ref: "main"})
	var be *BadRefError
	assert.ErrorAs(t, err, &be)
}

func TestLoadNotAGitRepo(t *testing.T) {
	requireGit(t)
	r := plainDir(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", baseA)
	b := r.load(Source{})
	assert.Equal(t, StateUnavailable, b.State)
	assert.Nil(t, b.Spec)
	assert.NotEmpty(t, b.Reason)
	// The index is equally unavailable.
	assert.Equal(t, StateUnavailable, r.load(Source{Index: true}).State)
}

func TestLoadGitMissing(t *testing.T) {
	r := plainDir(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", baseA)
	l := Loader{Git: filepath.Join(r.dir, "no-such-git")}
	b, err := l.Load(context.Background(), r.project(), Source{})
	require.NoError(t, err)
	assert.Equal(t, StateUnavailable, b.State)
	assert.Contains(t, b.Reason, "not installed")
}

func TestLoadIndex(t *testing.T) {
	r := seeded(t)
	r.write("a.spec.md", baseA+"\n**$Staged.One.** In the index.\n")
	r.git("add", "a.spec.md")
	r.write("a.spec.md", baseA+"\n**$Staged.One.** In the index.\n\n**$Worktree.Only.** Not staged.\n")
	r.write("c.spec.md", "**$Untracked.Thing.** untracked.\n")

	b := r.load(Source{Index: true})
	assert.Equal(t, StateOK, b.State)
	assert.Empty(t, b.Commit)
	assert.NotNil(t, b.Spec.Anchor("Staged.One"))
	assert.Nil(t, b.Spec.Anchor("Worktree.Only"))
	assert.Nil(t, b.Spec.Anchor("Untracked.Thing"), "untracked files are absent from the baseline")
	assert.Nil(t, r.load(Source{}).Spec.Anchor("Staged.One"))
}

func TestLoadIndexInUnbornRepo(t *testing.T) {
	r := newRepo(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", baseA)
	r.git("add", ".")
	b := r.load(Source{Index: true})
	assert.Equal(t, StateOK, b.State)
	assert.NotNil(t, b.Spec.Anchor("Frame.Timing"))
}

func TestLoadProjectInSubdirectory(t *testing.T) {
	r := newRepo(t)
	r.proj = "services/vod"
	r.write("README.md", "top\n")
	r.write("other.spec.md", "**$Outside.Proj.** not in the project.\n")
	r.write("services/vod/spec.toml", "")
	r.write("services/vod/a.spec.md", baseA)
	r.write("services/vod/docs/b.spec.md", baseB)
	r.write("services/other/c.spec.md", "**$Sibling.One.** sibling.\n")
	r.commitAll("initial")

	b := r.load(Source{})
	require.Equal(t, StateOK, b.State)
	var paths []string
	for _, f := range b.Spec.Files {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"a.spec.md", "docs/b.spec.md"}, paths, "project-relative paths; nothing outside the project")

	// Same for the index.
	b = r.load(Source{Index: true})
	assert.Len(t, b.Spec.Files, 2)
}

func TestLoadAppliesSpecTomlFilter(t *testing.T) {
	r := newRepo(t)
	r.write("spec.toml", "files = [\"docs/*.md\"]\n")
	r.write("docs/a.md", baseA)
	r.write("docs/b.md", baseB)
	r.write("x.spec.md", "**$Not.Selected.** nope.\n")
	r.write("nested/spec.toml", "")
	r.commitAll("initial")
	b := r.load(Source{})
	var paths []string
	for _, f := range b.Spec.Files {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"docs/a.md", "docs/b.md"}, paths)
}

func TestLoadSkipsNestedProjectsAndSkippedDirs(t *testing.T) {
	r := newRepo(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", baseA)
	r.write("nested/spec.toml", "")
	r.write("nested/n.spec.md", "**$Nested.One.** nested project.\n")
	r.write("vendor/v.spec.md", "**$Vendor.One.** vendored.\n")
	r.commitAll("initial")
	b := r.load(Source{})
	require.Len(t, b.Spec.Files, 1)
	assert.Equal(t, "a.spec.md", b.Spec.Files[0].Path)
}

func TestLoadIgnoresSymlinks(t *testing.T) {
	r := newRepo(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", baseA)
	require.NoError(t, os.Symlink("a.spec.md", r.path("link.spec.md")))
	r.commitAll("initial")
	b := r.load(Source{})
	require.Len(t, b.Spec.Files, 1)
	assert.Equal(t, "a.spec.md", b.Spec.Files[0].Path)
}

func TestLoadKeepsBytesExact(t *testing.T) {
	r := newRepo(t)
	r.write("spec.toml", "")
	crlf := "\xef\xbb\xbf**$Ab.Cd.** x\r\nsecond \xff line\r\n"
	r.write("a.spec.md", crlf)
	r.commitAll("initial")
	b := r.load(Source{})
	assert.Equal(t, []byte(crlf), b.Spec.File("a.spec.md").Source())
}

func TestLoadDetachedAndDeletedFileKeepsAnchors(t *testing.T) {
	r := seeded(t)
	r.remove("b.spec.md")
	b := r.load(Source{})
	assert.NotNil(t, b.Spec.Anchor("Hub.Idle"), "a deleted file still has its anchors in the baseline")
}

func TestSourceString(t *testing.T) {
	assert.Equal(t, "HEAD", Source{}.String())
	assert.Equal(t, "main~2", Source{Ref: "main~2"}.String())
	assert.Equal(t, "the index", Source{Index: true, Ref: "x"}.String())
}
