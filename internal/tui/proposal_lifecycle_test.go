// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

func TestRestoredHumanPrimaryRemainsWholeProposalUntilApplied(t *testing.T) {
	for _, children := range []bool{false, true} {
		t.Run(map[bool]string{false: "primary only", true: "with children"}[children], func(t *testing.T) {
			dir := demoRepo(t)
			reply := modelJSON(t, map[string]any{"summary": "clean", "findings": []any{}, "questions": []string{}})
			if children {
				reply = checkReply(t)
			}
			m := newFakeModel(t, reply)
			wf := testWorkflow(t, m)
			writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, timingOld, timingNew, 1))
			proj, err := project.Open(dir)
			require.NoError(t, err)
			res, e := wf.Propose(context.Background(), proj, workflow.ProposeRequest{SourceKind: "human", Author: "Chris", Why: "bound fragment"})
			require.Nil(t, e)
			require.Len(t, res.Proposals, 1)
			p := res.Proposals[0]
			assert.False(t, p.Primary.InWorktree)
			assert.Equal(t, demoSpec, readFile(t, dir, "demo.spec.md"))
			a := newTestApp(t, uiOptions(dir, wf))
			a.loadPending()
			require.NotEmpty(t, a.side.cards)
			assert.Equal(t, cardProposal, a.side.cards[0].kind, "restored primary is reviewed as a whole proposal")
			a.side.state.Decided = map[string]map[string]string{p.ID: {}}
			assert.Empty(t, a.decidedProposals(), "child-only housekeeping cannot settle missing human primary")
			a.openReview(p.ID)
			r := reviewOf(a)
			require.NotNil(t, r)
			assert.Equal(t, hunkAccepted, r.state[1])
			send(a, key("A"))
			stored, err := proposal.NewStore(dir).Load(p.ID)
			require.NoError(t, err)
			assert.Equal(t, proposal.StatusPending, stored.Status, "not recorded until the save")
			assert.Contains(t, string(a.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()), timingNew)
			assert.Equal(t, demoSpec, readFile(t, dir, "demo.spec.md"), "buffer approval remains unsaved")
			saveBuffers(t, a)
			stored, err = proposal.NewStore(dir).Load(p.ID)
			require.NoError(t, err)
			assert.Equal(t, proposal.StatusApplied, stored.Status)
			assert.Contains(t, stored.Decision.Applied, 1)
		})
	}
}

func TestRecheckUsesUnsavedBuffersAndResetsIdenticalChildSelections(t *testing.T) {
	dir := demoRepo(t)
	m := newFakeModel(t, checkReply(t))
	wf := testWorkflow(t, m)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, timingOld, timingNew, 1))
	proj, err := project.Open(dir)
	require.NoError(t, err)
	res, e := wf.Propose(context.Background(), proj, workflow.ProposeRequest{Names: []string{"Frame.Timing"}, SourceKind: "agent", Why: "bound fragment"})
	require.Nil(t, e)
	require.Len(t, res.Proposals, 1)
	p := res.Proposals[0]
	f := startFlow(t, uiOptions(dir, wf))
	f.do(func(a *App) {
		a.openReview(p.ID)
		r := reviewOf(a)
		r.state[2] = hunkAccepted
		r.state[3] = hunkSkipped
		a.cur.Ed.ReplaceAll(demoSpec+"\nUnsaved context for fresh check.\n", 0)
		a.ws.Reparse()
	})
	f.send(key("r"))
	f.until("fresh check", func(a *App) bool {
		return a.job == nil && reviewOf(a) != nil && reviewOf(a).p.CheckGeneration > p.CheckGeneration
	})
	f.do(func(a *App) {
		r := reviewOf(a)
		assert.Equal(t, hunkUndecided, r.state[2], "unchanged text still needs a fresh generation decision")
		assert.Equal(t, hunkUndecided, r.state[3])
		assert.Empty(t, r.approveBlocked(a))
	})
	requests := m.requests()
	require.Len(t, requests, 2)
	assert.Contains(t, requests[1], "Unsaved context for fresh check")
	f.send(key("A"))
	f.until("applied", func(a *App) bool { return reviewOf(a) == nil })
	f.do(func(a *App) {
		assert.Contains(t, string(a.cur.Ed.Buffer().Bytes()), timingNew)
		assert.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "Unsaved context for fresh check")
	})
	assert.Equal(t, demoSpec, readFile(t, dir, "demo.spec.md"))
	f.finish()
}

