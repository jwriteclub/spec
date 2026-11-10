// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jwriteclub/spec/internal/fsnote"
)

// FileEdit is the set of splices for one file. Snapshot is the file's bytes
// as parsed; Commit refuses to write if the file on disk no longer equals it.
type FileEdit struct {
	// Path is the project-relative slash path.
	Path     string
	Snapshot []byte
	Splices  []Splice
}

// Output is one file's before and after contents.
type Output struct {
	Path    string
	Before  []byte
	After   []byte
	Splices []Splice
}

// Plan is a verified set of file changes that has not been written yet. Its
// Outputs support `--dry-run`; Commit writes them.
type Plan struct {
	// Root is the project root that Output paths are relative to.
	Root string
	// Outputs are the changed files, sorted by path. Files whose splices
	// leave the bytes unchanged are omitted.
	Outputs []Output
}

// NewPlan applies every splice of every edit to its snapshot, in memory, and
// verifies that each splice's expected bytes are present (protocol step 2).
// Nothing is written. Edits for the same path are merged.
func NewPlan(root string, edits []FileEdit) (*Plan, error) {
	merged := map[string]*FileEdit{}
	var order []string
	for _, e := range edits {
		if m, ok := merged[e.Path]; ok {
			if !bytes.Equal(m.Snapshot, e.Snapshot) {
				return nil, fmt.Errorf("%s: two edits carry different snapshots", e.Path)
			}
			m.Splices = append(m.Splices, e.Splices...)
			continue
		}
		c := e
		c.Splices = append([]Splice(nil), e.Splices...)
		merged[e.Path] = &c
		order = append(order, e.Path)
	}
	sort.Strings(order)
	p := &Plan{Root: root}
	for _, path := range order {
		e := merged[path]
		if len(e.Splices) == 0 {
			continue
		}
		after, err := apply(path, e.Snapshot, e.Splices)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(after, e.Snapshot) {
			continue
		}
		p.Outputs = append(p.Outputs, Output{Path: path, Before: e.Snapshot, After: after, Splices: e.Splices})
	}
	return p, nil
}

// ConcurrentEditError says a target file changed after it was parsed (CLI
// error code concurrent_edit). Nothing was written.
type ConcurrentEditError struct {
	Path string
}

func (e *ConcurrentEditError) Error() string {
	return fmt.Sprintf("%s changed while the edit was being prepared; nothing was written. Re-run the command", e.Path)
}

// WriteError says writing failed part-way. Restored lists files that had
// already been replaced and were put back from the snapshot; RestoreFailed
// lists those that could not be put back (each is still the new version).
type WriteError struct {
	Path          string
	Err           error
	Restored      []string
	RestoreFailed []string
}

func (e *WriteError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "writing %s: %v", e.Path, e.Err)
	if len(e.Restored) > 0 {
		fmt.Fprintf(&b, "; restored %s to the original", strings.Join(e.Restored, ", "))
	}
	if len(e.RestoreFailed) > 0 {
		fmt.Fprintf(&b, "; COULD NOT restore %s (they hold the new version; recover them with git)", strings.Join(e.RestoreFailed, ", "))
	}
	return b.String()
}

func (e *WriteError) Unwrap() error { return e.Err }

// Test hooks.
var (
	renameFile = os.Rename
	// afterPrepare runs after every temp file is written and before the
	// targets are re-read.
	afterPrepare = func() {}
)

type staged struct {
	out  Output
	abs  string
	temp string
	mode fs.FileMode
}

// Commit writes the plan (protocol steps 3 to 5):
//
//  3. every output is written to a temp file in its target's directory, with
//     the target's permission bits;
//  4. every target is re-read and compared with its snapshot, and the call
//     fails with *ConcurrentEditError if any differs or vanished;
//  5. the temp files are renamed over the targets; if one rename fails, the
//     files already replaced are restored from the snapshot and a
//     *WriteError reports what happened.
//
// Temp files are removed on every failure path. The caller holds the project
// lock (protocol step 1).
func (p *Plan) Commit() error {
	if len(p.Outputs) == 0 {
		return nil
	}
	var sts []*staged
	cleanup := func() {
		for _, s := range sts {
			if s.temp != "" {
				_ = os.Remove(s.temp)
			}
		}
	}
	for _, out := range p.Outputs {
		s := &staged{out: out, abs: filepath.Join(p.Root, filepath.FromSlash(out.Path))}
		sts = append(sts, s)
		st, err := os.Lstat(s.abs)
		if err != nil {
			cleanup()
			if errors.Is(err, fs.ErrNotExist) {
				return &ConcurrentEditError{Path: out.Path}
			}
			return &WriteError{Path: out.Path, Err: err}
		}
		if !st.Mode().IsRegular() {
			cleanup()
			return &WriteError{Path: out.Path, Err: errors.New("not a regular file")}
		}
		s.mode = st.Mode().Perm()
		temp, err := writeTemp(s.abs, out.After, s.mode)
		if err != nil {
			cleanup()
			return &WriteError{Path: out.Path, Err: err}
		}
		s.temp = temp
	}

	afterPrepare()

	for _, s := range sts {
		cur, err := os.ReadFile(s.abs)
		if err != nil || !bytes.Equal(cur, s.out.Before) {
			cleanup()
			return &ConcurrentEditError{Path: s.out.Path}
		}
	}

	var done []*staged
	for _, s := range sts {
		if err := renameFile(s.temp, s.abs); err != nil {
			werr := &WriteError{Path: s.out.Path, Err: err}
			for _, d := range done {
				if rerr := restore(d); rerr != nil {
					werr.RestoreFailed = append(werr.RestoreFailed, d.out.Path)
				} else {
					werr.Restored = append(werr.Restored, d.out.Path)
					fsnote.Wrote(d.abs, d.out.Before)
				}
			}
			cleanup()
			return werr
		}
		s.temp = "" // renamed away
		done = append(done, s)
		fsnote.Wrote(s.abs, s.out.After)
	}
	for _, s := range done {
		syncDir(filepath.Dir(s.abs))
	}
	return nil
}

// restore writes the snapshot back over the target via temp + rename.
func restore(s *staged) error {
	temp, err := writeTemp(s.abs, s.out.Before, s.mode)
	if err != nil {
		return err
	}
	if err := renameFile(temp, s.abs); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

// writeTemp writes data to a new file in target's directory with mode, and
// returns its path. The data is synced to disk before returning.
func writeTemp(target string, data []byte, mode fs.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(target), ".spec-edit-*.tmp")
	if err != nil {
		return "", err
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(name)
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// syncDir flushes a directory entry change; failures are ignored because
// some filesystems do not support it.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
