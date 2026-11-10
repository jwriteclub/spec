// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/workflow"
)

// hunkState is the reviewer's decision on one hunk.
type hunkState int

const (
	hunkUndecided hunkState = iota
	hunkAccepted
	hunkSkipped
)

// reviewScreen shows one proposal hunk by hunk: a diff per hunk with the
// finding's issue, severity and explanation, then the notes and questions.
type reviewScreen struct {
	p     *proposal.Proposal
	state map[int]hunkState
	// stale holds why a hunk no longer applies; primaryStale why the
	// change itself does not (the whole proposal is then stale).
	stale        map[int]string
	primaryStale string
	// gone is set when the proposal was decided elsewhere meanwhile.
	gone   string
	nums   []int
	sel    int
	scroll int
	// starts and ends are each hunk's first and past-last line in the
	// last render.
	starts, ends map[int]int
	lines        []string
	// format is set by ^F: approving also formats the touched files (as
	// ^F formats the buffer), one undo step with the edits.
	format bool
}

// openReview opens the review screen on proposal id, which becomes the
// active proposal (the gutter shows it).
func (a *App) openReview(id string) {
	p, e := a.wf.LoadProposal(a.ws.Root, id)
	if e != nil {
		a.notice("Cannot open the proposal", e)
		return
	}
	if p.Status != proposal.StatusPending {
		a.setFlash("Proposal %s is already %s", id, p.Status)
		return
	}
	r := a.newReview(p)
	a.popup = nil
	a.list = nil
	a.comp = nil
	a.push(r)
	r.reveal(a)
}

// newReview builds the review of pending proposal p, on its first hunk
// that needs a decision, without showing it.
func (a *App) newReview(p *proposal.Proposal) *reviewScreen {
	r := &reviewScreen{p: p, state: map[int]hunkState{}}
	if p.Primary != nil {
		r.state[p.Primary.N] = hunkAccepted
	}
	if p.IsModel() {
		// A model edit applies as a whole.
		for _, h := range p.Hunks {
			r.state[h.N] = hunkAccepted
		}
	}
	r.recheck(a)
	for k, n := range r.nums {
		if r.state[n] == hunkUndecided {
			r.sel = k
			break
		}
	}
	return r
}

// recheck renumbers the hunks and finds the stale ones against the files
// on disk (what approving applies to).
func (r *reviewScreen) recheck(a *App) {
	r.nums = r.p.Numbers()
	r.sel = min(r.sel, max(len(r.nums)-1, 0))
	r.stale = map[int]string{}
	r.primaryStale = ""
	proj, err := project.Open(a.ws.Root)
	if err != nil {
		lg.WithError(err).Warn("review: cannot open the project")
		return
	}
	overlay := map[string][]byte{}
	for path, doc := range a.ws.docs {
		overlay[path] = doc.Ed.Buffer().Bytes()
	}
	loaded, err := proj.Load(overlay)
	if err != nil {
		lg.WithError(err).Warn("review: cannot load the project")
		return
	}
	if r.p.IsModel() {
		r.recheckModel(loaded)
		return
	}
	if pr := r.p.Primary; pr != nil {
		if _, err := r.p.Prepare(loaded.Spec, map[int]bool{pr.N: true}); err != nil {
			var st *proposal.StaleError
			if errors.As(err, &st) {
				r.primaryStale = st.Error()
			} else {
				r.primaryStale = err.Error()
			}
		}
	}
	for _, h := range r.p.Hunks {
		prep, err := r.p.Prepare(loaded.Spec, map[int]bool{h.N: true})
		if err != nil {
			continue
		}
		for _, f := range prep.Failed {
			r.stale[f.N] = f.Reason
		}
	}
	for n := range r.stale {
		if r.state[n] == hunkAccepted {
			r.state[n] = hunkUndecided
		}
	}
}

