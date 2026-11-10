// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/tui/editor"
)

// lensdReflowed is a repository whose HEAD holds the lensd fixture and
// whose working tree holds Chris's playground after his manual test: the
// whole file reflowed by ^F, plus one content change (an accepted fix
// that cites ($Time.Wall) in $Frame.Forward).
func lensdReflowed(t *testing.T) *App {
	t.Helper()
	gitEnv(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	head, err := os.ReadFile("../../testdata/lensd/lensd.spec.md")
	require.NoError(t, err)
	writeFile(t, dir, "spec.toml", "name = \"lensd\"\n")
	writeFile(t, dir, "lensd.spec.md", string(head))
	writeFile(t, dir, ".gitignore", ".spec/\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	cur, err := os.ReadFile("testdata/lensd-reflowed.spec.md")
	require.NoError(t, err)
	writeFile(t, dir, "lensd.spec.md", string(cur))
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.ws.Base = a.ws.LoadBaseline(context.Background())
	require.Equal(t, baseline.StateOK, a.ws.Base.State, a.ws.Base.Reason)
	return a
}

// TestNextEditKinds is Chris's report: ^] took him to 8 places that ^F
// had only reflowed, with no hint of what changed there, and never to
// $Frame.Forward, the one anchor with a real change (line runs merged it
// into a reflow). Stops now come in three kinds that never merge, each
// labelled: the changed anchor (at its label), prose whose words changed,
// and format only (one per reflowed paragraph). The gutter tells them
// apart, and the changed words are tinted.
func TestNextEditKinds(t *testing.T) {
	a := lensdReflowed(t)
	es, why := a.edits()
	require.Empty(t, why)
	as := a.ws.Spec.Lookup("Frame.Forward")
	require.Len(t, as, 1)
	an := as[0]
	kinds := map[int]int{}
	for _, e := range es {
		kinds[e.kind]++
		switch e.kind {
		case stopAnchor:
			assert.Equal(t, "inv $Frame.Forward changed: added ($Time.Wall)", e.label)
			assert.Equal(t, an.LabelRange.Start, e.start, "at the anchor")
		case stopFormat:
			assert.True(t, strings.HasPrefix(e.label, "format only in "), e.label)
			assert.True(t, strings.HasSuffix(e.label, " (whitespace or wrapping)"), e.label)
		}
	}
	assert.Equal(t, 1, kinds[stopAnchor], "%+v", es)
	assert.Equal(t, 0, kinds[stopProse], "%+v", es)
	assert.Greater(t, kinds[stopFormat], 20, "one stop per reflowed paragraph")
	labels := map[string]bool{}
	for _, e := range es {
		require.False(t, labels[e.path+e.label+fmt.Sprint(e.line)], "no duplicate stops")
		labels[e.path+e.label+fmt.Sprint(e.line)] = true
	}
	assert.Contains(t, labels, "lensd.spec.mdformat only in $Time.Wall (whitespace or wrapping)39")

	// The gutter: the thick green bar on every line of $Frame.Forward, a
	// thin cyan pipe on the reflowed lines only, no prose pipe.
	f := a.ws.File("lensd.spec.md")
	from, to := f.Index().Line(an.LabelRange.Start), f.Index().Line(an.BodyRange.End-1)
	bars := barsOf(a, "lensd.spec.md")
	var want []int
	for l := from; l <= to; l++ {
		want = append(want, l)
	}
	assert.Equal(t, want, bars[barAnchor])
	assert.Empty(t, bars[barProseAdd])
	assert.Empty(t, bars[barProseDel])
	assert.Contains(t, bars[barFormat], 39)
	assert.NotContains(t, bars[barFormat], 38, "the unchanged line 38 (blank) gets no pipe")
	fe, _ := a.fileEditsOf("lensd.spec.md", string(a.ws.Source("lensd.spec.md")))
	var reflowed []int
	for _, l := range fe.format {
		if l < from || l > to {
			reflowed = append(reflowed, l) // the anchor's lines keep the thick bar
		}
	}
	assert.Equal(t, reflowed, bars[barFormat], "the pipe marks exactly the reflowed lines")
	rows := renderedLines(a, an.LabelRange.Start)
	assert.Contains(t, rows[from], a.theme.Bar[barAnchor].Render(" "), "thick bar, line %d", from)
	assert.Contains(t, rows[to], a.theme.Bar[barAnchor].Render(" "), "thick bar, line %d", to)
	assert.Contains(t, rows[to+2], a.theme.Pipe[barFormat].Render("│"), "thin cyan pipe on the next, reflowed paragraph")

	// ^] from the top: the kinds in file order; the anchor gets its stop.
	a.jump(Location{Path: "lensd.spec.md"})
	var flashes []string
	for range es {
		send(a, key("ctrl+]"))
		flashes = append(flashes, a.flash)
	}
	assert.Equal(t, fmt.Sprintf("Edit %d/%d: inv $Frame.Forward changed: added ($Time.Wall)", slicesIndex(es, stopAnchor)+1, len(es)), flashes[slicesIndex(es, stopAnchor)])
	assert.Equal(t, fmt.Sprintf("Edit 1/%d: format only in lensd specification (whitespace or wrapping)", len(es)), flashes[0])

	// One real prose edit (unsaved) adds a prose stop, in its own colour.
	find(a, "lensd is a Go web gateway")
	send(a, key("ctrl+right"), key("ctrl+right"), key("ctrl+right"), key("ctrl+right"))
	send(a, typeText("HTTP ")...)
	es2, _ := a.edits()
	var prose []specEdit
	for _, e := range es2 {
		if e.kind == stopProse {
			prose = append(prose, e)
		}
	}
	require.Len(t, prose, 1)
	assert.Equal(t, "prose in Context (for readers without the code): added HTTP", prose[0].label)
	assert.Len(t, es2, len(es), "the reflowed paragraph's format stop became the prose stop")
	line := a.cur.Ed.Buffer().Index().Line(prose[0].start)
	assert.Equal(t, []int{line}, barsOf(a, "lensd.spec.md")[barProseAdd], "a green pipe on the edited line only")
	rows = renderedLines(a, prose[0].start)
	assert.Contains(t, rows[line], a.theme.Pipe[barProseAdd].Render("│"))
}

// renderedLines renders the editor around off and maps each buffer line
// on screen to its first row.
func renderedLines(a *App, off int) map[int]string {
	a.jump(Location{Path: a.cur.Ed.Path(), Off: off})
	a.cur.Ed.CenterCursor()
	_, _, w, h := a.editorRect()
	out := map[int]string{}
	for _, l := range a.renderEditor(a.cur.Ed, w, h) {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(ansi.Strip(l)), "%d", &n); err == nil && n > 0 {
			if _, seen := out[n]; !seen {
				out[n] = l
			}
		}
	}
	return out
}

// TestChangedWordsRender: the gutter's thick bar covers a changed anchor's
// every line; thin pipes mark only the prose lines that changed (green
// added, red removed, on the line the words left, yellow both); added
// words are underlined, removed ones hidden until shown as ghost text.
// With an underline colour (SGR 58) and without (Terminal.app).
func TestChangedWordsRender(t *testing.T) {
	dir := demoRepo(t)
	edited := strings.Replace(demoSpec, "above zero", "of at most 5 s", 1)                    // in $Frame.Timing
	edited = strings.Replace(edited, "Prose that mentions", "Prose that clearly mentions", 1) // added
	edited = strings.Replace(edited, "Media time is the", "Media time is", 1)                 // removed (in $Time.Media)
	writeFile(t, dir, "demo.spec.md", edited)
	writeFile(t, dir, "other.spec.md", strings.Replace(otherSpec, "at most 8 items", "at most 9 items", 1))
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.ws.Base = a.ws.LoadBaseline(context.Background())
	require.Equal(t, baseline.StateOK, a.ws.Base.State, a.ws.Base.Reason)
	// Lines: 5 $Time.Media (removed "the"), 9-10 $Frame.Timing, 15 prose.
	bars := barsOf(a, "demo.spec.md")
	assert.Equal(t, map[barKind][]int{barAnchor: {5, 9, 10}, barProseAdd: {15}}, bars, "thick bars on every anchor line; a pipe on the prose line only")

	// A removal in prose: the red pipe on the line it left; both: yellow.
	b := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	b.ws.Base = a.ws.Base
	b.jump(Location{Path: "demo.spec.md"})
	ed := b.cur.Ed
	at, ok := ed.Find("for the reader", 0)
	require.True(t, ok)
	ed.SetCursor(at.Start + len("for "))
	for range len("the ") {
		send(b, key("delete"))
	}
	assert.Equal(t, []int{15}, barsOf(b, "demo.spec.md")[barProseBoth], "line 15: one word added, one removed")
	send(b, key("ctrl+z"))
	send(b, key("ctrl+z"))
	at, _ = ed.Find("Prose that clearly mentions", 0)
	ed.SetCursor(at.Start)
	send(b, key("ctrl+k")) // cut line 15 whole: its words are removed
	bb := barsOf(b, "demo.spec.md")
	require.NotEmpty(t, bb[barProseDel], "%v", bb)
	for _, l := range bb[barProseDel] {
		assert.Equal(t, 13, l, "the red pipe is on the line where the words were (the end of the line before)")
	}

	for _, plainUL := range []bool{false, true} {
		a.theme = NewTheme(true, plainUL)
		th := a.theme
		rows := renderedLines(a, strings.Index(string(a.ws.Source("demo.spec.md")), "clearly"))
		// Gutter.
		assert.Contains(t, rows[9], th.Bar[barAnchor].Render("│"))
		assert.Contains(t, rows[10], th.Bar[barAnchor].Render(" "), "every line of the anchor")
		assert.Contains(t, rows[15], th.Pipe[barProseAdd].Render("│"))
		for _, k := range []barKind{barProseAdd, barProseDel, barProseBoth, barFormat} {
			assert.NotContains(t, rows[14], th.Pipe[k].Render("│"), "unchanged line 14: no pipe")
		}
		// Added words.
		added := th.Run(editor.FAdded).Render("clearly")
		assert.Contains(t, rows[15], added)
		if plainUL {
			assert.NotContains(t, strings.Join(mapValues(rows), ""), "58;", "no underline colour")
			assert.Equal(t, lipgloss.NewStyle().Foreground(th.add).Underline(true).Render("clearly"), added, "green prose, plain underline")
			t.Logf("plain: added %q", added)
		} else {
			assert.Equal(t, lipgloss.NewStyle().Underline(true).UnderlineColor(th.add).Render("clearly"), added, "a green underline colour")
			assert.Contains(t, added, "58;", "SGR 58")
			t.Logf("colour: added %q", added)
		}
		// Removals: hidden by default, shown as ghost text with r.
		assert.NotContains(t, ansi.Strip(rows[5]), "is the timestamp")
		a.showRemoved = true
		rows = renderedLines(a, strings.Index(string(a.ws.Source("demo.spec.md")), "Media time is"))
		assert.Contains(t, ansi.Strip(rows[5]), "Media time is the", "the ghost where the word was")
		ghost := th.Run(editor.FRemoved).Render(" the")
		assert.Contains(t, rows[5], ghost)
		if plainUL {
			assert.Equal(t, lipgloss.NewStyle().Foreground(th.dim).Strikethrough(true).Render(" the"), ghost, "struck through (SGR 9)")
			t.Logf("plain: ghost %q", ghost)
			assert.NotContains(t, ghost, "58;")
		} else {
			assert.Equal(t, lipgloss.NewStyle().Foreground(th.dim).Underline(true).UnderlineColor(th.bad).Render(" the"), ghost, "a red underline colour")
			assert.Contains(t, ghost, "58;", "SGR 58")
			t.Logf("colour: ghost %q", ghost)
		}
		a.showRemoved = false
	}
}

// mapValues returns m's values.
func mapValues(m map[int]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// slicesIndex returns the index of the first stop of kind k.
func slicesIndex(es []specEdit, k int) int {
	for i, e := range es {
		if e.kind == k {
			return i
		}
	}
	return -1
}

// TestContentEdits: words compared, whitespace and wrapping ignored.
func TestContentEdits(t *testing.T) {
	old := "One two three\nfour five.\n\nSix seven."
	reflowed := "One two\nthree four five.\n\nSix   seven."
	assert.Empty(t, contentEdits(old, reflowed), "reflow only")
	cur := "One two\nthree 4 five.\n\nSix seven eight."
	es := contentEdits(old, cur)
	require.Len(t, es, 2)
	assert.Equal(t, []string{"4"}, es[0].added)
	assert.Equal(t, []string{"four"}, es[0].removed)
	assert.Equal(t, "4", cur[es[0].start:es[0].end])
	assert.Equal(t, []string{"seven."}, es[1].removed)
	assert.Equal(t, "seven eight.", cur[es[1].start:es[1].end])
	assert.Equal(t, "replaced four with 4 (+1 more)", describe(es))
	// Removal only: an empty run where the words were.
	es = contentEdits("a b c", "a c")
	require.Len(t, es, 1)
	assert.Equal(t, es[0].start, es[0].end)
	assert.Equal(t, 1, es[0].start, "after the word before")
	assert.Equal(t, " b", es[0].ghost)
	assert.Equal(t, "removed b", describe(es))
}

// TestFormatWhileAccepting: ^F on a card previews the suggested edit
// formatted (as ^F formats the buffer), and y applies the edit and the
// format as one undo step. Review reserves that footer slot for retry instead.
func TestFormatWhileAccepting(t *testing.T) {
	dir := demoProject(t)
	// Trailing spaces next to the edit: ^F strips them.
	writeFile(t, dir, "other.spec.md", strings.Replace(otherSpec, "Checked by: TestLimit.", "Checked by: TestLimit.   ", 1))
	fixedProposals(t, dir)
	a := newTestApp(t, testOptions(dir))
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	send(a, key("ctrl+r"))
	a.side.selectProposal("m7qd2")
	require.Equal(t, cardProblem, a.side.selected().kind)
	orig := string(a.ws.Source("other.spec.md"))
	send(a, key("ctrl+f"))
	require.Equal(t, cardEdit, a.side.selected().kind, "^F on a problem with one fix selects the fix")
	assert.Contains(t, a.flash, "Formatted preview")
	frame := plain(a)
	assert.Contains(t, frame, "^F formatted: y applies both")
	assert.Contains(t, frame, "most 5 items. Checked by:")
	assert.Equal(t, orig, string(a.ws.Source("other.spec.md")), "the preview changes nothing")

	send(a, key("y"))
	want := strings.Replace(otherSpec, limitFind, limitReplace, 1)
	assert.Equal(t, want, string(a.ws.Docs()["other.spec.md"].Ed.Buffer().Bytes()), "the edit and the format")
	assert.Contains(t, a.flash, "Applied · unsaved · and formatted other.spec.md")
	send(a, key("esc"), key("ctrl+z"))
	assert.Equal(t, orig, string(a.ws.Docs()["other.spec.md"].Ed.Buffer().Bytes()), "one ^Z undoes both")

	// The agent's proposal, through its review.
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "Checked by: TestOrder.", "Checked by: TestOrder.  ", 1))
	b := newTestApp(t, testOptions(dir))
	send(b, key("ctrl+r"))
	b.side.selectProposal("k3x7a")
	send(b, key("enter"))
	r, ok := b.top().(*reviewScreen)
	require.True(t, ok)
	for _, n := range r.nums {
		r.state[n] = hunkAccepted
	}
	send(b, key("ctrl+f"))
	assert.False(t, r.format)
	assert.NotContains(t, plain(b), "^F formatted:")
	assert.Contains(t, plain(b), "Recheck")
	before := string(b.ws.Source("demo.spec.md"))
	send(b, key("A"))
	after := string(b.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes())
	assert.Contains(t, after, "TestOrder.  ", "review applies the proposed text without an invisible format")
	assert.Contains(t, after, "at most 5 s", "the change applied")
	assert.Contains(t, b.flash, "Applied · unsaved")
	assert.NotContains(t, b.flash, "and formatted")
	send(b, key("esc"), key("ctrl+z"))
	assert.Equal(t, before, string(b.ws.Docs()["demo.spec.md"].Ed.Buffer().Bytes()), "one ^Z undoes the reviewed application")
}

