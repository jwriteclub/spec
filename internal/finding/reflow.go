// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package finding

import (
	"regexp"
	"strings"
)

// Spec files are hard-wrapped, and models quoting a wrapped sentence often
// join its lines with a single space. A find that does not occur exactly is
// therefore matched again with every run of whitespace standing for any run
// of whitespace; when that matches exactly once, the hunk is rebuilt on the
// file's own text, so the line breaks outside the change survive.

// isSpace is the whitespace a wrapped line may differ in.
func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// words splits s at runs of isSpace.
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r < 0x80 && isSpace(byte(r)) })
}

// looseMatch returns the ranges in src that equal find up to the width and
// kind of each whitespace run, stopping after two.
func looseMatch(src, find string) [][]int {
	ws := words(find)
	if len(ws) < 2 {
		return nil // one word: whitespace cannot be what differs
	}
	quoted := make([]string, len(ws))
	for i, w := range ws {
		quoted[i] = regexp.QuoteMeta(w)
	}
	re, err := regexp.Compile(strings.Join(quoted, `[ \t\r\n]+`))
	if err != nil {
		return nil
	}
	return re.FindAllStringIndex(src, 2)
}

// reflow rewrites the model's replace for the file's actual text, which
// equals find up to whitespace runs: the part of find that replace keeps
// (common prefix and suffix) is taken from actual, and only the changed
// middle comes from replace.
func reflow(find, replace, actual string) string {
	p := 0
	for p < len(find) && p < len(replace) && find[p] == replace[p] {
		p++
	}
	s := 0
	for s < len(find)-p && s < len(replace)-p && find[len(find)-1-s] == replace[len(replace)-1-s] {
		s++
	}
	// A cut strictly inside a whitespace run moves to the run's edge, so the
	// run maps to one whole run of actual.
	for p > 0 && p < len(find) && isSpace(find[p-1]) && isSpace(find[p]) {
		p--
	}
	e := len(find) - s
	for e < len(find) && e > 0 && isSpace(find[e-1]) && isSpace(find[e]) {
		e++
	}
	a := mapOffset(find, actual, p)
	b := mapOffset(find, actual, e)
	if a < 0 || b < 0 || a > b {
		return ""
	}
	mid := replace[p : len(replace)-(len(find)-e)]
	return actual[:a] + mid + actual[b:]
}

// mapOffset maps an offset in find to the matching offset in actual, which
// has the same non-space bytes in the same order and a whitespace run
// wherever find has one. i must not be strictly inside a whitespace run.
// It returns -1 when the two do not line up.
func mapOffset(find, actual string, i int) int {
	fi, ai := 0, 0
	for fi < i {
		switch {
		case isSpace(find[fi]):
			for fi < len(find) && isSpace(find[fi]) {
				fi++
			}
			if ai >= len(actual) || !isSpace(actual[ai]) {
				return -1
			}
			for ai < len(actual) && isSpace(actual[ai]) {
				ai++
			}
		default:
			if ai >= len(actual) || actual[ai] != find[fi] {
				return -1
			}
			fi++
			ai++
		}
	}
	if fi != i {
		return -1
	}
	return ai
}
