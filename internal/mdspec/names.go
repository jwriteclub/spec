// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package mdspec

import (
	"path"
	"strings"
)

// ValidName reports whether s is a valid anchor name (plan 1.1):
// Segment ("." Segment)*, Segment = [A-Z][A-Za-z0-9]*, with at least one
// lowercase letter somewhere in the name. s carries no $ sigil.
func ValidName(s string) bool {
	if s == "" {
		return false
	}
	lower := false
	segStart := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case segStart:
			if c < 'A' || c > 'Z' {
				return false
			}
			segStart = false
		case c == '.':
			segStart = true
		case c >= 'a' && c <= 'z':
			lower = true
		case (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'):
		default:
			return false
		}
	}
	return !segStart && lower
}

// ValidSectionNumber reports whether s is a section reference number:
// N(.N)*[a-z]?.
func ValidSectionNumber(s string) bool {
	return s != "" && sectionNumberEnd([]byte(s), 0) == len(s)
}

// StemOf returns the stem of a spec file path: the base name minus
// ".spec.md", or minus its extension for other files.
func StemOf(p string) string {
	base := path.Base(strings.ReplaceAll(p, "\\", "/"))
	if strings.HasSuffix(base, ".spec.md") {
		return strings.TrimSuffix(base, ".spec.md")
	}
	return strings.TrimSuffix(base, path.Ext(base))
}

func isNameByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func isLower(c byte) bool {
	return c >= 'a' && c <= 'z'
}

func isAlnum(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func isStemByte(c byte) bool {
	return isAlnum(c) || c == '_' || c == '-'
}

// validRefStem reports whether b can be the stem of a §stem:N reference. It
// starts with a letter, or with a digit and holds a letter or '-', so
// §04-queue:4.2 is qualified and §10:30 stays section 10 followed by ":30".
func validRefStem(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if isDigit(b[0]) {
		for _, c := range b {
			if c == '-' || (c >= 'A' && c <= 'Z') || isLower(c) {
				return true
			}
		}
		return false
	}
	return isAlnum(b[0])
}

// ScanSectionRef reads the text after a § sign and its optional space: an
// optional "stem:" qualifier and a section number. n is the length of the
// reference in b. ok is false when b does not start with a reference, which
// includes a number that runs on into letters or digits, such as 5.2ab. The
// parser applies the same grammar to Markdown.
func ScanSectionRef(b []byte) (stem, number string, n int, ok bool) {
	k := 0
	for k < len(b) && isStemByte(b[k]) {
		k++
	}
	j := 0
	if k+1 < len(b) && b[k] == ':' && isDigit(b[k+1]) && validRefStem(b[:k]) {
		stem = string(b[:k])
		j = k + 1
	}
	end := sectionNumberEnd(b, j)
	if end == j || (end < len(b) && isAlnum(b[end])) {
		return "", "", 0, false
	}
	return stem, string(b[j:end]), end, true
}

// sectionNumberEnd returns the end of the section number N(.N)*[a-z]?
// starting at i, or i when there is none. A '.' is taken only when a digit
// follows it.
func sectionNumberEnd(b []byte, i int) int {
	j := i
	for j < len(b) && isDigit(b[j]) {
		j++
	}
	if j == i {
		return i
	}
	for j+1 < len(b) && b[j] == '.' && isDigit(b[j+1]) {
		j++
		for j < len(b) && isDigit(b[j]) {
			j++
		}
	}
	if j < len(b) && isLower(b[j]) {
		j++
	}
	return j
}
