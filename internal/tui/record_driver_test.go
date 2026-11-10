// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/fsnote"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/recording"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// cellOf is the screen cell of the first occurrence of text in the plain
// frame lines.
func cellOf(t *testing.T, lines []string, text string) (int, int) {
	t.Helper()
	for y, l := range lines {
		if i := strings.Index(l, text); i >= 0 {
			return ansi.StringWidth(l[:i]), y
		}
	}
	t.Fatalf("%q is not on screen:\n%s", text, strings.Join(lines, "\n"))
	return 0, 0
}

// sessionEvents reads the events written so far.
func sessionEvents(t *testing.T, dir string) []recording.Event {
	t.Helper()
	rec, err := recording.Load(dir)
	require.NoError(t, err)
	return rec.Events
}

// waitEvent waits until an event satisfying ok is written.
func waitEvent(t *testing.T, dir, what string, ok func(recording.Event) bool) recording.Event {
	t.Helper()
	var found recording.Event
	require.Eventually(t, func() bool {
		for _, ev := range sessionEvents(t, dir) {
			if ok(ev) {
				found = ev
				return true
			}
		}
		return false
	}, tui.DriverTimeout, 10*time.Millisecond, "waiting for %s", what)
	return found
}

// oneRecording lets one recording scenario run at a time. Scenarios run in
// parallel, but a recorder hears this process's writes through fsnote's single
// observer and its Quiet is process-wide: `spec ui` has one recorder per
// process, and a test binary must too, or one recorder takes another's own
// writes for an outside process's.
var oneRecording sync.Mutex

// Recorded records the scenario into recDir, as `spec ui --record` does, and
// sets *rs. It holds oneRecording until the scenario's UI has closed.
func Recorded(t *testing.T, recDir string, rs **tui.RecordSession) Opt {
	t.Helper()
	return Tweak(func(o *tui.Options) {
		oneRecording.Lock()
		session, err := tui.OpenRecording(context.Background(), tui.RecordConfig{
			Dir: recDir, Root: o.Root, Workflow: o.Workflow, Git: baseline.Loader{Env: scenes.GitEnv()},
			Target: o.Target.Name, HashToSection: o.HashToSection,
		})
		if err != nil {
			oneRecording.Unlock()
		}
		require.NoError(t, err)
		// Runs after the scenario's own cleanup has closed the UI.
		t.Cleanup(func() {
			session.Recorder.Close() // a test that failed early never closed it
			oneRecording.Unlock()
		})
		*rs = session
		o.Record = session.Recorder
		o.HoverDelay = 20 * time.Millisecond
	})
}

