// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

// ---- proposals ----

type proposalSummary struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Created   string `json:"created"`
	Author    string `json:"author"`
	Anchor    string `json:"anchor"`
	Why       string `json:"why"`
	Hunks     int    `json:"hunks"`
	Notes     int    `json:"notes"`
	Questions int    `json:"questions"`
}

type proposalsData struct {
	Proposals []proposalSummary `json:"proposals"`
}

func (s *session) proposalsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "proposals",
		Short: "list pending proposals and unacknowledged receipts of decided ones",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errTooMany("proposals")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runProposals()
			return nil
		},
	}
}

func (s *session) runProposals() *outcome {
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	if e := s.workflow().ReconcileChecks(root); e != nil {
		return fromWF(e)
	}
	ps, err := s.store(root).List()
	if err != nil {
		return internalErr(err)
	}
	data := proposalsData{Proposals: []proposalSummary{}}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	for _, p := range ps {
		sum := proposalSummary{
			ID: p.ID, Kind: p.Kind, Status: p.Status, Created: p.Created.UTC().Format(time.RFC3339),
			Author: p.Author, Why: p.Why, Hunks: len(p.Numbers()), Notes: len(p.Notes), Questions: len(p.Questions),
		}
		if p.Primary != nil {
			sum.Anchor = p.Primary.Name
		}
		data.Proposals = append(data.Proposals, sum)
		what := sum.Anchor
		switch {
		case what != "":
		case p.Fix != nil:
			what = "fix " + p.Fix.Code
		case p.IsModel():
			what = "model " + p.Model.File
		default:
			what = "analysis"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, p.Status, what, plural(sum.Hunks, "hunk"), p.Author, oneLineWhy(p.Why))
	}
	w.Flush()
	if len(ps) == 0 {
		b.WriteString("no proposals\n")
	}
	return &outcome{data: data, human: b.String()}
}

func oneLineWhy(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 60 {
		cut := 60
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		s = s[:cut] + "..."
	}
	return s
}

// ---- proposal ID ----

func (s *session) proposalCommand() *cobra.Command {
	var ack bool
	var wait time.Duration
	var answer, by string
	var question int
	cmd := &cobra.Command{
		Use:   "proposal ID [--ack] [--wait D] [--answer TEXT [--question N]]",
		Short: "show a proposal; --ack removes a decided one's receipt; --wait reports a decision or stale primary",
		Long: "Without flags it prints the proposal and exits 0. --wait D checks the current files and exits\n" +
			"0 applied, 3 rejected, 6 stale primary, or 2 still pending after D. --ack acknowledges only decided\n" +
			"receipts. A proposal with a stale primary stays pending; inspect it and ask the user to close or\n" +
			"rework it. An interrupted write comes first (exit 1 recovery_required, or 5 lock_held).\n" +
			"--answer TEXT answers a pending check's or analysis's question (the first, or --question N), and\n" +
			"the model redoes it (a check that spec propose --keep made, or a spec analyze). The new proposal is\n" +
			"filed and supersedes this one; every fix it proposes, a change to the edit itself included, is a\n" +
			"hunk, applied only when approved.",
		Args: needArgs(1, "proposal ID [--ack] [--wait D] [--answer TEXT [--question N]]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if answer != "" || question != 0 {
				s.result = s.runAnswer(args[0], answer, question, by, ack || wait != 0)
				return nil
			}
			s.result = s.runProposal(args[0], ack, wait)
			return nil
		},
	}
	cmd.Flags().BoolVar(&ack, "ack", false, "acknowledge a decided proposal: remove its receipt")
	cmd.Flags().DurationVar(&wait, "wait", 0, "wait for a decision, stale primary (exit 6), or this timeout (e.g. 30m)")
	cmd.Flags().StringVar(&answer, "answer", "", "answer the check's or analysis's question; the model rethinks it")
	cmd.Flags().IntVar(&question, "question", 0, "which question --answer answers (1-based; default the first)")
	cmd.Flags().StringVar(&by, "by", "", "who answers (default: human)")
	return cmd
}

// runAnswer answers question n (1-based, 0 for the first) of pending check
// or analysis id with answer: the model rethinks it, and the new one is
// filed and supersedes id.
func (s *session) runAnswer(id, answer string, n int, by string, other bool) *outcome {
	if other {
		return usageErr("--answer excludes --ack and --wait")
	}
	if strings.TrimSpace(answer) == "" {
		return usageErr("--answer needs the answer text")
	}
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	p, o := s.loadProposal(root, id)
	if o != nil {
		return o
	}
	note := proposal.RethinkNote{Text: answer}
	switch {
	case n < 0 || n > len(p.Questions) || n > 0 && len(p.Questions) == 0:
		return usageErr("--question must be between 1 and %d (proposal %s asks %d)", len(p.Questions), id, len(p.Questions))
	case n > 0:
		note.Question = p.Questions[n-1]
	case len(p.Questions) > 0:
		note.Question = p.Questions[0]
	}
	proj, o := s.openRoot(root)
	if o != nil {
		return o
	}
	wf := s.workflow()
	np, e := wf.Rethink(context.Background(), proj, workflow.RethinkRequest{Of: p, Note: note, Author: byOr(by)})
	if e != nil {
		return fromWF(e)
	}
	lock, e := wf.DecisionLock(context.Background(), root, "spec proposal --answer "+id)
	if e != nil {
		return fromWF(e)
	}
	defer func() { _ = lock.Release() }()
	if err := s.store(root).Create(np); err != nil {
		return internalErr(err)
	}
	if _, e := wf.RejectLocked(lock, root, id, "superseded by rethink "+np.ID, byOr(by)); e != nil {
		// The old check was decided meanwhile (or recovery is pending): do not
		// leave the new check pending beside a failed command.
		if err := s.store(root).Remove(np.ID); err != nil && !errors.Is(err, proposal.ErrNotFound) {
			lg.WithError(err).Warn("cannot remove " + np.ID + " after a failed supersede")
		}
		return fromWF(e)
	}
	return &outcome{data: proposalData{Proposal: np}, human: renderProposal(np) + "supersedes " + id + "\n"}
}

