// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package cliproc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMode names what the test binary does when it runs as the fake CLI.
const fakeMode = "CLIPROC_FAKE"

// TestMain lets the test binary stand in for a CLI: with CLIPROC_FAKE set
// it runs fake() instead of the tests.
func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeMode); mode != "" {
		os.Exit(fake(mode))
	}
	os.Exit(m.Run())
}

// seen is what the fake reports about how it was run.
type seen struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
	Cwd   string   `json:"cwd"`
	Files []string `json:"files"`
	Env   []string `json:"env"`
	Pgid  int      `json:"pgid"`
	Pid   int      `json:"pid"`
}

func fake(mode string) int {
	switch mode {
	case "echo":
		in, _ := io.ReadAll(os.Stdin)
		cwd, _ := os.Getwd()
		entries, _ := os.ReadDir(".")
		files := []string{}
		for _, e := range entries {
			files = append(files, e.Name())
		}
		pgid, _ := syscall.Getpgid(os.Getpid())
		_ = json.NewEncoder(os.Stdout).Encode(seen{
			Args: os.Args[1:], Stdin: string(in), Cwd: cwd, Files: files, Env: os.Environ(),
			Pgid: pgid, Pid: os.Getpid(),
		})
		return 0
	case "fail":
		fmt.Fprintln(os.Stderr, "\n  error: unknown option '--nope'  \nmore")
		fmt.Fprintln(os.Stdout, "partial")
		return 3
	case "flood":
		chunk := strings.Repeat("x", 1<<20)
		for range MaxStdout/len(chunk) + 2 {
			fmt.Fprint(os.Stdout, chunk)
		}
		return 0
	case "hang":
		// Start a grandchild in the same group, report its pid, and
		// sleep: only a group kill reaches the grandchild.
		child := exec.Command(os.Args[0])
		child.Env = []string{fakeMode + "=sleep"}
		if err := child.Start(); err != nil {
			return 9
		}
		_ = os.WriteFile(os.Getenv("CLIPROC_PIDFILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o644)
		time.Sleep(time.Minute)
		return 0
	case "sleep":
		time.Sleep(time.Minute)
		return 0
	case "parent":
		// Play spec: run a hanging CLI (which starts a grandchild) and
		// wait to be interrupted.
		_, _ = Run(context.Background(), Command{
			Path: os.Args[0],
			Env:  []string{fakeMode + "=hang", "CLIPROC_PIDFILE=" + os.Getenv("CLIPROC_PIDFILE")},
		})
		return 0
	}
	return 99
}

func self(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	return exe
}

func TestRunEchoesArgvStdinCwdAndEnv(t *testing.T) {
	res, err := Run(context.Background(), Command{
		Path:  self(t),
		Args:  []string{"-p", "--tools", "", "two words"},
		Stdin: []byte("the prompt\nline 2"),
		Env:   []string{fakeMode + "=echo", "ONLY=this"},
	})
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	var s seen
	require.NoError(t, json.Unmarshal(res.Stdout, &s))
	assert.Equal(t, []string{"-p", "--tools", "", "two words"}, s.Args, "argv passes through untouched, empty arguments included")
	assert.Equal(t, "the prompt\nline 2", s.Stdin)
	assert.Empty(t, s.Files, "the working directory is empty")
	assert.True(t, strings.HasPrefix(filepath.Base(s.Cwd), "spec-cli-"), s.Cwd)
	_, statErr := os.Stat(s.Cwd)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "the working directory is removed afterwards")
	assert.ElementsMatch(t, []string{fakeMode + "=echo", "ONLY=this"}, s.Env, "only the listed environment")
	assert.Equal(t, s.Pid, s.Pgid, "the CLI leads its own process group")
	assert.Positive(t, res.Elapsed)
}

func TestRunWritesFiles(t *testing.T) {
	res, err := Run(context.Background(), Command{
		Path:  self(t),
		Env:   []string{fakeMode + "=echo"},
		Files: map[string][]byte{"instructions.md": []byte("be brief")},
	})
	require.NoError(t, err)
	var s seen
	require.NoError(t, json.Unmarshal(res.Stdout, &s))
	assert.Equal(t, []string{"instructions.md"}, s.Files, "only the caller's files")
	_, statErr := os.Stat(s.Cwd)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "removed afterwards, files and all")

	for _, bad := range []string{"", "../x", "a/b", ".."} {
		_, err := Run(context.Background(), Command{Path: self(t), Env: []string{fakeMode + "=echo"}, Files: map[string][]byte{bad: nil}})
		assert.ErrorContains(t, err, "not a plain file name", bad)
	}
}

