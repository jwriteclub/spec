// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package tui is the terminal UI behind `spec ui`: an editor that knows the
// spec markup, with an outline, hover previews, jumps, quick open and a
// nano-style keymap. The editor component lives in package tui/editor.
package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sirupsen/logrus"
	"github.com/tenta-browser/polychromatic"

	"github.com/jwriteclub/spec/internal/baseline"
	fmtpkg "github.com/jwriteclub/spec/internal/format"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/tui/editor"
	"github.com/jwriteclub/spec/internal/workflow"
)

// lg logs to the file Run opens under .spec/ (never to the terminal, which
// the UI owns).
var lg *logrus.Entry = polychromatic.GetLogger("tui")

// Target is where `spec ui` opens: a file and line, an anchor name or a
// section. The zero value is the first spec file.
type Target struct {
	// File is a project-relative path; Line is 1-based (0 for the top).
	File string
	Line int
	// Name is an anchor name without $.
	Name string
	// Stem and Number name a section (Stem may be "").
	Stem, Number string
}

// Options configures the App.
type Options struct {
	Root   string
	Target Target
	// HashToSection turns on the #→§ conversion while typing.
	HashToSection bool
	// Watch watches the project on disk: changes to its files, and files that
	// join or leave it.
	Watch bool
	// WatchPoll makes Watch poll instead of using fsnotify, as it falls back
	// to doing when watches cannot be added.
	WatchPoll bool
	// Baseline loads the HEAD baseline for the anchor-changed state.
	Baseline bool
	// ParseDelay debounces reparsing after an edit; 0 parses at once.
	ParseDelay time.Duration
	// HoverDelay is how long the mouse rests before a hover popup.
	HoverDelay time.Duration
	// Light starts with the light theme; FixedTheme ignores the terminal's
	// reported background (tests, and a replay, which still applies the
	// user config's appearance).
	Light        bool
	FixedTheme   bool
	ReducedColor bool
	ASCIIGlyphs  bool
	// PlainUnderline underlines diagnostics without an underline colour
	// (SGR 58) or style: Terminal.app does not know SGR 58 and reads the
	// 5 of ESC[58;5;Nm as blink. The CLI sets it for TERM_PROGRAM
	// Apple_Terminal.
	PlainUnderline bool
	// WatchProposals watches .spec/proposals for proposals filed or
	// decided by others (the status line badge and the ^R list).
	WatchProposals bool
	// Workflow runs the checks and decisions (propose, analyze, approve,
	// reject); nil uses the process environment and git on PATH.
	Workflow *workflow.Workflow
	// Now replaces time.Now for the running job's and stage's times (UI
	// scenes freeze it, so a spinner and its seconds repeat exactly).
	Now func() time.Time
	// Record, when set, records the session's inputs (spec ui --record).
	Record *Recorder
	// Replay, when set, feeds a recording's mouse events and other
	// processes' file changes back in (spec ui --replay).
	Replay *Replay
}

// Location is a place in a file.
type Location struct {
	Path string
	Off  int
}

// TargetError says the target given on the command line does not exist.
type TargetError struct {
	Msg string
}

func (e *TargetError) Error() string { return e.Msg }

