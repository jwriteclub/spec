// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/mdspec"
)

// TestMemoRecomputesChangedFiles: the memo recomputes a file's flags only
// when its content, the standing of one of its words, spec.words or the
// cache changed, and gives what Cached gives.
func TestMemoRecomputesChangedFiles(t *testing.T) {
	dict := NewDictionary("the", "hub", "will", "receive", "lease", "store", "segment", "each")
	a := mdspec.ParseFile("a.spec.md", []byte("The hub will recieve the zorblat.\n"))
	b := mdspec.ParseFile("b.spec.md", []byte("Each segment will store the lease.\n"))
	cache := OpenCache(t.TempDir())
	spec := mdspec.Build([]*mdspec.File{a, b})
	_, cands := NewProject(spec, nil, "", dict).Check(spec.Files, nil)
	require.Equal(t, []string{"zorblat"}, words(cands))
	cache.Put(cands[0].Key, Verdict{Word: "zorblat", Typo: true, Fix: "zorblot", Stage: StageTriage, Models: "m", At: time.Now()})

	m := NewMemo()
	m.dict = dict
	flagWords := func(fs []Flag) []string {
		out := []string{}
		for _, f := range fs {
			out = append(out, f.Word)
		}
		return out
	}
	assert.Equal(t, []string{"recieve", "zorblat"}, flagWords(m.Flags(spec, nil, "", cache)))
	assert.Equal(t, 2, m.Computed())

	// The same files again (a model edit): nothing is recomputed, and the
	// same flags come back.
	first := m.Flags(spec, nil, "", cache)
	again := m.Flags(mdspec.Build([]*mdspec.File{a, b}), nil, "", cache)
	assert.Equal(t, 2, m.Computed())
	assert.Same(t, &first[0], &again[0], "the memo's flags, not a recomputation")

	// The same content parsed again: still nothing.
	a2 := mdspec.ParseFile("a.spec.md", a.Source())
	m.Flags(mdspec.Build([]*mdspec.File{a2, b}), nil, "", cache)
	assert.Equal(t, 2, m.Computed())

	// Editing b with words a does not use recomputes b only.
	b2 := mdspec.ParseFile("b.spec.md", []byte("Each segment will store the lease twice.\n"))
	assert.Equal(t, []string{"recieve", "zorblat"}, flagWords(m.Flags(mdspec.Build([]*mdspec.File{a2, b2}), nil, "", cache)))
	assert.Equal(t, 3, m.Computed())

	// b using zorblat in code makes it an identifier: a's flag goes.
	b3 := mdspec.ParseFile("b.spec.md", []byte("Each segment will store the lease in `zorblat`.\n"))
	spec3 := mdspec.Build([]*mdspec.File{a2, b3})
	got := m.Flags(spec3, nil, "", cache)
	assert.Equal(t, []string{"recieve"}, flagWords(got))
	assert.Equal(t, 5, m.Computed(), "b changed, and a's word zorblat changed standing")
	assert.Equal(t, Cached(NewProject(spec3, nil, "", dict), spec3.Files, nil, cache), got)

	// spec.words and the cache invalidate every file.
	m.Flags(spec3, []string{"recieve"}, "", cache)
	assert.Equal(t, 7, m.Computed())
	cache.Put("other", Verdict{Word: "other", Stage: StageGate, At: time.Now()})
	m.Flags(spec3, []string{"recieve"}, "", cache)
	assert.Equal(t, 9, m.Computed())
	m.Flags(spec3, []string{"recieve"}, "", cache)
	assert.Equal(t, 9, m.Computed())
}

// TestMemoMatchesCached: through a run of edits (words gained and lost,
// code, anchors, files added and removed), the memo gives what Cached
// computes from scratch.
func TestMemoMatchesCached(t *testing.T) {
	dict := NewDictionary("the", "hub", "will", "receive", "lease", "store", "segment", "each", "pool")
	steps := [][]string{
		{"a.spec.md", "The hub will recieve the zorblat and the quindle.\n", "b.spec.md", "Each segment will store the lease.\n"},
		{"a.spec.md", "The hub will recieve the zorblat and the quindle.\n", "b.spec.md", "Each segment will store the quindle.\n"},
		{"a.spec.md", "The hub will recieve the zorblat and the quindle.\n", "b.spec.md", "Each `zorblat` will store the quindle.\n"},
		{"a.spec.md", "The hub will recieve the zorblat and the quindle.\n", "b.spec.md", "# Pool\n\n**Def $Pool.Quindle (pool).** Each pool.\n"},
		{"a.spec.md", "The hub will recieve the zorblat and the quindle.\n", "b.spec.md", "Each segment will store the lease.\n", "c.spec.md", "The quindle, the quindle.\n"},
		{"a.spec.md", "The hub will recieve the zorblat and the quindle.\n", "c.spec.md", "The quindle, the quindle.\n"},
		{"a.spec.md", "The hub will recieve the zorblat and the quindle.\n"},
	}
	cache := OpenCache(t.TempDir())
	first := build(t, steps[0]...)
	_, cands := NewProject(first, nil, "", dict).Check(first.Files, nil)
	for _, c := range cands {
		cache.Put(c.Key, Verdict{Word: c.Word, Typo: true, Fix: c.Word + "x", Stage: StageTriage, Models: "m", At: time.Now()})
	}
	m := NewMemo()
	m.dict = dict
	for i, step := range steps {
		spec := build(t, step...)
		want := Cached(NewProject(spec, nil, "", dict), spec.Files, nil, cache)
		if want == nil {
			want = []Flag{}
		}
		assert.Equal(t, want, m.Flags(spec, nil, "", cache), "step %d", i)
	}
}
