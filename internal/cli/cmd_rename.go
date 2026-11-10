// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/project"
)

type renameFileCount struct {
	File  string `json:"file"`
	Count int    `json:"count"`
}

type renameEdit struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Role    string `json:"role"`
	InTitle bool   `json:"in_title"`
}

type renameLeft struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Kind string `json:"kind"`
}

type renameSkipped struct {
	File         string `json:"file"`
	Reason       string `json:"reason"`
	MayReference bool   `json:"may_reference"`
}

type renameData struct {
	From      string            `json:"from"`
	To        string            `json:"to"`
	DryRun    bool              `json:"dry_run"`
	Count     int               `json:"count"`
	Files     []renameFileCount `json:"files"`
	Edits     []renameEdit      `json:"edits"`
	LeftAlone []renameLeft      `json:"left_alone"`
	// SkippedFiles are spec files rename could not read (invalid UTF-8).
	SkippedFiles []renameSkipped `json:"skipped_files"`
	Trailer      string          `json:"trailer"`
}

type lockJSON struct {
	PID     int    `json:"pid"`
	Host    string `json:"host"`
	Started string `json:"started"`
	Command string `json:"command"`
}

func (s *session) renameCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "rename OLD NEW [--dry-run]",
		Short: "rename an anchor: its label and every reference, across all files",
		Long: "Matches whole names only (renaming Frame does not touch Frame.Timing). Refuses if NEW is already\n" +
			"defined or is not a valid name. Rewrites the label and every reference, titles included, and the tags\n" +
			"of formal models, and reports occurrences inside code and other opaque regions, which it leaves alone.\n" +
			"Names are written without $.",
		Args: needArgs(2, "rename OLD NEW [--dry-run]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runRename(args[0], args[1], dryRun)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would change and write nothing")
	return cmd
}

// bareName strips a leading sigil from a rename argument and rejects what a
// shell leaves of an expanded `$Name`.
func bareName(arg string) (string, *outcome) {
	a := strings.TrimPrefix(strings.TrimSpace(arg), "$")
	if a == "" || strings.HasPrefix(a, ".") {
		return "", usageErr("argument %q: %s", arg, shellHint)
	}
	return a, nil
}

