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

// fakeAgyScript stands in for `agy`: it records its argv (NUL-separated) in
// $SPEC_FAKE_DIR, prints the reply file to stdout, prints the stderr file (if
// non-empty) to stderr, and exits with the code in the exit file.
const fakeAgyScript = `#!/bin/sh
for a in "$@"; do printf '%s\0' "$a"; done > "$SPEC_FAKE_DIR/args"
if [ -s "$SPEC_FAKE_DIR/stderr" ]; then
	cat "$SPEC_FAKE_DIR/stderr" >&2
fi
cat "$SPEC_FAKE_DIR/reply"
exit "$(cat "$SPEC_FAKE_DIR/exit")"
`

// fakeAgy is a scripted agy for one test.
type fakeAgy struct {
	dir    string
	script string
}

func newFakeAgy(t *testing.T) *fakeAgy {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "agy")
	require.NoError(t, os.WriteFile(script, []byte(fakeAgyScript), 0o755))
	f := &fakeAgy{dir: dir, script: script}
	f.reply(t, 0, "OK")
	return f
}

// reply sets what the next run prints: a SUCCESS result with text, or, with a
// non-zero exit, an ERROR result whose error message is text.
func (f *fakeAgy) reply(t *testing.T, exit int, text string) {
	t.Helper()
	var res map[string]any
	if exit == 0 {
		res = map[string]any{
			"conversation_id":  "conv-1",
			"status":           "SUCCESS",
			"response":         text,
			"duration_seconds": 1.23,
			"num_turns":        1,
			"usage": map[string]any{
				"input_tokens":      16054,
				"output_tokens":     63,
				"thinking_tokens":   0,
				"cache_read_tokens": 11451,
				"total_tokens":      16117,
			},
		}
	} else {
		res = map[string]any{
			"conversation_id":  "",
			"status":           "ERROR",
			"response":         "",
			"error":            text,
			"duration_seconds": 0,
			"num_turns":        0,
			"usage": map[string]any{
				"input_tokens":      0,
				"output_tokens":     0,
				"thinking_tokens":   0,
				"cache_read_tokens": 0,
				"total_tokens":      0,
			},
			"finished_reason": "error",
		}
	}
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	f.rawReply(t, exit, string(raw)+"\n", "")
}

// rawReply sets literal stdout, stderr, and exit code for the next run.
func (f *fakeAgy) rawReply(t *testing.T, exit int, stdout, stderr string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "reply"), []byte(stdout), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "stderr"), []byte(stderr), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "exit"), []byte{byte('0' + exit)}, 0o644))
}

func (f *fakeAgy) args(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "args"))
	require.NoError(t, err)
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
}

// config is a user config whose default provider runs the fake.
func (f *fakeAgy) config() string {
	return `default = "agy"

[providers.agy]
kind = "agy-cli"
command = "` + f.script + `"
model = "gemini-4-neon-medium"
triage_model = "gemini-3.8-flash-low"
effort = "low"
pass_env = ["SPEC_FAKE_DIR"]
`
}

func (f *fakeAgy) env(t *testing.T) testEnv {
	t.Helper()
	env := isolatedEnv(t)
	env["SPEC_FAKE_DIR"] = f.dir
	return env
}