// TestPaneCycle: shift+Tab (ESC [ Z from Terminal.app) focuses the next
// visible pane, editor → sidebar → outline → editor, skipping a hidden
// one; Tab does the same in the side panes and indents in the editor; Esc
// returns to the editor.
func TestPaneCycle(t *testing.T) {
	r := runRaw(t, &keyRecorder{want: 1}, "\x1b[Z")
	require.Equal(t, []string{"shift+tab"}, r.keys)

	a := newTestApp(t, testOptions(demoProject(t)))
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	send(a, key("ctrl+r"), key("esc")) // the sidebar shown, the editor focused
	require.Equal(t, CtxEditor, a.context())
	var got []Context
	for range 4 {
		send(a, r.msg(0))
		got = append(got, a.context())
	}
	assert.Equal(t, []Context{CtxChecks, CtxOutline, CtxEditor, CtxChecks}, got, "all three panes")
	send(a, key("tab"))
	assert.Equal(t, CtxOutline, a.context(), "Tab in a side pane: the next pane")
	send(a, key("esc"))
	assert.Equal(t, CtxEditor, a.context(), "Esc: the editor")
	before := string(a.cur.Ed.Buffer().Bytes())
	send(a, key("tab"))
	assert.NotEqual(t, before, string(a.cur.Ed.Buffer().Bytes()), "Tab in the editor indents")
	send(a, key("ctrl+z"))

	// The outline hidden: editor → sidebar → editor.
	send(a, key("ctrl+^"), key("ctrl+^"))
	require.False(t, a.showOutline)
	got = nil
	for range 3 {
		send(a, key("shift+tab"))
		got = append(got, a.context())
	}
	assert.Equal(t, []Context{CtxChecks, CtxEditor, CtxChecks}, got, "the hidden outline is skipped")
	// Both hidden: nowhere to go.
	send(a, key("ctrl+r"))
	require.False(t, a.sideShown)
	send(a, key("shift+tab"))
	assert.Equal(t, CtxEditor, a.context())
	assert.Contains(t, a.flash, "No other pane")
}

