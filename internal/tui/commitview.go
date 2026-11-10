// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/baseline"
	inferprompt "github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/workflow"
)

// commitScreen is the one place that commits (A in the sidebar): every
// change of the files against HEAD as a tickable item, the message, and
// the trailers the ticked items need. A commits exactly the ticked items.
type commitScreen struct {
	head          *mdspec.Spec
	headCommit    string
	snapshot      map[string]string
	refreshNeeded bool
	cur           *mdspec.Spec
	changes       []workflow.Change
	ticked        map[string]bool
	// state is each change's review state ("checked", "2 open", ...).
	state    map[string]string
	sel      int
	scroll   int
	trailers []string
	// starts is each item's line in the last render.
	starts []int

	// draft is the message the model drafted for the ticked changes (""
	// until it arrives, or when it could not); drafting is set while it
	// is on its way, draftSeq numbers the requests (only the latest
	// counts) and draftNote says where the message comes from.
	draft       string
	drafting    bool
	draftSeq    int
	draftNote   string
	cancelDraft context.CancelFunc
}

// draftTimeout bounds the drafting of a commit message; the template
// stands in after it.
const draftTimeout = 90 * time.Second

// draftMsg carries a drafted commit message (or why there is none).
type draftMsg struct {
	screen *commitScreen
	seq    int
	text   string
	err    *workflow.Error
}

// startDraft asks the model, in the background, for a message for the
// ticked changes, unless Chris wrote his own. A newer draft replaces one
// still on its way.
func (s *commitScreen) startDraft(a *App) tea.Cmd {
	if s.cancelDraft != nil {
		s.cancelDraft()
		s.cancelDraft = nil
	}
	s.draftSeq++
	s.draft = ""
	s.draftNote = ""
	if strings.TrimSpace(a.side.state.Message) != "" || len(s.selectedChanges()) == 0 {
		s.drafting = false
		return nil
	}
	in := s.draftInput(a)
	seq := s.draftSeq
	s.drafting = true
	s.draftNote = ""
	ctx, cancel := context.WithTimeout(context.Background(), draftTimeout)
	s.cancelDraft = cancel
	wf := a.wf
	return func() tea.Msg {
		defer cancel()
		m := draftMsg{screen: s, seq: seq}
		ck, e := wf.OpenChecker(ctx, false)
		if e == nil {
			var d *inferprompt.CommitDraft
			if d, e = ck.DraftCommit(in); e == nil {
				m.text = d.Message()
			}
		}
		m.err = e
		return m
	}
}

// onDraft takes a drafted message, if it is the latest one asked for.
func (s *commitScreen) onDraft(a *App, m draftMsg) {
	if m.seq != s.draftSeq {
		return
	}
	s.drafting = false
	s.cancelDraft = nil
	if m.err != nil {
		s.draft = ""
		switch m.err.Code {
		case "provider_unavailable":
			s.draftNote = "a template: no model provider (spec doctor)"
		case "cancelled":
			s.draftNote = "a template: drafting took too long"
		default:
			s.draftNote = "a template: the model did not draft one"
		}
		lg.WithError(m.err).Info("commit message draft")
		return
	}
	s.draft = strings.TrimSpace(termtext.Clean(m.text))
	s.draftNote = "drafted by the model"
}

