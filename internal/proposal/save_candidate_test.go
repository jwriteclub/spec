// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package proposal

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveFailurePreservesCallerAndNestedNormalization(t *testing.T) {
	st := NewStore(t.TempDir())
	p := &Proposal{Kind: KindChange, Primary: &Primary{Placement: &Placement{}}}
	require.NoError(t, st.Create(p))
	p.Primary.Placement.Earlier = nil
	p.Decision = &Decision{}
	p.Hunks = nil
	revision := p.Revision
	require.NoError(t, os.Remove(st.Path(p.ID)))
	require.NoError(t, os.Mkdir(st.Path(p.ID), 0o755))
	require.Error(t, st.Save(p))
	assert.Equal(t, revision, p.Revision)
	assert.Nil(t, p.Primary.Placement.Earlier)
	assert.Nil(t, p.Decision.Applied)
	assert.Nil(t, p.Hunks)
	require.NoError(t, os.Remove(st.Path(p.ID)))
	require.NoError(t, st.Save(p))
	got, err := st.Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, revision+1, got.Revision)
	assert.Equal(t, got, p)
}

// Successful persistence keeps the caller's object graph: code holding the
// primary/placement/decision sees normalization without pointer replacement.
func TestSaveSuccessPreservesIdentityAndNestedAliases(t *testing.T) {
	st := NewStore(t.TempDir())
	p := &Proposal{Kind: KindChange, Author: "Chris", SourceKind: "human", Primary: &Primary{N: 1, Name: "$One", Placement: &Placement{}}}
	require.NoError(t, st.Create(p))
	p.Decision = &Decision{Reason: "reviewed"}
	p.Primary.Placement.Earlier = nil
	p.Hunks, p.Notes, p.Questions = nil, nil, nil
	p.Base.Files, p.Base.Anchors = nil, nil
	primary, placement, decision := p.Primary, p.Primary.Placement, p.Decision
	id, created, revision := p.ID, p.Created, p.Revision
	require.NoError(t, st.Save(p))
	assert.Equal(t, id, p.ID)
	assert.Equal(t, created, p.Created)
	assert.Equal(t, "Chris", p.Author)
	assert.Equal(t, "human", p.SourceKind)
	assert.Same(t, primary, p.Primary)
	assert.Same(t, placement, p.Primary.Placement)
	assert.Same(t, decision, p.Decision)
	assert.NotNil(t, placement.Earlier)
	assert.NotNil(t, decision.Accepted)
	assert.NotNil(t, decision.Applied)
	assert.NotNil(t, decision.Failed)
	assert.Equal(t, revision+1, p.Revision)
	persisted, err := st.Load(id)
	require.NoError(t, err)
	assert.Equal(t, p, persisted, "caller normalization agrees with the saved independent candidate")
}
