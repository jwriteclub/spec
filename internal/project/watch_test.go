// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// watchProject makes paths (a trailing slash makes a directory) under a new
// root holding a spec.toml.
func watchProject(t *testing.T, config string, paths ...string) *Project {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ConfigFile), []byte(config), 0o644))
	for _, p := range paths {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if p[len(p)-1] == '/' {
			require.NoError(t, os.MkdirAll(abs, 0o755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte("# x\n"), 0o644))
	}
	p, err := Open(root)
	require.NoError(t, err)
	return p
}

func TestWatchDirs(t *testing.T) {
	tree := []string{
		"a.md", "docs/b.md", "docs/deep/c.md", "tla/M.tla", "tla/states/x/", "node_modules/pkg/d.md", "vendor/v/e.md",
		"build/f.md", ".git/g.md", ".spec/h.md", ".hidden/i.md", "other/j.txt", "nested/spec.toml", "nested/k.md",
	}
	for name, c := range map[string]struct {
		config string
		want   []string
	}{
		"default glob walks but skips":   {"", []string{".", "docs", "docs/deep", "nested/spec.toml", "other", "tla", "tla/states", "tla/states/x"}},
		"literal prefix only":            {"files = [\"docs/*.md\"]\nformal = [\"tla/*.tla\"]\n", []string{".", "docs", "tla"}},
		"explicit file, and its parents": {"files = [\"docs/deep/c.md\"]\n", []string{".", "docs", "docs/deep"}},
		"recursive below a prefix":       {"files = [\"docs/**/*.md\"]\n", []string{".", "docs", "docs/deep"}},
		"missing prefix: its parent":     {"formal = [\"tla/sub/more/*.tla\"]\nfiles = [\"a.md\"]\n", []string{".", "tla"}},
		"wholly missing prefix":          {"formal = [\"models/x/*.tla\"]\nfiles = [\"a.md\"]\n", []string{"."}},
		"directory wildcard":             {"files = [\"*/b.md\"]\n", []string{".", "docs", "nested/spec.toml", "other", "tla"}},
		"hidden named explicitly":        {"files = [\".hidden/*.md\"]\n", []string{".", ".hidden"}},
		"hidden named by a wildcard":     {"files = [\".h*/*.md\"]\n", []string{".", ".hidden"}},
		"skipped named explicitly":       {"files = [\"node_modules/*.md\", \"a.md\"]\n", []string{"."}},
		"nested project named":           {"files = [\"nested/*.md\", \"a.md\"]\n", []string{".", "nested/spec.toml"}},
		"brace around a slash":           {"files = [\"{docs/deep,other}/*.md\"]\n", []string{".", "docs", "docs/deep", "other"}},
		"brace holding **":               {"files = [\"{docs/**,other}/*.md\"]\n", []string{".", "docs", "docs/deep", "other"}},
		"hidden named below **":          {"files = [\"**/.hidden/*.md\"]\n", []string{".", ".hidden", "docs", "docs/deep", "nested/spec.toml", "other", "tla", "tla/states", "tla/states/x"}},
		"hidden in a brace":              {"files = [\"{.hidden,docs}/*.md\"]\n", []string{".", ".hidden", "docs"}},
		"hidden by a leading ?":          {"files = [\"?hidden/*.md\"]\n", []string{".", ".hidden"}},
		"brace in a name":                {"files = [\"docs/*.{md,txt}\"]\n", []string{".", "docs"}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, watchProject(t, c.config, tree...).WatchDirs())
		})
	}
}

// TestWatchDirsSymlink: the file walk does not follow a linked directory, so
// no watch goes through one.
func TestWatchDirsSymlink(t *testing.T) {
	p := watchProject(t, "files = [\"**/*.md\", \"lnk/*.md\"]\n", "real/a.md")
	require.NoError(t, os.Symlink(filepath.Join(p.Root, "real"), filepath.Join(p.Root, "lnk")))
	assert.Equal(t, []string{".", "real"}, p.WatchDirs())
}

