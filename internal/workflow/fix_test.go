// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// TestEveryRuleHasAFix walks the rule table: every rule has a class, and
// every rule that may ask the model has its instruction file.
func TestEveryRuleHasAFix(t *testing.T) {
	for _, r := range lint.Rules() {
		class, ok := FixClassOf(r.Code)
		if !assert.True(t, ok, "rule %s has no fix class", r.Code) {
			continue
		}
		switch class {
		case FixModel:
			assert.True(t, FixUsesModel(r.Code), r.Code)
		case FixMechanical:
			assert.NotNil(t, fixRules[r.Code].mechanical, "mechanical rule %s has no Go fix", r.Code)
		case FixManual:
			assert.NotEmpty(t, FixManualText(r.Code), "manual rule %s says nothing to do by hand", r.Code)
			assert.False(t, FixUsesModel(r.Code), r.Code)
		default:
			t.Errorf("rule %s: unknown class %q", r.Code, class)
		}
		if FixUsesModel(r.Code) {
			text, ok := prompt.FixInstruction(r.Code)
			assert.True(t, ok, "rule %s may ask the model but has no prompts/fix/%s.md", r.Code, r.Code)
			assert.NotEmpty(t, strings.TrimSpace(text), r.Code)
		}
	}
	for code := range fixRules {
		assert.True(t, lint.Known(code), "the fix table names %s, which is no rule", code)
	}
}

// The plan's classes.
func TestFixClasses(t *testing.T) {
	want := map[FixClass][]string{
		FixModel: {diag.CodeDefUnreferenced, diag.CodeAnchorLong, diag.CodeRefDangling, diag.CodeDefDuplicate,
			diag.CodeDefMalformed, diag.CodeDefNested, diag.CodeSectionDup, diag.CodeSectionRefAmbiguous,
			diag.CodeRefInTitle, diag.CodeCheckedByMissing, diag.CodeAnchorCodeAfter, diag.CodeSectionRefDangling},
		FixMechanical: {diag.CodeMathHazard, diag.CodeFileBOM, diag.CodeSectionHashRef, diag.CodeRefNearly,
			diag.CodeSectionRefMalformed},
		FixManual: {diag.CodeProjectNoFiles, diag.CodeProjectFileMissing, diag.CodeFileNotUTF8, diag.CodeFileSymlink,
			diag.CodeConfigInvalid, diag.CodeFileStemDuplicate, diag.CodeFormalSyntax, diag.CodeFormalSemantic, diag.CodeFormalRefDangling, diag.CodeFormalOpUncited,
			diag.CodeFormalTagMalformed, diag.CodeFormalTagBlock, diag.CodeFormalQuoteStale, diag.CodeFormalCheckLimit, diag.CodeSpell},
	}
	n := 0
	for class, codes := range want {
		for _, c := range codes {
			got, _ := FixClassOf(c)
			assert.Equal(t, class, got, c)
			n++
		}
	}
	assert.Equal(t, len(lint.Rules()), n)
	assert.True(t, FixUsesModel(diag.CodeRefNearly), "a prefix match falls back to the model")
	assert.False(t, FixUsesModel(diag.CodeMathHazard))
}

const fixSpec = "# fix\n\n## 1.1 Terms\n\n" +
	"**Def $Time.Wall (wall time).** Wall-clock time of the host.\n\n" +
	"**Def $Time.Media (media time).** Ticks of 1/90000 s.\n\n" +
	"## 2. Rules\n\n" +
	"**$Frame.Order (order).** Frames follow $Time.Media order, at most 5$ apart, see #1.1 and §2for details.\n\n" +
	"**$Frame.Gap (gap).** Gaps follow $time.media and $Time.Medai.\n"

// fixProject writes a project with fixSpec (and extra files).
func fixProject(t *testing.T, files map[string]string) *project.Project {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spec.toml"), []byte("name = \"fix\"\n"), 0o644))
	if files == nil {
		files = map[string]string{"fix.spec.md": fixSpec}
	}
	for name, text := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644))
	}
	proj, err := project.Open(dir)
	require.NoError(t, err)
	return proj
}

// lineCol returns the 1-based line and byte column of the first needle.
func lineCol(t *testing.T, src, needle string) (int, int) {
	t.Helper()
	i := strings.Index(src, needle)
	require.GreaterOrEqual(t, i, 0, needle)
	line := strings.Count(src[:i], "\n") + 1
	return line, i - strings.LastIndex(src[:i], "\n")
}

