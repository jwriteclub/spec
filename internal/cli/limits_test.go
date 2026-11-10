// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/formal/tla"
)

const limitHint = "the model hit the acquisition limit of %s visits"

// --max-acquisition-visits takes only a positive integer; anything else is a
// usage error that says so.
func TestMaxAcquisitionVisitsValidation(t *testing.T) {
	dir := formalProject(t, cliModel)
	for _, v := range []string{"0", "-5", "abc", "1.5", "", "99999999999999999999"} {
		r := run(t, dir, "lint", "--max-acquisition-visits="+v)
		assert.Equal(t, exitFail, r.code, v)
		assert.Contains(t, r.stderr, `invalid argument "`+v+`" for "--max-acquisition-visits" flag: must be a positive integer number of visits`, v)

		jr := runJSON(t, dir, "lint", "--max-acquisition-visits="+v)
		assert.Equal(t, "usage", jr.errCode, v)
	}
	r := run(t, dir, "lint", "--max-acquisition-visits", "2100000")
	assert.Equal(t, exitOK, r.code, r.stderr)
	assert.Equal(t, formal.Checker(tla.Checker{}), formal.CheckerFor("M.tla"), "the run puts the registry back")
}

// spec.toml's limit reaches lint's model check, the flag overrides it, and a
// check that stops at the limit is a formal-check-limit hint naming both.
func TestLintAcquisitionLimit(t *testing.T) {
	dir := formalProject(t, cliModel)
	write(t, dir, "spec.toml", "formal = [\"tla/*.tla\"]\n\n[tla]\nmax_acquisition_visits = 5\n")

	r := run(t, dir, "lint")
	assert.Equal(t, exitOK, r.code, "a hint fails nothing: %s", r.stdout)
	assert.Contains(t, r.stdout, "tla/M.tla:1:1: hint: "+strings.Replace(limitHint, "%s", "5", 1))
	assert.Contains(t, r.stdout, "raise it with --max-acquisition-visits N, or max_acquisition_visits = N under [tla] in spec.toml [formal-check-limit]")

	jr := runJSON(t, dir, "lint")
	var hits int
	for _, d := range jr.diagnostics {
		if d["code"] == "formal-check-limit" {
			hits++
			assert.Equal(t, "hint", d["severity"])
			assert.Equal(t, "tla/M.tla", d["file"])
		}
	}
	assert.Equal(t, 1, hits)

	r = run(t, dir, "lint", "--max-acquisition-visits", "1000000")
	assert.NotContains(t, r.stdout, "formal-check-limit", "the flag overrides spec.toml")

	write(t, dir, "spec.toml", "formal = [\"tla/*.tla\"]\n")
	r = run(t, dir, "lint")
	assert.NotContains(t, r.stdout, "formal-check-limit", "the default fits a small model")
	r = run(t, dir, "lint", "--max-acquisition-visits", "7")
	assert.Contains(t, r.stdout, strings.Replace(limitHint, "%s", "7", 1))

	write(t, dir, "spec.toml", "formal = [\"tla/*.tla\"]\n\n[tla]\nmax_acquisition_visits = 0\n")
	r = run(t, dir, "lint")
	assert.Equal(t, exitCheck, r.code)
	assert.Contains(t, r.stdout, "[tla] max_acquisition_visits must be a positive number of visits, not 0 [config-invalid]")
}

// limitRecorder is a clean checker that keeps the limits it was asked with.
type limitRecorder struct {
	mu  sync.Mutex
	got []formal.CheckLimits
}

func (c *limitRecorder) Check(_ context.Context, req formal.CheckRequest) (formal.Report, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, req.Limits)
	return formal.Report{Checker: "fake", Version: "0.1", Level: formal.LevelComplete, Completion: formal.CompletionComplete}, nil
}

func (c *limitRecorder) all() []formal.CheckLimits {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]formal.CheckLimits(nil), c.got...)
}

// A model proposal's check gets spec.toml's limit, or the flag's over it.
func TestChangeModelForwardsLimits(t *testing.T) {
	newText := strings.Replace(cliModel, "x + 1", "x + 2", 1)
	for _, tc := range []struct {
		name, toml string
		args       []string
		want       int
	}{
		{"default", "", nil, 0},
		{"spec.toml", "\n[tla]\nmax_acquisition_visits = 9\n", nil, 9},
		{"flag", "", []string{"--max-acquisition-visits", "7"}, 7},
		{"flag over spec.toml", "\n[tla]\nmax_acquisition_visits = 9\n", []string{"--max-acquisition-visits", "7"}, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModelCLI(t)
			rec := &limitRecorder{}
			m.checker = rec
			write(t, m.dir, "spec.toml", "formal = [\"tla/*.tla\"]\n"+tc.toml)
			write(t, m.dir, "new.tla", newText)
			args := append([]string{"change", "--tla", "tla/M.tla", "--body-file", "new.tla", "--why", "step by two", "--by", "agent-7"}, tc.args...)
			jr := m.run(t, "", args...)
			require.Equal(t, exitPending, jr.code, jr.stdout)
			got := rec.all()
			require.NotEmpty(t, got)
			for _, l := range got {
				assert.Equal(t, formal.CheckLimits{MaxAcquisitionVisits: tc.want}, l)
			}
		})
	}
}

// With prudence itself, a model proposal checked under too small a limit is
// filed "syntax checked" with the limit's hint, which names the fix.
func TestChangeModelAcquisitionLimit(t *testing.T) {
	m := newModelCLI(t)
	write(t, m.dir, "new.tla", strings.Replace(cliModel, "x + 1", "x + 2", 1))
	r := m.runHuman(t, "", "change", "--tla", "tla/M.tla", "--body-file", "new.tla", "--why", "step by two", "--by", "agent-7",
		"--max-acquisition-visits", "5")
	require.Equal(t, exitPending, r.code, r.stderr)
	assert.Contains(t, r.stdout, "model check: syntax checked, clean")
	assert.Contains(t, r.stdout, "hint: "+strings.Replace(limitHint, "%s", "5", 1))
	assert.Contains(t, r.stdout, "--max-acquisition-visits N, or max_acquisition_visits = N under [tla] in spec.toml")
}
