// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/project"
)

// A recording's providers are names and models, nothing else: whatever kind,
// command, endpoint or prompt file its config.toml asks for, a replay runs
// nothing and contacts nothing, and every provider serves the canned replies.
func TestPrepareIgnoresProviderSettings(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho ran > '"+marker+"'\n"), 0o755))
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	secret := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(secret, []byte("do not read\n"), 0o600))

	dir := recordingWith(t, nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, CannedFile), []byte("[[check]]\nreply = \"canned\"\n[[check]]\nreply = \"canned\"\n[[check]]\nreply = \"canned\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(`
default = "cc"
max_output_tokens = 9000
underline_color = "off"

[providers.cc]
kind = "claude-cli"
command = "`+script+`"
model = "opus"
pass_env = ["AWS_SECRET_ACCESS_KEY"]

[providers.cx]
kind = "codex-cli"
command = "`+script+`"
model = "gpt"

[providers.web]
base_url = "`+srv.URL+`"
api_key_env = "HOME"
model = "m"
triage_model = "small"
triage = false

[prompts]
change = "`+secret+`"
`), 0o644))

	p, err := Prepare(context.Background(), dir, t.TempDir())
	require.NoError(t, err)
	defer func() { _ = p.Cleanup() }()
	loaded, err := config.Load(filepath.Join(p.Env["XDG_CONFIG_HOME"], "spec", "config.toml"))
	require.NoError(t, err)
	require.True(t, loaded.Exists)
	require.Empty(t, loaded.Diagnostics, string(loaded.Source))

	src := string(loaded.Source)
	for _, gone := range []string{script, srv.URL, "api_key_env", "pass_env", "claude-cli", "codex-cli", secret} {
		assert.NotContains(t, src, gone)
	}
	assert.Equal(t, config.Prompts{}, loaded.Config.Prompts)
	assert.Equal(t, "cc", loaded.Config.Default)
	assert.Equal(t, 9000, loaded.Config.MaxOutputTokens)
	assert.Equal(t, "off", loaded.Config.UnderlineColor)
	assert.Equal(t, "small", loaded.Config.Providers["web"].TriageModel)
	assert.False(t, loaded.Config.Providers["web"].TriageOn())

	for _, name := range []string{"cc", "cx", "web"} {
		prov := loaded.Config.Providers[name]
		assert.Equal(t, config.KindCanned, prov.Kind, name)
		assert.Equal(t, p.Canned, prov.Path, name)
		c, err := infer.Open(name, prov, p.Getenv)
		require.NoError(t, err, name)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		resp, err := c.Chat(ctx, infer.Request{Purpose: infer.PurposeCheck, Messages: []infer.Message{{Role: "user", Content: "x"}}})
		cancel()
		require.NoError(t, err, name)
		assert.Equal(t, "canned", resp.Content, name)
	}
	assert.NoFileExists(t, marker, "a recording's command ran")
	assert.Zero(t, hits.Load(), "a recording's endpoint was contacted")
}

// A config that is not TOML stops the replay rather than reaching the UI.
func TestPrepareRefusesBrokenConfig(t *testing.T) {
	dir := recordingWith(t, nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte("[[[nope"), 0o644))
	parent := t.TempDir()
	_, err := Prepare(context.Background(), dir, parent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ConfigFile)
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// Audit, not a fix: without head.tar a replay has no .git, and a project shaped
// like a repository (HEAD, objects, refs, a config asking for a work tree, hooks)
// is found by git, but as a bare one with no work tree. So nothing a replay does
// in it goes beyond rev-parse: no baseline, no commit, no hook.
func TestPlantedRepositoryRunsNothing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	marker := filepath.Join(t.TempDir(), "ran")
	hook := "#!/bin/sh\necho ran > '" + marker + "'\n"
	dir := recordingWith(t, map[string]string{WorkspaceTar: writeTar(t,
		member{name: "spec.toml", body: "name = \"x\"\n"},
		member{name: "HEAD", body: "ref: refs/heads/main\n"},
		member{name: "objects/.keep"},
		member{name: "refs/.keep"},
		member{name: "config", body: "[core]\n\tbare = false\n\tworktree = .\n"},
		member{name: "hooks/pre-commit", body: hook, mode: 0o755},
	)})
	p, err := Prepare(context.Background(), dir, t.TempDir())
	require.NoError(t, err)
	defer func() { _ = p.Cleanup() }()

	proj, err := project.Open(p.Root)
	require.NoError(t, err)
	base, err := p.Git.Load(context.Background(), proj, baseline.Source{})
	require.NoError(t, err)
	assert.Equal(t, baseline.StateUnavailable, base.State)
	_, err = p.Git.Run(context.Background(), p.Root, nil, "commit", "-q", "--allow-empty", "-m", "x")
	require.Error(t, err)
	assert.NoFileExists(t, marker, "the planted hook ran")
}

// Other spellings of .git that some file system reads as .git are .git.
func TestInGitDirSpellings(t *testing.T) {
	for _, p := range []string{
		".git", ".GIT", "a/.git/b", ".git.", ".git ", ".git. .", "git~1", "GIT~1/hooks/x",
		".g\u200cit/hooks/pre-commit", ".\u206agit", ".git\ufeff",
	} {
		assert.True(t, InGitDir(p), "%q", p)
	}
	for _, p := range []string{"git", ".gitignore", ".gitattributes", "a.git", "git~2", ".github/x", "a/b"} {
		assert.False(t, InGitDir(p), "%q", p)
	}
}

// A file whose name holds a control character is left out: its name would reach
// the terminal and the tape. The rest of the project restores.
func TestExtractTarSkipsControlCharacters(t *testing.T) {
	root, _ := sandbox(t)
	names, err := extractTar(writeTar(t,
		member{name: "a.md", body: "a"},
		member{name: "b\x1b]52;c;aGk=\x07.md", body: "b"},
		member{name: "c\n.md", body: "c"},
		member{name: "d\x7f.md", body: "d"},
		member{name: "e\u0085.md", body: "e"},
		member{name: "spec\u00a0\u00e9.md", body: "f"},
	), root)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.md", "spec\u00a0\u00e9.md"}, names)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Len(t, entries, 2)
}

// The tape's launch leaves no shell behind: VHS types whatever the recording
// holds, and after the UI quits a shell would run it.
func TestTapeLeavesNoShell(t *testing.T) {
	rec := sampleRecording()
	rec.Dir = "/rec/it's"
	tape, _ := Tape(rec, TapeOptions{Spec: "/bin/spec"})
	assert.Contains(t, tape, "\n"+quoteLaunch(`exec '/bin/spec' ui --replay '/rec/it'\''s' || exit`)+"\nEnter\n")
}

