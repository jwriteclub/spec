// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

// runChangeModel is `spec change --tla PATH --body-file F`: it files the
// model's new text as a model proposal and leaves the working tree alone.
func (s *session) runChangeModel(model string, f changeFlags) *outcome {
	switch {
	case f.source != "" && f.source != "agent" && f.source != "human":
		return usageErr("--source must be agent or human")
	case strings.TrimSpace(f.why) == "":
		return usageErr("--why is required: say why the change is needed")
	case f.bodyFile == "":
		return usageErr("--tla needs --body-file F with the model's whole new text (- for stdin)")
	case f.remove || f.def || f.inv || f.add || f.after != "" || f.section != "":
		return usageErr("--tla takes only --body-file: --remove, --def, --inv, --add, --after and --section are for spec anchors")
	case f.wait < 0:
		return usageErr("--wait must not be negative")
	}
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	proj, o := s.openRoot(root)
	if o != nil {
		return o
	}
	body, o := s.readBody(f.bodyFile)
	if o != nil {
		return o
	}
	p, e := s.workflow().ProposeModel(context.Background(), proj, workflow.ModelRequest{
		Path: s.modelPath(root, model), Text: []byte(body), Why: f.why,
		Author: f.sourceAuthor(), SourceKind: f.source,
	})
	if e != nil {
		out := fromWF(e)
		if e.Code == "check_failed" && e.Proposal != nil {
			out.human = renderProposal(e.Proposal)
		}
		return out
	}
	return s.fileProposals(root, []*proposal.Proposal{p}, nil, f.fileFlags, "")
}

// modelPath turns --tla into a project-relative slash path: a path that
// exists from the working directory is taken from there, any other as
// relative to the project root.
func (s *session) modelPath(root, arg string) string {
	abs := arg
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.env.Cwd, arg)
	}
	if _, err := os.Stat(abs); err == nil || filepath.IsAbs(arg) {
		if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(filepath.Clean(arg))
}

// renderModel is the human view of what a model proposal edits and how its
// check went.
func renderModel(p *proposal.Proposal) string {
	m := p.Model
	var b strings.Builder
	fmt.Fprintf(&b, "  model: %s", m.File)
	if m.Module != "" {
		fmt.Fprintf(&b, " (module %s)", m.Module)
	}
	b.WriteString("\n")
	if len(m.Decls) > 0 {
		fmt.Fprintf(&b, "  definitions: %s\n", strings.Join(m.Decls, ", "))
	}
	b.WriteString("  model check: " + ModelCheckText(m.Check, p.ID != "") + "\n")
	for _, d := range m.Check.Diagnostics {
		b.WriteString("    " + workflow.DiagnosticLine(d) + "\n")
	}
	for _, l := range m.Links {
		fmt.Fprintf(&b, "  link: %s:%d:%d: %s: %s [%s]\n", m.File, l.Line, l.Col, l.Severity, l.Message, l.Code)
	}
	for _, h := range p.Hunks {
		fmt.Fprintf(&b, "  [%d] edit in %s\n", h.N, h.File)
		b.WriteString(indentDiff(h.Find, h.Replace))
	}
	return b.String()
}

// ModelCheckText says in words how a model proposal's check went. filed is
// false for the unfiled proposal a `check_failed` refusal carries, whose
// errors include ones the proposal adds. A check that did not cover semantics
// says "syntax checked".
func ModelCheckText(c proposal.ModelCheck, filed bool) string {
	who := c.Checker
	if c.Checker != "" {
		who = " · " + who
	}
	if c.Version != "" {
		who += " " + c.Version
	}
	if c.Level != "" {
		scope := c.Level
		if c.Completion != formal.CompletionUnknown {
			scope += ", " + string(c.Completion)
		}
		who += " (" + scope + ")"
	}
	again := ""
	if c.Rechecked {
		again = ", rechecked at approval"
	}
	partial := ""
	if !c.FullCheck() {
		partial = "syntax checked, "
	}
	switch c.State {
	case proposal.ModelClean:
		return fmt.Sprintf("%sclean%s%s", partial, who, again)
	case proposal.ModelErrors:
		if c.Rechecked {
			return fmt.Sprintf("%s%s%s%s", partial, plural(c.Errors(), "error"), who, again)
		}
		if !filed {
			return fmt.Sprintf("%s%s%s (includes errors this proposal adds; nothing was filed)", partial, plural(c.Errors(), "error"), who)
		}
		return fmt.Sprintf("%s%s%s (errors the model already had; none is new)", partial, plural(c.Errors(), "error"), who)
	case proposal.ModelCheckFailed:
		return fmt.Sprintf("failed to run%s: %s", who, c.Message)
	}
	return "not checked (no model checker is installed yet)"
}
