// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/tui/editor"
)

// This file exports the App's semantic state to the scenario tests
// (package tui_test, scenario_*_test.go): what a key did, which pane has
// the keyboard, what the help bar offers, what the sidebar holds. None of
// it depends on colours or layout, so a redesign leaves the scenarios
// alone.

// HelpEntry is one help bar entry as it stands: "^R" "Commit", and
// whether its action can run now (a dimmed entry cannot).
type HelpEntry struct {
	Key, Label string
	Enabled    bool
}

// String writes the entry as the help bar does: "^R Commit".
func (e HelpEntry) String() string { return e.Key + " " + e.Label }

// CardState is one sidebar card.
type CardState struct {
	// Proposal is the card's proposal id; Key its item ("h2" for a fix,
	// "p2" or "n0" for a problem, "q0", "g" for the group, "" for an
	// agent's proposal).
	Proposal, Key string
	// Kind is "problem", "fix", "group", "question" or "proposal".
	Kind     string
	Severity string
	Anchor   string
	// Stale says why the card's edit no longer applies; Outdated why its
	// check is out of date ("" when neither).
	Stale, Outdated string
	Selected        bool
	// State is where a problem or fix stands: a problem "open", "no-fix",
	// "skipped", "resolved", "by-hand" or "dismissed"; a fix "pending",
	// "hand", "hand-unsure", "gone", "stale", "accepted", "edited",
	// "skipped" or "by-hand".
	State string
	// Head is the line over the card's cluster ("1 fix resolves 2
	// problems", "2 fixes for this problem"; "" for one problem and one
	// fix, or no cluster).
	Head string
	// Problems are the keys of the cluster's problems, Fixes its fixes'
	// keys (a problem: the fixes that resolve it). Nil without a cluster.
	Problems, Fixes []string
}

// StageState is one cascade stage of a run.
type StageState struct {
	Name, Outcome string
	// Line is the stage's line as the card shows it.
	Line string
}

// RunState is one anchor's cascade (the last ^P).
type RunState struct {
	Anchor  string
	Stages  []StageState
	Current string
	Done    bool
	Cleared string
	Failed  string
}

// EditStop is one ^] stop: an edit since HEAD.
type EditStop struct {
	// Kind is "anchor", "prose" or "format".
	Kind  string
	Line  int
	Label string
}

// CommitItem is one change of the commit view.
type CommitItem struct {
	ID     string
	Ticked bool
	State  string
}

// CommitState is the commit view.
type CommitState struct {
	Items    []CommitItem
	Selected string
	Message  string
	Drafting bool
	Note     string
}

// ReviewState is the review screen of one proposal.
type ReviewState struct {
	Proposal string
	// Hunks are each hunk's decision: "undecided", "accepted", "skipped".
	Hunks    map[int]string
	Stale    map[int]string
	Selected int
	Gone     string
}

// State is a snapshot of the App, taken on the program's goroutine.
type State struct {
	Context Context
	// Focus is the pane with the keyboard on the main screen: "editor",
	// "outline" or "sidebar"; another screen's name ("review", "commit",
	// "help", "text-edit") when one is on top.
	Focus string
	// Screens is the depth of the screen stack (1: the main screen).
	Screens int
	Flash   string
	// Status is the status line's plain text.
	Status string
	// Prompt is the open prompt's label and input ("" when none).
	Prompt, PromptInput string
	PromptLine          string
	PromptRow           int
	// Popup says a popup (hover, diagnostic or notice) is open; Notice a
	// modal notice; List the list popup's title; Completion the
	// completion popup's sigil.
	Popup      bool
	Notice     bool
	List       string
	ListItems  []string
	Completion string
	Help       []HelpEntry
	// HelpRows are the two rendered help rows after stripping colors;
	// HelpRow and StatusRow are their actual frame positions.
	HelpRows           []string
	HelpRow, StatusRow int
	// File, Line and Col are the editor's cursor (1-based); Dirty the
	// buffer's [+]; Text the buffer.
	File      string
	Line, Col int
	Dirty     bool
	Text      string
	// Anchor is the anchor the cursor is in ("$Name", "" when none).
	Anchor string

	OutlineShown, SideShown, SideColumn, HelpBarShown bool
	// EditorWidth is the editor pane's width in cells; WrapWidth the
	// width its text wraps at.
	EditorWidth, WrapWidth int
	Cards                  []CardState
	Runs                   []RunState
	Badge                  string
	Job                    string
	// Gutter is the marker column of the current file by line: "anchor",
	// "fix", "fix-stale", "prose-add", "prose-del", "prose-both",
	// "format", "other".
	Gutter map[int]string
	// Edits are the ^] stops, or EditsWhy says why there are none.
	Edits    []EditStop
	EditsWhy string
	Commit   *CommitState
	Review   *ReviewState
	// Outline is the outline's rows: "§1 Terms", "$Lock.Holder", and a model's
	// declarations as "tagged Name" or "untagged Name". OutlineSel is the
	// selected row's index.
	Outline    []string
	OutlineSel int
	// Models are the project's models, as the workspace knows them.
	Models []string
	// Diagnostics counts the current file's diagnostics.
	Diagnostics int
}

