// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/spell"
)

// codeAnchorChanged is the code of the information diagnostic on an anchor
// that differs from HEAD (the language server's view of `spec status`).
const codeAnchorChanged = "anchor-changed"

// diagnosticSource is the source of every published diagnostic.
const diagnosticSource = "spec"

// document is an open text document.
type document struct {
	uri     uri.URI
	abs     string
	version int32
	text    []byte
	// proj is the project the document belongs to, nil outside any project.
	proj *projectState
	rel  string // project-relative slash path
}

// note is an information diagnostic computed outside lint.
type note struct {
	rng pos.Range
	msg string
}

// snapshot is one analysis of a project: its buffers and disk files parsed,
// resolved and linted.
type snapshot struct {
	proj     *project.Project
	spec     *mdspec.Spec
	formal   *formal.Index                // the project's formal models, joined to spec
	diags    map[string][]diag.Diagnostic // by file; project-level ones under spec.toml
	notes    map[string][]note            // anchors changed since HEAD, by file
	cfgIndex *pos.Index
	// spell are the spelling flags among diags (what the spell cache
	// holds), for their quick fixes.
	spell []spell.Flag
	// versions are the versions of the open buffers the analysis read, by
	// file: diagnostics published later (when the baseline arrives while
	// newer edits wait for their debounce) still name the text they are for.
	versions map[string]int32
}

// index returns the position index of a project file ("spec.toml"
// included), or an empty index for a file the snapshot did not parse.
func (sn *snapshot) index(rel string) *pos.Index {
	if rel == project.ConfigFile {
		return sn.cfgIndex
	}
	if f := sn.spec.File(rel); f != nil {
		return f.Index()
	}
	if f := sn.formal.File(rel); f != nil {
		return f.Index()
	}
	return pos.NewIndex(nil)
}

// published is the last diagnostics notification sent for one file.
type published struct {
	uri uri.URI
	enc string
}

// projectState is one project with at least one open document.
type projectState struct {
	root  string
	docs  map[string]*document // open documents by project-relative path
	snap  *snapshot
	dirty bool
	gen   int
	timer *time.Timer
	sent  map[string]published // by project-relative path
	// scans keeps the project's scanned models between analyses.
	scans *project.ScanCache

	base        *baseline.Baseline
	baseAt      time.Time
	baseLoading bool
	baseAgain   bool

	// spell keeps the spelling flags between analyses (spellFlags).
	spell *spell.Memo
}

func (p *projectState) stopTimer() {
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
}

// scanCacheSize bounds the models a project's scan cache holds; a large
// project has a few dozen.
const scanCacheSize = 512

type fireEvent struct {
	root string
	gen  int
}

type baseResult struct {
	root string
	b    *baseline.Baseline
	err  error
}

// ---- document synchronization ----

func (s *Server) didOpen(p *protocol.DidOpenTextDocumentParams) {
	abs, err := pathOf(p.TextDocument.URI)
	if err != nil {
		return
	}
	d := &document{uri: p.TextDocument.URI, abs: abs, version: p.TextDocument.Version, text: []byte(p.TextDocument.Text)}
	if old := s.docs[abs]; old != nil {
		s.detach(old)
	}
	s.docs[abs] = d
	s.attach(d)
}

// attach adds d to the project found by walking up from its directory and
// schedules an analysis. A document outside any project stays detached (no
// features, no noise); attach is retried on its edits and saves, so a
// spec.toml created or restored later picks it up.
func (s *Server) attach(d *document) {
	root, ok := project.FindRoot(filepath.Dir(d.abs))
	if !ok {
		return
	}
	rel, err := filepath.Rel(root, d.abs)
	if err != nil {
		return
	}
	ps := s.projects[root]
	if ps == nil {
		ps = &projectState{root: root, docs: map[string]*document{}, sent: map[string]published{}, spell: spell.NewMemo(), scans: project.NewScanCache(scanCacheSize)}
		s.projects[root] = ps
	}
	d.proj = ps
	d.rel = filepath.ToSlash(rel)
	ps.docs[d.rel] = d
	s.markDirty(ps)
}

