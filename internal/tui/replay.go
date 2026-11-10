// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/recording"
	"github.com/jwriteclub/spec/internal/tui/editor"
)

// replayMsg fires when a fed event's moment has come.
type replayMsg struct{ k int }

// Replay feeds a recording back into the UI (`spec ui --replay`): the
// keys come from the terminal (VHS types them from the tape), and Replay
// injects everything else the recording saw at the same point: other
// processes' file changes (written to disk, then reported as the watchers
// would) and mouse events, applied by what they hit rather than by cell
// (notes/plan-record-replay.md, sections 6.2 and 7.4).
//
// Each fed event is anchored to the key count it was recorded after and
// to its delay from that key, not to wall time: it fires that long after
// the key arrives, and at the latest just before the next key is handled,
// so a tape whose pacing was changed keeps the order of events and keys.
// The clock (Now) follows the keys the same way. Divergences (a target
// not found, a cursor elsewhere than recorded, a request unlike the
// recorded one) are collected as warnings, logged to the UI log, and not
// shown in the frame unless Flash is set.
type Replay struct {
	// Flash shows each warning in the status line (debugging a replay;
	// off by default, since it would be in the video).
	Flash bool

	rec *recording.Recording
	// keyT[n] is the recorded time of the event that brought the key
	// count to n (keyT[0] = 0).
	keyT []int64
	// feed are the files and mouse events, in recorded order; done says
	// which were applied.
	feed   []recording.Event
	done   []bool
	anchor map[int][]int

	mu      sync.Mutex
	count   int
	keyAt   map[int]time.Time
	lastNow time.Time
	warns   []string
	shown   int
	sized   bool
	final   string
	ended   bool
}

// NewReplay builds the feeder of rec.
func NewReplay(rec *recording.Recording) *Replay {
	r := &Replay{rec: rec, anchor: map[int][]int{}, keyAt: map[int]time.Time{}, keyT: []int64{0}}
	for _, ev := range rec.Events {
		switch ev.Kind {
		case recording.KindKey, recording.KindPaste:
			for len(r.keyT) <= ev.Keys {
				r.keyT = append(r.keyT, ev.T)
			}
		case recording.KindFiles, recording.KindMouse:
			if ev.Kind == recording.KindMouse && ev.Mouse == nil {
				continue
			}
			r.anchor[ev.Keys] = append(r.anchor[ev.Keys], len(r.feed))
			r.feed = append(r.feed, ev)
		}
	}
	r.done = make([]bool, len(r.feed))
	return r
}

// keyTime is the recorded time of key n (the last known for a count the
// recording never reached).
func (r *Replay) keyTime(n int) int64 {
	if n < len(r.keyT) {
		return r.keyT[n]
	}
	return r.keyT[len(r.keyT)-1]
}

// Now is the replay's clock: the recording's start plus the recorded time
// of the last key, plus the time since that key arrived here. It never
// goes back.
func (r *Replay) Now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.count
	t := r.rec.Meta.Start.Add(time.Duration(r.keyTime(n)) * time.Millisecond)
	if at, ok := r.keyAt[n]; ok {
		t = t.Add(time.Since(at))
	}
	if t.Before(r.lastNow) {
		t = r.lastNow
	}
	r.lastNow = t
	return t
}

// Keys is the number of key presses replayed so far.
func (r *Replay) Keys() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// Warn records a divergence (safe from any goroutine).
func (r *Replay) Warn(msg string) {
	r.mu.Lock()
	r.warns = append(r.warns, msg)
	r.mu.Unlock()
	lg.WithField("replay", true).Warn(msg)
}

// Warnings are the divergences so far.
func (r *Replay) Warnings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.warns...)
}

// Unfed lists the events never applied (the replay ended first).
func (r *Replay) Unfed() []string {
	var out []string
	for k, ev := range r.feed {
		if !r.done[k] {
			out = append(out, fmt.Sprintf("%s (after key #%d)", recording.Describe(ev), ev.Keys))
		}
	}
	return out
}

// Final is the frame the replay quit on ("" before it quits).
func (r *Replay) Final() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.final
}

// end keeps the frame the replay quits on.
func (r *Replay) end(a *App) {
	if r.ended {
		return
	}
	r.ended = true
	frame, _ := a.render()
	r.mu.Lock()
	r.final = frame
	r.mu.Unlock()
}

