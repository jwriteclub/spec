// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/golden"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
)

const testdata = "../../testdata"

// tree writes files (slash paths) under a fresh temp dir and returns it.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return dir
}

func codes(ds []diag.Diagnostic) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return out
}

func TestParseConfigValid(t *testing.T) {
	src := "name = \"vod\"\nfiles = [\"docs/*.spec.md\", \"x.md\"]\n\n[lint]\nref-nearly = \"off\"\nanchor-long = \"error\"\n"
	cfg, ds := ParseConfig([]byte(src))
	assert.Empty(t, ds)
	assert.Equal(t, "vod", cfg.Name)
	assert.Equal(t, []string{"docs/*.spec.md", "x.md"}, cfg.Files)
	assert.Equal(t, lint.Overrides{"ref-nearly": "off", "anchor-long": "error"}, cfg.Lint)
}

func TestParseConfigFormat(t *testing.T) {
	cfg, ds := ParseConfig([]byte("[format]\nwidth = 80\n"))
	assert.Empty(t, ds)
	assert.Equal(t, 80, cfg.Format.Width)

	src := "[format]\nwidth = -3\n"
	cfg, ds = ParseConfig([]byte(src))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "[format] width must be a positive number")
	assert.Equal(t, "width", src[ds[0].Range.Start:ds[0].Range.End])
	assert.Zero(t, cfg.Format.Width, "the default applies")

	_, ds = ParseConfig([]byte("[format]\nwrap = 3\n"))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "format.wrap")

	_, ds = ParseConfig([]byte("[lint]\nref-adjacent = \"off\"\n"))
	assert.Empty(t, ds, "the old name of math-hazard is accepted")
}

func TestParseConfigEmpty(t *testing.T) {
	cfg, ds := ParseConfig(nil)
	assert.Empty(t, ds)
	assert.Equal(t, "", cfg.Name)
}

func TestParseConfigUnknownKeyNamesIt(t *testing.T) {
	src := "name = \"x\"\nbogus = 1\n"
	cfg, ds := ParseConfig([]byte(src))
	require.Len(t, ds, 1)
	assert.Equal(t, diag.CodeConfigInvalid, ds[0].Code)
	assert.Equal(t, diag.Error, ds[0].Severity)
	assert.Equal(t, "spec.toml", ds[0].File)
	assert.Contains(t, ds[0].Message, `"bogus"`)
	assert.Equal(t, "bogus", src[ds[0].Range.Start:ds[0].Range.End])
	assert.Equal(t, "x", cfg.Name, "known keys still load")
}

func TestParseConfigUnknownTable(t *testing.T) {
	_, ds := ParseConfig([]byte("[zzz]\nq = 1\n"))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "zzz")
}

func TestParseConfigInferenceTable(t *testing.T) {
	src := "name = \"x\"\n\n[inference]\nbase_url = \"http://x\"\n"
	_, ds := ParseConfig([]byte(src))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "provider settings live in ~/.config/spec/config.toml")
	assert.Equal(t, "[inference", src[ds[0].Range.Start:ds[0].Range.End])
}

func TestParseConfigLintErrors(t *testing.T) {
	src := "[lint]\nno-such-rule = \"off\"\nanchor-long = \"loud\"\nref-nearly = \"off\"\n"
	_, ds := ParseConfig([]byte(src))
	require.Len(t, ds, 2)
	assert.Contains(t, ds[0].Message, "anchor-long")
	assert.Equal(t, "anchor-long", src[ds[0].Range.Start:ds[0].Range.End])
	assert.Contains(t, ds[1].Message, "no-such-rule")
	assert.Equal(t, "no-such-rule", src[ds[1].Range.Start:ds[1].Range.End])
}

func TestParseConfigTypeAndSyntaxErrors(t *testing.T) {
	cfg, ds := ParseConfig([]byte("name = 3\n"))
	require.Len(t, ds, 1)
	assert.Equal(t, diag.CodeConfigInvalid, ds[0].Code)
	assert.Contains(t, ds[0].Message, "name")
	assert.Equal(t, Config{}, cfg)

	_, ds = ParseConfig([]byte("name = \"x\n"))
	require.Len(t, ds, 1)
	assert.Equal(t, diag.CodeConfigInvalid, ds[0].Code)

	_, ds = ParseConfig([]byte("[lint]\nref-dangling = 3\n"))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "ref-dangling")
}

