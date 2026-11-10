// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/fsnote"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/recording"
	"github.com/jwriteclub/spec/internal/tui/editor"
	"github.com/jwriteclub/spec/internal/workflow"
)

// dragEvery throttles the recorded drag events.
const dragEvery = 30 * time.Millisecond

// Recorder records a `spec ui` session's inputs (`spec ui --record`):
// keys, pastes, resizes, the theme, mouse events by their effect, and the
// files other processes changed, into session.jsonl
// (notes/plan-record-replay.md, section 4.5). App.Update calls it before
// and after each message.
type Recorder struct {
	w     *recording.Writer
	start time.Time
	keys  atomic.Int64

	// root and realRoot (symlinks resolved) map fsnote's absolute paths
	// to project-relative ones.
	root, realRoot string
	restore        func()

	// mirror is the sha256 of every watched file as this process last
	// knew it (absent: no file): the spec files, spec.toml and the proposals. The
	// app's own writes update it through fsnote, so a watcher event that
	// finds the mirror's bytes on disk is not another process's.
	mirrorMu sync.Mutex
	mirror   map[string][32]byte

	mu       sync.Mutex
	cols     int
	rows     int
	dark     *bool
	ended    bool
	app      *App
	lastDrag time.Duration
	// pendingDrag is a throttled drag not written yet.
	pendingDrag *recording.Event
}

// NewRecorder records into w; times are taken relative to start
// (time.Since: monotonic).
func NewRecorder(w *recording.Writer, start time.Time) *Recorder {
	return &Recorder{w: w, start: start, mirror: map[string][32]byte{}}
}

// Keys is the number of key presses so far, in tape units (a paste counts
// the keys typing it takes).
func (r *Recorder) Keys() int { return int(r.keys.Load()) }

// Size is the terminal's first size (0, 0 before it is known).
func (r *Recorder) Size() (cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cols, r.rows
}

// Dark is the theme the terminal reported (nil when it reported none).
func (r *Recorder) Dark() *bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dark
}

// attach starts recording a: it seeds the mirror from disk and listens to
// fsnote.
func (r *Recorder) attach(a *App) {
	r.mu.Lock()
	r.app = a
	r.mu.Unlock()
	r.root = a.ws.Root
	if real, err := filepath.EvalSymlinks(r.root); err == nil {
		r.realRoot = real
	}
	r.mirrorMu.Lock()
	for _, p := range recordedPaths(a.ws) {
		if b, err := os.ReadFile(r.abs(p)); err == nil {
			r.mirror[p] = sha256.Sum256(b)
		}
	}
	for p, b := range r.readProposals() {
		r.mirror[p] = sha256.Sum256(b)
	}
	r.mirrorMu.Unlock()
	r.restore = fsnote.SetObserver(r.ownChange)
}

// recordedPaths is the files the recorder's mirror starts from, besides the
// proposals: whatever the watcher reports (the spec files, the models, any other
// open document, spec.toml). A file missing from the mirror has no recorded
// deletion.
func recordedPaths(ws *Workspace) []string {
	return append(ws.WatchPaths(), project.ConfigFile)
}

// Close stops listening to fsnote and, when the session did not end
// through quit (the program was stopped), records the end with the frame
// as it is.
func (r *Recorder) Close() {
	if r.restore != nil {
		r.restore()
		r.restore = nil
	}
	r.mu.Lock()
	a, ended := r.app, r.ended
	r.mu.Unlock()
	if !ended && a != nil && !a.quitting {
		r.final(a)
	}
}

func (r *Recorder) abs(rel string) string {
	return filepath.Join(r.root, filepath.FromSlash(rel))
}

