// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package cli implements the spec command line: argument parsing, the
// command implementations, human and JSON output, and exit codes.
//
// Run is the entry point. It never calls os.Exit and takes all of its inputs
// (writers, working directory, environment) from Env, so tests drive it
// directly.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/spell"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/tui"
)

// Exit codes (plan section 5). When several apply the precedence is 5, then
// 1, then 4.
const (
	exitOK       = 0
	exitFail     = 1
	exitPending  = 2
	exitRejected = 3
	exitCheck    = 4
	exitLock     = 5
	exitStale    = 6
)

// schemaVersion is the value of the envelope's schema field.
const schemaVersion = "spec/v1"

// Env is everything Run needs from its surroundings.
type Env struct {
	// Stdin feeds `spec lsp`, the `spec ui` terminal, `spec review` and `--body-file -`.
	// Nil reads nothing.
	Stdin io.Reader
	// Stdout and Stderr receive the output. Nil writers discard.
	Stdout io.Writer
	Stderr io.Writer
	// Cwd is the working directory; "" means the process's.
	Cwd string
	// Getenv reads an environment variable; nil means os.Getenv.
	Getenv func(string) string
	// Git runs git for baselines. The zero value runs "git" from PATH with
	// the process environment.
	Git baseline.Loader

	// beforeCommit, when set, runs after `spec rename` has planned its edits
	// and before it writes them. Tests use it to edit a file at the moment
	// the rename's concurrent-edit check must notice.
	beforeCommit func()
	// tuneClient, when set, adjusts every inference client before use.
	// Tests use it to drop retry backoff.
	tuneClient func(*infer.Client)
	// spellDict, when set, replaces the system word list (tests).
	spellDict *spell.Dictionary
	// waitPoll, when set, replaces the 1 s poll of --wait.
	waitPoll time.Duration
	// Now, when set, replaces time.Now for proposal timestamps and receipt expiry.
	Now func() time.Time
	// runUI, when set, replaces running the terminal UI (tests).
	runUI func(*tui.App) error
	// modelChecker, when set, checks formal models in place of the
	// registered checker (tests).
	modelChecker formal.Checker
}

// outcome is what a command produces; Run renders it.
type outcome struct {
	exit int
	// data is the envelope's data (nil renders null).
	data any
	// code and message are the envelope's error; both are empty when exit
	// is 0.
	code    string
	message string
	// diags are the envelope's diagnostics (nil renders []).
	diags []diagJSON
	// human is the text for stdout without --json.
	human string
	// stderr is the text for stderr without --json.
	stderr string
}

// fail builds a failure outcome whose human form is "spec: MESSAGE" on stderr.
func fail(exit int, code, format string, args ...any) *outcome {
	msg := fmt.Sprintf(format, args...)
	return &outcome{exit: exit, code: code, message: msg, stderr: "spec: " + msg + "\n"}
}

// usageErr builds an exit 1 `usage` outcome.
func usageErr(format string, args ...any) *outcome {
	return fail(exitFail, "usage", format, args...)
}

// internalErr builds an exit 1 `internal` outcome for an unexpected error.
func internalErr(err error) *outcome {
	return fail(exitFail, "internal", "internal error: %v", err)
}

// session holds the state shared by the commands of one Run.
type session struct {
	env     Env
	project string
	json    bool
	// provider holds --provider and --engine for commands that call a model.
	provider providerFlags
	result   *outcome
	// envFiles caches the user config's env_file for this Run, so every
	// workflow the command builds reads it at most once.
	envFiles config.EnvFiles
	// maxVisits is --max-acquisition-visits (0: not given), and undoLimits
	// takes it back out of the checker registry when the run ends.
	maxVisits  maxVisitsFlag
	undoLimits func()
}

// Run executes the spec command line given by args (without the program
// name) and returns the exit code.
func Run(args []string, env Env) int {
	if env.Stdout == nil {
		env.Stdout = io.Discard
	}
	if env.Stderr == nil {
		env.Stderr = io.Discard
	}
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	if env.Stdin == nil {
		env.Stdin = strings.NewReader("")
	}
	if env.Cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			env.Cwd = wd
		}
	}

	s := &session{env: env}
	code, panicked := s.run(args)
	if panicked != nil {
		o := internalErr(fmt.Errorf("%v", panicked))
		s.render(o, wantsJSON(args))
		return o.exit
	}
	return code
}

