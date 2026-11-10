// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package explain

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	_ "github.com/jwriteclub/spec/internal/golden"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
)

var indexLine = regexp.MustCompile(`(?m)^\s+spec explain ([a-z]+)\s`)

// TestIndexNamesEveryTopic checks that the index names exactly the topics in
// Names(), in order, and that each one exists.
func TestIndexNamesEveryTopic(t *testing.T) {
	idx := Index()
	require.NotEmpty(t, idx)
	var listed []string
	for _, m := range indexLine.FindAllStringSubmatch(idx, -1) {
		listed = append(listed, m[1])
	}
	assert.Equal(t, Names(), listed)
	for _, name := range listed {
		text, ok := Topic(name)
		assert.True(t, ok, "topic %q is in the index but does not exist", name)
		assert.NotEmpty(t, text, name)
	}
}

// TestEveryTopicFileIsListed checks that no embedded topic is missing from
// the index.
func TestEveryTopicFileIsListed(t *testing.T) {
	entries, err := files.ReadDir("topics")
	require.NoError(t, err)
	known := map[string]bool{"index": true}
	for _, n := range Names() {
		known[n] = true
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		assert.True(t, known[name], "topics/%s is not in Names()", e.Name())
	}
	assert.Len(t, entries, len(Names())+1)
}

// TestSummaries checks that every topic gets its index line as a summary.
func TestSummaries(t *testing.T) {
	sums, err := Summaries()
	require.NoError(t, err)
	require.Len(t, sums, len(Names()))
	for i, n := range Names() {
		assert.Equal(t, n, sums[i].Name)
		assert.NotEmpty(t, sums[i].Summary, n)
		assert.Contains(t, Index(), "spec explain "+n, n)
		assert.True(t, strings.HasSuffix(strings.TrimRight(lineWith(Index(), "spec explain "+n+" "), " "), sums[i].Summary), n)
	}
}

// TestAdoptIsOneScreen checks that `spec adopt` names every topic and stays
// about one screen long.
func TestAdoptIsOneScreen(t *testing.T) {
	text, err := Adopt()
	require.NoError(t, err)
	for _, n := range Names() {
		assert.Contains(t, text, "spec explain "+n+" ", n)
	}
	lines := strings.Count(text, "\n")
	assert.LessOrEqual(t, lines, 60)
	assert.NotContains(t, text, "\n\n\n")
}

// lineWith returns the first line of text that contains sub.
func lineWith(text, sub string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestIndexIsShort(t *testing.T) {
	lines := strings.Count(strings.TrimSpace(Index()), "\n") + 1
	assert.LessOrEqual(t, lines, 19)
}

func TestTopicUnknown(t *testing.T) {
	_, ok := Topic("nope")
	assert.False(t, ok)
	_, ok = Topic("index")
	assert.False(t, ok, "the index is Index(), not a topic")
	_, ok = Topic("../explain")
	assert.False(t, ok)
}

func TestNamesIsACopy(t *testing.T) {
	n := Names()
	n[0] = "changed"
	assert.Equal(t, "method", Names()[0])
}

// TestHeadersStripped checks that no printed text carries the copyright
// comment or leading blank lines.
func TestHeadersStripped(t *testing.T) {
	texts := map[string]string{"index": Index(), "snippet": ClaudeSnippet()}
	for _, n := range Names() {
		text, _ := Topic(n)
		texts[n] = text
	}
	for name, text := range texts {
		assert.NotContains(t, text, "Copyright", name)
		assert.NotContains(t, text, "<!--", name)
		assert.False(t, strings.HasPrefix(text, "\n"), name)
		assert.True(t, strings.HasSuffix(text, "\n"), name)
	}
}

func TestStripHeader(t *testing.T) {
	assert.Equal(t, "# T\n", stripHeader("<!-- a -->\n<!-- b -->\n\n# T\n"))
	assert.Equal(t, "# T\n", stripHeader("# T\n"))
	assert.Equal(t, "<!-- open", stripHeader("<!-- open"))
	assert.Equal(t, "", stripHeader("<!-- a -->\n"))
}

func TestNoSigilInCommandExamples(t *testing.T) {
	// Every command example must use the sigil-free form (plan 5). A `$`
	// right after a spec subcommand would be expanded by the shell.
	bad := regexp.MustCompile(`spec (show|refs|rename|ls|explain)[^\n` + "`" + `]*?\s\$[A-Z]`)
	texts := map[string]string{"index": Index(), "snippet": ClaudeSnippet()}
	for _, n := range Names() {
		text, _ := Topic(n)
		texts[n] = text
	}
	sk, err := Skeleton("demo")
	require.NoError(t, err)
	texts["skeleton"] = sk
	adopt, err := Adopt()
	require.NoError(t, err)
	texts["adopt"] = adopt
	for name, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "runs `spec show .Timing`") {
				continue // the markup topic's illustration of the mistake
			}
			assert.False(t, bad.MatchString(line), "%s: %q", name, line)
		}
	}
}

