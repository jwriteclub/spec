// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package mdspec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/golden"
	"github.com/jwriteclub/spec/internal/pos"
)

func TestValidName(t *testing.T) {
	for _, s := range []string{"Frame.Timing", "HttpTimeout", "Interval", "Ab", "A.Bc", "Frame2.X9y", "AbC.D"} {
		assert.True(t, ValidName(s), s)
	}
	for _, s := range []string{"", "HOME", "HTTP", "frame", "Frame.", ".Frame", "Frame..Timing", "Frame.timing", "Frame_x", "5Ab", "A.B", "Ab-Cd", "Ab.9c"} {
		assert.False(t, ValidName(s), s)
	}
}

func TestValidSectionNumber(t *testing.T) {
	for _, s := range []string{"1", "05", "5a", "5.2", "5.2a", "10.20.30"} {
		assert.True(t, ValidSectionNumber(s), s)
	}
	for _, s := range []string{"", "a", "5.", "5.2ab", "5A", ".5", "5..2"} {
		assert.False(t, ValidSectionNumber(s), s)
	}
}

func TestStemOf(t *testing.T) {
	assert.Equal(t, "hls", StemOf("docs/hls.spec.md"))
	assert.Equal(t, "lensd", StemOf("lensd.spec.md"))
	assert.Equal(t, "notes", StemOf("a/b/notes.md"))
	assert.Equal(t, "x", StemOf(`a\x.spec.md`))
}

func names(f *File, role Role) []string {
	var out []string
	for _, o := range f.Occurrences {
		if o.Role == role {
			out = append(out, o.Name)
		}
	}
	return out
}

func TestScannerCorners(t *testing.T) {
	cases := []struct {
		src  string
		refs []string
	}{
		{"x $Ab.Cd y", []string{"Ab.Cd"}},
		{"$Ab.Cd.", []string{"Ab.Cd"}},
		{"$Ab.Cd's", []string{"Ab.Cd"}},
		{"$Ab.cd", nil},
		{"\\$Ab.Cd", nil},
		{"\\\\$Ab.Cd", []string{"Ab.Cd"}},
		{"\\\\\\$Ab.Cd", nil},
		{"a$Ab.Cd _$Ab.Cd 1$Ab.Cd $$Ab.Cd", nil},
		{"($Ab.Cd) [$Ab.Cd] {$Ab.Cd} -$Ab.Cd", []string{"Ab.Cd", "Ab.Cd", "Ab.Cd", "Ab.Cd"}},
		{"_$Ab.Cd_ and *$Ab.Cd*", []string{"Ab.Cd", "Ab.Cd"}},
		{"`$Ab.Cd` <b title=\"$Ab.Cd\">", nil},
		{"[t](u/$Ab.Cd) [$Ab.Cd](u)", []string{"Ab.Cd"}},
		{"$Ab._x_", []string{"Ab"}},
		{"line one\n$Ab.Cd at line start", []string{"Ab.Cd"}},
		{"~~$Ab.Cd~~", []string{"Ab.Cd"}},
	}
	for _, c := range cases {
		f := ParseFile("t.spec.md", []byte(c.src))
		assert.Equal(t, c.refs, names(f, RoleReference), "%q", c.src)
		checkFileInvariants(t, f)
	}
}

func TestSectionScannerCorners(t *testing.T) {
	cases := []struct {
		src  string
		nums []string
	}{
		{"§5.2a", []string{"5.2a"}},
		{"§ 5.2", []string{"5.2"}},
		{"§  5.2", nil},
		{"§5.2.", []string{"5.2"}},
		{"§5.2ab", nil},
		{"§x:1", []string{"1"}},
		{"§x-y_z:1.2", []string{"1.2"}},
		{"§x:", nil},
		{"(§3)", []string{"3"}},
		{"`§3`", nil},
		{"§\n3", nil},
	}
	for _, c := range cases {
		f := ParseFile("t.spec.md", []byte(c.src))
		var got []string
		for _, r := range f.SectionRefs {
			got = append(got, r.Number)
		}
		assert.Equal(t, c.nums, got, "%q", c.src)
		checkFileInvariants(t, f)
	}
}