// App is the Bubble Tea model.
type App struct {
	opts  Options
	ws    *Workspace
	cur   *Doc
	theme *Theme

	width, height int
	detectedDark  bool

	screens []screen
	main    *mainScreen

	outline      *outline
	showOutline  bool
	outlineFocus bool
	showHelpBar  bool

	// side is the Checks sidebar; sideShown and sideFocus say whether it
	// is visible and has the keyboard.
	side      *sidebar
	sideShown bool
	sideFocus bool
	// sidePeeked is set once moving through the cards has put the
	// editor's earlier place on the jump history.
	sidePeeked       bool
	sideHoverID      string
	proposalSpinning bool

	// buffered are the applies no save has made true (unsaved.go), and
	// bufferedShown is how they stood when a.pending was last loaded.
	buffered      []*bufferedApply
	bufferedShown string

	popup  *popup
	list   *listPopup
	prompt *prompt

	flash       string
	flashKind   statusKind
	flashAccent string
	flashUntil  time.Time
	flashSeq    int
	statusHover string
	statusTip   *statusTooltip
	// agentFlash is the flash an agent's arriving proposal set; while it
	// shows, a reload does not cover it (onFileChanged).
	agentFlash   string
	arrivalCount int
	arrivalUntil time.Time
	keepFlash    bool

	back, fwd []Location

	cutBuf  string
	lastCut bool
	query   string

	parseSeq int
	// baselineSeq numbers the baseline loads, so that only the latest counts.
	baselineSeq int
	hoverSeq    int
	hoverAt     [2]int
	mouseDown   bool

	watcher *watcher
	propW   *propWatcher
	// headW reloads the baseline when HEAD moves (Watch with Baseline).
	headW    *headWatcher
	quitting bool

	wf     *workflow.Workflow
	job    *job
	jobSeq int
	// wait is the decision waiting for .spec/lock (lockwait.go);
	// heldLock is the lock while decisions run with it.
	wait     *lockWait
	waitSeq  int
	heldLock *project.Lock
	// afterWait is set while decisions run after a wait for the lock;
	// appliedAt is where the last apply after a wait put its edit, for ^].
	afterWait bool
	appliedAt *landing
	pending   []*proposal.Proposal
	// modelConfirm is the model proposal with check errors that a second y
	// applies (modelprop.go).
	modelConfirm *modelConfirm
	comp         *completion
	// editCache holds each file's comparison with HEAD (edits.go).
	editCache map[string]editCacheEntry
	// diffCache holds the anchor comparison with HEAD (status.go).
	diffCache anchorDiffCache
	// showRemoved draws the words removed since HEAD as ghost text (a
	// per-session toggle, r in the sidebar).
	showRemoved bool
	// discard holds the files the user chose not to save on exit.
	discard map[string]bool
	// rec records the session (Options.Record).
	rec *Recorder
	// replay feeds a recording back in (Options.Replay).
	replay *Replay
	// termW and termH are the real terminal's size while replaying, which
	// may be larger than the recorded size the UI lays out at (width,
	// height); the frame is then centred in it (Replay.translate).
	termW, termH int
}

// New opens the workspace and resolves the target. A target that does not
// exist is a *TargetError.
func New(opts Options) (*App, error) {
	if opts.HoverDelay == 0 {
		opts.HoverDelay = 300 * time.Millisecond
	}
	ws, err := OpenWorkspace(opts.Root, editor.Options{HashToSection: opts.HashToSection})
	if err != nil {
		return nil, err
	}
	a := &App{
		opts: opts, ws: ws, theme: NewThemeProfile(!opts.Light, opts.PlainUnderline, opts.ReducedColor, opts.ASCIIGlyphs),
		width: 100, height: 30, detectedDark: !opts.Light,
		showOutline: true, showHelpBar: true,
		wf: opts.Workflow,
	}
	if a.wf == nil {
		a.wf = &workflow.Workflow{}
	}
	// A replay pins the background (FixedTheme, from the recording's meta)
	// but looks as the recorded session did: the restored config.toml and
	// the terminal decide the rest, as for `spec ui`.
	if !opts.FixedTheme || opts.Replay != nil {
		if cfg, err := a.loadUserConfig(); err == nil && len(cfg.Diagnostics) == 0 {
			_ = a.applyAppearance(cfg.Config) // Installs the fallback and records any error in status.
		}
	}
	a.side = &sidebar{}
	a.loadChecks()
	a.loadPending()
	a.main = &mainScreen{}
	a.screens = []screen{a.main}
	a.outline = newOutline()
	loc, err := a.resolveTarget(opts.Target)
	if err != nil {
		return nil, err
	}
	if loc.Path == "" {
		return nil, &TargetError{Msg: "the project has no spec files to open; create one (spec init) or fix spec.toml"}
	}
	if err := a.open(loc); err != nil {
		return nil, err
	}
	if opts.Target.Name != "" || opts.Target.Number != "" || opts.Target.Line > 0 {
		a.cur.Ed.CenterCursor()
	}
	a.outline.rebuild(a.ws)
	a.layout()
	if opts.Record != nil {
		a.rec = opts.Record
		a.rec.attach(a)
	}
	a.replay = opts.Replay
	return a, nil
}

// Workspace returns the app's workspace.
func (a *App) Workspace() *Workspace { return a.ws }