// draftInput is what the model drafts the message from: what the commit
// holds, the ticked changes (an anchor's text before and after; the words
// changed in prose; a whole file's lines) and the fixes applied. Findings
// left open are not in the commit, so not in the input: a draft once
// listed "Fix ..." bullets for them.
func (s *commitScreen) draftInput(a *App) inferprompt.CommitInput {
	var in inferprompt.CommitInput
	text := func(sp *mdspec.Spec, name string) string {
		if sp == nil {
			return ""
		}
		as := sp.Lookup(strings.TrimPrefix(name, "$"))
		if len(as) != 1 {
			return ""
		}
		f := sp.File(as[0].File)
		if f == nil || !as[0].BodyRange.Valid(len(f.Source())) {
			return ""
		}
		return f.Text(as[0].BodyRange)
	}
	for _, c := range s.selectedChanges() {
		if c.Whole() {
			in.Changes = append(in.Changes, inferprompt.CommitChange{What: c.Label(), Words: c.WholeLines(20)})
			continue
		}
		if !c.Prose() {
			for _, x := range c.Anchors {
				what, before := fmt.Sprintf("%s %s %s", x.Kind, x.Name, x.Status), text(s.head, x.Name)
				if x.OldName != "" {
					what, before = fmt.Sprintf("%s %s renamed from %s", x.Kind, x.Name, x.OldName), text(s.head, x.OldName)
					if in.Renames == nil {
						in.Renames = map[string]string{}
					}
					in.Renames[x.OldName] = x.Name
				}
				in.Changes = append(in.Changes, inferprompt.CommitChange{What: what, Before: before, After: text(s.cur, x.Name)})
			}
			continue
		}
		cc := inferprompt.CommitChange{What: "prose in " + c.File}
		var old, cur string
		if s.head != nil {
			if f := s.head.File(c.File); f != nil {
				old = string(f.Source())
			}
		}
		cf := s.cur.File(c.File)
		if cf != nil {
			cur = string(cf.Source())
		}
		for _, e := range contentEdits(old, cur) {
			if cf != nil {
				if an := cf.AnchorAt(e.start); an != nil && e.start < anchorSpan(an)[1] {
					continue // the anchor's own change
				}
			}
			cc.Words = append(cc.Words, describe([]contentEdit{e}))
		}
		cc.Reformat = len(cc.Words) == 0
		in.Changes = append(in.Changes, cc)
	}
	in.Accepted = append(in.Accepted, a.side.state.Accepted...)
	return in
}

// openCommit opens the commit view on the files as saved.
func (a *App) openCommit() tea.Cmd {
	proj := a.commitProject()
	b, e := a.wf.LoadBaseline(proj, baseline.Source{}, true)
	if e != nil {
		a.notice("Cannot read HEAD", e)
		return nil
	}
	if b.State == baseline.StateUnavailable {
		a.setFlash("Cannot commit: %s", b.Reason)
		return nil
	}
	loaded, err := proj.Load(nil)
	if err != nil {
		a.setFlash("Cannot read the spec files: %v", err)
		return nil
	}
	others, err := workflow.OtherChanges(proj, b)
	if err != nil {
		a.setFlash("Cannot read the project's files: %v", err)
		return nil
	}
	cs := append(workflow.Changes(b.Spec, loaded.Spec), others...)
	if len(cs) == 0 {
		a.setFlash("Nothing differs from HEAD: nothing to commit")
		return nil
	}
	s := &commitScreen{headCommit: b.Commit, snapshot: workflow.CommitSnapshot(loaded.Spec, others), head: b.Spec, cur: loaded.Spec, changes: cs, ticked: map[string]bool{}, state: map[string]string{}}
	for _, c := range cs {
		s.ticked[c.ID] = true
		s.state[c.ID] = a.reviewState(c, loaded.Spec)
	}
	s.retrail(a)
	a.popup, a.list, a.comp = nil, nil, nil
	a.push(s)
	return s.startDraft(a)
}

// commitProject is the project as a commit reads it: opened afresh, since
// spec.toml may have changed since the workspace read it (CommitChanges
// opens it so too).
func (a *App) commitProject() *project.Project {
	p, err := project.Open(a.ws.Root)
	if err != nil {
		lg.WithError(err).Warn("commit view: reopening the project")
		return a.ws.Project
	}
	return p
}

