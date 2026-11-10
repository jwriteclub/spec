// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/fsnote"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/spell"
	"github.com/jwriteclub/spec/internal/tui/editor"
	"github.com/jwriteclub/spec/internal/workflow"
)

// Doc is an open file: its editor and the bytes last read from or written
// to disk.
type Doc struct {
	Ed *editor.Editor
	// Disk is the file's content on disk as this process last saw it.
	Disk []byte
}

// Workspace is the project as the UI sees it: every spec file parsed, every
// formal model scanned, open files taken from their buffers, resolved and
// linted together. A model is not a spec file: files and Spec never hold one,
// baseline, format and proposals do not see one, and it saves directly.
type Workspace struct {
	Root    string
	Project *project.Project

	edOpts editor.Options
	docs   map[string]*Doc
	files  map[string]*mdspec.File
	// models holds every model scanned, by path, with any open model that has
	// left the project's formal list since (see rebuild).
	models map[string]*formal.File
	// scans keeps each model's latest scan by content, so a model is parsed
	// once per change; good is each model's last scan without a syntax
	// error, which fills in what an error hides (formal.Recover).
	scans *project.ScanCache
	good  map[string]*formal.File
	// parsedAt is the buffer version each open file was parsed at.
	parsedAt map[string]uint64

	Spec *mdspec.Spec
	// Formal is the project's models joined to Spec; never nil once open.
	Formal *formal.Index
	Diags  []diag.Diagnostic
	// Spell are the spelling flags the last spell check left in the
	// project's cache (spec analyze, ^N), still true of the buffers; they
	// are among Diags as code spell.
	Spell []spell.Flag
	// spell keeps the spelling flags between reparses (spellDiags).
	spell *spell.Memo
	// checks is each model's full check by path (prudence, with the modules
	// it extends), from the scan cache's check cache.
	checks map[string]project.ModelCheck
	// Base is the HEAD baseline, nil until loaded (or when unavailable).
	Base *baseline.Baseline
}

// scanSlack is room in the scan cache for models added to the project, or
// opened outside it, after the workspace opened.
const scanSlack = 32

// OpenWorkspace opens the project at root and parses every spec file.
func OpenWorkspace(root string, edOpts editor.Options) (*Workspace, error) {
	p, err := project.Open(root)
	if err != nil {
		return nil, err
	}
	ws := &Workspace{
		Root: p.Root, Project: p, edOpts: edOpts,
		docs: map[string]*Doc{}, files: map[string]*mdspec.File{}, models: map[string]*formal.File{}, parsedAt: map[string]uint64{},
		spell: spell.NewMemo(),
		scans: project.NewScanCache(len(p.Formal) + scanSlack), good: map[string]*formal.File{},
	}
	loaded, err := p.LoadWith(nil, ws.scans)
	if err != nil {
		return nil, err
	}
	for _, f := range loaded.Files {
		ws.files[f.Path] = f
	}
	for _, f := range loaded.Formal.Files {
		ws.models[f.Path] = ws.recover(f)
	}
	ws.rebuild()
	return ws, nil
}

// Paths returns the project's spec files in project order.
func (ws *Workspace) Paths() []string {
	return ws.Project.Files
}

// ModelPaths returns the project's formal models, mutants last.
func (ws *Workspace) ModelPaths() []string {
	out := make([]string, 0, len(ws.Formal.Files))
	for _, f := range ws.Formal.Files {
		out = append(out, f.Path)
	}
	return out
}

// WatchPaths returns every file to watch for changes on disk: the project's
// spec files and models, and any other open document.
func (ws *Workspace) WatchPaths() []string {
	return append(ws.OpenPaths(), ws.unopenedModels()...)
}

// unopenedModels returns the project's models with no open document.
func (ws *Workspace) unopenedModels() []string {
	var out []string
	for _, p := range ws.ModelPaths() {
		if _, open := ws.docs[p]; !open {
			out = append(out, p)
		}
	}
	return out
}

// IsModel reports whether path is a formal model, not a spec file.
func (ws *Workspace) IsModel(path string) bool {
	_, ok := ws.models[path]
	return ok
}

