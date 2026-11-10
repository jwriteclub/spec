// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/tui/editor"
)

// Step 4g, the group card (notes/plan-step4g-fix-review.md): every shape
// of findings and fixes, from hand-written replies of the fake model.

// itemCards counts the sidebar's items as the cards counted them before
// step 4g split each finding into a problem and its fix: problems,
// questions and agents' proposals.
func itemCards(a *App) int {
	n := 0
	for _, c := range a.side.cards {
		switch c.kind {
		case cardProblem, cardQuestion, cardProposal:
			n++
		}
	}
	return n
}

// kinds lists the sidebar's card kinds in order.
func kinds(a *App) []cardKind {
	var out []cardKind
	for _, c := range a.side.cards {
		out = append(out, c.kind)
	}
	return out
}

// specRepo is a committed repository holding files.
func specRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	gitEnv(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	writeFile(t, dir, ".gitignore", ".spec/\n")
	for name, text := range files {
		writeFile(t, dir, name, text)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	return dir
}

// checkedEdit starts the UI on dir, replaces old (the first match) with
// new in the editor, runs ^P there (saving first) with the model answering
// reply, and waits for the filed check; the sidebar is then focused.
func checkedEdit(t *testing.T, dir, path, old, new, reply string) *flow {
	t.Helper()
	m := newFakeModel(t, reply)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	if path != "" {
		f.do(func(a *App) { require.NoError(t, a.open(Location{Path: path})) })
	}
	f.findIn(old)
	f.replaceAt(len(old), new)
	f.send(key("ctrl+p"))
	f.do(func(a *App) { require.Equal(t, CtxSaveFirst, a.context()) })
	f.send(key("y"))
	f.until("the check", func(a *App) bool { return a.job == nil && len(a.pending) == 1 })
	f.send(key("ctrl+r"))
	f.do(func(a *App) { require.Equal(t, CtxChecks, a.context()) })
	return f
}

// demoChecked is checkedEdit on the demo repository: $Frame.Timing's
// "above zero" becomes "of at most 5 s".
func demoChecked(t *testing.T, reply string) (*flow, string) {
	t.Helper()
	dir := demoRepo(t)
	return checkedEdit(t, dir, "", "above zero", "of at most 5 s", reply), dir
}

// timedSpec is the demo spec after the edit demoChecked makes.
var timedSpec = strings.Replace(demoSpec, "above zero", "of at most 5 s", 1)

// bufferOf is path's text in the editor (its buffer, when it is open).
func bufferOf(a *App, path string) string {
	return string(a.ws.Source(path))
}

// sideFlat renders the sidebar wide enough that no box line wraps, plain.
func sideFlat(a *App) string {
	return ansi.Strip(strings.Join(a.sideView(240, 200), "\n"))
}

// storedStatus is the status of the only proposal in dir.
func storedStatus(t *testing.T, dir string) (string, []int) {
	t.Helper()
	ps := proposalsIn(t, dir)
	require.Len(t, ps, 1)
	if ps[0].Decision == nil {
		return ps[0].Status, nil
	}
	return ps[0].Status, ps[0].Decision.Applied
}

// fix-none: a problem with no fix and a question. The problem says no fix
// was proposed and what to do; y applies nothing; the problem stays open
// (and counts on its anchor) until n.
func TestFixNone(t *testing.T) {
	f, dir := demoChecked(t, modelJSON(t, map[string]any{
		"summary": "one rule is unclear now",
		"findings": []map[string]any{{"file": "demo.spec.md", "anchor": "$Frame.Order", "issue": "ambiguity", "severity": "inconsistent",
			"explanation": "with fragments bounded, is the order by arrival or by media time?"}},
		"questions": []string{"Should the 5 s bound apply to the last fragment too?"},
	}))
	f.do(func(a *App) {
		assert.Equal(t, []cardKind{cardProblem, cardQuestion}, kinds(a), "no fix card")
		side := sideFlat(a)
		assert.Contains(t, side, "No fix proposed")
		assert.Contains(t, side, "Edit by hand")
		assert.Contains(t, side, "$Frame.Order")
		assert.Contains(t, side, "demo.spec.md:12")
		assert.Contains(t, side, "Should the 5 s bound apply to the last fragment too?")
		assert.NotContains(t, side, "Suggested fix")
		assert.False(t, barEnabled(a, CtxChecks, ActAcceptHunk), "y is dimmed")
	})
	var receipt string
	f.do(func(a *App) { receipt = a.flash })
	f.send(key("y"))
	f.do(func(a *App) {
		assert.Equal(t, receipt, a.flash)
		assert.Equal(t, timedSpec, bufferOf(a, "demo.spec.md"), "nothing applied")
		assert.Equal(t, 2, itemCards(a))
		assert.True(t, a.openCard(a.side.selected()), "still open")
		assert.Contains(t, ansi.Strip(strings.Join(a.outline.view(a, 33, 20), "\n")), "$Frame.Order 1", "counted on its anchor")
	})
	st, _ := storedStatus(t, dir)
	assert.Equal(t, proposal.StatusPending, st)

	f.send(key("down"))
	assert.Contains(t, f.frame(), "Should the 5 s bound apply")
	f.do(func(a *App) { assert.False(t, barEnabled(a, CtxChecks, ActAcceptHunk)) })
	f.send(key("y"))
	f.do(func(a *App) { assert.Equal(t, receipt, a.flash) })
	f.send(key("up"), key("n"), key("n"))
	f.do(func(a *App) { assert.Empty(t, a.side.cards) })
	f.do(func(a *App) { saveBuffers(t, a) })
	f.until("the record", func(a *App) bool { return len(a.pending) == 0 })
	st, _ = storedStatus(t, dir)
	assert.Equal(t, proposal.StatusApplied, st, "settled: the check's own change, no fix")
	f.finish()
}

// fix-one-resolves-two: two problems name the same fix. They stack, the
// fix shows once (with its own finding's words), and accepting it settles
// both.
func TestFixOneResolvesTwo(t *testing.T) {
	f, dir := demoChecked(t, modelJSON(t, map[string]any{
		"summary": "the prose does not mention the bound",
		"findings": []map[string]any{
			{"file": "demo.spec.md", "anchor": "$Frame.Timing", "issue": "numeric_mismatch", "severity": "breaking",
				"explanation": "fragments are bounded to 5 s now, but the prose implies no bound", "fixed_by": []int{2}},
			{"file": "demo.spec.md", "anchor": "$Frame.Order", "issue": "stale_reference", "severity": "inconsistent",
				"explanation": "readers of $Frame.Order are pointed to prose that misses the bound", "fixed_by": []int{2}},
			{"file": "demo.spec.md", "issue": "missing_update", "severity": "inconsistent", "explanation": "mention the bound in the prose",
				"find": proseFind, "replace": proseReplace},
		},
		"questions": []string{},
	}))
	f.do(func(a *App) {
		assert.Equal(t, []cardKind{cardProblem, cardProblem, cardEdit}, kinds(a), "two problems, one fix, no group card")
		side := sideFlat(a)
		assert.Contains(t, side, "1 fix resolves 2 problems")
		assert.Equal(t, 1, strings.Count(side, "Suggested fix"), "the fix once")
		assert.Contains(t, side, "mention the bound in the prose", "the fix's own finding")
		assert.Contains(t, side, "fragments are bounded to 5 s now")
		assert.Contains(t, side, "readers of $Frame.Order")
		assert.Equal(t, a.side.cards[0].cl, a.side.cards[2].cl, "one cluster")
	})
	f.send(key("down"), key("down"))
	f.do(func(a *App) { require.Equal(t, cardEdit, a.side.selected().kind) })
	f.send(key("y"))
	f.do(func(a *App) {
		assert.Equal(t, strings.Replace(timedSpec, proseFind, proseReplace, 1), bufferOf(a, "demo.spec.md"))
		assert.Empty(t, a.side.cards, "both problems settled")
	})
	f.do(func(a *App) { saveBuffers(t, a) })
	f.until("the record", func(a *App) bool { return len(a.pending) == 0 })
	st, applied := storedStatus(t, dir)
	assert.Equal(t, proposal.StatusApplied, st)
	assert.Len(t, applied, 2, "the check's own change and the fix")
	f.finish()
}

// twoForOne is a contradiction that needs two fixes, each a place to edit.
func twoForOne(t *testing.T) string {
	return modelJSON(t, map[string]any{
		"summary": "the bound needs the prose and the queue to follow",
		"findings": []map[string]any{
			{"file": "demo.spec.md", "anchor": "$Frame.Timing", "issue": "contradiction", "severity": "breaking",
				"explanation": "a 5 s bound contradicts the prose and the queue size", "fixed_by": []int{1, 2}},
			{"file": "demo.spec.md", "issue": "missing_update", "severity": "inconsistent", "explanation": "mention the bound",
				"find": proseFind, "replace": proseReplace},
			{"file": "other.spec.md", "anchor": "$Extra.Limit", "issue": "numeric_mismatch", "severity": "cosmetic",
				"explanation": "align the queue size", "find": limitFind, "replace": limitReplace},
		},
		"questions": []string{},
	})
}

// fix-two-for-one: one problem, two fixes under it. Accepting one leaves
// the problem open until the other is decided; y on the check's title
// accepts both.
func TestFixTwoForOne(t *testing.T) {
	t.Run("one by one", func(t *testing.T) {
		f, dir := demoChecked(t, twoForOne(t))
		f.do(func(a *App) {
			assert.Equal(t, []cardKind{cardGroup, cardProblem, cardEdit, cardEdit}, kinds(a))
			assert.Equal(t, cardProblem, a.side.selected().kind, "the problem is selected, not the group")
			assert.Contains(t, sideFlat(a), "2 fixes for this problem")
		})
		f.send(key("down"), key("y"))
		f.do(func(a *App) {
			assert.Equal(t, strings.Replace(timedSpec, proseFind, proseReplace, 1), bufferOf(a, "demo.spec.md"))
			require.Len(t, a.side.cards, 4, "the cluster stays")
			p := a.side.cards[1]
			st, open := a.problemState(p.p, p.prob)
			assert.Equal(t, probOpen, st)
			assert.Equal(t, 1, open)
			side := sideFlat(a)
			assert.Contains(t, side, "1 of 2 fixes decided")
			assert.Contains(t, side, "✓ accepted")
			assert.Equal(t, 3, a.side.sel, "on to the other fix")
		})
		st, _ := storedStatus(t, dir)
		assert.Equal(t, proposal.StatusPending, st, "open until the other fix is decided")
		f.send(key("n"))
		f.do(func(a *App) {
			assert.Empty(t, a.side.cards, "decided: one accepted, one skipped")
			assert.Equal(t, otherSpec, bufferOf(a, "other.spec.md"), "the skipped fix is not applied")
		})
		f.do(func(a *App) { saveBuffers(t, a) })
		f.until("the record", func(a *App) bool { return len(a.pending) == 0 })
		st, applied := storedStatus(t, dir)
		assert.Equal(t, proposal.StatusApplied, st)
		assert.Len(t, applied, 2, "the change and the accepted fix")
		f.finish()
	})
	t.Run("accept all", func(t *testing.T) {
		f, _ := demoChecked(t, twoForOne(t))
		f.send(key("up"))
		f.do(func(a *App) {
			require.Equal(t, cardGroup, a.side.selected().kind)
			assert.NotContains(t, sideFlat(a), "y accepts all")
			assert.Contains(t, barState(t, a, CtxChecks), "y Accept All")
		})
		f.send(key("y"))
		f.do(func(a *App) {
			assert.Equal(t, strings.Replace(timedSpec, proseFind, proseReplace, 1), bufferOf(a, "demo.spec.md"))
			assert.Equal(t, strings.Replace(otherSpec, limitFind, limitReplace, 1), bufferOf(a, "other.spec.md"))
			assert.Empty(t, a.side.cards)
			assert.Contains(t, a.flash, "Applied · unsaved · 2 fixes to 2 files")
		})
		f.finish()
	})
}

// segSpec cites the segment length in three other places.
const segSpec = `# seg

## 1. Segments

**$Seg.Length (segment length).** Every segment MUST be 4 s long. Checked by: TestLength.

**$Seg.Window (live window).** The live window MUST hold 3 segments, 12 s in all (3 × 4 s). Checked by:
TestWindow.

**$Seg.Buffer (player buffer).** A player MUST buffer at least one 4 s segment before it starts. Checked
by: TestBuffer.

Prose: a 4 s segment is about 100 frames at 25 fps.
`

// fix-propagate: a value cited in three places; ^P after the edit yields
// one group with a fix per place, and accepting the group leaves no
// mention of the old value.
func TestFixPropagate(t *testing.T) {
	dir := specRepo(t, map[string]string{"spec.toml": "name = \"seg\"\n", "seg.spec.md": segSpec})
	place := func(anchor, find, replace string) map[string]any {
		return map[string]any{"file": "seg.spec.md", "anchor": anchor, "issue": "missing_update", "severity": "inconsistent",
			"explanation": "still cites 4 s segments", "find": find, "replace": replace}
	}
	reply := modelJSON(t, map[string]any{
		"summary": "three places cite the old length",
		"findings": []map[string]any{
			place("$Seg.Window", "12 s in all (3 × 4 s)", "18 s in all (3 × 6 s)"),
			place("$Seg.Buffer", "one 4 s segment", "one 6 s segment"),
			place("", "a 4 s segment is about 100 frames", "a 6 s segment is about 150 frames"),
		},
		"questions": []string{},
	})
	f := checkedEdit(t, dir, "seg.spec.md", "4 s long", "6 s long", reply)
	f.do(func(a *App) {
		assert.Equal(t, []cardKind{cardGroup, cardProblem, cardEdit, cardProblem, cardEdit, cardProblem, cardEdit}, kinds(a), "one group")
		assert.Equal(t, 3, strings.Count(sideFlat(a), "Suggested fix"))
	})
	f.send(key("up"), key("y"))
	f.do(func(a *App) {
		buf := bufferOf(a, "seg.spec.md")
		assert.NotContains(t, buf, "4 s", "no mention of the old value is left")
		assert.Contains(t, buf, "18 s in all (3 × 6 s)")
		assert.Empty(t, a.side.cards)
		assert.Contains(t, a.flash, "Applied · unsaved · 3 fixes to 1 file")
	})
	f.do(func(a *App) { saveBuffers(t, a) })
	f.until("the record", func(a *App) bool { return len(a.pending) == 0 })
	st, applied := storedStatus(t, dir)
	assert.Equal(t, proposal.StatusApplied, st)
	assert.Len(t, applied, 4)
	f.finish()
}

// fix-invalid-link: fixed_by naming the finding itself, an index out of
// range and a finding without an edit: the links are dropped and every
// finding stands alone.
func TestFixInvalidLink(t *testing.T) {
	f, _ := demoChecked(t, modelJSON(t, map[string]any{
		"summary": "links gone wrong",
		"findings": []map[string]any{
			{"file": "demo.spec.md", "anchor": "$Frame.Order", "issue": "ambiguity", "severity": "cosmetic", "explanation": "self", "fixed_by": []int{0}},
			{"file": "demo.spec.md", "issue": "missing_update", "severity": "inconsistent", "explanation": "out of range",
				"find": proseFind, "replace": proseReplace, "fixed_by": []int{7}},
			{"file": "demo.spec.md", "anchor": "$Frame.Order", "issue": "stale_reference", "severity": "cosmetic", "explanation": "to a note", "fixed_by": []any{3, nil, "x"}},
			{"file": "other.spec.md", "anchor": "$Extra.Limit", "issue": "ambiguity", "severity": "cosmetic", "explanation": "no edit"},
		},
		"questions": []string{},
	}))
	f.do(func(a *App) {
		assert.Equal(t, []cardKind{cardProblem, cardEdit, cardProblem, cardProblem, cardProblem}, kinds(a))
		side := sideFlat(a)
		assert.NotContains(t, side, "resolve")
		assert.NotContains(t, side, "for this problem")
		assert.Equal(t, 3, strings.Count(side, "No fix proposed"))
	})
	f.finish()

	// A stored proposal with links no reply could make: to a hunk that
	// does not exist, to itself, to the change's own hunk.
	p := &proposal.Proposal{ID: "bad", Primary: &proposal.Primary{N: 1, Name: "$Frame.Timing", InWorktree: true},
		Hunks: []finding.Hunk{{N: 1, Find: "x"}, {N: 2, Find: "a", Replace: "b", FixedBy: []int{2, 99, 1}}, {N: 3, Find: "c", Replace: "d", FixedBy: []int{2, 2}}},
		Notes: []finding.Note{{Explanation: "n", FixedBy: []int{5, 1}}}}
	cls := clustersOf(p)
	require.Len(t, cls, 2)
	assert.Equal(t, []int{2, 3}, cls[0].fixes)
	require.Len(t, cls[0].problems, 1, "hunk 2's links (99, itself, the change's hunk) are dropped and hunk 3 names it: a fix only")
	assert.Equal(t, "p3", cls[0].problems[0].key)
	assert.Equal(t, []int{3, 2}, cls[0].problems[0].fixes, "a repeat dropped")
	assert.Empty(t, cls[1].problems[0].fixes, "the note's links name no fix")
}

// fix-done-by-hand: Chris makes the fix's change himself. The reparse
// after the edit records it as done by hand, with no key: one flash, the
// problem resolved by hand, the group gone and the check recorded,
// nothing applied twice. Made differently, the fix no longer applies: n
// skips it and the problem stays open until n dismisses it.
func TestFixDoneByHand(t *testing.T) {
	reply := modelJSON(t, map[string]any{
		"summary": "the prose should mention the bound",
		"findings": []map[string]any{{"file": "demo.spec.md", "issue": "missing_update", "severity": "inconsistent",
			"explanation": "mention the bound", "find": proseFind, "replace": proseReplace}},
		"questions": []string{},
	})
	t.Run("the same change", func(t *testing.T) {
		f, dir := demoChecked(t, reply)
		var p *proposal.Proposal
		f.send(key("esc"))
		f.do(func(a *App) {
			p = a.side.cards[1].p
			assert.Equal(t, fixPending, a.fixState(p, a.side.cards[1].hunk))
		})
		f.findIn("for the reader")
		f.typeIn("and the 5 s bound ")
		want := strings.Replace(timedSpec, proseFind, proseReplace, 1)
		f.do(func(a *App) {
			require.Equal(t, want, bufferOf(a, "demo.spec.md"))
			assert.Equal(t, CtxEditor, a.context(), "no key, no focus change")
			assert.Equal(t, "Done by hand: $Frame.Timing fix recorded", a.flash)
			assert.Empty(t, a.side.cards, "the group settled")
			assert.Empty(t, a.pending, "settled, recorded by the save")
			saveBuffers(t, a)
		})
		st, applied := storedStatus(t, dir)
		assert.Equal(t, proposal.StatusApplied, st)
		assert.Len(t, applied, 1, "only the check's own change: the fix was made by hand")

		// Once: typing preserves the timed receipt and nothing records again.
		// Changing the text away again leaves the record (^P rechecks).
		f.typeIn("x")
		f.do(func(a *App) {
			assert.Equal(t, "Done by hand: $Frame.Timing fix recorded", a.flash)
			assert.Equal(t, strings.Replace(want, "bound for", "bound xfor", 1), bufferOf(a, "demo.spec.md"))
			assert.Empty(t, a.side.cards)
		})
		f.finish()
	})
	t.Run("a different change", func(t *testing.T) {
		f, _ := demoChecked(t, reply)
		f.send(key("esc"))
		f.findIn("for the reader")
		f.typeIn("and its bound ")
		f.do(func(a *App) {
			fix := a.side.cards[1]
			assert.Equal(t, fixGone, a.fixState(fix.p, fix.hunk))
			assert.Contains(t, sideFlat(a), "No longer applies")
		})
		f.send(key("ctrl+r"), key("down"), key("n"))
		f.do(func(a *App) {
			require.Len(t, a.side.cards, 2, "skipped: the problem stays open")
			assert.Contains(t, sideFlat(a), "Fix skipped")
		})
		f.send(key("up"), key("n"))
		f.do(func(a *App) { assert.Empty(t, a.side.cards) })
		f.finish()
	})
}

// handEdit types text where at is found, right characters on.
type handEdit struct {
	at    string
	right int
	text  string
}

// fix-done-by-hand, in doubt: the text holds the fix's replacement, but
// not only at the fix's place. Nothing is recorded on its own; the fix
// says why and y records it.
func TestFixDoneByHandUnsure(t *testing.T) {
	finding := func(anchor, find, replace string) string {
		return modelJSON(t, map[string]any{
			"summary": "the prose should mention the bound",
			"findings": []map[string]any{{"file": "demo.spec.md", "anchor": anchor, "issue": "missing_update",
				"severity": "inconsistent", "explanation": "mention the bound", "find": find, "replace": replace}},
			"questions": []string{},
		})
	}
	cases := []struct {
		name, reply string
		// edits type each text where its find lands, moved right.
		edits []handEdit
		doubt string
	}{
		{"the text it replaces is still there", finding("", "for the reader.", "and the 5 s bound for the reader."),
			[]handEdit{{"reader.", 7, " Read for the reader."}, {"Order for the reader", 6, "and the 5 s bound "}},
			"unsure: the text it replaces is still in demo.spec.md"},
		{"the replacement twice", finding("", "for the reader.", "and the 5 s bound for the reader."),
			[]handEdit{{"reader.", 7, " and the 5 s bound for the reader."}, {"Order for the reader", 6, "and the 5 s bound "}},
			"unsure: its text occurs 2 times in demo.spec.md"},
		{"not in the fix's anchor", finding("$Frame.Order", proseFind, proseReplace),
			[]handEdit{{"for the reader", 0, "and the 5 s bound "}},
			"unsure: it is not in $Frame.Order"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, dir := demoChecked(t, tc.reply)
			f.send(key("esc"))
			for _, e := range tc.edits {
				f.findIn(e.at)
				for range e.right {
					f.send(key("right"))
				}
				f.typeIn(e.text)
			}
			f.do(func(a *App) {
				fix := a.side.cards[1]
				require.Equal(t, cardEdit, fix.kind)
				assert.Equal(t, fixHandUnsure, a.fixState(fix.p, fix.hunk))
				assert.Empty(t, a.decided(fix.p.ID, fix.key), "not recorded on its own")
				assert.NotContains(t, a.flash, "Done by hand")
				prob := a.side.cards[0]
				st, _ := a.problemState(prob.p, prob.prob)
				assert.Equal(t, probOpen, st, "the problem stays open until y records the fix")
				side := sideFlat(a)
				assert.NotContains(t, side, "resolved by hand")
				assert.Contains(t, side, "Done by hand · confirm")
				assert.Contains(t, side, tc.doubt)
			})
			f.send(key("ctrl+r"), key("down"), key("y"))
			f.do(func(a *App) {
				assert.Equal(t, "Already done by hand: recorded, nothing applied", a.flash)
				assert.Empty(t, a.side.cards)
			})
			f.do(func(a *App) { saveBuffers(t, a) })
			f.until("the record", func(a *App) bool { return len(a.pending) == 0 })
			st, applied := storedStatus(t, dir)
			assert.Equal(t, proposal.StatusApplied, st)
			assert.Len(t, applied, 1, "nothing applied by the fix")
			f.finish()
		})
	}
}