func TestParseConfigBadFilesEntries(t *testing.T) {
	cases := map[string]string{
		"/abs/*.md":    "absolute",
		"../up/*.md":   "escapes the project root",
		"a/../../b.md": "escapes the project root",
		"":             "empty",
		"a\\b.md":      "backslash",
		"[unclosed":    "not a valid glob",
	}
	for pat, want := range cases {
		src := fmt.Sprintf("files = [%q]\n", pat)
		_, ds := ParseConfig([]byte(src))
		require.Len(t, ds, 1, pat)
		assert.Contains(t, ds[0].Message, want, pat)
	}
}

func TestOpenDefaultGlob(t *testing.T) {
	root := tree(t, map[string]string{
		"spec.toml":                         "",
		"b.spec.md":                         "x",
		"a.spec.md":                         "x",
		"docs/z.spec.md":                    "x",
		"docs/deep/er/y.spec.md":            "x",
		"README.md":                         "x",
		"vendor/v.spec.md":                  "x",
		"node_modules/n/n.spec.md":          "x",
		"sub/build/b.spec.md":               "x",
		".spec/p.spec.md":                   "x",
		".git/g.spec.md":                    "x",
		"nested/spec.toml":                  "",
		"nested/n.spec.md":                  "x",
		"nested/deeper/m.spec.md":           "x",
		"docs/notes.md":                     "x",
		"docs/not-a.spec.md.bak":            "x",
		"other/c.spec.md":                   "x",
		"other/inner-no-toml/also.spec.md":  "x",
		"other/inner-no-toml/spec.toml.bak": "x",
	})
	p, err := Open(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.spec.md", "b.spec.md", "docs/deep/er/y.spec.md", "docs/z.spec.md", "other/c.spec.md", "other/inner-no-toml/also.spec.md"}, p.Files)
	assert.Empty(t, p.Diagnostics)
	assert.Equal(t, filepath.Base(root), p.Name)
}

func TestOpenName(t *testing.T) {
	root := tree(t, map[string]string{"spec.toml": "name = \"custom\"\n", "a.spec.md": "x"})
	p, err := Open(root)
	require.NoError(t, err)
	assert.Equal(t, "custom", p.Name)
}

func TestOpenNoFiles(t *testing.T) {
	root := tree(t, map[string]string{"spec.toml": ""})
	p, err := Open(root)
	require.NoError(t, err)
	assert.Empty(t, p.Files)
	assert.Equal(t, []string{diag.CodeProjectNoFiles}, codes(p.Diagnostics))
	assert.Equal(t, diag.Error, p.Diagnostics[0].Severity)
}

func TestOpenExplicitFiles(t *testing.T) {
	root := tree(t, map[string]string{
		"spec.toml":     "files = [\"docs/*.md\", \"top.spec.md\", \"docs/*.md\", \"ghost/*.md\", \"vendor/*.md\"]\n",
		"docs/b.md":     "x",
		"docs/a.md":     "x",
		"docs/sub/c.md": "x",
		"top.spec.md":   "x",
		"other.spec.md": "x",
		"vendor/v.md":   "x",
	})
	p, err := Open(root)
	require.NoError(t, err)
	// Explicit patterns replace the default glob but keep its directory exclusions.
	assert.Equal(t, []string{"docs/a.md", "docs/b.md", "top.spec.md"}, p.Files)
	require.Equal(t, []string{diag.CodeProjectFileMissing, diag.CodeProjectFileMissing}, codes(p.Diagnostics))
	byPattern := map[string]diag.Diagnostic{}
	for _, d := range p.Diagnostics {
		byPattern[string(p.ConfigSource()[d.Range.Start:d.Range.End])] = d
	}
	d := byPattern["ghost/*.md"]
	assert.Contains(t, d.Message, "ghost/*.md")
	assert.Equal(t, "spec.toml", d.File)
	v := byPattern["vendor/*.md"]
	assert.Contains(t, v.Message, "vendor/*.md")
	assert.Contains(t, v.Message, "skipped", "an entry that only matches skipped directories says so")
}

// TestOpenExplicitGlobKeepsSkipList: the skip list of the default glob
// (.git, node_modules, build, vendor, .spec, nested projects) applies to
// explicit files globs too, on disk (Open) and in a baseline (Select).
func TestOpenExplicitGlobKeepsSkipList(t *testing.T) {
	files := map[string]string{
		"spec.toml":                      "files = [\"**/*.spec.md\"]\n",
		"a.spec.md":                      "x",
		"docs/b.spec.md":                 "x",
		"node_modules/pkg/n.spec.md":     "x",
		"web/node_modules/pkg/w.spec.md": "x",
		"build/out.spec.md":              "x",
		"sub/build/deep/o.spec.md":       "x",
		"vendor/v.spec.md":               "x",
		".spec/p.spec.md":                "x",
		".git/g.spec.md":                 "x",
		"nested/spec.toml":               "",
		"nested/n.spec.md":               "x",
		"nested/deeper/m.spec.md":        "x",
	}
	root := tree(t, files)
	p, err := Open(root)
	require.NoError(t, err)
	want := []string{"a.spec.md", "docs/b.spec.md"}
	assert.Equal(t, want, p.Files)
	assert.Empty(t, p.Diagnostics)

	var all []string
	for f := range files {
		all = append(all, f)
	}
	assert.Equal(t, want, p.Select(all))

	l, err := p.Load(map[string][]byte{"nested/new.spec.md": []byte("x"), "build/new.spec.md": []byte("x")})
	require.NoError(t, err)
	assert.Len(t, l.Files, 2, "overlays in skipped directories are ignored too")
}

func TestOpenAllExplicitMissing(t *testing.T) {
	root := tree(t, map[string]string{"spec.toml": "files = [\"nope.md\"]\n"})
	p, err := Open(root)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{diag.CodeProjectFileMissing, diag.CodeProjectNoFiles}, codes(p.Diagnostics))
}

