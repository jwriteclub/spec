// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// When the matching sections cannot be told apart by their qualified form
// (one file numbers two sections the same, or two files share a stem), the
// ambiguous candidates must be locations the user can act on, not the same
// "§x:1" twice.
func TestShowAmbiguousSectionCandidatesAreDistinct(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec.toml", "name = \"p\"\n")
	write(t, dir, "x.spec.md", "## 1. One\n\n## 1. Dup\n")
	write(t, dir, "a/y.spec.md", "## 2. A\n")
	write(t, dir, "b/y.spec.md", "## 2. B\n")
	for _, arg := range []string{"1", "x:1", "y:2", "2"} {
		jr := runJSON(t, dir, "show", arg)
		require.Equal(t, 1, jr.code, arg)
		assert.Equal(t, "ambiguous", jr.errCode, arg)
		var data struct {
			Candidates []string `json:"candidates"`
		}
		jr.decode(t, &data)
		require.Len(t, data.Candidates, 2, arg)
		assert.NotEqual(t, data.Candidates[0], data.Candidates[1], arg)
		assert.NotContains(t, jr.errMessage, "write one of them", arg)
	}
}

// pflag turns a back-quoted word in a flag's usage into the flag's value
// name, so a boolean flag must not quote anything in its usage.
func TestHelpShowsJSONFlagWithoutValueName(t *testing.T) {
	r := run(t, t.TempDir(), "--help")
	require.Equal(t, 0, r.code)
	assert.NotContains(t, r.stdout, "--json spec explain json")
	assert.Contains(t, r.stdout, "--json ")
}
