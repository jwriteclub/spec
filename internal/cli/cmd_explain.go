// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/explain"
)

type explainData struct {
	Topic  *string  `json:"topic"`
	Text   string   `json:"text"`
	Topics []string `json:"topics"`
}

func (s *session) explainCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "explain [topic]",
		Short: "print the documentation: an index, or one topic",
		Long:  "Bare `spec explain` prints an index with one line per topic. `spec explain TOPIC` prints a topic.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return errTooMany("explain [topic]")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runExplain(args)
			return nil
		},
	}
}

func (s *session) runExplain(args []string) *outcome {
	d := explainData{Topics: explain.Names()}
	var text string
	if len(args) == 0 {
		text = explain.Index()
	} else {
		t, ok := explain.Topic(args[0])
		if !ok {
			return fail(exitFail, "not_found", "no explain topic %q; topics: %s", args[0], strings.Join(explain.Names(), ", "))
		}
		d.Topic = &args[0]
		text = t
	}
	d.Text = text
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return &outcome{data: d, human: text}
}
