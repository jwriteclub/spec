// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/cli"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// A Scenario drives `spec ui` in-process through a scene's fixture and
// asserts on behaviour only: the input context and the pane with the
// keyboard, the help bar's labels, flash text, the buffer and cursor, the
// sidebar's cards, the proposal store, and the frame's structure
// (ExpectLayout). It asserts nothing about colours, cell styles or pixel
// layout, so a redesign of the look leaves it alone.
//
// Every step is recorded; a failure prints the step trail, the plain
// frame, the App's state and what differed. ExpectLayout runs after every
// step.
type Scenario struct {
	t      *testing.T
	sess   *scenes.Session
	d      *tui.Driver
	scene  scenes.Scene
	w, h   int
	trail  []string
	layout bool
}

// scenarioConfig is what the options of Open set.
type scenarioConfig struct {
	scene  scenes.Scene
	size   scenes.Size
	tweaks []func(*tui.Options)
	layout bool
}

// Opt configures a scenario.
type Opt func(*scenarioConfig)

// At opens the UI on anchor name (without $), centred.
func At(name string) Opt { return func(c *scenarioConfig) { c.scene.Open = tui.Target{Name: name} } }

// Size sets the terminal size (default 140x40: the sidebar has a column).
func Size(w, h int) Opt { return func(c *scenarioConfig) { c.size = scenes.Size{W: w, H: h} } }

// Edits makes uncommitted working-tree edits before the UI opens.
func Edits(es ...scenes.Edit) Opt {
	return func(c *scenarioConfig) { c.scene.Edits = append(c.scene.Edits, es...) }
}

// Agents files agents' proposals before the UI opens.
func Agents(as ...scenes.AgentChange) Opt {
	return func(c *scenarioConfig) { c.scene.Agents = append(c.scene.Agents, as...) }
}

// Replies are the canned model replies, in order within each purpose.
func Replies(f infer.CannedFile) Opt { return func(c *scenarioConfig) { c.scene.Replies = f } }

// Cascade configures the gate and triage stages.
func Cascade() Opt { return func(c *scenarioConfig) { c.scene.Cascade = true } }

// Watch watches the project and .spec/proposals, as `spec ui` does.
func Watch() Opt {
	return Tweak(func(o *tui.Options) { o.Watch, o.WatchProposals = true, true })
}

// Tweak adjusts the UI's options.
func Tweak(fn func(*tui.Options)) Opt {
	return func(c *scenarioConfig) { c.tweaks = append(c.tweaks, fn) }
}

// NoLayoutCheck skips the ExpectLayout that runs after every step (for a
// scenario that pins a known layout fault in its own assertion).
func NoLayoutCheck() Opt { return func(c *scenarioConfig) { c.layout = false } }

// needScenario skips when git is missing or under -short.
func needScenario(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if testing.Short() {
		t.Skip("scenarios run the UI on git fixtures")
	}
}

// Open opens the UI on fixture fx (scenes.Demo, scenes.Lensd,
// scenes.LensdFormatted) and waits for the HEAD baseline. The scenario
// runs in parallel with the others.
func Open(t *testing.T, fx scenes.Fixture, opts ...Opt) *Scenario {
	t.Helper()
	return open(t, scenes.Scene{Name: t.Name(), Fixture: fx}, opts)
}

// FromScene opens the UI as scene name of the registry starts it, at its
// first size; Play runs its steps.
func FromScene(t *testing.T, name string, opts ...Opt) *Scenario {
	t.Helper()
	sc, ok := scenes.Find(name)
	if !ok {
		t.Fatalf("no scene %q", name)
	}
	return open(t, sc, opts)
}

// running bounds the scenarios that run at once: each starts git
// processes and a Bubble Tea program, and the laptop swaps (and git has
// crashed) when dozens start together under -race.
var running = make(chan struct{}, 4)

