// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/workflow"
)

func (s *session) reviewCommand() *cobra.Command {
	var commit bool
	var by string
	cmd := &cobra.Command{
		Use:   "review ID [--commit]",
		Short: "review a proposal hunk by hunk in the terminal: y apply, n skip, e retry with feedback, q quit",
		Long: "Shows each hunk and asks what to do. `e` sends your feedback to the model, which revises that hunk.\n" +
			"At the end the accepted hunks are applied (as `spec approve --hunks`); skipping the change itself,\n" +
			"or every hunk, asks for a reason and rejects. q leaves the proposal pending.",
		Args: needArgs(1, "review ID [--commit]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runReview(args[0], commit, by)
			return nil
		},
	}
	cmd.Flags().BoolVar(&commit, "commit", false, "commit the touched spec files with the required trailers")
	cmd.Flags().StringVar(&by, "by", "", "who reviews (default: human)")
	s.addProviderFlags(cmd)
	return cmd
}

// reviewer is one interactive session.
type reviewer struct {
	s    *session
	in   *bufio.Reader
	out  io.Writer
	root string
	p    *proposal.Proposal
	ck   *workflow.Checker
}

// cleanWriter shows control characters in what it writes as text (the
// review prints model text outside render). Each Write is a whole
// formatted string, so no character is split between calls.
type cleanWriter struct{ w io.Writer }

func (c cleanWriter) Write(b []byte) (int, error) {
	if _, err := io.WriteString(c.w, termtext.Clean(string(b))); err != nil {
		return 0, err
	}
	return len(b), nil
}

// ask prints prompt and returns the trimmed answer; ok is false at EOF.
func (r *reviewer) ask(prompt string) (string, bool) {
	fmt.Fprint(r.out, prompt)
	line, err := r.in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(r.out)
		return "", false
	}
	return strings.TrimSpace(line), true
}

func (s *session) runReview(id string, commit bool, by string) *outcome {
	if s.json {
		return usageErr("spec review is interactive and has no --json form; use spec approve or spec reject")
	}
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	p, o := s.loadProposal(root, id)
	if o != nil {
		return o
	}
	if p.Status != proposal.StatusPending {
		return fail(exitFail, "not_pending", "proposal %s is already %s", id, p.Status)
	}
	r := &reviewer{s: s, in: bufio.NewReader(s.env.Stdin), out: cleanWriter{s.env.Stdout}, root: root, p: p}
	attribution := ""
	if p.Author != "" {
		attribution = ", by " + p.Author
	}
	fmt.Fprintf(r.out, "proposal %s (%s%s)\n", p.ID, p.Kind, attribution)
	if p.Why != "" {
		fmt.Fprintf(r.out, "why: %s\n", p.Why)
	}
	if p.Summary != "" {
		fmt.Fprintf(r.out, "check: %s\n", p.Summary)
	}
	for _, n := range p.Notes {
		fmt.Fprintf(r.out, "note: %s %s in %s (%s): %s\n", n.Severity, n.Issue, n.File, n.Reason, n.Explanation)
	}
	for _, q := range p.Questions {
		fmt.Fprintf(r.out, "question: %s\n", q)
	}

	if p.IsModel() {
		return r.reviewModel(commit, by)
	}
	var accepted []int
	if pr := p.Primary; pr != nil {
		fmt.Fprintf(r.out, "\n[%d] %s %s in %s\n%s", pr.N, pr.Action, pr.Name, pr.File, indentDiff(pr.OldText, pr.NewText))
		for {
			ans, ok := r.ask(fmt.Sprintf("apply the change itself (hunk %d)? [y/n/q] ", pr.N))
			if !ok || ans == "q" {
				return leftPending(p)
			}
			if ans == "y" {
				accepted = append(accepted, pr.N)
				break
			}
			if ans == "n" {
				return r.reject(by, "the change itself was declined")
			}
			fmt.Fprintln(r.out, "answer y (apply), n (reject the proposal) or q (decide later); the change is the author's text, so it cannot be revised here")
		}
	}
	for i := range p.Hunks {
		h := &p.Hunks[i]
		for decided := false; !decided; {
			fmt.Fprintf(r.out, "\n[%d] %s %s in %s %s\n", h.N, h.Severity, h.Issue, h.File, h.Anchor)
			if h.Explanation != "" {
				fmt.Fprintf(r.out, "    %s\n", h.Explanation)
			}
			fmt.Fprint(r.out, indentDiff(h.Find, h.Replace))
			ans, ok := r.ask(fmt.Sprintf("apply hunk %d? [y/n/e/q] ", h.N))
			switch {
			case !ok || ans == "q":
				return leftPending(p)
			case ans == "y":
				accepted = append(accepted, h.N)
				decided = true
			case ans == "n":
				decided = true
			case ans == "e":
				fb, ok := r.ask("feedback for the model: ")
				if !ok {
					return leftPending(p)
				}
				if o := r.revise(h, fb); o != nil {
					if o.code == "not_pending" {
						return o
					}
					fmt.Fprintf(r.out, "could not revise: %s\n", o.message)
				}
			default:
				fmt.Fprintln(r.out, "answer y (apply), n (skip), e (give feedback and let the model revise it) or q (decide later)")
			}
		}
	}
	if len(accepted) == 0 {
		return r.reject(by, "no hunk was accepted")
	}
	res, e := s.workflow().ApplySeen(root, p, accepted, byOr(by), commit)
	if e != nil {
		return fromWF(e)
	}
	return approvedOutcome(res)
}

