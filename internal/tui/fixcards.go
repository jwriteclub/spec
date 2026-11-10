// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// Problems and fixes (plan-step4g-fix-review.md, "The group card"). A
// check's findings are problems and its verified find/replace pairs are
// fixes; fixed_by links them many to many. Each problem is a box naming
// the sides it is between; the fixes that resolve it are teal boxes under
// it, each with its own state. A finding that carries its own
// find/replace (most of them today) is split in two: the problem, and the
// fix under it. A finding with a find/replace that another finding names
// in fixed_by (and that names none itself) is only a place to edit: it is
// a fix of that problem, not a problem of its own. A connected set of
// problems and fixes is one cluster; a cluster leaves the sidebar once
// every problem and fix in it is settled.

// More decision values (sidebar.go has accepted and dismissed; a skipped
// fix is dismissed).
const (
	// decEdited: the fix was edited (e), then applied.
	decEdited = "edited"
	// decHand: Chris made the fix's change by hand; the reparse after it
	// recorded it, or y did (when in doubt).
	decHand = "by hand"
)

// applied reports whether decision dec put the fix into the buffer.
func applied(dec string) bool { return dec == decAccepted || dec == decEdited }

// hunkKey is the decision key of hunk n.
func hunkKey(n int) string { return fmt.Sprintf("h%d", n) }

// problem is one finding of a check, shown as its own box.
type problem struct {
	// key is its decision key: "n0" for note 0, "p2" for the finding hunk
	// 2 carries.
	key                                        string
	file, anchor, issue, severity, explanation string
	// find is the text a note quotes (where it points; "" for none) and
	// reason why the note has no edit of its own.
	find, reason string
	// own is the hunk that carries this finding (0 for a note).
	own int
	// fixes are the numbers of the hunks that resolve it, own first.
	fixes []int
}

// cluster is a connected set of problems and the fixes that resolve them.
type cluster struct {
	problems []*problem
	fixes    []int
}

// head is the line above a cluster of more than one problem or fix ("1
// fix resolves 2 problems", "2 fixes for this problem"); "" otherwise.
func (cl *cluster) head() string {
	np, nf := len(cl.problems), len(cl.fixes)
	switch {
	case np <= 1 && nf <= 1:
		return ""
	case np == 1:
		return fmt.Sprintf("%s for this problem", fixes(nf))
	case nf == 1:
		return "1 fix resolves " + plural(np, "problem")
	}
	return fmt.Sprintf("%s resolve %s", fixes(nf), plural(np, "problem"))
}

// fixes is "1 fix" or "2 fixes".
func fixes(n int) string {
	if n == 1 {
		return "1 fix"
	}
	return fmt.Sprintf("%d fixes", n)
}

// fixList is p's suggested fixes: every hunk but a change's own, in
// order, each number once.
func fixList(p *proposal.Proposal) []finding.Hunk {
	var out []finding.Hunk
	seen := map[int]bool{}
	for _, h := range p.Hunks {
		if (p.Primary != nil && p.Primary.N == h.N) || seen[h.N] {
			continue
		}
		seen[h.N] = true
		out = append(out, h)
	}
	return out
}

// hasItems reports whether check p found anything: a fix, a finding or a
// question.
func hasItems(p *proposal.Proposal) bool {
	return len(fixList(p))+len(p.Notes)+len(p.Questions) > 0
}

// hunkOf returns hunk n of p.
func hunkOf(p *proposal.Proposal, n int) (finding.Hunk, bool) {
	for _, h := range p.Hunks {
		if h.N == n {
			return h, true
		}
	}
	return finding.Hunk{}, false
}