func TestNormalize(t *testing.T) {
	src := []byte("a  b\r\n\tc `x  y` d\u3000e")
	code := []pos.Range{{Start: 10, End: 14}} // "x  y"
	require.Equal(t, "x  y", string(src[10:14]))
	assert.Equal(t, "a b c `x  y` d e", Normalize(src, pos.Range{Start: 0, End: len(src)}, code, nil))
	// Without the code range the inner run collapses.
	assert.Equal(t, "a b c `x y` d e", Normalize(src, pos.Range{Start: 0, End: len(src)}, nil, nil))
	// Substitutions replace source ranges.
	subs := []Substitution{{Range: pos.Range{Start: 0, End: 1}, Text: "Z"}}
	assert.Equal(t, "Z b c `x  y` d e", Normalize(src, pos.Range{Start: 0, End: len(src)}, code, subs))
	// CRLF inside code becomes LF.
	src2 := []byte("```\na \r\nb\r\n```")
	assert.Equal(t, "``` a \nb\n```", Normalize(src2, pos.Range{Start: 0, End: len(src2)}, []pos.Range{{Start: 4, End: 11}}, nil))
	// Out-of-range clamps; invalid UTF-8 is kept.
	assert.Equal(t, "a \xff", Normalize([]byte("  a \xff "), pos.Range{Start: -3, End: 99}, nil, nil))
}

func TestHashBasics(t *testing.T) {
	h := HashNormalized("x")
	assert.Len(t, h, 64)
	assert.Equal(t, h[:8], ShortHash(h))
	assert.Equal(t, "abc", ShortHash("abc"))
	assert.NotEqual(t, HashNormalized("x"), HashNormalized("x "))
}

func TestHashReflowAndRenames(t *testing.T) {
	a1 := ParseFile("a.spec.md", []byte("**$Ab.Cd.** Uses $Ef.Gh and $Ab.Cd.\n\n**$Ef.Gh.** x\n"))
	a2 := ParseFile("a.spec.md", []byte("**$Ab.Cd (new title).**   Uses\n$Ef.Gh   and $Ab.Cd.\n\n**$Ef.Gh.** x\n"))
	a3 := ParseFile("a.spec.md", []byte("**Def $Ab.Cd.** Uses $Xy.Zz and $Ab.Cd.\n\n**$Xy.Zz.** x\n"))
	x1, x2, x3 := a1.Anchors[0], a2.Anchors[0], a3.Anchors[0]
	assert.Equal(t, x1.Hash, x2.Hash, "reflow and retitle keep the hash")
	assert.Equal(t, "Uses $Ef.Gh and $Ab.Cd.", x1.HashInput())
	assert.NotEqual(t, x1.Hash, x3.Hash)
	assert.Equal(t, x1.ShapeHash(), x3.ShapeHash(), "renaming a reference keeps the shape")
	assert.Equal(t, x3.Hash, x1.HashWithRenames(map[string]string{"Ef.Gh": "Xy.Zz"}))
	assert.Equal(t, x1.Hash, x1.HashWithRenames(map[string]string{}))
	assert.Equal(t, x1.Hash, x1.HashWithRenames(map[string]string{"Not.There": "X.Yz"}))
	assert.Equal(t, KindDefinition, x3.Kind)
}

type hashVector struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	Hash      string `json:"hash"`
	HashInput string `json:"hash_input"`
}

// TestHashVectors checks the hash golden vectors (plan 1.5): the base body,
// the same body reflowed (equal), a CRLF copy (equal) and a code-span
// whitespace change (different).
func TestHashVectors(t *testing.T) {
	dir := filepath.Join(testdata, "hash")
	var vs []hashVector
	hashes := map[string]string{}
	for _, name := range []string{"base", "reflowed", "crlf", "codespan"} {
		src, err := os.ReadFile(filepath.Join(dir, name+".spec.md"))
		require.NoError(t, err)
		f := ParseFile(name+".spec.md", src)
		require.Len(t, f.Anchors, 1, name)
		a := f.Anchors[0]
		hashes[name] = a.Hash
		vs = append(vs, hashVector{File: f.Path, Name: a.Name, Hash: a.Hash, HashInput: a.HashInput()})
	}
	golden.Check(t, filepath.Join(dir, "vectors.golden.json"), marshal(t, vs))
	assert.Equal(t, hashes["base"], hashes["reflowed"])
	assert.Equal(t, hashes["base"], hashes["crlf"])
	assert.NotEqual(t, hashes["base"], hashes["codespan"])

	// The vector body is the real fixture's body.
	src, err := os.ReadFile(filepath.Join(testdata, "lensd", "lensd.spec.md"))
	require.NoError(t, err)
	s := Build([]*File{ParseFile("lensd.spec.md", src)})
	require.NotNil(t, s.Anchor("Time.Elapsed"))
	assert.Equal(t, hashes["base"], s.Anchor("Time.Elapsed").Hash)
}