// recheckModel finds whether a model proposal still applies to the model as it
// is in the buffers: one stale hunk stales the whole edit.
func (r *reviewScreen) recheckModel(loaded *project.Loaded) {
	src := []byte{}
	if f := loaded.Formal.File(r.p.Model.File); f != nil {
		src = f.Source()
	}
	if _, err := r.p.PrepareModel(src, allNumbers(r.p)); err != nil {
		r.primaryStale = err.Error()
		var st *proposal.StaleError
		if errors.As(err, &st) {
			r.primaryStale = st.Error()
		}
	}
}

// allNumbers accepts every hunk of p.
func allNumbers(p *proposal.Proposal) map[int]bool {
	out := map[int]bool{}
	for _, n := range p.Numbers() {
		out[n] = true
	}
	return out
}

// onStoreChanged notices a proposal decided elsewhere while it is open.
func (r *reviewScreen) onStoreChanged(a *App) {
	cur, e := a.wf.LoadProposal(a.ws.Root, r.p.ID)
	switch {
	case e != nil:
		r.gone = "removed"
	case cur.Status != proposal.StatusPending:
		r.gone = cur.Status
	default:
		a.withBuffered(cur)
		r.take(a, cur)
		return
	}
	a.setFlash("Proposal %s was %s elsewhere; Esc closes the review", r.p.ID, r.gone)
}

// take shows cur, the proposal as it is on disk now, when its text differs
// from the one shown (a hunk revised elsewhere): the changed hunks need a
// new decision.
func (r *reviewScreen) take(a *App, cur *proposal.Proposal) {
	if r.p.CheckGeneration != cur.CheckGeneration {
		r.state = map[int]hunkState{}
		if cur.Primary != nil {
			r.state[cur.Primary.N] = hunkAccepted
		}
	}
	changed := workflow.Changed(r.p, cur, nil)
	if len(changed) == 0 {
		r.p = cur
		r.recheck(a)
		return
	}
	r.p = cur
	for _, n := range changed {
		if !r.isPrimary(n) && !cur.IsModel() {
			r.state[n] = hunkUndecided
		}
	}
	r.recheck(a)
	r.reveal(a)
	a.setFlash("Proposal %s changed elsewhere: hunk %s revised; review it again", cur.ID, workflow.JoinInts(changed))
}

// selected returns the number of the selected hunk (0 when none).
func (r *reviewScreen) selected() int {
	if r.sel < 0 || r.sel >= len(r.nums) {
		return 0
	}
	return r.nums[r.sel]
}

// hunk returns finding hunk n.
func (r *reviewScreen) hunk(n int) (finding.Hunk, bool) {
	for _, h := range r.p.Hunks {
		if h.N == n {
			return h, true
		}
	}
	return finding.Hunk{}, false
}

// isPrimary reports whether n is the change itself.
func (r *reviewScreen) isPrimary(n int) bool {
	return r.p.Primary != nil && r.p.Primary.N == n
}

func (r *reviewScreen) context(*App) Context { return CtxReview }

func (r *reviewScreen) key(a *App, _ tea.KeyPressMsg, b Binding, ok bool) tea.Cmd {
	if !ok {
		return nil
	}
	n := r.selected()
	page := max(a.bodyHeight()-reviewHeaderRows, 1)
	switch b.Action {
	case ActRetryCheck:
		return r.retryCheck(a)
	case ActCancel:
		a.pop()
	case ActUp:
		r.sel = max(r.sel-1, 0)
		r.reveal(a)
	case ActDown:
		r.sel = min(r.sel+1, max(len(r.nums)-1, 0))
		r.reveal(a)
	case ActPageUp:
		r.scroll = max(r.scroll-page, 0)
	case ActPageDown:
		r.scroll = min(r.scroll+page, max(len(r.lines)-a.bodyHeight(), 0))
	case ActAcceptHunk:
		r.accept(a, n)
	case ActSkipHunk:
		if n == 0 {
			a.setFlash("No hunk is selected")
			return nil
		}
		if r.isPrimary(n) {
			a.setFlash("Hunk %d is the change itself and cannot be skipped; R rejects the proposal", n)
			return nil
		}
		if r.p.IsModel() {
			a.setFlash("A model edit is checked and applied as a whole, so no hunk can be skipped; R rejects the proposal")
			return nil
		}
		if n > 0 {
			if r.state[n] == hunkSkipped || r.p.AppliedHunk(n) {
				return nil
			}
			r.state[n] = hunkSkipped
			r.advance(a)
		}
	case ActFeedback:
		return r.feedback(a, n)
	case ActEditHunk:
		r.edit(a, n)
	case ActApprove:
		return r.approve(a)

	case ActRejectAll:
		if r.gone != "" {
			a.setFlash("Proposal %s was %s elsewhere", r.p.ID, r.gone)
			return nil
		}
		if a.refuseWhileWaiting(r.p.ID) {
			return nil
		}
		a.prompt = &prompt{kind: promptReject, label: "Reason to reject " + r.p.ID + " (sent to its author)", review: r}
	}
	return nil
}

