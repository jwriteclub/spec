// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/mdspec"
)

// skipDirs are directory names the file set never enters, with the default
// glob and explicit files entries alike.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "build": true, "vendor": true, ".spec": true,
}

// NotFoundError says no spec.toml was found. Suggestions lists spec.toml
// directories found in the surrounding git work tree (relative to the
// starting directory), for the --project hint.
type NotFoundError struct {
	// Start is the directory the search started from (or the --project dir).
	Start string
	// Explicit is true when Start came from --project.
	Explicit    bool
	Suggestions []string
}

func (e *NotFoundError) Error() string {
	var b strings.Builder
	if e.Explicit {
		fmt.Fprintf(&b, "no %s in %s", ConfigFile, e.Start)
	} else {
		fmt.Fprintf(&b, "no %s found in %s or any parent directory", ConfigFile, e.Start)
	}
	if len(e.Suggestions) > 0 {
		b.WriteString("; found projects you can pass to --project:")
		for _, s := range e.Suggestions {
			b.WriteString("\n  " + s)
		}
	}
	return b.String()
}

// Discover walks up from start looking for spec.toml and returns the
// absolute directory holding it. On failure it returns a *NotFoundError
// with suggestions, which runs git and walks the repository.
func Discover(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if root, ok := findRoot(abs); ok {
		return root, nil
	}
	return "", &NotFoundError{Start: abs, Suggestions: Suggest(abs)}
}

// FindRoot is Discover without the suggestions: it only stats spec.toml in
// start and its parents, so an editor can call it on every edit. ok is
// false when no directory holds one.
func FindRoot(start string) (root string, ok bool) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	return findRoot(abs)
}

