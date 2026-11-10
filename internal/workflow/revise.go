// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"strings"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/proposal"
)

// Revision is the outcome of ReviseHunk.
type Revision struct {
	// Withdrawn is true when the model withdrew the edit (nothing changed).
	Withdrawn bool
	// Hunk is the revised hunk, saved into the proposal.
	Hunk finding.Hunk
	// Proposal is the proposal as saved.
	Proposal *proposal.Proposal
}

// ReviseHunk asks the model to redo hunk h of proposal p with the
// reviewer's feedback and, when the revision verifies against the working
// tree, saves it into the proposal.
func (w *Workflow) ReviseHunk(ck *Checker, root string, p *proposal.Proposal, h finding.Hunk, feedback string) (*Revision, *Error) {
	if e := w.CheckRecovery(root, p.ID); e != nil {
		return nil, e
	}
	proj, e := openRoot(root)
	if e != nil {
		return nil, e
	}
	loaded, err := proj.Load(nil)
	if err != nil {
		return nil, internalErr(err)
	}
	f := loaded.Spec.File(h.File)
	if f == nil {
		return nil, fail(ExitFail, "not_found", "%s is no longer a spec file", h.File)
	}
	rev, e := ck.Revise(prompt.ReviseInput{
		File: h.File, FileText: string(f.Source()), Find: h.Find, Replace: h.Replace,
		Explanation: h.Explanation, Feedback: feedback,
	})
	if e != nil {
		return nil, e
	}
	if rev.Find == "" {
		return &Revision{Withdrawn: true, Hunk: h, Proposal: p}, nil
	}
	src := string(f.Source())
	if c := strings.Count(src, rev.Find); c != 1 {
		return nil, fail(ExitFail, "bad_model_output", "the revised edit quotes text that occurs %d times in %s (it must occur exactly once); try other feedback", c, h.File)
	}
	if rev.Find == rev.Replace {
		return nil, fail(ExitFail, "bad_model_output", "the revised edit changes nothing")
	}
	start := strings.Index(src, rev.Find)
	if pr := p.Primary; pr != nil {
		for _, a := range loaded.Spec.Lookup(strings.TrimPrefix(pr.Name, "$")) {
			if a.File == h.File && start < a.BodyRange.End && a.BodyRange.Start < start+len(rev.Find) {
				return nil, fail(ExitFail, "bad_model_output", "the revised edit changes %s, the change under review; try other feedback", pr.Name)
			}
		}
	}
	revised := h
	revised.Find, revised.Replace = rev.Find, rev.Replace
	revised.Range = pos.Range{Start: start, End: start + len(rev.Find)}
	if rev.Explanation != "" {
		revised.Explanation = rev.Explanation
	}
	if e := ck.cancelled(); e != nil {
		return nil, e
	}
	lock, e := w.DecisionLock(ck.ctx, root, "spec revise "+p.ID)
	if e != nil {
		return nil, e
	}
	defer func() { _ = lock.Release() }()
	if e := ck.cancelled(); e != nil {
		return nil, e
	}
	cur, e := w.SetHunkSeenLocked(lock, root, p, revised)
	if e != nil {
		return nil, e
	}
	return &Revision{Hunk: revised, Proposal: cur}, nil
}
