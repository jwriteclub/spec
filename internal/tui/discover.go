// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/project"
)

// projectScan is a fresh reading of spec.toml and the globs, made off the UI
// goroutine: the source of truth for which files are in the project.
type projectScan struct {
	// project and loaded are the new file set and its files as on disk; nil
	// when the project in use stays.
	project *project.Project
	loaded  *project.Loaded
	// unreadable lists the selected spec files newly found impossible to read:
	// left out.
	unreadable []string
	// warn says why spec.toml was not taken.
	warn string
}

// mergeScan combines a scan with a later one the UI has not seen.
func mergeScan(old, next *projectScan) *projectScan {
	if old == nil {
		return next
	}
	if next == nil {
		return old
	}
	merged := *next
	if next.project == nil {
		merged = *old
		merged.warn = next.warn
	}
	merged.unreadable = slices.Concat(old.unreadable, next.unreadable)
	return &merged
}

// configProblem returns why p's spec.toml is unusable, or "".
func configProblem(p *project.Project) string {
	for _, d := range p.Diagnostics {
		if d.Code == diag.CodeConfigInvalid && d.Severity == diag.Error {
			return d.Message
		}
	}
	return ""
}

// globProject opens the project at root. With a warning p is nil and cur stays:
// spec.toml is gone, or changed from cur's and does not parse (one that was
// already bad when cur was read is no news, and does not stop discovery).
func globProject(root string, cur *project.Project) (p *project.Project, unreadable []string, warn string) {
	keep := func(format string, args ...any) (*project.Project, []string, string) {
		if cur == nil {
			return nil, nil, "" // no project to keep: nothing to say either
		}
		return nil, nil, fmt.Sprintf(format, args...)
	}
	if _, err := os.Stat(filepath.Join(root, project.ConfigFile)); errors.Is(err, fs.ErrNotExist) {
		// Before project.Open, which would look for suggestions with git.
		return keep("spec.toml was deleted · keeping the previous settings")
	}
	p, err := project.Open(root)
	if err != nil {
		return keep("the project cannot be read · keeping the previous settings · %v", err)
	}
	if cur != nil && !bytes.Equal(cur.ConfigSource(), p.ConfigSource()) {
		if msg := configProblem(p); msg != "" {
			return keep("spec.toml has an error · keeping the previous settings · %s", msg)
		}
	}
	return p, dropUnreadable(p), ""
}

// dropUnreadable takes the spec files that cannot be opened out of p: Load
// would fail the whole project over one. A model cannot fail it; lint reports it.
func dropUnreadable(p *project.Project) []string {
	var bad []string
	keep := p.Files[:0:0]
	for _, rel := range p.Files {
		f, err := os.Open(filepath.Join(p.Root, filepath.FromSlash(rel)))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				bad = append(bad, rel)
			}
			continue
		}
		_ = f.Close()
		keep = append(keep, rel)
	}
	p.Files = keep
	return bad
}

// sameProject reports whether nothing the UI shows differs between two globs.
// Content changes are not looked at: those go through Refresh.
func sameProject(a, b *project.Project) bool {
	return slices.Equal(a.Files, b.Files) && slices.Equal(a.Formal, b.Formal) &&
		bytes.Equal(a.ConfigSource(), b.ConfigSource()) && reflect.DeepEqual(a.Diagnostics, b.Diagnostics)
}

// loadScan reads the files of p. The scan holds its own copy of p's lists: the
// UI adds retained drafts to them while the watcher goes on reading its copy.
func loadScan(p *project.Project, unreadable []string) (*projectScan, error) {
	loaded, err := p.Load(nil)
	if err != nil {
		return nil, err
	}
	own := *p
	own.Files, own.Formal = slices.Clone(p.Files), slices.Clone(p.Formal)
	return &projectScan{project: &own, loaded: loaded, unreadable: unreadable}, nil
}

// readScan is a scan on the caller's goroutine, nil when nothing changed: for a
// report that carries none (a replay, or a test delivering a change).
func readScan(root string, cur *project.Project) *projectScan {
	p, unreadable, warn := globProject(root, cur)
	switch {
	case warn != "":
		return &projectScan{warn: warn}
	case p == nil || sameProject(cur, p):
		return nil
	}
	scan, err := loadScan(p, unreadable)
	if err != nil {
		lg.WithError(err).Warn("reading the project's files")
		return nil
	}
	return scan
}

