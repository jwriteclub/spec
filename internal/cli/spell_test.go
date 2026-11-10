// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/spell"
)

// spellWords is the word list of the spell tests: every word of the demo
// specs and the edits below, so only the planted words are unknown.
const spellWords = `demo terms def time media timestamp carried by frame in ticks of rules fragment timing every must
have duration above zero checked order arrive see prose that mentions for the reader other extras extra limit queue hold at
most items hub will hold a and it`

// spellSetup is setupInfer with a canned Jev and triage for the spell
// check's model pass.
func spellSetup(t *testing.T, canned string) *inferSetup {
	t.Helper()
	is := setupInfer(t, okReply(modelReply{Summary: "fine"}.json(t)))
	is.spellDict = spell.NewDictionary(strings.Fields(spellWords)...)
	path := filepath.Join(t.TempDir(), "canned.toml")
	require.NoError(t, os.WriteFile(path, []byte(canned), 0o644))
	t.Cleanup(func() { infer.ForgetCanned(path) })
	writeUserConfig(t, is.env, "default = \"fake\"\n[providers.fake]\nbase_url = \""+is.srv.URL+"\"\nmodel = \"check-model\"\n"+
		"[providers.canned]\nkind = \"canned\"\npath = \""+path+"\"\nmodel = \"canned\"\n[gate]\nprovider = \"canned\"\n[triage]\nprovider = \"canned\"\n")
	return is
}

// TestAnalyzeSpellCheck: analyze spell-checks the edited text: a known
// misspelling at once, and a word Jev flags and triage fixes; the second
// run answers from the cache.
func TestAnalyzeSpellCheck(t *testing.T) {
	requireGit(t)
	is := spellSetup(t, "[[spell_gate]]\nscores = { typo_1 = 0.93 }\n[[spell]]\nreply = \"1=hold\"\n")
	replaceIn(t, is.dir, "demo.spec.md", "Prose that mentions $Frame.Order for the reader.", "The hub will recieve and holdd it; see $Frame.Order.")

	jr := is.run(t, "analyze")
	require.Equal(t, 0, jr.code, jr.stdout)
	d := analyzeOf(t, jr)
	require.NotNil(t, d.Spell)
	assert.Empty(t, d.Spell.Error)
	require.Len(t, d.Spell.Flags, 2, "%+v", d.Spell.Flags)
	assert.Equal(t, "recieve", d.Spell.Flags[0].Word)
	assert.Equal(t, spell.ByKnown, d.Spell.Flags[0].By)
	assert.Equal(t, "holdd", d.Spell.Flags[1].Word)
	assert.Equal(t, "hold", d.Spell.Flags[1].Fix)
	assert.Equal(t, spell.ByModel, d.Spell.Flags[1].By)
	assert.Equal(t, 1, d.Spell.Stats.GateCalls)
	assert.Equal(t, 1, d.Spell.Stats.TriageCalls)

	h := is.runHuman(t, "", "analyze")
	require.Equal(t, 0, h.code, h.stderr)
	assert.Contains(t, h.stdout, `demo.spec.md:15:14: spell: "recieve" may be misspelled: did you mean "receive"?`)
	assert.Contains(t, h.stdout, `spell: "holdd" may be misspelled: did you mean "hold"?`)
	assert.NotContains(t, h.stdout, "model pass failed", "the second run asks nothing: the canned file has no entries left")

	// lint --spell shows the cached flags without a model.
	h = is.runHuman(t, "", "lint", "--spell")
	assert.Contains(t, h.stdout, `"holdd" may be misspelled`)
	h = is.runHuman(t, "", "lint")
	assert.NotContains(t, h.stdout, "spell")
}

