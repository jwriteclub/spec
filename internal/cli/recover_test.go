// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

func TestRecoverCommandInspectsAndArchivesWithoutDeciding(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec.toml", "name = \"recovery\"\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".spec"), 0o755))
	p := &proposal.Proposal{Kind: proposal.KindAnalysis, SourceKind: "agent"}
	require.NoError(t, proposal.NewStore(dir).Create(p))
	before, err := os.ReadFile(proposal.NewStore(dir).Path(p.ID))
	require.NoError(t, err)
	intent := workflow.RecoveryIntent{ID: "0123456789abcdef0123456789abcdef", Kind: "approve", Proposal: p, Accepted: []int{1}}
	data, err := json.Marshal(intent)
	require.NoError(t, err)
	write(t, dir, ".spec/operation.json", string(data))
	inspected := run(t, dir, "recover")
	require.Equal(t, 0, inspected.code, inspected.stderr)
	assert.Contains(t, inspected.stdout, intent.ID)
	assert.Contains(t, inspected.stdout, "git diff --cached")
	missingNote := run(t, dir, "recover", "--acknowledge")
	assert.Equal(t, 1, missingNote.code)
	acknowledged := run(t, dir, "recover", "--acknowledge", "--why", "retained later staged work")
	require.Equal(t, 0, acknowledged.code, acknowledged.stderr)
	assert.Contains(t, acknowledged.stdout, "proposal remains blocked")
	after, err := os.ReadFile(proposal.NewStore(dir).Path(p.ID))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = os.Stat(filepath.Join(dir, ".spec", "operation.json"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	archived, err := os.ReadFile(filepath.Join(dir, ".spec", "recovery", intent.ID+".json"))
	require.NoError(t, err)
	assert.Contains(t, string(archived), "retained later staged work")
	e := (&workflow.Workflow{}).CheckRecovery(dir, p.ID)
	require.NotNil(t, e)
	assert.Equal(t, "recovery_required", e.Code)
}
