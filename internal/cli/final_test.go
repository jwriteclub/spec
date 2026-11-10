// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `spec show $Frame.Timing` reaches the command with no argument when the
// shell expands $Frame.Timing to nothing, so a missing argument carries the
// shell hint. Too many arguments do not: that is a different mistake.
func TestMissingArgumentCarriesShellHint(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"show"}, {"refs"}, {"rename"}, {"rename", "Ab.Cd"}} {
		r := run(t, dir, args...)
		require.Equal(t, 1, r.code, args)
		assert.Contains(t, r.stderr, "usage: spec "+args[0], args)
		assert.Contains(t, r.stderr, "did your shell expand", args)
	}
	r := run(t, dir, "show", "Ab.Cd", "Ef.Gh")
	require.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "usage: spec show")
	assert.NotContains(t, r.stderr, "did your shell expand")
}
