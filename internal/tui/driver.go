// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/proposal"
)

// Driver runs an App in a Bubble Tea program without a terminal, as the
// UI scenes (tools/scenes) and scenario tests do: keys go in as messages,
// background jobs and their commands run for real, and frames come out as
// the ANSI text the App renders. Every method waits until the program has
// handled what it sent.
type Driver struct {
	a         *App
	p         driverProgram
	done      chan struct{}
	runErr    error // read only after done closes
	closeOnce sync.Once
	w, h      int
}

// driverProgram is the lifecycle and message surface used by Driver.
type driverProgram interface {
	Send(tea.Msg)
	Quit()
	Kill()
}

// runProgram publishes completion even when a test callback calls Goexit.
// Bubble Tea defers context cancellation, but Goexit skips its normal renderer
// shutdown; Kill finishes that cleanup before observers may release App state.
func runProgram(p *tea.Program, done chan struct{}, runErr *error) {
	defer close(done)
	defer p.Kill()
	*runErr = errors.New("the program exited without returning")
	_, *runErr = p.Run()
}

// stopProgram first requests a normal exit, then forces cancellation if it
// takes too long. Kill initiates shutdown; it does not join Program.Run.
// Always join Run before the caller releases mutable model resources.
func stopProgram(p driverProgram, done <-chan struct{}, timeout time.Duration) {
	quitDone := make(chan struct{})
	go func() {
		p.Quit() // Send can block while Update or View is handling a message.
		close(quitDone)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		p.Kill()
		<-done
	}
	<-quitDone
}

// driverProbe runs fn on the program's goroutine (from its message
// filter), so the driver reads and changes the App without racing it.
type driverProbe struct {
	fn   func(a *App)
	done chan struct{}
}

// DriverTimeout bounds probes and the graceful phase of Driver shutdown.
const DriverTimeout = 10 * time.Second

// NewDriver opens an App with opts and runs it at w x h cells.
func NewDriver(opts Options, w, h int) (*Driver, error) {
	a, err := New(opts)
	if err != nil {
		return nil, err
	}
	if opts.Watch {
		// As Run does: the watcher's first wait is in Init.
		a.startWatcher()
	}
	filter := func(_ tea.Model, m tea.Msg) tea.Msg {
		if p, ok := m.(driverProbe); ok {
			p.fn(a)
			close(p.done)
			return nil
		}
		return m
	}
	p := tea.NewProgram(a,
		tea.WithInput(&bytes.Buffer{}), tea.WithOutput(io.Discard),
		tea.WithoutSignals(), tea.WithoutRenderer(),
		tea.WithWindowSize(w, h), tea.WithFilter(filter))
	d := &Driver{a: a, p: p, done: make(chan struct{}), w: w, h: h}
	go runProgram(p, d.done, &d.runErr)
	if err := d.Send(tea.WindowSizeMsg{Width: w, Height: h}); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Close stops the program and the App's background work.
func (d *Driver) Close() {
	d.close(DriverTimeout)
}

func (d *Driver) close(timeout time.Duration) {
	d.closeOnce.Do(func() {
		stopProgram(d.p, d.done, timeout)
		if d.a.watcher != nil {
			d.a.watcher.Close()
		}
		d.a.Close()
	})
}

// do runs fn on the program's goroutine and waits for it.
func (d *Driver) do(fn func(a *App)) error {
	done := make(chan struct{})
	d.p.Send(driverProbe{fn: fn, done: done})
	select {
	case <-done:
		return nil
	case <-d.done:
		return fmt.Errorf("the program ended: %v", d.runErr)
	case <-time.After(DriverTimeout):
		return errors.New("the program did not handle a probe in time")
	}
}

// Send sends msgs and waits until the program has handled them.
func (d *Driver) Send(msgs ...tea.Msg) error {
	for _, m := range msgs {
		d.p.Send(m)
	}
	return d.do(func(*App) {})
}

// Press presses keys, each a Bubble Tea key string ("ctrl+p", "shift+tab",
// "esc", "y").
func (d *Driver) Press(keys ...string) error {
	for _, k := range keys {
		msg, err := ParseKey(k)
		if err != nil {
			return err
		}
		if err := d.Send(msg); err != nil {
			return err
		}
	}
	return nil
}

// Type types text ("\n" is Enter).
func (d *Driver) Type(text string) error {
	for _, r := range text {
		var msg tea.KeyPressMsg
		switch r {
		case '\n':
			msg = tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
		case ' ':
			msg = tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "})
		default:
			msg = tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)})
		}
		if err := d.Send(msg); err != nil {
			return err
		}
	}
	return nil
}

