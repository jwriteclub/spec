// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		arg  string
		want target
	}{
		{"Frame.Timing", target{kind: targetAnchor, name: "Frame.Timing"}},
		{"$Frame.Timing", target{kind: targetAnchor, name: "Frame.Timing"}},
		{"Frame", target{kind: targetAnchor, name: "Frame"}},
		{"5", target{kind: targetSection, number: "5"}},
		{"5.2a", target{kind: targetSection, number: "5.2a"}},
		{"5.2a.", target{kind: targetSection, number: "5.2a"}},
		{"§5.2a", target{kind: targetSection, number: "5.2a"}},
		{"§ 5.2a", target{kind: targetSection, number: "5.2a"}},
		{"#5.2a", target{kind: targetSection, number: "5.2a"}},
		{"hls:5.2a", target{kind: targetSection, stem: "hls", number: "5.2a"}},
		{"§hls:5.2a", target{kind: targetSection, stem: "hls", number: "5.2a"}},
		{"#hls:5", target{kind: targetSection, stem: "hls", number: "5"}},
		{"my-spec:1.1", target{kind: targetSection, stem: "my-spec", number: "1.1"}},
		{" Frame.Timing ", target{kind: targetAnchor, name: "Frame.Timing"}},
	}
	for _, c := range cases {
		got, o := parseTarget(c.arg)
		require.Nil(t, o, c.arg)
		assert.Equal(t, c.want, got, c.arg)
	}

	// What a shell leaves of an expanded $Name, and arguments that name nothing.
	for _, arg := range []string{"", ".Timing", "$.Timing", "$", "§", "#", "hls:", ":5"} {
		_, o := parseTarget(arg)
		require.NotNil(t, o, "%q", arg)
		assert.Equal(t, exitFail, o.exit)
		assert.Equal(t, "usage", o.code)
	}
	_, o := parseTarget(".Timing")
	assert.Contains(t, o.message, "did your shell expand `$`? write `Frame.Timing`")
}

func TestDefaultProjectName(t *testing.T) {
	cases := map[string]string{
		"lensd":           "lensd",
		"/x/My_Cool Proj": "my-cool-proj",
		"/x/--weird--":    "weird",
		"/x/Lens.Gate":    "lens-gate",
		"/x/2026":         "2026",
		"/x/___":          "",
	}
	for in, want := range cases {
		assert.Equal(t, want, defaultProjectName(in), in)
	}
}

func TestGitignoreHas(t *testing.T) {
	assert.True(t, gitignoreHas(".spec/\n"))
	assert.True(t, gitignoreHas("a\n  /.spec  \nb"))
	assert.False(t, gitignoreHas("a\n.specs/\n# .spec/\n"))
	assert.False(t, gitignoreHas(""))
}

func TestPlural(t *testing.T) {
	assert.Equal(t, "1 error", plural(1, "error"))
	assert.Equal(t, "0 errors", plural(0, "error"))
	assert.Equal(t, "2 hints", plural(2, "hint"))
}
