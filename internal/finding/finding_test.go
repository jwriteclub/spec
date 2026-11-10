// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package finding

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/pos"
)

func str(s string) *string { return &s }

const doc = "**$A.Rule.** Timeout is 30 s.\n\nThe client waits 60 s. The client waits 60 s.\n\nRetry after 5 s.\n"

func sources() map[string][]byte {
	return map[string][]byte{"specs/a.spec.md": []byte(doc), "b.spec.md": []byte("nothing\n")}
}

func TestVerify(t *testing.T) {
	r := &Result{Findings: []Finding{
		{File: "specs/a.spec.md", Issue: "numeric_mismatch", Severity: "breaking", Explanation: "e", Find: str("Retry after 5 s."), Replace: str("Retry after 30 s.")},
		{File: "a.spec.md", Issue: "Numeric Mismatch", Severity: "HIGH", Find: str("Timeout is 30 s."), Replace: str("Timeout is 60 s.")},
		{File: "specs/a.spec.md", Find: str("Timeout is 90 s."), Replace: str("x")},
		{File: "specs/a.spec.md", Find: str("The client waits 60 s."), Replace: str("y")},
		{File: "specs/a.spec.md", Explanation: "no edit"},
		{File: "nope.spec.md", Find: str("nothing"), Replace: str("x")},
		{File: "specs/a.spec.md", Find: str("after 5"), Replace: str("after 6")},
		{File: "specs/a.spec.md", Find: str("Retry"), Replace: str("Retry")},
		{File: "specs/a.spec.md", Find: str("Retry after")},
		{File: "./b.spec.md", Issue: "weird", Severity: "nonsense", Find: str("nothing"), Replace: str("something")},
	}}
	protected := map[string][]pos.Range{"specs/a.spec.md": {{Start: 0, End: 12}}}
	hunks, notes := Verify(r, sources(), Options{FirstN: 2, Protected: protected})

	require.Len(t, hunks, 3)
	assert.Equal(t, 2, hunks[0].N)
	assert.Equal(t, "specs/a.spec.md", hunks[0].File)
	assert.Equal(t, "Retry after 5 s.", doc[hunks[0].Range.Start:hunks[0].Range.End])
	// Resolved by base name, normalized issue and severity alias.
	assert.Equal(t, 3, hunks[1].N)
	assert.Equal(t, "specs/a.spec.md", hunks[1].File)
	assert.Equal(t, IssueNumericMismatch, hunks[1].Issue)
	assert.Equal(t, SeverityBreaking, hunks[1].Severity)
	// Unknown issue and severity fall back.
	assert.Equal(t, "b.spec.md", hunks[2].File)
	assert.Equal(t, IssueAmbiguity, hunks[2].Issue)
	assert.Equal(t, SeverityInconsistent, hunks[2].Severity)

	reasons := []string{}
	for _, n := range notes {
		reasons = append(reasons, n.Reason)
	}
	assert.Equal(t, []string{
		notePhantom,
		"the quoted text occurs 2 times in the file, so the edit is ambiguous",
		noteNoFind,
		noteNoFile,
		"the edit overlaps hunk 2",
		noteNoop,
		noteMissingReplacement,
	}, reasons)
	assert.Equal(t, "Timeout is 90 s.", notes[0].Find, "a phantom note keeps the quoted text")

	// The protected range turns an edit into a note.
	r = &Result{Findings: []Finding{{File: "specs/a.spec.md", Find: str("**$A.Rule.**"), Replace: str("**$A.Rule2.**")}}}
	hunks, notes = Verify(r, sources(), Options{Protected: protected})
	assert.Empty(t, hunks)
	require.Len(t, notes, 1)
	assert.Equal(t, noteProtected, notes[0].Reason)
}

func TestResultUnmarshalLenient(t *testing.T) {
	var r Result
	require.NoError(t, json.Unmarshal([]byte(`{"summary":"s","findings":[{"file":"a","anchor":"A.Rule","find":null,"severity":3},"junk"],"questions":["q1",{"id":"q-02","question":"q2"},null,7]}`), &r))
	assert.Equal(t, "s", r.Summary)
	require.Len(t, r.Findings, 1)
	assert.Nil(t, r.Findings[0].Find)
	assert.Equal(t, "", r.Findings[0].Severity)
	assert.Equal(t, []string{"q1", "q2"}, r.Questions)

	cleaned := clean(r.Findings[0])
	assert.Equal(t, "$A.Rule", cleaned.Anchor)

	var empty Result
	assert.Error(t, json.Unmarshal([]byte(`{"ok":true}`), &empty), "an object with none of the keys is not a result")
	require.NoError(t, json.Unmarshal([]byte(`{"summary":"fine","findings":null,"questions":null}`), &empty))
	assert.Empty(t, empty.Findings)
}

func TestCleanQuestions(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, CleanQuestions([]string{" a ", "", "b", "a"}))
	assert.Equal(t, []string{}, CleanQuestions(nil))
}

