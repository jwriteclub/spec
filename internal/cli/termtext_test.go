// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// evilText carries every kind of control a model or an agent could use to
// hide, rewrite or act on a reviewer's terminal: conceal, an OSC 52
// clipboard write, a bell, a carriage return and a bidi override.
const evilText = "x\x1b[8mHID\x1b]52;c;ZXZpbA==\x07\rY‮z"

// evilShown is evilText as the terminal output shows it.
const evilShown = "x^[[8mHID^[]52;c;ZXZpbA==^G^MY·z"

// assertCleanTerminal checks that out holds no raw control character other
// than line breaks and tabs.
func assertCleanTerminal(t *testing.T, out, where string) {
	t.Helper()
	for _, bad := range []string{"\x1b", "\x07", "\r", "‮"} {
		assert.NotContains(t, out, bad, "%s: %q reached the terminal", where, bad)
	}
}

// Text from a proposal or a model must not reach the terminal raw through
// the human output of any command: it could hide text from the reviewer,
// overwrite a line or write the clipboard. --json stays byte-exact.
func TestHumanOutputNeutralizesProposalText(t *testing.T) {
	requireGit(t)
	fix := proseFix
	fix.Explanation = "explain " + evilText
	fix.Replace = "Prose that mentions $Frame.Order " + evilText + " for the reader."
	note := modelFinding{File: "demo.spec.md", Issue: "ambiguity", Severity: "cosmetic", Explanation: "note " + evilText}
	reply := modelReply{Summary: "sum " + evilText, Findings: []modelFinding{fix, note}, Questions: []string{"q " + evilText}}.json(t)
	is := setupInfer(t, okReply(reply))
	write(t, is.dir, "body.md", timingNew)

	h := is.runHuman(t, "", "change", "Frame.Timing", "--body-file", "body.md", "--why", "why "+evilText, "--by", "agent")
	require.Equal(t, exitPending, h.code, h.stdout+h.stderr)
	assertCleanTerminal(t, h.stdout, "change")
	assert.Contains(t, h.stdout, "why: why "+evilShown)

	jr := is.run(t, "proposals")
	id := ""
	{
		var d proposalsData
		jr.decode(t, &d)
		require.Len(t, d.Proposals, 1)
		id = d.Proposals[0].ID
		assert.Equal(t, "why "+evilText, d.Proposals[0].Why, "--json is byte-exact")
	}

	h = is.runHuman(t, "", "proposals")
	assertCleanTerminal(t, h.stdout, "proposals")
	// A listing is one line per proposal: the carriage return is white space.
	assert.Contains(t, h.stdout, "why x^[[8mHID^[]52;c;ZXZpbA==^G Y\u00b7z")

	h = is.runHuman(t, "", "proposal", id)
	require.Equal(t, 0, h.code, h.stdout+h.stderr)
	assertCleanTerminal(t, h.stdout, "proposal")
	for _, want := range []string{"why: why " + evilShown, "check: sum " + evilShown, "explain " + evilShown,
		"Prose that mentions $Frame.Order " + evilShown, "note " + evilShown, "question: q " + evilShown} {
		assert.Contains(t, h.stdout, want)
	}

	h = is.runHuman(t, "q\n", "review", id)
	require.Equal(t, 0, h.code, h.stdout+h.stderr)
	assertCleanTerminal(t, h.stdout, "review")
	assertCleanTerminal(t, h.stderr, "review stderr")
	for _, want := range []string{"why: why " + evilShown, "check: sum " + evilShown, "note: ", "question: q " + evilShown} {
		assert.Contains(t, h.stdout, want)
	}
	h = is.runHuman(t, "y\nq\n", "review", id)
	assertCleanTerminal(t, h.stdout, "review hunks")
	assert.Contains(t, h.stdout, "explain "+evilShown)
	assert.Contains(t, h.stdout, "Prose that mentions $Frame.Order "+evilShown)

	// --json carries the bytes as they are.
	jr = is.run(t, "proposal", id)
	var pd proposalData
	jr.decode(t, &pd)
	assert.Equal(t, "why "+evilText, pd.Proposal.Why)
	assert.Equal(t, "sum "+evilText, pd.Proposal.Summary)
	assert.Equal(t, "Prose that mentions $Frame.Order "+evilText+" for the reader.", pd.Proposal.Hunks[0].Replace)
	assert.Equal(t, []string{"q " + evilText}, pd.Proposal.Questions)

	// analyze prints the proposal it filed.
	h = is.runHuman(t, "", "analyze", "--all")
	assertCleanTerminal(t, h.stdout, "analyze")
	assertCleanTerminal(t, h.stderr, "analyze stderr")
	assert.Contains(t, h.stdout, "check: sum "+evilShown)
}
