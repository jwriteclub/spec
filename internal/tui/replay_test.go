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

	"github.com/jwriteclub/spec/internal/recording"
)

// replayStart is the synthetic recordings' start.
var replayStart = time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC)

// keyEvents are n recorded key presses ("down"), 100 ms apart from t=100.
func keyEvents(n int) []recording.Event {
	var evs []recording.Event
	for k := 1; k <= n; k++ {
		evs = append(evs, recording.Event{Kind: recording.KindKey, Key: "down", Code: tea.KeyDown, T: int64(100 * k), Keys: k})
	}
	return evs
}

// syntheticRecording is a recording of four keys plus extra events, at
// the golden frame size.
func syntheticRecording(extra ...recording.Event) *recording.Recording {
	evs := append(keyEvents(4), extra...)
	for k := range evs {
		evs[k].Seq = k + 1
	}
	// Recorded order is time order.
	for i := 1; i < len(evs); i++ {
		for j := i; j > 0 && evs[j].T < evs[j-1].T; j-- {
			evs[j], evs[j-1] = evs[j-1], evs[j]
		}
	}
	return &recording.Recording{Meta: recording.Meta{Version: recording.FormatVersion, Start: replayStart,
		Cols: frameW, Rows: frameH, Dark: true}, Events: evs}
}

// outsideEdit is a files event writing text to lensd.spec.md, anchored at
// key keys and recorded at t.
func outsideEdit(t int64, keys int, text string) recording.Event {
	return recording.Event{Kind: recording.KindFiles, T: t, Keys: keys, Source: recording.SourceWatcher,
		Changes: []recording.FileChange{{Path: "lensd.spec.md", Content: text}}}
}

// replayDriver runs the lensd project with rec fed back in.
func replayDriver(t *testing.T, rec *recording.Recording) (*Driver, *Replay, string) {
	t.Helper()
	root := lensdProject(t)
	rp := NewReplay(rec)
	opts := testOptions(root)
	opts.Replay = rp
	d, err := NewDriver(opts, frameW, frameH)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	return d, rp, root
}

// waitFile waits until path holds text and returns how long that took.
func waitFile(t *testing.T, path, text string) time.Duration {
	t.Helper()
	began := time.Now()
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(path)
		return string(b) == text
	}, 5*time.Second, 2*time.Millisecond)
	return time.Since(began)
}

// An event recorded 200 ms after key 3 fires about 200 ms after key 3
// arrives.
func TestReplayFeedsAfterKey(t *testing.T) {
	const text = "# edited elsewhere\n"
	d, rp, root := replayDriver(t, syntheticRecording(outsideEdit(500, 3, text)))
	require.NoError(t, d.Press("down", "down"))
	time.Sleep(300 * time.Millisecond)
	path := filepath.Join(root, "lensd.spec.md")
	b, _ := os.ReadFile(path)
	require.NotEqual(t, text, string(b), "not before key 3")
	require.NoError(t, d.Press("down"))
	took := waitFile(t, path, text)
	assert.GreaterOrEqual(t, took, 150*time.Millisecond)
	assert.Less(t, took, time.Second)
	assert.Empty(t, rp.Unfed())
	f, err := d.Frame()
	require.NoError(t, err)
	assert.Contains(t, f.Content, "edited elsewhere", "the open file reloaded")
	assert.Empty(t, rp.Warnings())
}

// A key that arrives before an event's delay ran out applies the event
// first: the order of keys and events holds whatever the tape's pacing.
func TestReplayFlushesBeforeNextKey(t *testing.T) {
	const text = "# edited elsewhere\n"
	d, rp, root := replayDriver(t, syntheticRecording(outsideEdit(500, 3, text)))
	require.NoError(t, d.Press("down", "down", "down"))
	time.Sleep(50 * time.Millisecond)
	began := time.Now()
	require.NoError(t, d.Press("down"))
	b, err := os.ReadFile(filepath.Join(root, "lensd.spec.md"))
	require.NoError(t, err)
	assert.Equal(t, text, string(b), "applied before key 4 was handled")
	assert.Less(t, time.Since(began), 150*time.Millisecond)
	assert.Equal(t, 4, rp.Keys())
}

