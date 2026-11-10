// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lensdProject copies testdata/lensd to a temp directory and adds a spec.toml.
func lensdProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyDir(t, "../../testdata/lensd", dir)
	write(t, dir, "spec.toml", "name = \"lift\"\n")
	return dir
}

// nameRow matches a row of testdata/lensd/NAMES.md.
var nameRow = regexp.MustCompile("^\\| `[A-Z]+-[A-Z]+-[0-9]+` \\| `(Def )?\\$([A-Za-z0-9.]+)` \\| (.+) \\|$")

type tableAnchor struct{ kind, name, title string }

func nameTable(t *testing.T) []tableAnchor {
	t.Helper()
	b, err := os.ReadFile("../../testdata/lensd/NAMES.md")
	require.NoError(t, err)
	var rows []tableAnchor
	for _, l := range strings.Split(string(b), "\n") {
		m := nameRow.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		kind := "inv"
		if m[1] != "" {
			kind = "def"
		}
		rows = append(rows, tableAnchor{kind, m[2], m[3]})
	}
	return rows
}

// paragraphFrom returns the lines of src from the first line that starts with
// prefix up to the next blank line.
func paragraphFrom(t *testing.T, src, prefix string) string {
	t.Helper()
	ls := strings.Split(src, "\n")
	for i, l := range ls {
		if strings.HasPrefix(l, prefix) {
			j := i
			for j < len(ls) && strings.TrimSpace(ls[j]) != "" {
				j++
			}
			return strings.Join(ls[i:j], "\n")
		}
	}
	t.Fatalf("no line starts with %q", prefix)
	return ""
}

// TestLensd is acceptance criterion 5.
func TestLensd(t *testing.T) {
	dir := lensdProject(t)

	r := run(t, dir, "lint")
	assert.Equal(t, 0, r.code, r.stdout+r.stderr)
	jr := runJSON(t, dir, "lint")
	assert.Equal(t, 0, jr.code)
	var ld lintData
	jr.decode(t, &ld)
	assert.Equal(t, 0, ld.Counts.Error)
	assert.Equal(t, 30, ld.Anchors)

	// ls lists every anchor in the name table with the right kind, in order.
	table := nameTable(t)
	require.Len(t, table, 30)
	ls := runJSON(t, dir, "ls")
	require.Equal(t, 0, ls.code)
	var lsd lsData
	ls.decode(t, &lsd)
	got := map[string]tableAnchor{}
	for _, a := range lsd.Anchors {
		got[strings.TrimPrefix(a.Name, "$")] = tableAnchor{a.Kind, strings.TrimPrefix(a.Name, "$"), a.Title}
		assert.Equal(t, "lensd.spec.md", a.File)
	}
	for _, want := range table {
		assert.Equal(t, want, got[want.name], "anchor %s", want.name)
	}
	assert.Len(t, lsd.Anchors, len(table))
	for i := 1; i < len(lsd.Anchors); i++ {
		assert.Less(t, lsd.Anchors[i-1].Line, lsd.Anchors[i].Line, "source order")
	}
	human := run(t, dir, "ls")
	for _, want := range table {
		assert.Regexp(t, "(?m)^"+want.kind+" +"+regexp.QuoteMeta(want.name)+" +", human.stdout)
	}
	defs := runJSON(t, dir, "ls", "--kind", "def")
	var dd lsData
	defs.decode(t, &dd)
	assert.Len(t, dd.Anchors, 5)

	// show prints the right body for one definition and one invariant.
	src := readFile(t, dir, "lensd.spec.md")
	for _, c := range []struct{ name, prefix, kind string }{
		{"Time.Media", "**Def $Time.Media", "def"},
		{"Frame.Timing", "**$Frame.Timing", "inv"},
		{"$Frame.Timing", "**$Frame.Timing", "inv"}, // the sigil is accepted
	} {
		want := paragraphFrom(t, src, c.prefix)
		sh := runJSON(t, dir, "show", c.name)
		require.Equal(t, 0, sh.code, sh.stdout)
		var d showAnchorData
		sh.decode(t, &d)
		assert.Equal(t, "anchor", d.Type)
		assert.Equal(t, c.kind, d.Anchor.Kind)
		assert.Equal(t, want, d.Body, c.name)
		assert.Contains(t, run(t, dir, "show", c.name).stdout, want)
	}
}

