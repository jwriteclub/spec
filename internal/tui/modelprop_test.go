// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

const uiModel = "---- MODULE M ----\nEXTENDS Naturals\nVARIABLES x\n\nInit == x = 0\n\nNext == x' = x + 1\n\n" +
	"\\* Spec: $Queue.One \"exactly one active turn\"\nInv_One == x >= 0\n====\n"

// uiBroken stands in for prudence: a line holding BROKEN is an error.
type uiBroken struct{}

func (uiBroken) Check(_ context.Context, req formal.CheckRequest) (formal.Report, error) {
	rep := formal.Report{Checker: "fake", Level: "semantic", Completion: formal.CompletionComplete}
	for i, line := range strings.Split(string(req.Src), "\n") {
		if strings.Contains(line, "BROKEN") {
			rep.Diagnostics = append(rep.Diagnostics, formal.Diagnostic{Path: req.Path, Severity: formal.SeverityError, Message: "unknown operator BROKEN", Start: formal.Position{Line: i + 1, Column: 1}})
		}
	}
	return rep, nil
}

// modelUIRepo is a committed repository with a spec and the model text.
func modelUIRepo(t *testing.T, model string) string {
	t.Helper()
	gitEnv(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	writeFile(t, dir, "spec.toml", "name = \"m\"\nformal = [\"tla/*.tla\"]\n")
	writeFile(t, dir, "q.spec.md", "# Queue\n\n**$Queue.One (one turn).** There is exactly one active turn per session.\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tla"), 0o755))
	writeFile(t, dir, "tla/M.tla", model)
	writeFile(t, dir, ".gitignore", ".spec/\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	return dir
}

// fileModel files agent-7's model proposal of text, as `spec change --tla` does.
func fileModel(t *testing.T, dir string, wf *workflow.Workflow, text string) *proposal.Proposal {
	t.Helper()
	proj, err := project.Open(dir)
	require.NoError(t, err)
	p, e := wf.ProposeModel(context.Background(), proj, workflow.ModelRequest{Path: "tla/M.tla", Text: []byte(text), Why: "step by two", Author: "agent-7", SourceKind: "agent"})
	require.Nil(t, e)
	return p
}

func modelBuffer(a *App) string {
	if d, ok := a.ws.Docs()["tla/M.tla"]; ok {
		return string(d.Ed.Buffer().Bytes())
	}
	return ""
}

// Filed while the UI was closed, a model proposal shows as one card when it
// opens; y applies it into the model's buffer and the save records it.
func TestModelProposalCardAppliesAtSave(t *testing.T) {
	dir := modelUIRepo(t, uiModel)
	wf := testWorkflow(t, nil)
	newText := strings.Replace(uiModel, "x + 1", "x + 2", 1)
	p := fileModel(t, dir, wf, newText)

	opts := uiOptions(dir, wf)
	opts.WatchProposals = true
	f := startFlow(t, opts)
	f.until("the card", func(a *App) bool { return len(a.side.cards) == 1 })
	f.do(func(a *App) {
		c := a.side.cards[0]
		assert.Equal(t, cardProposal, c.kind, "a model edit is one card, decided as a whole")
		assert.Equal(t, "Agent proposal · agent-7 · M.tla", groupOriginTitle(c.p))
		loc, ok := a.cardLoc(c)
		assert.True(t, ok)
		assert.Equal(t, "tla/M.tla", loc.Path)
	})
	f.send(key("ctrl+r"))
	frame := f.frame()
	assert.Contains(t, frame, "Agent model proposal")
	assert.Contains(t, frame, "tla/M.tla")
	assert.Contains(t, frame, "Touches Next")
	assert.Contains(t, frame, "Check clean · prudence", "prudence checks semantics and levels")
	assert.Contains(t, frame, "x + 2", "the selected card shows the diff")

	// Enter reviews it: hunks cannot be skipped one by one.
	f.send(key("enter"))
	f.do(func(a *App) { require.NotNil(t, reviewOf(a)) })
	assert.Contains(t, f.frame(), "Model edit · 1 hunk, applied as a whole")
	f.send(key("n"))
	f.do(func(a *App) { assert.Contains(t, a.flash, "applied as a whole") })
	f.send(key("esc"))

	f.send(key("y"))
	f.until("the apply", func(a *App) bool { return strings.Contains(modelBuffer(a), "x + 2") })
	assert.Equal(t, uiModel, readFile(t, dir, "tla/M.tla"), "nothing is written before the save")
	rec, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, rec.Status)
	f.do(func(a *App) {
		assert.Empty(t, a.side.cards)
		saveBuffers(t, a)
	})
	assert.Equal(t, newText, readFile(t, dir, "tla/M.tla"))
	rec, err = proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, rec.Status)
	assert.Equal(t, []int{1}, rec.Decision.Applied)
	f.finish()
}

// A model proposal that arrives while the UI runs can be rejected with a
// reason from its card.
func TestModelProposalArrivesAndIsRejected(t *testing.T) {
	dir := modelUIRepo(t, uiModel)
	wf := testWorkflow(t, nil)
	opts := uiOptions(dir, wf)
	opts.WatchProposals = true
	f := startFlow(t, opts)
	p := fileModel(t, dir, wf, strings.Replace(uiModel, "x + 1", "x + 2", 1))
	f.until("the card", func(a *App) bool { return len(a.side.cards) == 1 })
	f.send(key("ctrl+r"), key("n"))
	f.do(func(a *App) {
		require.NotNil(t, a.prompt)
		assert.Equal(t, promptReject, a.prompt.kind)
	})
	f.typeIn("keep the step at one")
	f.send(key("enter"))
	f.until("the rejection", func(a *App) bool { return len(a.side.cards) == 0 })
	rec, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusRejected, rec.Status)
	assert.Equal(t, "keep the step at one", rec.Decision.Reason)
	assert.Equal(t, uiModel, readFile(t, dir, "tla/M.tla"))
	f.finish()
}

