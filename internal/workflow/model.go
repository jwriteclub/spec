// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// Model proposals: an agent's edit of one formal model, filed by `spec change
// --tla` without touching the working tree, reviewed like any proposal and
// applied as a whole. A person editing a model in spec ui saves directly.

// ModelRequest is what `spec change --tla` files.
type ModelRequest struct {
	// Path is the model's project-relative slash path.
	Path string
	// Text is the model's whole proposed text.
	Text []byte
	Why  string
	// Author is --by (or the human author).
	Author     string
	SourceKind string
}

// ModelTarget checks that path names one of proj's formal models, and says
// which command fits when it does not.
func ModelTarget(proj *project.Project, path string) *Error {
	for _, f := range proj.Formal {
		if f == path {
			return nil
		}
	}
	for _, f := range proj.Files {
		if f == path {
			return fail(ExitFail, "wrong_command", "%s is a spec file, not a TLA+ model: --tla files an edit of a model. Spec edits are proposed by anchor, so that each one is checked against the rest of the spec: `spec change NAME --body-file F --why W`, or edit the file and run `spec propose NAME --why W`", path)
		}
	}
	if !project.IsModelFile(path) {
		return fail(ExitFail, "wrong_command", "%s is not a TLA+ model (.tla): --tla files an edit of one of the project's formal models", path)
	}
	if clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))); filepath.IsAbs(filepath.FromSlash(path)) || strings.HasPrefix(path, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return fail(ExitFail, "not_found", "%s is not a project-relative path: --tla names one of the project's formal models, relative to the project root", path)
	}
	if _, err := os.Stat(filepath.Join(proj.Root, filepath.FromSlash(path))); err != nil {
		return fail(ExitFail, "not_found", "%s does not exist; a proposal cannot create a model: create it directly and list it under formal in spec.toml", path)
	}
	return fail(ExitFail, "not_found", "%s is not one of the project's formal models: formal in spec.toml selects them (`spec explain config`)", path)
}

// ModelCommandHint refuses an argument naming a TLA+ model where a command
// wants an anchor (`spec propose`, `spec change NAME`); nil when arg is no
// model path.
func ModelCommandHint(command, arg string) *Error {
	if !project.IsModelFile(arg) {
		return nil
	}
	why := "it takes anchor names"
	if command == "propose" {
		why = "it restores HEAD text in the working tree, which would revert uncommitted edits of the model"
	}
	return fail(ExitFail, "wrong_command", "%s is a TLA+ model, not an anchor, and `spec %s` is for spec anchors (%s). Propose a model edit with `spec change --tla %s --body-file NEW.tla --why W`: it files the whole new text for review and leaves the working tree alone", arg, command, why, arg)
}

