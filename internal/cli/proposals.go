// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/spell"
	"github.com/jwriteclub/spec/internal/workflow"
)

// now returns the session's clock.
func (s *session) now() time.Time {
	if s.env.Now != nil {
		return s.env.Now()
	}
	return time.Now()
}

// store returns the proposal store of the project at root.
func (s *session) store(root string) *proposal.Store {
	st := proposal.NewStore(root)
	st.Now = s.now
	return st
}

// loadProposal reads proposal id, mapping errors to outcomes.
func (s *session) loadProposal(root, id string) (*proposal.Proposal, *outcome) {
	if e := s.workflow().ReconcileChecks(root); e != nil {
		return nil, fromWF(e)
	}
	p, err := s.store(root).Load(id)
	if err != nil {
		if errors.Is(err, proposal.ErrNotFound) {
			return nil, fail(exitFail, "not_found", "no proposal %q (spec proposals lists them; decided ones are removed after acknowledgement or 7 days)", id)
		}
		return nil, internalErr(err)
	}
	return p, nil
}

// ---- output ----

// filedData is the data of change and propose.
type filedData struct {
	Proposals []*proposal.Proposal `json:"proposals"`
	Skipped   []string             `json:"skipped"`
	// Warnings are things the filer should know, such as another pending
	// proposal for the same anchor (propose only).
	Warnings []string  `json:"warnings"`
	Next     nextJSON  `json:"next"`
	Lock     *lockJSON `json:"lock,omitempty"`
	// ClearedBy is set when the per-edit cascade cleared every anchor and
	// the check ran for none: "gate" when the gate cleared them all, else
	// "triage" (propose and change only; absent when the check ran).
	ClearedBy string `json:"cleared_by,omitempty"`
	// Cleared lists the anchors the cascade cleared.
	Cleared []clearedJSON `json:"cleared,omitempty"`
	// Spell is the spell check of the proposed text (propose and change).
	Spell *spell.Report `json:"spell,omitempty"`
}

// clearedJSON is one anchor the cascade cleared.
type clearedJSON struct {
	Anchor    string `json:"anchor"`
	ClearedBy string `json:"cleared_by"`
	Reason    string `json:"reason"`
	// ID is the proposal filed for it ("" with --keep: nothing is filed).
	ID     string           `json:"id"`
	Stages []proposal.Stage `json:"stages"`
}

// clearedOf lists the proposals of ps the cascade cleared, and the
// cleared_by of the whole run: set only when every one of ps was cleared.
func clearedOf(ps []*proposal.Proposal) ([]clearedJSON, string) {
	var out []clearedJSON
	by := ""
	for _, p := range ps {
		inf := p.Inference
		if inf == nil || inf.ClearedBy == "" {
			return clearedList(ps), ""
		}
		if by == "" || inf.ClearedBy == proposal.StageTriage {
			by = inf.ClearedBy
		}
	}
	out = clearedList(ps)
	return out, by
}

// clearedList is clearedOf without the run's verdict.
func clearedList(ps []*proposal.Proposal) []clearedJSON {
	var out []clearedJSON
	for _, p := range ps {
		inf := p.Inference
		if inf == nil || inf.ClearedBy == "" || p.Primary == nil {
			continue
		}
		reason := ""
		for _, st := range inf.Stages {
			if st.Name == inf.ClearedBy {
				reason = workflow.ClearedText(st)
			}
		}
		out = append(out, clearedJSON{Anchor: p.Primary.Name, ClearedBy: inf.ClearedBy, Reason: reason, ID: p.ID, Stages: inf.Stages})
	}
	return out
}

