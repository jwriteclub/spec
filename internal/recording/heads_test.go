// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer"
)

// headsCanned opens a canned provider on replies (two check entries, one
// gate entry).
func headsCanned(t *testing.T) *infer.Canned {
	t.Helper()
	path := filepath.Join(t.TempDir(), "canned.toml")
	require.NoError(t, os.WriteFile(path, []byte("[[check]]\nreply = \"a\"\n[[check]]\nreply = \"b\"\n[[gate]]\nscores = { q = 0.5 }\n"), 0o644))
	c, err := infer.NewCanned("cf", config.Provider{Kind: config.KindCanned, Path: path}, func(string) string { return "" })
	require.NoError(t, err)
	return c
}

func TestHeadChecker(t *testing.T) {
	recorded := []RequestRecord{
		{Purpose: "check", Index: 1, Head: infer.RequestHead(chat("second", "check"))},
		{Purpose: "check", Index: 0, Head: infer.RequestHead(chat("first", "check"))},
		{Purpose: "gate", Index: 0, Head: infer.GateHead(infer.GateRequest{Situation: "gate one"})},
	}
	ctx := context.Background()

	t.Run("same order", func(t *testing.T) {
		var warns []string
		h := NewHeadChecker(recorded, func(s string) { warns = append(warns, s) })
		c := headsCanned(t)
		p, g := h.WrapProvider(c), h.WrapGate(c)
		r, err := p.Chat(ctx, chat("first", "check"))
		require.NoError(t, err)
		assert.Equal(t, "a", r.Content)
		_, err = p.Chat(ctx, chat("second", ""))
		require.NoError(t, err)
		_, err = g.Decide(ctx, infer.GateRequest{Situation: "gate one", Questions: []infer.Question{{ID: "q"}}})
		require.NoError(t, err)
		assert.Empty(t, warns)
		assert.Empty(t, h.Unused())
	})

	t.Run("reordered", func(t *testing.T) {
		var warns []string
		h := NewHeadChecker(recorded, func(s string) { warns = append(warns, s) })
		p := h.WrapProvider(headsCanned(t))
		_, err := p.Chat(ctx, chat("second", "check"))
		require.NoError(t, err, "the canned reply is served anyway")
		require.Len(t, warns, 1)
		assert.Contains(t, warns[0], "model call check #1 differs")
		assert.Contains(t, warns[0], "first")
		assert.Equal(t, []string{"check: 1 of 2 recorded model calls made", "gate: 0 of 1 recorded model calls made"}, h.Unused())
	})

	t.Run("extra call", func(t *testing.T) {
		var warns []string
		h := NewHeadChecker(recorded, func(s string) { warns = append(warns, s) })
		p := h.WrapProvider(headsCanned(t))
		_, err := p.Chat(ctx, chat("extra", "triage"))
		require.Error(t, err, "the canned provider has no triage entry")
		require.Len(t, warns, 1)
		assert.Contains(t, warns[0], "model call triage #1 is not in the recording")
	})
}
