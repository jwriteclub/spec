// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package diag

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/jwriteclub/spec/internal/golden"
	"github.com/jwriteclub/spec/internal/pos"
)

func TestSeverity(t *testing.T) {
	assert.Equal(t, 0, Error.Rank())
	assert.Equal(t, 1, Warning.Rank())
	assert.Equal(t, 2, Hint.Rank())
	assert.Equal(t, 3, Severity("off").Rank())
	assert.True(t, Hint.Valid())
	assert.False(t, Severity("").Valid())
}

func TestSortAndCount(t *testing.T) {
	ds := []Diagnostic{
		{Code: "b", Severity: Hint, File: "b.spec.md", Range: pos.Range{Start: 1, End: 2}},
		{Code: "z", Severity: Warning, File: "a.spec.md", Range: pos.Range{Start: 5, End: 6}},
		{Code: "a", Severity: Error, File: "a.spec.md", Range: pos.Range{Start: 5, End: 6}},
		{Code: "c", Severity: Error, File: "a.spec.md", Range: pos.Range{Start: 0, End: 9}},
	}
	Sort(ds)
	got := []string{}
	for _, d := range ds {
		got = append(got, d.Code)
	}
	assert.Equal(t, []string{"c", "a", "z", "b"}, got)
	assert.Equal(t, 2, Count(ds, Error))
	assert.True(t, HasErrors(ds))
	assert.False(t, HasErrors(ds[2:]))
}

func TestJSONShape(t *testing.T) {
	d := Diagnostic{Code: CodeRefDangling, Severity: Error, File: "a.spec.md", Range: pos.Range{Start: 3, End: 9}, Message: "m"}
	b, err := json.Marshal(d)
	require.NoError(t, err)
	assert.JSONEq(t, `{"code":"ref-dangling","severity":"error","file":"a.spec.md","range":{"start":3,"end":9},"message":"m"}`, string(b))
}
