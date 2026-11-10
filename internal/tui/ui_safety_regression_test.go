// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/proposal"
)

func TestSharedCtrlCCancellationPreservesSurfaceAndPrecedence(t *testing.T) {
	for _, surface := range []string{"help", "settings", "outline", "popup", "list"} {
		t.Run(surface, func(t *testing.T) {
			a := newTestApp(t, testOptions(lensdProject(t)))
			switch surface {
			case "help":
				a.openHelp()
			case "settings":
				a.openHelp()
				a.top().(*referenceScreen).settingsTab(a)
			case "outline":
				a.showOutline = true
				a.outlineFocus = true
			case "popup":
				a.popup = &popup{lines: []string{"read me"}}
			case "list":
				a.list = newList("items", nil, false)
			}
			ctx := a.context()
			require.Equal(t, map[string]Context{"help": CtxHelp, "settings": CtxSettings, "outline": CtxOutline, "popup": CtxPopup, "list": CtxList}[surface], ctx)
			cancelled := false
			a.job = &job{label: "Checking", cancel: func() { cancelled = true }}
			a.onKey(key("ctrl+c"))
			require.True(t, cancelled)
			require.Nil(t, a.job)
			require.Equal(t, ctx, a.context())
		})
	}
	t.Run("wait first", func(t *testing.T) {
		a := newTestApp(t, testOptions(lensdProject(t)))
		a.openHelp()
		waited, jobbed := false, false
		a.wait = &lockWait{cancel: func() { waited = true }, queue: []decision{{label: "apply p1"}}}
		a.job = &job{label: "Checking", cancel: func() { jobbed = true }}
		a.onKey(key("ctrl+c"))
		require.True(t, waited)
		require.False(t, jobbed)
		require.NotNil(t, a.job)
		a.onKey(key("ctrl+c"))
		require.True(t, jobbed)
	})
	t.Run("prompt and notice first", func(t *testing.T) {
		a := newTestApp(t, testOptions(lensdProject(t)))
		cancelled := false
		a.job = &job{label: "Checking", cancel: func() { cancelled = true }}
		a.prompt = &prompt{kind: promptFind, label: "Find"}
		a.onKey(key("ctrl+c"))
		require.Nil(t, a.prompt)
		require.False(t, cancelled)
		a.popup = &popup{notice: true, lines: []string{"failure"}}
		a.onKey(key("ctrl+c"))
		require.Nil(t, a.popup)
		require.False(t, cancelled)
	})
	t.Run("editor selection first", func(t *testing.T) {
		a := newTestApp(t, testOptions(lensdProject(t)))
		send(a, key("ctrl+home"), key("shift+right"))
		cancelled := false
		a.job = &job{label: "Checking", cancel: func() { cancelled = true }}
		a.onKey(key("ctrl+c"))
		require.Equal(t, "#", a.cutBuf)
		require.False(t, cancelled)
		a.onKey(key("ctrl+c"))
		require.True(t, cancelled)
	})
	t.Run("irreversible completion survives help", func(t *testing.T) {
		a := newTestApp(t, testOptions(lensdProject(t)))
		a.openHelp()
		_ = a.startCommitJob(func(context.Context) func(*App) tea.Cmd { return nil })
		seq := a.job.seq
		a.onKey(key("ctrl+c"))
		require.NotNil(t, a.job)
		finished := false
		a.onJobDone(jobDoneMsg{seq: seq, finish: func(*App) tea.Cmd { finished = true; return nil }})
		require.True(t, finished)
	})
}

func TestASCIIGeneratedGlyphsPreserveAuthoredText(t *testing.T) {
	th := NewThemeProfile(true, false, false, true)
	data := "author·name → §3…"
	require.Equal(t, "Agent proposal | "+data, groupOriginTitle(&proposal.Proposal{SourceKind: "agent", Author: data}, th))
	require.Equal(t, data, cardValue(data, 80, th))
	require.Equal(t, "ab...", cardValue("abcdefghi", 5, th))
	require.Equal(t, "|", ansi.Strip(markerCell(th, diag.Severity(""), barProseAdd)))
	a := newTestApp(t, testOptions(lensdProject(t)))
	a.theme = th
	s := newSettingsWithDocuments(config.NewDocument("user.toml", nil, false, nil), config.NewDocument("spec.toml", nil, false, nil))
	s.notice = data
	text := ansi.Strip(strings.Join(s.view(a, 100, 35), "\n"))
	require.Contains(t, text, data)
	require.NotContains(t, text, "config ·")
	require.NotContains(t, text, "Auto ·")
	require.Equal(t, []string{data}, settingsClip([]string{data}, 80))
	a.outline.entries = []outlineEntry{
		{kind: entrySection, depth: 1, text: "1 " + data},
		{kind: entryAnchor, depth: 1, text: "$" + data, anchor: mdspec.KindDefinition},
	}
	rows := ansi.Strip(strings.Join(a.outline.view(a, 80, 2), "\n"))
	require.Contains(t, rows, "S1 "+data)
	require.Contains(t, rows, "o $"+data)
	require.NotContains(t, rows, "◦")
}

