// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"io"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/tenta-browser/polychromatic"

	"github.com/jwriteclub/spec/internal/lsp"
	"github.com/jwriteclub/spec/internal/project"
)

// lspLogEnv names the environment variable holding the language server's
// log level (logrus names: debug, info, warn, error); logs go to stderr.
const lspLogEnv = "SPEC_LSP_LOG"

func (s *session) lspCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "lsp",
		Short: "serve the Language Server Protocol on stdin and stdout, for editors",
		Long: "Runs the language server over stdio: diagnostics, hover, definition, references, rename,\n" +
			"completion, semantic tokens and symbols for *.spec.md files, and the spell check's cached flags\n" +
			"(the spec.spellCheck command runs its models). Editors start it; see\n" +
			"`spec explain integrations`. Logs go to stderr at the level named by " + lspLogEnv + ".",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errTooMany("lsp")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runLSP()
			return nil
		},
	}
}

func (s *session) runLSP() *outcome {
	if s.json {
		return usageErr("spec lsp speaks the Language Server Protocol on stdout and has no --json output")
	}
	stdin := s.env.Stdin
	if stdin == nil {
		stdin = eofReader{}
	}
	polychromatic.SetOutput(s.env.Stderr)
	if lv, err := logrus.ParseLevel(s.env.Getenv(lspLogEnv)); err == nil {
		polychromatic.SetLogLevel(lv)
	}
	w := s.workflow()
	spellCheck := func(ctx context.Context, root string) error {
		proj, err := project.Open(root)
		if err != nil {
			return err
		}
		loaded, err := proj.Load(nil)
		if err != nil {
			return err
		}
		rep := w.Spell(ctx, proj, loaded.Spec, nil)
		switch {
		case rep.Error != "":
			return errors.New(rep.Error)
		case rep.Models == "":
			return errors.New("the model pass is off: the user config needs a [gate] and a [triage] provider (`spec explain spell`)")
		}
		return nil
	}
	srv := lsp.New(stdin, s.env.Stdout, lsp.Options{Git: s.env.Git, Spell: spellCheck})
	// The protocol asks for exit code 1 after an exit without shutdown
	// (lsp.ErrExitWithoutShutdown), as for any other failure.
	if err := srv.Run(context.Background()); err != nil {
		return &outcome{exit: exitFail, code: "lsp", message: err.Error(), stderr: "spec: lsp: " + err.Error() + "\n"}
	}
	return &outcome{}
}

// eofReader is an empty input.
type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }
