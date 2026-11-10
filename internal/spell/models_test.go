// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer"
)

func TestParseFixes(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		n     int
		want  map[int]string
	}{
		{"none", "-", 4, map[int]string{}},
		{"none with spaces", "  -\n", 4, map[int]string{}},
		{"empty", "", 4, map[int]string{}},
		{"plain", "1=receive\n3=delay", 4, map[int]string{1: "receive", 3: "delay"}},
		{"spaces", " 2 = until \n 4= reconnect", 4, map[int]string{2: "until", 4: "reconnect"}},
		{"leading zeros", "01=goroutine\n03=idempotent", 16, map[int]string{1: "goroutine", 3: "idempotent"}},
		{"glued", "06=malformed16=invalidated", 16, map[int]string{6: "malformed", 16: "invalidated"}},
		{"glued digits", "1=sha2562=receive", 3, map[int]string{1: "sha256", 2: "receive"}},
		{"fence and label", "```\nTypos:\n5=chunks\n```", 8, map[int]string{5: "chunks"}},
		{"punctuation and note", "3=delay.\n4=\"until\" (typo)\n- 5=chunks,", 8, map[int]string{3: "delay", 4: "until", 5: "chunks"}},
		{"out of range and duplicates", "9=nope\n0=zero\n2=first\n2=second", 4, map[int]string{2: "first"}},
		{"identifier", "6=liveIdleTimeout", 8, map[int]string{6: "liveIdleTimeout"}},
		{"bad line skipped", "the answer is\n2=until", 4, map[int]string{2: "until"}},
	}
	for _, c := range cases {
		got, err := ParseFixes(c.reply, c.n)
		require.NoError(t, err, c.name)
		assert.Equal(t, c.want, got, c.name)
	}
	_, err := ParseFixes("I think candidate two is a typo of until.", 4)
	assert.ErrorIs(t, err, ErrUnparsed)
	_, err = ParseFixes("9=nope", 4)
	assert.ErrorIs(t, err, ErrUnparsed, "nothing in range")
}

func TestGuard(t *testing.T) {
	spec := build(t, "a.spec.md", "Use `max_transcoders` and `liveIdleTimeout`. The playhelper runs, playhelper, playhelper.\n")
	p := NewProject(spec, nil, "", testDict())
	c := Candidate{Word: "recieve"}
	got, ok := p.Guard(c, "receive")
	assert.True(t, ok)
	assert.Equal(t, "receive", got)
	_, ok = p.Guard(c, "obtain")
	assert.False(t, ok, "a synonym is too far")
	_, ok = p.Guard(c, "Recieve")
	assert.False(t, ok, "the same word in another case is no fix")
	_, ok = p.Guard(Candidate{Word: "dealy"}, "dealt")
	assert.False(t, ok, "not a word the dictionary or project knows")
	got, ok = p.Guard(Candidate{Word: "Dealy"}, "delay")
	assert.True(t, ok)
	assert.Equal(t, "Delay", got, "case follows the word")

	id := Candidate{Word: "liveIdleTimout", Part: "Timout", PartOff: 8}
	got, ok = p.Guard(id, "liveIdleTimeout")
	assert.True(t, ok)
	assert.Equal(t, "liveIdleTimeout", got)
	got, ok = p.Guard(id, "Timeout")
	assert.True(t, ok, "a fix of only the misspelled part")
	assert.Equal(t, "liveIdleTimeout", got)
	_, ok = p.Guard(id, "deadIdleTimeout")
	assert.False(t, ok, "more than two edits")

	got, ok = p.Guard(Candidate{Word: "playhleper"}, "playhelper")
	assert.True(t, ok, "a project term")
	assert.Equal(t, "playhelper", got)
	got, ok = p.Guard(Candidate{Word: "max_transcodres", Part: "transcodres", PartOff: 4}, "max_transcoders")
	assert.True(t, ok)
	assert.Equal(t, "max_transcoders", got)
	_, ok = p.Guard(c, "recieve receive")
	assert.False(t, ok)
}

