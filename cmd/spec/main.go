// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Command spec checks, explains and edits a project's *.spec.md files.
package main

import (
	"fmt"
	"os"

	"github.com/jwriteclub/spec/internal/cli"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "spec: cannot determine the current directory: %v\n", err)
		os.Exit(1)
	}
	code := cli.Run(os.Args[1:], cli.Env{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Cwd:    cwd,
		Getenv: os.Getenv,
	})
	os.Exit(code)
}