// begin starts the clock and schedules the events before the first key
// (App.Init).
func (r *Replay) begin() tea.Cmd {
	r.mu.Lock()
	r.keyAt[0] = time.Now()
	r.mu.Unlock()
	return r.schedule(0)
}

// schedule arms the events anchored at key n.
func (r *Replay) schedule(n int) tea.Cmd {
	var cmds []tea.Cmd
	for _, k := range r.anchor[n] {
		d := time.Duration(max(r.feed[k].T-r.keyTime(n), 0)) * time.Millisecond
		k := k
		cmds = append(cmds, tea.Tick(d, func(time.Time) tea.Msg { return replayMsg{k: k} }))
	}
	return tea.Batch(cmds...)
}

// before runs ahead of App.update. A replayMsg is handled here (handled
// is true); a key first flushes the events due before it, then advances
// the count and schedules the events anchored at the new count.
func (r *Replay) before(a *App, msg tea.Msg) (handled bool, cmd tea.Cmd) {
	switch msg := msg.(type) {
	case replayMsg:
		return true, r.applyUpTo(a, msg.k)
	case tea.KeyPressMsg:
		return false, r.advance(a, keyUnits(msg))
	case tea.PasteMsg:
		return false, r.advance(a, TapeKeys(msg.Content))
	}
	return false, nil
}

// translate fits a terminal message to the recorded layout, ahead of
// before. The UI lays out at the recorded size in a terminal at least that
// big, centred in it with the margin left blank (App.View, centre), so a
// video's window need not match the recording's cell for cell; in a
// smaller terminal it lays out at the terminal's size, and the first such
// size is warned. A mouse message from the terminal is moved by the
// centring; one off the frame is dropped (nil), a release clamped to it.
// The events the feeder injects are in the layout's cells already.
func (r *Replay) translate(a *App, msg tea.Msg) tea.Msg {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.termW, a.termH = m.Width, m.Height
		meta := r.rec.Meta
		first := !r.sized
		r.sized = true
		if meta.Cols <= 0 || meta.Rows <= 0 {
			return msg
		}
		if m.Width >= meta.Cols && m.Height >= meta.Rows {
			return tea.WindowSizeMsg{Width: meta.Cols, Height: meta.Rows}
		}
		if first {
			r.Warn(fmt.Sprintf("the terminal is %dx%d, smaller than the recording's %dx%d; laid out at the terminal's size", m.Width, m.Height, meta.Cols, meta.Rows))
		}
		return msg
	case tea.MouseMsg:
		ox, oy := a.margin()
		if ox == 0 && oy == 0 {
			return msg
		}
		mo := m.Mouse()
		mo.X, mo.Y = mo.X-ox, mo.Y-oy
		in := mo.X >= 0 && mo.Y >= 0 && mo.X < a.width && mo.Y < a.height
		switch msg.(type) {
		case tea.MouseReleaseMsg:
			mo.X, mo.Y = min(max(mo.X, 0), a.width-1), min(max(mo.Y, 0), a.height-1)
			return tea.MouseReleaseMsg(mo)
		case tea.MouseClickMsg:
			if in {
				return tea.MouseClickMsg(mo)
			}
		case tea.MouseWheelMsg:
			if in {
				return tea.MouseWheelMsg(mo)
			}
		case tea.MouseMotionMsg:
			if in {
				return tea.MouseMotionMsg(mo)
			}
		}
		return nil
	}
	return msg
}

// margin is the offset of the UI's frame in the terminal: the left and
// top margins when a replay centres the recorded layout (0, 0 otherwise).
func (a *App) margin() (x, y int) {
	return max((a.termW-a.width)/2, 0), max((a.termH-a.height)/2, 0)
}

