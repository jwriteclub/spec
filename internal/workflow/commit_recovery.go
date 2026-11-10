// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

const operationPath = ".spec/operation.json"
const recoveryDir = ".spec/recovery"

// RecoveryFile records planned bytes, not proof that a write completed.
type RecoveryFile struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// RecoveryIntent survives a process interruption across file writes, Git and
// receipt persistence. It is evidence of authorization, never an applied receipt.
// Acknowledgment archives it without inferring which effects occurred.
type RecoveryIntent struct {
	ID              string             `json:"id"`
	Kind            string             `json:"kind"`
	Started         time.Time          `json:"started"`
	Proposal        *proposal.Proposal `json:"proposal,omitempty"`
	Accepted        []int              `json:"accepted,omitempty"`
	PlannedApplied  []int              `json:"planned_applied,omitempty"`
	Files           []RecoveryFile     `json:"files"`
	Changes         []string           `json:"changes,omitempty"`
	Parent          string             `json:"parent,omitempty"`
	PreparedCommit  string             `json:"prepared_commit,omitempty"`
	Commit          string             `json:"commit,omitempty"`
	Acknowledged    string             `json:"acknowledged,omitempty"`
	NeedsInspection bool               `json:"needs_inspection,omitempty"`
	refUncertain    bool
	warning         string
}

// CheckRecoveryBeforeLock classifies an early recovery guard using the OS lock.
// Active writers report lock_held; only evidence reread under a free lock is an
// interrupted operation. Already-locked callers must use CheckRecovery instead.
func (w *Workflow) CheckRecoveryBeforeLock(root, id string) *Error {
	if e := w.CheckRecovery(root, id); e == nil {
		return nil
	}
	lock, err := project.AcquireLock(root, "inspect operation recovery")
	if err != nil {
		return LockErr(err)
	}
	defer func() { _ = lock.Release() }()
	return w.CheckRecovery(root, id)
}

// CheckRecovery refuses mutations while an operation is unresolved. With an ID
// it also fences that proposal after acknowledgment; revisions and regenerated
// checks cannot establish what an interrupted approval actually wrote.
func (w *Workflow) CheckRecovery(root, id string) *Error {
	r, err := readRecovery(filepath.Join(root, operationPath))
	if err != nil {
		return recoveryReadError(err)
	}
	if r != nil {
		return fail(ExitFail, "recovery_required", "An approval or commit is unfinished; wait for active work, or run `spec recover` to inspect %s, HEAD, worktree and index before continuing", operationPath)
	}
	if id == "" {
		return nil
	}
	return checkArchivedRecovery(root, id)
}

// checkArchivedRecovery is CheckRecovery's fence on proposal id alone: the
// archived evidence of an interrupted approval of it, not the active intent.
func checkArchivedRecovery(root, id string) *Error {
	r, err := archivedRecovery(root, id)
	if err != nil {
		return recoveryReadError(err)
	}
	if r != nil {
		return fail(ExitFail, "recovery_required", "Proposal %s has an unresolved interrupted approval (evidence: %s/%s.json). Acknowledgment did not certify its writes. Inspect it, explicitly reject it with `spec reject %s --why ...`, and file only the remaining work as a new proposal", id, recoveryDir, r.ID, id)
	}
	return nil
}

func recoveryReadError(err error) *Error {
	return fail(ExitFail, "recovery_required", "Cannot read interruption evidence: %v; preserve %s and %s and inspect them before continuing", err, operationPath, recoveryDir)
}

func readRecovery(path string) (*RecoveryIntent, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r RecoveryIntent
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Archive paths must never come from unvalidated on-disk input.
	raw, err := hex.DecodeString(r.ID)
	if err != nil || len(raw) != 16 || (r.Kind != "approve" && r.Kind != "commit") {
		return nil, fmt.Errorf("invalid interruption evidence in %s", path)
	}
	return &r, nil
}

