// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/explain"
	"github.com/jwriteclub/spec/internal/project"
)

// keysOf returns the sorted keys of the JSON object in raw.
func keysOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m), "%s", raw)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sorted(ss ...string) []string {
	sort.Strings(ss)
	return ss
}

var (
	anchorKeys = []string{"file", "form", "hash", "kind", "line", "name", "ref_count", "section", "short_hash", "title"}
	changeKeys = []string{"body_changed", "kind_changed", "name", "new", "old", "renamed_from", "retitled", "state", "trailers"}
)

// TestJSONShapes checks, for every command, that the data object has exactly
// the keys that `spec explain json` documents, and that the topic mentions
// every one of them.
func TestJSONShapes(t *testing.T) {
	topic, _ := explain.Topic("json")
	doc := func(keys ...string) {
		t.Helper()
		for _, k := range keys {
			assert.Contains(t, topic, `"`+k+`"`, "json topic does not document %q", k)
		}
	}

	dir := demoProject(t)
	replaceIn(t, dir, "demo.spec.md", "above zero", "of at least one tick")
	replaceIn(t, dir, "demo.spec.md", "(frame order)", "(ordering)")

	check := func(jr jsonResult, want ...string) map[string]json.RawMessage {
		t.Helper()
		require.Equal(t, 0, jr.code, jr.stdout)
		assert.Equal(t, sorted(want...), keysOf(t, jr.data))
		doc(want...)
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(jr.data, &m))
		return m
	}

	// init (in a fresh directory)
	fresh := t.TempDir()
	check(runJSON(t, fresh, "init", "--name", "fresh"), "name", "root", "files", "claude_md", "next")

	// explain
	check(runJSON(t, dir, "explain"), "topic", "text", "topics")
	check(runJSON(t, dir, "explain", "markup"), "topic", "text", "topics")

	// adopt
	check(runJSON(t, dir, "adopt"), "text", "topics")

	// ls
	m := check(runJSON(t, dir, "ls"), "baseline", "anchors")
	var anchors []json.RawMessage
	require.NoError(t, json.Unmarshal(m["anchors"], &anchors))
	require.NotEmpty(t, anchors)
	assert.Equal(t, sorted(append([]string{"change"}, anchorKeys...)...), keysOf(t, anchors[0]))
	doc(anchorKeys...)
	check(runJSON(t, dir, "ls", "--changed"), "baseline", "anchors")

	// show
	m = check(runJSON(t, dir, "show", "Frame.Timing"), "type", "anchor", "body", "referenced_by", "mentioned_in", "baseline", "change")
	assert.Equal(t, sorted(anchorKeys...), keysOf(t, m["anchor"]))
	assert.Equal(t, sorted("from", "to", "state", "reason"), keysOf(t, m["baseline"]))
	doc("from", "to", "state", "reason")
	assert.Equal(t, sorted(changeKeys...), keysOf(t, m["change"]))
	doc(changeKeys...)
	var side map[string]json.RawMessage
	var ch struct {
		Old json.RawMessage `json:"old"`
	}
	require.NoError(t, json.Unmarshal(m["change"], &ch))
	require.NoError(t, json.Unmarshal(ch.Old, &side))
	assert.Equal(t, sorted("kind", "title", "hash", "short_hash", "file", "line"), keysOf(t, ch.Old))
	var refBy []json.RawMessage
	require.NoError(t, json.Unmarshal(m["referenced_by"], &refBy))
	require.NotEmpty(t, refBy)
	assert.Equal(t, sorted("name", "kind", "file", "line"), keysOf(t, refBy[0]))
	m2 := check(runJSON(t, dir, "show", "Frame.Order"), "type", "anchor", "body", "referenced_by", "mentioned_in", "baseline", "change")
	var ment []json.RawMessage
	require.NoError(t, json.Unmarshal(m2["mentioned_in"], &ment))
	require.NotEmpty(t, ment)
	assert.Equal(t, sorted("file", "line", "kind"), keysOf(t, ment[0]))
	doc("referenced_by", "mentioned_in")

	m = check(runJSON(t, dir, "show", "2"), "type", "section", "anchors")
	assert.Equal(t, sorted("number", "qualified", "title", "level", "file", "line"), keysOf(t, m["section"]))
	doc("number", "qualified", "level")

	// refs
	m = check(runJSON(t, dir, "refs", "Time.Media"), "target", "refs")
	assert.Equal(t, sorted("type", "name", "section"), keysOf(t, m["target"]))
	var refs []json.RawMessage
	require.NoError(t, json.Unmarshal(m["refs"], &refs))
	require.NotEmpty(t, refs)
	assert.Equal(t, sorted("file", "line", "col", "range", "text", "within", "in_title", "state"), keysOf(t, refs[0]))
	doc("in_title", "within", "text")

	// lint
	m = check(runJSON(t, dir, "lint"), "files", "anchors", "sections", "counts")
	assert.Equal(t, sorted("error", "warning", "hint"), keysOf(t, m["counts"]))

	// diff
	m = check(runJSON(t, dir, "diff"), "baseline", "changes", "skipped")
	var changes []json.RawMessage
	require.NoError(t, json.Unmarshal(m["changes"], &changes))
	require.Len(t, changes, 2)
	assert.Equal(t, sorted(changeKeys...), keysOf(t, changes[0]))

	// status
	m = check(runJSON(t, dir, "status"), "state", "baseline", "changed_files", "changes", "skipped", "lint", "trailers", "next")
	assert.Equal(t, sorted("text", "argv"), keysOf(t, m["next"]))
	doc("text", "argv")

	// check
	git(t, dir, "add", "-A")
	m = check(runJSONOK(t, dir, 4, "check", "--staged"), "baseline", "message_file", "changes", "skipped", "required", "present", "missing", "unexpected", "malformed")
	_ = m

	// rename
	m = check(runJSON(t, dir, "rename", "Frame.Order", "Frame.Sequence", "--dry-run"), "from", "to", "dry_run", "count", "files", "edits", "left_alone", "skipped_files", "trailer")
	var files, edits []json.RawMessage
	require.NoError(t, json.Unmarshal(m["files"], &files))
	require.NoError(t, json.Unmarshal(m["edits"], &edits))
	require.NotEmpty(t, files)
	require.NotEmpty(t, edits)
	assert.Equal(t, sorted("file", "count"), keysOf(t, files[0]))
	assert.Equal(t, sorted("file", "line", "col", "role", "in_title"), keysOf(t, edits[0]))
	doc("role", "left_alone")

	// fmt
	m = check(runJSONOK(t, dir, 4, "fmt", "--check"), "check", "written", "files", "unformatted")
	var fmtFiles []json.RawMessage
	require.NoError(t, json.Unmarshal(m["files"], &fmtFiles))
	require.NotEmpty(t, fmtFiles)
	assert.Equal(t, sorted("file", "changed", "refused"), keysOf(t, fmtFiles[0]))
	doc("changed", "refused", "unformatted", "written")

	// doctor
	m = check(runJSON(t, dir, "doctor"), "checks", "inference")
	var checks []json.RawMessage
	require.NoError(t, json.Unmarshal(m["checks"], &checks))
	assert.Len(t, checks, 8)
	assert.Equal(t, sorted("name", "status", "detail", "fix"), keysOf(t, checks[0]))
}

