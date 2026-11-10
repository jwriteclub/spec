// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/spell"
	"github.com/jwriteclub/spec/internal/tui/editor"
)

// spellRun renders one run of flag f in th as the 256-colour profile
// sends it.
func spellRun(t *testing.T, th *Theme, f editor.Flag) string {
	t.Helper()
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ANSI256}
	_, err := w.WriteString(th.Run(f).Render("x"))
	require.NoError(t, err)
	return buf.String()
}

// TestSpellUnderlineTheme: a flagged word is underlined plainly (no dotted
// or curly style) in the near-red spell colour, in every profile; with
// PlainUnderline it is underlined without a colour.
func TestSpellUnderlineTheme(t *testing.T) {
	for _, dark := range []bool{true, false} {
		for _, reduced := range []bool{false, true} {
			for _, ascii := range []bool{false, true} {
				th := NewThemeProfile(dark, false, reduced, ascii)
				role := builtinColorSchemes[0].roles["spell"]
				want := role.Light256
				if dark {
					want = role.Dark256
				}
				run := spellRun(t, th, editor.FSpell)
				if reduced {
					assert.Contains(t, run, "58;5;"+strconv.Itoa(want), "dark=%v reduced ascii=%v", dark, ascii)
				} else {
					assert.Contains(t, run, "58;5;", "dark=%v ascii=%v", dark, ascii)
				}
				assert.NotContains(t, run, "4:", "a plain underline, not dotted or curly")
				assert.NotEqual(t, spellRun(t, th, editor.FDiagHint), run, "not the hint underline")
			}
		}
		plain := spellRun(t, NewTheme(dark, true), editor.FSpell)
		assert.NotContains(t, plain, "58;")
		assert.Contains(t, plain, "4m")
	}
}

// TestSpellFlagRendersUnderlined: after a spell check has cached a flag, the
// editor draws the word with the spell underline and ^P puts the fix in.
func TestSpellFlagRendersUnderlined(t *testing.T) {
	dir := demoProject(t)
	writeFile(t, dir, "demo.spec.md", demoSpec+"\nThe hub will recieve it.\n")
	before := newTestApp(t, testOptions(dir))
	assert.Empty(t, before.ws.Spell, "nothing shows before a spell check ran")

	loaded, err := project.Load(dir, nil)
	require.NoError(t, err)
	p := spell.NewProject(loaded.Spec, nil, "", spell.NewDictionary())
	rep := spell.Run(context.Background(), p, loaded.Spec.Files, nil, nil, spell.OpenCache(dir))
	require.Len(t, rep.Flags, 1)

	a := newTestApp(t, testOptions(dir))
	require.Len(t, a.ws.Spell, 1)
	f := a.ws.Spell[0]
	assert.Equal(t, "recieve", f.Word)
	var spanned bool
	for _, s := range a.cur.Ed.Spans() {
		if s.Start == f.Range.Start && s.End == f.Range.End && s.Flags&editor.FSpell != 0 {
			spanned = true
		}
	}
	assert.True(t, spanned, "the word carries the spell highlight")
	frame := styled(t, a)
	require.Contains(t, ansi.Strip(frame), "The hub will recieve it.")
	var d diag.Diagnostic
	for _, x := range a.ws.FileDiags("demo.spec.md") {
		if x.Code == diag.CodeSpell {
			d = x
		}
	}
	require.Equal(t, diag.CodeSpell, d.Code)
	assert.Contains(t, d.Message, `did you mean "receive"`)

	find(a, "recieve")
	send(a, key("ctrl+p"))
	assert.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "The hub will receive it.")
	assert.Contains(t, a.flash, "recieve is now receive")
}

// TestProposeSpellChecksProposedText: ^P runs the spell check on the
// proposed text, as `spec propose` does; its flags land as spelling notes
// on the proposal's cards.
func TestProposeSpellChecksProposedText(t *testing.T) {
	dir := hookRepo(t)
	nothing := modelJSON(t, map[string]any{"summary": "consistent", "findings": []any{}, "questions": []string{}})
	m := newFakeModel(t, nothing)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })

	f.findIn("above zero")
	for range len("above zero") {
		f.send(key("shift+right"))
	}
	f.typeIn("of at most 5 s once recieved")
	f.send(key("ctrl+p"))
	f.send(key("y"))
	f.until("the cards", func(a *App) bool { return a.job == nil && itemCards(a) == 1 })
	f.do(func(a *App) {
		require.Len(t, a.pending, 1)
		p := a.pending[0]
		require.Len(t, p.Notes, 1)
		n := p.Notes[0]
		assert.Equal(t, "spelling", n.Issue)
		assert.Equal(t, "$Frame.Timing", n.Anchor)
		assert.Equal(t, "recieved", n.Find)
		assert.Equal(t, `"recieved" may be misspelled: did you mean "received"?`, n.Explanation)
	})
	f.send(key("ctrl+r"))
	assert.Contains(t, f.frame(), "cosmetic")
}

