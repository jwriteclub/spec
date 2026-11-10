// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/workflow"
)

// ---- diff ----

type diffData struct {
	Baseline baselineJSON `json:"baseline"`
	Changes  []changeJSON `json:"changes"`
	Skipped  []string     `json:"skipped"`
}

func (s *session) diffCommand() *cobra.Command {
	var against string
	var staged bool
	cmd := &cobra.Command{
		Use:   "diff [--against REF | --base REF] [--staged]",
		Short: "anchor-level diff against a git revision (default HEAD); exits 0 whatever it finds",
		Long: "Compares anchors by name over the whole project, so moving an anchor between files is not a change.\n" +
			"--staged compares the index (what `spec check` examines) against the baseline instead of the working tree.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errTooMany("diff [--against REF | --base REF] [--staged]")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runDiff(against, staged)
			return nil
		},
	}
	cmd.Flags().StringVar(&against, "against", "", "revision to compare against (default HEAD)")
	cmd.Flags().StringVar(&against, "base", "", "same as --against (the name status and check use)")
	cmd.Flags().BoolVar(&staged, "staged", false, "use the index, not the working tree, as the new side")
	return cmd
}

// newSide builds the view of the side being compared against the baseline.
func (s *session) newSide(staged bool) (*view, *outcome) {
	proj, o := s.openProject()
	if o != nil {
		return nil, o
	}
	if staged {
		return s.stagedView(proj)
	}
	return s.loadViewOf(proj)
}

func (s *session) runDiff(against string, staged bool) *outcome {
	if strings.HasPrefix(against, "-") {
		return fail(exitFail, "bad_ref", "bad revision %q: a revision cannot start with '-'", against)
	}
	v, o := s.newSide(staged)
	if o != nil {
		return o
	}
	cmp, o := s.compare(v, compareOpts{ref: against, staged: staged})
	if o != nil {
		return o
	}
	if cmp.base.State == baseline.StateUnavailable {
		return noBaseline(cmp.base)
	}
	data := diffData{Baseline: cmp.baselineObj(), Changes: cmp.changes(), Skipped: cmp.skipped()}
	var b strings.Builder
	fmt.Fprintf(&b, "comparing %s with %s", data.Baseline.To, data.Baseline.From)
	if cmp.base.State == baseline.StateUnborn {
		b.WriteString(" (no commits yet: every anchor is added)")
	}
	b.WriteString("\n")
	if len(data.Changes) == 0 {
		b.WriteString("no anchor differs\n")
	} else {
		b.WriteString(renderChanges(data.Changes))
	}
	b.WriteString(renderSkipped(data.Skipped))
	return &outcome{data: data, diags: v.diagsJSON(), human: b.String()}
}

// ---- status ----

type statusData struct {
	State        string       `json:"state"`
	Baseline     baselineJSON `json:"baseline"`
	ChangedFiles []string     `json:"changed_files"`
	Changes      []changeJSON `json:"changes"`
	Skipped      []string     `json:"skipped"`
	Lint         lintCounts   `json:"lint"`
	Trailers     []string     `json:"trailers"`
	Next         nextJSON     `json:"next"`
}

func (s *session) statusCommand() *cobra.Command {
	var base string
	cmd := &cobra.Command{
		Use:   "status [--base REF]",
		Short: "the post-edit check: anchor diff against HEAD, lint, the trailer lines to paste, and what to do next",
		Long: "Compares the working tree with HEAD (or --base REF). When amending a commit that already changed\n" +
			"anchors, pass --base HEAD^ so the trailers cover the whole amended commit.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errTooMany("status [--base REF]")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runStatus(base)
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "revision to compare against (default HEAD; HEAD^ when amending)")
	return cmd
}

