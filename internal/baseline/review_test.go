// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/mdspec"
)

func specOf(files map[string]string) *mdspec.Spec {
	var fs []*mdspec.File
	for p, src := range files {
		fs = append(fs, mdspec.ParseFile(p, []byte(src)))
	}
	return mdspec.Build(fs)
}

// A rename rewrites references inside other anchors' titles too. The
// compensation of plan 4 step 2 must cover them, or every anchor whose
// title mentions the renamed one shows up as "retitled".
func TestDiffRenameCompensatesTitles(t *testing.T) {
	before := "**Def $Ab.Cd (thing).** A thing.\n\n" +
		"**$Ef.Gh (about $Ab.Cd).** Every $Ab.Cd holds.\n\n" +
		"## $Ij.Kl: section form on $Ab.Cd\nBody mentions $Ab.Cd and $Ef.Gh.\n"
	after := strings.ReplaceAll(before, "$Ab.Cd", "$Xy.Zw")
	d := Compare(specOf(map[string]string{"t.spec.md": before}), specOf(map[string]string{"t.spec.md": after}))
	require.Len(t, d.Entries, 1, "%+v", d.Entries)
	assert.Equal(t, Renamed, d.Entries[0].Status)
	assert.Equal(t, "Xy.Zw", d.Entries[0].Name)
	assert.False(t, d.Entries[0].Retitled)
	assert.Equal(t, 2, d.Unchanged)

	// A real retitle alongside the rename is still reported.
	after2 := strings.Replace(after, "(about $Xy.Zw)", "(regarding $Xy.Zw)", 1)
	d = Compare(specOf(map[string]string{"t.spec.md": before}), specOf(map[string]string{"t.spec.md": after2}))
	assert.Equal(t, []string{"Ef.Gh"}, entryNames(d, Retitled))
	assert.Equal(t, "about $Ab.Cd", entry(t, d, "Ef.Gh").OldTitle)
}

// `git commit -v` hands the commit-msg hook a message that holds the
// staged diff below a scissors line. Git drops everything below it, so a
// trailer-like line there must not count as present.
func TestParseMessageStopsAtScissors(t *testing.T) {
	msg := "subject\n\n" +
		"# ------------------------ >8 ------------------------\n" +
		"# Do not modify or remove the line above.\n" +
		"# Everything below it will be ignored.\n" +
		"diff --git a/x.md b/x.md\n" +
		"@@ -1,2 +1,2 @@\n" +
		" Spec-Rename: $Ab.Cd -> $Xy.Zw\n" +
		" Spec-Change: $Name <kind>:<old8> -> <kind>:<new8>\n"
	have, bad := ParseMessage(msg)
	assert.Empty(t, have.Renames)
	assert.Empty(t, have.Changes)
	assert.Empty(t, bad)

	req := Trailers{Renames: []RenameLine{{Old: "Ab.Cd", New: "Xy.Zw"}}}
	assert.False(t, Check(req, msg).OK())
	// The same line above the scissors counts.
	assert.True(t, Check(req, "subject\n\nSpec-Rename: $Ab.Cd -> $Xy.Zw\n"+msg[len("subject\n\n"):]).OK())
}