// Theme returns the app's current theme.
func (a *App) Theme() *Theme { return a.theme }

// Current returns the document being edited.
func (a *App) Current() *Doc { return a.cur }

// resolveTarget turns the command-line target into a location.
func (a *App) resolveTarget(t Target) (Location, error) {
	switch {
	case t.File != "":
		var index *pos.Index
		if f := a.ws.File(t.File); f != nil {
			index = f.Index()
		} else if m := a.ws.Model(t.File); m != nil {
			index = m.Index()
		} else {
			return Location{}, &TargetError{Msg: fmt.Sprintf("%s is not a spec file or a formal model of this project (the files and formal lists in spec.toml select them)", t.File)}
		}
		off := 0
		if t.Line > 0 {
			off = index.LineStart(t.Line)
		}
		return Location{Path: t.File, Off: off}, nil
	case t.Name != "":
		as := a.ws.Spec.Lookup(t.Name)
		switch len(as) {
		case 0:
			return Location{}, &TargetError{Msg: fmt.Sprintf("no anchor named %s (spec ls lists them)", t.Name)}
		case 1:
			return Location{Path: as[0].File, Off: as[0].LabelRange.Start}, nil
		}
		return Location{}, &TargetError{Msg: fmt.Sprintf("$%s is defined %d times; open one of its files instead", t.Name, len(as))}
	case t.Number != "":
		from := ""
		if ps := a.ws.Paths(); len(ps) > 0 {
			from = ps[0]
		}
		st, sec, cands := a.ws.Spec.ResolveSection(from, t.Stem, t.Number)
		switch st {
		case mdspec.Resolved:
			return Location{Path: sec.File, Off: sec.HeadingRange.Start}, nil
		case mdspec.Ambiguous:
			var qs []string
			for _, c := range cands {
				qs = append(qs, c.Qualified())
			}
			return Location{}, &TargetError{Msg: fmt.Sprintf(a.theme.uiText("§%s is ambiguous: %s"), t.Number, strings.Join(qs, ", "))}
		}
		return Location{}, &TargetError{Msg: fmt.Sprintf("no section numbered %s", t.Number)}
	}
	for _, p := range a.ws.Paths() {
		if a.ws.File(p) != nil {
			return Location{Path: p}, nil
		}
	}
	return Location{}, nil
}

// open makes loc's file current and puts the cursor there.
func (a *App) open(loc Location) error {
	d, err := a.ws.Doc(loc.Path)
	if err != nil {
		return err
	}
	a.cur = d
	a.layout()
	d.Ed.SetCursor(loc.Off)
	return nil
}

// here returns the current location.
func (a *App) here() Location {
	return Location{Path: a.cur.Ed.Path(), Off: a.cur.Ed.Cursor()}
}

// ---- layout ----

// bodyHeight is the number of rows above the status line.
func (a *App) bodyHeight() int {
	h := a.height - a.footerHeight()
	return max(h, 1)
}

const (
	outlineMinWidth       = 20
	outlinePreferredWidth = 24
	outlineMaxWidth       = 28
)

// outlineWidth is the outline pane's width (border included), 0 when
// hidden. The outline yields first when an inline review column and the
// editor minimum cannot coexist; explicit outline focus keeps a narrow pane.
func (a *App) outlineWidth() int {
	ow, _, _ := a.regionWidths()
	return ow
}

