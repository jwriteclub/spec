// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCodexScript stands in for `codex`: it records its stdin, argv
// (NUL-separated) and the instructions file in $SPEC_FAKE_DIR, prints the
// events file and exits with the code in the exit file.
const fakeCodexScript = `#!/bin/sh
cat > "$SPEC_FAKE_DIR/stdin"
for a in "$@"; do printf '%s\0' "$a"; done > "$SPEC_FAKE_DIR/args"
cat spec-instructions.md > "$SPEC_FAKE_DIR/instructions"
cat "$SPEC_FAKE_DIR/reply"
exit "$(cat "$SPEC_FAKE_DIR/exit")"
`

// fakeCodex is a scripted codex for one test.
type fakeCodex struct {
	dir    string
	script string
}

func newFakeCodex(t *testing.T) *fakeCodex {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	require.NoError(t, os.WriteFile(script, []byte(fakeCodexScript), 0o755))
	f := &fakeCodex{dir: dir, script: script}
	f.reply(t, 0, "OK")
	return f
}

// reply sets what the next run prints: a turn whose reply is text, or, with
// a non-zero exit, a failed turn whose error is text.
func (f *fakeCodex) reply(t *testing.T, exit int, text string) {
	t.Helper()
	events := []map[string]any{{"type": "thread.started", "thread_id": "t1"}, {"type": "turn.started"}}
	if exit == 0 {
		events = append(events,
			map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_0", "type": "agent_message", "text": text}},
			map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 1688, "cached_input_tokens": 0, "output_tokens": 5, "reasoning_output_tokens": 0}})
	} else {
		events = append(events, map[string]any{"type": "turn.failed", "error": map[string]any{"message": text}})
	}
	var b strings.Builder
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		require.NoError(t, err)
		b.Write(raw)
		b.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "reply"), []byte(b.String()), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "exit"), []byte{byte('0' + exit)}, 0o644))
}

func (f *fakeCodex) read(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, name))
	require.NoError(t, err)
	return string(raw)
}

func (f *fakeCodex) args(t *testing.T) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(f.read(t, "args"), "\x00"), "\x00")
}

// config is a user config whose default provider runs the fake.
func (f *fakeCodex) config() string {
	return `default = "codex"

[providers.codex]
kind = "codex-cli"
command = "` + f.script + `"
model = "gpt-6.1-terra"
triage_model = "gpt-6-luna"
effort = "low"
pass_env = ["SPEC_FAKE_DIR"]
`
}

func TestCodexCLIDoctor(t *testing.T) {
	dir := demoProject(t)
	f := newFakeCodex(t)
	env := isolatedEnv(t)
	env["SPEC_FAKE_DIR"] = f.dir
	writeUserConfig(t, env, f.config())

	jr := runFast(t, env, dir, "doctor", "--effort", "none")
	require.Equal(t, 0, jr.code, jr.stdout)
	d, c := doctorChecks(t, jr)
	assert.Equal(t, "provider codex runs "+f.script+" on its own login (no key needed)", c["provider_key"].Detail)
	assert.Contains(t, c["inference"].Detail, "provider codex, model gpt-6.1-terra: answered in")
	assert.Contains(t, c["inference"].Detail, "(via codex exec, effort none; model id echoed: none; 1688 prompt + 5 completion tokens)")
	require.NotNil(t, d.Inference)
	assert.Equal(t, "codex-cli", d.Inference.Kind)
	assert.Equal(t, "none", d.Inference.Effort)
	assert.Equal(t, "OK", d.Inference.Reply)
	assert.False(t, d.Inference.Streamed)

	args := f.args(t)
	assert.Equal(t, []string{"exec", "--json"}, args[:2])
	assert.Equal(t, []string{"-c", `model_reasoning_effort="none"`, "-"}, args[len(args)-3:], "--effort overrides the config's low")
	assert.Equal(t, doctorPrompt, f.read(t, "stdin"))

	// An effort level codex-cli does not have is refused before anything runs.
	jr = runFast(t, env, dir, "doctor", "--effort", "ultra")
	assert.Equal(t, 1, jr.code)
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "fail", c["provider_key"].Status)
	assert.Contains(t, c["provider_key"].Detail, `--effort "ultra" is not a level of provider codex (kind codex-cli)`)
	assert.Equal(t, "skipped", c["inference"].Status)

	// Codex not logged in.
	f.reply(t, 1, "unexpected status 401 Unauthorized: Missing bearer or basic authentication in header")
	jr = runFast(t, env, dir, "doctor")
	assert.Equal(t, 1, jr.code)
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "fail", c["inference"].Status)
	assert.Contains(t, c["inference"].Detail, "401")
	assert.Contains(t, c["inference"].Fix, "codex login")
}

func TestCodexCLIAnalyze(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply("unused"))
	f := newFakeCodex(t)
	is.env["SPEC_FAKE_DIR"] = f.dir
	writeUserConfig(t, is.env, f.config())
	fix := modelFinding{
		File: "other.spec.md", Anchor: "Extra.Limit", Issue: "numeric_mismatch", Severity: "inconsistent",
		Explanation: "16 here, 8 there", Find: "The extra queue MUST hold at most 8 items.", Replace: "The extra queue MUST hold at most 16 items.",
	}
	f.reply(t, 0, "```json\n"+modelReply{Summary: "one mismatch", Findings: []modelFinding{fix}}.json(t)+"\n```")

	jr := is.run(t, "analyze", "--all")
	require.Equal(t, 0, jr.code, jr.stdout)
	d := analyzeOf(t, jr)
	require.NotNil(t, d.Proposal)
	require.Len(t, d.Proposal.Hunks, 1)
	inf := d.Proposal.Inference
	assert.Equal(t, "codex", inf.Provider)
	assert.Equal(t, "codex-cli", inf.Kind)
	assert.Equal(t, "low", inf.Effort)
	assert.Equal(t, "gpt-6.1-terra", inf.Model)
	assert.Equal(t, 1, inf.Calls)
	assert.Zero(t, inf.CostUSD)

	assert.Contains(t, f.read(t, "instructions"), "QUANTITIES", "the analyze prompt replaces Codex's instructions")
	assert.Contains(t, f.read(t, "stdin"), "other.spec.md", "the spec text goes on stdin")
	assert.NotContains(t, strings.Join(f.args(t), " "), "at most 8 items", "never on argv")
}
