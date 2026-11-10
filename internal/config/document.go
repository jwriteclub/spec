// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Document is an independent staged TOML file. Values retain unedited tables,
// including unknown keys. Validation belongs to its schema, never another scope.
// Saving preserves source outside edited expressions; unchanged documents are never rewritten.
type Document struct {
	Path      string
	Values    map[string]any
	source    []byte
	exists    bool
	original  map[string]any
	Validate  func([]byte) []string
	LoadError error
}

// OpenDocument reads only the explicit destination. Missing files stay absent
// until Save. A malformed file remains blocked, never a blank replacement form.
func OpenDocument(path string, validate func([]byte) []string) *Document {
	src, err := os.ReadFile(path)
	exists := err == nil
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	d := NewDocument(path, src, exists, validate)
	if err != nil {
		d.LoadError = err
	}
	return d
}

// NewDocument constructs a draft from an already-loaded snapshot (also fixtures).
func NewDocument(path string, src []byte, exists bool, validate func([]byte) []string) *Document {
	d := &Document{Path: path, source: bytes.Clone(src), exists: exists, Validate: validate, Values: map[string]any{}}
	if err := toml.Unmarshal(src, &d.Values); err != nil {
		d.LoadError = errors.New("invalid TOML; repair the source before editing settings")
	}
	d.original = cloneTable(d.Values)
	return d
}
func cloneTable(m map[string]any) map[string]any {
	b, _ := toml.Marshal(m)
	out := map[string]any{}
	_ = toml.Unmarshal(b, &out)
	return out
}

// Get uses path elements, so quoted provider names containing dots are safe.
func (d *Document) Get(path ...string) (any, bool) {
	var v any = d.Values
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = m[k]
		if !ok {
			return nil, false
		}
	}
	return v, true
}

// Set stages a value; nil removes the key (Use default).
func (d *Document) Set(value any, path ...string) {
	if len(path) == 0 {
		return
	}
	m := d.Values
	for _, k := range path[:len(path)-1] {
		n, ok := m[k].(map[string]any)
		if !ok {
			n = map[string]any{}
			m[k] = n
		}
		m = n
	}
	if value == nil {
		delete(m, path[len(path)-1])
	} else {
		m[path[len(path)-1]] = value
	}
}

// Changes lists changed field paths; deleting a whole table counts once.
func (d *Document) Changes() []string {
	var out []string
	var walk func(map[string]any, map[string]any, string)
	walk = func(a, b map[string]any, p string) {
		keys := map[string]bool{}
		for k := range a {
			keys[k] = true
		}
		for k := range b {
			keys[k] = true
		}
		for k := range keys {
			x, xok := a[k]
			y, yok := b[k]
			name := k
			if p != "" {
				name = p + "." + k
			}
			if xok && yok {
				xm, xmOK := x.(map[string]any)
				ym, ymOK := y.(map[string]any)
				if xmOK && ymOK {
					walk(xm, ym, name)
					continue
				}
			}
			if !sameMeaning(x, y) || xok != yok {
				out = append(out, name)
			}
		}
	}
	walk(d.original, d.Values, "")
	sort.Strings(out)
	return out
}
func (d *Document) Revert()                { d.Values = cloneTable(d.original) }
func (d *Document) Bytes() ([]byte, error) { return d.sourceBytes() }
func (d *Document) Problems() []string {
	if d.LoadError != nil {
		return []string{d.LoadError.Error()}
	}
	b, err := d.Bytes()
	if err != nil {
		return []string{err.Error()}
	}
	if d.Validate != nil {
		return d.Validate(b)
	}
	return nil
}

// Save validates, refuses changed-on-disk snapshots/symlinks, and atomically
// replaces one file, retaining its permissions. Failure retains the draft.
func (d *Document) Save() error {
	if len(d.Changes()) == 0 {
		return nil
	}
	if ps := d.Problems(); len(ps) > 0 {
		return errors.New(strings.Join(ps, "; "))
	}
	if d.Path == "" {
		return errors.New("configuration path is unavailable")
	}
	mode := fs.FileMode(0600)
	st, err := os.Lstat(d.Path)
	if err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return errors.New("configuration destination must be a regular file")
		}
		mode = st.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	disk, err := os.ReadFile(d.Path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if exists != d.exists || !bytes.Equal(disk, d.source) {
		return errors.New("config changed on disk; reload and review before saving")
	}
	b, err := d.Bytes()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(d.Path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(d.Path), ".spec-config-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	// Check again after staging to avoid replacing a concurrent settings edit.
	disk, err = os.ReadFile(d.Path)
	exists = err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if exists != d.exists || !bytes.Equal(disk, d.source) {
		return errors.New("config changed on disk; settings remain unsaved")
	}
	if err = os.Rename(tmp, d.Path); err != nil {
		return err
	}
	d.source = bytes.Clone(b)
	d.exists = true
	d.original = cloneTable(d.Values)
	return nil
}

// Restore stages the loaded value of one field, including its absence.
func (d *Document) Restore(path ...string) {
	var v any = d.original
	for _, key := range path {
		m, ok := v.(map[string]any)
		if !ok {
			d.Set(nil, path...)
			return
		}
		v, ok = m[key]
		if !ok {
			d.Set(nil, path...)
			return
		}
	}
	if m, ok := v.(map[string]any); ok {
		v = cloneTable(m)
	}
	d.Set(v, path...)
}

func sameMeaning(a, b any) bool {
	left, le := toml.Marshal(map[string]any{"value": a})
	right, re := toml.Marshal(map[string]any{"value": b})
	return le == nil && re == nil && bytes.Equal(left, right)
}

// ChangeDetail retains path elements and before/after values for a concrete
// save review, including provider names that themselves contain dots.
type ChangeDetail struct {
	Path                []string
	Before, After       any
	BeforeSet, AfterSet bool
}

func (d *Document) Review() []ChangeDetail {
	var out []ChangeDetail
	var walk func(map[string]any, map[string]any, []string)
	walk = func(a, b map[string]any, p []string) {
		keys := map[string]bool{}
		for k := range a {
			keys[k] = true
		}
		for k := range b {
			keys[k] = true
		}
		for k := range keys {
			x, xok := a[k]
			y, yok := b[k]
			path := append(append([]string{}, p...), k)
			if xm, ok := x.(map[string]any); ok && xok && yok {
				if ym, ok := y.(map[string]any); ok {
					walk(xm, ym, path)
					continue
				}
			}
			if xok != yok || !sameMeaning(x, y) {
				out = append(out, ChangeDetail{Path: path, Before: x, After: y, BeforeSet: xok, AfterSet: yok})
			}
		}
	}
	walk(d.original, d.Values, nil)
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i].Path, ".") < strings.Join(out[j].Path, ".") })
	return out
}