func TestClaudeSnippet(t *testing.T) {
	s := ClaudeSnippet()
	assert.True(t, strings.HasPrefix(s, "## Specs\n"))
	assert.Contains(t, s, "spec explain agent")
	assert.Contains(t, s, "spec status")
	assert.LessOrEqual(t, strings.Count(s, "\n"), 12)
}

// TestTextsNameNoPerson checks that the embedded texts, which every user of
// spec reads, name no particular person: agents ask "the user", not one
// maintainer.
func TestTextsNameNoPerson(t *testing.T) {
	texts := map[string]string{"index": Index(), "claude-snippet": ClaudeSnippet()}
	for _, name := range Names() {
		text, ok := Topic(name)
		require.True(t, ok, name)
		texts[name] = text
	}
	banned := regexp.MustCompile(`(?i)\bchris(topher)?\b`)
	for name, text := range texts {
		assert.Empty(t, banned.FindAllString(text, -1), "explain %s", name)
	}
}

func TestValidProjectName(t *testing.T) {
	for _, ok := range []string{"a", "lensd", "lens-d", "x_1", "Spec2"} {
		assert.True(t, ValidProjectName(ok), ok)
	}
	for _, bad := range []string{"", "1abc", "-a", "a.b", "a b", "a/b", "é"} {
		assert.False(t, ValidProjectName(bad), bad)
	}
	_, err := Skeleton("1abc")
	assert.Error(t, err)
}

// TestSkeletonIsClean renders the skeleton and checks that it parses with no
// diagnostics and shows everything plan 5 asks for: one definition, one
// invariant, a numbered section, every reference form, and no anchor without
// an outside reference (so def-unreferenced stays quiet).
func TestSkeletonIsClean(t *testing.T) {
	for _, name := range []string{"demo", "lens-gate", "My_Proj2"} {
		t.Run(name, func(t *testing.T) {
			src, err := Skeleton(name)
			require.NoError(t, err)
			assert.NotContains(t, src, "Copyright")
			assert.NotContains(t, src, "{{")
			assert.True(t, strings.HasPrefix(src, "# "+name+" specification\n"), src[:40])

			path := name + ".spec.md"
			f := mdspec.ParseFile(path, []byte(src))
			spec := mdspec.Build([]*mdspec.File{f})
			assert.Empty(t, spec.Diagnostics)

			var kinds []mdspec.Kind
			for _, a := range spec.Anchors {
				kinds = append(kinds, a.Kind)
				outside := 0
				for _, r := range spec.ReferencesTo(a.Name) {
					if r.Within != a {
						outside++
					}
				}
				assert.Positive(t, outside, "%s has no outside reference", a.Name)
				assert.Equal(t, mdspec.FormParagraph, a.Form)
				assert.NotEmpty(t, a.Title)
				assert.False(t, a.CodeAfter)
			}
			assert.ElementsMatch(t, []mdspec.Kind{mdspec.KindDefinition, mdspec.KindInvariant}, kinds)
			rule := spec.Anchor("Example.Rule")
			require.NotNil(t, rule)
			assert.Contains(t, f.Text(rule.BodyRange), "Checked by:")

			var numbers []string
			for _, s := range f.Sections {
				numbers = append(numbers, s.Number)
			}
			assert.Equal(t, []string{"1", "2", "2.1"}, numbers)

			forms := map[string]bool{}
			for _, r := range spec.SectionRefs {
				assert.Equal(t, mdspec.Resolved, r.State, f.Text(r.Range))
				text := f.Text(r.Range)
				switch {
				case r.Stem != "":
					forms["qualified"] = true
				case strings.HasPrefix(text, "§ "):
					forms["spaced"] = true
				case strings.Contains(r.Number, "."):
					forms["dotted"] = true
				default:
					forms["plain"] = true
				}
			}
			assert.Equal(t, map[string]bool{"qualified": true, "spaced": true, "dotted": true, "plain": true}, forms)
			for _, r := range spec.Refs {
				assert.Equal(t, mdspec.Resolved, r.State)
			}
			assertNoMathHazard(t, f)
			assert.NotEmpty(t, f.OpaqueMentions("Example.Rule"), "shows a reference in code")
		})
	}
}

// assertNoMathHazard runs the math-hazard rule over the topic.
func assertNoMathHazard(t *testing.T, f *mdspec.File) {
	t.Helper()
	for _, d := range lint.Run(mdspec.Build([]*mdspec.File{f}), lint.Options{}) {
		assert.NotEqual(t, diag.CodeMathHazard, d.Code, "%s at %d", d.Message, d.Range.Start)
	}
}

