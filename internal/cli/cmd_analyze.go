// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/spell"
	"github.com/jwriteclub/spec/internal/workflow"
)

type analyzeData struct {
	// Mode is changed, all or files.
	Mode string `json:"mode"`
	// Files are the files analyzed (their edited regions for changed).
	Files []string `json:"files"`
	// Regions counts the edited regions sent (changed only).
	Regions int `json:"regions"`
	// Saved is true when the proposal (with a fix hunk or a question) was
	// filed.
	Saved    bool               `json:"saved"`
	Proposal *proposal.Proposal `json:"proposal"`
	// Superseded are the earlier pending analyses of the same scope that
	// this one made stale and rejected as superseded (none the author
	// acted on in spec ui).
	Superseded []string `json:"superseded"`
	// Spell is the spell check of the same text.
	Spell *spell.Report `json:"spell"`
	Next  nextJSON      `json:"next"`
}

func (s *session) analyzeCommand() *cobra.Command {
	var all, changed, full bool
	var by string
	cmd := &cobra.Command{
		Use:   "analyze [--changed | --all | FILE...]",
		Short: "advisory consistency analysis (numbers first, then logic) and spell check of edited text against the whole spec",
		Long: "Default --changed: the regions of spec files that differ from HEAD. --all: every spec file. FILE...:\n" +
			"those files. Prints the findings; fix hunks and questions are filed as an analysis proposal (review\n" +
			"it with `spec review ID`; `spec proposal ID --answer TEXT` answers its question). It supersedes\n" +
			"earlier pending analyses of the same scope that nobody acted on. The same text is spell-checked\n" +
			"(`spec explain spell`). It never blocks: exit 0 whatever it finds, 1 when the provider fails.",
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runAnalyze(args, all, changed, full, by)
			return nil
		},
	}
	cmd.Flags().BoolVar(&changed, "changed", false, "analyze the regions that differ from HEAD (the default)")
	cmd.Flags().BoolVar(&all, "all", false, "analyze every spec file")
	cmd.Flags().BoolVar(&full, "full", false, "send every spec file whole, ignoring max_context_tokens")
	cmd.Flags().StringVar(&by, "by", "", "who asked, recorded on the proposal (default: human)")
	s.addProviderFlags(cmd)
	return cmd
}

func (s *session) runAnalyze(args []string, all, changed, full bool, by string) *outcome {
	modes := 0
	for _, on := range []bool{all, changed, len(args) > 0} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return usageErr("give one of --changed, --all or FILE...")
	}
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	proj, o := s.openRoot(root)
	if o != nil {
		return o
	}
	req := workflow.AnalyzeRequest{Mode: workflow.ModeChanged, Full: full, Author: by}
	switch {
	case all:
		req.Mode = workflow.ModeAll
	case len(args) > 0:
		req.Mode = workflow.ModeFiles
		loaded, err := proj.Load(nil)
		if err != nil {
			return internalErr(err)
		}
		for _, a := range args {
			abs := a
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(s.env.Cwd, a)
			}
			rel, err := filepath.Rel(root, abs)
			if err != nil || strings.HasPrefix(rel, "..") {
				return fail(exitFail, "not_found", "%s is outside the project", a)
			}
			rel = filepath.ToSlash(rel)
			if f := loaded.Spec.File(rel); f == nil || !f.ValidUTF8 {
				return fail(exitFail, "not_found", "%s is not one of the project's spec files (spec ls lists them)", a)
			}
			req.Files = append(req.Files, rel)
		}
	}
	an, e := s.workflow().Analyze(context.Background(), proj, req)
	if e != nil {
		return fromWF(e)
	}
	data := analyzeData{Mode: an.Mode, Files: an.Files, Regions: an.Regions, Saved: an.Saved, Proposal: an.Proposal, Spell: an.Spell, Superseded: []string{}}
	if data.Spell == nil {
		data.Spell = &spell.Report{Flags: []spell.Flag{}}
	}
	if an.Nothing {
		data.Next = nextJSON{Text: "nothing differs from HEAD; run `spec analyze --all` to check everything", Argv: []string{"spec", "analyze", "--all"}}
		return &outcome{data: data, human: "no spec text differs from HEAD; nothing to analyze\nnext: " + data.Next.Text + "\n"}
	}
	if ids, e := s.workflow().SupersedeAnalyses(context.Background(), root, an.Proposal, by); e != nil {
		// Advisory: the analysis stands; the stale ones stay pending.
		lg.WithError(e).Warn("cannot supersede stale analyses")
	} else {
		data.Superseded = append(data.Superseded, ids...)
	}
	rec := an.Record
	p := an.Proposal
	var b strings.Builder
	fmt.Fprintf(&b, "analyzed %s", plural(len(data.Files), "file"))
	if data.Mode == workflow.ModeChanged {
		fmt.Fprintf(&b, " (%s that differ from HEAD)", plural(data.Regions, "region"))
	}
	fmt.Fprintf(&b, " with %s/%s: %d+%d tokens, %.1f s\n", rec.Provider, rec.Model, rec.PromptTokens, rec.CompletionTokens, float64(rec.LatencyMS)/1000)
	if len(an.Omitted) > 0 {
		fmt.Fprintf(&b, "left out to fit max_context_tokens (use --full to send everything): %s\n", strings.Join(an.Omitted, ", "))
	}
	switch {
	case an.Saved && len(p.Hunks) > 0:
		data.Next = nextJSON{Text: fmt.Sprintf("review the fixes with `spec review %s` (or `spec approve %s --hunks N`, `spec reject %s --why W`)", p.ID, p.ID, p.ID), Argv: []string{"spec", "review", p.ID}}
	case an.Saved:
		data.Next = nextJSON{Text: fmt.Sprintf("answer the question with `spec proposal %s --answer TEXT` (or `spec reject %s --why W`)", p.ID, p.ID), Argv: []string{"spec", "proposal", p.ID}}
	default:
		data.Next = nextJSON{Text: "no fix to review; read the notes and questions, if any", Argv: []string{}}
	}
	b.WriteString(renderProposal(p))
	if len(data.Superseded) > 0 {
		fmt.Fprintf(&b, "superseded earlier analyses of the same scope: %s\n", strings.Join(data.Superseded, ", "))
	}
	b.WriteString(renderSpell(an.Spell))
	b.WriteString("next: " + data.Next.Text + "\n")
	return &outcome{data: data, human: b.String()}
}