func open(t *testing.T, sc scenes.Scene, opts []Opt) *Scenario {
	t.Helper()
	needScenario(t)
	t.Parallel()
	running <- struct{}{}
	t.Cleanup(func() { <-running })
	c := scenarioConfig{scene: sc, size: scenes.Size140, layout: true}
	if len(sc.Sizes) > 0 {
		c.size = sc.Sizes[0]
	}
	for _, o := range opts {
		o(&c)
	}
	sess, err := scenes.Start(c.scene, scenes.Variant{Size: c.size, Theme: scenes.Dark}, scenes.Options{
		TempDir: t.TempDir(),
		Tweak: func(o *tui.Options) {
			for _, fn := range c.tweaks {
				fn(o)
			}
		},
	})
	if err != nil {
		t.Fatalf("open %s: %v", sc.Fixture, err)
	}
	t.Cleanup(sess.Close)
	s := &Scenario{t: t, sess: sess, d: sess.Driver, scene: c.scene, w: c.size.W, h: c.size.H, layout: c.layout}
	s.trail = append(s.trail, fmt.Sprintf("open %s at %dx%d", sc.Fixture, c.size.W, c.size.H))
	s.afterStep()
	return s
}

// Deliver hands the UI a watcher's report (tui.FilesChanged,
// tui.ProposalsChanged) in a chosen order, for a scenario opened without
// Watch.
func (s *Scenario) Deliver(label string, w tui.Watched) {
	s.t.Helper()
	s.step("deliver "+label, s.d.Deliver(w))
}

// Reopen closes the UI and opens it again on the project (a restart).
func (s *Scenario) Reopen() {
	s.t.Helper()
	err := s.sess.Reopen()
	s.d = s.sess.Driver
	s.step("restart spec ui", err)
}

// Dir is the project directory.
func (s *Scenario) Dir() string { return s.sess.Dir }

// ---- steps ----

// step records a step and fails on err.
func (s *Scenario) step(label string, err error) {
	s.t.Helper()
	s.trail = append(s.trail, label)
	if err != nil {
		s.fail("%v", err)
	}
	s.afterStep()
}

// afterStep runs the checks every step gets.
func (s *Scenario) afterStep() {
	s.t.Helper()
	if s.layout {
		s.ExpectLayout()
	}
}

// Press presses keys ("ctrl+p", "shift+tab", "esc", "y").
func (s *Scenario) Press(keys ...string) {
	s.t.Helper()
	s.step("press "+strings.Join(keys, " "), s.d.Press(keys...))
}

// Type types text ("\n" is Enter).
func (s *Scenario) Type(text string) {
	s.t.Helper()
	s.step(fmt.Sprintf("type %q", text), s.d.Type(text))
}

// Find moves the cursor to the next match of text (^W text Enter).
func (s *Scenario) Find(text string) {
	s.t.Helper()
	err := s.d.Press("ctrl+w")
	if err == nil {
		err = s.d.Type(text)
	}
	if err == nil {
		err = s.d.Press("enter")
	}
	s.step(fmt.Sprintf("find %q", text), err)
}

// Select selects n characters from the cursor (shift+right n times).
func (s *Scenario) Select(n int) {
	s.t.Helper()
	keys := make([]string, n)
	for k := range keys {
		keys[k] = "shift+right"
	}
	s.step(fmt.Sprintf("select %d", n), s.d.Press(keys...))
}

// Replace finds old and types new over it.
func (s *Scenario) Replace(old, new string) {
	s.t.Helper()
	s.Find(old)
	s.Select(len([]rune(old)))
	s.Type(new)
}

// Resize changes the terminal size.
func (s *Scenario) Resize(w, h int) {
	s.t.Helper()
	s.w, s.h = w, h
	s.step(fmt.Sprintf("resize %dx%d", w, h), s.d.Resize(w, h))
}

// WaitFor waits until c holds (a job done, a stage running, cards in).
func (s *Scenario) WaitFor(c tui.Cond) {
	s.t.Helper()
	s.step("wait for "+c.String(), s.d.Wait(c))
}

// Idle waits until no job runs.
func (s *Scenario) Idle() {
	s.t.Helper()
	s.WaitFor(tui.Cond{Idle: true})
}

