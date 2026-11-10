// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/project"
)

func formatProject(t *testing.T, toml string, files map[string]string) *project.Project {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spec.toml"), []byte(toml), 0o644))
	for name, text := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644))
	}
	proj, err := project.Open(dir)
	require.NoError(t, err)
	return proj
}

func TestFormat(t *testing.T) {
	proj := formatProject(t, "name = \"f\"\n\n[format]\nwidth = 20\n", map[string]string{
		"a.spec.md": "# A\n\nOne two three four five six seven.\n",
		"b.spec.md": "# B\n\nShort.\n",
		"c.spec.md": "# C\r\n",
	})
	w := &Workflow{}

	res, e := w.Format(proj, nil, true)
	require.Nil(t, e)
	assert.False(t, res.Written)
	assert.Equal(t, []FormatFile{
		{File: "a.spec.md", Changed: 2},
		{File: "b.spec.md"},
		{File: "c.spec.md", Refused: "it has CR line endings; convert them to LF first"},
	}, res.Files)
	assert.Equal(t, []string{"a.spec.md"}, res.Unformatted())
	got, _ := os.ReadFile(filepath.Join(proj.Root, "a.spec.md"))
	assert.Equal(t, "# A\n\nOne two three four five six seven.\n", string(got), "check writes nothing")

	res, e = w.Format(proj, []string{"a.spec.md"}, false)
	require.Nil(t, e)
	assert.True(t, res.Written)
	require.Len(t, res.Files, 1)
	got, _ = os.ReadFile(filepath.Join(proj.Root, "a.spec.md"))
	assert.Equal(t, "# A\n\nOne two three four\nfive six seven.\n", string(got), "wrapped at [format] width")

	_, e = w.Format(proj, []string{"nope.spec.md"}, true)
	require.NotNil(t, e)
	assert.Equal(t, "not_found", e.Code)
}

func TestFormatHoldsTheLock(t *testing.T) {
	proj := formatProject(t, "name = \"f\"\n", map[string]string{"a.spec.md": "# A\n\n\n\nText.\n"})
	lock, err := project.AcquireLock(proj.Root, "test")
	require.NoError(t, err)
	defer func() { _ = lock.Release() }()
	w := &Workflow{}
	_, e := w.Format(proj, nil, false)
	require.NotNil(t, e)
	assert.Equal(t, "lock_held", e.Code)
	res, e := w.Format(proj, nil, true)
	require.Nil(t, e, "a check takes no lock")
	assert.Equal(t, []string{"a.spec.md"}, res.Unformatted())
}