func (s *session) runStatus(base string) *outcome {
	v, o := s.newSide(false)
	if o != nil {
		return o
	}
	cmp, o := s.compare(v, compareOpts{ref: base})
	if o != nil {
		return o
	}
	lc := v.counts()
	data := statusData{
		Baseline: cmp.baselineObj(), ChangedFiles: []string{}, Changes: cmp.changes(), Skipped: cmp.skipped(),
		Lint: lc, Trailers: []string{},
	}
	if cmp.diff == nil {
		data.State = "no_baseline"
	} else {
		data.State = cmp.diff.State()
		data.Trailers = cmp.diff.Required().Lines()
		data.ChangedFiles = changedFiles(cmp)
	}
	blocked := lc.Error > 0 || len(data.Skipped) > 0
	data.Next = statusNext(data, blocked, s.unaccounted(v, cmp))

	var b strings.Builder
	fmt.Fprintf(&b, "state: %s", data.State)
	switch cmp.base.State {
	case baseline.StateUnavailable:
		fmt.Fprintf(&b, " (%s; lint still runs)", cmp.base.Reason)
	case baseline.StateUnborn:
		b.WriteString(" (no commits yet: every anchor is added)")
	default:
		fmt.Fprintf(&b, " (compared with %s)", data.Baseline.From)
	}
	b.WriteString("\n")
	if len(data.ChangedFiles) > 0 {
		b.WriteString("changed files: " + strings.Join(data.ChangedFiles, ", ") + "\n")
	}
	if len(data.Changes) > 0 {
		b.WriteString("anchors:\n")
		for _, l := range strings.Split(strings.TrimRight(renderChanges(data.Changes), "\n"), "\n") {
			b.WriteString("  " + l + "\n")
		}
	}
	b.WriteString(renderSkipped(data.Skipped))
	dj := v.diagsJSON()
	for _, d := range dj {
		if d.Severity != diag.Hint {
			b.WriteString(formatDiag(d))
		}
	}
	fmt.Fprintf(&b, "lint: %s, %s, %s\n", plural(lc.Error, "error"), plural(lc.Warning, "warning"), plural(lc.Hint, "hint"))
	if len(data.Trailers) > 0 {
		b.WriteString("trailers to add to the commit message:\n")
		for _, t := range data.Trailers {
			b.WriteString(t + "\n")
		}
	}
	b.WriteString("next: " + data.Next.Text + "\n")

	out := &outcome{data: data, diags: dj, human: b.String()}
	if blocked {
		out.exit = exitCheck
		out.code = "lint_errors"
		out.message = blockedMessage(lc.Error, data.Skipped)
	}
	return out
}

// blockedMessage is the error text for exit 4 from lint errors or skipped
// duplicates.
func blockedMessage(errors int, skipped []string) string {
	var parts []string
	if errors > 0 {
		parts = append(parts, plural(errors, "lint error"))
	}
	if len(skipped) > 0 {
		parts = append(parts, "names defined more than once ("+strings.Join(skipped, ", ")+") that block the diff")
	}
	return strings.Join(parts, " and ")
}

// unaccounted names the changed anchors no applied proposal explains. If the
// receipts cannot be read, that is every changed anchor.
func (s *session) unaccounted(v *view, cmp *comparison) []string {
	if cmp.diff == nil {
		return nil
	}
	evidence, err := s.store(v.proj.Root).Applied()
	if err != nil {
		lg.WithError(err).Warn("cannot read the applied proposals")
	}
	return workflow.Unaccounted(cmp.diff, cmp.oldSpec, v.spec, evidence)
}

