// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keyRecorder is a Bubble Tea model that records the key strings Bubble
// Tea decodes, optionally passing each key to an App, and quits after want
// keys.
type keyRecorder struct {
	app  *App
	want int
	keys []string
	msgs []tea.KeyPressMsg
}

// msg returns the k-th decoded key press.
func (r *keyRecorder) msg(k int) tea.KeyPressMsg { return r.msgs[k] }

func (r *keyRecorder) Init() tea.Cmd { return nil }

func (r *keyRecorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return r, nil
	}
	r.keys = append(r.keys, k.String())
	r.msgs = append(r.msgs, k)
	if r.app != nil {
		// Commands (the clipboard, timers) are not run: the effect on
		// the App's state is what the test checks.
		r.app.Update(msg)
	}
	if len(r.keys) == r.want {
		return r, tea.Quit
	}
	return r, nil
}

func (r *keyRecorder) View() tea.View { return tea.NewView("") }

// runRaw writes raw terminal input bytes to a real Bubble Tea program and
// returns the model once it has decoded want keys.
func runRaw(t *testing.T, r *keyRecorder, input string) *keyRecorder {
	t.Helper()
	pr, pw := io.Pipe()
	p := tea.NewProgram(r, tea.WithInput(pr), tea.WithOutput(io.Discard),
		tea.WithoutSignals(), tea.WithEnvironment([]string{"TERM=xterm-256color"}))
	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		done <- err
	}()
	go func() { _, _ = pw.Write([]byte(input)) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		p.Kill()
		t.Fatalf("decoded %q of %q before the timeout", r.keys, input)
	}
	_ = pw.Close()
	return r
}

// TestRawControlBytes feeds the raw bytes a terminal in raw mode sends for
// the ctrl keys through Bubble Tea's own input decoder (a real program
// reading a pipe). ctrl+Z (0x1A) and ctrl+Q (0x11) arrive as ordinary keys:
// Bubble Tea suspends only on a SuspendMsg, which the App never sends, and
// in raw mode (ISIG and IXON off, see TestRawModeOverPTY) the tty driver
// passes both bytes through.
func TestRawControlBytes(t *testing.T) {
	cases := []struct {
		raw  byte
		key  string
		act  Action
		desc string
	}{
		{0x1a, "ctrl+z", ActUndo, "SUB: suspend in cooked mode"},
		{0x11, "ctrl+q", ActQuickOpen, "DC1: XON in cooked mode"},
		{0x19, "ctrl+y", ActRedo, "EM: delayed suspend (BSD dsusp) in cooked mode"},
		{0x1c, "ctrl+\\", ActRefs, "FS: SIGQUIT in cooked mode"},
		{0x14, "ctrl+t", ActToTarget, "DC4: status (BSD SIGINFO) in cooked mode"},
		{0x02, "ctrl+b", ActBack, ""},
		{0x06, "ctrl+f", ActFormat, ""},
		{0x03, "ctrl+c", ActCopy, "ETX: SIGINT in cooked mode"},
		{0x10, "ctrl+p", ActPropose, ""},
		{0x12, "ctrl+r", ActReview, ""},
		{0x0e, "ctrl+n", ActAnalyze, ""},
		{0x1e, "ctrl+^", ActOutline, "ctrl+6 (Terminal.app, xterm)"},
		{0x00, "ctrl+space", ActMark, "NUL: ctrl+space, ctrl+2"},
		{0x1f, "ctrl+_", ActGoToLine, "ctrl+/ on many keyboards"},
		{0x16, "ctrl+v", ActPageDown, "LNEXT in cooked mode"},
		{0x0f, "ctrl+o", ActWriteOut, "DISCARD in cooked mode"},
	}
	var input []byte
	for _, c := range cases {
		input = append(input, c.raw)
	}
	r := runRaw(t, &keyRecorder{want: len(cases)}, string(input))
	require.Len(t, r.keys, len(cases))
	for k, c := range cases {
		assert.Equal(t, c.key, r.keys[k], "byte 0x%02x (%s)", c.raw, c.desc)
		b, ok := Lookup(CtxEditor, r.keys[k])
		if assert.True(t, ok, c.key) {
			assert.Equal(t, c.act, b.Action, c.key)
		}
	}

	// The forbidden control codes decode as the plain keys they are, or
	// as ctrl+h, which the App treats as backspace.
	r = runRaw(t, &keyRecorder{want: 3}, "\x08\x09\x0d")
	assert.Equal(t, []string{"ctrl+h", "tab", "enter"}, r.keys)
}

// TestRawUndoAndQuickOpen drives the App through raw bytes: typing, ^Z
// (0x1A) undoes it, and ^Q (0x11) opens quick open.
func TestRawUndoAndQuickOpen(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	orig := string(a.cur.Ed.Buffer().Bytes())
	r := runRaw(t, &keyRecorder{app: a, want: 4}, "xy\x1a\x11")
	assert.Equal(t, []string{"x", "y", "ctrl+z", "ctrl+q"}, r.keys)
	assert.Equal(t, orig, string(a.cur.Ed.Buffer().Bytes()), "^Z undid the typed run")
	require.NotNil(t, a.list, "^Q opened quick open")
	assert.Equal(t, CtxList, a.context())
	assert.True(t, a.list.filter, "quick open is the filtered list")
}
