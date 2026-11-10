// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
)

func TestCovers(t *testing.T) {
	changed := &proposal.Scope{Mode: ModeChanged, Head: "h1"}
	for _, c := range []struct {
		name         string
		newer, older *proposal.Scope
		want         bool
	}{
		{"all covers changed", &proposal.Scope{Mode: ModeAll}, changed, true},
		{"all covers files", &proposal.Scope{Mode: ModeAll}, &proposal.Scope{Mode: ModeFiles, Files: []string{"a"}}, true},
		{"changed covers the same HEAD", &proposal.Scope{Mode: ModeChanged, Head: "h1"}, changed, true},
		{"changed does not cover another HEAD", &proposal.Scope{Mode: ModeChanged, Head: "h2"}, changed, false},
		{"changed covers a legacy analysis", &proposal.Scope{Mode: ModeChanged, Head: "h2"}, nil, true},
		{"changed does not cover all", changed, &proposal.Scope{Mode: ModeAll}, false},
		{"files cover a subset", &proposal.Scope{Mode: ModeFiles, Files: []string{"a", "b"}}, &proposal.Scope{Mode: ModeFiles, Files: []string{"b"}}, true},
		{"files do not cover others", &proposal.Scope{Mode: ModeFiles, Files: []string{"a"}}, &proposal.Scope{Mode: ModeFiles, Files: []string{"a", "b"}}, false},
		{"files do not cover changed", &proposal.Scope{Mode: ModeFiles, Files: []string{"a"}}, changed, false},
		{"no scope covers nothing", nil, changed, false},
	} {
		assert.Equal(t, c.want, Covers(c.newer, c.older), c.name)
	}
}

// A newer analysis of the same scope supersedes the older pending ones
// nobody acted on, keeping their records; answers, checks, analyses of
// another scope, partly applied ones and those decided in spec ui stay.
func TestSupersedeStaleAnalyses(t *testing.T) {
	root := t.TempDir()
	w := &Workflow{Getenv: func(string) string { return "" }}
	st := w.Store(root)
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	head := &proposal.Scope{Mode: ModeChanged, Head: "h1"}
	hunk := []finding.Hunk{{N: 1, File: "c.spec.md", Find: "a", Replace: "b"}}
	file := func(p *proposal.Proposal) string {
		t.Helper()
		if p.Kind == "" {
			p.Kind = proposal.KindAnalysis
		}
		if p.Created.IsZero() {
			p.Created = t0
		}
		p.Questions = []string{"which?"}
		require.NoError(t, st.Create(p))
		return p.ID
	}
	stale := file(&proposal.Proposal{Scope: head})
	legacy := file(&proposal.Proposal{})
	otherHead := file(&proposal.Proposal{Scope: &proposal.Scope{Mode: ModeChanged, Head: "h0"}})
	files := file(&proposal.Proposal{Scope: &proposal.Scope{Mode: ModeFiles, Files: []string{"c.spec.md"}}})
	answer := file(&proposal.Proposal{Scope: head, Rethink: &proposal.Rethink{Of: "x"}})
	check := file(&proposal.Proposal{Kind: proposal.KindChange})
	partly := file(&proposal.Proposal{Scope: head, Hunks: hunk, Decision: &proposal.Decision{Applied: []int{1}}})
	decided := file(&proposal.Proposal{Scope: head})
	later := file(&proposal.Proposal{Scope: head, Created: t0.Add(time.Hour)})
	write(t, root, ChecksFile, `{"decided": {"`+decided+`": {"q0": "dismissed"}, "`+stale+`": {}}}`)

	// A clean re-analysis is not filed: it supersedes all the same.
	newer := &proposal.Proposal{Kind: proposal.KindAnalysis, Created: t0.Add(time.Minute), Scope: &proposal.Scope{Mode: ModeChanged, Head: "h1"}}
	ids, e := w.SupersedeAnalyses(context.Background(), root, newer, "")
	require.Nil(t, e)
	assert.ElementsMatch(t, []string{stale, legacy}, ids)
	for _, id := range []string{stale, legacy} {
		p, e := w.LoadProposal(root, id)
		require.Nil(t, e)
		assert.Equal(t, proposal.StatusRejected, p.Status, id)
		assert.Equal(t, "superseded by a newer analysis of the same scope", p.Decision.Reason)
		assert.Equal(t, []string{"which?"}, p.Questions, "the record is kept")
	}
	for _, id := range []string{otherHead, files, answer, check, partly, decided, later} {
		p, e := w.LoadProposal(root, id)
		require.Nil(t, e)
		assert.Equal(t, proposal.StatusPending, p.Status, id)
	}

	// A filed newer analysis is named; the one decided in spec ui stays
	// whatever keep the caller adds.
	newer.ID = "newer"
	newer.Created = t0.Add(2 * time.Hour)
	lock, e := w.DecisionLock(context.Background(), root, "test")
	require.Nil(t, e)
	defer func() { _ = lock.Release() }()
	ids, e = w.SupersedeAnalysesLocked(lock, root, newer, func(id string) bool { return id == decided }, "")
	require.Nil(t, e)
	assert.Equal(t, []string{later}, ids)
	p, e := w.LoadProposal(root, later)
	require.Nil(t, e)
	assert.Equal(t, "superseded by analysis newer", p.Decision.Reason)
}

func TestSidebarActedOn(t *testing.T) {
	root := t.TempDir()
	keep, err := SidebarActedOn(root)
	require.NoError(t, err)
	assert.False(t, keep("a"), "no file")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".spec"), 0o755))
	write(t, root, ChecksFile, `{"decided": {"a": {"h1": "accepted"}, "b": {}}}`)
	keep, err = SidebarActedOn(root)
	require.NoError(t, err)
	assert.True(t, keep("a"))
	assert.False(t, keep("b"), "marked as Chris's to settle, but nothing decided")
	assert.False(t, keep("c"))
	write(t, root, ChecksFile, `{`)
	_, err = SidebarActedOn(root)
	assert.Error(t, err)
}