// ProposeModel files req as a model proposal without touching the working
// tree. With a checker, it refuses (exit 1 `check_failed`, the unfiled
// proposal in Error.Proposal) only for errors the new text adds over the
// model as it is, so a broken model can still be fixed by a proposal. A model
// with a pending proposal already is refused (exit 1 `model_pending`).
func (w *Workflow) ProposeModel(ctx context.Context, proj *project.Project, req ModelRequest) (*proposal.Proposal, *Error) {
	root := proj.Root
	if e := w.CheckRecoveryBeforeLock(root, ""); e != nil {
		return nil, e
	}
	if e := ModelTarget(proj, req.Path); e != nil {
		return nil, e
	}
	loaded, err := proj.Load(nil)
	if err != nil {
		return nil, internalErr(err)
	}
	f := loaded.Formal.File(req.Path)
	if f == nil {
		return nil, fail(ExitFail, "not_found", "%s is no longer on disk; a proposal cannot create a model", req.Path)
	}
	if f.ReadError != "" {
		return nil, fail(ExitFail, "write_failed", "cannot read %s: %s", req.Path, f.ReadError)
	}
	base := f.Source()
	text := modelText(base, req.Text)
	if bytes.Equal(text, base) {
		return nil, usageErr("the new text of %s is identical to its current text; nothing to propose", req.Path)
	}
	hunks := proposal.ModelHunks(req.Path, string(base), string(text))
	check, added := w.checkModelChange(ctx, loaded, req.Path, base, text)
	scanned := project.ScanModel(req.Path, text)
	m := &proposal.Model{
		File: req.Path, BaseHash: proposal.FileHash(base), Check: check,
		Decls: touchedDecls(req.Path, base, text),
		Links: newLinkFindings(loaded, req.Path, base, text),
	}
	if scanned != nil {
		m.Module = scanned.Module
	}
	p := &proposal.Proposal{
		Created: w.stamp(), Author: req.Author, SourceKind: req.SourceKind, Kind: proposal.KindModel,
		Why: strings.TrimSpace(req.Why), Summary: ModelSummary(m), Hunks: hunks, Model: m,
		Status: proposal.StatusPending,
	}
	p.Base = proposal.Base{Files: map[string]string{req.Path: m.BaseHash}, Anchors: map[string]string{}}
	p.Normalize()
	if len(added) > 0 {
		lines := make([]string, 0, len(added))
		for _, d := range added {
			lines = append(lines, DiagnosticLine(d))
		}
		e := fail(ExitFail, "check_failed", "%s found %d new error(s) in the proposed %s, so nothing was filed (errors the model already had do not count): %s. Fix them and send the proposal again", checkerName(check), len(added), req.Path, strings.Join(lines, "; "))
		e.Proposal = p
		return nil, e
	}
	lock, e := w.DecisionLock(ctx, root, "spec change --tla "+req.Path)
	if e != nil {
		return nil, e
	}
	defer func() { _ = lock.Release() }()
	if e := w.CheckRecovery(root, ""); e != nil {
		return nil, e
	}
	existing, err := w.Store(root).List()
	if err != nil {
		return nil, fail(ExitFail, "proposals_unreadable", "cannot read the proposal store, so a pending proposal for %s cannot be ruled out: %v", req.Path, err)
	}
	if q := PendingModel(existing, req.Path); q != nil {
		e := fail(ExitFail, "model_pending", "%s already has pending proposal %s, and model proposals do not stack. Withdraw it with `spec reject %s --why \"withdrawn: resending with ...\" --by YOUR-NAME`, then send one proposal with all the changes", req.Path, q.ID, q.ID)
		e.Proposal = q
		return nil, e
	}
	if err := w.Store(root).Create(p); err != nil {
		return nil, fail(ExitFail, "write_failed", "cannot write %s: %v", proposal.Dir, err)
	}
	return p, nil
}

// PendingModel returns the pending model proposal for path in existing, or nil.
func PendingModel(existing []*proposal.Proposal, path string) *proposal.Proposal {
	for _, p := range existing {
		if p.Status == proposal.StatusPending && p.IsModel() && p.Model.File == path {
			return p
		}
	}
	return nil
}

// ModelSummary says in a line what a model proposal changes.
func ModelSummary(m *proposal.Model) string {
	if len(m.Decls) == 0 {
		return "edits " + m.File
	}
	return "edits " + strings.Join(m.Decls, ", ") + " in " + m.File
}

// modelText is the proposed text with the base's line endings: a CRLF model
// stays CRLF when the agent wrote LF.
func modelText(base, text []byte) []byte {
	text = bytes.TrimPrefix(text, []byte(utf8BOM))
	if bytes.Contains(base, []byte("\r\n")) && !bytes.Contains(text, []byte("\r\n")) {
		text = bytes.ReplaceAll(text, []byte("\n"), []byte("\r\n"))
	}
	if bytes.HasPrefix(base, []byte(utf8BOM)) {
		text = append([]byte(utf8BOM), text...)
	}
	return text
}

// utf8BOM is the UTF-8 byte order mark, as bytes.
const utf8BOM = "\xef\xbb\xbf"

