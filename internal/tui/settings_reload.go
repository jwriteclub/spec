// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"fmt"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/project"
)

// ReloadProject refreshes membership and analysis without saving any document.
// Open documents that leave the project remain explicit open drafts; they are
// excluded from spec analysis but retain normal save/exit protection.
func (ws *Workspace) ReloadProject() error {
	p, err := project.Open(ws.Root)
	if err != nil {
		return err
	}
	if msg := configProblem(p); msg != "" {
		return fmt.Errorf("project config: %s", msg)
	}
	overlay := map[string][]byte{}
	for path, d := range ws.docs {
		if d.Ed.Buffer().Dirty() {
			overlay[path] = d.Ed.Buffer().Bytes()
		}
	}
	loaded, err := p.LoadWith(overlay, ws.scans)
	if err != nil {
		return err
	}
	ws.adopt(p, loaded, true)
	return nil
}

// adopt makes p the project, with loaded as its files; open documents keep their
// buffers. An unsaved buffer whose file is gone goes on counting while a glob
// selects it. With reloadClean, a clean buffer whose file differs takes the new
// content; without, it is returned in differs for the caller to Refresh. moved
// says the content of an unopened file changed, or of a reloaded buffer.
func (ws *Workspace) adopt(p *project.Project, loaded *project.Loaded, reloadClean bool) (differs []string, moved bool) {
	next := map[string]*mdspec.File{}
	for _, file := range loaded.Files {
		next[file.Path] = file
		if old := ws.files[file.Path]; old != nil && ws.docs[file.Path] == nil && !bytes.Equal(old.Source(), file.Source()) {
			moved = true
		}
	}
	nextModels := map[string]*formal.File{}
	for _, file := range loaded.Formal.Files {
		if ws.docs[file.Path] == nil {
			file = ws.recover(file)
		}
		nextModels[file.Path] = file
		if old := ws.models[file.Path]; old != nil && ws.docs[file.Path] == nil && !bytes.Equal(old.Source(), file.Source()) {
			moved = true
		}
	}
	for path, d := range ws.docs {
		if d.Ed.Buffer().Dirty() {
			p.Retain(path)
		}
		var source []byte
		switch file, model := next[path], nextModels[path]; {
		case file != nil:
			source = file.Source()
		case model != nil:
			source = model.Source()
		}
		if source != nil && !d.Ed.Buffer().Dirty() && !bytes.Equal(d.Disk, source) && reloadClean {
			d.Ed.Reload(source)
			d.Disk = bytes.Clone(source)
			moved = true
		} else if source != nil && !reloadClean && !bytes.Equal(d.Disk, source) {
			differs = append(differs, path)
		}
		// Orphan editors still need their own syntax/cursor analysis, while rebuild
		// deliberately includes only the new Project.Files and Project.Formal membership.
		_, isModel := nextModels[path]
		if next[path] == nil && (isModel || ws.IsModel(path)) {
			nextModels[path] = ws.scanModel(path, d.Ed.Buffer().Bytes())
		} else {
			next[path] = mdspec.ParseFile(path, d.Ed.Buffer().Bytes())
		}
		ws.parsedAt[path] = d.Ed.Buffer().Version()
	}
	ws.Project = p
	ws.files = next
	ws.models = nextModels
	ws.rebuild()
	for _, d := range ws.docs {
		ws.install(d)
	}
	return differs, moved
}

// reloadProjectSettings updates the live project after an explicit config save.
// The caller schedules watcher.next and reloadBaseline after this returns.
func (a *App) reloadProjectSettings() error {
	if err := a.ws.ReloadProject(); err != nil {
		return err
	}
	a.outline.rebuild(a.ws)
	a.rebuildCards()
	a.layout()
	if a.watcher != nil {
		a.watcher.Close()
		a.startWatcher()
	}
	return nil
}
