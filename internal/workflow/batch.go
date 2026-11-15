// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"maps"
	"strings"

	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/proposal"
)

// changeOf is the specctx change primary pr stands for, with the author's
// reason why.
func changeOf(pr *proposal.Primary, why string) *specctx.Change {
	ch := &specctx.Change{
		Action: pr.Action, Name: strings.TrimPrefix(pr.Name, "$"), File: pr.File,
		OldKind: mdspec.Kind(pr.OldKind), NewKind: mdspec.Kind(pr.NewKind),
		OldText: pr.OldText, NewText: pr.NewText, Why: why,
	}
	if pr.Action == specctx.ActionAdd && pr.Placement != nil {
		ch.Where = DescribePlacement(*pr.Placement)
	}
	return ch
}

// checkTarget is the target of p's required check: every primary's change.
func checkTarget(p *proposal.Proposal) specctx.Target {
	prs := p.Primaries()
	if len(prs) == 0 {
		return specctx.Target{}
	}
	t := specctx.Target{Change: changeOf(prs[0], p.Why)}
	for _, pr := range prs[1:] {
		t.Batch = append(t.Batch, changeOf(pr, p.Why))
	}
	return t
}

// CheckDeps is what p's required check depends on in spec
// (specctx.Deps): the target anchors, the anchors in its neighborhood and
// the prose that mentions the targets, by content hash. Nil without a
// primary.
func CheckDeps(p *proposal.Proposal, spec *mdspec.Spec) map[string]string {
	if p.Primary == nil {
		return nil
	}
	return specctx.Deps(spec, checkTarget(p))
}

// HasCheckSnapshot reports whether p records what its required check
// stood for: dependency hashes, or a legacy whole-tree snapshot.
func HasCheckSnapshot(p *proposal.Proposal) bool {
	return len(p.CheckDeps) > 0 || len(p.CheckFiles) > 0
}

// checkFresh reports whether p's required check still stands for spec. A
// check with dependency hashes is fresh while every dependency hashes the
// same; a legacy check (whole-tree file hashes only) while every spec file
// does. A proposal with neither has nothing to compare.
func checkFresh(p *proposal.Proposal, spec *mdspec.Spec) bool {
	switch {
	case len(p.CheckDeps) > 0:
		return maps.Equal(p.CheckDeps, CheckDeps(p, spec))
	case len(p.CheckFiles) > 0:
		return sameSnapshot(p.CheckFiles, spec)
	}
	return true
}

// snapshotCheck records in p what a check over spec depends on, replacing
// any legacy whole-tree snapshot.
func snapshotCheck(p *proposal.Proposal, spec *mdspec.Spec) {
	if p.Primary == nil {
		// An analysis or a fix covers files, not anchors: it keeps the
		// whole-tree snapshot.
		p.CheckDeps, p.CheckFiles = nil, snapshotHashes(spec)
		return
	}
	p.CheckDeps = CheckDeps(p, spec)
	p.CheckFiles = nil
}

// CopySnapshot makes dst stand for what src's check stood for (the
// reviewer's view laid over the stored proposal). A src without a snapshot
// leaves dst's in place, so staleness protection is never cleared.
func CopySnapshot(dst, src *proposal.Proposal) {
	if src != nil && HasCheckSnapshot(dst) && HasCheckSnapshot(src) {
		dst.CheckDeps, dst.CheckFiles = src.CheckDeps, src.CheckFiles
	}
}

// RefreshOwnSnapshot updates p's check snapshot after p's own edits were
// applied, giving spec, the text with them (and nothing else new: the
// caller verified the snapshot just before). A legacy whole-tree snapshot
// becomes a dependency snapshot.
func RefreshOwnSnapshot(p *proposal.Proposal, spec *mdspec.Spec) {
	if HasCheckSnapshot(p) {
		snapshotCheck(p, spec)
	}
}

// pendingPrimaries returns the primaries of p in set that still write
// text: not applied, not already in the working tree.
func pendingPrimaries(p *proposal.Proposal, set map[int]bool) []*proposal.Primary {
	var out []*proposal.Primary
	for _, pr := range p.Primaries() {
		if set[pr.N] && !pr.InWorktree && !p.AppliedHunk(pr.N) {
			out = append(out, pr)
		}
	}
	return out
}

// wholeBatch refuses an accept set that leaves out some primaries of p but
// not all: a batch is decided as a unit. Leaving out every primary (a
// reviewer applying finding hunks only) is the caller's to judge.
func wholeBatch(p *proposal.Proposal, set map[int]bool) *Error {
	if !p.IsBatch() {
		return nil
	}
	var in, out []int
	for _, pr := range p.Primaries() {
		if p.AppliedHunk(pr.N) {
			continue
		}
		if set[pr.N] {
			in = append(in, pr.N)
		} else {
			out = append(out, pr.N)
		}
	}
	if len(in) > 0 && len(out) > 0 {
		return usageErr("proposal %s is a batch: its changes (hunks %s) are applied together; hunk %s cannot be left out. To drop the batch, run `spec reject %s --why ...`", p.ID, JoinInts(p.PrimaryNumbers()), JoinInts(out), p.ID)
	}
	return nil
}
