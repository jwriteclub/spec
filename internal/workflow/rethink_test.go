// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// cannedWorkflow is a workflow whose user config names a canned provider
// that serves replies (check purpose, in order).
func cannedWorkflow(t *testing.T, replies ...string) *Workflow {
	t.Helper()
	tmp := t.TempDir()
	var f infer.CannedFile
	for _, r := range replies {
		f.Check = append(f.Check, infer.CannedEntry{Reply: r})
	}
	b, err := toml.Marshal(f)
	require.NoError(t, err)
	path := filepath.Join(tmp, "replies.toml")
	require.NoError(t, os.WriteFile(path, b, 0o644))
	t.Cleanup(func() { infer.ForgetCanned(path) })
	env := map[string]string{"HOME": filepath.Join(tmp, "home"), "XDG_CONFIG_HOME": filepath.Join(tmp, "home", "config")}
	cfg := fmt.Sprintf("default = \"canned\"\n\n[providers.canned]\nkind = \"canned\"\npath = %q\nmodel = \"canned-1\"\ntriage = false\n", path)
	cfgPath := filepath.Join(env["XDG_CONFIG_HOME"], "spec", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o644))
	return &Workflow{Getenv: func(k string) string { return env[k] }}
}

// replyJSON encodes a model reply.
func replyJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// rethinkQuestion is the first check's question in the rethink tests.
const rethinkQuestion = "Should Two's 5 s answer time grow with the capacity of 6?"

// checkedForRethink edits $A.One (at most 4 becomes at most 6), checks it
// as spec ui does (keep, human) with the first reply, files the check,
// and returns the project, the workflow and the check.
func checkedForRethink(t *testing.T, replies ...string) (*project.Project, *Workflow, *proposal.Proposal) {
	t.Helper()
	proj := commitRepo(t)
	write(t, proj.Root, "c.spec.md", strings.Replace(commitSpec, "at most 4", "at most 6", 1))
	w := cannedWorkflow(t, replies...)
	res, e := w.Propose(context.Background(), proj, ProposeRequest{Names: []string{"A.One"}, Author: proposal.AuthorHuman, SourceKind: "human", Keep: true, Why: "more room"})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 1)
	p := res.Proposals[0]
	require.NoError(t, w.Store(proj.Root).Create(p))
	require.Equal(t, []string{rethinkQuestion}, p.Questions)
	return proj, w, p
}

func firstCheckReply(t *testing.T) string {
	return replyJSON(t, map[string]any{"summary": "the capacity grew", "findings": []any{}, "questions": []string{rethinkQuestion}})
}

// A rethink sends the answer, proposes the fixes it implies and a change
// to the author's own edit, which becomes a hunk on $A.One that the
// rethink record names; the earlier notes are kept on the new check.
func TestRethinkProposesFixesAndPrimary(t *testing.T) {
	second := replyJSON(t, map[string]any{
		"summary": "the author wants 5 items and a matching answer time",
		"primary": map[string]string{"find": "at most 6 items", "replace": "at most 5 items", "explanation": "the notes settle on 5"},
		"findings": []map[string]any{{
			"file": "c.spec.md", "anchor": "$B.Two", "issue": "missing_update", "severity": "inconsistent",
			"explanation": "Two's answer time follows the capacity", "find": "Two MUST answer within 5 s.", "replace": "Two MUST answer within 6 s.",
		}},
		"questions": []string{},
	})
	proj, w, old := checkedForRethink(t, firstCheckReply(t), second)
	np, e := w.Rethink(context.Background(), proj, RethinkRequest{
		Of: old, Note: proposal.RethinkNote{Question: rethinkQuestion, Text: " Make it 5 items, and yes, grow the answer time. "},
		Author: proposal.AuthorHuman,
	})
	require.Nil(t, e)
	require.NotNil(t, np.Rethink)
	assert.Equal(t, old.ID, np.Rethink.Of)
	assert.Equal(t, []proposal.RethinkNote{{Question: rethinkQuestion, Text: "Make it 5 items, and yes, grow the answer time."}}, np.Rethink.Notes)
	assert.Equal(t, "human", np.SourceKind)
	assert.True(t, np.Primary.InWorktree)
	assert.Contains(t, np.Primary.NewText, "at most 6")
	assert.Empty(t, np.Questions)
	require.Len(t, np.Hunks, 2)
	assert.Equal(t, "$B.Two", np.Hunks[0].Anchor)
	pr := np.Hunks[1]
	assert.Equal(t, pr.N, np.Rethink.PrimaryHunk)
	assert.Equal(t, "$A.One", pr.Anchor)
	assert.Equal(t, "at most 6 items", pr.Find)
	src := read(t, proj.Root, "c.spec.md")
	assert.Equal(t, "at most 6 items", src[pr.Range.Start:pr.Range.End])
	assert.Contains(t, np.CheckedTexts(), strings.Replace(np.Primary.NewText, "at most 6", "at most 5", 1))

	// The change to the author's edit lands: the check still stands for
	// its anchor (a fix accepted after it is not refused as stale).
	write(t, proj.Root, "c.spec.md", strings.Replace(src, "at most 6 items", "at most 5 items", 1))
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	assert.NoError(t, validateCheckPrimary(np, loaded.Spec))
	write(t, proj.Root, "c.spec.md", strings.Replace(src, "at most 6 items", "at most 7 items", 1))
	loaded, err = proj.Load(nil)
	require.NoError(t, err)
	assert.Error(t, validateCheckPrimary(np, loaded.Spec), "any other text of the anchor is still stale")
}

