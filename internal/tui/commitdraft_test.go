// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/workflow"
)

// draftInputOf is the commit draft's input for every change from head to cur.
func draftInputOf(t *testing.T, head, cur string) (*App, *commitScreen) {
	t.Helper()
	a := newTestApp(t, testOptions(demoProject(t)))
	h := mdspec.Build([]*mdspec.File{mdspec.ParseFile("demo.spec.md", []byte(head))})
	c := mdspec.Build([]*mdspec.File{mdspec.ParseFile("demo.spec.md", []byte(cur))})
	cs := &commitScreen{head: h, cur: c, changes: workflow.Changes(h, c), ticked: map[string]bool{}, state: map[string]string{}}
	for _, ch := range cs.changes {
		cs.ticked[ch.ID] = true
	}
	return a, cs
}

// TestDraftInputRenames: the model drafting the commit message learns which
// anchors were renamed, and from what; a changed anchor is no rename. The
// demo's draft called a changed $Book.Steps "renamed $Book.Steps to
// $Book.Steps" from an input that named no rename at all.
func TestDraftInputRenames(t *testing.T) {
	a, cs := draftInputOf(t, demoSpec, strings.ReplaceAll(demoSpec, "$Time.Media", "$Time.Stamp"))
	in := cs.draftInput(a)
	require.Len(t, in.Changes, 3, "the rename, and the two anchors whose references follow it")
	assert.Equal(t, "def $Time.Stamp renamed from $Time.Media", in.Changes[0].What)
	assert.Equal(t, "inv $Frame.Timing edited", in.Changes[1].What)
	assert.Contains(t, in.Changes[0].Before, "**Def $Time.Media (media time).**")
	assert.Contains(t, in.Changes[0].After, "**Def $Time.Stamp (media time).**")
	assert.Equal(t, map[string]string{"$Time.Media": "$Time.Stamp"}, in.Renames)

	a, cs = draftInputOf(t, demoSpec, strings.Replace(demoSpec, "above zero", "of at most 5 s", 1))
	in = cs.draftInput(a)
	require.Len(t, in.Changes, 1)
	assert.Equal(t, "inv $Frame.Timing changed", in.Changes[0].What)
	assert.Empty(t, in.Renames)
}

// TestDraftOnlyAppliedFixes: a check of $Frame.Timing files two fixes on
// it; y applies one and the other stays open. The draft is asked for the
// applied fix only, and a draft that claims both is refused for the
// template. A demo recording's draft listed "Fix ..." bullets for check
// findings that were never applied: the open cards went into its input.
func TestDraftOnlyAppliedFixes(t *testing.T) {
	dir := demoRepo(t)
	check := modelJSON(t, map[string]any{
		"summary": "the prose and the test list should follow the bound",
		"findings": []map[string]any{
			{"file": "demo.spec.md", "issue": "missing_update", "severity": "inconsistent", "explanation": "mention the bound", "find": proseFind, "replace": proseReplace},
			{"file": "demo.spec.md", "anchor": "$Frame.Timing", "issue": "missing_update", "severity": "cosmetic", "explanation": "name a test of the bound", "find": "Checked by: TestFragment.", "replace": "Checked by: TestFragment, TestBound."},
		},
		"questions": []string{},
	})
	draft := modelJSON(t, map[string]string{"subject": "spec: bound $Frame.Timing to 5 s", "body": "- Fix the prose to mention the bound.\n- Fix the test list to name a test of the bound."})
	m := newFakeModel(t, check, draft)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })

	f.findIn("above zero")
	for range len("above zero") {
		f.send(key("shift+right"))
	}
	f.typeIn("of at most 5 s")
	f.send(key("ctrl+p"))
	f.send(key("y"))
	f.until("the cards", func(a *App) bool { return a.job == nil && itemCards(a) == 2 })
	f.send(key("ctrl+r"))
	f.send(key("y"))
	f.do(func(a *App) {
		assert.Contains(t, string(a.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()), proseReplace, "the prose fix applied")
		assert.Equal(t, 1, itemCards(a), "the test-list fix stays open")
		saveBuffers(t, a)
	})

	f.send(key("A"))
	var cs *commitScreen
	f.do(func(a *App) {
		var ok bool
		cs, ok = a.top().(*commitScreen)
		require.True(t, ok, "the commit view")
		in := cs.draftInput(a)
		assert.Equal(t, []string{"mention the bound"}, in.Accepted, "the applied fix only")
	})
	f.until("the draft", func(a *App) bool { return !cs.drafting })
	reqs := m.requests()
	require.Len(t, reqs, 2)
	assert.Contains(t, reqs[1], "mention the bound", "the applied fix")
	assert.NotContains(t, reqs[1], "name a test of the bound", "the open finding")
	assert.NotContains(t, reqs[1], "Open findings")
	f.do(func(a *App) {
		assert.Equal(t, "a template: the model did not draft one", cs.draftNote, "a draft claiming two fixes for one applied")
		msg := cs.message(a)
		assert.Contains(t, msg, "- mention the bound")
		assert.NotContains(t, msg, "test of the bound")
	})
	f.finish()
}