// centre places frame (w x h cells, cursor cur) in the middle of a tw x th
// terminal. The margin is plain blank cells, which show the terminal's
// own background: the theme's background, since the UI draws its body on
// it without a colour of its own. Each frame line ends with a style reset
// so no style runs into the margin.
func centre(frame string, cur *[2]int, w, h, tw, th int) (string, *[2]int) {
	tw, th = max(tw, w), max(th, h)
	ox, oy := (tw-w)/2, (th-h)/2
	blank := strings.Repeat(" ", tw)
	lines := strings.Split(frame, "\n")
	out := make([]string, 0, th)
	for y := 0; y < th; y++ {
		k := y - oy
		if k < 0 || k >= h {
			out = append(out, blank)
			continue
		}
		l := ""
		if k < len(lines) {
			l = lines[k]
		}
		pad := max(w-ansi.StringWidth(l), 0)
		out = append(out, strings.Repeat(" ", ox)+l+ansi.ResetStyle+strings.Repeat(" ", pad+tw-w-ox))
	}
	if cur != nil {
		cur = &[2]int{cur[0] + ox, cur[1] + oy}
	}
	return strings.Join(out, "\n"), cur
}

// after runs behind App.update: with Flash, the newest warning goes to the
// status line.
func (r *Replay) after(a *App) {
	if !r.Flash {
		return
	}
	r.mu.Lock()
	var last string
	if len(r.warns) > r.shown {
		last = r.warns[len(r.warns)-1]
		r.shown = len(r.warns)
	}
	r.mu.Unlock()
	if last != "" {
		a.setFlash("replay: %s", strings.SplitN(last, "\n", 2)[0])
	}
}

// advance flushes what is due before n more keys, then counts them.
func (r *Replay) advance(a *App, n int) tea.Cmd {
	if n <= 0 {
		return nil
	}
	r.mu.Lock()
	old := r.count
	r.mu.Unlock()
	cmds := []tea.Cmd{r.flush(a, old)}
	now := time.Now()
	r.mu.Lock()
	for k := old + 1; k <= old+n; k++ {
		r.keyAt[k] = now
	}
	r.count = old + n
	r.mu.Unlock()
	for k := old + 1; k <= old+n; k++ {
		cmds = append(cmds, r.schedule(k))
	}
	return tea.Batch(cmds...)
}

// flush applies, in order, every event anchored at key n or before that
// has not been applied.
func (r *Replay) flush(a *App, n int) tea.Cmd {
	last := -1
	for k, ev := range r.feed {
		if ev.Keys > n {
			break
		}
		last = k
	}
	if last < 0 {
		return nil
	}
	return r.applyUpTo(a, last)
}

// applyUpTo applies event k and every earlier one not applied yet, so
// close timers never reorder them.
func (r *Replay) applyUpTo(a *App, k int) tea.Cmd {
	var cmds []tea.Cmd
	for j := 0; j <= k && j < len(r.feed); j++ {
		if r.done[j] {
			continue
		}
		r.done[j] = true
		cmds = append(cmds, r.apply(a, r.feed[j]))
	}
	return tea.Batch(cmds...)
}

// apply applies one fed event.
func (r *Replay) apply(a *App, ev recording.Event) tea.Cmd {
	if a.quitting {
		return nil
	}
	switch ev.Kind {
	case recording.KindFiles:
		return r.applyFiles(a, ev)
	case recording.KindMouse:
		return r.applyMouse(a, ev)
	}
	return nil
}

// ---- files ----

// applyFiles writes another process's changes to disk and reports them
// as the watcher that saw them did. The real watcher reports them again a
// moment later and finds nothing new.
func (r *Replay) applyFiles(a *App, ev recording.Event) tea.Cmd {
	var paths []string
	for _, c := range ev.Changes {
		abs := filepath.Join(a.ws.Root, filepath.FromSlash(c.Path))
		err := inProject(a.ws.Root, c.Path)
		switch {
		case err != nil:
		case c.Deleted:
			err = os.Remove(abs)
			if os.IsNotExist(err) {
				err = nil
			}
		default:
			var data []byte
			if data, err = c.Bytes(); err == nil {
				if err = os.MkdirAll(filepath.Dir(abs), 0o755); err == nil {
					err = recording.WriteFileAtomic(abs, data)
				}
			}
		}
		if err != nil {
			r.Warn(fmt.Sprintf("files %s (after key #%d): %v", c.Path, ev.Keys, err))
			continue
		}
		paths = append(paths, c.Path)
	}
	if ev.Source == recording.SourceProposals {
		a.onProposalsChanged()
		return nil
	}
	if len(paths) == 0 {
		return nil
	}
	return tea.Batch(a.onFileChanged(paths), a.afterParse())
}