// Chris's report (#26): after a commit from the sidebar (^R, A, A), the
// help bar stayed the sidebar's and Esc was needed to type. A commit
// returns the keyboard to the editor, with the editor's help bar; the
// sidebar stays only where it is a column.
func TestCommitReturnsToEditor(t *testing.T) {
	f, dir := demoChecked(t, modelJSON(t, map[string]any{
		"summary": "the prose should mention the bound",
		"findings": []map[string]any{{"file": "demo.spec.md", "issue": "missing_update", "severity": "inconsistent",
			"explanation": "mention the bound", "find": proseFind, "replace": proseReplace}},
		"questions": []string{},
	}))
	f.send(key("y"))
	f.do(func(a *App) {
		require.Empty(t, a.side.cards, "the fix accepted")
		require.Equal(t, CtxChecks, a.context())
		a.side.state.Message = "spec: bound $Frame.Timing to 5 s"
	})
	f.send(key("A"))
	f.do(func(a *App) { require.Equal(t, CtxSaveFirst, a.context()) })
	f.send(key("y"))
	f.do(func(a *App) { require.Equal(t, CtxCommit, a.context()) })
	f.send(key("A"))
	f.until("the commit", func(a *App) bool { return a.job == nil && len(a.screens) == 1 })
	assert.Equal(t, "spec: bound $Frame.Timing to 5 s\n", gitRun(t, dir, "log", "-1", "--format=%s"))
	f.do(func(a *App) {
		assert.Contains(t, a.flash, "Committed ")
		assert.Equal(t, CtxEditor, a.context(), "the editor has the keyboard")
		assert.False(t, a.sideFocus)
		assert.Equal(t, a.sideFits(), a.sideShown, "the sidebar stays only as a column")
		frame := plain(a)
		for _, l := range a.helpBar(CtxEditor, frameW) {
			assert.Contains(t, frame, strings.TrimRight(ansi.Strip(l), " "), "the editor's help bar")
		}
		for _, l := range a.helpBar(CtxChecks, frameW) {
			assert.NotContains(t, frame, strings.TrimRight(ansi.Strip(l), " "), "not the sidebar's")
		}
	})
	f.typeIn("Z")
	f.do(func(a *App) { assert.True(t, a.cur.Ed.Buffer().Dirty(), "typing goes to the text") })
	f.finish()
}

