// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// Generated project for the debounce benchmark (acceptance criterion 6):
// benchFiles files of benchFileLines lines each, 1,500 lines in all.
const (
	benchFiles     = 3
	benchFileLines = 500
	benchAnchors   = 50 // per file
)

// benchName is the name of anchor i of file f.
func benchName(f, i int) string {
	return fmt.Sprintf("Area%d.Rule%d", f, i)
}

// generateFile returns file f of the benchmark project: numbered sections of
// anchors that reference anchors of the next file and sections of the same
// file, padded with prose to exactly benchFileLines lines.
func generateFile(f int) string {
	var out []string
	add := func(s ...string) { out = append(out, s...) }
	add(fmt.Sprintf("# Generated spec %d", f), "", "Generated for the language server benchmark.", "")
	next := (f + 1) % benchFiles
	for i := 0; i < benchAnchors; i++ {
		if i%10 == 0 {
			add(fmt.Sprintf("## %d. Area %d", i/10+1, i/10), "")
		}
		label := fmt.Sprintf("**$%s (rule %d).**", benchName(f, i), i)
		if i%5 == 0 {
			label = fmt.Sprintf("**Def $%s (term %d).**", benchName(f, i), i)
		}
		add(
			label+" The component MUST keep `state_"+fmt.Sprint(i)+"` consistent with $"+benchName(next, i)+",",
			"and it MUST NOT exceed the bound in $"+benchName(f, (i+7)%benchAnchors)+" (see §"+fmt.Sprint(i/10+1)+").",
			"A second sentence wraps across lines so the paragraph looks like real prose,",
			"with a duration of 30 s and a limit of 4 sessions per NVR.",
			"- Step one holds for every input.",
			"Checked by: a property test.",
			"",
		)
	}
	for i := 0; len(out) < benchFileLines-1; i++ {
		add(fmt.Sprintf("Prose line %d mentions $%s and plain words around it.", i, benchName(f, i%benchAnchors)))
	}
	out = out[:benchFileLines-1]
	add("End.")
	return strings.Join(out, "\n") + "\n"
}

// benchProject writes the generated project, committed to git when git is
// available so the "changed since HEAD" comparison runs too, and returns its
// directory.
func benchProject(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	require.NoError(tb, os.WriteFile(filepath.Join(dir, "spec.toml"), []byte("name = \"bench\"\n"), 0o644))
	lines := 0
	for f := 0; f < benchFiles; f++ {
		src := generateFile(f)
		lines += strings.Count(src, "\n")
		require.NoError(tb, os.WriteFile(filepath.Join(dir, fmt.Sprintf("gen%d.spec.md", f)), []byte(src), 0o644))
	}
	require.Equal(tb, 1500, lines)
	if _, err := exec.LookPath("git"); err == nil {
		for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"commit", "-q", "-m", "bench"}} {
			full := append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
			cmd := exec.Command("git", full...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), gitEnv()...)
			out, err := cmd.CombinedOutput()
			require.NoError(tb, err, "%s", out)
		}
	}
	return dir
}

// benchServer opens gen0 in a server that is driven directly (no transport)
// and writes to io.Discard, and returns the server, the project and the two
// texts the benchmark alternates between: the file as committed, and the
// file with its first definition renamed, which dangles references in
// another file and changes an anchor against HEAD.
func benchServer(tb testing.TB) (*Server, *projectState, uri.URI, [2]string) {
	tb.Helper()
	dir := benchProject(tb)
	srv := New(strings.NewReader(""), io.Discard, Options{})
	srv.syncBaseline = true
	srv.initialize(&protocol.InitializeParams{})
	path := filepath.Join(dir, "gen0.spec.md")
	src, err := os.ReadFile(path)
	require.NoError(tb, err)
	u, err := fileURI(path)
	require.NoError(tb, err)
	srv.didOpen(&protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: u, Version: 1, Text: string(src)}})
	p := srv.docs[path].proj
	require.NotNil(tb, p)
	srv.flush(p) // first analysis and the git baseline
	tb.Cleanup(func() {
		p.stopTimer()
		close(srv.done)
	})
	edited := strings.Replace(string(src), "**Def $"+benchName(0, 0)+" ", "**Def $"+benchName(0, 0)+"x ", 1)
	require.NotEqual(tb, string(src), edited)
	return srv, p, u, [2]string{string(src), edited}
}

// change applies a full-text change without scheduling work: it is what
// didChange does before the debounce timer fires.
func change(srv *Server, u uri.URI, version int32, text string) {
	srv.didChange(&protocol.DidChangeTextDocumentParams{
		TextDocument:   protocol.VersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: u}, Version: version},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{&protocol.TextDocumentContentChangeWholeDocument{Text: text}},
	})
}

// BenchmarkDebounceToPublish measures what runs when the debounce timer
// fires on the generated 1,500-line project: load with the buffer overlay,
// parse, resolve, lint, compare with HEAD, and publish every changed
// diagnostic set (acceptance criterion 6: at most 50 ms).
func BenchmarkDebounceToPublish(b *testing.B) {
	srv, p, u, texts := benchServer(b)
	version := int32(1)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		version++
		change(srv, u, version, texts[version%2])
		b.StartTimer()
		srv.flush(p)
	}
}

// TestDebounceToPublishBudget reports the median time of the benchmark's
// operation and fails above 50 ms. The race detector slows everything
// several-fold, so the budget is only enforced without it.
func TestDebounceToPublishBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	srv, p, u, texts := benchServer(t)
	var times []time.Duration
	srv.onAnalyze = func(d time.Duration) { times = append(times, d) }
	for i := 0; i < 11; i++ {
		change(srv, u, int32(i+2), texts[(i+1)%2])
		srv.flush(p)
		if i%2 == 0 {
			// The edited text: the renamed definition is new against HEAD
			// (when git is available) and its references in gen2 dangle.
			if p.base != nil && p.base.Spec != nil {
				require.NotEmpty(t, p.snap.notes["gen0.spec.md"])
			}
			require.Contains(t, p.sent, "gen2.spec.md")
		} else {
			require.NotContains(t, p.sent, "gen2.spec.md")
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	median := times[len(times)/2]
	t.Logf("debounce-to-publish on a 1,500-line project: median %v (min %v, max %v)", median, times[0], times[len(times)-1])
	if !raceEnabled {
		require.LessOrEqual(t, median, 50*time.Millisecond)
	}
}