// modelChecker returns the checker for the model at path, or nil.
func (w *Workflow) modelChecker(path string) formal.Checker {
	if w.ModelChecker != nil {
		return w.ModelChecker
	}
	return formal.CheckerFor(path)
}

// resolver resolves the modules a model at path (with text src) extends or
// instantiates: project.ModuleResolver over the model's directory, with the
// loaded models' texts (unsaved buffers included) before the disk.
func resolver(loaded *project.Loaded, path string, src []byte) formal.Resolver {
	open := map[string][]byte{path: src}
	for _, f := range loaded.Formal.Files {
		if f.Path != path && f.ValidUTF8 && f.ReadError == "" {
			open[f.Path] = f.Source()
		}
	}
	return project.ModuleResolver(loaded.Project.Root, path, func(rel string) ([]byte, bool) {
		text, ok := open[rel]
		return text, ok
	})
}

// runModelCheck checks src, the text of the model at path; nil checker means
// not checked.
func (w *Workflow) runModelCheck(ctx context.Context, loaded *project.Loaded, path string, src, base []byte) (proposal.ModelCheck, formal.Report, bool) {
	ck := w.modelChecker(path)
	out := proposal.ModelCheck{State: proposal.ModelNotChecked, Hash: proposal.FileHash(base), Diagnostics: []formal.Diagnostic{}}
	if ck == nil {
		return out, formal.Report{}, false
	}
	start := w.now()
	rep, err := ck.Check(ctx, formal.CheckRequest{Path: path, Src: src, Resolve: resolver(loaded, path, src), Limits: loaded.Project.Config.CheckLimits()})
	rep = rep.DemoteIncomplete()
	out.LatencyMS = w.now().Sub(start).Milliseconds()
	out.Checker, out.Version, out.Level, out.Completion = rep.Checker, rep.Version, rep.Level, rep.Completion
	if err != nil {
		out.State, out.Message = proposal.ModelCheckFailed, err.Error()
		return out, rep, false
	}
	if rep.Diagnostics != nil {
		out.Diagnostics = rep.Diagnostics
	}
	out.State = proposal.ModelClean
	if rep.Errors() > 0 {
		out.State = proposal.ModelErrors
	}
	return out, rep, true
}

// checkModelChange checks the proposed text and, when it has errors, the base
// too, and returns the check and the errors the proposed text adds. A checker
// that cannot run files the proposal with state failed: the reviewer sees it.
func (w *Workflow) checkModelChange(ctx context.Context, loaded *project.Loaded, path string, base, text []byte) (proposal.ModelCheck, []formal.Diagnostic) {
	check, after, ok := w.runModelCheck(ctx, loaded, path, text, base)
	if !ok || after.Errors() == 0 {
		return check, nil
	}
	_, before, ok := w.runModelCheck(ctx, loaded, path, base, base)
	if !ok {
		// Without the base's errors every error counts as new.
		before = formal.Report{}
	}
	return check, NewModelErrors(after.Diagnostics, before.Diagnostics)
}

// messagePlace matches a place written into a checker's message, as SANY
// writes "at line 5, column 1".
var messagePlace = regexp.MustCompile(`\bline \d+(, column \d+)?`)

// NewModelErrors returns the errors of after that before does not have (as
// many times), compared by path and message: positions move with the edit,
// so a place the message names does not count either.
func NewModelErrors(after, before []formal.Diagnostic) []formal.Diagnostic {
	key := func(d formal.Diagnostic) string {
		return d.Path + "\x00" + messagePlace.ReplaceAllString(d.Message, "line #")
	}
	have := map[string]int{}
	for _, d := range before {
		if d.Severity == formal.SeverityError {
			have[key(d)]++
		}
	}
	var out []formal.Diagnostic
	for _, d := range after {
		if d.Severity != formal.SeverityError {
			continue
		}
		if have[key(d)] > 0 {
			have[key(d)]--
			continue
		}
		out = append(out, d)
	}
	return out
}

