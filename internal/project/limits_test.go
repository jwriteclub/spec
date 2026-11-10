// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
)

// [tla] max_acquisition_visits is a positive integer; anything else is
// config-invalid on the key, and the default applies.
func TestParseConfigTLA(t *testing.T) {
	cfg, ds := ParseConfig([]byte("[tla]\nmax_acquisition_visits = 2100000\n"))
	assert.Empty(t, ds)
	assert.Equal(t, formal.CheckLimits{MaxAcquisitionVisits: 2100000}, cfg.CheckLimits())

	cfg, ds = ParseConfig([]byte("formal = [\"*.tla\"]\n"))
	assert.Empty(t, ds)
	assert.Equal(t, formal.CheckLimits{}, cfg.CheckLimits(), "unset is prudence's default")

	for _, v := range []string{"0", "-3"} {
		src := "[tla]\nmax_acquisition_visits = " + v + "\n"
		cfg, ds = ParseConfig([]byte(src))
		require.Len(t, ds, 1, v)
		assert.Equal(t, diag.CodeConfigInvalid, ds[0].Code)
		assert.Equal(t, "[tla] max_acquisition_visits must be a positive number of visits, not "+v, ds[0].Message)
		assert.Equal(t, "max_acquisition_visits", src[ds[0].Range.Start:ds[0].Range.End])
		assert.Equal(t, formal.CheckLimits{}, cfg.CheckLimits(), "the default applies")
	}

	_, ds = ParseConfig([]byte("[tla]\nmax_acquisition_visits = \"lots\"\n"))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "tla.max_acquisition_visits")

	_, ds = ParseConfig([]byte("[tla]\nmax_visits = 3\n"))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, `unknown key "tla.max_visits"`)
}

// A model too large for spec.toml's limit draws a formal-check-limit hint,
// the only finding; a new limit checks it again, and a large enough one
// clears it.
func TestCheckModelsAcquisitionLimit(t *testing.T) {
	cc := &countingChecker{inner: formal.CheckerFor("x.tla"), n: map[string]int{}}
	formal.RegisterChecker(".tla", cc)
	t.Cleanup(func() { formal.RegisterChecker(".tla", cc.inner) })
	cache := NewCheckCache()
	root := t.TempDir()
	models := checkFiles("tla/Top.tla", checkTop, "tla/Base.tla", checkLib)

	got := cache.CheckModels(context.Background(), root, models, formal.CheckLimits{MaxAcquisitionVisits: 5})
	top := got["tla/Top.tla"]
	require.True(t, top.Ran)
	assert.Equal(t, formal.CompletionIncomplete, top.Report.Completion)
	require.Len(t, top.Diags, 1)
	d := top.Diags[0]
	assert.Equal(t, diag.CodeFormalCheckLimit, d.Code)
	assert.Equal(t, diag.Hint, d.Severity)
	assert.Equal(t, "tla/Top.tla", d.File)
	assert.Contains(t, d.Message, "the model hit the acquisition limit of 5 visits")
	assert.Contains(t, d.Message, "--max-acquisition-visits")
	assert.Contains(t, d.Message, "max_acquisition_visits = N under [tla] in spec.toml")
	assert.False(t, d.Range.Empty(), "an editor can underline it")
	assert.Contains(t, top.Note(), "the model hit the acquisition limit", "spec ui's status line")
	assert.Equal(t, 1, cc.n["tla/Top.tla"])

	cache.CheckModels(context.Background(), root, models, formal.CheckLimits{MaxAcquisitionVisits: 5})
	assert.Equal(t, 1, cc.n["tla/Top.tla"], "the same limit reuses the check")

	got = cache.CheckModels(context.Background(), root, models, formal.CheckLimits{})
	assert.Equal(t, 2, cc.n["tla/Top.tla"], "a new limit checks again")
	top = got["tla/Top.tla"]
	assert.Equal(t, formal.CompletionComplete, top.Report.Completion)
	assert.Empty(t, top.Diags)
}