// stageLines are a proposal's cascade stages, one indented line each.
func stageLines(p *proposal.Proposal, indent string) string {
	if p.Inference == nil {
		return ""
	}
	var b strings.Builder
	for _, st := range p.Inference.Stages {
		b.WriteString(indent + workflow.StageLine(st))
		if st.CostUSD > 0 {
			fmt.Fprintf(&b, ", $%.5f", st.CostUSD)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// proposalData is the data of proposal, reject and a failed approve.
type proposalData struct {
	Proposal *proposal.Proposal `json:"proposal"`
	Acked    bool               `json:"acked"`
}

// approveData is the data of approve and review.
type approveData struct {
	Warning  string             `json:"warning,omitempty"`
	Proposal *proposal.Proposal `json:"proposal"`
	Commit   string             `json:"commit"`
	Trailers []string           `json:"trailers"`
	Next     nextJSON           `json:"next"`
}

// settle turns filed proposals into the outcome of change or propose:
// without wait, exit 2 (pending) at once; with wait, block until every
// proposal is decided, any primary is stale, or the wait ends. Then exit 0
// when all are applied, 3 when any was rejected, 6 for a stale primary,
// otherwise 2 when any is still pending. Recovery errors take priority.
// Decisions delivered after a wait (or when ack is set, for auto-approved
// ones) are acknowledged (proposal.Store.Acknowledge).
func (s *session) settle(root string, ps []*proposal.Proposal, skipped []string, wait time.Duration, ack bool, human string) *outcome {
	st := s.store(root)
	if wait > 0 {
		ids := make([]string, 0, len(ps))
		for _, p := range ps {
			ids = append(ids, p.ID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		got, err := s.workflow().Wait(ctx, root, ids, s.env.waitPoll)
		cancel()
		switch {
		case err == nil, errors.Is(err, context.DeadlineExceeded):
			if got != nil {
				ps = got
			}
		case errors.Is(err, proposal.ErrNotFound):
			return fail(exitFail, "not_found", "a proposal being waited for was removed (acknowledged elsewhere); run `spec proposals`")
		default:
			var e *workflow.Error
			if errors.As(err, &e) {
				if got != nil {
					ps = got
				}
				return filedFailure(ps, skipped, fromWF(e))
			}
			return internalErr(err)
		}
	}
	return s.verdict(st, ps, skipped, ack || wait > 0, human)
}

// verdict maps the proposals' states to an exit code and output.
func (s *session) verdict(st *proposal.Store, ps []*proposal.Proposal, skipped []string, ack bool, human string) *outcome {
	if skipped == nil {
		skipped = []string{}
	}
	var pending, rejected, stale []*proposal.Proposal
	for _, p := range ps {
		switch p.Status {
		case proposal.StatusPending:
			pending = append(pending, p)
			if p.StalePrimary != nil {
				stale = append(stale, p)
			}
		case proposal.StatusRejected:
			rejected = append(rejected, p)
		}
	}
	var b strings.Builder
	b.WriteString(human)
	for _, p := range ps {
		b.WriteString(renderProposal(p))
	}
	data := filedData{Proposals: ps, Skipped: skipped, Warnings: []string{}}
	data.Cleared, data.ClearedBy = clearedOf(ps)
	out := &outcome{data: &data}
	switch {
	case len(rejected) > 0:
		var why []string
		for _, p := range rejected {
			reason := ""
			if p.Decision != nil {
				reason = p.Decision.Reason
			}
			why = append(why, fmt.Sprintf("%s: %s", p.ID, reason))
		}
		out.exit, out.code = exitRejected, "rejected"
		out.message = "rejected: " + strings.Join(why, "; ")
		data.Next = nextJSON{Text: "read the reason, revise the change, and file it again; do not commit the rejected text", Argv: []string{}}
	case len(stale) > 0:
		var reasons []string
		for _, p := range stale {
			reasons = append(reasons, p.ID+": "+p.StalePrimary.Reason)
		}
		out.exit, out.code = exitStale, "stale"
		out.message = "pending primary no longer applies: " + strings.Join(reasons, "; ")
		data.Next = nextJSON{Text: "inspect the proposal and current source; ask the user to explicitly close or rework it and file remaining work again. Pending receipts are retained", Argv: []string{"spec", "proposal", stale[0].ID}}
	case len(pending) > 0:
		ids := make([]string, 0, len(pending))
		for _, p := range pending {
			ids = append(ids, p.ID)
		}
		out.exit, out.code = exitPending, "pending"
		out.message = fmt.Sprintf("waiting for review: %s", strings.Join(ids, ", "))
		data.Next = nextJSON{
			Text: fmt.Sprintf("wait for a human to review %s (`spec review %s`); `spec proposal %s --wait 30m` reports a decision or stale primary", strings.Join(ids, ", "), ids[0], ids[0]),
			Argv: []string{"spec", "proposal", ids[0], "--wait", "30m"},
		}
	default:
		data.Next = nextJSON{Text: "the change is applied; run `spec status` for the trailer lines, then commit", Argv: []string{"spec", "status"}}
		if allModels(ps) {
			data.Next = nextJSON{Text: "the model change is applied; commit the model (a model edit needs no trailer)", Argv: []string{}}
		}
		for _, p := range ps {
			if p.Decision != nil && p.Decision.Commit != "" {
				data.Next = nextJSON{Text: "the change is applied and committed; nothing more to do", Argv: []string{}}
			}
		}
	}
	if ack {
		for _, p := range ps {
			if p.Decided() {
				if err := st.Acknowledge(p); err != nil && !errors.Is(err, proposal.ErrNotFound) {
					lg.WithError(err).Warn("cannot acknowledge receipt")
				}
			}
		}
	}
	b.WriteString("next: " + data.Next.Text + "\n")
	out.human = b.String()
	if out.exit != exitOK {
		out.stderr = "spec: " + out.message + "\n"
	}
	return out
}

// allModels reports whether every proposal of ps is a model proposal.
func allModels(ps []*proposal.Proposal) bool {
	for _, p := range ps {
		if !p.IsModel() {
			return false
		}
	}
	return len(ps) > 0
}

// renderProposal is the human view of a proposal.
func renderProposal(p *proposal.Proposal) string {
	var b strings.Builder
	if p.ID == "" {
		b.WriteString("findings (not filed: no fix to review):\n")
	} else {
		head := fmt.Sprintf("proposal %s (%s, %s", p.ID, p.Kind, p.Status)
		if p.Author != "" {
			head += ", by " + p.Author
		}
		b.WriteString(head + ")\n")
	}
	if p.StalePrimary != nil {
		b.WriteString("  stale primary: " + p.StalePrimary.Reason + "\n")
	}
	if p.SourceKind != "" {
		fmt.Fprintf(&b, "  source: %s\n", p.SourceKind)
	}
	if p.CheckState != "" {
		fmt.Fprintf(&b, "  required check: %s · %s\n", p.CheckState, p.CheckMessage)
	}
	if fx := p.Fix; fx != nil {
		fmt.Fprintf(&b, "  fixes: %s at %s:%d:%d (%s): %s\n", fx.Code, fx.File, fx.Line, fx.Col, fx.Class, fx.Message)
	}
	if p.Why != "" {
		b.WriteString("  why: " + p.Why + "\n")
	}
	if r := p.Rethink; r != nil {
		fmt.Fprintf(&b, "  rethink of %s\n", r.Of)
		for _, n := range r.Notes {
			if n.Question != "" {
				b.WriteString("  asked: " + strings.Join(strings.Fields(n.Question), " ") + "\n")
			}
			b.WriteString("  answer: " + strings.Join(strings.Fields(n.Text), " ") + "\n")
		}
	}
	b.WriteString(stageLines(p, "  "))
	if p.Summary != "" {
		label := "  check: "
		if p.Fix != nil {
			label = "  fix: "
		}
		if p.IsModel() {
			label = "  change: "
		}
		b.WriteString(label + p.Summary + "\n")
	}
	if p.IsModel() {
		b.WriteString(renderModel(p))
	}
	if pr := p.Primary; pr != nil {
		fmt.Fprintf(&b, "  [%d] %s %s in %s", pr.N, pr.Action, pr.Name, pr.File)
		if pr.OldKind != "" && pr.NewKind != "" && pr.OldKind != pr.NewKind {
			fmt.Fprintf(&b, " (%s -> %s)", pr.OldKind, pr.NewKind)
		}
		if pr.InWorktree {
			b.WriteString(" (already in the working tree)")
		}
		b.WriteString("\n")
		b.WriteString(indentDiff(pr.OldText, pr.NewText))
	}
	for _, h := range p.Hunks {
		if p.IsModel() {
			break
		}
		fmt.Fprintf(&b, "  [%d] %s %s in %s", h.N, h.Severity, h.Issue, h.File)
		if h.Anchor != "" {
			b.WriteString(" " + h.Anchor)
		}
		if p.Rethink != nil && p.Rethink.PrimaryHunk == h.N {
			b.WriteString(" (changes the edit itself)")
		}
		b.WriteString("\n")
		if h.Explanation != "" {
			b.WriteString("      " + h.Explanation + "\n")
		}
		b.WriteString(indentDiff(h.Find, h.Replace))
	}
	for _, n := range p.Notes {
		fmt.Fprintf(&b, "  note: %s %s in %s", n.Severity, n.Issue, n.File)
		if n.Anchor != "" {
			b.WriteString(" " + n.Anchor)
		}
		fmt.Fprintf(&b, " (%s)\n", n.Reason)
		if n.Explanation != "" {
			b.WriteString("      " + n.Explanation + "\n")
		}
	}
	for _, q := range p.Questions {
		b.WriteString("  question: " + q + "\n")
	}
	if d := p.Decision; d != nil {
		switch p.Status {
		case proposal.StatusApplied:
			fmt.Fprintf(&b, "  applied hunks %s by %s", joinInts(d.Applied), d.By)
			if d.Commit != "" {
				b.WriteString(" in commit " + shortSHA(d.Commit))
			}
			b.WriteString("\n")
			for _, f := range d.Failed {
				fmt.Fprintf(&b, "  hunk %d failed: %s\n", f.N, f.Reason)
			}
		case proposal.StatusRejected:
			fmt.Fprintf(&b, "  rejected by %s: %s\n", d.By, d.Reason)
		}
	}
	return b.String()
}

// indentDiff shows old and new text as -/+ lines.
func indentDiff(old, new string) string {
	var b strings.Builder
	for _, l := range splitKeep(old) {
		b.WriteString("      - " + l + "\n")
	}
	for _, l := range splitKeep(new) {
		b.WriteString("      + " + l + "\n")
	}
	return b.String()
}

func splitKeep(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func joinInts(ns []int) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ",")
}

// maxHunkNumber bounds --hunks numbers (a proposal has at most about a
// hundred hunks), so a typo like 1-1000000000 cannot allocate gigabytes.
const maxHunkNumber = 1000

// parseHunks parses --hunks 1,3 or 1-3.
func parseHunks(spec string) ([]int, *outcome) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || a < 1 || b < a || b > maxHunkNumber {
			return nil, usageErr("--hunks %q: give hunk numbers like 1,3 or 2-4", spec)
		}
		for n := a; n <= b; n++ {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out, nil
}
