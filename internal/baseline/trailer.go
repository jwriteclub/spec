// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jwriteclub/spec/internal/mdspec"
)

// Trailer keys.
const (
	KeyChange = "Spec-Change"
	KeyRename = "Spec-Rename"
)

// ChangeLine is a Spec-Change trailer:
//
//	Spec-Change: $Name <kind>:<old8> -> <kind>:<new8>
//
// with "-" for the side that does not exist (an add or a remove):
//
//	Spec-Change: $Frame.Timing - -> inv:8b77d0a4
//
// A kind change shows in the kind prefixes. An empty kind means "-".
type ChangeLine struct {
	Name     string // without $
	OldKind  mdspec.Kind
	OldShort string // 8 hex characters
	NewKind  mdspec.Kind
	NewShort string
}

func side(k mdspec.Kind, short string) string {
	if k == "" {
		return "-"
	}
	return string(k) + ":" + short
}

// String formats the trailer line.
func (c ChangeLine) String() string {
	return fmt.Sprintf("%s: $%s %s -> %s", KeyChange, c.Name, side(c.OldKind, c.OldShort), side(c.NewKind, c.NewShort))
}

// RenameLine is a Spec-Rename trailer: `Spec-Rename: $Old -> $New`.
type RenameLine struct {
	Old string // without $
	New string
}

// String formats the trailer line.
func (r RenameLine) String() string {
	return fmt.Sprintf("%s: $%s -> $%s", KeyRename, r.Old, r.New)
}

// Trailers is a set of trailer lines.
type Trailers struct {
	Renames []RenameLine `json:"renames"`
	Changes []ChangeLine `json:"changes"`
}

// Lines formats the trailers, Spec-Rename lines first, each group sorted by
// name, so the output is deterministic.
func (t Trailers) Lines() []string {
	rs := append([]RenameLine(nil), t.Renames...)
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Old != rs[j].Old {
			return rs[i].Old < rs[j].Old
		}
		return rs[i].New < rs[j].New
	})
	cs := append([]ChangeLine(nil), t.Changes...)
	sort.Slice(cs, func(i, j int) bool { return cs[i].String() < cs[j].String() })
	lines := make([]string, 0, len(rs)+len(cs))
	for _, r := range rs {
		lines = append(lines, r.String())
	}
	for _, c := range cs {
		lines = append(lines, c.String())
	}
	return lines
}

// Empty reports whether there are no trailers.
func (t Trailers) Empty() bool {
	return len(t.Renames) == 0 && len(t.Changes) == 0
}

// Required returns the trailers the diff demands (plan 4): a Spec-Change
// for every added, removed, changed or kind-changed anchor (including a
// renamed one that also changed or changed kind; its old side uses the
// anchor's real hash in the baseline), and a Spec-Rename for every rename.
// Retitled anchors need none.
func (d *Diff) Required() Trailers {
	t := Trailers{Renames: []RenameLine{}, Changes: []ChangeLine{}}
	for _, e := range d.Entries {
		if e.Status == Renamed {
			t.Renames = append(t.Renames, RenameLine{Old: e.OldName, New: e.Name})
		}
		if !e.NeedsChangeTrailer() {
			continue
		}
		c := ChangeLine{Name: e.Name}
		if e.Old != nil {
			c.OldKind, c.OldShort = e.OldKind, e.OldShort
		}
		if e.New != nil {
			c.NewKind, c.NewShort = e.NewKind, e.NewShort
		}
		t.Changes = append(t.Changes, c)
	}
	return t
}

// Malformed is a line that starts with a Spec trailer key but does not parse.
type Malformed struct {
	Line   int    `json:"line"` // 1-based line in the message
	Text   string `json:"text"`
	Reason string `json:"reason"`
}

const (
	nameRE = `[A-Z][A-Za-z0-9]*(?:\.[A-Z][A-Za-z0-9]*)*`
	sideRE = `-|(?:inv|def):[0-9a-fA-F]{8}`
)

var (
	keyLine    = regexp.MustCompile(`(?i)^\s*(spec-change|spec-rename)\s*:\s*(.*?)\s*$`)
	changeBody = regexp.MustCompile(`^\$(` + nameRE + `)\s+(` + sideRE + `)\s+->\s+(` + sideRE + `)$`)
	renameBody = regexp.MustCompile(`^\$(` + nameRE + `)\s+->\s+\$(` + nameRE + `)$`)
)