// ModelCheck returns the latest full check of the model at path; its Ran is
// false when it was not checked.
func (ws *Workspace) ModelCheck(path string) project.ModelCheck {
	return ws.checks[path]
}

// Model returns the latest scan of the model at path, or nil.
func (ws *Workspace) Model(path string) *formal.File {
	return ws.models[path]
}

// OpenPaths includes every project file and each open document excluded by a
// later settings change. Save/exit iterate it so no open draft becomes orphaned.
func (ws *Workspace) OpenPaths() []string {
	paths := append([]string(nil), ws.Project.Files...)
	have := map[string]bool{}
	for _, path := range paths {
		have[path] = true
	}
	var extra []string
	for path := range ws.docs {
		if !have[path] {
			extra = append(extra, path)
		}
	}
	sort.Strings(extra)
	return append(paths, extra...)
}

// File returns the latest parse of path, or nil.
func (ws *Workspace) File(path string) *mdspec.File {
	return ws.files[path]
}

// Doc returns the open document for path, opening it if needed.
func (ws *Workspace) Doc(path string) (*Doc, error) {
	if d, ok := ws.docs[path]; ok {
		return d, nil
	}
	var parsed []byte
	switch f, m := ws.files[path], ws.models[path]; {
	case f != nil:
		parsed = f.Source()
	case m != nil:
		parsed = m.Source()
	default:
		return nil, fmt.Errorf("%s is not a spec file or a formal model of this project", path)
	}
	disk, err := os.ReadFile(filepath.Join(ws.Root, filepath.FromSlash(path)))
	if err != nil {
		return nil, err
	}
	d := &Doc{Ed: editor.New(path, disk, ws.edOpts), Disk: disk}
	ws.docs[path] = d
	if !bytes.Equal(disk, parsed) {
		// Changed since the project was loaded: parse what is on disk now.
		ws.setSource(path, disk)
		ws.rebuild()
	}
	ws.parsedAt[path] = d.Ed.Buffer().Version()
	ws.install(d)
	return d, nil
}

// Docs returns the open documents.
func (ws *Workspace) Docs() map[string]*Doc {
	return ws.docs
}

// SetEditorOptions changes the options of every open and future editor.
func (ws *Workspace) SetEditorOptions(o editor.Options) {
	ws.edOpts = o
	for _, d := range ws.docs {
		d.Ed.SetOptions(o)
	}
}

// Stale reports whether some open buffer changed since it was parsed.
func (ws *Workspace) Stale() bool {
	for p, d := range ws.docs {
		if ws.parsedAt[p] != d.Ed.Buffer().Version() {
			return true
		}
	}
	return false
}

// Reparse parses every open buffer that changed, rebuilds the spec, lints
// it and hands every open editor its analysis. It reports whether anything
// was reparsed.
func (ws *Workspace) Reparse() bool {
	changed := false
	for p, d := range ws.docs {
		v := d.Ed.Buffer().Version()
		if ws.parsedAt[p] == v {
			continue
		}
		ws.setSource(p, d.Ed.Buffer().Bytes())
		ws.parsedAt[p] = v
		changed = true
	}
	if !changed {
		return false
	}
	ws.rebuild()
	for _, d := range ws.docs {
		ws.install(d)
	}
	return true
}

// setSource parses src as the current content of the spec file or model at
// path.
func (ws *Workspace) setSource(path string, src []byte) {
	if ws.IsModel(path) {
		ws.models[path] = ws.scanModel(path, src)
		return
	}
	ws.files[path] = mdspec.ParseFile(path, src)
}

// scanModel scans the model at path through the scan cache, so unchanged
// text is not parsed again; see recover.
func (ws *Workspace) scanModel(path string, src []byte) *formal.File {
	f := ws.scans.Scan(path, src)
	if f == nil {
		return nil
	}
	return ws.recover(f)
}

// recover remembers f when it has no syntax error; otherwise it fills in the
// declarations and tags the error hid from the model's last good scan.
func (ws *Workspace) recover(f *formal.File) *formal.File {
	if f.ValidUTF8 && f.ReadError == "" && !f.HasError() {
		ws.good[f.Path] = f
		return f
	}
	return formal.Recover(f, ws.good[f.Path])
}

