// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

const modelSpecText = "# Queue\n\n**$Queue.One (one turn).** There is exactly one active turn per session.\n"

const baseModel = "---- MODULE M ----\nEXTENDS Naturals\nVARIABLES x\n\nInit == x = 0\n\nNext == x' = x + 1\n\n" +
	"\\* Spec: $Queue.One \"exactly one active turn\"\nInv_One == x >= 0\n====\n"

// brokenChecker stands in for prudence: every line holding BROKEN is an error.
type brokenChecker struct{ calls atomic.Int32 }

func (c *brokenChecker) Check(_ context.Context, req formal.CheckRequest) (formal.Report, error) {
	c.calls.Add(1)
	rep := formal.Report{Checker: "fake", Version: "0.1", Level: "semantic"}
	off := 0
	for i, line := range strings.SplitAfter(string(req.Src), "\n") {
		if at := strings.Index(line, "BROKEN"); at >= 0 {
			rep.Diagnostics = append(rep.Diagnostics, formal.Diagnostic{
				Path: req.Path, Severity: formal.SeverityError, Message: "unknown operator " + strings.TrimSpace(line[at:]),
				Start: formal.Position{Line: i + 1, Column: at + 1, Offset: off + at},
			})
		}
		off += len(line)
	}
	return rep, nil
}

// modelRepo is a committed git repository with a spec and one formal model.
func modelRepo(t *testing.T) (*project.Project, *Workflow) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "Spec Test", "GIT_AUTHOR_EMAIL": "spec@example.com",
		"GIT_COMMITTER_NAME": "Spec Test", "GIT_COMMITTER_EMAIL": "spec@example.com",
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0",
	} {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "spec.toml", "formal = [\"tla/*.tla\"]\n")
	write(t, dir, "q.spec.md", modelSpecText)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tla"), 0o755))
	write(t, dir, "tla/M.tla", baseModel)
	write(t, dir, ".gitignore", ".spec/\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base")
	proj, err := project.Open(dir)
	require.NoError(t, err)
	return proj, &Workflow{}
}

func proposeModel(t *testing.T, w *Workflow, proj *project.Project, text string) (*proposal.Proposal, *Error) {
	t.Helper()
	return w.ProposeModel(context.Background(), proj, ModelRequest{Path: "tla/M.tla", Text: []byte(text), Why: "step by two", Author: "agent-7", SourceKind: "agent"})
}

func TestProposeModelFilesWithoutTouchingTheModel(t *testing.T) {
	proj, w := modelRepo(t)
	text := strings.Replace(baseModel, "x + 1", "x + 2", 1)
	text = strings.Replace(text, "====", "\\* Spec: $Queue.Nope\nInv_Two == TRUE\n====", 1)
	p, e := proposeModel(t, w, proj, text)
	require.Nil(t, e)
	assert.NotEmpty(t, p.ID)
	assert.Equal(t, baseModel, read(t, proj.Root, "tla/M.tla"), "the working tree is untouched")

	stored, err := w.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.KindModel, stored.Kind)
	assert.Nil(t, stored.Primary)
	assert.Equal(t, "tla/M.tla", stored.Model.File)
	assert.Equal(t, "M", stored.Model.Module)
	assert.Equal(t, []string{"Next", "Inv_Two"}, stored.Model.Decls)
	assert.Equal(t, proposal.ModelClean, stored.Model.Check.State, "prudence checks it")
	assert.Equal(t, "prudence", stored.Model.Check.Checker)
	assert.Equal(t, formal.LevelComplete, stored.Model.Check.Level, "prudence checks semantics and levels")
	assert.Equal(t, formal.CompletionComplete, stored.Model.Check.Completion)
	assert.True(t, stored.Model.Check.FullCheck())
	assert.Empty(t, stored.Model.Check.Diagnostics, "a clean model has no findings")
	assert.Empty(t, ModelErrorsWarning(stored))
	assert.Equal(t, proposal.FileHash([]byte(baseModel)), stored.Model.BaseHash)
	assert.Equal(t, "", stored.CheckState, "no required LLM check blocks it")
	require.Len(t, stored.Model.Links, 1, "the new dangling tag is linted")
	assert.Equal(t, "formal-ref-dangling", stored.Model.Links[0].Code)
	assert.Equal(t, 11, stored.Model.Links[0].Line)
	assert.Len(t, stored.Hunks, 2)
	assert.Equal(t, "spec: change model tla/M.tla (proposal "+p.ID+")", CommitSubject(stored))
}