// regionWidths reserves the narrowest useful regions first, then grows the
// sidebar and outline to their preferred widths before using any remaining
// cells for the editor. A sidebar that cannot leave a 60-cell editor becomes
// a deliberate drawer over the editor.
func (a *App) regionWidths() (outline, editor, side int) {
	total := max(a.width, 0)
	wantSide := a.sideShown
	canColumn := wantSide && total >= minEditorWidth+sideWidth

	if a.showOutline {
		availableForOutline := total - minEditorWidth
		if canColumn {
			availableForOutline -= sideWidth
		}
		if a.outlineFocus || availableForOutline >= outlineMinWidth {
			outline = outlineMinWidth
		}
	}
	if canColumn && outline > 0 && total < minEditorWidth+sideWidth+outlineMinWidth {
		if a.outlineFocus {
			canColumn = false
		} else {
			outline = 0
		}
	}
	if canColumn && total-outline-sideWidth < minEditorWidth {
		canColumn = false
	}
	if canColumn {
		side = sideWidth
	}
	if outline > 0 && !a.outlineFocus && total-outline-side < minEditorWidth {
		outline = 0
	}
	if !canColumn && outline > 0 && !a.outlineFocus && total-outline < a.textColumn() {
		outline = 0
	}

	// Prefer the complete inline region at 140 columns when it fits. At 100,
	// the default wrap column keeps the outline hidden unless the user focuses it.
	if outline == 0 && a.showOutline && a.outlineFocus {
		outline = min(outlineMinWidth, total)
	}
	extra := max(total-outline-side-minEditorWidth, 0)
	grow := func(current, target int) int {
		n := min(max(target-current, 0), extra)
		extra -= n
		return current + n
	}
	if side > 0 {
		side = grow(side, sidePreferredWidth)
	}
	if outline > 0 {
		outline = grow(outline, outlinePreferredWidth)
	}
	// Keep the preferred 54/24 widths at ordinary terminal sizes. The
	// additional sidebar/outline room is a wide-terminal luxury; at 220
	// columns this allows 68/28 while leaving the rest to the editor.
	if total >= 200 {
		if side > 0 {
			side = grow(side, sideMaxWidth)
		}
		if outline > 0 {
			outline = grow(outline, outlineMaxWidth)
		}
	}
	editor = max(total-outline-side, 0)
	return outline, editor, side
}

// textColumn is the editor width that holds a line of the spec's wrap
// column whole: the gutter, the text, and one cell for the cursor after
// it.
func (a *App) textColumn() int {
	wrap := fmtpkg.DefaultWidth
	if a.ws != nil {
		if w := workflow.FormatOptions(a.ws.Project).Width; w > 0 {
			wrap = w
		}
	}
	gutter := 6
	if a.cur != nil {
		gutter = a.cur.Ed.GutterWidth()
	}
	return wrap + gutter + 1
}

// editorRect returns the editor pane's origin and size: between the
// outline and the sidebar (when the sidebar has a column).
func (a *App) editorRect() (x, y, w, h int) {
	x, w, _ = a.regionWidths()
	return x, 0, w, a.bodyHeight()
}

// layout sizes the editor to its pane.
func (a *App) layout() {
	if a.cur == nil {
		return
	}
	_, _, w, h := a.editorRect()
	a.cur.Ed.SetSize(w, h)
}

// context returns the current input context.
func (a *App) context() Context {
	if a.prompt != nil {
		return a.prompt.context()
	}
	if a.popup != nil && a.popup.notice {
		return CtxNotice
	}
	return a.top().context(a)
}

// ---- Bubble Tea ----

// parseMsg asks for a reparse (debounced: only the latest seq parses).
type parseMsg struct{ seq int }

// hoverMsg fires when the mouse has rested.
type hoverMsg struct{ seq int }

// baselineMsg carries the loaded HEAD baseline.
type baselineMsg struct {
	base *baseline.Baseline
	seq  int
}

// Init implements tea.Model.
func (a *App) Init() tea.Cmd {
	var cmds []tea.Cmd
	if !a.opts.FixedTheme {
		cmds = append(cmds, tea.RequestBackgroundColor)
	}
	if a.opts.Baseline {
		cmds = append(cmds, a.reloadBaseline())
	}
	if a.watcher != nil {
		cmds = append(cmds, a.watcher.next())
	}
	if a.opts.WatchProposals && a.propW == nil {
		a.propW = newPropWatcher(a.ws.Root)
	}
	if a.propW != nil {
		cmds = append(cmds, a.propW.next())
	}
	if a.opts.Watch && a.opts.Baseline && a.headW == nil {
		a.headW = newHeadWatcher(a.ws.Root, pollEvery)
	}
	if a.headW != nil {
		cmds = append(cmds, a.headW.next())
	}
	if a.replay != nil {
		cmds = append(cmds, a.replay.begin())
	}
	cmds = append(cmds, a.ensureProposalSpinner())
	return tea.Batch(cmds...)
}

