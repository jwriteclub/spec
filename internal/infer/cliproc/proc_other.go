// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package cliproc

import "os/exec"

// ownGroup leaves cancellation to exec's default, which kills the program
// itself; without process groups its children are not reached.
func ownGroup(cmd *exec.Cmd) {}

// killGroup has nothing to do without process groups.
func killGroup(pid int) {}

// watchSignals has nothing to cover: without process groups the CLI gets
// the console's signals itself.
func watchSignals(pid int) (stop func()) { return func() {} }
