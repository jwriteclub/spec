// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package termtext makes text from outside the program (a proposal, a model,
// a spec file) safe to write to a terminal.
package termtext

import (
	"strings"
	"unicode/utf8"
)

// Marks drawn in place of characters that must not reach the terminal as
// they are (the editor shows them the same way).
const (
	// invisibleMark is a middle dot: a character that draws nothing but
	// hides or reorders text (bidi controls, zero-width space, BOM).
	invisibleMark = "\u00b7"
	// c1Mark is the replacement character: a C1 control (U+0080-U+009F),
	// which some terminals act on like ESC sequences.
	c1Mark = "\ufffd"
)

// Clean makes text that came from a proposal, a model or a spec file
// safe to draw. C0 controls and DEL are shown in caret notation (ESC as
// ^[), so an escape sequence can neither style, hide nor act (OSC 52
// clipboard writes, conceal, carriage returns); C1 controls become U+FFFD;
// bidi controls, zero-width spaces and BOMs become a middle dot, so text
// cannot be reordered or hidden from the reviewer. Line breaks and tabs
// stay (the callers lay them out).
func Clean(s string) string {
	if !needsClean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			b.WriteByte('^')
			b.WriteRune(r ^ 0x40)
		case r >= 0x80 && r <= 0x9f:
			b.WriteString(c1Mark)
		case invisible(r):
			b.WriteString(invisibleMark)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// needsClean reports whether Clean would change s (invalid UTF-8
// becomes U+FFFD too).
func needsClean(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' || r >= 0x7f && r <= 0x9f || invisible(r) {
			return true
		}
	}
	return false
}

// invisible reports whether r draws nothing but can hide or reorder the
// text around it.
func invisible(r rune) bool {
	switch {
	case r == 0x061c, r == 0x200b, r == 0x200e, r == 0x200f, r == 0xfeff:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// CleanAll applies Clean to each of ss in place and returns ss.
func CleanAll(ss []string) []string {
	for k := range ss {
		ss[k] = Clean(ss[k])
	}
	return ss
}