// Resize changes the terminal size.
func (d *Driver) Resize(w, h int) error {
	d.w, d.h = w, h
	return d.Send(tea.WindowSizeMsg{Width: w, Height: h})
}

// Cond is what Wait waits for; every field set must hold.
type Cond struct {
	// Idle: no job runs, no decision waits for .spec/lock, and no commit
	// message is being drafted.
	Idle bool
	// Baseline: the HEAD baseline has loaded (Options.Baseline).
	Baseline bool
	// Stage: the running cascade stage is this one ("gate", "triage",
	// "check").
	Stage string
	// Text: the plain frame contains it.
	Text string
	// Cards: the Checks sidebar holds at least this many cards.
	Cards int
	// Popup: a hover popup is open.
	Popup bool
}

// String describes the condition, for an error.
func (c Cond) String() string {
	var parts []string
	if c.Idle {
		parts = append(parts, "idle")
	}
	if c.Baseline {
		parts = append(parts, "the baseline")
	}
	if c.Stage != "" {
		parts = append(parts, "stage "+c.Stage+" running")
	}
	if c.Text != "" {
		parts = append(parts, fmt.Sprintf("text %q", c.Text))
	}
	if c.Cards > 0 {
		parts = append(parts, fmt.Sprintf("%d cards", c.Cards))
	}
	if c.Popup {
		parts = append(parts, "a popup")
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}

// holds reports whether c holds for a.
func (c Cond) holds(a *App) bool {
	if c.Idle {
		if a.job != nil || a.wait != nil {
			return false
		}
		if cs, ok := a.top().(*commitScreen); ok && cs.drafting {
			return false
		}
	}
	if c.Baseline && a.ws.Base == nil {
		return false
	}
	if c.Stage != "" {
		r := a.activeRun()
		if r == nil || r.current == nil || r.current.Name != c.Stage || r.current.Outcome != proposal.OutcomeRunning {
			return false
		}
	}
	if c.Cards > 0 && len(a.side.cards) < c.Cards {
		return false
	}
	if c.Popup && a.popup == nil {
		return false
	}
	if c.Text != "" {
		content, _ := a.render()
		if !strings.Contains(ansi.Strip(content), c.Text) {
			return false
		}
	}
	return true
}

// Wait waits until c holds; after DriverTimeout it fails with the plain
// frame.
func (d *Driver) Wait(c Cond) error {
	deadline := time.Now().Add(DriverTimeout)
	for {
		ok := false
		if err := d.do(func(a *App) { ok = c.holds(a) }); err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			f, _ := d.Frame()
			return fmt.Errorf("timed out waiting for %s; the frame:\n%s", c, ansi.Strip(f.Content))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Frame is one rendered frame.
type Frame struct {
	// Content is the frame as the App renders it: lines of ANSI text,
	// at most W cells wide and H lines.
	Content string
	W, H    int
	// Cursor is the terminal cursor's cell, nil when hidden.
	Cursor *[2]int
	// Light says the frame was drawn with the light theme.
	Light bool
	// Flash is the status line's message, for a caption.
	Flash string
}

// Frame renders the current frame.
func (d *Driver) Frame() (Frame, error) {
	var f Frame
	err := d.do(func(a *App) {
		content, cur := a.render()
		f = Frame{Content: content, W: a.width, H: a.height, Cursor: cur, Light: !a.theme.Dark, Flash: a.flash}
	})
	return f, err
}

// ParseKey builds a key press from a Bubble Tea key string such as
// "ctrl+t", "alt+,", "shift+tab", "enter" or "y".
func ParseKey(s string) (tea.KeyPressMsg, error) {
	var k tea.Key
	parts := strings.Split(s, "+")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		// "alt++" style: the key itself is '+'.
		parts = append(parts[:len(parts)-2], "+")
	}
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl":
			k.Mod |= tea.ModCtrl
		case "alt":
			k.Mod |= tea.ModAlt
		case "shift":
			k.Mod |= tea.ModShift
		default:
			return tea.KeyPressMsg{}, fmt.Errorf("key %q: unknown modifier %q", s, p)
		}
	}
	last := parts[len(parts)-1]
	if code, ok := namedKeys[last]; ok {
		k.Code = code
		if code == tea.KeySpace && k.Mod == 0 {
			k.Text = " "
		}
		return tea.KeyPressMsg(k), nil
	}
	r := []rune(last)
	if len(r) != 1 {
		return tea.KeyPressMsg{}, fmt.Errorf("key %q: unknown key %q", s, last)
	}
	k.Code = r[0]
	if k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		k.Text = last
	}
	return tea.KeyPressMsg(k), nil
}