// Close stops the background work the App started: the proposal watcher
// and a running job. Call it only after the Bubble Tea program has stopped
// and Program.Run has returned; Close mutates UI-owned state. Run and Driver
// enforce this order.
func (a *App) Close() {
	if a.job != nil && !a.job.irreversible {
		a.job.cancel()
		a.job = nil
	}
	if a.wait != nil {
		a.wait.cancel()
		a.wait = nil
	}
	a.propW.Close()
	a.propW = nil
	a.headW.Close()
	a.headW = nil
}

// Update implements tea.Model: update with the recorder's and the
// replay's hooks around it.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var pre recPre
	if a.rec != nil {
		pre = a.rec.before(a, msg)
	}
	var fed tea.Cmd
	if a.replay != nil {
		if msg = a.replay.translate(a, msg); msg == nil {
			return a, nil
		}
		var handled bool
		if handled, fed = a.replay.before(a, msg); handled {
			a.replay.after(a)
			return a, fed
		}
	}
	m, cmd := a.update(msg)
	if a.rec != nil {
		a.rec.after(a, msg, pre)
	}
	if a.replay != nil {
		a.replay.after(a)
		cmd = tea.Batch(fed, cmd)
	}
	return m, cmd
}

// update handles one message.
func (a *App) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.statusTip = nil
		a.layout()
		return a, nil
	case tea.BackgroundColorMsg:
		if !a.opts.FixedTheme {
			a.detectedDark = msg.IsDark()
			a.theme = newThemeScheme(msg.IsDark(), a.opts.PlainUnderline, a.theme.Reduced, a.theme.ASCII, a.theme.scheme)
			if cfg, err := a.loadUserConfig(); err == nil && len(cfg.Diagnostics) == 0 {
				_ = a.applyAppearance(cfg.Config) // Installs the fallback and records any error in status.
			}
		}
		return a, nil
	case baselineMsg:
		if msg.seq != a.baselineSeq {
			return a, nil // a later project's answer is on its way, or here
		}
		a.ws.Base = msg.base
		a.outline.rebuild(a.ws)
		return a, a.withdrawReverted()
	case parseMsg:
		if msg.seq == a.parseSeq {
			a.reparse()
			return a, a.afterParse()
		}
		return a, nil
	case hoverMsg:
		if msg.seq == a.hoverSeq && a.popup == nil && a.list == nil && a.prompt == nil && len(a.screens) == 1 {
			a.hoverAtCell(a.hoverAt[0], a.hoverAt[1])
		}
		return a, nil
	case fileChangedMsg:
		cmd := a.onWatched(msg)
		next := a.watcher.next()
		if a.staleReport(msg) {
			next = nil // the reload armed the new watcher; a second wait could reorder reports
		}
		return a, tea.Batch(cmd, a.afterParse(), next, a.statusExpiry())
	case headMovedMsg:
		// A commit, reset or checkout outside spec ui: compare with the new HEAD.
		return a, tea.Batch(a.reloadBaseline(), a.headW.next())
	case proposalsChangedMsg:
		a.onProposalsChanged()
		if msg.err != nil {
			a.setStatus(statusFailure, "Check recovery", "Check recovery failed: %s", msg.err.Message)
		}
		return a, tea.Batch(a.propW.next(), a.statusExpiry(), a.ensureProposalSpinner())
	case proposalSpinMsg:
		if a.checksInFlight() {
			return a, proposalSpinTick()
		}
		a.proposalSpinning = false
		return a, nil
	case spinMsg:
		if a.job != nil && a.job.seq == msg.seq {
			return a, spinTick(msg.seq)
		}
		return a, nil
	case jobDoneMsg:
		cmd := a.onJobDone(msg)
		return a, tea.Batch(cmd, a.statusExpiry())
	case stageMsg:
		return a, a.onStage(msg)
	case lockTickMsg:
		if a.wait != nil && a.wait.seq == msg.seq {
			return a, lockTick(msg.seq)
		}
		return a, nil
	case lockMsg:
		cmd := a.onLock(msg)
		return a, tea.Batch(cmd, a.statusExpiry())
	case draftMsg:
		msg.screen.onDraft(a, msg)
		return a, nil
	case settingsProbeProgressMsg:
		return a, msg.screen.probeProgressDone(msg)
	case settingsProbeResult:
		msg.screen.probeDone(msg)
		return a, nil
	case settingsProbeTick:
		return a, msg.screen.probeTick()
	case settingsSavedMsg:
		var watchNext, baselineNext tea.Cmd
		if msg.scope == "User" {
			if cfg, err := a.loadUserConfig(); err == nil && len(cfg.Diagnostics) == 0 {
				if err := a.applyAppearance(cfg.Config); err != nil {
					return a, nil
				}
			}
		} else {
			if err := a.reloadProjectSettings(); err != nil {
				a.notice("Project reload failed", err)
				return a, nil
			}
			watchNext = a.watcher.next()
			baselineNext = a.reloadBaseline()
		}
		test := msg.test
		if test == "" {
			test = "not tested"
		}
		a.setStatus(statusSuccess, "Saved", a.theme.uiText("Saved %s settings · %s"), strings.ToLower(msg.scope), test)
		return a, tea.Batch(a.statusExpiry(), watchNext, baselineNext)
	case statusExpiryMsg:
		if msg.seq == a.flashSeq && a.flashKind != statusFailure && a.flashKind != statusPartial && !a.now().Before(a.flashUntil) {
			a.flash = ""
			a.flashAccent = ""
		}
		return a, nil
	case tea.PasteMsg:
		if ref, ok := a.top().(*referenceScreen); ok {
			ref.paste(msg.Content)
			return a, nil
		}
		a.appliedAt = nil // a paste ends the ^] offer, as a key does
		if c := a.context(); c == CtxPopup || c == CtxDiagPopup || c == CtxNotice {
			// Like a key the popup does not bind: close it and paste.
			a.popup = nil
		}
		if a.context() == CtxCompletion {
			a.comp = nil
		}
		if a.context() == CtxEditor {
			a.lastCut = false
			a.cur.Ed.Insert(msg.Content)
			return a, a.edited()
		}
		if h, ok := a.top().(*hunkEditScreen); ok && a.prompt == nil {
			h.ed.Insert(msg.Content)
			return a, nil
		}
		if a.context() == CtxList || a.context() == CtxPrompt {
			return a, a.typeInto(msg.Content)
		}
		return a, nil
	case tea.KeyPressMsg:
		return a, a.onKey(msg)
	case tea.MouseMsg:
		return a, a.onMouse(msg)
	}
	return a, nil
}

