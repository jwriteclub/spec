// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/tui/editor"
	"github.com/jwriteclub/spec/internal/workflow"
)

// mainScreen is the editing screen: outline, editor, the Checks sidebar,
// and the popups over them.
type mainScreen struct{}

func (s *mainScreen) context(a *App) Context {
	switch {
	case a.list != nil:
		return CtxList
	case a.popup != nil && len(a.popup.diags) > 0:
		return CtxDiagPopup
	case a.popup != nil:
		return CtxPopup
	case a.comp != nil:
		return CtxCompletion
	case a.sideFocus && a.sideShown:
		return CtxChecks
	case a.outlineFocus && a.showOutline:
		return CtxOutline
	}
	return CtxEditor
}

func (s *mainScreen) key(a *App, msg tea.KeyPressMsg, b Binding, ok bool) tea.Cmd {
	switch s.context(a) {
	case CtxList:
		return a.listKey(msg, b, ok)
	case CtxPopup, CtxDiagPopup:
		if ok {
			switch b.Action {
			case ActCancel:
				a.popup = nil
				return nil
			case ActFix:
				d, _ := workflow.MostSevere(a.popup.diags)
				a.popup = nil
				return a.fix(d)
			case ActToTarget:
				t, choices, title := a.popup.target, a.popup.choices, a.popup.choicesTitle
				a.popup = nil
				if len(choices) > 0 {
					a.list = newList(title, choices, false)
					return nil
				}
				if t == nil {
					a.setFlash("Nothing to follow in this popup")
					return nil
				}
				a.jump(*t)
				return nil
			}
		}
		// Any other key closes the popup and goes to the editor.
		a.popup = nil
		b, ok = lookupKey(CtxEditor, msg)
		return a.editorKey(msg, b, ok)
	case CtxOutline:
		return a.outlineKey(b, ok)
	case CtxCompletion:
		return a.completionKey(msg, b, ok)
	case CtxChecks:
		return a.sideKey(b, ok)
	}
	return a.editorKey(msg, b, ok)
}

