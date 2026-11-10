// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const baseSpec = `# demo

## 1. Terms

**Def $Time.Media (media time).** Media time is the timestamp carried by a frame, in ticks of 1/90000 s.

## 2. Rules

**$Frame.Timing (fragment timing).** Every fragment MUST have a duration above zero in $Time.Media.
Checked by: TestFragment.

**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, see $Frame.Timing.
Checked by: TestOrder.

Prose that mentions $Frame.Order for the reader.
`

const otherSpec = `# other

## 1. Extras

**$Extra.Limit (extra limit).** The extra queue MUST hold at most 8 items. Checked by: TestLimit.
`

// demoProject is a committed repository with baseSpec and otherSpec.
func demoProject(t *testing.T) string {
	t.Helper()
	return repoWith(t, map[string]string{
		"spec.toml":       "name = \"demo\"\n",
		"demo.spec.md":    baseSpec,
		"other.spec.md":   otherSpec,
		"notes/README.md": "not a spec\n",
	})
}

// replaceIn edits dir/rel, requiring that old occurs.
func replaceIn(t *testing.T, dir, rel, old, new string) {
	t.Helper()
	src := readFile(t, dir, rel)
	require.Contains(t, src, old)
	write(t, dir, rel, strings.Replace(src, old, new, 1))
}

func anchorsOf(t *testing.T, dir string, extra ...string) map[string]anchorJSON {
	t.Helper()
	jr := runJSON(t, dir, append([]string{"ls"}, extra...)...)
	require.Equal(t, 0, jr.code, jr.stdout)
	var d lsData
	jr.decode(t, &d)
	m := map[string]anchorJSON{}
	for _, a := range d.Anchors {
		m[a.Name] = a.anchorJSON
	}
	return m
}

func statusOf(t *testing.T, dir string, args ...string) (jsonResult, statusData) {
	t.Helper()
	jr := runJSON(t, dir, append([]string{"status"}, args...)...)
	var d statusData
	jr.decode(t, &d)
	return jr, d
}

func changeStates(d statusData) map[string]string {
	m := map[string]string{}
	for _, c := range d.Changes {
		m[c.Name] = c.State
	}
	return m
}

func TestStatusScenarios(t *testing.T) {
	requireGit(t)
	type anchors = map[string]anchorJSON
	cases := []struct {
		name     string
		edit     func(t *testing.T, dir string)
		state    string
		states   map[string]string
		trailers func(before, after anchors) []string
		files    []string
	}{
		{
			name:     "clean",
			edit:     func(t *testing.T, dir string) {},
			state:    "clean",
			states:   map[string]string{},
			trailers: func(b, a anchors) []string { return []string{} },
			files:    []string{},
		},
		{
			name: "prose only",
			edit: func(t *testing.T, dir string) {
				replaceIn(t, dir, "demo.spec.md", "Prose that mentions", "Some more prose that mentions")
			},
			state:    "prose_only",
			states:   map[string]string{},
			trailers: func(b, a anchors) []string { return []string{} },
			files:    []string{"demo.spec.md"},
		},
		{
			name: "reflow is not a change",
			edit: func(t *testing.T, dir string) {
				replaceIn(t, dir, "demo.spec.md", "Every fragment MUST have a duration above zero in $Time.Media.\nChecked by: TestFragment.",
					"Every fragment MUST\nhave a duration   above zero in $Time.Media.\nChecked by: TestFragment.")
			},
			state:    "prose_only",
			states:   map[string]string{},
			trailers: func(b, a anchors) []string { return []string{} },
			files:    []string{"demo.spec.md"},
		},
		{
			name: "changed",
			edit: func(t *testing.T, dir string) {
				replaceIn(t, dir, "demo.spec.md", "above zero", "of at least one tick")
			},
			state:  "anchors_changed",
			states: map[string]string{"$Frame.Timing": "changed"},
			trailers: func(b, a anchors) []string {
				return []string{"Spec-Change: $Frame.Timing inv:" + b["$Frame.Timing"].ShortHash + " -> inv:" + a["$Frame.Timing"].ShortHash}
			},
			files: []string{"demo.spec.md"},
		},
		{
			name: "added",
			edit: func(t *testing.T, dir string) {
				replaceIn(t, dir, "demo.spec.md", "Prose that mentions", "**$Frame.Gap (gap).** A gap MUST NOT exceed 5 s, measured in $Time.Media. Checked by: TestGap.\n\nProse that mentions")
			},
			state:  "anchors_changed",
			states: map[string]string{"$Frame.Gap": "added"},
			trailers: func(b, a anchors) []string {
				return []string{"Spec-Change: $Frame.Gap - -> inv:" + a["$Frame.Gap"].ShortHash}
			},
			files: []string{"demo.spec.md"},
		},
		{
			name: "removed",
			edit: func(t *testing.T, dir string) {
				replaceIn(t, dir, "demo.spec.md", "**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, see $Frame.Timing.\nChecked by: TestOrder.\n\nProse that mentions $Frame.Order for the reader.\n", "")
			},
			state:  "anchors_changed",
			states: map[string]string{"$Frame.Order": "removed"},
			trailers: func(b, a anchors) []string {
				return []string{"Spec-Change: $Frame.Order inv:" + b["$Frame.Order"].ShortHash + " -> -"}
			},
			files: []string{"demo.spec.md"},
		},
		{
			name: "kind changed",
			edit: func(t *testing.T, dir string) {
				replaceIn(t, dir, "demo.spec.md", "**$Frame.Order (frame order).**", "**Def $Frame.Order (frame order).**")
			},
			state:  "anchors_changed",
			states: map[string]string{"$Frame.Order": "kind_changed"},
			trailers: func(b, a anchors) []string {
				h := b["$Frame.Order"].ShortHash
				return []string{"Spec-Change: $Frame.Order inv:" + h + " -> def:" + a["$Frame.Order"].ShortHash}
			},
			files: []string{"demo.spec.md"},
		},
		{
			name: "retitled",
			edit: func(t *testing.T, dir string) {
				replaceIn(t, dir, "demo.spec.md", "(frame order)", "(ordering)")
			},
			state:    "prose_only",
			states:   map[string]string{"$Frame.Order": "retitled"},
			trailers: func(b, a anchors) []string { return []string{} },
			files:    []string{"demo.spec.md"},
		},
		{
			name: "moved between files",
			edit: func(t *testing.T, dir string) {
				const para = "**$Extra.Limit (extra limit).** The extra queue MUST hold at most 8 items. Checked by: TestLimit.\n"
				replaceIn(t, dir, "other.spec.md", para, "")
				replaceIn(t, dir, "demo.spec.md", "Prose that mentions", para+"\nProse that mentions")
			},
			state:    "prose_only",
			states:   map[string]string{},
			trailers: func(b, a anchors) []string { return []string{} },
			files:    []string{"demo.spec.md", "other.spec.md"},
		},
		{
			name: "deleted spec file",
			edit: func(t *testing.T, dir string) {
				require.NoError(t, os.Remove(filepath.Join(dir, "other.spec.md")))
			},
			state:  "anchors_changed",
			states: map[string]string{"$Extra.Limit": "removed"},
			trailers: func(b, a anchors) []string {
				return []string{"Spec-Change: $Extra.Limit inv:" + b["$Extra.Limit"].ShortHash + " -> -"}
			},
			files: []string{"other.spec.md"},
		},
		{
			name: "untracked new spec file",
			edit: func(t *testing.T, dir string) {
				write(t, dir, "new.spec.md", "# new\n\n**$Fresh.Rule (fresh).** Fresh things MUST be tested in $Time.Media. Checked by: TestFresh.\n")
			},
			state:  "anchors_changed",
			states: map[string]string{"$Fresh.Rule": "added"},
			trailers: func(b, a anchors) []string {
				return []string{"Spec-Change: $Fresh.Rule - -> inv:" + a["$Fresh.Rule"].ShortHash}
			},
			files: []string{"new.spec.md"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := demoProject(t)
			before := anchorsOf(t, dir)
			c.edit(t, dir)
			after := anchorsOf(t, dir)

			jr, d := statusOf(t, dir)
			require.Equal(t, 0, jr.code, jr.stdout)
			assert.Equal(t, c.state, d.State)
			assert.Equal(t, c.states, changeStates(d))
			assert.Equal(t, c.trailers(before, after), d.Trailers)
			assert.Equal(t, c.files, d.ChangedFiles)
			assert.Equal(t, "ok", d.Baseline.State)
			assert.Equal(t, "HEAD", d.Baseline.From)
			assert.Equal(t, "worktree", d.Baseline.To)
			assert.Empty(t, jr.diagnostics)
			for _, ch := range d.Changes {
				switch ch.State {
				case "added":
					assert.Nil(t, ch.Old)
					assert.NotNil(t, ch.New)
				case "removed":
					assert.NotNil(t, ch.Old)
					assert.Nil(t, ch.New)
				}
			}

			// The human form prints the same lines under "next:".
			h := run(t, dir, "status")
			assert.Equal(t, 0, h.code, h.stderr)
			assert.Contains(t, h.stdout, "state: "+c.state)
			for _, l := range c.trailers(before, after) {
				assert.Contains(t, h.stdout, "\n"+l+"\n")
			}
			assert.Contains(t, h.stdout, "\nnext: ")

			// diff agrees with status.
			df := runJSON(t, dir, "diff")
			require.Equal(t, 0, df.code)
			var dd diffData
			df.decode(t, &dd)
			assert.Equal(t, d.Changes, dd.Changes)
		})
	}
}

func TestStatusRename(t *testing.T) {
	dir := demoProject(t)
	r := runJSON(t, dir, "rename", "Frame.Timing", "Frame.Duration")
	require.Equal(t, 0, r.code, r.stdout)
	_, d := statusOf(t, dir)
	assert.Equal(t, "anchors_changed", d.State)
	assert.Equal(t, map[string]string{"$Frame.Duration": "renamed"}, changeStates(d), "one renamed and no referencer changed")
	assert.Equal(t, []string{"Spec-Rename: $Frame.Timing -> $Frame.Duration"}, d.Trailers)
	require.NotNil(t, d.Changes[0].RenamedFrom)
	assert.Equal(t, "$Frame.Timing", *d.Changes[0].RenamedFrom)
	assert.Equal(t, "the changed anchors are renames; commit them with the Spec-Rename lines above", d.Next.Text,
		"a rename is no applied proposal")

	// Rename plus a reworded body is a removal and an addition.
	replaceIn(t, dir, "demo.spec.md", "above zero", "of at least one tick")
	before := anchorsOf(t, dir)
	_, d = statusOf(t, dir)
	// The referencing invariant now names a different, unrelated anchor, so
	// its body changed too.
	assert.Equal(t, map[string]string{"$Frame.Timing": "removed", "$Frame.Duration": "added", "$Frame.Order": "changed"}, changeStates(d))
	assert.Len(t, d.Trailers, 3)
	for _, l := range d.Trailers {
		assert.True(t, strings.HasPrefix(l, "Spec-Change: "), l)
	}
	assert.Contains(t, d.Trailers, "Spec-Change: $Frame.Duration - -> inv:"+before["$Frame.Duration"].ShortHash)
}

// TestAccountedText: the next: line names renames as renames and only the
// other accounted changes as applied proposals.
func TestAccountedText(t *testing.T) {
	renamed := changeJSON{Name: "$Light.Car", State: "renamed"}
	renamedEdited := changeJSON{Name: "$Light.Walk", State: "renamed", BodyChanged: true}
	changed := changeJSON{Name: "$Car.Go", State: "changed", BodyChanged: true}
	retitled := changeJSON{Name: "$Button", State: "retitled", Retitled: true}
	assert.Equal(t, "the changed anchors are renames; commit them with the Spec-Rename lines above",
		accountedText([]changeJSON{renamed, retitled}))
	assert.Equal(t, "the changed anchors are applied proposals; commit them with the trailer lines above",
		accountedText([]changeJSON{changed, renamedEdited}))
	assert.Equal(t, "the renamed anchors need only their Spec-Rename lines and the other changed anchors are applied proposals; commit them with the trailer lines above",
		accountedText([]changeJSON{renamed, changed}))
}

func TestStatusIdenticalBodiesAreNotARename(t *testing.T) {
	requireGit(t)
	body := "Every widget MUST be labelled before it ships."
	dir := repoWith(t, map[string]string{
		"spec.toml": "name = \"pairs\"\n",
		"p.spec.md": "# p\n\n**$Pair.One (one).** " + body + "\n\n**$Pair.Two (two).** " + body + "\n\nSee $Pair.One and $Pair.Two.\n",
	})
	write(t, dir, "p.spec.md", "# p\n\n**$Pair.Three (three).** "+body+"\n\n**$Pair.Four (four).** "+body+"\n\nSee $Pair.Three and $Pair.Four.\n")
	_, d := statusOf(t, dir)
	assert.Equal(t, map[string]string{
		"$Pair.One": "removed", "$Pair.Two": "removed", "$Pair.Three": "added", "$Pair.Four": "added",
	}, changeStates(d), "no false rename between identical bodies")
	assert.Len(t, d.Trailers, 4)
}

func TestStatusUnbornRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	gitInit(t, dir)
	write(t, dir, "spec.toml", "name = \"fresh\"\n")
	write(t, dir, "fresh.spec.md", baseSpec)
	jr, d := statusOf(t, dir)
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Equal(t, "anchors_changed", d.State)
	assert.Equal(t, "unborn", d.Baseline.State)
	assert.Equal(t, "no commits yet", d.Baseline.Reason)
	assert.Equal(t, map[string]string{"$Time.Media": "added", "$Frame.Timing": "added", "$Frame.Order": "added"}, changeStates(d))
	assert.Len(t, d.Trailers, 3)
	h := run(t, dir, "status")
	assert.Contains(t, h.stdout, "no commits yet")

	// diff and check work on the empty baseline.
	df := runJSON(t, dir, "diff")
	assert.Equal(t, 0, df.code)
	git(t, dir, "add", "-A")
	msg := filepath.Join(t.TempDir(), "msg")
	require.NoError(t, os.WriteFile(msg, []byte("first\n\n"+strings.Join(d.Trailers, "\n")+"\n"), 0o644))
	ck := runJSON(t, dir, "check", "--message-file", msg)
	assert.Equal(t, 0, ck.code, ck.stdout)
	require.NoError(t, os.WriteFile(msg, []byte("first\n"), 0o644))
	ck = runJSON(t, dir, "check", "--message-file", msg)
	assert.Equal(t, 4, ck.code)
	var cd checkData
	ck.decode(t, &cd)
	assert.ElementsMatch(t, d.Trailers, cd.Missing)
}

func TestStatusProjectInSubdirectory(t *testing.T) {
	requireGit(t)
	dir := repoWith(t, map[string]string{
		"README.md":                "top\n",
		"docs/proj/spec.toml":      "name = \"sub\"\n",
		"docs/proj/sub.spec.md":    baseSpec,
		"docs/proj/more/x.spec.md": otherSpec,
		"elsewhere.spec.md":        "# not in the project\n\n**$Out.Side (out).** Outside MUST NOT matter here.\n",
	})
	proj := filepath.Join(dir, "docs", "proj")
	replaceIn(t, proj, "sub.spec.md", "above zero", "of at least one tick")
	before := map[string]string{"short": ""}
	_ = before

	for _, c := range []struct{ cwd, project string }{
		{proj, ""},
		{filepath.Join(proj, "more"), ""},
		{dir, "docs/proj"},
	} {
		args := []string{"status"}
		if c.project != "" {
			args = append(args, "--project", c.project)
		}
		jr, d := statusOf(t, c.cwd, args[1:]...)
		require.Equal(t, 0, jr.code, "cwd %s: %s", c.cwd, jr.stdout)
		assert.Equal(t, "anchors_changed", d.State)
		assert.Equal(t, map[string]string{"$Frame.Timing": "changed"}, changeStates(d))
		assert.Equal(t, []string{"sub.spec.md"}, d.ChangedFiles, "paths are project-relative")
	}
}

func TestNotAGitRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec.toml", "name = \"x\"\n")
	write(t, dir, "x.spec.md", baseSpec)

	jr, d := statusOf(t, dir)
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Equal(t, "no_baseline", d.State)
	assert.Equal(t, "unavailable", d.Baseline.State)
	assert.NotEmpty(t, d.Baseline.Reason)
	assert.Empty(t, d.Changes)
	assert.Empty(t, d.Trailers)
	h := run(t, dir, "status")
	assert.Equal(t, 0, h.code)
	assert.Contains(t, h.stdout, "state: no_baseline")

	for _, args := range [][]string{{"diff"}, {"diff", "--staged"}, {"ls", "--changed"}, {"check"}, {"check", "--staged"}} {
		r := runJSON(t, dir, args...)
		assert.Equal(t, 1, r.code, "%v", args)
		assert.Equal(t, "no_baseline", r.errCode, "%v", args)
		assert.Equal(t, "null", string(r.data))
	}
	r := run(t, dir, "diff")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "no baseline")

	// Plain ls and show still work and say the baseline is unavailable.
	sh := runJSON(t, dir, "show", "Frame.Timing")
	require.Equal(t, 0, sh.code)
	var sd showAnchorData
	sh.decode(t, &sd)
	assert.Equal(t, "unavailable", sd.Baseline.State)
	assert.Nil(t, sd.Change)

	// Exit 1 beats exit 4: a missing baseline is reported even when lint
	// would fail.
	replaceIn(t, dir, "x.spec.md", "see $Frame.Timing", "see $Frame.Nothing")
	assert.Equal(t, 4, run(t, dir, "lint").code)
	assert.Equal(t, 1, run(t, dir, "check").code)
	assert.Equal(t, 4, run(t, dir, "status").code, "status still lints without a baseline")
}

func TestBadRef(t *testing.T) {
	dir := demoProject(t)
	for _, args := range [][]string{{"diff", "--against", "no-such-ref"}, {"diff", "--against", "-x"}} {
		r := runJSON(t, dir, args...)
		assert.Equal(t, 1, r.code, "%v", args)
		assert.Equal(t, "bad_ref", r.errCode)
		assert.Equal(t, "null", string(r.data))
	}
	r := run(t, dir, "diff", "--against", "no-such-ref")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "no-such-ref")
}

func TestDiffAgainstAndStaged(t *testing.T) {
	dir := demoProject(t)
	replaceIn(t, dir, "demo.spec.md", "above zero", "of at least one tick")
	commitAll(t, dir, "change timing")
	replaceIn(t, dir, "other.spec.md", "at most 8", "at most 9")

	// Against HEAD~1 the committed change shows; HEAD sees only the new edit.
	var d diffData
	df := runJSON(t, dir, "diff", "--against", "HEAD~1")
	df.decode(t, &d)
	assert.Equal(t, "HEAD~1", d.Baseline.From)
	assert.Equal(t, map[string]string{"$Frame.Timing": "changed", "$Extra.Limit": "changed"}, changeMap(d.Changes))
	df = runJSON(t, dir, "diff")
	df.decode(t, &d)
	assert.Equal(t, map[string]string{"$Extra.Limit": "changed"}, changeMap(d.Changes))

	// Nothing is staged yet; stage the edit and the index side shows it.
	df = runJSON(t, dir, "diff", "--staged")
	df.decode(t, &d)
	assert.Equal(t, "index", d.Baseline.To)
	assert.Empty(t, d.Changes)
	git(t, dir, "add", "other.spec.md")
	df = runJSON(t, dir, "diff", "--staged")
	df.decode(t, &d)
	assert.Equal(t, map[string]string{"$Extra.Limit": "changed"}, changeMap(d.Changes))
}

func changeMap(cs []changeJSON) map[string]string {
	m := map[string]string{}
	for _, c := range cs {
		m[c.Name] = c.State
	}
	return m
}

func TestLsChangedAndShowChange(t *testing.T) {
	dir := demoProject(t)
	replaceIn(t, dir, "demo.spec.md", "above zero", "of at least one tick")
	jr := runJSON(t, dir, "ls", "--changed")
	require.Equal(t, 0, jr.code, jr.stdout)
	var d lsData
	jr.decode(t, &d)
	require.NotNil(t, d.Baseline)
	assert.Equal(t, "ok", d.Baseline.State)
	require.Len(t, d.Anchors, 1)
	assert.Equal(t, "$Frame.Timing", d.Anchors[0].Name)
	require.NotNil(t, d.Anchors[0].Change)
	assert.Equal(t, "changed", *d.Anchors[0].Change)

	sh := runJSON(t, dir, "show", "Frame.Timing")
	var sd showAnchorData
	sh.decode(t, &sd)
	require.NotNil(t, sd.Change)
	assert.Equal(t, "changed", sd.Change.State)
	assert.Equal(t, "ok", sd.Baseline.State)
	assert.Contains(t, run(t, dir, "show", "Frame.Timing").stdout, "changed vs HEAD")

	sh = runJSON(t, dir, "show", "Frame.Order")
	sh.decode(t, &sd)
	assert.Nil(t, sd.Change)
}

func TestStatusLintErrorsExit4(t *testing.T) {
	dir := demoProject(t)
	replaceIn(t, dir, "demo.spec.md", "see $Frame.Timing", "see $Frame.Nothing")
	jr, d := statusOf(t, dir)
	assert.Equal(t, 4, jr.code)
	assert.Equal(t, "lint_errors", jr.errCode)
	assert.Equal(t, 1, d.Lint.Error)
	assert.Equal(t, "$Frame.Nothing", "$"+"Frame.Nothing")
	require.NotEmpty(t, jr.diagnostics)
	assert.Equal(t, "ref-dangling", jr.diagnostics[0]["code"])
	assert.Equal(t, []string{"spec", "lint"}, d.Next.Argv)
	h := run(t, dir, "status")
	assert.Equal(t, 4, h.code)
	assert.Contains(t, h.stdout, "ref-dangling")
	assert.Contains(t, h.stdout, "demo.spec.md:")
	assert.Contains(t, h.stdout, "next: ")
}

// TestCheck covers spec check against trailers, with a real commit.
func TestCheck(t *testing.T) {
	dir := demoProject(t)
	replaceIn(t, dir, "demo.spec.md", "above zero", "of at least one tick")
	replaceIn(t, dir, "demo.spec.md", "(frame order)", "(ordering)")
	_, d := statusOf(t, dir)
	require.Len(t, d.Trailers, 1)
	git(t, dir, "add", "-A")

	msgs := t.TempDir()
	msg := func(name, body string) string {
		p := filepath.Join(msgs, name)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		return p
	}

	// With the trailers status printed, check passes.
	good := msg("good", "Tighten timing\n\nWhy.\n\n"+strings.Join(d.Trailers, "\n")+"\n# Please enter the commit message\n")
	jr := runJSON(t, dir, "check", "--message-file", good)
	require.Equal(t, 0, jr.code, jr.stdout)
	var cd checkData
	jr.decode(t, &cd)
	assert.Equal(t, d.Trailers, cd.Required)
	assert.Equal(t, d.Trailers, cd.Present)
	assert.Empty(t, cd.Missing)
	assert.Equal(t, "index", cd.Baseline.To)
	require.NotNil(t, cd.MessageFile)
	r := run(t, dir, "check", "--message-file", good)
	assert.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "ok")

	// Without them it fails and lists exactly the missing lines.
	bad := msg("bad", "Tighten timing\n")
	jr = runJSON(t, dir, "check", "--message-file", bad)
	assert.Equal(t, 4, jr.code)
	assert.Equal(t, "missing_trailers", jr.errCode)
	jr.decode(t, &cd)
	assert.Equal(t, d.Trailers, cd.Missing)
	assert.Empty(t, cd.Present)
	r = run(t, dir, "check", "--message-file", bad)
	assert.Equal(t, 4, r.code)
	assert.Equal(t, "missing trailer lines (add them to the commit message, after a blank line):\n"+d.Trailers[0]+"\n", r.stdout)
	assert.Contains(t, r.stderr, "missing 1 trailer line")

	// A stale hash does not satisfy the requirement and is reported.
	stale := msg("stale", "x\n\nSpec-Change: $Frame.Timing inv:00000000 -> inv:11111111\n")
	jr = runJSON(t, dir, "check", "--message-file", stale)
	assert.Equal(t, 4, jr.code)
	jr.decode(t, &cd)
	assert.Equal(t, d.Trailers, cd.Missing)
	assert.Equal(t, []string{"Spec-Change: $Frame.Timing inv:00000000 -> inv:11111111"}, cd.Unexpected)

	// A trailer whose $ was eaten by the shell is reported as malformed.
	shell := msg("shell", "x\n\nSpec-Change: .Timing inv:00000000 -> inv:11111111\n")
	jr = runJSON(t, dir, "check", "--message-file", shell)
	assert.Equal(t, 4, jr.code)
	jr.decode(t, &cd)
	assert.Len(t, cd.Malformed, 1)

	// Without --message-file there is no message: every line is missing.
	jr = runJSON(t, dir, "check", "--staged")
	assert.Equal(t, 4, jr.code)
	jr.decode(t, &cd)
	assert.Nil(t, cd.MessageFile)
	assert.Equal(t, d.Trailers, cd.Missing)

	// An unreadable message file is a usage error.
	jr = runJSON(t, dir, "check", "--message-file", filepath.Join(msgs, "nope"))
	assert.Equal(t, 1, jr.code)
	assert.Equal(t, "usage", jr.errCode)

	// Commit with the trailers; status is then clean and check needs nothing.
	git(t, dir, "commit", "-q", "-F", good)
	_, d = statusOf(t, dir)
	assert.Equal(t, "clean", d.State)
	jr = runJSON(t, dir, "check", "--staged")
	assert.Equal(t, 0, jr.code)
	jr.decode(t, &cd)
	assert.Empty(t, cd.Required)
}

// TestCheckOnlyStagedContent: an unstaged edit does not need a trailer yet.
func TestCheckOnlyStagedContent(t *testing.T) {
	dir := demoProject(t)
	replaceIn(t, dir, "demo.spec.md", "above zero", "of at least one tick")
	msg := filepath.Join(t.TempDir(), "m")
	require.NoError(t, os.WriteFile(msg, []byte("x\n"), 0o644))
	jr := runJSON(t, dir, "check", "--message-file", msg)
	assert.Equal(t, 0, jr.code, "nothing is staged: %s", jr.stdout)
	git(t, dir, "add", "demo.spec.md")
	jr = runJSON(t, dir, "check", "--message-file", msg)
	assert.Equal(t, 4, jr.code)
}

// TestCheckRenameNeedsRenameLine: the renamed anchor needs Spec-Rename only.
func TestCheckRename(t *testing.T) {
	dir := demoProject(t)
	require.Equal(t, 0, run(t, dir, "rename", "Frame.Timing", "Frame.Duration").code)
	git(t, dir, "add", "-A")
	msg := filepath.Join(t.TempDir(), "m")
	require.NoError(t, os.WriteFile(msg, []byte("rename\n"), 0o644))
	jr := runJSON(t, dir, "check", "--message-file", msg)
	assert.Equal(t, 4, jr.code)
	var cd checkData
	jr.decode(t, &cd)
	assert.Equal(t, []string{"Spec-Rename: $Frame.Timing -> $Frame.Duration"}, cd.Missing)
	require.NoError(t, os.WriteFile(msg, []byte("rename\n\nSpec-Rename: $Frame.Timing -> $Frame.Duration\n"), 0o644))
	assert.Equal(t, 0, run(t, dir, "check", "--message-file", msg).code)
}

// TestBaseForAmend: on git commit --amend the trailers must cover the change
// since HEAD^ (h0 -> h2), not since the commit being amended (h1 -> h2).
func TestBaseForAmend(t *testing.T) {
	dir := demoProject(t)
	h0 := anchorsOf(t, dir)["$Frame.Timing"].ShortHash
	replaceIn(t, dir, "demo.spec.md", "above zero", "of at least one tick")
	h1 := anchorsOf(t, dir)["$Frame.Timing"].ShortHash
	_, d := statusOf(t, dir)
	require.Len(t, d.Trailers, 1)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "tighten timing\n\n"+d.Trailers[0])

	// Amend: change the same anchor again.
	replaceIn(t, dir, "demo.spec.md", "of at least one tick", "of at least two ticks")
	h2 := anchorsOf(t, dir)["$Frame.Timing"].ShortHash
	require.NotEqual(t, h0, h1)
	require.NotEqual(t, h1, h2)
	want := "Spec-Change: $Frame.Timing inv:" + h0 + " -> inv:" + h2

	_, d = statusOf(t, dir)
	assert.Equal(t, []string{"Spec-Change: $Frame.Timing inv:" + h1 + " -> inv:" + h2}, d.Trailers, "default base is HEAD")
	jr, d := statusOf(t, dir, "--base", "HEAD^")
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Equal(t, "HEAD^", d.Baseline.From)
	assert.Equal(t, []string{want}, d.Trailers)
	assert.Contains(t, run(t, dir, "status", "--base", "HEAD^").stdout, "(compared with HEAD^)")

	git(t, dir, "add", "-A")
	msg := filepath.Join(t.TempDir(), "m")
	require.NoError(t, os.WriteFile(msg, []byte("tighten timing\n\n"+want+"\n"), 0o644))
	jr = runJSON(t, dir, "check", "--message-file", msg, "--base", "HEAD^")
	require.Equal(t, 0, jr.code, jr.stdout)
	var cd checkData
	jr.decode(t, &cd)
	assert.Equal(t, "HEAD^", cd.Baseline.From)
	assert.Equal(t, []string{want}, cd.Required)
	jr = runJSON(t, dir, "check", "--message-file", msg)
	assert.Equal(t, 4, jr.code, "against HEAD the h0 -> h2 line is not the required one")

	// diff accepts --base as well.
	var dd diffData
	df := runJSON(t, dir, "diff", "--base", "HEAD^")
	require.Equal(t, 0, df.code, df.stdout)
	df.decode(t, &dd)
	assert.Equal(t, "HEAD^", dd.Baseline.From)
	assert.Equal(t, map[string]string{"$Frame.Timing": "changed"}, changeMap(dd.Changes))

	// An invalid base is exit 1, bad_ref, naming the ref.
	for _, args := range [][]string{
		{"status", "--base", "no-such-ref"}, {"check", "--staged", "--base", "no-such-ref"},
		{"check", "--message-file", msg, "--base", "no-such-ref"}, {"diff", "--base", "no-such-ref"},
	} {
		r := runJSON(t, dir, args...)
		assert.Equal(t, 1, r.code, "%v", args)
		assert.Equal(t, "bad_ref", r.errCode, "%v", args)
		h := run(t, dir, args...)
		assert.Equal(t, 1, h.code, "%v", args)
		assert.Contains(t, h.stderr, "no-such-ref", "%v", args)
	}
}
