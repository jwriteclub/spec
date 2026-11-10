// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// Item decisions a rethink is told about (spec ui's sidebar words, in
// .spec/checks.json).
const (
	RethinkAccepted  = "accepted"
	RethinkEdited    = "edited"
	RethinkDismissed = "dismissed"
	RethinkByHand    = "by hand"
)

// RethinkRequest says which check to redo with which notes.
type RethinkRequest struct {
	// Of is the check to redo: a pending check of an edit in the files.
	Of *proposal.Proposal
	// Decisions are the author's decisions on Of's items by item key ("h2"
	// for hunk 2, "p2" for the problem it carries, "n0" for note 0, "q0"
	// for question 0): RethinkAccepted, RethinkEdited, RethinkDismissed or
	// RethinkByHand. Hunks Of records as applied count as accepted.
	Decisions map[string]string
	// Note is the author's new note: an answer to one of Of's questions
	// (Question set) or free notes.
	Note proposal.RethinkNote
	// Author is the author of the new check.
	Author string
	// Full sends every spec file whole.
	Full bool
}

// Rethink redoes check req.Of with the author's notes and returns the new
// check (an analysis is redone by rethinkAnalysis), not yet filed (the caller files it and supersedes Of). It sends
// what the check sent (the spec's text, the anchor's neighbourhood and the
// anchor index), the edit as it is now (the anchor's committed and current
// text, and the reason), Of's findings, fixes and questions with the
// author's decisions, and the notes (Of's earlier notes first). The reply
// has a check's shape plus an optional primary block, a change to the
// author's own anchor: it becomes a hunk on that anchor that the new
// check's Rethink.PrimaryHunk names. Like every hunk it is proposed, never
// applied here. The text is read from the files, so the caller saves the
// buffers first: accepted fixes are then in the text the model sees.
func (w *Workflow) Rethink(ctx context.Context, proj *project.Project, req RethinkRequest) (*proposal.Proposal, *Error) {
	of := req.Of
	if of != nil && of.Kind == proposal.KindAnalysis {
		return w.rethinkAnalysis(ctx, proj, req)
	}
	if of == nil || of.Primary == nil || !of.Primary.InWorktree || of.Primary.Name == "" {
		return nil, fail(ExitFail, "bad_change", "only a check of an edit in the files can be rethought")
	}
	if of.Status != proposal.StatusPending {
		return nil, fail(ExitFail, "not_pending", "check %s is already %s; ^P checks the edit again", of.ID, of.Status)
	}
	if strings.TrimSpace(req.Note.Text) == "" {
		return nil, fail(ExitFail, "bad_change", "no notes to rethink the check with")
	}
	if e := w.CheckRecoveryBeforeLock(proj.Root, of.ID); e != nil {
		return nil, e
	}
	loaded, err := proj.Load(nil)
	if err != nil {
		return nil, internalErr(err)
	}
	work := loaded.Spec
	b, e := w.LoadBaseline(proj, baseline.Source{}, false)
	if e != nil {
		return nil, e
	}
	if b.State == baseline.StateUnavailable {
		return nil, NoBaseline(b)
	}
	name := strings.TrimPrefix(of.Primary.Name, "$")
	var entry *baseline.Entry
	d := baseline.Compare(b.Spec, work)
	for k := range d.Entries {
		if d.Entries[k].Name == name {
			entry = &d.Entries[k]
		}
	}
	switch {
	case entry == nil || !entry.NeedsChangeTrailer():
		return nil, fail(ExitFail, "not_found", "$%s no longer differs from HEAD, so there is no edit to rethink", name)
	case entry.Status == baseline.Renamed:
		return nil, fail(ExitFail, "bad_change", "$%s was renamed from $%s; commit the rename first", name, entry.OldName)
	}
	pe, e := pendingFor(b.Spec, work, *entry, true)
	if e != nil {
		return nil, e
	}
	pe.change.Why = of.Why
	ck, e := w.OpenChecker(ctx, req.Full)
	if e != nil {
		return nil, e
	}
	ck.ResetRecord()
	protected := map[string][]pos.Range{}
	var body pos.Range
	var bodyFile string
	if as := work.Lookup(name); len(as) == 1 {
		body, bodyFile = as[0].BodyRange, as[0].File
		protected[bodyFile] = []pos.Range{body}
	}
	notes := rethinkNotes(of, req.Note)
	system, e := ck.system(prompt.Rethink)
	if e != nil {
		return nil, e
	}
	c := specctx.Build(work, specctx.Target{Change: pe.change}, ck.options())
	in := rethinkInput(of, req.Decisions, notes)
	res, e := ck.runMessages(prompt.RethinkMessages(system, c, in), infer.PurposeCheck)
	if e != nil {
		return nil, e
	}
	sources := SourcesOf(work)
	hunks, fnotes := finding.Verify(res, sources, finding.Options{Protected: protected, FirstN: 2})
	primaryN := 0
	if pr := res.Primary; pr != nil {
		h, why := primaryHunk(pr, name, bodyFile, body, sources[bodyFile], 2+len(hunks))
		if why == "" {
			hunks = append(hunks, h)
			primaryN = h.N
		} else {
			fnotes = append(fnotes, finding.Note{
				File: bodyFile, Anchor: "$" + name, Issue: finding.IssueMissingUpdate, Severity: finding.SeverityInconsistent,
				Explanation: primaryExplanation(pr), Find: pr.Find, Replace: pr.Replace, Reason: why,
			})
		}
	}
	rec := ck.Record
	source := of.SourceKind
	if source == "" {
		source = "human"
	}
	p := &proposal.Proposal{
		Created: w.stamp(), Author: req.Author, SourceKind: source, Kind: proposal.KindChange,
		Why: of.Why, Summary: strings.TrimSpace(res.Summary), Primary: pe.primary, Hunks: hunks, Notes: fnotes,
		Questions: res.Questions, Status: proposal.StatusPending, Inference: &rec,
		Rethink: &proposal.Rethink{Of: of.ID, Notes: notes, PrimaryHunk: primaryN},
	}
	FillBase(p, work)
	return p, nil
}