// Events before the first key fire from Init.
func TestReplayFeedsBeforeFirstKey(t *testing.T) {
	const text = "# edited at once\n"
	_, rp, root := replayDriver(t, syntheticRecording(outsideEdit(50, 0, text)))
	waitFile(t, filepath.Join(root, "lensd.spec.md"), text)
	assert.Empty(t, rp.Unfed())
}

// The clock is the recorded time of the last key plus the time since it
// arrived, and never goes back.
func TestReplayClock(t *testing.T) {
	rec := syntheticRecording()
	rec.Events[1].T = 50 // key 2 recorded before key 1: the clock holds
	rp := NewReplay(rec)
	assert.Equal(t, replayStart, rp.Now(), "before Init")
	_ = rp.begin()
	n0 := rp.Now()
	assert.False(t, n0.Before(replayStart))
	assert.Less(t, n0.Sub(replayStart), 100*time.Millisecond)
	rp.advance(nil, 1)
	n1 := rp.Now()
	assert.GreaterOrEqual(t, n1.Sub(replayStart), 100*time.Millisecond)
	assert.Less(t, n1.Sub(replayStart), 200*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	assert.GreaterOrEqual(t, rp.Now().Sub(n1), 20*time.Millisecond, "the clock runs between keys")
	rp.advance(nil, 1)
	assert.False(t, rp.Now().Before(n1), "key 2's earlier time does not turn the clock back")
}

// hoverTarget opens the same popup, in the same place, as resting the
// mouse on the token does.
func TestHoverTargetMatchesHover(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	x, y, off := cellOfText(t, a, "$Time.Elapsed),")
	send(a, tea.MouseMotionMsg{X: x + 3, Y: y}, hoverMsg{seq: a.hoverSeq + 1})
	require.NotNil(t, a.popup)
	want := *a.popup
	wantFrame := plain(a)
	a.popup = nil

	target := recording.MouseTarget{Kind: "anchor", Name: "Time.Elapsed", Pos: posOf(a.cur.Ed, off)}
	require.True(t, a.hoverTarget(target))
	require.NotNil(t, a.popup)
	assert.Equal(t, want.lines, a.popup.lines)
	assert.Equal(t, [2]int{want.x, want.y}, [2]int{a.popup.x, a.popup.y})
	assert.True(t, a.popup.mouse)
	assert.Equal(t, wantFrame, plain(a))

	// Not found: nothing opens.
	a.popup = nil
	assert.False(t, a.hoverTarget(recording.MouseTarget{Kind: "anchor", Name: "No.Such"}))
	assert.False(t, a.hoverTarget(recording.MouseTarget{Kind: "anchor", Name: "Time.Elapsed",
		Pos: &recording.Pos{File: "other.spec.md", Line: 1, Col: 1}}), "another file")
}

// A hover whose token scrolled off screen falls back to the recorded cell
// and says so.
func TestReplayHoverFallsBack(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	x, y, off := cellOfText(t, a, "$Time.Elapsed),")
	target := &recording.MouseTarget{Kind: "anchor", Name: "Time.Elapsed", Pos: posOf(a.cur.Ed, off)}
	require.True(t, a.hoverTarget(*target))
	first := strings.TrimSpace(ansi.Strip(a.popup.lines[0]))
	a.popup = nil
	ev := recording.Event{Kind: recording.KindMouse, Keys: 7, Mouse: &recording.Mouse{
		Action: recording.MouseHover, X: x, Y: y, Screen: "main", Pane: "editor",
		Target: target, Result: &recording.MouseResult{Popup: first},
	}}
	rp := NewReplay(syntheticRecording())
	a.cur.Ed.SetCursor(len(a.cur.Ed.Buffer().Bytes())) // the token scrolls away
	_, _, onScreen := a.cur.Ed.CellOf(off)
	require.False(t, onScreen)
	rp.applyMouse(a, ev)
	ws := rp.Warnings()
	require.NotEmpty(t, ws)
	assert.Contains(t, ws[0], "the target is not on screen; hovered the recorded cell")
	assert.Contains(t, ws[0], "after key #7")

	// Scrolled back, the target is found and nothing is reported.
	rp = NewReplay(syntheticRecording())
	a.popup = nil
	a.cur.Ed.SetCursor(off)
	rp.applyMouse(a, ev)
	assert.Empty(t, rp.Warnings())
	require.NotNil(t, a.popup)
}

// A click is applied at the cell where its recorded place is now, and a
// wheel lands on the recorded scroll.
func TestReplayClickAndWheel(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	_, _, off := cellOfText(t, a, "lensd is a Go web gateway")
	pos := posOf(a.cur.Ed, off+3)
	ev := recording.Event{Kind: recording.KindMouse, Keys: 2, Mouse: &recording.Mouse{
		Action: recording.MouseClick, Button: tea.MouseLeft.String(), X: 0, Y: 0, Screen: "main", Pane: "editor",
		Target: &recording.MouseTarget{Pos: pos}, Result: &recording.MouseResult{Cursor: pos},
	}}
	rp := NewReplay(syntheticRecording())
	rp.applyMouse(a, ev)
	assert.Equal(t, off+3, a.cur.Ed.Cursor())
	assert.True(t, a.mouseDown, "the click went through the mouse code")
	assert.Empty(t, rp.Warnings())

	// Off screen, the cursor is set directly.
	a.mouseDown = false
	a.cur.Ed.SetCursor(len(a.cur.Ed.Buffer().Bytes()))
	rp.applyMouse(a, ev)
	assert.Equal(t, off+3, a.cur.Ed.Cursor())
	assert.Empty(t, rp.Warnings())

	// A wheel whose recorded scroll differs from three rows lands there.
	a.cur.Ed.SetCursor(0)
	wheel := recording.Event{Kind: recording.KindMouse, Mouse: &recording.Mouse{
		Action: recording.MouseWheel, Button: tea.MouseWheelDown.String(), Screen: "main", Pane: "editor",
		Target: &recording.MouseTarget{Direction: "down"}, Result: &recording.MouseResult{Scroll: &recording.Scroll{Line: 5}},
	}}
	rp.applyMouse(a, wheel)
	assert.Equal(t, 5, a.cur.Ed.TopRow().Line)
	assert.Equal(t, 0, a.cur.Ed.TopRow().Sub)
}

// A drag's recorded selection is set, anchor and cursor in place.
func TestReplayDrag(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	_, _, off := cellOfText(t, a, "lensd is a Go web gateway")
	ed := a.cur.Ed
	from, to := posOf(ed, off), posOf(ed, off+5)
	ev := recording.Event{Kind: recording.KindMouse, Mouse: &recording.Mouse{
		Action: recording.MouseDrag, Button: tea.MouseLeft.String(), Screen: "main", Pane: "editor",
		Result: &recording.MouseResult{Selection: &recording.Span{From: *from, To: *to}, Cursor: from},
	}}
	NewReplay(syntheticRecording()).applyMouse(a, ev)
	sel, ok := ed.Selection()
	require.True(t, ok)
	assert.Equal(t, "lensd", ed.Buffer().Text(sel))
	assert.Equal(t, off, ed.Cursor(), "dragged right to left: the cursor at the start")
}

// A terminal smaller than the recording's is warned once and laid out at
// its own size; a larger one lays out at the recorded size, unwarned.
func TestReplayWarnsOnSize(t *testing.T) {
	rp := NewReplay(syntheticRecording())
	a := newTestApp(t, testOptions(lensdProject(t)))
	assert.Equal(t, tea.WindowSizeMsg{Width: 80, Height: 24}, rp.translate(a, tea.WindowSizeMsg{Width: 80, Height: 24}))
	rp.translate(a, tea.WindowSizeMsg{Width: 90, Height: 24})
	ws := rp.Warnings()
	require.Len(t, ws, 1)
	assert.True(t, strings.Contains(ws[0], "80x24") && strings.Contains(ws[0], "100x30"), ws[0])

	rp = NewReplay(syntheticRecording())
	assert.Equal(t, tea.WindowSizeMsg{Width: frameW, Height: frameH}, rp.translate(a, tea.WindowSizeMsg{Width: 140, Height: 41}))
	assert.Empty(t, rp.Warnings())
}

// In a terminal larger than recorded, the replay lays out at the recorded
// size and centres the frame: the inner frame is the recorded layout, the
// margin blank cells on the terminal's (the theme's) background, the
// cursor moved with it; the terminal's mouse is moved back by the margin.
func TestReplayCentresInLargerTerminal(t *testing.T) {
	const tw, th = 140, 41 // margins 20 left, 5 top (6 bottom)
	root := lensdProject(t)
	opts := testOptions(root)
	opts.Replay = NewReplay(syntheticRecording())
	d, err := NewDriver(opts, tw, th)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	plain, err := NewDriver(testOptions(root), frameW, frameH)
	require.NoError(t, err)
	t.Cleanup(plain.Close)

	var view tea.View
	var inner string
	var innerCur *[2]int
	require.NoError(t, d.do(func(a *App) {
		assert.Equal(t, [4]int{frameW, frameH, tw, th}, [4]int{a.width, a.height, a.termW, a.termH})
		view = a.View()
		inner, innerCur = a.render()
	}))
	want, err := plain.Frame()
	require.NoError(t, err)
	assert.Equal(t, ansi.Strip(want.Content), ansi.Strip(inner), "the inner frame is the recorded layout")

	lines := strings.Split(view.Content, "\n")
	require.Len(t, lines, th)
	ox, oy := (tw-frameW)/2, (th-frameH)/2
	var got []string
	for y, l := range lines {
		require.Equal(t, tw, ansi.StringWidth(l), "line %d", y)
		if y < oy || y >= oy+frameH {
			assert.Equal(t, strings.Repeat(" ", tw), l, "margin line %d", y)
			continue
		}
		// The margins are blank cells on the default (the theme's)
		// background: no style is open where the right one begins.
		assert.Equal(t, strings.Repeat(" ", ox), l[:ox], "left margin of line %d", y)
		tail := l[strings.LastIndex(l, ansi.ResetStyle)+len(ansi.ResetStyle):]
		assert.GreaterOrEqual(t, len(tail), tw-frameW-ox, "right margin of line %d", y)
		assert.Equal(t, strings.Repeat(" ", len(tail)), tail, "right margin of line %d", y)
		got = append(got, strings.TrimRight(ansi.Cut(l, ox, ox+frameW), " "))
	}
	var wantLines []string
	for _, l := range strings.Split(inner, "\n") {
		wantLines = append(wantLines, strings.TrimRight(l, " "))
	}
	for len(wantLines) < frameH {
		wantLines = append(wantLines, "")
	}
	gotPlain := strings.Split(ansi.Strip(strings.Join(got, "\n")), "\n")
	for k := range gotPlain {
		gotPlain[k] = strings.TrimRight(gotPlain[k], " ")
	}
	wantPlain := strings.Split(ansi.Strip(strings.Join(wantLines, "\n")), "\n")
	for k := range wantPlain {
		wantPlain[k] = strings.TrimRight(wantPlain[k], " ")
	}
	assert.Equal(t, wantPlain, gotPlain, "the centred frame holds the recorded layout")
	if innerCur != nil {
		require.NotNil(t, view.Cursor)
		assert.Equal(t, innerCur[0]+ox, view.Cursor.X)
		assert.Equal(t, innerCur[1]+oy, view.Cursor.Y)
	}
	require.NoError(t, d.do(func(a *App) {
		rp := a.replay
		assert.Equal(t, tea.MouseClickMsg{X: 3, Y: 4, Button: tea.MouseLeft},
			rp.translate(a, tea.MouseClickMsg{X: ox + 3, Y: oy + 4, Button: tea.MouseLeft}))
		assert.Nil(t, rp.translate(a, tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft}), "a click in the margin is dropped")
		assert.Equal(t, tea.MouseReleaseMsg{X: 0, Y: 0, Button: tea.MouseLeft},
			rp.translate(a, tea.MouseReleaseMsg{X: 2, Y: 2, Button: tea.MouseLeft}), "a release is clamped")
	}))
}