func TestAgyCLIDoctor(t *testing.T) {
	dir := demoProject(t)
	f := newFakeAgy(t)
	env := f.env(t)
	writeUserConfig(t, env, f.config())

	jr := runFast(t, env, dir, "doctor", "--effort", "high")
	require.Equal(t, 0, jr.code, jr.stdout)
	d, c := doctorChecks(t, jr)
	assert.Equal(t, "provider agy runs "+f.script+" on its own login (no key needed)", c["provider_key"].Detail)
	assert.Contains(t, c["inference"].Detail, "provider agy, model gemini-4-neon-medium: answered in")
	assert.Contains(t, c["inference"].Detail, "(via agy -p, effort high; model id echoed: none; 16054 prompt + 63 completion tokens)")
	require.NotNil(t, d.Inference)
	assert.Equal(t, "agy", d.Inference.Provider)
	assert.Equal(t, "agy-cli", d.Inference.Kind)
	assert.Equal(t, "gemini-4-neon-medium", d.Inference.Model)
	assert.Equal(t, "high", d.Inference.Effort)
	assert.Equal(t, 16054, d.Inference.PromptTokens)
	assert.Equal(t, 63, d.Inference.CompletionTokens)
	assert.Equal(t, 16117, d.Inference.TotalTokens)
	assert.Equal(t, "stop", d.Inference.FinishReason)
	assert.Equal(t, "OK", d.Inference.Reply)
	assert.False(t, d.Inference.Streamed)
	assert.Zero(t, d.Inference.CostUSD)

	args := f.args(t)
	require.Len(t, args, 9)
	assert.Equal(t, []string{
		"--output-format", "json",
		"--model", "gemini-4-neon-medium",
		"--disable-slash-commands",
		"--sandbox",
		"--effort", "high",
	}, args[:8], "--effort overrides the config's low")
	assert.True(t, strings.HasPrefix(args[8], "-p="), "prompt passed via -p= form")
	assert.Contains(t, args[8], doctorPrompt)

	// An effort level agy-cli does not have is refused before anything runs.
	jr = runFast(t, env, dir, "doctor", "--effort", "none")
	assert.Equal(t, 1, jr.code)
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "fail", c["provider_key"].Status)
	assert.Contains(t, c["provider_key"].Detail, `--effort "none" is not a level of provider agy (kind agy-cli)`)
	assert.Equal(t, "skipped", c["inference"].Status)

	// Antigravity CLI not logged in.
	f.reply(t, 1, "401 Unauthorized: not logged in")
	jr = runFast(t, env, dir, "doctor")
	assert.Equal(t, 1, jr.code)
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "fail", c["inference"].Status)
	assert.Contains(t, c["inference"].Detail, "not logged in")
	assert.Contains(t, c["inference"].Fix, "run `agy` to sign in")

	// Incompatible agy version: unknown flag exits 2 with message on stderr.
	f.rawReply(t, 2, "", "flags provided but not defined: -disable-slash-commands\n")
	jr = runFast(t, env, dir, "doctor")
	assert.Equal(t, 1, jr.code)
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "fail", c["inference"].Status)
	assert.Contains(t, c["inference"].Detail, "flags provided but not defined")
	assert.Contains(t, c["inference"].Fix, "agy update")

	// The command is missing: a setup problem, not a failed call.
	writeUserConfig(t, env, strings.Replace(f.config(), f.script, filepath.Join(f.dir, "nope"), 1))
	jr = runFast(t, env, dir, "doctor")
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "warn", c["provider_key"].Status)
	assert.Contains(t, c["provider_key"].Detail, "cannot run Antigravity CLI")
	assert.Contains(t, c["provider_key"].Fix, "install the Antigravity CLI")
	assert.Equal(t, "skipped", c["inference"].Status)
}

func TestAgyCLIAnalyze(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply("unused"))
	f := newFakeAgy(t)
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
	assert.Equal(t, "agy", inf.Provider)
	assert.Equal(t, "agy-cli", inf.Kind)
	assert.Equal(t, "low", inf.Effort)
	assert.Equal(t, "gemini-4-neon-medium", inf.Model)
	assert.Equal(t, 1, inf.Calls)
	assert.Equal(t, 16054, inf.PromptTokens)
	assert.Equal(t, 63, inf.CompletionTokens)
	assert.Equal(t, 16117, inf.TotalTokens)
	assert.Zero(t, inf.CostUSD)

	args := f.args(t)
	require.NotEmpty(t, args)
	promptArg := args[len(args)-1]
	assert.True(t, strings.HasPrefix(promptArg, "-p="))
	assert.Contains(t, promptArg, "<system_instructions>")
	assert.Contains(t, promptArg, "QUANTITIES")
	assert.Contains(t, promptArg, "Do not invoke any tools or modify files; answer directly.")
	assert.Contains(t, promptArg, "other.spec.md")
}
