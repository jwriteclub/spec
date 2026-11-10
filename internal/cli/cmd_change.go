// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

// fileFlags are the flags shared by change and propose.
type fileFlags struct {
	why         string
	by          string
	source      string
	wait        time.Duration
	autoApprove bool
	full        bool
}

func (s *session) addFileFlags(cmd *cobra.Command, f *fileFlags) {
	cmd.Flags().StringVar(&f.why, "why", "", "why the change is needed (required; shown to the reviewer and used as the commit body)")
	cmd.Flags().StringVar(&f.by, "by", "", "optional author name shown to the reviewer")
	cmd.Flags().StringVar(&f.source, "source", "agent", "who wrote the new text: agent or human; separate from --by")
	cmd.Flags().DurationVar(&f.wait, "wait", 0, "wait for a decision (e.g. 30m): exit 0 applied, 3 rejected, 6 stale (still pending), 2 timeout")
	cmd.Flags().BoolVar(&f.autoApprove, "auto-approve", false, "HUMANS ONLY: apply at once when the check finds nothing breaking and asks nothing")
	cmd.Flags().BoolVar(&f.full, "full", false, "send every spec file whole to the model, ignoring max_context_tokens")
	s.addProviderFlags(cmd)
}

func (f fileFlags) author() string {
	if strings.TrimSpace(f.by) == "" {
		return proposal.AuthorHuman
	}
	return strings.TrimSpace(f.by)
}

func (f fileFlags) sourceAuthor() string {
	if f.source == "human" {
		return f.author()
	}
	return strings.TrimSpace(f.by)
}

// ---- change ----

type changeFlags struct {
	fileFlags
	bodyFile string
	remove   bool
	def      bool
	inv      bool
	add      bool
	after    string
	section  string
	tla      string
}

