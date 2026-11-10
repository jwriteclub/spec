// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lint

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	_ "github.com/jwriteclub/spec/internal/golden"
	"github.com/jwriteclub/spec/internal/mdspec"
)

// build parses the given path -> source files and resolves them.
func build(files map[string]string) *mdspec.Spec {
	var fs []*mdspec.File
	for p, src := range files {
		fs = append(fs, mdspec.ParseFile(p, []byte(src)))
	}
	return mdspec.Build(fs)
}

func one(src string) *mdspec.Spec {
	return build(map[string]string{"a.spec.md": src})
}

func codes(ds []diag.Diagnostic) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Code)
	}
	sort.Strings(out)
	return out
}

func only(ds []diag.Diagnostic, code string) []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, d := range ds {
		if d.Code == code {
			out = append(out, d)
		}
	}
	return out
}

func text(spec *mdspec.Spec, d diag.Diagnostic) string {
	return spec.File(d.File).Text(d.Range)
}

func TestCleanSpecHasNoDiagnostics(t *testing.T) {
	spec := one("**Def $Time.Media.** Media time.\n\n**$Frame.Timing.** Uses $Time.Media. See §1.\n\n## 1. Frames\n")
	assert.Empty(t, Run(spec, Options{}))
}

func TestDefUnreferenced(t *testing.T) {
	spec := one("**Def $Time.Media.** Media time.\n\n**Def $Time.Wall.** Wall time, see $Time.Wall.\n\n**Def $Used.Thing.** Used.\n\n**$Frame.Timing.** Uses $Used.Thing.\n\n**$Inv.Alone.** An invariant nobody references is fine.\n")
	ds := only(Run(spec, Options{}), diag.CodeDefUnreferenced)
	require.Len(t, ds, 2)
	assert.Equal(t, "$Time.Media", text(spec, ds[0]))
	assert.Equal(t, "$Time.Wall", text(spec, ds[1]), "self-references do not count")
	assert.Equal(t, diag.Warning, ds[0].Severity)
}

func TestDefReferencedFromProse(t *testing.T) {
	spec := one("**Def $Time.Media.** Media time.\n\nSee $Time.Media in prose.\n")
	assert.Empty(t, only(Run(spec, Options{}), diag.CodeDefUnreferenced))
}

// TestMathHazard holds the rows measured against GitHub's renderer on
// 2026-10-31 (plan-step4d-ergonomics.md, part 4): a $ closes inline math
// only when the character before it is not a space and the one after it is
// not a letter, digit or _. want is the flagged $ signs, each named by the
// text from it to the end of the line.
func TestMathHazard(t *testing.T) {
	cases := []struct {
		line string
		want []string // the text from each flagged $ to the end of the line
	}{
		// GitHub: text.
		{"($Sdk.Preempt). From the instant lensd detects it ($Time.Elapsed), requests", nil},
		{"$Frame.Timing/$Time.Media", nil},
		{"$Frame.Timing$Time.Media", nil},
		{"($Frame.Timing)/($Time.Media)", nil},
		{"$Frame.Timing $Time.Media", nil},
		{"$Frame.Timing ($Time.Media)", nil},
		{"$Frame.Timing costs $5 today", nil},
		{"$Frame.Timing costs 5$_x today", nil},
		{"$Frame.Timing and `x$` ok", nil},
		{"costs 5$ today, see $Frame.Timing", nil}, // no reference before it
		{"$Frame.Timing costs 5 $ today", nil},     // a space before it
		// GitHub: math.
		{"$Frame.Timing$", []string{"$"}},
		{"$Frame.Timing/$", []string{"$"}},
		{"$Frame.Timing costs 5$ today", []string{"$ today"}},
		{"$Frame.Timing and x$.", []string{"$."}},
		{"$Frame.Timing costs (5$) today", []string{"$) today"}},
		{"$Frame.Timing and x\\$ ok", []string{"$ ok"}},
		{"$Frame.Timing $Time.Media$ c", []string{"$ c"}},
	}
	for _, c := range cases {
		src := "# m\n\n" + c.line + "\n"
		spec := one(src)
		var got []string
		for _, d := range only(Run(spec, Options{}), diag.CodeMathHazard) {
			f := spec.File(d.File)
			assert.Equal(t, "$", text(spec, d), c.line)
			got = append(got, strings.TrimSuffix(string(f.Source()[d.Range.Start:]), "\n"))
			require.Len(t, d.Related, 1)
			assert.Equal(t, "$", f.Text(d.Related[0].Range), "the earlier reference's $")
			assert.Equal(t, diag.Warning, d.Severity)
		}
		assert.Equal(t, c.want, got, c.line)
	}
}

