// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package fsnote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestObserver(t *testing.T) {
	Wrote("/x", []byte("a")) // no observer: nothing happens
	var got []Change
	restore := SetObserver(func(c Change) { got = append(got, c) })
	Wrote("/a", []byte("1"))
	Removed("/b")
	Quiet(func() { Wrote("/c", []byte("2")) })
	restore()
	Wrote("/d", []byte("3"))
	assert.Equal(t, []Change{{Path: "/a", Data: []byte("1")}, {Path: "/b", Removed: true}}, got)
}