// statusNext decides the next: line. names are the changed anchors that want
// a proposal.
func statusNext(d statusData, blocked bool, names []string) nextJSON {
	switch {
	case d.Lint.Error > 0:
		return nextJSON{Text: fmt.Sprintf("run `spec lint`, fix its %s, then run `spec status` again", plural(d.Lint.Error, "error")), Argv: []string{"spec", "lint"}}
	case len(d.Skipped) > 0:
		return nextJSON{Text: "give each name in `skipped` one definition (`spec lint` shows where), then run `spec status` again", Argv: []string{"spec", "lint"}}
	}
	warn := ""
	argv := []string{}
	if d.Lint.Warning > 0 {
		warn = fmt.Sprintf("; first run `spec lint` to read its %s", plural(d.Lint.Warning, "warning"))
		argv = []string{"spec", "lint"}
	}
	switch d.State {
	case "no_baseline":
		return nextJSON{Text: "there is no git baseline, so changes cannot be detected; commit once git is available" + warn, Argv: argv}
	case "anchors_changed":
		if len(names) == 0 {
			text := accountedText(d.Changes)
			if warn != "" {
				return nextJSON{Text: text + warn, Argv: argv}
			}
			return nextJSON{Text: text, Argv: []string{}}
		}
		list := strings.Join(names, " ")
		text := "agents: run `spec propose " + list + " --why WHY`, naming only your own edits, to file them for review; humans: run `spec propose --keep --why WHY` (or ^P in `spec ui`) to review the follow-on fixes, then commit with the trailer lines above"
		if warn != "" {
			return nextJSON{Text: text + warn, Argv: argv}
		}
		return nextJSON{Text: text, Argv: append(append([]string{"spec", "propose"}, names...), "--why", "WHY")}
	case "prose_only":
		if warn != "" {
			return nextJSON{Text: fmt.Sprintf("prose changes need no trailer; read the %s from `spec lint`", plural(d.Lint.Warning, "warning")), Argv: argv}
		}
		return nextJSON{Text: "prose changes need no trailer; nothing more to do", Argv: []string{}}
	}
	if warn != "" {
		return nextJSON{Text: fmt.Sprintf("nothing differs from the baseline; read the %s from `spec lint`", plural(d.Lint.Warning, "warning")), Argv: argv}
	}
	return nextJSON{Text: "nothing differs from the baseline; nothing to do", Argv: []string{}}
}

// accountedText is the next: line when every changed anchor is accounted
// for: renames need only their Spec-Rename lines, and the rest are applied
// proposals.
func accountedText(changes []changeJSON) string {
	renamed, applied := 0, 0
	for _, c := range changes {
		switch {
		case c.State == string(baseline.Renamed) && !c.BodyChanged && !c.KindChanged:
			renamed++
		case c.State != string(baseline.Retitled):
			applied++
		}
	}
	switch {
	case renamed > 0 && applied == 0:
		return "the changed anchors are renames; commit them with the Spec-Rename lines above"
	case renamed > 0:
		return "the renamed anchors need only their Spec-Rename lines and the other changed anchors are applied proposals; commit them with the trailer lines above"
	}
	return "the changed anchors are applied proposals; commit them with the trailer lines above"
}