// rethinkAnalysis redoes analysis req.Of with the author's notes and
// returns the new analysis, not yet filed (the caller files it and
// supersedes Of). It sends what an analysis of Of's scope sends now (the
// text that differs from HEAD, or the whole text when nothing does any
// more), Of's findings, fixes and questions with the author's decisions,
// and the notes, with prompts/rethink-analysis.md. There is no author's
// own anchor here: the notes may lead to a fix anywhere, and every fix is
// proposed, never applied. The new analysis keeps Of's scope.
func (w *Workflow) rethinkAnalysis(ctx context.Context, proj *project.Project, req RethinkRequest) (*proposal.Proposal, *Error) {
	of := req.Of
	if of.Status != proposal.StatusPending {
		return nil, fail(ExitFail, "not_pending", "analysis %s is already %s; ^N analyzes again", of.ID, of.Status)
	}
	if strings.TrimSpace(req.Note.Text) == "" {
		return nil, fail(ExitFail, "bad_change", "no notes to rethink the analysis with")
	}
	if e := w.CheckRecoveryBeforeLock(proj.Root, of.ID); e != nil {
		return nil, e
	}
	loaded, err := proj.Load(nil)
	if err != nil {
		return nil, internalErr(err)
	}
	work := loaded.Spec
	scope := proposal.Scope{Mode: ModeChanged}
	if of.Scope != nil {
		scope = *of.Scope
	}
	target, _, e := w.analysisTarget(proj, work, scope.Mode, scope.Files)
	if e != nil {
		return nil, e
	}
	if len(target.Regions) == 0 && len(target.Files) == 0 {
		target.Files = validFiles(work)
	}
	ck, e := w.OpenChecker(ctx, req.Full)
	if e != nil {
		return nil, e
	}
	ck.ResetRecord()
	notes := rethinkNotes(of, req.Note)
	system, e := ck.system(prompt.RethinkAnalysis)
	if e != nil {
		return nil, e
	}
	c := specctx.Build(work, target, ck.options())
	res, e := ck.runMessages(prompt.RethinkAnalysisMessages(system, c, rethinkInput(of, req.Decisions, notes)), infer.PurposeCheck)
	if e != nil {
		return nil, e
	}
	if e := ck.cancelled(); e != nil {
		return nil, e
	}
	hunks, fnotes := finding.Verify(res, SourcesOf(work), finding.Options{FirstN: 1})
	rec := ck.Record
	p := &proposal.Proposal{
		Created: w.stamp(), Author: ByOr(req.Author), SourceKind: of.SourceKind, Kind: proposal.KindAnalysis,
		Summary: strings.TrimSpace(res.Summary), Hunks: hunks, Notes: fnotes, Questions: res.Questions,
		Status: proposal.StatusPending, Inference: &rec, Scope: &scope,
		Rethink: &proposal.Rethink{Of: of.ID, Notes: notes},
	}
	FillBase(p, work)
	p.Normalize()
	return p, nil
}