// barNames name the gutter's bar kinds.
var barNames = map[barKind]string{
	barAnchor: "anchor", barFix: "fix", barFixStale: "fix-stale", barProseAdd: "prose-add",
	barProseDel: "prose-del", barProseBoth: "prose-both", barFormat: "format", barOther: "other",
}

// cardKinds name the card kinds.
var cardKinds = map[cardKind]string{cardEdit: "fix", cardProblem: "problem", cardGroup: "group", cardQuestion: "question", cardProposal: "proposal"}

// fixStates and probStates name where a fix or a problem stands.
var (
	fixStates = map[fixState]string{fixPending: "pending", fixHand: "hand", fixHandUnsure: "hand-unsure", fixGone: "gone",
		fixStale: "stale", fixAccepted: "accepted", fixEdited: "edited", fixSkipped: "skipped", fixByHand: "by-hand"}
	probStates = map[probState]string{probOpen: "open", probNoFix: "no-fix", probSkipped: "skipped", probResolved: "resolved",
		probHand: "by-hand", probDismissed: "dismissed"}
)

// cardModel fills cs's State, Head, Problems and Fixes from c.
func (a *App) cardModel(c *card, cs *CardState) {
	switch c.kind {
	case cardEdit:
		cs.State = fixStates[a.fixState(c.p, c.hunk)]
	case cardProblem:
		st, _ := a.problemState(c.p, c.prob)
		cs.State = probStates[st]
	}
	if c.cl == nil {
		return
	}
	cs.Head = c.cl.head()
	for _, pr := range c.cl.problems {
		cs.Problems = append(cs.Problems, pr.key)
	}
	fixes := c.cl.fixes
	if c.kind == cardProblem {
		fixes = c.prob.fixes
	}
	for _, n := range fixes {
		cs.Fixes = append(cs.Fixes, hunkKey(n))
	}
}

// stopKinds name the ^] stop kinds.
var stopKinds = map[int]string{stopAnchor: "anchor", stopProse: "prose", stopFormat: "format"}

// hunkStates name the review decisions.
var hunkStates = map[hunkState]string{hunkUndecided: "undecided", hunkAccepted: "accepted", hunkSkipped: "skipped"}

// State takes a snapshot of the App.
func (d *Driver) State() (State, error) {
	var s State
	err := d.do(func(a *App) { s = a.state() })
	return s, err
}

// Snapshot renders the frame and takes the state at the same moment.
func (d *Driver) Snapshot() (Frame, State, error) {
	var f Frame
	var s State
	err := d.do(func(a *App) {
		content, cur := a.render()
		f = Frame{Content: content, W: a.width, H: a.height, Cursor: cur, Light: !a.theme.Dark, Flash: a.flash}
		s = a.state()
	})
	return f, s, err
}

