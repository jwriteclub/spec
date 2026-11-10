// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package mdspec

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// benchLines and benchAnchors size the generated spec of acceptance
// criterion 3.
const (
	benchLines   = 1500
	benchAnchors = 150
)

// generateSpec returns a deterministic spec of exactly lines lines holding
// anchors anchors in numbered sections, with cross references, section
// references, code spans, lists and plain prose.
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

func TestGeneratedSpecShape(t *testing.T) {
	src := generateSpec(benchLines, benchAnchors)
	require.Equal(t, benchLines, strings.Count(string(src), "\n"))
	f := ParseFile("gen.spec.md", src)
	s := Build([]*File{f})
	require.Len(t, s.Anchors, benchAnchors)
	for _, d := range s.Diagnostics {
		t.Errorf("unexpected diagnostic %s: %s", d.Code, d.Message)
	}
}

// BenchmarkParse parses and builds the generated 1,500-line, 150-anchor spec
// (acceptance criterion 3: under 20 ms per parse).
func BenchmarkParse(b *testing.B) {
	src := generateSpec(benchLines, benchAnchors)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		f := ParseFile("gen.spec.md", src)
		_ = Build([]*File{f})
	}
}