// TestOutlineToggleKey: ctrl+6 arrives as ctrl+^ (0x1E) and toggles the
// outline: shows and focuses it, then hides it; F2 and M-O still do.
func TestOutlineToggleKey(t *testing.T) {
	r := runRaw(t, &keyRecorder{want: 1}, "\x1e")
	require.Equal(t, []string{"ctrl+^"}, r.keys)
	a := newTestApp(t, testOptions(demoProject(t)))
	send(a, key("ctrl+^"), key("ctrl+^"))
	assert.False(t, a.showOutline, "focused, then hidden")
	send(a, r.msg(0))
	assert.True(t, a.showOutline)
	assert.Equal(t, CtxOutline, a.context())
	send(a, key("f2"))
	assert.False(t, a.showOutline)
	assert.Contains(t, strings.Join(strings.Fields(ansi.Strip(strings.Join(a.helpBar(CtxEditor, 160), "\n"))), " "), "^^ Outline")
}

// TestNoTildeLines: past the end of the buffer the gutter is blank, no
// vi-style ~ markers.
func TestNoTildeLines(t *testing.T) {
	a := newTestApp(t, testOptions(demoProject(t)))
	frame := plain(a)
	for _, l := range strings.Split(frame, "\n") {
		assert.NotRegexp(t, `│\s*~\s*$`, l)
	}
	assert.NotContains(t, frame, "  ~\n")
}

