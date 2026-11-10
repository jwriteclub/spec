// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/lint"
)

const formalSpec = "# A\n\n**$Frame.Timing.** Decode time MUST add up.\n\n## 1.2 Fragments\n\nText.\n"

const formalModel = "---- MODULE M ----\n\\* Spec: $Frame.Timing \"decode time\"\nInv_A == TRUE\n====\n"

func TestParseConfigFormal(t *testing.T) {
	cfg, ds := ParseConfig([]byte(`formal = ["tla/*.tla", "models/**/*.tla"]`))
	assert.Empty(t, ds)
	assert.Equal(t, []string{"tla/*.tla", "models/**/*.tla"}, cfg.Formal)

	for _, bad := range []string{`"/abs/*.tla"`, `"../up/*.tla"`, `"a\\b.tla"`, `""`, `"[.tla"`} {
		_, ds := ParseConfig([]byte("formal = [" + bad + "]"))
		require.Len(t, ds, 1, bad)
		assert.Equal(t, diag.CodeConfigInvalid, ds[0].Code)
		assert.Contains(t, ds[0].Message, "formal entry")
	}
	_, ds = ParseConfig([]byte("formal = 3"))
	assert.Equal(t, []string{diag.CodeConfigInvalid}, codes(ds))
	_, ds = ParseConfig([]byte("nope = 3"))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "formal", "the known keys list it")
}

func TestModelsAreASeparateSet(t *testing.T) {
	dir := tree(t, map[string]string{
		"spec.toml":          `formal = ["tla/*.tla"]`,
		"a.spec.md":          formalSpec,
		"tla/M.tla":          formalModel,
		"tla/M_Mut1_Bad.tla": "---- MODULE M_Mut1_Bad ----\n====\n",
		"tla/M.cfg":          "INIT Init\n",
		"tla/README.md":      "not selected\n",
		"other/N.tla":        formalModel,
		"vendor/tla/V.tla":   formalModel,
	})
	p, err := Open(dir)
	require.NoError(t, err)
	assert.Empty(t, p.Diagnostics)
	assert.Equal(t, []string{"a.spec.md"}, p.Files, "a model is never a spec file")
	assert.Equal(t, []string{"tla/M.tla", "tla/M_Mut1_Bad.tla"}, p.Formal)

	l, err := p.Load(nil)
	require.NoError(t, err)
	assert.Len(t, l.Spec.Files, 1)
	assert.Nil(t, l.Spec.File("tla/M.tla"))
	require.Len(t, l.Formal.Files, 2)
	assert.Equal(t, "M", l.Formal.Files[0].Module)
	assert.True(t, l.Formal.Covered("Frame.Timing"))
	assert.Empty(t, l.Formal.Lint())
}

func TestNoFormalKeyMeansNoModels(t *testing.T) {
	dir := tree(t, map[string]string{"spec.toml": "", "a.spec.md": formalSpec, "tla/M.tla": formalModel})
	l, err := Load(dir, nil)
	require.NoError(t, err)
	assert.Empty(t, l.Project.Formal)
	assert.NotNil(t, l.Formal)
	assert.Empty(t, l.Formal.Files)
}

func TestFormalEntryMatchingNothing(t *testing.T) {
	dir := tree(t, map[string]string{
		"spec.toml": `formal = ["tla/*.tla", "als/*.als"]`, "a.spec.md": formalSpec, "tla/M.tla": formalModel, "als/X.als": "x",
	})
	p, err := Open(dir)
	require.NoError(t, err)
	require.Len(t, p.Diagnostics, 1, "als has no scanner, so its entry selects nothing")
	assert.Equal(t, diag.CodeProjectFileMissing, p.Diagnostics[0].Code)
	assert.Contains(t, p.Diagnostics[0].Message, "als/*.als")
	assert.Contains(t, p.Diagnostics[0].Message, ".tla")
}

func TestFormalEntryMatchingOnlySkippedDirs(t *testing.T) {
	dir := tree(t, map[string]string{
		"spec.toml": `formal = ["**/*.tla"]`, "a.spec.md": formalSpec, "vendor/M.tla": formalModel,
	})
	p, err := Open(dir)
	require.NoError(t, err)
	require.Len(t, p.Diagnostics, 1)
	assert.Equal(t, diag.CodeProjectFileMissing, p.Diagnostics[0].Code)
	assert.Contains(t, p.Diagnostics[0].Message, "only files in skipped directories")
}