// rel maps an absolute path to a project-relative one ("" outside).
func (r *Recorder) rel(abs string) string {
	for _, root := range []string{r.root, r.realRoot} {
		if root == "" {
			continue
		}
		if rel, err := filepath.Rel(root, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return ""
}

// ownChange is the fsnote observer: this process wrote or removed a file.
func (r *Recorder) ownChange(c fsnote.Change) {
	rel := r.rel(c.Path)
	if rel == "" {
		return
	}
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	if c.Removed {
		delete(r.mirror, rel)
	} else {
		r.mirror[rel] = sha256.Sum256(c.Data)
	}
}

// readProposals reads every proposal file.
func (r *Recorder) readProposals() map[string][]byte {
	out := map[string][]byte{}
	ents, err := os.ReadDir(r.abs(proposal.Dir))
	if err != nil {
		return out
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		p := proposal.Dir + "/" + name
		if b, err := os.ReadFile(r.abs(p)); err == nil {
			out[p] = b
		}
	}
	return out
}

// since is the time since recording started.
func (r *Recorder) since() time.Duration { return time.Since(r.start) }

// write stamps ev with the time and the key count and writes it.
func (r *Recorder) write(ev recording.Event) {
	ev.T = r.since().Milliseconds()
	ev.Keys = r.Keys()
	r.w.Write(ev)
}

// recPre is what the recorder saw before a message was handled.
type recPre struct {
	// mouse is the event being built for a mouse message (nil: none).
	mouse *recording.Event
	// popup is set when a mouse popup was open.
	popup bool
	// down is the editor's mouseDown.
	down bool
}

// before records msg as it arrives (keys, pastes, sizes, files) and
// resolves a mouse message's target with the layout it hit.
func (r *Recorder) before(a *App, msg tea.Msg) recPre {
	pre := recPre{popup: a.popup != nil && a.popup.mouse, down: a.mouseDown}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		r.flushDrag()
		r.keys.Add(int64(keyUnits(msg)))
		r.write(recording.Event{Kind: recording.KindKey, Key: msg.String(), Code: msg.Code,
			Text: msg.Text, Mod: int(msg.Mod), Repeat: msg.IsRepeat})
	case tea.PasteMsg:
		r.keys.Add(int64(TapeKeys(msg.Content)))
		r.write(recording.Event{Kind: recording.KindPaste, Text: msg.Content})
	case tea.WindowSizeMsg:
		r.mu.Lock()
		if r.cols == 0 && r.rows == 0 {
			r.cols, r.rows = msg.Width, msg.Height
		}
		r.mu.Unlock()
		r.write(recording.Event{Kind: recording.KindResize, Cols: msg.Width, Rows: msg.Height})
	case tea.BackgroundColorMsg:
		dark := msg.IsDark()
		r.mu.Lock()
		r.dark = &dark
		r.mu.Unlock()
		r.write(recording.Event{Kind: recording.KindTheme, Dark: &dark})
	case fileChangedMsg:
		r.files(recording.SourceWatcher, msg.paths, false)
	case proposalsChangedMsg:
		r.files(recording.SourceProposals, nil, true)
	case tea.MouseMsg:
		pre.mouse = r.mouseTarget(a, msg)
	}
	return pre
}

// after records what a message did: a mouse event's result, a hover
// opened by the mouse resting.
func (r *Recorder) after(a *App, msg tea.Msg, pre recPre) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		r.mouseResult(a, msg, pre)
	case hoverMsg:
		if !pre.popup && a.popup != nil && a.popup.mouse {
			r.hover(a)
		}
	}
}

// files records the changes of paths (or of every proposal) that are not
// this process's.
func (r *Recorder) files(source string, paths []string, proposals bool) {
	disk := map[string][]byte{}
	if proposals {
		disk = r.readProposals()
	} else {
		for _, p := range paths {
			if b, err := os.ReadFile(r.abs(p)); err == nil {
				disk[p] = b
			}
		}
	}
	var changes []recording.FileChange
	r.mirrorMu.Lock()
	for p, b := range disk {
		sum := sha256.Sum256(b)
		if old, ok := r.mirror[p]; ok && old == sum {
			continue
		}
		r.mirror[p] = sum
		changes = append(changes, recording.ChangeOf(p, b, false))
	}
	gone := paths
	if proposals {
		gone = nil
		for p := range r.mirror {
			if strings.HasPrefix(p, proposal.Dir+"/") {
				gone = append(gone, p)
			}
		}
	}
	for _, p := range gone {
		if _, there := disk[p]; there {
			continue
		}
		if _, known := r.mirror[p]; known {
			delete(r.mirror, p)
			changes = append(changes, recording.ChangeOf(p, nil, true))
		}
	}
	r.mirrorMu.Unlock()
	if len(changes) == 0 {
		return
	}
	sortChanges(changes)
	r.write(recording.Event{Kind: recording.KindFiles, Source: source, Changes: changes})
}

func sortChanges(cs []recording.FileChange) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].Path < cs[j-1].Path; j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

// TapeKeys is the number of key presses typing text takes: one per rune,
// a line break one Enter (recording.PasteKeys).
func TapeKeys(text string) int { return recording.PasteKeys(text) }

