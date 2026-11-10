// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tla

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/formal"
)

// recoverModel has declarations and tags on both sides of where the tests
// break it.
const recoverModel = `---- MODULE M ----
EXTENDS Naturals
VARIABLES x, y
\* Spec: $A.One "one"
Init == x = 0 /\ y = 0
\* Spec: $A.Two "two"
Next == x' = x + 1 /\ UNCHANGED y
\* Spec: $A.Three "three"
Inv == x >= 0
(* Spec: $A.Four "four" *)
Other == TRUE
====
`

// edited returns recoverModel with the first old replaced by new.
func edited(t *testing.T, old, new string) string {
	t.Helper()
	require.Contains(t, recoverModel, old)
	return strings.Replace(recoverModel, old, new, 1)
}

func declNames(ds []*formal.Decl) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

// placed checks what may be jumped to or linked in a recovered file: every
// declaration's name and every tag's reference sit at their ranges in the
// current text, and every tag is bound to one of the file's own declarations.
func placed(t *testing.T, f *formal.File) {
	t.Helper()
	own := map[*formal.Decl]bool{}
	for _, d := range f.Decls {
		own[d] = true
		assert.Equal(t, d.Name, f.Text(d.NameRange), "declaration %s", d.Name)
		assert.True(t, d.Range.ContainsRange(d.NameRange), "declaration %s", d.Name)
	}
	for _, t2 := range f.Tags {
		if t2.Ref != nil && t2.Ref.IsAnchor() {
			assert.Equal(t, "$"+t2.Ref.Name, f.Text(t2.Ref.Range))
		}
		assert.True(t, strings.Contains(f.Text(t2.Range), string(t2.Kind)+":"), "tag at %v", t2.Range)
		if t2.Decl != nil {
			assert.True(t, own[t2.Decl], "tag %s bound to a declaration the file does not hold", t2.Ref.Written())
		}
	}
	for _, d := range f.Stale {
		assert.Nil(t, f.DeclAt(d.NameRange.Start), "a stale declaration is no lookup target")
	}
}

// TestRecoverKeepsDeclarationsAfterAnError: an unbalanced parenthesis in Next
// stops the parse there; the declarations and tags after it come back from
// the last good scan, moved by the edit, and bound as before.
func TestRecoverKeepsDeclarationsAfterAnError(t *testing.T) {
	good := Scan("tla/M.tla", []byte(recoverModel))
	require.False(t, good.HasError())
	require.Equal(t, []string{"x", "y", "Init", "Next", "Inv", "Other"}, declNames(good.Decls))

	cur := Scan("tla/M.tla", []byte(edited(t, "Next == x'", "Next == ((x'")))
	require.True(t, cur.HasError())
	require.Less(t, len(cur.Decls), len(good.Decls), "the parse stops at the error")

	got := formal.Recover(cur, good)
	assert.Equal(t, declNames(good.Decls), declNames(got.Decls))
	assert.Empty(t, got.Stale)
	assert.Equal(t, boundTo(good), boundTo(got))
	assert.Equal(t, cur.Syntax, got.Syntax, "the error is still reported")
	assert.Equal(t, cur.Tokens, got.Tokens, "highlighting follows the current text")
	assert.Same(t, &cur.Source()[0], &got.Source()[0])
	placed(t, got)

	// Neither scan changed: a scan cache shares them.
	assert.Equal(t, []string{"x", "y", "Init", "Next", "Inv", "Other"}, declNames(good.Decls))
	assert.Less(t, len(cur.Decls), len(good.Decls))
	assert.Empty(t, cur.Stale)
}

// TestRecoverMarksAnEditedNameStale: when the edit touches a declaration's
// name, its place is unknown: it is kept as stale, outside Decls, and the tag
// bound to it is unbound rather than pointing at a wrong range.
func TestRecoverMarksAnEditedNameStale(t *testing.T) {
	good := Scan("tla/M.tla", []byte(recoverModel))
	cur := Scan("tla/M.tla", []byte(edited(t, "Inv == x", "In(v == x")))
	require.True(t, cur.HasError())

	got := formal.Recover(cur, good)
	assert.NotContains(t, declNames(got.Decls), "Inv")
	assert.Equal(t, []string{"Inv"}, declNames(got.Stale))
	assert.Contains(t, declNames(got.Decls), "Other")
	assert.Contains(t, boundTo(got), "$A.Three -> (none)")
	assert.Contains(t, boundTo(got), "$A.Four -> Other")
	placed(t, got)
}

// TestRecoverLeavesGoodScansAlone: a scan with no error, or no good scan to
// draw on, is returned as it is.
func TestRecoverLeavesGoodScansAlone(t *testing.T) {
	good := Scan("tla/M.tla", []byte(recoverModel))
	other := Scan("tla/M.tla", []byte(edited(t, "Other == TRUE", "Other == FALSE")))
	assert.Same(t, other, formal.Recover(other, good))
	bad := Scan("tla/M.tla", []byte(edited(t, "Next == x'", "Next == ((x'")))
	assert.Same(t, bad, formal.Recover(bad, nil))
	assert.Same(t, bad, formal.Recover(bad, bad), "a scan with an error is no good scan")
	assert.Same(t, bad, formal.Recover(bad, Scan("tla/N.tla", []byte(recoverModel))), "another file")
}

// TestRecoverPlacesEverythingExactly breaks the model at every line with
// several edits; whatever Recover keeps lies where it says.
func TestRecoverPlacesEverythingExactly(t *testing.T) {
	good := Scan("tla/M.tla", []byte(recoverModel))
	lines := strings.SplitAfter(recoverModel, "\n")
	for i := 1; i < len(lines)-2; i++ {
		for _, ins := range []string{"(", "((", "==", "(*", "\"", ")"} {
			for _, at := range []int{0, len(lines[i]) / 2} {
				var b strings.Builder
				for j, l := range lines {
					if j == i {
						l = l[:at] + ins + l[at:]
					}
					b.WriteString(l)
				}
				cur := Scan("tla/M.tla", []byte(b.String()))
				got := formal.Recover(cur, good)
				if !cur.HasError() {
					assert.Same(t, cur, got)
					continue
				}
				t.Run(strings.TrimSpace(lines[i])+" "+ins, func(t *testing.T) {
					placed(t, got)
					kept := append(declNames(got.Decls), declNames(got.Stale)...)
					for _, d := range good.Decls {
						assert.Contains(t, kept, d.Name, "every declaration is kept")
					}
				})
			}
		}
	}
}

// TestScanTokensAreLexTokens: the highlight Scan keeps from its one prudence
// call is what Lex gives, so the UI need not lex a model again.
func TestScanTokensAreLexTokens(t *testing.T) {
	for _, src := range []string{
		recoverModel,
		edited(t, "Next == x'", "Next == ((x'"),
		edited(t, "Inv == x", "(* Inv == x"),
		"preamble\n" + recoverModel + "trailer\n",
		"no module at all\n",
		"",
	} {
		assert.Equal(t, Lex([]byte(src)), Scan("tla/M.tla", []byte(src)).Tokens, "%q", src)
	}
	bad := []byte("---- MODULE M ----\nA == \"\xff\"\n====\n")
	assert.Equal(t, Lex(bad), Scan("tla/M.tla", bad).Tokens, "not UTF-8")
}
