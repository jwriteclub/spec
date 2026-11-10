// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

// Step 4f (notes/plan-step4f-cascade.md): ^P runs the gate, triage and
// check cascade in the background; the anchor's card in the sidebar and
// the status line show each stage as it finishes.

// fakeGate is a Jev Decisions API answering class with the class
// questions and 0.95 for material_change.
func fakeGate(t *testing.T, class *atomic.Value) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		v := class.Load().(float64)
		answers := map[string]any{}
		for q, s := range map[string]float64{"material_change": 0.95, "numeric_mismatch": 0.1, "contradiction": v, "stale_reference": 0.05} {
			answers[q] = map[string]any{"type": "noul", "noul": s}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "typesafe/jev-1.14", "answers": answers,
			"usage": map[string]any{"input_tokens": 900, "output_tokens": 70, "cost": 0.00007}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// cascadeWorkflow is testWorkflow with the fake gate in front of m; extra
// is appended to the config.
func cascadeWorkflow(t *testing.T, m *fakeModel, gate *httptest.Server, extra ...string) *workflow.Workflow {
	t.Helper()
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config")}
	cfg := filepath.Join(env["XDG_CONFIG_HOME"], "spec", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfg), 0o755))
	require.NoError(t, os.WriteFile(cfg, []byte("default = \"fake\"\n[providers.fake]\nbase_url = \""+m.URL+"\"\nmodel = \"check-model\"\nstream = false\n"+
		"[providers.jev]\nkind = \"decisions\"\nbase_url = \""+gate.URL+"\"\nmodel = \"typesafe/jev-1.14\"\n[gate]\nprovider = \"jev\"\n"+strings.Join(extra, "")), 0o644))
	return &workflow.Workflow{
		Getenv:     func(k string) string { return env[k] },
		TuneClient: func(c *infer.Client) { c.BaseDelay = time.Millisecond; c.MaxDelay = time.Millisecond },
	}
}

// sidebarText renders the sidebar 70 cells wide, plain.
func sidebarText(a *App) string {
	return ansi.Strip(strings.Join(a.sideView(70, 60), "\n"))
}

// ^P on an edit the gate escalates: the editor keeps the keyboard; the
// anchor's card shows the gate's line, then the running check; the status
// line shows the chain; the check's findings land with the stage lines
// under their group.
func TestCascadeShowsStagesLive(t *testing.T) {
	dir := demoRepo(t)
	var class atomic.Value
	class.Store(0.62)
	gate := fakeGate(t, &class)
	m := newFakeModel(t, checkReply(t))
	m.gate = make(chan struct{})
	f := startFlow(t, uiOptions(dir, cascadeWorkflow(t, m, gate)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })

	f.findIn("above zero")
	for range len("above zero") {
		f.send(key("shift+right"))
	}
	f.typeIn("of at most 5 s")
	f.send(key("ctrl+p"))
	f.send(key("y")) // save first

	f.until("the check running after the gate", func(a *App) bool {
		r := a.activeRun()
		return r != nil && len(r.stages) == 1 && r.current != nil && r.current.Name == proposal.StageCheck
	})
	f.do(func(a *App) {
		assert.Equal(t, CtxEditor, a.context(), "the editor keeps the keyboard")
		side := sidebarText(a)
		assert.Contains(t, side, "$Frame.Timing")
		assert.Contains(t, strings.ToLower(side), "checking")
		run := a.activeRun()
		require.NotNil(t, run)
		assert.Equal(t, proposal.StageGate, run.stages[0].Name)
		assert.Equal(t, proposal.StageCheck, run.current.Name)
		assert.Contains(t, a.jobText(), "Checking $Frame.Timing")
		assert.NotContains(t, a.jobText(), "jev/")
		assert.NotContains(t, a.jobText(), "gate")
		assert.Contains(t, a.jobText(), "^C cancels")
	})
	// Typing goes on while the check runs.
	f.typeIn(" ")
	f.do(func(a *App) { assert.True(t, a.cur.Ed.Buffer().Dirty(), "typed into the editor") })

	close(m.gate)
	f.until("the findings", func(a *App) bool { return a.job == nil && itemCards(a) == 2 })
	f.do(func(a *App) {
		assert.Empty(t, a.side.runs, "the filed check's card replaces the run")
		assert.Equal(t, CtxEditor, a.context())
		side := sidebarText(a)
		assert.Contains(t, side, "Your edit")
		assert.Contains(t, side, "$Frame.Timing")
		assert.Contains(t, side, "mention the bound")
		assert.Contains(t, side, "align the queue size")
		assert.NotContains(t, side, "jev/")
	})
	ps := proposalsIn(t, dir)
	require.Len(t, ps, 1)
	assert.Equal(t, []string{"gate", "check"}, []string{ps[0].Inference.Stages[0].Name, ps[0].Inference.Stages[1].Name})
}