func (s *session) changeCommand() *cobra.Command {
	var f changeFlags
	cmd := &cobra.Command{
		Use:   "change (NAME (--body-file F | --remove | --def | --inv | --add --after NAME|--section N --body-file F) | --tla PATH --body-file F) --why W",
		Short: "propose adding, changing or removing an anchor, or editing a TLA+ model; files a proposal for review without touching the files",
		Long: "Files a proposal, then runs its contradiction check (`spec explain workflow`). The body\n" +
			"file holds the anchor's full text, label included (`-` reads stdin); for a paragraph-form anchor the\n" +
			"label may be left out. With --tla PATH the body file holds the TLA+ model's whole new text instead: the\n" +
			"edit is filed for review as one proposal, applied as a whole, with no contradiction check.\n" +
			"Exit 2 with the proposal id, or with --wait: 0 applied, 3 rejected, 6 stale (still pending), 2 timeout.",
		Args: func(cmd *cobra.Command, args []string) error {
			if f.tla != "" {
				if len(args) > 0 {
					return fmt.Errorf("usage: spec change --tla PATH --body-file F --why W (no NAME: the model's whole text is the change)")
				}
				return nil
			}
			return needArgs(1, "change NAME --body-file F --why W")(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.tla != "" {
				s.result = s.runChangeModel(f.tla, f)
				return nil
			}
			s.result = s.runChange(args[0], f)
			return nil
		},
	}
	cmd.Flags().StringVar(&f.bodyFile, "body-file", "", "file with the anchor's new text, or with --tla PATH the model's (- for stdin)")
	cmd.Flags().BoolVar(&f.remove, "remove", false, "remove the anchor")
	cmd.Flags().BoolVar(&f.def, "def", false, "make it a definition")
	cmd.Flags().BoolVar(&f.inv, "inv", false, "make it an invariant")
	cmd.Flags().BoolVar(&f.add, "add", false, "add a new anchor (with --body-file and --after or --section)")
	cmd.Flags().StringVar(&f.after, "after", "", "with --add: place it after this anchor")
	cmd.Flags().StringVar(&f.section, "section", "", "with --add: place it at the end of this section (5.2 or hls:5.2)")
	cmd.Flags().StringVar(&f.tla, "tla", "", "a TLA+ model's path (.tla, a formal file): propose an edit of it, its whole new text in --body-file")
	s.addFileFlags(cmd, &f.fileFlags)
	return cmd
}

func (s *session) runChange(arg string, f changeFlags) *outcome {
	if f.source != "" && f.source != "agent" && f.source != "human" {
		return usageErr("--source must be agent or human")
	}
	if e := workflow.ModelCommandHint("change", arg); e != nil {
		return fromWF(e)
	}
	name, o := bareName(arg)
	if o != nil {
		return o
	}
	if strings.TrimSpace(f.why) == "" {
		return usageErr("--why is required: say why the change is needed")
	}
	switch {
	case f.def && f.inv:
		return usageErr("--def and --inv exclude each other")
	case f.remove && (f.bodyFile != "" || f.def || f.inv || f.add):
		return usageErr("--remove takes no --body-file, --def, --inv or --add")
	case f.add && f.bodyFile == "":
		return usageErr("--add needs --body-file with the new anchor's text")
	case f.add && (f.after == "") == (f.section == ""):
		return usageErr("--add needs exactly one of --after NAME and --section N")
	case !f.add && (f.after != "" || f.section != ""):
		return usageErr("--after and --section only go with --add")
	case !f.remove && !f.add && f.bodyFile == "" && !f.def && !f.inv:
		return usageErr("say what changes: --body-file F, --remove, --def, --inv, or --add")
	}
	if f.wait < 0 {
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
	loaded, err := proj.Load(nil)
	if err != nil {
		return internalErr(err)
	}
	spec := loaded.Spec

	var body string
	if f.bodyFile != "" {
		if body, o = s.readBody(f.bodyFile); o != nil {
			return o
		}
	}
	ch := &specctx.Change{Name: name, Why: strings.TrimSpace(f.why)}
	pr := &proposal.Primary{N: 1, Name: "$" + name}
	protected := map[string][]pos.Range{}
	as := spec.Lookup(name)

	if f.add {
		if !mdspec.ValidName(name) {
			return fail(exitFail, "bad_name", "%q is not a valid name (`spec explain markup`)", name)
		}
		if len(as) > 0 {
			return fail(exitFail, "name_exists", "$%s is already defined at %s; change it instead of adding it", name, locString(as[0].File, lineOfAnchor(spec, as[0]), 0))
		}
		pl, where, o := s.placementFor(spec, f.after, f.section)
		if o != nil {
			return o
		}
		text, kind, o := prepareBody(name, body, nil, nil, wantKind(f.def, f.inv), crlfFile(spec, pl.File))
		if o != nil {
			return o
		}
		ch.Action, ch.File, ch.NewKind, ch.NewText, ch.Where = specctx.ActionAdd, pl.File, kind, text, where
		pr.Placement = &pl
	} else {
		switch len(as) {
		case 0:
			return fail(exitFail, "not_found", "$%s is not defined; to add it use --add", name)
		case 1:
		default:
			o := fail(exitFail, "ambiguous", "$%s is defined %d times; fix that first (spec lint)", name, len(as))
			cands := []string{}
			for _, a := range as {
				cands = append(cands, locString(a.File, lineOfAnchor(spec, a), 0))
			}
			o.data = map[string]any{"candidates": cands}
			return o
		}
		a := as[0]
		file := spec.File(a.File)
		ch.File, ch.OldKind, ch.OldText = a.File, a.Kind, file.Text(a.BodyRange)
		protected[a.File] = []pos.Range{a.BodyRange}
		switch {
		case f.remove:
			ch.Action = specctx.ActionRemove
		case f.bodyFile != "":
			text, kind, o := prepareBody(name, body, file, a, wantKind(f.def, f.inv), crlfFile(spec, a.File))
			if o != nil {
				return o
			}
			ch.Action, ch.NewKind, ch.NewText = specctx.ActionChange, kind, text
		default:
			to := wantKind(f.def, f.inv)
			if a.Kind == to {
				return usageErr("$%s is already %s", name, kindWord(to))
			}
			text, err := proposal.KindToggle(file, a, to)
			if err != nil {
				return fail(exitFail, "bad_change", "%v", err)
			}
			ch.Action, ch.NewKind, ch.NewText = specctx.ActionKind, to, text
		}
		if ch.Action != specctx.ActionRemove && ch.NewText == ch.OldText {
			return usageErr("the new text of $%s is identical to its current text; nothing to propose", name)
		}
	}
	pr.Action, pr.File, pr.OldKind, pr.NewKind = ch.Action, ch.File, string(ch.OldKind), string(ch.NewKind)
	pr.OldText, pr.NewText = ch.OldText, ch.NewText

	w := s.workflow()
	if f.source == "agent" {
		p, e := w.PublishChange(context.Background(), proj, spec, pr, ch.Why, f.sourceAuthor(), f.full)
		if p == nil {
			return fromWF(e)
		}
		if e != nil && p.Status == proposal.StatusPending && p.StalePrimary == nil && (p.CheckState == "queued" || p.CheckState == "running") {
			return filedFailure([]*proposal.Proposal{p}, nil, fromWF(e))
		}
		return withSpell(s.fileProposals(root, []*proposal.Proposal{p}, nil, f.fileFlags, ""), s.spellProposals(proj, spec, []*proposal.Proposal{p}))
	}
	ck, e := w.OpenChecker(context.Background(), f.full)
	if e != nil {
		return fromWF(e)
	}
	p, e := w.CheckChange(ck, spec, ch, pr, protected, f.author())
	if e != nil {
		return fromWF(e)
	}
	p.SourceKind = f.source
	return withSpell(s.fileProposals(root, []*proposal.Proposal{p}, nil, f.fileFlags, ""), s.spellProposals(proj, spec, []*proposal.Proposal{p}))
}

// fileProposals stores ps (those without an id yet) and settles them: auto-approved ones are applied
// at once (they are created first, so a receipt exists); the rest wait for
// review.
func (s *session) fileProposals(root string, ps []*proposal.Proposal, skipped []string, f fileFlags, human string) *outcome {
	st := s.store(root)
	if err := st.Prune(); err != nil {
		lg.WithError(err).Warn("cannot prune receipts")
	}
	var b strings.Builder
	b.WriteString(human)
	for i, p := range ps {
		if p.ID == "" { // propose files its proposals before restoring
			if err := st.Create(p); err != nil {
				return fail(exitFail, "write_failed", "cannot write %s: %v", proposal.Dir, err)
			}
		}
		if f.autoApprove && p.Status == proposal.StatusPending && p.StalePrimary == nil {
			// Every successful application changes the context authorizing the
			// remaining candidates. Recheck once, then evaluate fresh findings.
			loaded, err := project.Load(root, nil)
			if err != nil {
				return filedFailure(ps, skipped, internalErr(err))
			}
			if workflow.CheckSnapshot(p, loaded.Spec) != nil {
				fresh, e := s.workflow().RefreshForAutoApprove(context.Background(), loaded.Project, p.ID, f.full)
				if fresh != nil {
					p, ps[i] = fresh, fresh
				}
				if e != nil {
					fmt.Fprintf(&b, "not auto-approved: %s: %s\n", p.ID, e.Message)
					continue
				}
			}
			if !workflow.Eligible(p) {
				fmt.Fprintf(&b, "not auto-approved: %s has an unfinished/failed required check, breaking findings or questions\n", p.ID)
				continue
			}
			res, e := s.workflow().Apply(root, p.ID, nil, f.author()+" (auto-approve)", false)
			if e != nil {
				return filedFailure(ps, skipped, fromWF(e))
			}
			ps[i] = res.Proposal
		}
	}
	return s.settle(root, ps, skipped, f.wait, f.autoApprove, b.String())
}

// readBody reads --body-file (- is stdin).
func (s *session) readBody(path string) (string, *outcome) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(io.LimitReader(s.env.Stdin, 4<<20))
	} else {
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.env.Cwd, path)
		}
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", usageErr("cannot read --body-file: %v", err)
	}
	return string(data), nil
}