func (s *Server) didChange(p *protocol.DidChangeTextDocumentParams) {
	abs, err := pathOf(p.TextDocument.URI)
	if err != nil {
		return
	}
	d := s.docs[abs]
	if d == nil {
		return
	}
	for _, c := range p.ContentChanges {
		switch c := c.(type) {
		case *protocol.TextDocumentContentChangeWholeDocument:
			d.text = []byte(c.Text)
		case *protocol.TextDocumentContentChangePartial:
			x := pos.NewIndex(d.text)
			start := s.conv.offset(x, c.Range.Start)
			end := s.conv.offset(x, c.Range.End)
			if end < start {
				start, end = end, start
			}
			next := make([]byte, 0, len(d.text)-(end-start)+len(c.Text))
			next = append(next, d.text[:start]...)
			next = append(next, c.Text...)
			next = append(next, d.text[end:]...)
			d.text = next
		}
	}
	d.version = p.TextDocument.Version
	if d.proj != nil {
		s.markDirty(d.proj)
	} else {
		s.attach(d)
	}
}

func (s *Server) didClose(p *protocol.DidCloseTextDocumentParams) {
	abs, err := pathOf(p.TextDocument.URI)
	if err != nil {
		return
	}
	if d := s.docs[abs]; d != nil {
		delete(s.docs, abs)
		s.detach(d)
	}
}

// detach removes d from its project's overlay, so the disk content counts
// again.
func (s *Server) detach(d *document) {
	if d.proj == nil {
		return
	}
	if d.proj.docs[d.rel] == d {
		delete(d.proj.docs, d.rel)
	}
	s.markDirty(d.proj)
}

func (s *Server) didSave(p *protocol.DidSaveTextDocumentParams) {
	abs, err := pathOf(p.TextDocument.URI)
	if err != nil {
		return
	}
	d := s.docs[abs]
	switch {
	case d == nil:
	case d.proj != nil:
		d.proj.baseAt = time.Time{} // a save often precedes a commit
		s.markDirty(d.proj)
	default:
		s.attach(d)
	}
}

func (s *Server) didChangeWatchedFiles(p *protocol.DidChangeWatchedFilesParams) {
	config := false
	for _, c := range p.Changes {
		abs, err := pathOf(c.URI)
		if err != nil {
			continue
		}
		config = config || filepath.Base(abs) == project.ConfigFile
		for root, ps := range s.projects {
			if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
				ps.baseAt = time.Time{}
				s.markDirty(ps)
			}
		}
	}
	for _, d := range s.docs {
		switch {
		case d.proj == nil:
			s.attach(d)
		case config:
			s.reattach(d)
		}
	}
}

// reattach moves d to the project that owns it now, when a spec.toml appearing
// or going has changed that. The old project goes first, so that what it clears
// does not land after what the new one publishes.
func (s *Server) reattach(d *document) {
	root, ok := project.FindRoot(filepath.Dir(d.abs))
	if ok && root == d.proj.root {
		return
	}
	old := d.proj
	s.detach(d)
	d.proj = nil
	s.flush(old)
	s.attach(d)
}

// ---- analysis ----

// markDirty schedules a re-analysis of p after the debounce delay.
func (s *Server) markDirty(p *projectState) {
	p.dirty = true
	p.gen++
	p.stopTimer()
	ev := fireEvent{root: p.root, gen: p.gen}
	p.timer = time.AfterFunc(s.debounce, func() {
		select {
		case s.fire <- ev:
		case <-s.done:
		}
	})
}

func (s *Server) onFire(ev fireEvent) {
	p := s.projects[ev.root]
	if p == nil || p.gen != ev.gen || !p.dirty {
		return
	}
	s.flush(p)
}

// fresh re-analyzes p now when it has pending edits, so a request never
// answers from stale buffers.
func (s *Server) fresh(p *projectState) {
	if p.dirty {
		s.flush(p)
	}
}

// flush analyzes p and publishes the diagnostics that changed.
func (s *Server) flush(p *projectState) {
	start := time.Now()
	p.dirty = false
	p.stopTimer()
	s.analyze(p)
	if s.onAnalyze != nil {
		s.onAnalyze(time.Since(start))
	}
}