// The TranscoderCap case as Chris saw it: he changed $Hub.TranscoderCap's
// "$Frame.CleanGeneration's 1 s" to 2 s and pressed ^P; the one finding
// carries its fix on $Frame.CleanGeneration. The group is about his edit,
// the problem names both sides, and the teal fix sits at
// CleanGeneration; Enter and a click show it there in place; y applies
// it.
const (
	cleanFind    = "MUST have exited within 1 s"
	cleanReplace = "MUST have exited within 2 s"
	cleanWhy     = "$Hub.TranscoderCap now bounds an exiting generation by $Frame.CleanGeneration's 2 s, but $Frame.CleanGeneration says it MUST have exited within 1 s."
)

// lensdRepo is a committed repository holding the lensd fixture.
func lensdRepo(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../testdata/lensd/lensd.spec.md")
	require.NoError(t, err)
	return specRepo(t, map[string]string{"spec.toml": "name = \"lensd\"\n", "lensd.spec.md": string(b)})
}

// transcoderReply is the model's one finding.
func transcoderReply(t *testing.T) string {
	return modelJSON(t, map[string]any{
		"summary": "$Frame.CleanGeneration still bounds the exit by 1 s",
		"findings": []map[string]any{{"file": "lensd.spec.md", "anchor": "$Frame.CleanGeneration", "issue": "numeric_mismatch",
			"severity": "breaking", "explanation": cleanWhy, "find": cleanFind, "replace": cleanReplace}},
		"questions": []string{},
	})
}