// runJSONOK runs with --json and requires the given exit code.
func runJSONOK(t *testing.T, dir string, code int, args ...string) jsonResult {
	t.Helper()
	jr := runJSON(t, dir, args...)
	require.Equal(t, code, jr.code, jr.stdout)
	if code == 4 {
		jr.code = 0 // let check() inspect the data
	}
	return jr
}

// TestErrorCodes drives every error code of the JSON contract through the
// real command line, in both modes: the exit code, the envelope's error code,
// and (human) the message on stderr with nothing on stdout.
func TestErrorCodes(t *testing.T) {
	requireGit(t)
	demo := demoProject(t)
	empty := t.TempDir()
	notGit := t.TempDir()
	write(t, notGit, "spec.toml", "name = \"x\"\n")
	write(t, notGit, "x.spec.md", baseSpec)
	dupes := t.TempDir()
	write(t, dupes, "spec.toml", "")
	write(t, dupes, "a.spec.md", "# a\n\n## 1. One\n\n**$Dup.Name (first).** The first.\n")
	write(t, dupes, "b.spec.md", "# b\n\n## 1. One\n\n**$Dup.Name (second).** The second.\n")
	lintBad := t.TempDir()
	write(t, lintBad, "spec.toml", "")
	write(t, lintBad, "x.spec.md", "# x\n\nSee $Missing.Name here.\n")
	existing := t.TempDir()
	write(t, existing, "spec.toml", "")
	write(t, existing, "x.spec.md", "# x\n")

	cases := []struct {
		name  string
		dir   string
		args  []string
		exit  int
		code  string
		human string // substring of stderr (exit 1) or stdout (exit 4)
	}{
		{"unknown flag", demo, []string{"lint", "--bogus"}, 1, "usage", "unknown flag"},
		{"unknown command", demo, []string{"bogus"}, 1, "usage", "unknown command"},
		{"missing argument", demo, []string{"show"}, 1, "usage", "usage: spec show"},
		{"too many arguments", demo, []string{"lint", "extra"}, 1, "usage", "too many"},
		{"bad kind", demo, []string{"ls", "--kind", "x"}, 1, "usage", "--kind"},
		{"shell expansion", demo, []string{"show", ".Timing"}, 1, "usage", "shell"},
		{"shell expansion rename", demo, []string{"rename", "Frame.Timing", ".Duration"}, 1, "usage", "shell"},
		{"no project", empty, []string{"lint"}, 1, "no_project", "no spec.toml found"},
		{"no project explicit", empty, []string{"lint", "--project", "nowhere"}, 1, "no_project", "no spec.toml in"},
		{"project exists", existing, []string{"init", "--name", "x"}, 1, "project_exists", "already exists"},
		{"unknown anchor", demo, []string{"show", "Nope.Name"}, 1, "not_found", "no anchor named Nope.Name"},
		{"unknown section", demo, []string{"show", "99"}, 1, "not_found", "no section 99"},
		{"unknown refs", demo, []string{"refs", "Nope.Name"}, 1, "not_found", "Nope.Name"},
		{"unknown topic", demo, []string{"explain", "nope"}, 1, "not_found", "topics:"},
		{"rename unknown", demo, []string{"rename", "Nope.Name", "Other.Name"}, 1, "not_found", "Nope.Name"},
		{"ambiguous anchor", dupes, []string{"show", "Dup.Name"}, 1, "ambiguous", "defined 2 times"},
		{"ambiguous section", dupes, []string{"show", "1"}, 1, "ambiguous", "several files"},
		{"rename ambiguous", dupes, []string{"rename", "Dup.Name", "New.Name"}, 1, "ambiguous", "more than once"},
		{"bad name", demo, []string{"rename", "Frame.Timing", "lower.case"}, 1, "bad_name", "not a valid name"},
		{"name exists", demo, []string{"rename", "Frame.Timing", "Frame.Order"}, 1, "name_exists", "already defined"},
		{"bad ref", demo, []string{"diff", "--against", "nope"}, 1, "bad_ref", "nope"},
		{"no baseline", notGit, []string{"diff"}, 1, "no_baseline", "no baseline"},
		{"lint errors", lintBad, []string{"lint"}, 4, "lint_errors", "ref-dangling"},
		{"status lint errors", lintBad, []string{"status"}, 4, "lint_errors", "ref-dangling"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			jr := runJSON(t, c.dir, c.args...)
			assert.Equal(t, c.exit, jr.code, jr.stdout)
			assert.Equal(t, c.code, jr.errCode)
			if c.exit == 1 && c.code != "no_project" && c.code != "ambiguous" {
				assert.Equal(t, "null", string(jr.data), "data is null on exit 1")
			}
			if c.exit == 4 {
				assert.NotEqual(t, "null", string(jr.data), "data is present on exit 4")
			}

			h := run(t, c.dir, c.args...)
			assert.Equal(t, c.exit, h.code)
			if c.exit == 1 {
				assert.Empty(t, h.stdout, "errors do not print to stdout")
				assert.Contains(t, h.stderr, c.human)
			} else {
				assert.Contains(t, h.stdout, c.human)
			}
		})
	}
}

