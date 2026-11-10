// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package golden compares test output with checked-in golden files.
//
// Importing this package from a test registers the -update flag, so
// `go test ./... -update` rewrites every golden file. Every package whose
// tests run under `go test ./... -update` must import it (directly or through
// a helper), otherwise its test binary rejects the unknown flag.
package golden

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Update is true when the test binary was started with -update.
var Update = flag.Bool("update", false, "rewrite golden files with the current output")

// Check compares got with the golden file at path. With -update it writes got
// to path instead (creating directories as needed) and passes.
func Check(t testing.TB, path string, got []byte) {
	t.Helper()
	if *Update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("golden: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden: %v (run with -update to create it)", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("golden mismatch for %s (run with -update to accept):\n%s", path, diffLines(want, got))
	}
}

// diffLines returns a short description of the first differing line.
func diffLines(want, got []byte) string {
	wl := bytes.Split(want, []byte("\n"))
	gl := bytes.Split(got, []byte("\n"))
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g []byte
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if !bytes.Equal(w, g) {
			return "line " + strconv.Itoa(i+1) + ":\n  want: " + string(w) + "\n  got:  " + string(g)
		}
	}
	return "(no line differs; trailing bytes differ)"
}