// accept marks hunk n accepted, unless it is stale.
func (r *reviewScreen) accept(a *App, n int) {
	if n == 0 {
		a.setFlash("No hunk is selected")
		return
	}
	if r.isPrimary(n) {
		a.setFlash("Hunk %d is the required primary change", n)
		return
	}
	if why, stale := r.stale[n]; stale {
		a.setFlash("Hunk %d is stale and cannot be accepted (%s); f asks the model to redo it, n skips it", n, why)
		return
	}
	if r.state[n] == hunkAccepted || r.p.AppliedHunk(n) {
		return
	}
	r.state[n] = hunkAccepted
	r.advance(a)
}

// advance selects the next undecided hunk after the selection, if any.
func (r *reviewScreen) advance(a *App) {
	for k := r.sel + 1; k < len(r.nums); k++ {
		if r.state[r.nums[k]] == hunkUndecided {
			r.sel = k
			r.reveal(a)
			return
		}
	}
}

// reveal scrolls so the selected hunk is on screen.
func (r *reviewScreen) reveal(a *App) {
	r.render(a, a.width)
	h := a.bodyHeight()
	n := r.selected()
	s, ok := r.starts[n]
	if !ok {
		return
	}
	e := r.ends[n]
	switch {
	case s < r.scroll+reviewHeaderRows:
		r.scroll = max(s-reviewHeaderRows, 0)
	case e > r.scroll+h:
		r.scroll = max(min(s-reviewHeaderRows, e-h), 0)
	}
}

// accepted returns the hunk numbers to apply.
func (r *reviewScreen) accepted() []int {
	var out []int
	for _, n := range r.nums {
		if r.state[n] == hunkAccepted && !r.p.AppliedHunk(n) {
			out = append(out, n)
		}
	}
	return out
}

// approveBlocked says why approving cannot run now ("" when it can): the
// proposal was decided elsewhere, the change itself is stale, nothing is
// accepted, or a file it touches has unsaved changes in the editor.
func (r *reviewScreen) approveBlocked(a *App) string {
	if e := a.wf.CheckRecovery(a.ws.Root, r.p.ID); e != nil {
		return e.Message
	}
	if a.job != nil || a.wait != nil {
		return "Another operation is still running"
	}
	if r.p.StalePrimary != nil {
		return r.p.StalePrimary.Reason
	}
	if a.ws.Spec != nil {
		if e := workflow.CheckSnapshot(r.p, a.ws.Spec); e != nil {
			return e.Message
		}
	}
	if r.p.ChecksBlocking() {
		return "Required check " + r.p.CheckState + ": " + r.p.CheckMessage
	}
	if r.gone != "" {
		return fmt.Sprintf("Proposal %s was %s elsewhere", r.p.ID, r.gone)
	}
	if r.primaryStale != "" && r.p.IsModel() {
		return fmt.Sprintf("The model edit no longer applies (%s); R rejects the proposal", r.primaryStale)
	}
	if r.primaryStale != "" {
		return fmt.Sprintf("The change itself is stale (%s); R rejects the proposal", r.primaryStale)
	}
	accept := r.accepted()
	findings := 0
	for _, n := range accept {
		if !r.isPrimary(n) {
			findings++
		}
	}
	if (r.p.Primary == nil || r.p.AppliedHunk(r.p.Primary.N)) && findings == 0 {
		return "No hunk is accepted: y accepts the selected hunk; R rejects the proposal"
	}
	return ""
}