// typoSentence has three words a spell check judged typos, in one sentence.
const typoSentence = "\nThe worker will relinqiush the lease, finish the iteraton and log the acquistion.\n"

// seedTypos records the model's typo verdicts on typoSentence's three words
// in dir's spell cache, as a spell check would.
func seedTypos(t *testing.T, dir string) {
	t.Helper()
	loaded, err := project.Load(dir, nil)
	require.NoError(t, err)
	_, cands := spell.NewProject(loaded.Spec, nil, loaded.Project.Config.Name, nil).Check(loaded.Spec.Files, nil)
	fixes := map[string]string{"relinqiush": "relinquish", "iteraton": "iteration", "acquistion": "acquisition"}
	cache := spell.OpenCache(dir)
	for _, c := range cands {
		if fix, ok := fixes[c.Word]; ok {
			cache.Put(c.Key, spell.Verdict{Word: c.Word, Typo: true, Fix: fix, Stage: spell.StageTriage, Models: "m", At: time.Now()})
			delete(fixes, c.Word)
		}
	}
	require.Empty(t, fixes, "every typo is a candidate")
	require.NoError(t, cache.Save())
}

// spellWords lists the flagged words of a's workspace.
func spellWords(a *App) []string {
	out := []string{}
	for _, f := range a.ws.Spell {
		out = append(out, f.Word)
	}
	return out
}

// TestSpellFixKeepsSentenceFlags: fixing one of three typos in a sentence
// with ^P, or typing elsewhere in the sentence, keeps the other flags: the
// verdict on a word outlives the sentence it was judged in.
func TestSpellFixKeepsSentenceFlags(t *testing.T) {
	dir := demoProject(t)
	writeFile(t, dir, "demo.spec.md", demoSpec+typoSentence+"\nWe recieve what occured and seperate it.\n")
	seedTypos(t, dir)
	a := newTestApp(t, testOptions(dir))
	require.Equal(t, []string{"relinqiush", "iteraton", "acquistion", "recieve", "occured", "seperate"}, spellWords(a))

	find(a, "relinqiush")
	send(a, key("ctrl+p"))
	require.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "will relinquish the lease")
	assert.Equal(t, []string{"iteraton", "acquistion", "recieve", "occured", "seperate"}, spellWords(a))

	find(a, "lease")
	send(a, typeText("new ")...)
	require.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "the new lease, finish")
	assert.Equal(t, []string{"iteraton", "acquistion", "recieve", "occured", "seperate"}, spellWords(a))

	find(a, "recieve")
	send(a, key("ctrl+p"))
	require.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "We receive what")
	assert.Equal(t, []string{"iteraton", "acquistion", "occured", "seperate"}, spellWords(a),
		"known misspellings show without a cache entry")
}

// TestModelEditKeepsSpellFlags: typing in a model checks no spec file's
// spelling again; typing in a spec file checks that file only.
func TestModelEditKeepsSpellFlags(t *testing.T) {
	dir := demoProject(t)
	writeFile(t, dir, "spec.toml", "name = \"demo\"\nformal = [\"tla/*.tla\"]\n")
	writeFile(t, dir, "demo.spec.md", demoSpec+typoSentence)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tla"), 0o755))
	writeFile(t, dir, "tla/M.tla", "---- MODULE M ----\nInv == TRUE\n====\n")
	seedTypos(t, dir)
	opts := testOptions(dir)
	opts.Target = Target{File: "tla/M.tla", Line: 2}
	a := newTestApp(t, opts)
	require.Equal(t, []string{"relinqiush", "iteraton", "acquistion"}, spellWords(a))
	computed, flags := a.ws.spell.Computed(), a.ws.Spell
	require.Equal(t, 2, computed, "demo.spec.md and other.spec.md")

	send(a, typeText("x")...)
	require.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "x")
	require.Equal(t, "tla/M.tla", a.cur.Ed.Path())
	assert.Equal(t, computed, a.ws.spell.Computed(), "a model edit recomputes no spelling")
	assert.Same(t, &flags[0], &a.ws.Spell[0])

	require.NoError(t, a.open(Location{Path: "demo.spec.md"}))
	find(a, "lease")
	send(a, typeText("new ")...)
	require.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "the new lease, finish")
	assert.Equal(t, computed+len("new "), a.ws.spell.Computed(), "a keystroke in a spec file recomputes that file only")
	assert.Equal(t, []string{"relinqiush", "iteraton", "acquistion"}, spellWords(a))
}