// onKey dispatches a key press through the keymap of the current context.
func (a *App) onKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.Keystroke() == "ctrl+g" || msg.String() == "f1" {
		a.openHelp()
		return nil
	}
	ctx := a.context()
	b, ok := lookupKey(ctx, msg)
	if ok && !a.canRun(ctx, b) {
		return nil
	}
	if a.job != nil && a.job.irreversible && (ctx == CtxCommit && msg.Keystroke() == "ctrl+c" || b.Action == ActExit) {
		return nil
	}
	defer func() { a.keepFlash, a.appliedAt = false, nil }()
	a.sideHoverID = ""
	a.statusTip = nil
	if a.prompt != nil {
		cmd := a.prompt.key(a, msg, b, ok)
		if a.quitting {
			return cmd
		}
		return tea.Batch(cmd, a.statusExpiry())
	}
	if a.popup != nil && a.popup.notice {
		// A notice is modal: any key acknowledges its failure.
		if a.flashKind == statusFailure || a.flashKind == statusPartial {
			a.flash = ""
			a.flashAccent = ""
		}
		a.popup = nil
		return nil
	}
	if (a.wait != nil || a.job != nil) && msg.Keystroke() == "ctrl+c" && ctx != CtxEditor {
		// The status line says ^C cancels the running check, or stops
		// waiting for .spec/lock (first): on every screen. (The editor
		// copies a selection first, else cancels.)
		if !a.cancelWait() {
			a.cancelJob()
		}
		return nil
	}
	return tea.Batch(a.top().key(a, msg, b, ok), a.statusExpiry())
}

// lookupKey finds msg's binding in ctx by its key string, then by its
// keystroke (for shifted printable keys). A key in keyAliases is looked up
// as the key it stands for.
func lookupKey(ctx Context, msg tea.KeyPressMsg) (Binding, bool) {
	s := msg.String()
	if k, ok := keyAliases[s]; ok {
		return Lookup(ctx, k)
	}
	if b, ok := Lookup(ctx, s); ok {
		return b, true
	}
	return Lookup(ctx, msg.Keystroke())
}

