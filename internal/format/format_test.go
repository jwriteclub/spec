// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
)

func fmtString(t *testing.T, src string, width int) string {
	t.Helper()
	res, err := Source("a.spec.md", []byte(src), Options{Width: width})
	require.NoError(t, err)
	again, err := Source("a.spec.md", res.Text, Options{Width: width})
	require.NoError(t, err)
	assert.Equal(t, string(res.Text), string(again.Text), "idempotent")
	assert.Zero(t, again.Changed, "idempotent")
	return string(res.Text)
}

func TestWrapParagraph(t *testing.T) {
	src := "# T\n\nOne two three four five six seven eight nine ten eleven twelve thirteen fourteen.\n"
	got := fmtString(t, src, 30)
	assert.Equal(t, "# T\n\nOne two three four five six\nseven eight nine ten eleven\ntwelve thirteen fourteen.\n", got)

	// Short lines are joined.
	got = fmtString(t, "One\ntwo\nthree.\n", 30)
	assert.Equal(t, "One two three.\n", got)
}

func TestWhitespace(t *testing.T) {
	src := "\n\n# T   \n\n\n\nText.  \n\n\n## 1. Next\t\n\nMore.\n\n\n"
	assert.Equal(t, "# T\n\nText.\n\n## 1. Next\n\nMore.\n", fmtString(t, src, 100))
	assert.Equal(t, "Text.\n", fmtString(t, "Text.", 100), "ends with one newline")
	assert.Equal(t, "", fmtString(t, "", 100))
	assert.Equal(t, "", fmtString(t, "\n\n", 100))
}

func TestAlreadyFormatted(t *testing.T) {
	src := "# T\n\nText that fits.\n"
	res, err := Source("a.spec.md", []byte(src), Options{})
	require.NoError(t, err)
	assert.Zero(t, res.Changed)
	assert.Equal(t, src, string(res.Text))

	res, err = Source("a.spec.md", []byte("# T\n\n\n\nText   \n"), Options{})
	require.NoError(t, err)
	assert.Equal(t, 3, res.Changed, "two dropped blank lines and a stripped line")
}

// Code blocks, tables, HTML, headings and link reference definitions are
// left alone: not wrapped, and code keeps its trailing spaces and blank
// lines.
func TestLeftAlone(t *testing.T) {
	long := strings.Repeat("word ", 12)
	src := "## 1. A heading that is " + long + "long\n\n" +
		"```\ncode " + long + "  \n\n\nmore\n```\n\n" +
		"    indented code " + long + "\n\n" +
		"| a | b |\n|---|---|\n| " + long + " | x |\n\n" +
		"<div>\n" + long + "  \n\n</div>\n\n" +
		"[ref]: http://example.com/" + strings.Repeat("x", 40) + "  \"a title " + long + "\"\n"
	assert.Equal(t, src, fmtString(t, src, 30))
}

// A wrap never breaks inside a $Name, a § reference, a code span, a link
// or an anchor label.
func TestUnbreakable(t *testing.T) {
	src := "**Def $Time.Media (the media time base).** Uses `a b c d e f`, [the link text](http://x/y \"t t\") and § 1.\n\n## 1. One\n"
	got := fmtString(t, src, 12)
	for _, keep := range []string{"**Def $Time.Media (the media time base).**", "`a b c d e f`,", "[the link text](http://x/y \"t t\")", "§ 1."} {
		assert.Contains(t, got, keep)
	}
	for _, l := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if !strings.ContainsAny(l, "`[*§") {
			assert.LessOrEqual(t, ansi.StringWidth(l), 12, l)
		}
	}
}

// A paragraph with a link or inline code still wraps, around them.
func TestWrapAroundLinks(t *testing.T) {
	src := "Read [the guide](http://example.com/guide) and `the code` before you change anything here.\n"
	assert.Equal(t, "Read\n[the guide](http://example.com/guide)\nand `the code` before you change\nanything here.\n", fmtString(t, src, 40),
		"the link is 37 columns: it moves to its own line, whole")
}

func TestLists(t *testing.T) {
	src := "**$Frame.Timing.** A viewer MUST:\n- preempt only when fewer leases are preempted than viewers wait, and\n- pick the newest lease.\n  1. nested item text that is long enough to wrap around\n\nAfter.\n"
	got := fmtString(t, src, 30)
	want := "**$Frame.Timing.** A viewer\nMUST:\n" +
		"- preempt only when fewer\n  leases are preempted than\n  viewers wait, and\n" +
		"- pick the newest lease.\n" +
		"  1. nested item text that is\n     long enough to wrap\n     around\n\nAfter.\n"
	assert.Equal(t, want, got)
}

// A word that would start a list, heading, quote, table or fence stays on
// the line before.
func TestNoBlockStarts(t *testing.T) {
	for _, w := range []string{"-", "+", "*", "1.", "2)", "#", "##", ">x", "|", "```x", "~~~", "<div>", "---", "===", "***", ":-:"} {
		src := "aaaa bbbb " + w + " cccc\n"
		got := fmtString(t, src, 9)
		for _, l := range strings.Split(got, "\n") {
			assert.False(t, strings.HasPrefix(l, w), "%q starts a line in %q", w, got)
		}
	}
	// A backslash at the end of a line would be a hard break.
	got := fmtString(t, "aaaa bbb\\ cccc\n", 9)
	assert.NotContains(t, got, "\\\n")
}