func (s *session) run(args []string) (code int, panicked any) {
	defer func() {
		if r := recover(); r != nil {
			panicked = r
		}
	}()
	defer func() {
		if s.undoLimits != nil {
			s.undoLimits()
		}
	}()
	root := s.rootCommand()
	root.SetArgs(args)
	root.SetOut(s.env.Stdout)
	root.SetErr(s.env.Stderr)
	err := root.Execute()
	switch {
	case err != nil:
		// Commands never return errors: this is cobra rejecting the command
		// line.
		o := usageErr("%s", err.Error())
		o.stderr = "spec: " + err.Error() + "\nrun `spec explain` for the commands, or `spec COMMAND --help` for flags\n"
		s.render(o, s.json || wantsJSON(args))
		return o.exit, nil
	case s.result == nil:
		return exitOK, nil // help was printed
	}
	s.render(s.result, s.json)
	return s.result.exit, nil
}

// wantsJSON reports whether args ask for --json; used when flag parsing
// itself failed.
func wantsJSON(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--json" || a == "--json=true" {
			return true
		}
	}
	return false
}

// envelope is the --json output.
type envelope struct {
	Schema      string     `json:"schema"`
	OK          bool       `json:"ok"`
	Data        any        `json:"data"`
	Error       *errorJSON `json:"error"`
	Diagnostics []diagJSON `json:"diagnostics"`
}

type errorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// render writes o to stdout and stderr in the requested mode.
func (s *session) render(o *outcome, asJSON bool) {
	if !asJSON {
		// Proposal and model text reaches the terminal here: show control
		// characters as text. --json (below) carries the bytes as they are.
		_, _ = io.WriteString(s.env.Stdout, termtext.Clean(o.human))
		_, _ = io.WriteString(s.env.Stderr, termtext.Clean(o.stderr))
		return
	}
	env := envelope{Schema: schemaVersion, OK: o.exit == exitOK, Data: o.data, Diagnostics: o.diags}
	if env.Diagnostics == nil {
		env.Diagnostics = []diagJSON{}
	}
	if o.exit != exitOK {
		env.Error = &errorJSON{Code: o.code, Message: o.message}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		// Unreachable for our own types; keep the contract of one object.
		buf.Reset()
		fmt.Fprintf(&buf, `{"schema":%q,"ok":false,"data":null,"error":{"code":"internal","message":%q},"diagnostics":[]}`+"\n", schemaVersion, err.Error())
		o.exit = exitFail
	}
	_, _ = s.env.Stdout.Write(buf.Bytes())
}

// rootCommand builds the cobra tree.
func (s *session) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "spec",
		Short:         "check, explain and edit a project's *.spec.md files",
		Long:          "spec checks a project's specification: *.spec.md files with named invariants and definitions.\nStart with `spec explain`: it lists what to read.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&s.project, "project", "", "directory that holds spec.toml (default: search upward from the current directory)")
	root.PersistentFlags().BoolVar(&s.json, "json", false, "write one JSON envelope to stdout (see: spec explain json)")
	s.addLimitFlags(root)
	root.PersistentPreRun = func(*cobra.Command, []string) { s.undoLimits = s.applyLimits() }
	root.AddCommand(
		s.adoptCommand(),
		s.initCommand(),
		s.explainCommand(),
		s.lsCommand(),
		s.showCommand(),
		s.refsCommand(),
		s.lintCommand(),
		s.diffCommand(),
		s.statusCommand(),
		s.checkCommand(),
		s.renameCommand(),
		s.doctorCommand(),
		s.analyzeCommand(),
		s.changeCommand(),
		s.proposeCommand(),
		s.fixCommand(),
		s.fmtCommand(),
		s.proposalsCommand(),
		s.proposalCommand(),
		s.recheckCommand(),
		s.recoverCommand(),
		s.reviewCommand(),
		s.approveCommand(),
		s.rejectCommand(),
		s.lspCommand(),
		s.uiCommand(),
		s.replayCommand(),
	)
	return root
}

// needArgs returns a cobra Args validator requiring exactly n arguments,
// with a message that says what is missing.
func needArgs(n int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) < n:
			// `spec show $Frame.Timing` reaches us with no argument at all
			// when the shell expands $Frame.Timing to nothing.
			return fmt.Errorf("usage: spec %s (%s)", usage, shellHint)
		case len(args) > n:
			return fmt.Errorf("usage: spec %s", usage)
		}
		return nil
	}
}

// plural returns "1 error" or "2 errors".
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
