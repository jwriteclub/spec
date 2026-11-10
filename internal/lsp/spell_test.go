// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/spell"
)

// spellDiags returns the spelling diagnostics among ds.
func spellDiags(ds []protocol.Diagnostic) []protocol.Diagnostic {
	var out []protocol.Diagnostic
	for _, d := range ds {
		if d.Code == protocol.String(spell.Code) {
			out = append(out, d)
		}
	}
	return out
}

// TestSpellDiagnosticsAndQuickFixes: the server shows nothing until a
// spell check has run; spec.spellCheck runs the models (a canned Jev and
// triage here), after which the flags are information diagnostics from
// source spell with two quick fixes: the correction, and adding the word to
// spec.words (which clears the flag).
func TestSpellDiagnosticsAndQuickFixes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spec.toml"), []byte("name = \"demo\"\n"), 0o644))
	src := "# Demo\n\nThe hub will recieve it.\n\nCameras use zorblat for streams.\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "demo.spec.md"), []byte(src), 0o644))
	canned := filepath.Join(t.TempDir(), "canned.toml")
	require.NoError(t, os.WriteFile(canned, []byte("[[spell_gate]]\nscores = { typo_1 = 0.9 }\n[[spell]]\nreply = \"1=zorblot\"\n"), 0o644))
	t.Cleanup(func() { infer.ForgetCanned(canned) })

	var ran int
	run := func(ctx context.Context, root string) error {
		ran++
		g, err := infer.OpenGate("jev", config.Provider{Kind: config.KindCanned, Path: canned, Model: "jev"}, os.Getenv)
		require.NoError(t, err)
		tr, err := infer.Open("glm", config.Provider{Kind: config.KindCanned, Path: canned, Model: "glm"}, os.Getenv)
		require.NoError(t, err)
		loaded, err := project.Load(root, nil)
		require.NoError(t, err)
		// Only "zorblat" is unknown to this word list (and "zorblot", the
		// fix, is a project word below).
		dict := spell.NewDictionary(strings.Fields("demo the hub will it cameras use for streams zorblot")...)
		p := spell.NewProject(loaded.Spec, []string{"zorblot"}, "", dict)
		m := &spell.Models{Gate: g, Triage: tr, Prompt: spell.Prompt{Policy: "p", Question: "{id}?", Reply: "r"}}
		rep := spell.Run(ctx, p, loaded.Spec.Files, nil, m, spell.OpenCache(root))
		if rep.Error != "" {
			t.Errorf("spell: %s", rep.Error)
		}
		return nil
	}
	c := newClient(t, clientOptions{docChanges: true, spell: run})
	u := c.open(filepath.Join(dir, "demo.spec.md"))
	c.sync()
	assert.Empty(t, spellDiags(c.diagnostics(u)), "nothing before a spell check")

	c.call("workspace/executeCommand", &protocol.ExecuteCommandParams{Command: commandSpellCheck, Arguments: []protocol.LSPAny{mustJSON(t, string(u))}}, nil)
	require.True(t, c.waitFor(func() bool { return len(spellDiags(c.diagnostics(u))) == 2 }), "the flags arrive after the model pass")
	assert.Equal(t, 1, ran)
	ds := spellDiags(c.diagnostics(u))
	assert.Equal(t, protocol.DiagnosticSeverityInformation, ds[0].Severity)
	assert.Equal(t, protocol.NewOptional(spellSource), ds[0].Source)
	assert.Equal(t, protocol.String(`"recieve" may be misspelled: did you mean "receive"?`), ds[0].Message)
	assert.Equal(t, lspRange(2, 13, 20), ds[0].Range)
	assert.Equal(t, lspRange(4, 12, 19), ds[1].Range)

	var raw []protocol.CommandOrCodeAction
	c.call("textDocument/codeAction", &protocol.CodeActionParams{TextDocument: protocol.TextDocumentIdentifier{URI: u}, Range: lspRange(4, 14, 14)}, &raw)
	require.Len(t, raw, 2)
	fix := raw[0].(*protocol.CodeAction)
	assert.Equal(t, `Change to "zorblot"`, fix.Title)
	assert.True(t, *fix.IsPreferred)
	require.NotNil(t, fix.Edit)
	add := raw[1].(*protocol.CodeAction)
	assert.Equal(t, `Add "zorblat" to spec.words`, add.Title)
	assert.Equal(t, commandAddWord, add.Command.Command)
	require.Len(t, add.Command.Arguments, 2)

	params := &protocol.ExecuteCommandParams{Command: add.Command.Command, Arguments: add.Command.Arguments}
	c.call("workspace/executeCommand", params, nil)
	words, err := os.ReadFile(filepath.Join(dir, spell.WordsFile))
	require.NoError(t, err)
	assert.Contains(t, string(words), "\nzorblat\n")
	require.True(t, c.waitFor(func() bool { return len(spellDiags(c.diagnostics(u))) == 1 }), "the added word is no longer flagged")

	resp := c.request("workspace/executeCommand", &protocol.ExecuteCommandParams{Command: commandAddWord, Arguments: []protocol.LSPAny{mustJSON(t, "/elsewhere"), mustJSON(t, "x")}})
	require.NotNil(t, resp.Error, "only an open project's word list can be written")
}

// TestSpellCommandWithoutModels: a server without a model pass does not
// advertise spec.spellCheck and refuses it.
func TestSpellCommandWithoutModels(t *testing.T) {
	c := newClient(t, clientOptions{})
	advertised := func(o Options) []string {
		return New(strings.NewReader(""), io.Discard, o).initialize(&protocol.InitializeParams{}).Capabilities.ExecuteCommandProvider.Commands
	}
	assert.Equal(t, []string{commandAddWord}, advertised(Options{}), "spec.spellCheck is not advertised without a model pass")
	assert.Equal(t, []string{commandAddWord, commandSpellCheck}, advertised(Options{Spell: func(context.Context, string) error { return nil }}))
	resp := c.request("workspace/executeCommand", &protocol.ExecuteCommandParams{Command: commandSpellCheck})
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, "not available")
}

func mustJSON(t *testing.T, v any) protocol.LSPAny {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}