// ^P on an edit the gate clears: no check request, nothing filed; the
// flash and the anchor's card say why, and the commit flow counts the
// text as checked.
func TestCascadeGateClearsInUI(t *testing.T) {
	dir := demoRepo(t)
	var class atomic.Value
	class.Store(0.1)
	gate := fakeGate(t, &class)
	m := newFakeModel(t, checkReply(t))
	f := startFlow(t, uiOptions(dir, cascadeWorkflow(t, m, gate)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })

	f.findIn("above zero")
	f.typeIn("strictly ")
	f.send(key("ctrl+p"))
	f.send(key("y"))
	f.until("the check", func(a *App) bool { return a.job == nil && len(a.side.runs) == 1 && a.side.runs[0].done })
	f.do(func(a *App) {
		assert.Regexp(t, `^\$Frame\.Timing: No logically significant change found \([0-9.]+ s\)$`, a.flash)
		r := a.side.runs[0]
		assert.Equal(t, proposal.StageGate, r.cleared)
		side := sidebarText(a)
		assert.Contains(t, side, "No logically significant change found (")
		assert.NotContains(t, side, "jev/")
		require.Len(t, r.stages, 1)
		assert.Equal(t, "clear", r.stages[0].Outcome)
		assert.NotContains(t, side, "nothing filed")
		assert.Empty(t, a.uncheckedAnchors(), "the cleared text counts as checked")
		assert.Equal(t, CtxEditor, a.context())
	})
	assert.Empty(t, m.requests(), "the check did not run")
	assert.Empty(t, proposalsIn(t, dir), "nothing filed")

	// A further edit is unchecked again.
	f.typeIn("x")
	f.send(key("ctrl+s"))
	f.until("the reparse", func(a *App) bool { return len(a.uncheckedAnchors()) == 1 })
}

// ^P on an edit the gate escalates and triage clears: the flash and the
// card lead with "No conflict found", the stage lines below.
func TestCascadeTriageClearsInUI(t *testing.T) {
	dir := demoRepo(t)
	var class atomic.Value
	class.Store(0.62)
	gate := fakeGate(t, &class)
	m := newFakeModel(t, checkReply(t))
	tri := newFakeModel(t, "```json\n{\"material_change\": 1, \"numeric_mismatch\": 0.1, \"contradiction\": 0.05, \"stale_reference\": 0}\n```")
	wf := cascadeWorkflow(t, m, gate, "[providers.tri]\nbase_url = \""+tri.URL+"\"\nmodel = \"glm\"\nstream = false\n[triage]\nprovider = \"tri\"\n")
	f := startFlow(t, uiOptions(dir, wf))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })

	f.findIn("above zero")
	f.typeIn("strictly ")
	f.send(key("ctrl+p"))
	f.send(key("y"))
	f.until("the check", func(a *App) bool { return a.job == nil && len(a.side.runs) == 1 && a.side.runs[0].done })
	f.do(func(a *App) {
		assert.Regexp(t, `^\$Frame\.Timing: No conflict found \([0-9.]+ s\)$`, a.flash)
		assert.Equal(t, proposal.StageTriage, a.side.runs[0].cleared)
		side := sidebarText(a)
		assert.Contains(t, side, "No conflict found (")
		assert.NotContains(t, side, "tri/glm")
		stages := a.side.runs[0].stages
		require.Len(t, stages, 2)
		assert.Equal(t, proposal.StageGate, stages[0].Name)
		assert.Equal(t, "pass", stages[0].Outcome)
		assert.Equal(t, proposal.StageTriage, stages[1].Name)
		assert.Equal(t, "clear", stages[1].Outcome)
	})
	assert.Empty(t, m.requests(), "the check did not run")
	assert.Empty(t, proposalsIn(t, dir), "nothing filed")
	f.finish()
}