// state builds the snapshot. It must change nothing in the App.
func (a *App) state() State {
	ctx := a.context()
	s := State{
		Context: ctx, Screens: len(a.screens), Flash: a.flash,
		OutlineShown: a.showOutline, HelpBarShown: a.showHelpBar, SideShown: a.sideShown, SideColumn: a.sideShown && a.sideColumn(),
		Badge: a.badge(), Job: a.jobText(),
	}
	switch top := a.top().(type) {
	case *mainScreen:
		s.Focus = "editor"
		switch {
		case a.sideFocus && a.sideShown:
			s.Focus = "sidebar"
		case a.outlineFocus && a.showOutline:
			s.Focus = "outline"
		}
	case *reviewScreen:
		s.Focus = "review"
		rs := &ReviewState{Proposal: top.p.ID, Hunks: map[int]string{}, Stale: map[int]string{}, Selected: top.selected(), Gone: top.gone}
		for _, n := range top.nums {
			rs.Hunks[n] = hunkStates[top.state[n]]
		}
		for n, why := range top.stale {
			rs.Stale[n] = why
		}
		s.Review = rs
	case *commitScreen:
		s.Focus = "commit"
		cs := &CommitState{Message: top.message(a), Drafting: top.drafting, Note: top.draftNote}
		for _, c := range top.changes {
			cs.Items = append(cs.Items, CommitItem{ID: c.ID, Ticked: top.ticked[c.ID], State: top.state[c.ID]})
		}
		if c := top.selected(); c != nil {
			cs.Selected = c.ID
		}
		s.Commit = cs
	case *helpScreen:
		s.Focus = "help"
	case *referenceScreen:
		if top.showSettings {
			s.Focus = "settings"
		} else {
			s.Focus = "help"
		}
	case *hunkEditScreen:
		s.Focus = "text-edit"
	default:
		s.Focus = fmt.Sprintf("%T", top)
	}
	if a.prompt != nil {
		s.Prompt, s.PromptInput = a.prompt.label, a.prompt.input
		line, _ := a.prompt.view(a, a.width)
		s.PromptLine = ansi.Strip(line)
		s.PromptRow = max(a.bodyHeight()-1, 0)
	}
	s.Status = ansi.Strip(a.statusLine(a.width))
	if a.popup != nil {
		s.Popup, s.Notice = true, a.popup.notice
	}
	if a.list != nil {
		s.List = a.list.title
		for _, it := range a.list.items {
			s.ListItems = append(s.ListItems, it.key)
		}
	}
	if a.comp != nil {
		s.Completion = a.comp.sigil
	}
	bindings := helperTemplate(ctx, a.width)
	if custom, ok := a.top().(interface{ helperBindings(*App) []Binding }); ok {
		bindings = custom.helperBindings(a)
	}
	for _, b := range bindings {
		enabled := a.canRun(ctx, b)
		if custom, ok := a.top().(interface{ helperEnabled(*App, Binding) bool }); ok {
			enabled = enabled && custom.helperEnabled(a, b)
		}
		s.Help = append(s.Help, HelpEntry{Key: displayKey(b.Key, a.theme.ASCII), Label: a.labelOf(ctx, b), Enabled: enabled})
	}
	if a.showHelpBar {
		for _, row := range a.helpBar(ctx, a.width) {
			s.HelpRows = append(s.HelpRows, ansi.Strip(row))
		}
		s.HelpRow = a.bodyHeight() + len(a.statusBand(a.width))
	}
	s.StatusRow = a.bodyHeight()
	if a.showHelpBar && a.height > 36 {
		s.StatusRow++ // top status-band separator
	}
	ed := a.cur.Ed
	p := ed.Buffer().Index().Position(ed.Cursor())
	s.File, s.Line, s.Col, s.Dirty = ed.Path(), p.Line, p.Col, ed.Buffer().Dirty()
	s.Text = string(ed.Buffer().Bytes())
	if f := a.ws.File(ed.Path()); f != nil {
		if an := f.AnchorAt(ed.Cursor()); an != nil {
			s.Anchor = "$" + an.Name
		}
	}
	_, _, s.EditorWidth, _ = a.editorRect()
	s.WrapWidth = ed.TextWidth() - 1
	sel := a.side.selected()
	for _, c := range a.side.cards {
		cs := CardState{Proposal: c.p.ID, Key: c.key, Kind: cardKinds[c.kind], Severity: c.severity(), Anchor: c.anchor(),
			Stale: a.cardStale(c), Outdated: a.checkOutdated(c.p), Selected: c == sel}
		a.cardModel(c, &cs)
		s.Cards = append(s.Cards, cs)
	}
	now := a.now()
	for _, r := range a.side.runs {
		rs := RunState{Anchor: r.anchor, Done: r.done, Cleared: r.cleared, Failed: r.failed}
		for _, st := range r.stages {
			rs.Stages = append(rs.Stages, StageState{Name: st.Name, Outcome: st.Outcome, Line: runLine(st, r.since, now)})
		}
		if r.current != nil {
			rs.Current = r.current.Name
		}
		s.Runs = append(s.Runs, rs)
	}
	for _, e := range a.outline.entries {
		switch {
		case e.kind == entryDecl && e.tagged:
			s.Outline = append(s.Outline, "tagged "+e.text)
		case e.kind == entryDecl:
			s.Outline = append(s.Outline, "untagged "+e.text)
		default:
			s.Outline = append(s.Outline, e.text)
		}
	}
	s.OutlineSel = a.outline.sel
	s.Models = a.ws.ModelPaths()
	s.Diagnostics = len(a.ws.FileDiags(ed.Path()))
	if bars := a.gutterBars(ed.Path()); len(bars) > 0 {
		s.Gutter = map[int]string{}
		for l, k := range bars {
			if k != barNone {
				s.Gutter[l] = barNames[k]
			}
		}
	}
	if a.ws.Stale() {
		// edits() would reparse, and so drop a pending debounced parse:
		// a snapshot must change nothing.
		s.EditsWhy = "the analysis is not up to date (a parse is pending)"
	} else {
		es, why := a.edits()
		s.EditsWhy = why
		for _, e := range es {
			s.Edits = append(s.Edits, EditStop{Kind: stopKinds[e.kind], Line: e.line, Label: e.label})
		}
	}
	return s
}

