// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package proposal

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
	"github.com/tenta-browser/polychromatic"

	"github.com/jwriteclub/spec/internal/fsnote"
)

var lg *logrus.Entry = polychromatic.GetLogger("proposal")

// Dir is the project-relative directory of the proposal files.
const Dir = ".spec/proposals"

// idAlphabet is lowercase base32.
const idAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// idLen is the length of a new id (ids of 4 to 6 characters are valid).
const idLen = 5

// ErrNotFound says no proposal has the id.
var ErrNotFound = errors.New("no such proposal")

// ErrObserveLater is what a WaitObserved observer returns when the batch
// cannot be judged yet (another writer is mid-operation): the wait goes on
// and reads the proposals again at the next event or poll.
var ErrObserveLater = errors.New("observe the proposals again later")

// ValidID reports whether id is 4 to 6 lowercase base32 characters.
func ValidID(id string) bool {
	if len(id) < 4 || len(id) > 6 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if !strings.ContainsRune(idAlphabet, rune(id[i])) {
			return false
		}
	}
	return true
}

// Store is the proposal directory of one project.
type Store struct {
	dir string
	// Now returns the current time; tests replace it.
	Now func() time.Time
	// Rand is where new ids come from; nil is crypto/rand. UI scenes give
	// a seeded source so the ids repeat from run to run.
	Rand io.Reader
}

// NewStore returns the store of the project at root. Nothing is created
// until a proposal is written.
func NewStore(root string) *Store {
	return &Store{dir: filepath.Join(root, filepath.FromSlash(Dir)), Now: time.Now}
}

// Path returns the file of proposal id.
func (s *Store) Path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// RelPath returns the project-relative slash path of proposal id.
func RelPath(id string) string {
	return Dir + "/" + id + ".json"
}

func newID(r io.Reader) (string, error) {
	b := make([]byte, idLen)
	if r == nil {
		r = rand.Reader
	}
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
	}
	return string(b), nil
}