// reviewModel reviews a model proposal, which applies as a whole: one y
// applies every hunk, and a model left with check errors asks for a second y.
// The model is checked again first when it moved since filing.
func (r *reviewer) reviewModel(commit bool, by string) *outcome {
	w := r.s.workflow()
	proj, o := r.s.openRoot(r.root)
	if o != nil {
		return o
	}
	if _, e := w.RecheckModel(context.Background(), proj, r.p, nil); e != nil {
		return fromWF(e)
	}
	fmt.Fprint(r.out, renderModel(r.p))
	for {
		ans, ok := r.ask(fmt.Sprintf("apply the model edit (%s, as a whole)? [y/n/q] ", plural(len(r.p.Hunks), "hunk")))
		if !ok || ans == "q" {
			return leftPending(r.p)
		}
		if ans == "n" {
			return r.reject(by, "the model edit was declined")
		}
		if ans == "y" {
			break
		}
		fmt.Fprintln(r.out, "answer y (apply), n (reject the proposal) or q (decide later); a model edit is checked and applied as a whole")
	}
	if warn := workflow.ModelErrorsWarning(r.p); warn != "" {
		ans, ok := r.ask(warn + "; apply anyway? [y/n] ")
		if !ok || ans != "y" {
			return leftPending(r.p)
		}
	}
	res, e := w.ApplySeen(r.root, r.p, nil, byOr(by), commit)
	if e != nil {
		return fromWF(e)
	}
	return approvedOutcome(res)
}

// revise asks the model to redo hunk h with the reviewer's feedback and,
// when the revision verifies, stores it.
func (r *reviewer) revise(h *finding.Hunk, feedback string) *outcome {
	w := r.s.workflow()
	if r.ck == nil {
		ck, e := w.OpenChecker(context.Background(), false)
		if e != nil {
			return fromWF(e)
		}
		r.ck = ck
	}
	rev, e := w.ReviseHunk(r.ck, r.root, r.p, *h, feedback)
	if e != nil {
		return fromWF(e)
	}
	if rev.Withdrawn {
		fmt.Fprintln(r.out, "the model withdrew the edit; answer n to skip it")
		return nil
	}
	*h = rev.Hunk
	r.p.Revision = rev.Proposal.Revision
	return nil
}

// reject asks for a reason and rejects; an empty answer leaves the proposal
// pending.
func (r *reviewer) reject(by, context string) *outcome {
	reason, ok := r.ask(context + "; reason to reject (empty: decide later): ")
	if !ok || reason == "" {
		return leftPending(r.p)
	}
	p, e := r.s.workflow().Reject(r.root, r.p.ID, reason, byOr(by))
	if e != nil {
		return fromWF(e)
	}
	return &outcome{data: proposalData{Proposal: p}, human: renderProposal(p)}
}

func leftPending(p *proposal.Proposal) *outcome {
	return &outcome{data: proposalData{Proposal: p}, human: fmt.Sprintf("proposal %s is still pending\n", p.ID)}
}
