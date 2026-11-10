// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jwriteclub/spec/internal/proposal"
)

func TestPendingFor(t *testing.T) {
	mk := func(id, name, status string) *proposal.Proposal {
		p := &proposal.Proposal{ID: id, Status: status}
		if name != "" {
			p.Primary = &proposal.Primary{Name: name}
		}
		return p
	}
	existing := []*proposal.Proposal{
		mk("a1", "$Frame.Timing", proposal.StatusRejected),
		mk("b2", "$Frame.Timing", proposal.StatusPending),
		mk("c3", "$Frame.Timing", proposal.StatusPending),
		mk("d4", "", proposal.StatusPending), // an analysis
		mk("e5", "$Frame.Order", proposal.StatusApplied),
	}
	got := PendingFor(existing, []string{"Frame.Timing", "$Frame.Timing", "Frame.Order", "Frame.Gap"})
	assert.Equal(t, []Duplicate{{Name: "$Frame.Timing", ID: "b2"}}, got, "decided and anchorless proposals do not count; the first pending one is named")
	assert.Equal(t, "$Frame.Timing already has pending proposal b2", got[0].Message())
	assert.Empty(t, PendingFor(nil, []string{"Frame.Timing"}))
}