func archivedRecovery(root, id string) (*RecoveryIntent, error) {
	entries, err := os.ReadDir(filepath.Join(root, recoveryDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		r, err := readRecovery(filepath.Join(root, recoveryDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if r != nil && r.Proposal != nil && r.Proposal.ID == id {
			return r, nil
		}
	}
	return nil, nil
}

func newRecovery(kind string) (*RecoveryIntent, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return &RecoveryIntent{ID: hex.EncodeToString(id[:]), Kind: kind, Started: time.Now().UTC()}, nil
}

func (w *Workflow) beginApproval(root string, p *proposal.Proposal, accepted, applied []int, outputs []edit.Output) (*RecoveryIntent, *Error) {
	r, err := newRecovery("approve")
	if err != nil {
		return nil, internalErr(err)
	}
	// Snapshot before the caller mutates its decision or checked file hashes.
	data, err := json.Marshal(p)
	if err != nil {
		return nil, internalErr(err)
	}
	if err := json.Unmarshal(data, &r.Proposal); err != nil {
		return nil, internalErr(err)
	}
	r.Accepted, r.PlannedApplied = SortedInts(accepted), SortedInts(applied)
	for _, out := range outputs {
		r.Files = append(r.Files, RecoveryFile{Path: out.Path, Before: proposal.FileHash(out.Before), After: proposal.FileHash(out.After)})
	}
	if head, err := w.Git.Run(context.Background(), root, nil, "rev-parse", "--verify", "HEAD"); err == nil {
		r.Parent = strings.TrimSpace(string(head))
	}
	if err := saveRecovery(root, r); err != nil {
		return nil, internalErr(err)
	}
	return r, nil
}

func saveRecovery(root string, r *RecoveryIntent) error {
	return writeRecovery(filepath.Join(root, operationPath), r)
}

func writeRecovery(path string, r *RecoveryIntent) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".operation-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
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
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncRecoveryDir(filepath.Dir(path))
}

func syncRecoveryDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func finishRecovery(root string) error {
	if err := os.Remove(filepath.Join(root, operationPath)); err != nil {
		return err
	}
	return syncRecoveryDir(filepath.Join(root, ".spec"))
}

// Recover inspects the durable intent; acknowledgment preserves it in an
// immutable archive and clears only the project-wide guard. It does not update
// files, the index or proposal decisions. The caller must inspect Git and the
// worktree; the old proposal must be explicitly closed and remaining work refiled.
func (w *Workflow) Recover(root string, acknowledge bool, why string) (*RecoveryIntent, *Error) {
	if acknowledge && strings.TrimSpace(why) == "" {
		return nil, usageErr("--why is required to record what you inspected; acknowledgment does not certify writes")
	}
	lock, err := project.AcquireLock(root, "spec recover")
	if err != nil {
		return nil, LockErr(err)
	}
	defer func() { _ = lock.Release() }()
	r, err := readRecovery(filepath.Join(root, operationPath))
	if err != nil {
		return nil, recoveryReadError(err)
	}
	if r == nil || !acknowledge {
		return r, nil
	}
	r.Acknowledged = strings.TrimSpace(why)
	archive := filepath.Join(root, recoveryDir, r.ID+".json")
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		return nil, internalErr(err)
	}
	// A link reserves the archive without replacing evidence if acknowledgment
	// was interrupted after archiving but before clearing the active intent.
	staged := filepath.Join(root, recoveryDir, ".ack-"+r.ID)
	if err := writeRecovery(staged, r); err != nil {
		return nil, internalErr(err)
	}
	defer os.Remove(staged)
	if err := os.Link(staged, archive); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, internalErr(err)
		}
		prior, err := readRecovery(archive)
		if err != nil || prior == nil || prior.ID != r.ID {
			return nil, recoveryReadError(fmt.Errorf("cannot verify existing recovery archive %s", archive))
		}
		r = prior
	}
	if err := syncRecoveryDir(filepath.Dir(archive)); err != nil {
		return nil, internalErr(err)
	}
	if err := finishRecovery(root); err != nil {
		return nil, internalErr(err)
	}
	return r, nil
}

// commitPreflight is deliberately also called before approval writes. The
// later Git checks and reference compare-and-swap remain necessary because
// external Git commands do not take the project lock.
func (w *Workflow) commitPreflight(root string) *Error {
	ctx := context.Background()
	for _, operation := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if _, err := w.Git.Run(ctx, root, nil, "rev-parse", "--verify", "-q", operation); err == nil {
			return fail(ExitFail, "commit_failed", "Finish the active Git merge, cherry-pick or revert before committing selected changes")
		}
	}
	if _, err := w.Git.Run(ctx, root, nil, "symbolic-ref", "-q", "HEAD"); err != nil {
		return fail(ExitFail, "commit_failed", "Selective commit requires a branch; create or switch to a branch before committing")
	}
	return nil
}
