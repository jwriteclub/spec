// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// A check owns an OS-held lock for its lifetime. The random token fences late
// writes; PID/host metadata is diagnostic only. Never unlink these lock files:
// doing so could let two workers lock different inodes at the same path.
func checkOwnerRoot(root, token string) string {
	return filepath.Join(root, ".spec", "checks", "owners", token)
}
func newCheckOwner(root string) (string, *project.Lock, *Error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", nil, internalErr(err)
	}
	name := hex.EncodeToString(token[:])
	lock, err := project.AcquireLock(checkOwnerRoot(root, name), "required check")
	if err != nil {
		return "", nil, internalErr(err)
	}
	return name, lock, nil
}

var errInvalidCheckOwner = errors.New("invalid check owner token")

func ownerLock(root, token string) (*project.Lock, error) {
	// Legacy records have no provable owner and are conservatively interrupted.
	if token == "" {
		return nil, nil
	}
	if len(token) != 32 {
		return nil, errInvalidCheckOwner
	}
	if _, err := hex.DecodeString(token); err != nil {
		return nil, errInvalidCheckOwner
	}
	return project.AcquireLock(checkOwnerRoot(root, token), "recover required check")
}

type checkCheckpoint struct {
	Seen    *proposal.Proposal `json:"seen"`
	State   string             `json:"state"`
	Message string             `json:"message"`
	Result  *proposal.Proposal `json:"result,omitempty"`
	Recheck bool               `json:"recheck,omitempty"`
}

func checkpointPath(root, id, token string) string {
	return filepath.Join(root, ".spec", "checks", "results", id+"-"+token+".json")
}
func saveCheckpoint(root string, cp checkCheckpoint) error {
	path := checkpointPath(root, cp.Seen.ID, cp.Seen.CheckOwner)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".result-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// checkCompletionLock deliberately outlives interactive decision timeouts and
// the model request context. A ready result is checkpointed before waiting.
func (w *Workflow) checkCompletionLock(root string) (*project.Lock, *Error) {
	for {
		lock, err := project.AcquireLockWait(context.Background(), root, "finish required check", time.Second)
		if err == nil {
			return lock, nil
		}
		var busy *project.LockHeldError
		if !errors.As(err, &busy) {
			return nil, LockErr(err)
		}
	}
}

// ReconcileChecks discovers dead owners even when no proposal file changed.
// It never waits for a live owner or a project writer, and never calls a model.
// Completed checkpoints are installed only under the original revision/token
// and full disk snapshot guards. Buffer-only checkpoints become stale on restart.
func (w *Workflow) ReconcileChecks(root string) *Error {
	ps, err := w.Store(root).List()
	if err != nil {
		return internalErr(err)
	}
	for _, p := range ps {
		if p.Status != proposal.StatusPending || p.CheckState != "queued" && p.CheckState != "running" {
			continue
		}
		owner, err := ownerLock(root, p.CheckOwner)
		var busy *project.LockHeldError
		if errors.As(err, &busy) {
			continue
		}
		invalidOwner := errors.Is(err, errInvalidCheckOwner)
		if err != nil && !invalidOwner {
			return internalErr(err)
		}
		lock, err := project.AcquireLock(root, "recover required check")
		if err != nil {
			if owner != nil {
				_ = owner.Release()
			}
			if errors.As(err, &busy) {
				continue
			}
			return internalErr(err)
		}
		cur, e := w.LoadProposal(root, p.ID)
		if e == nil && cur.Status == proposal.StatusPending && cur.Revision == p.Revision && cur.CheckOwner == p.CheckOwner {
			cp := checkCheckpoint{Seen: cur, State: "failed", Message: "Check interrupted; retry or recheck this proposal"}
			if invalidOwner {
				cp.Message = "Check owner record is invalid; recheck this proposal"
			} else {
				saved, err := loadCheckpoint(root, cur)
				if err != nil {
					_ = lock.Release()
					if owner != nil {
						_ = owner.Release()
					}
					return internalErr(err)
				}
				if saved != nil {
					cp = *saved
				}
			}
			_, e = w.finishCheckLocked(root, cp, nil)
		}
		_ = lock.Release()
		if owner != nil {
			_ = owner.Release()
		}
		if e != nil && e.Code != "stale" && e.Code != "changed" {
			return e
		}
	}
	return nil
}

func (w *Workflow) finishCheckLocked(root string, cp checkCheckpoint, overlay map[string][]byte) (*proposal.Proposal, *Error) {
	cur, e := w.LoadProposal(root, cp.Seen.ID)
	if e != nil {
		return cp.Seen, e
	}
	if cur.Status != proposal.StatusPending || cur.Revision != cp.Seen.Revision || cur.CheckOwner != cp.Seen.CheckOwner {
		removeCheckpoint(root, cp.Seen)
		return cur, fail(ExitFail, "changed", "proposal %s changed while checking; result discarded", cur.ID)
	}
	var snapshotErr *Error
	if cp.State == "complete" {
		loaded, err := project.Load(root, overlay)
		if err != nil || !sameSnapshot(cur.CheckFiles, loaded.Spec) {
			cp.State, cp.Message, cp.Result = "stale", "Source files changed while checking; recheck the proposal", nil
			snapshotErr = fail(ExitFail, "stale", "%s", cp.Message)
		}
	}
	if cp.Recheck && cp.State == "complete" {
		cur.StalePrimary = nil
	}
	cur.CheckState, cur.CheckMessage, cur.CheckOwner = cp.State, cp.Message, ""
	if cp.Result != nil {
		cur.Hunks, cur.Notes, cur.Questions = cp.Result.Hunks, cp.Result.Notes, cp.Result.Questions
		cur.Summary, cur.Inference, cur.Base = cp.Result.Summary, cp.Result.Inference, cp.Result.Base
	}
	if err := w.Store(root).Save(cur); err != nil {
		return cp.Seen, internalErr(err)
	}
	removeCheckpoint(root, cp.Seen)
	return cur, snapshotErr
}

func removeCheckpoint(root string, seen *proposal.Proposal) {
	if len(seen.CheckOwner) != 32 {
		return
	}
	if _, err := hex.DecodeString(seen.CheckOwner); err != nil {
		return
	}
	_ = os.Remove(checkpointPath(root, seen.ID, seen.CheckOwner))
}

// loadCheckpoint requires the exact attempt and revision. Malformed data is
// made an explicit failure; I/O errors leave the result intact for a later read.
func loadCheckpoint(root string, cur *proposal.Proposal) (*checkCheckpoint, error) {
	data, err := os.ReadFile(checkpointPath(root, cur.ID, cur.CheckOwner))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var saved checkCheckpoint
	if json.Unmarshal(data, &saved) == nil && saved.Seen != nil && saved.Seen.ID == cur.ID && saved.Seen.Revision == cur.Revision && saved.Seen.CheckOwner == cur.CheckOwner && (saved.State == "failed" || saved.State == "stale" || saved.State == "complete" && saved.Result != nil) {
		return &saved, nil
	}
	return &checkCheckpoint{Seen: cur, State: "failed", Message: "Check result record is invalid; recheck this proposal"}, nil
}
