// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/recording"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/workflow"
)

// uiParseDelay debounces reparsing while typing in the UI.
const uiParseDelay = 30 * time.Millisecond

const uiUsage = "ui [FILE[:LINE] | NAME | §N]"

func (s *session) uiCommand() *cobra.Command {
	var noHash, keep, flash bool
	var record, replay string
	cmd := &cobra.Command{
		Use:   uiUsage,
		Short: "open the terminal editor (nano-style keys; ^G inside lists them)",
		Long: "Opens the project's specs in a terminal editor that knows the markup: highlighting, hover previews,\n" +
			"jumps to $Name and § targets, quick open and an outline. Opens at FILE (optionally :LINE), at an\n" +
			"anchor NAME (written without $), or at a section (§N, #N or N); by default at the first spec file.\n" +
			"FILE may be a formal model; models link to the spec both ways (`spec explain integrations`).\n" +
			"One `spec ui` runs per project (.spec/ui.lock); a second one exits 5.\n\n" +
			"--record DIR records the session's inputs into DIR (new or empty; suggested: .dev/recordings/NAME,\n" +
			"untracked): a snapshot of the project, every key, paste, resize and mouse event (by what it hit),\n" +
			"the spec files and proposals other processes changed, and every model reply in canned form\n" +
			"(canned.toml) with the full requests (requests.jsonl). A recording holds whole prompts and replies.\n\n" +
			"--replay DIR plays a recording back: it restores the recorded project into a temp directory, serves\n" +
			"the recorded model replies, and feeds in the recorded mouse events and other processes' file changes\n" +
			"after the same keys; the keys come from the terminal (`spec replay DIR` writes a VHS tape that types\n" +
			"them). What it cannot reproduce is printed when the UI exits and written to DIR/replay.log (empty\n" +
			"when nothing diverged). --keep keeps the temp project.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return errTooMany(uiUsage)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case replay != "" && (record != "" || len(args) > 0):
				s.result = usageErr("--replay takes no FILE and no --record: it opens where the recording did")
			case replay != "":
				s.result = s.runReplayUI(replay, keep, flash)
			case keep || flash:
				s.result = usageErr("--keep and --replay-flash go with --replay DIR")
			default:
				s.result = s.runUI(args, noHash, record)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noHash, "no-hash-convert", false, "do not turn #N into §N while typing")
	cmd.Flags().StringVar(&record, "record", "", "record the session's inputs and model replies into `DIR` (for a replay)")
	cmd.Flags().StringVar(&replay, "replay", "", "replay the recording in `DIR` (the keys come from the terminal; see spec replay)")
	cmd.Flags().BoolVar(&keep, "keep", false, "with --replay: keep the restored project and print where it is")
	cmd.Flags().BoolVar(&flash, "replay-flash", false, "with --replay: show divergences from the recording in the status line")
	return cmd
}

func (s *session) runUI(args []string, noHash bool, record string) *outcome {
	if s.json {
		return usageErr("spec ui is interactive; --json does not apply")
	}
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	var target tui.Target
	if len(args) == 1 {
		t, o := s.uiTarget(root, args[0])
		if o != nil {
			return o
		}
		target = t
	}
	run := s.env.runUI
	if run == nil {
		if o := needTerminal(s.env.Stdin, s.env.Stdout); o != nil {
			return o
		}
		run = func(a *tui.App) error { return tui.Run(a, s.env.Stdin, s.env.Stdout) }
	}

	lock, err := project.AcquireUILock(root, "spec ui")
	if err != nil {
		var held *project.LockHeldError
		if errors.As(err, &held) {
			out := fail(exitLock, "lock_held", "another spec ui is open on this project (%s): %s; switch to it, or stop it first", project.UILockFile, held.Record.String())
			out.data = map[string]any{"lock": lockJSON{
				PID: held.Record.PID, Host: held.Record.Host,
				Started: held.Record.Started.UTC().Format("2006-01-02T15:04:05Z"), Command: held.Record.Command,
			}}
			return out
		}
		return fail(exitFail, "write_failed", "cannot take %s: %v", project.UILockFile, err)
	}
	defer func() { _ = lock.Release() }()

	wf := s.workflow()
	var rs *tui.RecordSession
	if record != "" {
		var o *outcome
		rs, o = s.startRecording(root, record, args, !noHash, wf)
		if o != nil {
			return o
		}
	}
	app, err := tui.New(tui.Options{
		Root: root, Target: target, HashToSection: !noHash,
		Watch: true, WatchProposals: true, Baseline: true, ParseDelay: uiParseDelay,
		PlainUnderline: plainUnderline(s.env.Getenv, wf),
		Workflow:       wf,
		Record:         recorderOf(rs),
	})
	if err != nil {
		if rs != nil {
			_, _ = rs.Close()
		}
		var te *tui.TargetError
		if errors.As(err, &te) {
			return fail(exitFail, "not_found", "%s", te.Msg)
		}
		return s.projectErr(err)
	}
	runErr := run(app)
	if rs != nil {
		m, err := rs.Close()
		if err != nil {
			fmt.Fprintf(s.env.Stderr, "spec ui: the recording in %s is incomplete: %v\n", rs.Dir, err)
		} else {
			fmt.Fprintf(s.env.Stderr, "recorded to %s (%d events, %d model calls)\n", rs.Dir, m.Events, m.ModelCalls)
		}
	}
	if runErr != nil {
		return internalErr(runErr)
	}
	return &outcome{}
}