func TestNoProjectCandidates(t *testing.T) {
	requireGit(t)
	repo := repoWith(t, map[string]string{
		"a/spec.toml": "", "a/x.spec.md": "# x\n",
		"b/c/spec.toml": "", "b/c/y.spec.md": "# y\n",
	})
	jr := runJSON(t, repo, "lint")
	assert.Equal(t, 1, jr.code)
	assert.Equal(t, "no_project", jr.errCode)
	var d struct {
		Candidates []string `json:"candidates"`
	}
	jr.decode(t, &d)
	assert.Equal(t, []string{"a", "b/c"}, d.Candidates)
	h := run(t, repo, "lint")
	assert.Contains(t, h.stderr, "--project")
	assert.Contains(t, h.stderr, "b/c")

	// The listed directory works as --project, relative to the cwd.
	jr = runJSON(t, repo, "lint", "--project", "b/c")
	assert.Equal(t, 0, jr.code, jr.stdout)

	// Suggestions are relative to the cwd also for an explicit --project.
	jr = runJSON(t, filepath.Join(repo, "a"), "lint", "--project", "..")
	jr.decode(t, &d)
	assert.Equal(t, []string{".", "../b/c"}, d.Candidates)
}

func TestCobraFailuresStillProduceEnvelope(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"bogus", "--json"}, {"--json", "lint", "--bogus"}, {"explain", "a", "b", "--json"}} {
		r := runIn(t, isolatedEnv(t), dir, args...)
		jr := decodeEnvelope(t, r, args)
		assert.Equal(t, 1, jr.code)
		assert.Equal(t, "usage", jr.errCode)
	}
	// Without --json the same errors are human text on stderr.
	r := run(t, dir, "bogus")
	assert.Equal(t, 1, r.code)
	assert.Empty(t, r.stdout)
	assert.Contains(t, r.stderr, "unknown command")
}

func TestHelpIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"--help"}, {"lint", "--help"}, {}} {
		r := run(t, dir, args...)
		assert.Equal(t, 0, r.code, "%v", args)
		assert.Contains(t, r.stdout, "spec")
		assert.Empty(t, r.stderr)
	}
}

// TestLockHeld: exit 5 beats exit 1, and read-only commands ignore the lock.
func TestLockHeld(t *testing.T) {
	dir := demoProject(t)
	lock, err := project.AcquireLock(dir, "spec rename Other Thing")
	require.NoError(t, err)
	defer lock.Release()

	// Even a rename that would fail for other reasons reports the lock.
	for _, args := range [][]string{
		{"rename", "Frame.Timing", "Frame.Duration"},
		{"rename", "Nope.Name", "lower"},
	} {
		jr := runJSON(t, dir, args...)
		assert.Equal(t, 5, jr.code, "%v", args)
		assert.Equal(t, "lock_held", jr.errCode)
		var d struct {
			Lock lockJSON `json:"lock"`
		}
		jr.decode(t, &d)
		assert.Equal(t, os.Getpid(), d.Lock.PID)
		assert.Equal(t, "spec rename Other Thing", d.Lock.Command)
		assert.NotEmpty(t, d.Lock.Host)
		assert.NotEmpty(t, d.Lock.Started)
	}
	h := run(t, dir, "rename", "Frame.Timing", "Frame.Duration")
	assert.Equal(t, 5, h.code)
	assert.Contains(t, h.stderr, "spec rename Other Thing")
	assert.Empty(t, h.stdout)
	assert.Contains(t, readFile(t, dir, "demo.spec.md"), "$Frame.Timing", "nothing was renamed")

	// Read-only commands and dry runs are not blocked.
	assert.Equal(t, 0, run(t, dir, "lint").code)
	assert.Equal(t, 0, run(t, dir, "rename", "Frame.Timing", "Frame.Duration", "--dry-run").code)

	require.NoError(t, lock.Release())
	assert.Equal(t, 0, run(t, dir, "rename", "Frame.Timing", "Frame.Duration").code)
	assert.FileExists(t, filepath.Join(dir, ".spec", "lock"), "the lock file persists")
	assert.Equal(t, 0, run(t, dir, "rename", "Frame.Duration", "Frame.Timing").code, "released after the rename")
}

func TestReadOnlyCommandsNeverCreateDotSpec(t *testing.T) {
	dir := demoProject(t)
	for _, args := range [][]string{{"ls"}, {"lint"}, {"status"}, {"diff"}, {"show", "Frame.Timing"}, {"refs", "Frame.Timing"},
		{"check", "--staged"}, {"doctor"}, {"rename", "Frame.Timing", "Frame.Duration", "--dry-run"}} {
		run(t, dir, args...)
		assert.NoDirExists(t, filepath.Join(dir, ".spec"), "%v", args)
	}
}