func parseSide(s string) (mdspec.Kind, string) {
	if s == "-" {
		return "", ""
	}
	kind, short, _ := strings.Cut(s, ":")
	return mdspec.Kind(kind), strings.ToLower(short)
}

// scissors is git's cut line (`git commit -v`) without its leading comment
// character: git drops it and everything below it from the message.
const scissors = " ------------------------ >8 ------------------------"

// ParseMessage extracts the Spec-Change and Spec-Rename trailers from a
// commit message. Every line is considered (not only the final paragraph),
// except lines starting with '#' (git's comment lines) and everything from
// git's scissors line on; keys are case-insensitive; CRLF is tolerated. Lines that start with a Spec key but
// do not match the grammar are returned as Malformed, with a hint when the
// cause is likely shell expansion of $Name.
func ParseMessage(msg string) (Trailers, []Malformed) {
	t := Trailers{Renames: []RenameLine{}, Changes: []ChangeLine{}}
	var bad []Malformed
	for i, line := range strings.Split(msg, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) == len(scissors)+1 && strings.HasSuffix(line, scissors) {
			break
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		m := keyLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key, body := strings.ToLower(m[1]), m[2]
		if key == "spec-rename" {
			rm := renameBody.FindStringSubmatch(body)
			if rm == nil {
				bad = append(bad, Malformed{Line: i + 1, Text: line, Reason: malformedReason(body, "Spec-Rename: $Old -> $New")})
				continue
			}
			t.Renames = append(t.Renames, RenameLine{Old: rm[1], New: rm[2]})
			continue
		}
		cm := changeBody.FindStringSubmatch(body)
		if cm == nil {
			bad = append(bad, Malformed{Line: i + 1, Text: line, Reason: malformedReason(body, "Spec-Change: $Name <kind>:<old8> -> <kind>:<new8>")})
			continue
		}
		if cm[2] == "-" && cm[3] == "-" {
			bad = append(bad, Malformed{Line: i + 1, Text: line, Reason: "both sides are '-'; one side must be kind:hash8"})
			continue
		}
		ok, os := parseSide(cm[2])
		nk, ns := parseSide(cm[3])
		t.Changes = append(t.Changes, ChangeLine{Name: cm[1], OldKind: ok, OldShort: os, NewKind: nk, NewShort: ns})
	}
	return t, bad
}

func malformedReason(body, form string) string {
	if !strings.Contains(body, "$") {
		return fmt.Sprintf("expected `%s`; the name has no $ (did your shell expand $Name inside double quotes? use single quotes or --message-file)", form)
	}
	return fmt.Sprintf("expected `%s`, where kind is inv or def and the hashes are 8 hex characters (or - for a missing side)", form)
}

// CheckResult is the outcome of matching required trailers against a
// commit message.
type CheckResult struct {
	// Missing are the required trailers that the message lacks.
	Missing Trailers `json:"missing"`
	// Malformed are Spec-keyed lines that failed to parse; they may be
	// failed attempts at the missing lines.
	Malformed []Malformed `json:"malformed"`
}

// OK reports whether nothing is missing.
func (r *CheckResult) OK() bool { return r.Missing.Empty() }

// MissingLines returns the exact lines to add, as Trailers.Lines does.
func (r *CheckResult) MissingLines() []string { return r.Missing.Lines() }

// Check matches the required trailers against the message. A required line
// is present when the message holds an identical trailer (same name, kinds
// and hashes; hash case is ignored). Extra trailers are ignored.
func Check(required Trailers, message string) *CheckResult {
	have, bad := ParseMessage(message)
	res := &CheckResult{Missing: Trailers{Renames: []RenameLine{}, Changes: []ChangeLine{}}, Malformed: bad}
	if res.Malformed == nil {
		res.Malformed = []Malformed{}
	}
	hr := map[RenameLine]bool{}
	for _, r := range have.Renames {
		hr[r] = true
	}
	hc := map[ChangeLine]bool{}
	for _, c := range have.Changes {
		hc[c] = true
	}
	for _, r := range required.Renames {
		if !hr[r] {
			res.Missing.Renames = append(res.Missing.Renames, r)
		}
	}
	for _, c := range required.Changes {
		if !hc[c] {
			res.Missing.Changes = append(res.Missing.Changes, c)
		}
	}
	return res
}
