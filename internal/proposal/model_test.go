// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package proposal

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/edit"
)

const modelBase = "---- MODULE M ----\nEXTENDS Naturals\nVARIABLES x\n\nInit == x = 0\n\nNext == x' = x + 1\n\nInv_A == TRUE\n\nInv_B == TRUE\n====\n"

// modelProposal is a model proposal of base into text.
func modelProposal(base, text string) *Proposal {
	p := &Proposal{ID: "m1", Kind: KindModel, Model: &Model{File: "tla/M.tla"}, Status: StatusPending}
	p.Hunks = ModelHunks("tla/M.tla", base, text)
	return p
}

// applyAll applies every hunk of p to src.
func applyAll(t *testing.T, p *Proposal, src string) string {
	t.Helper()
	prep, err := p.PrepareModel([]byte(src), map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true})
	require.NoError(t, err)
	if len(prep.Edits) == 0 {
		return src
	}
	out, err := edit.Apply(prep.Edits[0].Snapshot, prep.Edits[0].Splices)
	require.NoError(t, err)
	return string(out)
}

func TestModelHunksRoundTrip(t *testing.T) {
	cases := map[string]string{
		"one line":            strings.Replace(modelBase, "x + 1", "x + 2", 1),
		"repeated line":       strings.Replace(modelBase, "Inv_B == TRUE", "Inv_B == FALSE", 1),
		"insert at end":       strings.Replace(modelBase, "====\n", "Inv_C == TRUE\n====\n", 1),
		"insert at start":     "\\* header\n" + modelBase,
		"two places":          strings.Replace(strings.Replace(modelBase, "x = 0", "x = 1", 1), "x + 1", "x + 3", 1),
		"delete a definition": strings.Replace(modelBase, "Inv_A == TRUE\n\n", "", 1),
		"no final newline":    strings.TrimSuffix(modelBase, "\n") + "\n\\* tail",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			p := modelProposal(modelBase, text)
			require.NotEmpty(t, p.Hunks)
			for _, h := range p.Hunks {
				assert.Equal(t, 1, strings.Count(modelBase, h.Find), "hunk %d's find must be unique: %q", h.N, h.Find)
				assert.Equal(t, h.Find, modelBase[h.Range.Start:h.Range.End])
			}
			assert.Equal(t, text, applyAll(t, p, modelBase))
		})
	}
}

func TestModelHunksGrowPastRepeatedLines(t *testing.T) {
	// "Inv_B == TRUE" alone is not unique ("TRUE" lines repeat), so context grows.
	text := strings.Replace(modelBase, "Inv_B == TRUE", "Inv_B == FALSE", 1)
	p := modelProposal(modelBase, text)
	require.Len(t, p.Hunks, 1)
	assert.Contains(t, p.Hunks[0].Find, "Inv_B == TRUE")
}

func TestPrepareModelIgnoresEditsElsewhere(t *testing.T) {
	text := strings.Replace(modelBase, "x + 1", "x + 2", 1)
	p := modelProposal(modelBase, text)
	moved := strings.Replace(modelBase, "VARIABLES x", "VARIABLES x \\* the counter", 1)
	got := applyAll(t, p, moved)
	assert.Contains(t, got, "x + 2")
	assert.Contains(t, got, "the counter", "the other edit survives")
}

func TestPrepareModelIsAllOrNothing(t *testing.T) {
	text := strings.Replace(strings.Replace(modelBase, "x = 0", "x = 1", 1), "x + 1", "x + 3", 1)
	p := modelProposal(modelBase, text)
	require.Len(t, p.Hunks, 2)

	// One hunk's text is gone: the whole proposal is stale, naming the model.
	moved := strings.Replace(modelBase, "x + 1", "x + 9", 1)
	_, err := p.PrepareModel([]byte(moved), map[int]bool{1: true, 2: true})
	var stale *StaleError
	require.True(t, errors.As(err, &stale), "got %v", err)
	assert.Equal(t, "tla/M.tla", stale.Name)
	assert.Contains(t, stale.Reason, "applies as a whole")

	// Leaving a hunk out is refused.
	_, err = p.PrepareModel([]byte(modelBase), map[int]bool{1: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "applies as a whole")
	assert.False(t, errors.As(err, &stale))

	// Prepare refuses other kinds.
	_, err = (&Proposal{Kind: KindChange}).PrepareModel([]byte(modelBase), nil)
	require.Error(t, err)
}

func TestModelNormalizeAndErrors(t *testing.T) {
	p := modelProposal(modelBase, strings.Replace(modelBase, "x + 1", "x + 2", 1))
	p.Normalize()
	assert.NotNil(t, p.Model.Decls)
	assert.NotNil(t, p.Model.Links)
	assert.NotNil(t, p.Model.Check.Diagnostics)
	assert.True(t, p.IsModel())
	assert.Equal(t, 0, p.Model.Check.Errors())
}