// TestLensdArgumentForms checks that anchor and section arguments take every
// documented form.
func TestLensdArgumentForms(t *testing.T) {
	dir := lensdProject(t)
	for _, arg := range []string{"Time.Media", "$Time.Media"} {
		r := runJSON(t, dir, "show", arg)
		require.Equal(t, 0, r.code, arg)
		var d showAnchorData
		r.decode(t, &d)
		assert.Equal(t, "$Time.Media", d.Anchor.Name)
	}
	for _, arg := range []string{"1", "§1", "#1", "lensd:1", "§lensd:1", "1."} {
		r := runJSON(t, dir, "show", arg)
		require.Equal(t, 0, r.code, "%s: %s", arg, r.stdout)
		var d showSectionData
		r.decode(t, &d)
		assert.Equal(t, "section", d.Type)
		assert.Equal(t, "§lensd:1", d.Section.Qualified)
		assert.NotEmpty(t, d.Anchors, "section 1 holds anchors")
	}
	// The shell leaves `.Timing` of an expanded $Frame.Timing.
	for _, arg := range []string{".Timing", ""} {
		for _, cmd := range []string{"show", "refs"} {
			r := runJSON(t, dir, cmd, arg)
			assert.Equal(t, 1, r.code, "%s %q", cmd, arg)
			assert.Equal(t, "usage", r.errCode)
			assert.Contains(t, r.errMessage, "shell")
		}
	}
	r := run(t, dir, "show", ".Timing")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "did your shell expand `$`? write `Frame.Timing`")
	assert.Empty(t, r.stdout)
}

// multiProject copies testdata/multi to a temp directory.
func multiProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyDir(t, "../../testdata/multi", dir)
	return dir
}

type multiGoldenRef struct {
	At     string `json:"at"`
	Text   string `json:"text"`
	State  string `json:"state"`
	Target string `json:"target"`
	Within string `json:"within"`
}

type multiGolden struct {
	Refs        []multiGoldenRef `json:"refs"`
	SectionRefs []multiGoldenRef `json:"section_refs"`
	Lint        []any            `json:"lint"`
}

// TestMultiReferencesMatchGolden is the first half of criterion 6: every $
// and § reference in testdata/multi resolves as the golden file says.
func TestMultiReferencesMatchGolden(t *testing.T) {
	dir := multiProject(t)
	b, err := os.ReadFile("../../testdata/multi/multi.golden.json")
	require.NoError(t, err)
	var g multiGolden
	require.NoError(t, json.Unmarshal(b, &g))

	lr := runJSON(t, dir, "lint")
	assert.Equal(t, 0, lr.code)
	assert.Equal(t, len(g.Lint), len(lr.diagnostics), "lint findings: %v", lr.diagnostics)

	// Group the golden references by what they name, then compare with what
	// `spec refs` reports for each.
	byName := map[string][]string{}
	for _, r := range g.Refs {
		byName[strings.TrimPrefix(r.Text, "$")] = append(byName[strings.TrimPrefix(r.Text, "$")], r.At)
		assert.Equal(t, "resolved", r.State)
	}
	require.NotEmpty(t, byName)
	for name, want := range byName {
		rr := runJSON(t, dir, "refs", name)
		require.Equal(t, 0, rr.code, rr.stdout)
		var d refsData
		rr.decode(t, &d)
		var have []string
		for _, s := range d.Refs {
			assert.Equal(t, "resolved", s.State)
			have = append(have, fmt.Sprintf("%s:%d:%d", s.File, s.Line, s.Col))
		}
		sort.Strings(want)
		sort.Strings(have)
		assert.Equal(t, want, have, "references to %s", name)
	}

	// Section references: golden targets look like "§hls:2 hls.spec.md:14:1".
	secByTarget := map[string][]string{}
	for _, r := range g.SectionRefs {
		assert.Equal(t, "resolved", r.State)
		target := strings.Fields(r.Target)[0]
		secByTarget[target] = append(secByTarget[target], r.At)
	}
	require.NotEmpty(t, secByTarget)
	for target, want := range secByTarget {
		rr := runJSON(t, dir, "refs", target)
		require.Equal(t, 0, rr.code, "%s: %s", target, rr.stdout)
		var d refsData
		rr.decode(t, &d)
		assert.Equal(t, "section", d.Target.Type)
		var have []string
		for _, s := range d.Refs {
			have = append(have, fmt.Sprintf("%s:%d:%d", s.File, s.Line, s.Col))
		}
		sort.Strings(want)
		sort.Strings(have)
		assert.Equal(t, want, have, "references to %s", target)
	}
}