// rethinkNotes are of's earlier notes followed by note.
func rethinkNotes(of *proposal.Proposal, note proposal.RethinkNote) []proposal.RethinkNote {
	var out []proposal.RethinkNote
	if of.Rethink != nil {
		out = append(out, of.Rethink.Notes...)
	}
	note.Question = strings.TrimSpace(note.Question)
	note.Text = strings.TrimSpace(note.Text)
	return append(out, note)
}

// rethinkInput tells the model about check of: its summary, its findings
// and fixes with the author's decisions, its questions, and the notes.
func rethinkInput(of *proposal.Proposal, decisions map[string]string, notes []proposal.RethinkNote) prompt.RethinkInput {
	in := prompt.RethinkInput{Summary: of.Summary}
	for _, h := range of.Hunks {
		dec := decisions[fmt.Sprintf("h%d", h.N)]
		if of.AppliedHunk(h.N) {
			dec = RethinkAccepted
		}
		if dec == "" && decisions[fmt.Sprintf("p%d", h.N)] == RethinkDismissed {
			dec = RethinkDismissed
		}
		item := prompt.RethinkItem{File: h.File, Anchor: h.Anchor, Issue: h.Issue, Severity: h.Severity,
			Explanation: h.Explanation, Find: h.Find, Replace: h.Replace, Decision: decisionWords(dec, true)}
		if of.Rethink != nil && h.N == of.Rethink.PrimaryHunk {
			item.Explanation = "(a change to the author's own edit) " + item.Explanation
		}
		in.Items = append(in.Items, item)
	}
	for k, n := range of.Notes {
		in.Items = append(in.Items, prompt.RethinkItem{File: n.File, Anchor: n.Anchor, Issue: n.Issue, Severity: n.Severity,
			Explanation: n.Explanation, Decision: decisionWords(decisions[fmt.Sprintf("n%d", k)], false)})
	}
	for k, q := range of.Questions {
		dec := "open"
		if decisions[fmt.Sprintf("q%d", k)] == RethinkDismissed {
			dec = "dismissed by the author"
		}
		for _, n := range notes {
			if n.Question == strings.TrimSpace(q) {
				dec = "answered in the author's notes below"
			}
		}
		in.Questions = append(in.Questions, prompt.RethinkQuestion{Text: q, Decision: dec})
	}
	for _, n := range notes {
		in.Notes = append(in.Notes, prompt.RethinkNote{Question: n.Question, Text: n.Text})
	}
	return in
}

// decisionWords says decision dec in words for the model; fix is true for
// a finding with a suggested fix.
func decisionWords(dec string, fix bool) string {
	switch dec {
	case RethinkAccepted:
		return "accepted; the files already have this fix"
	case RethinkEdited:
		return "edited by the author and accepted; the files have the author's version"
	case RethinkByHand:
		return "made by the author by hand; the files already have it"
	case RethinkDismissed:
		if fix {
			return "skipped"
		}
		return "dismissed"
	}
	return "undecided"
}

// primaryHunk verifies a rethink's change to the author's own anchor (name,
// whose body is body in file, whose text is src): its find must occur
// exactly once in the anchor's text. It returns the hunk numbered n, or why
// it is not one.
func primaryHunk(pr *finding.PrimaryEdit, name, file string, body pos.Range, src []byte, n int) (finding.Hunk, string) {
	if file == "" || !body.Valid(len(src)) {
		return finding.Hunk{}, fmt.Sprintf("$%s is not defined exactly once", name)
	}
	text := string(src[body.Start:body.End])
	switch c := strings.Count(text, pr.Find); {
	case pr.Find == pr.Replace:
		return finding.Hunk{}, "find and replace are identical"
	case c == 0:
		return finding.Hunk{}, fmt.Sprintf("the change to your edit quotes text that is not in $%s", name)
	case c > 1:
		return finding.Hunk{}, fmt.Sprintf("the quoted text occurs %d times in $%s, so the change is ambiguous", c, name)
	}
	start := body.Start + strings.Index(text, pr.Find)
	return finding.Hunk{
		N: n, File: file, Anchor: "$" + name, Issue: finding.IssueMissingUpdate, Severity: finding.SeverityInconsistent,
		Explanation: primaryExplanation(pr), Find: pr.Find, Replace: pr.Replace,
		Range: pos.Range{Start: start, End: start + len(pr.Find)},
	}, ""
}

// primaryExplanation is a primary change's explanation, or a default.
func primaryExplanation(pr *finding.PrimaryEdit) string {
	if x := strings.TrimSpace(pr.Explanation); x != "" {
		return x
	}
	return "Your notes change your edit."
}