func TestBreaking(t *testing.T) {
	assert.False(t, Breaking(nil, nil))
	assert.True(t, Breaking([]Hunk{{Severity: SeverityBreaking}}, nil))
	assert.True(t, Breaking(nil, []Note{{Severity: SeverityBreaking}}))
	assert.False(t, Breaking([]Hunk{{Severity: SeverityCosmetic}}, []Note{{Severity: SeverityInconsistent}}))
}

// TestVerifyWrappedQuote: a model that quotes a hard-wrapped sentence with
// its line breaks turned into spaces still gets a hunk, rebuilt on the
// file's own text so the wrapping outside the change survives.
func TestVerifyWrappedQuote(t *testing.T) {
	src := "- **Viewer rotation.** With more viewers than slots, each newcomer\n" +
		"  takes the oldest viewer's slot and that viewer waits out its 60 s grace ($Hub.TakenOver), so playback\n" +
		"  rotates.\n\nTwice: a b\nc d. Twice: a  b c d.\n"
	files := map[string][]byte{"x.spec.md": []byte(src)}
	r := &Result{Findings: []Finding{
		{File: "x.spec.md", Issue: "numeric_mismatch",
			Find:    str("each newcomer takes the oldest viewer's slot and that viewer waits out its 60 s grace ($Hub.TakenOver)"),
			Replace: str("each newcomer takes the oldest viewer's slot and that viewer waits out its 30 s grace ($Hub.TakenOver)")},
		{File: "x.spec.md", Find: str("a b c d"), Replace: str("a b c e")},
		{File: "x.spec.md", Find: str("so playback rotates and stops"), Replace: str("so playback stops")},
	}}
	hunks, notes := Verify(r, files, Options{})
	require.Len(t, hunks, 1)
	h := hunks[0]
	assert.Equal(t, "each newcomer\n  takes the oldest viewer's slot and that viewer waits out its 60 s grace ($Hub.TakenOver)", h.Find)
	assert.Equal(t, "each newcomer\n  takes the oldest viewer's slot and that viewer waits out its 30 s grace ($Hub.TakenOver)", h.Replace)
	assert.Equal(t, h.Find, src[h.Range.Start:h.Range.End])
	require.Len(t, notes, 2)
	assert.Equal(t, "the quoted text occurs 2 times in the file, so the edit is ambiguous", notes[0].Reason)
	assert.Equal(t, notePhantom, notes[1].Reason)
}

func TestReflow(t *testing.T) {
	for _, c := range []struct{ find, replace, actual, want string }{
		// Change after the wrap.
		{"one two three", "one two four", "one\n  two three", "one\n  two four"},
		// Change before the wrap.
		{"one two three", "uno two three", "one two\nthree", "uno two\nthree"},
		// The change spans the wrap: the model's own spacing is used there.
		{"one two three", "one 2 3", "one two\nthree", "one 2 3"},
		// Words added at a wrap point.
		{"one two", "one and two", "one\ntwo", "one\nand two"},
		// Deleting the last word.
		{"one two three", "one two", "one two\n three", "one two"},
		// The whole text replaced.
		{"a b", "c", "a\nb", "c"},
	} {
		assert.Equal(t, c.want, reflow(c.find, c.replace, c.actual), "%q -> %q in %q", c.find, c.replace, c.actual)
	}
	assert.Equal(t, -1, mapOffset("a b", "a-b", 3), "texts that do not line up")
}

// FuzzReflow: reflow never panics, keeps the file's text when nothing
// changes, and its result differs from actual only where replace differs
// from find.
func FuzzReflow(f *testing.F) {
	f.Add("one two three", "one two four")
	f.Add("a  b\tc", "a b")
	f.Add(" x ", "")
	f.Fuzz(func(t *testing.T, find, replace string) {
		find = strings.Trim(find, " \t\r\n")
		actual := strings.Join(words(find), "\n  ")
		if len(words(find)) < 2 {
			return
		}
		got := reflow(find, replace, actual)
		if replace == find {
			assert.Equal(t, actual, got)
		}
		if replace != find && strings.Join(words(got), " ") != strings.Join(words(replace), " ") && got != "" {
			t.Fatalf("reflow(%q, %q, %q) = %q: not replace up to whitespace", find, replace, actual, got)
		}
	})
}