// applyHunks applies a proposal's hunks to src the way approving does.
func applyHunks(t *testing.T, src string, p *proposal.Proposal) string {
	t.Helper()
	for _, h := range p.Hunks {
		require.Equal(t, 1, strings.Count(src, h.Find), "hunk %d's find occurs once", h.N)
		src = strings.Replace(src, h.Find, h.Replace, 1)
	}
	return src
}

func TestMechanicalFixes(t *testing.T) {
	proj := fixProject(t, nil)
	w := &Workflow{Getenv: func(string) string { return "" }}
	cases := []struct {
		needle, code, want, why string
	}{
		{"$ apart", diag.CodeMathHazard, "at most `5$` apart", "Fix math-hazard: put 5$ in backticks"},
		{"#1.1", diag.CodeSectionHashRef, "see §1.1 and", "Fix section-hash-ref: write §1.1 for #1.1"},
		{"§2for", diag.CodeSectionRefMalformed, "§2 for details", "Fix section-ref-malformed: correct §2for"},
		{"$time.media", diag.CodeRefNearly, "follow $Time.Media and", "Fix ref-nearly: correct $time.media"},
		{"$Time.Medai", diag.CodeRefDangling, "and $Time.Media.\n", "Fix ref-dangling: define or correct $Time.Medai"},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			line, col := lineCol(t, fixSpec, c.needle)
			res, e := w.Fix(context.Background(), proj, FixRequest{File: "fix.spec.md", Line: line, Col: col, Code: c.code})
			require.Nil(t, e)
			assert.Equal(t, FixMechanical, res.Class)
			p := res.Proposal
			assert.Equal(t, proposal.KindFix, p.Kind)
			assert.Equal(t, c.why, p.Why)
			require.NotNil(t, p.Fix)
			assert.Equal(t, c.code, p.Fix.Code)
			assert.Equal(t, line, p.Fix.Line)
			assert.Equal(t, "mechanical", p.Fix.Class)
			assert.Nil(t, p.Inference, "no model")
			require.Len(t, p.Hunks, 1)
			assert.Equal(t, c.code, p.Hunks[0].Issue)
			assert.NotEmpty(t, p.Hunks[0].Explanation)
			assert.Contains(t, applyHunks(t, fixSpec, p), c.want)
			assert.Equal(t, "fix.spec.md", p.Hunks[0].File)
			assert.NotEmpty(t, p.Base.Files["fix.spec.md"])
		})
	}
}

// The math-hazard fix wraps the word in backticks and drops a backslash
// before the $ (it does not escape it); the old name ref-adjacent picks the
// finding too; a line GitHub renders as text has no finding to fix.
func TestFixMathHazard(t *testing.T) {
	src := "# m\n\n**Def $Time.Media.** Ticks.\n\nSee $Time.Media and x\\$ ok.\n\n" +
		"($Time.Media). From the instant lensd detects it ($Time.Media), requests\n"
	proj := fixProject(t, map[string]string{"m.spec.md": src})
	w := &Workflow{}
	res, e := w.Fix(context.Background(), proj, FixRequest{File: "m.spec.md", Line: 5, Code: "ref-adjacent"})
	require.Nil(t, e)
	assert.Equal(t, diag.CodeMathHazard, res.Diagnostic.Code)
	assert.Equal(t, "Fix math-hazard: put x\\$ in backticks", res.Proposal.Why)
	assert.Contains(t, applyHunks(t, src, res.Proposal), "See $Time.Media and `x$` ok.\n")

	_, e = w.Fix(context.Background(), proj, FixRequest{File: "m.spec.md", Line: 7})
	require.NotNil(t, e)
	assert.Equal(t, "not_found", e.Code, "GitHub renders the line as text")
}

func TestFixBOM(t *testing.T) {
	proj := fixProject(t, map[string]string{"a.spec.md": byteOrderMark + "# a\n\nText.\n"})
	w := &Workflow{}
	res, e := w.Fix(context.Background(), proj, FixRequest{File: "a.spec.md", Line: 1})
	require.Nil(t, e)
	require.Len(t, res.Proposal.Hunks, 1)
	assert.Equal(t, "# a\n\nText.\n", applyHunks(t, byteOrderMark+"# a\n\nText.\n", res.Proposal))
	assert.Equal(t, "Fix file-bom: remove the byte order mark from a.spec.md", res.Proposal.Why)
}

