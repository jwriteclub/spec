// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/infer"
)

func TestLoadQuestions(t *testing.T) {
	for _, kind := range []string{Gate, Triage} {
		q, err := LoadQuestions(kind, "")
		require.NoError(t, err, kind)
		assert.Contains(t, q.Policy, "A spec is a Markdown document")
		require.Len(t, q.Questions, 4)
		assert.Equal(t, infer.QuestionMaterialChange, q.Questions[0].ID)
		assert.Equal(t, infer.ClassQuestions, []string{q.Questions[1].ID, q.Questions[2].ID, q.Questions[3].ID})
		assert.NotContains(t, q.Policy, "Copyright")
	}
	tq, _ := LoadQuestions(Triage, "")
	assert.Contains(t, tq.Reply, "Reply with ONLY a JSON object")
	gq, _ := LoadQuestions(Gate, "")
	assert.Empty(t, gq.Reply)

	msgs := TriageMessages(tq, "THE EDIT")
	require.Len(t, msgs, 2)
	assert.Contains(t, msgs[0].Content, "## stale_reference")
	assert.Contains(t, msgs[0].Content, tq.Reply)
	assert.Equal(t, "THE EDIT", msgs[1].Content)

	dir := t.TempDir()
	p := filepath.Join(dir, "gate.md")
	block := func(name, text string) string { return "## " + name + "\n\n```text\n" + text + "\n```\n\n" }
	src := block("policy", "P") + block("material_change", "m?") + block("numeric_mismatch", "n?") + block("contradiction", "c?") + block("stale_reference", "s?")
	require.NoError(t, os.WriteFile(p, []byte(src), 0o644))
	q, err := LoadQuestions(Gate, p)
	require.NoError(t, err)
	assert.Equal(t, "P", q.Policy)
	assert.Equal(t, "c?", q.Questions[2].Instructions)
	_, err = LoadQuestions(Triage, p)
	assert.ErrorContains(t, err, `no "## reply" text block`)

	require.NoError(t, os.WriteFile(p, []byte(block("policy", "P")), 0o644))
	_, err = LoadQuestions(Gate, p)
	assert.ErrorContains(t, err, `the gate prompt override `+p+` has no "## material_change" text block`)
}

func TestLoadSpell(t *testing.T) {
	p, err := LoadSpell("")
	require.NoError(t, err)
	assert.Contains(t, p.Policy, "second pass of a spell checker")
	assert.Contains(t, p.Question, "{id}")
	assert.Contains(t, p.Reply, "ID=intended spelling")

	dir := t.TempDir()
	bad := filepath.Join(dir, "spell.md")
	require.NoError(t, os.WriteFile(bad, []byte("## policy\n\n```text\nP\n```\n\n## question\n\n```text\nno placeholder\n```\n\n## reply\n\n```text\nR\n```\n"), 0o644))
	_, err = LoadSpell(bad)
	assert.ErrorContains(t, err, "{id}")
	require.NoError(t, os.WriteFile(bad, []byte("## policy\n\n```text\nP\n```\n"), 0o644))
	_, err = LoadSpell(bad)
	assert.ErrorContains(t, err, `no "## question" text block`)
}