// TestFourMarks: with the cursor in a changed anchor, next to a
// format-only paragraph and a prose edit, the four marks show together,
// each in its own place: the thick bar and the thin pipes in the marker
// column, the extent tick in the column before it, in a neutral grey.
func TestFourMarks(t *testing.T) {
	dir := demoRepo(t)
	edited := strings.Replace(demoSpec, "above zero", "of at most 5 s", 1)
	edited = strings.Replace(edited, "see $Frame.Timing.\nChecked by: TestOrder.", "see\n$Frame.Timing. Checked by: TestOrder.", 1) // reflow only
	edited = strings.Replace(edited, "Prose that mentions", "Prose that clearly mentions", 1)
	writeFile(t, dir, "demo.spec.md", edited)
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.ws.Base = a.ws.LoadBaseline(context.Background())
	require.Equal(t, baseline.StateOK, a.ws.Base.State, a.ws.Base.Reason)
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	th := a.theme
	rows := renderedLines(a, strings.Index(edited, "of at most"))
	tick := th.Tick.Render("│")
	assert.Contains(t, rows[9], tick+th.Bar[barAnchor].Render("│"), "the exact changed anchor line has a thin pipe over its extent")
	assert.Contains(t, rows[10], tick+th.Bar[barAnchor].Render(" "), "the other anchor line retains the broad extent fill")
	for _, l := range []int{12, 13} {
		assert.Contains(t, rows[l], " "+th.Pipe[barFormat].Render("│"), "line %d: the thin cyan pipe, no tick", l)
		assert.NotContains(t, rows[l], tick)
	}
	assert.Contains(t, rows[15], th.Pipe[barProseAdd].Render("│"), "line 15: the thin green pipe")
	assert.NotEqual(t, th.Tick.GetForeground(), th.Pipe[barFormat].GetForeground(), "the tick is not the format cyan")
	es, _ := a.edits()
	var kinds []int
	for _, e := range es {
		kinds = append(kinds, e.kind)
	}
	assert.Equal(t, []int{stopAnchor, stopFormat, stopProse}, kinds)
}