// editorKey runs an editor-context key.
func (a *App) editorKey(msg tea.KeyPressMsg, b Binding, ok bool) tea.Cmd {
	if ok && !a.canRun(CtxEditor, b) {
		return nil
	}
	ed := a.cur.Ed
	if !ok {
		k := msg.Key()
		if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper|tea.ModHyper|tea.ModMeta) == 0 {
			a.lastCut = false
			ed.Type(k.Text)
			cmd := a.edited()
			a.maybeComplete(k.Text)
			return cmd
		}
		return nil
	}
	if b.Action != ActCut {
		a.lastCut = false
	}
	switch b.Action {
	case ActHelp:
		a.openHelp()
	case ActWriteOut:
		a.save(false)
	case ActExit:
		return a.exit()
	case ActWhereIs:
		label := "Search"
		if a.query != "" {
			label += " [" + a.query + "]"
		}
		a.prompt = newTextPrompt(promptFind, label, "")
	case ActGoToLine:
		a.prompt = newTextPrompt(promptGoTo, "Enter line number", "")
	case ActLineStart:
		ed.LineStart(false)
	case ActLineEnd:
		ed.LineEnd(false)
	case ActPageUp:
		ed.PageUp(false)
	case ActPageDown:
		ed.PageDown(false)
	case ActRight:
		ed.Right(false)
	case ActLeft:
		ed.Left(false)
	case ActUp:
		ed.Up(false)
	case ActDown:
		ed.Down(false)
	case ActTop:
		ed.Top(false)
	case ActBottom:
		ed.Bottom(false)
	case ActWordLeft:
		ed.WordLeft(false)
	case ActWordRight:
		ed.WordRight(false)
	case ActSelLeft:
		ed.Left(true)
	case ActSelRight:
		ed.Right(true)
	case ActSelUp:
		ed.Up(true)
	case ActSelDown:
		ed.Down(true)
	case ActSelLineStart:
		ed.LineStart(true)
	case ActSelLineEnd:
		ed.LineEnd(true)
	case ActMark:
		if ed.ToggleMark() {
			a.setFlash("Mark Set")
		} else {
			a.setFlash("Mark Unset")
		}
	case ActCut:
		text := ed.Cut()
		if text == "" {
			a.setFlash("Nothing to cut")
			return nil
		}
		if a.lastCut {
			a.cutBuf += text
		} else {
			a.cutBuf = text
		}
		a.lastCut = true
		return a.edited()
	case ActPaste:
		if a.cutBuf == "" {
			a.setFlash("Cutbuffer is empty")
			return nil
		}
		ed.Insert(a.cutBuf)
		return a.edited()
	case ActCopy:
		if _, sel := ed.Selection(); !sel {
			// ^C with nothing selected cancels, like Esc, and stops a
			// running check.
			if !a.cancelWait() && !a.cancelJob() {
				a.setFlash("Nothing selected to copy")
			}
			ed.ClearMark()
			ed.SetMatch(pos.Range{})
			return nil
		}
		text := ed.Copy()
		a.cutBuf = text
		a.setFlash("Copied %d bytes", len(text))
		return tea.SetClipboard(text)
	case ActDeleteChar:
		ed.Delete()
		return a.edited()
	case ActBackspace:
		ed.Backspace()
		return a.edited()
	case ActNewline:
		ed.Newline()
		return a.edited()
	case ActTab:
		ed.Tab()
		return a.edited()
	case ActUndo:
		if !ed.Undo() {
			a.setFlash("Nothing to undo")
			return nil
		}
		return a.edited()
	case ActRedo:
		if !ed.Redo() {
			a.setFlash("Nothing to redo")
			return nil
		}
		return a.edited()
	case ActToTarget:
		a.toTarget()
	case ActBack:
		a.goBack()
	case ActForward:
		a.goForward()
	case ActFormat:
		return a.formatBuffer()
	case ActQuickOpen:
		a.openQuickOpen()
	case ActLook:
		a.look()
	case ActRefs:
		a.refs()
	case ActOutline:
		a.toggleOutline()
	case ActNextPane:
		a.nextPane()
	case ActPropose:
		return a.propose()
	case ActReview:
		return a.commitFlow()
	case ActAnalyze:
		return a.analyze()
	case ActNextEdit:
		a.jumpToEdit(1)
	case ActPrevEdit:
		a.jumpToEdit(-1)
	case ActHelpBar:
		a.showHelpBar = !a.showHelpBar
		a.layout()
	case ActCancel:
		ed.ClearMark()
		ed.SetMatch(pos.Range{})
	}
	return nil
}

// nextPane is shift+Tab: focus the next visible pane, editor → sidebar →
// outline → editor, skipping the hidden ones.
func (a *App) nextPane() {
	type pane int
	const (
		editor pane = iota
		side
		outline
	)
	cur := editor
	switch {
	case a.sideFocus && a.sideShown:
		cur = side
	case a.outlineFocus && a.outlineWidth() > 0:
		cur = outline
	}
	visible := map[pane]bool{editor: true, side: a.sideShown, outline: a.outlineWidth() > 0}
	next := cur
	for range 3 {
		next = (next + 1) % 3
		if visible[next] {
			break
		}
	}
	if next == cur {
		a.setFlash("No other pane: ^R shows the sidebar, ^^ the outline")
		return
	}
	a.sideFocus, a.outlineFocus = next == side, next == outline
	switch next {
	case side:
		a.sidePeeked = false
	case outline:
		a.outline.selectNear(a.here())
		a.outline.shown = -1
	}
}

// toggleOutline shows and focuses the outline, or hides it when it has
// focus.
func (a *App) toggleOutline() {
	if a.showOutline && a.outlineFocus {
		a.showOutline = false
		a.outlineFocus = false
	} else {
		a.showOutline = true
		a.outlineFocus = true
		a.outline.selectNear(a.here())
		a.outline.shown = -1 // bring the selection into view
	}
	a.layout()
}

