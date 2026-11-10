// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/mdspec"
)

// Status is the headline of one anchor's difference.
type Status string

// Statuses.
const (
	// Added: the name exists only on the new side.
	Added Status = "added"
	// Removed: the name exists only on the old side.
	Removed Status = "removed"
	// Changed: the body hash differs (after applying detected renames to
	// the old body's references).
	Changed Status = "changed"
	// KindChanged: only the kind (inv/def) differs; the body hash is equal.
	KindChanged Status = "kind_changed"
	// Renamed: a removed and an added anchor form the unique pair sharing a
	// shape hash. Entry.Name is the new name and OldName the old one.
	Renamed Status = "renamed"
	// Retitled: only the title differs (after applying detected renames to
	// the old title's references). Informational: it needs no trailer.
	Retitled Status = "retitled"
)

// Entry is one anchor that differs between the two sides. Unchanged anchors
// are not listed.
//
// Status is the headline. Changed, KindChanged and Retitled are independent
// flags: a "changed" anchor whose kind and title also differ has all three
// set, and a "renamed" anchor may also carry any of them.
type Entry struct {
	Name    string `json:"name"`               // new name (old name for removed), without $
	OldName string `json:"old_name,omitempty"` // renamed only
	Status  Status `json:"status"`

	Changed     bool `json:"changed"`
	KindChanged bool `json:"kind_changed"`
	Retitled    bool `json:"retitled"`

	OldKind  mdspec.Kind `json:"old_kind,omitempty"`
	NewKind  mdspec.Kind `json:"new_kind,omitempty"`
	OldHash  string      `json:"old_hash,omitempty"` // full hash on the old side, as it was
	NewHash  string      `json:"new_hash,omitempty"`
	OldShort string      `json:"old_short,omitempty"`
	NewShort string      `json:"new_short,omitempty"`
	OldTitle string      `json:"old_title,omitempty"`
	NewTitle string      `json:"new_title,omitempty"`
	OldFile  string      `json:"old_file,omitempty"`
	NewFile  string      `json:"new_file,omitempty"`

	// Old and New are the anchors on each side (nil when absent).
	Old *mdspec.Anchor `json:"-"`
	New *mdspec.Anchor `json:"-"`
}

// NeedsChangeTrailer reports whether the entry requires a Spec-Change line.
func (e Entry) NeedsChangeTrailer() bool {
	switch e.Status {
	case Added, Removed, Changed, KindChanged:
		return true
	case Renamed:
		return e.Changed || e.KindChanged
	}
	return false
}

// Skipped is a name that was not compared because it is defined more than
// once on a side.
type Skipped struct {
	Name string `json:"name"`
	// Side is "old", "new" or "both".
	Side string `json:"side"`
}

// Diff is the anchor-level difference between two specs.
type Diff struct {
	// Entries are the differing anchors: new-side anchors in project order
	// (added, changed, renamed, ...), then removed anchors in old project
	// order.
	Entries []Entry `json:"entries"`
	// Renames maps each detected old name to its new name (no $ sigils).
	Renames map[string]string `json:"renames"`
	// Unchanged counts anchors present on both sides with equal hash, kind
	// and title.
	Unchanged int `json:"unchanged"`
	// Skipped lists names defined more than once on a side.
	Skipped []Skipped `json:"skipped"`
	// Diagnostics holds one def-duplicate error per skipped name and side.
	// The CLI exits 4 when it is non-empty.
	Diagnostics []diag.Diagnostic `json:"diagnostics"`
	// SourcesDiffer is true when any spec file was added, removed or has
	// different bytes. With no entries it means the edit was prose only.
	SourcesDiffer bool `json:"sources_differ"`
}

// State names for status output.
const (
	StateClean          = "clean"
	StateProseOnly      = "prose_only"
	StateAnchorsChanged = "anchors_changed"
)

// State summarizes the diff: "anchors_changed" when any anchor needs a
// trailer, "prose_only" when files differ but no anchor needs one (titles
// alone do not), else "clean". The status command adds "no_baseline".
func (d *Diff) State() string {
	for _, e := range d.Entries {
		if e.NeedsChangeTrailer() || e.Status == Renamed {
			return StateAnchorsChanged
		}
	}
	if d.SourcesDiffer || len(d.Entries) > 0 {
		return StateProseOnly
	}
	return StateClean
}