// A primary change that quotes text the anchor does not hold is shown as
// a note, never applied; a check of a reverted edit cannot be rethought.
func TestRethinkBadPrimaryIsANote(t *testing.T) {
	second := replyJSON(t, map[string]any{
		"summary":  "make it 5",
		"primary":  map[string]string{"find": "at most 9 items", "replace": "at most 5 items"},
		"findings": []any{}, "questions": []string{},
	})
	proj, w, old := checkedForRethink(t, firstCheckReply(t), second)
	np, e := w.Rethink(context.Background(), proj, RethinkRequest{Of: old, Note: proposal.RethinkNote{Question: rethinkQuestion, Text: "5"}})
	require.Nil(t, e)
	assert.Empty(t, np.Hunks)
	assert.Zero(t, np.Rethink.PrimaryHunk)
	require.Len(t, np.Notes, 1)
	assert.Contains(t, np.Notes[0].Reason, "not in $A.One")
	assert.Equal(t, "at most 5 items", np.Notes[0].Replace)

	write(t, proj.Root, "c.spec.md", commitSpec)
	_, e = w.Rethink(context.Background(), proj, RethinkRequest{Of: old, Note: proposal.RethinkNote{Text: "5"}})
	require.NotNil(t, e)
	assert.Equal(t, "not_found", e.Code)
	_, e = w.Rethink(context.Background(), proj, RethinkRequest{Of: old, Note: proposal.RethinkNote{Text: "  "}})
	require.NotNil(t, e)
	assert.Equal(t, "bad_change", e.Code)
}

// The rethink is told the first check's fixes with the author's decisions,
// its questions, and every note, earlier notes first.
func TestRethinkInputCarriesDecisionsAndNotes(t *testing.T) {
	of := &proposal.Proposal{
		ID: "c-1", Summary: "first",
		Hunks: []finding.Hunk{
			{N: 2, File: "c.spec.md", Anchor: "$B.Two", Issue: "missing_update", Severity: "inconsistent", Explanation: "follow", Find: "5 s", Replace: "6 s"},
			{N: 3, File: "c.spec.md", Issue: "missing_update", Severity: "cosmetic", Explanation: "prose", Find: "Prose", Replace: "Text"},
			{N: 4, File: "c.spec.md", Issue: "missing_update", Severity: "cosmetic", Explanation: "applied", Find: "between", Replace: "amid"},
		},
		Notes:     []finding.Note{{File: "c.spec.md", Issue: "ambiguity", Severity: "inconsistent", Explanation: "unclear"}},
		Questions: []string{"q one?", "q two?"},
		Decision:  &proposal.Decision{Applied: []int{4}},
		Rethink:   &proposal.Rethink{Of: "c-0", Notes: []proposal.RethinkNote{{Text: "earlier"}}},
	}
	notes := rethinkNotes(of, proposal.RethinkNote{Question: "q two?", Text: "answer"})
	in := rethinkInput(of, map[string]string{"h2": RethinkAccepted, "h3": RethinkDismissed, "q0": RethinkDismissed}, notes)
	require.Len(t, in.Items, 4)
	assert.Contains(t, in.Items[0].Decision, "accepted")
	assert.Equal(t, "skipped", in.Items[1].Decision)
	assert.Contains(t, in.Items[2].Decision, "accepted", "a hunk recorded as applied")
	assert.Equal(t, "undecided", in.Items[3].Decision)
	assert.Equal(t, []prompt.RethinkQuestion{{Text: "q one?", Decision: "dismissed by the author"}, {Text: "q two?", Decision: "answered in the author's notes below"}}, in.Questions)
	assert.Equal(t, []prompt.RethinkNote{{Text: "earlier"}, {Question: "q two?", Text: "answer"}}, in.Notes)

	c := &specctx.Context{}
	msgs := prompt.RethinkMessages("system", c, in)
	require.Len(t, msgs, 2)
	u := msgs[1].Content
	for _, want := range []string{"## The first review", "The author's decision: skipped", "<find>\n5 s\n</find>", "q two? (answered in the author's notes below)", "The review asked: q two?\nThe author answered:\n<notes>\nanswer\n</notes>", "The author wrote:\n<notes>\nearlier\n</notes>"} {
		assert.Contains(t, u, want)
	}
	sys, err := prompt.Embedded(prompt.Rethink)
	require.NoError(t, err)
	assert.Contains(t, sys, `"primary"`)
	assert.NotContains(t, sys, "Copyright")
}