func TestOpenEscapingPatternIsConfigInvalid(t *testing.T) {
	root := tree(t, map[string]string{"spec.toml": "files = [\"../*.md\", \"a.md\"]\n", "a.md": "x"})
	p, err := Open(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.md"}, p.Files)
	assert.Equal(t, []string{diag.CodeConfigInvalid}, codes(p.Diagnostics))
}

func TestOpenSymlinkedSpecFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	root := tree(t, map[string]string{"spec.toml": "", "real.spec.md": "x", "outside/o.spec.md": "x"})
	require.NoError(t, os.Symlink("real.spec.md", filepath.Join(root, "link.spec.md")))
	require.NoError(t, os.Symlink("outside", filepath.Join(root, "dirlink")))
	p, err := Open(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"outside/o.spec.md", "real.spec.md"}, p.Files, "symlinked directories are not followed")
	require.Equal(t, []string{diag.CodeFileSymlink}, codes(p.Diagnostics))
	assert.Equal(t, "link.spec.md", p.Diagnostics[0].File)
}

func TestOpenSymlinkWithExplicitGlob(t *testing.T) {
	root := tree(t, map[string]string{"spec.toml": "files = [\"*.md\"]\n", "real.md": "x"})
	require.NoError(t, os.Symlink("real.md", filepath.Join(root, "link.md")))
	p, err := Open(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"real.md"}, p.Files)
	assert.Equal(t, []string{diag.CodeFileSymlink}, codes(p.Diagnostics))
}

func TestOpenMissingConfig(t *testing.T) {
	_, err := Open(t.TempDir())
	var nf *NotFoundError
	require.ErrorAs(t, err, &nf)
	assert.True(t, nf.Explicit)
}

func TestSelect(t *testing.T) {
	root := tree(t, map[string]string{"spec.toml": ""})
	p, err := Open(root)
	require.NoError(t, err)
	got := p.Select([]string{
		"b.spec.md", "a.spec.md", "a.spec.md", "docs/c.spec.md", "vendor/v.spec.md", "x/node_modules/y.spec.md",
		"nested/spec.toml", "nested/n.spec.md", "nested/deep/m.spec.md", "readme.md", "../out.spec.md", "docs/./d.spec.md",
	})
	assert.Equal(t, []string{"a.spec.md", "b.spec.md", "docs/c.spec.md", "docs/d.spec.md"}, got)
}

func TestSelectExplicit(t *testing.T) {
	root := tree(t, map[string]string{"spec.toml": "files = [\"docs/*.md\"]\n", "docs/a.md": "x"})
	p, err := Open(root)
	require.NoError(t, err)
	got := p.Select([]string{"docs/a.md", "docs/sub/b.md", "x.spec.md", "vendor/docs/a.md"})
	assert.Equal(t, []string{"docs/a.md"}, got)
}