func TestProposeModelIdenticalTextFilesNothing(t *testing.T) {
	proj, w := modelRepo(t)
	_, e := proposeModel(t, w, proj, baseModel)
	require.NotNil(t, e)
	assert.Equal(t, "usage", e.Code)
}

func TestModelTargetSaysWhichCommandFits(t *testing.T) {
	proj, w := modelRepo(t)
	_, e := w.ProposeModel(context.Background(), proj, ModelRequest{Path: "q.spec.md", Text: []byte("x"), Why: "w"})
	require.NotNil(t, e)
	assert.Equal(t, "wrong_command", e.Code)
	assert.Contains(t, e.Message, "spec change NAME --body-file F")

	_, e = w.ProposeModel(context.Background(), proj, ModelRequest{Path: "tla/New.tla", Text: []byte("x"), Why: "w"})
	require.NotNil(t, e)
	assert.Equal(t, "not_found", e.Code)
	assert.Contains(t, e.Message, "cannot create a model")

	write(t, proj.Root, "Other.tla", "---- MODULE Other ----\n====\n")
	e = ModelTarget(proj, "Other.tla")
	require.NotNil(t, e)
	assert.Contains(t, e.Message, "formal in spec.toml")

	for _, bad := range []string{"../Out.tla", "tla/../../Out.tla", "/etc/Out.tla"} {
		e = ModelTarget(proj, bad)
		require.NotNil(t, e, bad)
		assert.Equal(t, "not_found", e.Code)
		assert.Contains(t, e.Message, "not a project-relative path", bad)
	}

	e = ModelCommandHint("propose", "tla/M.tla")
	require.NotNil(t, e)
	assert.Equal(t, "wrong_command", e.Code)
	assert.Contains(t, e.Message, "spec change --tla tla/M.tla")
	assert.Contains(t, e.Message, "revert uncommitted edits")
	assert.Nil(t, ModelCommandHint("propose", "Queue.One"))
}

func TestOneModelProposalPendingPerModel(t *testing.T) {
	proj, w := modelRepo(t)
	first, e := proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + 2", 1))
	require.Nil(t, e)
	_, e = proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + 3", 1))
	require.NotNil(t, e)
	assert.Equal(t, "model_pending", e.Code)
	assert.Contains(t, e.Message, "spec reject "+first.ID+" --why \"withdrawn:")

	// Withdraw and resend.
	got, e := w.Reject(proj.Root, first.ID, "withdrawn: resending with +3", "agent-7")
	require.Nil(t, e)
	assert.Equal(t, proposal.StatusRejected, got.Status)
	second, e := proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + 3", 1))
	require.Nil(t, e)
	assert.NotEqual(t, first.ID, second.ID)
}

func TestModelProposalStaleOnlyWhenItsTextGoes(t *testing.T) {
	proj, w := modelRepo(t)
	p, e := proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + 2", 1))
	require.Nil(t, e)
	wait := func() *proposal.Proposal {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		got, err := w.Wait(ctx, proj.Root, []string{p.ID}, 50*time.Millisecond)
		if err != nil {
			require.ErrorIs(t, err, context.DeadlineExceeded)
		}
		return got[0]
	}

	// An edit elsewhere in the model leaves it pending and applicable.
	write(t, proj.Root, "tla/M.tla", strings.Replace(baseModel, "VARIABLES x", "VARIABLES x \\* counter", 1))
	got := wait()
	assert.Equal(t, proposal.StatusPending, got.Status)
	assert.Nil(t, got.StalePrimary)

	// Its own text changing stales it durably.
	write(t, proj.Root, "tla/M.tla", strings.Replace(baseModel, "x + 1", "x + 5", 1))
	got = wait()
	require.NotNil(t, got.StalePrimary)
	assert.Contains(t, got.StalePrimary.Reason, "tla/M.tla: hunk 1 no longer applies")
	assert.Contains(t, got.StalePrimary.Reason, "spec reject "+p.ID)

	_, e = w.Apply(proj.Root, p.ID, nil, "human", false)
	require.NotNil(t, e)
	assert.Equal(t, "stale", e.Code)
}