// codes returns the diagnostic codes of a spec built from one file.
func codes(spec *mdspec.Spec) []string {
	var out []string
	for _, d := range spec.Diagnostics {
		out = append(out, d.Code)
	}
	return out
}

// TestMarkupListsEveryLintCode checks the markup topic's lint list against
// plan section 7, using the diag constants.
func TestMarkupListsEveryLintCode(t *testing.T) {
	doc := markupText(t)
	section := doc[strings.Index(doc, "## Lint codes"):]
	groups := map[string][]string{
		"Errors:": {diag.CodeRefDangling, diag.CodeSectionRefDangling, diag.CodeSectionRefAmbiguous,
			diag.CodeDefDuplicate, diag.CodeDefMalformed, diag.CodeDefNested, diag.CodeSectionDup,
			diag.CodeProjectNoFiles, diag.CodeProjectFileMissing, diag.CodeFileNotUTF8,
			diag.CodeFileSymlink, diag.CodeConfigInvalid, diag.CodeFormalRefDangling},
		"Warnings:": {diag.CodeDefUnreferenced, diag.CodeMathHazard, diag.CodeFileBOM,
			diag.CodeFileStemDuplicate, diag.CodeAnchorLong, diag.CodeCheckedByMissing, diag.CodeFormalOpUncited},
		"Hints:": {diag.CodeSectionHashRef, diag.CodeSectionRefMalformed, diag.CodeRefNearly,
			diag.CodeRefInTitle, diag.CodeAnchorCodeAfter, diag.CodeFormalTagMalformed, diag.CodeFormalTagBlock,
			diag.CodeFormalQuoteStale},
	}
	for head, codes := range groups {
		i := strings.Index(section, "- "+head)
		require.GreaterOrEqual(t, i, 0, head)
		item := section[i+2:]
		if j := strings.Index(item, "\n- "); j >= 0 {
			item = item[:j]
		}
		for _, c := range codes {
			assert.Contains(t, item, "`"+c+"`", "%s %s", head, c)
		}
	}
}

// TestJSONCoversEveryCommand checks that the json topic documents the data
// shape of every plan-5 command and every error code the plan names.
func TestJSONCoversEveryCommand(t *testing.T) {
	doc, ok := Topic("json")
	require.True(t, ok)
	for _, cmd := range []string{"init", "explain", "adopt", "ls", "show", "refs", "lint", "diff", "status",
		"check", "rename", "fmt", "doctor", "analyze", "change", "propose", "fix", "proposals", "proposal",
		"approve", "review", "reject", "recheck", "recover"} {
		assert.Contains(t, doc, "**`spec "+cmd, cmd)
	}
	for _, code := range []string{"not_found", "bad_ref", "no_baseline", "concurrent_edit",
		"lock_held", "lint_errors", "missing_trailers", "pending", "rejected", "stale", "not_pending",
		"bad_model_output", "bad_change", "commit_failed", "not_fixable", "no_fix", "not_formatted",
		"unformatted"} {
		assert.Contains(t, doc, "`"+code+"`", code)
	}
	for _, key := range []string{`"schema": "spec/v1"`, `"ok"`, `"data"`, `"error"`, `"diagnostics"`,
		`"trailers"`, `"next"`, `"argv"`, "`warning`", "`stale_primary`", "`check_state`",
		"`source_kind`", "`revision`", "`check_owner`", "`check_files`", "`check_generation`"} {
		assert.Contains(t, doc, key)
	}
	for _, state := range []string{"clean", "prose_only", "anchors_changed", "no_baseline"} {
		assert.Contains(t, doc, "`"+state+"`")
	}
}

// TestTrailerExamples checks the trailer lines shown in the workflow topic
// against the grammar of plan section 4.
func TestTrailerExamples(t *testing.T) {
	doc, ok := Topic("workflow")
	require.True(t, ok)
	side := `(?:(?:inv|def):[0-9a-f]{8}|-)`
	name := `\$[A-Z][A-Za-z0-9]*(?:\.[A-Z][A-Za-z0-9]*)*`
	change := regexp.MustCompile(`^Spec-Change: ` + name + ` ` + side + ` -> ` + side + `$`)
	rename := regexp.MustCompile(`^Spec-Rename: ` + name + ` -> ` + name + `$`)
	n := 0
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Spec-Change: $") && !strings.HasPrefix(line, "Spec-Rename: $") ||
			strings.Contains(line, "<") || strings.Contains(line, "$Old") {
			continue // not a trailer, or the grammar itself
		}
		// Strip the commentary column.
		if i := strings.Index(line, "   "); i >= 0 {
			line = line[:i]
		}
		n++
		assert.True(t, change.MatchString(line) || rename.MatchString(line), line)
	}
	assert.Equal(t, 5, n)
}
