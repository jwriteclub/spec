// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package workflow is the proposal workflow shared by the command line and
// the terminal UI: the contradiction check of changed anchors (propose,
// change), the consistency analysis, revising a hunk with feedback, and
// approving or rejecting a proposal (with the commit and its trailers).
//
// Errors are *Error values carrying the command line's exit code and error
// code, so `spec` reports them exactly as before and the UI can show the
// same message (and the same fix line).
package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/tenta-browser/polychromatic"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/spell"
)

var lg *logrus.Entry = polychromatic.GetLogger("workflow")

// Exit codes of the failures (the command line's codes).
const (
	ExitFail  = 1
	ExitCheck = 4
	ExitLock  = 5
)

// Error is a failure with the command line's exit code, its JSON error code
// and a message that says how to fix it.
type Error struct {
	Exit    int
	Code    string
	Message string
	// Lock is the holder when Code is lock_held.
	Lock *project.LockHeldError
	// Proposal is the last durable proposal when available. A stale primary
	// stays pending; a post-write error may carry a saved applied receipt.
	Proposal *proposal.Proposal
	// Written is true when source writes completed before a later failure.
	// Code recovery_required directs inspection before any further commit;
	// Trailers retain the trailer lines the intended commit needed.
	Written  bool
	Trailers []string
	// Names are the anchors a names_required refusal lists, and Argv the
	// command that files them.
	Names []string
	Argv  []string
}

func (e *Error) Error() string { return e.Message }

// fail builds an Error.
func fail(exit int, code, format string, args ...any) *Error {
	return &Error{Exit: exit, Code: code, Message: fmt.Sprintf(format, args...)}
}

// usageErr builds an exit 1 `usage` error.
func usageErr(format string, args ...any) *Error {
	return fail(ExitFail, "usage", format, args...)
}

// internalErr builds an exit 1 `internal` error for an unexpected error.
func internalErr(err error) *Error {
	return fail(ExitFail, "internal", "internal error: %v", err)
}

// Workflow holds what the operations need from their surroundings.
type Workflow struct {
	// sessionKeys privately holds process-local credentials under sessionMu.
	// Access through the locking methods; keys never enter saved config.
	sessionKeys map[string]string
	sessionMu   sync.RWMutex

	// Getenv reads an environment variable (HOME, XDG_CONFIG_HOME and the
	// providers' keys); nil means os.Getenv. It is the process environment
	// only: the user config's env_file is consulted through ProviderEnv.
	Getenv func(string) string
	// EnvFiles caches the user config's env_file for one command; nil
	// gives the Workflow its own cache.
	EnvFiles    *config.EnvFiles
	ownEnvFiles config.EnvFiles
	// Git runs git for baselines and commits.
	Git baseline.Loader
	// Now replaces time.Now for timestamps when set.
	Now func() time.Time
	// IDs, when set, is where new proposal ids come from (crypto/rand
	// otherwise): UI scenes pass a seeded source.
	IDs io.Reader
	// BeforeCommit, when set, runs after a write is planned and before it
	// is committed (tests).
	BeforeCommit func()
	// LockWait bounds how long a decision on a proposal (reject, settle,
	// a revised hunk) waits for .spec/lock; zero means DecisionLockWait.
	LockWait time.Duration
	// TuneClient, when set, adjusts every HTTP inference client (kind
	// openai) before use (tests drop the retry backoff).
	TuneClient func(*infer.Client)
	// WrapProvider and WrapGate, when set, wrap every provider and gate
	// the workflow opens, after it is configured (`spec ui --record`
	// records the calls through them).
	WrapProvider func(infer.Provider) infer.Provider
	WrapGate     func(infer.Gate) infer.Gate
	// SpellDict, when set, replaces the system word list of the spell
	// check (tests).
	SpellDict *spell.Dictionary
	// Provider, Model and Effort are --provider, --engine and --effort (""
	// for the user config's defaults).
	Provider string
	Model    string
	Effort   string
	// ModelChecker, when set, checks formal models in place of the checker
	// registered for their extension (formal.CheckerFor); tests set it.
	ModelChecker formal.Checker
}

func (w *Workflow) getenv(k string) string {
	if w.Getenv == nil {
		return os.Getenv(k)
	}
	return w.Getenv(k)
}

// wrapProvider applies WrapProvider to p.
func (w *Workflow) wrapProvider(p infer.Provider) infer.Provider {
	if w.WrapProvider == nil {
		return p
	}
	return w.WrapProvider(p)
}

// wrapGate applies WrapGate to g.
func (w *Workflow) wrapGate(g infer.Gate) infer.Gate {
	if w.WrapGate == nil {
		return g
	}
	return w.WrapGate(g)
}

// now returns the workflow's clock.
func (w *Workflow) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// stamp is now, in UTC, to the second (proposal timestamps).
func (w *Workflow) stamp() time.Time {
	return w.now().UTC().Truncate(time.Second)
}

