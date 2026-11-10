// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/formal/tla"
	"github.com/jwriteclub/spec/internal/mdspec"
)

func TestRenameRewritesTags(t *testing.T) {
	root := t.TempDir()
	specSrc := "# A\n\n**$Ab.Cd.** Text, and see $Ab.Cd.Longer.\n\n**$Ab.Cd.Longer.** Uses $Ab.Cd here.\n"
	modelSrc := strings.Join([]string{
		"---- MODULE M ----",
		`\* Spec: $Ab.Cd "text, and see"`,
		`\* Derived-from: $Ab.Cd.Longer "uses $Ab.Cd here"`,
		`\* A plain comment about $Ab.Cd`,
		`(* Spec: $Ab.Cd *)`,
		`Inv_A == "$Ab.Cd"`,
		"====",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.spec.md"), []byte(specSrc), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tla"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tla", "M.tla"), []byte(modelSrc), 0o644))

	spec := mdspec.Build([]*mdspec.File{mdspec.ParseFile("a.spec.md", []byte(specSrc))})
	model := tla.Scan("tla/M.tla", []byte(modelSrc))
	res, err := PlanRename(root, spec, "Ab.Cd", "Ab.Ef", model)
	require.NoError(t, err)
	assert.Len(t, res.Tags, 3, "two references and one quote")
	assert.Equal(t, 3, res.PerFile["tla/M.tla"])
	assert.Equal(t, res.Definitions+res.References+3, res.Tokens())
	require.Len(t, res.LeftAlone, 2, "a plain comment and a string")
	for _, la := range res.LeftAlone {
		assert.Equal(t, "tla/M.tla", la.File)
	}
	require.NoError(t, res.Plan.Commit())

	got, err := os.ReadFile(filepath.Join(root, "tla", "M.tla"))
	require.NoError(t, err)
	assert.Equal(t, strings.Join([]string{
		"---- MODULE M ----",
		`\* Spec: $Ab.Ef "text, and see"`,
		`\* Derived-from: $Ab.Cd.Longer "uses $Ab.Ef here"`,
		`\* A plain comment about $Ab.Cd`,
		`(* Spec: $Ab.Ef *)`,
		`Inv_A == "$Ab.Cd"`,
		"====",
		"",
	}, "\n"), string(got))
}

func TestRenameWithoutTagsLeavesModelsAlone(t *testing.T) {
	root := t.TempDir()
	specSrc := "# A\n\n**$Ab.Cd.** Text.\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.spec.md"), []byte(specSrc), 0o644))
	spec := mdspec.Build([]*mdspec.File{mdspec.ParseFile("a.spec.md", []byte(specSrc))})
	model := tla.Scan("tla/M.tla", []byte("---- MODULE M ----\n\\* Spec: $Other.Name\nA == 1\n====\n"))
	res, err := PlanRename(root, spec, "Ab.Cd", "Ab.Ef", model)
	require.NoError(t, err)
	assert.Empty(t, res.Tags)
	require.Len(t, res.Plan.Outputs, 1)
	assert.Equal(t, "a.spec.md", res.Plan.Outputs[0].Path)
}

func TestRenameSkipsInvalidUTF8Model(t *testing.T) {
	root := t.TempDir()
	specSrc := "# A\n\n**$Ab.Cd.** Text.\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.spec.md"), []byte(specSrc), 0o644))
	spec := mdspec.Build([]*mdspec.File{mdspec.ParseFile("a.spec.md", []byte(specSrc))})
	var models []*formal.File
	models = append(models, tla.Scan("tla/M.tla", []byte("\\* Spec: $Ab.Cd \xA7\n")))
	res, err := PlanRename(root, spec, "Ab.Cd", "Ab.Ef", models...)
	require.NoError(t, err)
	require.Len(t, res.Skipped, 1)
	assert.Equal(t, "tla/M.tla", res.Skipped[0].File)
	assert.True(t, res.Skipped[0].MayReference)
}