// DiagnosticLine is a checker diagnostic as path:line:col: severity: message.
func DiagnosticLine(d formal.Diagnostic) string {
	return fmt.Sprintf("%s:%d:%d: %s: %s", d.Path, d.Start.Line, d.Start.Column, d.Severity, d.Message)
}

// checkerName names the checker of c for a message.
func checkerName(c proposal.ModelCheck) string {
	if c.Checker != "" {
		return c.Checker
	}
	return "the model checker"
}

// RecheckModel checks model proposal p again, against the model as it is now
// (overlay over disk), when the model is no longer the text its check judged
// and a checker is registered. It updates p.Model.Check, and reports whether
// it checked. It writes nothing.
func (w *Workflow) RecheckModel(ctx context.Context, proj *project.Project, p *proposal.Proposal, overlay map[string][]byte) (bool, *Error) {
	if !p.IsModel() || w.modelChecker(p.Model.File) == nil {
		return false, nil
	}
	loaded, err := proj.Load(overlay)
	if err != nil {
		return false, internalErr(err)
	}
	cur, ok := modelSource(loaded, p.Model.File)
	if !ok {
		return false, nil
	}
	prep, err := p.PrepareModel(cur, allHunks(p))
	if err != nil || len(prep.Edits) == 0 {
		// Stale: approval refuses it with the reason.
		return false, nil
	}
	after, err := applyEdit(prep.Edits[0])
	if err != nil {
		return false, internalErr(err)
	}
	return w.recheckModel(ctx, loaded, p, cur, after), nil
}

// recheckModel rechecks after, the text approval would leave, when cur is not
// the text p's check judged. It updates p.Model.Check.
func (w *Workflow) recheckModel(ctx context.Context, loaded *project.Loaded, p *proposal.Proposal, cur, after []byte) bool {
	if w.modelChecker(p.Model.File) == nil || proposal.FileHash(cur) == p.Model.Check.Hash {
		return false
	}
	check, _, _ := w.runModelCheck(ctx, loaded, p.Model.File, after, cur)
	check.Rechecked = true
	p.Model.Check = check
	return true
}

// modelSource is the text of the model at path in loaded.
func modelSource(loaded *project.Loaded, path string) ([]byte, bool) {
	f := loaded.Formal.File(path)
	if f == nil || f.ReadError != "" {
		return nil, false
	}
	return f.Source(), true
}

// allHunks accepts every hunk of p.
func allHunks(p *proposal.Proposal) map[int]bool {
	out := map[int]bool{}
	for _, n := range p.Numbers() {
		out[n] = true
	}
	return out
}

// prepareProposal prepares the accepted hunks of p against loaded: a model
// proposal against its model, any other against the spec.
func prepareProposal(p *proposal.Proposal, loaded *project.Loaded, accept map[int]bool) (*proposal.Prepared, error) {
	if !p.IsModel() {
		return p.Prepare(loaded.Spec, accept)
	}
	src, ok := modelSource(loaded, p.Model.File)
	if !ok {
		return nil, &proposal.StaleError{Name: p.Model.File, Reason: "is no longer a formal model of this project"}
	}
	return p.PrepareModel(src, accept)
}

// wholeModel refuses to apply part of model proposal p: accept must hold every
// hunk not applied yet.
func wholeModel(p *proposal.Proposal, accept map[int]bool) *Error {
	if !p.IsModel() {
		return nil
	}
	for _, h := range p.Hunks {
		if !p.AppliedHunk(h.N) && !accept[h.N] {
			return usageErr("proposal %s edits the model %s, which applies as a whole, so hunk %d cannot be left out: approve it without --hunks, or reject it with `spec reject %s --why ...`", p.ID, p.Model.File, h.N, p.ID)
		}
	}
	return nil
}

