// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/x/ansi"
)

// promptKind is what a prompt asks.
type promptKind int

const (
	promptFind promptKind = iota
	promptGoTo
	promptSave
	promptConflict
	// promptSaveFirst asks to save modified buffers before a check.
	promptSaveFirst
	// promptReject asks for the reason of a rejection.
	promptReject
)

// prompt is the one-line question that replaces the status line, nano
// style.
type prompt struct {
	kind  promptKind
	label string
	input string
	// path and disk belong to a conflict prompt.
	path string
	disk []byte
	// then runs after promptSaveFirst saved the buffers.
	then func() tea.Cmd
	// review is the review a reject prompt answers for (the top screen
	// may change meanwhile).
	review *reviewScreen
	// reject is the proposal a reject prompt from the sidebar rejects.
	reject string
}

// newTextPrompt returns a prompt that reads a line of text, starting with
// def.
func newTextPrompt(kind promptKind, label, def string) *prompt {
	return &prompt{kind: kind, label: label, input: def}
}

// context returns the prompt's input context.
func (p *prompt) context() Context {
	switch p.kind {
	case promptSave:
		return CtxConfirm
	case promptConflict:
		return CtxConflict
	case promptSaveFirst:
		return CtxSaveFirst
	}
	return CtxPrompt
}

// key handles a key while the prompt is open.
func (p *prompt) key(a *App, msg tea.KeyPressMsg, b Binding, ok bool) tea.Cmd {
	switch p.kind {
	case promptSave:
		if !ok {
			return nil
		}
		switch b.Action {
		case ActYes:
			a.prompt = nil
			return a.save(true)
		case ActNo:
			a.prompt = nil
			if a.discard == nil {
				a.discard = map[string]bool{}
			}
			a.discard[a.cur.Ed.Path()] = true
			return a.exit()
		case ActCancel:
			a.prompt = nil
			a.discard = nil
			a.setFlash("Cancelled")
		}
		return nil
	case promptConflict:
		return a.conflictKey(b, ok)
	case promptSaveFirst:
		if !ok {
			return nil
		}
		switch b.Action {
		case ActYes:
			a.prompt = nil
			if !a.saveAll() {
				return nil
			}
			return p.then()
		case ActCancel:
			a.prompt = nil
			a.setFlash("Cancelled")
		}
		return nil
	}
	if !ok {
		k := msg.Key()
		if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
			p.input += k.Text
		}
		return nil
	}
	switch b.Action {
	case ActCancel:
		a.prompt = nil
		a.setFlash("Cancelled")
	case ActBackspace:
		if p.input != "" {
			_, n := utf8.DecodeLastRuneInString(p.input)
			p.input = p.input[:len(p.input)-n]
		}
	case ActAccept:
		a.prompt = nil
		return p.accept(a)
	}
	return nil
}

// accept acts on a text prompt's answer.
func (p *prompt) accept(a *App) tea.Cmd {
	answer := strings.TrimSpace(p.input)
	switch p.kind {
	case promptReject:
		if answer == "" {
			a.prompt = p
			a.setFlash("A rejection needs a reason (^C keeps the proposal pending)")
			return nil
		}
		switch {
		case p.reject != "":
			return a.rejectProposal(p.reject, answer)
		case p.review != nil:
			return a.rejectProposal(p.review.p.ID, answer)
		}
		return nil
	case promptFind:
		// An empty answer searches for the last pattern again (nano).
		if p.input != "" {
			a.query = p.input
		}
		a.findNext()
	case promptGoTo:
		n, err := strconv.Atoi(strings.TrimSpace(p.input))
		if err != nil || n < 1 {
			a.setFlash("Invalid line number")
			return nil
		}
		from := a.here()
		a.cur.Ed.GotoLine(n)
		a.back = append(a.back, from)
		a.fwd = nil
	}
	return nil
}

// view renders the prompt line and returns the cursor column.
func (p *prompt) view(a *App, w int) (string, int) {
	th := a.theme
	switch p.kind {
	case promptSave, promptConflict, promptSaveFirst:
		s := padTo(th.Prompt.Render(p.label), w)
		return s, min(ansi.StringWidth(p.label), w-1)
	}
	head := p.label + ": "
	text := head + p.input
	cx := ansi.StringWidth(text)
	if cx >= w {
		// Show the end of a long answer.
		text = ansi.TruncateLeft(text, cx-w+1, "")
		cx = w - 1
	}
	return padTo(th.Prompt.Render(text), w), cx
}

// ---- files changed on disk ----

// startConflict asks what to do about path, which changed on disk while
// its buffer is modified.
func (a *App) startConflict(path string) {
	disk, err := os.ReadFile(filepath.Join(a.ws.Root, filepath.FromSlash(path)))
	if err != nil {
		a.setFlash("%s changed on disk and cannot be read: %v", path, err)
		return
	}
	if d, ok := a.ws.Docs()[path]; ok && d != a.cur {
		_ = a.open(Location{Path: path, Off: d.Ed.Cursor()})
	}
	a.popup = nil
	a.list = nil
	a.prompt = &prompt{kind: promptConflict, path: path, disk: disk,
		label: path + " changed on disk while you were editing it."}
}

// conflictKey resolves a conflict prompt.
func (a *App) conflictKey(b Binding, ok bool) tea.Cmd {
	p := a.prompt
	if !ok {
		return nil
	}
	d, open := a.ws.Docs()[p.path]
	if !open {
		a.prompt = nil
		return nil
	}
	switch b.Action {
	case ActKeepMine:
		a.ws.KeepMine(d, p.disk)
		a.prompt = nil
		a.popup = nil
		a.setFlash("Kept your version of %s; ^O overwrites the file", p.path)
	case ActTakeTheirs:
		a.ws.TakeDisk(d, p.disk)
		a.outline.rebuild(a.ws)
		a.reconcileBuffered()
		a.prompt = nil
		a.popup = nil
		a.setFlash("Loaded %s from disk", p.path)
	case ActShowDiff:
		a.showDiff(d.Ed.Path(), string(p.disk), string(d.Ed.Buffer().Bytes()))
	case ActCancel:
		// The buffer keeps its old base, so the next save finds the
		// change again and asks.
		a.prompt = nil
		a.popup = nil
		a.setFlash("%s changed on disk; ^O asks again", p.path)
	}
	return nil
}

// showDiff opens a popup with the unified diff from disk to the buffer.
func (a *App) showDiff(path, disk, mine string) {
	diff := udiff.Unified(path+" (on disk)", path+" (your buffer)", disk, mine)
	if diff == "" {
		diff = "no differences"
	}
	th := a.theme
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			l = th.OutlineInv.Render(l)
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
			l = th.MarkError.UnsetBold().Render(l)
		case strings.HasPrefix(l, "@@"):
			l = th.PopupDim.Render(l)
		}
		lines = append(lines, ansi.Truncate(l, a.width-6, a.theme.uiText("…")))
	}
	p := &popup{lines: lines}
	a.place(p, 0, 0)
	p.x = max((a.width-p.boxW)/2, 0)
	p.y = 0
	a.popup = p
	a.setFlash("%s", fmt.Sprintf("Diff of %s: K keeps yours, T takes the file", path))
}
