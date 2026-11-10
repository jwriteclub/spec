// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nvimInit is the Neovim configuration of `spec explain integrations`, with
// the binary's path, plus a script that hovers $Rws.Session in hls.spec.md,
// writes the result to OUT and quits.
const nvimInit = `
vim.lsp.config('spec', { cmd = { BIN, 'lsp' }, filetypes = { 'markdown' }, root_markers = { 'spec.toml' } })
vim.lsp.enable('spec')
vim.api.nvim_create_autocmd('LspAttach', { callback = function(args)
  local client = vim.lsp.get_client_by_id(args.data.client_id)
  vim.defer_fn(function()
    local params = { textDocument = { uri = vim.uri_from_bufnr(args.buf) }, position = { line = 7, character = 80 } }
    local res = client:request_sync('textDocument/hover', params, 5000, args.buf)
    local f = io.open(OUT, 'w')
    f:write(vim.json.encode(res and res.result or vim.NIL))
    f:close()
    vim.cmd('qa!')
  end, 100)
end })
vim.defer_fn(function() vim.cmd('cquit 2') end, 20000)
`

// TestNeovimHover is acceptance criterion 5: with nvim installed, a headless
// Neovim using the documented configuration gets a hover.
func TestNeovimHover(t *testing.T) {
	nvim, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("nvim is not installed")
	}
	bin := buildSpec(t)
	dir, _, _, _ := multi(t)
	out := filepath.Join(t.TempDir(), "hover.json")
	init := filepath.Join(t.TempDir(), "init.lua")
	lua := "BIN = " + luaString(bin) + "\nOUT = " + luaString(out) + "\n" + nvimInit
	require.NoError(t, os.WriteFile(init, []byte(lua), 0o644))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nvim, "--headless", "-u", init, filepath.Join(dir, "hls.spec.md"))
	cmd.Dir = dir
	msg, err := cmd.CombinedOutput()
	require.NoError(t, err, "nvim: %s", msg)
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(got), "$Rws.Session")
}

// luaString quotes s as a Lua string literal.
func luaString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
