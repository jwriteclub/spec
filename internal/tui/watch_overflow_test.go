// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// The test sends on fsnotify's Errors channel, which its backend closes on Close.
// Close orders the two through a pipe, which the race detector cannot see.
//go:build !race

package tui

import (
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
)

// TestWatcherOverflowLooksAtContent: when the kernel drops events the watcher looks
// at every known file's content too, since a re-glob compares only the lists.
func TestWatcherOverflowLooksAtContent(t *testing.T) {
	root := discoverProject(t, "")
	writeFile(t, root, "other.spec.md", discoverLater)
	var fw *fsnotify.Watcher
	got := make(chan struct{})
	w := openWatcher(watchConfig{root: root, paths: []string{"main.spec.md", "other.spec.md"}, addWatch: func(f *fsnotify.Watcher, _ string) error {
		// No real watch: the write below is an event that never comes.
		if fw == nil {
			fw = f
			close(got)
		}
		return nil
	}})
	defer w.Close()
	<-got
	writeFile(t, root, "other.spec.md", discoverLater+"\nEdited.\n")
	select {
	case fw.Errors <- fsnotify.ErrEventOverflow:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher is not reading errors")
	}
	assert.Equal(t, []string{"main.spec.md", "other.spec.md"}, waitChange(t, w))
}
