// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package formal_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/formal/tla"
	"github.com/jwriteclub/spec/internal/mdspec"
)

const hlsSpec = "# HLS\n\n" +
	"**$Frame.Timing (fragment timing).** Decode time MUST equal the sum of\nall earlier `durations`.\n\n" +
	"## 1.2 Fragments\n\nEvery fragment has *exactly one* key frame.\n\n" +
	"## 3.6 Shared\n\nIn hls.\n\n" +
	"## 7.1 Only here\n\nUnique.\n"

const rwsSpec = "# RWS\n\n## 3.6 Shared\n\nIn rws.\n\n## 8. Other\n\nText.\n"

func buildSpec() *mdspec.Spec {
	return mdspec.Build([]*mdspec.File{
		mdspec.ParseFile("hls.spec.md", []byte(hlsSpec)),
		mdspec.ParseFile("04-rws.md", []byte(rwsSpec)),
	})
}

func model(path, body string) *formal.File {
	return tla.Scan(path, []byte("---- MODULE M ----\n"+body+"\n====\n"))
}

func joined(files ...*formal.File) *formal.Index {
	return formal.Join(buildSpec(), files)
}

func TestJoinAnchorAndSection(t *testing.T) {
	f := model("tla/M.tla", "\\* Spec: $Frame.Timing\n\\* Spec: §1.2\nA == 1\n\\* Derived-from: §7.1\nB == 1")
	ix := joined(f)
	spec := ix.Spec

	l := ix.LinkOf(f.Tags[0])
	require.Equal(t, mdspec.Resolved, l.State)
	assert.Equal(t, "Frame.Timing", l.Anchor.Name)
	assert.Equal(t, "tla/M.tla:A", l.Where())

	sec := spec.File("hls.spec.md").Sections[0]
	require.Equal(t, "1.2", sec.Number)
	l = ix.LinkOf(f.Tags[1])
	require.Equal(t, mdspec.Resolved, l.State)
	assert.Same(t, sec, l.Section)

	assert.Len(t, ix.ToAnchor("Frame.Timing"), 1)
	assert.Empty(t, ix.ToAnchor("Nope.Name"))
	assert.Len(t, ix.ToSection(sec), 1)
	assert.Same(t, f, ix.File("tla/M.tla"))
	assert.Nil(t, ix.File("hls.spec.md"))
}

func TestWhereWithoutADeclaration(t *testing.T) {
	f := model("tla/M.tla", "\\* Spec: §1.2")
	l := joined(f).LinkOf(f.Tags[0])
	assert.Equal(t, "tla/M.tla:2", l.Where())
}

// Section lookup: qualified, then the Spec-file list, then project-unique,
// otherwise ambiguous.
func TestSectionResolutionOrder(t *testing.T) {
	cases := []struct {
		name, body string
		state      mdspec.Resolution
		file       string
	}{
		{"unique", "\\* Spec: §7.1", mdspec.Resolved, "hls.spec.md"},
		{"ambiguous", "\\* Spec: §3.6", mdspec.Ambiguous, ""},
		{"dangling", "\\* Spec: §9.9", mdspec.Dangling, ""},
		{"qualified", "\\* Spec: §hls:3.6", mdspec.Resolved, "hls.spec.md"},
		{"qualified digit-first", "\\* Spec: §04-rws:3.6", mdspec.Resolved, "04-rws.md"},
		{"qualified wrong file", "\\* Spec: §hls:8", mdspec.Dangling, ""},
		{"unknown stem", "\\* Spec: §nope:1", mdspec.Dangling, ""},
		{"spec-file", "\\* Spec-file: 04-rws\n\\* Spec: §3.6", mdspec.Resolved, "04-rws.md"},
		{"spec-file below the tag", "\\* Spec: §3.6\n\\* Spec-file: hls", mdspec.Resolved, "hls.spec.md"},
		{"spec-file misses, project-unique", "\\* Spec-file: hls\n\\* Spec: §8", mdspec.Resolved, "04-rws.md"},
		{"two spec-files both have it", "\\* Spec-file: hls\n\\* Spec-file: 04-rws\n\\* Spec: §3.6", mdspec.Ambiguous, ""},
		{"qualified beats spec-file", "\\* Spec-file: hls\n\\* Spec: §04-rws:3.6", mdspec.Resolved, "04-rws.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := model("tla/M.tla", c.body)
			ix := joined(f)
			var l *formal.Link
			for _, tag := range f.Tags {
				if tag.Kind == formal.TagSpec {
					l = ix.LinkOf(tag)
				}
			}
			require.NotNil(t, l)
			assert.Equal(t, c.state, l.State)
			if c.file != "" {
				require.NotNil(t, l.Section)
				assert.Equal(t, c.file, l.Section.File)
			}
		})
	}
}