func TestBuildAcrossFiles(t *testing.T) {
	a := ParseFile("a.spec.md", []byte("# A\n\n## 1. One\n\n**$Ab.Cd.** See $Ef.Gh and §b:1 and §2.\n\n## 3. Three\n\nProse mentions $Ef.Gh twice: $Ef.Gh.\n"))
	b := ParseFile("b.spec.md", []byte("# B\n\n## 1. One\n\n## 2. Two\n\n**Def $Ef.Gh (eh).** Uses $Ab.Cd and $Ef.Gh, §1 and §3.\n\n- a list mentions $Ab.Cd\n"))
	c := ParseFile("sub/b.spec.md", []byte("## 1. Again\n\n§b:1 is ambiguous; §4 dangles.\n"))
	s := Build([]*File{c, b, a})
	require.Equal(t, []string{"a.spec.md", "b.spec.md", "sub/b.spec.md"}, []string{s.Files[0].Path, s.Files[1].Path, s.Files[2].Path})
	require.Len(t, s.Anchors, 2)
	assert.Equal(t, "Ab.Cd", s.Anchors[0].Name)
	assert.Equal(t, "1", s.Anchors[0].Section.Number)

	states := map[string]Resolution{}
	for _, r := range s.SectionRefs {
		states[r.File+" "+r.Stem+":"+r.Number] = r.State
	}
	assert.Equal(t, Ambiguous, states["a.spec.md b:1"], "two files share the stem b")
	assert.Equal(t, Resolved, states["a.spec.md :2"], "unique project-wide")
	assert.Equal(t, Resolved, states["b.spec.md :1"], "same file first")
	assert.Equal(t, Resolved, states["b.spec.md :3"], "only a.spec.md has 3")
	assert.Equal(t, Dangling, states["sub/b.spec.md :4"])

	st, target, _ := s.ResolveSection("b.spec.md", "", "1")
	assert.Equal(t, Resolved, st)
	assert.Equal(t, "b.spec.md", target.File)
	st, _, cands := s.ResolveSection("a.spec.md", "", "1")
	assert.Equal(t, Resolved, st, "a.spec.md has its own 1")
	assert.Empty(t, cands)
	st, _, cands = s.ResolveSection("x.spec.md", "", "1")
	assert.Equal(t, Ambiguous, st)
	assert.Len(t, cands, 3)

	refBy := s.ReferencedBy("Ef.Gh")
	require.Len(t, refBy, 1, "self-references do not count")
	assert.Equal(t, "Ab.Cd", refBy[0].Name)
	ms := s.Mentions("Ef.Gh")
	require.Len(t, ms, 1, "two references in one paragraph are one mention")
	assert.Equal(t, ContainerParagraph, ms[0].Kind)
	ms = s.Mentions("Ab.Cd")
	require.Len(t, ms, 1)
	assert.Equal(t, ContainerListItem, ms[0].Kind)
	assert.Len(t, s.ReferencesTo("Ef.Gh"), 4)
	assert.Len(t, s.FindSections("1"), 3)
	assert.Len(t, s.AnchorsIn(s.FindSections("1")[0]), 1)
	assert.Equal(t, "§a:1", s.FindSections("1")[0].Qualified())
	assert.Len(t, s.SectionReferencesTo(s.FindSections("3")[0]), 1)
	assert.Nil(t, s.Anchor("Nope.Nope"))
	assert.Len(t, s.Lookup("Ab.Cd"), 1)
	assert.Equal(t, b, s.File("b.spec.md"))
	assert.Len(t, s.FilesWithStem("b"), 2)

	codes := map[string]int{}
	for _, d := range s.Diagnostics {
		codes[d.Code]++
	}
	assert.Equal(t, 2, codes[diag.CodeFileStemDuplicate])
	assert.Equal(t, 2, codes[diag.CodeSectionRefAmbiguous])
	assert.Equal(t, 1, codes[diag.CodeSectionRefDangling])
}

