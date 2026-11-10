// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/spell"
)

// formatSpell is one flag as a lint-style line.
func formatSpell(f spell.Flag) string {
	return locString(f.File, f.Line, f.Col) + ": spell: " + spell.Message(f) + "\n"
}

// renderSpell is a spell report for the human output: one line per flag,
// then what the model pass did, or why it could not run.
func renderSpell(rep *spell.Report) string {
	if rep == nil {
		return ""
	}
	var b strings.Builder
	for _, f := range rep.Flags {
		b.WriteString(formatSpell(f))
	}
	st := rep.Stats
	if rep.Models != "" && st.Asked > 0 {
		fmt.Fprintf(&b, "spell check: %s, %d cached, %d asked (%s, %s), %.1f s",
			plural(st.Candidates, "unknown word"), st.Cached, st.Asked, plural(st.GateCalls, "gate call"), plural(st.TriageCalls, "triage call"), float64(st.LatencyMS)/1000)
		if st.CostUSD > 0 {
			fmt.Fprintf(&b, ", $%.5f", st.CostUSD)
		}
		b.WriteString("\n")
	}
	if rep.Error != "" && (st.Asked > 0 || rep.Models == "") {
		b.WriteString("spell check: the model pass failed, so only known misspellings and cached verdicts are shown: " + rep.Error + "\n")
	}
	return b.String()
}

// spellProposals spell-checks the proposed text of ps against the working
// tree's vocabulary.
func (s *session) spellProposals(proj *project.Project, spec *mdspec.Spec, ps []*proposal.Proposal) *spell.Report {
	if spec == nil {
		loaded, err := proj.Load(nil)
		if err != nil {
			return nil
		}
		spec = loaded.Spec
	}
	return s.workflow().SpellProposals(context.Background(), proj, spec, ps)
}

// withSpell adds a spell report to a successful propose or change outcome.
func withSpell(o *outcome, rep *spell.Report) *outcome {
	if o == nil || rep == nil {
		return o
	}
	if d, ok := o.data.(*filedData); ok {
		d.Spell = rep
	}
	o.human += renderSpell(rep)
	return o
}
