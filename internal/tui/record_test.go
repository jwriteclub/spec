// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/fsnote"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/recording"
)

// recApp opens the lensd project (with an undefined reference on the line
// after the elapsed-time definition, for a gutter marker) with a recorder.
func recApp(t *testing.T) (*App, *Recorder, *recording.Writer) {
	t.Helper()
	root := lensdProject(t)
	path := filepath.Join(root, "lensd.spec.md")
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	src = []byte(strings.Replace(string(src), "\n**Def $Time.Elapsed", "\nSee $No.Such.\n\n**Def $Time.Elapsed", 1))
	require.NoError(t, os.WriteFile(path, src, 0o644))
	w, err := recording.NewWriter(t.TempDir())
	require.NoError(t, err)
	rec := NewRecorder(w, time.Now())
	opts := testOptions(root)
	opts.Record = rec
	a := newTestApp(t, opts)
	t.Cleanup(func() {
		rec.Close()
		_ = w.Close()
	})
	return a, rec, w
}

// events reads the events written so far.
func events(t *testing.T, w *recording.Writer) []recording.Event {
	t.Helper()
	var out []recording.Event
	b, err := os.ReadFile(w.Path())
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var ev recording.Event
		require.NoError(t, json.Unmarshal([]byte(line), &ev), line)
		out = append(out, ev)
	}
	return out
}

// lastOf is the last event of kind (and mouse action, when given).
func lastOf(t *testing.T, w *recording.Writer, kind, action string) recording.Event {
	t.Helper()
	evs := events(t, w)
	for k := len(evs) - 1; k >= 0; k-- {
		if evs[k].Kind == kind && (action == "" || (evs[k].Mouse != nil && evs[k].Action == action)) {
			return evs[k]
		}
	}
	t.Fatalf("no %s %s event in %d", kind, action, len(evs))
	return recording.Event{}
}

// Keys and pastes count in tape keystrokes; resizes, sequence numbers and
// times are recorded.
func TestRecordKeysAndPaste(t *testing.T) {
	a, rec, w := recApp(t)
	send(a, key("ctrl+e"), key("x"), key("backspace"))
	send(a, tea.PasteMsg{Content: "ab\r\nc"})
	send(a, key("alt+a"))
	assert.Equal(t, 3+4+1, rec.Keys())
	evs := events(t, w)
	require.GreaterOrEqual(t, len(evs), 6)
	assert.Equal(t, recording.KindResize, evs[0].Kind)
	assert.Equal(t, frameW, evs[0].Cols)
	cols, rows := rec.Size()
	assert.Equal(t, [2]int{frameW, frameH}, [2]int{cols, rows})
	var kinds []string
	for k, ev := range evs {
		assert.Equal(t, k+1, ev.Seq)
		if k > 0 {
			assert.GreaterOrEqual(t, ev.T, evs[k-1].T)
		}
		kinds = append(kinds, ev.Kind)
	}
	assert.Equal(t, []string{"resize", "key", "key", "key", "paste", "key"}, kinds)
	assert.Equal(t, "ctrl+e", evs[1].Key)
	assert.Equal(t, 1, evs[1].Keys)
	assert.Equal(t, "x", evs[2].Text)
	assert.Equal(t, 7, evs[4].Keys, "the paste counts its keystrokes")
	assert.Equal(t, "ab\r\nc", evs[4].Text)
	assert.Equal(t, "alt+a", evs[5].Key)
	assert.Equal(t, int(tea.ModAlt), evs[5].Mod)
	assert.Equal(t, 8, evs[5].Keys)
}

// A save is the app's own write: the watcher's report of it records
// nothing. Another process's write records its exact bytes; a removal
// records the deletion.
func TestRecordFileChanges(t *testing.T) {
	a, _, w := recApp(t)
	const file = "lensd.spec.md"
	send(a, key("ctrl+e"), key("Z"), key("ctrl+o"))
	require.False(t, a.cur.Ed.Buffer().Dirty(), a.flash)
	send(a, fileChangedMsg{paths: []string{file}})
	for _, ev := range events(t, w) {
		assert.NotEqual(t, recording.KindFiles, ev.Kind, "the app's own save")
	}

	abs := filepath.Join(a.ws.Root, file)
	outside := append([]byte("outside edit\n"), a.cur.Ed.Buffer().Bytes()...)
	require.NoError(t, os.WriteFile(abs, outside, 0o644))
	send(a, fileChangedMsg{paths: []string{file}})
	ev := lastOf(t, w, recording.KindFiles, "")
	assert.Equal(t, recording.SourceWatcher, ev.Source)
	require.Len(t, ev.Changes, 1)
	got, err := ev.Changes[0].Bytes()
	require.NoError(t, err)
	assert.Equal(t, outside, got)
	assert.Equal(t, file, ev.Changes[0].Path)
	assert.Equal(t, 3, ev.Keys)

	n := len(events(t, w))
	send(a, fileChangedMsg{paths: []string{file}}) // the same bytes again
	assert.Len(t, events(t, w), n)

	require.NoError(t, os.Remove(abs))
	send(a, fileChangedMsg{paths: []string{file}})
	ev = lastOf(t, w, recording.KindFiles, "")
	require.Len(t, ev.Changes, 1)
	assert.True(t, ev.Changes[0].Deleted)
}