// keyUnits is what a key press counts: the runes of its text (a grapheme
// such as "e" plus an accent may come as one press or several, and a
// tape types it rune by rune), at least 1.
func keyUnits(msg tea.KeyPressMsg) int { return max(TapeKeys(msg.Text), 1) }

// final records the end: the last frame (final.txt, final.plain.txt) and
// the end event. quit calls it before the frame goes blank.
func (r *Recorder) final(a *App) {
	r.mu.Lock()
	if r.ended {
		r.mu.Unlock()
		return
	}
	r.ended = true
	r.mu.Unlock()
	r.flushDrag()
	frame, _ := a.render()
	plain := ansi.Strip(frame)
	dir := filepath.Dir(r.w.Path())
	_ = recording.WriteFileAtomic(filepath.Join(dir, recording.FinalFile), []byte(frame))
	_ = recording.WriteFileAtomic(filepath.Join(dir, recording.FinalPlain), []byte(plain))
	sum := sha256.Sum256([]byte(plain))
	r.write(recording.Event{Kind: recording.KindEnd, FrameSHA: hex.EncodeToString(sum[:])})
}

// ---- mouse ----

// screenName names the top screen.
func screenName(s screen) string {
	switch s.(type) {
	case *mainScreen:
		return "main"
	case *reviewScreen:
		return "review"
	case *commitScreen:
		return "commit"
	case *hunkEditScreen:
		return "hunkedit"
	case *helpScreen:
		return "help"
	}
	return "other"
}

// mouseAction is the recorded action of a mouse message.
func mouseAction(msg tea.MouseMsg, down bool) string {
	switch m := msg.(type) {
	case tea.MouseClickMsg:
		return recording.MouseClick
	case tea.MouseReleaseMsg:
		return recording.MouseRelease
	case tea.MouseWheelMsg:
		return recording.MouseWheel
	case tea.MouseMotionMsg:
		if down && m.Button == tea.MouseLeft {
			return recording.MouseDrag
		}
		return recording.MouseUnhover
	}
	return ""
}

// posOf is offset off of ed as a recorded place.
func posOf(ed *editor.Editor, off int) *recording.Pos {
	idx := ed.Buffer().Index()
	line := idx.Line(off)
	return &recording.Pos{File: ed.Path(), Line: line, Col: off - idx.LineStart(line) + 1}
}

// selectionOf is ed's selection, nil when none.
func selectionOf(ed *editor.Editor) *recording.Span {
	sel, ok := ed.Selection()
	if !ok {
		return nil
	}
	return &recording.Span{From: *posOf(ed, sel.Start), To: *posOf(ed, sel.End)}
}

func intp(n int) *int { return &n }

// mainPane is the pane of the main screen at cell (x, y), as
// mainScreen.mouse decides it.
func (a *App) mainPane(x, y int) string {
	ex, ey, ew, eh := a.editorRect()
	inEditor := x >= ex && x < ex+ew && y >= ey && y < ey+eh
	inOutline := a.outlineWidth() > 0 && x < a.outlineWidth()-1 && y < a.bodyHeight()
	sx, _ := a.sideRect()
	switch {
	case a.sideShown && x >= sx && y < a.bodyHeight():
		return "sidebar"
	case inEditor && x-ex < a.cur.Ed.GutterWidth():
		return "gutter"
	case inEditor:
		return "editor"
	case inOutline:
		return "outline"
	}
	return "none"
}

