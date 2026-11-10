// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package cliproc

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// ownGroup starts cmd in a new process group whose id is its pid, and makes
// cancellation (the timeout or the caller's context) kill that whole group.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		killGroup(cmd.Process.Pid)
		return nil
	}
}

// killGroup kills every process left in the group led by pid. A group
// that is already gone is not an error.
func killGroup(pid int) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		lg.WithError(err).Debugf("killing process group %d", pid)
	}
}

// watchSignals covers the gap Setpgid opens: the CLI is not in the
// terminal's foreground group, so a ^C, hangup or kill aimed at spec would
// not reach it. While the run lasts, such a signal kills the CLI's group
// and is then raised again, so spec still ends the way it would have. stop
// ends the watch.
func watchSignals(pid int) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-ch:
			killGroup(pid)
			signal.Stop(ch)
			if s, ok := sig.(syscall.Signal); ok {
				_ = syscall.Kill(os.Getpid(), s)
			}
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