// prepareBody turns the body file's text into the anchor's exact new text:
// line endings follow the target file, surrounding blank space is trimmed,
// a missing label (paragraph form only) is taken from the current anchor,
// and --def/--inv switch the label. The text must define exactly name and
// nothing else.
func prepareBody(name, raw string, file *mdspec.File, cur *mdspec.Anchor, want mdspec.Kind, crlf bool) (string, mdspec.Kind, *outcome) {
	text := strings.TrimPrefix(raw, utf8BOM)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimRight(strings.TrimLeft(text, "\n"), " \t\n")
	if strings.TrimSpace(text) == "" {
		return "", "", usageErr("the body file is empty")
	}
	parsed := mdspec.ParseFile("body.spec.md", []byte(text))
	if len(parsed.Anchors) == 0 && cur != nil && cur.Form == mdspec.FormParagraph {
		text = file.Text(cur.LabelRange) + " " + strings.TrimLeft(text, " \t")
		text = strings.ReplaceAll(text, "\r\n", "\n")
		parsed = mdspec.ParseFile("body.spec.md", []byte(text))
	}
	if o := checkBody(name, text, parsed); o != nil {
		return "", "", o
	}
	a := parsed.Anchors[0]
	kind := a.Kind
	if want != "" && want != kind {
		t, err := proposal.KindToggle(parsed, a, want)
		if err != nil {
			return "", "", fail(exitFail, "bad_change", "%v", err)
		}
		text, kind = t, want
	}
	if crlf {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text, kind, nil
}

// checkBody requires that text is exactly one anchor named name.
func checkBody(name, text string, f *mdspec.File) *outcome {
	for _, d := range f.Diagnostics {
		if d.Severity == diag.Error {
			return fail(exitFail, "bad_change", "the new text does not parse: %s", d.Message)
		}
	}
	switch {
	case len(f.Anchors) == 0:
		return fail(exitFail, "bad_change", "the new text has no label for $%s; start it with **$%s (title).** or **Def $%s (title).**", name, name, name)
	case len(f.Anchors) > 1:
		return fail(exitFail, "bad_change", "the new text defines %d anchors; give one anchor per change", len(f.Anchors))
	case f.Anchors[0].Name != name:
		return fail(exitFail, "bad_change", "the new text defines $%s, not $%s; to rename use `spec rename`", f.Anchors[0].Name, name)
	}
	a := f.Anchors[0]
	if a.BodyRange.Start != 0 || a.BodyRange.End != len(text) {
		return fail(exitFail, "bad_change", "the body file holds more than the anchor $%s: text before its label or after its body (a paragraph-form body ends at the first blank line not followed by a list or quote)", name)
	}
	return nil
}

// utf8BOM is the UTF-8 byte order mark, as bytes.
const utf8BOM = "\xef\xbb\xbf"

func wantKind(def, inv bool) mdspec.Kind {
	switch {
	case def:
		return mdspec.KindDefinition
	case inv:
		return mdspec.KindInvariant
	}
	return ""
}

func kindWord(k mdspec.Kind) string {
	if k == mdspec.KindDefinition {
		return "a definition"
	}
	return "an invariant"
}

func crlfFile(spec *mdspec.Spec, path string) bool {
	f := spec.File(path)
	return f != nil && strings.Contains(string(f.Source()), "\r\n")
}

func lineOfAnchor(spec *mdspec.Spec, a *mdspec.Anchor) int {
	return lineIn(spec, a)
}

// placementFor resolves --after NAME or --section N for --add.
func (s *session) placementFor(spec *mdspec.Spec, after, section string) (proposal.Placement, string, *outcome) {
	if after != "" {
		n, o := bareName(after)
		if o != nil {
			return proposal.Placement{}, "", o
		}
		as := spec.Lookup(n)
		switch len(as) {
		case 0:
			return proposal.Placement{}, "", fail(exitFail, "not_found", "--after: $%s is not defined", n)
		case 1:
		default:
			return proposal.Placement{}, "", fail(exitFail, "ambiguous", "--after: $%s is defined %d times", n, len(as))
		}
		return proposal.Placement{File: as[0].File, After: "$" + n, Sep: sepFor(spec, as[0].File)}, "after $" + n, nil
	}
	t, o := parseTarget(section)
	if o != nil {
		return proposal.Placement{}, "", o
	}
	if t.kind != targetSection {
		return proposal.Placement{}, "", usageErr("--section %q is not a section number (5.2, hls:5.2)", section)
	}
	var cands []*mdspec.Section
	for _, sec := range spec.FindSections(t.number) {
		if t.stem == "" || mdspec.StemOf(sec.File) == t.stem {
			cands = append(cands, sec)
		}
	}
	switch len(cands) {
	case 0:
		return proposal.Placement{}, "", fail(exitFail, "not_found", "--section: no section %s", t.display())
	case 1:
	default:
		qs := []string{}
		for _, c := range cands {
			qs = append(qs, c.Qualified())
		}
		o := fail(exitFail, "ambiguous", "--section %s matches %d sections; qualify it (%s)", t.display(), len(cands), strings.Join(qs, ", "))
		o.data = map[string]any{"candidates": qs}
		return proposal.Placement{}, "", o
	}
	sec := cands[0]
	return proposal.Placement{File: sec.File, Section: sec.Number, Sep: sepFor(spec, sec.File)}, "at the end of " + sec.Qualified(), nil
}

func sepFor(spec *mdspec.Spec, path string) string {
	if crlfFile(spec, path) {
		return "\r\n\r\n"
	}
	return "\n\n"
}

// Infrastructure errors must still identify every published proposal.
func filedFailure(ps []*proposal.Proposal, skipped []string, out *outcome) *outcome {
	if skipped == nil {
		skipped = []string{}
	}
	next := nextJSON{Text: "inspect the published proposals with spec proposals", Argv: []string{"spec", "proposals"}}
	if out.code == "recovery_required" {
		next = nextJSON{Text: "ask the user to inspect interruption evidence, HEAD, worktree and index with spec recover before continuing", Argv: []string{"spec", "recover"}}
	}
	data := &filedData{Proposals: ps, Skipped: skipped, Warnings: []string{}, Next: next}
	if prior, ok := out.data.(map[string]any); ok {
		if lock, ok := prior["lock"].(lockJSON); ok {
			data.Lock = &lock
		}
	}
	out.data = data
	for _, p := range ps {
		if p.ID != "" {
			out.human += "published proposal " + p.ID + "\n"
		}
	}
	return out
}
