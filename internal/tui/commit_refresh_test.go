// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func refreshCommitApp(t *testing.T) (*App, *commitScreen) {
	t.Helper()
	gitEnv(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	writeFile(t, dir, "spec.toml", "name = \"demo\"\n")
	writeFile(t, dir, "demo.spec.md", demoSpec)
	writeFile(t, dir, ".gitignore", ".spec/\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "above zero", "above one", 1))
	a := newTestApp(t, testOptions(dir))
	// Commands remain unexecuted: no external draft provider is called.
	_ = a.openCommit()
	s, ok := a.top().(*commitScreen)
	require.True(t, ok)
	t.Cleanup(func() {
		if s.cancelDraft != nil {
			s.cancelDraft()
		}
	})
	return a, s
}

func TestCommitRefreshInvalidatesGeneratedDraftAndLateReply(t *testing.T) {
	a, s := refreshCommitApp(t)
	oldSeq := s.draftSeq
	cancelled := false
	if s.cancelDraft != nil {
		s.cancelDraft()
	}
	s.cancelDraft = func() { cancelled = true }
	s.draft, s.draftNote = "old generated draft", "drafted by the model"
	cmd := s.refresh(a)
	require.NotNil(t, cmd, "unchanged selection gets a fresh draft request")
	assert.True(t, cancelled)
	assert.Greater(t, s.draftSeq, oldSeq)
	assert.Empty(t, a.side.state.Message, "generated text must never become manual")
	assert.Empty(t, s.draft)
	s.onDraft(a, draftMsg{screen: s, seq: oldSeq, text: "obsolete reply"})
	assert.Empty(t, s.draft)
	assert.True(t, s.drafting)
	s.onDraft(a, draftMsg{screen: s, seq: s.draftSeq, text: "fresh reply"})
	assert.Equal(t, "fresh reply", s.message(a))
}

func TestCommitRefreshPreservesManualMessageAndExcludesChangedGroup(t *testing.T) {
	a, s := refreshCommitApp(t)
	a.side.state.Message = "my explicit message → café"
	oldSeq := s.draftSeq
	writeFile(t, a.ws.Root, "demo.spec.md", strings.Replace(demoSpec, "above zero", "above two", 1))
	cmd := s.refresh(a)
	assert.Nil(t, cmd, "manual messages never generate another draft")
	assert.Equal(t, "my explicit message → café", a.side.state.Message)
	assert.Empty(t, s.selectedChanges())
	assert.False(t, s.drafting)
	s.onDraft(a, draftMsg{screen: s, seq: oldSeq, text: "obsolete reply"})
	assert.Empty(t, s.draft)
	a.theme.ASCII = true
	frame := ansi.Strip(strings.Join(s.view(a, 120, 25), "\n"))
	assert.Contains(t, frame, "my explicit message → café", "ASCII conversion preserves authored text")
	assert.NotContains(t, frame, "groups included ·")
	assert.NotContains(t, frame, "▶")
}

func TestCommitRefreshChangedSelectionDoesNotKeepGeneratedText(t *testing.T) {
	a, s := refreshCommitApp(t)
	s.draft = "old generated draft"
	writeFile(t, a.ws.Root, "demo.spec.md", strings.Replace(demoSpec, "above zero", "above two", 1))
	assert.Nil(t, s.refresh(a), "changed group excluded, so nothing to draft")
	assert.Empty(t, s.draft)
	assert.Empty(t, s.draftNote)
	assert.Empty(t, a.side.state.Message)
	assert.Empty(t, s.message(a))
}
