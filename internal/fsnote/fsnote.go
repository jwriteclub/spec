// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package fsnote is a ledger of this process's own writes to project
// files: the writers (spec file saves, edit plans, the proposal store)
// report each file they put in place or removed, and an observer (the
// session recorder of `spec ui --record`) hears them, so it can tell the
// UI's own writes from another process's. With no observer every call is
// a no-op.
package fsnote

import (
	"sync"
	"sync/atomic"
)

// Change is one write or removal of a file.
type Change struct {
	// Path is the file's absolute path.
	Path string
	// Data is the file's new content (nil for a removal).
	Data []byte
	// Removed is true when the file was removed.
	Removed bool
}

var (
	mu       sync.Mutex
	observer func(Change)
	// quiet counts the Quiet calls running.
	quiet atomic.Int32
)

// SetObserver makes fn hear every change from now on, and returns the
// function that puts the previous observer back. fn runs on the writer's
// goroutine and must not block.
func SetObserver(fn func(Change)) (restore func()) {
	mu.Lock()
	prev := observer
	observer = fn
	mu.Unlock()
	return func() {
		mu.Lock()
		observer = prev
		mu.Unlock()
	}
}

// Wrote reports that the file at abs now holds data.
func Wrote(abs string, data []byte) {
	notify(Change{Path: abs, Data: data})
}

// Removed reports that the file at abs was removed.
func Removed(abs string) {
	notify(Change{Path: abs, Removed: true})
}

// Quiet runs fn with reports dropped: tests use it to write as another
// process would (an agent's `spec propose`) from within the process.
func Quiet(fn func()) {
	quiet.Add(1)
	defer quiet.Add(-1)
	fn()
}

func notify(c Change) {
	if quiet.Load() > 0 {
		return
	}
	mu.Lock()
	fn := observer
	mu.Unlock()
	if fn != nil {
		fn(c)
	}
}