// TestRecordSession records a session through the Driver as `spec ui
// --record` does: an edit escalated through gate, triage and check, a
// hover, a click, the wheel, an agent's proposal and an outside edit, a
// list opened and closed, a save, then exit. The directory holds the
// snapshot, the events (one files event per outside change, none for the
// UI's own writes) and the model replies in canned form.
func TestRecordSession(t *testing.T) {
	recDir := filepath.Join(t.TempDir(), "rec")
	var rs *tui.RecordSession
	finding := scenes.CheckReply("the extra queue should follow", nil,
		scenes.Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch", Severity: "inconsistent",
			Explanation: "5 items", Find: limitFind, Replace: limitReplace})
	s := Open(t, scenes.Demo, At("Frame.Timing"), Cascade(), Watch(),
		Replies(infer.CannedFile{
			// The second gate decision clears the outside edit, which ^R
			// checks.
			Gate:   []infer.CannedEntry{scenes.GateReply(0.1, 0.62, 0.05), scenes.GateReply(0.08, 0.1, 0.05)},
			Triage: []infer.CannedEntry{scenes.Slow(scenes.TriageReply(0.7, 0.2, 0), 50)},
			Check:  []infer.CannedEntry{scenes.Slow(finding, 50)},
		}),
		Recorded(t, recDir, &rs))
	require.NotNil(t, rs)

	// Snapshot and config are written before the UI opens.
	ws, err := recording.ReadTar(filepath.Join(recDir, recording.WorkspaceTar))
	require.NoError(t, err)
	assert.Contains(t, ws, "demo.spec.md")
	assert.Contains(t, ws, "other.spec.md")
	head, err := recording.ReadTar(filepath.Join(recDir, recording.HeadTar))
	require.NoError(t, err)
	assert.Contains(t, head, "demo.spec.md")
	cfg, err := os.ReadFile(filepath.Join(recDir, recording.ConfigFile))
	require.NoError(t, err)
	assert.Contains(t, string(cfg), recording.CannedPathToken)

	// An edit, saved and checked through the three stages.
	s.Replace(aboveZero, atMost5s)
	s.Press("ctrl+p", "y")
	s.WaitFor(tui.Cond{Idle: true, Cards: 1})

	// Hover over a $Time.Media reference.
	x, y := cellOf(t, s.Frame(), "in $Time.Media")
	require.NoError(t, s.d.Send(tea.MouseMotionMsg{X: x + 4, Y: y}))
	hover := waitEvent(t, recDir, "the hover", func(ev recording.Event) bool {
		return ev.Kind == recording.KindMouse && ev.Action == recording.MouseHover
	})
	// Click elsewhere in the editor, then wheel down.
	cx, cy := cellOf(t, s.Frame(), "Checked by: TestOrder")
	require.NoError(t, s.d.Send(tea.MouseClickMsg{X: cx, Y: cy, Button: tea.MouseLeft},
		tea.MouseReleaseMsg{X: cx, Y: cy, Button: tea.MouseLeft}))
	require.NoError(t, s.d.Send(tea.MouseWheelMsg{X: cx, Y: cy, Button: tea.MouseWheelDown}))

	// An agent files a proposal from its own process, and edits a spec
	// file on disk.
	agent := &proposal.Proposal{Author: "claude", Why: "agent change", Kind: "change"}
	fsnote.Quiet(func() { require.NoError(t, proposal.NewStore(s.Dir()).Create(agent)) })
	waitEvent(t, recDir, "the agent's proposal", func(ev recording.Event) bool {
		return ev.Kind == recording.KindFiles && ev.Source == recording.SourceProposals
	})
	outside := strings.Replace(s.ReadFile("other.spec.md"), "8 items", "9 items", 1)
	s.WriteFile("other.spec.md", outside, true)
	waitEvent(t, recDir, "the outside edit", func(ev recording.Event) bool {
		return ev.Kind == recording.KindFiles && ev.Source == recording.SourceWatcher
	})

	// A few keys: the proposals list and back, an edit, a save, exit.
	s.Press("ctrl+r")
	s.Idle()
	s.Press("esc")
	s.Type("ok")
	s.Press("ctrl+o")
	time.Sleep(300 * time.Millisecond)      // the watcher reports the save
	require.NoError(t, s.d.Press("ctrl+x")) // not a step: the frame goes blank
	s.d.Close()
	s.sess.Driver = nil // closed: the cleanup must not wait for it again
	meta, err := rs.Close()
	require.NoError(t, err)

	rec, err := recording.Load(recDir)
	require.NoError(t, err)
	assert.Equal(t, meta, rec.Meta)
	assert.Equal(t, 140, rec.Meta.Cols)
	assert.Equal(t, 4, rec.Meta.ModelCalls)
	assert.Equal(t, len(rec.Events), rec.Meta.Events)
	assert.NotEmpty(t, rec.Meta.GitHead)
	assert.NotEmpty(t, rec.Final)

	// The model replies, as canned entries the canned provider accepts.
	require.NotNil(t, rec.Canned)
	require.Len(t, rec.Canned.Gate, 2)
	assert.InDelta(t, 0.62, rec.Canned.Gate[0].Scores[infer.QuestionContradiction], 1e-9)
	require.Len(t, rec.Canned.Triage, 1)
	require.Len(t, rec.Canned.Check, 1)
	assert.Equal(t, finding.Reply, rec.Canned.Check[0].Reply)
	assert.GreaterOrEqual(t, rec.Canned.Check[0].DelayMS, 50)
	require.Len(t, rec.Requests, 4)
	for _, r := range rec.Requests {
		assert.Equal(t, recording.OutcomeOK, r.Outcome)
		assert.NotEmpty(t, r.Head)
	}

	// The events: keys counted, one files event per outside change.
	var files []recording.Event
	keys, last := 0, 0
	for k, ev := range rec.Events {
		assert.Equal(t, k+1, ev.Seq)
		assert.GreaterOrEqual(t, ev.Keys, last)
		last = ev.Keys
		switch ev.Kind {
		case recording.KindKey:
			keys++
			assert.Equal(t, keys, ev.Keys)
		case recording.KindFiles:
			files = append(files, ev)
		}
	}
	require.Len(t, files, 2, "the UI's saves and its own proposal are not recorded")
	assert.Equal(t, recording.SourceProposals, files[0].Source)
	require.Len(t, files[0].Changes, 1)
	assert.Equal(t, proposal.Dir+"/"+agent.ID+".json", files[0].Changes[0].Path)
	assert.Equal(t, recording.SourceWatcher, files[1].Source)
	require.Len(t, files[1].Changes, 1)
	assert.Equal(t, "other.spec.md", files[1].Changes[0].Path)
	assert.Equal(t, outside, files[1].Changes[0].Content)

	require.NotNil(t, hover.Target)
	assert.Equal(t, "anchor", hover.Target.Kind)
	assert.Equal(t, "Time.Media", hover.Target.Name)
	assert.Equal(t, "demo.spec.md", hover.Target.Pos.File)
	var click, wheel *recording.Event
	for k := range rec.Events {
		ev := &rec.Events[k]
		if ev.Mouse != nil && ev.Action == recording.MouseClick {
			click = ev
		}
		if ev.Mouse != nil && ev.Action == recording.MouseWheel {
			wheel = ev
		}
	}
	require.NotNil(t, click)
	require.NotNil(t, click.Result.Cursor)
	assert.Equal(t, "demo.spec.md", click.Result.Cursor.File)
	assert.Equal(t, strings.Count(demoUpTo(t, s, "Checked by: TestOrder"), "\n")+1, click.Result.Cursor.Line)
	require.NotNil(t, wheel)
	assert.NotNil(t, wheel.Result.Scroll)
	end := rec.Events[len(rec.Events)-1]
	assert.Equal(t, recording.KindEnd, end.Kind)
	plain, err := os.ReadFile(filepath.Join(recDir, recording.FinalPlain))
	require.NoError(t, err)
	assert.Equal(t, ansi.Strip(rec.Final), string(plain))
}

// demoUpTo is demo.spec.md on disk up to text.
func demoUpTo(t *testing.T, s *Scenario, text string) string {
	t.Helper()
	src := s.ReadFile("demo.spec.md")
	i := strings.Index(src, text)
	require.GreaterOrEqual(t, i, 0)
	return src[:i]
}