func TestReviewShowsPersistentStaleBlockerWhileDisabledApplyIsInert(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	loaded, err := project.Load(dir, nil)
	require.NoError(t, err)
	p.CheckState = "complete"
	p.CheckFiles = map[string]string{}
	for _, file := range loaded.Spec.Files {
		p.CheckFiles[file.Path] = proposal.FileHash(file.Source())
	}
	require.NoError(t, proposal.NewStore(dir).Save(p))
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.openReview(p.ID)
	a.cur.Ed.ReplaceAll(demoSpec+"\nSurrounding context changed.\n", 0)
	a.ws.Reparse()
	a.setFlash("Existing receipt")
	before := string(a.cur.Ed.Buffer().Bytes())
	send(a, key("A"))
	assert.Equal(t, "Existing receipt", a.flash)
	assert.Equal(t, before, string(a.cur.Ed.Buffer().Bytes()))
	r := reviewOf(a)
	require.NotNil(t, r)
	assert.Contains(t, r.approveBlocked(a), "recheck")
	r.scroll = 100
	assert.Contains(t, strings.Join(r.view(a, 160, 12), "\n"), "Apply unavailable: Source files changed")
}

func TestProvenanceHousekeepingHonorsExplicitSource(t *testing.T) {
	for _, source := range []string{"agent", "unknown", "human", ""} {
		t.Run(source, func(t *testing.T) {
			dir := demoRepo(t)
			p := agentProposal(t, dir, false)
			p.SourceKind = source
			p.Author = proposal.AuthorHuman
			p.Primary.InWorktree = true
			require.NoError(t, proposal.NewStore(dir).Save(p))
			a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
			a.loadPending()
			next := *p
			next.ID = "other"
			_ = a.supersede(&next)
			stored, err := proposal.NewStore(dir).Load(p.ID)
			require.NoError(t, err)
			if source == "human" || source == "" {
				assert.Equal(t, proposal.StatusRejected, stored.Status)
			} else {
				assert.Equal(t, proposal.StatusPending, stored.Status)
			}
		})
	}
}

func TestProposalWatcherRecoversKilledWorkerWithoutFileEvent(t *testing.T) {
	if root := os.Getenv("LOGEDIT_UI_CHECK_WORKER"); root != "" {
		token := strings.Repeat("a", 32)
		owner, err := project.AcquireLock(filepath.Join(root, ".spec", "checks", "owners", token), "test check worker")
		if err != nil {
			os.Exit(20)
		}
		defer owner.Release()
		st := proposal.NewStore(root)
		p, err := st.Load(os.Getenv("LOGEDIT_UI_CHECK_ID"))
		if err != nil {
			os.Exit(21)
		}
		p.CheckOwner, p.CheckState = token, "running"
		if st.Save(p) != nil {
			os.Exit(22)
		}
		if os.WriteFile(filepath.Join(root, "worker-ready"), []byte("ready"), 0o600) != nil {
			os.Exit(23)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	loaded, err := project.Load(dir, nil)
	require.NoError(t, err)
	p.CheckFiles = map[string]string{}
	for _, file := range loaded.Spec.Files {
		p.CheckFiles[file.Path] = proposal.FileHash(file.Source())
	}
	require.NoError(t, proposal.NewStore(dir).Save(p))
	cmd := exec.Command(os.Args[0], "-test.run=^TestProposalWatcherRecoversKilledWorkerWithoutFileEvent$")
	cmd.Env = append(os.Environ(), "LOGEDIT_UI_CHECK_WORKER="+dir, "LOGEDIT_UI_CHECK_ID="+p.ID)
	require.NoError(t, cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "worker-ready")); return err == nil }, 5*time.Second, 10*time.Millisecond)
	opts := uiOptions(dir, testWorkflow(t, nil))
	opts.WatchProposals = true
	f := startFlow(t, opts)
	f.until("live worker spinner", func(a *App) bool { return a.propW != nil && a.checksInFlight() && a.proposalSpinning })
	f.do(func(a *App) { a.openReview(p.ID); assert.NotEmpty(t, reviewOf(a).retryBlocked(a)) })
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
	f.until("interruption discovered by poll", func(a *App) bool {
		return len(a.pending) == 1 && a.pending[0].CheckState == "failed" && !a.proposalSpinning
	})
	f.do(func(a *App) {
		assert.Contains(t, reviewOf(a).p.CheckMessage, "interrupted")
		assert.Empty(t, reviewOf(a).retryBlocked(a))
	})
	f.finish()
}

func TestSidebarAppliedReceiptOmitsEmptyAuthorClause(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	p.Author = ""
	require.NoError(t, proposal.NewStore(dir).Save(p))
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.loadPending()
	require.NotEmpty(t, a.side.cards)
	a.acceptCard(a.side.cards[0])
	assert.Contains(t, a.flash, p.ID+" into the buffer")
	assert.NotContains(t, a.flash, " by ")
	saveBuffers(t, a)
	stored, err := proposal.NewStore(dir).Load(p.ID)
	require.NoError(t, err)
	assert.Empty(t, stored.Author)
	assert.Equal(t, proposal.StatusApplied, stored.Status)
}