// HighlightOf returns the highlight classes of the first byte of the first
// occurrence of text in the current buffer.
func (d *Driver) HighlightOf(text string) (editor.Flag, bool, error) {
	var fl editor.Flag
	found := false
	err := d.do(func(a *App) {
		off := strings.Index(string(a.cur.Ed.Buffer().Bytes()), text)
		if off < 0 {
			return
		}
		found = true
		for _, sp := range a.cur.Ed.Spans() {
			if sp.Start <= off && off < sp.End {
				fl |= sp.Flags
			}
		}
	})
	return fl, found, err
}

// CardsOf returns the cards of kind ("" for every kind).
func (s State) CardsOf(kind string) []CardState {
	var out []CardState
	for _, c := range s.Cards {
		if kind == "" || c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// GutterCount counts the gutter's lines of kind.
func (s State) GutterCount(kind string) int {
	n := 0
	for _, k := range s.Gutter {
		if k == kind {
			n++
		}
	}
	return n
}

// GutterLines returns the lines of kind, in order.
func (s State) GutterLines(kind string) []int {
	var out []int
	for l, k := range s.Gutter {
		if k == kind {
			out = append(out, l)
		}
	}
	sort.Ints(out)
	return out
}

// HelpText is the help bar entries as text, one per entry.
func (s State) HelpText() []string {
	out := make([]string, len(s.Help))
	for k, e := range s.Help {
		out[k] = e.String()
	}
	return out
}

// HelpKey returns the entry of key ("^R").
func (s State) HelpKey(key string) (HelpEntry, bool) {
	for _, e := range s.Help {
		if e.Key == key {
			return e, true
		}
	}
	return HelpEntry{}, false
}

// Summary describes the state in a few lines, for a failure.
func (s State) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "context %s, focus %s, screens %d\n", s.Context, s.Focus, s.Screens)
	fmt.Fprintf(&b, "flash %q\n", s.Flash)
	fmt.Fprintf(&b, "status %q\n", strings.TrimSpace(s.Status))
	fmt.Fprintf(&b, "status row %d help row %d\n", s.StatusRow, s.HelpRow)
	if s.Prompt != "" {
		fmt.Fprintf(&b, "prompt %q input %q\n", s.Prompt, s.PromptInput)
	}
	fmt.Fprintf(&b, "cursor %s %d:%d dirty %v anchor %s\n", s.File, s.Line, s.Col, s.Dirty, s.Anchor)
	var help []string
	for _, e := range s.Help {
		t := e.String()
		if !e.Enabled {
			t += " (dim)"
		}
		help = append(help, t)
	}
	fmt.Fprintf(&b, "help bar: %s\n", strings.Join(help, " | "))
	fmt.Fprintf(&b, "outline %v sidebar %v (column %v), %d cards, badge %q, job %q\n", s.OutlineShown, s.SideShown, s.SideColumn, len(s.Cards), s.Badge, s.Job)
	for _, c := range s.Cards {
		fmt.Fprintf(&b, "  card %s/%s %s %s %s stale %q outdated %q selected %v\n", c.Proposal, c.Key, c.Kind, c.Severity, c.Anchor, c.Stale, c.Outdated, c.Selected)
	}
	for _, r := range s.Runs {
		fmt.Fprintf(&b, "  run %s current %q done %v cleared %q failed %q\n", r.Anchor, r.Current, r.Done, r.Cleared, r.Failed)
		for _, st := range r.Stages {
			fmt.Fprintf(&b, "    %s\n", st.Line)
		}
	}
	return b.String()
}

// Watched is a watcher's report: of paths changed on disk (FilesChanged),
// or of a change under .spec/proposals (ProposalsChanged). A scenario
// delivers them itself to fix their order.
type Watched struct {
	paths     []string
	proposals bool
}

// FilesChanged is the file watcher's report of paths.
func FilesChanged(paths ...string) Watched { return Watched{paths: paths} }

// ProposalsChanged is the proposals watcher's report.
func ProposalsChanged() Watched { return Watched{proposals: true} }

// Deliver hands w to the program, as the watcher would, and waits until
// it is handled.
func (d *Driver) Deliver(w Watched) error {
	if w.proposals {
		return d.Send(proposalsChangedMsg{})
	}
	return d.Send(fileChangedMsg{paths: w.paths})
}
