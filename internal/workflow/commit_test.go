// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

const commitSpec = "# c\n\n## 1. Rules\n\n" +
	"**$A.One (one).** One MUST hold at most 4 items.\nChecked by: TestOne.\n\n" +
	"Prose between the anchors.\n\n" +
	"**$B.Two (two).** Two MUST answer within 5 s.\nChecked by: TestTwo.\n"

// commitRepo is a committed git repository holding commitSpec in a
// subdirectory (so repository-relative paths are exercised), with a
// commit-msg hook that records it ran. It returns the project.
func commitRepo(t *testing.T) *project.Project {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "Spec Test", "GIT_AUTHOR_EMAIL": "spec@example.com",
		"GIT_COMMITTER_NAME": "Spec Test", "GIT_COMMITTER_EMAIL": "spec@example.com",
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0",
	} {
		t.Setenv(k, v)
	}
	top := t.TempDir()
	dir := filepath.Join(top, "spec")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	git(t, top, "init", "-q")
	write(t, dir, "spec.toml", "name = \"c\"\n")
	write(t, dir, "c.spec.md", commitSpec)
	write(t, top, ".gitignore", ".spec/\n")
	git(t, top, "add", "-A")
	git(t, top, "commit", "-q", "-m", "base")
	hook := filepath.Join(top, ".git", "hooks", "commit-msg")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o755))
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho ran >> \"$(git rev-parse --git-dir)/hook-ran\"\n"), 0o755))
	proj, err := project.Open(dir)
	require.NoError(t, err)
	return proj
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644))
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	return string(b)
}

// changesOf lists the changes of the project's working tree.
func changesOf(t *testing.T, proj *project.Project) []Change {
	t.Helper()
	w := &Workflow{}
	b, e := w.LoadBaseline(proj, baseline.Source{}, false)
	require.Nil(t, e)
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	return Changes(b.Spec, loaded.Spec)
}

func ids(cs []Change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func TestChangesGroupsByAnchor(t *testing.T) {
	proj := commitRepo(t)
	edited := strings.NewReplacer("at most 4 items", "at most 8 items", "within 5 s", "within 2 s",
		"Prose between", "Plain prose between").Replace(commitSpec)
	write(t, proj.Root, "c.spec.md", edited)
	cs := changesOf(t, proj)
	assert.Equal(t, []string{"$A.One", "prose:c.spec.md", "$B.Two"}, ids(cs))
	assert.Equal(t, "changed", cs[0].Anchors[0].Status)
	assert.Equal(t, "inv", cs[0].Anchors[0].Kind)
	assert.Equal(t, 5, cs[0].Line)
	assert.True(t, cs[1].Prose())
	assert.Equal(t, "prose in c.spec.md", cs[1].Label())

	// Whitespace inside an anchor is an edit that needs no trailer.
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "One MUST hold", "One MUST\nhold", 1))
	cs = changesOf(t, proj)
	require.Len(t, cs, 1)
	assert.Equal(t, StatusEdited, cs[0].Anchors[0].Status)

	// A new anchor next to a changed one is its own change.
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "Prose between the anchors.", "**$C.Three (three).** New.\n\nProse between the anchors.", 1))
	cs = changesOf(t, proj)
	require.Len(t, cs, 1)
	assert.Equal(t, "$C.Three", cs[0].ID)
	assert.Equal(t, "added", cs[0].Anchors[0].Status)
}

// Acceptance 1: two changed anchors in one file, commit one. The other
// stays modified in the working tree and unstaged; the trailers name only
// the committed anchor; the commit-msg hook runs.
func TestCommitChangesPartial(t *testing.T) {
	proj := commitRepo(t)
	top := filepath.Dir(proj.Root)
	edited := strings.NewReplacer("at most 4 items", "at most 8 items", "within 5 s", "within 2 s").Replace(commitSpec)
	write(t, proj.Root, "c.spec.md", edited)

	w := &Workflow{}
	res, e := w.CommitChanges(proj, []string{"$A.One"}, "spec: raise the cap of $A.One\n\nEight items fit.")
	require.Nil(t, e)
	require.Len(t, res.Trailers, 1)
	assert.Regexp(t, `^Spec-Change: \$A\.One inv:[0-9a-f]{8} -> inv:[0-9a-f]{8}$`, res.Trailers[0])
	assert.Empty(t, res.Warning)
	assert.Equal(t, []string{"c.spec.md"}, res.Files)

	committed := git(t, top, "show", "HEAD:spec/c.spec.md")
	assert.Equal(t, strings.Replace(commitSpec, "at most 4 items", "at most 8 items", 1), committed, "only $A.One's edit")
	assert.Equal(t, edited, read(t, proj.Root, "c.spec.md"), "the working tree keeps both edits")
	assert.Equal(t, " M spec/c.spec.md\n", git(t, top, "status", "--porcelain"), "modified, unstaged")
	assert.Empty(t, git(t, top, "diff", "--cached"), "the index holds the commit")
	assert.Contains(t, git(t, top, "diff"), "+**$B.Two (two).** Two MUST answer within 2 s.")

	msg := git(t, top, "log", "-1", "--format=%B")
	assert.Contains(t, msg, "spec: raise the cap of $A.One\n\nEight items fit.\n")
	assert.Contains(t, msg, "Spec-Change: $A.One")
	assert.NotContains(t, msg, "$B.Two")
	assert.Equal(t, "ran\n", read(t, filepath.Join(top, ".git"), "hook-ran"), "the commit-msg hook ran")

	// What is left is the other change alone.
	assert.Equal(t, []string{"$B.Two"}, ids(changesOf(t, proj)))
}