// inProject refuses a recorded path that would lead out of the project
// root: "..", or a directory on the way that is a symbolic link (the
// replay never writes outside its copy of the project), one in .git,
// where git would run what a recording put there, and one that names no file or
// holds a control character (restoring leaves those out, too).
func inProject(root, rel string) error {
	clean := path.Clean(rel)
	if rel == "" || clean == "." || recording.HasControl(rel) {
		return fmt.Errorf("the path is not one a project can hold")
	}
	if path.IsAbs(clean) || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return fmt.Errorf("the path leads out of the project")
	}
	if recording.InGitDir(clean) {
		return fmt.Errorf("the path is in .git")
	}
	parts := strings.Split(clean, "/")
	dir := root
	for _, p := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, p)
		st, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; not written through it", filepath.ToSlash(dir[len(root)+1:]))
		}
	}
	return nil
}

// ---- mouse ----

// offsetOf is the offset of a recorded place in ed (clamped to its line).
func offsetOf(ed *editor.Editor, p recording.Pos) int {
	idx := ed.Buffer().Index()
	lr := idx.LineRange(max(p.Line, 1))
	return min(lr.Start+max(p.Col-1, 0), lr.End)
}

// mouseButtons are the buttons a recorded name may be.
var mouseButtons = []tea.MouseButton{
	tea.MouseNone, tea.MouseLeft, tea.MouseMiddle, tea.MouseRight, tea.MouseWheelUp, tea.MouseWheelDown,
	tea.MouseWheelLeft, tea.MouseWheelRight, tea.MouseBackward, tea.MouseForward,
}

// buttonOf is the button named name (MouseNone when unknown).
func buttonOf(name string) tea.MouseButton {
	for _, b := range mouseButtons {
		if b.String() == name {
			return b
		}
	}
	return tea.MouseNone
}

// rawMsg is the tea message of a recorded mouse event at its recorded
// cell.
func rawMsg(ev recording.Event) tea.Msg {
	mo := ev.Mouse
	m := tea.Mouse{X: mo.X, Y: mo.Y, Button: buttonOf(mo.Button), Mod: tea.KeyMod(ev.Mod)}
	switch mo.Action {
	case recording.MouseClick:
		return tea.MouseClickMsg(m)
	case recording.MouseRelease:
		return tea.MouseReleaseMsg(m)
	case recording.MouseWheel:
		return tea.MouseWheelMsg(m)
	}
	return tea.MouseMotionMsg(m)
}

// inject handles a synthesized message as if it came from the terminal.
func inject(a *App, msg tea.Msg) tea.Cmd {
	_, cmd := a.update(msg)
	return cmd
}

// raw sends the recorded event at its recorded cell.
func (r *Replay) raw(a *App, ev recording.Event) tea.Cmd {
	return inject(a, rawMsg(ev))
}

// applyMouse applies a recorded mouse event by its target (6.2), at the
// recorded cell only when the target cannot be placed.
func (r *Replay) applyMouse(a *App, ev recording.Event) tea.Cmd {
	mo := ev.Mouse
	label := fmt.Sprintf("mouse %s (after key #%d)", recording.Describe(ev), ev.Keys)
	if got := screenName(a.top()); got != mo.Screen {
		r.Warn(fmt.Sprintf("%s: recorded on the %s screen, now on %s; sent at the recorded cell", label, mo.Screen, got))
		return r.raw(a, ev)
	}
	var cmd tea.Cmd
	switch mo.Action {
	case recording.MouseHover:
		r.hover(a, ev, label)
		return nil
	case recording.MouseUnhover:
		if a.popup != nil && a.popup.mouse {
			a.popup = nil
		}
		return nil
	case recording.MouseDrag:
		if mo.Screen == "main" && r.drag(a, ev, label) {
			return nil
		}
		cmd = r.raw(a, ev)
	case recording.MouseWheel:
		cmd = r.wheel(a, ev)
	case recording.MouseClick:
		if mo.Screen == "main" && mo.Button == tea.MouseLeft.String() && a.prompt == nil {
			cmd = r.mainClick(a, ev, label)
		} else {
			cmd = r.raw(a, ev)
		}
	default:
		cmd = r.raw(a, ev)
	}
	r.check(a, ev, label)
	return cmd
}