// clustersOf splits p's findings into problems and groups them with their
// fixes. Links to a hunk p does not have, or to the finding itself, are
// dropped (the finding then stands alone). A hunk another finding links
// to, which links to none itself, is a fix only. Clusters with fixes come
// first, in hunk order, then the problems no fix resolves; within a
// cluster the notes come before the findings that carry a fix.
func clustersOf(p *proposal.Proposal) []*cluster {
	hs := fixList(p)
	have := map[int]bool{}
	for _, h := range hs {
		have[h.N] = true
	}
	valid := func(self int, ns []int) []int {
		var out []int
		for _, n := range ns {
			if n != self && have[n] && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
		return out
	}
	named := map[int]bool{}
	for _, n := range p.Notes {
		for _, x := range valid(0, n.FixedBy) {
			named[x] = true
		}
	}
	for _, h := range hs {
		for _, x := range valid(h.N, h.FixedBy) {
			named[x] = true
		}
	}
	var probs []*problem
	for k, n := range p.Notes {
		probs = append(probs, &problem{key: fmt.Sprintf("n%d", k), file: n.File, anchor: n.Anchor, issue: n.Issue,
			severity: n.Severity, explanation: n.Explanation, find: n.Find, reason: n.Reason, fixes: valid(0, n.FixedBy)})
	}
	for _, h := range hs {
		if named[h.N] && len(valid(h.N, h.FixedBy)) == 0 {
			continue // a place to edit for another finding
		}
		probs = append(probs, &problem{key: fmt.Sprintf("p%d", h.N), file: h.File, anchor: h.Anchor, issue: h.Issue,
			severity: h.Severity, explanation: h.Explanation, own: h.N, fixes: append([]int{h.N}, valid(h.N, h.FixedBy)...)})
	}
	// Union the fixes each problem names.
	parent := map[int]int{}
	root := func(n int) int {
		for parent[n] != n {
			parent[n] = parent[parent[n]]
			n = parent[n]
		}
		return n
	}
	for _, h := range hs {
		parent[h.N] = h.N
	}
	for _, pr := range probs {
		for _, n := range pr.fixes[min(1, len(pr.fixes)):] {
			if a, b := root(pr.fixes[0]), root(n); a != b {
				parent[b] = a
			}
		}
	}
	byRoot := map[int]*cluster{}
	var out []*cluster
	for _, h := range hs {
		r := root(h.N)
		cl := byRoot[r]
		if cl == nil {
			cl = &cluster{}
			byRoot[r] = cl
			out = append(out, cl)
		}
		cl.fixes = append(cl.fixes, h.N)
	}
	var alone []*cluster
	for _, pr := range probs {
		if len(pr.fixes) == 0 {
			alone = append(alone, &cluster{problems: []*problem{pr}})
			continue
		}
		cl := byRoot[root(pr.fixes[0])]
		cl.problems = append(cl.problems, pr)
	}
	return append(out, alone...)
}

// fixState is where one fix stands.
type fixState int

const (
	// Not decided yet.
	fixPending fixState = iota
	// fixHand: the buffer already holds the replacement, at the fix's
	// place and nowhere else (Chris made the change by hand); the reparse
	// after the edit records it (recordDoneByHand).
	fixHand
	// fixHandUnsure: the buffer holds the replacement, but not only at the
	// fix's place (handDoubt); y records it.
	fixHandUnsure
	// fixGone: the text it replaces is gone and the replacement is not
	// there either; n skips it, ^P rechecks.
	fixGone
	// fixStale: the text it replaces occurs more than once now.
	fixStale
	// Decided.
	fixAccepted
	fixEdited
	fixSkipped
	fixByHand
)

// fixState says where fix h of p stands.
func (a *App) fixState(p *proposal.Proposal, h finding.Hunk) fixState {
	if p.AppliedHunk(h.N) {
		return fixAccepted
	}
	switch a.decided(p.ID, hunkKey(h.N)) {
	case decAccepted:
		return fixAccepted
	case decEdited:
		return fixEdited
	case decDismissed:
		return fixSkipped
	case decHand:
		return fixByHand
	}
	src := string(a.ws.Source(h.File))
	if src == "" && a.ws.File(h.File) == nil {
		return fixGone
	}
	n := strings.Count(src, h.Find)
	if h.Replace != "" && strings.Contains(src, h.Replace) && (n == 0 || strings.Contains(h.Replace, h.Find)) {
		if a.handDoubt(h, src) != "" {
			return fixHandUnsure
		}
		return fixHand
	}
	switch n {
	case 1:
		return fixPending
	case 0:
		return fixGone
	}
	return fixStale
}

// handDoubt says why fix h, whose replacement src (h.File's text) holds,
// may not have been made by hand at the fix's place ("" when it clearly
// was): the replacement occurs more than once, the text it replaces still
// occurs outside it, or the fix names an anchor of its file and the
// replacement is not in it.
func (a *App) handDoubt(h finding.Hunk, src string) string {
	if n := strings.Count(src, h.Replace); n > 1 {
		return fmt.Sprintf("its text occurs %d times in %s", n, h.File)
	}
	if h.Find != "" && strings.Count(src, h.Find) > strings.Count(h.Replace, h.Find) {
		return "the text it replaces is still in " + h.File
	}
	if h.Anchor == "" || a.ws.Spec == nil {
		return ""
	}
	ans := a.ws.Spec.Lookup(strings.TrimPrefix(h.Anchor, "$"))
	if len(ans) != 1 || ans[0].File != h.File {
		return ""
	}
	at, r := strings.Index(src, h.Replace), ans[0].BodyRange
	if at < r.Start || at >= r.End {
		return "it is not in " + h.Anchor
	}
	return ""
}

// probState is where one problem stands.
type probState int

const (
	// probOpen: some fix is still to decide.
	probOpen probState = iota
	// probNoFix: no fix was proposed; n dismisses it.
	probNoFix
	// probSkipped: every fix was skipped; it stays open until n
	// dismisses it or a recheck supersedes the check.
	probSkipped
	// probResolved: every fix is decided and one was applied.
	probResolved
	// probHand: every fix is decided or done by hand, none applied here.
	probHand
	// probDismissed: n dismissed it.
	probDismissed
)

// problemState says where problem pr of p stands and how many of its
// fixes are still to decide.
func (a *App) problemState(p *proposal.Proposal, pr *problem) (probState, int) {
	if a.decided(p.ID, pr.key) == decDismissed {
		return probDismissed, 0
	}
	if len(pr.fixes) == 0 {
		return probNoFix, 0
	}
	open, skipped, done := 0, 0, 0
	for _, n := range pr.fixes {
		h, _ := hunkOf(p, n)
		switch a.fixState(p, h) {
		case fixPending, fixGone, fixStale, fixHandUnsure:
			// In doubt, a fix done by hand is not recorded until y.
			open++
		case fixSkipped:
			skipped++
		case fixAccepted, fixEdited:
			done++
		}
	}
	switch {
	case open > 0:
		return probOpen, open
	case skipped == len(pr.fixes):
		return probSkipped, 0
	case done > 0:
		return probResolved, 0
	}
	return probHand, 0
}

// problemSettled reports whether pr needs nothing more from Chris:
// dismissed, or every fix decided and not all of them skipped.
func (a *App) problemSettled(p *proposal.Proposal, pr *problem) bool {
	if a.decided(p.ID, pr.key) == decDismissed {
		return true
	}
	if len(pr.fixes) == 0 {
		return false
	}
	skipped := 0
	for _, n := range pr.fixes {
		if p.AppliedHunk(n) {
			continue
		}
		switch a.decided(p.ID, hunkKey(n)) {
		case "":
			return false
		case decDismissed:
			skipped++
		}
	}
	return skipped < len(pr.fixes)
}

// clusterSettled reports whether every problem and fix of cl is settled.
func (a *App) clusterSettled(p *proposal.Proposal, cl *cluster) bool {
	for _, n := range cl.fixes {
		if !p.AppliedHunk(n) && a.decided(p.ID, hunkKey(n)) == "" {
			return false
		}
	}
	for _, pr := range cl.problems {
		if !a.problemSettled(p, pr) {
			return false
		}
	}
	return true
}

// proposalSettled reports whether every cluster and question of check p
// is settled: its decision can be recorded.
func (a *App) proposalSettled(p *proposal.Proposal) bool {
	// Suggested fixes can be reviewed independently, but settling them must
	// not silently accept an agent's parent proposal. The parent card remains
	// actionable until its own primary edit has been applied.
	if p.Primary != nil && !p.AppliedHunk(p.Primary.N) && (p.SourceKind == "agent" || !p.Primary.InWorktree) {
		return false
	}
	for _, cl := range clustersOf(p) {
		if !a.clusterSettled(p, cl) {
			return false
		}
	}
	for k := range p.Questions {
		if a.decided(p.ID, fmt.Sprintf("q%d", k)) == "" {
			return false
		}
	}
	return true
}

// pendingFixes are the fixes of cards cs (a problem's, or a whole
// check's) that y can apply now, in hunk order, each once.
func (a *App) pendingFixes(p *proposal.Proposal, ns []int) []finding.Hunk {
	var out []finding.Hunk
	seen := map[int]bool{}
	for _, h := range fixList(p) {
		if !slices.Contains(ns, h.N) || seen[h.N] {
			continue
		}
		seen[h.N] = true
		if !p.AppliedHunk(h.N) && a.fixState(p, h) == fixPending {
			out = append(out, h)
		}
	}
	return out
}

// fixesOf are the fix numbers card c covers: a problem's fixes, a fix
// itself, every fix of a check for its group card.
func fixesOf(c *card) []int {
	switch c.kind {
	case cardProblem:
		return c.prob.fixes
	case cardEdit:
		return []int{c.hunk.N}
	case cardGroup:
		var ns []int
		for _, h := range fixList(c.p) {
			ns = append(ns, h.N)
		}
		return ns
	}
	return nil
}

// fixCard is the fix card e, f and ^F act on for c: c itself, or the one
// undecided fix of a problem with a single fix (nil when there is none).
func (s *sidebar) fixCard(c *card) *card {
	if c == nil {
		return nil
	}
	if c.kind == cardEdit {
		return c
	}
	if c.kind != cardProblem || len(c.prob.fixes) != 1 {
		return nil
	}
	for _, x := range s.cards {
		if x.kind == cardEdit && x.p.ID == c.p.ID && x.hunk.N == c.prob.fixes[0] {
			return x
		}
	}
	return nil
}

// actionable reports whether card c still asks for a decision.
func (a *App) actionable(c *card) bool {
	switch c.kind {
	case cardEdit:
		return a.decided(c.p.ID, c.key) == "" && !c.p.AppliedHunk(c.hunk.N)
	case cardProblem:
		return !a.problemSettled(c.p, c.prob)
	case cardGroup:
		return len(a.pendingFixes(c.p, fixesOf(c))) > 0
	}
	return true
}

// openCard reports whether card c counts as open in the counts (header,
// badge, outline, commit view): an unsettled problem, a question, an
// agent's proposal.
func (a *App) openCard(c *card) bool {
	switch c.kind {
	case cardProblem:
		return !a.problemSettled(c.p, c.prob)
	case cardQuestion, cardProposal:
		return true
	}
	return false
}

// advanceFrom moves the selection on after a decision on card id: when
// that card no longer asks for anything, to the next card that does
// (else it stays).
func (a *App) advanceFrom(id string) {
	s := a.side
	at := slices.IndexFunc(s.cards, func(c *card) bool { return c.id() == id })
	if at < 0 || a.actionable(s.cards[at]) {
		return
	}
	for k := at + 1; k < len(s.cards); k++ {
		if a.actionable(s.cards[k]) {
			s.sel = k
			return
		}
	}
}

// acceptFixes applies fixes hs of p into the buffers as one step per file
// and records each applied one as how (accepted or edited).
func (a *App) acceptFixes(p *proposal.Proposal, hs []finding.Hunk, how string, format bool, from string) tea.Cmd {
	return a.underLock(p.ID, "apply "+p.ID, func(a *App, lock *project.Lock) tea.Cmd {
		return a.acceptFixesLocked(lock, p, hs, how, format, from)
	})
}

func (a *App) acceptFixesLocked(lock *project.Lock, p *proposal.Proposal, hs []finding.Hunk, how string, format bool, from string) tea.Cmd {
	var ns []int
	for _, h := range hs {
		if !p.AppliedHunk(h.N) {
			ns = append(ns, h.N)
		}
	}
	if len(ns) == 0 {
		return nil
	}
	prep, edits, e := a.applyInto(p, ns, format)
	if e != nil {
		return nil
	}
	if len(prep.Applied) == 0 {
		reason := "it does not apply"
		if len(prep.Failed) > 0 {
			reason = prep.Failed[0].Reason
		}
		a.setFlash("Not applied: %s; f asks the model to redo it, n skips it", reason)
		return nil
	}
	files := map[string]bool{}
	var keys []string
	for _, n := range prep.Applied {
		a.decide(p, hunkKey(n), how)
		keys = append(keys, hunkKey(n))
		h, _ := hunkOf(p, n)
		files[h.File] = true
		if x := strings.TrimSpace(h.Explanation); x != "" {
			a.side.state.Accepted = append(a.side.state.Accepted, oneLine(x))
		}
	}
	first, _ := hunkOf(p, prep.Applied[0])
	msg := fmt.Sprintf("Applied to %s (^Z undoes, ^O saves; nothing is committed)", first.File)
	switch {
	case len(prep.Applied) > 1:
		msg = fmt.Sprintf("Applied %s to %s (^Z undoes, ^O saves; nothing is committed)", fixes(len(prep.Applied)), plural(len(files), "file"))
	case format:
		msg = fmt.Sprintf("Applied and formatted %s (one ^Z undoes both, ^O saves; nothing is committed)", first.File)
	}
	name := first.Anchor
	if name == "" && p.Primary != nil {
		name = p.Primary.Name
	}
	if name == "" {
		name = p.ID
	}
	a.appliedFlash(name, failedText(prep.Failed), msg+failedText(prep.Failed))
	var d *proposal.Decision
	if p.SourceKind == "agent" && p.Primary != nil && (p.Primary.InWorktree || p.AppliedHunk(p.Primary.N)) && !a.proposalSettled(p) {
		d = &proposal.Decision{Accepted: prep.Applied, Applied: prep.Applied, Failed: prep.Failed, By: proposal.AuthorHuman}
	}
	a.holdApply(lock, p, d, false, edits, keys)
	settle := a.settleIfDone(p, lock)
	a.saveChecks()
	a.loadPending()
	a.advanceFrom(from)
	return tea.Batch(settle, a.edited())
}

// recordDoneByHand records, on the reparse after an edit, every undecided
// fix whose change the buffer now clearly holds (fixHand) as done by
// hand, as y on it would: nothing is applied, and its check is recorded
// as done once every item of it is settled. A fix in doubt
// (fixHandUnsure) and a fix of an outdated check wait for y. The record
// stands if the text changes again before the commit; ^P rechecks.
func (a *App) recordDoneByHand() tea.Cmd {
	var names []string
	var last *proposal.Proposal
	for _, p := range a.pending {
		if !suggestions(p) || p.ChecksBlocking() || a.waitingOn(p.ID) || a.checkOutdated(p) != "" {
			continue
		}
		var hs []finding.Hunk
		for _, h := range fixList(p) {
			if a.decided(p.ID, hunkKey(h.N)) == "" && a.fixState(p, h) == fixHand {
				hs = append(hs, h)
			}
		}
		for _, h := range hs {
			a.decide(p, hunkKey(h.N), decHand)
			a.holdHand(p, h.File, hunkKey(h.N))
			names = append(names, fixName(p, h))
		}
		if len(hs) > 0 {
			last = p
		}
	}
	if last == nil {
		return nil
	}
	if len(names) == 1 {
		a.setFlash("Done by hand: %s fix recorded", names[0])
	} else {
		a.setFlash("Done by hand: %s recorded", fixes(len(names)))
	}
	settle := a.settleIfDone(last) // records every settled check
	a.saveChecks()
	a.loadPending()
	return settle
}

// fixName names fix h of p for a flash: the anchor it edits, else the
// check's anchor, else the check.
func fixName(p *proposal.Proposal, h finding.Hunk) string {
	switch {
	case h.Anchor != "":
		return h.Anchor
	case p.Primary != nil && p.Primary.Name != "":
		return p.Primary.Name
	}
	return p.ID
}

// recordByHand is y on a fix whose change Chris made by hand: it records
// it, applying nothing.
func (a *App) recordByHand(c *card) tea.Cmd {
	a.decide(c.p, c.key, decHand)
	a.holdHand(c.p, c.hunk.File, c.key)
	a.setFlash("Already done by hand: recorded, nothing applied")
	settle := a.settleIfDone(c.p)
	a.saveChecks()
	a.loadPending()
	a.advanceFrom(c.id())
	return settle
}

// dismissProblem is n on a problem: it is dismissed, and so is each of
// its undecided fixes that resolves no other open problem.
func (a *App) dismissProblem(c *card) {
	p := c.p
	a.decide(p, c.prob.key, decDismissed)
	var all []*problem
	for _, cl := range clustersOf(p) {
		all = append(all, cl.problems...)
	}
	for _, n := range c.prob.fixes {
		if a.decided(p.ID, hunkKey(n)) != "" {
			continue
		}
		wanted := slices.ContainsFunc(all, func(o *problem) bool {
			return slices.Contains(o.fixes, n) && a.decided(p.ID, o.key) != decDismissed
		})
		if !wanted {
			a.decide(p, hunkKey(n), decDismissed)
		}
	}
}

// ---- where things are ----

// hunkLoc is where fix h is in the buffers, and whether it was found.
func (a *App) hunkLoc(h finding.Hunk) (Location, bool) {
	src := string(a.ws.Source(h.File))
	if src == "" && a.ws.File(h.File) == nil {
		return Location{}, false
	}
	r, _, ok := locateHunk(a.diskOf(h.File, src), src, h)
	if !ok {
		return Location{}, false
	}
	return Location{Path: h.File, Off: r.Start}, true
}

// anchorAt is where anchor name ("$Name") is, when it exists once.
func (a *App) anchorAt(name string) (Location, bool) {
	if a.ws.Spec == nil || name == "" {
		return Location{}, false
	}
	ans := a.ws.Spec.Lookup(strings.TrimPrefix(name, "$"))
	if len(ans) != 1 {
		return Location{}, false
	}
	return anchorLoc(ans[0]), true
}

// noteLoc is where a note-borne problem points: its quoted text when that
// occurs once, else its anchor, else its file.
func (a *App) noteLoc(pr *problem) (Location, bool) {
	if pr.file != "" && a.ws.File(pr.file) != nil {
		src := string(a.ws.Source(pr.file))
		if pr.find != "" && strings.Count(src, pr.find) == 1 {
			return Location{Path: pr.file, Off: strings.Index(src, pr.find)}, true
		}
	}
	if l, ok := a.anchorAt(pr.anchor); ok && (pr.file == "" || l.Path == pr.file) {
		return l, true
	}
	if pr.file != "" && a.ws.File(pr.file) != nil {
		return Location{Path: pr.file}, true
	}
	return Location{}, false
}

// problemLoc is where a problem's card points: Chris's edit when a fix
// resolves it elsewhere (the fix's card goes there), else the finding's
// own place.
func (a *App) problemLoc(c *card) (Location, bool) {
	pr := c.prob
	if len(pr.fixes) > 0 && checkOf(c.p) {
		if l, ok := a.anchorAt(c.p.Primary.Name); ok {
			return l, true
		}
	}
	if pr.own > 0 {
		h, _ := hunkOf(c.p, pr.own)
		return a.hunkLoc(h)
	}
	return a.noteLoc(pr)
}
