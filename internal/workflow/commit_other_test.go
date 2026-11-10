// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/project"
)

// othersOf reopens the project and lists its model and settings changes.
func othersOf(t *testing.T, root string) (*project.Project, []Change) {
	t.Helper()
	proj, err := project.Open(root)
	require.NoError(t, err)
	b, e := (&Workflow{}).LoadBaseline(proj, baseline.Source{}, false)
	require.Nil(t, e)
	others, err := OtherChanges(proj, b)
	require.NoError(t, err)
	return proj, others
}

// TestCommitChangesCarriesModelsAndSettings: a new model and the formal
// line that selects it are changes of their own beside the spec's, and a
// commit of them carries both files whole while the spec edit stays
// uncommitted. A deleted model is a change too.
func TestCommitChangesCarriesModelsAndSettings(t *testing.T) {
	proj := commitRepo(t)
	root, top := proj.Root, filepath.Dir(proj.Root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tla"), 0o755))
	model := "---- MODULE One ----\nCap == 4\n====\n"
	write(t, root, "tla/One.tla", model)
	write(t, root, "spec.toml", "name = \"c\"\nformal = [\"tla/*.tla\"]\n")
	write(t, root, "c.spec.md", strings.Replace(commitSpec, "at most 4 items", "at most 8 items", 1))

	proj, others := othersOf(t, root)
	require.Equal(t, []string{"file:spec.toml", "file:tla/One.tla"}, ids(others))
	assert.Equal(t, KindSettings, others[0].Kind)
	assert.Equal(t, "settings spec.toml", others[0].Label())
	assert.False(t, others[0].Prose())
	assert.Equal(t, []string{`added: formal = ["tla/*.tla"]`}, others[0].WholeLines(20))
	assert.Equal(t, KindModel, others[1].Kind)
	assert.Equal(t, "model tla/One.tla", others[1].Label())
	assert.Equal(t, 3, others[1].Added)
	assert.Equal(t, []string{"$A.One"}, ids(changesOf(t, proj)), "the spec's own changes are unchanged")

	w := &Workflow{}
	res, e := w.CommitChanges(proj, []string{"file:spec.toml", "file:tla/One.tla"}, "model the cap")
	require.Nil(t, e)
	assert.Empty(t, res.Trailers)
	assert.Equal(t, "spec/spec.toml\nspec/tla/One.tla\n", git(t, top, "show", "--name-only", "--format=", "HEAD"))
	assert.Equal(t, model, git(t, top, "show", "HEAD:spec/tla/One.tla"))
	assert.Equal(t, " M spec/c.spec.md\n", git(t, top, "status", "--porcelain"))
	_, others = othersOf(t, root)
	assert.Empty(t, others, "committed files no longer differ")

	require.NoError(t, os.Remove(filepath.Join(root, "tla", "One.tla")))
	proj, others = othersOf(t, root)
	require.Equal(t, []string{"file:tla/One.tla"}, ids(others))
	assert.Equal(t, []string{"removed: ---- MODULE One ----", "removed: Cap == 4", "removed: ===="}, others[0].WholeLines(20))
	_, e = w.CommitChanges(proj, []string{"file:tla/One.tla"}, "drop the model")
	require.Nil(t, e)
	assert.Equal(t, "D\tspec/tla/One.tla\n", git(t, top, "show", "--name-status", "--format=", "HEAD"))
}

// TestCommitChangesSeenCoversModels: a model edited after the review
// changes the snapshot, so nothing is committed.
func TestCommitChangesSeenCoversModels(t *testing.T) {
	proj := commitRepo(t)
	root := proj.Root
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tla"), 0o755))
	write(t, root, "tla/One.tla", "---- MODULE One ----\n====\n")
	write(t, root, "spec.toml", "name = \"c\"\nformal = [\"tla/*.tla\"]\n")
	proj, others := othersOf(t, root)
	w := &Workflow{}
	b, e := w.LoadBaseline(proj, baseline.Source{}, false)
	require.Nil(t, e)
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	seen := CommitSnapshot(loaded.Spec, others)

	write(t, root, "tla/One.tla", "---- MODULE One ----\nCap == 4\n====\n")
	_, e = w.CommitChangesSeen(proj, ids(others), "model", b.Commit, seen)
	require.NotNil(t, e)
	assert.Equal(t, "changed", e.Code)

	_, others = othersOf(t, root)
	seen = CommitSnapshot(loaded.Spec, others)
	_, e = w.CommitChangesSeen(proj, ids(others), "model", b.Commit, seen)
	require.Nil(t, e)
}
