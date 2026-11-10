// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"slices"
	"strings"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/spell"
)

// Analysis modes.
const (
	ModeChanged = "changed"
	ModeAll     = "all"
	ModeFiles   = "files"
)

// AnalyzeRequest says what `spec analyze` checks.
type AnalyzeRequest struct {
	// Mode is ModeChanged (the regions that differ from HEAD), ModeAll or
	// ModeFiles.
	Mode string
	// Files are the project-relative spec files of ModeFiles.
	Files []string
	Full  bool
	// Author is recorded on the proposal.
	Author string
}

// Analysis is what Analyze found.
type Analysis struct {
	Mode string
	// Files are the files analyzed (their edited regions for changed).
	Files []string
	// Regions counts the edited regions sent (changed only).
	Regions int
	// Nothing is true when nothing differs from HEAD (changed only): no
	// model call was made.
	Nothing bool
	// Proposal holds the findings: its hunks are the fixes, its notes the
	// findings without one. It is filed (Saved) when it has a hunk or a
	// question.
	Proposal *proposal.Proposal
	Saved    bool
	// Omitted are the sections left out to fit max_context_tokens.
	Omitted []string
	Record  proposal.Inference
	// Spell is the spell check of the same text (nil when nothing was
	// analyzed).
	Spell *spell.Report
}

// Analyze runs the advisory consistency analysis. Fix hunks and questions
// are filed as an analysis proposal.
func (w *Workflow) Analyze(ctx context.Context, proj *project.Project, req AnalyzeRequest) (*Analysis, *Error) {
	loaded, err := proj.Load(nil)
	if err != nil {
		return nil, internalErr(err)
	}
	spec := loaded.Spec
	target, covered, e := w.analysisTarget(proj, spec, req.Mode, req.Files)
	if e != nil {
		return nil, e
	}
	out := &Analysis{Mode: covered.Mode, Files: []string{}}
	if covered.Mode == ModeChanged && len(target.Regions) == 0 {
		out.Nothing = true
		return out, nil
	}
	seen := map[string]bool{}
	for _, r := range target.Regions {
		if !seen[r.File] {
			seen[r.File] = true
			out.Files = append(out.Files, r.File)
		}
	}
	for _, f := range target.Files {
		if !seen[f] {
			seen[f] = true
			out.Files = append(out.Files, f)
		}
	}
	out.Regions = len(target.Regions)

	ck, e := w.OpenChecker(ctx, req.Full)
	if e != nil {
		return nil, e
	}
	// The spell check runs beside the check (its models answer in
	// seconds, the check in minutes).
	scope := spell.Scope{}
	for _, r := range target.Regions {
		scope[r.File] = append(scope[r.File], r.Range)
	}
	for _, f := range target.Files {
		scope[f] = nil
	}
	spellCtx, cancelSpell := context.WithCancel(ctx)
	defer cancelSpell()
	spelled := make(chan *spell.Report, 1)
	go func() { spelled <- w.Spell(spellCtx, proj, spec, scope) }()
	c := specctx.Build(spec, target, ck.options())
	res, e := ck.Run(prompt.Analyze, c)
	if e != nil {
		// A failed check discards the analysis: stop the spell check
		// rather than wait for its models.
		cancelSpell()
		return nil, e
	}
	out.Spell = <-spelled
	hunks, notes := finding.Verify(res, SourcesOf(spec), finding.Options{FirstN: 1})
	out.Record = ck.Record
	out.Omitted = c.Omitted
	rec := ck.Record
	p := &proposal.Proposal{
		Created: w.stamp(), Author: ByOr(req.Author), Kind: proposal.KindAnalysis,
		Summary: strings.TrimSpace(res.Summary), Hunks: hunks, Notes: notes, Questions: res.Questions,
		Status: proposal.StatusPending, Inference: &rec, Scope: &covered,
	}
	FillBase(p, spec)
	p.Normalize()
	out.Proposal = p
	if e := ck.cancelled(); e != nil {
		return nil, e
	}
	// A question is filed too: it can be answered (spec proposal ID
	// --answer, f in spec ui).
	if len(hunks) > 0 || len(p.Questions) > 0 {
		st := w.Store(proj.Root)
		if err := st.Prune(); err != nil {
			lg.WithError(err).Warn("cannot prune receipts")
		}
		if err := st.Create(p); err != nil {
			return nil, fail(ExitFail, "write_failed", "cannot write %s: %v", proposal.Dir, err)
		}
		out.Saved = true
	}
	return out, nil
}

// analysisTarget is what an analysis of mode sends (files are the files
// of ModeFiles) and the scope it covers. For ModeChanged (any other mode)
// it holds the regions that differ from HEAD, none when nothing does.
func (w *Workflow) analysisTarget(proj *project.Project, spec *mdspec.Spec, mode string, files []string) (specctx.Target, proposal.Scope, *Error) {
	var target specctx.Target
	switch mode {
	case ModeAll:
		target.Files = validFiles(spec)
		return target, proposal.Scope{Mode: ModeAll}, nil
	case ModeFiles:
		target.Files = append(target.Files, files...)
		return target, proposal.Scope{Mode: ModeFiles, Files: slices.Clone(files)}, nil
	}
	b, e := w.LoadBaseline(proj, baseline.Source{}, false)
	if e != nil {
		return target, proposal.Scope{}, e
	}
	if b.State == baseline.StateUnavailable {
		return target, proposal.Scope{}, NoBaseline(b)
	}
	for _, f := range spec.Files {
		if !f.ValidUTF8 {
			continue
		}
		var old []byte
		if hf := b.Spec.File(f.Path); hf != nil {
			old = hf.Source()
		}
		target.Regions = append(target.Regions, specctx.Changed(f.Path, old, f.Source())...)
	}
	return target, proposal.Scope{Mode: ModeChanged, Head: b.Commit}, nil
}

// validFiles are the spec's files that are valid UTF-8.
func validFiles(spec *mdspec.Spec) []string {
	var out []string
	for _, f := range spec.Files {
		if f.ValidUTF8 {
			out = append(out, f.Path)
		}
	}
	return out
}
