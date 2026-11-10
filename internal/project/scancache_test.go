// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/formal"
)

// cacheProject writes a project with two models.
func cacheProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for rel, text := range map[string]string{
		"spec.toml": "formal = [\"tla/*.tla\"]\n", "a.spec.md": formalSpec,
		"tla/M.tla": formalModel, "tla/N.tla": "---- MODULE N ----\nB == 1\n====\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(text), 0o644))
	}
	return dir
}

func loadModels(t *testing.T, dir string, overlay map[string][]byte, c *ScanCache) map[string]*formal.File {
	t.Helper()
	l, err := LoadWith(dir, overlay, c)
	require.NoError(t, err)
	out := map[string]*formal.File{}
	for _, f := range l.Formal.Files {
		out[f.Path] = f
	}
	return out
}

// A load with the cache reuses the scan of an unchanged model, the same
// *formal.File, and scans a changed one again.
func TestScanCacheReusesUnchangedModels(t *testing.T) {
	dir := cacheProject(t)
	c := NewScanCache(8)
	first := loadModels(t, dir, nil, c)
	again := loadModels(t, dir, nil, c)
	assert.Same(t, first["tla/M.tla"], again["tla/M.tla"], "unchanged: not parsed again")
	assert.Same(t, first["tla/N.tla"], again["tla/N.tla"])

	edited := loadModels(t, dir, map[string][]byte{"tla/N.tla": []byte("---- MODULE N ----\nB == 2\nC == 3\n====\n")}, c)
	assert.NotSame(t, again["tla/N.tla"], edited["tla/N.tla"], "changed: parsed again")
	assert.Len(t, edited["tla/N.tla"].Decls, 2)
	assert.Same(t, first["tla/M.tla"], edited["tla/M.tla"])

	assert.NotSame(t, first["tla/M.tla"], loadModels(t, dir, nil, nil)["tla/M.tla"], "no cache: always parsed")
}

// A new parser version does not reuse the old one's scans.
func TestScanCacheKeysOnParserVersion(t *testing.T) {
	dir := cacheProject(t)
	c := NewScanCache(8)
	first := loadModels(t, dir, nil, c)
	old := scanVersions[".tla"]
	t.Cleanup(func() { scanVersions[".tla"] = old })
	scanVersions[".tla"] = old + "-next"
	assert.NotSame(t, first["tla/M.tla"], loadModels(t, dir, nil, c)["tla/M.tla"])
}

// The cache holds at most its size, and a load past it is still right.
func TestScanCacheIsBounded(t *testing.T) {
	dir := cacheProject(t)
	c := NewScanCache(1)
	for i := 0; i < 3; i++ {
		got := loadModels(t, dir, nil, c)
		require.Len(t, got, 2)
		assert.Equal(t, "N", got["tla/N.tla"].Module)
		assert.LessOrEqual(t, len(c.entries), 1)
	}
}