// recorderOf is the session's recorder (nil without one).
func recorderOf(rs *tui.RecordSession) *tui.Recorder {
	if rs == nil {
		return nil
	}
	return rs.Recorder
}

// startRecording opens the recording of `spec ui --record DIR` (relative
// to the working directory). It warns on stderr, before the UI starts,
// when DIR is inside the project and git does not ignore it, and when the
// snapshot is large.
func (s *session) startRecording(root, dir string, args []string, hash bool, wf *workflow.Workflow) (*tui.RecordSession, *outcome) {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.env.Cwd, dir)
	}
	target := ""
	if len(args) == 1 {
		target = args[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rs, err := tui.OpenRecording(ctx, tui.RecordConfig{
		Dir: dir, Root: root, Workflow: wf, Git: s.env.Git, Target: target, HashToSection: hash,
	})
	if err != nil {
		return nil, fail(exitFail, "write_failed", "cannot record into %s: %v", dir, err)
	}
	if rel, err := filepath.Rel(root, rs.Dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rs.Snapshot.Git {
		if _, err := s.env.Git.Run(ctx, root, nil, "check-ignore", "-q", filepath.ToSlash(filepath.Join(rel, recording.MetaFile))); err != nil {
			fmt.Fprintf(s.env.Stderr, "spec ui: warning: %s is inside the project and not ignored by git; a recording holds whole prompts and replies (add .dev/recordings/ to .gitignore)\n", rel)
		}
	}
	if rs.Snapshot.Bytes > recordWarnBytes {
		fmt.Fprintf(s.env.Stderr, "spec ui: warning: the snapshot of the project is %d MB\n", rs.Snapshot.Bytes>>20)
	}
	return rs, nil
}

// recordWarnBytes is the snapshot size above which --record warns.
const recordWarnBytes = 50 << 20

// needTerminal refuses to start the UI without a terminal on both ends.
func needTerminal(in, out any) *outcome {
	isTTY := func(v any) bool {
		f, ok := v.(*os.File)
		return ok && term.IsTerminal(f.Fd())
	}
	if !isTTY(in) || !isTTY(out) {
		return fail(exitFail, "usage", "spec ui needs a terminal on stdin and stdout")
	}
	return nil
}

// uiTarget parses the place to open: an existing file (relative to the
// working directory or the project root) with an optional :LINE, else an
// anchor name or a section.
func (s *session) uiTarget(root, arg string) (tui.Target, *outcome) {
	return s.uiTargetFrom([]string{s.env.Cwd, root}, root, arg)
}

// uiTargetFrom is uiTarget with a file looked for under bases, in order.
func (s *session) uiTargetFrom(bases []string, root, arg string) (tui.Target, *outcome) {
	path, line := arg, 0
	if i := strings.LastIndexByte(arg, ':'); i > 0 {
		if n, err := strconv.Atoi(arg[i+1:]); err == nil && n > 0 {
			path, line = arg[:i], n
		}
	}
	for _, base := range bases {
		abs := path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(base, path)
		}
		st, err := os.Stat(abs)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return tui.Target{}, usageErr("%s is outside the project at %s", arg, root)
		}
		return tui.Target{File: filepath.ToSlash(rel), Line: line}, nil
	}
	if strings.HasSuffix(path, ".md") {
		return tui.Target{}, fail(exitFail, "not_found", "no file %s", path)
	}
	t, o := parseTarget(arg)
	if o != nil {
		return tui.Target{}, o
	}
	if t.kind == targetSection {
		if !sectionNumberOK(t.number) {
			return tui.Target{}, usageErr("%q is not a section number (N, N.N, N.Na)", t.display())
		}
		return tui.Target{Stem: t.stem, Number: t.number}, nil
	}
	return tui.Target{Name: t.name}, nil
}