// reviewState says how far change c was reviewed: its anchors' open cards,
// or whether a check saw their current text.
func (a *App) reviewState(c workflow.Change, cur *mdspec.Spec) string {
	if c.Whole() {
		return c.Kind
	}
	if c.Prose() {
		return "prose"
	}
	open := 0
	for _, cd := range a.side.cards {
		if !a.openCard(cd) {
			continue
		}
		if slices.ContainsFunc(c.Anchors, func(x workflow.ChangedAnchor) bool { return x.Name == cd.anchor() }) {
			open++
		}
	}
	if open > 0 {
		return plural(open, "open finding")
	}
	all, err := a.checkEvidence()
	if err != nil {
		return "not checked"
	}
	for _, x := range c.Anchors {
		if x.Status == workflow.StatusEdited || x.Status == string(baseline.Removed) {
			continue
		}
		as := cur.Lookup(strings.TrimPrefix(x.Name, "$"))
		if len(as) != 1 {
			return "not checked"
		}
		text := cur.File(as[0].File).Text(as[0].BodyRange)
		checked := slices.ContainsFunc(all, func(p *proposal.Proposal) bool {
			superseded := p.Decision != nil && strings.HasPrefix(p.Decision.Reason, "superseded")
			return !p.ChecksBlocking() && p.Primary != nil && p.Primary.Name == x.Name && p.Primary.NewText == text && !superseded
		})
		if !checked && !a.clearedText(x.Name, text) {
			return "not checked"
		}
	}
	return "checked"
}

// checkEvidence is every proposal whose check can vouch for an anchor's
// current text: the proposal list, and the applied receipts a waiter has
// acknowledged (they leave the list, but their text is still uncommitted).
func (a *App) checkEvidence() ([]*proposal.Proposal, error) {
	return a.wf.Store(a.ws.Root).Evidence()
}