// onMouse routes a mouse message to the top screen.
func (a *App) onMouse(msg tea.MouseMsg) tea.Cmd {
	if a.statusTip != nil {
		m := msg.Mouse()
		if a.statusTip.covers(m.X, m.Y) {
			return nil // The explanatory surface never forwards clicks or scrolls behind it.
		}
	}
	switch m := msg.(type) {
	case tea.MouseMotionMsg:
		a.statusHover = ""
		if badge := a.statusBadgeAt(m.X, m.Y); badge >= 0 && (a.popup == nil || !a.popup.notice) {
			a.sideHoverID = ""
			a.statusTip = &statusTooltip{kind: badge}
			a.statusTip.place(a)
			return nil
		}
		a.statusTip = nil
		a.updateSideHover(m.X, m.Y)
	case tea.MouseClickMsg:
		a.sideHoverID = ""
		if a.prompt == nil && (a.popup == nil || !a.popup.notice) && a.statusBadgeAt(m.X, m.Y) >= 0 {
			a.statusTip = nil
			a.push(&statusReferenceScreen{})
			return nil
		}
		a.statusTip = nil
	}
	if a.prompt != nil {
		return nil
	}
	if _, ok := msg.(tea.MouseClickMsg); ok {
		// A click moves the cursor: the next ^K starts a new cut buffer.
		a.lastCut = false
	}
	return a.top().mouse(a, msg)
}

// setFlash shows msg in the status line until the next key.
func (a *App) setFlash(format string, args ...any) {
	a.setStatus(statusInfo, "", format, args...)
}

// edited is called after every buffer change: it schedules the reparse,
// after which a check whose anchor is back to HEAD is withdrawn and a fix
// made by hand is recorded (afterParse).
func (a *App) edited() tea.Cmd {
	if f := a.cur.Ed.TakeFlash(); f != "" {
		// The editor currently emits only the generated #N conversion receipt.
		a.setFlash("%s", a.theme.uiText(f))
	}
	a.parseSeq++
	if a.opts.ParseDelay <= 0 {
		a.reparse()
		return a.afterParse()
	}
	seq := a.parseSeq
	return tea.Tick(a.opts.ParseDelay, func(time.Time) tea.Msg { return parseMsg{seq: seq} })
}

// reparse brings the analysis up to date.
func (a *App) reparse() {
	if a.ws.Reparse() {
		a.outline.rebuild(a.ws)
	}
}

// afterParse is what the reparse after an edit does to the checks: a
// check whose anchor is back to HEAD is withdrawn, then a fix whose change
// the text now holds is recorded as done by hand.
func (a *App) afterParse() tea.Cmd {
	a.reconcileBuffered()
	withdraw := a.withdrawReverted()
	return tea.Batch(withdraw, a.recordDoneByHand())
}

// fresh makes sure the current analysis matches the buffer (before a jump
// or a hover, which need exact token positions). It leaves the debounced
// parse pending: that parse finds nothing left to reparse, but still runs
// what follows an edit (afterParse: withdrawReverted, recordDoneByHand),
// which would otherwise wait for the next edit.
func (a *App) fresh() {
	if a.ws.Stale() {
		a.reparse()
	}
}

// quit ends the program.
func (a *App) quit() tea.Cmd {
	if a.job != nil && a.job.irreversible {
		return nil
	}
	if a.rec != nil {
		a.rec.final(a)
	}
	if a.replay != nil {
		a.replay.end(a)
	}
	a.quitting = true
	return tea.Quit
}

// ---- View ----