func TestCommitChangesRefusesUnknown(t *testing.T) {
	proj := commitRepo(t)
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "within 5 s", "within 2 s", 1))
	w := &Workflow{}
	_, e := w.CommitChanges(proj, []string{"$A.One"}, "x")
	require.NotNil(t, e)
	assert.Equal(t, "changed", e.Code)
	_, e = w.CommitChanges(proj, []string{"$B.Two"}, "  ")
	require.NotNil(t, e)
	assert.Equal(t, "usage", e.Code)
	assert.Equal(t, "base\n", git(t, filepath.Dir(proj.Root), "log", "-1", "--format=%s"), "nothing committed")
}

// spec approve --commit commits what the proposal wrote, not an unrelated
// edit of another anchor in the same file.
func TestApproveCommitsOnlyTheProposal(t *testing.T) {
	proj := commitRepo(t)
	top := filepath.Dir(proj.Root)
	unrelated := strings.Replace(commitSpec, "within 5 s", "within 2 s", 1)
	write(t, proj.Root, "c.spec.md", unrelated)

	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	find := "One MUST hold at most 4 items."
	src := string(loaded.Spec.File("c.spec.md").Source())
	h := finding.Hunk{N: 1, File: "c.spec.md", Anchor: "$A.One", Issue: "numeric_mismatch", Severity: "inconsistent",
		Explanation: "align", Find: find, Replace: "One MUST hold at most 6 items."}
	h.Range.Start = strings.Index(src, find)
	h.Range.End = h.Range.Start + len(find)
	p := &proposal.Proposal{Author: "agent", Kind: proposal.KindAnalysis, Summary: "align the cap",
		Hunks: []finding.Hunk{h}, Status: proposal.StatusPending}
	FillBase(p, loaded.Spec)
	w := &Workflow{}
	require.NoError(t, w.Store(proj.Root).Create(p))

	res, e := w.Apply(proj.Root, p.ID, nil, "human", true)
	require.Nil(t, e)
	assert.NotEmpty(t, res.Commit)
	committed := git(t, top, "show", "HEAD:spec/c.spec.md")
	assert.Equal(t, strings.Replace(commitSpec, "at most 4 items", "at most 6 items", 1), committed)
	assert.Contains(t, git(t, top, "diff"), "within 2 s", "the unrelated edit stays uncommitted")
	assert.Equal(t, []string{"$B.Two"}, ids(changesOf(t, proj)))
}

func TestSettleAndPrepareInto(t *testing.T) {
	proj := commitRepo(t)
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	find := "One MUST hold at most 4 items."
	h := finding.Hunk{N: 1, File: "c.spec.md", Issue: "x", Severity: "cosmetic", Find: find, Replace: "One MUST hold at most 6 items."}
	p := &proposal.Proposal{Author: "human", Kind: proposal.KindAnalysis, Hunks: []finding.Hunk{h}, Status: proposal.StatusPending}
	FillBase(p, loaded.Spec)
	w := &Workflow{}
	require.NoError(t, w.Store(proj.Root).Create(p))

	// Prepared against a buffer that differs from the file on disk.
	buf := strings.Replace(commitSpec, "# c\n", "# c, edited\n", 1)
	prep, e := w.PrepareInto(proj, p, []int{1}, map[string][]byte{"c.spec.md": []byte(buf)})
	require.Nil(t, e)
	require.Len(t, prep.Edits, 1)
	assert.Equal(t, buf, string(prep.Edits[0].Snapshot))
	assert.Equal(t, commitSpec, read(t, proj.Root, "c.spec.md"), "nothing written")

	got, e := w.Settle(proj.Root, p.ID, proposal.Decision{Accepted: []int{1}, Applied: []int{1}, By: "human"})
	require.Nil(t, e)
	assert.Equal(t, proposal.StatusApplied, got.Status)
	_, e = w.Settle(proj.Root, p.ID, proposal.Decision{})
	require.NotNil(t, e)
	assert.Equal(t, "not_pending", e.Code)
}