// approve applies the accepted hunks into the editor's buffers (nothing
// is saved or committed), with .spec/lock held (it may wait for another
// spec process: see lockwait.go). The save records it (unsaved.go).
func (r *reviewScreen) approve(a *App) tea.Cmd {
	if why := r.approveBlocked(a); why != "" {
		a.setFlash("%s", why)
		return nil
	}
	if a.refuseWhileWaiting(r.p.ID) || !a.modelGate(r.p) {
		return nil
	}
	return a.underLock(r.p.ID, "apply "+r.p.ID, r.approveLocked)
}

// approveLocked is approve once the lock is held: the review as it is
// now (the wait may have taken seconds).
func (r *reviewScreen) approveLocked(a *App, lock *project.Lock) tea.Cmd {
	if why := r.approveBlocked(a); why != "" {
		a.setFlash("%s", why)
		return nil
	}
	accept := r.accepted()
	prep, edits, e := a.applyInto(r.p, accept, r.format)
	if e != nil {
		switch {
		case e.Code == "changed" && e.Proposal != nil:
			r.take(a, e.Proposal)
		case e.Code == "stale" || e.Code == "not_pending":
			if p, le := a.wf.LoadProposal(a.ws.Root, r.p.ID); le == nil {
				r.p = p
				r.recheck(a)
				if p.Status != proposal.StatusPending {
					r.gone = p.Status
				}
			}
		}
		return nil
	}
	d := proposal.Decision{Accepted: accept, Applied: prep.Applied, Failed: prep.Failed, By: proposal.AuthorHuman}
	a.holdApply(lock, r.p, &d, true, edits, nil)
	if len(prep.Failed) > 0 {
		r.recheck(a)
		for _, f := range prep.Failed {
			r.state[f.N] = hunkUndecided
		}
	} else {
		a.remove(r)
	}
	a.loadPending()
	verb := "Applied"
	if r.format {
		verb = "Applied and formatted"
	}
	msg := appliedHunksText(verb, prep.Applied, r.p.ID)
	failed := failedText(prep.Failed)
	cmd := a.edited()
	a.appliedFlash(proposalName(r.p), failed, msg+failed+" (^O saves; nothing is committed)")
	return cmd
}

// appliedHunksText says which hunks of proposal id an apply put into the
// buffer. The id is named as a proposal: a bare "hunk 1 of 56756" reads as
// a count of hunks, since an id may be all digits.
func appliedHunksText(verb string, applied []int, id string) string {
	noun := "hunk"
	if len(applied) > 1 {
		noun = "hunks"
	}
	return fmt.Sprintf("%s %s %s of proposal %s into the buffer", verb, noun, workflow.JoinInts(applied), id)
}

// reloadBaseline reloads the HEAD baseline (after a commit).
func (a *App) reloadBaseline() tea.Cmd {
	if !a.opts.Baseline {
		return nil
	}
	// The project as of now: a re-glob may replace it while git runs.
	proj := a.ws.Project
	a.baselineSeq++
	seq := a.baselineSeq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), baselineTimeout)
		defer cancel()
		return baselineMsg{base: loadBaseline(ctx, proj), seq: seq}
	}
}

// shortHash shortens a commit hash for the status line.
func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// trailerKinds names the trailers ("Spec-Change $Frame.Timing").
func trailerKinds(ts []string) []string {
	var out []string
	for _, t := range ts {
		key, rest, _ := strings.Cut(t, ":")
		f := strings.Fields(rest)
		if len(f) > 0 {
			out = append(out, key+" "+f[0])
		} else {
			out = append(out, key)
		}
	}
	return out
}

