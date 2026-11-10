// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package lsp

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/spell"
)

// spellSource is the source of a spelling diagnostic.
const spellSource = "spell"

// Commands the server executes (workspace/executeCommand).
const (
	// commandAddWord appends a word to the project's spec.words; its
	// arguments are the project root and the word.
	commandAddWord = "spec.addWord"
	// commandSpellCheck runs the spell check's model pass over the
	// project of the document named by its one argument (a URI), or over
	// every open project without one.
	commandSpellCheck = "spec.spellCheck"
)

// commands are the commands the server executes: spec.spellCheck only
// when it has a model pass (Options.Spell).
func (s *Server) commands() []string {
	out := []string{commandAddWord}
	if s.opts.Spell != nil {
		out = append(out, commandSpellCheck)
	}
	return out
}

// spellDone is a finished model pass.
type spellDone struct {
	root string
	err  error
}

// spellFlags returns the spelling flags of loaded: every known
// misspelling, and the verdicts the project's spell cache holds that still
// apply, recomputed through memo only for the files that changed. Nothing
// is read when no spell check has run.
func spellFlags(loaded *project.Loaded, memo *spell.Memo) []spell.Flag {
	root := loaded.Project.Root
	cache := spell.OpenCache(root)
	if loaded.Project.Config.Lint[diag.CodeSpell] == string(lint.Off) || !cache.Exists() {
		return nil
	}
	words, err := spell.ReadWords(root)
	if err != nil {
		lg.WithError(err).Warn("cannot read spec.words")
	}
	return memo.Flags(loaded.Spec, words, loaded.Project.Config.Name, cache)
}

// spellFixes offers the fix of a spelling flag and adding the word to
// spec.words.
func (s *Server) spellFixes(v view, d diag.Diagnostic) []protocol.CommandOrCodeAction {
	for _, f := range v.snap.spell {
		if f.File != d.File || f.Range != d.Range {
			continue
		}
		var out []protocol.CommandOrCodeAction
		if f.Fix != "" {
			out = append(out, s.quickFix(v, d, fmt.Sprintf("Change to %q", f.Fix), f.Range, f.Fix, true))
		}
		kind := protocol.CodeActionKindQuickFix
		root, _ := json.Marshal(v.p.root)
		word, _ := json.Marshal(f.Word)
		title := fmt.Sprintf("Add %q to %s", f.Word, spell.WordsFile)
		out = append(out, &protocol.CodeAction{
			Title: title, Kind: &kind,
			Diagnostics: []protocol.Diagnostic{s.lspDiagnostic(v.p, v.file.Index(), d)},
			Command:     protocol.Command{Title: title, Command: commandAddWord, Arguments: []protocol.LSPAny{root, word}},
		})
		return out
	}
	return nil
}

// executeCommand runs spec.addWord or spec.spellCheck.
func (s *Server) executeCommand(p *protocol.ExecuteCommandParams) (any, *rpcError) {
	var args []string
	for _, a := range p.Arguments {
		var str string
		if err := json.Unmarshal(a, &str); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "the arguments must be strings"}
		}
		args = append(args, str)
	}
	switch p.Command {
	case commandAddWord:
		if len(args) != 2 {
			return nil, &rpcError{Code: codeInvalidParams, Message: commandAddWord + " takes the project root and the word"}
		}
		ps := s.projects[filepath.Clean(args[0])]
		if ps == nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "no open project at " + args[0]}
		}
		if _, err := spell.AddWord(ps.root, args[1]); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
		}
		s.analyze(ps)
		return nil, nil
	case commandSpellCheck:
		if s.opts.Spell == nil {
			return nil, &rpcError{Code: codeInvalidRequest, Message: "the spell check's models are not available in this server"}
		}
		var roots []string
		if len(args) > 0 {
			d := s.docs[s.docPath(args[0])]
			if d == nil || d.proj == nil {
				return nil, &rpcError{Code: codeInvalidParams, Message: "no open spec document " + args[0]}
			}
			roots = append(roots, d.proj.root)
		} else {
			for root := range s.projects {
				roots = append(roots, root)
			}
		}
		for _, root := range roots {
			s.startSpell(root)
		}
		return nil, nil
	}
	return nil, &rpcError{Code: codeInvalidParams, Message: "unknown command " + p.Command}
}

// docPath returns the absolute path of a document URI ("" when it is not a
// file URI).
func (s *Server) docPath(u string) string {
	abs, err := pathOf(uri.URI(u))
	if err != nil {
		return ""
	}
	return abs
}

// startSpell runs the model pass for root in the background; Run hears
// when it ends.
func (s *Server) startSpell(root string) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := s.opts.Spell(s.ctx, root)
		select {
		case s.spellCh <- spellDone{root: root, err: err}:
		case <-s.done:
		}
	}()
}

// spellFinished republishes a project whose spell cache a model pass
// filled.
func (s *Server) spellFinished(r spellDone) {
	if r.err != nil {
		lg.WithError(r.err).WithField("root", r.root).Warn("spell check failed; showing cached verdicts")
		s.sendNotification("window/showMessage", &protocol.ShowMessageParams{Type: protocol.MessageTypeWarning,
			Message: "spec: the spell check's model pass failed: " + r.err.Error()})
	}
	if p := s.projects[r.root]; p != nil {
		s.analyze(p)
	}
}