// replaceName replaces whole-name occurrences of $old with $new in src:
// the name must not continue with a name character or a '.' that is followed
// by one. It is an independent oracle for what rename may touch (the
// fixtures hold no occurrences in code).
func replaceName(src, old, new string) (string, int) {
	var b strings.Builder
	n := 0
	for i := 0; i < len(src); {
		if strings.HasPrefix(src[i:], "$"+old) {
			rest := src[i+1+len(old):]
			cont := false
			if len(rest) > 0 {
				c := rest[0]
				isName := func(c byte) bool {
					return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'
				}
				if isName(c) {
					cont = true
				}
				if c == '.' && len(rest) > 1 && isName(rest[1]) {
					cont = true
				}
			}
			if !cont {
				b.WriteString("$" + new)
				i += 1 + len(old)
				n++
				continue
			}
		}
		b.WriteByte(src[i])
		i++
	}
	return b.String(), n
}

// TestMultiRename is the second half of criterion 6: rename across files,
// then lint is clean, only the expected tokens changed, and diff reports one
// renamed and zero changed.
func TestMultiRename(t *testing.T) {
	requireGit(t)
	dir := multiProject(t)
	gitInit(t, dir)
	commitAll(t, dir, "base")
	files := []string{"hls.spec.md", "index.spec.md", "rws.spec.md"}
	before := map[string]string{}
	for _, f := range files {
		before[f] = readFile(t, dir, f)
	}

	// Rws.Session is defined in rws and used in hls, so the rename crosses
	// files. Rws.OnePerCamera must not be touched by renaming Rws.
	dry := runJSON(t, dir, "rename", "Rws.Session", "Rws.Relay", "--dry-run")
	require.Equal(t, 0, dry.code, dry.stdout)
	var dd renameData
	dry.decode(t, &dd)
	assert.True(t, dd.DryRun)
	for _, f := range files {
		assert.Equal(t, before[f], readFile(t, dir, f), "--dry-run writes nothing")
	}
	assert.NoDirExists(t, dir+"/.spec", "a dry run never creates .spec")

	jr := runJSON(t, dir, "rename", "Rws.Session", "Rws.Relay")
	require.Equal(t, 0, jr.code, jr.stdout)
	var rd renameData
	jr.decode(t, &rd)
	assert.Equal(t, "$Rws.Session", rd.From)
	assert.Equal(t, "$Rws.Relay", rd.To)
	assert.Equal(t, "Spec-Rename: $Rws.Session -> $Rws.Relay", rd.Trailer)
	assert.Equal(t, dd.Count, rd.Count)
	assert.Greater(t, len(rd.Files), 1, "the rename crosses files")

	total := 0
	for _, f := range files {
		want, n := replaceName(before[f], "Rws.Session", "Rws.Relay")
		total += n
		assert.Equal(t, want, readFile(t, dir, f), "%s: only the name tokens changed", f)
	}
	assert.Equal(t, total, rd.Count)
	assert.Len(t, rd.Edits, total)
	assert.Contains(t, readFile(t, dir, "rws.spec.md"), "$Rws.OnePerCamera", "other names with the same prefix are untouched")

	lr := runJSON(t, dir, "lint")
	assert.Equal(t, 0, lr.code)
	assert.Empty(t, lr.diagnostics, "lint is clean after the rename")

	df := runJSON(t, dir, "diff")
	require.Equal(t, 0, df.code, df.stdout)
	var d diffData
	df.decode(t, &d)
	require.Len(t, d.Changes, 1, "exactly one renamed and zero changed: %s", df.data)
	assert.Equal(t, "renamed", d.Changes[0].State)
	assert.Equal(t, "$Rws.Relay", d.Changes[0].Name)
	require.NotNil(t, d.Changes[0].RenamedFrom)
	assert.Equal(t, "$Rws.Session", *d.Changes[0].RenamedFrom)
	assert.Equal(t, []string{"Spec-Rename: $Rws.Session -> $Rws.Relay"}, d.Changes[0].Trailers)

	// git diff shows only the expected tokens: split into name tokens and
	// single other characters, every removed word is the old name and every
	// added word the new one.
	diff := git(t, dir, "diff", "--word-diff=porcelain", "--word-diff-regex=\\$[A-Za-z0-9]+(\\.[A-Za-z0-9]+)*|[^[:space:]]", "-U0")
	removed, added := 0, 0
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "---"), strings.HasPrefix(l, "+++"):
		case strings.HasPrefix(l, "-"):
			assert.Equal(t, "-$Rws.Session", l)
			removed++
		case strings.HasPrefix(l, "+"):
			assert.Equal(t, "+$Rws.Relay", l)
			added++
		}
	}
	assert.Equal(t, total, removed)
	assert.Equal(t, total, added)
}
