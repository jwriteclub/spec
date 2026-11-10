// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package explain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
)

// The tests in this file hold `spec explain markup` to the parser: every
// example the topic shows must appear in it verbatim and behave as it says.

func markupText(t *testing.T) string {
	t.Helper()
	text, ok := Topic("markup")
	require.True(t, ok)
	return text
}

// inDoc fails unless every line of snippet appears in the markup topic.
func inDoc(t *testing.T, doc, snippet string) {
	t.Helper()
	for _, line := range strings.Split(snippet, "\n") {
		assert.Contains(t, doc, line, "the markup topic must show this example")
	}
}

// build parses files given as path, source pairs.
func build(pairs ...string) (*mdspec.Spec, []*mdspec.File) {
	var fs []*mdspec.File
	for i := 0; i+1 < len(pairs); i += 2 {
		fs = append(fs, mdspec.ParseFile(pairs[i], []byte(pairs[i+1])))
	}
	return mdspec.Build(fs), fs
}

// refNames returns the names of all reference occurrences in f.
func refNames(f *mdspec.File) []string {
	var out []string
	for _, o := range f.Occurrences {
		if o.Role == mdspec.RoleReference {
			out = append(out, o.Name)
		}
	}
	return out
}

func TestMarkupNames(t *testing.T) {
	doc := markupText(t)
	for _, n := range []string{"Frame.Timing", "HttpTimeout", "Sdk.Lease2", "Interval"} {
		inDoc(t, doc, "`"+n+"`")
		assert.True(t, mdspec.ValidName(n), n)
	}
	for _, n := range []string{"frame.timing", "HTTP", "Frame_x", "Frame.2x"} {
		inDoc(t, doc, "`"+n+"`")
		assert.False(t, mdspec.ValidName(n), n)
	}
}

// The math hazard paragraph's examples lint as it says.
func TestMarkupMathHazard(t *testing.T) {
	doc := markupText(t)
	cases := map[string]bool{
		"$Frame.Timing/$Time.Media":    false,
		"$Frame.Timing ($Time.Media)":  false,
		"$Frame.Timing costs 5$ today": true,
		"$Frame.Timing$":               true,
	}
	for text, hazard := range cases {
		inDoc(t, doc, text)
		_, fs := build("doc.spec.md", "Some text "+text+"\n")
		var got bool
		for _, d := range lint.Run(mdspec.Build(fs), lint.Options{}) {
			got = got || d.Code == diag.CodeMathHazard
		}
		assert.Equal(t, hazard, got, text)
	}
	inDoc(t, doc, "A backslash does not help")
}

func TestMarkupReferences(t *testing.T) {
	doc := markupText(t)
	cases := []struct {
		text string
		refs []string
	}{
		{"Every fragment obeys $Frame.Timing.", []string{"Frame.Timing"}},
		{"a$Frame.Timing", nil},
		{"$$Frame.Timing", nil},
		{"($Frame.Timing)", []string{"Frame.Timing"}},
		{`"$Frame.Timing"`, []string{"Frame.Timing"}},
		{"*$Frame.Timing*", []string{"Frame.Timing"}},
		{`\$Frame.Timing`, nil},
		{"obeys $Frame.Timing.", []string{"Frame.Timing"}},
		{"$Frame.Timing's", []string{"Frame.Timing"}},
		{"$Frame.timing", nil},
		{"$HOME $PATH $5.00 $1 $USD", nil},
		{"[$Frame.Timing](notes.md)", []string{"Frame.Timing"}},
		{"$Frame.Timing/$Time.Media", []string{"Frame.Timing", "Time.Media"}},
		{"$Frame.Timing ($Time.Media)", []string{"Frame.Timing", "Time.Media"}},
		{"$Frame.Timing costs 5$ today", []string{"Frame.Timing"}},
		{"$Frame.Timing$", []string{"Frame.Timing"}},
		{"Frames obey **$Frame.Timing** too.", []string{"Frame.Timing"}},
	}
	for _, c := range cases {
		inDoc(t, doc, c.text)
		_, fs := build("doc.spec.md", "Some text "+c.text+"\n")
		assert.Equal(t, c.refs, refNames(fs[0]), c.text)
		assert.Empty(t, fs[0].Anchors, c.text)
	}

	// Maximal munch: the token stops before the final period.
	_, fs := build("doc.spec.md", "obeys $Frame.Timing.\n")
	require.Len(t, fs[0].Occurrences, 1)
	assert.Equal(t, "$Frame.Timing", fs[0].Text(fs[0].Occurrences[0].Range))
}

