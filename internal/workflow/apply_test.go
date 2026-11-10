// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
)

func TestChanged(t *testing.T) {
	base := func() *proposal.Proposal {
		return &proposal.Proposal{
			ID:      "abcd",
			Primary: &proposal.Primary{N: 1, Name: "$A", NewText: "new"},
			Hunks: []finding.Hunk{
				{N: 2, File: "a.spec.md", Find: "x", Replace: "y"},
				{N: 3, File: "a.spec.md", Find: "p", Replace: "q"},
			},
		}
	}
	a, b := base(), base()
	assert.Empty(t, Changed(a, b, nil))

	b.Hunks[1].Replace = "r"
	assert.Equal(t, []int{3}, Changed(a, b, nil))
	assert.Equal(t, []int{3}, Changed(a, b, []int{1, 3}))
	assert.Empty(t, Changed(a, b, []int{1, 2}), "hunk 3 was not accepted")

	b = base()
	b.Hunks = b.Hunks[:1]
	assert.Equal(t, []int{3}, Changed(a, b, []int{3}), "a hunk gone")

	b = base()
	b.Primary.NewText = "other"
	assert.Equal(t, []int{1}, Changed(a, b, []int{1}))
}
