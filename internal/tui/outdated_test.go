// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
)

// A check whose anchor moved on is outdated; one whose anchor is back to
// HEAD is withdrawn; an agent's proposal is never withdrawn.

// checkedFlow starts the UI on a repo whose $Frame.Timing reads "of at
// most 5 s" on disk and runs ^P on it; it returns the filed check.
func checkedFlow(t *testing.T, reply string) (*flow, string, *proposal.Proposal) {
	t.Helper()
	dir := demoRepo(t)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "above zero", "of at most 5 s", 1))
	m := newFakeModel(t, reply)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	f.findIn("of at most 5 s")
	f.send(key("ctrl+p"))
	f.until("the check", func(a *App) bool { return a.job == nil && len(a.pending) == 1 && itemCards(a) == 2 })
	var p *proposal.Proposal
	f.do(func(a *App) {
		p = a.pending[0]
		assert.Empty(t, a.checkOutdated(p), "fresh")
		assert.NotContains(t, sidebarText(a), "outdated")
	})
	return f, dir, p
}

// replaceAt selects the n bytes at the cursor and types s over them.
func (f *flow) replaceAt(n int, s string) {
	f.t.Helper()
	for range n {
		f.send(key("shift+right"))
	}
	f.typeIn(s)
}

// Edit, check, edit back to HEAD: the check is withdrawn as superseded
// and the flash says so.
func TestCheckWithdrawnWhenReverted(t *testing.T) {
	f, dir, p := checkedFlow(t, checkReply(t))
	f.findIn("of at most 5 s")
	f.replaceAt(len("of at most 5 s"), "above zero")
	f.until("the withdrawal", func(a *App) bool { return len(a.pending) == 0 })
	f.do(func(a *App) {
		assert.Equal(t, "$Frame.Timing is back to its committed text; its check was withdrawn", a.flash)
		assert.Empty(t, a.side.cards)
		assert.NotContains(t, sidebarText(a), "check $Frame.Timing")
	})
	old, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusRejected, old.Status)
	assert.True(t, strings.HasPrefix(old.Decision.Reason, "superseded"), old.Decision.Reason)
	f.finish()
}

// Edit, check, a further different edit: the check is outdated, its
// group says so, and y does not apply its edits; its own accepted edit
// and rewrapping do not make it outdated.
func TestCheckOutdatedAfterFurtherEdit(t *testing.T) {
	f, _, p := checkedFlow(t, checkReply(t))

	// Rewrapping is no change.
	f.findIn("of at most 5 s")
	f.typeIn("  ")
	f.do(func(a *App) { assert.Empty(t, a.checkOutdated(p), "whitespace only") })

	f.findIn("5 s")
	f.replaceAt(len("5"), "7")
	f.until("outdated", func(a *App) bool { return a.checkOutdated(p) != "" })
	f.do(func(a *App) {
		assert.Equal(t, "$Frame.Timing changed since this check", a.checkOutdated(p))
		side := sidebarText(a)
		assert.Contains(t, side, "Needs refresh · $Frame.Timing changed since this check")
		assert.Contains(t, side, "OUTDATED")
		var edit *card
		for _, c := range a.side.cards {
			if c.kind == cardEdit {
				edit = c
				break
			}
		}
		require.NotNil(t, edit)
		assert.Contains(t, a.cardBlocked(edit), "$Frame.Timing changed since this check")
		src := string(a.ws.Source(edit.hunk.File))
		assert.Nil(t, a.acceptCard(edit))
		assert.Contains(t, a.flash, "Outdated: $Frame.Timing changed since this check")
		assert.Equal(t, src, string(a.ws.Source(edit.hunk.File)), "nothing applied")
		assert.Empty(t, a.decided(p.ID, edit.key))

		// A check whose own accepted edit made the same change is current.
		own := *p
		own.ID = "own"
		own.Hunks = []finding.Hunk{{N: 9, File: "demo.spec.md", Find: "at most 5 s", Replace: "at most 7 s"}}
		assert.NotEmpty(t, a.checkOutdated(&own), "not accepted yet")
		a.decide(&own, "h9", decAccepted)
		assert.Empty(t, a.checkOutdated(&own), "its own accepted edit")
	})
	assert.Len(t, f.finish().pending, 1, "an outdated check is not withdrawn")
}

// An agent's proposal of a change still at HEAD in the files is never
// withdrawn, nor marked outdated.
func TestAgentProposalNotWithdrawn(t *testing.T) {
	dir := demoRepo(t)
	agent := agentProposal(t, dir, false)
	m := newFakeModel(t, checkReply(t))
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil && len(a.pending) == 1 })
	f.findIn("above zero")
	f.typeIn("x")
	f.send(key("backspace"))
	f.do(func(a *App) {
		require.Len(t, a.pending, 1)
		assert.Equal(t, agent.ID, a.pending[0].ID)
		assert.Empty(t, a.checkOutdated(a.pending[0]))
		assert.NotContains(t, sidebarText(a), "outdated")
	})
	ag, err := proposal.NewStore(dir).Load(agent.ID)
	require.NoError(t, err)
	assert.Equal(t, proposal.StatusPending, ag.Status)
	f.finish()
}

// A check summary remains readable through Help → Status without duplicating
// its full text across the ordinary finding cards.
func TestCheckSummaryRemainsAccessible(t *testing.T) {
	summary := "Raising CleanGeneration's bound to 2 s conflicts with $Hub.TranscoderCap, which still assumes 1 s; " +
		"either restore the 1 s bound or update the cap and the drain prose, otherwise the transcoder queue overflows at the hub."
	reply := modelJSON(t, map[string]any{
		"summary": summary,
		"findings": []map[string]any{
			{"file": "demo.spec.md", "issue": "missing_update", "severity": "inconsistent", "explanation": "mention the bound", "find": proseFind, "replace": proseReplace},
			{"file": "other.spec.md", "anchor": "$Extra.Limit", "issue": "numeric_mismatch", "severity": "cosmetic", "explanation": "align the queue size", "find": limitFind, "replace": limitReplace},
		},
		"questions": []string{},
	})
	f, _, _ := checkedFlow(t, reply)
	f.do(func(a *App) {
		require.Len(t, a.pending, 1)
		assert.Equal(t, summary, a.pending[0].Summary)
		assert.NotContains(t, sidebarText(a), summary)
	})
	f.send(key("ctrl+g"), key("s"))
	f.do(func(a *App) {
		_, ok := a.top().(*statusReferenceScreen)
		require.True(t, ok)
		assert.Contains(t, strings.Join(strings.Fields(plain(a)), " "), summary, "the whole summary remains readable")
	})
	f.send(key("esc"), key("esc"))
	f.finish()
}
