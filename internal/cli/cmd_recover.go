// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"
)

func (s *session) recoverCommand() *cobra.Command {
	var acknowledge bool
	var why string
	cmd := &cobra.Command{
		Use:   "recover [--acknowledge --why NOTE]",
		Short: "inspect and acknowledge an interrupted approval or commit",
		Long: "Prints the record an interrupted approval or commit left: what it planned to write. Before\n" +
			"retrying, inspect HEAD, the working tree, the index (git diff --cached) and the saved proposal;\n" +
			"nothing is repaired automatically. --acknowledge archives the record and clears only the\n" +
			"project-wide guard. It does not confirm any write or unblock the interrupted proposal: reject that\n" +
			"proposal with a note on what you found, then file only the remaining work as a new proposal.\n" +
			"Existing receipts are kept.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, o := s.resolveRoot()
			if o != nil {
				s.result = o
				return nil
			}
			if !acknowledge && strings.TrimSpace(why) != "" {
				s.result = usageErr("--why requires --acknowledge")
				return nil
			}
			intent, e := s.workflow().Recover(root, acknowledge, why)
			if e != nil {
				s.result = fromWF(e)
				return nil
			}
			if intent == nil {
				s.result = &outcome{human: "No active interrupted operation. Archived evidence, if any, remains in .spec/recovery/.\n"}
				return nil
			}
			data, err := json.MarshalIndent(intent, "", "  ")
			if err != nil {
				s.result = internalErr(err)
				return nil
			}
			text := string(data) + "\n"
			if acknowledge {
				text += "Acknowledged; evidence archived in .spec/recovery/" + intent.ID + ".json. Files, index and proposal receipts are unchanged.\n"
				if intent.Proposal != nil {
					text += "The interrupted proposal remains blocked: explicitly reject " + intent.Proposal.ID + " with an inspection note, then file only remaining work anew.\n"
				}
			} else {
				text += "Inspect HEAD, worktree, git diff --cached, and the saved proposal. Do not blindly reset the index: it may contain later staging.\n" +
					"Then run `spec recover --acknowledge --why NOTE` to archive this evidence and clear the project-wide guard only.\n"
			}
			s.result = &outcome{data: intent, human: text}
			return nil
		},
	}
	cmd.Flags().BoolVar(&acknowledge, "acknowledge", false, "archive the inspected intent and clear the project-wide guard only")
	cmd.Flags().StringVar(&why, "why", "", "inspection note required for acknowledgment")
	return cmd
}
