// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package edit changes spec files by exact byte-range splices and writes
// several files crash-safely. We never render Markdown back out: every
// change replaces known bytes of the original source with new bytes and
// leaves everything else untouched.
package edit

import (
	"fmt"
	"sort"

	"github.com/jwriteclub/spec/internal/pos"
)

// Splice replaces src[Range] with New. Old is the text the caller expects
// to find there; it is verified before any change is made, so a splice
// computed from a stale parse fails loudly instead of corrupting a file.
type Splice struct {
	Range pos.Range
	Old   string
	New   string
}

// MismatchError says a splice's range is out of bounds or does not hold the
// bytes the splice expected.
type MismatchError struct {
	Path   string // "" when applying to a bare buffer
	Splice Splice
	Found  string // what the range actually holds ("" when out of bounds)
	Bounds bool   // true when the range is out of bounds
}

func (e *MismatchError) Error() string {
	where := ""
	if e.Path != "" {
		where = e.Path + ": "
	}
	if e.Bounds {
		return fmt.Sprintf("%ssplice range %s is outside the file", where, e.Splice.Range)
	}
	return fmt.Sprintf("%ssplice at %s expected %q but found %q (the file changed since it was parsed)", where, e.Splice.Range, e.Splice.Old, e.Found)
}

// OverlapError says two splices touch the same bytes.
type OverlapError struct {
	Path string
	A, B Splice
}

func (e *OverlapError) Error() string {
	where := ""
	if e.Path != "" {
		where = e.Path + ": "
	}
	return fmt.Sprintf("%ssplices at %s and %s overlap", where, e.A.Range, e.B.Range)
}

// Apply returns src with every splice applied. Splices may be given in any
// order but must not overlap (they may touch, and several empty splices at
// one offset apply in the order given). Each splice's Old must equal the
// bytes it replaces. src is not modified.
func Apply(src []byte, splices []Splice) ([]byte, error) {
	return apply("", src, splices)
}

func apply(path string, src []byte, splices []Splice) ([]byte, error) {
	ss := append([]Splice(nil), splices...)
	sort.SliceStable(ss, func(i, j int) bool {
		if ss[i].Range.Start != ss[j].Range.Start {
			return ss[i].Range.Start < ss[j].Range.Start
		}
		return ss[i].Range.End < ss[j].Range.End
	})
	size := len(src)
	for i, s := range ss {
		if !s.Range.Valid(len(src)) {
			return nil, &MismatchError{Path: path, Splice: s, Bounds: true}
		}
		if found := string(src[s.Range.Start:s.Range.End]); found != s.Old {
			return nil, &MismatchError{Path: path, Splice: s, Found: found}
		}
		if i > 0 && ss[i-1].Range.End > s.Range.Start {
			return nil, &OverlapError{Path: path, A: ss[i-1], B: s}
		}
		size += len(s.New) - s.Range.Len()
	}
	out := make([]byte, 0, size)
	at := 0
	for _, s := range ss {
		out = append(out, src[at:s.Range.Start]...)
		out = append(out, s.New...)
		at = s.Range.End
	}
	out = append(out, src[at:]...)
	return out, nil
}
