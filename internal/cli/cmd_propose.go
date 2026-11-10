// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

type proposeFlags struct {
	fileFlags
	keep bool
}

func (s *session) proposeCommand() *cobra.Command {
	var f proposeFlags
	cmd := &cobra.Command{
		Use:   "propose [NAME...] --why W [--keep] [--wait D]",
		Short: "turn direct edits of anchors (as spec status reports them) into proposals for review",
		Long: "For each NAMEd anchor: records the new text, runs the contradiction check, files a proposal, and\n" +
			"restores the anchor's HEAD text in the working tree. An anchor an applied proposal explains is\n" +
			"skipped. Only --keep (leaves the edit in place and proposes only the follow-on fixes; for humans) and\n" +
			"--source human may leave out the names, for every other changed anchor. Exit 2 with the\n" +
			"proposal ids, or with --wait: 0 applied, 3 rejected, 6 stale (still pending), 2 timeout.",
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runPropose(args, f)
			return nil
		},
	}
	cmd.Flags().BoolVar(&f.keep, "keep", false, "keep the edit in the working tree; propose only the follow-on fixes")
	s.addFileFlags(cmd, &f.fileFlags)
	return cmd
}

func (s *session) runPropose(args []string, f proposeFlags) *outcome {
	if f.source != "" && f.source != "agent" && f.source != "human" {
		return usageErr("--source must be agent or human")
	}
	if strings.TrimSpace(f.why) == "" {
		return usageErr("--why is required: say why the change is needed")
	}
	if f.wait < 0 {
		return usageErr("--wait must not be negative")
	}
	var names []string
	seen := map[string]bool{}
	for _, a := range args {
		if e := workflow.ModelCommandHint("propose", a); e != nil {
			return fromWF(e)
		}
		n, o := bareName(a)
		if o != nil {
			return o
		}
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	proj, o := s.openRoot(root)
	if o != nil {
		return o
	}
	// What is pending before this run files anything, for the warning.
	pending, _ := s.store(root).List()
	res, e := s.workflow().Propose(context.Background(), proj, workflow.ProposeRequest{
		Names: names, Why: f.why, Author: f.sourceAuthor(), SourceKind: f.source, Keep: f.keep, AutoApprove: f.autoApprove, Full: f.full,
	})
	if e != nil {
		out := fromWF(e)
		if res != nil && len(res.Proposals) > 0 {
			out = filedFailure(res.Proposals, res.Skipped, out)
		}
		if e.Code == "names_required" {
			out.data = &filedData{Proposals: []*proposal.Proposal{}, Skipped: []string{}, Warnings: []string{}, Next: nextJSON{Text: "name your own edits", Argv: e.Argv}}
		}
		if e.Code == "lint_errors" {
			out.data = &filedData{Proposals: []*proposal.Proposal{}, Skipped: []string{}, Warnings: []string{}, Next: nextJSON{Text: "give each name one definition (spec lint shows where), then run spec propose again", Argv: []string{"spec", "lint"}}}
		}
		return out
	}
	sp := s.spellProposals(proj, nil, append(append([]*proposal.Proposal(nil), res.Proposals...), res.Cleared...))
	cleared, clearedBy := clearedOf(res.Cleared)
	clearedText := ""
	for k, c := range cleared {
		clearedText += fmt.Sprintf("%s: %s; nothing filed\n", c.Anchor, c.Reason)
		clearedText += stageLines(res.Cleared[k], "  ")
	}
	if len(res.Proposals) == 0 && len(res.Cleared) > 0 {
		msg := clearedText
		for _, sk := range res.Skipped {
			msg += "skipped: " + sk + "\n"
		}
		next := nextJSON{Text: "nothing to check; commit the change with the Spec-Change lines spec status prints", Argv: []string{"spec", "status"}}
		msg += "next: " + next.Text + "\n"
		return withSpell(&outcome{data: &filedData{Proposals: []*proposal.Proposal{}, Skipped: res.Skipped, Warnings: []string{}, Next: next, ClearedBy: clearedBy, Cleared: cleared}, human: msg}, sp)
	}
	if len(res.Proposals) == 0 {
		msg := "no changed anchor to propose; spec status says what differs\n"
		for _, sk := range res.Skipped {
			msg += "skipped: " + sk + "\n"
		}
		return &outcome{data: &filedData{Proposals: []*proposal.Proposal{}, Skipped: res.Skipped, Warnings: []string{}, Next: nextJSON{Text: "nothing to propose", Argv: []string{}}}, human: msg}
	}
	human := clearedText
	if len(res.Restored) > 0 {
		human = "restored the HEAD text of " + strings.Join(res.Restored, ", ") + " in the working tree; the edit is kept in the proposal until it is approved\n"
	}
	warnings := []string{}
	var filed []string
	for _, p := range res.Proposals {
		if p.Primary != nil {
			filed = append(filed, p.Primary.Name)
		}
	}
	for _, d := range workflow.PendingFor(pending, filed) {
		warnings = append(warnings, d.Message())
		human += "warning: " + d.Message() + " (filed another anyway; `spec review " + d.ID + "` or `spec reject " + d.ID + "` settles it)\n"
	}
	o = s.fileProposals(root, res.Proposals, res.Skipped, f.fileFlags, human)
	if d, ok := o.data.(*filedData); ok {
		d.Warnings = warnings
		// With --keep the cleared anchors were not filed: list them too.
		d.Cleared = append(cleared, d.Cleared...)
	}
	return withSpell(o, sp)
}