func TestAmbiguousTagIndexesEveryCandidate(t *testing.T) {
	f := model("tla/M.tla", "\\* Spec: §3.6")
	ix := joined(f)
	assert.Len(t, ix.ToSection(ix.Spec.File("hls.spec.md").Sections[1]), 1)
	assert.Len(t, ix.ToSection(ix.Spec.File("04-rws.md").Sections[0]), 1)
}

func TestLinksOrderSpecThenDerivedThenMutants(t *testing.T) {
	a := model("tla/A.tla", "\\* Derived-from: $Frame.Timing\nX == 1\n\\* Spec: $Frame.Timing\nY == 1")
	m := model("tla/A_Mut1_Bad.tla", "\\* Spec: $Frame.Timing\nZ == 1")
	b := model("tla/B.tla", "\\* Spec: $Frame.Timing\nW == 1")
	ix := joined(m, b, a)
	var got []string
	for _, l := range ix.ToAnchor("Frame.Timing") {
		got = append(got, l.Where())
	}
	assert.Equal(t, []string{"tla/A.tla:Y", "tla/B.tla:W", "tla/A.tla:X", "tla/A_Mut1_Bad.tla:Z"}, got)
}

func TestCovered(t *testing.T) {
	spec := joined(model("tla/A.tla", "\\* Spec: $Frame.Timing\nX == 1"))
	assert.True(t, spec.Covered("Frame.Timing"))
	assert.False(t, spec.Covered("Other.Name"))
	assert.False(t, joined(model("tla/A.tla", "\\* Derived-from: $Frame.Timing\nX == 1")).Covered("Frame.Timing"),
		"Derived-from does not say anything checks the anchor")
	assert.False(t, joined(model("tla/A_Mut1.tla", "\\* Spec: $Frame.Timing\nX == 1")).Covered("Frame.Timing"),
		"a mutant is no coverage")
	var none *formal.Index
	assert.False(t, none.Covered("Frame.Timing"))
	assert.Nil(t, none.Lint())
}

// findings returns "code@text" for each finding of one model.
func findings(t *testing.T, f *formal.File) []string {
	t.Helper()
	var out []string
	for _, d := range joined(f).Lint() {
		assert.Equal(t, f.Path, d.File)
		out = append(out, d.Code+"@"+f.Text(d.Range))
	}
	return out
}

func TestLintClean(t *testing.T) {
	f := model("tla/M.tla", "\\* Spec: $Frame.Timing \"decode time must equal the sum of all earlier durations\"\n"+
		"\\* Spec: §1.2 \"has exactly one key frame\"\nInv_Good == TRUE")
	assert.Empty(t, findings(t, f))
}

func TestLintRefDangling(t *testing.T) {
	for _, c := range []struct{ tag, want, msg string }{
		{`\* Spec: $Nope.Name`, "$Nope.Name", "not defined"},
		{`\* Spec: §9.9`, "§9.9", "does not match"},
		{`\* Derived-from: §nope:1`, "§nope:1", `no spec file has the stem "nope"`},
		{`\* Spec: §3.6`, "§3.6", "§04-rws:3.6"},
		{`\* Spec-file: nope`, "nope", `no spec file has the stem "nope"`},
	} {
		f := model("tla/M.tla", c.tag+"\nA == 1")
		ds := joined(f).Lint()
		require.Len(t, ds, 1, c.tag)
		assert.Equal(t, diag.CodeFormalRefDangling, ds[0].Code)
		assert.Equal(t, diag.Error, ds[0].Severity)
		assert.Equal(t, c.want, f.Text(ds[0].Range))
		assert.Contains(t, ds[0].Message, c.msg)
	}
}