// TestCommitDraftFallback: without a provider the commit view uses the
// template and says so; a message Chris wrote is never redrafted.
func TestCommitDraftFallback(t *testing.T) {
	dir := demoRepo(t)
	writeFile(t, dir, "demo.spec.md", strings.Replace(demoSpec, "above zero", "of at most 5 s", 1))
	f := startFlow(t, uiOptions(dir, testWorkflow(t, nil)))
	f.until("the baseline", func(a *App) bool { return a.ws.Base != nil })
	f.send(key("ctrl+r"))
	f.do(func(a *App) { assert.Contains(t, a.flash, "No model to check $Frame.Timing") })
	f.send(key("A"))
	var cs *commitScreen
	f.do(func(a *App) {
		var ok bool
		cs, ok = a.top().(*commitScreen)
		require.True(t, ok)
	})
	f.until("the fallback", func(a *App) bool { return !cs.drafting })
	frame := f.frame()
	assert.Contains(t, frame, "spec: change $Frame.Timing")
	assert.Contains(t, frame, "a template: no model provider")
	// c writes Chris's own message: no draft is asked for after that.
	f.send(key("c"))
	f.do(func(a *App) { require.Equal(t, CtxHunkEdit, a.context()) })
	f.typeIn(" (mine)")
	f.send(key("ctrl+o"))
	f.send(key("n"), key("y"))
	f.do(func(a *App) {
		assert.False(t, cs.drafting, "not redrafted")
		assert.Equal(t, "spec: change $Frame.Timing (mine)", cs.message(a))
	})
	assert.Contains(t, f.frame(), "yours; c edits it")
	f.finish()
}