// A hunk's find is widened until it occurs once.
func TestLineHunkUnique(t *testing.T) {
	src := "# r\n\n## 1. A\n\nSee $Time.Media costs 5$.\n\nSee $Time.Media costs 5$.\nend\n\n**Def $Time.Media.** T.\n"
	proj := fixProject(t, map[string]string{"r.spec.md": src})
	w := &Workflow{}
	res, e := w.Fix(context.Background(), proj, FixRequest{File: "r.spec.md", Line: 7, Code: diag.CodeMathHazard})
	require.Nil(t, e)
	h := res.Proposal.Hunks[0]
	assert.Equal(t, "See $Time.Media costs 5$.\nend", h.Find)
	assert.Equal(t, "See $Time.Media costs `5$`.\nend", h.Replace)
	assert.Equal(t, 1, strings.Count(src, h.Find))
}

func TestFixPicksTheFinding(t *testing.T) {
	proj := fixProject(t, nil)
	w := &Workflow{}
	line, _ := lineCol(t, fixSpec, "$Time.Medai")
	// The line has a hint (ref-nearly) and an error (ref-dangling): the
	// error wins.
	res, e := w.Fix(context.Background(), proj, FixRequest{File: "fix.spec.md", Line: line})
	require.Nil(t, e)
	assert.Equal(t, diag.CodeRefDangling, res.Diagnostic.Code)

	_, e = w.Fix(context.Background(), proj, FixRequest{File: "fix.spec.md", Line: 1})
	require.NotNil(t, e)
	assert.Equal(t, "not_found", e.Code)
	assert.Contains(t, e.Message, "no lint finding at fix.spec.md:1")
	_, e = w.Fix(context.Background(), proj, FixRequest{File: "fix.spec.md", Line: line, Code: diag.CodeAnchorLong})
	require.NotNil(t, e)
	assert.Contains(t, e.Message, "no anchor-long finding at")
	_, e = w.Fix(context.Background(), proj, FixRequest{File: "nope.spec.md", Line: 1})
	require.NotNil(t, e)
	assert.Equal(t, "not_found", e.Code)
}

func TestFixNotFixable(t *testing.T) {
	proj := fixProject(t, map[string]string{"a.spec.md": "# a\n\nText.\n", "spec.toml": "name = \"x\"\nfiles = [\"a.spec.md\", \"gone.spec.md\"]\n"})
	w := &Workflow{}
	var d diag.Diagnostic
	for _, x := range proj.Diagnostics {
		if x.Code == diag.CodeProjectFileMissing {
			d = x
		}
	}
	require.Equal(t, diag.CodeProjectFileMissing, d.Code, "%v", proj.Diagnostics)
	_, e := w.Fix(context.Background(), proj, FixRequest{File: d.File, Line: 1})
	require.NotNil(t, e)
	assert.Equal(t, "not_fixable", e.Code)
	assert.Contains(t, e.Message, FixManualText(diag.CodeProjectFileMissing))
}

func TestNearNames(t *testing.T) {
	proj := fixProject(t, nil)
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"Time.Media"}, NearNames(loaded.Spec, "Time.Medai"))
	assert.Equal(t, []string{"Time.Media"}, NearNames(loaded.Spec, "TIME.MEDIA"))
	assert.Empty(t, NearNames(loaded.Spec, "Time.X"))
	assert.Equal(t, 1, editDistance("kitten", "sitten"))
	assert.Equal(t, 3, editDistance("kitten", "sitting"))
}

func TestMostSevere(t *testing.T) {
	_, ok := MostSevere(nil)
	assert.False(t, ok)
	d, _ := MostSevere([]diag.Diagnostic{{Code: "a", Severity: diag.Hint}, {Code: "b", Severity: diag.Warning}, {Code: "c", Severity: diag.Warning}})
	assert.Equal(t, "b", d.Code)
}

func TestFixCommitSubject(t *testing.T) {
	p := &proposal.Proposal{ID: "k3x7a", Kind: proposal.KindFix, Fix: &proposal.Fix{Code: "def-unreferenced", File: "lensd.spec.md"}}
	assert.Equal(t, "spec: fix def-unreferenced in lensd.spec.md (proposal k3x7a)", CommitSubject(p))
}