// namedKeys are the key names ParseKey knows besides single characters.
var namedKeys = map[string]rune{
	"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "backspace": tea.KeyBackspace,
	"delete": tea.KeyDelete, "up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
	"home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "space": tea.KeySpace,
	"f1": tea.KeyF1, "f2": tea.KeyF2, "f3": tea.KeyF3, "f8": tea.KeyF8, "f12": tea.KeyF12,
}

// paneCell returns the screen cell of the first occurrence of text in the
// body's columns x0..x0+w of the current frame.
func (a *App) paneCell(text string, x0, w int) (x, y int, ok bool) {
	content, _ := a.render()
	rows := strings.Split(ansi.Strip(content), "\n")
	for y, row := range rows[:min(len(rows), a.bodyHeight())] {
		pane := ansi.Cut(row, x0, x0+w)
		if i := strings.Index(pane, text); i >= 0 {
			return x0 + ansi.StringWidth(pane[:i]), y, true
		}
	}
	return 0, 0, false
}

// EditorCell returns the screen cell of the first occurrence of text in the
// editor pane.
func (d *Driver) EditorCell(text string) (x, y int, err error) {
	ok := false
	if derr := d.do(func(a *App) {
		ex, _, ew, _ := a.editorRect()
		x, y, ok = a.paneCell(text, ex, ew)
	}); derr != nil {
		return 0, 0, derr
	}
	if !ok {
		f, _ := d.Frame()
		return 0, 0, fmt.Errorf("%q is not in the editor pane; the frame:\n%s", text, ansi.Strip(f.Content))
	}
	return x, y, nil
}

// OutlineCell returns the screen cell of the first occurrence of text in the
// outline pane.
func (d *Driver) OutlineCell(text string) (x, y int, err error) {
	ok := false
	if derr := d.do(func(a *App) { x, y, ok = a.paneCell(text, 0, max(a.outlineWidth()-1, 0)) }); derr != nil {
		return 0, 0, derr
	}
	if !ok {
		f, _ := d.Frame()
		return 0, 0, fmt.Errorf("%q is not in the outline; the frame:\n%s", text, ansi.Strip(f.Content))
	}
	return x, y, nil
}

// HoverOn rests the pointer on the first occurrence of text in the editor pane
// and waits for the popup.
func (d *Driver) HoverOn(text string) error {
	x, y, err := d.EditorCell(text)
	if err != nil {
		return err
	}
	if err := d.Send(tea.MouseMotionMsg{X: x, Y: y}); err != nil {
		return err
	}
	return d.Wait(Cond{Popup: true})
}

// HoverStatusCounter moves the pointer onto one of the three status counters.
// Counter is 1 (diagnostics), 2 (proposals), or 3 (other review work).
func (d *Driver) HoverStatusCounter(counter int) error {
	if counter < 1 || counter > 3 {
		return fmt.Errorf("status counter %d is outside 1..3", counter)
	}
	var x, y int
	if err := d.do(func(a *App) {
		x = a.width - 16 + []int{3, 8, 13}[counter-1]
		y = a.bodyHeight()
	}); err != nil {
		return err
	}
	return d.Send(tea.MouseMotionMsg{X: x, Y: y})
}