func TestSelectMatchesOpen(t *testing.T) {
	root := tree(t, map[string]string{
		"spec.toml": "", "a.spec.md": "x", "docs/b.spec.md": "x", "vendor/v.spec.md": "x",
		"nested/spec.toml": "", "nested/n.spec.md": "x", "other.md": "x",
	})
	p, err := Open(root)
	require.NoError(t, err)
	all := []string{"spec.toml", "a.spec.md", "docs/b.spec.md", "vendor/v.spec.md", "nested/spec.toml", "nested/n.spec.md", "other.md"}
	assert.Equal(t, p.Files, p.Select(all))
}

func TestLoadAndOverlay(t *testing.T) {
	root := tree(t, map[string]string{
		"spec.toml": "",
		"a.spec.md": "**Def $Aa.Bb.** On disk.\n",
		"b.spec.md": "**$Cc.Dd.** Uses $Aa.Bb.\n",
	})
	l, err := Load(root, nil)
	require.NoError(t, err)
	require.Len(t, l.Files, 2)
	assert.Equal(t, "a.spec.md", l.Files[0].Path)
	assert.Equal(t, "On disk.", l.Spec.Anchor("Aa.Bb").Title+l.Files[0].Text(l.Spec.Anchor("Aa.Bb").BodyRange)[len("**Def $Aa.Bb.** "):])

	overlay := map[string][]byte{
		"a.spec.md":       []byte("**Def $Aa.Zz.** Unsaved rename.\n"),
		"c.spec.md":       []byte("**Def $New.File.** From an unsaved new buffer.\n"),
		"vendor/v.md":     []byte("ignored"),
		"notes/x.spec.md": []byte("**Def $Notes.One.** Nested directory.\n"),
	}
	l, err = Load(root, overlay)
	require.NoError(t, err)
	var paths []string
	for _, f := range l.Files {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"a.spec.md", "b.spec.md", "c.spec.md", "notes/x.spec.md"}, paths)
	assert.Nil(t, l.Spec.Anchor("Aa.Bb"))
	assert.NotNil(t, l.Spec.Anchor("Aa.Zz"))
	assert.NotNil(t, l.Spec.Anchor("New.File"))
	// The unsaved rename leaves b.spec.md's reference dangling.
	assert.Contains(t, codes(l.Spec.Diagnostics), diag.CodeRefDangling)

	// Disk is untouched.
	b, err := os.ReadFile(filepath.Join(root, "a.spec.md"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "$Aa.Bb")
}

func TestLoadOverlayInsideNestedProjectIsIgnored(t *testing.T) {
	root := tree(t, map[string]string{"spec.toml": "", "a.spec.md": "x\n", "nested/spec.toml": ""})
	l, err := Load(root, map[string][]byte{"nested/n.spec.md": []byte("**Def $Nn.Mm.** x\n")})
	require.NoError(t, err)
	assert.Len(t, l.Files, 1)
}

func TestDiscover(t *testing.T) {
	root := tree(t, map[string]string{"spec.toml": "", "a/b/c/x.txt": "x"})
	got, err := Discover(filepath.Join(root, "a", "b", "c"))
	require.NoError(t, err)
	assert.Equal(t, root, got)

	got, ok := FindRoot(filepath.Join(root, "a", "b"))
	assert.True(t, ok)
	assert.Equal(t, root, got)
	_, ok = FindRoot(t.TempDir())
	assert.False(t, ok)

	got, err = Resolve(root, "/nonexistent")
	require.NoError(t, err)
	assert.Equal(t, root, got)

	got, err = Resolve("", filepath.Join(root, "a"))
	require.NoError(t, err)
	assert.Equal(t, root, got)
}

func hasGit() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func TestDiscoverNotFoundSuggests(t *testing.T) {
	if !hasGit() {
		t.Skip("git not installed")
	}
	root := tree(t, map[string]string{
		"proj1/spec.toml":              "",
		"services/api/docs/spec.toml":  "",
		"a/b/c/d/e/too-deep/spec.toml": "",
		"node_modules/x/spec.toml":     "",
		"work/x.txt":                   "x",
	})
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	require.NoError(t, cmd.Run())

	_, err := Discover(filepath.Join(root, "work"))
	var nf *NotFoundError
	require.ErrorAs(t, err, &nf)
	assert.False(t, nf.Explicit)
	assert.Equal(t, []string{"../proj1", "../services/api/docs"}, nf.Suggestions)
	assert.Contains(t, nf.Error(), "--project")
	assert.Contains(t, nf.Error(), "../proj1")

	_, err = Resolve(filepath.Join(root, "work"), "/")
	require.ErrorAs(t, err, &nf)
	assert.True(t, nf.Explicit)
}