func TestMarkupLabels(t *testing.T) {
	doc := markupText(t)
	cases := []struct {
		text  string
		name  string
		kind  mdspec.Kind
		title string
	}{
		{"**$Frame.Timing.** Every fragment MUST have a duration in (0, 5 s].", "Frame.Timing", mdspec.KindInvariant, ""},
		{"**$Frame.Timing (fragment timing).** The same, with a title.", "Frame.Timing", mdspec.KindInvariant, "fragment timing"},
		{"**Def $Time.Media.** Media time is an integer count of 90 kHz ticks.", "Time.Media", mdspec.KindDefinition, ""},
		{"**Def $Time.Media (media time).** A definition with a title.", "Time.Media", mdspec.KindDefinition, "media time"},
		{"**$Frame.Timing**. Text", "Frame.Timing", mdspec.KindInvariant, ""},
		{"**$Frame.Timing** is strict.", "Frame.Timing", mdspec.KindInvariant, ""},
		{"**$Frame.Titled (depends on $Time.Media).**", "Frame.Titled", mdspec.KindInvariant, "depends on $Time.Media"},
	}
	for _, c := range cases {
		inDoc(t, doc, c.text)
		spec, fs := build("doc.spec.md", c.text+"\n")
		require.Len(t, fs[0].Anchors, 1, c.text)
		a := fs[0].Anchors[0]
		assert.Equal(t, c.name, a.Name, c.text)
		assert.Equal(t, c.kind, a.Kind, c.text)
		assert.Equal(t, c.title, a.Title, c.text)
		assert.Equal(t, mdspec.FormParagraph, a.Form, c.text)
		assert.NotContains(t, codes(spec), "def-malformed", c.text)
	}

	// The same label written with underscores.
	inDoc(t, doc, "`__...__` works like `**...**`")
	_, fs := build("doc.spec.md", "__$Frame.Timing.__ Text.\n")
	require.Len(t, fs[0].Anchors, 1)

	// A reference in a title is a reference, flagged as in a title.
	_, fs = build("doc.spec.md", "**$Frame.Titled (depends on $Time.Media).**\n")
	var inTitle []string
	for _, o := range fs[0].Occurrences {
		if o.Role == mdspec.RoleReference && o.InTitle {
			inTitle = append(inTitle, o.Name)
		}
	}
	assert.Equal(t, []string{"Time.Media"}, inTitle)

	// A paragraph starting with a bold reference defines the name again.
	inDoc(t, doc, "`def-duplicate`")
	spec, _ := build("doc.spec.md", "**$Frame.Timing.** Text.\n\n**$Frame.Timing** is strict.\n")
	assert.Contains(t, codes(spec), "def-duplicate")
}

func TestMarkupNestedAndMalformed(t *testing.T) {
	doc := markupText(t)
	for _, text := range []string{
		"- **$Frame.InList.** Text.",
		"> **$Frame.InQuote.** Text.",
	} {
		inDoc(t, doc, "    "+text)
		spec, fs := build("doc.spec.md", text+"\n")
		assert.Empty(t, fs[0].Anchors, text)
		assert.Contains(t, codes(spec), "def-nested", text)
	}
	spec, fs := build("doc.spec.md", "| Cell |\n|------|\n| **$Frame.InCell.** |\n")
	assert.Empty(t, fs[0].Anchors)
	assert.Contains(t, codes(spec), "def-nested")

	for _, c := range []struct{ bad, good string }{
		{"**$frame.timing.**", "**$Frame.Timing.**"},
		{"**$HTTP.**", "**$Http.Status.**"},
		{"**Definition $Time.Media.**", "**Def $Time.Media.**"},
		{"**Def. $Time.Media.**", "**Def $Time.Media.**"},
		{"**$Frame.Timing extra words.**", "**$Frame.Timing (extra words).**"},
	} {
		inDoc(t, doc, "`"+c.bad+"`")
		inDoc(t, doc, "`"+c.good+"`")
		spec, fs := build("doc.spec.md", c.bad+" Text.\n")
		assert.Empty(t, fs[0].Anchors, c.bad)
		assert.Contains(t, codes(spec), "def-malformed", c.bad)
		_, fs = build("doc.spec.md", c.good+" Text.\n")
		assert.Len(t, fs[0].Anchors, 1, c.good)
	}
}