func TestLintTagMalformed(t *testing.T) {
	f := model("tla/M.tla", "\\* Spec: rc-host §10 INV-4\nA == 1\n\\* Derived-from: model bookkeeping\nB == 1")
	ds := joined(f).Lint()
	require.Len(t, ds, 2)
	for _, d := range ds {
		assert.Equal(t, diag.CodeFormalTagMalformed, d.Code)
		assert.Equal(t, diag.Hint, d.Severity)
	}
	assert.Contains(t, ds[0].Message, "rc-host", "no alias: it is reported, not resolved")
}

func TestLintTagOnSymbolicOperator(t *testing.T) {
	f := model("tla/M.tla", "\\* Spec: §1.2\na ++ b == a")
	ds := joined(f).Lint()
	require.Len(t, ds, 1)
	assert.Equal(t, diag.CodeFormalTagMalformed, ds[0].Code)
	assert.Contains(t, ds[0].Message, "symbolic operator")
}

func TestLintInvalidUTF8(t *testing.T) {
	f := tla.Scan("tla/M.tla", []byte("\\* Spec: \xA7 1\n"))
	ds := joined(f).Lint()
	require.Len(t, ds, 1)
	assert.Equal(t, diag.CodeFormalTagMalformed, ds[0].Code)
	assert.Contains(t, ds[0].Message, "UTF-8")
}

func TestLintTagBlock(t *testing.T) {
	f := model("tla/M.tla", "(* Spec: §1.2 *)\nA == 1")
	ds := joined(f).Lint()
	require.Len(t, ds, 1)
	assert.Equal(t, diag.CodeFormalTagBlock, ds[0].Code)
	assert.Equal(t, diag.Hint, ds[0].Severity)
}

func TestLintOpUncited(t *testing.T) {
	f := model("tla/M.tla", "Inv_Bare == TRUE\nOther == TRUE\n\\* Spec: §1.2\nInv_Cited == TRUE\n"+
		"Inv_Body ==\n    \\* Derived-from: §1.2\n    TRUE\n\\* Derived-from: no ref\nInv_Malformed == TRUE")
	ds := joined(f).Lint()
	var got []string
	for _, d := range ds {
		if d.Code == diag.CodeFormalOpUncited {
			assert.Equal(t, diag.Warning, d.Severity)
			got = append(got, f.Text(d.Range))
		}
	}
	assert.Equal(t, []string{"Inv_Bare"}, got)

	m := model("tla/M_Mut1_X.tla", "Inv_Bare == TRUE")
	assert.Empty(t, joined(m).Lint(), "mutants are excluded from coverage")
	v := model("tla/M.tla", "VARIABLE Inv_var")
	assert.Empty(t, joined(v).Lint(), "only definitions need a citation")
}

func TestLintQuoteStale(t *testing.T) {
	f := model("tla/M.tla", "\\* Spec: $Frame.Timing \"decode time must be fast\"\nA == 1\n"+
		"\\* Spec: §1.2 \"Unique.\"\nB == 1")
	ds := joined(f).Lint()
	require.Len(t, ds, 2)
	for _, d := range ds {
		assert.Equal(t, diag.CodeFormalQuoteStale, d.Code)
		assert.Equal(t, diag.Hint, d.Severity)
	}
	assert.Equal(t, "decode time must be fast", f.Text(ds[0].Range))
	assert.Equal(t, "Unique.", f.Text(ds[1].Range), "the quote is in another section, not the cited one")
}

// A quote survives reflowing, letter case and inline emphasis, not rewording.
func TestQuoteComparison(t *testing.T) {
	for _, q := range []string{
		"Decode time MUST equal the sum of all earlier durations",
		"decode  time must equal the sum of all earlier `durations`.",
		"the sum of all",
	} {
		f := model("tla/M.tla", "\\* Spec: $Frame.Timing \""+q+"\"\nA == 1")
		assert.Empty(t, joined(f).Lint(), q)
	}
	f := model("tla/M.tla", "\\* Spec: §1.2 \"has exactly one key frame\"\nA == 1")
	assert.Empty(t, joined(f).Lint(), "*exactly one* in the section")
}