func TestRecheckGenerationInvalidatesPersistedChildChoicesAfterRestart(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unversioned legacy decisions", true: "recorded generation"}[known], func(t *testing.T) {
			dir := demoRepo(t)
			p := agentProposal(t, dir, false)
			p.CheckGeneration = 1
			require.NoError(t, proposal.NewStore(dir).Save(p))
			a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
			a.side.state.Decided = map[string]map[string]string{p.ID: {"h2": decDismissed, "q0": decDismissed}}
			if !known {
				a.side.state.Generations = nil
			}
			a.saveChecks()
			a.Close()
			p.CheckGeneration++
			require.NoError(t, proposal.NewStore(dir).Save(p))
			restarted := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
			defer restarted.Close()
			assert.Empty(t, restarted.side.state.Decided[p.ID])
			assert.Equal(t, p.CheckGeneration, restarted.side.state.Generations[p.ID])
			assert.Contains(t, restarted.proposalHunksToApply(p), 2, "old dismissal cannot suppress a new-generation edit")
		})
	}
}

// A question card shows the whole question, wrapped to the card, selected
// or not (a question cut short cannot be answered), in either glyph set.
func TestQuestionCardShowsWholeQuestion(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		a := newTestApp(t, testOptions(demoProject(t)))
		a.theme.ASCII = ascii
		q := strings.TrimSpace(strings.Repeat("Question context with words ", 20)) + " last?"
		c := &card{kind: cardQuestion, p: &proposal.Proposal{}, text: q}
		for _, sel := range []bool{false, true} {
			rows := a.cardBox(c, sel, 24)
			var words []string
			for _, row := range rows {
				assert.Equal(t, 28, ansi.StringWidth(row))
				words = append(words, strings.Fields(strings.Trim(ansi.Strip(row), " |│┃▌▐"))...)
			}
			got := strings.Join(words, " ")
			assert.Contains(t, got, q, "ascii %v selected %v", ascii, sel)
			assert.NotContains(t, got, "...")
			assert.NotContains(t, got, "…")
		}
	}
}

func TestReviewPersistentBlockerSanitizesExternalCheckMessage(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	p.CheckState = "failed"
	p.CheckMessage = "bad\x1b[31m\nnext"
	a := newTestApp(t, testOptions(dir))
	r := a.newReview(p)
	r.render(a, 160)
	require.Greater(t, len(r.lines), 1)
	header := ansi.Strip(r.lines[1])
	assert.Contains(t, header, "bad^[[31m next")
	assert.NotContains(t, header, "\n")
}

// The two fixed rows must retain the selection as well as the actual blocker.
func TestBlockedReviewHeaderPreservesCountsAtCommonWidths(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		t.Run(fmt.Sprintf("ascii=%t", ascii), func(t *testing.T) {
			dir := demoRepo(t)
			p := agentProposal(t, dir, false)
			p.CheckState = "failed"
			p.CheckMessage = "Recheck this proposal"
			p.Author = strings.Repeat("long attribution ", 20)
			child := p.Hunks[0]
			for n := 3; n <= 20; n++ {
				child.N = n
				p.Hunks = append(p.Hunks, child)
			}
			require.NoError(t, proposal.NewStore(dir).Save(p))
			a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
			a.theme.ASCII = ascii
			a.openReview(p.ID)
			r := reviewOf(a)
			require.NotNil(t, r)
			for n := 2; n <= 20; n++ {
				switch {
				case n <= 13:
					r.state[n] = hunkAccepted
				case n <= 16:
					r.state[n] = hunkUndecided
				default:
					r.state[n] = hunkSkipped
				}
			}
			a.setFlash("Existing receipt")
			before := string(a.cur.Ed.Buffer().Bytes())
			send(a, key("A"))
			assert.Equal(t, "Existing receipt", a.flash)
			assert.Equal(t, before, string(a.cur.Ed.Buffer().Bytes()))
			assert.Nil(t, a.job)
			assert.Same(t, r, reviewOf(a))
			for _, width := range []int{80, 100, 140, 220} {
				r.scroll = 0
				top := r.view(a, width, 12)
				r.scroll = 100
				scrolled := r.view(a, width, 12)
				assert.Equal(t, top[:reviewHeaderRows], scrolled[:reviewHeaderRows])
				header := ansi.Strip(scrolled[0])
				for _, text := range []string{p.ID, "Primary required", "12 selected", "3 undecided", "4 skipped"} {
					assert.Contains(t, header, text, "width %d", width)
				}
				assert.Contains(t, ansi.Strip(scrolled[1]), "Apply unavailable:")
				assert.Contains(t, ansi.Strip(scrolled[1]), p.CheckMessage)
				for _, row := range scrolled {
					assert.LessOrEqual(t, ansi.StringWidth(row), width)
				}
				if ascii {
					for _, char := range header {
						assert.Less(t, char, rune(128), "generated header punctuation is ASCII")
					}
				}
			}
		})
	}
}