func TestFileSelectedByBothStaysASpecFile(t *testing.T) {
	dir := tree(t, map[string]string{
		"spec.toml": "files = [\"**/*\"]\nformal = [\"tla/*.tla\"]", "a.spec.md": formalSpec, "tla/M.tla": formalModel,
	})
	p, err := Open(dir)
	require.NoError(t, err)
	assert.Contains(t, p.Files, "tla/M.tla")
	assert.Empty(t, p.Formal)
	assert.Contains(t, codes(p.Diagnostics), diag.CodeConfigInvalid)
}

func TestFormalSymlink(t *testing.T) {
	dir := tree(t, map[string]string{"spec.toml": `formal = ["tla/*.tla"]`, "a.spec.md": formalSpec, "real.txt": formalModel})
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tla"), 0o755))
	if err := os.Symlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "tla", "L.tla")); err != nil {
		t.Skip("no symlinks here")
	}
	p, err := Open(dir)
	require.NoError(t, err)
	assert.Empty(t, p.Formal)
	assert.Equal(t, []string{diag.CodeFileSymlink}, codes(p.Diagnostics))
}

func TestOverlayReachesModels(t *testing.T) {
	dir := tree(t, map[string]string{"spec.toml": `formal = ["tla/*.tla"]`, "a.spec.md": formalSpec, "tla/M.tla": formalModel})
	edited := "---- MODULE M ----\n\\* Spec: $Nope.Name\nA == 1\n====\n"
	unsaved := "---- MODULE U ----\n====\n"
	l, err := Load(dir, map[string][]byte{"tla/M.tla": []byte(edited), "tla/U.tla": []byte(unsaved), "tla/x.cfg": []byte("y")})
	require.NoError(t, err)
	assert.Len(t, l.Spec.Files, 1, "the overlay does not make a model a spec file")
	require.Len(t, l.Formal.Files, 2)
	assert.Equal(t, edited, string(l.Formal.File("tla/M.tla").Source()))
	assert.NotNil(t, l.Formal.File("tla/U.tla"), "an unsaved model counts")
	assert.False(t, l.Formal.Covered("Frame.Timing"))
}

func TestModelDeletedAfterOpenIsSkipped(t *testing.T) {
	dir := tree(t, map[string]string{"spec.toml": `formal = ["tla/*.tla"]`, "a.spec.md": formalSpec, "tla/M.tla": formalModel})
	p, err := Open(dir)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(dir, "tla", "M.tla")))
	l, err := p.Load(nil)
	require.NoError(t, err)
	assert.Empty(t, l.Formal.Files)
}

func TestLintOptionsCombine(t *testing.T) {
	dir := tree(t, map[string]string{
		"spec.toml": "formal = [\"tla/*.tla\"]\n[lint]\nchecked-by-missing = \"warning\"\nformal-tag-block = \"off\"\nformal-quote-stale = \"error\"\n",
		"a.spec.md": formalSpec + "\n**$Other.Thing.** Uncovered, as it says.\n",
		"tla/M.tla": "---- MODULE M ----\n\\* Spec: $Frame.Timing \"not in there\"\n(* Spec: §1.2 *)\nInv_A == TRUE\n====\n",
	})
	l, err := Load(dir, nil)
	require.NoError(t, err)
	before := len(l.Project.Diagnostics)
	ds := lint.Run(l.Spec, l.LintOptions())
	assert.Len(t, l.Project.Diagnostics, before, "LintOptions must not grow the project's list")

	got := map[string]diag.Diagnostic{}
	for _, d := range ds {
		got[d.Code] = d
	}
	assert.NotContains(t, got, diag.CodeFormalTagBlock, "off in [lint]")
	assert.Equal(t, diag.Error, got[diag.CodeFormalQuoteStale].Severity, "overridden in [lint]")
	require.Contains(t, got, diag.CodeCheckedByMissing)
	assert.Contains(t, got[diag.CodeCheckedByMissing].Message, "$Other.Thing", "a tag satisfies it for $Frame.Timing")
}

// A model that cannot be read fails no spec operation: lint reports it.
func TestUnreadableModelDoesNotFailLoad(t *testing.T) {
	dir := tree(t, map[string]string{"spec.toml": `formal = ["tla/*.tla"]`, "a.spec.md": formalSpec, "tla/M.tla": formalModel})
	path := filepath.Join(dir, "tla", "M.tla")
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this user can read a file with mode 0")
	}
	l, err := Load(dir, nil)
	require.NoError(t, err)
	assert.NotNil(t, l.Spec.File("a.spec.md"))
	ds := l.Formal.Lint()
	require.Len(t, ds, 1)
	assert.Equal(t, diag.CodeFormalTagMalformed, ds[0].Code)
	assert.Equal(t, "tla/M.tla", ds[0].File)
	assert.Contains(t, ds[0].Message, "could not be read")
}
