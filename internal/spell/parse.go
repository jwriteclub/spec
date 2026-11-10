// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// idEquals finds "ID=" in a reply line with its white space removed.
var idEquals = regexp.MustCompile(`\d+=`)

// ErrUnparsed is a reply with text but no ID=fix line.
var ErrUnparsed = errors.New("the reply has no ID=fix line")

// ParseFixes reads a triage reply: one ID=fix line per typo, or "-" when
// there is none. It is tolerant: white space is removed, a leading list
// marker or label is skipped, leading zeros are accepted ("03=delay"), two
// answers run together on one line are split ("6=malformed16=invalidated"),
// trailing punctuation and quotes are trimmed, and a line it cannot read is
// skipped. IDs outside 1..n are dropped, and the first answer for an ID
// wins. A reply with text but no answer at all is ErrUnparsed.
func ParseFixes(reply string, n int) (map[int]string, error) {
	out := map[int]string{}
	text := strings.TrimSpace(reply)
	if text == "" || text == "-" {
		return out, nil
	}
	sawNone := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, line)
		line = strings.Trim(line, "`")
		if line == "" || strings.HasPrefix(line, "```") {
			continue
		}
		if line == "-" || strings.EqualFold(line, "none") {
			sawNone = true
			continue
		}
		locs := idEquals.FindAllStringIndex(line, -1)
		// A glued answer's digits may run into the previous fix
		// ("sha2562=x" with 3 IDs is fix "sha256", then "2=x"): take the
		// longest digit suffix that is an ID in range.
		for k := 1; k < len(locs); k++ {
			for s := locs[k][0]; s < locs[k][1]-1; s++ {
				if id, err := strconv.Atoi(line[s : locs[k][1]-1]); err == nil && id >= 1 && id <= n {
					locs[k][0] = s
					break
				}
			}
		}
		for k, loc := range locs {
			end := len(line)
			if k+1 < len(locs) {
				end = locs[k+1][0]
			}
			id, err := strconv.Atoi(line[loc[0] : loc[1]-1])
			if err != nil || id < 1 || id > n {
				continue
			}
			fix := cleanFix(line[loc[1]:end])
			if fix == "" {
				continue
			}
			if _, dup := out[id]; !dup {
				out[id] = fix
			}
		}
	}
	if len(out) == 0 && !sawNone {
		return nil, fmt.Errorf("%w: %q", ErrUnparsed, shorten(text, 120))
	}
	return out, nil
}

// cleanFix trims quotes, brackets and trailing punctuation from a fix and
// cuts it at an opening parenthesis (a comment the model added).
func cleanFix(s string) string {
	if i := strings.IndexAny(s, "(["); i > 0 {
		s = s[:i]
	}
	s = strings.Trim(s, "\"'`*[]()<>")
	s = strings.TrimRight(s, ".,;:!?")
	s = strings.Trim(s, "\"'`*")
	if strings.ContainsAny(s, "=") || s == "-" {
		return ""
	}
	return s
}

// shorten cuts s to n bytes.
func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
