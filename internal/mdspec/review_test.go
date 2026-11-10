// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package mdspec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
)

// A qualified reference into the one file with that stem, where the file
// numbers two sections the same, is ambiguous because of the duplicate
// section, not because of the stem. The message must say what to fix.
func TestSectionRefAmbiguousQualifiedDuplicateInOneFile(t *testing.T) {
	src := "## 1. One\n\nSee §x:1 and §1.\n\n## 1. Dup\n"
	s := Build([]*File{ParseFile("x.spec.md", []byte(src))})
	var msgs []string
	for _, d := range s.Diagnostics {
		if d.Code == diag.CodeSectionRefAmbiguous {
			msgs = append(msgs, d.Message)
		}
	}
	require.Len(t, msgs, 2)
	for _, m := range msgs {
		assert.NotContains(t, m, "several spec files")
		assert.Contains(t, m, "Renumber the duplicate section")
	}
}

// Two files with the same stem: neither the qualified nor the unqualified
// form can pick one, so the message asks for a file rename.
func TestSectionRefAmbiguousSharedStem(t *testing.T) {
	s := Build([]*File{
		ParseFile("a/x.spec.md", []byte("## 1. A\n")),
		ParseFile("b/x.spec.md", []byte("## 1. B\n")),
		ParseFile("c.spec.md", []byte("See §x:1 and §1.\n")),
	})
	n := 0
	for _, d := range s.Diagnostics {
		if d.Code == diag.CodeSectionRefAmbiguous {
			n++
			assert.Contains(t, d.Message, "Rename one of those files", d.Message)
			assert.Contains(t, d.Message, "a/x.spec.md, b/x.spec.md", d.Message)
			assert.Len(t, d.Related, 2)
		}
	}
	assert.Equal(t, 2, n)
}