// changedFiles lists the spec files whose bytes differ between the baseline
// and the new side, including added and deleted ones.
func changedFiles(c *comparison) []string {
	out := []string{}
	old := map[string][]byte{}
	if c.oldSpec != nil {
		for _, f := range c.oldSpec.Files {
			old[f.Path] = f.Source()
		}
	}
	seen := map[string]bool{}
	for _, f := range c.newView.spec.Files {
		seen[f.Path] = true
		if prev, ok := old[f.Path]; !ok || string(prev) != string(f.Source()) {
			out = append(out, f.Path)
		}
	}
	for p := range old {
		if !seen[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// ---- check ----

type checkData struct {
	Baseline    baselineJSON         `json:"baseline"`
	MessageFile *string              `json:"message_file"`
	Changes     []changeJSON         `json:"changes"`
	Skipped     []string             `json:"skipped"`
	Required    []string             `json:"required"`
	Present     []string             `json:"present"`
	Missing     []string             `json:"missing"`
	Unexpected  []string             `json:"unexpected"`
	Malformed   []baseline.Malformed `json:"malformed"`
}

func (s *session) checkCommand() *cobra.Command {
	var staged bool
	var msgFile, base string
	cmd := &cobra.Command{
		Use:   "check [--staged] [--message-file F] [--base REF]",
		Short: "verify that a commit message carries the Spec-Change/Spec-Rename trailers the spec changes require",
		Long: "The backstop behind the commit-msg hook (`spec explain integrations`). Compares the index against HEAD\n" +
			"(--staged, implied by --message-file; without either, the working tree) and exits 4, listing exactly\n" +
			"the missing trailer lines, when the message lacks one. Without --message-file there is no message,\n" +
			"so every required line is missing. On git commit --amend the hook cannot tell it is an amend: run it\n" +
			"with --base HEAD^ so the trailers must cover the whole amended commit.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errTooMany("check [--staged] [--message-file F] [--base REF]")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runCheck(staged, msgFile, base)
			return nil
		},
	}
	cmd.Flags().BoolVar(&staged, "staged", false, "compare the index with HEAD (the default when --message-file is given)")
	cmd.Flags().StringVar(&msgFile, "message-file", "", "commit message file to check (git passes it to commit-msg hooks)")
	cmd.Flags().StringVar(&base, "base", "", "revision to compare against (default HEAD; HEAD^ when amending)")
	return cmd
}

func (s *session) runCheck(staged bool, msgFile, base string) *outcome {
	var message string
	var msgPtr *string
	if msgFile != "" {
		path := msgFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.env.Cwd, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return usageErr("cannot read --message-file %s: %v", msgFile, err)
		}
		message = string(b)
		msgPtr = &msgFile
		staged = true
	}
	v, o := s.newSide(staged)
	if o != nil {
		return o
	}
	cmp, o := s.compare(v, compareOpts{ref: base, staged: staged})
	if o != nil {
		return o
	}
	if cmp.base.State == baseline.StateUnavailable {
		return noBaseline(cmp.base)
	}
	required := cmp.diff.Required()
	reqLines := required.Lines()
	res := baseline.Check(required, message)
	missing := res.MissingLines()
	if missing == nil {
		missing = []string{}
	}
	missingSet := toSet(missing)
	reqSet := toSet(reqLines)
	present := []string{}
	for _, l := range reqLines {
		if !missingSet[l] {
			present = append(present, l)
		}
	}
	unexpected := []string{}
	parsed, _ := baseline.ParseMessage(message)
	for _, l := range parsed.Lines() {
		if !reqSet[l] {
			unexpected = append(unexpected, l)
		}
	}
	malformed := res.Malformed
	if malformed == nil {
		malformed = []baseline.Malformed{}
	}
	data := checkData{
		Baseline: cmp.baselineObj(), MessageFile: msgPtr, Changes: cmp.changes(), Skipped: cmp.skipped(),
		Required: reqLines, Present: present, Missing: missing, Unexpected: unexpected, Malformed: malformed,
	}

	var b strings.Builder
	out := &outcome{data: data, diags: v.diagsJSON()}
	switch {
	case len(missing) > 0:
		b.WriteString("missing trailer lines (add them to the commit message, after a blank line):\n")
		for _, l := range missing {
			b.WriteString(l + "\n")
		}
		out.exit = exitCheck
		out.code = "missing_trailers"
		hint := "run `spec status` for the current lines (amending? see `spec explain integrations`)"
		if base != "" && base != "HEAD" {
			hint = "run `spec status --base " + base + "` for the current lines"
		}
		out.message = fmt.Sprintf("the commit message is missing %s; %s", plural(len(missing), "trailer line"), hint)
		out.stderr = "spec: " + out.message + "\n"
	case len(data.Skipped) > 0:
		out.exit = exitCheck
		out.code = "lint_errors"
		out.message = blockedMessage(0, data.Skipped)
		out.stderr = "spec: " + out.message + "\n"
	case len(reqLines) == 0:
		b.WriteString("ok: no Spec-Change or Spec-Rename trailers are required\n")
	default:
		fmt.Fprintf(&b, "ok: all %s present\n", plural(len(reqLines), "required trailer line"))
	}
	for _, m := range malformed {
		fmt.Fprintf(&b, "message line %d: %q is not a valid trailer: %s\n", m.Line, m.Text, m.Reason)
	}
	for _, l := range unexpected {
		fmt.Fprintf(&b, "note: %q matches no required line (a stale hash? run `spec status`)\n", l)
	}
	b.WriteString(renderSkipped(data.Skipped))
	out.human = b.String()
	return out
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