// hover opens the recorded hover by its target, or at the recorded cell.
func (r *Replay) hover(a *App, ev recording.Event, label string) {
	mo := ev.Mouse
	if a.popup != nil && a.popup.mouse {
		a.popup = nil
	}
	if mo.Target == nil || !a.hoverTarget(*mo.Target) {
		r.Warn(label + ": the target is not on screen; hovered the recorded cell")
		ex, ey, _, _ := a.editorRect()
		a.hoverAt = [2]int{mo.X - ex, mo.Y - ey}
		a.hoverAtCell(mo.X-ex, mo.Y-ey)
	}
	if mo.Result == nil || mo.Result.Popup == "" {
		return
	}
	got := ""
	if a.popup != nil && len(a.popup.lines) > 0 {
		got = strings.TrimSpace(ansi.Strip(a.popup.lines[0]))
	}
	if got != mo.Result.Popup {
		r.Warn(fmt.Sprintf("%s: the popup begins %q; the recording's began %q", label, got, mo.Result.Popup))
	}
}

// drag sets the selection a recorded drag made.
func (r *Replay) drag(a *App, ev recording.Event, label string) bool {
	res := ev.Mouse.Result
	if res == nil || res.Selection == nil {
		return false
	}
	ed := a.cur.Ed
	if res.Selection.From.File != ed.Path() {
		r.Warn(fmt.Sprintf("%s: the selection was in %s; %s is open", label, res.Selection.From.File, ed.Path()))
		return false
	}
	from, to := offsetOf(ed, res.Selection.From), offsetOf(ed, res.Selection.To)
	anchor, cur := from, to
	if res.Cursor != nil && *res.Cursor == res.Selection.From {
		anchor, cur = to, from
	}
	ed.SetSelection(anchor, cur)
	return true
}

// mainClick applies a left click on the main screen by what it hit.
func (r *Replay) mainClick(a *App, ev recording.Event, label string) tea.Cmd {
	mo, t := ev.Mouse, ev.Mouse.Target
	if mo.Pane == "list" || a.list != nil {
		if a.list == nil {
			r.Warn(label + ": no list is open")
			return nil
		}
		if t == nil || t.Outside {
			a.list = nil
			return nil
		}
		best, bestD := -1, 0
		for k, it := range a.list.items {
			if ansi.Strip(it.label) != t.Label || it.loc.Path != t.File {
				continue
			}
			if d := absInt(a.ws.Line(it.loc.Path, it.loc.Off) - t.Line); best < 0 || d < bestD {
				best, bestD = k, d
			}
		}
		if best < 0 {
			r.Warn(label + ": the item is not in the list; clicked the recorded cell")
			return r.raw(a, ev)
		}
		loc := a.list.items[best].loc
		a.list = nil
		a.jump(loc)
		return nil
	}
	if t == nil {
		return r.raw(a, ev)
	}
	switch mo.Pane {
	case "editor", "gutter":
		ed := a.cur.Ed
		if t.Pos == nil || t.Pos.File != ed.Path() {
			r.Warn(label + ": the clicked file is not open; clicked the recorded cell")
			return r.raw(a, ev)
		}
		off := offsetOf(ed, *t.Pos)
		if x, y, ok := ed.CellOf(off); ok {
			ex, ey, _, _ := a.editorRect()
			return inject(a, tea.MouseClickMsg{X: ex + x, Y: ey + y, Button: tea.MouseLeft, Mod: tea.KeyMod(ev.Mod)})
		}
		// Off screen: what the click does, without the cell.
		a.popup, a.comp = nil, nil
		a.outlineFocus, a.sideFocus = false, false
		if tea.KeyMod(ev.Mod)&(tea.ModCtrl|tea.ModAlt) != 0 {
			a.fresh()
			ed.SetCursor(off)
			a.toTarget()
			return nil
		}
		ed.ClearMark()
		ed.SetCursor(off)
		return nil
	case "outline":
		o := a.outline
		best, bestD := -1, 0
		for k, e := range o.entries {
			if e.loc.Path != t.File || e.text != t.Label || e.stale {
				continue
			}
			if d := absInt(a.ws.Line(e.loc.Path, e.loc.Off) - t.Line); best < 0 || d < bestD {
				best, bestD = k, d
			}
		}
		if best < 0 {
			r.Warn(label + ": the outline entry is gone; clicked the recorded cell")
			return r.raw(a, ev)
		}
		if row := best - o.scroll; row >= 0 && row < a.bodyHeight() && a.outlineWidth() > 1 {
			return inject(a, tea.MouseClickMsg{X: 0, Y: row, Button: tea.MouseLeft})
		}
		a.popup, a.comp = nil, nil
		o.sel = best
		a.outlineFocus = false
		a.jump(o.entries[best].loc)
		return nil
	case "sidebar":
		s := a.side
		for k, c := range s.cards {
			if c.p != nil && c.p.ID == t.Proposal && c.key == t.Item {
				a.popup, a.comp = nil, nil
				if !a.sideFocus {
					a.sidePeeked = false
				}
				a.sideFocus, a.outlineFocus = true, false
				s.sel = k
				a.peek()
				return nil
			}
		}
		r.Warn(label + ": the card is gone; clicked the recorded cell")
		return r.raw(a, ev)
	}
	return r.raw(a, ev)
}

