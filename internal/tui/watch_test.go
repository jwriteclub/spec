// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitChange runs the watcher's next command with a timeout.
func waitChange(t *testing.T, w *watcher) []string {
	t.Helper()
	got := make(chan []string, 1)
	go func() {
		if m, ok := w.next()().(fileChangedMsg); ok {
			got <- m.paths
			return
		}
		got <- nil
	}()
	select {
	case ps := <-got:
		return ps
	case <-time.After(5 * time.Second):
		t.Fatal("no change reported within 5 s")
		return nil
	}
}

// TestWatcherSeesWritesAndRenames: an in-place write and a replace by
// rename (how editors and agents save) are both reported; other files in
// the directory are not.
func TestWatcherSeesWritesAndRenames(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.spec.md")
	require.NoError(t, os.WriteFile(p, []byte("one\n"), 0o644))
	w := newWatcher(dir, []string{"a.spec.md"})
	defer w.Close()

	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("two\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Equal(t, []string{"a.spec.md"}, waitChange(t, w))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o644))
	tmp := filepath.Join(dir, "a.tmp")
	require.NoError(t, os.WriteFile(tmp, []byte("three\n"), 0o644))
	require.NoError(t, os.Rename(tmp, p))
	assert.Equal(t, []string{"a.spec.md"}, waitChange(t, w))

	// A later in-place write after the rename is still seen.
	require.NoError(t, os.WriteFile(p, []byte("four\n"), 0o644))
	assert.Equal(t, []string{"a.spec.md"}, waitChange(t, w))
}

// TestWatcherCloses: Close stops the goroutine and unblocks a waiting
// command.
func TestWatcherCloses(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.spec.md"), []byte("x"), 0o644))
	w := newWatcher(dir, []string{"a.spec.md"})
	done := make(chan any, 1)
	go func() { done <- w.next()() }()
	w.Close()
	select {
	case m := <-done:
		assert.Nil(t, m)
	case <-time.After(5 * time.Second):
		t.Fatal("next did not return after Close")
	}
	w.Close() // idempotent
}
