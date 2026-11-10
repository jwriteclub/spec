// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/proposal"
)

// Step 4b review: text from proposals and models reaches the terminal
// safely. Each test failed before its fix.

// rawFrame is the frame as written to the terminal, escapes included.
func rawFrame(a *App) string {
	content, _ := a.render()
	return content
}

// assertNoInjectedControls checks that none of the control sequences a
// proposal carried reach the terminal.
func assertNoInjectedControls(t *testing.T, frame, where string) {
	t.Helper()
	assert.NotContains(t, frame, "\x1b[8m", "%s: a conceal sequence from the proposal", where)
	assert.NotContains(t, frame, "\x1b]52", "%s: an OSC 52 clipboard write from the proposal", where)
	assert.NotContains(t, frame, "\x07", "%s: a bell", where)
	assert.NotContains(t, frame, "\r", "%s: a carriage return", where)
	assert.NotContains(t, frame, string(rune(0x202e)), "%s: a right-to-left override", where)
}

// A proposal is written by an agent or a model: control characters in it
// must not reach the terminal, where they could hide text from the
// reviewer (conceal, carriage return, bidi override) or act (OSC 52).
func TestReviewNeutralizesControls(t *testing.T) {
	dir := demoRepo(t)
	p := agentProposal(t, dir, false)
	p.Hunks[0].Replace = "Prose that mentions $Frame.Order\x1b[8m and HIDDEN\x1b[0m for the reader."
	p.Hunks[0].Explanation = "explain\rX"
	p.Why = "why \x1b]52;c;ZXZpbA==\x07 ok"
	p.Author = "agent" + string(rune(0x202e)) + "7"
	p.Questions = []string{"q\x1b[8m?"}
	require.NoError(t, proposal.NewStore(dir).Save(p))

	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	send(a, key("ctrl+r"))
	assertNoInjectedControls(t, rawFrame(a), "sidebar")
	assert.Contains(t, plain(a), "agent·7", "origin is sanitized in the queue")
	send(a, key("enter"))
	require.NotNil(t, reviewOf(a))
	frame := rawFrame(a)
	assertNoInjectedControls(t, frame, "review")
	assert.Contains(t, plain(a), "why ^[]52;c;", "full rationale is safely displayed in deliberate review")
	assert.Contains(t, plain(a), "+ Prose that mentions $Frame.Order^[[8m and HIDDEN^[[0m for the reader.")
	assert.Contains(t, plain(a), "by agent·7")
}

// An analysis's findings come from the model too.
func TestSidebarNeutralizesControls(t *testing.T) {
	dir := demoRepo(t)
	writeFile(t, dir, "other.spec.md", strings.Replace(otherSpec, "8 items", "9 items", 1))
	reply := modelJSON(t, map[string]any{
		"summary":   "sum\x1b]52;c;eA==\x07",
		"findings":  []map[string]any{{"file": "other.spec.md", "issue": "ambiguity", "severity": "cosmetic", "explanation": "x\x1b[8mhidden"}},
		"questions": []string{"q\r?"},
	})
	m := newFakeModel(t, reply)
	f := startFlow(t, uiOptions(dir, testWorkflow(t, m)))
	f.send(key("ctrl+n"))
	f.until("the findings", func(a *App) bool { return itemCards(a) == 2 })
	f.send(key("ctrl+r"))
	var frame string
	f.do(func(a *App) { frame = rawFrame(a) })
	assertNoInjectedControls(t, frame, "sidebar")
	f.send(key("down"))
	f.do(func(a *App) { frame = rawFrame(a) })
	assertNoInjectedControls(t, frame, "sidebar, the question")
	f.finish()
}

// Spec files are Chris's own, but they come from a repository: raw
// controls in an anchor's title or body must not reach the terminal
// through the hover popup either.
func TestHoverNeutralizesSpecText(t *testing.T) {
	dir := demoRepo(t)
	src := strings.Replace(demoSpec, "**$Frame.Order (frame order).** Fragments MUST arrive",
		"**$Frame.Order (frame\x1b[8m order).** Fragments\x1b]52;c;ZXZpbA==\x07 MUST\r arrive "+string(rune(0x202e))+"in", 1)
	require.NotEqual(t, demoSpec, src)
	writeFile(t, dir, "demo.spec.md", src)

	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	send(a, key("ctrl+w"))
	send(a, typeText("$Frame.Order for")...)
	send(a, key("enter"), key("ctrl+l"))
	require.NotNil(t, a.popup)
	assertNoInjectedControls(t, rawFrame(a), "hover")
	assert.NotContains(t, rawFrame(a), "\x1b[8m")
	assert.Contains(t, plain(a), "inv $Frame.Order (frame^[[8m order)", "the popup's head, in caret notation")
}