func (s *session) runRename(oldArg, newArg string, dryRun bool) *outcome {
	oldName, o := bareName(oldArg)
	if o != nil {
		return o
	}
	newName, o := bareName(newArg)
	if o != nil {
		return o
	}
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}

	// Plan step 1: take the lock before loading. A dry run writes nothing and
	// so does not need it.
	if !dryRun {
		lock, err := project.AcquireLock(root, "spec rename "+oldName+" "+newName)
		if err != nil {
			var held *project.LockHeldError
			if errors.As(err, &held) {
				out := fail(exitLock, "lock_held", "another spec process holds %s: %s; wait for it to finish and retry", held.Path, held.Record.String())
				out.data = map[string]any{"lock": lockJSON{
					PID: held.Record.PID, Host: held.Record.Host,
					Started: held.Record.Started.UTC().Format("2006-01-02T15:04:05Z"), Command: held.Record.Command,
				}}
				return out
			}
			return fail(exitFail, "write_failed", "cannot take the project lock: %v", err)
		}
		defer func() { _ = lock.Release() }()
	}

	proj, o := s.openRoot(root)
	if o != nil {
		return o
	}
	loaded, err := proj.Load(nil)
	if err != nil {
		return internalErr(err)
	}
	v := newLoadedView(loaded)

	res, err := edit.PlanRename(root, loaded.Spec, oldName, newName, loaded.Formal.Files...)
	if err != nil {
		switch {
		case errors.Is(err, edit.ErrInvalidName):
			return fail(exitFail, "bad_name", "%v", err)
		case errors.Is(err, edit.ErrNameExists):
			return fail(exitFail, "name_exists", "%v", err)
		case errors.Is(err, edit.ErrNotFound):
			return fail(exitFail, "not_found", "%v", err)
		case errors.Is(err, edit.ErrAmbiguous):
			o := fail(exitFail, "ambiguous", "%v", err)
			cands := []string{}
			for _, a := range loaded.Spec.Lookup(oldName) {
				cands = append(cands, locString(a.File, v.line(a.File, a.LabelRange.Start), 0))
			}
			o.data = map[string]any{"candidates": cands}
			return o
		}
		return internalErr(err)
	}

	data := renameData{
		From: "$" + oldName, To: "$" + newName, DryRun: dryRun, Count: res.Tokens(),
		Files: []renameFileCount{}, Edits: []renameEdit{}, LeftAlone: []renameLeft{}, SkippedFiles: []renameSkipped{},
		Trailer: baseline.RenameLine{Old: oldName, New: newName}.String(),
	}
	paths := make([]string, 0, len(res.PerFile))
	for p := range res.PerFile {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		data.Files = append(data.Files, renameFileCount{File: p, Count: res.PerFile[p]})
	}
	for _, f := range loaded.Spec.Files {
		if !f.ValidUTF8 {
			continue
		}
		for _, oc := range f.Occurrences {
			if oc.Name != oldName {
				continue
			}
			line, col := v.lineCol(f.Path, oc.Range.Start)
			data.Edits = append(data.Edits, renameEdit{File: f.Path, Line: line, Col: col, Role: string(oc.Role), InTitle: oc.InTitle})
		}
	}
	for _, te := range res.Tags {
		line, col := v.lineCol(te.File, te.Range.Start)
		data.Edits = append(data.Edits, renameEdit{File: te.File, Line: line, Col: col, Role: "tag"})
	}
	for _, sk := range res.Skipped {
		data.SkippedFiles = append(data.SkippedFiles, renameSkipped{File: sk.File, Reason: sk.Reason, MayReference: sk.MayReference})
	}
	for _, la := range res.LeftAlone {
		line, col := v.lineCol(la.File, la.Range.Start)
		kind := "opaque"
		if loaded.Formal.File(la.File) != nil {
			kind = "model_comment"
		}
		if f := loaded.Spec.File(la.File); f != nil {
			for _, op := range f.Opaque {
				if op.Range.ContainsRange(la.Range) {
					kind = string(op.Kind)
					break
				}
			}
		}
		data.LeftAlone = append(data.LeftAlone, renameLeft{File: la.File, Line: line, Col: col, Kind: kind})
	}

	if !dryRun {
		if s.env.beforeCommit != nil {
			s.env.beforeCommit()
		}
		if err := res.Plan.Commit(); err != nil {
			var ce *edit.ConcurrentEditError
			var we *edit.WriteError
			switch {
			case errors.As(err, &ce):
				return fail(exitFail, "concurrent_edit", "%s changed while spec rename was running; nothing was written; run it again", ce.Path)
			case errors.As(err, &we):
				msg := fmt.Sprintf("writing %s failed: %v", we.Path, we.Err)
				if len(we.Restored) > 0 {
					msg += "; restored: " + strings.Join(we.Restored, ", ")
				}
				if len(we.RestoreFailed) > 0 {
					msg += "; COULD NOT restore (these still hold the new name): " + strings.Join(we.RestoreFailed, ", ")
				}
				return fail(exitFail, "write_failed", "%s", msg)
			}
			return fail(exitFail, "write_failed", "%v", err)
		}
		// Report the diagnostics of what is on disk now.
		if after, err := proj.Load(nil); err == nil {
			v = newLoadedView(after)
		}
	}

	var b strings.Builder
	verb := "renamed"
	if dryRun {
		verb = "would rename"
	}
	fmt.Fprintf(&b, "%s %s -> %s: %s in %s\n", verb, data.From, data.To, plural(data.Count, "token"), plural(len(data.Files), "file"))
	for _, f := range data.Files {
		fmt.Fprintf(&b, "  %s  %d\n", f.File, f.Count)
	}
	if dryRun {
		b.WriteString("edits:\n")
		for _, e := range data.Edits {
			extra := e.Role
			if e.InTitle {
				extra += ", in a title"
			}
			fmt.Fprintf(&b, "  %s  (%s)\n", locString(e.File, e.Line, e.Col), extra)
		}
	}
	if len(data.LeftAlone) > 0 {
		b.WriteString("left alone (inside code or other literal text; fix by hand if they are citations):\n")
		for _, l := range data.LeftAlone {
			fmt.Fprintf(&b, "  %s  (%s)\n", locString(l.File, l.Line, l.Col), l.Kind)
		}
	}
	for _, sk := range data.SkippedFiles {
		fmt.Fprintf(&b, "skipped %s: %s", sk.File, sk.Reason)
		if sk.MayReference {
			fmt.Fprintf(&b, "; it contains %s and may hold references that were not rewritten", oldName)
		}
		b.WriteString("\n")
	}
	if dryRun {
		b.WriteString("nothing was written\n")
	} else {
		b.WriteString("next: run `spec status`; it prints the " + data.Trailer + " line for the commit message\n")
	}
	return &outcome{data: data, diags: v.diagsJSON(), human: b.String()}
}