// findRoot walks up from the absolute directory abs.
func findRoot(abs string) (string, bool) {
	for dir := abs; ; {
		if isFile(filepath.Join(dir, ConfigFile)) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Resolve returns the project root: dir when it is non-empty (it must
// contain spec.toml), otherwise Discover(cwd).
func Resolve(dir, cwd string) (string, error) {
	if dir == "" {
		return Discover(cwd)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if !isFile(filepath.Join(abs, ConfigFile)) {
		return "", &NotFoundError{Start: abs, Explicit: true, Suggestions: Suggest(abs)}
	}
	return abs, nil
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// suggestDepth is how deep below the git top level Suggest looks.
const suggestDepth = 4

// Suggest lists directories containing spec.toml within the git work tree
// that contains dir (depth at most 4 below its top level), relative to dir
// and slash-separated. It returns nil outside a git work tree or without
// git.
func Suggest(dir string) []string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real // git reports the real path of the top level
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return nil
	}
	var found []string
	_ = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(top, p)
		if rerr != nil {
			return nil
		}
		depth := 0
		if rel != "." {
			depth = strings.Count(rel, string(filepath.Separator)) + 1
		}
		if d.IsDir() {
			if p != top && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			if depth > suggestDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ConfigFile && d.Type().IsRegular() {
			found = append(found, filepath.Dir(p))
		}
		return nil
	})
	var result []string
	for _, f := range found {
		rel, err := filepath.Rel(dir, f)
		if err != nil {
			rel = f
		}
		result = append(result, filepath.ToSlash(rel))
	}
	sort.Strings(result)
	return result
}

// Project is an opened project: its configuration and the set of spec files
// it selects.
type Project struct {
	// Root is the absolute directory holding spec.toml.
	Root string
	// Name is Config.Name, or the directory name when that is empty.
	Name string
	// Config is the parsed spec.toml.
	Config Config
	// Files are the selected spec files as project-relative slash paths,
	// sorted by byte order, deduplicated, symlinks excluded.
	Files []string
	// Formal are the selected formal models, a separate set from Files, as
	// project-relative slash paths, sorted, symlinks excluded.
	Formal []string
	// Diagnostics are the project-level findings: config-invalid,
	// project-file-missing, project-no-files and file-symlink. lint.Run
	// takes them as Options.Extra.
	Diagnostics []diag.Diagnostic

	configSrc []byte
	// retained holds the unsaved drafts Retain added to Files or Formal, which may
	// have no file behind them.
	retained map[string]bool
}

// ConfigSource returns the raw bytes of spec.toml (diagnostic ranges index
// them).
func (p *Project) ConfigSource() []byte {
	return p.configSrc
}

// Open reads root/spec.toml and selects the spec files. A missing spec.toml
// is a *NotFoundError; every other problem is a diagnostic on the returned
// Project.
func Open(root string) (*Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	src, err := os.ReadFile(filepath.Join(abs, ConfigFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &NotFoundError{Start: abs, Explicit: true, Suggestions: Suggest(abs)}
		}
		return nil, err
	}
	cfg, ds := ParseConfig(src)
	p := &Project{Root: abs, Config: cfg, Diagnostics: ds, configSrc: src}
	p.Name = cfg.Name
	if p.Name == "" {
		p.Name = filepath.Base(abs)
	}
	if err := p.selectFiles(); err != nil {
		return nil, err
	}
	if err := p.selectFormal(); err != nil {
		return nil, err
	}
	diag.Sort(p.Diagnostics)
	return p, nil
}

// tempName reports whether a file's base name marks it as someone's working
// file rather than a document: any hidden file (Vim's .x.swp, Emacs's .#x lock,
// an atomic save's temp), a backup (x~, #x#), Vim's write probe (4913).
func tempName(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") ||
		(strings.HasPrefix(name, "#") && strings.HasSuffix(name, "#")) || name == "4913"
}

// globSelects reports whether the cleaned pattern pat selects the file rel. A
// temp name takes a pattern whose own last component looks like one (.*, *~):
// docs/* is not asking for docs/.a.md.swp.
func globSelects(pat, rel string) bool {
	ok, _ := doublestar.Match(pat, rel)
	return ok && (!tempName(path.Base(rel)) || tempName(path.Base(pat)))
}

// patterns returns the usable files entries, cleaned. Entries rejected as
// invalid (and already reported as config-invalid) are dropped.
func (p *Project) patterns() []string {
	var out []string
	for _, pat := range p.Config.Files {
		if badPattern(pat) == "" {
			out = append(out, path.Clean(pat))
		}
	}
	return out
}

func (p *Project) selectFiles() error {
	seen := map[string]bool{}
	var files []string
	add := func(rel string, mode fs.FileMode) {
		if seen[rel] {
			return
		}
		seen[rel] = true
		if mode&fs.ModeSymlink != 0 {
			p.Diagnostics = append(p.Diagnostics, diag.Diagnostic{
				Code: diag.CodeFileSymlink, Severity: diag.Error, File: rel,
				Message: fmt.Sprintf("%s is a symbolic link; spec files must be regular files (symlinks are not followed)", rel),
			})
			return
		}
		if mode.IsRegular() {
			files = append(files, rel)
		}
	}

	// One walk serves the default glob and explicit files entries alike, so
	// both skip the same directories (skipDirs and nested projects).
	pats := []string{DefaultGlob}
	var raws []string
	explicit := len(p.Config.Files) > 0
	if explicit {
		pats = nil
		for _, raw := range p.Config.Files {
			if badPattern(raw) != "" {
				continue // already reported as config-invalid
			}
			pats = append(pats, path.Clean(raw))
			raws = append(raws, raw)
		}
	}
	matched := make([]int, len(pats))
	err := filepath.WalkDir(p.Root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if abs == p.Root {
				return err
			}
			return nil // an unreadable subdirectory is skipped
		}
		if d.IsDir() {
			if abs == p.Root {
				return nil
			}
			if skipDirs[d.Name()] || isFile(filepath.Join(abs, ConfigFile)) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(p.Root, abs)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		mode := d.Type()
		if !mode.IsRegular() && mode&fs.ModeSymlink == 0 {
			return nil
		}
		hit := false
		for i, pat := range pats {
			if globSelects(pat, rel) {
				matched[i]++
				hit = true
			}
		}
		if hit {
			add(rel, mode)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if explicit {
		for i, raw := range raws {
			if matched[i] > 0 {
				continue
			}
			msg := fmt.Sprintf("files entry %q matches no file", raw)
			if p.matchesSkipped(pats[i]) {
				msg = fmt.Sprintf("files entry %q matches only files in skipped directories (.git, node_modules, build, vendor, .spec, or a nested project with its own spec.toml)", raw)
			}
			p.Diagnostics = append(p.Diagnostics, diag.Diagnostic{
				Code: diag.CodeProjectFileMissing, Severity: diag.Error, File: ConfigFile,
				Range: findString(p.configSrc, raw), Message: msg,
			})
		}
	}
	sort.Strings(files)
	p.Files = files
	if len(files) == 0 {
		msg := "no spec files selected: create one (spec init) or fix the files list in spec.toml"
		if len(p.Config.Files) == 0 {
			msg = fmt.Sprintf("no spec files found: nothing under %s matches %s", p.Root, DefaultGlob)
		}
		p.Diagnostics = append(p.Diagnostics, diag.Diagnostic{
			Code: diag.CodeProjectNoFiles, Severity: diag.Error, File: ConfigFile, Message: msg,
		})
	}
	return nil
}

// Select filters candidate project-relative slash paths (for example every
// file in a git tree) down to those this project's spec.toml would select,
// using the same rules as Open but without touching the disk: with the
// default glob or explicit files entries alike, paths below skipped
// directories and below any subdirectory that holds its own spec.toml among
// the candidates are dropped. The result is sorted and deduplicated.
func (p *Project) Select(candidates []string) []string {
	have := map[string]bool{}
	for _, c := range candidates {
		have[path.Clean(c)] = true
	}
	pats := p.patterns()
	explicit := len(p.Config.Files) > 0
	seen := map[string]bool{}
	var out []string
	for _, c := range candidates {
		c = path.Clean(c)
		if c == "." || strings.HasPrefix(c, "../") || seen[c] {
			continue
		}
		ok := false
		if explicit {
			for _, pat := range pats {
				if globSelects(pat, c) {
					ok = true
					break
				}
			}
		} else if globSelects(DefaultGlob, c) {
			ok = true
		}
		if ok && inSkipped(c, func(dir string) bool { return have[dir+"/"+ConfigFile] }) {
			ok = false
		}
		if ok {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// Loaded is a project read into a resolved spec.
type Loaded struct {
	Project *Project
	// Files are the parsed files, in project order.
	Files []*mdspec.File
	Spec  *mdspec.Spec
	// Formal is the project's formal models joined to Spec; never nil.
	Formal *formal.Index

	// scans is the cache the load used, whose check cache LintOptions uses.
	scans *ScanCache
}

// Load opens the project at root, reads its spec files from disk and builds
// the Spec. overlay maps project-relative slash paths to unsaved buffer
// contents: an entry replaces the on-disk bytes of a selected file, and an
// entry for a path that the project would select but that is not on disk
// adds it. A nil overlay reads disk only. Project-level diagnostics are on
// Loaded.Project.Diagnostics; pass them to lint.Run as Options.Extra.
func Load(root string, overlay map[string][]byte) (*Loaded, error) {
	return LoadWith(root, overlay, nil)
}

// LoadWith is Load taking unchanged models from cache (nil scans them all).
func LoadWith(root string, overlay map[string][]byte, cache *ScanCache) (*Loaded, error) {
	p, err := Open(root)
	if err != nil {
		return nil, err
	}
	return p.LoadWith(overlay, cache)
}

// Load reads the project's files, applying overlay (see the package-level
// Load).
func (p *Project) Load(overlay map[string][]byte) (*Loaded, error) {
	return p.LoadWith(overlay, nil)
}

// LoadWith is Load taking unchanged models from cache (nil scans them all).
func (p *Project) LoadWith(overlay map[string][]byte, cache *ScanCache) (*Loaded, error) {
	clean := map[string][]byte{}
	for k, v := range overlay {
		clean[path.Clean(filepath.ToSlash(k))] = v
	}
	paths := append([]string(nil), p.Files...)
	have := map[string]bool{}
	for _, f := range paths {
		have[f] = true
	}
	var extra []string
	for k := range clean {
		if !have[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		for _, rel := range p.Select(extra) {
			if p.insideNestedProject(rel) {
				continue
			}
			paths = append(paths, rel)
		}
		sort.Strings(paths)
	}
	l := &Loaded{Project: p, scans: cache}
	for _, rel := range paths {
		src, ok := clean[rel]
		if !ok {
			var err error
			src, err = os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(rel)))
			if p.retained[rel] && errors.Is(err, fs.ErrNotExist) {
				continue // a draft with no file: not part of what the disk holds
			}
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", rel, err)
			}
		}
		l.Files = append(l.Files, mdspec.ParseFile(rel, src))
	}
	l.Spec = mdspec.Build(l.Files)
	l.Formal = formal.Join(l.Spec, p.loadFormal(clean, cache))
	return l, nil
}

// insideNestedProject reports whether a directory between the root and rel
// is skipped: a skipDirs name, or one that holds its own spec.toml on disk.
func (p *Project) insideNestedProject(rel string) bool {
	return inSkipped(rel, p.nestedOnDisk)
}

// nestedOnDisk reports whether the project-relative directory dir holds its
// own spec.toml on disk.
func (p *Project) nestedOnDisk(dir string) bool {
	return isFile(filepath.Join(p.Root, filepath.FromSlash(dir), ConfigFile))
}

// inSkipped reports whether a directory on the way from the project root to
// the slash path rel is one the file set skips: a skipDirs name, or a nested
// project (nested reports whether a project-relative directory holds its
// own spec.toml).
func inSkipped(rel string, nested func(dir string) bool) bool {
	parts := strings.Split(rel, "/")
	for i := 0; i < len(parts)-1; i++ {
		if skipDirs[parts[i]] || nested(strings.Join(parts[:i+1], "/")) {
			return true
		}
	}
	return false
}

// matchesSkipped reports whether pattern matches some regular file or
// symlink below a skipped directory, for the project-file-missing message.
// It runs only for an entry that matched nothing.
func (p *Project) matchesSkipped(pattern string) bool {
	matches, err := doublestar.Glob(os.DirFS(p.Root), pattern, doublestar.WithNoFollow(), doublestar.WithFilesOnly())
	if err != nil {
		return false
	}
	for _, m := range matches {
		if inSkipped(m, p.nestedOnDisk) {
			return true
		}
	}
	return false
}