// outlineKey runs an outline-context key.
func (a *App) outlineKey(b Binding, ok bool) tea.Cmd {
	if !ok {
		return nil
	}
	o := a.outline
	page := max(a.bodyHeight()-2, 1)
	switch b.Action {
	case ActUp:
		o.move(-1)
	case ActDown:
		o.move(1)
	case ActPageUp:
		o.move(-page)
	case ActPageDown:
		o.move(page)
	case ActAccept:
		if loc, ok := o.selected(); ok {
			a.outlineFocus = false
			a.jump(loc)
		}
	case ActCancel:
		a.outlineFocus = false
	case ActNextPane:
		a.nextPane()
	case ActOutline:
		a.toggleOutline()
	case ActHelp:
		a.openHelp()
	case ActExit:
		return a.exit()
	case ActQuickOpen:
		a.outlineFocus = false
		a.openQuickOpen()
	case ActPropose:
		a.outlineFocus = false
		return a.propose()
	case ActReview:
		return a.commitFlow()
	case ActAnalyze:
		a.outlineFocus = false
		return a.analyze()
	case ActHelpBar:
		a.showHelpBar = !a.showHelpBar
		a.layout()
	}
	return nil
}

// ---- mouse ----

func (s *mainScreen) mouse(a *App, msg tea.MouseMsg) tea.Cmd {
	m := msg.Mouse()
	ex, ey, ew, eh := a.editorRect()
	inEditor := m.X >= ex && m.X < ex+ew && m.Y >= ey && m.Y < ey+eh
	inOutline := a.outlineWidth() > 0 && m.X < a.outlineWidth()-1 && m.Y < a.bodyHeight()
	sx, _ := a.sideRect()
	inSide := a.sideShown && m.X >= sx && m.Y < a.bodyHeight()
	if inSide {
		inEditor = false
	}

	if a.list != nil {
		if c, ok := msg.(tea.MouseClickMsg); ok && c.Button == tea.MouseLeft {
			if loc, hit := a.list.click(a, m.X, m.Y); hit {
				if loc != nil {
					a.list = nil
					a.jump(*loc)
				}
				return nil
			}
			a.list = nil
		}
		if w, ok := msg.(tea.MouseWheelMsg); ok {
			if w.Button == tea.MouseWheelUp {
				a.list.move(-1)
			} else if w.Button == tea.MouseWheelDown {
				a.list.move(1)
			}
		}
		return nil
	}

	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		a.popup = nil
		a.comp = nil
		if msg.Button != tea.MouseLeft {
			return nil
		}
		switch {
		case inSide:
			a.sideClick(m.Y)
		case inEditor:
			a.outlineFocus = false
			a.sideFocus = false
			if msg.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
				a.fresh()
				if off, ok := a.cur.Ed.OffsetAtCell(m.X-ex, m.Y-ey); ok {
					a.cur.Ed.SetCursor(off)
					a.toTarget()
				}
				return nil
			}
			a.cur.Ed.Click(m.X-ex, m.Y-ey)
			a.mouseDown = true
		case inOutline:
			a.outlineFocus = true
			if loc, ok := a.outline.click(m.Y); ok {
				a.outlineFocus = false
				a.jump(loc)
			}
		}
	case tea.MouseMotionMsg:
		if a.mouseDown && msg.Button == tea.MouseLeft {
			a.cur.Ed.Drag(m.X-ex, m.Y-ey)
			return nil
		}
		if a.popup != nil && a.popup.mouse && !a.popup.covers(m.X, m.Y) && !a.popup.over(m.X, m.Y) {
			a.popup = nil
		}
		if inEditor && a.popup == nil {
			a.hoverSeq++
			a.hoverAt = [2]int{m.X - ex, m.Y - ey}
			seq := a.hoverSeq
			return tea.Tick(a.opts.HoverDelay, func(time.Time) tea.Msg { return hoverMsg{seq: seq} })
		}
	case tea.MouseReleaseMsg:
		if a.mouseDown {
			a.mouseDown = false
			a.cur.Ed.Release()
		}
	case tea.MouseWheelMsg:
		a.popup = nil
		a.comp = nil
		step := 3
		switch {
		case inOutline && msg.Button == tea.MouseWheelUp:
			a.outline.scrollBy(-step, a.bodyHeight())
		case inOutline && msg.Button == tea.MouseWheelDown:
			a.outline.scrollBy(step, a.bodyHeight())
		case inSide && msg.Button == tea.MouseWheelUp:
			a.side.scroll = max(a.side.scroll-step, 0)
		case inSide && msg.Button == tea.MouseWheelDown:
			a.side.scroll += step
		case msg.Button == tea.MouseWheelUp:
			a.cur.Ed.ScrollRows(-step)
		case msg.Button == tea.MouseWheelDown:
			a.cur.Ed.ScrollRows(step)
		}
	}
	return nil
}

