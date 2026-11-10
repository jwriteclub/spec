// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/golden"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

const small = `# demo

## 1. Rules

**$Net.Timeout (timeout).** A request times out after 30 s.

**$Net.Retry (retry).** A request that hits $Net.Timeout is retried twice, 60 s apart.

Clients see $Net.Timeout as an error.
`

func smallSpec() *mdspec.Spec {
	return mdspec.Build([]*mdspec.File{mdspec.ParseFile("net.spec.md", []byte(small))})
}

func TestEmbedded(t *testing.T) {
	for _, k := range []string{Change, Analyze, Revise, Commit} {
		text, err := Embedded(k)
		require.NoError(t, err, k)
		assert.NotContains(t, text, "Copyright", "the copyright comment is stripped")
		assert.False(t, strings.HasPrefix(text, "\n"))
		assert.Contains(t, text, "exact", k)
	}
	_, err := Embedded("nope")
	assert.Error(t, err)

	an, _ := Embedded(Analyze)
	for _, want := range []string{"QUANTITIES", "REFERENCES", "RULES AGAINST RULES", "\"checks\"", "numeric_mismatch", "questions", "\"find\"", "exactly once"} {
		assert.Contains(t, an, want)
	}
	ch, _ := Embedded(Change)
	for _, want := range []string{"inventory", "Never edit the changed anchor itself", "do not guess", "new truth", "fixed_by", "\"summary\""} {
		assert.Contains(t, ch, want)
	}
}

func TestLoadOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mine.md")
	require.NoError(t, os.WriteFile(p, []byte("<!-- note -->\n\nMy prompt.\n"), 0o644))
	text, err := Load(Analyze, p)
	require.NoError(t, err)
	assert.Equal(t, "My prompt.\n", text)

	_, err = Load(Analyze, filepath.Join(dir, "missing.md"))
	assert.ErrorContains(t, err, "analyze prompt override")
	require.NoError(t, os.WriteFile(p, []byte("<!-- only a comment -->\n"), 0o644))
	_, err = Load(Change, p)
	assert.ErrorContains(t, err, "empty")

	text, err = Load(Change, "")
	require.NoError(t, err)
	emb, _ := Embedded(Change)
	assert.Equal(t, emb, text)
}

// renderGolden writes messages as JSON for a golden file.
func renderGolden(t *testing.T, name string, kind string, c *specctx.Context) {
	t.Helper()
	system, err := Embedded(kind)
	require.NoError(t, err)
	msgs := Messages(kind, system, c)
	require.Len(t, msgs, 2)
	assert.Equal(t, "system", msgs[0].Role)
	assert.Equal(t, "user", msgs[1].Role)
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString("=== " + m.Role + " ===\n" + m.Content)
		if !strings.HasSuffix(m.Content, "\n") {
			b.WriteString("\n")
		}
	}
	golden.Check(t, filepath.Join("testdata", name), []byte(b.String()))
}

func TestMessagesGolden(t *testing.T) {
	spec := smallSpec()
	a := spec.Anchor("Net.Timeout")
	f := spec.File(a.File)
	old := f.Text(a.BodyRange)
	ch := &specctx.Change{
		Action: specctx.ActionChange, Name: a.Name, File: a.File, OldKind: a.Kind, NewKind: a.Kind,
		OldText: old, NewText: strings.Replace(old, "30 s", "45 s", 1), Why: "slow links",
	}
	renderGolden(t, "change.golden.txt", Change, specctx.Build(spec, specctx.Target{Change: ch}, specctx.Options{}))

	i := strings.Index(small, "30 s")
	regions := []specctx.Region{{File: "net.spec.md", Range: pos.Range{Start: i, End: i + 4}, Old: "20 s"}}
	renderGolden(t, "analyze.golden.txt", Analyze, specctx.Build(spec, specctx.Target{Regions: regions}, specctx.Options{}))
	renderGolden(t, "analyze-all.golden.txt", Analyze, specctx.Build(spec, specctx.Target{Files: []string{"net.spec.md"}}, specctx.Options{}))
}

func TestReviseMessages(t *testing.T) {
	system, err := Embedded(Revise)
	require.NoError(t, err)
	msgs := ReviseMessages(system, ReviseInput{File: "net.spec.md", FileText: small, Find: "after 30 s.", Replace: "after \"45\" s.", Explanation: "x", Feedback: "use 45"})
	require.Len(t, msgs, 2)
	assert.Contains(t, msgs[1].Content, `<file path="net.spec.md">`)
	line := ""
	for _, l := range strings.Split(msgs[1].Content, "\n") {
		if strings.HasPrefix(l, "edit: ") {
			line = strings.TrimPrefix(l, "edit: ")
		}
	}
	var rev Revision
	require.NoError(t, json.Unmarshal([]byte(line), &rev), "the edit is sent as JSON so quotes survive")
	assert.Equal(t, `after "45" s.`, rev.Replace)
	assert.Contains(t, msgs[1].Content, "feedback: use 45")
}