func TestHelpCapturesReviewBlockerWithoutDispatch(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	r := &reviewScreen{p: &proposal.Proposal{ID: "p1", Status: proposal.StatusPending, CheckState: "running", CheckMessage: "checking required"}, state: map[int]hunkState{}}
	a.push(r)
	want := r.approveBlocked(a)
	ref := newReferenceScreen(a)
	require.NotEmpty(t, want)
	var text strings.Builder
	for _, g := range ref.groups(100) {
		for _, row := range g.rows {
			text.WriteString(row.text)
			text.WriteByte(' ')
		}
	}
	require.Contains(t, strings.Join(strings.Fields(text.String()), " "), "Unavailable when Help opened: "+want)
	require.Contains(t, strings.Join(strings.Fields(text.String()), " "), "The required check is still running")
	require.Equal(t, "running", r.p.CheckState)
	require.Nil(t, a.job)
	r.p.CheckMessage = "\x1b[31mcontrolled\u202e"
	ref = newReferenceScreen(a)
	for _, g := range ref.groups(100) {
		for _, row := range g.rows {
			require.NotContains(t, row.text, "\x1b")
			require.NotContains(t, row.text, "\u202e")
		}
	}
}

func TestSettingsScopeSavesPreserveSupportedComments(t *testing.T) {
	for _, scope := range []struct {
		name, source string
		validate     func([]byte) []string
		path         []string
		value        any
	}{
		{"User", "# copyright\ndefault='local' # route\n[providers.local] # connection\nmodel='old' # keep\nbase_url='http://localhost/v1'\n", userSettingsValidate, []string{"providers", "local", "model"}, "new"},
		{"Project", "# copyright\nfiles=['*.spec.md'] # files\n", projectSettingsValidate, []string{"files"}, []string{"docs/*.spec.md"}},
	} {
		t.Run(scope.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(p, []byte(scope.source), 0600))
			d := config.OpenDocument(p, scope.validate)
			d.Set(scope.value, scope.path...)
			s := newSettingsWithDocuments(config.NewDocument("", nil, false, nil), config.NewDocument("", nil, false, nil))
			if scope.name == "User" {
				s.user = d
			} else {
				s.project = d
			}
			a := &App{theme: NewTheme(true, false)}
			cmd := s.save(a, scope.name)
			require.NotNil(t, cmd, s.notice)
			require.Equal(t, settingsSavedMsg{scope: scope.name, test: "not tested"}, cmd())
			require.Empty(t, d.Changes())
			b, err := os.ReadFile(p)
			require.NoError(t, err)
			require.Contains(t, string(b), "# copyright")
			require.Empty(t, scope.validate(b))
			if scope.name == "User" {
				require.Contains(t, string(b), "# route")
				require.Contains(t, string(b), "# connection")
				require.Contains(t, string(b), "# keep")
			} else {
				require.Contains(t, string(b), "# files")
			}
		})
	}
}

func TestSettingsPathUsesDisplayCellsForFallback(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		th := NewThemeProfile(true, false, false, ascii)
		for _, path := range []string{"/long/path/to/a/document/name.txt", "/路径路径路径路径路径路径路径路径路径路径.txt"} {
			for _, width := range []int{8, 12, 20} {
				require.LessOrEqual(t, ansi.StringWidth(settingsPath(path, width, th)), width)
			}
		}
	}
}

func TestDisabledKeyPreservesHoverSelectionAndReceipt(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	send(a, key("ctrl+home"), key("shift+right"))
	before, selected := a.cur.Ed.Selection()
	require.True(t, selected)
	tip := &statusTooltip{kind: 1}
	a.statusTip = tip
	a.sideHoverID = "hovered"
	landed := &landing{name: "applied"}
	a.appliedAt = landed
	a.keepFlash = true
	a.flash = "Applied receipt"
	a.onKey(key("ctrl+u")) // Empty cut buffer: Paste is gray.
	after, selected := a.cur.Ed.Selection()
	require.True(t, selected)
	require.Equal(t, before, after)
	require.Same(t, tip, a.statusTip)
	require.Equal(t, "hovered", a.sideHoverID)
	require.Same(t, landed, a.appliedAt)
	require.True(t, a.keepFlash)
	require.Equal(t, "Applied receipt", a.flash)
}