func TestDiscoverNotFoundOutsideGit(t *testing.T) {
	dir := t.TempDir()
	_, err := Discover(dir)
	var nf *NotFoundError
	if err == nil {
		t.Skip("a spec.toml exists above the temp directory")
	}
	require.ErrorAs(t, err, &nf)
	assert.Empty(t, nf.Suggestions)
}

// multiGolden is the golden shape for testdata/multi.
type multiGolden struct {
	Project     string          `json:"project"`
	Files       []string        `json:"files"`
	Anchors     []multiAnchor   `json:"anchors"`
	Refs        []multiRef      `json:"refs"`
	SectionRefs []multiSecRef   `json:"section_refs"`
	Sections    []string        `json:"sections"`
	Lint        []multiLintItem `json:"lint"`
}

type multiAnchor struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	At   string `json:"at"`
	Hash string `json:"hash"`
}

type multiRef struct {
	At     string `json:"at"`
	Text   string `json:"text"`
	State  string `json:"state"`
	Target string `json:"target,omitempty"`
	Within string `json:"within,omitempty"`
}

type multiSecRef struct {
	At     string `json:"at"`
	Text   string `json:"text"`
	State  string `json:"state"`
	Target string `json:"target,omitempty"`
}

type multiLintItem struct {
	Code string `json:"code"`
	At   string `json:"at"`
}

func TestMultiGolden(t *testing.T) {
	l, err := Load(filepath.Join(testdata, "multi"), nil)
	require.NoError(t, err)
	assert.Empty(t, l.Project.Diagnostics)

	line := func(file string, off int) string {
		f := l.Spec.File(file)
		p := f.Index().Position(off)
		return fmt.Sprintf("%s:%d:%d", file, p.Line, p.Col)
	}
	g := multiGolden{Project: l.Project.Name, Files: l.Project.Files, Anchors: []multiAnchor{}, Refs: []multiRef{},
		SectionRefs: []multiSecRef{}, Sections: []string{}, Lint: []multiLintItem{}}
	for _, a := range l.Spec.Anchors {
		g.Anchors = append(g.Anchors, multiAnchor{Name: a.Name, Kind: string(a.Kind), At: line(a.File, a.NameRange.Start), Hash: a.ShortHash})
	}
	for _, r := range l.Spec.Refs {
		mr := multiRef{At: line(r.File, r.Range.Start), Text: l.Spec.File(r.File).Text(r.Range), State: string(r.State)}
		if r.Target != nil {
			mr.Target = line(r.Target.File, r.Target.NameRange.Start)
		}
		if r.Within != nil {
			mr.Within = r.Within.Name
		}
		g.Refs = append(g.Refs, mr)
	}
	for _, r := range l.Spec.SectionRefs {
		ms := multiSecRef{At: line(r.File, r.Range.Start), Text: l.Spec.File(r.File).Text(r.Range), State: string(r.State)}
		if r.Target != nil {
			ms.Target = r.Target.Qualified() + " " + line(r.Target.File, r.Target.HeadingRange.Start)
		}
		g.SectionRefs = append(g.SectionRefs, ms)
	}
	for _, s := range l.Spec.Sections {
		g.Sections = append(g.Sections, s.Qualified())
	}
	ds := lint.Run(l.Spec, lint.Options{Overrides: l.Project.Config.Lint, Extra: l.Project.Diagnostics})
	for _, d := range ds {
		g.Lint = append(g.Lint, multiLintItem{Code: d.Code, At: line(d.File, d.Range.Start)})
	}
	data, err := json.MarshalIndent(g, "", "  ")
	require.NoError(t, err)
	golden.Check(t, filepath.Join(testdata, "multi", "multi.golden.json"), append(data, '\n'))

	// Independent of the golden file: everything resolves and lint is clean.
	assert.Empty(t, ds)
	for _, r := range l.Spec.Refs {
		assert.Equal(t, mdspec.Resolved, r.State)
	}
	for _, r := range l.Spec.SectionRefs {
		assert.Equal(t, mdspec.Resolved, r.State, "%s", l.Spec.File(r.File).Text(r.Range))
	}
}