// A proposal filed by another process (an agent's `spec propose`) is
// recorded; one this process files is not; a removal by another process
// is.
func TestRecordProposals(t *testing.T) {
	a, _, w := recApp(t)
	st := proposal.NewStore(a.ws.Root)
	agent := &proposal.Proposal{Author: "claude", Why: "agent change", Kind: "change"}
	fsnote.Quiet(func() { require.NoError(t, st.Create(agent)) })
	send(a, proposalsChangedMsg{})
	ev := lastOf(t, w, recording.KindFiles, "")
	assert.Equal(t, recording.SourceProposals, ev.Source)
	require.Len(t, ev.Changes, 1)
	assert.Equal(t, proposal.Dir+"/"+agent.ID+".json", ev.Changes[0].Path)
	disk, err := os.ReadFile(st.Path(agent.ID))
	require.NoError(t, err)
	assert.Equal(t, string(disk), ev.Changes[0].Content)

	n := len(events(t, w))
	own := &proposal.Proposal{Author: "chris", Why: "own", Kind: "change"}
	require.NoError(t, st.Create(own))
	own.Status = proposal.StatusRejected
	require.NoError(t, st.Save(own))
	send(a, proposalsChangedMsg{})
	assert.Len(t, events(t, w), n, "the UI's own proposal writes are not recorded")
	require.NoError(t, st.Remove(own.ID))
	send(a, proposalsChangedMsg{})
	assert.Len(t, events(t, w), n)

	fsnote.Quiet(func() { require.NoError(t, st.Remove(agent.ID)) })
	send(a, proposalsChangedMsg{})
	ev = lastOf(t, w, recording.KindFiles, "")
	require.Len(t, ev.Changes, 1)
	assert.True(t, ev.Changes[0].Deleted)
}

// Mouse events record their effect: a click the cursor's place, a
// ctrl+click the landing, an outline click the entry, a drag and release
// the selection, the wheel the scroll position, a hover the token.
func TestRecordMouse(t *testing.T) {
	a, _, w := recApp(t)
	send(a, tea.WindowSizeMsg{Width: 160, Height: 40}) // wide enough to show the outline
	const file = "lensd.spec.md"
	x, y, off := cellOfText(t, a, "lensd is a Go web gateway")
	want := posOf(a.cur.Ed, off)
	send(a, click(x+3, y, 0))
	ev := lastOf(t, w, recording.KindMouse, recording.MouseClick)
	assert.Equal(t, "main", ev.Screen)
	assert.Equal(t, "editor", ev.Pane)
	assert.Equal(t, x+3, ev.X)
	require.NotNil(t, ev.Target)
	require.NotNil(t, ev.Target.Pos)
	assert.Equal(t, recording.Pos{File: file, Line: want.Line, Col: want.Col + 3}, *ev.Target.Pos)
	assert.Equal(t, *ev.Target.Pos, *ev.Result.Cursor)

	// Drag and release select "lensd".
	send(a, click(x, y, 0), tea.MouseMotionMsg{X: x + 5, Y: y, Button: tea.MouseLeft}, tea.MouseReleaseMsg{X: x + 5, Y: y})
	drag := lastOf(t, w, recording.KindMouse, recording.MouseDrag)
	require.NotNil(t, drag.Result.Selection)
	assert.Equal(t, recording.Span{From: *want, To: recording.Pos{File: file, Line: want.Line, Col: want.Col + 5}}, *drag.Result.Selection)
	rel := lastOf(t, w, recording.KindMouse, recording.MouseRelease)
	assert.Equal(t, drag.Result.Selection, rel.Result.Selection)

	// Ctrl+click on a reference lands on the definition.
	x, y, _ = cellOfText(t, a, "$Time.Elapsed),")
	send(a, click(x+2, y, tea.ModCtrl))
	ev = lastOf(t, w, recording.KindMouse, recording.MouseClick)
	assert.Equal(t, int(tea.ModCtrl), ev.Mod)
	def := strings.Index(string(a.cur.Ed.Buffer().Bytes()), "**Def $Time.Elapsed")
	assert.Equal(t, *posOf(a.cur.Ed, def), *ev.Result.Cursor)

	// The wheel records the editor's top row.
	ex, _, _, _ := a.editorRect()
	send(a, tea.MouseWheelMsg{X: ex + 10, Y: 5, Button: tea.MouseWheelDown})
	ev = lastOf(t, w, recording.KindMouse, recording.MouseWheel)
	assert.Equal(t, "down", ev.Target.Direction)
	top := a.cur.Ed.TopRow()
	assert.Equal(t, recording.Scroll{Line: top.Line, Sub: top.Sub}, *ev.Result.Scroll)
	send(a, tea.MouseWheelMsg{X: 2, Y: 5, Button: tea.MouseWheelDown})
	ev = lastOf(t, w, recording.KindMouse, recording.MouseWheel)
	assert.Equal(t, "outline", ev.Pane)
	assert.Equal(t, a.outline.scroll, ev.Result.Scroll.Offset)

	// An outline click names the entry.
	k := -1
	for i, e := range a.outline.entries {
		if e.text == "$Interval.Gap" {
			k = i
		}
	}
	require.GreaterOrEqual(t, k, a.outline.scroll)
	send(a, click(3, k-a.outline.scroll, 0))
	ev = lastOf(t, w, recording.KindMouse, recording.MouseClick)
	assert.Equal(t, "outline", ev.Pane)
	assert.Equal(t, "$Interval.Gap", ev.Target.Label)
	assert.Equal(t, file, ev.Target.File)
	gap := strings.Index(string(a.cur.Ed.Buffer().Bytes()), "**Def $Interval.Gap")
	assert.Equal(t, a.ws.Line(file, gap), ev.Target.Line)
	assert.Equal(t, *posOf(a.cur.Ed, gap), *ev.Result.Cursor)
}

