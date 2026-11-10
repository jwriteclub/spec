// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/proposal"
)

const cliModel = "---- MODULE M ----\nEXTENDS Naturals\nVARIABLES x\n\nInit == x = 0\n\nNext == x' = x + 1\n\n" +
	"\\* Spec: $Queue.One \"exactly one active turn\"\nInv_One == x >= 0\n====\n"

// modelCLI runs spec on a committed project with one formal model.
type modelCLI struct {
	dir     string
	env     testEnv
	checker formal.Checker
}

func newModelCLI(t *testing.T) *modelCLI {
	t.Helper()
	dir := repoWith(t, map[string]string{
		"spec.toml": "formal = [\"tla/*.tla\"]\n", "q.spec.md": formalSpecText, "tla/M.tla": cliModel, ".gitignore": ".spec/\n",
	})
	return &modelCLI{dir: dir, env: isolatedEnv(t)}
}

func (m *modelCLI) envFor(out, errb *bytes.Buffer, stdin string) Env {
	return Env{Stdout: out, Stderr: errb, Stdin: strings.NewReader(stdin), Cwd: m.dir, Getenv: m.env.get, waitPoll: 20 * time.Millisecond, modelChecker: m.checker}
}

func (m *modelCLI) run(t *testing.T, stdin string, args ...string) jsonResult {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(append(append([]string{}, args...), "--json"), m.envFor(&out, &errb, stdin))
	return decodeEnvelope(t, result{code: code, stdout: out.String(), stderr: errb.String()}, args)
}

func (m *modelCLI) runHuman(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, m.envFor(&out, &errb, stdin))
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func (m *modelCLI) runAsync(args ...string) <-chan result {
	ch := make(chan result, 1)
	go func() {
		var out, errb bytes.Buffer
		code := Run(append(append([]string{}, args...), "--json"), m.envFor(&out, &errb, ""))
		ch <- result{code: code, stdout: out.String(), stderr: errb.String()}
	}()
	return ch
}

// cliBroken stands in for prudence: a line holding BROKEN is an error.
type cliBroken struct{}

func (cliBroken) Check(_ context.Context, req formal.CheckRequest) (formal.Report, error) {
	rep := formal.Report{Checker: "fake", Version: "0.1"}
	for i, line := range strings.Split(string(req.Src), "\n") {
		if strings.Contains(line, "BROKEN") {
			rep.Diagnostics = append(rep.Diagnostics, formal.Diagnostic{Path: req.Path, Severity: formal.SeverityError, Message: "unknown operator BROKEN", Start: formal.Position{Line: i + 1, Column: 1}})
		}
	}
	return rep, nil
}

