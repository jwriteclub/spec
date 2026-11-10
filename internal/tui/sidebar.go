// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/finding"
	fmtpkg "github.com/jwriteclub/spec/internal/format"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/workflow"
)

// The Checks sidebar (plan-step4e-review-flow.md): what ^P, ^N and the
// lint fixes found, and the agents' pending proposals, as cards on the
// right. Accepting a card applies it into the editor's buffers; nothing is
// saved or committed until Chris does so.

// Sidebar widths include its focus edge. A deliberately opened drawer uses
// the preferred width; an inline column can grow to the cap when space allows.
const (
	sideWidth          = 42
	sidePreferredWidth = 54
	sideMaxWidth       = 68
)

// minEditorWidth is the narrowest editor the sidebar leaves as a column;
// below it the sidebar is drawn over the text instead.
const minEditorWidth = 60

// checksPath is where the sidebar keeps its decisions, under the project.
const checksPath = workflow.ChecksFile

// checksState is what the sidebar remembers across restarts.
type checksState struct {
	// Decided maps a proposal ID to its decided items: item key ("h2" for
	// hunk 2, "n0" for note 0, "q0" for question 0) to "accepted" or
	// "dismissed". A proposal leaves the map once it is settled.
	Decided map[string]map[string]string `json:"decided"`
	// Generations fences persisted child decisions across app restarts. Missing
	// entries are legacy generation zero, never authorization for a new check.
	Generations map[string]int `json:"generations,omitempty"`
	// Message is the commit message draft (c).
	Message string `json:"message"`
	// Accepted are the explanations of the suggestions accepted since the
	// last commit: the default body of the commit message.
	Accepted []string `json:"accepted"`
	// Cleared maps an anchor ("$Name") the per-edit cascade cleared to
	// the sha256 of the body text it cleared: nothing was filed, but that
	// text counts as checked.
	Cleared map[string]string `json:"cleared,omitempty"`
}

// Decision values.
const (
	decAccepted  = "accepted"
	decDismissed = "dismissed"
)

// cardKind is what a card shows.
type cardKind int

const (
	// cardEdit is a suggested fix: a hunk of a check, a fix or an
	// analysis (fixcards.go). y applies it into the buffer, n skips it.
	cardEdit cardKind = iota
	// cardProblem is a finding: what is wrong, between which places. y
	// applies its pending fixes; n dismisses it.
	cardProblem
	// cardQuestion is a question the model asked.
	cardQuestion
	// cardProposal is an agent's proposal, decided as a whole: y applies
	// its change and edits into the buffers, n rejects it.
	cardProposal
	// cardGroup is the title of a check with two fixes or more: y
	// accepts every pending fix.
	cardGroup
)

// card is one item of the sidebar.
type card struct {
	kind cardKind
	p    *proposal.Proposal
	// key names the item within p ("h2" for a fix, "p2" or "n0" for a
	// problem, "q1", "g" for the group; "" for a proposal).
	key  string
	hunk finding.Hunk
	prob *problem
	// cl is the cluster a problem or fix belongs to.
	cl   *cluster
	text string
}

// id identifies the card across rebuilds.
func (c *card) id() string { return c.p.ID + "/" + c.key }

// severity is the card's severity word, which picks its colour.
func (c *card) severity() string {
	switch c.kind {
	case cardEdit:
		return c.hunk.Severity
	case cardProblem:
		return c.prob.severity
	case cardQuestion:
		return "question"
	}
	return "proposal"
}

// ownAnchor is the anchor the card's finding names ("" when none).
func (c *card) ownAnchor() string {
	switch c.kind {
	case cardEdit:
		return c.hunk.Anchor
	case cardProblem:
		return c.prob.anchor
	case cardProposal:
		if c.p.Primary != nil {
			return c.p.Primary.Name
		}
	}
	return ""
}

// anchor is the anchor the card is about: the one its finding names, else
// the anchor its check checked ("$Name", "" when none).
func (c *card) anchor() string {
	switch {
	case c.kind == cardEdit && c.hunk.Anchor != "":
		return c.hunk.Anchor
	case c.kind == cardProblem && c.prob.anchor != "":
		return c.prob.anchor
	case c.p.Primary != nil:
		return c.p.Primary.Name
	}
	return ""
}

// sidebar is the right-hand pane of cards.
type sidebar struct {
	cards                            []*card
	sel                              int
	scroll                           int
	state                            checksState
	revealedID                       string
	revealedAt, revealedW, revealedH int
	// starts and ends are each card's first and past-last line in the
	// last render (pane lines, the header included).
	starts, ends []int
	// format holds the cards (by id) that ^F set to be formatted as they
	// are accepted, previewed formatted.
	format map[string]bool
	// runs are the cascades of the last ^P, shown above the cards
	// (cascade.go).
	runs []*cascadeRun
}

// suggestions reports whether p's parts are reviewed one by one as
// suggestions (a check of Chris's own edit, a fix, an analysis), rather
// than as one proposal of a change that is not in the files yet.
func suggestions(p *proposal.Proposal) bool {
	if p.IsModel() {
		return false
	}
	if p.SourceKind == "agent" {
		return true
	}
	return p.Primary == nil || p.Primary.InWorktree
}

// loadChecks reads the sidebar's state (empty when there is none).
func (a *App) loadChecks() {
	a.side.state = checksState{}
	b, err := os.ReadFile(filepath.Join(a.ws.Root, filepath.FromSlash(checksPath)))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			lg.WithError(err).Warn("cannot read the sidebar state")
		}
		return
	}
	if err := json.Unmarshal(b, &a.side.state); err != nil {
		lg.WithError(err).Warn("the sidebar state is not valid JSON; starting afresh")
		a.side.state = checksState{}
	}
}

