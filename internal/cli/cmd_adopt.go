// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/explain"
)

type adoptData struct {
	Text   string                 `json:"text"`
	Topics []explain.TopicSummary `json:"topics"`
}

func (s *session) adoptCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "adopt",
		Short: "start here (for coding agents): what specs are, how to start, and where the documentation is",
		Long: "Prints a one-screen introduction for a coding agent: specs as spec uses them, how to start\n" +
			"with spec init, and every documentation command. Needs no project.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errTooMany("adopt")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runAdopt()
			return nil
		},
	}
}

func (s *session) runAdopt() *outcome {
	text, err := explain.Adopt()
	if err != nil {
		return internalErr(err)
	}
	topics, err := explain.Summaries()
	if err != nil {
		return internalErr(err)
	}
	return &outcome{data: adoptData{Text: text, Topics: topics}, human: text}
}