func TestCache(t *testing.T) {
	dir := t.TempDir()
	c := OpenCache(dir)
	assert.Same(t, c, OpenCache(dir), "one cache per project in a process")
	_, ok := c.Lookup("k1")
	assert.False(t, ok)
	now := time.Now()
	c.Put("k1", Verdict{Word: "recieve", Typo: true, Fix: "receive", Stage: StageTriage, Models: "m", At: now})
	c.Put("old", Verdict{Word: "x", Stage: StageGate, Models: "m", At: now.Add(-100 * 24 * time.Hour)})
	require.NoError(t, c.Save())
	raw, err := os.ReadFile(filepath.Join(dir, ".spec", "spell", "verdicts.json"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"recieve"`)
	assert.NotContains(t, string(raw), `"old"`, "verdicts older than 90 days are dropped")

	// Another process's cache (a fresh one on the same file) sees it.
	other := &Cache{path: c.path, entries: map[string]Verdict{}, dirty: map[string]bool{}}
	v, ok := other.Lookup("k1")
	require.True(t, ok)
	assert.Equal(t, "receive", v.Fix)
	other.Put("k2", Verdict{Word: "dealy", Typo: true, Fix: "delay", Models: "m", At: now})
	require.NoError(t, other.Save())
	time.Sleep(10 * time.Millisecond)
	_, ok = c.Lookup("k2")
	assert.True(t, ok, "the first cache re-reads a changed file")

	// Removing the file invalidates every verdict.
	require.NoError(t, os.Remove(c.Path()))
	_, ok = c.Lookup("k1")
	assert.False(t, ok)
	assert.Equal(t, 0, c.Len())
}

// cannedModels builds Models over a canned replies file.
func cannedModels(t *testing.T, replies string) *Models {
	t.Helper()
	path := filepath.Join(t.TempDir(), "canned.toml")
	require.NoError(t, os.WriteFile(path, []byte(replies), 0o644))
	t.Cleanup(func() { infer.ForgetCanned(path) })
	p := config.Provider{Kind: config.KindCanned, Path: path, Model: "jev"}
	g, err := infer.OpenGate("jev", p, os.Getenv)
	require.NoError(t, err)
	tp := config.Provider{Kind: config.KindCanned, Path: path, Model: "glm"}
	tr, err := infer.Open("glm", tp, os.Getenv)
	require.NoError(t, err)
	return &Models{Gate: g, Triage: tr, Prompt: Prompt{Policy: "policy", Question: "Is {id} a typo?", Reply: "reply ID=fix"}}
}

const pipelineSpec = "# Pool\n\n" +
	"Each camera stream ends after liveIdleTimout seconds. `liveIdleTimeout` is set.\n\n" +
	"The hub fetches each credential from quuxlib.\n\n" +
	"The lease is held untill the delay ends; the retry dealy doubles.\n\n" +
	"Segments are stored by zorblat.\n"

func TestRunPipeline(t *testing.T) {
	dir := t.TempDir()
	spec := build(t, "a.spec.md", pipelineSpec)
	p := NewProject(spec, nil, "", testDict())
	_, cands := p.Check(spec.Files, nil)
	// untill is a known misspelling; the rest are words.
	require.Equal(t, []string{"liveIdleTimout", "quuxlib", "dealy", "zorblat"}, words(cands))
	m := cannedModels(t, `
[[spell_gate]]
scores = { typo_1 = 0.95, typo_2 = 0.01, typo_3 = 0.98, typo_4 = 0.5 }
cost_usd = 0.0001
[[spell_gate]]
scores = { typo_1 = 0.6 }
[[spell]]
reply = "1=liveIdleTimeout\n2=delay\n3=zorblot"
`)
	cache := OpenCache(dir)
	rep := Run(context.Background(), p, spec.Files, nil, m, cache)
	require.Empty(t, rep.Error)
	assert.Equal(t, "jev|glm|"+m.Prompt.Version(), rep.Models)
	var got []string
	for _, f := range rep.Flags {
		got = append(got, f.By+":"+f.Word+">"+f.Fix)
	}
	assert.Equal(t, []string{"model:liveIdleTimout>liveIdleTimeout", "known:untill>until", "model:dealy>delay"}, got,
		"zorblat's fix is not a word, so the guard drops it")
	assert.Equal(t, 4, rep.Stats.Candidates)
	assert.Equal(t, 4, rep.Stats.Asked)
	assert.Equal(t, 2, rep.Stats.GateCalls, "one batch and one re-ask")
	assert.Equal(t, 1, rep.Stats.TriageCalls)
	assert.Equal(t, 3, rep.Stats.Escalated)
	assert.InDelta(t, 0.0001, rep.Stats.CostUSD, 1e-9)

	// Cached: the editor shows the same flags without a model, and a
	// second run asks nothing (the canned file has no entries left).
	shown := Cached(p, spec.Files, nil, cache)
	assert.Len(t, shown, 3)
	rep = Run(context.Background(), p, spec.Files, nil, m, cache)
	require.Empty(t, rep.Error)
	assert.Equal(t, 4, rep.Stats.Cached)
	assert.Equal(t, 0, rep.Stats.GateCalls)
	assert.Len(t, rep.Flags, 3)

	// Editing the sentence keeps the verdicts on its words: dealy is still
	// a typo, and untill is a known misspelling, shown without the cache.
	edited := build(t, "a.spec.md", strings.Replace(pipelineSpec, "the retry dealy doubles", "the retry dealy triples", 1))
	shown = Cached(NewProject(edited, nil, "", testDict()), edited.Files, nil, cache)
	got = nil
	for _, f := range shown {
		got = append(got, f.By+":"+f.Word+">"+f.Fix)
	}
	assert.Equal(t, []string{"model:liveIdleTimout>liveIdleTimeout", "known:untill>until", "model:dealy>delay"}, got)
}

// TestCachedSentenceEdits: three typos in one sentence. Fixing one, or
// editing anywhere else in the sentence, changes every candidate's key;
// the other two keep their flags through the verdict on their word, and a
// word judged correct stays unflagged.
func TestCachedSentenceEdits(t *testing.T) {
	const sent = "The worker will relinqiush the lease, finish the iteraton and log the acquistion of a zorblat.\n"
	dict := NewDictionary("the", "worker", "will", "lease", "finish", "and", "log", "of", "we", "what", "on", "path",
		"receive", "occurred", "relinquish", "iteration", "acquisition")
	spec := build(t, "a.spec.md", sent)
	p := NewProject(spec, nil, "", dict)
	_, cands := p.Check(spec.Files, nil)
	require.Equal(t, []string{"relinqiush", "iteraton", "acquistion", "zorblat"}, words(cands))
	cache := OpenCache(t.TempDir())
	fixes := map[string]string{"relinqiush": "relinquish", "iteraton": "iteration", "acquistion": "acquisition"}
	for _, c := range cands {
		v := Verdict{Word: c.Word, Stage: StageTriage, Models: "m", At: time.Now()}
		if fix, ok := fixes[c.Word]; ok {
			v.Typo, v.Fix = true, fix
		}
		cache.Put(c.Key, v)
	}
	// Known misspellings in the same sentence: recieve, occured.
	const known = "We recieve what occured on the relinqiush path.\n"
	flagged := func(src string) []string {
		s := build(t, "a.spec.md", src)
		var out []string
		for _, f := range Cached(NewProject(s, nil, "", dict), s.Files, nil, cache) {
			out = append(out, f.Word+">"+f.Fix)
		}
		return out
	}
	assert.Equal(t, []string{"relinqiush>relinquish", "iteraton>iteration", "acquistion>acquisition"}, flagged(sent))
	assert.Equal(t, []string{"iteraton>iteration", "acquistion>acquisition"},
		flagged(strings.Replace(sent, "relinqiush", "relinquish", 1)), "fixing one keeps the others")
	assert.Equal(t, []string{"relinqiush>relinquish", "iteraton>iteration", "acquistion>acquisition"},
		flagged(strings.Replace(sent, "The worker", "A worker", 1)), "an edit elsewhere in the sentence keeps all three")
	assert.Equal(t, []string{"Relinqiush>Relinquish"}, flagged("Relinqiush the lease.\n"), "the fix follows the word's capitals")
	assert.Equal(t, []string{"recieve>receive", "occured>occurred", "relinqiush>relinquish"}, flagged(known))
	assert.Equal(t, []string{"occured>occurred", "relinqiush>relinquish"},
		flagged(strings.Replace(known, "recieve", "receive", 1)), "known misspellings show without a cache entry")
}

func TestRunFailsClosed(t *testing.T) {
	spec := build(t, "a.spec.md", pipelineSpec)
	p := NewProject(spec, nil, "", testDict())
	cases := map[string]string{
		"gate error":     "[[spell_gate]]\nerror = \"http_500\"\n",
		"gate missing":   "",
		"triage error":   "[[spell_gate]]\nscores = { typo_1 = 0.9, typo_2 = 0.9, typo_3 = 0.9, typo_4 = 0.9 }\n[[spell]]\nerror = \"timeout\"\n",
		"triage garbled": "[[spell_gate]]\nscores = { typo_1 = 0.9, typo_2 = 0.9, typo_3 = 0.9, typo_4 = 0.9 }\n[[spell]]\nreply = \"Sorry, I cannot help with that.\"\n",
	}
	for name, replies := range cases {
		cache := OpenCache(t.TempDir())
		rep := Run(context.Background(), p, spec.Files, nil, cannedModels(t, replies), cache)
		assert.NotEmpty(t, rep.Error, name)
		require.Len(t, rep.Flags, 1, name)
		assert.Equal(t, ByKnown, rep.Flags[0].By, "%s: only the known misspelling", name)
		for _, c := range func() []Candidate { _, cs := p.Check(spec.Files, nil); return cs }() {
			v, ok := cache.Lookup(c.Key)
			assert.False(t, ok && v.Typo, "%s: no typo verdict is cached for %s", name, c.Word)
		}
	}
	// Without models the pass is off: known misspellings only, no error.
	rep := Run(context.Background(), p, spec.Files, nil, nil, OpenCache(t.TempDir()))
	assert.Empty(t, rep.Error)
	assert.Empty(t, rep.Models)
	assert.Len(t, rep.Flags, 1)
}