// feedback asks for feedback on hunk n, then has the model redo it.
func (r *reviewScreen) feedback(a *App, n int) tea.Cmd {
	if r.p.AppliedHunk(n) {
		a.setFlash("This hunk is already applied; edit the document instead")
		return nil
	}
	if r.isPrimary(n) || n == 0 {
		a.setFlash("Hunk %d is the author's text and cannot be revised here; R rejects it with a reason", n)
		return nil
	}
	if r.p.IsModel() {
		a.setFlash("A model edit is the agent's text: e edits a hunk, or R rejects it with a reason and the agent resends")
		return nil
	}
	if a.busy() {
		return nil
	}
	h, _ := r.hunk(n)
	a.push(newFeedbackScreen(a, fmt.Sprintf("Feedback on hunk %d: the model redoes it", n), h.Find, func(a *App, text string) tea.Cmd {
		return r.revise(a, n, text)
	}))
	return nil
}

// revise runs the revision of hunk n with feedback in the background.
func (r *reviewScreen) revise(a *App, n int, feedback string) tea.Cmd {
	h, ok := r.hunk(n)
	if !ok || a.busy() {
		return nil
	}
	wf, root, p := a.wf, a.ws.Root, r.p
	return a.startJob(fmt.Sprintf("Revising hunk %d", n), func(ctx context.Context) func(*App) tea.Cmd {
		ck, e := wf.OpenChecker(ctx, false)
		var rev *workflow.Revision
		if e == nil {
			rev, e = wf.ReviseHunk(ck, root, p, h, feedback)
		}
		return func(a *App) tea.Cmd {
			if e != nil {
				a.notice(fmt.Sprintf("Revising hunk %d failed", n), e)
				return nil
			}
			if rev.Withdrawn {
				a.setFlash("The model withdrew hunk %d; n skips it", n)
				return nil
			}
			r.p = rev.Proposal
			r.state[n] = hunkUndecided
			r.recheck(a)
			a.setFlash("Hunk %d revised; y accepts it", n)
			return nil
		}
	})
}

// edit opens the small editor on hunk n's replacement text.
func (r *reviewScreen) edit(a *App, n int) {
	if r.p.AppliedHunk(n) {
		a.setFlash("This hunk is already applied; edit the document instead")
		return
	}
	if r.isPrimary(n) || n == 0 {
		a.setFlash("Hunk %d is the author's text and cannot be edited here", n)
		return
	}
	h, ok := r.hunk(n)
	if !ok {
		return
	}
	if why, stale := r.stale[n]; stale {
		a.setFlash("Hunk %d is stale (%s); f asks the model to redo it", n, why)
		return
	}
	var s *hunkEditScreen
	s = newHunkEditScreen(a, r.p, h, func(a *App, text string) tea.Cmd {
		h.Replace = text
		return a.keepHunk(s, r.p, h, func(a *App, cur *proposal.Proposal) tea.Cmd {
			r.p = cur
			r.recheck(a)
			r.state[h.N] = hunkAccepted
			a.setFlash("Hunk %d edited and accepted", h.N)
			return nil
		})
	})
	a.push(s)
}

func (r *reviewScreen) mouse(a *App, msg tea.MouseMsg) tea.Cmd {
	switch m := msg.(type) {
	case tea.MouseWheelMsg:
		switch m.Button {
		case tea.MouseWheelUp:
			r.scroll = max(r.scroll-3, 0)
		case tea.MouseWheelDown:
			r.scroll = min(r.scroll+3, max(len(r.lines)-a.bodyHeight(), 0))
		}
	case tea.MouseClickMsg:
		x, width := taskReadingRegion(a.width)
		if m.Button != tea.MouseLeft || m.X < x || m.X >= x+width || m.Y < reviewHeaderRows || m.Y >= a.bodyHeight() {
			return nil
		}
		line := m.Mouse().Y + r.scroll
		for k, n := range r.nums {
			if line >= r.starts[n] && line < r.ends[n] {
				r.sel = k
			}
		}
	}
	return nil
}

// taskReadingRegion keeps full-screen review tasks readable without filling
// the inherited terminal background around their centered content.
func taskReadingRegion(width int) (int, int) {
	cap := 120
	if width >= 200 {
		cap = 160
	}
	w := min(max(width-2, 1), cap)
	return max((width-w)/2, 0), w
}

const reviewHeaderRows = 2