// inlineFix reports whether the editor's rows show the selected fix in
// place: the old word struck, the new one as ghost text.
func inlineFix(a *App, old, new string) bool {
	_ = plain(a) // lays the rows out with the current ghosts
	struck, ghost := false, false
	for _, r := range a.cur.Ed.Screen() {
		for _, run := range r.Runs {
			if run.Flags&editor.FFixOld != 0 && strings.Contains(run.Text, old) {
				struck = true
			}
			if run.Flags&editor.FFixNew != 0 && strings.Contains(run.Text, new) {
				ghost = true
			}
		}
	}
	return struck && ghost
}

// cursorLine is the editor's cursor line.
func cursorLine(a *App) int {
	return a.cur.Ed.Buffer().Index().Line(a.cur.Ed.Cursor())
}

func TestFixTranscoderCap(t *testing.T) {
	dir := lensdRepo(t)
	m := newFakeModel(t, transcoderReply(t))
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	f.findIn("1 s), so a new")
	f.replaceAt(1, "2")
	f.send(key("ctrl+p"), key("y"))
	f.until("the check", func(a *App) bool { return a.job == nil && len(a.pending) == 1 })
	f.send(key("ctrl+r"))
	f.do(func(a *App) {
		require.True(t, a.sideColumn(), "140 columns: a column")
		assert.Equal(t, []cardKind{cardProblem, cardEdit}, kinds(a))
		side := sideFlat(a)
		assert.Contains(t, side, "Your edit")
		assert.Contains(t, side, "$Hub.TranscoderCap")
		assert.Contains(t, side, "breaking numeric_mismatch")
		assert.Contains(t, side, "$Frame.CleanGeneration")
		assert.Contains(t, side, "lensd.spec.md:99")
		assert.Contains(t, side, "Suggested fix")
		assert.Contains(t, side, "within 1 → within 2")
		problem := strings.Index(side, "numeric_mismatch")
		fix := strings.Index(side, "Suggested fix")
		require.Greater(t, fix, problem, "the fix under the problem")
		assert.NotContains(t, side[problem:fix], "+ ", "no diff in the problem box")
	})

	// Enter on the fix: the editor goes there and shows the edit in place;
	// the sidebar keeps it selected.
	f.send(key("down"), key("enter"))
	f.do(func(a *App) {
		assert.Equal(t, CtxEditor, a.context())
		assert.Equal(t, 99, cursorLine(a))
		assert.True(t, inlineFix(a, "1", "→2"), "the proposed edit in place")
		assert.Equal(t, cardEdit, a.side.selected().kind, "still selected")
	})

	// Back in the sidebar, up to the problem, then a click on the fix
	// does the same (and keeps the keyboard in the sidebar).
	f.send(key("ctrl+r"), key("up"))
	f.do(func(a *App) {
		assert.Equal(t, 121, cursorLine(a), "the problem shows his edit")
		assert.False(t, inlineFix(a, "1", "→2"), "only a selected fix shows in place")
	})
	f.do(func(a *App) {
		_ = plain(a)
		x, _ := a.sideRect()
		s := a.side
		a.Update(click(x+4, s.starts[1]+2-s.scroll, 0))
		assert.Equal(t, 1, s.sel)
		assert.Equal(t, CtxChecks, a.context())
		assert.Equal(t, 99, cursorLine(a))
		assert.True(t, inlineFix(a, "1", "→2"))
	})

	f.send(key("y"))
	f.do(func(a *App) {
		assert.Contains(t, bufferOf(a, "lensd.spec.md"), cleanReplace)
		assert.NotContains(t, bufferOf(a, "lensd.spec.md"), cleanFind)
		assert.Empty(t, a.side.cards)
	})
	f.do(func(a *App) { saveBuffers(t, a) })
	f.until("the record", func(a *App) bool { return len(a.pending) == 0 })
	st, applied := storedStatus(t, dir)
	assert.Equal(t, proposal.StatusApplied, st)
	assert.Len(t, applied, 2)
	f.finish()
}

