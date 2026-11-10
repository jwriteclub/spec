// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/format"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/project"
)

// FormatFile is what formatting did to one spec file.
type FormatFile struct {
	File string `json:"file"`
	// Changed counts the lines that changed (or would); 0 when the file
	// is formatted already.
	Changed int `json:"changed"`
	// Refused says why the file was left as it was ("" when it was not).
	Refused string `json:"refused"`
}

// FormatResult is the outcome of Format.
type FormatResult struct {
	Files []FormatFile
	// Written is true when the changed files were written.
	Written bool
}

// Unformatted returns the files that formatting changes (or would).
func (r *FormatResult) Unformatted() []string {
	var out []string
	for _, f := range r.Files {
		if f.Changed > 0 {
			out = append(out, f.File)
		}
	}
	return out
}

// Refused returns the files that were left as they were, and why.
func (r *FormatResult) Refused() []FormatFile {
	var out []FormatFile
	for _, f := range r.Files {
		if f.Refused != "" {
			out = append(out, f)
		}
	}
	return out
}

// FormatOptions returns the formatter options of proj (spec.toml's
// [format] table).
func FormatOptions(proj *project.Project) format.Options {
	return format.Options{Width: proj.Config.Format.Width}
}

// Format formats the spec files paths of proj (all of them when paths is
// empty), whitespace only (`spec explain workflow`). With check it writes
// nothing and takes no lock; otherwise it writes the changed files under
// .spec/lock. A file the formatter refuses is left as it is, with the
// reason in its FormatFile.
func (w *Workflow) Format(proj *project.Project, paths []string, check bool) (*FormatResult, *Error) {
	for _, p := range paths {
		if !slices.Contains(proj.Files, p) {
			return nil, fail(ExitFail, "not_found", "%s is not a spec file of this project (spec ls lists them)", p)
		}
	}
	if len(paths) == 0 {
		paths = proj.Files
	}
	if !check {
		lock, err := project.AcquireLock(proj.Root, "spec fmt")
		if err != nil {
			return nil, LockErr(err)
		}
		defer func() { _ = lock.Release() }()
	}
	res := &FormatResult{}
	var edits []edit.FileEdit
	for _, p := range paths {
		src, err := os.ReadFile(filepath.Join(proj.Root, filepath.FromSlash(p)))
		if err != nil {
			return nil, internalErr(err)
		}
		out, err := format.Source(p, src, FormatOptions(proj))
		ff := FormatFile{File: p, Changed: out.Changed}
		var refused *format.RefusedError
		if errors.As(err, &refused) {
			ff.Refused = refused.Reason
		} else if err != nil {
			return nil, internalErr(err)
		}
		res.Files = append(res.Files, ff)
		if out.Changed > 0 {
			edits = append(edits, edit.FileEdit{Path: p, Snapshot: src, Splices: []edit.Splice{
				{Range: pos.Range{Start: 0, End: len(src)}, Old: string(src), New: string(out.Text)},
			}})
		}
	}
	if check || len(edits) == 0 {
		return res, nil
	}
	plan, err := edit.NewPlan(proj.Root, edits)
	if err != nil {
		return nil, internalErr(err)
	}
	if err := plan.Commit(); err != nil {
		return nil, WriteErr("spec fmt", err)
	}
	res.Written = true
	return res, nil
}
