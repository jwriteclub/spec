// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package cliproc runs a local inference CLI (Claude Code's `claude -p`,
// Codex's `codex exec`) as one bounded subprocess: an explicit argv with no
// shell, the prompt on stdin, a fresh temporary directory as the working
// directory, holding only the files the caller puts there (so the CLI finds
// no project instruction files such as CLAUDE.md or AGENTS.md), only the
// environment the caller lists, and a timeout. When the timeout passes, the caller's context ends, or spec
// itself gets an interrupt, hangup or termination signal, the whole process
// group is killed, so nothing the CLI started outlives the call.
// The temporary directory is removed afterwards.
//
// The package knows nothing about any CLI's flags or output; the providers
// in package infer build the argv and parse stdout.
package cliproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/tenta-browser/polychromatic"
)

var lg *logrus.Entry = polychromatic.GetLogger("cliproc")

// Limits on what a run keeps.
const (
	// MaxStdout bounds the stdout kept; a run that writes more fails with
	// ErrOutputTooLarge.
	MaxStdout = 16 << 20
	// MaxStderr bounds the stderr kept (the start of it), for error
	// messages.
	MaxStderr = 16 << 10
	// waitDelay is how long Wait waits for the output pipes to close after
	// the process was killed (a grandchild may hold them).
	waitDelay = 2 * time.Second
)

// ErrTimeout is the cause of a run that took longer than its timeout. It
// wraps context.DeadlineExceeded, so errors.Is(err, context.DeadlineExceeded)
// holds too.
var ErrTimeout = fmt.Errorf("the command took longer than its timeout: %w", context.DeadlineExceeded)

// ErrOutputTooLarge is a run whose stdout exceeded MaxStdout.
var ErrOutputTooLarge = fmt.Errorf("the command wrote more than %d bytes to stdout", MaxStdout)

// Command is one run of a CLI.
type Command struct {
	// Path is the program: a path, or a name looked up on PATH by Lookup
	// beforehand.
	Path string
	// Args are the arguments after the program name; no shell sees them.
	Args []string
	// Stdin is written to the program's standard input, which is then
	// closed.
	Stdin []byte
	// Env is the program's whole environment ("NAME=value" entries); nil
	// is an empty environment, never the caller's.
	Env []string
	// Timeout bounds the run; 0 means none (the context still applies).
	Timeout time.Duration
	// Files are written into the working directory before the program
	// starts, name to contents (a plain file name, no directories), so
	// Args can name them by relative path: Codex reads its instructions
	// from a file, for example.
	Files map[string][]byte
}

// Result is a run that finished (whatever its exit code).
type Result struct {
	Stdout []byte
	// Stderr is the start of standard error, at most MaxStderr bytes.
	Stderr string
	// ExitCode is the program's exit status.
	ExitCode int
	// Elapsed is the wall time from start to exit.
	Elapsed time.Duration
}

