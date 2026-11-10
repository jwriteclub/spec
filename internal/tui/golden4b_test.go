// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

// demoProject is the demo project without git (goldens need no baseline).
func demoProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "spec.toml", "name = \"demo\"\n")
	writeFile(t, dir, "demo.spec.md", demoSpec)
	writeFile(t, dir, "other.spec.md", otherSpec)
	return dir
}

// fixedProposals saves two proposals with fixed ids and times: an agent's
// change of $Frame.Timing with two fixes, a note and a question, and an
// older analysis.
func fixedProposals(t *testing.T, dir string) {
	t.Helper()
	proj, err := project.Open(dir)
	require.NoError(t, err)
	loaded, err := proj.Load(nil)
	require.NoError(t, err)
	src := string(loaded.Spec.File("demo.spec.md").Source())
	other := string(loaded.Spec.File("other.spec.md").Source())
	hunk := func(n int, file, src, find, repl, issue, sev, expl, anchor string) finding.Hunk {
		s := strings.Index(src, find)
		require.GreaterOrEqual(t, s, 0, find)
		h := finding.Hunk{N: n, File: file, Anchor: anchor, Issue: issue, Severity: sev, Explanation: expl, Find: find, Replace: repl}
		h.Range.Start, h.Range.End = s, s+len(find)
		return h
	}
	st := proposal.NewStore(dir)
	change := &proposal.Proposal{
		ID: "k3x7a", Created: time.Date(2026, 10, 30, 14, 2, 0, 0, time.UTC), Author: "agent-7", Kind: proposal.KindChange,
		Why:     "players stall on fragments longer than 5 s, so bound the fragment length",
		Summary: "the bound is consistent with $Frame.Order; the prose and the extra queue should follow it",
		Primary: &proposal.Primary{N: 1, Action: "change", Name: "$Frame.Timing", File: "demo.spec.md",
			OldKind: "inv", NewKind: "inv", OldText: timingOld, NewText: timingNew},
		Hunks: []finding.Hunk{
			hunk(2, "demo.spec.md", src, proseFind, proseReplace, "missing_update", "inconsistent", "The prose should mention the new bound so readers see it.", ""),
			hunk(3, "other.spec.md", other, limitFind, limitReplace, "numeric_mismatch", "cosmetic", "Align the queue size with the fragment bound.", "$Extra.Limit"),
		},
		Notes: []finding.Note{{File: "demo.spec.md", Anchor: "$Frame.Order", Issue: "ambiguity", Severity: "cosmetic",
			Explanation: "order of arrival or of media time?", Reason: "no edit proposed"}},
		Questions: []string{"Should the bound apply to the last fragment of a stream too?"},
		Status:    proposal.StatusPending,
		Inference: &proposal.Inference{Provider: "fake", Model: "check-model"},
	}
	workflow.FillBase(change, loaded.Spec)
	require.NoError(t, st.Save(change))
	analysis := &proposal.Proposal{
		ID: "m7qd2", Created: time.Date(2026, 10, 29, 9, 15, 0, 0, time.UTC), Author: "human", Kind: proposal.KindAnalysis,
		Summary: "one queue size disagrees with the design notes",
		Hunks:   []finding.Hunk{hunk(1, "other.spec.md", other, limitFind, limitReplace, "numeric_mismatch", "inconsistent", "8 here, 5 in the notes", "$Extra.Limit")},
		Status:  proposal.StatusPending,
	}
	workflow.FillBase(analysis, loaded.Spec)
	require.NoError(t, st.Save(analysis))
}

// TestGolden4b snapshots the Checks sidebar (step 4e; it replaced the
// proposals list) and the review screen at 100x30. Regenerate with `go
// test ./internal/tui -run TestGolden4b -update`.
func TestGolden4b(t *testing.T) {
	t.Run("sidebar", func(t *testing.T) {
		dir := demoProject(t)
		fixedProposals(t, dir)
		a := runFlow(t, testOptions(dir), key("ctrl+r"), key("down"))
		require.Equal(t, CtxChecks, a.context())
		checkFrame(t, a)
	})
	t.Run("review", func(t *testing.T) {
		dir := demoProject(t)
		fixedProposals(t, dir)
		a := runFlow(t, testOptions(dir), key("ctrl+r"), key("enter"), key("y"))
		r := reviewOf(a)
		require.NotNil(t, r)
		require.Equal(t, "k3x7a", r.p.ID)
		require.Equal(t, hunkAccepted, r.state[2])
		checkFrame(t, a)
		if out := os.Getenv("SPEC_REVIEW_RENDER"); out != "" {
			require.NoError(t, os.WriteFile(out, []byte(plain(a)), 0o644))
		}
	})
}