func TestBuildExcludesInvalidUTF8(t *testing.T) {
	good := ParseFile("a.spec.md", []byte("x $Ab.Cd\n"))
	bad := ParseFile("b.spec.md", []byte("**$Ab.Cd.** \xff\n"))
	require.False(t, bad.ValidUTF8)
	require.Len(t, bad.Anchors, 1, "the bad file is still parsed")
	s := Build([]*File{good, bad})
	assert.Empty(t, s.Anchors)
	require.Len(t, s.Refs, 1)
	assert.Equal(t, Dangling, s.Refs[0].State)
	codes := map[string]bool{}
	for _, d := range s.Diagnostics {
		codes[d.Code] = true
	}
	assert.True(t, codes[diag.CodeFileNotUTF8])
	assert.True(t, codes[diag.CodeRefDangling])
}

func TestBuildDoesNotMutateFiles(t *testing.T) {
	f := ParseFile("a.spec.md", []byte("**$Ab.Cd.** x $Ab.Cd and $No.Ne\n"))
	s1 := Build([]*File{f})
	s2 := Build([]*File{f, ParseFile("b.spec.md", []byte("**$No.Ne.** y\n"))})
	assert.Equal(t, Dangling, s1.Refs[1].State)
	assert.Equal(t, Resolved, s2.Refs[1].State)
	assert.Equal(t, Dangling, s1.Refs[1].State, "first spec unchanged")
}

func TestDocumentAndSource(t *testing.T) {
	src := []byte("\xEF\xBB\xBF# T\n")
	f := ParseFile("a.spec.md", src)
	src[4] = 'X'
	assert.Equal(t, byte('#'), f.Source()[3], "source is copied")
	require.NotNil(t, f.Document())
	assert.Equal(t, "a", f.Stem())
	assert.Equal(t, "", f.Text(pos.Range{Start: 5, End: 2}))
	assert.Equal(t, "# T\n", f.Text(pos.Range{Start: 3, End: 99}))
	assert.True(t, f.HasBOM)
}

func TestOpaqueMentions(t *testing.T) {
	f := ParseFile("a.spec.md", []byte("x `$Frame and $Frame.Timing and a$Frame` $Frame\n\n```\n$Frame.\n```\n"))
	got := f.OpaqueMentions("Frame")
	require.Len(t, got, 2)
	assert.Equal(t, "$Frame", f.Text(got[0]))
	assert.Equal(t, 4, f.Index().Line(got[1].Start))
	assert.Empty(t, f.OpaqueMentions("Fr"))
}

func TestConcurrentParse(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(testdata, "lensd", "lensd.spec.md"))
	require.NoError(t, err)
	want := Build([]*File{ParseFile("lensd.spec.md", src)})
	done := make(chan *Spec, 8)
	for i := 0; i < 8; i++ {
		go func() { done <- Build([]*File{ParseFile("lensd.spec.md", src)}) }()
	}
	for i := 0; i < 8; i++ {
		s := <-done
		require.Len(t, s.Anchors, len(want.Anchors))
		for k := range s.Anchors {
			assert.Equal(t, want.Anchors[k].Hash, s.Anchors[k].Hash)
		}
	}
}

