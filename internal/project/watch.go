// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// ClientWatchGlobs are the patterns an editor is asked to watch for the
// language server: spec.toml, Markdown (what spec files are) and each model
// language's extension. A files entry that selects something other than
// Markdown is still found, at the next analysis anything else causes.
func ClientWatchGlobs() []string {
	out := []string{"**/" + ConfigFile, "**/*.md"}
	exts := make([]string, 0, len(scanners))
	for e := range scanners {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	for _, e := range exts {
		out = append(out, "**/*"+e)
	}
	return out
}

// globs returns the cleaned, usable patterns that select spec files (the
// default one when spec.toml names none) and models.
func (p *Project) globs() (files, models []string) {
	files = []string{DefaultGlob}
	if len(p.Config.Files) > 0 {
		files = p.patterns()
	}
	for _, raw := range p.Config.Formal {
		if badPattern(raw) == "" {
			models = append(models, path.Clean(raw))
		}
	}
	return files, models
}

// Selects reports whether rel is a name the project's globs select, before the
// directory exclusions and whether the file exists. A file watcher uses it to
// drop events about files that cannot matter.
func (p *Project) Selects(rel string) bool {
	files, models := p.globs()
	for _, pat := range files {
		if globSelects(pat, rel) {
			return true
		}
	}
	if !IsModelFile(rel) {
		return false
	}
	for _, pat := range models {
		if globSelects(pat, rel) {
			return true
		}
	}
	return false
}

// Retain adds rel, an unsaved buffer whose file is gone, to the list that
// would select it, so the buffer goes on counting as it did while the file was
// there; Load leaves it out unless it is on disk or in the overlay. It reports
// whether either list holds rel afterwards.
func (p *Project) Retain(rel string) bool {
	if slices.Contains(p.Files, rel) || slices.Contains(p.Formal, rel) {
		return true
	}
	switch {
	case len(p.Select([]string{rel})) == 1 && !p.insideNestedProject(rel):
		p.Files = insertSorted(p.Files, rel)
		if p.retained == nil {
			p.retained = map[string]bool{}
		}
		p.retained[rel] = true
	case p.IsModelPath(rel):
		p.Formal = insertSorted(p.Formal, rel)
	default:
		return false
	}
	return true
}

func insertSorted(list []string, s string) []string {
	i, _ := slices.BinarySearch(list, s)
	return slices.Insert(list, i, s)
}

// WatchDirs returns what to watch so that any change to the project's file set
// is seen, as project-relative slash paths ("." is the root, always present for
// spec.toml). It costs as little as it can, because kqueue holds a file
// descriptor per entry of a watched directory: a glob contributes its literal
// prefix and the directories above it (tla/*.tla means tla), the deepest ancestor
// that exists when the prefix does not yet (so its creation is seen), and every
// directory below a ** or a directory wildcard. Directories the file set never
// enters are skipped (skipDirs and nested projects), and so are hidden ones
// unless a glob names them. A nested project contributes its spec.toml alone,
// the one file there that can change this project's set.
func (p *Project) WatchDirs() []string {
	out := map[string]bool{".": true}
	files, models := p.globs()
	for _, pat := range append(files, models...) {
		for _, plain := range expandBraces(pat) {
			p.watchComps(".", strings.Split(plain, "/"), out)
		}
	}
	dirs := make([]string, 0, len(out))
	for d := range out {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}

// expandBraces writes out pat's brace groups, so that one holding a slash or a **
// is split like any other pattern. An unbalanced brace is left: such a pattern
// matches nothing anyway.
func expandBraces(pat string) []string {
	open, depth := -1, 0
	var cuts []int // the commas of the first group
	for i := 0; i < len(pat); i++ {
		switch pat[i] {
		case '\\':
			i++
		case '{':
			if depth == 0 {
				open = i
			}
			depth++
		case ',':
			if depth == 1 {
				cuts = append(cuts, i)
			}
		case '}':
			if depth > 0 {
				depth--
			}
			if depth == 0 && open >= 0 {
				var out []string
				from := open + 1
				for _, to := range append(cuts, i) {
					out = append(out, expandBraces(pat[:open]+pat[from:to]+pat[i+1:])...)
					from = to + 1
				}
				return out
			}
		}
	}
	return []string{pat}
}

// watchComps adds what the remaining components of a glob need, from dir.
func (p *Project) watchComps(dir string, comps []string, out map[string]bool) {
	c := comps[0]
	switch {
	case c == "**":
		p.watchTree(dir, comps[1:], out)
	case len(comps) == 1:
		out[dir] = true
	case strings.ContainsAny(c, "*?[{"):
		out[dir] = true
		// A leading * is not asking for hidden directories. Anything else may be
		// (?h, [.]h, .*), and the match decides.
		for _, name := range p.subdirs(dir, !strings.HasPrefix(c, "*")) {
			if ok, _ := doublestar.Match(c, name); ok && !p.watchNested(path.Join(dir, name), out) {
				p.watchComps(path.Join(dir, name), comps[1:], out)
			}
		}
	default:
		out[dir] = true // for sub being renamed, created or deleted
		sub := path.Join(dir, c)
		if skipDirs[c] || p.watchNested(sub, out) {
			return // the file set never enters it, whatever the glob says
		}
		st, err := os.Lstat(filepath.Join(p.Root, filepath.FromSlash(sub)))
		if err != nil || !st.IsDir() {
			return // the walk does not follow a symlink either
		}
		p.watchComps(sub, comps[1:], out)
	}
}

// watchTree adds dir and every directory below it the file set can enter, for a
// **. What follows the ** starts at each of them, which is how it can name a
// hidden directory the tree does not enter.
func (p *Project) watchTree(dir string, rest []string, out map[string]bool) {
	out[dir] = true
	if len(rest) > 1 {
		p.watchComps(dir, rest, out)
	}
	for _, name := range p.subdirs(dir, false) {
		if sub := path.Join(dir, name); !p.watchNested(sub, out) {
			p.watchTree(sub, rest, out)
		}
	}
}

// watchNested reports whether dir is a nested project, adding its spec.toml.
func (p *Project) watchNested(dir string, out map[string]bool) bool {
	if !p.nestedOnDisk(dir) {
		return false
	}
	out[path.Join(dir, ConfigFile)] = true
	return true
}

// subdirs lists the directories in dir worth entering. Symlinks are not
// directories here, as in the walk that selects files.
func (p *Project) subdirs(dir string, hidden bool) []string {
	ents, err := os.ReadDir(filepath.Join(p.Root, filepath.FromSlash(dir)))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() && !skipDirs[name] && (hidden || !strings.HasPrefix(name, ".")) {
			names = append(names, name)
		}
	}
	return names
}