func TestHardBreaks(t *testing.T) {
	src := "one two  \nthree four five six seven\\\neight\n"
	got := fmtString(t, src, 12)
	assert.Equal(t, "one two  \nthree four\nfive six\nseven\\\neight\n", got)
}

// Quotes are left as written, apart from trailing whitespace.
func TestQuotes(t *testing.T) {
	src := "> one two three four five six seven \n> eight\n"
	assert.Equal(t, "> one two three four five six seven\n> eight\n", fmtString(t, src, 10))
}

// Formatting is whitespace only: the math hazard stays for ^P to fix.
func TestMathHazardUntouched(t *testing.T) {
	src := "$Frame.Timing costs 5$ today.\n"
	assert.Equal(t, src, fmtString(t, src, 100))
}

func TestBOM(t *testing.T) {
	src := "\xef\xbb\xbfone\ntwo\n"
	assert.Equal(t, "\xef\xbb\xbfone two\n", fmtString(t, src, 100))
}

func TestRefuses(t *testing.T) {
	_, err := Source("a.spec.md", []byte("a\r\nb\r\n"), Options{})
	var re *RefusedError
	require.True(t, errors.As(err, &re))
	assert.Contains(t, err.Error(), "CR line endings")
	_, err = Source("a.spec.md", []byte("a \xff\n"), Options{})
	require.True(t, errors.As(err, &re))
	assert.Contains(t, err.Error(), "UTF-8")
}

// Verify catches each kind of change.
func TestVerify(t *testing.T) {
	base := "**$Frame.Timing (t).** Body text here.\n\nProse.\n"
	cases := map[string]string{
		"a list":         strings.Replace(base, "Prose.", "- Prose.", 1),
		"the hash":       strings.Replace(base, "Body text", "Body texts", 1),
		"anchors":        strings.Replace(base, "**$Frame.Timing (t).**", "$Frame.Timing (t).", 1),
		"the title":      strings.Replace(base, "(t)", "(u)", 1),
		"a heading":      strings.Replace(base, "Prose.", "Prose.\n---", 1),
		"a hard break":   strings.Replace(base, "Body text", "Body  \ntext", 1),
		"code bytes":     "`a  b`\n",
		"list tightness": "- a\n- b\n",
	}
	before := map[string]string{"code bytes": "`a b`\n", "list tightness": "- a\n\n- b\n"}
	for name, after := range cases {
		b := base
		if s, ok := before[name]; ok {
			b = s
		}
		if name == "code bytes" {
			assert.Empty(t, Verify("a.spec.md", []byte(b), []byte(after)), "code spans are not anchors: no hash to compare")
			continue
		}
		assert.NotEmpty(t, Verify("a.spec.md", []byte(b), []byte(after)), name)
	}
	assert.Empty(t, Verify("a.spec.md", []byte(base), []byte(strings.Replace(base, "Body text here.", "Body\ntext   here.", 1))))
}

// Every spec file and explain topic in the repository formats without a
// refusal, keeps its lint findings, and formats idempotently.
func TestRepositoryFiles(t *testing.T) {
	root := filepath.Join("..", "..")
	var paths []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".claude" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, ".spec.md") || (!d.IsDir() && strings.HasSuffix(p, ".md") && strings.Contains(p, filepath.Join("explain", "topics"))) {
			paths = append(paths, p)
		}
		return nil
	}))
	require.NotEmpty(t, paths)
	formatted := 0
	for _, p := range paths {
		src, err := os.ReadFile(p)
		require.NoError(t, err)
		res, err := Source(p, src, Options{Width: 72})
		var re *RefusedError
		if errors.As(err, &re) && (strings.Contains(re.Reason, "UTF-8") || strings.Contains(re.Reason, "CR line")) {
			continue // fixtures of bad files
		}
		require.NoError(t, err, p)
		if res.Changed > 0 {
			formatted++
		}
		again, err := Source(p, res.Text, Options{Width: 72})
		require.NoError(t, err, p)
		assert.Zero(t, again.Changed, "idempotent: %s", p)
		assert.Equal(t, lintCodes(p, src), lintCodes(p, res.Text), p)
	}
	assert.Positive(t, formatted)
}

// lintCodes counts the findings of one file by code, the line-based rules
// left out: a reflow may move a $ onto a reference's line (math-hazard) or
// make a body longer than 25 lines at a narrow width (anchor-long).
func lintCodes(path string, src []byte) map[string]int {
	f := mdspec.ParseFile(path, src)
	out := map[string]int{}
	for _, d := range lint.Run(mdspec.Build([]*mdspec.File{f}), lint.Options{}) {
		if d.Code != diag.CodeMathHazard && d.Code != diag.CodeAnchorLong {
			out[d.Code]++
		}
	}
	return out
}
