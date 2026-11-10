// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package termtext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCleanText(t *testing.T) {
	r := func(c rune) string { return string(c) }
	dot, repl := r(0xb7), r(0xfffd)
	emoji := r(0x1f469) + r(0x200d) + r(0x1f4bb)
	for in, want := range map[string]string{
		"plain text\n\tx":                       "plain text\n\tx",
		"a\x1b[31mb":                            "a^[[31mb",
		"bell\x07 del\x7f cr\r":                 "bell^G del^? cr^M",
		"c1 " + r(0x9b) + "31m":                 "c1 " + repl + "31m",
		"bidi " + r(0x202e) + "abc" + r(0x2066): "bidi " + dot + "abc" + dot,
		"zw" + r(0x200b) + "sp " + r(0xfeff):    "zw" + dot + "sp " + dot,
		"bad \xff byte":                         "bad " + repl + " byte",
		"emoji " + emoji:                        "emoji " + emoji,
	} {
		assert.Equal(t, want, Clean(in), "%q", in)
	}
}
