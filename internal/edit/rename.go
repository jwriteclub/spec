// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package edit

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// Rename errors. PlanRename wraps them with a message saying what to do.
var (
	// ErrInvalidName: the new name does not satisfy the name grammar.
	ErrInvalidName = errors.New("invalid name")
	// ErrNameExists: the new name is already defined (or equals the old).
	ErrNameExists = errors.New("name already exists")
	// ErrNotFound: no anchor has the old name.
	ErrNotFound = errors.New("no such anchor")
	// ErrAmbiguous: the old name is defined more than once.
	ErrAmbiguous = errors.New("name is defined more than once")
)

// LeftAlone is an occurrence of the old name inside an opaque region (code,
// HTML, link destination, front matter), or in a formal model outside a tag,
// that rename does not touch.
type LeftAlone struct {
	File  string
	Range pos.Range // "$name", sigil included
}

// TagEdit is one rewritten $Name in a formal model's tag.
type TagEdit struct {
	File  string
	Range pos.Range // "$name", sigil included, before the rename
}

// SkippedFile is a spec file or model rename could not rewrite.
type SkippedFile struct {
	File string
	// Reason says why, e.g. "not valid UTF-8".
	Reason string
	// MayReference is true when the file's bytes contain the old name, so
	// it may hold references that were not rewritten.
	MayReference bool
}

// RenameResult is a planned rename.
type RenameResult struct {
	Old, New string // without the $ sigil
	Plan     *Plan
	// Definitions is 1; References counts every other rewritten token
	// (references in bodies, prose and titles).
	Definitions int
	References  int
	// Tags lists the rewritten tokens in formal models' tags and their quotes.
	Tags []TagEdit
	// PerFile counts rewritten tokens per project-relative path.
	PerFile map[string]int
	// LeftAlone lists "$Old" occurrences in opaque regions, in file order.
	LeftAlone []LeftAlone
	// Skipped lists the spec files that were not parsed (invalid UTF-8),
	// in file order.
	Skipped []SkippedFile
}

// Tokens is the total number of rewritten tokens.
func (r *RenameResult) Tokens() int { return r.Definitions + r.References + len(r.Tags) }

// PlanRename plans renaming anchor old to new (names with or without the $
// sigil) across spec: the label, every reference, and references inside
// titles. It matches whole names only, so renaming Frame never touches
// $Frame.Timing. models are the project's formal models: their tags change
// too, quotes included, since a quote copies the body being renamed. spec and
// models must have been built from the files as they are on disk (not from
// unsaved buffers) so snapshots match what Commit re-reads.
//
// It fails with a wrapped ErrNotFound, ErrAmbiguous, ErrInvalidName or
// ErrNameExists, writing nothing.
func PlanRename(root string, spec *mdspec.Spec, old, new string, models ...*formal.File) (*RenameResult, error) {
	old = strings.TrimPrefix(old, "$")
	new = strings.TrimPrefix(new, "$")
	if !mdspec.ValidName(new) {
		return nil, fmt.Errorf("%w: $%s is not a valid name; a name is dot-separated segments that start with a capital letter and contain only letters and digits, with at least one lowercase letter (for example Frame.Timing)", ErrInvalidName, new)
	}
	switch as := spec.Lookup(old); len(as) {
	case 0:
		return nil, fmt.Errorf("%w: $%s is not defined in the project", ErrNotFound, old)
	case 1:
	default:
		return nil, fmt.Errorf("%w: $%s has %d definitions; fix the duplicates first (spec lint)", ErrAmbiguous, old, len(as))
	}
	if old == new {
		return nil, fmt.Errorf("%w: the new name is the same as the old name", ErrNameExists)
	}
	if len(spec.Lookup(new)) > 0 {
		return nil, fmt.Errorf("%w: $%s is already defined; renaming onto it would merge two anchors", ErrNameExists, new)
	}

	res := &RenameResult{Old: old, New: new, PerFile: map[string]int{}}
	var edits []FileEdit
	for _, f := range spec.Files {
		if !f.ValidUTF8 {
			res.Skipped = append(res.Skipped, SkippedFile{
				File: f.Path, Reason: "not valid UTF-8", MayReference: bytes.Contains(f.Source(), []byte(old)),
			})
			continue
		}
		var splices []Splice
		for _, o := range f.Occurrences {
			if o.Name != old {
				continue
			}
			splices = append(splices, Splice{Range: o.NameRange(), Old: old, New: new})
			if o.Role == mdspec.RoleDefinition {
				res.Definitions++
			} else {
				res.References++
			}
		}
		for _, r := range f.OpaqueMentions(old) {
			res.LeftAlone = append(res.LeftAlone, LeftAlone{File: f.Path, Range: r})
		}
		if len(splices) == 0 {
			continue
		}
		res.PerFile[f.Path] = len(splices)
		edits = append(edits, FileEdit{Path: f.Path, Snapshot: f.Source(), Splices: splices})
	}
	for _, f := range models {
		if e, ok := res.planModel(f); ok {
			edits = append(edits, e)
		}
	}
	plan, err := NewPlan(root, edits)
	if err != nil {
		return nil, err
	}
	res.Plan = plan
	return res, nil
}

// planModel rewrites the old name in one model's tags and records the rest.
func (r *RenameResult) planModel(f *formal.File) (FileEdit, bool) {
	if !f.ValidUTF8 {
		if bytes.Contains(f.Source(), []byte(r.Old)) {
			r.Skipped = append(r.Skipped, SkippedFile{File: f.Path, Reason: "not valid UTF-8", MayReference: true})
		}
		return FileEdit{}, false
	}
	var splices []Splice
	for _, m := range f.Mentions(r.Old) {
		if !inTag(f, m) {
			r.LeftAlone = append(r.LeftAlone, LeftAlone{File: f.Path, Range: m})
			continue
		}
		splices = append(splices, Splice{Range: pos.Range{Start: m.Start + 1, End: m.End}, Old: r.Old, New: r.New})
		r.Tags = append(r.Tags, TagEdit{File: f.Path, Range: m})
	}
	if len(splices) == 0 {
		return FileEdit{}, false
	}
	r.PerFile[f.Path] = len(splices)
	return FileEdit{Path: f.Path, Snapshot: f.Source(), Splices: splices}, true
}

// inTag reports whether a mention is a tag's reference or lies in its quote.
func inTag(f *formal.File, m pos.Range) bool {
	for _, t := range f.Tags {
		if t.Ref != nil && t.Ref.Range == m {
			return true
		}
		if !t.QuoteRange.Empty() && t.QuoteRange.ContainsRange(m) {
			return true
		}
	}
	return false
}