func (s *Server) analyze(p *projectState) {
	overlay := make(map[string][]byte, len(p.docs))
	versions := make(map[string]int32, len(p.docs))
	for rel, d := range p.docs {
		overlay[rel] = d.text
		versions[rel] = d.version
	}
	loaded, err := project.LoadWith(p.root, overlay, p.scans)
	if err != nil {
		var nf *project.NotFoundError
		if errors.As(err, &nf) {
			s.dropProject(p)
			return
		}
		lg.WithError(err).WithField("root", p.root).Warn("analysis failed; keeping the previous result")
		return
	}
	opts := loaded.LintOptions()
	flags := spellFlags(loaded, p.spell)
	opts.Extra = append(opts.Extra, spell.Diagnostics(flags)...)
	ds := lint.Run(loaded.Spec, opts)
	sn := &snapshot{
		spell:    flags,
		proj:     loaded.Project,
		spec:     loaded.Spec,
		formal:   loaded.Formal,
		diags:    map[string][]diag.Diagnostic{},
		cfgIndex: pos.NewIndex(loaded.Project.ConfigSource()),
		versions: versions,
	}
	for _, d := range ds {
		file := d.File
		if file == "" {
			file = project.ConfigFile
		}
		sn.diags[file] = append(sn.diags[file], d)
	}
	sn.notes = changeNotes(p.base, sn.spec)
	p.snap = sn
	s.publish(p)
	if !p.baseLoading && time.Since(p.baseAt) > baselineTTL {
		s.loadBaseline(p)
	}
}

// dropProject forgets a project whose spec.toml disappeared and clears its
// diagnostics.
func (s *Server) dropProject(p *projectState) {
	p.stopTimer()
	for _, pub := range p.sent {
		s.sendNotification("textDocument/publishDiagnostics", &protocol.PublishDiagnosticsParams{URI: pub.uri, Diagnostics: []protocol.Diagnostic{}})
	}
	delete(s.projects, p.root)
	for _, d := range p.docs {
		d.proj = nil
		s.attach(d) // a parent project may take it in
	}
}

// ---- git baseline ----

// loadBaseline reads HEAD for p, in the background unless syncBaseline is
// set. A load requested while one runs is repeated when it finishes.
func (s *Server) loadBaseline(p *projectState) {
	if p.snap == nil {
		return
	}
	if p.baseLoading {
		p.baseAgain = true
		return
	}
	p.baseLoading = true
	proj, root := p.snap.proj, p.root
	if s.syncBaseline {
		b, err := s.opts.Git.Load(s.ctx, proj, baseline.Source{})
		s.applyBaseline(baseResult{root: root, b: b, err: err})
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		b, err := s.opts.Git.Load(s.ctx, proj, baseline.Source{})
		select {
		case s.baseCh <- baseResult{root: root, b: b, err: err}:
		case <-s.done:
		}
	}()
}

func (s *Server) applyBaseline(r baseResult) {
	p := s.projects[r.root]
	if p == nil {
		return
	}
	p.baseLoading = false
	p.baseAt = time.Now()
	if r.err != nil {
		lg.WithError(r.err).WithField("root", r.root).Debug("no git baseline")
		p.base = nil
	} else {
		p.base = r.b
	}
	if p.baseAgain {
		p.baseAgain = false
		s.loadBaseline(p)
	}
	if p.snap != nil {
		p.snap.notes = changeNotes(p.base, p.snap.spec)
		s.publish(p)
	}
}

// changeNotes returns the "changed since HEAD" information per file: one
// note on the label of every anchor that needs a commit trailer.
func changeNotes(b *baseline.Baseline, spec *mdspec.Spec) map[string][]note {
	if b == nil || b.Spec == nil {
		return nil
	}
	d := baseline.Compare(b.Spec, spec)
	out := map[string][]note{}
	for _, e := range d.Entries {
		if e.New == nil {
			continue
		}
		msg := changeMessage(e, b.Source.String())
		if msg == "" {
			continue
		}
		out[e.New.File] = append(out[e.New.File], note{rng: e.New.LabelRange, msg: msg})
	}
	return out
}