func TestApplyModelCommitsOnlyItsLines(t *testing.T) {
	proj, w := modelRepo(t)
	p, e := proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + 2", 1))
	require.Nil(t, e)
	// Chris's own uncommitted edit of the model, elsewhere.
	human := strings.Replace(baseModel, "VARIABLES x", "VARIABLES x \\* counter", 1)
	write(t, proj.Root, "tla/M.tla", human)

	res, e := w.Apply(proj.Root, p.ID, nil, "human", true)
	require.Nil(t, e)
	assert.Equal(t, proposal.StatusApplied, res.Proposal.Status)
	assert.NotEmpty(t, res.Commit)
	assert.Empty(t, res.Trailers, "a model edit carries no trailer")
	assert.Equal(t, strings.Replace(human, "x + 1", "x + 2", 1), read(t, proj.Root, "tla/M.tla"))

	committed := git(t, proj.Root, "show", "HEAD:tla/M.tla")
	assert.Contains(t, committed, "x + 2")
	assert.NotContains(t, committed, "counter", "the human edit stays uncommitted")
	assert.Contains(t, git(t, proj.Root, "diff", "--", "tla/M.tla"), "+VARIABLES x \\* counter")
	assert.Contains(t, git(t, proj.Root, "log", "-1", "--format=%B"), "spec: change model tla/M.tla")
}

func TestApplyModelLeavesAnAdjacentHumanEditUncommitted(t *testing.T) {
	for name, human := range map[string]string{
		"above": strings.Replace(baseModel, "\n\nNext ==", "\n\\* human note\nNext ==", 1),
		"below": strings.Replace(baseModel, "x + 1\n\n", "x + 1\n\\* human note\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			proj, w := modelRepo(t)
			p, e := proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + 2", 1))
			require.Nil(t, e)
			// Chris's own uncommitted edit, on the line next to the proposal's.
			write(t, proj.Root, "tla/M.tla", human)

			res, e := w.Apply(proj.Root, p.ID, nil, "human", true)
			require.Nil(t, e)
			assert.NotEmpty(t, res.Commit)
			assert.Equal(t, strings.Replace(human, "x + 1", "x + 2", 1), read(t, proj.Root, "tla/M.tla"))

			committed := git(t, proj.Root, "show", "HEAD:tla/M.tla")
			assert.Equal(t, strings.Replace(baseModel, "x + 1", "x + 2", 1), committed, "only the proposal's line is committed")
			diff := git(t, proj.Root, "diff", "--", "tla/M.tla")
			assert.Contains(t, diff, "+\\* human note", "the human edit stays uncommitted")
			assert.NotContains(t, diff, "-Next")
			assert.NotContains(t, diff, "+Next", "the proposal's line is committed")
		})
	}
}

func TestApplyModelRefusesToCommitAHumanEditOfItsLines(t *testing.T) {
	proj, w := modelRepo(t)
	// Chris's uncommitted edit of the very line the proposal then changes.
	human := strings.Replace(baseModel, "x + 1", "x + 1 \\* human", 1)
	write(t, proj.Root, "tla/M.tla", human)
	p, e := proposeModel(t, w, proj, strings.Replace(human, "x + 1", "x + 2", 1))
	require.Nil(t, e)
	head := git(t, proj.Root, "rev-parse", "HEAD")

	_, e = w.Apply(proj.Root, p.ID, nil, "human", true)
	require.NotNil(t, e)
	assert.Equal(t, "commit_failed", e.Code)
	assert.True(t, e.Written)
	assert.Contains(t, e.Message, "applied but not committed")
	assert.Contains(t, e.Message, "uncommitted edits")
	assert.Equal(t, head, git(t, proj.Root, "rev-parse", "HEAD"), "nothing is committed")
	assert.Equal(t, strings.Replace(human, "x + 1", "x + 2", 1), read(t, proj.Root, "tla/M.tla"), "the applied edit stays")
	stored, err := w.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusApplied, stored.Status)
}

func TestApplyModelRefusesPartialHunks(t *testing.T) {
	proj, w := modelRepo(t)
	text := strings.Replace(strings.Replace(baseModel, "x = 0", "x = 1", 1), "Inv_One == x >= 0", "Inv_One == x >= 1", 1)
	p, e := proposeModel(t, w, proj, text)
	require.Nil(t, e)
	require.Len(t, p.Hunks, 2)
	_, e = w.Apply(proj.Root, p.ID, []int{1}, "human", false)
	require.NotNil(t, e)
	assert.Equal(t, "usage", e.Code)
	assert.Contains(t, e.Message, "applies as a whole")
	assert.Equal(t, baseModel, read(t, proj.Root, "tla/M.tla"))
}