// rebuild resolves and lints the current files.
func (ws *Workspace) rebuild() {
	files := make([]*mdspec.File, 0, len(ws.files))
	for _, p := range ws.Project.Files {
		if f := ws.files[p]; f != nil {
			files = append(files, f)
		}
	}
	ws.Spec = mdspec.Build(files)
	models := make([]*formal.File, 0, len(ws.Project.Formal))
	for _, p := range ws.Project.Formal {
		if m := ws.models[p]; m != nil {
			models = append(models, m)
		}
	}
	ws.Formal = formal.Join(ws.Spec, models)
	ws.checks = ws.scans.Checks().CheckModels(context.Background(), ws.Root, models, ws.Project.Config.CheckLimits())
	extra := append(append([]diag.Diagnostic(nil), ws.Project.Diagnostics...), ws.Formal.Lint()...)
	extra = append(extra, ws.spellDiags()...)
	extra = append(extra, project.CheckDiagnostics(ws.checks)...)
	ws.Diags = lint.Run(ws.Spec, lint.Options{Overrides: ws.Project.Config.Lint, Extra: extra, Covered: ws.Formal.Covered, Cited: ws.Formal.Cited})
}

// spellDiags returns the spec's spelling flags: every known misspelling,
// and what the last spell check flagged and is still there. Only the spec
// files that changed are checked again. Nothing is read when no spell check
// has run.
func (ws *Workspace) spellDiags() []diag.Diagnostic {
	ws.Spell = nil
	cache := spell.OpenCache(ws.Root)
	if workflow.SpellOff(ws.Project) || !cache.Exists() {
		return nil
	}
	words, err := spell.ReadWords(ws.Root)
	if err != nil {
		lg.WithError(err).Warn("cannot read spec.words")
	}
	ws.Spell = ws.spell.Flags(ws.Spec, words, ws.Project.Config.Name, cache)
	return spell.Diagnostics(ws.Spell)
}

// Relint lints the current files again (the spell cache or spec.words
// changed) and hands every open editor the result.
func (ws *Workspace) Relint() {
	ws.rebuild()
	for _, d := range ws.docs {
		ws.install(d)
	}
}

// SpellFlag returns the spelling flag of diagnostic d.
func (ws *Workspace) SpellFlag(d diag.Diagnostic) (spell.Flag, bool) {
	for _, f := range ws.Spell {
		if f.File == d.File && f.Range == d.Range {
			return f, true
		}
	}
	return spell.Flag{}, false
}

// install gives d's editor the analysis of its file.
func (ws *Workspace) install(d *Doc) {
	p := d.Ed.Path()
	var ds []diag.Diagnostic
	for _, dg := range ws.Diags {
		if dg.File == p {
			ds = append(ds, dg)
		}
	}
	if m := ws.models[p]; m != nil {
		d.Ed.SetAnalysis(editor.Analysis{Path: p, Syntax: ws.modelSyntax(m), Diags: ds}, ws.parsedAt[p])
		return
	}
	d.Ed.SetAnalysis(editor.Analysis{File: ws.files[p], Spec: ws.Spec, Diags: ds}, ws.parsedAt[p])
}

// modelTokenFlags maps a model's token classes to highlight classes.
var modelTokenFlags = map[formal.TokenKind]editor.Flag{
	formal.TokenKeyword: editor.FModelKeyword, formal.TokenOperator: editor.FModelOperator,
	formal.TokenString: editor.FModelString, formal.TokenNumber: editor.FModelNumber,
	formal.TokenComment: editor.FModelComment, formal.TokenDefinition: editor.FModelDecl,
}