// TestGoldenWide: the editor and the Checks sidebar at 160 and 220
// columns. The editor keeps the spec's wrap width (100 plus the gutter)
// where it can and the sidebar takes the rest, up to 45% of the screen;
// the focused pane's edge is a thick accent line and its band says how to
// leave it. Regenerate with -update.
func TestGoldenWide(t *testing.T) {
	for _, tc := range []struct {
		name  string
		w     int
		focus bool
	}{{"w160-sidebar", 160, true}, {"w160-editor", 160, false}, {"w220-sidebar", 220, true}} {
		t.Run(tc.name, func(t *testing.T) {
			a := lensdReflowed(t)
			src := string(a.ws.Source("lensd.spec.md"))
			find := "consecutive segments (equal is allowed)"
			start := strings.Index(src, find)
			require.GreaterOrEqual(t, start, 0)
			p := &proposal.Proposal{
				ID: "w4d2e", Created: time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC), Author: proposal.AuthorHuman,
				Kind: proposal.KindAnalysis, Status: proposal.StatusPending, Summary: "one rule is ambiguous",
				Hunks: []finding.Hunk{{N: 1, File: "lensd.spec.md", Anchor: "$Frame.Forward", Issue: "ambiguity", Severity: "cosmetic",
					Explanation: "say which clock: the stamps are wall time", Find: find, Replace: "consecutive segments by $Time.Wall (equal is allowed)",
					Range: pos.Range{Start: start, End: start + len(find)}}},
			}
			require.NoError(t, proposal.NewStore(a.ws.Root).Save(p))
			a.loadPending()
			a.Update(tea.WindowSizeMsg{Width: tc.w, Height: 34})
			a.jump(Location{Path: "lensd.spec.md", Off: start})
			a.cur.Ed.CenterCursor()
			send(a, key("ctrl+r"))
			if !tc.focus {
				send(a, key("esc"))
			}
			a.flash = ""
			_, sw := a.sideRect()
			_, _, ew, _ := a.editorRect()
			t.Logf("%d columns: outline %d, editor %d, sidebar %d", tc.w, a.outlineWidth(), ew, sw)
			assert.True(t, a.sideColumn())
			assert.GreaterOrEqual(t, sw, sideWidth)
			assert.LessOrEqual(t, sw, sideMaxWidth)
			golden.RequireEqual(t, plain(a))
		})
	}
}