// A key whose text is several runes (a grapheme the terminal sent at
// once) counts as many keys as its runes, as the recording's paste of it
// did, so the events after it keep their anchor.
func TestReplayCountsKeyRunes(t *testing.T) {
	const text = "# after the accent\n"
	rec := syntheticRecording()
	// A paste of "e" plus a combining accent after key 4: keys 5 and 6.
	rec.Events = append(rec.Events,
		recording.Event{Kind: recording.KindPaste, Text: "e\u0301", T: 500, Keys: 6, Seq: 5},
		outsideEdit(520, 6, text))
	rec.Events[len(rec.Events)-1].Seq = 6
	d, rp, root := replayDriver(t, rec)
	require.NoError(t, d.Press("down", "down", "down", "down"))
	require.NoError(t, d.Send(tea.KeyPressMsg{Code: 'e', Text: "e\u0301"}))
	assert.Equal(t, 6, rp.Keys())
	waitFile(t, filepath.Join(root, "lensd.spec.md"), text)
}

// A fed file change never writes outside the replayed project, even when
// a directory on its path is a symbolic link out of it.
func TestReplayFilesStayInProject(t *testing.T) {
	outside := t.TempDir()
	rec := syntheticRecording(recording.Event{Kind: recording.KindFiles, T: 50, Keys: 0, Source: recording.SourceWatcher,
		Changes: []recording.FileChange{{Path: "ext/a.spec.md", Content: "# escaped\n"}}})
	root := lensdProject(t)
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "ext")))
	rp := NewReplay(rec)
	opts := testOptions(root)
	opts.Replay = rp
	d, err := NewDriver(opts, frameW, frameH)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	require.Eventually(t, func() bool { return len(rp.Warnings()) > 0 }, 5*time.Second, 5*time.Millisecond)
	_, err = os.Stat(filepath.Join(outside, "a.spec.md"))
	assert.True(t, os.IsNotExist(err), "nothing written through the link")
}

// A recorded file change never reaches .git, where git would run what it finds,
// or leaves the project.
func TestReplayFileChangesStayInTheProject(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{".git/hooks/pre-commit", ".GIT/config", "a/.git/config", "a/../.git/config", "../x", "/etc/x"} {
		assert.Error(t, inProject(root, rel), rel)
	}
	for _, rel := range []string{"a.md", ".gitignore", "a/.github/x", ".spec/proposals/p.json"} {
		assert.NoError(t, inProject(root, rel), rel)
	}
}

// A recorded path with a control character is refused, and so is the project
// root itself.
func TestInProjectRefusesOddPaths(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{".", "", "./", "a/..", "a\x1b]0;x\x07.md", "a\nb.md"} {
		assert.Error(t, inProject(root, rel), "%q", rel)
	}
}

// What the replay reports has no terminal control: names in it come from the
// recording.
func TestPrintWarningsStripsControl(t *testing.T) {
	var b strings.Builder
	PrintWarnings(&b, "p: ", []string{"file \x1b]52;c;aGk=\x07a\x1b[2Jb\x07c\rd\nsecond\tline\x7f"})
	assert.Equal(t, "p: file abcd\n    second\tline\n", b.String())
}
