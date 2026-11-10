// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/workflow"
)

// The demo project of the step 4b flows (the CLI tests' demo spec).
const demoSpec = `# demo

## 1. Terms

**Def $Time.Media (media time).** Media time is the timestamp carried by a frame, in ticks of 1/90000 s.

## 2. Rules

**$Frame.Timing (fragment timing).** Every fragment MUST have a duration above zero in $Time.Media.
Checked by: TestFragment.

**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, see $Frame.Timing.
Checked by: TestOrder.

Prose that mentions $Frame.Order for the reader.
`

const otherSpec = `# other

## 1. Extras

**$Extra.Limit (extra limit).** The extra queue MUST hold at most 8 items. Checked by: TestLimit.
`

const (
	timingOld = "**$Frame.Timing (fragment timing).** Every fragment MUST have a duration above zero in $Time.Media.\nChecked by: TestFragment."
	timingNew = "**$Frame.Timing (fragment timing).** Every fragment MUST have a duration of at most 5 s in $Time.Media.\nChecked by: TestFragment."
)

// gitEnv isolates git from the machine's configuration.
func gitEnv(t *testing.T) {
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
}

// gitRun runs git in dir and returns its output.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

// demoRepo is a committed repository holding the demo project.
func demoRepo(t *testing.T) string {
	t.Helper()
	gitEnv(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	writeFile(t, dir, "spec.toml", "name = \"demo\"\n")
	writeFile(t, dir, "demo.spec.md", demoSpec)
	writeFile(t, dir, "other.spec.md", otherSpec)
	writeFile(t, dir, ".gitignore", ".spec/\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	return dir
}

// fakeModel is a fake OpenAI-compatible provider. Each request gets the
// next reply of the queue (the last one repeats); gate, when set, holds
// every reply until it is closed.
type fakeModel struct {
	*httptest.Server
	mu      sync.Mutex
	replies []string
	bodies  []string
	gate    chan struct{}
}

func newFakeModel(t *testing.T, replies ...string) *fakeModel {
	t.Helper()
	m := &fakeModel{replies: replies}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.bodies = append(m.bodies, string(raw))
		reply := ""
		if len(m.replies) > 0 {
			reply = m.replies[0]
			if len(m.replies) > 1 {
				m.replies = m.replies[1:]
			}
		}
		gate := m.gate
		m.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "check-model",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15},
		})
	}))
	t.Cleanup(m.Close)
	return m
}

// requests returns the request bodies seen so far.
func (m *fakeModel) requests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.bodies...)
}

// testWorkflow is a workflow whose user config (in a fresh HOME) names the
// fake provider m (nil: no user config at all).
func testWorkflow(t *testing.T, m *fakeModel) *workflow.Workflow {
	t.Helper()
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config")}
	if m != nil {
		cfg := filepath.Join(env["XDG_CONFIG_HOME"], "spec", "config.toml")
		require.NoError(t, os.MkdirAll(filepath.Dir(cfg), 0o755))
		require.NoError(t, os.WriteFile(cfg, []byte("default = \"fake\"\n[providers.fake]\nbase_url = \""+m.URL+"\"\nmodel = \"check-model\"\nstream = false\n"), 0o644))
	}
	return &workflow.Workflow{
		Getenv: func(k string) string {
			if v, ok := env[k]; ok {
				return v
			}
			return os.Getenv(k)
		},
		TuneClient: func(c *infer.Client) { c.BaseDelay = time.Millisecond; c.MaxDelay = time.Millisecond },
	}
}

// modelJSON encodes a model reply.
func modelJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// ---- the flow driver ----

// probeMsg runs fn on the program's goroutine (from its message filter),
// so a test reads and changes the model without racing the program.
type probeMsg struct {
	fn   func(a *App)
	done chan struct{}
}

// flow drives an App through a real Bubble Tea program at
// 100x30, with background jobs and watchers running for real.
type flow struct {
	t         *testing.T
	p         *tea.Program
	a         *App
	done      chan struct{}
	runErr    error
	closeOnce sync.Once
}

func startFlow(t *testing.T, opts Options) *flow {
	t.Helper()
	a, err := New(opts)
	require.NoError(t, err)
	filter := func(_ tea.Model, m tea.Msg) tea.Msg {
		switch p := m.(type) {
		case probeMsg:
			p.fn(a)
			close(p.done)
			return nil
		}
		return m
	}
	p := tea.NewProgram(a, tea.WithInput(&bytes.Buffer{}), tea.WithOutput(io.Discard),
		tea.WithoutSignals(), tea.WithWindowSize(frameW, frameH), tea.WithFilter(filter))
	f := &flow{t: t, p: p, a: a, done: make(chan struct{})}
	go runProgram(p, f.done, &f.runErr)
	// A test may fail, or intentionally omit finish. Stop and join the
	// renderer/event loop before App.Close mutates the model.
	t.Cleanup(f.close)
	f.send(tea.WindowSizeMsg{Width: frameW, Height: frameH})
	return f
}

// send sends msgs and waits until the program has handled them.
func (f *flow) send(msgs ...tea.Msg) {
	f.t.Helper()
	for _, m := range msgs {
		f.p.Send(m)
	}
	f.do(func(*App) {})
}

// typeIn types s.
func (f *flow) typeIn(s string) {
	f.t.Helper()
	f.send(typeText(s)...)
}

// do runs fn on the program's goroutine and waits for it.
func (f *flow) do(fn func(a *App)) {
	f.t.Helper()
	done := make(chan struct{})
	f.p.Send(probeMsg{fn: fn, done: done})
	select {
	case <-done:
	case <-f.done:
		f.t.Fatalf("the program ended before the probe: %v", f.runErr)
	case <-time.After(10 * time.Second):
		f.t.Fatal("the program did not handle the probe")
	}
}

// until waits for cond (checked on the program's goroutine).
func (f *flow) until(what string, cond func(a *App) bool) {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ok := false
		f.do(func(a *App) { ok = cond(a) })
		if ok {
			return
		}
		if time.Now().After(deadline) {
			var frame string
			f.do(func(a *App) { frame = plain(a) })
			f.t.Fatalf("timed out waiting for %s; the frame:\n%s", what, frame)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// frame returns the plain frame.
func (f *flow) frame() string {
	var s string
	f.do(func(a *App) { s = plain(a) })
	return s
}

// finish quits the program and returns the final model.
func (f *flow) finish() *App {
	f.t.Helper()
	f.close()
	require.NoError(f.t, f.runErr)
	return f.a
}

func (f *flow) close() {
	f.closeOnce.Do(func() {
		stopProgram(f.p, f.done, DriverTimeout)
		f.a.Close()
		if f.runErr != nil {
			f.t.Errorf("Bubble Tea program failed: %v", f.runErr)
		}
	})
}

// findIn moves the cursor to q with ^W.
func (f *flow) findIn(q string) {
	f.t.Helper()
	msgs := []tea.Msg{key("ctrl+w")}
	msgs = append(msgs, typeText(q)...)
	f.send(append(msgs, key("enter"))...)
}