// plainUnderlineTerminal reports whether the terminal needs plain
// underlines: Terminal.app reads an underline colour (SGR 58) as blink.
func plainUnderlineTerminal(getenv func(string) string) bool {
	return getenv("TERM_PROGRAM") == "Apple_Terminal"
}

// plainUnderline decides whether the UI underlines without colour: the
// user config's underline_color ("on" colours them, "off" never does),
// else "auto": not in Terminal.app. A config that cannot be read counts
// as "auto" (spec doctor reports it).
func plainUnderline(getenv func(string) string, wf *workflow.Workflow) bool {
	if path := wf.UserConfigPath(); path != "" {
		if l, err := config.Load(path); err == nil {
			switch l.Config.UnderlineColor {
			case "on":
				return false
			case "off":
				return true
			}
		}
	}
	return plainUnderlineTerminal(getenv)
}

// runReplayUI runs `spec ui --replay DIR`: the UI on the recording's
// project restored into a temp directory, with the recording fed back in.
// The warnings (what the replay did not reproduce) go to stderr when the
// UI exits.
func (s *session) runReplayUI(dir string, keep, flash bool) *outcome {
	if s.json {
		return usageErr("spec ui is interactive; --json does not apply")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.env.Cwd, dir)
	}
	run := s.env.runUI
	if run == nil {
		if o := needTerminal(s.env.Stdin, s.env.Stdout); o != nil {
			return o
		}
		run = func(a *tui.App) error { return tui.Run(a, s.env.Stdin, s.env.Stdout) }
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	rs, err := tui.OpenReplay(ctx, tui.ReplayConfig{Dir: dir, Flash: flash, Getenv: s.env.Getenv})
	cancel()
	if err != nil {
		return fail(exitFail, "not_found", "cannot replay %s: %v", dir, err)
	}
	root := rs.Prepared.Root
	defer func() {
		if err := rs.Close(keep); err != nil {
			fmt.Fprintf(s.env.Stderr, "spec ui --replay: cannot remove %s: %v\n", rs.Prepared.Tmp, err)
		}
		if keep {
			fmt.Fprintf(s.env.Stderr, "spec ui --replay: the replayed project is in %s\n", root)
		}
	}()
	lock, err := project.AcquireUILock(root, "spec ui --replay")
	if err != nil {
		return fail(exitFail, "write_failed", "cannot take %s: %v", project.UILockFile, err)
	}
	defer func() { _ = lock.Release() }()

	opts := rs.Options
	opts.ParseDelay = uiParseDelay
	opts.PlainUnderline = plainUnderline(s.env.Getenv, rs.Workflow)
	if t := rs.Prepared.Rec.Meta.Target; t != "" {
		target, o := s.uiTargetFrom([]string{root}, root, t)
		if o != nil {
			rs.Replay.Warn(fmt.Sprintf("the recording opened at %q, which the replay cannot find (%s); opening the first spec file", t, o.message))
		} else {
			opts.Target = target
		}
	}
	app, err := tui.New(opts)
	if err != nil {
		var te *tui.TargetError
		if errors.As(err, &te) {
			return fail(exitFail, "not_found", "%s", te.Msg)
		}
		return s.projectErr(err)
	}
	runErr := run(app)
	warnings := rs.Warnings()
	tui.PrintWarnings(s.env.Stderr, "spec ui --replay: ", warnings)
	var log strings.Builder
	tui.PrintWarnings(&log, "", warnings)
	// Atomically: a recording may hold a link at either name, not to be followed.
	if err := recording.WriteFileAtomic(filepath.Join(dir, recording.ReplayLog), []byte(log.String())); err != nil {
		fmt.Fprintf(s.env.Stderr, "spec ui --replay: cannot write %s: %v\n", recording.ReplayLog, err)
	}
	if final := rs.Replay.Final(); final != "" {
		if err := recording.WriteFileAtomic(filepath.Join(dir, recording.ReplayFinal), []byte(ansi.Strip(final))); err != nil {
			fmt.Fprintf(s.env.Stderr, "spec ui --replay: cannot write %s: %v\n", recording.ReplayFinal, err)
		}
	}
	if runErr != nil {
		return internalErr(runErr)
	}
	return &outcome{}
}