// TestDollarAmountsAreNotLabels: bold text starting with a dollar amount, or
// with `$` and something that cannot start a name, is plain text, not a
// malformed label.
func TestDollarAmountsAreNotLabels(t *testing.T) {
	for _, src := range []string{
		"**$5.00 off** for early adopters.\n",
		"**$ amounts** are in USD.\n",
		"**$x** is a variable.\n",
		"**$_tmp** is a scratch value.\n",
		"**Def $5 fee.** Plain text.\n",
	} {
		f := ParseFile("a.spec.md", []byte(src))
		s := Build([]*File{f})
		assert.Empty(t, f.Anchors, src)
		for _, d := range s.Diagnostics {
			assert.NotEqual(t, diag.CodeDefMalformed, d.Code, src)
		}
	}
	for _, src := range []string{
		"**$Foo bar.** Words after the name.\n",
		"**$HTTP.** No lowercase letter.\n",
		"**$frame.timing.** Lowercase dotted name.\n",
		"**Definition $Time.Media.** Wrong prefix.\n",
		"**Def. $Time.Media.** Period after Def.\n",
	} {
		f := ParseFile("a.spec.md", []byte(src))
		s := Build([]*File{f})
		var got []string
		for _, d := range s.Diagnostics {
			got = append(got, d.Code)
		}
		assert.Contains(t, got, diag.CodeDefMalformed, src)
	}
}

// A digit-first stem qualifies when it holds a letter or '-'; digits alone
// stay a section number, so §10:30 is section 10.
func TestDigitFirstStems(t *testing.T) {
	cases := []struct {
		src, stem, num string
	}{
		{"§04-q:4.2", "04-q", "4.2"},
		{"§ 04-q:4.2", "04-q", "4.2"},
		{"§03a:1", "03a", "1"},
		{"§2024_x:7", "2024_x", "7"},
		{"§10:30", "", "10"},
		{"§10_1:3", "", "10"},
		{"§4-5", "", "4"},
		{"§04-q: 4", "", "04"},
	}
	for _, c := range cases {
		f := ParseFile("t.spec.md", []byte(c.src))
		require.Len(t, f.SectionRefs, 1, c.src)
		assert.Equal(t, c.stem, f.SectionRefs[0].Stem, c.src)
		assert.Equal(t, c.num, f.SectionRefs[0].Number, c.src)
		checkFileInvariants(t, f)
	}
}

// The advice of section-ref-ambiguous names a form that really resolves when
// the stems start with digits.
func TestAmbiguousAdviceResolvesWithDigitFirstStem(t *testing.T) {
	a := ParseFile("04-q.md", []byte("# Q\n\n## 4.2 Mutex\n\nText.\n"))
	b := ParseFile("05-r.md", []byte("# R\n\n## 4.2 Other\n\nText.\n"))
	ref := ParseFile("c.spec.md", []byte("# C\n\nSee §4.2.\n"))
	spec := Build([]*File{a, b, ref})
	var msg string
	for _, d := range spec.Diagnostics {
		if d.Code == diag.CodeSectionRefAmbiguous {
			msg = d.Message
		}
	}
	require.Contains(t, msg, "e.g. §04-q:4.2")

	fixed := ParseFile("c.spec.md", []byte("# C\n\nSee §04-q:4.2.\n"))
	spec = Build([]*File{a, b, fixed})
	assert.Empty(t, spec.Diagnostics)
	require.Len(t, spec.SectionRefs, 1)
	assert.Equal(t, Resolved, spec.SectionRefs[0].State)
	assert.Equal(t, "04-q.md", spec.SectionRefs[0].Target.File)
}

func TestScanSectionRef(t *testing.T) {
	cases := []struct {
		src, stem, num string
		n              int
		ok             bool
	}{
		{"4.2 rest", "", "4.2", 3, true},
		{"4.2.", "", "4.2", 3, true},
		{"5.2a", "", "5.2a", 4, true},
		{"5.2ab", "", "", 0, false},
		{"hls:1.2 x", "hls", "1.2", 7, true},
		{"04-q:4.2\"", "04-q", "4.2", 8, true},
		{"10:30", "", "10", 2, true},
		{"x", "", "", 0, false},
		{"", "", "", 0, false},
	}
	for _, c := range cases {
		stem, num, n, ok := ScanSectionRef([]byte(c.src))
		assert.Equal(t, c.ok, ok, c.src)
		assert.Equal(t, c.stem, stem, c.src)
		assert.Equal(t, c.num, num, c.src)
		assert.Equal(t, c.n, n, c.src)
	}
}