// View implements tea.Model.
func (a *App) View() tea.View {
	content, cur := a.render()
	if a.termW > a.width || a.termH > a.height {
		content, cur = centre(content, cur, a.width, a.height, a.termW, a.termH)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	v.WindowTitle = "spec ui: " + a.cur.Ed.Path()
	if cur != nil {
		v.Cursor = tea.NewCursor(cur[0], cur[1])
	}
	return v
}

// render draws the frame and returns it with the cursor cell, if shown.
// The frame never exceeds the terminal: on a screen too small for the
// layout, lines are cut at the width and the bottom ones (the help bar
// first) are dropped.
func (a *App) render() (string, *[2]int) {
	if a.quitting || a.width <= 0 || a.height <= 0 {
		return "", nil
	}
	frame, cur := a.compose()
	return clip(frame, cur, a.width, a.height)
}

// clip cuts frame to w x h cells and drops a cursor that falls outside.
func clip(frame string, cur *[2]int, w, h int) (string, *[2]int) {
	lines := strings.Split(frame, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for k, l := range lines {
		if ansi.StringWidth(l) > w {
			lines[k] = ansi.Truncate(l, w, "")
		}
	}
	if cur != nil && (cur[0] < 0 || cur[1] < 0 || cur[0] >= w || cur[1] >= h) {
		cur = nil
	}
	return strings.Join(lines, "\n"), cur
}

// compose draws the frame for the current size, before clipping.
func (a *App) compose() (string, *[2]int) {
	bh := a.bodyHeight()
	lines := a.top().view(a, a.width, bh)
	var cur *[2]int
	if len(a.screens) == 1 && a.prompt == nil && a.list == nil && !a.outlineFocus && !a.sideFocus && (a.popup == nil || !a.popup.notice) {
		if x, y, ok := a.cur.Ed.CursorCell(); ok {
			ex, ey, _, _ := a.editorRect()
			cur = &[2]int{ex + x, ey + y}
			if sx, _ := a.sideRect(); a.sideShown && !a.sideColumn() && cur[0] >= sx {
				cur = nil // under the sidebar drawn over the text
			}
		}
	}
	if a.prompt != nil {
		line, cx := a.prompt.view(a, a.width)
		lines[bh-1] = a.promptBand(line, a.width)
		cur = &[2]int{cx, bh - 1}
	}
	lines = append(lines, a.statusBand(a.width)...)
	if a.showHelpBar {
		lines = append(lines, a.helpBar(a.context(), a.width)...)
	}
	if c, ok := a.top().(cursorScreen); ok && a.prompt == nil && (a.popup == nil || !a.popup.notice) {
		cur = c.cursor(a)
	}
	frame := strings.Join(lines, "\n")
	var layers []*lipgloss.Layer
	if len(a.screens) == 1 {
		if a.sideShown && !a.sideColumn() {
			x, w := a.sideRect()
			layers = append(layers, lipgloss.NewLayer(strings.Join(a.sideView(w, bh), "\n")).X(x).Y(0))
		}
		if a.comp != nil {
			layers = append(layers, a.comp.layer(a))
		}
		if a.popup != nil && !a.popup.notice {
			layers = append(layers, a.popup.layer(a))
		}
		if a.list != nil {
			layers = append(layers, a.list.layer(a))
		}
	}
	if a.prompt != nil {
		line, _ := a.prompt.view(a, a.width)
		layers = append(layers, lipgloss.NewLayer(a.promptBand(line, a.width)).Y(bh-1))
	}
	if a.popup != nil && a.popup.notice {
		layers = append(layers, a.popup.layer(a))
	}
	if a.statusTip != nil && (a.popup == nil || !a.popup.notice) {
		a.statusTip.place(a)
		layers = append(layers, a.statusTip.layer(a))
		if cur != nil && a.statusTip.covers(cur[0], cur[1]) {
			cur = nil
		}
	}
	if len(layers) == 0 {
		return fitFrameWidth(a.theme.Ink.Render(frame), a.width), cur
	}
	canvas := lipgloss.NewCanvas(a.width, a.height)
	base := lipgloss.NewLayer(frame)
	for k, l := range layers {
		l.Z(k + 1)
	}
	canvas.Compose(lipgloss.NewCompositor(append([]*lipgloss.Layer{base}, layers...)...))
	frame = fitFrameWidth(a.theme.Ink.Render(canvas.Render()), a.width)
	return frame, cur
}

func fitFrameWidth(frame string, width int) string {
	rows := strings.Split(frame, "\n")
	for i := range rows {
		if ansi.StringWidth(rows[i]) > width {
			rows[i] = ansi.Truncate(rows[i], max(width-1, 0), "")
		}
	}
	return strings.Join(rows, "\n")
}

// errorText formats an error for the status line.
func errorText(err error) string {
	var held *project.LockHeldError
	if errors.As(err, &held) {
		return fmt.Sprintf("another spec process is writing (%s); try again", held.Record.String())
	}
	return err.Error()
}
