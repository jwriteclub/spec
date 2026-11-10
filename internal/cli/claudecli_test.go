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

// fakeClaudeScript stands in for `claude`: it records its stdin and argv
// (NUL-separated) in $SPEC_FAKE_DIR, prints the reply file and exits with
// the code in the exit file.
const fakeClaudeScript = `#!/bin/sh
cat > "$SPEC_FAKE_DIR/stdin"
for a in "$@"; do printf '%s\0' "$a"; done > "$SPEC_FAKE_DIR/args"
cat "$SPEC_FAKE_DIR/reply"
exit "$(cat "$SPEC_FAKE_DIR/exit")"
`

// fakeClaude is a scripted claude for one test.
type fakeClaude struct {
	dir    string
	script string
}

func newFakeClaude(t *testing.T) *fakeClaude {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(script, []byte(fakeClaudeScript), 0o755))
	f := &fakeClaude{dir: dir, script: script}
	f.reply(t, 0, "OK", 0.000123)
	return f
}

// reply sets what the next run prints: a success result with text, or,
// with a non-zero exit, an error result whose message is text.
func (f *fakeClaude) reply(t *testing.T, exit int, text string, cost float64) {
	t.Helper()
	res := map[string]any{
		"type": "result", "subtype": "success", "is_error": exit != 0, "num_turns": 1, "result": text,
		"stop_reason": "end_turn", "total_cost_usd": cost, "api_error_status": nil, "permission_denials": []any{},
		"usage":      map[string]any{"input_tokens": 2, "cache_creation_input_tokens": 600, "cache_read_input_tokens": 0, "output_tokens": 4},
		"modelUsage": map[string]any{"claude-sonnet-5-5": map[string]any{"outputTokens": 4}},
	}
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "reply"), append(raw, '\n'), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "exit"), []byte{byte('0' + exit)}, 0o644))
}

func (f *fakeClaude) args(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "args"))
	require.NoError(t, err)
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
}

func (f *fakeClaude) stdin(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "stdin"))
	require.NoError(t, err)
	return string(raw)
}

// config is a user config whose default provider runs the fake.
func (f *fakeClaude) config(extra string) string {
	return `default = "claude"

[providers.claude]
kind = "claude-cli"
command = "` + f.script + `"
model = "sonnet"
triage_model = "haiku"
effort = "low"
pass_env = ["SPEC_FAKE_DIR"]
` + extra
}

func (f *fakeClaude) env(t *testing.T) testEnv {
	t.Helper()
	env := isolatedEnv(t)
	env["SPEC_FAKE_DIR"] = f.dir
	return env
}

func TestClaudeCLIDoctor(t *testing.T) {
	dir := demoProject(t)
	f := newFakeClaude(t)
	env := f.env(t)
	writeUserConfig(t, env, f.config(""))

	jr := runFast(t, env, dir, "doctor", "--effort", "high")
	require.Equal(t, 0, jr.code, jr.stdout)
	d, c := doctorChecks(t, jr)
	assert.Equal(t, "provider claude runs "+f.script+" on its own login (no key needed)", c["provider_key"].Detail)
	assert.Contains(t, c["inference"].Detail, "provider claude, model sonnet: answered in")
	assert.Contains(t, c["inference"].Detail, "(via claude -p, effort high; model id echoed: claude-sonnet-5-5; 602 prompt + 4 completion tokens; $0.000123)")
	require.NotNil(t, d.Inference)
	assert.Equal(t, "claude-cli", d.Inference.Kind)
	assert.Equal(t, "high", d.Inference.Effort)
	assert.InDelta(t, 0.000123, d.Inference.CostUSD, 1e-9)
	assert.Equal(t, "OK", d.Inference.Reply)
	assert.False(t, d.Inference.Streamed)

	args := f.args(t)
	assert.Equal(t, []string{"-p", "--output-format", "json", "--model", "sonnet"}, args[:5])
	assert.Equal(t, []string{"--effort", "high"}, args[len(args)-2:], "--effort overrides the config's low")
	assert.Equal(t, doctorPrompt, f.stdin(t))

	// An effort level of another kind is refused before anything runs.
	jr = runFast(t, env, dir, "doctor", "--effort", "none")
	assert.Equal(t, 1, jr.code)
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "fail", c["provider_key"].Status)
	assert.Contains(t, c["provider_key"].Detail, `--effort "none" is not a level of provider claude (kind claude-cli)`)
	assert.Equal(t, "skipped", c["inference"].Status)

	// Claude Code not logged in.
	f.reply(t, 1, "Not logged in · Please run /login", 0)
	jr = runFast(t, env, dir, "doctor")
	assert.Equal(t, 1, jr.code)
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "fail", c["inference"].Status)
	assert.Contains(t, c["inference"].Detail, "Not logged in")
	assert.Contains(t, c["inference"].Fix, "claude auth login")

	// The command is missing: a setup problem, not a failed call.
	writeUserConfig(t, env, strings.Replace(f.config(""), f.script, filepath.Join(f.dir, "nope"), 1))
	jr = runFast(t, env, dir, "doctor")
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "warn", c["provider_key"].Status)
	assert.Contains(t, c["provider_key"].Detail, "cannot run Claude Code")
	assert.Contains(t, c["provider_key"].Fix, "install Claude Code")
	assert.Equal(t, "skipped", c["inference"].Status)
}