func (r *reviewScreen) selectionCounts() (selected, skipped, undecided int) {
	for _, n := range r.nums {
		if r.isPrimary(n) || r.p.AppliedHunk(n) {
			continue
		}
		switch r.state[n] {
		case hunkAccepted:
			selected++
		case hunkSkipped:
			skipped++
		default:
			undecided++
		}
	}
	return selected, skipped, undecided
}

func (r *reviewScreen) selectionSummary() string {
	if r.p.IsModel() {
		return fmt.Sprintf("Model edit · %s, applied as a whole", plural(len(r.p.Hunks), "hunk"))
	}
	selected, skipped, undecided := r.selectionCounts()
	word := "fixes"
	if selected == 1 {
		word = "fix"
	}
	summary := fmt.Sprintf("%d selected %s · %d undecided · %d skipped", selected, word, undecided, skipped)
	if r.p.Primary != nil && !r.p.AppliedHunk(r.p.Primary.N) {
		summary = "Required primary · " + summary
	}
	return summary
}

func (r *reviewScreen) view(a *App, w, h int) []string {
	x, _ := taskReadingRegion(w)
	r.render(a, w)
	r.scroll = min(max(r.scroll, 0), max(len(r.lines)-h, 0))
	out := make([]string, 0, h)
	for k := 0; k < min(reviewHeaderRows, min(h, len(r.lines))); k++ {
		out = append(out, strings.Repeat(" ", x)+r.lines[k])
	}
	for k := reviewHeaderRows + r.scroll; k < len(r.lines) && len(out) < h; k++ {
		out = append(out, strings.Repeat(" ", x)+r.lines[k])
	}
	for len(out) < h {
		out = append(out, "")
	}
	return out
}