// TestAnalyzeSpellFailsClosed: a gate that fails leaves only the known
// misspelling, and the output says why.
func TestAnalyzeSpellFailsClosed(t *testing.T) {
	requireGit(t)
	is := spellSetup(t, "[[spell_gate]]\nerror = \"http_500\"\n")
	replaceIn(t, is.dir, "demo.spec.md", "Prose that mentions $Frame.Order for the reader.", "The hub will recieve and holdd it; see $Frame.Order.")
	h := is.runHuman(t, "", "analyze")
	require.Equal(t, 0, h.code, h.stderr)
	assert.Contains(t, h.stdout, `"recieve" may be misspelled`)
	assert.NotContains(t, h.stdout, `"holdd"`)
	assert.Contains(t, h.stdout, "spell check: the model pass failed")
}

// TestAnalyzeCheckFailureStopsSpell: a check that fails returns at once,
// without waiting for a slow spell check.
func TestAnalyzeCheckFailureStopsSpell(t *testing.T) {
	requireGit(t)
	is := spellSetup(t, "[[spell_gate]]\nscores = { typo_1 = 0.9 }\ndelay_ms = 60000\n")
	// The check's provider refuses every connection.
	cfgPath := filepath.Join(is.env["XDG_CONFIG_HOME"], "spec", "config.toml")
	cfg, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	writeUserConfig(t, is.env, strings.Replace(string(cfg), is.srv.URL, "http://127.0.0.1:1", 1))
	replaceIn(t, is.dir, "demo.spec.md", "Prose that mentions $Frame.Order for the reader.", "The hub will holdd it; see $Frame.Order.")
	start := time.Now()
	jr := is.run(t, "analyze")
	assert.Equal(t, 1, jr.code, jr.stdout)
	assert.Less(t, time.Since(start), 20*time.Second, "the failure does not wait for the spell check")
}

// TestChangeSpellChecksProposedText: spec change checks the new text only.
func TestChangeSpellChecksProposedText(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "fine"}.json(t)))
	is.spellDict = spell.NewDictionary(strings.Fields(spellWords)...)
	write(t, is.dir, "body.md", "Every fragment MUST have a duration above zero in $Time.Media; seperately checked.")
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	d := filedOf(t, jr)
	require.NotNil(t, d.Spell)
	require.Len(t, d.Spell.Flags, 1)
	f := d.Spell.Flags[0]
	assert.Equal(t, "$Frame.Timing", f.File)
	assert.Equal(t, "seperately", f.Word)
	assert.Equal(t, "separately", f.Fix)
	assert.Equal(t, 1, f.Line)
}

// TestSpellLintRule: spell is a lint rule. "off" turns the spell check off
// (no flags, no model call); another level sets its flags' severity.
func TestSpellLintRule(t *testing.T) {
	requireGit(t)
	is := spellSetup(t, "") // no canned entries: any model call would fail
	replaceIn(t, is.dir, "demo.spec.md", "Prose that mentions $Frame.Order for the reader.", "The hub will recieve and holdd it; see $Frame.Order.")
	cfg := readFile(t, is.dir, "spec.toml")

	write(t, is.dir, "spec.toml", cfg+"\n[lint]\nspell = \"off\"\n")
	jr := is.run(t, "analyze")
	require.Equal(t, 0, jr.code, jr.stdout)
	d := analyzeOf(t, jr)
	assert.True(t, d.Spell.Off)
	assert.Empty(t, d.Spell.Flags)
	assert.Empty(t, d.Spell.Error, "no model was called")
	h := is.runHuman(t, "", "lint", "--spell")
	assert.NotContains(t, h.stdout, "recieve")

	write(t, is.dir, "spec.toml", cfg+"\n[lint]\nspell = \"warning\"\n")
	h = is.runHuman(t, "", "lint", "--spell")
	assert.Contains(t, h.stdout, `warning: "recieve" may be misspelled: did you mean "receive"? [spell]`)
	write(t, is.dir, "spec.toml", cfg+"\n[lint]\nspel = \"off\"\n")
	h = is.runHuman(t, "", "lint")
	assert.Contains(t, h.stdout+h.stderr, "spell", "the known rules listed include spell")
}
