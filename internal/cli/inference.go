// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"github.com/sirupsen/logrus"
	"github.com/tenta-browser/polychromatic"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/workflow"
)

var lg *logrus.Entry = polychromatic.GetLogger("cli")

// workflow returns the proposal workflow in s's environment, with
// --provider, --engine and --effort applied.
func (s *session) workflow() *workflow.Workflow {
	return &workflow.Workflow{
		Getenv: s.env.Getenv, EnvFiles: &s.envFiles, Git: s.env.Git, Now: s.env.Now,
		BeforeCommit: s.env.beforeCommit, TuneClient: s.env.tuneClient, SpellDict: s.env.spellDict,
		Provider: s.provider.name, Model: s.provider.model, Effort: s.provider.effort,
		ModelChecker: formal.WithLimits(s.env.modelChecker, s.checkLimits()),
	}
}

// fromWF maps a workflow error to an outcome, with the data the JSON
// contract gives it.
func fromWF(e *workflow.Error) *outcome {
	o := fail(e.Exit, e.Code, "%s", e.Message)
	switch {
	case e.Lock != nil:
		held := e.Lock
		o.data = map[string]any{"lock": lockJSON{
			PID: held.Record.PID, Host: held.Record.Host,
			Started: held.Record.Started.UTC().Format("2006-01-02T15:04:05Z"), Command: held.Record.Command,
		}}
	case e.Written:
		data := approveData{Proposal: e.Proposal, Trailers: e.Trailers, Next: nextJSON{Text: "commit the spec files yourself with the trailer lines `spec status` prints", Argv: []string{"spec", "status"}}}
		if e.Proposal != nil && e.Proposal.Decision != nil {
			data.Commit = e.Proposal.Decision.Commit
		}
		if e.Code == "recovery_required" {
			data.Next = nextJSON{Text: "inspect the interrupted operation with spec recover before any further write or commit", Argv: []string{"spec", "recover"}}
		}
		o.data = data
	case e.Proposal != nil:
		o.data = proposalData{Proposal: e.Proposal}
	}
	return o
}