func TestMarkupBody(t *testing.T) {
	doc := markupText(t)
	body := "**$Sdk.Preempt (preempt only as needed).** A viewer MUST preempt only when:\n" +
		"- fewer leases are preempted than viewers are waiting, and\n" +
		"- the victim is the newest analysis lease, else the oldest viewer lease."
	inDoc(t, doc, body)
	_, fs := build("doc.spec.md", body+"\n")
	require.Len(t, fs[0].Anchors, 1)
	assert.Equal(t, body, fs[0].Text(fs[0].Anchors[0].BodyRange))

	// A blank line ends the body.
	split := strings.Replace(body, ":\n-", ":\n\n-", 1)
	_, fs = build("doc.spec.md", split+"\n")
	require.Len(t, fs[0].Anchors, 1)
	assert.Equal(t, "**$Sdk.Preempt (preempt only as needed).** A viewer MUST preempt only when:",
		fs[0].Text(fs[0].Anchors[0].BodyRange))

	// A fenced block on the next line is not body.
	_, fs = build("doc.spec.md", "**$Frame.Timing.** Text.\n```\ncode\n```\n")
	require.Len(t, fs[0].Anchors, 1)
	assert.True(t, fs[0].Anchors[0].CodeAfter)
	assert.Equal(t, "**$Frame.Timing.** Text.", fs[0].Text(fs[0].Anchors[0].BodyRange))

	// Reflowing does not change the hash; code whitespace does.
	_, a := build("doc.spec.md", "**$Frame.Timing.** One two\nthree `a b`.\n")
	_, b := build("doc.spec.md", "**$Frame.Timing.** One   two three\n`a b`.\n")
	_, c := build("doc.spec.md", "**$Frame.Timing.** One two three `a  b`.\n")
	assert.Equal(t, a[0].Anchors[0].Hash, b[0].Anchors[0].Hash)
	assert.NotEqual(t, a[0].Anchors[0].Hash, c[0].Anchors[0].Hash)
}

func TestMarkupSectionForm(t *testing.T) {
	doc := markupText(t)
	for _, c := range []struct {
		heading, name, title string
		kind                 mdspec.Kind
	}{
		{"### $Relay.OneSession: one session per camera", "Relay.OneSession", "one session per camera", mdspec.KindInvariant},
		{"### Def $Relay.Term", "Relay.Term", "", mdspec.KindDefinition},
	} {
		inDoc(t, doc, "    "+c.heading)
		src := c.heading + "\n\nBody text.\n\n#### Deeper plain heading\n\nStill body.\n\n### Next\n\nOutside.\n"
		_, fs := build("doc.spec.md", src)
		require.Len(t, fs[0].Anchors, 1, c.heading)
		a := fs[0].Anchors[0]
		assert.Equal(t, mdspec.FormSection, a.Form)
		assert.Equal(t, c.name, a.Name)
		assert.Equal(t, c.title, a.Title)
		assert.Equal(t, c.kind, a.Kind)
		bodyText := fs[0].Text(a.BodyRange)
		assert.Contains(t, bodyText, "Still body.")
		assert.NotContains(t, bodyText, "Outside.")
	}

	// A label inside a section-form body is nested.
	spec, _ := build("doc.spec.md", "### $Relay.OneSession: one\n\n**$Relay.Inner.** Text.\n")
	assert.Contains(t, codes(spec), "def-nested")

	// Setext trap, and the blank line that avoids it.
	inDoc(t, doc, "    **$Frame.Setext.**\n    ---")
	_, fs := build("doc.spec.md", "**$Frame.Setext.**\n---\n")
	assert.Empty(t, fs[0].Anchors)
	_, fs = build("doc.spec.md", "**$Frame.Setext.**\n\n---\n")
	assert.Len(t, fs[0].Anchors, 1)
}

func TestMarkupSections(t *testing.T) {
	doc := markupText(t)
	for _, c := range []struct{ heading, number string }{
		{"## 1. Frames", "1"},
		{"## 5a. Retry", "5a"},
		{"### 0.1 Time", "0.1"},
		{"### 1.2. Timing", "1.2"},
		{"#### 5.2a. Retry", "5.2a"},
		{"## 10 Gbit uplink", ""},
		{"## 3 reasons", ""},
		{"## 05. Zero padded", "05"},
	} {
		inDoc(t, doc, "`"+c.heading+"`")
		_, fs := build("doc.spec.md", c.heading+"\n")
		if c.number == "" {
			assert.Empty(t, fs[0].Sections, c.heading)
			continue
		}
		require.Len(t, fs[0].Sections, 1, c.heading)
		assert.Equal(t, c.number, fs[0].Sections[0].Number, c.heading)
	}

	// Headings in lists do not count; duplicates are errors.
	_, fs := build("doc.spec.md", "- ## 1. In a list\n")
	assert.Empty(t, fs[0].Sections)
	spec, _ := build("doc.spec.md", "## 1. A\n\n## 1. B\n")
	assert.Contains(t, codes(spec), "section-dup")
}