func (s *session) runProposal(id string, ack bool, wait time.Duration) *outcome {
	if ack && wait > 0 {
		return usageErr("--ack and --wait exclude each other (--wait acknowledges what it reports)")
	}
	if wait < 0 {
		return usageErr("--wait must not be negative")
	}
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	p, o := s.loadProposal(root, id)
	if o != nil {
		return o
	}
	if wait > 0 {
		return s.settle(root, []*proposal.Proposal{p}, nil, wait, false, "")
	}
	if ack {
		if !p.Decided() {
			return fail(exitFail, "not_pending", "proposal %s is still pending; only a decided proposal can be acknowledged", id)
		}
		if err := s.store(root).Acknowledge(p); err != nil && !errors.Is(err, proposal.ErrNotFound) {
			return internalErr(err)
		}
		return &outcome{data: proposalData{Proposal: p, Acked: true}, human: renderProposal(p) + "acknowledged: the receipt is removed\n"}
	}
	return &outcome{data: proposalData{Proposal: p}, human: renderProposal(p)}
}

// ---- approve ----

func (s *session) approveCommand() *cobra.Command {
	var hunks, by string
	var commit bool
	cmd := &cobra.Command{
		Use:   "approve ID [--hunks 1,3] [--commit]",
		Short: "apply a proposal (all hunks, or the listed ones) to the spec files",
		Long: "Re-verifies every hunk against the files first: a hunk whose text moved or changed, or that would\n" +
			"introduce a lint error, fails alone; a change whose anchor changed since fails the whole proposal\n" +
			"(exit 1 stale; it stays pending with stale_primary, and --wait reports exit 6). Inspect it, and ask\n" +
			"the user to close or rework it and refile the remaining work. A partly applied stale proposal is\n" +
			"closed by rejecting its remaining edits before refiling; the applied edits stay.\n" +
			"--commit commits the touched spec files with the Spec-Change/Spec-Rename trailers and the reason.",
		Args: needArgs(1, "approve ID [--hunks 1,3] [--commit]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runApprove(args[0], hunks, by, commit)
			return nil
		},
	}
	cmd.Flags().StringVar(&hunks, "hunks", "", "apply only these hunks, e.g. 1,3 or 1-3 (hunk 1 of a change is the change itself)")
	cmd.Flags().BoolVar(&commit, "commit", false, "commit the touched spec files with the required trailers")
	cmd.Flags().StringVar(&by, "by", "", "who approves (default: human)")
	return cmd
}

func (s *session) runApprove(id, hunks, by string, commit bool) *outcome {
	accept, o := parseHunks(hunks)
	if o != nil {
		return o
	}
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	res, e := s.workflow().Apply(root, id, accept, byOr(by), commit)
	if e != nil {
		return fromWF(e)
	}
	return approvedOutcome(res)
}

func byOr(by string) string {
	return workflow.ByOr(by)
}

func approvedOutcome(res *workflow.Applied) *outcome {
	data := approveData{Proposal: res.Proposal, Commit: res.Commit, Trailers: res.Trailers, Warning: res.Warning}
	var b strings.Builder
	b.WriteString(renderProposal(res.Proposal))
	if res.Commit != "" {
		fmt.Fprintf(&b, "committed %s", shortSHA(res.Commit))
		if len(res.Trailers) > 0 {
			b.WriteString(" with:\n" + strings.Join(res.Trailers, "\n"))
		}
		b.WriteString("\n")
		data.Next = nextJSON{Text: "done; the waiting agent (if any) sees the decision", Argv: []string{}}
	} else if res.Proposal.IsModel() {
		data.Next = nextJSON{Text: "commit the model (a model edit needs no trailer), or approve with --commit next time", Argv: []string{}}
	} else {
		data.Next = nextJSON{Text: "run `spec status` for the trailer lines, then commit the spec files", Argv: []string{"spec", "status"}}
	}
	if res.Warning != "" {
		b.WriteString("warning: " + res.Warning + "\n")
	}
	b.WriteString("next: " + data.Next.Text + "\n")
	return &outcome{data: data, human: b.String()}
}

// ---- reject ----

func (s *session) rejectCommand() *cobra.Command {
	var why, by string
	cmd := &cobra.Command{
		Use:   "reject ID --why W",
		Short: "reject a proposal; the reason reaches the agent waiting on it",
		Args:  needArgs(1, "reject ID --why W"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runReject(args[0], why, by)
			return nil
		},
	}
	cmd.Flags().StringVar(&why, "why", "", "the reason (required)")
	cmd.Flags().StringVar(&by, "by", "", "who rejects (default: human)")
	return cmd
}

func (s *session) runReject(id, why, by string) *outcome {
	if strings.TrimSpace(why) == "" {
		return usageErr("--why is required: the reason reaches the proposer")
	}
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	p, e := s.workflow().Reject(root, id, strings.TrimSpace(why), byOr(by))
	if e != nil {
		return fromWF(e)
	}
	return &outcome{data: proposalData{Proposal: p}, human: renderProposal(p)}
}
