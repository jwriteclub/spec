// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTree writes files (slash paths under root) and returns root.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, text := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte(text), 0o644))
	}
	return root
}

func TestValidModuleName(t *testing.T) {
	for _, ok := range []string{"Naturals", "TLC", "My_Mod2", "_x", "a"} {
		assert.True(t, ValidModuleName(ok), ok)
	}
	for _, bad := range []string{"", "..", "../x", "a/b", `a\b`, "C:x", "/etc/passwd", "a.b", "a b", "Ünï", "a\x00"} {
		assert.False(t, ValidModuleName(bad), "%q", bad)
	}
}

// The model's own directory, by exact file name; open buffers come first.
func TestModuleResolverWorkspace(t *testing.T) {
	root := writeTree(t, map[string]string{"tla/M.tla": "m", "tla/Lib.tla": "disk", "Top.tla": "top"})
	r := ModuleResolver(root, "tla/M.tla", nil)

	rel, src, ok := r("Lib")
	require.True(t, ok)
	assert.Equal(t, "tla/Lib.tla", rel)
	assert.Equal(t, "disk", string(src))

	_, _, ok = r("Top")
	assert.False(t, ok, "only the model's own directory, as SANY looks")

	_, _, ok = r("Missing")
	assert.False(t, ok)

	buf := ModuleResolver(root, "tla/M.tla", func(rel string) ([]byte, bool) {
		if rel == "tla/Lib.tla" || rel == "tla/New.tla" {
			return []byte("unsaved " + rel), true
		}
		return nil, false
	})
	_, src, ok = buf("Lib")
	require.True(t, ok)
	assert.Equal(t, "unsaved tla/Lib.tla", string(src), "an open buffer beats the disk")
	rel, _, ok = buf("New")
	require.True(t, ok, "a buffer not yet on disk resolves")
	assert.Equal(t, "tla/New.tla", rel)
}

// A workspace file named like a standard module shadows the standard one,
// as in SANY.
func TestModuleResolverStandardNamesFromWorkspace(t *testing.T) {
	root := writeTree(t, map[string]string{"tla/M.tla": "m", "tla/Naturals.tla": "mine"})
	_, src, ok := ModuleResolver(root, "tla/M.tla", nil)("Naturals")
	require.True(t, ok)
	assert.Equal(t, "mine", string(src))
	_, _, ok = ModuleResolver(root, "tla/M.tla", nil)("Sequences")
	assert.False(t, ok, "absent: the checker uses its standard copy")
}

func TestModuleResolverRejectsTraversal(t *testing.T) {
	outside := writeTree(t, map[string]string{"Secret.tla": "secret"})
	root := writeTree(t, map[string]string{"tla/M.tla": "m", "Up.tla": "up"})
	asked := []string{}
	r := ModuleResolver(root, "tla/M.tla", func(rel string) ([]byte, bool) {
		asked = append(asked, rel)
		return nil, false
	})
	for _, name := range []string{"../Up", "..", `..\Up`, "/etc/passwd", filepath.Join(outside, "Secret"), "C:Secret", "", "tla/M"} {
		_, _, ok := r(name)
		assert.False(t, ok, "%q", name)
	}
	assert.Empty(t, asked, "rejected names never reach the buffers")

	_, _, ok := ModuleResolver(root, "../x/M.tla", nil)("Up")
	assert.False(t, ok, "a model outside the project resolves nothing")
}

func TestModuleResolverSymlinks(t *testing.T) {
	outside := writeTree(t, map[string]string{"Secret.tla": "secret", "dir/Lib.tla": "lib"})
	root := writeTree(t, map[string]string{"tla/M.tla": "m", "shared/Inside.tla": "inside"})
	require.NoError(t, os.Symlink(filepath.Join(outside, "Secret.tla"), filepath.Join(root, "tla", "Secret.tla")))
	require.NoError(t, os.Symlink(filepath.Join(root, "shared", "Inside.tla"), filepath.Join(root, "tla", "Inside.tla")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "dir"), filepath.Join(root, "linked")))
	r := ModuleResolver(root, "tla/M.tla", nil)

	_, _, ok := r("Secret")
	assert.False(t, ok, "a symlink out of the project is not followed")

	_, src, ok := r("Inside")
	require.True(t, ok, "a symlink that stays inside is")
	assert.Equal(t, "inside", string(src))

	_, _, ok = ModuleResolver(root, "linked/M.tla", nil)("Lib")
	assert.False(t, ok, "a symlinked directory that leaves the project is not followed")
}

func TestModuleResolverDirectoryAndCase(t *testing.T) {
	root := writeTree(t, map[string]string{"tla/M.tla": "m", "tla/naturals.tla": "lower"})
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tla", "Dir.tla"), 0o755))
	r := ModuleResolver(root, "tla/M.tla", nil)

	_, _, ok := r("Dir")
	assert.False(t, ok, "a directory named like a module is not one")

	_, _, ok = r("Naturals")
	assert.False(t, ok, "naturals.tla does not answer for Naturals, even on a case-insensitive file system")
}

// A real root reached through a symlink still counts as inside.
func TestModuleResolverRootThroughSymlink(t *testing.T) {
	real := writeTree(t, map[string]string{"tla/M.tla": "m", "tla/Lib.tla": "lib"})
	link := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.Symlink(real, link))
	_, src, ok := ModuleResolver(link, "tla/M.tla", nil)("Lib")
	require.True(t, ok)
	assert.Equal(t, "lib", string(src))
}

// A model directory that is a symlink out of the project is never listed:
// containment is checked before the case-exact name lookup reads a directory.
func TestModuleResolverDoesNotListOutside(t *testing.T) {
	outside := writeTree(t, map[string]string{"dir/Lib.tla": "lib"})
	root := writeTree(t, map[string]string{"tla/M.tla": "m", "tla/Inside.tla": "inside"})
	require.NoError(t, os.Symlink(filepath.Join(outside, "dir"), filepath.Join(root, "linked")))
	realOutside, err := filepath.EvalSymlinks(outside)
	require.NoError(t, err)
	var listed []string
	readDir = func(dir string) ([]os.DirEntry, error) {
		listed = append(listed, dir)
		return os.ReadDir(dir)
	}
	t.Cleanup(func() { readDir = os.ReadDir })

	_, _, ok := ModuleResolver(root, "linked/M.tla", nil)("Lib")
	assert.False(t, ok)
	for _, d := range listed {
		real, err := filepath.EvalSymlinks(d)
		require.NoError(t, err)
		assert.False(t, strings.HasPrefix(real, realOutside), "listed %s, outside the project", d)
	}

	_, src, ok := ModuleResolver(root, "tla/M.tla", nil)("Inside")
	require.True(t, ok, "a directory inside is still listed")
	assert.Equal(t, "inside", string(src))
	assert.NotEmpty(t, listed)
}