func TestModelCheckRefusesOnlyNewErrors(t *testing.T) {
	proj, w := modelRepo(t)
	ck := &brokenChecker{}
	w.ModelChecker = ck

	// A clean edit is filed clean.
	p, e := proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + 2", 1))
	require.Nil(t, e)
	assert.Equal(t, proposal.ModelClean, p.Model.Check.State)
	assert.Equal(t, "fake", p.Model.Check.Checker)
	_, e = w.Reject(proj.Root, p.ID, "withdrawn: next", "agent-7")
	require.Nil(t, e)

	// A new error refuses filing, with the diagnostics on the unfiled proposal.
	_, e = proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + BROKEN", 1))
	require.NotNil(t, e)
	assert.Equal(t, "check_failed", e.Code)
	assert.Contains(t, e.Message, "tla/M.tla:7:")
	require.NotNil(t, e.Proposal)
	assert.Empty(t, e.Proposal.ID, "nothing was filed")
	assert.Len(t, e.Proposal.Model.Check.Diagnostics, 1)
	ps, err := w.Store(proj.Root).List()
	require.NoError(t, err)
	assert.Nil(t, PendingModel(ps, "tla/M.tla"))

	// A model already broken can still be fixed, or edited elsewhere, by proposal.
	broken := strings.Replace(baseModel, "Init == x = 0", "Init == x = BROKEN", 1)
	write(t, proj.Root, "tla/M.tla", broken)
	git(t, proj.Root, "commit", "-qam", "broken")
	p, e = proposeModel(t, w, proj, strings.Replace(broken, "x + 1", "x + 2", 1))
	require.Nil(t, e, "pre-existing errors do not refuse")
	assert.Equal(t, proposal.ModelErrors, p.Model.Check.State)
	assert.Contains(t, ModelErrorsWarning(p), "1 check error(s)")
}

// The same rules with prudence itself, whose messages name the line: an
// error that an edit above it moves is still not new.
func TestPrudenceRefusesOnlyNewErrors(t *testing.T) {
	proj, w := modelRepo(t)

	_, e := proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + `", 1))
	require.NotNil(t, e)
	assert.Equal(t, "check_failed", e.Code)
	assert.Contains(t, e.Message, "prudence found 1 new error(s)")
	assert.Contains(t, e.Message, "tla/M.tla:7:")
	require.NotNil(t, e.Proposal)
	assert.Equal(t, formal.CompletionComplete, e.Proposal.Model.Check.Completion, "a syntax rejection is definitive")
	assert.Equal(t, "syntax", e.Proposal.Model.Check.Level)

	broken := strings.Replace(baseModel, "Init == x = 0", "Init == x = `", 1)
	write(t, proj.Root, "tla/M.tla", broken)
	git(t, proj.Root, "commit", "-qam", "broken")
	moved := strings.Replace(broken, "VARIABLES x\n", "VARIABLES x\n\\* the counter\n", 1)
	p, e := proposeModel(t, w, proj, moved)
	require.Nil(t, e, "the error the base has, a line lower, does not refuse")
	assert.Equal(t, proposal.ModelErrors, p.Model.Check.State)
	require.Len(t, p.Model.Check.Diagnostics, 1)
	assert.Equal(t, 6, p.Model.Check.Diagnostics[0].Start.Line)
}

