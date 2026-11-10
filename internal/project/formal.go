// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/formal/tla"
	"github.com/jwriteclub/spec/internal/lint"
)

// scanners maps a model's file extension to what reads it. It lives here
// because package tla imports package formal.
var scanners = map[string]formal.Scanner{
	".tla": tla.Scan,
}

// scanVersions names the parser behind each scanner, so a cached scan is not
// reused across parser versions.
var scanVersions = map[string]string{
	".tla": tla.ParserVersion,
}

// prudence checks TLA+ models: model proposals are checked with it.
func init() {
	formal.RegisterChecker(".tla", tla.Checker{})
}

// IsModelFile reports whether a path has the extension of a model language.
func IsModelFile(rel string) bool {
	_, ok := scanners[path.Ext(rel)]
	return ok
}

// ScanModel reads a model's source; nil when no language reads its extension.
func ScanModel(rel string, src []byte) *formal.File {
	scan, ok := scanners[path.Ext(rel)]
	if !ok {
		return nil
	}
	return scan(rel, src)
}

// modelExtensions lists the extensions in scanners for messages.
func modelExtensions() string {
	exts := make([]string, 0, len(scanners))
	for e := range scanners {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	return strings.Join(exts, ", ")
}

// selectFormal selects the models. A file that both lists select stays a spec
// file, with an error saying to narrow the files entry.
func (p *Project) selectFormal() error {
	if len(p.Config.Formal) == 0 {
		return nil
	}
	spec := map[string]bool{}
	for _, f := range p.Files {
		spec[f] = true
	}
	var pats, raws []string
	for _, raw := range p.Config.Formal {
		if badPattern(raw) == "" {
			pats = append(pats, path.Clean(raw))
			raws = append(raws, raw)
		}
	}
	matched := make([]int, len(pats))
	seen := map[string]bool{}
	err := filepath.WalkDir(p.Root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if abs == p.Root {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if abs != p.Root && (skipDirs[d.Name()] || isFile(filepath.Join(abs, ConfigFile))) {
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
		if _, ok := scanners[path.Ext(rel)]; !ok {
			return nil
		}
		hit := false
		for i, pat := range pats {
			if globSelects(pat, rel) {
				matched[i]++
				hit = true
			}
		}
		if !hit || seen[rel] {
			return nil
		}
		seen[rel] = true
		switch {
		case spec[rel]:
			p.Diagnostics = append(p.Diagnostics, diag.Diagnostic{
				Code: diag.CodeConfigInvalid, Severity: diag.Error, File: ConfigFile,
				Message: fmt.Sprintf("%s is selected by both files and formal; a model is not a spec file, so narrow the files entry", rel),
			})
		case mode&fs.ModeSymlink != 0:
			p.Diagnostics = append(p.Diagnostics, diag.Diagnostic{
				Code: diag.CodeFileSymlink, Severity: diag.Error, File: rel,
				Message: fmt.Sprintf("%s is a symbolic link; formal models must be regular files (symlinks are not followed)", rel),
			})
		default:
			p.Formal = append(p.Formal, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i, raw := range raws {
		if matched[i] == 0 {
			msg := fmt.Sprintf("formal entry %q matches no model file (%s)", raw, modelExtensions())
			if p.matchesSkipped(pats[i]) {
				msg = fmt.Sprintf("formal entry %q matches only files in skipped directories (.git, node_modules, build, vendor, .spec, or a nested project with its own spec.toml)", raw)
			}
			p.Diagnostics = append(p.Diagnostics, diag.Diagnostic{
				Code: diag.CodeProjectFileMissing, Severity: diag.Error, File: ConfigFile,
				Range: findString(p.configSrc, raw), Message: msg,
			})
		}
	}
	sort.Strings(p.Formal)
	return nil
}

// IsModelPath reports whether a path that may not exist on disk would be
// selected as a model: an unsaved buffer counts as one.
func (p *Project) IsModelPath(rel string) bool {
	if _, ok := scanners[path.Ext(rel)]; !ok {
		return false
	}
	for _, raw := range p.Config.Formal {
		if badPattern(raw) != "" {
			continue
		}
		if globSelects(path.Clean(raw), rel) {
			return !p.insideNestedProject(rel)
		}
	}
	return false
}

// loadFormal scans the models, preferring the overlay's unsaved buffers. No
// model can fail a spec operation, since none is part of the spec: one deleted
// since Open (a long-lived Project, as in spec ui) is skipped, and one that
// cannot be read is kept with its ReadError, for lint to report.
func (p *Project) loadFormal(overlay map[string][]byte, cache *ScanCache) []*formal.File {
	paths := append([]string(nil), p.Formal...)
	have := map[string]bool{}
	for _, f := range paths {
		have[f] = true
	}
	for _, f := range p.Files {
		have[f] = true
	}
	for rel := range overlay {
		if !have[rel] && p.IsModelPath(rel) {
			paths = append(paths, rel)
		}
	}
	out := make([]*formal.File, 0, len(paths))
	for _, rel := range paths {
		src, ok := overlay[rel]
		if !ok {
			var err error
			src, err = os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(rel)))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				f := formal.NewFile(rel, nil)
				f.ReadError = err.Error()
				out = append(out, f)
				continue
			}
		}
		out = append(out, cache.Scan(rel, src))
	}
	return out
}

// LintOptions returns what lint.Run needs to report on the project and its
// models together.
func (l *Loaded) LintOptions() lint.Options {
	extra := append([]diag.Diagnostic(nil), l.Project.Diagnostics...)
	extra = append(extra, l.Formal.Lint()...)
	extra = append(extra, CheckDiagnostics(l.ModelChecks(context.Background()))...)
	return lint.Options{Overrides: l.Project.Config.Lint, Extra: extra, Covered: l.Formal.Covered, Cited: l.Formal.Cited}
}

// ModelChecks checks the loaded models fully (see CheckCache.CheckModels),
// within spec.toml's limits, reusing the checks of the cache the load used.
func (l *Loaded) ModelChecks(ctx context.Context) map[string]ModelCheck {
	return l.scans.Checks().CheckModels(ctx, l.Project.Root, l.Formal.Files, l.Project.Config.CheckLimits())
}

// CheckDiagnostics returns the findings of checks, in path order.
func CheckDiagnostics(checks map[string]ModelCheck) []diag.Diagnostic {
	paths := make([]string, 0, len(checks))
	for p := range checks {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []diag.Diagnostic
	for _, p := range paths {
		out = append(out, checks[p].Diags...)
	}
	return out
}