// saveChecks writes the sidebar's state, keeping only pending proposals'
// decisions.
func (a *App) saveChecks() {
	st := &a.side.state
	for id := range st.Decided {
		if !slices.ContainsFunc(a.pending, func(p *proposal.Proposal) bool { return p.ID == id }) && len(a.bufferedOf(id)) == 0 {
			delete(st.Decided, id)
			delete(st.Generations, id)
		}
	}
	for name := range st.Cleared {
		if !a.changedAnchor(name) {
			delete(st.Cleared, name)
		}
	}
	durable := *st
	durable.Decided = a.durableDecisions()
	b, err := json.MarshalIndent(&durable, "", "  ")
	if err != nil {
		lg.WithError(err).Warn("cannot encode the sidebar state")
		return
	}
	path := filepath.Join(a.ws.Root, filepath.FromSlash(checksPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		lg.WithError(err).Warn("cannot write the sidebar state")
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		lg.WithError(err).Warn("cannot write the sidebar state")
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		lg.WithError(err).Warn("cannot write the sidebar state")
	}
}

// decided returns the decision on item key of proposal id ("" when none).
func (a *App) decided(id, key string) string {
	return a.side.state.Decided[id][key]
}

// decide records the decision on item key of p.
func (a *App) decide(p *proposal.Proposal, key, how string) {
	st := &a.side.state
	if st.Decided == nil {
		st.Decided = map[string]map[string]string{}
	}
	if st.Decided[p.ID] == nil {
		st.Decided[p.ID] = map[string]string{}
	}
	st.Decided[p.ID][key] = how
}

// rebuildCards builds the cards from the pending proposals (newest first)
// and the decisions, keeping the selection on the same card when it is
// still there.
func (a *App) rebuildCards() {
	s := a.side
	keep := ""
	if c := s.selected(); c != nil {
		keep = c.id()
	}
	s.cards = s.cards[:0]
	for _, p := range a.pending {
		if !suggestions(p) {
			s.cards = append(s.cards, &card{kind: cardProposal, p: p})
			continue
		}
		// A cluster shows until every problem and fix in it is settled,
		// its decided fixes with their state.
		var cs []*card
		for _, cl := range clustersOf(p) {
			if a.clusterSettled(p, cl) {
				continue
			}
			for _, pr := range cl.problems {
				c := &card{kind: cardProblem, p: p, key: pr.key, prob: pr, cl: cl}
				if pr.own > 0 {
					c.hunk, _ = hunkOf(p, pr.own)
				}
				cs = append(cs, c)
			}
			for _, n := range cl.fixes {
				h, _ := hunkOf(p, n)
				cs = append(cs, &card{kind: cardEdit, p: p, key: hunkKey(n), hunk: h, cl: cl})
			}
		}
		for k, q := range p.Questions {
			if key := fmt.Sprintf("q%d", k); a.decided(p.ID, key) == "" {
				cs = append(cs, &card{kind: cardQuestion, p: p, key: key, text: q})
			}
		}
		if len(cs) == 0 {
			if p.SourceKind != "agent" {
				continue
			}
		}
		if p.SourceKind == "agent" {
			s.cards = append(s.cards, &card{kind: cardProposal, p: p, key: "proposal"})
		}
		if p.SourceKind != "agent" && len(fixList(p)) >= 2 {
			s.cards = append(s.cards, &card{kind: cardGroup, p: p, key: "g"})
		}
		s.cards = append(s.cards, cs...)
	}
	s.sel = min(s.sel, max(len(s.cards)-1, 0))
	kept := false
	for k, c := range s.cards {
		if c.id() == keep {
			s.sel, kept = k, true
		}
	}
	// Never land on a check's title by accident: one y there accepts
	// every fix.
	if !kept && s.sel+1 < len(s.cards) && s.cards[s.sel].kind == cardGroup {
		s.sel++
	}
}

// selected returns the selected card, or nil.
func (s *sidebar) selected() *card {
	if s.sel < 0 || s.sel >= len(s.cards) {
		return nil
	}
	return s.cards[s.sel]
}

// selectProposal selects the first card of proposal id (past its group
// card: one y there would accept every fix).
func (s *sidebar) selectProposal(id string) {
	for k, c := range s.cards {
		if c.p.ID == id && c.kind != cardGroup {
			s.sel = k
			return
		}
	}
}

// oneLine collapses whitespace.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ---- layout ----

// sideColumnWidth is the responsive sidebar width when it can sit beside
// the editor. Narrow terminals use the sidebar as a deliberate drawer.
func (a *App) sideColumnWidth() int {
	_, _, sw := a.regionWidths()
	if sw > 0 {
		return sw
	}
	return sideWidth
}

// sideColumn reports whether the shown sidebar takes a column of its own
// (else it is drawn over the text).
func (a *App) sideColumn() bool {
	return a.sideShown && a.sideFits()
}

// sideFits reports whether a sidebar column leaves the editor at least
// minEditorWidth.
func (a *App) sideFits() bool {
	// Capacity is independent of current visibility: checks may reveal the
	// sidebar when findings arrive, while callers that render a hidden pane
	// still keep it hidden.
	copy := *a
	copy.sideShown = true
	_, _, sw := copy.regionWidths()
	return sw > 0
}

// sideRect returns the sidebar's left edge and width on screen.
func (a *App) sideRect() (int, int) {
	w := min(sidePreferredWidth, max(a.width-4, 10))
	if a.sideFits() {
		w = a.sideColumnWidth()
	}
	return a.width - w, w
}

// showSide shows the sidebar (without focus).
func (a *App) showSide() {
	a.sideShown = true
	a.layout()
}

// toggleSide is ^R: show and focus the sidebar, or hide it when it has
// focus.
func (a *App) toggleSide() {
	switch {
	case !a.sideShown || !a.sideFocus:
		a.sideShown = true
		a.sideFocus = true
		a.outlineFocus = false
		a.sidePeeked = false
		a.loadPending()
	default:
		a.sideShown = false
		a.sideFocus = false
	}
	a.layout()
}

// ---- ^R: the commit flow ----

// uncommitted reports whether anything differs from HEAD: words,
// wrapping, or an unsaved buffer. It reads the cached comparison, so the
// help bar can ask on every frame.
func (a *App) uncommitted() bool {
	for _, p := range a.ws.Paths() {
		if d, ok := a.ws.Docs()[p]; ok && d.Ed.Buffer().Dirty() {
			return true
		}
		if a.ws.File(p) == nil {
			continue
		}
		if fe, ok := a.fileEditsOf(p, string(a.ws.Source(p))); ok && (len(fe.content) > 0 || len(fe.format) > 0) {
			return true
		}
	}
	return false
}

// uncheckedAnchors are the anchors whose change vs HEAD a check reads
// (needsCheck: not a rename alone) and whose current text no check has
// seen (a check superseded since does not count).
func (a *App) uncheckedAnchors() []string {
	if a.ws.Spec == nil {
		return nil
	}
	all, err := a.wf.Store(a.ws.Root).List()
	if err != nil {
		lg.WithError(err).Warn("cannot list proposals")
	}
	var out []string
	for _, an := range a.ws.Spec.Anchors {
		if !a.needsCheck(an) {
			continue
		}
		f := a.ws.Spec.File(an.File)
		if f == nil || !an.BodyRange.Valid(len(f.Source())) {
			continue
		}
		text := f.Text(an.BodyRange)
		seen := slices.ContainsFunc(all, func(p *proposal.Proposal) bool {
			superseded := p.Decision != nil && strings.HasPrefix(p.Decision.Reason, "superseded")
			return p.Primary != nil && p.Primary.Name == "$"+an.Name && slices.Contains(p.CheckedTexts(), text) && !superseded
		})
		if !seen && !a.clearedText("$"+an.Name, text) {
			out = append(out, an.Name)
		}
	}
	return out
}

// commitFlow is ^R from the editor or the outline: show and focus the
// sidebar (hide it when it has focus) and, when something differs from
// HEAD, check every changed anchor no check has seen yet, in the
// background; A in the sidebar opens the commit view.
func (a *App) commitFlow() tea.Cmd {
	opening := !a.sideShown || !a.sideFocus
	a.toggleSide()
	// From a model ^R only reaches the cards: the check reads spec anchors.
	if !opening || a.inModel() || !a.uncommitted() {
		return nil
	}
	names := a.uncheckedAnchors()
	switch {
	case len(names) == 0:
		if r := a.renamesText(); r != "" && !a.anyNeedsCheck() {
			a.setFlash("%s", nothingToCheckRenamed(r))
			return nil
		}
		a.setFlash("Every changed anchor is checked")
		return nil
	case a.job != nil:
		a.setFlash("A check is running")
		return nil
	}
	if _, _, e := a.wf.OpenProviderConfig(); e != nil {
		a.setFlash("No model to check %s with (spec doctor says why)", "$"+strings.Join(names, ", $"))
		return nil
	}
	return a.saveFirst("Check", func() tea.Cmd { return a.runPropose(names) })
}

// ---- keys ----

// sideKey runs a key of the focused sidebar.
func (a *App) sideKey(b Binding, ok bool) tea.Cmd {
	if !ok {
		return nil
	}
	s := a.side
	c := s.selected()
	switch b.Action {
	case ActUp:
		s.sel = max(s.sel-1, 0)
		a.peek()
	case ActDown:
		s.sel = min(s.sel+1, max(len(s.cards)-1, 0))
		a.peek()
	case ActCancel:
		a.sideFocus = false
	case ActNextPane:
		a.nextPane()
	case ActReview:
		a.toggleSide()
	case ActAcceptHunk:
		return a.acceptCard(c)
	case ActSkipHunk:
		return a.dismissCard(c)
	case ActEditHunk:
		a.editCard(s.fixCard(c))
	case ActFeedback:
		if c != nil && c.kind == cardQuestion {
			return a.answerCard(c)
		}
		return a.feedbackCard(s.fixCard(c))
	case ActAccept:
		a.gotoCard(c)
	case ActFormat:
		if c != nil && c.kind != cardProposal {
			// ^F is about one fix: a problem's single fix is selected,
			// so its formatted preview shows.
			c = s.fixCard(c)
			if k := slices.Index(s.cards, c); k >= 0 {
				s.sel = k
			}
		}
		a.toggleCardFormat(c)
	case ActShowRemoved:
		a.showRemoved = !a.showRemoved
		if a.showRemoved {
			a.setFlash("Removed words shown as ghost text (r hides them)")
		} else {
			a.setFlash("Removed words hidden (r shows them)")
		}
	case ActMessage:
		a.editMessage()
	case ActCommitView:
		return a.saveFirst("Commit", a.openCommit)
	case ActWriteOut:
		return a.save(false)
	case ActHelp:
		a.push(newHelpScreen())
	case ActExit:
		return a.exit()
	}
	return nil
}

// toggleCardFormat is ^F on a card: accept it formatted (as ^F formats
// the buffer), previewed so, or not.
func (a *App) toggleCardFormat(c *card) {
	if c == nil || (c.kind != cardEdit && c.kind != cardProposal) {
		a.setFlash("Only a fix or a proposal can be accepted formatted: select the fix")
		return
	}
	if c.p.IsModel() {
		a.setFlash("A TLA+ model is not formatted: y applies the edit as it is")
		return
	}
	if a.side.format == nil {
		a.side.format = map[string]bool{}
	}
	if a.side.format[c.id()] {
		delete(a.side.format, c.id())
		a.setFlash("Accepting applies the edit as it is (^F formats it too)")
		return
	}
	a.side.format[c.id()] = true
	a.setFlash("Formatted preview: y applies the edit and the format, one ^Z undoes both")
}

// cardBlocked says why y cannot apply c ("" when it can).
func (a *App) cardBlocked(c *card) string {
	if c == nil {
		return "No card is selected"
	}
	if c.kind == cardQuestion {
		return "Nothing to apply: f answers the question and the model rethinks the check; n dismisses it"
	}
	if c.p != nil && c.p.ChecksBlocking() {
		if c.p.CheckMessage != "" {
			return "Required check is " + c.p.CheckState + ": " + c.p.CheckMessage
		}
		return "Required check is " + c.p.CheckState + "; wait for it to finish before applying"
	}
	if c.p != nil && a.ws != nil && a.ws.Spec != nil {
		if e := workflow.CheckSnapshot(c.p, a.ws.Spec); e != nil {
			return e.Message
		}
	}
	if a.agentChildNeedsParent(c) {
		return "Apply the parent proposal first"
	}
	if c.kind == cardProposal {
		return ""
	}
	if why := a.checkOutdated(c.p); why != "" {
		return "Outdated: " + why + "; ^P rechecks, n dismisses it"
	}
	switch c.kind {
	case cardEdit:
		switch a.fixState(c.p, c.hunk) {
		case fixAccepted, fixEdited:
			return "Already applied (^Z in the editor undoes it)"
		case fixByHand:
			return "Already recorded as done by hand"
		case fixGone:
			return "No longer applies: the text it replaces is gone; n skips it, ^P rechecks"
		case fixStale:
			return "Stale: " + a.cardStale(c) + "; f asks the model to redo it, n skips it"
		}
	case cardProblem:
		if len(c.prob.fixes) == 0 {
			return "Nothing to apply: no fix proposed; edit by hand, then ^P (n dismisses it)"
		}
		if len(a.pendingFixes(c.p, c.prob.fixes)) == 0 {
			return "No fix of this problem is left to apply; n dismisses it"
		}
	case cardGroup:
		if len(a.pendingFixes(c.p, fixesOf(c))) == 0 {
			return "No fix of this check is left to apply"
		}
	}
	return ""
}

// agentChildNeedsParent keeps generated fixes anchored to the proposed
// parent change until that primary hunk is already present.
func (a *App) agentChildNeedsParent(c *card) bool {
	return c != nil && c.p != nil && c.p.SourceKind == "agent" && c.kind != cardProposal && c.p.Primary != nil &&
		!c.p.Primary.InWorktree && !c.p.AppliedHunk(c.p.Primary.N)
}

// cardStale says why fix c no longer applies to its buffer ("" when it
// does).
func (a *App) cardStale(c *card) string {
	if c.kind != cardEdit {
		return ""
	}
	src := string(a.ws.Source(c.hunk.File))
	if src == "" && a.ws.File(c.hunk.File) == nil {
		return c.hunk.File + " is no longer a spec file"
	}
	if n := strings.Count(src, c.hunk.Find); n != 1 {
		return fmt.Sprintf("the text it replaces occurs %d times in %s now", n, c.hunk.File)
	}
	return ""
}

// acceptCard is y: apply the card into the buffers.
func (a *App) acceptCard(c *card) tea.Cmd {
	if why := a.cardBlocked(c); why != "" {
		a.setFlash("%s", why)
		return nil
	}
	switch c.kind {
	case cardEdit:
		if st := a.fixState(c.p, c.hunk); st == fixHand || st == fixHandUnsure {
			return a.recordByHand(c)
		}
		return a.acceptFixes(c.p, []finding.Hunk{c.hunk}, decAccepted, a.side.format[c.id()], c.id())
	case cardProblem, cardGroup:
		format := false
		if fc := a.side.fixCard(c); fc != nil {
			format = a.side.format[fc.id()]
		}
		return a.acceptFixes(c.p, a.pendingFixes(c.p, fixesOf(c)), decAccepted, format, c.id())
	case cardProposal:
		if a.refuseWhileWaiting(c.p.ID) || !a.modelGate(c.p) {
			return nil
		}
		p, format := c.p, a.side.format[c.id()]
		return a.underLock(p.ID, "apply "+p.ID, func(a *App, lock *project.Lock) tea.Cmd {
			return a.applyProposal(lock, p, format)
		})
	}
	return nil
}

// applyProposal applies agent proposal p (as shown) into the buffers, with
// .spec/lock held: what y on its card does. The save records it (unsaved.go).
func (a *App) applyProposal(lock *project.Lock, p *proposal.Proposal, format bool) tea.Cmd {
	accept := a.proposalHunksToApply(p)
	if len(accept) == 0 {
		a.setFlash("Nothing left to apply: the successful edits are already in the buffer")
		return nil
	}
	prep, edits, e := a.applyInto(p, accept, format)
	if e != nil {
		return nil
	}
	d := proposal.Decision{Accepted: accept, Applied: prep.Applied, Failed: prep.Failed, By: proposal.AuthorHuman}
	a.holdApply(lock, p, &d, true, edits, nil)
	failed := failedText(prep.Failed)
	a.appliedFlash(proposalName(p), failed, fmt.Sprintf("Applied %s%s into the buffer (^O saves; nothing is committed)%s", p.ID, authorClause(p.Author), failed))
	a.saveChecks()
	a.loadPending()
	return a.edited()
}

func (a *App) proposalHunksToApply(p *proposal.Proposal) []int {
	var ns []int
	if p.Primary != nil && !p.AppliedHunk(p.Primary.N) {
		ns = append(ns, p.Primary.N)
	}
	for _, h := range p.Hunks {
		if !p.AppliedHunk(h.N) && a.decided(p.ID, hunkKey(h.N)) != decDismissed {
			ns = append(ns, h.N)
		}
	}
	return ns
}

// applyInto applies hunks accept of seen (the proposal as shown) into the
// editor's buffers, each touched file as one undo step, and moves the
// cursor to the first edit (after a wait for the lock it records it for
// ^] instead: lockwait.go). With format, each touched file is also
// formatted as ^F formats it, in the same undo step. It returns each changed
// file's history state after the edit. When nothing was applied it says why
// and returns the error.
func (a *App) applyInto(seen *proposal.Proposal, accept []int, format bool) (*proposal.Prepared, map[string]uint64, *workflow.Error) {
	prep, results, e := a.wouldBe(seen, accept, format)
	if e != nil {
		switch e.Code {
		case "not_pending":
			a.loadPending()
			if e.Proposal != nil {
				a.setFlash("Not applied: %s was already %s by another spec process; nothing changed here", e.Proposal.ID, e.Proposal.Status)
			} else {
				a.setFlash("Not applied: %s", e.Message)
			}
		case "changed":
			a.loadPending()
			a.setFlash("Not applied: %s", e.Message)
		case "internal":
			a.setFlash("Not applied: %s", e.Message)
		default:
			a.notice("Not applied", e)
		}
		return nil, nil, e
	}
	if format {
		for _, r := range results {
			if r.unformatted == "" {
				continue
			}
			message := fmt.Sprintf("not applied: could not format %s: %s", r.path, r.unformatted)
			a.setFlash("Not applied: could not format %s: %s", r.path, r.unformatted)
			return nil, nil, &workflow.Error{Code: "format_refused", Message: message}
		}
	}
	// Resolve every destination before touching any buffer. An unavailable
	// later file must not leave earlier edits applied without a durable receipt.
	docs := make(map[string]*Doc, len(results))
	for _, r := range results {
		d, err := a.ws.Doc(r.path)
		if err != nil {
			a.setFlash("Cannot open %s: %v", r.path, err)
			return nil, nil, &workflow.Error{Code: "internal", Message: err.Error()}
		}
		docs[r.path] = d
	}
	var first *Location
	edits := make(map[string]uint64, len(results))
	for _, r := range results {
		d := docs[r.path]
		d.Ed.ReplaceAll(r.after, mapOffset(r.before, r.after, d.Ed.Cursor()))
		if r.after != r.before {
			edits[r.path] = d.Ed.Buffer().StateID()
		}
		if len(seen.CheckFiles) > 0 {
			seen.CheckFiles[r.path] = proposal.FileHash([]byte(r.after))
		}
		if first == nil {
			first = &Location{Path: r.path, Off: mapOffset(r.before, r.after, r.start)}
		}
	}
	switch {
	case first != nil && a.afterWait:
		a.appliedAt = &landing{loc: *first}
	case first != nil:
		a.jump(*first)
	}
	return prep, edits, nil
}

// fileResult is one file as accepting would leave it.
type fileResult struct {
	path          string
	before, after string
	// start is where the first edit is, in before.
	start int
	// unformatted says why format was asked for but not done ("" when
	// it was, or was not asked for).
	unformatted string
}

// wouldBe computes what accepting hunks accept of seen would do to the
// buffers, formatted as ^F formats when format is set, without changing
// buffer contents. Preparation may persist a stale-primary receipt.
func (a *App) wouldBe(seen *proposal.Proposal, accept []int, format bool) (*proposal.Prepared, []fileResult, *workflow.Error) {
	overlay := map[string][]byte{}
	for p, d := range a.ws.Docs() {
		overlay[p] = d.Ed.Buffer().Bytes()
	}
	var prep *proposal.Prepared
	var e *workflow.Error
	if a.heldLock != nil {
		prep, e = a.wf.PrepareIntoLocked(a.heldLock, a.ws.Project, seen, accept, overlay)
	} else {
		prep, e = a.wf.PrepareInto(a.ws.Project, seen, accept, overlay)
	}
	if e != nil {
		return nil, nil, e
	}
	var out []fileResult
	for _, fe := range prep.Edits {
		after, err := edit.Apply(fe.Snapshot, fe.Splices)
		if err != nil {
			return nil, nil, &workflow.Error{Code: "internal", Message: err.Error()}
		}
		r := fileResult{path: fe.Path, before: string(fe.Snapshot), after: string(after), start: len(fe.Snapshot)}
		for _, sp := range fe.Splices {
			r.start = min(r.start, sp.Range.Start)
		}
		if format {
			r.after, r.unformatted = formatText(a, fe.Path, r.after)
		}
		out = append(out, r)
	}
	return prep, out, nil
}

// formatText formats src (the text of path) as ^F does; on refusal it
// returns src and why.
func formatText(a *App, path, src string) (string, string) {
	res, err := fmtpkg.Source(path, []byte(src), workflow.FormatOptions(a.ws.Project))
	if err != nil {
		var refused *fmtpkg.RefusedError
		if errors.As(err, &refused) {
			return src, refused.Reason
		}
		return src, err.Error()
	}
	return string(res.Text), ""
}

// formatPreview is the diff a formatted accept would make around its
// edits (whole lines of before and after), and how many lines it changes
// elsewhere.
func formatPreview(r fileResult) (before, after string, elsewhere int) {
	runs := changedRuns(r.before, r.after)
	if len(runs) == 0 {
		return "", "", 0
	}
	// The run that holds the first edit (in after coordinates).
	at := mapOffset(r.before, r.after, r.start)
	pick := 0
	for k, run := range runs {
		if run[0] <= at && at <= run[1] {
			pick = k
		}
	}
	for k, run := range runs {
		if k != pick {
			elsewhere += strings.Count(r.after[run[0]:run[1]], "\n") + 1
		}
	}
	lineStart := func(s string, off int) int { return strings.LastIndexByte(s[:off], '\n') + 1 }
	lineEnd := func(s string, off int) int {
		if i := strings.IndexByte(s[off:], '\n'); i >= 0 {
			return off + i
		}
		return len(s)
	}
	run := runs[pick]
	a0, a1 := lineStart(r.after, run[0]), lineEnd(r.after, max(run[1]-1, run[0]))
	// The same lines in before: map the run's ends back.
	b0 := lineStart(r.before, mapOffset(r.after, r.before, a0))
	b1 := lineEnd(r.before, mapOffset(r.after, r.before, a1))
	return r.before[b0:b1], r.after[a0:a1], elsewhere
}

// settleIfDone records p's decision once every item of it is decided:
// applied with the accepted hunks (and a check's own change), or rejected
// when nothing was accepted. It records every other fully decided check
// too (one whose record waited for .spec/lock in vain).
func (a *App) settleIfDone(p *proposal.Proposal, held ...*project.Lock) tea.Cmd {
	st := &a.side.state
	if st.Decided == nil {
		st.Decided = map[string]map[string]string{}
	}
	if st.Decided[p.ID] == nil {
		// Marks p as Chris's to settle, even with no item to decide.
		st.Decided[p.ID] = map[string]string{}
	}
	done := a.decidedProposals()
	if len(done) == 0 || a.waitingOn(settleAll) {
		return nil
	}
	var ids []string
	for _, q := range done {
		ids = append(ids, q.ID)
	}
	run := func(a *App, lock *project.Lock) tea.Cmd {
		a.settleDecided(lock)
		a.loadPending()
		return nil
	}
	if len(held) > 0 {
		return run(a, held[0])
	}
	return a.underLock(settleAll, "record "+strings.Join(ids, ", ")+" as done", run)
}

// settleDecided records every decided check, with .spec/lock held, and
// returns the records it wrote.
func (a *App) settleDecided(lock *project.Lock) []*proposal.Proposal {
	var out []*proposal.Proposal
	for _, q := range a.decidedProposals() {
		d := proposal.Decision{Accepted: a.acceptedOf(q), Applied: a.acceptedOf(q), By: proposal.AuthorHuman}
		rec, e := a.wf.SettleSeenLocked(lock, a.ws.Root, q, d)
		if e != nil {
			if e.Code != "not_pending" {
				lg.WithError(e).Warn("cannot settle " + q.ID)
			}
			continue
		}
		out = append(out, rec)
	}
	return out
}

// decidedProposals are the pending checks whose every problem, fix and
// question Chris has settled, which are not recorded as done yet. A check
// with a change no save has made true waits for that save.
func (a *App) decidedProposals() []*proposal.Proposal {
	var out []*proposal.Proposal
	for _, p := range a.pending {
		if _, ok := a.side.state.Decided[p.ID]; !ok || !suggestions(p) || len(a.bufferedOf(p.ID)) > 0 {
			continue
		}
		if a.proposalSettled(p) {
			out = append(out, p)
		}
	}
	return out
}

// acceptedOf are the hunks of p to record as applied: a check's own
// change and the accepted hunks.
func (a *App) acceptedOf(p *proposal.Proposal) []int {
	var accepted []int
	if pr := p.Primary; pr != nil && (pr.InWorktree || p.AppliedHunk(pr.N)) {
		accepted = append(accepted, pr.N)
	}
	for _, h := range fixList(p) {
		if p.AppliedHunk(h.N) || applied(a.decided(p.ID, hunkKey(h.N))) {
			accepted = append(accepted, h.N)
		}
	}
	return accepted
}

// dismissCard is n: skip a fix, dismiss a problem (and the fixes only it
// wants) or a question; reject an agent's proposal (with a reason).
func (a *App) dismissCard(c *card) tea.Cmd {
	if c == nil {
		a.setFlash("No card is selected")
		return nil
	}
	switch c.kind {
	case cardProposal:
		if !a.refuseWhileWaiting(c.p.ID) {
			a.prompt = &prompt{kind: promptReject, label: "Reason to reject " + c.p.ID + " (sent to its author)", reject: c.p.ID}
		}
		return nil
	case cardGroup:
		a.setFlash("n skips one fix or dismisses one problem: select it (y here accepts every pending fix)")
		return nil
	case cardEdit:
		if applied(a.decided(c.p.ID, c.key)) {
			a.setFlash("Already applied: ^Z in the editor undoes it")
			return nil
		}
		a.decide(c.p, c.key, decDismissed)
		a.setFlash("Skipped the fix")
	case cardProblem:
		a.dismissProblem(c)
		a.setFlash("Dismissed")
	default:
		a.decide(c.p, c.key, decDismissed)
		a.setFlash("Dismissed")
	}
	settle := a.settleIfDone(c.p)
	a.saveChecks()
	a.loadPending()
	a.advanceFrom(c.id())
	a.peek()
	return settle
}

// rejectProposal rejects proposal id with reason and closes its review if
// open.
func (a *App) rejectProposal(id, reason string) tea.Cmd {
	if a.refuseWhileWaiting(id) {
		return nil
	}
	return a.underLock(id, "reject "+id, func(a *App, lock *project.Lock) tea.Cmd {
		p, e := a.wf.RejectLocked(lock, a.ws.Root, id, reason, proposal.AuthorHuman)
		if e != nil {
			if e.Code == "not_pending" {
				a.loadPending()
				a.setFlash("Not rejected: %s (decided by another spec process meanwhile)", e.Message)
				return nil
			}
			a.notice("Reject failed", e)
			return nil
		}
		for _, s := range a.screens {
			if r, ok := s.(*reviewScreen); ok && r.p.ID == id {
				a.remove(r)
			}
		}
		a.loadPending()
		a.setFlash("Rejected %s: %s", p.ID, reason)
		return nil
	})
}

// editCard is e: edit a fix's replacement, then apply it.
func (a *App) editCard(c *card) {
	if c == nil || c.kind != cardEdit {
		a.setFlash("Only a fix can be edited: select the fix")
		return
	}
	if why := a.checkOutdated(c.p); why != "" {
		a.setFlash("Outdated: %s; ^P rechecks", why)
		return
	}
	if a.fixState(c.p, c.hunk) != fixPending {
		a.setFlash("%s", a.cardBlocked(c))
		return
	}
	p, h := c.p, c.hunk
	var s *hunkEditScreen
	s = newHunkEditScreen(a, p, h, func(a *App, text string) tea.Cmd {
		h.Replace = text
		return a.keepHunk(s, p, h, func(a *App, cur *proposal.Proposal) tea.Cmd {
			a.loadPending()
			for _, nc := range a.side.cards {
				if nc.p.ID == cur.ID && nc.key == c.key {
					cmd := a.acceptFixes(nc.p, []finding.Hunk{nc.hunk}, decEdited, a.side.format[nc.id()], nc.id())
					a.fresh() // the edited buffer's parse, not left to a debounce
					return cmd
				}
			}
			return nil
		})
	})
	a.push(s)
}

// keepHunk stores edited hunk h of proposal id, with .spec/lock held,
// then runs kept with the proposal as stored. The small editor s closes
// once the edit is kept; while the edit waits for the lock it is closed,
// and it opens again, with the text, when the edit cannot be kept.

func (a *App) keepHunk(s *hunkEditScreen, seen *proposal.Proposal, h finding.Hunk, kept func(a *App, cur *proposal.Proposal) tea.Cmd) tea.Cmd {
	id := seen.ID
	if a.refuseWhileWaiting(id) {
		return nil
	}
	cmd := a.underLock(id, fmt.Sprintf("keep the edit of hunk %d of %s", h.N, id), func(a *App, lock *project.Lock) tea.Cmd {
		cur, e := a.wf.SetHunkSeenLocked(lock, a.ws.Root, seen, h)
		if e != nil {
			if !slices.Contains(a.screens, screen(s)) {
				a.push(s)
			}
			a.notice("Cannot keep the edit", e)
			return nil
		}
		a.remove(s)
		return kept(a, cur)
	})
	if a.waitingOn(id) {
		a.remove(s)
	}
	return cmd
}

// feedbackCard is f: ask for feedback on a suggested edit; the model redoes
// it.
func (a *App) feedbackCard(c *card) tea.Cmd {
	if c == nil || c.kind != cardEdit {
		a.setFlash("Only a fix can be redone with feedback: select the fix")
		return nil
	}
	if a.busy() {
		return nil
	}
	p, n := c.p, c.hunk.N
	a.push(newFeedbackScreen(a, "Feedback on this edit: the model redoes it", c.hunk.Find, func(a *App, text string) tea.Cmd {
		return a.reviseCard(p, n, text)
	}))
	return nil
}

// reviseCard runs the revision of hunk n of p with feedback in the
// background; the sidebar shows the new text.
func (a *App) reviseCard(p *proposal.Proposal, n int, feedback string) tea.Cmd {
	var h finding.Hunk
	found := false
	for _, x := range p.Hunks {
		if x.N == n {
			h, found = x, true
		}
	}
	if !found || a.busy() {
		return nil
	}
	wf, root := a.wf, a.ws.Root
	return a.startJob("Revising the edit", func(ctx context.Context) func(*App) tea.Cmd {
		ck, e := wf.OpenChecker(ctx, false)
		var rev *workflow.Revision
		if e == nil {
			rev, e = wf.ReviseHunk(ck, root, p, h, feedback)
		}
		return func(a *App) tea.Cmd {
			if e != nil {
				a.notice("Revising the edit failed", e)
				return nil
			}
			a.loadPending()
			if rev.Withdrawn {
				a.setFlash("The model withdrew the edit; n dismisses it")
				return nil
			}
			a.setFlash("Edit revised; y applies it")
			return nil
		}
	})
}

// cardLoc is where card c points, and whether it points anywhere.
func (a *App) cardLoc(c *card) (Location, bool) {
	if c == nil {
		return Location{}, false
	}
	file, anchor := "", c.anchor()
	switch c.kind {
	case cardEdit:
		file = c.hunk.File
		if l, ok := a.hunkLoc(c.hunk); ok {
			return l, true
		}
	case cardProblem:
		return a.problemLoc(c)
	case cardGroup:
		if checkOf(c.p) {
			if l, ok := a.anchorAt(c.p.Primary.Name); ok {
				return l, true
			}
		}
		return Location{}, false
	case cardQuestion:
		return Location{}, false
	case cardProposal:
		if c.p.IsModel() {
			return a.modelLoc(c.p)
		}
		if pr := c.p.Primary; pr != nil {
			file = pr.File
		}
	}
	if anchor != "" && a.ws.Spec != nil {
		as := a.ws.Spec.Lookup(strings.TrimPrefix(anchor, "$"))
		for _, an := range as {
			if file == "" || an.File == file {
				return anchorLoc(an), true
			}
		}
	}
	if file != "" && a.ws.File(file) != nil {
		return Location{Path: file}, true
	}
	return Location{}, false
}

// peek shows the selected card's place in the editor (the sidebar keeps
// focus). Only the place the editor had before the first peek goes on the
// jump history, so ^B returns there.
func (a *App) peek() {
	loc, ok := a.cardLoc(a.side.selected())
	if !ok {
		return
	}
	from := a.here()
	if err := a.open(loc); err != nil {
		return
	}
	a.cur.Ed.CenterCursor()
	if !a.sidePeeked && from != a.here() {
		a.back = append(a.back, from)
		a.fwd = nil
		a.sidePeeked = true
	}
}

// gotoCard is Enter: go to the card's place and give the editor focus (an
// agent's proposal opens its review, hunk by hunk).
func (a *App) gotoCard(c *card) {
	if c != nil && c.kind == cardProposal {
		a.openReview(c.p.ID)
		return
	}
	loc, ok := a.cardLoc(c)
	if !ok {
		a.setFlash("This card names no place in the spec")
		return
	}
	a.sideFocus = false
	a.jump(loc)
}

// formattedPreview renders what accepting card c formatted would do: the
// buffer's lines around the edit before and after, and the lines the
// format changes elsewhere.
func (a *App) formattedPreview(c *card, inner int) []string {
	th := a.theme
	accept := []int{c.hunk.N}
	if c.kind == cardProposal {
		accept = nil
		if c.p.Primary != nil {
			accept = append(accept, c.p.Primary.N)
		}
		for _, h := range c.p.Hunks {
			accept = append(accept, h.N)
		}
	}
	_, results, e := a.wouldBe(c.p, accept, true)
	if e != nil {
		return []string{th.Stale.Render(ansi.Truncate("cannot preview: "+e.Message, inner, th.uiText("…")))}
	}
	out := []string{th.SideGroup.Render("^F formatted: y applies both")}
	for _, r := range results {
		if r.unformatted != "" {
			out = append(out, th.Stale.Render(ansi.Truncate("not formatted: "+r.unformatted, inner, th.uiText("…"))))
		}
		before, after, elsewhere := formatPreview(r)
		d := diffLines(th, before, after, inner, "")
		if len(d) > 14 {
			d = append(d[:13], th.PopupDim.Render(fmt.Sprintf(th.uiText("… %d more lines"), len(d)-13)))
		}
		out = append(out, d...)
		if elsewhere > 0 {
			out = append(out, th.PopupDim.Render(fmt.Sprintf("and reformats %s elsewhere in %s", plural(elsewhere, "line"), r.path)))
		}
	}
	return out
}

// ---- mouse ----

// sideClick selects the card at pane line y (and focuses the sidebar).
func (a *App) sideClick(y int) {
	s := a.side
	if !a.sideFocus {
		a.sidePeeked = false
	}
	a.sideFocus = true
	a.outlineFocus = false
	line := y + s.scroll
	for k := range s.cards {
		if k < len(s.starts) && line >= s.starts[k] && line < s.ends[k] {
			s.sel = k
			a.peek()
			return
		}
	}
}

// ---- view ----

// sevColor is the colour of a card of severity sev.
func (t *Theme) sevColor(sev string) color.Color {
	switch sev {
	case finding.SeverityBreaking:
		return t.bad
	case finding.SeverityInconsistent:
		return t.diagWarn
	case finding.SeverityCosmetic:
		return t.diagHint
	case "question":
		return t.def
	case "proposal":
		return t.inv
	}
	return t.dim
}

// sideView renders the sidebar: h lines of w cells.
func (a *App) sideView(w, h int) []string {
	th := a.theme
	s := a.side
	// The left edge is the focus indicator: a thick accent line while the
	// sidebar has the keyboard, a thin dim one otherwise.
	edgeGlyph := "│"
	if th.ASCII {
		edgeGlyph = "|"
	}
	edge := th.Border.Render(edgeGlyph)
	if a.sideFocus {
		edge = th.FocusEdge.Render(edgeGlyph)
	}
	w = max(w-1, 8)
	inner := max(w-4, 8) // border and padding
	var lines []string
	head := "Review"
	_, _, _, proposals, other := a.statusTotals()
	awaiting := proposals + other
	if awaiting > 0 {
		sep := " · "
		if th.ASCII {
			sep = " - "
		}
		head += sep + fmt.Sprint(awaiting) + " awaiting"
	}
	headStyle := th.SideHead
	if a.sideFocus {
		headStyle = th.SideHeadFocus
	}
	lines = append(lines, headStyle.Render(padTo(ansi.Truncate(head, w, th.uiText("…")), w)))
	s.starts, s.ends = make([]int, len(s.cards)), make([]int, len(s.cards))
	for _, r := range s.runs {
		lines = append(lines, "", th.SideGroup.Render(padTo(ansi.Truncate(termtext.Clean(" check "+r.anchor), w, th.uiText("…")), w)))
		lines = append(lines, a.runBox(r, inner)...)
	}
	if len(s.cards) == 0 && len(s.runs) == 0 {
		for _, l := range wrapText("Nothing to review. ^P checks your edit or fixes a warning, ^N analyzes the edits since HEAD; what they find shows here. A opens the commit view.", w-2) {
			lines = append(lines, " "+th.PopupDim.Render(l))
		}
	}
	var group string
	var groupProposal *proposal.Proposal
	groupStart := 0
	hasCard := false
	var lastCard *card
	lastIndex := -1
	finishGroup := func() {
		if groupProposal == nil {
			return
		}
		if a.height >= 30 && lastCard != nil && lastCard.kind != cardEdit {
			lines = append(lines, filledParentLine("", w, cardOriginStyle(th, groupProposal.SourceKind), th, lastIndex == s.sel))
		}
		selected := s.selected() != nil && s.selected().p.ID == groupProposal.ID && s.selected().kind != cardEdit
		for y := groupStart; y < len(lines); y++ {
			lines[y] = a.parentRow(lines[y], groupProposal, w, selected)
		}
	}
	var shownCluster *cluster
	for k, c := range s.cards {
		if c.p.ID != group {
			finishGroup()
			group = c.p.ID
			groupProposal = c.p
			lastCard, lastIndex = nil, -1
			hasCard = false
			shownCluster = nil
			lines = append(lines, "")
			groupStart = len(lines)
			if a.height > 36 {
				lines = append(lines, "")
			}
			headAt := len(lines)
			if c.kind != cardProposal {
				lines = append(lines, a.groupHead(c.p, w, c.kind == cardGroup && k == s.sel)...)
				switch c.p.CheckState {
				case "queued":
					lines = append(lines, " "+th.PopupDim.Render(a.progressMark()+" Check queued"))
				case "running":
					lines = append(lines, " "+th.PopupDim.Render(a.progressMark()+" Checking"))
				case "failed":
					message := "Check failed"
					if c.p.CheckMessage != "" {
						message += th.uiText(" · ") + c.p.CheckMessage
					}
					lines = append(lines, " "+th.Stale.Render(message))
				case "stale":
					lines = append(lines, " "+th.Stale.Render("Needs refresh"))
				}
				if d := c.p.Decision; d != nil && len(d.Applied) > 0 && len(d.Failed) > 0 {
					lines = append(lines, " "+th.Stale.Render(fmt.Sprintf(th.uiText("Partly applied · %d applied, %d failed"), len(d.Applied), len(d.Failed))))
					for _, failure := range d.Failed {
						label := fmt.Sprintf("A follow-on edit failed: %s", failure.Reason)
						for _, row := range wrapText(termtext.Clean(oneLine(label)), w-2) {
							lines = append(lines, " "+th.PopupDim.Render(row))
						}
					}
				}
				if why := a.checkOutdated(c.p); why != "" {
					for _, l := range wrapText(termtext.Clean(th.uiText("Needs refresh · ")+why), w-2) {
						lines = append(lines, " "+th.Stale.Render(l))
					}
				}
				if e := a.checkSnapshotError(c.p); e != nil {
					for _, l := range wrapText(termtext.Clean(th.uiText("Needs refresh · ")+e.Message), w-2) {
						lines = append(lines, " "+th.Stale.Render(l))
					}
				}
				if c.kind == cardGroup {
					s.starts[k], s.ends[k] = headAt, len(lines)
					lastCard, lastIndex = c, k
					continue
				}
			}
		}
		if c.kind == cardEdit && a.height >= 30 && (lastCard == nil || lastCard.kind != cardEdit) {
			// One parent-coloured inset before the first fix. Later fixes
			// already have the previous fix's trailing row between them.
			lines = append(lines, filledParentLine("", w, cardOriginStyle(th, c.p.SourceKind), th, lastIndex == s.sel))
		}
		if c.kind != cardEdit && hasCard && (a.height < 30 || lastCard == nil || lastCard.kind != cardEdit) {
			spacer := ""
			if a.height >= 30 && lastCard != nil {
				spacer = filledParentLine("", w, cardOriginStyle(th, c.p.SourceKind), th, lastIndex == s.sel)
			}
			lines = append(lines, spacer)
		}
		if c.kind != cardEdit {
			hasCard = true
		}
		if c.cl != nil && c.cl != shownCluster {
			shownCluster = c.cl
			if title := c.cl.head(); title != "" {
				lines = append(lines, " "+th.HunkHead.Render(ansi.Truncate(title, w-1, th.uiText("…"))))
			}
		}
		s.starts[k] = len(lines)
		switch c.kind {
		case cardProblem:
			lines = append(lines, a.problemBox(c, k == s.sel, inner)...)
		case cardEdit:
			lines = append(lines, a.fixBox(c, k == s.sel, inner)...)
		default:
			lines = append(lines, a.cardBox(c, k == s.sel, inner)...)
		}
		s.ends[k] = len(lines)
		lastCard, lastIndex = c, k
	}
	finishGroup()
	// Reveal on navigation, reflow or a moved selected object. A plain wheel
	// scroll may leave the selection off-screen; rendering must not snap it back.
	if len(s.cards) > 0 {
		st, en := s.starts[s.sel], s.ends[s.sel]
		id := s.cards[s.sel].id()
		if id != s.revealedID || st != s.revealedAt || w != s.revealedW || h != s.revealedH {
			switch {
			case st-1 < s.scroll:
				s.scroll = max(st-2, 0)
			case en > s.scroll+h:
				s.scroll = min(st, en-h)
			}
		}
		s.revealedID, s.revealedAt, s.revealedW, s.revealedH = id, st, w, h
	}
	s.scroll = min(max(s.scroll, 0), max(len(lines)-h, 0))
	out := make([]string, 0, h)
	for k := s.scroll; k < len(lines) && len(out) < h; k++ {
		out = append(out, edge+padTo(lines[k], w))
	}
	for len(out) < h {
		out = append(out, edge+strings.Repeat(" ", w))
	}
	return out
}

// cardBox renders a question or an agent's proposal as a bordered box
// whose content is inner cells wide (problems and fixes: fixcards.go). The
// selected card shows everything; the others a summary.
func (a *App) cardBox(c *card, sel bool, inner int) []string {
	th := a.theme
	if c.kind == cardProposal {
		body := []string{th.Focus.Bold(true).Render(proposalLabel(c.p))}
		if c.p.Author != "" {
			body = append(body, th.PopupDim.Render("From "+cardValue(c.p.Author, max(inner-5, 1), th)))
		}
		if c.p.IsModel() {
			body = append(body, a.modelCardBody(c.p, inner, sel)...)
			return filledCard(body, inner, cardOriginStyle(th, c.p.SourceKind), th, false, sel)
		}
		if c.p.Primary != nil {
			body = append(body, th.HunkHead.Render(cardValue(c.p.Primary.Name, inner, th)))
			body = append(body, th.PopupDim.Render(settingsPath(oneLine(termtext.Clean(c.p.Primary.File)), inner, th)))
			if n := len(fixList(c.p)); n > 0 {
				word := "fixes"
				if n == 1 {
					word = "fix"
				}
				body = append(body, fmt.Sprintf("%d follow-on %s", n, word))
			}
		}
		switch c.p.CheckState {
		case "queued":
			body = append(body, a.progressMark()+" Check queued")
		case "running":
			body = append(body, a.progressMark()+" Checking")
		case "failed":
			body = append(body, "Check failed")
		case "stale":
			body = append(body, "Needs refresh")
		default:
			if e := a.checkSnapshotError(c.p); e != nil {
				body = append(body, "Needs refresh")
			} else {
				body = append(body, "Ready to review")
			}
		}
		if d := c.p.Decision; d != nil && len(d.Applied) > 0 && len(d.Failed) > 0 {
			body = append(body, "Partly applied", fmt.Sprintf(th.uiText("%d applied · %d need review"), len(d.Applied), len(d.Failed)))
		}
		if c.p.CheckMessage != "" && c.p.CheckState == "failed" {
			for _, line := range wrapText(oneLine(termtext.Clean(c.p.CheckMessage)), inner) {
				body = append(body, th.PopupDim.Render(line))
			}
		}
		return filledCard(body, inner, cardOriginStyle(th, c.p.SourceKind), th, false, sel)
	}
	sev := c.severity()
	outdated := a.checkOutdated(c.p) != ""
	col := th.sevColor(sev)
	text := lipgloss.NewStyle()
	if outdated {
		col = th.dim
		text = th.PopupDim
	}
	var body []string
	add := func(ls ...string) { body = append(body, ls...) }
	wrapped := func(text string, style lipgloss.Style) {
		for _, l := range wrapText(termtext.Clean(oneLine(text)), inner) {
			add(style.Render(l))
		}
	}
	badge := lipgloss.NewStyle().Foreground(col).Bold(true).Render(sev)
	if outdated {
		badge += " " + th.Stale.Render("OUTDATED")
	}
	head := badge
	add(head)
	if a.waitingOn(c.p.ID) {
		wrapped(a.waitText(), th.Stale)
	}
	if an := c.ownAnchor(); an != "" {
		loc := strings.TrimSpace(an)
		if l, ok := a.cardLoc(c); ok && l.Path != "" {
			loc = fmt.Sprintf("%s:%d %s", l.Path, a.ws.Line(l.Path, l.Off), an)
		}
		add(th.PopupDim.Render(ansi.Truncate(termtext.Clean(strings.TrimSpace(loc)), inner, th.uiText("…"))))
	}
	switch c.kind {
	case cardQuestion:
		// The whole question, always: a question cut short cannot be
		// answered.
		wrapped(c.text, text)
		if sel {
			if answerable(c.p) {
				wrapped(th.uiText("f answers it · n dismisses it"), th.PopupDim)
			} else {
				wrapped("Answer by editing the spec.", th.PopupDim)
			}
		}
	}
	return filledCard(body, inner, cardOriginStyle(th, c.p.SourceKind), th, false, sel)
}

func (a *App) checkSnapshotError(p *proposal.Proposal) *workflow.Error {
	if p == nil || a.ws == nil || a.ws.Spec == nil {
		return nil
	}
	return workflow.CheckSnapshot(p, a.ws.Spec)
}

func authorClause(author string) string {
	if author == "" {
		return ""
	}
	return " by " + author
}