// ---- view ----

func (s *mainScreen) view(a *App, w, h int) []string {
	ow := a.outlineWidth()
	ex, _, ew, eh := a.editorRect()
	ed := a.editorLines(ew, eh)
	var side []string
	if a.sideShown {
		_, sw := a.sideRect()
		side = a.sideView(sw, h)
	}
	if ow == 0 && side == nil {
		return ed
	}
	var ol []string
	if ow > 0 {
		ol = a.outline.view(a, ow-1, h)
	}
	out := make([]string, h)
	borderGlyph := a.theme.uiText("│")
	if a.theme.ASCII {
		borderGlyph = "|"
	}
	border := a.theme.Border.Render(borderGlyph)
	if a.outlineFocus {
		border = a.theme.FocusEdge.Render(borderGlyph) // the outline has the keyboard
	}
	for k := 0; k < h; k++ {
		line := ""
		if ow > 0 {
			left := ""
			if k < len(ol) {
				left = ol[k]
			}
			line = padTo(left, ow-1) + border
		}
		mid := ""
		if k < len(ed) {
			mid = ed[k]
		}
		if side != nil && a.sideColumn() {
			line += padTo(mid, ew) + side[k]
		} else if side != nil {
			sx, _ := a.sideRect()
			visibleEditor := min(max(sx-ex, 0), ew)
			line += padTo(ansi.Truncate(mid, visibleEditor, ""), visibleEditor) + side[k]
		} else {
			line += mid
		}
		out[k] = line
	}
	return out
}

// editorLines renders the editor pane: gutter and highlighted text.
func (a *App) editorLines(w, h int) []string {
	return a.renderEditor(a.cur.Ed, w, h)
}

// renderEditor renders ed at w x h: gutter and highlighted text.
func (a *App) renderEditor(ed *editor.Editor, w, h int) []string {
	if ew, eh := ed.Size(); ew != w || eh != h {
		// The pane changed size without a layout pass (a panel appeared):
		// lay out now rather than draw at the old size.
		ed.SetSize(w, h)
	}
	gw := ed.GutterWidth()
	digits := gw - 3
	th := a.theme
	var bars map[int]barKind
	var exactChanged map[int]bool
	var tick [2]int // the lines the cursor's anchor (or section) spans
	tickStyle := th.Tick
	// A model has no gutter diff: HEAD's baseline holds only spec files, so
	// every word of one would read as added.
	if d, ok := a.ws.Docs()[ed.Path()]; ok && d.Ed == ed && !a.ws.IsModel(ed.Path()) {
		bars = a.gutterBars(ed.Path())
		exactChanged = a.gutterChangedLines(ed.Path())
		tick, tickStyle = a.cursorExtent(ed)
		spans, ghosts := a.changedWords(ed.Path())
		fs, fg := a.fixPreview(ed.Path())
		ed.SetExtra(append(spans, fs...))
		ed.SetGhosts(sortGhosts(append(ghosts, fg...)))
	}
	rows := ed.Screen()
	out := make([]string, 0, len(rows))
	var b strings.Builder
	for _, r := range rows {
		b.Reset()
		if !r.Valid {
			out = append(out, "") // past the end of the buffer: blank, no marker
			continue
		}
		num := strings.Repeat(" ", digits)
		if r.LineNo > 0 {
			num = padLeft(fmt.Sprint(r.LineNo), digits)
		}
		if r.CursorLine {
			b.WriteString(th.GutterCur.Render(num))
		} else {
			b.WriteString(th.Gutter.Render(num))
		}
		if l := r.Row.Line + 1; l >= tick[0] && l <= tick[1] && tick[0] > 0 {
			tickGlyph := a.theme.uiText("│")
			if th.ASCII {
				tickGlyph = "|"
			}
			b.WriteString(tickStyle.Render(tickGlyph))
		} else {
			b.WriteByte(' ')
		}
		b.WriteString(markerCell(th, r.Marker, bars[r.Row.Line+1], exactChanged[r.Row.Line+1]))
		b.WriteByte(' ')
		for _, run := range r.Runs {
			if run.Flags == 0 {
				b.WriteString(run.Text)
				continue
			}
			b.WriteString(th.Run(run.Flags).Render(run.Text))
		}
		out = append(out, b.String())
	}
	return out
}

