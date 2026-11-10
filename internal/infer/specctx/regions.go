// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package specctx

import (
	"bytes"

	"github.com/jwriteclub/spec/internal/pos"
)

// maxDiffCells bounds the line LCS table; larger middles become one region.
const maxDiffCells = 4 << 20

// Changed returns the regions of new that differ from old, line by line:
// each region is a run of changed lines in new (a pure deletion becomes the
// empty range where the lines were) with the old lines it replaced. Runs
// separated by fewer than three unchanged lines are merged. A nil old (a new
// file) gives one region covering everything.
func Changed(path string, old, new []byte) []Region {
	if bytes.Equal(old, new) {
		return nil
	}
	if old == nil {
		return []Region{{File: path, Range: pos.Range{Start: 0, End: len(new)}}}
	}
	a, aOff := splitLines(old)
	b, bOff := splitLines(new)

	// Common prefix and suffix.
	p := 0
	for p < len(a) && p < len(b) && bytes.Equal(a[p], b[p]) {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && bytes.Equal(a[len(a)-1-s], b[len(b)-1-s]) {
		s++
	}
	am, bm := a[p:len(a)-s], b[p:len(b)-s]

	// keepA[i] / keepB[j]: the line is in the LCS of the middles.
	keepA := make([]bool, len(am))
	keepB := make([]bool, len(bm))
	if len(am) > 0 && len(bm) > 0 && (len(am)+1)*(len(bm)+1) <= maxDiffCells {
		lcs(am, bm, keepA, keepB)
	}

	// Walk both middles, emitting runs of changed lines.
	type run struct{ aFrom, aTo, bFrom, bTo int } // line indexes in a and b, half-open
	var runs []run
	i, j := 0, 0
	for i < len(am) || j < len(bm) {
		if i < len(am) && j < len(bm) && keepA[i] && keepB[j] {
			i++
			j++
			continue
		}
		r := run{aFrom: p + i, bFrom: p + j}
		for i < len(am) && !keepA[i] {
			i++
		}
		for j < len(bm) && !keepB[j] {
			j++
		}
		r.aTo, r.bTo = p+i, p+j
		if n := len(runs); n > 0 && r.bFrom-runs[n-1].bTo < 3 {
			runs[n-1].aTo, runs[n-1].bTo = r.aTo, r.bTo
			continue
		}
		runs = append(runs, r)
	}

	out := make([]Region, 0, len(runs))
	for _, r := range runs {
		out = append(out, Region{
			File:  path,
			Range: pos.Range{Start: bOff[r.bFrom], End: bOff[r.bTo]},
			Old:   string(old[aOff[r.aFrom]:aOff[r.aTo]]),
		})
	}
	return out
}

// splitLines splits src into lines (each with its line ending) and returns
// the start offset of every line plus one final offset at len(src).
func splitLines(src []byte) ([][]byte, []int) {
	var lines [][]byte
	offs := []int{0}
	for start := 0; start < len(src); {
		end := bytes.IndexByte(src[start:], '\n')
		if end < 0 {
			end = len(src)
		} else {
			end += start + 1
		}
		lines = append(lines, src[start:end])
		offs = append(offs, end)
		start = end
	}
	return lines, offs
}

// lcs marks the lines of a and b that belong to one longest common
// subsequence.
func lcs(a, b [][]byte, keepA, keepB []bool) {
	n, m := len(a), len(b)
	t := make([]int32, (n+1)*(m+1))
	at := func(i, j int) *int32 { return &t[i*(m+1)+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case bytes.Equal(a[i], b[j]):
				*at(i, j) = *at(i+1, j+1) + 1
			case *at(i+1, j) >= *at(i, j+1):
				*at(i, j) = *at(i+1, j)
			default:
				*at(i, j) = *at(i, j+1)
			}
		}
	}
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case bytes.Equal(a[i], b[j]):
			keepA[i], keepB[j] = true, true
			i++
			j++
		case *at(i+1, j) >= *at(i, j+1):
			i++
		default:
			j++
		}
	}
}
