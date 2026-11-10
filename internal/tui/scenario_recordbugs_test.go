// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"testing"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/tui"
)

// Bugs found while recording the demo videos (2026-11-08).

// TestScenarioCommitShowsAcknowledgedProposalChecked: an agent's proposal
// that went through its check, applied and saved in spec ui, and whose
// waiter then acknowledged it, still counts as the check of its anchor in
// the commit view. The waiter moves the receipt out of the proposal list;
// the commit view read only that list and said "Not checked".
func TestScenarioCommitShowsAcknowledgedProposalChecked(t *testing.T) {
	s := unsaved(t, Replies(infer.CannedFile{
		Check: []infer.CannedEntry{cleanReply},
		Draft: []infer.CannedEntry{draftReply},
	}))
	s.Press("ctrl+o")
	expectApplied(s) // its waiter acknowledges the receipt
	if ps := s.Proposals(); len(ps) != 0 {
		s.fail("the waiter left %d proposals in the list", len(ps))
	}
	s.Press("A")
	s.Idle()
	s.ExpectFocus("commit")
	s.Expect("$Frame.Timing reads checked", func(st tui.State) bool {
		if st.Commit == nil || len(st.Commit.Items) != 1 {
			return false
		}
		it := st.Commit.Items[0]
		return it.ID == "$Frame.Timing" && it.State == "checked"
	})
	s.ExpectText("Checked current text")
	s.ExpectNoText("Not checked")
}
