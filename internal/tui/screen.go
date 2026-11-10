// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// screen is a full-window view. The App keeps a stack of screens (a
// router): the main editing screen at the bottom, others pushed over it
// (the help screen now; the proposal review screen in step 4b). The App
// draws the status line and the help bar under whichever screen is on top.
type screen interface {
	// context is the input context while the screen is on top.
	context(a *App) Context
	// key handles a key the keymap resolved (b) or did not (ok false).
	key(a *App, msg tea.KeyPressMsg, b Binding, ok bool) tea.Cmd
	// mouse handles a mouse message.
	mouse(a *App, msg tea.MouseMsg) tea.Cmd
	// view renders the screen's body: exactly h lines.
	view(a *App, w, h int) []string
}

// push opens s over the current screen.
func (a *App) push(s screen) {
	a.screens = append(a.screens, s)
}

// pop closes the top screen; the main screen is never popped.
func (a *App) pop() {
	if len(a.screens) > 1 {
		if _, ok := a.top().(*statusReferenceScreen); ok && (a.flashKind == statusFailure || a.flashKind == statusPartial) {
			a.flash = ""
			a.flashAccent = ""
		}
		if ref, ok := a.top().(*referenceScreen); ok {
			a.prompt, a.popup = ref.savedPrompt, ref.savedPopup
		}
		a.screens = a.screens[:len(a.screens)-1]
	}
}

// remove closes screen s wherever it is on the stack (the main screen
// stays).
func (a *App) remove(s screen) {
	for k := len(a.screens) - 1; k > 0; k-- {
		if a.screens[k] == s {
			a.screens = append(a.screens[:k], a.screens[k+1:]...)
			return
		}
	}
}

// top returns the screen on top.
func (a *App) top() screen {
	return a.screens[len(a.screens)-1]
}

// helpScreen lists every binding by context (^G).
type helpScreen struct {
	scroll int
	lines  []string
}

func newHelpScreen() *helpScreen {
	h := &helpScreen{}
	h.lines = append(h.lines, "spec ui: keys", "",
		"Keys are nano style: ^X is ctrl+X. Every common key is a ctrl key; alt keys (M-X is alt+X) are only",
		"aliases. ctrl+S is never bound (flow control on some terminals). PgUp/PgDn are fn+up/down on a Mac.",
		"Mouse: click places the cursor, drag selects, the wheel scrolls, ctrl+click or alt+click jumps,",
		"and resting over a $Name or § shows its preview.", "")
	for _, c := range Contexts {
		h.lines = append(h.lines, c.String())
		for _, b := range BindingsIn(c) {
			h.lines = append(h.lines, fmt.Sprintf("  %-28s %s", keyNames(b), b.Desc))
		}
		h.lines = append(h.lines, "")
	}
	return h
}

func (h *helpScreen) context(*App) Context { return CtxHelp }

func (h *helpScreen) key(a *App, _ tea.KeyPressMsg, b Binding, ok bool) tea.Cmd {
	if !ok {
		return nil
	}
	page := max(a.bodyHeight()-2, 1)
	switch b.Action {
	case ActCancel:
		a.pop()
	case ActPageUp:
		h.scroll -= page
	case ActPageDown:
		h.scroll += page
	case ActUp:
		h.scroll--
	case ActDown:
		h.scroll++
	}
	h.scroll = min(max(h.scroll, 0), max(len(h.lines)-a.bodyHeight(), 0))
	return nil
}

func (h *helpScreen) mouse(a *App, msg tea.MouseMsg) tea.Cmd {
	if w, ok := msg.(tea.MouseWheelMsg); ok {
		switch w.Button {
		case tea.MouseWheelUp:
			h.scroll = max(h.scroll-3, 0)
		case tea.MouseWheelDown:
			h.scroll = min(h.scroll+3, max(len(h.lines)-a.bodyHeight(), 0))
		}
	}
	return nil
}

func (h *helpScreen) view(a *App, w, hgt int) []string {
	out := make([]string, 0, hgt)
	for k := 0; k < hgt; k++ {
		i := h.scroll + k
		if i >= len(h.lines) {
			out = append(out, "")
			continue
		}
		line := ansi.Truncate(h.lines[i], w, "")
		if i == 0 || (i > 6 && !strings.HasPrefix(h.lines[i], " ") && h.lines[i] != "") {
			line = a.theme.PopupTitle.Render(line)
		}
		out = append(out, line)
	}
	return out
}
