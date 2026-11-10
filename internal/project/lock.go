// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// LockDir is the project-relative directory that holds the lock (created
// lazily by writers only).
const LockDir = ".spec"

// LockFile is the project-relative slash path of the writer lock.
const LockFile = LockDir + "/lock"

// UILockFile is the project-relative slash path of the lock that keeps
// `spec ui` to one instance per project. It is separate from the writer
// lock, which the UI takes only while it writes a file.
const UILockFile = LockDir + "/ui.lock"

// LockRecord is what the holder writes into the lock file after acquiring
// it. A second process that finds the lock held reads it to say who holds it.
type LockRecord struct {
	PID     int       `json:"pid"`
	Host    string    `json:"host"`
	Started time.Time `json:"started"`
	Command string    `json:"command"`
}

func (r LockRecord) String() string {
	if r.PID == 0 {
		return "held by another process"
	}
	return fmt.Sprintf("held by pid %d on %s since %s (%s)", r.PID, r.Host, r.Started.Format(time.RFC3339), r.Command)
}

// LockHeldError is returned by AcquireLock when another process holds the
// lock (CLI exit code 5). Record is what the holder wrote; it is the zero
// value when the file was empty or unreadable.
type LockHeldError struct {
	Path   string
	Record LockRecord
}

func (e *LockHeldError) Error() string {
	return fmt.Sprintf("%s is %s", e.Path, e.Record)
}

// Lock is a held writer lock. The lock file persists after Release; only
// the flock is dropped.
type Lock struct {
	f    *os.File
	path string
}

// Path returns the lock file's path.
func (l *Lock) Path() string {
	return l.path
}

// AcquireLock takes the exclusive, non-blocking lock at root/.spec/lock,
// creating the directory and file if needed, and records the holder in the
// file. command is the command line to record (for example "spec rename").
// If another process holds the lock it returns a *LockHeldError.
func AcquireLock(root, command string) (*Lock, error) {
	return acquire(root, LockFile, command)
}

// lockPoll is how often AcquireLockWait retries a held lock.
const lockPoll = 25 * time.Millisecond

// AcquireLockWait is AcquireLock that retries while another process holds
// the lock, for up to wait, before it returns the *LockHeldError. A
// cancelled ctx stops the wait with ctx.Err().
func AcquireLockWait(ctx context.Context, root, command string, wait time.Duration) (*Lock, error) {
	deadline := time.Now().Add(wait)
	for {
		l, err := AcquireLock(root, command)
		var held *LockHeldError
		if err == nil || !errors.As(err, &held) || !time.Now().Before(deadline) {
			return l, err
		}
		t := time.NewTimer(min(lockPoll, time.Until(deadline)))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// AcquireUILock takes the single-instance lock of `spec ui` at
// root/.spec/ui.lock, like AcquireLock. A second UI gets a
// *LockHeldError naming the holder.
func AcquireUILock(root, command string) (*Lock, error) {
	return acquire(root, UILockFile, command)
}

// acquire takes the lock file at the project-relative slash path rel.
func acquire(root, rel, command string) (*Lock, error) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := flockExclusive(f); err != nil {
		var held LockRecord
		if data, rerr := io.ReadAll(f); rerr == nil {
			_ = json.Unmarshal(data, &held)
		}
		_ = f.Close()
		if errors.Is(err, errLockBusy) {
			return nil, &LockHeldError{Path: p, Record: held}
		}
		return nil, err
	}
	host, _ := os.Hostname()
	rec := LockRecord{PID: os.Getpid(), Host: host, Started: time.Now().UTC().Truncate(time.Second), Command: command}
	data, err := json.Marshal(rec)
	if err == nil {
		err = f.Truncate(0)
	}
	if err == nil {
		_, err = f.WriteAt(append(data, '\n'), 0)
	}
	if err != nil {
		_ = funlock(f)
		_ = f.Close()
		return nil, err
	}
	return &Lock{f: f, path: p}, nil
}

// Release drops the lock. The file stays. Release is idempotent.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := funlock(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}
