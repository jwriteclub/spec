// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tla

import (
	"testing"

	"github.com/jwriteclub/prudence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
)

// A lexical error after the closing ==== is prudence's informational
// lexical/trailing: a hint at its place, never a syntax error.
func TestTrailingLexicalIsAHint(t *testing.T) {
	src := wrap("A == 1") + "` stray\n"
	f := Scan("tla/M.tla", []byte(src))
	var got []string
	for _, p := range f.Syntax {
		if p.Code == prudence.CodeTrailingLexical {
			got = append(got, src[p.Range.Start:p.Range.End])
			assert.Equal(t, diag.Hint, p.Severity)
		}
	}
	require.NotEmpty(t, got, "%+v", f.Syntax)
	assert.Equal(t, "`", got[0])
	assert.False(t, f.HasError())
	assert.Len(t, f.Decls, 1)
}