// TestGoldenFixCards renders the TranscoderCap group at 140 and 220
// columns: the problem, and the fix selected (shown in place in the
// editor). Regenerate with -update.
func TestGoldenFixCards(t *testing.T) {
	for _, w := range []int{140, 220} {
		t.Run(map[int]string{140: "w140", 220: "w220"}[w], func(t *testing.T) {
			dir := lensdRepo(t)
			src := readFile(t, dir, "lensd.spec.md")
			writeFile(t, dir, "lensd.spec.md", strings.Replace(src, "CleanGeneration's 1 s), so", "CleanGeneration's 2 s), so", 1))
			a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
			a.ws.Base = a.ws.LoadBaseline(context.Background())
			body, n := a.anchorBody("$Hub.TranscoderCap")
			require.Equal(t, 1, n)
			cur := string(a.ws.Source("lensd.spec.md"))
			at := strings.Index(cur, cleanFind)
			h := finding.Hunk{N: 2, File: "lensd.spec.md", Anchor: "$Frame.CleanGeneration", Issue: "numeric_mismatch", Severity: "breaking",
				Explanation: cleanWhy, Find: cleanFind, Replace: cleanReplace}
			h.Range.Start, h.Range.End = at, at+len(cleanFind)
			p := &proposal.Proposal{
				ID: "3ha7n", Created: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), Author: proposal.AuthorHuman, Kind: proposal.KindChange,
				Status:  proposal.StatusPending,
				Summary: "$Frame.CleanGeneration still bounds the exit by 1 s",
				Primary: &proposal.Primary{N: 1, Action: "change", Name: "$Hub.TranscoderCap", File: "lensd.spec.md", OldKind: "inv", NewKind: "inv",
					OldText: strings.Replace(body, "2 s)", "1 s)", 1), NewText: body, InWorktree: true},
				Hunks: []finding.Hunk{h},
				Inference: &proposal.Inference{Provider: "fake", Model: "check-model", Stages: []proposal.Stage{
					{Name: proposal.StageCheck, Provider: "fake", Model: "check-model", LatencyMS: 8400, Outcome: proposal.OutcomeDone, Findings: 1}}},
			}
			require.NoError(t, proposal.NewStore(dir).Save(p))
			a.loadPending()
			a.Update(tea.WindowSizeMsg{Width: w, Height: 40})
			send(a, key("ctrl+r"), key("down"))
			require.Equal(t, cardEdit, a.side.selected().kind)
			a.flash = ""
			golden.RequireEqual(t, plain(a))
		})
	}
}