// render lays out the whole review at width w.
func (r *reviewScreen) render(a *App, w int) {
	_, w = taskReadingRegion(w)
	th := a.theme
	var lines []string
	add := func(ls ...string) { lines = append(lines, ls...) }
	wrapped := func(prefix, text string, style func(...string) string) {
		ind := strings.Repeat(" ", ansi.StringWidth(prefix))
		for k, l := range wrapText(termtext.Clean(strings.Join(strings.Fields(text), " ")), max(w-ansi.StringWidth(prefix)-1, 10)) {
			p := ind
			if k == 0 {
				p = prefix
			}
			add(p + style(l))
		}
	}
	plainStyle := func(s ...string) string { return strings.Join(s, "") }
	p := r.p
	head := " Review proposal " + p.ID
	if p.SourceKind != "" {
		head += th.uiText(" · ") + p.SourceKind
	}
	if p.Author != "" {
		head += th.uiText(" · by ") + p.Author
	}
	status := th.uiText(r.selectionSummary())
	if reason := r.approveBlocked(a); reason != "" {
		// Keep both the selection and the blocker fixed while scrolling. Give
		// counts room before optional attribution; the ready header stays intact.
		selected, skipped, undecided := r.selectionCounts()
		counts := fmt.Sprintf("%d selected, %d undecided, %d skipped", selected, undecided, skipped)
		if p.Primary != nil && !p.AppliedHunk(p.Primary.N) {
			counts = "Primary required; " + counts
		}
		title := oneLine(termtext.Clean(" Review " + p.ID))
		attribution := ""
		if p.SourceKind != "" {
			attribution += th.uiText(" · ") + p.SourceKind
		}
		if p.Author != "" {
			attribution += th.uiText(" · by ") + p.Author
		}
		room := max(w-ansi.StringWidth(counts)-2, 1)
		optionalRoom := room - ansi.StringWidth(title)
		if optionalRoom > ansi.StringWidth(th.uiText("…")) {
			title += ansi.Truncate(oneLine(termtext.Clean(attribution)), optionalRoom, th.uiText("…"))
		}
		add(th.PopupTitle.Render(ansi.Truncate(title, room, th.uiText("…"))) + th.PopupDim.Render("  "+counts))
		status = "Apply unavailable: " + reason
	} else {
		add(th.PopupTitle.Render(ansi.Truncate(termtext.Clean(head), w, th.uiText("…"))))
	}
	add(th.PopupDim.Render(ansi.Truncate(" "+oneLine(termtext.Clean(status)), w, th.uiText("…"))))

	if p.CheckState != "" {
		wrapped(" ", "Required check: "+p.CheckState+th.uiText(" · ")+p.CheckMessage, th.PopupDim.Render)
	}
	if r.gone != "" {
		add(th.Stale.Render(fmt.Sprintf(" This proposal was %s elsewhere; Esc closes the review.", r.gone)))
	}
	if a.waitingOn(p.ID) {
		wrapped(" ", a.waitText()+" (your decision runs then)", th.Stale.Render)
	}
	if r.primaryStale != "" && p.IsModel() {
		wrapped(" ", "STALE: the model edit no longer applies ("+r.primaryStale+"); R rejects the proposal.", th.Stale.Render)
	} else if r.primaryStale != "" {
		wrapped(" ", "STALE: the change itself no longer applies ("+r.primaryStale+"); R rejects the proposal.", th.Stale.Render)
	}
	if fx := p.Fix; fx != nil {
		wrapped(" finding: ", fmt.Sprintf("%s at %s:%d (%s fix): %s", fx.Code, fx.File, fx.Line, fx.Class, fx.Message), th.PopupDim.Render)
	}
	if p.Why != "" {
		wrapped(" why: ", p.Why, plainStyle)
	}
	if p.Summary != "" {
		label := " check: "
		if p.Fix != nil {
			label = " fix: "
		}
		if p.IsModel() {
			label = " change: "
		}
		wrapped(label, p.Summary, th.PopupDim.Render)
	}
	if m := p.Model; p.IsModel() {
		wrapped(" model check: ", modelCheckBadge(m.Check), th.PopupDim.Render)
		for _, d := range m.Check.Diagnostics {
			style := th.PopupDim.Render
			if d.Severity == formal.SeverityError {
				style = th.Stale.Render
			}
			wrapped("   ", workflow.DiagnosticLine(d), style)
		}
		for _, l := range m.Links {
			wrapped(" link: ", fmt.Sprintf("L%d:%d %s: %s [%s]", l.Line, l.Col, l.Severity, l.Message, l.Code), th.Stale.Render)
		}
		if p.StalePrimary != nil && r.primaryStale == "" {
			wrapped(" ", "STALE: "+p.StalePrimary.Reason, th.Stale.Render)
		}
	}

	r.starts, r.ends = map[int]int{}, map[int]int{}
	sel := r.selected()
	if pr := p.Primary; pr != nil {
		add("")
		r.starts[pr.N] = len(lines)
		title := fmt.Sprintf("[%d] %s %s in %s", pr.N, pr.Action, pr.Name, pr.File)
		if pr.OldKind != "" && pr.NewKind != "" && pr.OldKind != pr.NewKind {
			title += fmt.Sprintf(" (%s -> %s)", pr.OldKind, pr.NewKind)
		}
		add(r.hunkTitle(a, pr.N, title, sel == pr.N, w))
		if pr.InWorktree {
			add(th.PopupDim.Render("     the change itself, already in the working tree"))
		} else {
			add(th.PopupDim.Render("     the change itself"))
		}
		add(diffLines(th, pr.OldText, pr.NewText, w, "   ")...)
		r.ends[pr.N] = len(lines)
	}
	for _, hk := range p.Hunks {
		add("")
		r.starts[hk.N] = len(lines)
		title := fmt.Sprintf("[%d] %s %s in %s", hk.N, hk.Severity, hk.Issue, hk.File)
		if p.IsModel() {
			title = fmt.Sprintf("[%d] edit in %s", hk.N, hk.File)
		}
		if hk.Anchor != "" {
			title += " " + hk.Anchor
		}
		add(r.hunkTitle(a, hk.N, title, sel == hk.N, w))
		if hk.Explanation != "" {
			wrapped("     ", hk.Explanation, plainStyle)
		}
		if why, stale := r.stale[hk.N]; stale {
			wrapped("     ", "stale: "+why, th.Stale.Render)
		}
		add(diffLines(th, hk.Find, hk.Replace, w, "   ")...)
		r.ends[hk.N] = len(lines)
	}
	if len(p.Hunks) == 0 && p.Primary != nil {
		add("", th.PopupDim.Render(" The check proposed no follow-on fix."))
	}
	if len(p.Notes) > 0 {
		add("", th.PopupTitle.Render(" Notes (shown, not applied)"))
		for _, n := range p.Notes {
			t := fmt.Sprintf("%s %s in %s", n.Severity, n.Issue, n.File)
			if n.Anchor != "" {
				t += " " + n.Anchor
			}
			t += " (" + n.Reason + ")"
			if n.Explanation != "" {
				t += ": " + n.Explanation
			}
			wrapped("   - ", t, plainStyle)
		}
	}
	if len(p.Questions) > 0 {
		add("", th.PopupTitle.Render(" Questions"))
		for _, q := range p.Questions {
			wrapped("   ? ", q, plainStyle)
		}
	}
	for k, l := range lines {
		if ansi.StringWidth(l) > w {
			lines[k] = ansi.Truncate(l, w, "")
		}
	}
	r.lines = lines
}

