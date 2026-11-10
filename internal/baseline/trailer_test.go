// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/mdspec"
)

func TestTrailerFormatting(t *testing.T) {
	assert.Equal(t, "Spec-Change: $Frame.Timing - -> inv:8b77d0a4",
		ChangeLine{Name: "Frame.Timing", NewKind: mdspec.KindInvariant, NewShort: "8b77d0a4"}.String(), "add")
	assert.Equal(t, "Spec-Change: $Frame.Timing def:8b77d0a4 -> -",
		ChangeLine{Name: "Frame.Timing", OldKind: mdspec.KindDefinition, OldShort: "8b77d0a4"}.String(), "remove")
	assert.Equal(t, "Spec-Change: $Frame.Timing inv:11111111 -> def:22222222",
		ChangeLine{Name: "Frame.Timing", OldKind: "inv", OldShort: "11111111", NewKind: "def", NewShort: "22222222"}.String(), "kind change")
	assert.Equal(t, "Spec-Rename: $Old.Name -> $New.Name", RenameLine{Old: "Old.Name", New: "New.Name"}.String())
}

func TestTrailerLinesAreSortedAndRenamesFirst(t *testing.T) {
	tr := Trailers{
		Changes: []ChangeLine{
			{Name: "Zed.Last", NewKind: "inv", NewShort: "aaaaaaaa"},
			{Name: "Abe.First", NewKind: "inv", NewShort: "bbbbbbbb"},
		},
		Renames: []RenameLine{{Old: "Q.Two", New: "R.Two"}, {Old: "A.One", New: "B.One"}},
	}
	assert.Equal(t, []string{
		"Spec-Rename: $A.One -> $B.One",
		"Spec-Rename: $Q.Two -> $R.Two",
		"Spec-Change: $Abe.First - -> inv:bbbbbbbb",
		"Spec-Change: $Zed.Last - -> inv:aaaaaaaa",
	}, tr.Lines())
}

func TestParseMessageRoundTrip(t *testing.T) {
	tr := Trailers{
		Changes: []ChangeLine{
			{Name: "A.Add", NewKind: "inv", NewShort: "0123abcd"},
			{Name: "B.Rem", OldKind: "def", OldShort: "deadbeef"},
			{Name: "C.Chg", OldKind: "inv", OldShort: "11111111", NewKind: "def", NewShort: "22222222"},
		},
		Renames: []RenameLine{{Old: "D.Old", New: "D.New"}},
	}
	msg := "Subject line\n\nBody text.\n\n" + joinLines(tr.Lines()) + "\nCo-Authored-By: Someone <x@y>\n"
	got, bad := ParseMessage(msg)
	assert.Empty(t, bad)
	assert.ElementsMatch(t, tr.Changes, got.Changes)
	assert.ElementsMatch(t, tr.Renames, got.Renames)
}

func joinLines(ls []string) string {
	out := ""
	for _, l := range ls {
		out += l + "\n"
	}
	return out
}

func TestParseMessageTolerance(t *testing.T) {
	msg := "subject\r\n\r\n" +
		"spec-change:   $Ab.Cd   inv:AABBCCDD   ->   def:11223344  \r\n" +
		"SPEC-RENAME: $Ab.Cd -> $Ef.Gh\r\n" +
		"# Spec-Change: $Commented.Out - -> inv:00000000\r\n" +
		"  Spec-Change: $Indented.Ok - -> inv:00000000\r\n"
	got, bad := ParseMessage(msg)
	assert.Empty(t, bad)
	require.Len(t, got.Changes, 2)
	assert.Equal(t, ChangeLine{Name: "Ab.Cd", OldKind: "inv", OldShort: "aabbccdd", NewKind: "def", NewShort: "11223344"}, got.Changes[0], "hex is lowercased")
	assert.Equal(t, "Indented.Ok", got.Changes[1].Name)
	assert.Equal(t, []RenameLine{{Old: "Ab.Cd", New: "Ef.Gh"}}, got.Renames)
}

func TestParseMessageMalformed(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"Spec-Change: Frame.Timing - -> inv:8b77d0a4", "no $"},
		{"Spec-Change: $ - -> inv:8b77d0a4", "expected"},
		{"Spec-Change: $Frame.Timing - -> inv:8b77d0", "8 hex"},
		{"Spec-Change: $Frame.Timing - -> foo:8b77d0a4", "kind is inv or def"},
		{"Spec-Change: $Frame.Timing - -> -", "both sides"},
		{"Spec-Change: $frame.timing - -> inv:8b77d0a4", "expected"},
		{"Spec-Change: $Frame.Timing inv:8b77d0a4", "expected"},
		{"Spec-Rename: Old -> New", "no $"},
		{"Spec-Rename: $Old.Name", "expected"},
		{"Spec-Rename: $Old.Name -> $new", "expected"},
	}
	for _, c := range cases {
		got, bad := ParseMessage("subject\n\n" + c.line + "\n")
		assert.True(t, got.Empty(), c.line)
		require.Len(t, bad, 1, c.line)
		assert.Equal(t, 3, bad[0].Line)
		assert.Equal(t, c.line, bad[0].Text)
		assert.Contains(t, bad[0].Reason, c.want, c.line)
	}
}

func TestParseMessageIgnoresUnrelatedLines(t *testing.T) {
	got, bad := ParseMessage("Spec changes are great\nSpec-Changes: $X.Yz - -> inv:00000000\nnot a Spec-Change: line\n")
	assert.True(t, got.Empty())
	assert.Empty(t, bad)
}

func TestCheckMatchesExactly(t *testing.T) {
	req := Trailers{
		Changes: []ChangeLine{{Name: "A.Bc", OldKind: "inv", OldShort: "11111111", NewKind: "inv", NewShort: "22222222"}},
		Renames: []RenameLine{{Old: "A.Bc", New: "D.Ef"}},
	}
	assert.True(t, Check(req, "Spec-Change: $A.Bc inv:11111111 -> inv:22222222\nSpec-Rename: $A.Bc -> $D.Ef\n").OK())

	// A wrong hash, a wrong kind, a swapped rename and a different name do not match.
	for _, msg := range []string{
		"Spec-Change: $A.Bc inv:11111111 -> inv:22222223\nSpec-Rename: $A.Bc -> $D.Ef\n",
		"Spec-Change: $A.Bc inv:11111111 -> def:22222222\nSpec-Rename: $A.Bc -> $D.Ef\n",
		"Spec-Change: $A.Bc inv:11111111 -> inv:22222222\nSpec-Rename: $D.Ef -> $A.Bc\n",
		"Spec-Change: $A.Bd inv:11111111 -> inv:22222222\nSpec-Rename: $A.Bc -> $D.Ef\n",
	} {
		res := Check(req, msg)
		assert.False(t, res.OK(), msg)
		assert.Len(t, res.MissingLines(), 1, msg)
	}

	res := Check(req, "")
	assert.Equal(t, req.Lines(), res.MissingLines())
	assert.Empty(t, res.Malformed)
}

func TestCheckReportsMalformedAttempts(t *testing.T) {
	req := Trailers{Changes: []ChangeLine{{Name: "A.Bc", NewKind: "inv", NewShort: "22222222"}}}
	res := Check(req, "Spec-Change:  - -> inv:22222222\n")
	assert.False(t, res.OK())
	require.Len(t, res.Malformed, 1)
	assert.Equal(t, 1, res.Malformed[0].Line)
}

func TestCheckNothingRequired(t *testing.T) {
	assert.True(t, Check(Trailers{}, "anything\n").OK())
	assert.True(t, Check(Trailers{}, "").OK())
}