func TestMarkupSectionRefs(t *testing.T) {
	doc := markupText(t)
	hls := "## 1. Manifests\n\n### 1.2 Segments\n\n### 5.2a Retry\n\nText.\n"
	cases := []struct {
		text   string
		number string // "" when the text holds no reference
		stem   string
	}{
		{"§5.2a", "5.2a", ""},
		{"§ 5.2a", "5.2a", ""},
		{"§hls:1.2", "1.2", "hls"},
		{"see §1.2.", "1.2", ""},
		{"§5.2ab", "", ""},
		{"#5.2a", "", ""},
	}
	for _, c := range cases {
		inDoc(t, doc, "`"+c.text+"`")
		spec, _ := build("hls.spec.md", hls+"\nRef: "+c.text+"\n")
		if c.number == "" {
			assert.Empty(t, spec.SectionRefs, c.text)
			continue
		}
		require.Len(t, spec.SectionRefs, 1, c.text)
		r := spec.SectionRefs[0]
		assert.Equal(t, c.number, r.Number, c.text)
		assert.Equal(t, c.stem, r.Stem, c.text)
		assert.Equal(t, mdspec.Resolved, r.State, c.text)
	}
	_, fs := build("hls.spec.md", hls+"\nRef: §5.2ab and #5.2a.\n")
	assert.NotEmpty(t, fs[0].MalformedSectionRefs)
	assert.NotEmpty(t, fs[0].HashRefs)

	// Resolution order.
	a := "## 1. A\n\n### 1.2 Local\n"
	b := "## 1. B\n\n### 1.2 In b\n\n### 3.1 Only in b\n"
	c := "## 1. C\n\n### 1.2 In c\n"
	resolve := func(from, text string, files ...string) *mdspec.ResolvedSectionRef {
		var pairs []string
		for _, name := range files {
			src := map[string]string{"a": a, "b": b, "c": c}[name]
			if name == from {
				src += "\nRef " + text + "\n"
			}
			pairs = append(pairs, name+".spec.md", src)
		}
		spec, _ := build(pairs...)
		require.Len(t, spec.SectionRefs, 1, text)
		return spec.SectionRefs[0]
	}
	r := resolve("a", "§1.2", "a", "b", "c")
	assert.Equal(t, mdspec.Resolved, r.State)
	assert.Equal(t, "a.spec.md", r.Target.File, "same file first")
	r = resolve("a", "§3.1", "a", "b", "c")
	assert.Equal(t, mdspec.Resolved, r.State)
	assert.Equal(t, "b.spec.md", r.Target.File, "the one file that has it")
	r = resolve("a", "§b:1.2", "a", "b", "c")
	assert.Equal(t, "b.spec.md", r.Target.File, "qualified")
	r = resolve("a", "§a:3.1", "a", "b", "c")
	assert.Equal(t, mdspec.Dangling, r.State, "qualified looks only in that file")
	r = resolve("a", "§9", "a", "b", "c")
	assert.Equal(t, mdspec.Dangling, r.State)

	// Ambiguous: two other files have 1.2 and the referencing file does not.
	spec, _ := build("b.spec.md", b, "c.spec.md", c, "d.spec.md", "Ref §1.2\n")
	require.Len(t, spec.SectionRefs, 1)
	assert.Equal(t, mdspec.Ambiguous, spec.SectionRefs[0].State)
	assert.Contains(t, codes(spec), "section-ref-ambiguous")

	// Duplicate stems.
	inDoc(t, doc, "`file-stem-duplicate`")
	spec, _ = build("x/hls.spec.md", hls, "y/hls.spec.md", hls, "z.spec.md", "Ref §hls:1.2\n")
	assert.Contains(t, codes(spec), "file-stem-duplicate")
	require.Len(t, spec.SectionRefs, 1)
	assert.Equal(t, mdspec.Ambiguous, spec.SectionRefs[0].State)
}

func TestMarkupOpaque(t *testing.T) {
	src := "---\nkey: $Frame.Timing\n---\n\nCode `$Frame.Timing` and <span title=\"$Frame.Timing\">x</span> and " +
		"<https://x.test/$Frame.Timing> and [text](https://x.test/$Frame.Timing \"$Frame.Timing\").\n\n" +
		"```\n$Frame.Timing §1\n```\n\n    $Frame.Timing indented\n"
	_, fs := build("doc.spec.md", src)
	assert.Empty(t, refNames(fs[0]))
	assert.Empty(t, fs[0].SectionRefs)
}
