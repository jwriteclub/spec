// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/fsnote"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/recording"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// plainFrame is d's frame without styles.
func plainFrame(t *testing.T, d *tui.Driver) string {
	t.Helper()
	f, err := d.Frame()
	require.NoError(t, err)
	return ansi.Strip(f.Content)
}

// waitFrame waits until d's plain frame is want; it fails with both
// frames after timeout.
func waitFrame(t *testing.T, d *tui.Driver, what, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	got := ""
	for time.Now().Before(deadline) {
		if got = plainFrame(t, d); got == want {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("the replay never showed the recorded %s.\nrecorded:\n%s\nreplayed:\n%s", what, want, got)
}

// TestRecordReplayRoundTrip records a session (an edit escalated through
// gate, triage and check; a hover; a click; the wheel; an agent's proposal
// and an outside edit; the proposals list; a save; exit), turns it into a
// VHS tape, and replays the tape's keys with its pauses into `spec ui
// --replay`'s UI: the replay shows the recorded hover and the outside
// changes at the same points, and ends on the recorded final frame.
func TestRecordReplayRoundTrip(t *testing.T) {
	recDir := filepath.Join(t.TempDir(), "rec")
	var rs *tui.RecordSession
	finding := scenes.CheckReply("the extra queue should follow", nil,
		scenes.Finding{File: "other.spec.md", Anchor: "$Extra.Limit", Issue: "numeric_mismatch", Severity: "inconsistent",
			Explanation: "5 items", Find: limitFind, Replace: limitReplace})
	s := Open(t, scenes.Demo, At("Frame.Timing"), Cascade(), Watch(),
		Replies(infer.CannedFile{
			Gate:   []infer.CannedEntry{scenes.GateReply(0.1, 0.62, 0.05), scenes.GateReply(0.08, 0.1, 0.05)},
			Triage: []infer.CannedEntry{scenes.Slow(scenes.TriageReply(0.7, 0.2, 0), 50)},
			Check:  []infer.CannedEntry{scenes.Slow(finding, 50)},
		}),
		Recorded(t, recDir, &rs))
	require.NotNil(t, rs)
	pause := func(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

	// ---- record ----
	// The edit, by keys VHS can press: find, mark, select right, type.
	s.Find(aboveZero)
	s.Press("ctrl+space")
	for range len(aboveZero) {
		s.Press("right")
		pause(5)
	}
	s.Type(atMost5s)
	s.ExpectBuffer(atMost5s)
	pause(60)
	s.Press("ctrl+p")
	pause(40)
	s.Press("y")
	s.WaitFor(tui.Cond{Idle: true, Cards: 1})
	pause(150)

	// Hover a $Time.Media reference; the popup stays a while.
	x, y := cellOf(t, s.Frame(), "in $Time.Media")
	require.NoError(t, s.d.Send(tea.MouseMotionMsg{X: x + 4, Y: y}))
	waitEvent(t, recDir, "the hover", func(ev recording.Event) bool {
		return ev.Kind == recording.KindMouse && ev.Action == recording.MouseHover
	})
	hoverFrame := plainFrame(t, s.d)
	pause(250)
	// Click elsewhere in the editor (closing the popup), wheel down.
	cx, cy := cellOf(t, s.Frame(), "Checked by: TestOrder")
	require.NoError(t, s.d.Send(tea.MouseClickMsg{X: cx, Y: cy, Button: tea.MouseLeft},
		tea.MouseReleaseMsg{X: cx, Y: cy, Button: tea.MouseLeft}))
	pause(80)
	require.NoError(t, s.d.Send(tea.MouseWheelMsg{X: cx, Y: cy, Button: tea.MouseWheelDown}))
	pause(80)

	// An agent files a proposal from its own process and edits a spec
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
	pause(60)
	filesFrame := plainFrame(t, s.d)
	pause(200)

	// The proposals list and back, an edit, a save, exit.
	s.Press("ctrl+r")
	s.Idle()
	pause(150)
	s.Press("esc")
	pause(60)
	s.Type("ok")
	pause(60)
	s.Press("ctrl+o")
	pause(300) // the watcher reports the save
	require.NoError(t, s.d.Press("ctrl+x"))
	s.d.Close()
	s.sess.Driver = nil
	_, err := rs.Close()
	require.NoError(t, err)

	rec, err := recording.Load(recDir)
	require.NoError(t, err)
	require.NotEmpty(t, rec.Final)
	require.NotNil(t, rec.Canned)
	assert.Len(t, rec.Canned.Gate, 2)
	assert.Len(t, rec.Canned.Triage, 1)
	assert.Len(t, rec.Canned.Check, 1)
	var hover recording.Event
	files := 0
	for _, ev := range rec.Events {
		switch {
		case ev.Kind == recording.KindFiles:
			files++
		case ev.Kind == recording.KindMouse && ev.Action == recording.MouseHover:
			hover = ev
		}
	}
	assert.Equal(t, 2, files, "one files event per outside change, none for the UI's saves")
	require.NotNil(t, hover.Target)
	assert.Equal(t, "Time.Media", hover.Target.Name)
	assert.Contains(t, hoverFrame, hover.Result.Popup)

	// ---- the tape ----
	tape, warns := recording.Tape(rec, recording.TapeOptions{Spec: "spec"})
	assert.Empty(t, warns, "every key has a VHS command")
	steps, err := recording.ParseTape(tape)
	require.NoError(t, err)
	if vhs, err := exec.LookPath("vhs"); err == nil {
		path := filepath.Join(t.TempDir(), "replay.tape")
		require.NoError(t, os.WriteFile(path, []byte(tape), 0o644))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, vhs, "validate", path).CombinedOutput()
		cancel()
		assert.NoError(t, err, "vhs validate:\n%s", out)
	}

	// ---- replay ----
	rp, err := tui.OpenReplay(context.Background(), tui.ReplayConfig{Dir: recDir, TempParent: t.TempDir()})
	require.NoError(t, err)
	opts := rp.Options
	opts.Target = tui.Target{Name: "Frame.Timing"}
	// The recording ran on the scenes' frozen clock.
	opts.Now = func() time.Time { return scenes.Clock }
	rp.Workflow.Now = opts.Now
	d, err := tui.NewDriver(opts, s.w, s.h)
	require.NoError(t, err)
	defer d.Close()
	require.NoError(t, d.Wait(tui.Cond{Baseline: true, Idle: true}))

	keys := 0
	for k, st := range steps {
		switch {
		case st.Sleep > 0:
			if keys != hover.Keys {
				time.Sleep(st.Sleep)
				continue
			}
			// The gap after the hover's key holds the hover, the click,
			// the wheel and the outside changes: look at the frames on
			// the way, then sleep what is left of it.
			began := time.Now()
			waitFrame(t, d, "hover", hoverFrame, 3*time.Second)
			waitFrame(t, d, "outside changes", filesFrame, 3*time.Second)
			time.Sleep(st.Sleep - time.Since(began))
			keys = -1 // looked once
		case st.Key != "":
			require.NoError(t, d.Press(st.Key), "step %d", k)
			if keys >= 0 {
				keys++
			}
		case st.Text != "":
			require.NoError(t, d.Type(st.Text), "step %d", k)
			if keys >= 0 {
				keys += len([]rune(st.Text))
			}
		}
	}
	require.Equal(t, -1, keys, "the hover's key came")

	b, err := os.ReadFile(filepath.Join(rp.Prepared.Root, "other.spec.md"))
	require.NoError(t, err)
	assert.Equal(t, outside, string(b))
	assert.FileExists(t, filepath.Join(rp.Prepared.Root, proposal.Dir, agent.ID+".json"))

	final := rp.Replay.Final()
	require.NotEmpty(t, final, "the replay quit on ^X")
	if final != rec.Final {
		t.Errorf("the replay's final frame differs from the recording's.\nrecorded:\n%s\nreplayed:\n%s",
			ansi.Strip(rec.Final), ansi.Strip(final))
	}
	assert.Empty(t, rp.Warnings(), "nothing diverged:\n%s", strings.Join(rp.Warnings(), "\n"))
	if t.Failed() {
		fmt.Println(tape)
	}
}

// replayOutside records a session in which change changes files from outside,
// quits, and replays the recording: the replay diverges nowhere and ends on the
// recorded final frame. It returns the replayed project's root and the plain
// recorded final frame.
func replayOutside(t *testing.T, change func(s *Scenario, recDir string)) (root, final string) {
	t.Helper()
	return replayOutsideIn(t, scenes.Demo, "Frame.Timing", change)
}

// replayOutsideIn is replayOutside on the fixture fx, opened at the anchor name.
func replayOutsideIn(t *testing.T, fx scenes.Fixture, name string, change func(s *Scenario, recDir string)) (root, final string) {
	t.Helper()
	recDir := filepath.Join(t.TempDir(), "rec")
	var rs *tui.RecordSession
	s := Open(t, fx, At(name), Watch(), Recorded(t, recDir, &rs))
	require.NotNil(t, rs)
	change(s, recDir)
	require.NoError(t, s.d.Press("ctrl+x"))
	s.d.Close()
	s.sess.Driver = nil
	_, err := rs.Close()
	require.NoError(t, err)
	rec, err := recording.Load(recDir)
	require.NoError(t, err)
	require.NotEmpty(t, rec.Final)

	rp, err := tui.OpenReplay(context.Background(), tui.ReplayConfig{Dir: recDir, TempParent: t.TempDir()})
	require.NoError(t, err)
	opts := rp.Options
	opts.Target = tui.Target{Name: name}
	opts.Now = func() time.Time { return scenes.Clock }
	rp.Workflow.Now = opts.Now
	d, err := tui.NewDriver(opts, s.w, s.h)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	require.NoError(t, d.Wait(tui.Cond{Baseline: true, Idle: true}))
	require.NoError(t, d.Press("ctrl+x"))
	require.Eventually(t, func() bool { return rp.Replay.Final() != "" }, tui.DriverTimeout, 5*time.Millisecond)
	assert.Equal(t, ansi.Strip(rec.Final), ansi.Strip(rp.Replay.Final()))
	assert.Empty(t, rp.Warnings(), "nothing diverged:\n%s", strings.Join(rp.Warnings(), "\n"))
	return rp.Prepared.Root, ansi.Strip(rec.Final)
}

// waitChange waits for the watcher's files event changing path.
func waitChange(t *testing.T, recDir, path string, deleted bool) {
	t.Helper()
	waitEvent(t, recDir, path, func(ev recording.Event) bool {
		for _, c := range ev.Changes {
			if ev.Source == recording.SourceWatcher && c.Path == path && c.Deleted == deleted {
				return true
			}
		}
		return false
	})
}

// TestRecordReplayEditsConfigDeletesFile: another process edits spec.toml and
// deletes a spec file during a recording; the watcher's events carry both and
// the replay's project ends the same.
func TestRecordReplayEditsConfigDeletesFile(t *testing.T) {
	const edited = "name = \"demo\"\n# edited outside\n"
	root, _ := replayOutside(t, func(s *Scenario, recDir string) {
		s.WriteFile(project.ConfigFile, edited, true)
		waitChange(t, recDir, project.ConfigFile, false)
		require.NoError(t, os.Remove(filepath.Join(s.Dir(), "other.spec.md")))
		waitChange(t, recDir, "other.spec.md", true)
	})
	b, err := os.ReadFile(filepath.Join(root, project.ConfigFile))
	require.NoError(t, err)
	assert.Equal(t, edited, string(b))
	assert.NoFileExists(t, filepath.Join(root, "other.spec.md"))
}

// TestRecordReplayDeletesConfig: spec.toml, never edited before, is deleted
// during a recording: only the mirror's seed makes that a known file's deletion.
// The UI goes on as it was, in the recording and the replay.
func TestRecordReplayDeletesConfig(t *testing.T) {
	root, final := replayOutside(t, func(s *Scenario, recDir string) {
		require.NoError(t, os.Remove(filepath.Join(s.Dir(), project.ConfigFile)))
		waitChange(t, recDir, project.ConfigFile, true)
	})
	assert.NoFileExists(t, filepath.Join(root, project.ConfigFile))
	assert.NotContains(t, final, "cannot be read", "the UI does not treat spec.toml as a spec file")
}

// TestRecordReplayCreatesFile: another process creates a spec file during a
// recording; the watcher finds it, the recording carries it, and the replay
// writes it and finds it the same way.
func TestRecordReplayCreatesFile(t *testing.T) {
	const made = "# made\n\nWritten while the UI ran.\n"
	root, _ := replayOutside(t, func(s *Scenario, recDir string) {
		s.WriteFile("made.spec.md", made, true)
		waitChange(t, recDir, "made.spec.md", false)
	})
	b, err := os.ReadFile(filepath.Join(root, "made.spec.md"))
	require.NoError(t, err)
	assert.Equal(t, made, string(b))
}

// TestRecordReplayModels: another process adds a model, edits it once the
// watcher knows it, and deletes one never edited before (only the mirror's seed
// from the models makes that a known file's deletion); the replay's project ends
// the same.
func TestRecordReplayModels(t *testing.T) {
	const added = "---- MODULE Added ----\nVARIABLE x\n====\n"
	const edited = "---- MODULE Added ----\nVARIABLES x, y\n====\n"
	root, _ := replayOutsideIn(t, scenes.Formal, "Lock.Holder", func(s *Scenario, recDir string) {
		s.WriteFile("tla/Added.tla", added, true)
		waitChange(t, recDir, "tla/Added.tla", false)
		s.WriteFile("tla/Added.tla", edited, false)
		waitEvent(t, recDir, "the edit of tla/Added.tla", func(ev recording.Event) bool {
			for _, c := range ev.Changes {
				if b, err := c.Bytes(); err == nil && c.Path == "tla/Added.tla" && string(b) == edited {
					return true
				}
			}
			return false
		})
		require.NoError(t, os.Remove(filepath.Join(s.Dir(), "tla", "Lock.tla")))
		waitChange(t, recDir, "tla/Lock.tla", true)
	})
	b, err := os.ReadFile(filepath.Join(root, "tla", "Added.tla"))
	require.NoError(t, err)
	assert.Equal(t, edited, string(b))
	assert.NoFileExists(t, filepath.Join(root, "tla", "Lock.tla"))
}