// kindWord names an anchor kind for messages.
func kindWord(k mdspec.Kind) string {
	if k == mdspec.KindDefinition {
		return "definition"
	}
	return "invariant"
}

// changeMessage words one diff entry, or returns "" when it needs no
// trailer.
func changeMessage(e baseline.Entry, since string) string {
	kind := kindWord(e.NewKind)
	switch e.Status {
	case baseline.Added:
		return fmt.Sprintf("%s added since %s: needs review (Spec-Change trailer)", kind, since)
	case baseline.Changed:
		if e.KindChanged {
			return fmt.Sprintf("%s changed since %s (kind was %s): needs review (Spec-Change trailer)", kind, since, kindWord(e.OldKind))
		}
		return fmt.Sprintf("%s changed since %s: needs review (Spec-Change trailer)", kind, since)
	case baseline.KindChanged:
		return fmt.Sprintf("%s since %s (kind changed, was %s): needs review (Spec-Change trailer)", kind, since, kindWord(e.OldKind))
	case baseline.Renamed:
		if e.Changed || e.KindChanged {
			return fmt.Sprintf("%s renamed from $%s and changed since %s: needs review (Spec-Rename and Spec-Change trailers)", kind, e.OldName, since)
		}
		return fmt.Sprintf("%s renamed from $%s since %s (Spec-Rename trailer)", kind, e.OldName, since)
	}
	return ""
}

// ---- diagnostics ----

// uriOf returns the URI for a project file: the client's own URI when the
// file is open.
func (s *Server) uriOf(p *projectState, rel string) (uri.URI, error) {
	if d := p.docs[rel]; d != nil {
		return d.uri, nil
	}
	return fileURI(filepath.Join(p.root, filepath.FromSlash(rel)))
}

// lspDiagnostics converts the snapshot's diagnostics for one file.
func (s *Server) lspDiagnostics(p *projectState, rel string) []protocol.Diagnostic {
	sn := p.snap
	x := sn.index(rel)
	out := []protocol.Diagnostic{}
	for _, d := range sn.diags[rel] {
		out = append(out, s.lspDiagnostic(p, x, d))
	}
	for _, n := range sn.notes[rel] {
		out = append(out, protocol.Diagnostic{
			Range:    s.conv.rng(x, n.rng),
			Severity: protocol.DiagnosticSeverityInformation,
			Code:     protocol.String(codeAnchorChanged),
			Source:   protocol.NewOptional(diagnosticSource),
			Message:  protocol.String(n.msg),
		})
	}
	return out
}

// lspDiagnostic converts one diagnostic of the file indexed by x. A
// spelling flag is information from source spell, as a spell checker's.
func (s *Server) lspDiagnostic(p *projectState, x *pos.Index, d diag.Diagnostic) protocol.Diagnostic {
	out := protocol.Diagnostic{
		Range:    s.conv.rng(x, d.Range),
		Severity: severity(d.Severity),
		Code:     protocol.String(d.Code),
		Source:   protocol.NewOptional(diagnosticSource),
		Message:  protocol.String(d.Message),
	}
	if d.Code == diag.CodeSpell {
		if d.Severity == diag.Hint {
			// The default: information, as a spell checker's.
			out.Severity = protocol.DiagnosticSeverityInformation
		}
		out.Source = protocol.NewOptional(spellSource)
	}
	for _, r := range d.Related {
		file := r.File
		if file == "" {
			file = project.ConfigFile
		}
		u, err := s.uriOf(p, file)
		if err != nil {
			continue
		}
		out.RelatedInformation = append(out.RelatedInformation, protocol.DiagnosticRelatedInformation{
			Location: protocol.Location{URI: u, Range: s.conv.rng(p.snap.index(file), r.Range)},
			Message:  r.Message,
		})
	}
	return out
}