// TestRenameConcurrentEdit: a file that changes between loading and writing
// aborts the rename without writing anything.
func TestRenameConcurrentEdit(t *testing.T) {
	dir := demoProject(t)
	before := readFile(t, dir, "demo.spec.md")
	edited := before + "\nAn edit made while the rename ran.\n"
	hook := func() { write(t, dir, "demo.spec.md", edited) }
	var out, errb bytes.Buffer
	code := Run([]string{"rename", "Frame.Timing", "Frame.Duration", "--json"},
		Env{Stdout: &out, Stderr: &errb, Cwd: dir, Getenv: isolatedEnv(t).get, beforeCommit: hook})
	jr := decodeEnvelope(t, result{code: code, stdout: out.String(), stderr: errb.String()}, nil)
	assert.Equal(t, 1, jr.code)
	assert.Equal(t, "concurrent_edit", jr.errCode)
	assert.Equal(t, edited, readFile(t, dir, "demo.spec.md"), "nothing was written; the other edit survives")

	// The human form says what happened.
	out.Reset()
	errb.Reset()
	code = Run([]string{"rename", "Frame.Timing", "Frame.Duration"},
		Env{Stdout: &out, Stderr: &errb, Cwd: dir, Getenv: isolatedEnv(t).get, beforeCommit: func() { write(t, dir, "demo.spec.md", before) }})
	assert.Equal(t, 1, code)
	assert.Contains(t, errb.String(), "changed while spec rename was running")
	assert.Empty(t, out.String())
}

// TestRenameLeavesOpaqueAlone: occurrences in code are reported, not rewritten.
func TestRenameLeavesOpaqueAlone(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec.toml", "")
	src := "# x\n\n**$Old.Name (old).** It MUST hold, see `$Old.Name` in code. Checked by: T.\n\nRefers $Old.Name.\n\n    $Old.Name in an indented block\n"
	write(t, dir, "x.spec.md", src)
	jr := runJSON(t, dir, "rename", "Old.Name", "New.Name")
	require.Equal(t, 0, jr.code, jr.stdout)
	var d renameData
	jr.decode(t, &d)
	assert.Equal(t, 2, d.Count)
	require.Len(t, d.LeftAlone, 2)
	assert.Equal(t, "code_span", d.LeftAlone[0].Kind)
	assert.Equal(t, "code_block", d.LeftAlone[1].Kind)
	assert.Equal(t, 3, d.LeftAlone[0].Line)
	assert.Equal(t, []string{"definition", "reference"}, []string{d.Edits[0].Role, d.Edits[1].Role})
	want := "# x\n\n**$New.Name (old).** It MUST hold, see `$Old.Name` in code. Checked by: T.\n\nRefers $New.Name.\n\n    $Old.Name in an indented block\n"
	assert.Equal(t, want, readFile(t, dir, "x.spec.md"))
	h := run(t, dir, "rename", "New.Name", "Old.Name", "--dry-run")
	assert.Equal(t, 0, h.code)
	assert.Contains(t, h.stdout, "would rename $New.Name -> $Old.Name")
}

// TestDiagnosticsPositions checks line/col and ranges in diagnostics.
func TestDiagnosticsPositions(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec.toml", "name = \"x\"\nbogus = 1\n")
	write(t, dir, "x.spec.md", "# x\n\n**$Aa.Bb (b).** Uses $Cc.Dd and é $Ee.Ff here.\n")
	jr := runJSON(t, dir, "lint")
	assert.Equal(t, 4, jr.code)
	byCode := map[string][]map[string]any{}
	for _, d := range jr.diagnostics {
		byCode[d["code"].(string)] = append(byCode[d["code"].(string)], d)
	}
	dangling := byCode["ref-dangling"]
	require.Len(t, dangling, 2)
	assert.Equal(t, "x.spec.md", dangling[0]["file"])
	assert.EqualValues(t, 3, dangling[0]["line"])
	assert.EqualValues(t, 22, dangling[0]["col"])
	assert.Equal(t, "error", dangling[0]["severity"])
	// The second reference follows a two-byte character: the column counts bytes.
	assert.EqualValues(t, 3, dangling[1]["line"])
	assert.EqualValues(t, 36, dangling[1]["col"])
	rng := dangling[0]["range"].(map[string]any)
	src := readFile(t, dir, "x.spec.md")
	assert.Equal(t, "$Cc.Dd", src[int(rng["start"].(float64)):int(rng["end"].(float64))])
	assert.NotNil(t, dangling[0]["related"])

	cfg := byCode["config-invalid"]
	require.Len(t, cfg, 1)
	assert.Equal(t, "spec.toml", cfg[0]["file"])
	assert.EqualValues(t, 2, cfg[0]["line"])

	h := run(t, dir, "lint")
	assert.Contains(t, h.stdout, "x.spec.md:3:22: error: ")
	assert.Contains(t, h.stdout, "[ref-dangling]")
	assert.Contains(t, h.stdout, "spec.toml:2:")
}