// TestAgentProposalShowsSidebar: at 160 columns an agent's proposal that
// arrives while Chris edits opens the sidebar as a column, its card
// selected, and leaves the keyboard in the editor; the help bar and the
// outline count it.
func TestAgentProposalKeepsHiddenSidebarClosed(t *testing.T) {
	dir := demoRepo(t)
	a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	require.False(t, a.sideShown)
	p := agentProposal(t, dir, false)
	a.Update(proposalsChangedMsg{})
	assert.Equal(t, "Agent proposal from agent-7 for $Frame.Timing", a.flash)
	assert.False(t, a.sideShown, "arrival does not open a deliberately hidden sidebar")
	assert.False(t, a.sideFocus)
	assert.Equal(t, CtxEditor, a.context())
	require.Equal(t, 2, itemCards(a), "one agent parent and its related problem")
	assert.Equal(t, p.ID, a.side.selected().p.ID)
	assert.Equal(t, "[1 proposal, 1 check]", a.badge())
}

// TestAgentProposalAuthorFlash: an agent's proposal with no author, as an
// external process may write one, flashes "Agent" instead of panicking, and
// a named author is capitalized by its first letter, not its first byte.
func TestAgentProposalAuthorFlash(t *testing.T) {
	for _, tc := range []struct{ author, want string }{
		{"", "Agent proposal for $Frame.Timing"},
		{"émile", "Agent proposal from émile for $Frame.Timing"},
	} {
		t.Run(tc.author, func(t *testing.T) {
			dir := demoRepo(t)
			a := newTestApp(t, uiOptions(dir, testWorkflow(t, nil)))
			a.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
			p := agentProposal(t, dir, false)
			p.Author = tc.author
			require.NoError(t, proposal.NewStore(dir).Save(p))
			a.Update(proposalsChangedMsg{})
			assert.Equal(t, tc.want, a.flash)
		})
	}
}
