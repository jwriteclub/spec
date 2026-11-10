// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/workflow"
)

type fmtData struct {
	Check       bool                  `json:"check"`
	Written     bool                  `json:"written"`
	Files       []workflow.FormatFile `json:"files"`
	Unformatted []string              `json:"unformatted"`
}

func (s *session) fmtCommand() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "fmt [FILE...] [--check]",
		Short: "format the spec files' whitespace: wrap paragraphs, strip trailing spaces",
		Long: "Wraps paragraphs and list-item text to spec.toml's [format] width (default 100), strips trailing\n" +
			"whitespace, collapses runs of blank lines and ends each file with one newline. Code, tables, HTML,\n" +
			"headings and quotes are left as they are. Whitespace only: a file whose anchor hashes or Markdown\n" +
			"structure would change is left alone, with the reason. --check writes nothing, lists the files\n" +
			"that are not formatted, and exits 4 when there are any (for a pre-commit hook).",
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runFmt(args, check)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "write nothing; list unformatted files and exit 4 if there are any")
	return cmd
}

func (s *session) runFmt(args []string, check bool) *outcome {
	root, o := s.resolveRoot()
	if o != nil {
		return o
	}
	var paths []string
	for _, a := range args {
		p, o := s.projectPath(root, a)
		if o != nil {
			return o
		}
		paths = append(paths, p)
	}
	proj, o := s.openRoot(root)
	if o != nil {
		return o
	}
	res, e := s.workflow().Format(proj, paths, check)
	if e != nil {
		return fromWF(e)
	}
	v, o := s.loadViewOf(proj)
	if o != nil {
		return o
	}
	data := fmtData{Check: check, Written: res.Written, Files: res.Files, Unformatted: res.Unformatted()}
	if data.Unformatted == nil {
		data.Unformatted = []string{}
	}

	var b strings.Builder
	if check {
		for _, f := range data.Unformatted {
			b.WriteString(f + "\n")
		}
	} else {
		for _, f := range res.Files {
			if f.Changed > 0 {
				fmt.Fprintf(&b, "formatted %s: %s changed\n", f.File, plural(f.Changed, "line"))
			}
		}
		if len(data.Unformatted) == 0 && len(res.Refused()) == 0 {
			fmt.Fprintf(&b, "already formatted: %s\n", plural(len(res.Files), "file"))
		}
	}
	out := &outcome{data: data, diags: v.diagsJSON(), human: b.String()}
	if refused := res.Refused(); len(refused) > 0 {
		var msgs []string
		for _, f := range refused {
			msgs = append(msgs, f.File+": "+f.Refused)
			out.stderr += "spec: " + f.File + " was not formatted: " + f.Refused + "\n"
		}
		out.exit, out.code = exitFail, "not_formatted"
		out.message = "not formatted: " + strings.Join(msgs, "; ")
		return out
	}
	if check && len(data.Unformatted) > 0 {
		out.exit, out.code = exitCheck, "unformatted"
		out.message = plural(len(data.Unformatted), "file") + " not formatted; run spec fmt"
	}
	return out
}