func TestChangeModelFilesAndWaits(t *testing.T) {
	m := newModelCLI(t)
	newText := strings.Replace(cliModel, "x + 1", "x + 2", 1)
	write(t, m.dir, "new.tla", newText)

	// Filed for review; the model is untouched; exit 2.
	jr := m.run(t, "", "change", "--tla", "tla/M.tla", "--body-file", "new.tla", "--why", "step by two", "--by", "agent-7")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	d := filedOf(t, jr)
	require.Len(t, d.Proposals, 1)
	p := d.Proposals[0]
	assert.Equal(t, proposal.KindModel, p.Kind)
	assert.Equal(t, "tla/M.tla", p.Model.File)
	assert.Equal(t, proposal.ModelClean, p.Model.Check.State, "prudence checks it")
	assert.Equal(t, []string{"Next"}, p.Model.Decls)
	assert.Equal(t, []string{"spec", "proposal", p.ID, "--wait", "30m"}, d.Next.Argv)
	assert.Equal(t, cliModel, readFile(t, m.dir, "tla/M.tla"))

	// One pending proposal per model: withdraw and resend.
	jr = m.run(t, newText, "change", "--tla", "tla/M.tla", "--body-file", "-", "--why", "again", "--by", "agent-7")
	assert.Equal(t, exitFail, jr.code)
	assert.Equal(t, "model_pending", jr.errCode)
	assert.Contains(t, jr.errMessage, "spec reject "+p.ID)
	jr = m.run(t, "", "reject", p.ID, "--why", "withdrawn: resending", "--by", "agent-7")
	require.Equal(t, 0, jr.code, jr.stdout)

	// Resent from stdin and waited on; Chris approves with a commit.
	done := m.runAsync("change", "--tla", "tla/M.tla", "--body-file", "new.tla", "--why", "step by two", "--by", "agent-7", "--wait", "20s")
	var id string
	deadline := time.Now().Add(10 * time.Second)
	for id == "" && time.Now().Before(deadline) {
		for _, n := range proposalFiles(t, m.dir) {
			if strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, ".tmp") && strings.TrimSuffix(n, ".json") != p.ID {
				id = strings.TrimSuffix(n, ".json")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotEmpty(t, id, "the resent proposal was filed")
	// The filer can still hold .spec/lock for a moment after the proposal file
	// appears; exit 5 means wait and retry, as a person would.
	h := m.runHuman(t, "", "approve", id, "--commit")
	for retry := time.Now().Add(10 * time.Second); h.code == exitLock && time.Now().Before(retry); {
		time.Sleep(50 * time.Millisecond)
		h = m.runHuman(t, "", "approve", id, "--commit")
	}
	require.Equal(t, 0, h.code, h.stdout+h.stderr)
	assert.Contains(t, h.stdout, "model: tla/M.tla (module M)")
	assert.Contains(t, h.stdout, "model check: clean · prudence")
	assert.Contains(t, h.stdout, "(complete, complete)")
	assert.NotContains(t, h.stdout, ": error: ")
	var r result
	select {
	case r = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the waiting change did not return")
	}
	// The waiter may poll while the approval holds the lock; it still
	// delivers the decision, never exit 5.
	wjr := decodeEnvelope(t, r, []string{"change", "--wait"})
	assert.Equal(t, 0, wjr.code, wjr.stdout)
	assert.Equal(t, newText, readFile(t, m.dir, "tla/M.tla"))
	assert.Contains(t, git(t, m.dir, "log", "-1", "--format=%s"), "spec: change model tla/M.tla (proposal "+id+")")
	assert.Empty(t, git(t, m.dir, "status", "--porcelain", "--", "tla"))
}

func TestWrongCommandForModelsAndSpecs(t *testing.T) {
	m := newModelCLI(t)
	write(t, m.dir, "new.tla", cliModel+"\n")
	write(t, m.dir, "body.md", "x")

	jr := m.run(t, "", "change", "--tla", "q.spec.md", "--body-file", "body.md", "--why", "w")
	assert.Equal(t, exitFail, jr.code)
	assert.Equal(t, "wrong_command", jr.errCode)
	assert.Contains(t, jr.errMessage, "q.spec.md is a spec file, not a TLA+ model")

	jr = m.run(t, "", "propose", "tla/M.tla", "--why", "w")
	assert.Equal(t, "wrong_command", jr.errCode)
	assert.Contains(t, jr.errMessage, "spec change --tla tla/M.tla")

	jr = m.run(t, "", "change", "tla/M.tla", "--body-file", "new.tla", "--why", "w")
	assert.Equal(t, "wrong_command", jr.errCode)
	assert.Contains(t, jr.errMessage, "spec change --tla tla/M.tla")

	jr = m.run(t, "", "change", "--tla", "tla/M.tla", "--remove", "--body-file", "new.tla", "--why", "w")
	assert.Equal(t, "usage", jr.errCode)

	assert.Empty(t, proposalFiles(t, m.dir), "nothing was filed by a wrong command")

	// The LLM model id is --engine; --model is no flag at all.
	jr = m.run(t, "", "change", "Queue.One", "--engine", "gpt-x", "--remove", "--why", "w")
	assert.NotEqual(t, "wrong_command", jr.errCode)
	assert.NotEqual(t, "usage", jr.errCode)
	jr = m.run(t, "", "change", "--model", "tla/M.tla", "--body-file", "new.tla", "--why", "w")
	assert.Equal(t, "usage", jr.errCode)
	assert.Contains(t, jr.errMessage, "unknown flag: --model")
}

func TestChangeModelCheckFailedFilesNothing(t *testing.T) {
	m := newModelCLI(t)
	m.checker = cliBroken{}
	write(t, m.dir, "new.tla", strings.Replace(cliModel, "x + 1", "x + BROKEN", 1))
	jr := m.run(t, "", "change", "--tla", "tla/M.tla", "--body-file", "new.tla", "--why", "w", "--by", "agent-7")
	assert.Equal(t, exitFail, jr.code)
	assert.Equal(t, "check_failed", jr.errCode)
	assert.Contains(t, jr.errMessage, "tla/M.tla:7:1: error: unknown operator BROKEN")
	var d proposalData
	jr.decode(t, &d)
	require.NotNil(t, d.Proposal)
	assert.Empty(t, d.Proposal.ID)
	assert.Len(t, d.Proposal.Model.Check.Diagnostics, 1)
	assert.Empty(t, proposalFiles(t, m.dir))
}

func TestReviewModelAsksTwiceWithErrors(t *testing.T) {
	m := newModelCLI(t)
	m.checker = cliBroken{}
	broken := strings.Replace(cliModel, "Init == x = 0", "Init == x = BROKEN", 1)
	write(t, m.dir, "tla/M.tla", broken)
	commitAll(t, m.dir, "broken")
	write(t, m.dir, "new.tla", strings.Replace(broken, "x + 1", "x + 2", 1))
	jr := m.run(t, "", "change", "--tla", "tla/M.tla", "--body-file", "new.tla", "--why", "w", "--by", "agent-7")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	id := filedOf(t, jr).Proposals[0].ID

	// y then n: the second question keeps it pending.
	h := m.runHuman(t, "y\nn\n", "review", id)
	require.Equal(t, 0, h.code, h.stdout+h.stderr)
	assert.Contains(t, h.stdout, "apply the model edit (1 hunk, as a whole)?")
	assert.Contains(t, h.stdout, "1 check error(s) with this change")
	assert.Contains(t, h.stdout, "still pending")
	assert.Equal(t, broken, readFile(t, m.dir, "tla/M.tla"))

	// y y applies it.
	h = m.runHuman(t, "y\ny\n", "review", id)
	require.Equal(t, 0, h.code, h.stdout+h.stderr)
	assert.Contains(t, readFile(t, m.dir, "tla/M.tla"), "x + 2")
}

func TestModelCheckTextWording(t *testing.T) {
	assert.Equal(t, "syntax checked, clean", ModelCheckText(proposal.ModelCheck{State: proposal.ModelClean}, true),
		"a checker that does not say it finished is not credited with semantics")
	assert.Equal(t, "syntax checked, clean · prudence 1 (syntax, incomplete)", ModelCheckText(proposal.ModelCheck{
		State: proposal.ModelClean, Checker: "prudence", Version: "1", Level: "syntax", Completion: formal.CompletionIncomplete}, true))
	assert.Equal(t, "syntax checked, clean · prudence 1 (semantic, incomplete)", ModelCheckText(proposal.ModelCheck{
		State: proposal.ModelClean, Checker: "prudence", Version: "1", Level: "semantic", Completion: formal.CompletionIncomplete}, true))
	assert.Equal(t, "clean · prudence 1 (semantic, complete)", ModelCheckText(proposal.ModelCheck{
		State: proposal.ModelClean, Checker: "prudence", Version: "1", Level: "semantic", Completion: formal.CompletionComplete}, true))
	syntaxErr := proposal.ModelCheck{State: proposal.ModelErrors, Checker: "prudence", Level: "syntax", Completion: formal.CompletionComplete,
		Diagnostics: []formal.Diagnostic{{Severity: "error"}}}
	assert.Equal(t, "syntax checked, 1 error · prudence (syntax, complete) (errors the model already had; none is new)", ModelCheckText(syntaxErr, true))
	errs := proposal.ModelCheck{State: proposal.ModelErrors, Checker: "prudence", Diagnostics: []formal.Diagnostic{{Severity: "error"}}}
	assert.Contains(t, ModelCheckText(errs, true), "errors the model already had")
	assert.Contains(t, ModelCheckText(errs, false), "includes errors this proposal adds")
	assert.NotContains(t, ModelCheckText(errs, false), "none is new")
	errs.Rechecked = true
	assert.NotContains(t, ModelCheckText(errs, true), "already had")
}
