// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLockAcquireRecordsHolder(t *testing.T) {
	root := t.TempDir()
	_, err := os.Stat(filepath.Join(root, LockDir))
	require.True(t, os.IsNotExist(err), "read-only commands never create .spec/")

	l, err := AcquireLock(root, "spec rename A B")
	require.NoError(t, err)
	data, err := os.ReadFile(l.Path())
	require.NoError(t, err)
	var rec LockRecord
	require.NoError(t, json.Unmarshal(data, &rec))
	assert.Equal(t, os.Getpid(), rec.PID)
	assert.Equal(t, "spec rename A B", rec.Command)
	assert.False(t, rec.Started.IsZero())
	assert.NotEmpty(t, rec.Host)
	assert.Equal(t, filepath.Join(root, ".spec", "lock"), l.Path())
	require.NoError(t, l.Release())
}

func TestLockHeldReportsRecord(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(root, "first")
	require.NoError(t, err)
	defer l.Release()

	_, err = AcquireLock(root, "second")
	var held *LockHeldError
	require.ErrorAs(t, err, &held)
	assert.Equal(t, os.Getpid(), held.Record.PID)
	assert.Equal(t, "first", held.Record.Command)
	assert.Contains(t, held.Error(), "first")
}

func TestLockReleaseKeepsFileAndAllowsReacquire(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(root, "first")
	require.NoError(t, err)
	require.NoError(t, l.Release())
	require.NoError(t, l.Release(), "release is idempotent")
	_, err = os.Stat(l.Path())
	require.NoError(t, err, "the lock file persists")

	l2, err := AcquireLock(root, "a much longer command line than before so truncation is exercised")
	require.NoError(t, err)
	require.NoError(t, l2.Release())
	l3, err := AcquireLock(root, "x")
	require.NoError(t, err)
	data, err := os.ReadFile(l3.Path())
	require.NoError(t, err)
	var rec LockRecord
	require.NoError(t, json.Unmarshal(data, &rec), "the record is rewritten, not appended")
	assert.Equal(t, "x", rec.Command)
	require.NoError(t, l3.Release())
}

func TestLockHeldWithUnreadableRecord(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(root, "first")
	require.NoError(t, err)
	defer l.Release()
	require.NoError(t, os.WriteFile(l.Path(), []byte("garbage"), 0o644))
	_, err = AcquireLock(root, "second")
	var held *LockHeldError
	require.ErrorAs(t, err, &held)
	assert.Equal(t, LockRecord{}, held.Record)
	assert.Contains(t, held.Error(), "held by another process")
}

func TestLockWaitBoundedThenTakes(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(root, "first")
	require.NoError(t, err)

	_, err = AcquireLockWait(context.Background(), root, "second", 30*time.Millisecond)
	var held *LockHeldError
	require.ErrorAs(t, err, &held, "still held when the wait ends")
	assert.Equal(t, "first", held.Record.Command)

	require.NoError(t, l.Release())
	l2, err := AcquireLockWait(context.Background(), root, "second", time.Nanosecond)
	require.NoError(t, err, "a free lock is taken on the first try")
	require.NoError(t, l2.Release())
}

// A cancelled wait stops at once, without the lock (spec ui's ^C).
func TestLockWaitCancelled(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(root, "first")
	require.NoError(t, err)
	defer l.Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := AcquireLockWait(ctx, root, "second", time.Hour)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, got)
}
