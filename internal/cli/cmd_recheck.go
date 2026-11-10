// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

func (s *session) recheckCommand() *cobra.Command {
	var full, retry bool
	cmd := &cobra.Command{
		Use:   "recheck ID [--full] [--retry]",
		Short: "recheck a pending proposal in place against current source files",
		Long: "Keeps the proposal ID and the change itself (the primary), reads the current spec files and\n" +
			"rebuilds the check's findings. Only a successful recheck clears stale_primary; a failed one leaves\n" +
			"it. --retry reuses the original snapshot and cannot clear stale_primary. A primary that no longer\n" +
			"applies fails with exit 1 stale. A partly applied proposal cannot be rechecked: look at the applied\n" +
			"hunks, ask the user to reject the remaining edits to close it, and file the remaining work anew.",
		Args: needArgs(1, "recheck ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, o := s.resolveRoot()
			if o != nil {
				s.result = o
				return nil
			}
			proj, o := s.openRoot(root)
			if o != nil {
				s.result = o
				return nil
			}
			wf := s.workflow()
			var p *proposal.Proposal
			var e *workflow.Error
			if retry {
				p, e = wf.CheckPublished(context.Background(), proj, args[0], full, nil)
			} else {
				p, e = wf.RecheckPublished(context.Background(), proj, args[0], full, nil, nil)
			}
			if e != nil {
				s.result = fromWF(e)
				if p != nil {
					s.result.data = proposalData{Proposal: p}
					s.result.human += renderProposal(p)
				}
			} else {
				s.result = &outcome{data: proposalData{Proposal: p}, human: renderProposal(p)}
			}
			return nil
		},
	}
	s.addProviderFlags(cmd)
	cmd.Flags().BoolVar(&full, "full", false, "send all selected spec files whole to the model")
	cmd.Flags().BoolVar(&retry, "retry", false, "retry only if the original source snapshot is unchanged")
	return cmd
}