func TestFixPrompts(t *testing.T) {
	shared, err := Embedded(Fix)
	require.NoError(t, err)
	assert.NotContains(t, shared, "Copyright")
	for _, want := range []string{"exact substring", "Never change what the specification requires", "\"summary\""} {
		assert.Contains(t, shared, want)
	}
	text, ok := FixInstruction("def-unreferenced")
	require.True(t, ok)
	assert.NotContains(t, text, "Copyright")
	assert.Contains(t, text, "Cite the definition")
	for _, bad := range []string{"", "nope", "../fix", "fix/../change", "def-unreferenced.md"} {
		_, ok := FixInstruction(bad)
		assert.False(t, ok, bad)
	}
	sys := FixSystem(shared, "def-unreferenced", text)
	assert.Contains(t, sys, "## How to fix `def-unreferenced`\n\nA definition that nothing else references")

	spec := smallSpec()
	c := specctx.Build(spec, specctx.Target{Files: []string{"net.spec.md"}}, specctx.Options{})
	msgs := FixMessages(sys, FixInput{Code: "def-unreferenced", Severity: "warning", File: "net.spec.md", Line: 5, Col: 3,
		Message: "definition $Net.Timeout is\nnot referenced", Text: "$Net.Timeout", Lines: "**$Net.Timeout (timeout).** A request times out after 30 s."}, c)
	require.Len(t, msgs, 2)
	assert.Equal(t, sys, msgs[0].Content)
	u := msgs[1].Content
	assert.Contains(t, u, "code: def-unreferenced\nseverity: warning\nfile: net.spec.md\nline: 5\ncolumn: 3\nmessage: definition $Net.Timeout is not referenced\n")
	assert.Contains(t, u, "<text>\n$Net.Timeout\n</text>")
	assert.Contains(t, u, "<lines>\n**$Net.Timeout (timeout).**")
	assert.Contains(t, u, `<file path="net.spec.md">`)
}

func TestCommitMessages(t *testing.T) {
	system, err := Embedded(Commit)
	require.NoError(t, err)
	assert.Contains(t, system, `{"subject": "spec: ...", "body":`)
	msgs := CommitMessages(system, CommitInput{
		Changes: []CommitChange{
			{What: "inv $Frame.Timing changed", Before: "above zero", After: "at most 5 s"},
			{What: "prose in demo.spec.md", Words: []string{"added clearly"}},
			{What: "prose in other.spec.md", Reformat: true},
		},
		Accepted: []string{"mention the bound"},
	})
	require.Len(t, msgs, 2)
	u := msgs[1].Content
	for _, want := range []string{"### inv $Frame.Timing changed", "<before>\nabove zero\n</before>", "<after>\nat most 5 s\n</after>",
		"- added clearly", "Whitespace and line wrapping only (reformat).", "## Suggested fixes the author applied\n\n- mention the bound"} {
		assert.Contains(t, u, want)
	}
	assert.NotContains(t, u, "finding", "only what the commit holds")
	assert.Contains(t, system, "Describe only what the commit holds")
	assert.Equal(t, "spec: bound fragments\n\n- why", CommitDraft{Subject: " spec:  bound fragments ", Body: "- why\n"}.Message())
	assert.Equal(t, "spec: x", CommitDraft{Subject: "spec: x"}.Message())
}

// TestCommitFalseRename: a draft may name only the renames the changes make.
// The first case is the draft spec ui showed for a changed $Book.Steps.
func TestCommitFalseRename(t *testing.T) {
	in := CommitInput{Renames: map[string]string{"$Time.Media": "$Time.Stamp"}}
	for _, c := range []struct {
		body, want string
	}{
		{"- `$Book.Steps` now states that checking a seat and booking it MUST be one step, and is renamed `$Book.Steps` to\n  `$Book.Steps`.", "$Book.Steps to $Book.Steps"},
		{"- `$Book.Steps` was renamed to `$Book.Flow`.", "$Book.Steps to $Book.Flow"},
		{"- rename $Time.Media -> $Time.Clock", "$Time.Media to $Time.Clock"},
		{"- `$Time.Media` is renamed to `$Time.Stamp`.", ""},
		{"- Renamed `$Time.Media` to `$Time.Stamp`.", ""},
		{"- `$Time.Stamp` (renamed from `$Time.Media`) now counts media time.", ""},
		{"- `$Book.Steps` now makes checking and booking one step.", ""},
	} {
		assert.Equal(t, c.want, in.FalseRename(CommitDraft{Subject: "spec: x", Body: c.body}), c.body)
	}
	assert.Equal(t, "$A to $A", CommitInput{}.FalseRename(CommitDraft{Subject: "spec: rename $A to $A"}))
}

// TestCommitFalseFix: a draft may claim, in "- Fix ..." bullets, no more
// fixes than the author applied. The first case is the draft a demo
// recording showed, with two fixes for findings left open.
func TestCommitFalseFix(t *testing.T) {
	for _, c := range []struct {
		applied []string
		body    string
		want    int
	}{
		{nil, "- `$Frame.Timing` now caps fragments at 5 s.\n- Fix the prose to mention the bound.\n- Fix the test list.", 2},
		{[]string{"mention the bound"}, "- Fix the prose to mention the bound.\n* fixed the test list", 2},
		{[]string{"mention the bound"}, "- `$Frame.Timing` now caps fragments at 5 s.\n- Fix the prose to mention the bound.", 0},
		{nil, "- `$Frame.Timing` now caps fragments at 5 s, a fixed bound.\n- Prefix the names.", 0},
		{nil, "", 0},
	} {
		in := CommitInput{Accepted: c.applied}
		assert.Equal(t, c.want, in.FalseFix(CommitDraft{Subject: "spec: x", Body: c.body}), c.body)
	}
}

// TestCommitMessagesNameTheOldName: a renamed anchor's heading says what it
// was renamed from, and the system prompt allows no other rename.
func TestCommitMessagesNameTheOldName(t *testing.T) {
	system, err := Embedded(Commit)
	require.NoError(t, err)
	assert.Contains(t, system, "renamed only when its heading says `renamed from $Old`")
	msgs := CommitMessages(system, CommitInput{Changes: []CommitChange{{What: "inv $Time.Stamp renamed from $Time.Media", Before: "a", After: "b"}}})
	assert.Contains(t, msgs[1].Content, "### inv $Time.Stamp renamed from $Time.Media")
}