// hunkTitle renders a hunk's title line with its state.
func (r *reviewScreen) hunkTitle(a *App, n int, title string, selected bool, w int) string {
	th := a.theme
	mark := "  "
	if selected {
		mark = th.uiText("▶ ")
		if th.ASCII {
			mark = "> "
		}
	}
	var state string
	switch {
	case r.p.AppliedHunk(n):
		state = th.Accepted.Render("applied to buffer")
	case r.isPrimary(n) && r.primaryStale != "":
		state = th.Stale.Render("STALE")
	case r.stale[n] != "":
		state = th.Stale.Render("STALE")
	case r.isPrimary(n), r.p.IsModel():
		state = th.PopupDim.Render("Required with Apply")
	case r.state[n] == hunkAccepted:
		state = th.PopupDim.Render("Selected for application")
	case r.state[n] == hunkSkipped:
		state = th.Skipped.Render("Skipped")
	default:
		state = th.PopupDim.Render("Undecided")
	}
	room := w - ansi.StringWidth(mark) - ansi.StringWidth(state) - 2
	title = ansi.Truncate(termtext.Clean(title), max(room, 1), th.uiText("…"))
	style := th.HunkHead
	if selected {
		style = th.Selected
	}
	return mark + style.Render(title) + "  " + state
}

func (r *reviewScreen) retryBlocked(a *App) string {
	if e := a.wf.CheckRecovery(a.ws.Root, r.p.ID); e != nil {
		return e.Message
	}
	if r.gone != "" || r.p.Status != proposal.StatusPending {
		return "This proposal was already decided"
	}
	if r.p.CheckState == "queued" || r.p.CheckState == "running" {
		return "The required check is still running"
	}
	if r.p.CheckState == "" {
		return "No required check to recheck"
	}
	if r.p.Primary == nil || len(r.p.CheckFiles) == 0 {
		return "No stored check snapshot; ask the author to resubmit"
	}
	if r.p.Decision != nil && len(r.p.Decision.Applied) > 0 {
		return workflow.ResidualGuidance(r.p)
	}
	if a.job != nil || a.wait != nil {
		return "Another operation is still running"
	}
	return ""
}

func (r *reviewScreen) retryCheck(a *App) tea.Cmd {
	if why := r.retryBlocked(a); why != "" {
		a.setFlash("%s", why)
		return nil
	}
	wf, proj, id, full := a.wf, a.ws.Project, r.p.ID, false
	overlay := map[string][]byte{}
	for path, doc := range a.ws.docs {
		overlay[path] = append([]byte(nil), doc.Ed.Buffer().Bytes()...)
	}
	return a.startJob("Checking proposal", func(ctx context.Context) func(*App) tea.Cmd {
		p, e := wf.RecheckPublished(ctx, proj, id, full, nil, overlay)
		return func(a *App) tea.Cmd {
			if p != nil {
				r.take(a, p)
			}
			a.loadPending()
			if e != nil {
				a.setStatus(statusFailure, "Check failed", "Check failed: %s", e.Message)
			} else {
				a.setFlash("Check finished; review the proposal")
			}
			return nil
		}
	})
}