// validateModel reports why pending model proposal p no longer applies, or nil.
func validateModel(p *proposal.Proposal, loaded *project.Loaded) error {
	for _, h := range p.Hunks {
		if p.AppliedHunk(h.N) {
			return nil
		}
	}
	_, err := prepareProposal(p, loaded, allHunks(p))
	return err
}

// ModelErrorsWarning says that approving model proposal p leaves the model with
// check errors ("" when it does not).
func ModelErrorsWarning(p *proposal.Proposal) string {
	if !p.IsModel() {
		return ""
	}
	n := p.Model.Check.Errors()
	if n == 0 {
		return ""
	}
	first := DiagnosticLine(p.Model.Check.Diagnostics[0])
	for _, d := range p.Model.Check.Diagnostics {
		if d.Severity == formal.SeverityError {
			first = DiagnosticLine(d)
			break
		}
	}
	return fmt.Sprintf("%s has %d check error(s) with this change (first: %s)", p.Model.File, n, first)
}

// touchedDecls names the top-level definitions an edit of base into text
// touches, from its changed lines (not the context around them): in base, the
// definition a replaced or deleted run starts in and any it covers; in text,
// any declared in the new lines. File order, each once.
func touchedDecls(path string, base, text []byte) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	bf := project.ScanModel(path, base)
	tf := project.ScanModel(path, text)
	if bf == nil || tf == nil {
		return out
	}
	delta := 0
	for _, e := range udiff.Lines(string(base), string(text)) {
		if e.End > e.Start {
			var within *formal.Decl
			for _, d := range bf.Decls {
				if d.NameRange.Start <= e.Start {
					within = d
				}
			}
			if within != nil && within.Kind == formal.DeclDefinition {
				add(within.Name)
			}
			for _, d := range bf.Decls {
				if d.NameRange.Start >= e.Start && d.NameRange.Start < e.End {
					add(d.Name)
				}
			}
		}
		ns := e.Start + delta
		for _, d := range tf.Decls {
			if d.NameRange.Start >= ns && d.NameRange.Start < ns+len(e.New) {
				add(d.Name)
			}
		}
		delta += len(e.New) - (e.End - e.Start)
	}
	return out
}

// newLinkFindings lints the tags of the proposed text (the tag scanner only)
// and returns the findings the base text does not have, compared by code and
// message.
func newLinkFindings(loaded *project.Loaded, path string, base, text []byte) []proposal.LinkFinding {
	lintOf := func(src []byte) ([]diag.Diagnostic, *formal.File) {
		f := project.ScanModel(path, src)
		if f == nil {
			return nil, nil
		}
		return formal.Join(loaded.Spec, []*formal.File{f}).Lint(), f
	}
	before, _ := lintOf(base)
	after, f := lintOf(text)
	have := map[string]int{}
	for _, d := range before {
		have[d.Code+"\x00"+d.Message]++
	}
	out := []proposal.LinkFinding{}
	for _, d := range after {
		k := d.Code + "\x00" + d.Message
		if have[k] > 0 {
			have[k]--
			continue
		}
		lf := proposal.LinkFinding{Code: d.Code, Severity: string(d.Severity), Message: d.Message}
		if f != nil && d.Range.Valid(len(f.Source())) {
			at := f.Index().Position(d.Range.Start)
			lf.Line, lf.Col = at.Line, at.Col
		}
		out = append(out, lf)
	}
	return out
}

// applyEdit returns the text fe leaves.
func applyEdit(fe edit.FileEdit) ([]byte, error) {
	return edit.Apply(fe.Snapshot, fe.Splices)
}