// wheel scrolls the recorded pane, then sets the recorded scroll if the
// wheel's steps landed elsewhere.
func (r *Replay) wheel(a *App, ev recording.Event) tea.Cmd {
	mo := ev.Mouse
	if mo.Screen != "main" || a.list != nil || mo.Pane == "list" {
		return r.raw(a, ev)
	}
	msg := rawMsg(ev).(tea.MouseWheelMsg)
	ex, ey, _, _ := a.editorRect()
	switch mo.Pane {
	case "editor", "gutter":
		msg.X, msg.Y = ex+a.cur.Ed.GutterWidth(), ey
	case "outline":
		msg.X, msg.Y = 0, 0
	case "sidebar":
		sx, _ := a.sideRect()
		msg.X, msg.Y = sx+1, 0
	}
	cmd := inject(a, msg)
	if res := mo.Result; res != nil && res.Scroll != nil {
		switch mo.Pane {
		case "outline":
			a.outline.scroll = res.Scroll.Offset
		case "sidebar":
			a.side.scroll = res.Scroll.Offset
		default:
			want := editor.RowPos{Line: res.Scroll.Line, Sub: res.Scroll.Sub}
			if a.cur.Ed.TopRow() != want {
				lg.WithField("replay", true).Infof("wheel after key #%d: top row %v, recorded %v; set", ev.Keys, a.cur.Ed.TopRow(), want)
				a.cur.Ed.SetTopRow(want)
			}
		}
	}
	return cmd
}

// check compares the state after a click (or a raw event) with what the
// recording saw, warning on a difference.
func (r *Replay) check(a *App, ev recording.Event, label string) {
	res := ev.Mouse.Result
	if res == nil || ev.Mouse.Action == recording.MouseWheel && ev.Mouse.Screen == "main" {
		return
	}
	differ := func(what string, got, want any) {
		r.Warn(fmt.Sprintf("%s: %s is %v; the recording's was %v", label, what, got, want))
	}
	switch s := a.top().(type) {
	case *mainScreen:
		if res.Cursor != nil {
			if got := posOf(a.cur.Ed, a.cur.Ed.Cursor()); *got != *res.Cursor {
				differ("the cursor", *got, *res.Cursor)
			}
		}
		if res.Sel != nil && ev.Mouse.Pane == "sidebar" && a.side.sel != *res.Sel {
			differ("the selected card", a.side.sel, *res.Sel)
		}
	case *reviewScreen:
		if res.Sel != nil && s.sel != *res.Sel {
			differ("the selected hunk", s.sel, *res.Sel)
		}
	case *commitScreen:
		if res.Ticked != nil && ev.Mouse.Target != nil && s.ticked[ev.Mouse.Target.Change] != *res.Ticked {
			differ("the tick", s.ticked[ev.Mouse.Target.Change], *res.Ticked)
		}
	case *hunkEditScreen:
		if res.Cursor != nil {
			if got := posOf(s.ed, s.ed.Cursor()); *got != *res.Cursor {
				differ("the cursor", *got, *res.Cursor)
			}
		}
	case *helpScreen:
		if res.Scroll != nil && s.scroll != res.Scroll.Offset {
			differ("the scroll", s.scroll, res.Scroll.Offset)
		}
	}
}
