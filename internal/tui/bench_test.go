// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/pos"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/tui/editor"
)

// generateSpec returns a deterministic spec of exactly lines lines holding
// anchors anchors, the same shape as mdspec's parse benchmark (step 1
// acceptance criterion 3).
func generateSpec(lines, anchors int) []byte {
	var out []string
	add := func(s ...string) { out = append(out, s...) }
	name := func(i int) string { return fmt.Sprintf("Area%d.Rule%d", i/10, i%10) }
	add("# Generated spec", "", "Generated for the parse benchmark; every anchor references two others.", "")
	for i := 0; i < anchors; i++ {
		if i%10 == 0 {
			add(fmt.Sprintf("## %d. Area %d", i/10+1, i/10), "")
		}
		label := fmt.Sprintf("**$%s (rule %d).**", name(i), i)
		if i%5 == 0 {
			label = fmt.Sprintf("**Def $%s (term %d).**", name(i), i)
		}
		add(
			label+" The component MUST keep `state_"+fmt.Sprint(i)+"` consistent with $"+name((i+1)%anchors)+",",
			"and it MUST NOT exceed the bound stated in $"+name((i+7)%anchors)+" (see §"+fmt.Sprint(i/10+1)+").",
			"A second sentence wraps across lines so the paragraph looks like real",
			"prose, with a duration of 30 s and a limit of 4 sessions per NVR.",
			"- Step one holds for every input.",
			"- Step two holds after a restart.",
			"Checked by: a property test.",
			"",
		)
	}
	i := 0
	for len(out) < lines-1 {
		add(fmt.Sprintf("Prose line %d mentions $%s and the plain words around it.", i, name(i%anchors)))
		i++
	}
	out = out[:lines-1]
	add("End.")
	return []byte(strings.Join(out, "\n") + "\n")
}

// genApp opens a project holding the generated 1,500-line spec, with the
// cursor in the middle and the parse debounced as in `spec ui`.
func genApp(b testing.TB, delay time.Duration) *App {
	b.Helper()
	dir := b.TempDir()
	require.NoError(b, os.WriteFile(filepath.Join(dir, "spec.toml"), []byte("name = \"gen\"\n"), 0o644))
	require.NoError(b, os.WriteFile(filepath.Join(dir, "gen.spec.md"), generateSpec(1500, 150), 0o644))
	opts := testOptions(dir)
	opts.ParseDelay = delay
	a := newTestApp(b, opts)
	a.cur.Ed.GotoLine(750)
	a.cur.Ed.LineEnd(false)
	return a
}

// BenchmarkKeystrokeView is acceptance criterion 5: one typed character,
// Update then View, on the 1,500-line spec. The reparse is debounced
// (30 ms, as in `spec ui`), so it is not part of the keystroke; see
// BenchmarkKeystrokeParseView for the cost when it lands.
func BenchmarkKeystrokeView(b *testing.B) {
	a := genApp(b, 30*time.Millisecond)
	k := tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"})
	b.ReportAllocs()
	for b.Loop() {
		a.Update(k)
		_ = a.View()
	}
}

// BenchmarkCursorView moves the cursor down and renders.
func BenchmarkCursorView(b *testing.B) {
	a := genApp(b, 30*time.Millisecond)
	down, up := key("down"), key("up")
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		if n%2 == 0 {
			a.Update(down)
		} else {
			a.Update(up)
		}
		n++
		_ = a.View()
	}
}

// BenchmarkKeystrokeParseView includes the reparse, rebuild and lint of
// the whole project (what a debounced parse adds once typing pauses).
func BenchmarkKeystrokeParseView(b *testing.B) {
	a := genApp(b, 0)
	k := tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"})
	b.ReportAllocs()
	for b.Loop() {
		a.Update(k)
		_ = a.View()
	}
}

// BenchmarkViewWithPopup renders a frame with a hover popup composited
// over it.
func BenchmarkViewWithPopup(b *testing.B) {
	a := genApp(b, 30*time.Millisecond)
	a.cur.Ed.SetCursor(strings.Index(string(a.cur.Ed.Buffer().Bytes()), "$Area0.Rule1,"))
	a.Update(key("ctrl+l"))
	require.NotNil(b, a.popup)
	b.ReportAllocs()
	for b.Loop() {
		_ = a.View()
	}
}

// BenchmarkBufferInsert measures one insertion with the line index rebuilt
// (the plan's "pos.Index rebuilt per edit is fine at our sizes; measure
// it").
func BenchmarkBufferInsert(b *testing.B) {
	src := generateSpec(1500, 150)
	buf := editor.NewBuffer(src)
	mid := len(src) / 2
	b.ReportAllocs()
	for b.Loop() {
		buf.Replace(pos.Range{Start: mid, End: mid}, "x", editor.KindType, mid, mid+1)
		mid++
	}
}

// TestKeystrokeBudget checks the 16 ms budget with a margin for slow CI
// machines (the benchmark gives the real number).
func TestKeystrokeBudget(t *testing.T) {
	a := genApp(t, 30*time.Millisecond)
	k := tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"})
	const n = 50
	start := time.Now()
	for i := 0; i < n; i++ {
		a.Update(k)
		_ = a.View()
	}
	per := time.Since(start) / n
	t.Logf("keystroke to View: %v", per)
	require.Less(t, per, 16*time.Millisecond)
}

// BenchmarkKeystrokeViewActive is BenchmarkKeystrokeView with a proposal
// shown (step 4d; a card selected in the sidebar since 4e): the gutter
// bars diff the buffer against HEAD on every frame.
func BenchmarkKeystrokeViewActive(b *testing.B) {
	a := genApp(b, 30*time.Millisecond)
	a.ws.Base = &baseline.Baseline{State: baseline.StateOK, Spec: a.ws.Spec}
	src := string(a.ws.Source("gen.spec.md"))
	line := strings.Split(src, "\n")[760]
	p := &proposal.Proposal{ID: "bench", Hunks: []finding.Hunk{{N: 1, File: "gen.spec.md", Find: line}}}
	a.side.cards = []*card{{kind: cardEdit, p: p, key: "h1", hunk: p.Hunks[0]}}
	a.sideShown = true
	k := tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"})
	b.ReportAllocs()
	for b.Loop() {
		a.Update(k)
		_ = a.View()
	}
}