// Compare diffs two specs by anchor name over the whole project (plan 4):
//
//  1. Names defined more than once on a side are skipped and reported as
//     def-duplicate.
//  2. Rename detection: each removed and each added anchor gets its shape
//     hash (body hash with every reference name replaced by a placeholder).
//     A removed and an added anchor form a rename only if they are the
//     unique pair sharing a shape hash.
//  3. The rename map is applied to the old bodies' references before
//     hashing, then anchors are compared by name: a different hash is
//     changed, a different kind kind_changed, a different title retitled.
//
// Moving an anchor between files is not a change. Compare is pure.
func Compare(old, new *mdspec.Spec) *Diff {
	d := &Diff{Renames: map[string]string{}, Entries: []Entry{}, Skipped: []Skipped{}, Diagnostics: []diag.Diagnostic{}}

	dupOld := duplicates(old)
	dupNew := duplicates(new)
	skip := map[string]bool{}
	for n := range dupOld {
		skip[n] = true
	}
	for n := range dupNew {
		skip[n] = true
	}
	var skipNames []string
	for n := range skip {
		skipNames = append(skipNames, n)
	}
	sort.Strings(skipNames)
	for _, n := range skipNames {
		side := "old"
		switch {
		case dupOld[n] != nil && dupNew[n] != nil:
			side = "both"
		case dupNew[n] != nil:
			side = "new"
		}
		d.Skipped = append(d.Skipped, Skipped{Name: n, Side: side})
		for _, s := range []struct {
			label string
			as    []*mdspec.Anchor
		}{{"baseline", dupOld[n]}, {"current", dupNew[n]}} {
			if s.as == nil {
				continue
			}
			first := s.as[0]
			d.Diagnostics = append(d.Diagnostics, diag.Diagnostic{
				Code: diag.CodeDefDuplicate, Severity: diag.Error, File: first.File, Range: first.NameRange,
				Message: fmt.Sprintf("$%s is defined %d times in the %s spec, so it was not compared; names must be unique across the project. Rename all but one", n, len(s.as), s.label),
			})
		}
	}

	oldBy := uniqueByName(old, skip)
	newBy := uniqueByName(new, skip)

	// Removed and added sets, in project order.
	var removed, added []*mdspec.Anchor
	for _, a := range anchorsOf(old) {
		if skip[a.Name] {
			continue
		}
		if _, ok := newBy[a.Name]; !ok {
			removed = append(removed, a)
		}
	}
	for _, a := range anchorsOf(new) {
		if skip[a.Name] {
			continue
		}
		if _, ok := oldBy[a.Name]; !ok {
			added = append(added, a)
		}
	}

	// Rename detection: the unique pair sharing a shape hash.
	remByShape := map[string][]*mdspec.Anchor{}
	for _, a := range removed {
		h := a.ShapeHash()
		remByShape[h] = append(remByShape[h], a)
	}
	addByShape := map[string][]*mdspec.Anchor{}
	for _, a := range added {
		h := a.ShapeHash()
		addByShape[h] = append(addByShape[h], a)
	}
	renamedFrom := map[string]*mdspec.Anchor{} // new name -> old anchor
	renamedTo := map[string]bool{}             // old name -> was renamed
	for h, rs := range remByShape {
		as := addByShape[h]
		if len(rs) == 1 && len(as) == 1 {
			d.Renames[rs[0].Name] = as[0].Name
			renamedFrom[as[0].Name] = rs[0]
			renamedTo[rs[0].Name] = true
		}
	}

	compare := func(o, n *mdspec.Anchor, e *Entry) {
		e.OldKind, e.NewKind = o.Kind, n.Kind
		e.OldHash, e.NewHash = o.Hash, n.Hash
		e.OldShort, e.NewShort = o.ShortHash, n.ShortHash
		e.OldTitle, e.NewTitle = o.Title, n.Title
		e.OldFile, e.NewFile = o.File, n.File
		e.Old, e.New = o, n
		e.Changed = o.HashWithRenames(d.Renames) != n.Hash
		e.KindChanged = o.Kind != n.Kind
		e.Retitled = o.TitleWithRenames(d.Renames) != n.Title
	}

	for _, n := range anchorsOf(new) {
		if skip[n.Name] {
			continue
		}
		e := Entry{Name: n.Name}
		switch {
		case renamedFrom[n.Name] != nil:
			o := renamedFrom[n.Name]
			e.OldName = o.Name
			compare(o, n, &e)
			e.Status = Renamed
		case oldBy[n.Name] == nil:
			e.Status = Added
			e.NewKind, e.NewHash, e.NewShort, e.NewTitle, e.NewFile, e.New = n.Kind, n.Hash, n.ShortHash, n.Title, n.File, n
		default:
			compare(oldBy[n.Name], n, &e)
			switch {
			case e.Changed:
				e.Status = Changed
			case e.KindChanged:
				e.Status = KindChanged
			case e.Retitled:
				e.Status = Retitled
			default:
				d.Unchanged++
				continue
			}
		}
		d.Entries = append(d.Entries, e)
	}
	for _, o := range removed {
		if renamedTo[o.Name] {
			continue
		}
		d.Entries = append(d.Entries, Entry{
			Name: o.Name, Status: Removed, OldKind: o.Kind, OldHash: o.Hash, OldShort: o.ShortHash,
			OldTitle: o.Title, OldFile: o.File, Old: o,
		})
	}

	d.SourcesDiffer = sourcesDiffer(old, new)
	diag.Sort(d.Diagnostics)
	return d
}

func anchorsOf(s *mdspec.Spec) []*mdspec.Anchor {
	if s == nil {
		return nil
	}
	return s.Anchors
}

// duplicates returns the anchors of every name defined more than once.
func duplicates(s *mdspec.Spec) map[string][]*mdspec.Anchor {
	if s == nil {
		return nil
	}
	out := map[string][]*mdspec.Anchor{}
	for n, as := range s.ByName {
		if len(as) > 1 {
			out[n] = as
		}
	}
	return out
}

func uniqueByName(s *mdspec.Spec, skip map[string]bool) map[string]*mdspec.Anchor {
	out := map[string]*mdspec.Anchor{}
	for _, a := range anchorsOf(s) {
		if !skip[a.Name] {
			out[a.Name] = a
		}
	}
	return out
}

func sourcesDiffer(old, new *mdspec.Spec) bool {
	if old == nil || new == nil {
		return old != new
	}
	if len(old.Files) != len(new.Files) {
		return true
	}
	for _, f := range new.Files {
		o := old.File(f.Path)
		if o == nil || !bytes.Equal(o.Source(), f.Source()) {
			return true
		}
	}
	return false
}