// TestNewScreensFitWidth measures every step 4b screen, before the frame
// is clipped, at several terminal sizes: no line is wider than the
// terminal and there are no more lines than rows.
func TestNewScreensFitWidth(t *testing.T) {
	dir := demoProject(t)
	fixedProposals(t, dir)
	check := func(t *testing.T, a *App, what string) {
		t.Helper()
		frame, _ := a.compose()
		lines := strings.Split(frame, "\n")
		assert.LessOrEqual(t, len(lines), a.height, "%s at %dx%d: %d lines", what, a.width, a.height, len(lines))
		for k, l := range lines {
			assert.LessOrEqual(t, ansi.StringWidth(l), a.width, "%s at %dx%d: line %d is %d cells", what, a.width, a.height, k, ansi.StringWidth(l))
		}
		for k, l := range a.top().view(a, a.width, a.bodyHeight()) {
			assert.LessOrEqual(t, ansi.StringWidth(l), a.width, "%s view at %dx%d: line %d", what, a.width, a.height, k)
		}
	}
	for _, size := range [][2]int{{100, 30}, {80, 24}, {60, 20}, {40, 16}, {24, 10}} {
		w, h := size[0], size[1]
		a := newTestApp(t, testOptions(dir))
		a.Update(tea.WindowSizeMsg{Width: w, Height: h})

		send(a, key("ctrl+r"))
		check(t, a, "sidebar")
		send(a, key("down"))
		check(t, a, "sidebar, second card")
		send(a, key("up"), key("enter"))
		require.NotNil(t, reviewOf(a))
		check(t, a, "review")
		for range 3 {
			send(a, key("pgdown"))
			check(t, a, "review scrolled")
		}
		send(a, key("e"))
		_, editing := a.top().(*hunkEditScreen)
		require.True(t, editing)
		send(a, typeText(" and a long tail of words that wraps past the edge of a narrow terminal")...)
		check(t, a, "hunk editor")
		send(a, key("esc"), key("R"))
		check(t, a, "reject prompt")
		send(a, key("esc"), key("ctrl+x"), key("esc"))

		// Step 4d: the editor with a proposal shown (the bars and
		// "showing k3x7a"), dimmed help bar entries, and ^F's flash.
		require.NotNil(t, a.shown())
		check(t, a, "editor with a shown proposal")
		send(a, key("ctrl+f"))
		check(t, a, "format flash")
		send(a, key("ctrl+z"))

		// The notice popup and the findings panel on the main screen.
		a.notice("Propose failed", &workflow.Error{Code: "provider_unavailable", Message: "no user config at /home/someone/with/a/long/path/config.toml; fix: create it with a [providers.NAME] table (`spec explain config` has examples)"})
		check(t, a, "notice")
		send(a, key("x"))
		long := &proposal.Proposal{ID: "zz9zz", Summary: strings.Repeat("a long summary ", 10), Author: "human", Kind: proposal.KindAnalysis,
			Hunks: []finding.Hunk{{N: 1, File: "other.spec.md", Issue: "numeric_mismatch", Severity: "inconsistent", Explanation: strings.Repeat("why ", 40), Find: limitFind,
				Replace: strings.Repeat("a long replacement that wraps ", 6)}}}
		a.side.cards = append([]*card{{kind: cardEdit, p: long, key: "h1", hunk: long.Hunks[0]}}, a.side.cards...)
		a.side.sel = 0
		send(a, key("ctrl+r"))
		check(t, a, "sidebar with a long card")
		send(a, key("esc"))

		// The commit view and its message editor.
		head := mdspec.Build([]*mdspec.File{mdspec.ParseFile("demo.spec.md", []byte(demoSpec))})
		cur := mdspec.Build([]*mdspec.File{mdspec.ParseFile("demo.spec.md", []byte(strings.Replace(strings.Replace(demoSpec, "above zero", "of at most 5 s", 1), "for the reader", "for every reader of this long line", 1)))})
		cs := &commitScreen{head: head, cur: cur, changes: workflow.Changes(head, cur), ticked: map[string]bool{}, state: map[string]string{}}
		for _, c := range cs.changes {
			cs.ticked[c.ID] = true
			cs.state[c.ID] = "2 open findings"
		}
		cs.retrail(a)
		a.push(cs)
		check(t, a, "commit view")
		send(a, key("n"), key("c"))
		check(t, a, "commit message editor")
		send(a, key("ctrl+x"), key("ctrl+x"))

		// The completion popup.
		find(a, "the reader.")
		send(a, typeText(" $")...)
		require.NotNil(t, a.comp, "%dx%d", w, h)
		check(t, a, "completion")
	}
}

// TestSaveReviewRender writes the plain review screen for the manager when
// SPEC_REVIEW_RENDER names a file.
func TestSaveReviewRender(t *testing.T) {
	out := os.Getenv("SPEC_REVIEW_RENDER")
	if out == "" {
		t.Skip("SPEC_REVIEW_RENDER is not set")
	}
	b, err := os.ReadFile(filepath.Join("testdata", "TestGolden4b", "review", "plain.golden"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(out, b, 0o644))
}
