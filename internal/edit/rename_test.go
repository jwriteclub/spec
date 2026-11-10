// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package edit

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
)

func buildSpec(files map[string]string) *mdspec.Spec {
	var fs []*mdspec.File
	for p, s := range files {
		fs = append(fs, mdspec.ParseFile(p, []byte(s)))
	}
	return mdspec.Build(fs)
}

func TestRenameWholeNamesOnly(t *testing.T) {
	spec := buildSpec(map[string]string{
		"a.spec.md": "**$Frame.Timing.** Body. See $Frame.Timing and $Frame.Timing's.\n\n**Def $Frame.Timing.Extra.** x\n\n**$Frame.Ab.** Uses $Frame.Timing.Extra.\n",
	})
	res, err := PlanRename(t.TempDir(), spec, "$Frame.Timing", "$Frame.Clock")
	require.NoError(t, err)
	assert.Equal(t, 1, res.Definitions)
	assert.Equal(t, 2, res.References)
	require.Len(t, res.Plan.Outputs, 1)
	assert.Equal(t, "**$Frame.Clock.** Body. See $Frame.Clock and $Frame.Clock's.\n\n**Def $Frame.Timing.Extra.** x\n\n**$Frame.Ab.** Uses $Frame.Timing.Extra.\n",
		string(res.Plan.Outputs[0].After))
}

func TestRenameInTitlesAndSectionForm(t *testing.T) {
	src := "### $Relay.One: depends on $Relay.One too\n\nBody $Relay.One.\n\n**$Other.Thing (about $Relay.One).** Body.\n"
	spec := buildSpec(map[string]string{"a.spec.md": src})
	res, err := PlanRename(t.TempDir(), spec, "Relay.One", "Relay.Two")
	require.NoError(t, err)
	assert.Equal(t, strings.ReplaceAll(src, "Relay.One", "Relay.Two"), string(res.Plan.Outputs[0].After))
	assert.Equal(t, 4, res.Tokens())
}

func TestRenameReportsOpaqueMentionsLeftAlone(t *testing.T) {
	src := "**$Frame.Timing.** Body `$Frame.Timing` in code and $Frame.Timing out.\n\n```\n$Frame.Timing and $Frame.Timing.Extra\n```\n"
	spec := buildSpec(map[string]string{"a.spec.md": src})
	res, err := PlanRename(t.TempDir(), spec, "Frame.Timing", "Frame.Clock")
	require.NoError(t, err)
	assert.Equal(t, 2, res.Tokens())
	require.Len(t, res.LeftAlone, 2)
	for _, l := range res.LeftAlone {
		assert.Equal(t, "$Frame.Timing", src[l.Range.Start:l.Range.End])
		assert.Equal(t, "a.spec.md", l.File)
	}
	assert.Contains(t, string(res.Plan.Outputs[0].After), "`$Frame.Timing` in code")
}

func TestRenameErrors(t *testing.T) {
	spec := buildSpec(map[string]string{
		"a.spec.md": "**$Ab.Cd.** x\n\n**$Ef.Gh.** y\n\n**$Dup.One.** z\n\n**$Dup.One.** z2\n",
	})
	_, err := PlanRename(t.TempDir(), spec, "Nope.Name", "Ab.New")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = PlanRename(t.TempDir(), spec, "Ab.Cd", "Ef.Gh")
	assert.ErrorIs(t, err, ErrNameExists)
	_, err = PlanRename(t.TempDir(), spec, "Ab.Cd", "Ab.Cd")
	assert.ErrorIs(t, err, ErrNameExists)
	_, err = PlanRename(t.TempDir(), spec, "Ab.Cd", "frame.timing")
	assert.ErrorIs(t, err, ErrInvalidName)
	_, err = PlanRename(t.TempDir(), spec, "Ab.Cd", "HOME")
	assert.ErrorIs(t, err, ErrInvalidName)
	_, err = PlanRename(t.TempDir(), spec, "Dup.One", "Dup.Two")
	assert.ErrorIs(t, err, ErrAmbiguous)
}

