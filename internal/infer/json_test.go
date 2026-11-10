// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"bare", `{"a":1}`, `{"a":1}`},
		{"whitespace", "\n  {\"a\":1}\n", `{"a":1}`},
		{"json fence", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"plain fence with prose", "Here you go:\n```\n{\"a\": [1, 2]}\n```\nAnything else?", `{"a": [1, 2]}`},
		{"unclosed fence", "```json\n{\"a\":1}\n", `{"a":1}`},
		{"second fence holds the object", "```\nnot json\n```\nthen\n```json\n{\"b\":2}\n```", `{"b":2}`},
		{"prose around", `The answer is {"a": {"b": "}"}} as requested.`, `{"a": {"b": "}"}}`},
		{"escaped quote in string", `x {"a": "say \"{hi\""} y`, `{"a": "say \"{hi\""}`},
		{"think block", "<think>maybe {\"wrong\": true}</think>\n{\"right\": true}", `{"right": true}`},
		{"unclosed think", "{\"a\":1}<think>{\"b\":2}", `{"a":1}`},
		{"skips an invalid candidate", `{not json} and {"ok": true}`, `{"ok": true}`},
		{"outermost wins", `{"outer": {"inner": 1}}`, `{"outer": {"inner": 1}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractJSON(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
		})
	}
	for _, in := range []string{"", "no json here", "[1, 2]", "{unclosed", `"just a string"`, "<think>{\"a\":1}</think>"} {
		_, err := ExtractJSON(in)
		assert.ErrorIs(t, err, ErrNoJSON, in)
	}
}

func TestExtractJSONBounded(t *testing.T) {
	// A long reply of unmatched braces does not go quadratic.
	in := strings.Repeat("{", 200000)
	_, err := ExtractJSON(in)
	assert.ErrorIs(t, err, ErrNoJSON)
}

func FuzzExtractJSON(f *testing.F) {
	for _, s := range []string{`{"a":1}`, "```json\n{}\n```", `x {"a": "}"} y`, "<think>", "{{{", `{"a":"\`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ExtractJSON(s)
		if err != nil {
			return
		}
		if !json.Valid(got) || got[0] != '{' {
			t.Fatalf("ExtractJSON(%q) = %q, not a JSON object", s, got)
		}
	})
}