// changedWords returns the words of path's buffer added since HEAD as
// highlight spans, and the removed ones as ghost text when they are shown
// (in anchors and prose alike).
func (a *App) changedWords(path string) ([]editor.Span, []editor.Ghost) {
	fe, ok := a.fileEditsOf(path, string(a.ws.Source(path)))
	if !ok {
		return nil, nil
	}
	var spans []editor.Span
	var ghosts []editor.Ghost
	for _, e := range fe.content {
		if e.end > e.start && len(e.added) > 0 {
			spans = append(spans, editor.Span{Start: e.start, End: e.end, Flags: editor.FAdded})
		}
		if a.showRemoved && e.ghost != "" {
			ghosts = append(ghosts, editor.Ghost{Off: e.ghostAt, Text: e.ghost})
		}
	}
	return spans, ghosts
}

// cursorExtent returns the first and last line of the anchor under ed's
// cursor (or, outside every anchor, its section) and the style of their
// tick; zero lines when there is neither. It reads the current analysis
// without reparsing.
func (a *App) cursorExtent(ed *editor.Editor) ([2]int, lipgloss.Style) {
	f := a.ws.File(ed.Path())
	if f == nil {
		return [2]int{}, a.theme.Tick
	}
	off := ed.Cursor()
	n := len(f.Source())
	if an := f.AnchorAt(off); an != nil && an.BodyRange.End <= n {
		return [2]int{f.Index().Line(an.LabelRange.Start), f.Index().Line(max(an.BodyRange.End-1, an.LabelRange.Start))}, a.theme.Tick
	}
	if s := f.SectionAt(off); s != nil && s.Range.End <= n {
		return [2]int{f.Index().Line(s.Range.Start), f.Index().Line(max(s.Range.End-1, s.Range.Start))}, a.theme.TickSection
	}
	return [2]int{}, a.theme.Tick
}

// markerCell renders the gutter's diagnostic marker, on the background of
// the proposal bar when the line has one.
func markerCell(th *Theme, s diag.Severity, bar barKind, exactChanged ...bool) string {
	letter, st := " ", lipgloss.NewStyle()
	switch s {
	case diag.Error:
		letter, st = "E", th.MarkError
	case diag.Warning:
		letter, st = "W", th.MarkWarn
	case diag.Hint:
		letter, st = "h", th.MarkHint
	}
	if pipe, ok := th.Pipe[bar]; ok {
		// A thin pipe, not a bar: prose edits and format-only lines. The
		// diagnostic letter wins the cell.
		if letter == " " {
			return pipe.Render(th.uiText("│"))
		}
		bar = barNone
	}
	if bar != barNone && len(exactChanged) > 0 && exactChanged[0] {
		// Preserve the broad extent fill while the centered pipe marks only
		// source lines that actually changed within the anchor.
		if letter != " " {
			return st.Background(th.Bar[bar].GetBackground()).Render(letter)
		}
		pipe := th.uiText("│")
		if th.ASCII {
			pipe = "|"
		}
		return th.Bar[bar].Render(pipe)
	}
	if bar != barNone {
		return th.Bar[bar].Render(letter)
	}
	if letter == " " {
		return letter
	}
	return st.Render(letter)
}

// padLeft right-aligns s in n cells.
func padLeft(s string, n int) string {
	if w := ansi.StringWidth(s); w < n {
		return strings.Repeat(" ", n-w) + s
	}
	return s
}

// padTo pads or truncates styled s to exactly n cells.
func padTo(s string, n int) string {
	w := ansi.StringWidth(s)
	switch {
	case w > n:
		return ansi.Truncate(s, n, "")
	case w < n:
		return s + strings.Repeat(" ", n-w)
	}
	return s
}