func TestDuplicateDefinitionRelated(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec.toml", "")
	write(t, dir, "a.spec.md", "# a\n\n**$Dup.Name (one).** First.\n\n**$Dup.Name (two).** Second.\n")
	jr := runJSON(t, dir, "lint")
	assert.Equal(t, 4, jr.code)
	var found bool
	for _, d := range jr.diagnostics {
		if d["code"] == "def-duplicate" {
			rel, _ := d["related"].([]any)
			if len(rel) > 0 {
				r := rel[0].(map[string]any)
				assert.Equal(t, "a.spec.md", r["file"])
				assert.NotZero(t, r["line"])
				found = true
			}
		}
	}
	assert.True(t, found, "def-duplicate carries related locations: %v", jr.diagnostics)
	// Duplicated names block the diff and status.
	requireGit(t)
	gitInit(t, dir)
	commitAll(t, dir, "dupes")
	st := runJSON(t, dir, "status")
	assert.Equal(t, 4, st.code)
	var d statusData
	st.decode(t, &d)
	assert.Equal(t, []string{"$Dup.Name"}, d.Skipped)
	df := runJSON(t, dir, "diff")
	assert.Equal(t, 0, df.code, "diff exits 0 whatever it finds")
}

func TestLintExitCodes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec.toml", "")
	// A definition nothing references: a warning, so exit 0.
	write(t, dir, "x.spec.md", "# x\n\n**Def $Lone.Term (lone).** Nothing uses this.\n")
	jr := runJSON(t, dir, "lint")
	assert.Equal(t, 0, jr.code)
	require.Len(t, jr.diagnostics, 1)
	assert.Equal(t, "def-unreferenced", jr.diagnostics[0]["code"])
	assert.Equal(t, "warning", jr.diagnostics[0]["severity"])
	h := run(t, dir, "lint")
	assert.Equal(t, 0, h.code)
	assert.Contains(t, h.stdout, "warning:")
	assert.Empty(t, h.stderr)

	// spec.toml can promote it to an error (exit 4) or silence it.
	write(t, dir, "spec.toml", "[lint]\ndef-unreferenced = \"error\"\n")
	assert.Equal(t, 4, run(t, dir, "lint").code)
	write(t, dir, "spec.toml", "[lint]\ndef-unreferenced = \"off\"\n")
	jr = runJSON(t, dir, "lint")
	assert.Equal(t, 0, jr.code)
	assert.Empty(t, jr.diagnostics)
	// ls --unreferenced still lists it.
	var d lsData
	runJSON(t, dir, "ls", "--unreferenced").decode(t, &d)
	assert.Len(t, d.Anchors, 1)
}