// The related information is the nearest reference before the stray $:
// GitHub pairs the closing $ with it ($Time.Media$ is the math).
func TestMathHazardRelated(t *testing.T) {
	src := "# m\n\n$Frame.Timing $Time.Media$ c\n"
	spec := one(src)
	ds := only(Run(spec, Options{}), diag.CodeMathHazard)
	require.Len(t, ds, 1)
	assert.Equal(t, strings.Index(src, "$Time.Media"), ds[0].Related[0].Range.Start)
	assert.Contains(t, ds[0].Message, "$Time.Media")
	assert.Contains(t, ds[0].Message, "backticks")
	assert.Contains(t, ds[0].Message, "backslash")
}

// Code, HTML and link destinations hold no math; a definition's $ opens
// math like a reference's; the hazard does not cross lines.
func TestMathHazardRegions(t *testing.T) {
	cases := []struct {
		src  string
		want int
	}{
		{"# m\n\n$Frame.Timing and `5$` and <b title=\"5$\">x</b> and [a](http://x/5$).\n", 0},
		{"# m\n\n```\n$Frame.Timing costs 5$ today\n```\n", 0},
		{"**Def $Time.Media.** Ticks, at most 5$ each.\n", 1},
		{"# m\n\nSee $Frame.Timing\nwhich costs 5$ today.\n", 0},
		{"# m\n\n$Frame.Timing costs 5$ and 6$ today.\n", 1}, // the first $ closes the span
		{"# m\n\n$Frame.Timing costs 5$ and $Time.Media 6$ today.\n", 2},
	}
	for _, c := range cases {
		assert.Len(t, only(Run(one(c.src), Options{}), diag.CodeMathHazard), c.want, c.src)
	}
}

// StrayWord is the word a stray $ ends: what the fix puts in backticks.
func TestStrayWord(t *testing.T) {
	cases := []struct{ line, want string }{
		{"$Frame.Timing costs 5$ today", "5$"},
		{"$Frame.Timing costs (5$) today", "5$"},
		{"$Frame.Timing and x$.", "x$"},
		{"$Frame.Timing$", "$"},
		{"$Frame.Timing/$", "$"},
		{"$Frame.Timing and x\\$ ok", "x\\$"},
		{"$Frame.Timing costs 1.5$ today", "1.5$"},
		{"$Frame.Timing costs **5$** today", "5$"},
	}
	for _, c := range cases {
		src := "# m\n\n" + c.line + "\n"
		spec := one(src)
		ds := only(Run(spec, Options{}), diag.CodeMathHazard)
		require.Len(t, ds, 1, c.line)
		f := spec.File(ds[0].File)
		assert.Equal(t, c.want, f.Text(StrayWord(f, ds[0].Range.Start)), c.line)
	}
}

// ref-adjacent, the rule's old name, is accepted in spec.toml's [lint].
func TestRefAdjacentAlias(t *testing.T) {
	assert.True(t, Known("ref-adjacent"))
	assert.Equal(t, diag.CodeMathHazard, Canonical("ref-adjacent"))
	assert.NotContains(t, KnownCodes(), "ref-adjacent")
	assert.Empty(t, Overrides{"ref-adjacent": "hint"}.Validate())
	spec := one("# m\n\n$Frame.Timing costs 5$ today\n")
	assert.Empty(t, only(Run(spec, Options{Overrides: Overrides{"ref-adjacent": "off"}}), diag.CodeMathHazard))
	ds := only(Run(spec, Options{Overrides: Overrides{"ref-adjacent": "error"}}), diag.CodeMathHazard)
	require.Len(t, ds, 1)
	assert.Equal(t, diag.Error, ds[0].Severity)
	ds = only(Run(spec, Options{Overrides: Overrides{"ref-adjacent": "error", "math-hazard": "hint"}}), diag.CodeMathHazard)
	require.Len(t, ds, 1)
	assert.Equal(t, diag.Hint, ds[0].Severity, "the current name wins")
}

func TestAnchorLong(t *testing.T) {
	body := func(n int) string {
		var b strings.Builder
		b.WriteString("**$Long.One.** first\n")
		for i := 1; i < n; i++ {
			b.WriteString("- item\n")
		}
		return b.String()
	}
	spec := one(body(25))
	assert.Empty(t, only(Run(spec, Options{}), diag.CodeAnchorLong), "25 lines is the limit")
	spec = one(body(26))
	ds := only(Run(spec, Options{}), diag.CodeAnchorLong)
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "26 lines")
}