// mouseTarget builds the event of a mouse message with what it aims at,
// or nil when the message is not recorded (a prompt has the mouse, plain
// motion).
func (r *Recorder) mouseTarget(a *App, msg tea.MouseMsg) *recording.Event {
	if a.prompt != nil {
		return nil
	}
	m := msg.Mouse()
	mo := &recording.Mouse{Action: mouseAction(msg, a.mouseDown), Button: m.Button.String(), X: m.X, Y: m.Y,
		Screen: screenName(a.top())}
	ev := &recording.Event{Kind: recording.KindMouse, Mod: int(m.Mod), Mouse: mo}
	switch s := a.top().(type) {
	case *mainScreen:
		r.mainTarget(a, msg, mo)
	case *reviewScreen:
		if _, ok := msg.(tea.MouseClickMsg); ok {
			line := m.Y + s.scroll
			for _, n := range s.nums {
				if line >= s.starts[n] && line < s.ends[n] {
					mo.Target = &recording.MouseTarget{Index: intp(n)}
				}
			}
		}
	case *commitScreen:
		if _, ok := msg.(tea.MouseClickMsg); ok {
			line := m.Y + s.scroll
			for k, st := range s.starts {
				if line == st && k < len(s.changes) {
					mo.Target = &recording.MouseTarget{Change: s.changes[k].ID}
				}
			}
		}
	case *hunkEditScreen:
		if _, ok := msg.(tea.MouseClickMsg); ok {
			if off, ok := s.ed.OffsetAtCell(m.X-s.left+s.ed.GutterWidth(), m.Y-s.top); ok {
				mo.Target = &recording.MouseTarget{Pos: posOf(s.ed, off)}
			}
		}
	}
	if w, ok := msg.(tea.MouseWheelMsg); ok {
		if mo.Target == nil {
			mo.Target = &recording.MouseTarget{}
		}
		switch w.Button {
		case tea.MouseWheelUp:
			mo.Target.Direction = "up"
		case tea.MouseWheelDown:
			mo.Target.Direction = "down"
		}
	}
	return ev
}

// mainTarget fills the pane and target of a main-screen mouse event.
func (r *Recorder) mainTarget(a *App, msg tea.MouseMsg, mo *recording.Mouse) {
	m := msg.Mouse()
	if a.list != nil {
		mo.Pane = "list"
		l := a.list
		if c, ok := msg.(tea.MouseClickMsg); ok && c.Button == tea.MouseLeft {
			if _, hit := l.click(a, m.X, m.Y); !hit {
				mo.Target = &recording.MouseTarget{Outside: true}
				return
			}
			k := m.Y - l.first
			if k >= 0 && k < l.rows && l.scroll+k < len(l.items) {
				it := l.items[l.scroll+k]
				mo.Target = &recording.MouseTarget{Label: ansi.Strip(it.label), File: it.loc.Path,
					Line: a.ws.Line(it.loc.Path, it.loc.Off), Index: intp(l.scroll + k)}
			}
		}
		return
	}
	mo.Pane = a.mainPane(m.X, m.Y)
	if _, ok := msg.(tea.MouseClickMsg); !ok {
		return
	}
	switch mo.Pane {
	case "editor", "gutter":
		ex, ey, _, _ := a.editorRect()
		if off, ok := a.cur.Ed.OffsetAtCell(m.X-ex, m.Y-ey); ok {
			mo.Target = &recording.MouseTarget{Pos: posOf(a.cur.Ed, off)}
		}
	case "outline":
		o := a.outline
		k := o.scroll + m.Y
		if k >= 0 && k < len(o.entries) {
			e := o.entries[k]
			mo.Target = &recording.MouseTarget{File: e.loc.Path, Line: a.ws.Line(e.loc.Path, e.loc.Off),
				Label: e.text, Index: intp(k)}
		}
	case "sidebar":
		s := a.side
		line := m.Y + s.scroll
		for k, c := range s.cards {
			if k < len(s.starts) && line >= s.starts[k] && line < s.ends[k] {
				mo.Target = &recording.MouseTarget{Proposal: c.p.ID, Item: c.key, Index: intp(k)}
			}
		}
	}
}