func TestSelects(t *testing.T) {
	p := watchProject(t, "files = [\"docs/*.md\"]\nformal = [\"tla/*\"]\n", "docs/a.md")
	assert.True(t, p.Selects("docs/new.md"))
	assert.False(t, p.Selects("docs/new.md~"))
	assert.False(t, p.Selects("docs/.new.md.swp"))
	assert.False(t, p.Selects("docs/4913"))
	assert.True(t, p.Selects("tla/M.tla"))
	assert.False(t, p.Selects("tla/M.cfg"), "a model needs a model language's extension")
	assert.True(t, watchProject(t, "").Selects("deep/er/x.spec.md"), "the default glob")
}

func TestRetain(t *testing.T) {
	p := watchProject(t, "formal = [\"tla/*.tla\"]\n", "b.spec.md", "z.spec.md", "tla/M.tla", "nested/spec.toml")
	assert.True(t, p.Retain("b.spec.md"), "already there")
	assert.Equal(t, []string{"b.spec.md", "z.spec.md"}, p.Files)
	assert.True(t, p.Retain("c.spec.md"))
	assert.Equal(t, []string{"b.spec.md", "c.spec.md", "z.spec.md"}, p.Files)
	assert.True(t, p.Retain("tla/A.tla"))
	assert.Equal(t, []string{"tla/A.tla", "tla/M.tla"}, p.Formal)
	assert.False(t, p.Retain("notes.txt"), "no glob selects it")
	assert.False(t, p.Retain("nested/x.spec.md"), "a nested project owns it")
}

// TestWatchDirsCover: wherever a new file would be selected, its directory is
// watched. namesHidden patterns name their hidden directories; below the others a
// hidden directory is the disclosed exception.
func TestWatchDirsCover(t *testing.T) {
	tree := []string{
		"docs/a/x/y/", "docs/b/", "docs/.h/x/", ".h/x/", "z/.h/", "specs/c/", "other/deep/er/", "a/b/c/e/", "a/b/e/", "ab/", "ac/",
	}
	namesHidden := []string{"{.h,docs}/*.md", "docs/{.h,a}/*.md", "?h/*.md", "[.]h/*.md", ".*/*.md", ".h/**/*.md", "**/.h/*.md", "**/.h/**/*.md", "docs/.h/*.md"}
	others := []string{
		"*.md", "README.md", "docs/README.md", "docs/*.md", "docs/**/*.md", "docs/**", "**/*.md", "**/x/*.md", "docs/**/x/*.md", "**/docs/*.md",
		"./docs/*.md", "d?cs/*.md", "[ds]ocs/*.md", "docs/*/*.md", "docs/*/*/*.md", "{docs,specs}/*.md", "{docs/a,specs}/*.md", "docs/{a,b}/*.md",
		"docs/{a,b/c}/*.md", "docs{,2}/*.md", "a{b,c}/*.md", "a/{b,b/c}/e/*.md", "{**,x}/new.md", "{docs/**,z}/*.md", "docs/*{a,b}*/*.md", "*/*.md", "*/**/*.md",
	}
	check := func(t *testing.T, pat string, hiddenToo bool) {
		p := watchProject(t, "files = [\""+pat+"\"]\n", tree...)
		got := p.WatchDirs()
		require.NoError(t, filepath.WalkDir(p.Root, func(abs string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(p.Root, abs)
			rel = filepath.ToSlash(rel)
			hidden := slices.ContainsFunc(strings.Split(rel, "/"), func(c string) bool { return c != "." && strings.HasPrefix(c, ".") })
			if hidden && !hiddenToo {
				return nil
			}
			if ok, _ := doublestar.Match(path.Clean(pat), path.Join(rel, "new.md")); ok {
				assert.Contains(t, got, rel, "%s selects a new file there", pat)
			}
			return nil
		}))
	}
	for _, pat := range namesHidden {
		check(t, pat, true)
	}
	for _, pat := range others {
		check(t, pat, false)
	}
}