func TestCheckedByMissingIsOptIn(t *testing.T) {
	src := "**$Has.Check.** Do it. Checked by: a test.\n\n**$No.Check.** Do it.\n\n**Def $A.Def.** Defs are exempt, see $Has.Check and $No.Check.\n"
	spec := one(src)
	assert.Empty(t, only(Run(spec, Options{}), diag.CodeCheckedByMissing))

	ds := only(Run(spec, Options{Overrides: Overrides{diag.CodeCheckedByMissing: "warning"}}), diag.CodeCheckedByMissing)
	require.Len(t, ds, 1)
	assert.Equal(t, "$No.Check", text(spec, ds[0]))
	assert.Equal(t, diag.Warning, ds[0].Severity)

	ds = only(Run(spec, Options{Overrides: Overrides{diag.CodeCheckedByMissing: "error"}}), diag.CodeCheckedByMissing)
	require.Len(t, ds, 1)
	assert.Equal(t, diag.Error, ds[0].Severity)
}

func TestSectionHashRef(t *testing.T) {
	spec := one("## 3. Three\n\n## 3.2 Sub\n\nSee #3.2 and #9.9 and PR #271 and #3.\n")
	ds := only(Run(spec, Options{}), diag.CodeSectionHashRef)
	require.Len(t, ds, 1, "only #N.N that resolves is flagged")
	assert.Equal(t, "#3.2", text(spec, ds[0]))
	assert.Equal(t, diag.Hint, ds[0].Severity)
	assert.Contains(t, ds[0].Message, "§3.2")
}

func TestSectionRefMalformed(t *testing.T) {
	spec := one("## 5. Five\n\nSee §5.2ab here.\n")
	ds := only(Run(spec, Options{}), diag.CodeSectionRefMalformed)
	require.Len(t, ds, 1)
	assert.Equal(t, diag.Hint, ds[0].Severity)
}

func TestRefNearly(t *testing.T) {
	spec := one("**$Frame.Timing.** Body mentions $Frame.timing and $Frame.Timing_x and $HOME and $5.00 and $Frame.Timing.\n")
	ds := only(Run(spec, Options{}), diag.CodeRefNearly)
	var got []string
	for _, d := range ds {
		got = append(got, text(spec, d))
	}
	assert.Equal(t, []string{"$Frame.timing", "$Frame.Timing_x"}, got)
	assert.Contains(t, ds[0].Message, "did you mean `$Frame.Timing`")
	assert.Contains(t, ds[1].Message, "starts with the defined name `$Frame.Timing`")
}

func TestRefNearlyPrefixMustBeDefined(t *testing.T) {
	spec := one("**$Frame.Timing.** Mentions $Other.timing.\n")
	assert.Empty(t, only(Run(spec, Options{}), diag.CodeRefNearly))
}

func TestRefInTitle(t *testing.T) {
	spec := one("**Def $Time.Media.** Media.\n\n**$Frame.Timing (depends on $Time.Media).** Body uses $Time.Media.\n")
	ds := only(Run(spec, Options{}), diag.CodeRefInTitle)
	require.Len(t, ds, 1)
	assert.Equal(t, "$Time.Media", text(spec, ds[0]))
	assert.Equal(t, diag.Hint, ds[0].Severity)
}

func TestAnchorCodeAfter(t *testing.T) {
	spec := one("**$Frame.Timing.** Body.\n```\ncode\n```\n")
	ds := only(Run(spec, Options{}), diag.CodeAnchorCodeAfter)
	require.Len(t, ds, 1)
	assert.Equal(t, "$Frame.Timing", text(spec, ds[0]))
}

func TestPassThroughOfMdspecDiagnostics(t *testing.T) {
	spec := one("**$Frame.Timing.** Uses $Missing.Name.\n")
	ds := Run(spec, Options{})
	assert.Equal(t, []string{diag.CodeRefDangling}, codes(ds))
	assert.Equal(t, diag.Error, ds[0].Severity)
}