func TestRunNonZeroExitIsAResult(t *testing.T) {
	res, err := Run(context.Background(), Command{Path: self(t), Env: []string{fakeMode + "=fail"}})
	require.NoError(t, err)
	assert.Equal(t, 3, res.ExitCode)
	assert.Equal(t, "partial\n", string(res.Stdout))
	assert.Equal(t, "error: unknown option '--nope'", res.StderrLine())
}

func TestRunOutputTooLarge(t *testing.T) {
	_, err := Run(context.Background(), Command{Path: self(t), Env: []string{fakeMode + "=flood"}, Timeout: 30 * time.Second})
	assert.ErrorIs(t, err, ErrOutputTooLarge)
}

// alive reports whether pid still runs.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestRunTimeoutKillsTheWholeGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	_, err := Run(context.Background(), Command{
		Path:    self(t),
		Env:     []string{fakeMode + "=hang", "CLIPROC_PIDFILE=" + pidFile},
		Timeout: 500 * time.Millisecond,
	})
	require.ErrorIs(t, err, ErrTimeout)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 10*time.Second)
	raw, rerr := os.ReadFile(pidFile)
	require.NoError(t, rerr, "the fake started its grandchild")
	pid, _ := strconv.Atoi(string(raw))
	require.Positive(t, pid)
	assert.Eventually(t, func() bool { return !alive(pid) }, 5*time.Second, 20*time.Millisecond, "the grandchild was killed with the group")
}

func TestInterruptKillsTheGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	parent := exec.Command(self(t))
	parent.Env = []string{fakeMode + "=parent", "CLIPROC_PIDFILE=" + pidFile}
	require.NoError(t, parent.Start())
	var pid int
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, _ = strconv.Atoi(string(raw))
		return pid > 0
	}, 10*time.Second, 20*time.Millisecond, "the CLI started its grandchild")
	require.NoError(t, parent.Process.Signal(syscall.SIGINT))
	err := parent.Wait()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, "spec still dies of the interrupt")
	ws, _ := ee.Sys().(syscall.WaitStatus)
	assert.True(t, ws.Signaled() && ws.Signal() == syscall.SIGINT, "re-raised: %v", err)
	assert.Eventually(t, func() bool { return !alive(pid) }, 5*time.Second, 20*time.Millisecond, "the grandchild was killed with the group")
}

func TestRunCallerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := Run(ctx, Command{Path: self(t), Env: []string{fakeMode + "=sleep"}, Timeout: time.Minute})
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrTimeout)
}

func TestRunMissingProgram(t *testing.T) {
	_, err := Run(context.Background(), Command{Path: filepath.Join(t.TempDir(), "nope")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "starting")
}

func TestLookup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mycli")
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain"), []byte("x"), 0o644))

	p, err := Lookup("mycli", "/nonexistent"+string(filepath.ListSeparator)+dir)
	require.NoError(t, err)
	assert.Equal(t, exe, p)
	p, err = Lookup(exe, "")
	require.NoError(t, err)
	assert.Equal(t, exe, p)

	_, err = Lookup("mycli", "/nonexistent")
	assert.EqualError(t, err, "mycli was not found on PATH")
	_, err = Lookup(filepath.Join(dir, "plain"), dir)
	assert.ErrorContains(t, err, "is not an executable file")
	_, err = Lookup("", dir)
	assert.Error(t, err)
}

func TestEnv(t *testing.T) {
	vars := map[string]string{"HOME": "/h", "PATH": "/bin", "EMPTY": "", "SECRET": "s"}
	got := Env(func(k string) string { return vars[k] },
		[]string{"HOME", "PATH", "EMPTY", "MISSING", "HOME", "OVERRIDE"},
		map[string]string{"Z_SET": "1", "OVERRIDE": "mine", "A_SET": "2"})
	assert.Equal(t, []string{"HOME=/h", "PATH=/bin", "A_SET=2", "OVERRIDE=mine", "Z_SET=1"}, got)
}
