// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package formal_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jwriteclub/spec/internal/formal"
)

// recordingChecker keeps the last request's limits.
type recordingChecker struct{ got *formal.CheckLimits }

func (c recordingChecker) Check(_ context.Context, req formal.CheckRequest) (formal.Report, error) {
	*c.got = req.Limits
	return formal.Report{}, nil
}

// WithLimits overrides the request's bounds with those it sets and leaves the
// rest; with none set, or no checker, it changes nothing.
func TestWithLimits(t *testing.T) {
	var got formal.CheckLimits
	inner := recordingChecker{got: &got}

	_, _ = formal.WithLimits(inner, formal.CheckLimits{MaxAcquisitionVisits: 7}).Check(context.Background(),
		formal.CheckRequest{Limits: formal.CheckLimits{MaxAcquisitionVisits: 9}})
	assert.Equal(t, 7, got.MaxAcquisitionVisits, "the flag wins over spec.toml")

	_, _ = formal.WithLimits(inner, formal.CheckLimits{MaxAcquisitionVisits: 7}).Check(context.Background(), formal.CheckRequest{})
	assert.Equal(t, 7, got.MaxAcquisitionVisits)

	assert.Equal(t, formal.Checker(inner), formal.WithLimits(inner, formal.CheckLimits{}))
	assert.Nil(t, formal.WithLimits(nil, formal.CheckLimits{MaxAcquisitionVisits: 7}))

	l := formal.CheckLimits{MaxAcquisitionVisits: 9}
	assert.Equal(t, l, l.Override(formal.CheckLimits{}), "an unset bound keeps spec.toml's")
}
