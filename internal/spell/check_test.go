// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/mdspec"
)

// testDict is a small word list, so the tests do not depend on the
// system's.
func testDict() *Dictionary {
	return NewDictionary("the", "a", "client", "must", "receive", "ack", "before", "it", "retries", "retry", "live", "idle",
		"timeout", "time", "out", "stream", "camera", "each", "proxies", "proxy", "hub", "fetch", "credential", "start", "up",
		"delay", "double", "after", "failure", "word", "words", "segment", "store", "separately", "until", "lease", "held",
		"held", "evict", "oldest", "policy", "pool", "slot", "free", "least", "recently", "used", "read", "ahead", "data", "base",
		"color", "spell", "check", "end", "second", "project", "only", "one", "frame", "order", "timing", "on", "is", "and", "of", "to")
}

// build parses files (path, source pairs) into a spec.
func build(t *testing.T, pairs ...string) *mdspec.Spec {
	t.Helper()
	require.Zero(t, len(pairs)%2)
	var fs []*mdspec.File
	for i := 0; i < len(pairs); i += 2 {
		fs = append(fs, mdspec.ParseFile(pairs[i], []byte(pairs[i+1])))
	}
	return mdspec.Build(fs)
}

func words(cs []Candidate) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Word)
	}
	return out
}

func TestCheckClassical(t *testing.T) {
	spec := build(t, "a.spec.md",
		"# Pool\n\n**Def $Pool.EvictOldest (oldest).** The pool frees the least recently used slot.\n\n"+
			"The client MUST recieve an ack before it retries. The colour is red.\n\n"+
			"Each camera stream proxies via lensd, lensd and lensd.\n\n"+
			"When full the EvictOldset policy frees a slot. The liveIdleTimout ends it.\n\n"+
			"Use `max_transcoders` as max_transcodres. A readahead and a database.\n",
		"b.spec.md", "Playhelper fetches credentials. Cameras use quuxlib.\n", "c.spec.md", "The playhelper starts.\n")
	p := NewProject(spec, nil, "", testDict())
	flags, cands := p.Check(spec.Files, nil)

	require.Len(t, flags, 2)
	assert.Equal(t, "recieve", flags[0].Word)
	assert.Equal(t, "receive", flags[0].Fix)
	assert.Equal(t, ByKnown, flags[0].By)
	assert.Equal(t, 5, flags[0].Line)
	assert.Equal(t, "colour", flags[1].Word)
	assert.Equal(t, "color", flags[1].Fix, "US spellings")

	got := words(cands)
	assert.NotContains(t, got, "lensd", "used three times: project vocabulary")
	assert.NotContains(t, got, "Playhelper", "used in two files")
	assert.Contains(t, got, "quuxlib", "used once")
	assert.Contains(t, got, "EvictOldset")
	assert.Contains(t, got, "liveIdleTimout")
	assert.Contains(t, got, "max_transcodres")
	assert.NotContains(t, got, "readahead", "a compound of dictionary words")
	assert.NotContains(t, got, "database")
	for _, c := range cands {
		switch c.Word {
		case "EvictOldset":
			assert.Equal(t, "Oldset", c.Part)
			assert.Equal(t, 5, c.PartOff)
		case "liveIdleTimout":
			assert.Equal(t, "Timout", c.Part)
			assert.Contains(t, c.Sentence, "The liveIdleTimout ends it.")
			assert.Equal(t, "liveIdleTimout", c.Sentence[c.Mark:c.Mark+len(c.Word)])
		}
		assert.Len(t, c.Key, 16)
	}

	info := p.Info(cands[indexOf(got, "max_transcodres")])
	assert.Contains(t, info, "max_transcodres: 1 use in the project")
	assert.Contains(t, info, `"transcodres" unknown`)
	assert.Contains(t, info, "similar project terms: 'max_transcoders'")
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func TestCheckScopeAndProjectWords(t *testing.T) {
	src := "Para one has zorblat.\n\nPara two has quuxfoo and recieve.\n"
	spec := build(t, "a.spec.md", src)
	p := NewProject(spec, []string{"quuxfoo", "recieve"}, "", testDict())
	_, cands := p.Check(spec.Files, nil)
	assert.Equal(t, []string{"Para", "zorblat", "Para"}, words(cands), "spec.words accepts a word, even a known misspelling")

	p = NewProject(spec, nil, "", testDict())
	second := Scope{"a.spec.md": {{Start: 23, End: len(src)}}}
	flags, cands := p.Check(spec.Files, second)
	assert.Equal(t, []string{"Para", "quuxfoo"}, words(cands))
	require.Len(t, flags, 1)
	assert.Equal(t, "recieve", flags[0].Word)
	_, cands = p.Check(spec.Files, Scope{"other.spec.md": nil})
	assert.Empty(t, cands)
}

func TestDictionaryForms(t *testing.T) {
	d := NewDictionary("retry", "receive", "stop", "time", "out", "check", "sum", "read", "ahead", "invalid", "ted", "connect", "large")
	for _, w := range []string{"retries", "retried", "receives", "received", "receiving", "stopped", "stopping", "timeout",
		"timeouts", "checksum", "readahead", "reconnect", "larger", "largest"} {
		assert.True(t, d.has(w), w)
	}
	for _, w := range []string{"invalidted", "recieve", "reconect", "chuncks"} {
		assert.False(t, d.has(w), w)
	}
}

func TestSystemDictionaryAbsent(t *testing.T) {
	d := NewDictionary()
	assert.True(t, d.Empty())
	spec := build(t, "a.spec.md", "The recieve path uses quorum logic and quorum votes and quorum.\n")
	p := NewProject(spec, nil, "", d)
	flags, cands := p.Check(spec.Files, nil)
	require.Len(t, flags, 1, "known misspellings work without a word list")
	assert.NotContains(t, words(cands), "quorum", "frequent words are vocabulary")
	assert.True(t, p.Valid("quorum"), "a project term is a valid fix")
	assert.True(t, p.Valid("receive"), "a known correction is a valid fix")
	assert.False(t, p.Valid("zorblat"))
}

func TestWordsFile(t *testing.T) {
	dir := t.TempDir()
	ws, err := ReadWords(dir)
	require.NoError(t, err)
	assert.Empty(t, ws)
	changed, err := AddWord(dir, "lensd")
	require.NoError(t, err)
	assert.True(t, changed)
	changed, err = AddWord(dir, "LensD")
	require.NoError(t, err)
	assert.False(t, changed, "already there, case-insensitively")
	_, err = AddWord(dir, "two words")
	assert.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, WordsFile), []byte("# c\nalpha # trailing\n\nbeta"), 0o644))
	_, err = AddWord(dir, "gamma")
	require.NoError(t, err)
	ws, err = ReadWords(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, ws)
}