// A model proposal that arrives while its model is the current document: ^R
// reaches the card without starting a check of the changed spec anchor, and y
// applies the proposal instead of typing into the model.
func TestModelProposalReachedFromTheModel(t *testing.T) {
	dir := modelUIRepo(t, uiModel)
	// An uncommitted spec edit, which ^R from a spec file would check.
	writeFile(t, dir, "q.spec.md", "# Queue\n\n**$Queue.One (one turn).** There is exactly one active turn per queue.\n")
	wf := testWorkflow(t, nil)
	opts := uiOptions(dir, wf)
	opts.WatchProposals = true
	opts.Target = Target{File: "tla/M.tla"}
	f := startFlow(t, opts)
	f.do(func(a *App) { require.True(t, a.inModel()) })
	f.until("the baseline", func(a *App) bool { return a.uncommitted() })
	newText := strings.Replace(uiModel, "x + 1", "x + 2", 1)
	p := fileModel(t, dir, wf, newText)
	f.until("the card", func(a *App) bool { return len(a.side.cards) == 1 })
	f.do(func(a *App) {
		assert.Contains(t, a.flash, "Agent proposal from agent-7")
		assert.True(t, barEnabled(a, CtxEditor, ActReview), "^R is open on a model")
	})

	f.send(key("ctrl+r"))
	f.do(func(a *App) {
		require.Equal(t, CtxChecks, a.context())
		require.Equal(t, cardProposal, a.side.selected().kind)
		assert.Equal(t, p.ID, a.side.selected().p.ID)
		assert.Nil(t, a.job, "no anchor check starts from a model")
		assert.NotContains(t, a.flash, "check")
		assert.True(t, a.inModel())
	})

	f.send(key("y"))
	f.until("the apply", func(a *App) bool { return strings.Contains(modelBuffer(a), "x + 2") })
	f.do(func(a *App) {
		assert.Equal(t, newText, modelBuffer(a), "y applied the proposal and typed nothing")
		assert.Empty(t, a.side.cards)
		saveBuffers(t, a)
	})
	assert.Equal(t, newText, readFile(t, dir, "tla/M.tla"))
	rec, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, rec.Status)
	f.finish()
}

// With a checker, a model left with errors needs a second y; the model moved
// since filing, so y checks it again first.
func TestModelProposalErrorsAskTwice(t *testing.T) {
	dir := modelUIRepo(t, uiModel)
	wf := testWorkflow(t, nil)
	wf.ModelChecker = uiBroken{}
	p := fileModel(t, dir, wf, strings.Replace(uiModel, "x + 1", "x + 2", 1))
	require.Equal(t, proposal.ModelClean, p.Model.Check.State)
	// Chris breaks the model elsewhere after filing.
	writeFile(t, dir, "tla/M.tla", strings.Replace(uiModel, "Init == x = 0", "Init == x = BROKEN", 1))

	f := startFlow(t, uiOptions(dir, wf))
	f.until("the card", func(a *App) bool { return len(a.side.cards) == 1 })
	f.send(key("ctrl+r"), key("y"))
	f.do(func(a *App) {
		assert.Contains(t, a.flash, "1 check error(s) with this change")
		assert.Contains(t, a.flash, "y again applies it anyway")
		assert.NotContains(t, modelBuffer(a), "x + 2", "the first y applies nothing")
	})
	assert.Contains(t, f.frame(), "1 check error · fake, rechecked")
	f.send(key("y"))
	f.until("the apply", func(a *App) bool { return strings.Contains(modelBuffer(a), "x + 2") })
	f.do(func(a *App) { saveBuffers(t, a) })
	rec, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, rec.Status)
	assert.True(t, rec.Model.Check.Rechecked, "the save's receipt keeps the recheck")
	assert.Equal(t, proposal.ModelErrors, rec.Model.Check.State)
	f.finish()
}

func TestModelCheckBadgeSaysSyntaxChecked(t *testing.T) {
	partial := proposal.ModelCheck{State: proposal.ModelClean, Checker: "prudence", Level: "syntax", Completion: formal.CompletionIncomplete,
		Diagnostics: []formal.Diagnostic{{Severity: formal.SeverityHint, Code: "analysis/incomplete"}}}
	assert.Equal(t, "Syntax checked, clean · prudence", modelCheckBadge(partial))
	partial.Completion = formal.CompletionComplete
	assert.Equal(t, "Syntax checked, clean · prudence", modelCheckBadge(partial), "complete at syntax is no semantic check")
	full := proposal.ModelCheck{State: proposal.ModelClean, Checker: "prudence", Level: "semantic", Completion: formal.CompletionComplete}
	assert.Equal(t, "Check clean · prudence", modelCheckBadge(full))
	full.State, full.Diagnostics = proposal.ModelErrors, []formal.Diagnostic{{Severity: formal.SeverityError}}
	assert.Equal(t, "1 check error · prudence", modelCheckBadge(full))
	partial.State, partial.Diagnostics = proposal.ModelErrors, []formal.Diagnostic{{Severity: formal.SeverityError}, {Severity: formal.SeverityHint}}
	assert.Equal(t, "Syntax checked, 1 error · prudence", modelCheckBadge(partial))
}