// mouseResult fills in what a mouse message did and writes its event.
func (r *Recorder) mouseResult(a *App, msg tea.MouseMsg, pre recPre) {
	ev := pre.mouse
	if ev == nil {
		return
	}
	mo := ev.Mouse
	res := &recording.MouseResult{}
	switch mo.Action {
	case recording.MouseUnhover:
		if !pre.popup || (a.popup != nil && a.popup.mouse) {
			return // plain motion: nothing changed
		}
		r.flushDrag()
		r.write(*ev)
		return
	case recording.MouseDrag:
		res.Selection = selectionOf(a.cur.Ed)
		res.Cursor = posOf(a.cur.Ed, a.cur.Ed.Cursor())
		mo.Result = res
		r.drag(*ev)
		return
	case recording.MouseRelease:
		if !pre.down {
			return // nothing was held
		}
	}
	switch s := a.top().(type) {
	case *mainScreen:
		switch {
		case mo.Action == recording.MouseWheel && mo.Pane == "list" && a.list != nil:
			res.Sel = intp(a.list.sel)
		case mo.Action == recording.MouseWheel && mo.Pane == "outline":
			res.Scroll = &recording.Scroll{Offset: a.outline.scroll}
		case mo.Action == recording.MouseWheel && mo.Pane == "sidebar":
			res.Scroll = &recording.Scroll{Offset: a.side.scroll}
		case mo.Action == recording.MouseWheel:
			top := a.cur.Ed.TopRow()
			res.Scroll = &recording.Scroll{Line: top.Line, Sub: top.Sub}
		case mo.Pane == "sidebar":
			res.Sel = intp(a.side.sel)
			res.Cursor = posOf(a.cur.Ed, a.cur.Ed.Cursor())
		default:
			res.Cursor = posOf(a.cur.Ed, a.cur.Ed.Cursor())
			res.Selection = selectionOf(a.cur.Ed)
		}
	case *reviewScreen:
		res.Sel = intp(s.sel)
		res.Scroll = &recording.Scroll{Offset: s.scroll}
	case *commitScreen:
		res.Sel = intp(s.sel)
		if mo.Target != nil && mo.Target.Change != "" {
			t := s.ticked[mo.Target.Change]
			res.Ticked = &t
		}
	case *hunkEditScreen:
		res.Cursor = posOf(s.ed, s.ed.Cursor())
		top := s.ed.TopRow()
		res.Scroll = &recording.Scroll{Line: top.Line, Sub: top.Sub}
	case *helpScreen:
		res.Scroll = &recording.Scroll{Offset: s.scroll}
	default:
		// The message changed the screen (a click that opened one): the
		// main editor's place is the result.
		res.Cursor = posOf(a.cur.Ed, a.cur.Ed.Cursor())
	}
	mo.Result = res
	r.flushDrag()
	r.write(*ev)
}

// drag writes a drag event, at most one per dragEvery; the last one before
// the release is always written.
func (r *Recorder) drag(ev recording.Event) {
	now := r.since()
	r.mu.Lock()
	if r.lastDrag != 0 && now-r.lastDrag < dragEvery {
		r.pendingDrag = &ev
		r.mu.Unlock()
		return
	}
	r.lastDrag = now
	r.pendingDrag = nil
	r.mu.Unlock()
	r.write(ev)
}

// flushDrag writes a throttled drag event still pending.
func (r *Recorder) flushDrag() {
	r.mu.Lock()
	ev := r.pendingDrag
	r.pendingDrag = nil
	if ev != nil {
		r.lastDrag = r.since()
	}
	r.mu.Unlock()
	if ev != nil {
		r.write(*ev)
	}
}

// hover records the mouse popup a hoverMsg just opened, by the token it
// shows.
func (r *Recorder) hover(a *App) {
	ed := a.cur.Ed
	ex, ey, _, _ := a.editorRect()
	x, y := a.hoverAt[0], a.hoverAt[1]
	t := &recording.MouseTarget{}
	pane := "editor"
	if x < ed.GutterWidth() {
		pane = "gutter"
		t.Kind = "gutter"
		if row, ok := ed.RowAtCell(y); ok {
			t.Line = row.Line + 1
		}
		if d, ok := workflow.MostSevere(a.popup.diags); ok {
			t.Code = d.Code
		}
	} else if off, ok := ed.OffsetAtCell(x, y); ok {
		tok := a.tokenIn(off, false)
		switch {
		case tok.occ != nil:
			t.Kind, t.Name, t.Pos = "anchor", tok.occ.Name, posOf(ed, tok.occ.Range.Start)
		case tok.sec != nil:
			t.Kind, t.Number, t.Pos = "section", tok.sec.Number, posOf(ed, tok.sec.Range.Start)
		default:
			t.Kind, t.Pos = "diag", posOf(ed, off)
			if d, ok := workflow.MostSevere(underAt(a.diagsAt(off), off)); ok {
				t.Code = d.Code
				t.Pos = posOf(ed, d.Range.Start)
			}
		}
	}
	first := ""
	if len(a.popup.lines) > 0 {
		first = strings.TrimSpace(ansi.Strip(a.popup.lines[0]))
	}
	r.flushDrag()
	r.write(recording.Event{Kind: recording.KindMouse, Mouse: &recording.Mouse{
		Action: recording.MouseHover, X: ex + x, Y: ey + y, Screen: screenName(a.top()), Pane: pane,
		Target: t, Result: &recording.MouseResult{Popup: first},
	}})
}

// underAt keeps the diagnostics of ds that cover off.
func underAt(ds []diag.Diagnostic, off int) []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, d := range ds {
		if off < d.Range.End {
			out = append(out, d)
		}
	}
	return out
}