// StderrLine returns the first non-blank line of Stderr, trimmed ("" when
// there is none), for an error message.
func (r *Result) StderrLine() string {
	for line := range strings.SplitSeq(r.Stderr, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// Run runs c in a new temporary directory holding only c.Files and waits
// for it. A
// program that ran and exited, with any exit code, returns its Result and a
// nil error; the caller reads the exit code and the output. Otherwise the
// error says why: the program could not start, ErrTimeout, ErrOutputTooLarge,
// or the context's own error when ctx ended first.
func Run(ctx context.Context, c Command) (*Result, error) {
	dir, err := os.MkdirTemp("", "spec-cli-")
	if err != nil {
		return nil, fmt.Errorf("creating a working directory for %s: %w", filepath.Base(c.Path), err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			lg.WithError(err).Warnf("removing %s", dir)
		}
	}()
	if err := writeFiles(dir, c.Files); err != nil {
		return nil, err
	}

	rctx := ctx
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		rctx, cancel = context.WithTimeoutCause(ctx, c.Timeout, ErrTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(rctx, c.Path, c.Args...)
	cmd.Dir = dir
	cmd.Env = c.Env
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	cmd.Stdin = bytes.NewReader(c.Stdin)
	stdout := &capped{limit: MaxStdout}
	stderr := &capped{limit: MaxStderr}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = waitDelay
	ownGroup(cmd)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", c.Path, err)
	}
	pid := cmd.Process.Pid
	stopWatch := watchSignals(pid)
	defer stopWatch()
	lg.Debugf("started %s (pid %d) with %d arguments, %d bytes on stdin, in %s", filepath.Base(c.Path), pid, len(c.Args), len(c.Stdin), dir)
	werr := cmd.Wait()
	elapsed := time.Since(start)
	// Whatever the CLI left running in its group goes with it.
	killGroup(pid)

	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case rctx.Err() != nil:
		return nil, context.Cause(rctx)
	case stdout.over:
		return nil, ErrOutputTooLarge
	}
	res := &Result{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.String(), Elapsed: elapsed}
	if werr != nil {
		var ee *exec.ExitError
		if !errors.As(werr, &ee) {
			return nil, fmt.Errorf("running %s: %w", c.Path, werr)
		}
		res.ExitCode = ee.ExitCode()
	}
	lg.Debugf("%s (pid %d) exited %d after %s: %d bytes out, %d bytes on stderr", filepath.Base(c.Path), pid, res.ExitCode, elapsed.Round(time.Millisecond), len(res.Stdout), len(res.Stderr))
	return res, nil
}

// writeFiles writes files into dir, readable by the owner only. A name
// must be a plain file name.
func writeFiles(dir string, files map[string][]byte) error {
	for name, data := range files {
		if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
			return fmt.Errorf("cliproc: %q is not a plain file name", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return fmt.Errorf("writing %s for the command: %w", name, err)
		}
	}
	return nil
}

// capped keeps the first limit bytes written to it. Past the limit it drops
// the rest and sets over, but still reports every byte as written: the pipe
// keeps draining, so the program never blocks on a full pipe.
type capped struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (w *capped) Write(p []byte) (int, error) {
	room := w.limit - w.buf.Len()
	if len(p) > room {
		w.over = true
		w.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return w.buf.Write(p)
}

// Lookup resolves a command for Command.Path: a name with no slash is
// searched in path (a PATH value), anything else must be an executable
// file. The error says where it looked.
func Lookup(command, path string) (string, error) {
	if command == "" {
		return "", errors.New("no command given")
	}
	if strings.ContainsRune(command, filepath.Separator) {
		p, err := exec.LookPath(command)
		if err != nil {
			return "", fmt.Errorf("%s is not an executable file", command)
		}
		return p, nil
	}
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			continue
		}
		if p, err := exec.LookPath(filepath.Join(d, command)); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s was not found on PATH", command)
}

// Env builds a minimal environment: each variable in names that getenv
// reports non-empty, in order and once each, then set (which wins).
func Env(getenv func(string) string, names []string, set map[string]string) []string {
	seen := map[string]bool{}
	var env []string
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		if _, ok := set[n]; ok {
			continue
		}
		if v := getenv(n); v != "" {
			env = append(env, n+"="+v)
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	// Deterministic order, for tests and debugging.
	slices.Sort(keys)
	for _, k := range keys {
		env = append(env, k+"="+set[k])
	}
	return env
}

// BaseEnv names the variables every CLI provider passes through: what a
// CLI needs to find its own login and configuration (HOME, USER), run its
// runtime (PATH, TMPDIR, locale) and reach the network through a proxy or a
// corporate CA. Anything else (API keys, the parent agent's session
// variables) stays out unless the provider's pass_env names it.
var BaseEnv = []string{
	"HOME", "USER", "LOGNAME", "PATH", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "no_proxy", "all_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
}