func TestUnresolvedTagHasNoQuoteFinding(t *testing.T) {
	f := model("tla/M.tla", "\\* Spec: $Nope.Name \"whatever\"\nA == 1")
	ds := joined(f).Lint()
	require.Len(t, ds, 1)
	assert.Equal(t, diag.CodeFormalRefDangling, ds[0].Code)
}

func TestMentions(t *testing.T) {
	f := model("tla/M.tla", "\\* see $Frame.Timing, $Frame.Timing.Sub, a$Frame.Timing, $$Frame.Timing, \\$Frame.Timing, $Frame.TimingX\n"+
		"\\* Spec: $Frame.Timing.\nA == 1")
	got := f.Mentions("Frame.Timing")
	require.Len(t, got, 2)
	assert.Equal(t, 1, f.Index().Line(got[0].Start)-1)
	assert.Equal(t, 2, f.Index().Line(got[1].Start)-1)
}

func TestRefSlot(t *testing.T) {
	src := "---- MODULE M ----\n\\* Spec: $Fr\n\\* Derived-from:  §1\n\\* Spec: §1 \"$Fr\"\n\\* See: $Fr\n(* Spec: $Fr *)\nA == \"\\\\* Spec: $Fr\"\n====\n"
	f := tla.Scan("M.tla", []byte(src))
	at := func(needle string, nth int) int {
		i := -1
		for n := 0; n <= nth; n++ {
			j := indexFrom(src, needle, i+1)
			require.GreaterOrEqual(t, j, 0)
			i = j
		}
		return i
	}
	assert.True(t, f.RefSlot(at("$Fr", 0)))
	assert.True(t, f.RefSlot(at("§1", 0)))
	assert.True(t, f.RefSlot(at("§1", 1)))
	assert.False(t, f.RefSlot(at("$Fr", 1)), "in a quote")
	assert.False(t, f.RefSlot(at("$Fr", 2)), "not a tag keyword")
	assert.False(t, f.RefSlot(at("$Fr", 3)), "a block comment")
	assert.False(t, f.RefSlot(at("$Fr", 4)), "a string")
}

func indexFrom(s, sub string, from int) int {
	for i := from; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestIsMutant(t *testing.T) {
	for p, want := range map[string]bool{
		"tla/LaneQueue_Mut2_WeakMutex.tla": true,
		"tla/A_Mut1.tla":                   true,
		"tla/A_Mut.tla":                    true,
		"tla/A_MutNoLock.tla":              true,
		"tla/A_Mutex_Mut1.tla":             true,
		"tla/Queue_Mutex.tla":              false,
		"tla/A_Mutable.tla":                false,
		"tla/A.tla":                        false,
		"tla_Mut1/A.tla":                   false,
	} {
		assert.Equal(t, want, formal.IsMutant(p), p)
	}
}

func TestCited(t *testing.T) {
	ix := joined(model("tla/A.tla", "\\* Derived-from: $Frame.Timing\nX == 1"))
	assert.True(t, ix.Cited("Frame.Timing"), "a Derived-from tag is a reference, though it covers nothing")
	assert.False(t, ix.Covered("Frame.Timing"))
	assert.False(t, ix.Cited("Other.Name"))
	mut := joined(model("tla/A_Mut1.tla", "\\* Spec: $Frame.Timing\nX == 1"))
	assert.False(t, mut.Cited("Frame.Timing"), "a mutant is not a use")
	var none *formal.Index
	assert.False(t, none.Cited("Frame.Timing"))
}

func TestLintUnreadableModel(t *testing.T) {
	f := formal.NewFile("tla/A.tla", nil)
	f.ReadError = "permission denied"
	ds := joined(f).Lint()
	require.Len(t, ds, 1)
	assert.Equal(t, diag.CodeFormalTagMalformed, ds[0].Code)
	assert.Equal(t, diag.Hint, ds[0].Severity)
	assert.Contains(t, ds[0].Message, "permission denied")
}