// commitModel commits what the approval of model proposal p wrote to its
// model, and nothing else: HEAD's text with the proposal's own line edits
// applied to it. Other edits of the model stay in the working tree,
// uncommitted. When an uncommitted edit changes lines the proposal also
// changes, its lines cannot be committed apart, so it refuses and leaves the
// applied edit uncommitted. A model edit carries no trailer: models are not
// hashed.
func (w *Workflow) commitModel(proj *project.Project, b *baseline.Baseline, p *proposal.Proposal, outputs []edit.Output, intent *RecoveryIntent) ([]string, string, *Error) {
	path := p.Model.File
	var out *edit.Output
	for i := range outputs {
		if outputs[i].Path == path {
			out = &outputs[i]
		}
	}
	nothing := fail(ExitFail, "commit_failed", "the files were written, but nothing the proposal touched differs from HEAD; nothing to commit")
	if out == nil {
		return []string{}, "", nothing
	}
	head, err := w.committedText(proj.Root, b.Commit, path)
	if err != nil {
		return []string{}, "", fail(ExitFail, "commit_failed", "the files were written, but cannot read %s at HEAD: %v", path, err)
	}
	text, ok := onHead(head, string(out.Before), out.Splices)
	if !ok {
		return []string{}, "", fail(ExitFail, "commit_failed", "proposal %s was applied but not committed: uncommitted edits of %s change lines the proposal also changes, so its lines cannot be committed without them; review %s and commit it yourself", p.ID, path, path)
	}
	if text == head {
		return []string{}, "", nothing
	}
	var msg strings.Builder
	msg.WriteString(CommitSubject(p) + "\n")
	body := p.Why
	if body == "" {
		body = p.Summary
	}
	if strings.TrimSpace(body) != "" {
		msg.WriteString("\n" + strings.TrimSpace(body) + "\n")
	}
	hash, warning, e := w.gitCommit(proj.Root, b.Commit, []string{path}, map[string]string{path: text}, nil, msg.String(), intent)
	if e != nil {
		if e.Code == "commit_failed" && !strings.HasPrefix(e.Message, "the files were written") {
			e.Message = "the files were written, but " + e.Message
		}
		return []string{}, "", e
	}
	if warning != "" {
		intent.warning = warning
		lg.Warn(warning)
	}
	return []string{}, hash, nil
}

// onHead applies to head the line edits that splices make to before, and
// reports false when it cannot do so exactly: when a line they change differs
// between head and before.
func onHead(head, before string, splices []edit.Splice) (string, bool) {
	human := udiff.Lines(head, before)
	var moved []edit.Splice
	for _, sp := range splices {
		for _, e := range udiff.Lines(sp.Old, sp.New) {
			at := [2]int{sp.Range.Start + e.Start, sp.Range.Start + e.End}
			delta := 0
			for _, h := range human {
				hb := [2]int{h.Start + delta, h.Start + delta + len(h.New)}
				if crosses(at, hb) {
					return "", false
				}
				if hb[1] > at[0] {
					break
				}
				delta += len(h.New) - (h.End - h.Start)
			}
			moved = append(moved, edit.Splice{
				Range: pos.Range{Start: at[0] - delta, End: at[1] - delta},
				Old:   before[at[0]:at[1]], New: e.New,
			})
		}
	}
	text, err := edit.Apply([]byte(head), moved)
	if err != nil {
		return "", false
	}
	return string(text), true
}

// crosses reports whether ranges a and b share a byte, or an empty one lies
// strictly inside the other.
func crosses(a, b [2]int) bool {
	if a[0] == a[1] {
		return b[0] < a[0] && a[0] < b[1]
	}
	if b[0] == b[1] {
		return a[0] < b[0] && b[0] < a[1]
	}
	return a[0] < b[1] && b[0] < a[1]
}

// committedText is path's text in commit ("" when commit is "" or does not
// hold path).
func (w *Workflow) committedText(root, commit, path string) (string, error) {
	if commit == "" {
		return "", nil
	}
	ctx := context.Background()
	rev := commit + ":./" + path
	if _, err := w.Git.Run(ctx, root, nil, "cat-file", "-e", rev); err != nil {
		return "", nil
	}
	out, err := w.Git.Run(ctx, root, nil, "cat-file", "blob", rev)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
