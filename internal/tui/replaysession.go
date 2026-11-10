// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/recording"
	"github.com/jwriteclub/spec/internal/workflow"
)

// ReplayConfig says what to replay (spec ui --replay).
type ReplayConfig struct {
	// Dir is the recording.
	Dir string
	// TempParent is where the restored project goes ("" for the system's
	// temp directory).
	TempParent string
	// Flash shows the replay's warnings in the status line.
	Flash bool
	// Getenv is the environment of the terminal the replay shows in (nil
	// for none). Its terminalKeys decide the colour depth as they do for
	// `spec ui`; the rest of the appearance is the recording's.
	Getenv func(string) string
}

// terminalKeys are the variables of the replaying terminal the restored
// environment takes: what applyAppearance reads to detect reduced colour.
var terminalKeys = []string{"TERM", "COLORTERM", "TERM_PROGRAM"}

// ReplaySession is a recording restored for its replay: open the UI with
// Options (adding the target and anything the terminal needs), run it,
// then Close.
type ReplaySession struct {
	Prepared *recording.Prepared
	Replay   *Replay
	Heads    *recording.HeadChecker
	Workflow *workflow.Workflow
	// Options are the UI's: the restored project, the recording's theme
	// (its background from meta, its colour scheme and profiles from its
	// config.toml) and #→§ setting, the watchers and baseline as `spec ui` has them,
	// the replay's clock and feeder.
	Options Options
}

// OpenReplay restores the recording in cfg.Dir into a temp directory and
// builds the workflow and options that replay it: every provider serves
// the recorded replies (checked against the recorded requests), proposal
// ids come from the recorded seed, and the clock follows the keys.
func OpenReplay(ctx context.Context, cfg ReplayConfig) (*ReplaySession, error) {
	p, err := recording.Prepare(ctx, cfg.Dir, cfg.TempParent)
	if err != nil {
		return nil, err
	}
	meta := p.Rec.Meta
	if cfg.Getenv != nil {
		for _, k := range terminalKeys {
			if v := cfg.Getenv(k); v != "" {
				p.Env[k] = v
			}
		}
	}
	rp := NewReplay(p.Rec)
	rp.Flash = cfg.Flash
	heads := recording.NewHeadChecker(p.Rec.Requests, rp.Warn)
	wf := &workflow.Workflow{
		Getenv: p.Getenv, Git: p.Git, Now: rp.Now,
		WrapProvider: heads.WrapProvider, WrapGate: heads.WrapGate,
		Provider: meta.Provider, Model: meta.Model, Effort: meta.Effort,
	}
	if ids, err := recording.Seeded(meta.IDsSeed); err == nil {
		wf.IDs = ids
	} else {
		rp.Warn(fmt.Sprintf("%v; proposal ids will differ from the recording's", err))
	}
	return &ReplaySession{Prepared: p, Replay: rp, Heads: heads, Workflow: wf, Options: Options{
		Root: p.Root, HashToSection: meta.HashToSection,
		Watch: true, WatchProposals: true, Baseline: true,
		FixedTheme: true, Light: !meta.Dark,
		Workflow: wf, Now: rp.Now, Replay: rp,
	}}, nil
}

// Warnings are everything the replay did not reproduce: the divergences
// it saw, the recorded model calls it never made, the events it never
// fed, and a final frame unlike the recording's.
func (s *ReplaySession) Warnings() []string {
	ws := s.Replay.Warnings()
	ws = append(ws, s.Heads.Unused()...)
	for _, u := range s.Replay.Unfed() {
		ws = append(ws, "not fed (the replay ended first): "+u)
	}
	rec := s.Prepared.Rec
	if final := s.Replay.Final(); final != "" && len(rec.Events) > 0 {
		differs := false
		if rec.Final != "" {
			differs = !recording.SameFrame(ansi.Strip(final), ansi.Strip(rec.Final))
		} else if end := rec.Events[len(rec.Events)-1]; end.Kind == recording.KindEnd && end.FrameSHA != "" {
			sum := sha256.Sum256([]byte(ansi.Strip(final)))
			differs = hex.EncodeToString(sum[:]) != end.FrameSHA
		}
		if differs {
			ws = append(ws, "the final frame differs from the recording's ("+recording.FinalPlain+" vs "+recording.ReplayFinal+")")
		}
	}
	if m := rec.Meta; m.FinalHead != "" && m.FinalHead != m.GitHead {
		ws = append(ws, "the recorded session moved HEAD; commits made in the replay have other hashes")
	}
	return ws
}

// Close removes the restored project unless keep is set.
func (s *ReplaySession) Close(keep bool) error {
	if keep {
		return nil
	}
	return s.Prepared.Cleanup()
}

// PrintWarnings writes the warnings to w, one per line (continuation
// lines indented). They quote the recording, so its terminal controls go.
func PrintWarnings(w io.Writer, prefix string, ws []string) {
	for _, m := range ws {
		fmt.Fprintf(w, "%s%s\n", prefix, indentRest(plainText(m)))
	}
}

// plainText is s without escape sequences or control characters but line breaks
// and tabs.
func plainText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

// indentRest indents every line of s but the first.
func indentRest(s string) string {
	out := []byte{}
	for i := 0; i < len(s); i++ {
		out = append(out, s[i])
		if s[i] == '\n' && i+1 < len(s) {
			out = append(out, "    "...)
		}
	}
	return string(out)
}