// WaitUntil waits until cond holds on the App's state.
func (s *Scenario) WaitUntil(what string, cond func(st tui.State) bool) {
	s.t.Helper()
	deadline := time.Now().Add(tui.DriverTimeout)
	for {
		st := s.State()
		if cond(st) {
			s.step("wait until "+what, nil)
			return
		}
		if time.Now().After(deadline) {
			s.trail = append(s.trail, "wait until "+what)
			s.fail("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Shell runs a command as an agent or Chris would in a second terminal,
// in the project directory, and returns its output; it fails the
// scenario when the command fails. "spec" runs the CLI in-process with
// the scenario's user config (the canned provider); anything else runs
// as a process with the fixture's git environment.
func (s *Scenario) Shell(cmd string, args ...string) string {
	s.t.Helper()
	out, code := s.ShellCode(cmd, args...)
	if code != 0 {
		s.fail("%s %s exited %d:\n%s", cmd, strings.Join(args, " "), code, out)
	}
	return out
}

// ShellCode is Shell that returns the exit code instead of failing.
func (s *Scenario) ShellCode(cmd string, args ...string) (string, int) {
	s.t.Helper()
	label := "shell: " + cmd + " " + strings.Join(args, " ")
	var out bytes.Buffer
	code := 0
	if cmd == "spec" {
		code = cli.Run(args, cli.Env{
			Stdout: &out, Stderr: &out, Cwd: s.sess.Dir, Getenv: s.sess.Getenv,
			Git: baseline.Loader{Env: scenes.GitEnv()},
			Now: func() time.Time { return scenes.Clock },
		})
	} else {
		c := exec.Command(cmd, args...)
		c.Dir = s.sess.Dir
		c.Env = append(os.Environ(), scenes.GitEnv()...)
		c.Stdout, c.Stderr = &out, &out
		if err := c.Run(); err != nil {
			code = -1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
	}
	s.step(fmt.Sprintf("%s (exit %d)", label, code), nil)
	return out.String(), code
}

// WriteFile writes a project file as another program would (by rename
// when rename is set, else in place).
func (s *Scenario) WriteFile(name, text string, rename bool) {
	s.t.Helper()
	path := filepath.Join(s.sess.Dir, name)
	var err error
	if rename {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, []byte(text), 0o644); err == nil {
			err = os.Rename(tmp, path)
		}
	} else {
		var f *os.File
		if f, err = os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
			_, err = f.WriteString(text)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
	}
	s.step(fmt.Sprintf("write %s on disk", name), err)
}

// ReadFile reads a project file from disk.
func (s *Scenario) ReadFile(name string) string {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(s.sess.Dir, name))
	if err != nil {
		s.fail("read %s: %v", name, err)
	}
	return string(b)
}

// Play runs the steps of the scene the scenario was opened from.
func (s *Scenario) Play() {
	s.t.Helper()
	for k, st := range s.scene.Steps {
		s.step(fmt.Sprintf("scene step %d: %s", k+1, st.StepText()), s.sess.Step(st))
	}
}

// ---- reading ----

// State is the App's semantic state now.
func (s *Scenario) State() tui.State {
	s.t.Helper()
	st, err := s.d.State()
	if err != nil {
		s.fail("state: %v", err)
	}
	return st
}

// Frame is the plain frame, one string per row.
func (s *Scenario) Frame() []string {
	s.t.Helper()
	f, err := s.d.Frame()
	if err != nil {
		s.fail("frame: %v", err)
	}
	return strings.Split(ansi.Strip(f.Content), "\n")
}

// Proposals lists the project's proposal store, oldest first.
func (s *Scenario) Proposals() []*proposal.Proposal {
	s.t.Helper()
	// Decisions are stamped with the scenario's fixed clock; list on that
	// clock too, or the wall clock expires their receipts a week later.
	st := proposal.NewStore(s.sess.Dir)
	st.Now = func() time.Time { return scenes.Clock }
	ps, err := st.List()
	if err != nil {
		s.fail("proposals: %v", err)
	}
	return ps
}

// ---- expectations ----

// fail stops the scenario with the trail, the frame, the state and why.
func (s *Scenario) fail(format string, args ...any) {
	s.t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nsteps:\n", fmt.Sprintf(format, args...))
	for k, l := range s.trail {
		fmt.Fprintf(&b, "  %2d. %s\n", k+1, l)
	}
	if f, err := s.d.Frame(); err == nil {
		b.WriteString("\nframe:\n")
		for k, l := range strings.Split(ansi.Strip(f.Content), "\n") {
			fmt.Fprintf(&b, "  %2d|%s\n", k, strings.TrimRight(l, " "))
		}
	}
	if st, err := s.d.State(); err == nil {
		b.WriteString("\nstate:\n" + st.Summary())
	}
	s.t.Fatal(b.String())
}

// Expect fails unless cond holds on the state; what says what was
// expected.
func (s *Scenario) Expect(what string, cond func(st tui.State) bool) {
	s.t.Helper()
	if !cond(s.State()) {
		s.fail("expected %s", what)
	}
}

// ExpectContext expects the input context.
func (s *Scenario) ExpectContext(want tui.Context) {
	s.t.Helper()
	if got := s.State().Context; got != want {
		s.fail("context: got %s, want %s", got, want)
	}
}

// ExpectFocus expects the pane or screen with the keyboard: "editor",
// "outline", "sidebar", "review", "commit", "help", "text-edit".
func (s *Scenario) ExpectFocus(want string) {
	s.t.Helper()
	if got := s.State().Focus; got != want {
		s.fail("focus: got %s, want %s", got, want)
	}
}

// ExpectFlash expects the flash to match the regular expression re.
func (s *Scenario) ExpectFlash(re string) {
	s.t.Helper()
	got := s.State().Flash
	if !regexp.MustCompile(re).MatchString(got) {
		s.fail("flash: got %q, want a match of %q", got, re)
	}
}

// ExpectFlashText expects the flash to be exactly text.
func (s *Scenario) ExpectFlashText(text string) {
	s.t.Helper()
	if got := s.State().Flash; got != text {
		s.fail("flash: got %q, want %q", got, text)
	}
}

// ExpectStatus expects the status line to contain text.
func (s *Scenario) ExpectStatus(text string) {
	s.t.Helper()
	if got := s.State().Status; !strings.Contains(got, text) {
		s.fail("status line: got %q, want it to contain %q", strings.TrimSpace(got), text)
	}
}

// ExpectHelp expects these help bar entries ("^R Commit",
// "⇧Tab Next Pane").
func (s *Scenario) ExpectHelp(entries ...string) {
	s.t.Helper()
	have := s.State().HelpText()
	for _, e := range entries {
		if !slices.Contains(have, e) {
			s.fail("help bar: no %q in %q", e, have)
		}
	}
}

// ExpectNoHelp expects none of these help bar entries.
func (s *Scenario) ExpectNoHelp(entries ...string) {
	s.t.Helper()
	have := s.State().HelpText()
	for _, e := range entries {
		if slices.Contains(have, e) {
			s.fail("help bar: %q should not be in %q", e, have)
		}
	}
}

// ExpectHelpDim expects key's help bar entry to be dimmed (its action
// cannot run now); ExpectHelpLive expects it to be live.
func (s *Scenario) ExpectHelpDim(key string) {
	s.t.Helper()
	s.expectHelpEnabled(key, false)
}

// ExpectHelpLive expects key's help bar entry to be live.
func (s *Scenario) ExpectHelpLive(key string) {
	s.t.Helper()
	s.expectHelpEnabled(key, true)
}

func (s *Scenario) expectHelpEnabled(key string, want bool) {
	s.t.Helper()
	e, ok := s.State().HelpKey(key)
	if !ok {
		s.fail("help bar: no entry for %s", key)
	}
	if e.Enabled != want {
		s.fail("help bar: %s enabled %v, want %v", e, e.Enabled, want)
	}
}

// ExpectText expects the plain frame to contain text.
func (s *Scenario) ExpectText(text string) {
	s.t.Helper()
	if !strings.Contains(strings.Join(s.Frame(), "\n"), text) {
		s.fail("frame: no %q", text)
	}
}

// ExpectNoText expects the plain frame not to contain text.
func (s *Scenario) ExpectNoText(text string) {
	s.t.Helper()
	if strings.Contains(strings.Join(s.Frame(), "\n"), text) {
		s.fail("frame: %q should not be there", text)
	}
}

// ExpectBuffer expects the editor's buffer to contain text.
func (s *Scenario) ExpectBuffer(text string) {
	s.t.Helper()
	if !strings.Contains(s.State().Text, text) {
		s.fail("buffer: no %q", text)
	}
}

// ExpectNoBuffer expects the editor's buffer not to contain text.
func (s *Scenario) ExpectNoBuffer(text string) {
	s.t.Helper()
	if strings.Contains(s.State().Text, text) {
		s.fail("buffer: %q should not be there", text)
	}
}

// ExpectCursor expects the cursor on line (1-based) of file ("" for the
// current one).
func (s *Scenario) ExpectCursor(file string, line int) {
	s.t.Helper()
	st := s.State()
	if (file != "" && st.File != file) || st.Line != line {
		s.fail("cursor: got %s:%d, want %s:%d", st.File, st.Line, file, line)
	}
}

// ExpectAnchor expects the cursor inside anchor ("$Name").
func (s *Scenario) ExpectAnchor(name string) {
	s.t.Helper()
	if got := s.State().Anchor; got != name {
		s.fail("cursor anchor: got %q, want %q", got, name)
	}
}

// ExpectDirty expects the buffer's modified marker.
func (s *Scenario) ExpectDirty(want bool) {
	s.t.Helper()
	if got := s.State().Dirty; got != want {
		s.fail("dirty: got %v, want %v", got, want)
	}
}

// ExpectCards expects the sidebar's cards by kind, in order ("problem",
// "fix", "group", "question", "proposal").
func (s *Scenario) ExpectCards(kinds ...string) {
	s.t.Helper()
	var got []string
	for _, c := range s.State().Cards {
		got = append(got, c.Kind)
	}
	if !slices.Equal(got, kinds) {
		s.fail("cards: got %q, want %q", got, kinds)
	}
}

// ExpectLayout checks the frame's structure, as text: exactly as many
// rows as the terminal, none wider; the status line (or the prompt) on
// its row, holding the whole flash; and, when the help bar is shown, its
// two rows holding every entry whole (no label cut at the edge).
func (s *Scenario) ExpectLayout() {
	s.t.Helper()
	f, st, err := s.d.Snapshot()
	if err != nil {
		s.fail("frame: %v", err)
	}
	rows := strings.Split(f.Content, "\n")
	if len(rows) != s.h {
		s.fail("layout: the frame has %d rows, the terminal %d", len(rows), s.h)
	}
	for k, r := range rows {
		if w := ansi.StringWidth(r); w > s.w {
			s.fail("layout: row %d is %d cells wide, the terminal %d", k, w, s.w)
		}
	}
	plain := make([]string, len(rows))
	for k, r := range rows {
		plain[k] = strings.TrimRight(ansi.Strip(r), " ")
	}
	statusRow := st.StatusRow
	if st.HelpBarShown {
		for k, row := range st.HelpRows {
			at := st.HelpRow + k
			if at >= len(plain) || plain[at] != strings.TrimRight(row, " ") {
				got := "<missing>"
				if at < len(plain) {
					got = plain[at]
				}
				s.fail("layout: help row %d differs from the rendered template: want %q, got %q", at, strings.TrimRight(row, " "), got)
			}
		}
		for k, e := range st.Help {
			rowIndex := k % 2
			if rowIndex >= len(st.HelpRows) {
				s.fail("layout: help entry %q has no rendered row", e.String())
				continue
			}
			row := st.HelpRows[rowIndex]
			if !strings.Contains(row, e.Key) || !strings.Contains(row, e.Label) {
				s.fail("layout: help entry %q is not whole on its row %q", e.String(), row)
			}
		}
	}
	if st.Notice {
		return // a notice may cover the status line
	}
	if st.PromptLine != "" {
		if !strings.Contains(plain[st.PromptRow], strings.TrimSpace(st.PromptLine)) {
			s.fail("layout: row %d is not the prompt %q: %q", st.PromptRow, st.PromptLine, plain[st.PromptRow])
		}
	}
	if want := strings.TrimSpace(st.Status); !strings.Contains(plain[statusRow], want) {
		s.fail("layout: row %d is not the status line %q: %q", statusRow, want, plain[statusRow])
	}
	if st.Prompt == "" && st.Flash != "" {
		flash := []rune(termtext.Clean(st.Flash))
		// The footer keeps the status line within its cell budget, so a long
		// flash may end in an ellipsis. Verify that its opening is visible.
		if len(flash) > 12 {
			flash = flash[:12]
		}
		if len(flash) > 0 && !strings.Contains(plain[statusRow], string(flash)) {
			s.fail("layout: the flash opening %q is missing from the status line %q", string(flash), plain[statusRow])
		}
	}
}
