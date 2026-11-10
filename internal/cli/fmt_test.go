// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spec fmt --check lists unformatted files and exits 4; spec fmt rewrites
// them; a refused file is exit 1 with the reason.
func TestFmt(t *testing.T) {
	dir := demoProject(t)
	write(t, dir, "spec.toml", "name = \"demo\"\n\n[format]\nwidth = 40\n")
	clean := readFile(t, dir, "other.spec.md")

	jr := runJSON(t, dir, "fmt", "--check")
	require.Equal(t, exitCheck, jr.code, jr.stdout)
	assert.Equal(t, "unformatted", jr.errCode)
	var d fmtData
	jr.decode(t, &d)
	assert.True(t, d.Check)
	assert.False(t, d.Written)
	assert.Contains(t, d.Unformatted, "demo.spec.md")

	h := run(t, dir, "fmt", "--check")
	require.Equal(t, exitCheck, h.code)
	assert.Contains(t, h.stdout, "demo.spec.md\n", "gofmt -l style")
	assert.Equal(t, baseSpec, readFile(t, dir, "demo.spec.md"), "--check writes nothing")

	h = run(t, dir, "fmt", "demo.spec.md")
	require.Equal(t, 0, h.code, h.stderr)
	assert.Contains(t, h.stdout, "formatted demo.spec.md: ")
	assert.NotEqual(t, baseSpec, readFile(t, dir, "demo.spec.md"))
	assert.Equal(t, clean, readFile(t, dir, "other.spec.md"), "only the named file")
	jr = runJSON(t, dir, "fmt", "--check", "demo.spec.md")
	require.Equal(t, 0, jr.code, jr.stdout)

	// Hashes are unchanged: nothing to commit as a Spec-Change.
	jr = runJSON(t, dir, "diff")
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Contains(t, string(jr.data), `"changes":[]`)

	write(t, dir, "other.spec.md", "# Other\r\n")
	h = run(t, dir, "fmt")
	require.Equal(t, exitFail, h.code)
	assert.Contains(t, h.stderr, "other.spec.md was not formatted: it has CR line endings")
	jr = runJSON(t, dir, "fmt", "--check")
	require.Equal(t, exitFail, jr.code, "refusal wins over exit 4")
	assert.Equal(t, "not_formatted", jr.errCode)

	h = run(t, dir, "fmt", "notes/README.md")
	require.Equal(t, exitFail, h.code)
	assert.Contains(t, h.stderr, "not a spec file of this project")
}