// TestRetainedDraftLoad: a retained draft with no file behind it is left out of
// what the disk holds, and comes in with its overlay. Any other missing file is
// still an error.
func TestRetainedDraftLoad(t *testing.T) {
	p := watchProject(t, "", "b.spec.md")
	require.True(t, p.Retain("draft.spec.md"))
	l, err := p.Load(nil)
	require.NoError(t, err)
	require.Len(t, l.Files, 1)
	assert.Equal(t, "b.spec.md", l.Files[0].Path)

	l, err = p.Load(map[string][]byte{"draft.spec.md": []byte("# draft\n")})
	require.NoError(t, err)
	assert.Len(t, l.Files, 2)

	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "draft.spec.md"), []byte("# back\n"), 0o644))
	l, err = p.Load(nil)
	require.NoError(t, err)
	assert.Len(t, l.Files, 2, "written back, it is read")

	p.Files = append(p.Files, "gone.spec.md")
	_, err = p.Load(nil)
	assert.Error(t, err)
}

// TestTempFiles: a hidden file or an editor's temp joins only when a glob's last
// component looks like one too, wherever selection happens.
func TestTempFiles(t *testing.T) {
	tree := []string{
		"docs/real.md", "docs/real.spec.md", "docs/.d/real.md", "docs/.x.spec.md",
		"docs/.a.md.swp", "docs/.#a.md", "docs/.a.md", "docs/a.md~", "docs/#a.md#", "docs/4913",
	}
	plain := []string{"docs/real.md", "docs/real.spec.md"}
	for name, c := range map[string]struct {
		config string
		want   []string
	}{
		"wide glob":                             {"files = [\"docs/*\"]\n", plain},
		"doublestar":                            {"files = [\"docs/**\"]\n", []string{"docs/.d/real.md", "docs/real.md", "docs/real.spec.md"}},
		"named hidden":                          {"files = [\"docs/*\", \"docs/.*\"]\n", []string{"docs/.#a.md", "docs/.a.md", "docs/.a.md.swp", "docs/.x.spec.md", "docs/real.md", "docs/real.spec.md"}},
		"named backup":                          {"files = [\"docs/*\", \"docs/*~\"]\n", []string{"docs/a.md~", "docs/real.md", "docs/real.spec.md"}},
		"named exactly":                         {"files = [\"docs/*\", \"docs/4913\", \"docs/#a.md#\"]\n", []string{"docs/#a.md#", "docs/4913", "docs/real.md", "docs/real.spec.md"}},
		"default glob":                          {"", []string{"docs/real.spec.md"}},
		"hidden directory is not a hidden file": {"files = [\"docs/.d/*\"]\n", []string{"docs/.d/real.md"}},
	} {
		t.Run(name, func(t *testing.T) {
			p := watchProject(t, c.config, tree...)
			assert.Equal(t, c.want, p.Files, "Open")
			assert.Equal(t, c.want, p.Select(tree), "Select")
			for _, f := range tree {
				assert.Equal(t, slices.Contains(c.want, f), p.Selects(f), "Selects %s", f)
			}
		})
	}
}

// TestTempModels: the same for a model.
func TestTempModels(t *testing.T) {
	p := watchProject(t, "files = [\"*.md\"]\nformal = [\"tla/*\"]\n", "a.md", "tla/M.tla", "tla/.N.tla", "tla/.#M.tla", "tla/M.tla~")
	assert.Equal(t, []string{"tla/M.tla"}, p.Formal)
	assert.False(t, p.Selects("tla/.N.tla"))
	named := watchProject(t, "files = [\"*.md\"]\nformal = [\"tla/*\", \"tla/.N*\"]\n", "a.md", "tla/M.tla", "tla/.N.tla")
	assert.Equal(t, []string{"tla/.N.tla", "tla/M.tla"}, named.Formal)
}

func TestClientWatchGlobs(t *testing.T) {
	assert.Equal(t, []string{"**/spec.toml", "**/*.md", "**/*.tla"}, ClientWatchGlobs())
}