func TestClaudeCLIAnalyze(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply("unused"))
	f := newFakeClaude(t)
	is.env["SPEC_FAKE_DIR"] = f.dir
	writeUserConfig(t, is.env, f.config(""))
	fix := modelFinding{
		File: "other.spec.md", Anchor: "Extra.Limit", Issue: "numeric_mismatch", Severity: "inconsistent",
		Explanation: "16 here, 8 there", Find: "The extra queue MUST hold at most 8 items.", Replace: "The extra queue MUST hold at most 16 items.",
	}
	// Claude has no JSON mode: the reply comes in a code fence with prose.
	f.reply(t, 0, "Here is the analysis.\n```json\n"+modelReply{Summary: "one mismatch", Findings: []modelFinding{fix}}.json(t)+"\n```", 0.0042)

	jr := is.run(t, "analyze", "--all")
	require.Equal(t, 0, jr.code, jr.stdout)
	d := analyzeOf(t, jr)
	require.NotNil(t, d.Proposal)
	require.Len(t, d.Proposal.Hunks, 1)
	inf := d.Proposal.Inference
	assert.Equal(t, "claude", inf.Provider)
	assert.Equal(t, "claude-cli", inf.Kind)
	assert.Equal(t, "low", inf.Effort)
	assert.Equal(t, "sonnet", inf.Model)
	assert.Equal(t, "claude-sonnet-5-5", inf.EchoedModel)
	assert.Equal(t, 1, inf.Calls)
	assert.InDelta(t, 0.0042, inf.CostUSD, 1e-9)

	args := f.args(t)
	var system string
	for _, a := range args {
		if s, ok := strings.CutPrefix(a, "--system-prompt="); ok {
			system = s
		}
	}
	assert.Contains(t, system, "QUANTITIES", "the analyze prompt replaces Claude Code's own")
	assert.Contains(t, f.stdin(t), "other.spec.md", "the spec text goes on stdin")
	assert.NotContains(t, strings.Join(args, " "), "at most 8 items", "never on argv")
}

func TestClaudeCLIReplyCapHit(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply("unused"))
	f := newFakeClaude(t)
	is.env["SPEC_FAKE_DIR"] = f.dir
	writeUserConfig(t, is.env, f.config(""))
	f.reply(t, 0, `{"summary": "cut`, 0.01)
	reply := filepath.Join(f.dir, "reply")
	raw, err := os.ReadFile(reply)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(reply, []byte(strings.Replace(string(raw), `"end_turn"`, `"max_tokens"`, 1)), 0o644))

	jr := is.run(t, "analyze", "--all")
	assert.Equal(t, 1, jr.code)
	assert.Equal(t, "bad_model_output", jr.errCode)
	assert.Contains(t, jr.errMessage, "Claude Code's reply cap")
	assert.Contains(t, jr.errMessage, "--effort low")
	assert.NotContains(t, jr.errMessage, "triage_extra")
}