// A hover records the token the popup shows (an anchor by name, a gutter
// marker by line and code); moving away records the unhover; plain motion
// records nothing.
func TestRecordHover(t *testing.T) {
	a, _, w := recApp(t)
	x, y, _ := cellOfText(t, a, "$Time.Elapsed),")
	n := len(events(t, w))
	send(a, tea.MouseMotionMsg{X: x + 1, Y: y})
	assert.Len(t, events(t, w), n, "plain motion is not recorded")
	send(a, hoverMsg{seq: a.hoverSeq})
	require.NotNil(t, a.popup)
	ev := lastOf(t, w, recording.KindMouse, recording.MouseHover)
	require.NotNil(t, ev.Target)
	assert.Equal(t, "anchor", ev.Target.Kind)
	assert.Equal(t, "Time.Elapsed", ev.Target.Name)
	off := strings.Index(string(a.cur.Ed.Buffer().Bytes()), "$Time.Elapsed),")
	assert.Equal(t, *posOf(a.cur.Ed, off), *ev.Target.Pos)
	assert.Equal(t, strings.TrimSpace(ansi.Strip(a.popup.lines[0])), ev.Result.Popup)
	assert.NotEmpty(t, ev.Result.Popup)

	send(a, tea.MouseMotionMsg{X: x + 1, Y: 0})
	require.Nil(t, a.popup)
	lastOf(t, w, recording.KindMouse, recording.MouseUnhover)

	// The gutter marker of the undefined reference's line.
	a.cur.Ed.SetCursor(strings.Index(string(a.cur.Ed.Buffer().Bytes()), "See $No.Such."))
	a.cur.Ed.CenterCursor()
	x, y, off = cellOfText(t, a, "See $No.Such.")
	ex, _, _, _ := a.editorRect()
	send(a, tea.MouseMotionMsg{X: ex + 1, Y: y})
	send(a, hoverMsg{seq: a.hoverSeq})
	require.NotNil(t, a.popup, plain(a))
	ev = lastOf(t, w, recording.KindMouse, recording.MouseHover)
	assert.Equal(t, "gutter", ev.Target.Kind)
	assert.Equal(t, "gutter", ev.Pane)
	assert.Equal(t, a.ws.Line("lensd.spec.md", off), ev.Target.Line)
	assert.NotEmpty(t, ev.Target.Code)
	_ = x
}

// Quitting records the last frame and the end event.
func TestRecordFinal(t *testing.T) {
	a, _, w := recApp(t)
	want, _ := a.render()
	a.quit()
	ev := lastOf(t, w, recording.KindEnd, "")
	assert.NotEmpty(t, ev.FrameSHA)
	dir := filepath.Dir(w.Path())
	got, err := os.ReadFile(filepath.Join(dir, recording.FinalFile))
	require.NoError(t, err)
	assert.Equal(t, want, string(got))
	pl, err := os.ReadFile(filepath.Join(dir, recording.FinalPlain))
	require.NoError(t, err)
	assert.Equal(t, ansi.Strip(want), string(pl))
}
