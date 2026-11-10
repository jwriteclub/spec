// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"strings"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/proposal"
)

// Accounting says how far applied proposals explain one anchor's difference
// from the baseline.
type Accounting struct {
	// IDs are the applied proposals that changed the anchor since the
	// baseline; none means they explain nothing of it.
	IDs []string
	// Whole is true when they explain all of it: the anchor is what they
	// left. With IDs and not Whole, it has a further edit on top.
	Whole bool
}

// AccountFor replays the applied hunks of evidence (oldest first, from
// proposal.Store.Applied) over the anchor's baseline text, and compares the
// result with the working tree. A hunk whose starting text is not what the
// replay has reached does not count, so a change since committed explains
// nothing. Text is compared by anchor hash and kind, so a reflow changes
// nothing.
func AccountFor(e baseline.Entry, head, work *mdspec.Spec, evidence []*proposal.Proposal) Accounting {
	var cur, want anchorText
	if o := e.Old; o != nil {
		cur = anchorText{present: true, file: o.File, text: head.File(o.File).Text(o.BodyRange)}
	}
	if n := e.New; n != nil {
		want = anchorText{present: true, file: n.File, text: work.File(n.File).Text(n.BodyRange)}
	}
	var out Accounting
	for _, p := range evidence {
		touched := false
		if pr := p.Primary; pr != nil && pr.Name == "$"+e.Name && p.AppliedHunk(pr.N) {
			if pr.Action == specctx.ActionAdd && !cur.present || pr.Action != specctx.ActionAdd && cur.present && sameAnchor(e.Name, cur.text, pr.OldText) {
				cur = anchorText{present: pr.Action != specctx.ActionRemove, file: pr.File, text: pr.NewText}
				touched = true
			}
		}
		for _, h := range p.Hunks {
			if p.AppliedHunk(h.N) && cur.present && h.File == cur.file && h.Find != "" && strings.Count(cur.text, h.Find) == 1 {
				cur.text = strings.Replace(cur.text, h.Find, h.Replace, 1)
				touched = true
			}
		}
		if touched {
			out.IDs = append(out.IDs, p.ID)
		}
	}
	if len(out.IDs) > 0 {
		out.Whole = cur.present == want.present && (!cur.present || sameAnchor(e.Name, cur.text, want.text))
	}
	return out
}

// Unaccounted returns the names of d's anchors that need a proposal and are
// not wholly explained by applied proposals, in d's order.
func Unaccounted(d *baseline.Diff, head, work *mdspec.Spec, evidence []*proposal.Proposal) []string {
	out := []string{}
	for _, e := range d.Entries {
		if e.NeedsChangeTrailer() && !AccountFor(e, head, work, evidence).Whole {
			out = append(out, e.Name)
		}
	}
	return out
}

// anchorText is one state of an anchor in a replay.
type anchorText struct {
	present bool
	file    string
	text    string
}

// sameAnchor reports whether two texts define anchor name the same way.
func sameAnchor(name, a, b string) bool {
	if a == b {
		return true
	}
	ha, ka, okA := hashOf(name, a)
	hb, kb, okB := hashOf(name, b)
	return okA && okB && ha == hb && ka == kb
}

// hashOf parses text alone and returns anchor name's hash and kind.
func hashOf(name, text string) (string, mdspec.Kind, bool) {
	spec := mdspec.Build([]*mdspec.File{mdspec.ParseFile("anchor.spec.md", []byte(text))})
	as := spec.Lookup(name)
	if len(as) != 1 {
		return "", "", false
	}
	return as[0].Hash, as[0].Kind, true
}
