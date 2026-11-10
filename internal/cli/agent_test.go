// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/explain"
)

var (
	// codeLine matches an indented command line in a topic text, such as
	// "    spec lint     problems; exit 4 when there are errors".
	codeLine = regexp.MustCompile(`(?m)^ {4}(spec [^\n]*)$`)
	// inlineCmd matches a command written in backticks.
	inlineCmd = regexp.MustCompile("`(spec [^`\n]+)`")
	// commentary splits a code line from its trailing commentary.
	commentary = regexp.MustCompile(`\s{2,}`)
)

// commandsIn returns the spec command lines a text names, as field lists
// starting after "spec".
func commandsIn(text string) [][]string {
	var out [][]string
	for _, m := range codeLine.FindAllStringSubmatch(text, -1) {
		line := commentary.Split(strings.TrimSpace(m[1]), 2)[0]
		out = append(out, strings.Fields(line)[1:])
	}
	for _, m := range inlineCmd.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.Fields(m[1])[1:])
	}
	return out
}

// allTopicTexts returns the index and every topic.
func allTopicTexts() map[string]string {
	texts := map[string]string{"index": explain.Index()}
	for _, n := range explain.Names() {
		texts[n], _ = explain.Topic(n)
	}
	return texts
}

// notBuiltYet are commands the texts mention as arriving in a later step.
var notBuiltYet = map[string]bool{}

// TestExplainCommandsExist checks that every command the texts show names a
// real command and real flags, so the documentation cannot drift from the
// CLI.
func TestExplainCommandsExist(t *testing.T) {
	root := (&session{}).rootCommand()
	known := map[string]bool{}
	for _, c := range root.Commands() {
		known[c.Name()] = true
	}
	for name, text := range allTopicTexts() {
		for _, fields := range commandsIn(text) {
			sub := fields[0]
			if strings.HasPrefix(sub, "--") || notBuiltYet[sub] {
				continue
			}
			cmd, _, err := root.Find(fields)
			if !assert.NoError(t, err, "topic %s: spec %s", name, strings.Join(fields, " ")) {
				continue
			}
			if !assert.True(t, known[sub] || sub == "help", "topic %s: `spec %s`: no such command %q", name, strings.Join(fields, " "), sub) {
				continue
			}
			for _, f := range fields {
				if !strings.HasPrefix(f, "--") {
					continue
				}
				flag := strings.SplitN(strings.TrimPrefix(f, "--"), "=", 2)[0]
				found := cmd.LocalFlags().Lookup(flag) != nil || cmd.InheritedFlags().Lookup(flag) != nil
				assert.True(t, found, "topic %s: `spec %s`: command %s has no flag --%s", name, strings.Join(fields, " "), cmd.Name(), flag)
			}
		}
	}
}

// TestExplainSigilFreeExamples checks that no command example passes a $Name
// to the shell.
func TestExplainSigilFreeExamples(t *testing.T) {
	for name, text := range allTopicTexts() {
		for _, fields := range commandsIn(text) {
			if strings.Join(fields, " ") == "show $Frame.Timing" {
				continue // the markup topic's example of what NOT to type
			}
			for _, f := range fields {
				if f == `"$1"` {
					continue // the commit-msg hook's own argument
				}
				assert.NotContains(t, f, "$", "topic %s: spec %s", name, strings.Join(fields, " "))
			}
		}
	}
}

