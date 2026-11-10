// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"

	"github.com/jwriteclub/spec/internal/mdspec"
)

// targetKind says what a NAME|SECTION argument names.
type targetKind int

const (
	targetAnchor targetKind = iota
	targetSection
)

// target is a parsed NAME|SECTION argument.
type target struct {
	kind targetKind
	// name is the anchor name without a sigil (targetAnchor).
	name string
	// stem and number identify a section (targetSection); stem may be "".
	stem   string
	number string
}

// shellHint is the message for an argument the shell already expanded.
const shellHint = "did your shell expand `$`? write `Frame.Timing`, not `$Frame.Timing`"

// parseTarget parses an anchor or section argument:
//
//	Frame.Timing  $Frame.Timing          an anchor
//	5.2a  §5.2a  #5.2a  hls:5.2a  §hls:5.2a   a section
//
// Anchors start with a letter, sections with a digit or a stem followed by
// a colon. An argument that starts with '.' (or is empty) is what the shell
// leaves of `$Frame.Timing` when it expands `$Frame`.
func parseTarget(arg string) (target, *outcome) {
	a := strings.TrimSpace(arg)
	if a == "" || strings.HasPrefix(a, ".") {
		return target{}, usageErr("argument %q: %s", arg, shellHint)
	}
	sectionSigil := false
	switch {
	case strings.HasPrefix(a, "$"):
		a = a[1:]
		if a == "" || a[0] == '.' {
			return target{}, usageErr("argument %q: %s", arg, shellHint)
		}
	case strings.HasPrefix(a, "§"):
		a = strings.TrimSpace(strings.TrimPrefix(a, "§"))
		sectionSigil = true
	case strings.HasPrefix(a, "#"):
		a = strings.TrimSpace(strings.TrimPrefix(a, "#"))
		sectionSigil = true
	}
	if a == "" {
		return target{}, usageErr("argument %q names nothing: give an anchor (Frame.Timing) or a section (5.2a, hls:5.2a)", arg)
	}
	if stem, num, ok := strings.Cut(a, ":"); ok {
		num = strings.TrimSuffix(strings.TrimSpace(num), ".")
		if stem == "" || num == "" {
			return target{}, usageErr("argument %q: a qualified section is stem:number, for example hls:5.2a", arg)
		}
		return target{kind: targetSection, stem: stem, number: num}, nil
	}
	if sectionSigil || a[0] >= '0' && a[0] <= '9' {
		return target{kind: targetSection, number: strings.TrimSuffix(a, ".")}, nil
	}
	return target{kind: targetAnchor, name: a}, nil
}

// display returns the target as the user would write it.
func (t target) display() string {
	if t.kind == targetAnchor {
		return t.name
	}
	if t.stem != "" {
		return t.stem + ":" + t.number
	}
	return t.number
}

// sectionNumberOK reports whether number is a plausible section number.
func sectionNumberOK(number string) bool {
	return mdspec.ValidSectionNumber(number)
}