// TestVerifyNoteFiles: a note's file is resolved as a hunk's is (a leading
// "./", a directory the sources do not have), so the UI can jump to it; a
// name that matches no source is kept as given.
func TestVerifyNoteFiles(t *testing.T) {
	r := &Result{Findings: []Finding{
		{File: "./b.spec.md", Explanation: "no edit"},
		{File: "docs/a.spec.md", Explanation: "no edit"},
		{File: "a.spec.md", Find: str("Timeout is 90 s."), Replace: str("x")},
		{File: "a.spec.md", Find: str("The client waits 60 s."), Replace: str("y")},
		{File: "nope.spec.md", Explanation: "unknown file"},
		{File: "nope.spec.md", Find: str("nothing"), Replace: str("x")},
	}}
	hunks, notes := Verify(r, sources(), Options{})
	assert.Empty(t, hunks)
	var files, reasons []string
	for _, n := range notes {
		files = append(files, n.File)
		reasons = append(reasons, n.Reason)
	}
	assert.Equal(t, []string{"b.spec.md", "specs/a.spec.md", "specs/a.spec.md", "specs/a.spec.md", "nope.spec.md", "nope.spec.md"}, files)
	assert.Equal(t, []string{noteNoFind, noteNoFind, notePhantom, "the quoted text occurs 2 times in the file, so the edit is ambiguous", noteNoFind, noteNoFile}, reasons)
}

func TestVerifyFixedBy(t *testing.T) {
	// Indices are positions in the reply: 0 is junk and is dropped; 3, 5
	// and 6 become hunks 2, 3 and 4.
	var r Result
	require.NoError(t, json.Unmarshal([]byte(`{"summary":"s","findings":[
		"junk",
		{"file":"specs/a.spec.md","explanation":"one fix","fixed_by":[3]},
		{"file":"specs/a.spec.md","explanation":"bad links","fixed_by":[2, 0, 99, 1, -1, 1.5, "x", 8]},
		{"file":"specs/a.spec.md","explanation":"retry","find":"Retry after 5 s.","replace":"Retry after 60 s."},
		{"file":"specs/a.spec.md","explanation":"several fixes","fixed_by":[3, "5", 6, 3]},
		{"file":"specs/a.spec.md","explanation":"timeout","find":"Timeout is 30 s.","replace":"Timeout is 60 s.","fixed_by":[3]},
		{"file":"b.spec.md","explanation":"nothing","find":"nothing","replace":"something","fixed_by":6},
		{"file":"specs/a.spec.md","explanation":"no fix"},
		{"file":"specs/a.spec.md","explanation":"phantom","find":"Retry after 7 s.","replace":"x"}
	]}`), &r))
	require.Len(t, r.Findings, 8)
	assert.Equal(t, []int{2}, r.Findings[0].FixedBy, "indices are remapped past the dropped junk entry")
	assert.Equal(t, []int{1, 0, 7}, r.Findings[1].FixedBy, "the junk, out-of-range and malformed entries are dropped")
	assert.Equal(t, []int{2, 4, 5, 2}, r.Findings[3].FixedBy)
	assert.Equal(t, []int{5}, r.Findings[5].FixedBy, "a bare number is a list of one")

	hunks, notes := Verify(&r, sources(), Options{FirstN: 2})
	require.Len(t, hunks, 3)
	assert.Equal(t, []int{2, 3, 4}, []int{hunks[0].N, hunks[1].N, hunks[2].N})
	assert.Empty(t, hunks[0].FixedBy)
	assert.Equal(t, []int{2}, hunks[1].FixedBy, "a hunk may need another hunk too")
	assert.Empty(t, hunks[2].FixedBy, "a link to itself is dropped")
	got := map[string][]int{}
	for _, n := range notes {
		got[n.Explanation] = n.FixedBy
	}
	assert.Equal(t, map[string][]int{
		"one fix":       {2},
		"bad links":     nil, // self, a note, and a finding whose edit failed
		"several fixes": {2, 3, 4},
		"no fix":        nil,
		"phantom":       nil,
	}, got)

	b, err := json.Marshal(notes[0])
	require.NoError(t, err)
	assert.Contains(t, string(b), `"fixed_by":[2]`)
	b, err = json.Marshal(notes[1])
	require.NoError(t, err)
	assert.NotContains(t, string(b), "fixed_by", "a standalone note has no fixed_by")
}

// TestIndexRejectsNull: a JSON null is not an index; Unmarshal would leave
// it as 0 and link to the first finding.
func TestIndexRejectsNull(t *testing.T) {
	_, ok := index(json.RawMessage("null"))
	assert.False(t, ok)
	n, ok := index(json.RawMessage(`"4"`))
	assert.True(t, ok)
	assert.Equal(t, 4, n)
}

// A rethink's primary block decodes; a null, absent, malformed or empty
// one is nil.
func TestResultPrimary(t *testing.T) {
	var r Result
	require.NoError(t, json.Unmarshal([]byte(`{"summary":"s","findings":[],"primary":{"find":"6","replace":"5","explanation":"notes"}}`), &r))
	require.NotNil(t, r.Primary)
	assert.Equal(t, PrimaryEdit{Find: "6", Replace: "5", Explanation: "notes"}, *r.Primary)
	for _, raw := range []string{`{"summary":"s"}`, `{"summary":"s","primary":null}`, `{"summary":"s","primary":"6 to 5"}`, `{"summary":"s","primary":{"replace":"5"}}`} {
		r = Result{}
		require.NoError(t, json.Unmarshal([]byte(raw), &r), raw)
		assert.Nil(t, r.Primary, raw)
	}
}