// Store returns the proposal store of the project at root.
func (w *Workflow) Store(root string) *proposal.Store {
	st := proposal.NewStore(root)
	st.Now = w.now
	st.Rand = w.IDs
	return st
}

// DecisionLockWait is how long a decision on a proposal waits for
// .spec/lock by default: longer than an approval takes to write and commit.
const DecisionLockWait = 10 * time.Second

// decisionLock takes .spec/lock for a decision on a proposal, waiting up
// to w.LockWait for another writer (an approval writing and committing).
// Every change of a proposal's status is made under it, so the status a
// decision checks is the one it overwrites.
func (w *Workflow) decisionLock(root, command string) (*project.Lock, *Error) {
	return w.DecisionLock(context.Background(), root, command)
}

// DecisionLock is decisionLock for a caller that waits in the background
// (spec ui): a cancelled ctx stops the wait with exit 1 `cancelled`. The
// caller then makes its decision with the *Locked methods and releases
// the lock.
func (w *Workflow) DecisionLock(ctx context.Context, root, command string) (*project.Lock, *Error) {
	wait := w.LockWait
	if wait == 0 {
		wait = DecisionLockWait
	}
	lock, err := project.AcquireLockWait(ctx, root, command, wait)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fail(ExitFail, "cancelled", "cancelled while waiting for %s", project.LockFile)
		}
		return nil, LockErr(err)
	}
	return lock, nil
}

// held checks that a *Locked method's caller holds .spec/lock.
func held(lock *project.Lock) *Error {
	if lock == nil {
		return internalErr(errors.New("a decision needs .spec/lock held"))
	}
	return nil
}

// LockErr maps a failure to take .spec/lock to an Error.
func LockErr(err error) *Error {
	var held *project.LockHeldError
	if errors.As(err, &held) {
		e := fail(ExitLock, "lock_held", "another spec process holds %s: %s; wait for it to finish and retry", held.Path, held.Record.String())
		e.Lock = held
		return e
	}
	return fail(ExitFail, "write_failed", "cannot take the project lock: %v", err)
}

// WriteErr maps a failed plan commit to an Error.
func WriteErr(cmd string, err error) *Error {
	var ce *edit.ConcurrentEditError
	var we *edit.WriteError
	switch {
	case errors.As(err, &ce):
		return fail(ExitFail, "concurrent_edit", "%s changed while %s was running; nothing was written; run it again", ce.Path, cmd)
	case errors.As(err, &we):
		msg := fmt.Sprintf("writing %s failed: %v", we.Path, we.Err)
		if len(we.Restored) > 0 {
			msg += "; restored: " + strings.Join(we.Restored, ", ")
		}
		if len(we.RestoreFailed) > 0 {
			msg += "; COULD NOT restore: " + strings.Join(we.RestoreFailed, ", ")
		}
		return fail(ExitFail, "write_failed", "%s", msg)
	}
	return fail(ExitFail, "write_failed", "%v", err)
}

// LoadProposal reads proposal id.
func (w *Workflow) LoadProposal(root, id string) (*proposal.Proposal, *Error) {
	p, err := w.Store(root).Load(id)
	if err != nil {
		if errors.Is(err, proposal.ErrNotFound) {
			return nil, fail(ExitFail, "not_found", "no proposal %q (spec proposals lists them; decided ones are removed after acknowledgement or 7 days)", id)
		}
		return nil, internalErr(err)
	}
	return p, nil
}

// LoadBaseline reads a baseline. With lenient, a baseline that cannot be
// read is returned as unavailable instead of an error.
func (w *Workflow) LoadBaseline(proj *project.Project, src baseline.Source, lenient bool) (*baseline.Baseline, *Error) {
	b, err := w.Git.Load(context.Background(), proj, src)
	if err != nil {
		var bad *baseline.BadRefError
		if errors.As(err, &bad) {
			return nil, fail(ExitFail, "bad_ref", "%s", bad.Error())
		}
		if lenient {
			return &baseline.Baseline{State: baseline.StateUnavailable, Source: src, Reason: err.Error()}, nil
		}
		return nil, internalErr(err)
	}
	return b, nil
}

// NoBaseline is the error for a missing baseline.
func NoBaseline(b *baseline.Baseline) *Error {
	return fail(ExitFail, "no_baseline", "no baseline to compare against: %s (diff, ls --changed and check need a git repository and git on PATH)", b.Reason)
}

// openRoot opens the project at root.
func openRoot(root string) (*project.Project, *Error) {
	p, err := project.Open(root)
	if err != nil {
		return nil, internalErr(err)
	}
	return p, nil
}

// ByOr returns by trimmed, or the human author when it is empty.
func ByOr(by string) string {
	if strings.TrimSpace(by) == "" {
		return proposal.AuthorHuman
	}
	return strings.TrimSpace(by)
}