// modelSyntax returns the highlight of model m: its tokens, its declared names,
// and its tags drawn as links to the spec.
func (ws *Workspace) modelSyntax(m *formal.File) []editor.Span {
	var out []editor.Span
	add := func(r pos.Range, fl editor.Flag) {
		if r.End > r.Start {
			out = append(out, editor.Span{Start: r.Start, End: r.End, Flags: fl})
		}
	}
	for _, t := range m.Tokens {
		add(t.Range, modelTokenFlags[t.Kind])
	}
	tagged := map[*formal.Decl]bool{}
	src := m.Source()
	for _, t := range m.Tags {
		if t.Decl != nil {
			tagged[t.Decl] = true
		}
		key := string(t.Kind) + ":"
		if at := bytes.Index(src[t.Range.Start:t.Range.End], []byte(key)); at >= 0 {
			add(pos.Range{Start: t.Range.Start + at, End: t.Range.Start + at + len(key)}, editor.FModelTagKey)
		}
		switch {
		case t.Ref != nil:
			add(t.Ref.Range, ws.refFlag(t)|editor.FModelLink)
		case t.Stem != "":
			fl := editor.FBadRef
			if len(ws.Spec.FilesWithStem(t.Stem)) > 0 {
				fl = editor.FSection | editor.FModelLink
			}
			add(t.StemRange, fl)
		}
	}
	for _, d := range m.Decls {
		fl := editor.FModelDecl
		if tagged[d] {
			fl |= editor.FModelLink
		}
		add(d.NameRange, fl)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// refFlag is the class of a tag's reference: what the spec would give it.
func (ws *Workspace) refFlag(t *formal.Tag) editor.Flag {
	l := ws.Formal.LinkOf(t)
	switch {
	case l == nil:
		return 0
	case l.Anchor != nil && l.Anchor.Kind == mdspec.KindDefinition:
		return editor.FDef
	case l.Anchor != nil:
		return editor.FInv
	case l.Section != nil:
		return editor.FSection
	}
	return editor.FBadRef
}

// FileDiags returns the diagnostics of path.
func (ws *Workspace) FileDiags(path string) []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, d := range ws.Diags {
		if d.File == path {
			out = append(out, d)
		}
	}
	return out
}

// Source returns the current bytes of path: the buffer when open, else the
// parsed file.
func (ws *Workspace) Source(path string) []byte {
	if d, ok := ws.docs[path]; ok {
		return d.Ed.Buffer().Bytes()
	}
	if f := ws.files[path]; f != nil {
		return f.Source()
	}
	if m := ws.models[path]; m != nil {
		return m.Source()
	}
	return nil
}

// Line returns the 1-based line of offset off in the latest parse of path.
func (ws *Workspace) Line(path string, off int) int {
	if f := ws.files[path]; f != nil {
		return f.Index().Line(off)
	}
	if m := ws.models[path]; m != nil {
		return m.Index().Line(off)
	}
	return 0
}

// LineText returns the text of a 1-based line of the latest parse of path.
func (ws *Workspace) LineText(path string, line int) string {
	if f := ws.files[path]; f != nil {
		return f.Text(f.Index().LineRange(line))
	}
	if m := ws.models[path]; m != nil {
		return m.Text(m.Index().LineRange(line))
	}
	return ""
}

// ErrConflict says the file changed on disk since it was read, so a save
// would overwrite someone else's edit.
var ErrConflict = errors.New("the file changed on disk")

// Save writes d's buffer to disk atomically under the project lock. It
// returns ErrConflict when the file on disk no longer matches what was
// read, and a *project.LockHeldError when another process is writing.
func (ws *Workspace) Save(d *Doc) (int, error) {
	lock, err := project.AcquireLock(ws.Root, "spec ui")
	if err != nil {
		return 0, err
	}
	defer func() {
		if rerr := lock.Release(); rerr != nil {
			lg.WithError(rerr).Warn("releasing the project lock")
		}
	}()
	return ws.SaveLocked(d)
}

// SaveLocked is Save for a caller that holds the project lock.
func (ws *Workspace) SaveLocked(d *Doc) (int, error) {
	buf := append([]byte(nil), d.Ed.Buffer().Bytes()...)
	path := d.Ed.Path()
	abs := filepath.Join(ws.Root, filepath.FromSlash(path))
	if _, err := os.Lstat(abs); errors.Is(err, fs.ErrNotExist) {
		// Deleted on disk (by an agent, a branch switch): ^O writes the
		// buffer back rather than leaving no way to keep the edits.
		if err := writeNew(abs, buf); err != nil {
			return 0, err
		}
		return ws.saved(d, buf), nil
	}
	plan, err := edit.NewPlan(ws.Root, []edit.FileEdit{{
		Path: path, Snapshot: d.Disk,
		Splices: []edit.Splice{{Range: pos.Range{Start: 0, End: len(d.Disk)}, Old: string(d.Disk), New: string(buf)}},
	}})
	if err != nil {
		return 0, err
	}
	if err := plan.Commit(); err != nil {
		var ce *edit.ConcurrentEditError
		if errors.As(err, &ce) {
			return 0, ErrConflict
		}
		return 0, err
	}
	return ws.saved(d, buf), nil
}

// saved records that buf is now d's file on disk and returns its line
// count.
func (ws *Workspace) saved(d *Doc, buf []byte) int {
	d.Disk = buf
	d.Ed.Buffer().MarkSaved()
	lg.WithField("file", d.Ed.Path()).WithField("bytes", len(buf)).Info("saved")
	return d.Ed.Buffer().Index().LineCount() - boolInt(bytes.HasSuffix(buf, []byte("\n")))
}

// writeNew creates the file at abs (and its directory) holding data,
// atomically: a complete temp file linked into place. It returns
// ErrConflict when the file exists by then.
func writeNew(abs string, data []byte) error {
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".spec-ui-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o644)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// A hard link never replaces a file someone created meanwhile.
		err = os.Link(tmp, abs)
		if err == nil {
			fsnote.Wrote(abs, data)
		}
	}
	_ = os.Remove(tmp)
	if errors.Is(err, fs.ErrExist) {
		return ErrConflict
	}
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DiskChange is what Refresh found for one file.
type DiskChange int

// Disk change outcomes.
const (
	// DiskSame: the file on disk is what we last saw.
	DiskSame DiskChange = iota
	// DiskReloaded: the file changed and a clean buffer (or an unopened
	// file) took the new content.
	DiskReloaded
	// DiskConflict: the file changed while its buffer is modified.
	DiskConflict
	// DiskGone: the file cannot be read.
	DiskGone
)

// Refresh compares path on disk with what the workspace last saw and
// reloads it when that is safe. It returns the outcome and the disk bytes.
func (ws *Workspace) Refresh(path string) (DiskChange, []byte) {
	disk, err := os.ReadFile(filepath.Join(ws.Root, filepath.FromSlash(path)))
	if err != nil {
		return DiskGone, nil
	}
	d, open := ws.docs[path]
	if !open {
		if bytes.Equal(ws.Source(path), disk) && (ws.files[path] != nil || ws.IsModel(path)) {
			return DiskSame, disk
		}
		ws.setSource(path, disk)
		ws.rebuild()
		for _, od := range ws.docs {
			ws.install(od)
		}
		return DiskReloaded, disk
	}
	if bytes.Equal(d.Disk, disk) {
		return DiskSame, disk
	}
	if d.Ed.Buffer().Dirty() {
		return DiskConflict, disk
	}
	ws.TakeDisk(d, disk)
	return DiskReloaded, disk
}

// TakeDisk replaces d's buffer with disk (keeping the cursor near its
// offset) and reparses.
func (ws *Workspace) TakeDisk(d *Doc, disk []byte) {
	d.Disk = disk
	d.Ed.Reload(disk)
	ws.parsedAt[d.Ed.Path()] = 0
	ws.Reparse()
}

// KeepMine accepts disk as the new base of d, so the next save overwrites
// it; the buffer stays modified.
func (ws *Workspace) KeepMine(d *Doc, disk []byte) {
	d.Disk = disk
}

// LoadBaseline reads the HEAD baseline. It never fails: problems leave the
// baseline unavailable.
func (ws *Workspace) LoadBaseline(ctx context.Context) *baseline.Baseline {
	return loadBaseline(ctx, ws.Project)
}

// loadBaseline reads p's HEAD baseline; see LoadBaseline.
func loadBaseline(ctx context.Context, p *project.Project) *baseline.Baseline {
	b, err := baseline.Loader{}.Load(ctx, p, baseline.Source{})
	if err != nil {
		return &baseline.Baseline{State: baseline.StateUnavailable, Reason: err.Error()}
	}
	return b
}
