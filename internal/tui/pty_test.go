// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

//go:build darwin || linux

package tui

import (
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// ttyFlags reads the ISIG and IXON bits of the terminal on fd.
func ttyFlags(t *testing.T, fd int) (isig, ixon bool) {
	t.Helper()
	tio, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	require.NoError(t, err)
	return tio.Lflag&unix.ISIG != 0, tio.Iflag&unix.IXON != 0
}

// TestRawModeOverPTY runs a real Bubble Tea program on a pseudo-terminal
// and types ctrl+Z and ctrl+Q on it. In the tty's default (cooked) mode,
// ISIG turns 0x1A into SIGTSTP and IXON swallows 0x11 as XON, so neither
// would reach the program. Bubble Tea's raw mode (x/term MakeRaw, like
// cfmakeraw) clears both, so both arrive as keys; on exit the terminal is
// restored.
func TestRawModeOverPTY(t *testing.T) {
	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	fd := int(slave.Fd())

	isig, ixon := ttyFlags(t, fd)
	require.True(t, isig, "a fresh tty has ISIG on")
	require.True(t, ixon, "a fresh tty has IXON on")

	r := &keyRecorder{want: 2}
	p := tea.NewProgram(r, tea.WithInput(slave), tea.WithOutput(io.Discard),
		tea.WithoutSignals(), tea.WithEnvironment([]string{"TERM=xterm-256color"}))
	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		done <- err
	}()

	// Type only once the program has the tty in raw mode.
	deadline := time.Now().Add(10 * time.Second)
	for {
		isig, ixon = ttyFlags(t, fd)
		if !isig && !ixon {
			break
		}
		if time.Now().After(deadline) {
			p.Kill()
			t.Fatalf("Bubble Tea left ISIG=%v IXON=%v", isig, ixon)
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, err = master.Write([]byte{0x1a, 0x11})
	require.NoError(t, err)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		p.Kill()
		t.Fatalf("only %q arrived", r.keys)
	}
	assert.Equal(t, []string{"ctrl+z", "ctrl+q"}, r.keys)
	for _, k := range r.keys {
		b, ok := Lookup(CtxEditor, k)
		require.True(t, ok, k)
		assert.NotEqual(t, ActNone, b.Action)
	}

	isig, ixon = ttyFlags(t, fd)
	assert.True(t, isig, "ISIG restored on exit")
	assert.True(t, ixon, "IXON restored on exit")
}