// Create gives p a fresh id and writes it. The id is reserved with a hard
// link, so two processes never share one.
func (s *Store) Create(p *Proposal) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	if p.Created.IsZero() {
		p.Created = s.Now().UTC().Truncate(time.Second)
	}
	if p.Status == "" {
		p.Status = StatusPending
	}
	for try := 0; try < 32; try++ {
		id, err := newID(s.Rand)
		if err != nil {
			return err
		}
		p.ID = id
		p.Revision = 1
		tmp, data, err := s.writeTemp(p)
		if err != nil {
			return err
		}
		err = os.Link(tmp, s.Path(id))
		_ = os.Remove(tmp)
		if err == nil {
			fsnote.Wrote(s.Path(id), data)
			return nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return errors.New("could not find a free proposal id")
}

// Save writes p atomically (temp file and rename).
func (s *Store) Save(p *Proposal) error {
	if !ValidID(p.ID) {
		return fmt.Errorf("invalid proposal id %q", p.ID)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	candidate, err := p.Clone()
	if err != nil {
		return err
	}
	candidate.Revision++
	tmp, data, err := s.writeTemp(candidate)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, s.Path(p.ID)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fsnote.Wrote(s.Path(p.ID), data)
	// Publish only Save-owned mutations. Replacing the whole struct would
	// rewrite immutable identity fields read by waiters and replace caller aliases.
	p.Revision = candidate.Revision
	p.Normalize()
	return nil
}

// writeTemp writes p to a temp file in the store and returns its name and
// bytes.
func (s *Store) writeTemp(p *Proposal) (string, []byte, error) {
	p.Normalize()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp(s.dir, ".tmp-*.json")
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", nil, err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		_ = os.Remove(name)
		return "", nil, err
	}
	return name, data, nil
}

// Load reads proposal id. A missing file is ErrNotFound.
func (s *Store) Load(id string) (*Proposal, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	return loadFile(s.Path(id), RelPath(id))
}

// loadFile reads a proposal file; name is how errors call it.
func loadFile(path string, name string) (*Proposal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var p Proposal
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	p.Normalize()
	return &p, nil
}

// Remove deletes proposal id; a missing file is ErrNotFound.
func (s *Store) Remove(id string) error {
	if !ValidID(id) {
		return ErrNotFound
	}
	err := os.Remove(s.Path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	if err == nil {
		fsnote.Removed(s.Path(id))
	}
	return err
}

// ackedDir holds acknowledged receipts that still explain text in the working
// tree: see Acknowledge. It is a subdirectory, so List and Load do not see it.
const ackedDir = "acked"

// Acknowledge ends decided proposal p's receipt: Load and List no longer find
// it. A receipt whose hunks were applied and not committed by the approval
// moves to .spec/proposals/acked, for Applied: the applied text is still
// uncommitted, as far as the store knows, and nothing else says where it came
// from. Any other receipt is deleted. A missing file is ErrNotFound.
func (s *Store) Acknowledge(p *Proposal) error {
	if p.Decision == nil || len(p.Decision.Applied) == 0 || p.Decision.Commit != "" {
		return s.Remove(p.ID)
	}
	if !ValidID(p.ID) {
		return ErrNotFound
	}
	if err := os.MkdirAll(filepath.Join(s.dir, ackedDir), 0o755); err != nil {
		return err
	}
	err := os.Rename(s.Path(p.ID), filepath.Join(s.dir, ackedDir, p.ID+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// Applied returns every proposal with applied hunks, oldest decision first:
// receipts, and acknowledged receipts younger than ReceiptTTL (older ones are
// removed). Unreadable files, and an unreadable acknowledged directory, are
// skipped with a warning: the rest is still evidence.
func (s *Store) Applied() ([]*Proposal, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	return appliedOf(append(all, s.ackedReceipts()...)), nil
}

// Evidence returns every proposal, then the applied acknowledged receipts the
// list does not hold: what a check can vouch for. It reads the directory once,
// where List followed by Applied would read it twice.
func (s *Store) Evidence() ([]*Proposal, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, p := range appliedOf(s.ackedReceipts()) {
		if !slices.ContainsFunc(all, func(q *Proposal) bool { return q.ID == p.ID }) {
			all = append(all, p)
		}
	}
	return all, nil
}

// appliedOf keeps the proposals with applied hunks, oldest decision first.
func appliedOf(all []*Proposal) []*Proposal {
	out := []*Proposal{}
	for _, p := range all {
		if p.Decision != nil && len(p.Decision.Applied) > 0 {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Decision.At.Before(out[j].Decision.At) })
	return out
}

// ackedReceipts reads the acknowledged receipts younger than ReceiptTTL,
// removing older ones. Unreadable ones are skipped with a warning.
func (s *Store) ackedReceipts() []*Proposal {
	var out []*Proposal
	dir := filepath.Join(s.dir, ackedDir)
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		lg.WithError(err).Warn("cannot read all acknowledged receipts")
	}
	now := s.Now()
	for _, e := range ents {
		path := filepath.Join(dir, e.Name())
		p, err := loadFile(path, Dir+"/"+ackedDir+"/"+e.Name())
		if err != nil {
			lg.WithError(err).Warn("skipping unreadable acknowledged receipt")
			continue
		}
		if p.Expired(now) {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				lg.WithError(err).Warn("cannot remove expired acknowledged receipt")
			}
			continue
		}
		out = append(out, p)
	}
	return out
}

// List returns every proposal, oldest first, after removing expired
// receipts. Unreadable files are skipped with a warning.
func (s *Store) List() ([]*Proposal, error) {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []*Proposal{}, nil
		}
		return nil, err
	}
	out := []*Proposal{}
	now := s.Now()
	for _, e := range ents {
		name := e.Name()
		id := strings.TrimSuffix(name, ".json")
		if e.IsDir() || id == name || !ValidID(id) {
			continue
		}
		p, err := s.Load(id)
		if err != nil {
			lg.WithError(err).Warn("skipping unreadable proposal")
			continue
		}
		if p.Expired(now) {
			if err := s.Remove(id); err != nil && !errors.Is(err, ErrNotFound) {
				lg.WithError(err).Warn("cannot remove expired receipt")
			}
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Prune removes expired receipts.
func (s *Store) Prune() error {
	_, err := s.List()
	return err
}

// Wait blocks until every proposal in ids is decided, any pending primary
// has a durable stale marker, ctx ends, or one
// vanishes. It watches the directory with fsnotify and also polls every
// poll interval (the fallback where events are unavailable). It returns the
// proposals as last read; on ctx's end the error is ctx.Err().
func (s *Store) Wait(ctx context.Context, ids []string, poll time.Duration) ([]*Proposal, error) {
	return s.WaitObserved(ctx, ids, poll, nil)
}

// WaitObserved invokes observe on each freshly read batch before evaluating its
// outcome. The observer may replace entries with their current durable records,
// or return ErrObserveLater to leave the batch unjudged until the next read.
// Any pending stale primary ends the wait, even if other IDs remain pending.
func (s *Store) WaitObserved(ctx context.Context, ids []string, poll time.Duration, observe func([]*Proposal) error) ([]*Proposal, error) {
	if poll <= 0 {
		poll = time.Second
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, err
	}
	var events <-chan fsnotify.Event
	var errs <-chan error
	if w, err := fsnotify.NewWatcher(); err == nil {
		defer func() { _ = w.Close() }()
		if err := w.Add(s.dir); err == nil {
			events, errs = w.Events, w.Errors
		} else {
			lg.WithError(err).Debug("fsnotify unavailable; polling")
		}
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		ps := make([]*Proposal, 0, len(ids))
		for _, id := range ids {
			p, err := s.Load(id)
			if err != nil {
				return nil, err
			}
			ps = append(ps, p)
		}
		later := false
		if observe != nil {
			if err := observe(ps); errors.Is(err, ErrObserveLater) {
				later = true
			} else if err != nil {
				return ps, err
			}
		}
		done, stale := true, false
		for _, p := range ps {
			if !p.Decided() {
				done = false
			}
			stale = stale || p.Status == StatusPending && p.StalePrimary != nil
		}
		if !later && (done || stale) {
			return ps, nil
		}
		select {
		case <-ctx.Done():
			return ps, ctx.Err()
		case <-events:
		case err := <-errs:
			lg.WithError(err).Debug("fsnotify error; still polling")
		case <-tick.C:
		}
	}
}
