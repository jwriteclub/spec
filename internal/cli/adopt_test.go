// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/explain"
)

// TestAdoptCommand checks that `spec adopt`, the coding agent's entry point,
// names how to start and every explain topic, needs no project, and carries
// the same text and topics in --json.
func TestAdoptCommand(t *testing.T) {
	dir := t.TempDir()
	r := run(t, dir, "adopt")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Empty(t, r.stderr)
	for _, want := range []string{"spec init", "spec lint", "spec doctor", "spec explain agent",
		"spec explain adopt", "spec explain ", "spec --help", "spec COMMAND --help", "*.spec.md", "§"} {
		assert.Contains(t, r.stdout, want)
	}
	for _, n := range explain.Names() {
		assert.Contains(t, r.stdout, "spec explain "+n+" ", "spec adopt does not list topic %q", n)
	}

	jr := runJSON(t, dir, "adopt")
	require.Equal(t, 0, jr.code)
	assert.Empty(t, jr.diagnostics)
	var d adoptData
	jr.decode(t, &d)
	assert.Equal(t, r.stdout, d.Text)
	want, err := explain.Summaries()
	require.NoError(t, err)
	assert.Equal(t, want, d.Topics)
	require.Len(t, d.Topics, len(explain.Names()))
	for i, n := range explain.Names() {
		assert.Equal(t, n, d.Topics[i].Name)
		assert.NotEmpty(t, d.Topics[i].Summary)
	}

	bad := run(t, dir, "adopt", "extra")
	assert.NotEqual(t, 0, bad.code)
}