func TestModelApprovalRechecksWhenTheModelMoved(t *testing.T) {
	proj, w := modelRepo(t)
	ck := &brokenChecker{}
	w.ModelChecker = ck
	p, e := proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + 2", 1))
	require.Nil(t, e)
	require.Equal(t, proposal.ModelClean, p.Model.Check.State)

	// Unmoved: approval does not check again.
	calls := ck.calls.Load()
	moved, e := w.RecheckModel(context.Background(), proj, p, nil)
	require.Nil(t, e)
	assert.False(t, moved)
	assert.Equal(t, calls, ck.calls.Load())

	// Chris breaks the model elsewhere meanwhile: approval checks again,
	// does not block, and reports the errors.
	write(t, proj.Root, "tla/M.tla", strings.Replace(baseModel, "Init == x = 0", "Init == x = BROKEN", 1))
	res, e := w.Apply(proj.Root, p.ID, nil, "human", false)
	require.Nil(t, e, "errors do not block approval")
	assert.Equal(t, proposal.StatusApplied, res.Proposal.Status)
	assert.True(t, res.Proposal.Model.Check.Rechecked)
	assert.Equal(t, proposal.ModelErrors, res.Proposal.Model.Check.State)
	assert.Contains(t, res.Warning, "check error")
	stored, err := w.Store(proj.Root).Load(p.ID)
	require.NoError(t, err)
	assert.True(t, stored.Model.Check.Rechecked, "the recheck is on the receipt")
}

func TestModelEditedHunkIsRecheckedAtApproval(t *testing.T) {
	proj, w := modelRepo(t)
	ck := &brokenChecker{}
	w.ModelChecker = ck
	p, e := proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + 2", 1))
	require.Nil(t, e)
	h := p.Hunks[0]
	h.Replace = strings.Replace(h.Replace, "x + 2", "x + BROKEN", 1)
	cur, e := w.SetHunk(proj.Root, p.ID, h)
	require.Nil(t, e)
	moved, e := w.RecheckModel(context.Background(), proj, cur, nil)
	require.Nil(t, e)
	assert.True(t, moved)
	assert.Equal(t, proposal.ModelErrors, cur.Model.Check.State)
}

// phaseChecker stands in for a prudence that returns partial semantic
// diagnostics: a line holding SEMANTIC is a semantic error, one holding
// SYNTAX a syntax error, and completion is what the test sets.
type phaseChecker struct{ completion formal.Completion }

func (c phaseChecker) Check(_ context.Context, req formal.CheckRequest) (formal.Report, error) {
	rep := formal.Report{Checker: "fake", Version: "0.1", Level: "semantic", Completion: c.completion}
	for i, line := range strings.Split(string(req.Src), "\n") {
		for _, k := range []struct{ word, phase, code string }{{"SEMANTIC", "semantic", "semantic/4004"}, {"SYNTAX", "syntax", "syntax/expected"}} {
			if strings.Contains(line, k.word) {
				rep.Diagnostics = append(rep.Diagnostics, formal.Diagnostic{
					Path: req.Path, Severity: formal.SeverityError, Phase: k.phase, Code: k.code,
					Message: "problem " + k.word, Start: formal.Position{Line: i + 1, Column: 1},
				})
			}
		}
	}
	return rep, nil
}

func TestIncompleteCheckBlocksOnlyOnSyntaxErrors(t *testing.T) {
	proj, w := modelRepo(t)
	w.ModelChecker = phaseChecker{completion: formal.CompletionIncomplete}

	p, e := proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + SEMANTIC", 1))
	require.Nil(t, e, "a semantic error from an incomplete check does not refuse")
	assert.Equal(t, proposal.ModelClean, p.Model.Check.State)
	assert.Zero(t, p.Model.Check.Errors())
	assert.Empty(t, ModelErrorsWarning(p))
	require.Len(t, p.Model.Check.Diagnostics, 1)
	assert.Equal(t, formal.SeverityHint, p.Model.Check.Diagnostics[0].Severity)
	assert.Contains(t, p.Model.Check.Diagnostics[0].Message, formal.IncompleteSemanticLabel)
	_, e = w.Reject(proj.Root, p.ID, "withdrawn: next", "agent-7")
	require.Nil(t, e)

	_, e = proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + SYNTAX", 1))
	require.NotNil(t, e, "a new syntax error refuses")
	assert.Equal(t, "check_failed", e.Code)
}

func TestCompleteCheckBlocksOnNewSemanticErrors(t *testing.T) {
	proj, w := modelRepo(t)
	w.ModelChecker = phaseChecker{completion: formal.CompletionComplete}

	_, e := proposeModel(t, w, proj, strings.Replace(baseModel, "x + 1", "x + SEMANTIC", 1))
	require.NotNil(t, e)
	assert.Equal(t, "check_failed", e.Code)
	require.NotNil(t, e.Proposal)
	assert.Equal(t, formal.SeverityError, e.Proposal.Model.Check.Diagnostics[0].Severity)
}