func TestRenamePreservesBOMAndCRLF(t *testing.T) {
	src := "\xef\xbb\xbf**$Ab.Cd.** x\r\n\r\nSee $Ab.Cd.\r\n"
	spec := buildSpec(map[string]string{"a.spec.md": src})
	res, err := PlanRename(t.TempDir(), spec, "Ab.Cd", "Ab.Ef")
	require.NoError(t, err)
	assert.Equal(t, strings.ReplaceAll(src, "Ab.Cd", "Ab.Ef"), string(res.Plan.Outputs[0].After))
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		return os.WriteFile(dst, b, 0o644)
	}))
}

func loadDir(t *testing.T, root string) *mdspec.Spec {
	t.Helper()
	ents, err := os.ReadDir(root)
	require.NoError(t, err)
	var files []*mdspec.File
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".spec.md") {
			b, err := os.ReadFile(filepath.Join(root, e.Name()))
			require.NoError(t, err)
			files = append(files, mdspec.ParseFile(e.Name(), b))
		}
	}
	return mdspec.Build(files)
}

// TestRenameAcrossMultiFixture renames $Rws.Session, defined in rws and
// referenced from all three files, and checks lint stays clean and only the
// expected tokens changed.
func TestRenameAcrossMultiFixture(t *testing.T) {
	root := t.TempDir()
	copyDir(t, "../../testdata/multi", root)
	before := map[string]string{}
	for _, n := range []string{"rws", "index", "hls"} {
		b, err := os.ReadFile(filepath.Join(root, n+".spec.md"))
		require.NoError(t, err)
		before[n+".spec.md"] = string(b)
	}

	spec := loadDir(t, root)
	require.Empty(t, lint.Run(spec, lint.Options{}))
	res, err := PlanRename(root, spec, "Rws.Session", "Rws.Connection")
	require.NoError(t, err)
	assert.Equal(t, 1, res.Definitions)
	assert.Positive(t, res.References)
	assert.Len(t, res.PerFile, 3, "definition and references span all three files")
	require.NoError(t, res.Plan.Commit())

	for name, old := range before {
		got, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		assert.Equal(t, strings.ReplaceAll(old, "$Rws.Session", "$Rws.Connection"), string(got), name)
	}
	after := loadDir(t, root)
	assert.Empty(t, lint.Run(after, lint.Options{}), "lint is clean after the rename")
	assert.Nil(t, after.Anchor("Rws.Session"))
	require.NotNil(t, after.Anchor("Rws.Connection"))

	// A rename moves no referencer's hash once references are compensated.
	var names []string
	for _, a := range spec.Anchors {
		old := a.HashWithRenames(map[string]string{"Rws.Session": "Rws.Connection"})
		nn := a.Name
		if nn == "Rws.Session" {
			nn = "Rws.Connection"
		}
		na := after.Anchor(nn)
		require.NotNil(t, na)
		if old != na.Hash {
			names = append(names, a.Name)
		}
	}
	sort.Strings(names)
	assert.Empty(t, names)
	assert.False(t, diag.HasErrors(lint.Run(after, lint.Options{})))
}

// TestRenameReportsSkippedInvalidUTF8: a spec file that is not valid UTF-8
// is excluded from the build, so rename cannot rewrite it; it is reported,
// and flagged when its bytes contain the old name.
func TestRenameReportsSkippedInvalidUTF8(t *testing.T) {
	spec := buildSpec(map[string]string{
		"a.spec.md":   "**$Frame.Timing.** Body.\n",
		"bad.spec.md": "Uses $Frame.Timing here \xff\xfe.\n",
		"odd.spec.md": "Nothing relevant \xff.\n",
	})
	res, err := PlanRename(t.TempDir(), spec, "Frame.Timing", "Frame.Clock")
	require.NoError(t, err)
	assert.Equal(t, 1, res.Tokens())
	assert.Equal(t, []SkippedFile{
		{File: "bad.spec.md", Reason: "not valid UTF-8", MayReference: true},
		{File: "odd.spec.md", Reason: "not valid UTF-8", MayReference: false},
	}, res.Skipped)
}
