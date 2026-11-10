// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tla

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/formal"
)

// limitModel is a small clean model: far under prudence's default budget,
// far over a budget of 5.
var limitModel = wrap("EXTENDS Naturals\nVARIABLES x\nInit == x = 0\nNext == x' = x + 1")

// A budget too small for the model reaches prudence and stops the check at
// the acquisition limit: one incomplete hint that names the flag and the
// spec.toml key, detected by prudence's code.
func TestCheckerAcquisitionLimit(t *testing.T) {
	rep, err := Checker{}.Check(context.Background(), formal.CheckRequest{
		Path: "tla/M.tla", Src: []byte(limitModel), Limits: formal.CheckLimits{MaxAcquisitionVisits: 5},
	})
	require.NoError(t, err)
	assert.Equal(t, formal.CompletionIncomplete, rep.Completion)
	require.Len(t, rep.Diagnostics, 1)
	d := rep.Diagnostics[0]
	assert.Equal(t, CodeAcquisitionLimit, d.Code)
	assert.Equal(t, "analysis/acquisition-limit", d.Code)
	assert.Equal(t, formal.SeverityHint, d.Severity)
	assert.True(t, d.Incomplete)
	assert.Equal(t, "tla/M.tla", d.Path)
	assert.Equal(t, "the model hit the acquisition limit of 5 visits, so its semantic check is incomplete; "+
		"raise it with --max-acquisition-visits N, or max_acquisition_visits = N under [tla] in spec.toml", d.Message)
	assert.NotContains(t, d.Message, "Options.", "prudence's Go option is not what a user sets")
	assert.Equal(t, d.Message, rep.Incomplete, "the status line's reason")
	assert.Zero(t, rep.Errors(), "a stopped check refuses nothing")
	assert.False(t, formal.FullCheck(rep.Level, rep.Completion))
}

// With no limit, or one large enough, the same model checks completely.
func TestCheckerAcquisitionLimitRaised(t *testing.T) {
	for _, n := range []int{0, DefaultMaxAcquisitionVisits, 50000} {
		rep, err := Checker{}.Check(context.Background(), formal.CheckRequest{
			Path: "tla/M.tla", Src: []byte(limitModel), Limits: formal.CheckLimits{MaxAcquisitionVisits: n},
		})
		require.NoError(t, err, n)
		assert.Equal(t, formal.CompletionComplete, rep.Completion, n)
		assert.Empty(t, rep.Diagnostics, n)
	}
}

// The message names the default when no limit was set.
func TestAcquisitionLimitMessage(t *testing.T) {
	assert.Equal(t, 1000000, DefaultMaxAcquisitionVisits)
	assert.Contains(t, AcquisitionLimitMessage(0), "limit of 1000000 visits")
	assert.Contains(t, AcquisitionLimitMessage(2100000), "limit of 2100000 visits")
}