func TestDoctor(t *testing.T) {
	dir := demoProject(t)
	env := isolatedEnv(t)

	jr := runJSONEnv(t, env, dir, "doctor")
	require.Equal(t, 0, jr.code, jr.stdout)
	var d doctorData
	jr.decode(t, &d)
	status := map[string]string{}
	var names []string
	for _, c := range d.Checks {
		status[c.Name] = c.Status
		names = append(names, c.Name)
		if c.Status == "warn" || c.Status == "fail" {
			assert.NotEmpty(t, c.Fix, c.Name)
		}
	}
	assert.Equal(t, []string{"project", "files", "lint", "git", "user_config", "provider_key", "inference", "spell"}, names)
	assert.Equal(t, map[string]string{"project": "ok", "files": "ok", "lint": "ok", "git": "ok", "user_config": "warn", "provider_key": "skipped", "inference": "skipped", "spell": "ok"}, status)
	assert.Equal(t, "no provider configured", d.Checks[6].Detail)
	assert.Nil(t, d.Inference)

	// A valid user config whose key variable is unset warns; set, it is ok.
	cfg := filepath.Join(env["XDG_CONFIG_HOME"], "spec", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfg), 0o755))
	srv := fakeProvider(t, okReply("OK"))
	require.NoError(t, os.WriteFile(cfg, []byte("default = \"p\"\n[providers.p]\nbase_url = \""+srv.URL+"/v1\"\nmodel = \"m\"\napi_key_env = \"SPEC_TEST_KEY\"\n"), 0o644))
	env["SPEC_TEST_KEY"] = ""
	jr = runJSONEnv(t, env, dir, "doctor")
	require.Equal(t, 0, jr.code, jr.stdout)
	jr.decode(t, &d)
	assert.Equal(t, "ok", d.Checks[4].Status)
	assert.Equal(t, "warn", d.Checks[5].Status)
	assert.Contains(t, d.Checks[5].Fix, "SPEC_TEST_KEY")
	env["SPEC_TEST_KEY"] = "secret"
	jr = runJSONEnv(t, env, dir, "doctor")
	jr.decode(t, &d)
	assert.Equal(t, "ok", d.Checks[5].Status)
	assert.Equal(t, "ok", d.Checks[6].Status, d.Checks[6].Detail)
	assert.NotContains(t, jr.stdout, "secret", "the key is never printed")

	// An invalid user config fails the check and the command.
	require.NoError(t, os.WriteFile(cfg, []byte("default = \"missing\"\n"), 0o644))
	jr = runJSONEnv(t, env, dir, "doctor")
	assert.Equal(t, 1, jr.code)
	assert.Equal(t, "doctor_failed", jr.errCode)
	jr.decode(t, &d)
	assert.Equal(t, "fail", d.Checks[4].Status)
	assert.Len(t, d.Checks, 8, "data carries the whole result")
	h := runIn(t, env, dir, "doctor")
	assert.Equal(t, 1, h.code)
	assert.Contains(t, h.stdout, "fail")
	assert.Contains(t, h.stdout, "fix:")
	assert.Contains(t, h.stderr, "doctor check failed")

	// No project: the project check fails but the rest still reports.
	jr = runJSONEnv(t, isolatedEnv(t), t.TempDir(), "doctor")
	assert.Equal(t, 1, jr.code)
	assert.Equal(t, "doctor_failed", jr.errCode)
	jr.decode(t, &d)
	assert.Equal(t, "fail", d.Checks[0].Status)
	assert.Equal(t, "skipped", d.Checks[1].Status)
	assert.Len(t, d.Checks, 8)
}

func TestExplainCommand(t *testing.T) {
	dir := t.TempDir()
	idx := runJSON(t, dir, "explain")
	require.Equal(t, 0, idx.code)
	var d explainData
	idx.decode(t, &d)
	assert.Nil(t, d.Topic)
	assert.Equal(t, explain.Names(), d.Topics)
	assert.Equal(t, explain.Index(), d.Text)
	assert.Empty(t, idx.diagnostics)
	for _, n := range explain.Names() {
		jr := runJSON(t, dir, "explain", n)
		require.Equal(t, 0, jr.code, n)
		jr.decode(t, &d)
		require.NotNil(t, d.Topic)
		assert.Equal(t, n, *d.Topic)
		want, _ := explain.Topic(n)
		assert.Equal(t, want, d.Text)
		h := run(t, dir, "explain", n)
		assert.Equal(t, want, h.stdout)
	}
	// Needs no project and no git.
	assert.Equal(t, 0, run(t, dir, "explain", "json").code)
}