// namedBy reports whether the texts name the command line (subcommand and
// every flag it uses).
func namedBy(named [][]string, line []string) bool {
	for _, n := range named {
		if len(n) == 0 || n[0] != line[0] {
			continue
		}
		ok := true
		for _, f := range line[1:] {
			if !strings.HasPrefix(f, "--") {
				continue
			}
			has := false
			for _, nf := range n {
				if nf == f {
					has = true
				}
			}
			if !has {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// TestAgentPath is acceptance criterion 4: in an empty directory, an agent
// that has read only `spec explain` and `spec explain adopt` can run spec init
// and get a spec that passes spec lint, using only commands those texts name.
func TestAgentPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agentproj")
	require.NoError(t, os.Mkdir(dir, 0o755))

	index := run(t, dir, "explain")
	require.Equal(t, 0, index.code, index.stderr)
	adopt := run(t, dir, "explain", "adopt")
	require.Equal(t, 0, adopt.code, adopt.stderr)
	require.Equal(t, strings.TrimRight(index.stdout, "\n"), strings.TrimRight(explain.Index(), "\n"))
	named := commandsIn(index.stdout + "\n" + adopt.stdout)

	step := func(args ...string) result {
		t.Helper()
		assert.True(t, namedBy(named, args), "spec %s is not named by `spec explain` or `spec explain adopt`", strings.Join(args, " "))
		return run(t, dir, args...)
	}

	r := step("init", "--name", "lensd")
	require.Equal(t, 0, r.code, r.stderr)
	assert.FileExists(t, filepath.Join(dir, "spec.toml"))
	assert.FileExists(t, filepath.Join(dir, "lensd.spec.md"))
	assert.Contains(t, readFile(t, dir, ".gitignore"), ".spec/")
	assert.Contains(t, r.stdout, "## Specs", "init prints the suggested CLAUDE.md section")
	assert.Contains(t, r.stdout, "add it only if the user agrees")
	assert.NotContains(t, strings.ToLower(r.stdout), "chris", "spec is for any user: its output names no one")

	// The skeleton passes lint with nothing to report, in both forms.
	r = step("lint")
	assert.Equal(t, 0, r.code, r.stdout+r.stderr)
	assert.Equal(t, "0 errors, 0 warnings, 0 hints in 1 file, 2 anchors, 3 sections\n", r.stdout)
	jr := runJSON(t, dir, "lint")
	assert.Equal(t, 0, jr.code)
	assert.Empty(t, jr.diagnostics)

	r = step("ls")
	assert.Equal(t, 0, r.code)
	assert.Contains(t, r.stdout, "Example.Rule")
	assert.Contains(t, r.stdout, "Example.Term")
	r = step("ls", "--unreferenced")
	assert.Equal(t, 0, r.code)
	r = step("show", "Example.Rule")
	assert.Equal(t, 0, r.code, r.stderr)
	r = step("status")
	assert.Equal(t, 0, r.code, r.stdout+r.stderr)
	assert.Contains(t, r.stdout, "next:")

	// A second init refuses and changes nothing.
	before := readFile(t, dir, "lensd.spec.md")
	r = step("init", "--name", "lensd")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "already exists")
	assert.Equal(t, before, readFile(t, dir, "lensd.spec.md"))
	jr = runJSON(t, dir, "init", "--name", "lensd")
	assert.Equal(t, "project_exists", jr.errCode)
}

func TestInitAdoptsExistingSpecByteForByte(t *testing.T) {
	dir := t.TempDir()
	custom := "# Mine\r\n\r\n## 1. Rules\r\n\r\n**$Mine.Rule (rule).** It MUST hold.\r\nChecked by: TestMine\r\n"
	write(t, dir, "lensd.spec.md", custom)
	r := runJSON(t, dir, "init", "--name", "lensd")
	require.Equal(t, 0, r.code, r.stdout)
	var d initData
	r.decode(t, &d)
	assert.Equal(t, []initFile{
		{File: "spec.toml", Action: "created"},
		{File: "lensd.spec.md", Action: "adopted"},
		{File: ".gitignore", Action: "created"},
	}, d.Files)
	assert.Equal(t, custom, readFile(t, dir, "lensd.spec.md"), "an existing spec file is adopted byte for byte")
	assert.Equal(t, 0, run(t, dir, "lint").code)
}

func TestInitGitignore(t *testing.T) {
	cases := []struct {
		name, before, after, action string
	}{
		{"appended", "node_modules\n", "node_modules\n.spec/\n", "appended"},
		{"no trailing newline", "node_modules", "node_modules\n.spec/\n", "appended"},
		{"present", "a\n.spec/\nb\n", "a\n.spec/\nb\n", "unchanged"},
		{"present anchored", "/.spec\n", "/.spec\n", "unchanged"},
		{"absent", "", ".spec/\n", "created"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.name != "absent" {
				write(t, dir, ".gitignore", c.before)
			}
			r := runJSON(t, dir, "init", "--name", "demo")
			require.Equal(t, 0, r.code, r.stdout)
			var d initData
			r.decode(t, &d)
			assert.Equal(t, c.action, d.Files[2].Action)
			assert.Equal(t, c.after, readFile(t, dir, ".gitignore"))
		})
	}
}

func TestInitNames(t *testing.T) {
	base := t.TempDir()
	mk := func(name string) string {
		d := filepath.Join(base, name)
		require.NoError(t, os.Mkdir(d, 0o755))
		return d
	}

	d := mk("My_Cool Project")
	r := runJSON(t, d, "init")
	require.Equal(t, 0, r.code, r.stdout)
	assert.FileExists(t, filepath.Join(d, "my-cool-project.spec.md"))

	// A directory name that cannot start a project name needs --name.
	d = mk("2026-proj")
	r = runJSON(t, d, "init")
	assert.Equal(t, 1, r.code)
	assert.Equal(t, "usage", r.errCode)
	assert.Contains(t, r.errMessage, "--name")
	assert.NoFileExists(t, filepath.Join(d, "spec.toml"))
	r = runJSON(t, d, "init", "--name", "proj2026")
	assert.Equal(t, 0, r.code, r.stdout)

	// --project names the directory to initialise.
	d = mk("elsewhere")
	r = runJSON(t, base, "init", "--name", "else", "--project", "elsewhere")
	assert.Equal(t, 0, r.code, r.stdout)
	assert.FileExists(t, filepath.Join(d, "else.spec.md"))

	// A bad --name is a usage error and writes nothing.
	d = mk("badname")
	r = runJSON(t, d, "init", "--name", "9lives")
	assert.Equal(t, "usage", r.errCode)
	assert.NoFileExists(t, filepath.Join(d, "spec.toml"))
}

func TestInitWriteFailureIsRetryable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	r := runJSON(t, dir, "init", "--name", "demo")
	assert.Equal(t, 1, r.code)
	assert.Equal(t, "write_failed", r.errCode)

	require.NoError(t, os.Chmod(dir, 0o755))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a failed init leaves nothing behind")
	r = runJSON(t, dir, "init", "--name", "demo")
	assert.Equal(t, 0, r.code, r.stdout)
}
