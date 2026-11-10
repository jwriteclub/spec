// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/jwriteclub/spec/internal/formal"
)

// maxModuleBytes bounds one resolved module's size: a model is text, and
// a larger file is not one.
const maxModuleBytes = 8 << 20

// ModuleResolver returns the resolver for checking the model at model, a
// project-relative slash path under root. It looks a module up the way
// SANY does, in the model's own directory as <name>.tla, and in the
// workspace before the checker's standard modules: a project with its own
// Naturals.tla gets that file, not the embedded one. Lookup order:
//
//  1. open, the unsaved text of a project-relative path (editor buffers);
//  2. the file on disk, if it stays under root once symlinks are resolved.
//
// A name that is not a plain module identifier, a path that leaves root, a
// directory named like a module, and a file whose name differs from
// <name>.tla in case all count as not found, so the checker falls back to
// its standard modules. The case rule holds on case-insensitive file
// systems too (macOS), where naturals.tla would otherwise answer for
// Naturals: lookups behave the same on every machine. The error never says
// where it looked. Library paths (SANY's -DTLA-Library) are not supported.
func ModuleResolver(root, model string, open func(rel string) ([]byte, bool)) formal.Resolver {
	r := &moduleResolver{root: root, dir: path.Dir(path.Clean(model)), open: open}
	return r.resolve
}

type moduleResolver struct {
	root string
	dir  string
	open func(rel string) ([]byte, bool)
}

func (r *moduleResolver) resolve(name string) (string, []byte, bool) {
	if !ValidModuleName(name) || !localRel(r.dir) {
		return "", nil, false
	}
	rel := path.Join(r.dir, name+".tla")
	if r.open != nil {
		if src, ok := r.open(rel); ok {
			return rel, src, true
		}
	}
	src, ok := readUnder(r.root, rel)
	if !ok {
		return "", nil, false
	}
	return rel, src, true
}

// ValidModuleName reports whether name can only be a TLA+ module name: a
// nonempty run of ASCII letters, digits and underscores, so never a path
// (no "/", "\", ":" or "..").
func ValidModuleName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// localRel reports whether a cleaned slash path stays inside its base.
func localRel(rel string) bool {
	return rel != ".." && !strings.HasPrefix(rel, "../") && !path.IsAbs(rel) && !strings.Contains(rel, ":") && !strings.Contains(rel, "\\")
}

// readUnder reads the regular file at rel under root, only when its real
// path (symlinks resolved) stays under root's real path, its name matches in
// case, and it is not a directory. Containment is checked before anything
// is listed or read: the directory's real path first, so a symlinked
// directory that leaves root is never listed, then the file's.
func readUnder(root, rel string) ([]byte, bool) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, false
	}
	realDir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil || !within(realRoot, realDir) {
		return nil, false
	}
	if !exactName(filepath.Join(realDir, filepath.Base(abs))) {
		return nil, false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil || !within(realRoot, real) {
		return nil, false
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxModuleBytes {
		return nil, false
	}
	src, err := io.ReadAll(io.LimitReader(f, maxModuleBytes+1))
	if err != nil || len(src) > maxModuleBytes {
		return nil, false
	}
	return src, true
}

// within reports whether the real path p is root or lies under it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// readDir lists a directory; tests replace it to see what was listed.
var readDir = os.ReadDir

// exactName reports whether abs's directory holds an entry spelled exactly
// as abs's base name, which a case-insensitive Stat cannot tell.
func exactName(abs string) bool {
	ents, err := readDir(filepath.Dir(abs))
	if err != nil {
		return false
	}
	base := filepath.Base(abs)
	for _, e := range ents {
		if e.Name() == base {
			return true
		}
	}
	return false
}
