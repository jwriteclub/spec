// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/workflow"
)

type fixFlags struct {
	fileFlags
	code string
}

func (s *session) fixCommand() *cobra.Command {
	var f fixFlags
	cmd := &cobra.Command{
		Use:   "fix FILE:LINE[:COL] [--code CODE] [--why W]",
		Short: "propose the fix of a lint finding (as `spec lint` prints its place) for review",
		Long: "Files a proposal that fixes the lint finding at FILE:LINE[:COL]: the most severe one there, or the\n" +
			"one --code names. Some rules are fixed without a model (backticks around a stray $ that closes\n" +
			"GitHub math, a byte order mark, #5.2 written for §5.2, a misspelled name with one close match);\n" +
			"the others ask the model with the rule's own instruction. Findings about the project rather than a\n" +
			"spec file's text say what to do by hand (exit 1 not_fixable). --why is the commit message (default:\n" +
			"\"Fix CODE: ...\"). Exit 2 with the proposal id, or with --wait: 0 applied, 3 rejected, 6 stale\n" +
			"(still pending), 2 timeout.",
		Args: needArgs(1, "fix FILE:LINE[:COL]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runFix(args[0], f)
			return nil
		},
	}
	cmd.Flags().StringVar(&f.code, "code", "", "the lint rule to fix when several findings are at the place (spec lint shows the codes)")
	s.addFileFlags(cmd, &f.fileFlags)
	cmd.Flags().Lookup("why").Usage = "the commit message (default: \"Fix CODE: what the fix does\"); shown to the reviewer"
	return cmd
}

func (s *session) runFix(arg string, f fixFlags) *outcome {
	if f.wait < 0 {
		return usageErr("--wait must not be negative")
	}
	if f.code != "" && !lint.Known(f.code) {
		return usageErr("--code %q is not a lint rule (known rules: %s)", f.code, strings.Join(lint.KnownCodes(), ", "))
	}
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	path, line, col, o := s.fixPlace(root, arg)
	if o != nil {
		return o
	}
	proj, o := s.openRoot(root)
	if o != nil {
		return o
	}
	res, e := s.workflow().Fix(context.Background(), proj, workflow.FixRequest{
		File: path, Line: line, Col: col, Code: f.code, Why: f.why, Author: f.author(), Full: f.full,
	})
	if e != nil {
		return fromWF(e)
	}
	human := "fixing " + res.Diagnostic.Code + " (" + string(res.Class) + ")\n"
	return s.fileProposals(root, []*proposal.Proposal{res.Proposal}, nil, f.fileFlags, human)
}

// fixPlace parses FILE:LINE[:COL]: FILE relative to the working directory
// or the project root, LINE and COL 1-based (COL in bytes, as `spec lint`
// prints it).
func (s *session) fixPlace(root, arg string) (string, int, int, *outcome) {
	bad := usageErr("%q: give the place as FILE:LINE or FILE:LINE:COL, as `spec lint` prints it", arg)
	parts := strings.Split(arg, ":")
	if len(parts) < 2 {
		return "", 0, 0, bad
	}
	nums := []int{}
	for len(parts) > 1 && len(nums) < 2 {
		n, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil {
			break
		}
		if n < 1 {
			return "", 0, 0, bad
		}
		nums = append([]int{n}, nums...)
		parts = parts[:len(parts)-1]
	}
	if len(nums) == 0 {
		return "", 0, 0, bad
	}
	line, col := nums[0], 0
	if len(nums) == 2 {
		col = nums[1]
	}
	path, o := s.projectPath(root, strings.Join(parts, ":"))
	if o != nil {
		return "", 0, 0, o
	}
	return path, line, col, nil
}

// projectPath resolves a file argument, relative to the working directory
// or else to the project root, to a project-relative slash path.
func (s *session) projectPath(root, path string) (string, *outcome) {
	for _, base := range []string{s.env.Cwd, root} {
		abs := path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(base, path)
		}
		if st, err := os.Stat(abs); err != nil || st.IsDir() {
			continue
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", usageErr("%s is outside the project at %s", path, root)
		}
		return filepath.ToSlash(rel), nil
	}
	return "", fail(exitFail, "not_found", "no file %s", path)
}