func TestOverridesChangeAndDrop(t *testing.T) {
	spec := one("**$Frame.Timing.** Uses $Missing.Name.\n\n**Def $Time.Media.** Unused.\n")
	ds := Run(spec, Options{Overrides: Overrides{
		diag.CodeRefDangling:     "warning",
		diag.CodeDefUnreferenced: "off",
	}})
	require.Equal(t, []string{diag.CodeRefDangling}, codes(ds))
	assert.Equal(t, diag.Warning, ds[0].Severity)
	assert.False(t, diag.HasErrors(ds))
}

func TestOverrideRaisesHintToError(t *testing.T) {
	spec := one("**$Frame.Timing.** Body.\n```\ncode\n```\n")
	ds := Run(spec, Options{Overrides: Overrides{diag.CodeAnchorCodeAfter: "error"}})
	require.Len(t, ds, 1)
	assert.Equal(t, diag.Error, ds[0].Severity)
}

func TestExtraDiagnosticsAreMergedAndOverridable(t *testing.T) {
	spec := one("**$Frame.Timing.** Body.\n")
	extra := []diag.Diagnostic{
		{Code: diag.CodeProjectFileMissing, Severity: diag.Error, File: "spec.toml", Message: "missing"},
		{Code: diag.CodeFileSymlink, Severity: diag.Error, File: "b.spec.md", Message: "link"},
	}
	ds := Run(spec, Options{Extra: extra, Overrides: Overrides{diag.CodeFileSymlink: "off"}})
	assert.Equal(t, []string{diag.CodeProjectFileMissing}, codes(ds))
}

func TestInvalidUTF8FileIsSkipped(t *testing.T) {
	spec := build(map[string]string{"a.spec.md": "**Def $A.B.** x\n", "b.spec.md": "**Def $C.D.** \xff $E.F\n"})
	ds := Run(spec, Options{})
	assert.Contains(t, codes(ds), diag.CodeFileNotUTF8)
	for _, d := range ds {
		if d.File == "b.spec.md" {
			assert.NotEqual(t, diag.CodeRefDangling, d.Code)
			assert.NotEqual(t, diag.CodeDefUnreferenced, d.Code)
		}
	}
}

func TestDiagnosticsAreSorted(t *testing.T) {
	spec := build(map[string]string{
		"b.spec.md": "**$B.One.** Uses $Nope.One.\n",
		"a.spec.md": "**$A.One.** Uses $Nope.Two.\n",
	})
	ds := Run(spec, Options{})
	require.Len(t, ds, 2)
	assert.Equal(t, "a.spec.md", ds[0].File)
	assert.Equal(t, "b.spec.md", ds[1].File)
}

func TestRulesTable(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Rules() {
		assert.False(t, seen[r.Code], r.Code)
		seen[r.Code] = true
		assert.True(t, Known(r.Code))
		assert.NotEmpty(t, r.Summary)
	}
	assert.Len(t, Rules(), 32)
	sev, ok := DefaultSeverity(diag.CodeCheckedByMissing)
	assert.True(t, ok)
	assert.Equal(t, Off, sev)
	_, ok = DefaultSeverity("nope")
	assert.False(t, ok)
	assert.False(t, Known("nope"))
}

func TestOverridesValidate(t *testing.T) {
	assert.Empty(t, Overrides{diag.CodeAnchorLong: "off", diag.CodeRefNearly: "error"}.Validate())
	errs := Overrides{"no-such-rule": "off", diag.CodeAnchorLong: "loud"}.Validate()
	require.Len(t, errs, 2)
	assert.Contains(t, errs[0].Error(), "anchor-long")
	assert.Contains(t, errs[0].Error(), "loud")
	assert.Contains(t, errs[1].Error(), "no-such-rule")
}

func TestParseLevel(t *testing.T) {
	for _, s := range []string{"off", "hint", "warning", "error"} {
		_, err := ParseLevel(s)
		assert.NoError(t, err, s)
	}
	_, err := ParseLevel("fatal")
	assert.Error(t, err)
}

func TestLensdFixtureHasNoErrors(t *testing.T) {
	src, err := os.ReadFile("../../testdata/lensd/lensd.spec.md")
	require.NoError(t, err)
	spec := mdspec.Build([]*mdspec.File{mdspec.ParseFile("lensd.spec.md", src)})
	ds := Run(spec, Options{})
	for _, d := range ds {
		assert.NotEqual(t, diag.Error, d.Severity, "%s %s", d.Code, d.Message)
	}
	assert.Empty(t, only(Run(spec, Options{Overrides: Overrides{diag.CodeCheckedByMissing: "warning"}}), diag.CodeCheckedByMissing),
		"every lensd invariant names what checks it")
}
