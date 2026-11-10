// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/spell"
)

// SpellGateTimeout bounds one Jev request of the spell check when the
// gate provider sets no timeout.
const SpellGateTimeout = 10 * time.Second

// SpellTimeout bounds a whole spell check's model pass.
const SpellTimeout = 120 * time.Second

// SpellOff reports whether spec.toml turns the spell check off
// ([lint] spell = "off").
func SpellOff(proj *project.Project) bool {
	return proj != nil && proj.Config.Lint[diag.CodeSpell] == string(lint.Off)
}

// SpellProject builds the spell check's classical pass for proj over spec:
// the word list (w.SpellDict, else the system's), spec.words and the
// project's vocabulary.
func (w *Workflow) SpellProject(proj *project.Project, spec *mdspec.Spec) *spell.Project {
	words, err := spell.ReadWords(proj.Root)
	if err != nil {
		lg.WithError(err).Warn("cannot read spec.words; ignoring it")
	}
	return spell.NewProject(spec, words, proj.Config.Name, w.SpellDict)
}

// SpellModels opens the spell check's model pass from the user config: the
// [gate] provider (Jev) and the [triage] provider. It returns nil and no
// error when either table is missing (the model pass is off), and an error
// when they are set but cannot be opened.
func (w *Workflow) SpellModels() (*spell.Models, error) {
	path := w.UserConfigPath()
	if path == "" {
		return nil, nil
	}
	loaded, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if !loaded.Exists {
		return nil, nil
	}
	cfg := loaded.Config
	if cfg.Gate == nil || cfg.Triage == nil {
		return nil, nil
	}
	if len(loaded.Diagnostics) > 0 {
		return nil, fmt.Errorf("%s: %s", path, loaded.Diagnostics[0].Message)
	}
	gp, ok := cfg.Providers[cfg.Gate.Provider]
	if !ok {
		return nil, fmt.Errorf("[gate] provider %q names no [providers.%s] table", cfg.Gate.Provider, cfg.Gate.Provider)
	}
	resolved, getenv, err := w.credentialProvider(cfg, path, cfg.Gate.Provider, gp)
	if err != nil {
		return nil, fmt.Errorf("cannot open the [gate] provider: %w", err)
	}
	gate, err := infer.OpenGate(cfg.Gate.Provider, resolved, getenv)
	if err != nil {
		return nil, fmt.Errorf("cannot open the [gate] provider: %w", err)
	}
	timeout := SpellGateTimeout
	if gp.Timeout > 0 {
		timeout = time.Duration(gp.Timeout) * time.Second
	}
	gate.SetLimits(timeout, 2)
	tp, ok := cfg.Providers[cfg.Triage.Provider]
	if !ok {
		return nil, fmt.Errorf("[triage] provider %q names no [providers.%s] table", cfg.Triage.Provider, cfg.Triage.Provider)
	}
	resolved, getenv, err = w.credentialProvider(cfg, path, cfg.Triage.Provider, tp)
	if err != nil {
		return nil, fmt.Errorf("cannot open the [triage] provider: %w", err)
	}
	tri, err := infer.Open(cfg.Triage.Provider, resolved, getenv)
	if err != nil {
		return nil, fmt.Errorf("cannot open the [triage] provider: %w", err)
	}
	if hc, ok := tri.(*infer.Client); ok && w.TuneClient != nil {
		w.TuneClient(hc)
	}
	sp, err := prompt.LoadSpell(config.PromptPath(cfg.Prompts.Spell, loaded.Path, w.home()))
	if err != nil {
		return nil, fmt.Errorf("%w; fix the [prompts] spell entry in %s", err, path)
	}
	return &spell.Models{
		Gate: w.wrapGate(gate), Triage: w.wrapProvider(tri),
		Prompt: spell.Prompt{Policy: sp.Policy, Question: sp.Question, Reply: sp.Reply},
	}, nil
}

// Spell runs the spell check over spec's files in scope: known
// misspellings, cached verdicts and, when the user config names a [gate]
// and a [triage] provider, the model pass. It never fails: a model pass
// that cannot run says why in the report's Error and fails closed.
func (w *Workflow) Spell(ctx context.Context, proj *project.Project, spec *mdspec.Spec, scope spell.Scope) *spell.Report {
	var files []*mdspec.File
	for _, f := range spec.Files {
		if f.ValidUTF8 {
			files = append(files, f)
		}
	}
	return w.spellFiles(ctx, proj, spec, files, scope)
}

// SpellProposals spell-checks the proposed text of each proposal (its
// primary's new text), against the vocabulary of spec. Flags name the
// anchor ("$Name") as their file and count lines from the proposed
// text's first. It is nil when no proposal has new text.
func (w *Workflow) SpellProposals(ctx context.Context, proj *project.Project, spec *mdspec.Spec, ps []*proposal.Proposal) *spell.Report {
	var files []*mdspec.File
	seen := map[string]bool{}
	for _, p := range ps {
		if p == nil || p.Primary == nil || p.Primary.NewText == "" || seen[p.Primary.Name] {
			continue
		}
		seen[p.Primary.Name] = true
		files = append(files, mdspec.ParseFile(p.Primary.Name, []byte(p.Primary.NewText)))
	}
	if len(files) == 0 {
		return nil
	}
	return w.spellFiles(ctx, proj, spec, files, nil)
}

// spellFiles runs the spell check over files with spec's vocabulary.
func (w *Workflow) spellFiles(ctx context.Context, proj *project.Project, spec *mdspec.Spec, files []*mdspec.File, scope spell.Scope) *spell.Report {
	if SpellOff(proj) {
		return &spell.Report{Flags: []spell.Flag{}, Off: true}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p := w.SpellProject(proj, spec)
	m, err := w.SpellModels()
	if err != nil {
		lg.WithError(err).Warn("spell: the model pass is off")
	}
	ctx, cancel := context.WithTimeoutCause(ctx, SpellTimeout, fmt.Errorf("the spell check took longer than %s: %w", SpellTimeout, context.DeadlineExceeded))
	defer cancel()
	rep := spell.Run(ctx, p, files, scope, m, spell.OpenCache(proj.Root))
	if err != nil {
		rep.Error = errors.Join(err, errorOf(rep.Error)).Error()
	}
	return rep
}

// errorOf turns a report's error text back into an error (nil for "").
func errorOf(s string) error {
	if s == "" {
		return nil
	}
	return errors.New(s)
}

// SpellNotes spell-checks the proposed text of ps (against the working
// tree's vocabulary) and adds a note per flag to the proposal it is in:
// issue spelling, severity cosmetic, the word as find. spec ui shows them
// on the proposal's cards. A spell check that cannot run adds nothing.
func (w *Workflow) SpellNotes(ctx context.Context, proj *project.Project, ps []*proposal.Proposal) *spell.Report {
	loaded, err := proj.Load(nil)
	if err != nil {
		lg.WithError(err).Warn("spell: cannot load the project")
		return nil
	}
	rep := w.SpellProposals(ctx, proj, loaded.Spec, ps)
	if rep == nil {
		return nil
	}
	for _, f := range rep.Flags {
		for _, p := range ps {
			if p == nil || p.Primary == nil || p.Primary.Name != f.File {
				continue
			}
			p.Notes = append(p.Notes, finding.Note{
				File: p.Primary.File, Anchor: p.Primary.Name, Issue: finding.IssueSpelling, Severity: finding.SeverityCosmetic,
				Explanation: spell.Message(f), Find: f.Word, Reason: "spell check",
			})
		}
	}
	return rep
}
