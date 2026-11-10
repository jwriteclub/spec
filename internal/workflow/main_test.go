// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain points HOME and XDG_CONFIG_HOME at a temp directory: a Workflow with
// no Getenv reads the process's environment, and without this a check in a test
// would load the user's real spec config and its providers.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "spec-workflow-test-home-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config")); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