// quoteLaunch is the Type line of a launch command.
func quoteLaunch(cmd string) string {
	return typeLines(cmd)[0]
}

// Nothing a recording holds starts a new line of the tape: VHS would obey it.
func TestTapeCommentsStayOnOneLine(t *testing.T) {
	inject := "x\nOutput \"/tmp/pwned\"\r\nSet Shell \"pwned\"\u2028Source \"pwned\""
	rec := sampleRecording()
	rec.Dir = "/rec/" + inject
	rec.Events = append(rec.Events[:len(rec.Events)-1:len(rec.Events)-1],
		Event{Kind: KindFiles, Keys: 1, Source: inject, Changes: []FileChange{ChangeOf(inject, []byte("x"), false), ChangeOf(inject, nil, true)}},
		Event{Kind: KindMouse, Keys: 1, Mouse: &Mouse{Action: inject, Pane: inject, Target: &MouseTarget{Label: inject}}},
		Event{Kind: KindMouse, Keys: 1, Mouse: &Mouse{Action: MouseHover, Pane: "editor", Target: &MouseTarget{Kind: "anchor", Name: inject}}},
		Event{Kind: KindMouse, Keys: 1, Mouse: &Mouse{Action: MouseClick, Target: &MouseTarget{Pos: &Pos{File: inject, Line: 1, Col: 1}}}},
		Event{Kind: KindKey, Keys: 2, Key: inject},
		Event{Kind: KindEnd, T: 5000},
	)
	tape, _ := Tape(rec, TapeOptions{Spec: "/bin/spec", Video: "/v.mp4"})
	for _, l := range strings.Split(tape, "\n") {
		if strings.HasPrefix(l, "#") {
			assert.NotContains(t, l, "\u2028", l)
			continue
		}
		if strings.HasPrefix(l, "Type ") {
			continue // typed, not obeyed; `spec replay` refuses such a DIR anyway
		}
		assert.NotContains(t, l, "pwned", l)
	}
	assert.NotContains(t, tape, "\r")
}
