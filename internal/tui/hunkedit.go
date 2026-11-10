// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/tui/editor"
)

// cursorScreen is a screen that places the terminal cursor itself.
type cursorScreen interface {
	cursor(a *App) *[2]int
}

// textEdit configures the small text editor: what it edits and what ^O
// does with the result.
type textEdit struct {
	// title is the first line; quote, when set, is the text being
	// replaced, shown above the editor; prompt is the line over the editor.
	title, quote, prompt string
	keepLabel            string
	// status is the status line's left side.
	status string
	// path names the buffer (no file is read or written).
	path string
	text string
	// done keeps the edited text; it reports whether the editor closed.
	done func(a *App, text string) bool
	// submit, when set instead of done, keeps the text and may start
	// work (the feedback that the model redoes an edit with).
	submit func(a *App, text string) tea.Cmd
	// cancel is the flash when ^X discards the edit.
	cancel string
}

// hunkEditScreen is a small editor for a short piece of text, drawn as a
// modal box over the screen below it, sized to its content: a hunk's
// replacement (e on a card or in the review), the commit message (c), the
// feedback for the model (f). ^O keeps the text, Esc (^X) discards it.
type hunkEditScreen struct {
	te textEdit
	ed *editor.Editor
	// top and left are the screen cell of the editor's first row and
	// column inside the box.
	top, left int
}

// hunkEditHeadMax bounds the lines of the replaced text shown above the
// editor.
const hunkEditHeadMax = 6

// newTextEditScreen opens the small editor as te says.
func newTextEditScreen(_ *App, te textEdit) *hunkEditScreen {
	s := &hunkEditScreen{te: te, ed: editor.New(te.path, []byte(te.text), editor.Options{})}
	s.ed.SetCursor(len(te.text))
	return s
}

// newHunkEditScreen edits hunk h of proposal p; done gets the new
// replacement (never the find text unchanged) and closes the editor.
func newHunkEditScreen(a *App, p *proposal.Proposal, h finding.Hunk, done func(a *App, text string) tea.Cmd) *hunkEditScreen {
	return newTextEditScreen(a, textEdit{
		title:     fmt.Sprintf("Edit %d in %s: the text that replaces", h.N, h.File),
		keepLabel: "Keep edit",
		quote:     h.Find,
		prompt:    "with (^O keeps it and applies it):",
		status:    fmt.Sprintf(" Edit hunk %d of %s: the replacement text", h.N, p.ID),
		path:      h.File,
		text:      h.Replace,
		submit: func(a *App, text string) tea.Cmd {
			if text == h.Find {
				a.setFlash("The replacement equals the text it replaces; n skips the hunk instead")
				return nil
			}
			return done(a, text)
		},
		cancel: fmt.Sprintf("Edit of hunk %d discarded", h.N),
	})
}

// newFeedbackScreen asks for feedback on an edit (quote is the text it
// replaces); send runs with the feedback once it is kept.
func newFeedbackScreen(a *App, title, quote string, send func(a *App, text string) tea.Cmd) *hunkEditScreen {
	return newTextEditScreen(a, textEdit{
		title:     title,
		keepLabel: "Send feedback",
		quote:     quote,
		prompt:    "What should change?",
		status:    " Feedback for the model",
		submit: func(a *App, text string) tea.Cmd {
			if strings.TrimSpace(text) == "" {
				a.setFlash("No feedback written: Esc cancels")
				return nil
			}
			a.pop()
			return send(a, strings.TrimSpace(text))
		},
		cancel: "No feedback sent",
	})
}

func (s *hunkEditScreen) context(*App) Context { return CtxHunkEdit }

// editActions are the editor actions the small editor runs.
var editActions = map[Action]bool{
	ActLineStart: true, ActLineEnd: true, ActPageUp: true, ActPageDown: true, ActRight: true, ActLeft: true,
	ActUp: true, ActDown: true, ActTop: true, ActBottom: true, ActWordLeft: true, ActWordRight: true,
	ActSelLeft: true, ActSelRight: true, ActSelUp: true, ActSelDown: true, ActSelLineStart: true, ActSelLineEnd: true,
	ActDeleteChar: true, ActBackspace: true, ActNewline: true, ActTab: true, ActUndo: true, ActRedo: true, ActCut: true,
}

func (s *hunkEditScreen) key(a *App, msg tea.KeyPressMsg, b Binding, ok bool) tea.Cmd {
	if ok {
		switch b.Action {
		case ActCancel:
			a.pop()
			a.setFlash("%s", s.te.cancel)
		case ActDone:
			if s.te.submit != nil {
				return s.te.submit(a, string(s.ed.Buffer().Bytes()))
			}
			s.te.done(a, string(s.ed.Buffer().Bytes()))
		}
		return nil
	}
	ed := s.ed
	eb, eok := lookupKey(CtxEditor, msg)
	if !eok {
		k := msg.Key()
		if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper|tea.ModHyper|tea.ModMeta) == 0 {
			ed.Type(k.Text)
		}
		return nil
	}
	if !editActions[eb.Action] {
		return nil
	}
	switch eb.Action {
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
	case ActDeleteChar:
		ed.Delete()
	case ActBackspace:
		ed.Backspace()
	case ActNewline:
		ed.Newline()
	case ActTab:
		ed.Tab()
	case ActUndo:
		ed.Undo()
	case ActRedo:
		ed.Redo()
	case ActCut:
		ed.Cut()
	}
	return nil
}