func TestLsFiltersAndShowDetails(t *testing.T) {
	dir := demoProject(t)
	var d lsData
	runJSON(t, dir, "ls", "--kind", "def").decode(t, &d)
	require.Len(t, d.Anchors, 1)
	assert.Equal(t, "$Time.Media", d.Anchors[0].Name)
	assert.Equal(t, 2, d.Anchors[0].RefCount)
	assert.Equal(t, "§demo:1", *d.Anchors[0].Section)

	runJSON(t, dir, "ls", "--unreferenced").decode(t, &d)
	var names []string
	for _, a := range d.Anchors {
		names = append(names, a.Name)
	}
	assert.Equal(t, []string{"$Extra.Limit"}, names)

	h := run(t, dir, "ls")
	lines := strings.Split(strings.TrimSpace(h.stdout), "\n")
	require.Len(t, lines, 4)
	assert.Regexp(t, `^def +Time\.Media +media time +demo\.spec\.md:5 +[0-9a-f]{8} +2 refs$`, lines[0])
	assert.Regexp(t, `^inv +Extra\.Limit +extra limit +other\.spec\.md:5 +[0-9a-f]{8} +0 refs$`, lines[3])

	sh := run(t, dir, "show", "Frame.Timing")
	assert.Equal(t, 0, sh.code)
	assert.Contains(t, sh.stdout, "referenced by:\n  Frame.Order  demo.spec.md:")
	assert.Contains(t, sh.stdout, "in §demo:2")
	assert.Contains(t, sh.stdout, "mentioned in: no prose outside anchor bodies")

	rf := run(t, dir, "refs", "Frame.Order")
	assert.Equal(t, 0, rf.code)
	assert.Regexp(t, `^demo\.spec\.md:15:21: Prose that mentions \$Frame\.Order for the reader\.\n$`, rf.stdout)

	// A name that is only referenced still lists its (dangling) references.
	write(t, dir, "demo.spec.md", readFile(t, dir, "demo.spec.md")+"\nSee $Ghost.Name.\n")
	var rd refsData
	jr := runJSON(t, dir, "refs", "Ghost.Name")
	require.Equal(t, 0, jr.code)
	jr.decode(t, &rd)
	require.Len(t, rd.Refs, 1)
	assert.Equal(t, "dangling", rd.Refs[0].State)
	assert.Nil(t, rd.Refs[0].Within)
	assert.Equal(t, "See $Ghost.Name.", rd.Refs[0].Text)

	// Section show lists the anchors inside.
	var sd showSectionData
	runJSON(t, dir, "show", "demo:2").decode(t, &sd)
	require.Len(t, sd.Anchors, 2)
	assert.Equal(t, "$Frame.Timing", sd.Anchors[0].Name)
}

// TestBinary builds the real binary and checks exit codes and streams
// through os.Exit.
func TestBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "spec")
	build := execCommand(t, "../..", "go", "build", "-o", bin, "./cmd/spec")
	require.NoError(t, build.Run(), "go build")

	dir := demoProject(t)
	runBin := func(args ...string) (int, string, string) {
		cmd := execCommand(t, dir, bin, args...)
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		err := cmd.Run()
		code := 0
		if err != nil {
			ee, ok := err.(interface{ ExitCode() int })
			require.True(t, ok, "%v", err)
			code = ee.ExitCode()
		}
		return code, o.String(), e.String()
	}
	code, out, errs := runBin("lint")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "0 errors")
	assert.Empty(t, errs)

	code, out, errs = runBin("show", ".Timing")
	assert.Equal(t, 1, code)
	assert.Empty(t, out)
	assert.Contains(t, errs, "shell")

	replaceIn(t, dir, "demo.spec.md", "see $Frame.Timing", "see $Frame.Nothing")
	code, out, _ = runBin("lint", "--json")
	assert.Equal(t, 4, code)
	assert.Contains(t, out, `"lint_errors"`)
}

// TestRenameReportsSkippedFiles: spec files that are not valid UTF-8 are
// listed as skipped, with a warning when they contain the old name.
func TestRenameReportsSkippedFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec.toml", "")
	write(t, dir, "x.spec.md", "# x\n\n**$Old.Name (old).** It MUST hold. Checked by: T.\n")
	write(t, dir, "bad.spec.md", "# bad\n\nRefers $Old.Name \xff.\n")
	write(t, dir, "odd.spec.md", "# odd\n\nNothing \xfe.\n")
	jr := runJSON(t, dir, "rename", "Old.Name", "New.Name", "--dry-run")
	require.Equal(t, 0, jr.code, jr.stdout)
	var d renameData
	jr.decode(t, &d)
	assert.Equal(t, []renameSkipped{
		{File: "bad.spec.md", Reason: "not valid UTF-8", MayReference: true},
		{File: "odd.spec.md", Reason: "not valid UTF-8", MayReference: false},
	}, d.SkippedFiles)

	h := run(t, dir, "rename", "Old.Name", "New.Name")
	require.Equal(t, 0, h.code, h.stderr)
	assert.Contains(t, h.stdout, "skipped bad.spec.md: not valid UTF-8; it contains Old.Name and may hold references that were not rewritten")
	assert.Contains(t, h.stdout, "skipped odd.spec.md: not valid UTF-8\n")
	assert.Contains(t, readFile(t, dir, "bad.spec.md"), "$Old.Name", "the skipped file is untouched")
}