// publish sends publishDiagnostics for every project file whose diagnostic
// set changed since the last notification, including files that no longer
// have any.
func (s *Server) publish(p *projectState) {
	sn := p.snap
	files := map[string]bool{}
	for _, f := range sn.spec.Files {
		files[f.Path] = true
	}
	for rel := range sn.diags {
		files[rel] = true
	}
	for rel := range sn.notes {
		files[rel] = true
	}
	for rel := range p.sent {
		files[rel] = true
	}
	order := make([]string, 0, len(files))
	for rel := range files {
		order = append(order, rel)
	}
	sort.Strings(order)
	for _, rel := range order {
		u, err := s.uriOf(p, rel)
		if err != nil {
			continue
		}
		ds := s.lspDiagnostics(p, rel)
		raw, err := protocol.Marshal(ds)
		if err != nil {
			lg.WithError(err).Warn("encoding diagnostics failed")
			continue
		}
		enc := string(raw)
		prev, had := p.sent[rel]
		if had && prev.uri != u {
			// The client reopened the file under another spelling of its URI.
			s.sendNotification("textDocument/publishDiagnostics", &protocol.PublishDiagnosticsParams{URI: prev.uri, Diagnostics: []protocol.Diagnostic{}})
			had = false
		}
		if (!had && len(ds) == 0) || (had && prev.enc == enc) {
			if !had {
				delete(p.sent, rel)
			}
			continue
		}
		params := &protocol.PublishDiagnosticsParams{URI: u, Diagnostics: ds}
		if v, ok := sn.versions[rel]; ok {
			params.Version = protocol.NewOptional(v)
		}
		s.sendNotification("textDocument/publishDiagnostics", params)
		if len(ds) == 0 {
			delete(p.sent, rel)
		} else {
			p.sent[rel] = published{uri: u, enc: enc}
		}
	}
}

// ---- lookups for requests ----

// view is an open document in its project's current analysis.
type view struct {
	p    *projectState
	snap *snapshot
	doc  *document
	file *mdspec.File
}

// viewOf returns the analysis of an open document, re-analyzing first when
// edits are pending. ok is false outside any project and for files the
// project does not select.
func (s *Server) viewOf(u uri.URI) (view, bool) {
	abs, err := pathOf(u)
	if err != nil {
		return view{}, false
	}
	d := s.docs[abs]
	if d == nil || d.proj == nil {
		return view{}, false
	}
	s.fresh(d.proj)
	p := d.proj
	if p == nil || p.snap == nil {
		return view{}, false
	}
	f := p.snap.spec.File(d.rel)
	if f == nil || !f.ValidUTF8 {
		return view{}, false
	}
	return view{p: p, snap: p.snap, doc: d, file: f}, true
}

// offset converts a position in the view's document.
func (s *Server) offset(v view, at protocol.Position) int {
	return s.conv.offset(v.file.Index(), at)
}

// location returns the LSP location of a byte range in a project file.
func (s *Server) location(p *projectState, rel string, r pos.Range) (protocol.Location, bool) {
	u, err := s.uriOf(p, rel)
	if err != nil {
		return protocol.Location{}, false
	}
	return protocol.Location{URI: u, Range: s.conv.rng(p.snap.index(rel), r)}, true
}

// workspaceEdit builds an edit from splices per project-relative file, as
// versioned documentChanges when the client supports them.
func (s *Server) workspaceEdit(p *projectState, outputs []edit.Output) *protocol.WorkspaceEdit {
	we := &protocol.WorkspaceEdit{}
	for _, o := range outputs {
		u, err := s.uriOf(p, o.Path)
		if err != nil {
			continue
		}
		x := p.snap.index(o.Path)
		ss := append([]edit.Splice(nil), o.Splices...)
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].Range.Start < ss[j].Range.Start })
		var edits []protocol.TextEdit
		for _, sp := range ss {
			edits = append(edits, protocol.TextEdit{Range: s.conv.rng(x, sp.Range), NewText: sp.New})
		}
		if s.docChanges {
			tde := &protocol.TextDocumentEdit{TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{
				TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: u},
			}}
			if d := p.docs[o.Path]; d != nil {
				v := d.version
				tde.TextDocument.Version = &v
			}
			for i := range edits {
				tde.Edits = append(tde.Edits, &edits[i])
			}
			we.DocumentChanges = append(we.DocumentChanges, tde)
			continue
		}
		if we.Changes == nil {
			we.Changes = map[uri.URI][]protocol.TextEdit{}
		}
		we.Changes[u] = edits
	}
	return we
}
