// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package formal

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jwriteclub/spec/internal/pos"
)

// TestDiffMapsOnlyUnchangedBytes: "abcXYZdef" became "abcQdef": a range
// maps when its bytes are all before or all after the change, or when it holds
// the whole change; one the change cuts does not.
func TestDiffMapsOnlyUnchangedBytes(t *testing.T) {
	m := diffOf([]byte("abcXYZdef"), []byte("abcQdef"))
	assert.Equal(t, diff{prefix: 3, oldEnd: 6, delta: -2}, m)
	r := func(s, e int) pos.Range { return pos.Range{Start: s, End: e} }
	for _, c := range []struct {
		in, exact, around pos.Range
		okExact, okAround bool
	}{
		{in: r(0, 3), exact: r(0, 3), around: r(0, 3), okExact: true, okAround: true},
		{in: r(6, 9), exact: r(4, 7), around: r(4, 7), okExact: true, okAround: true},
		{in: r(3, 3), exact: r(3, 3), around: r(3, 3), okExact: true, okAround: true},
		{in: r(2, 7), around: r(2, 5), okAround: true},
		{in: r(2, 4)},
		{in: r(4, 8)},
		{in: r(3, 9)},
		{in: r(0, 6)},
	} {
		got, ok := m.exact(c.in)
		assert.Equal(t, c.okExact, ok, "exact %v", c.in)
		if ok {
			assert.Equal(t, c.exact, got, "exact %v", c.in)
		}
		got, ok = m.around(c.in)
		assert.Equal(t, c.okAround, ok, "around %v", c.in)
		if ok {
			assert.Equal(t, c.around, got, "around %v", c.in)
		}
	}
	assert.Equal(t, 2, m.near(2))
	assert.Equal(t, 3, m.near(4))
	assert.Equal(t, 5, m.near(7))
}

// TestDiffOfAnInsertion: the common prefix and suffix do not overlap when the
// inserted text repeats its neighbours.
func TestDiffOfAnInsertion(t *testing.T) {
	m := diffOf([]byte("aa"), []byte("aaa"))
	assert.Equal(t, diff{prefix: 2, oldEnd: 2, delta: 1}, m)
	got, ok := m.exact(pos.Range{Start: 0, End: 2})
	assert.True(t, ok)
	assert.Equal(t, pos.Range{Start: 0, End: 2}, got)
	m = diffOf([]byte("abc"), nil)
	assert.Equal(t, diff{prefix: 0, oldEnd: 3, delta: -3}, m)
}