// membersOf returns p's spec files and models.
func membersOf(p *project.Project) map[string]bool {
	out := make(map[string]bool, len(p.Files)+len(p.Formal))
	for _, f := range p.Files {
		out[f] = true
	}
	for _, f := range p.Formal {
		out[f] = true
	}
	return out
}

// isKnown reports whether the workspace holds a parse of path: a project file,
// or an open document that left the project.
func (ws *Workspace) isKnown(path string) bool {
	return ws.files[path] != nil || ws.IsModel(path)
}

// membershipInQuestion reports whether paths say something a re-glob must
// settle: spec.toml, or a file whose existence differs from what is known.
func (a *App) membershipInQuestion(paths []string) bool {
	for _, p := range paths {
		if p == project.ConfigFile {
			return true
		}
		_, err := os.Stat(filepath.Join(a.ws.Root, filepath.FromSlash(p)))
		if a.ws.isKnown(p) == errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// discovery is what a scan did to the project.
type discovery struct {
	added, removed, unreadable []string
	config                     bool
	// goneCurrent is the file on screen, when it is no longer on disk.
	goneCurrent string
	// refresh lists open files whose disk content differs from their buffer's
	// base: Refresh decides what that means.
	refresh []string
	// changed says the project changed, so what depends on HEAD's selection
	// may have too.
	changed bool
}

// applyScan makes scan the project. It leaves alone what the user is doing:
// buffers, the cursor, the current file, a prompt. A popup holds offsets and
// goes only if a file moved under them.
func (a *App) applyScan(scan *projectScan) discovery {
	var d discovery
	if scan == nil {
		return d
	}
	if scan.warn != "" {
		a.setStatus(statusWarning, "spec.toml", "%s", a.theme.uiText(scan.warn))
	}
	d.unreadable = scan.unreadable
	if scan.project == nil {
		return d
	}
	old := a.ws.Project
	was := membersOf(old)
	// The scan may predate ^O writing an open file back; the disk has the last word.
	for path := range a.ws.docs {
		if was[path] && !slices.Contains(scan.unreadable, path) && isRegularFile(a.ws.Root, path) {
			scan.project.Retain(path)
		}
	}
	if sameProject(old, scan.project) {
		return d
	}
	refresh, moved := a.ws.adopt(scan.project, scan.loaded, false)
	now := membersOf(a.ws.Project)
	for p := range now {
		if _, open := a.ws.docs[p]; !was[p] && !open {
			d.added = append(d.added, p)
		}
	}
	for p := range was {
		switch {
		case now[p] || slices.Contains(scan.unreadable, p):
		case p == a.cur.Ed.Path() && !onDisk(a.ws.Root, p):
			d.goneCurrent = p
		default:
			d.removed = append(d.removed, p)
		}
	}
	slices.Sort(d.added)
	slices.Sort(d.removed)
	d.refresh = refresh
	d.config = !bytes.Equal(old.ConfigSource(), a.ws.Project.ConfigSource())
	d.changed = true
	a.closeGoneDocs()
	a.watcher.setOpen(a.openDocs())
	a.outline.rebuild(a.ws)
	a.rebuildCards()
	a.layout()
	if (moved || len(d.removed) > 0 || d.goneCurrent != "") && a.popup != nil && !a.popup.notice {
		a.popup = nil
	}
	return d
}

// closeGoneDocs closes clean documents whose file left the project and the disk.
// The current one stays on screen: nothing is lost, and it may be coming back.
func (a *App) closeGoneDocs() {
	for path, d := range a.ws.docs {
		if d == a.cur || d.Ed.Buffer().Dirty() || slices.Contains(a.ws.Project.Files, path) || slices.Contains(a.ws.Project.Formal, path) {
			continue
		}
		if !onDisk(a.ws.Root, path) {
			a.ws.closeDoc(path)
		}
	}
}

// openDocs returns the paths of the open documents.
func (a *App) openDocs() map[string]bool {
	out := make(map[string]bool, len(a.ws.docs))
	for path := range a.ws.docs {
		out[path] = true
	}
	return out
}

// isRegularFile reports whether a regular file is at the project-relative path.
func isRegularFile(root, path string) bool {
	st, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
	return err == nil && st.Mode().IsRegular()
}

// onDisk reports whether anything is at the project-relative path.
func onDisk(root, path string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
	return !errors.Is(err, fs.ErrNotExist)
}

// closeDoc forgets an open document.
func (ws *Workspace) closeDoc(path string) {
	delete(ws.docs, path)
	delete(ws.parsedAt, path)
	delete(ws.files, path)
	delete(ws.models, path)
}

// text is the flash for a discovery: the file's name, or a count for a burst.
func (d discovery) text() string {
	var parts []string
	if d.goneCurrent != "" {
		parts = append(parts, d.goneCurrent+" was deleted on disk; ^O writes it back")
	}
	if d.config {
		parts = append(parts, "Reloaded spec.toml")
	}
	count := func(list []string, one, many string) {
		switch len(list) {
		case 0:
		case 1:
			parts = append(parts, fmt.Sprintf(one, list[0]))
		default:
			parts = append(parts, fmt.Sprintf(many, len(list)))
		}
	}
	count(d.added, "Found new file %s", "Found %d new files")
	count(d.removed, "%s left the project", "%d files left the project")
	count(d.unreadable, "%s cannot be read", "%d files cannot be read")
	return strings.Join(parts, "; ")
}

// staleReport reports whether msg came from a watcher a settings reload replaced.
func (a *App) staleReport(msg fileChangedMsg) bool {
	return msg.from != nil && msg.from != a.watcher
}

// onWatched applies a watcher's report: the scan first, then each changed file.
// A watcher has re-globbed where it matters, off this goroutine, so only a report
// from something else gets a re-glob here. A stale report's scan is older than
// the reload that replaced its watcher; its content changes are still news.
func (a *App) onWatched(msg fileChangedMsg) tea.Cmd {
	scan := msg.scan
	switch {
	case a.staleReport(msg):
		scan = nil
	case scan == nil && msg.from == nil && a.membershipInQuestion(msg.paths):
		scan = readScan(a.ws.Root, a.ws.Project)
	}
	d := a.applyScan(scan)
	flash, urgent := a.refreshFiles(mergedPaths(msg.paths, d.refresh))
	if t := d.text(); t != "" {
		flash = t
	}
	urgent = urgent || (scan != nil && scan.warn != "")
	if flash != "" && !urgent && (a.flash == "" || a.flash != a.agentFlash) {
		a.setFlash("%s", flash)
	}
	if d.changed {
		return a.reloadBaseline()
	}
	return nil
}

func mergedPaths(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

// onFileChanged reloads clean buffers and asks about modified ones, for a report
// of paths from something that did not re-glob. An agent's `spec propose` edits
// and restores files as it files the proposal, and the watchers report the
// reload and the proposal in either order: the proposal's flash stays, so it is
// obvious that an outside process sent something.
func (a *App) onFileChanged(paths []string) tea.Cmd {
	return a.onWatched(fileChangedMsg{paths: paths})
}

// refreshFiles brings each changed file up to date. It returns the flash to show
// if nothing outranks it, and whether a message it set itself must stay.
func (a *App) refreshFiles(paths []string) (flash string, urgent bool) {
	for _, p := range paths {
		d, open := a.ws.docs[p]
		if !open && !a.ws.isKnown(p) {
			continue // spec.toml, or a file that left the project
		}
		switch st, _ := a.ws.Refresh(p); st {
		case DiskReloaded:
			a.outline.rebuild(a.ws)
			if a.popup != nil && !a.popup.notice {
				a.popup = nil // its offsets and target are from before the reload
			}
			if open {
				flash = fmt.Sprintf("Reloaded %s (changed on disk)", p)
			}
		case DiskConflict:
			urgent = true
			if a.prompt == nil && len(a.screens) == 1 {
				a.startConflict(p)
			} else {
				a.setStatus(statusWarning, "File changed", a.theme.uiText("File changed · unsaved buffer retained · %s"), p)
			}
		case DiskGone:
			switch {
			case open && d.Ed.Buffer().Dirty():
				urgent = true
				a.setFlash("%s was deleted on disk; ^O writes your buffer back", p)
			case onDisk(a.ws.Root, p):
				urgent = true
				a.setFlash("%s cannot be read any more", p)
			}
		}
	}
	return flash, urgent
}
