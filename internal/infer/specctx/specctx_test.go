// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package specctx

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/golden"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// multi loads testdata/multi as a spec.
func multi(t *testing.T) *mdspec.Spec {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "testdata", "multi")
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	var files []*mdspec.File
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".spec.md") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		files = append(files, mdspec.ParseFile(e.Name(), src))
	}
	return mdspec.Build(files)
}

func idleStopChange(spec *mdspec.Spec) *Change {
	a := spec.Anchor("Rws.IdleStop")
	f := spec.File(a.File)
	old := f.Text(a.BodyRange)
	return &Change{
		Action: ActionChange, Name: "Rws.IdleStop", File: a.File, OldKind: a.Kind, NewKind: a.Kind,
		OldText: old, NewText: strings.Replace(old, "for 30 s", "for 60 s", 1),
		Why: "viewers reconnect\nslowly on mobile",
	}
}

func TestBuildChangeGolden(t *testing.T) {
	spec := multi(t)
	c := Build(spec, Target{Change: idleStopChange(spec)}, Options{})
	require.NotEmpty(t, c.Neighbors)
	for _, n := range c.Neighbors {
		assert.NotEqual(t, "Rws.IdleStop", n.Name, "the target is not its own neighbor")
		assert.Contains(t, []int{1, 2}, n.Hops)
	}
	assert.Equal(t, len(spec.Anchors), len(c.Index))
	assert.Equal(t, DefaultMaxTokens, c.Budget)
	assert.Empty(t, c.Omitted)
	golden.Check(t, filepath.Join("testdata", "change.golden.md"), []byte(c.Render()))

	// Deterministic: building again renders the same bytes.
	assert.Equal(t, c.Render(), Build(spec, Target{Change: idleStopChange(spec)}, Options{}).Render())
}

func TestBuildNeighborsTwoHops(t *testing.T) {
	spec := multi(t)
	c := Build(spec, Target{Change: idleStopChange(spec)}, Options{})
	hops := map[string]int{}
	for _, n := range c.Neighbors {
		hops[n.Name] = n.Hops
	}
	// $Rws.IdleStop references $Rws.Session directly.
	assert.Equal(t, 1, hops["Rws.Session"])
	// Every hop-2 neighbor is a neighbor of some hop-1 neighbor.
	for name, h := range hops {
		if h != 2 {
			continue
		}
		ok := false
		for first, h1 := range hops {
			if h1 == 1 && containsStr(neighbors(spec, first), name) {
				ok = true
			}
		}
		assert.True(t, ok, "$%s at hop 2 has no hop-1 neighbor", name)
	}
	// Sorted by hops, then file, then line.
	assert.True(t, sort.SliceIsSorted(c.Neighbors, func(i, j int) bool {
		a, b := c.Neighbors[i], c.Neighbors[j]
		if a.Hops != b.Hops {
			return a.Hops < b.Hops
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	}))
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestBuildBudgetUsesSections(t *testing.T) {
	spec := multi(t)
	c := Build(spec, Target{Change: idleStopChange(spec)}, Options{MaxTokens: 1500})
	require.NotEmpty(t, c.Files)
	for _, f := range c.Files {
		assert.False(t, f.Full, "%s should be excerpted under a small budget", f.Path)
	}
	golden.Check(t, filepath.Join("testdata", "change-budget.golden.md"), []byte(c.Render()))

	full := Build(spec, Target{Change: idleStopChange(spec)}, Options{MaxTokens: 10, Full: true})
	assert.Len(t, full.Files, len(spec.Files), "--full sends every file")
	for _, f := range full.Files {
		assert.True(t, f.Full)
	}
	assert.Zero(t, full.Budget)
}

func TestBuildAnalysisGolden(t *testing.T) {
	spec := multi(t)
	f := spec.File("rws.spec.md")
	src := f.Source()
	i := strings.Index(string(src), "for 30 s is closed")
	require.Positive(t, i)
	regions := []Region{{File: "rws.spec.md", Range: pos.Range{Start: i, End: i + len("for 30 s")}, Old: "for 20 s"}}
	c := Build(spec, Target{Regions: regions}, Options{})
	require.Len(t, c.Regions, 1)
	assert.Contains(t, c.Regions[0].Text, "**$Rws.IdleStop (idle stop).**")
	golden.Check(t, filepath.Join("testdata", "analysis.golden.md"), []byte(c.Render()))

	all := Build(spec, Target{Files: []string{"hls.spec.md", "index.spec.md", "rws.spec.md"}}, Options{})
	assert.Empty(t, all.Neighbors, "every anchor is a target")
	assert.Len(t, all.Files, 3)
}

func TestChanged(t *testing.T) {
	old := []byte("a\nb\nc\nd\ne\nf\ng\nh\n")
	assert.Nil(t, Changed("x", old, old))

	// One line changed.
	rs := Changed("x", old, []byte("a\nb\nC\nd\ne\nf\ng\nh\n"))
	require.Len(t, rs, 1)
	assert.Equal(t, pos.Range{Start: 4, End: 6}, rs[0].Range)
	assert.Equal(t, "c\n", rs[0].Old)

	// Two edits far apart stay separate; close ones merge.
	rs = Changed("x", old, []byte("A\nb\nc\nd\ne\nf\ng\nH\n"))
	require.Len(t, rs, 2)
	assert.Equal(t, "a\n", rs[0].Old)
	assert.Equal(t, "h\n", rs[1].Old)
	rs = Changed("x", old, []byte("A\nb\nC\nd\ne\nf\ng\nh\n"))
	require.Len(t, rs, 1)
	assert.Equal(t, "a\nb\nc\n", rs[0].Old)

	// A deletion is an empty range where the lines were.
	rs = Changed("x", old, []byte("a\nb\nc\nd\nf\ng\nh\n"))
	require.Len(t, rs, 1)
	assert.True(t, rs[0].Range.Empty())
	assert.Equal(t, "e\n", rs[0].Old)

	// An insertion.
	rs = Changed("x", old, []byte("a\nb\nc\nd\nnew\ne\nf\ng\nh\n"))
	require.Len(t, rs, 1)
	assert.Equal(t, "", rs[0].Old)
	assert.Equal(t, 8, rs[0].Range.Start)
	assert.Equal(t, 12, rs[0].Range.End)

	// A new file is one region.
	rs = Changed("x", nil, []byte("hello\n"))
	require.Len(t, rs, 1)
	assert.Equal(t, pos.Range{Start: 0, End: 6}, rs[0].Range)
}

func TestFirstSentence(t *testing.T) {
	f := mdspec.ParseFile("a.spec.md", []byte("**$A.Rule (rule).** Every   frame MUST arrive. Then more text.\n"))
	require.Len(t, f.Anchors, 1)
	assert.Equal(t, "Every frame MUST arrive.", firstSentence(f, f.Anchors[0]))
}