// selectedChanges returns the ticked changes in order.
func (s *commitScreen) selectedChanges() []workflow.Change {
	var out []workflow.Change
	for _, c := range s.changes {
		if s.ticked[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

// retrail recomputes the trailers of the ticked changes.
func (s *commitScreen) retrail(a *App) {
	t, err := workflow.CommitTrailers(a.ws.Project, s.head, s.cur, s.selectedChanges())
	if err != nil {
		lg.WithError(err).Warn("commit view: trailers")
		t = nil
	}
	s.trailers = t
}

// verbs words a change's anchor status for the subject line.
var verbs = map[string]string{
	string(baseline.Added): "add", string(baseline.Removed): "remove", string(baseline.Changed): "change",
	string(baseline.KindChanged): "change the kind of", string(baseline.Renamed): "rename",
	string(baseline.Retitled): "retitle", workflow.StatusEdited: "reflow",
}

// message returns the commit message: the one Chris wrote with c, else
// the model's draft, else one made from the ticked changes and the
// suggestions accepted since the last commit.
func (s *commitScreen) message(a *App) string {
	if m := strings.TrimSpace(a.side.state.Message); m != "" {
		return m
	}
	if s.draft != "" {
		return s.draft
	}
	return s.template(a)
}

// template is the message made without a model: the ticked changes'
// verbs and names, then the accepted suggestions' reasons.
func (s *commitScreen) template(a *App) string {
	var order []string
	names := map[string][]string{}
	prose := false
	var files []string
	for _, c := range s.selectedChanges() {
		if c.Whole() {
			files = append(files, c.File)
			continue
		}
		if c.Prose() {
			prose = true
			continue
		}
		for _, x := range c.Anchors {
			v := verbs[x.Status]
			if v == "" {
				v = "change"
			}
			if names[v] == nil {
				order = append(order, v)
			}
			names[v] = append(names[v], x.Name)
		}
	}
	var parts []string
	for _, v := range order {
		parts = append(parts, v+" "+strings.Join(names[v], ", "))
	}
	if prose {
		parts = append(parts, "edit prose")
	}
	if len(files) > 0 {
		parts = append(parts, "update "+strings.Join(files, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	msg := "spec: " + strings.Join(parts, "; ")
	if acc := a.side.state.Accepted; len(acc) > 0 {
		msg += "\n"
		for _, x := range acc {
			msg += "\n- " + x
		}
	}
	return msg
}

func (s *commitScreen) context(*App) Context { return CtxCommit }

func (s *commitScreen) key(a *App, _ tea.KeyPressMsg, b Binding, ok bool) tea.Cmd {
	if !ok {
		return nil
	}
	if a.job != nil && a.job.irreversible {
		a.setFlash("Committing; wait for the result")
		return nil
	}
	c := s.selected()
	switch b.Action {
	case ActRefreshSelection:
		return s.refresh(a)
	case ActCancel:
		if s.cancelDraft != nil {
			s.cancelDraft()
		}
		a.pop()
	case ActUp:
		s.sel = max(s.sel-1, 0)
	case ActDown:
		s.sel = min(s.sel+1, max(len(s.changes)-1, 0))
	case ActYes, ActNo, ActToggle:
		if c == nil {
			return nil
		}
		switch b.Action {
		case ActYes:
			if s.ticked[c.ID] {
				return nil
			}
			s.ticked[c.ID] = true
		case ActNo:
			if !s.ticked[c.ID] {
				return nil
			}
			s.ticked[c.ID] = false
		default:
			s.ticked[c.ID] = !s.ticked[c.ID]
		}
		s.retrail(a)
		return s.startDraft(a)
	case ActMessage:
		a.editMessageWith(s.message(a))
	case ActCommitView:
		return s.commit(a)
	}
	return nil
}

// selected returns the selected change, or nil.
func (s *commitScreen) selected() *workflow.Change {
	if s.sel < 0 || s.sel >= len(s.changes) {
		return nil
	}
	return &s.changes[s.sel]
}

// commitBlocked says why A cannot commit now ("" when it can).
func (s *commitScreen) commitBlocked(a *App) string {
	switch {
	case s.refreshNeeded:
		return "Saved files or HEAD changed; r refreshes the selection"
	case len(s.selectedChanges()) == 0:
		return "Nothing is ticked: y ticks the selected change"
	case s.drafting:
		return a.theme.uiText("Drafting the commit message…; c writes your own")
	case strings.TrimSpace(s.message(a)) == "":
		return "No commit message: c writes one"
	}
	return ""
}

// commit commits the ticked changes in the background (hooks may take a
// while).
func (s *commitScreen) commit(a *App) tea.Cmd {
	if why := s.commitBlocked(a); why != "" {
		a.setFlash("%s", why)
		return nil
	}
	if a.busy() {
		return nil
	}
	var ids []string
	for _, c := range s.selectedChanges() {
		ids = append(ids, c.ID)
	}
	left := len(s.changes) - len(ids)
	wf, proj, msg := a.wf, a.ws.Project, s.message(a)
	return a.startCommitJob(func(context.Context) func(*App) tea.Cmd {
		res, e := wf.CommitChangesSeen(proj, ids, msg, s.headCommit, s.snapshot)
		return func(a *App) tea.Cmd {
			if e != nil {
				if e.Code == "changed" {
					s.refreshNeeded = true
				}
				a.notice("Commit failed", e)
				return nil
			}
			a.remove(s)
			a.toEditor()
			a.side.state.Message = ""
			a.side.state.Accepted = nil
			a.saveChecks()
			text := fmt.Sprintf("Committed %s: %s", shortHash(res.Commit), plural(len(ids), "change"))
			if len(res.Trailers) > 0 {
				text += " with " + strings.Join(trailerKinds(res.Trailers), ", ")
			}
			if left > 0 {
				text += fmt.Sprintf("; %s left uncommitted", plural(left, "change"))
			}
			if res.Warning != "" {
				text += "; " + res.Warning
			}
			a.setStatus(statusSuccess, "Committed", "%s", text)
			return a.reloadBaseline()
		}
	})
}

// toEditor gives the editor the keyboard after a commit: the sidebar
// loses focus and stays only where it is a column (drawn over the text,
// it would hide what Chris goes back to).
func (a *App) toEditor() {
	a.sideFocus, a.outlineFocus = false, false
	if !a.sideFits() {
		a.sideShown = false
	}
	a.layout()
}

func (s *commitScreen) mouse(a *App, msg tea.MouseMsg) tea.Cmd {
	if a.job != nil && a.job.irreversible {
		return nil
	}
	if m, ok := msg.(tea.MouseClickMsg); ok && m.Button == tea.MouseLeft {
		x, width := taskReadingRegion(a.width)
		if m.X < x || m.X >= x+width || m.Y < 3 || m.Y >= a.bodyHeight() {
			return nil
		}
		line := m.Mouse().Y + s.scroll
		for k, st := range s.starts {
			if line == st {
				s.sel = k
				s.ticked[s.changes[k].ID] = !s.ticked[s.changes[k].ID]
				s.retrail(a)
				return s.startDraft(a)
			}
		}
	}
	return nil
}

func (s *commitScreen) view(a *App, w, h int) []string {
	x, width := taskReadingRegion(w)
	w = width
	th := a.theme
	var lines []string
	add := func(ls ...string) { lines = append(lines, ls...) }
	add(th.SideHead.Render(ansi.Truncate(" Commit saved changes", w, th.uiText("…"))))
	add(th.PopupDim.Render(ansi.Truncate(fmt.Sprintf(th.uiText(" %d of %d groups included · excluded changes stay uncommitted"), len(s.selectedChanges()), len(s.changes)), w, th.uiText("…"))))
	refreshStyle := th.HelpKeyDim
	if s.refreshNeeded {
		refreshStyle = th.HelpKey
	}
	add(refreshStyle.Render(" [r] Refresh selection"))
	if s.refreshNeeded {
		add(th.Stale.Render(ansi.Truncate(" Saved files or HEAD changed; refresh before committing", w, th.uiText("…"))))
	}
	add("")
	s.starts = make([]int, len(s.changes))
	for k, c := range s.changes {
		s.starts[k] = len(lines)
		box := "[ ]"
		if s.ticked[c.ID] {
			box = th.Accepted.Render("[x]")
		}
		var what string
		switch {
		case c.Whole():
			what = c.Label()
		case c.Prose():
			what = "prose"
		default:
			var parts []string
			for _, x := range c.Anchors {
				parts = append(parts, fmt.Sprintf("%s %s %s", x.Kind, x.Name, x.Status))
			}
			what = strings.Join(parts, ", ")
		}
		state := s.state[c.ID]
		stStyle := th.PopupDim
		switch {
		case state == "checked":
			state = "Checked current text"
		case strings.Contains(state, "open"):
			stStyle = th.Stale
		}
		if state == "not checked" {
			state = "Not checked"
		}
		switch state {
		case "prose":
			state = "Prose"
		case workflow.KindModel:
			state = "Model, whole file"
		case workflow.KindSettings:
			state = "Settings, whole file"
		}
		where := fmt.Sprintf("%s:%d  +%d -%d", c.File, c.Line, c.Added, c.Removed)
		mark := "  "
		if k == s.sel {
			mark = th.uiText("▶ ")
		}
		label := termtext.Clean(what)
		room := w - 6 - ansi.StringWidth(state) - ansi.StringWidth(where) - 4
		line := mark + box + " " + padTo(ansi.Truncate(label, max(room, 8), th.uiText("…")), max(room, 8)) + "  " + stStyle.Render(state) + "  " + th.PopupDim.Render(where)
		if k == s.sel {
			line = mark + box + " " + th.Selected.Render(padTo(ansi.Truncate(label, max(room, 8), th.uiText("…")), max(room, 8))) + "  " + stStyle.Render(state) + "  " + th.PopupDim.Render(where)
		}
		add(line)
	}
	note := "c edits it"
	switch {
	case strings.TrimSpace(a.side.state.Message) != "":
		note = "yours; c edits it"
	case s.drafting:
		note = th.uiText("drafting…; c writes your own")
	case s.draftNote != "":
		note = s.draftNote + "; c edits it"
	}
	add("", th.PopupTitle.Render(" Message")+th.PopupDim.Render("  ("+note+")"))
	msg := s.message(a)
	if msg == "" {
		add(th.PopupDim.Render("   (none: c writes one)"))
	}
	for _, l := range strings.Split(msg, "\n") {
		// wrapText keeps a blank line as one empty row.
		for _, wl := range wrapText(termtext.Clean(l), max(w-4, 10)) {
			add("   " + wl)
		}
	}
	if len(s.trailers) > 0 {
		add("", th.PopupTitle.Render(" Trailers")+th.PopupDim.Render("  (added at commit)"))
		for _, t := range s.trailers {
			add("   " + th.PopupDim.Render(termtext.Clean(t)))
		}
	}
	if s.sel < len(s.starts) {
		if st := s.starts[s.sel]; st < s.scroll+3 {
			s.scroll = max(st-3, 0)
		} else if st >= s.scroll+h {
			s.scroll = st - h + 1
		}
	}
	s.scroll = min(max(s.scroll, 0), max(len(lines)-h, 0))
	out := make([]string, 0, h)
	for k := 0; k < min(3, min(h, len(lines))); k++ {
		out = append(out, strings.Repeat(" ", x)+ansi.Truncate(lines[k], w, ""))
	}
	for k := 3 + s.scroll; k < len(lines) && len(out) < h; k++ {
		out = append(out, strings.Repeat(" ", x)+ansi.Truncate(lines[k], w, ""))
	}
	for len(out) < h {
		out = append(out, "")
	}
	return out
}

// editMessage is c in the sidebar: write the commit message draft.
func (a *App) editMessage() {
	msg := a.side.state.Message
	if strings.TrimSpace(msg) == "" {
		msg = "spec: "
	}
	a.editMessageWith(msg)
}

// editMessageWith opens the small editor on the commit message draft,
// starting from msg.
func (a *App) editMessageWith(msg string) {
	a.push(newTextEditScreen(a, textEdit{
		title:     "Commit message: the first line is the subject; trailers are added at commit",
		keepLabel: "Keep message",
		status:    " Commit message",
		text:      msg,
		done: func(a *App, text string) bool {
			a.side.state.Message = strings.TrimSpace(text)
			a.saveChecks()
			a.pop()
			a.setFlash("Commit message kept; A in the commit view commits")
			return true
		},
		cancel: "Message unchanged",
	}))
}

// refresh keeps only unchanged groups selected, and never commits as a side effect.
func (s *commitScreen) refresh(a *App) tea.Cmd {
	if a.job != nil || a.wait != nil {
		a.setFlash("Wait for the current operation before refreshing")
		return nil
	}
	proj := a.commitProject()
	b, e := a.wf.LoadBaseline(proj, baseline.Source{}, true)
	if e != nil {
		a.notice("Cannot refresh HEAD", e)
		return nil
	}
	if b.State == baseline.StateUnavailable {
		a.setStatus(statusFailure, "Refresh failed", "Cannot read HEAD: %s", b.Reason)
		return nil
	}
	loaded, err := proj.Load(nil)
	if err != nil {
		a.setFlash("Cannot refresh files: %v", err)
		return nil
	}
	others, err := workflow.OtherChanges(proj, b)
	if err != nil {
		a.setFlash("Cannot refresh files: %v", err)
		return nil
	}
	old := s.changes
	next := append(workflow.Changes(b.Spec, loaded.Spec), others...)
	ticked := map[string]bool{}
	for _, n := range next {
		for _, o := range old {
			if o.ID == n.ID && workflow.SameChange(o, n) && b.Commit == s.headCommit {
				ticked[n.ID] = s.ticked[o.ID]
			}
		}
	}
	s.head, s.cur, s.headCommit, s.snapshot = b.Spec, loaded.Spec, b.Commit, workflow.CommitSnapshot(loaded.Spec, others)
	s.changes, s.ticked, s.refreshNeeded = next, ticked, false
	s.sel = min(s.sel, max(len(next)-1, 0))
	for _, c := range next {
		s.state[c.ID] = a.reviewState(c, loaded.Spec)
	}
	s.retrail(a)
	a.setFlash("Selection refreshed; new or changed groups remain excluded")
	return s.startDraft(a)
}
