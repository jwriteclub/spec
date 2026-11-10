// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// texts returns the token texts.
func texts(ts []Token) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.Text)
	}
	return out
}

func TestTokenizeSkipsMarkupAndReferences(t *testing.T) {
	src := "---\ntitle: frontmattr\n---\n\n# Overvew\n\n" +
		"**$Frame.Timng (Frame timnig).** The hub MUST recieve `codespn` before $Retry.Schedle and §5.2 apply.\n\n" +
		"```go\nfunc bloksz() {}\n```\n\n" +
		"See [the docz](https://exmple.com/pathh) and <https://autolnk.example> or https://barelnk.example/x.\n" +
		"Hash #5.2 and path /usr/lib/foo and mail me@exampel.com and deadbeef1234 and v1.2.3 ok.\n" +
		"<!-- spell:ignore hiddn -->\n"
	f := mdspec.ParseFile("demo.spec.md", []byte(src))
	got := texts(Tokenize(f))
	assert.Contains(t, got, "Overvew")
	assert.Contains(t, got, "timnig", "an anchor's title is prose")
	assert.Contains(t, got, "recieve")
	assert.Contains(t, got, "docz", "link text is prose")
	for _, skip := range []string{"frontmattr", "Frame.Timng", "Timng", "codespn", "Retry.Schedle", "Schedle", "bloksz",
		"exmple", "pathh", "autolnk", "barelnk", "usr", "exampel", "deadbeef1234", "hiddn"} {
		assert.NotContains(t, got, skip)
	}
	for _, tk := range Tokenize(f) {
		assert.Equal(t, tk.Text, string(f.Source()[tk.Range.Start:tk.Range.End]), "ranges index the source")
	}
}

func TestTokenizeTrimsAndPossessives(t *testing.T) {
	f := mdspec.ParseFile("a.spec.md", []byte("The camera's stream (_emphasised_) ends -- 'quoted' max_transcoders. It\u2019s done.\n"))
	got := texts(Tokenize(f))
	assert.Equal(t, []string{"The", "camera", "stream", "emphasised", "ends", "quoted", "max_transcoders", "It", "done"}, got)
}

func TestSplit(t *testing.T) {
	cases := map[string][]string{
		"liveIdleTimout":    {"live", "Idle", "Timout"},
		"max_transcodres":   {"max", "transcodres"},
		"parseHTMLTokens":   {"parse", "HTML", "Tokens"},
		"First10HTMLTokens": {"First", "HTML", "Tokens"},
		"Pool.EvictOldset":  {"Pool", "Evict", "Oldset"},
		"start-up":          {"start", "up"},
		"sqlite3":           {"sqlite"},
		"HTTPS":             {"HTTPS"},
		"word":              {"word"},
	}
	for in, want := range cases {
		var got []string
		for _, p := range Split(in) {
			got = append(got, p.Text)
			assert.Equal(t, p.Text, in[p.Off:p.Off+len(p.Text)], "offset of %q in %q", p.Text, in)
		}
		assert.Equal(t, want, got, in)
	}
}

func TestSentence(t *testing.T) {
	src := []byte("First sentence here. The client MUST   recieve\nan ack before it retries. Next one.\n\nOther block.")
	i := bytes.Index(src, []byte("recieve"))
	require.Equal(t, "recieve", string(src[i:i+7]))
	s, mark := sentence(src, pos.Range{Start: i, End: i + 7}, 240)
	assert.Equal(t, "The client MUST recieve an ack before it retries.", s)
	assert.Equal(t, "recieve", s[mark:mark+7])
}

func TestMatchCase(t *testing.T) {
	assert.Equal(t, "Receive", MatchCase("Recieve", "receive"))
	assert.Equal(t, "RECEIVE", MatchCase("RECIEVE", "receive"))
	assert.Equal(t, "receive", MatchCase("recieve", "receive"))
	assert.Equal(t, "liveIdleTimeout", MatchCase("liveIdleTimout", "liveIdleTimeout"))
}

func TestDistance(t *testing.T) {
	assert.Equal(t, 0, Distance("abc", "abc"))
	assert.Equal(t, 1, Distance("recieve", "receive"), "a transposition costs 1")
	assert.Equal(t, 1, Distance("untill", "until"))
	assert.Equal(t, 1, Distance("dealy", "delay"))
	assert.Equal(t, 1, Distance("transcodres", "transcoders"))
	assert.Equal(t, 1, Distance("goroutnie", "goroutine"))
	assert.Equal(t, 2, Distance("seperatly", "separately"))
	assert.Equal(t, 3, Distance("kitten", "sitting"))
}