func (s *hunkEditScreen) mouse(a *App, msg tea.MouseMsg) tea.Cmd {
	m := msg.Mouse()
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			s.ed.Click(m.X-s.left+s.ed.GutterWidth(), m.Y-s.top)
		}
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelUp {
			s.ed.ScrollRows(-3)
		} else if msg.Button == tea.MouseWheelDown {
			s.ed.ScrollRows(3)
		}
	}
	return nil
}

// Bounds of the modal's text area.
const (
	modalMinWidth = 60
	modalMinRows  = 3
)

func (s *hunkEditScreen) view(a *App, w, h int) []string {
	th := a.theme
	var base []string
	if n := len(a.screens); n >= 2 && a.screens[n-1] == s {
		base = a.screens[n-2].view(a, w, h)
	}
	// The width: the longest line of the text or the quote, within the
	// screen.
	longest := 0
	for _, l := range strings.Split(s.te.text+"\n"+s.te.quote, "\n") {
		longest = max(longest, ansi.StringWidth(l))
	}
	inner := min(max(modalMinWidth, longest+2, ansi.StringWidth(s.te.title)), max(w-6, 20))
	var head []string
	for _, l := range wrapText(termtext.Clean(strings.TrimSpace(s.te.title)), inner) {
		head = append(head, th.PopupTitle.Render(l))
	}
	var quoted []string
	if s.te.quote != "" {
		for _, l := range wrapText(termtext.Clean(s.te.quote), max(inner-2, 1)) {
			quoted = append(quoted, th.DiffDel.Render("- "+l))
		}
	}
	if len(quoted) > hunkEditHeadMax {
		more := fmt.Sprintf(a.theme.uiText("  … %d more lines"), len(quoted)-hunkEditHeadMax+1)
		quoted = append(quoted[:hunkEditHeadMax-1], th.PopupDim.Render(more))
	}
	head = append(head, quoted...)
	if p := strings.TrimSpace(s.te.prompt); p != "" {
		head = append(head, th.PopupDim.Render(ansi.Truncate(p, inner, a.theme.uiText("…"))))
	}
	foot := th.PopupDim.Render(ansi.Truncate(fmt.Sprintf(a.theme.uiText("^O %s · Esc cancels · Enter is a new line"), s.keepLabel()), inner, ""))
	// The height: the text's rows, within the screen.
	gw := s.ed.GutterWidth()
	s.ed.SetSize(inner+gw, max(h, 1))
	rows := 0
	for l := 0; l < s.ed.Buffer().Index().LineCount(); l++ {
		rows += len(s.ed.Rows(l))
	}
	eh := min(max(rows+1, modalMinRows), max(h-len(head)-5, 1))
	s.ed.SetSize(inner+gw, eh)
	body := append([]string{}, head...)
	body = append(body, "")
	for _, r := range s.ed.Screen() {
		var b strings.Builder
		for _, run := range r.Runs {
			if run.Flags == 0 {
				b.WriteString(run.Text)
				continue
			}
			b.WriteString(th.Run(run.Flags).Render(run.Text))
		}
		body = append(body, padTo(b.String(), inner))
	}
	body = append(body, "", foot)
	for k, l := range body {
		body[k] = padTo(l, inner)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(th.accent).Padding(0, 1).Render(strings.Join(body, "\n"))
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	bx, by := max((w-bw)/2, 0), max((h-bh)/3, 0)
	s.top = by + 1 + len(head) + 1
	s.left = bx + 2
	for len(base) < h {
		base = append(base, "")
	}
	canvas := lipgloss.NewCanvas(w, h)
	canvas.Compose(lipgloss.NewCompositor(lipgloss.NewLayer(strings.Join(base[:h], "\n")), lipgloss.NewLayer(box).X(bx).Y(by).Z(1)))
	out := strings.Split(canvas.Render(), "\n")
	for len(out) < h {
		out = append(out, "")
	}
	return out[:h]
}

func (s *hunkEditScreen) cursor(a *App) *[2]int {
	if x, y, ok := s.ed.CursorCell(); ok {
		return &[2]int{s.left + x - s.ed.GutterWidth(), s.top + y}
	}
	return nil
}

func (s *hunkEditScreen) keepLabel() string {
	if s.te.keepLabel == "" {
		return "Keep text"
	}
	return s.te.keepLabel
}

func (s *hunkEditScreen) helperBindings(*App) []Binding {
	return []Binding{
		{Key: "^O", Label: s.keepLabel(), Action: ActDone}, {Key: "Esc", Label: "Cancel", Action: ActCancel},
		{Key: "^Z", Label: "Undo", Action: ActUndo}, {Key: "^Y", Label: "Redo", Action: ActRedo},
	}
}
