// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/recording"
	"github.com/jwriteclub/spec/internal/workflow"
)

// RecordConfig says what to record (spec ui --record).
type RecordConfig struct {
	// Dir is the recording directory: created, and refused when it holds
	// anything.
	Dir string
	// Root is the project.
	Root string
	// Workflow is the UI's: OpenRecording sets its WrapProvider, WrapGate
	// and IDs.
	Workflow *workflow.Workflow
	// Git runs git for the snapshot and HEAD.
	Git baseline.Loader
	// Target is the command line's place to open, for meta.json.
	Target        string
	HashToSection bool
}

// RecordSession is a recording in progress: pass Recorder as
// Options.Record, run the UI, then Close.
type RecordSession struct {
	Dir      string
	Recorder *Recorder
	Models   *recording.ModelRecorder
	Snapshot recording.SnapshotInfo
	w        *recording.Writer
	meta     recording.Meta
	cfg      RecordConfig
}

// OpenRecording starts recording into cfg.Dir: it writes the snapshot of
// the project, the canned copy of the user config and meta.json, and
// hooks the workflow's model calls and proposal ids. Call it before
// opening the UI (New), with the UI lock held.
func OpenRecording(ctx context.Context, cfg RecordConfig) (*RecordSession, error) {
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if ents, err := os.ReadDir(dir); err != nil {
		return nil, err
	} else if len(ents) > 0 {
		return nil, fmt.Errorf("%s is not empty; record into a new directory", dir)
	}
	start := time.Now()
	snap, err := recording.Snapshot(ctx, cfg.Git, cfg.Root, dir)
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	wf := cfg.Workflow
	if path := wf.UserConfigPath(); path != "" {
		loaded, err := config.Load(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		b, err := recording.CannedConfig(loaded)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, recording.ConfigFile), b, 0o644); err != nil {
			return nil, err
		}
	}
	seed, err := recording.NewSeed()
	if err != nil {
		return nil, err
	}
	ids, err := recording.Seeded(seed)
	if err != nil {
		return nil, err
	}
	models, err := recording.NewModelRecorder(dir, start)
	if err != nil {
		return nil, err
	}
	w, err := recording.NewWriter(dir)
	if err != nil {
		_ = models.Close()
		return nil, err
	}
	rec := NewRecorder(w, start)
	models.SetKeys(rec.Keys)
	wf.IDs = ids
	wf.WrapProvider = models.WrapProvider
	wf.WrapGate = models.WrapGate
	s := &RecordSession{Dir: dir, Recorder: rec, Models: models, Snapshot: snap, w: w, cfg: cfg,
		meta: recording.Meta{
			Version: recording.FormatVersion, Start: start.Round(0), Dark: true,
			Target: cfg.Target, HashToSection: cfg.HashToSection,
			Provider: wf.Provider, Model: wf.Model, Effort: wf.Effort,
			GitHead: snap.Head, IDsSeed: seed, Build: buildRevision(),
		}}
	if err := recording.WriteMeta(dir, s.meta); err != nil {
		_ = s.closeFiles()
		return nil, err
	}
	return s, nil
}

// closeFiles closes the recorder, the event file and the model recorder.
func (s *RecordSession) closeFiles() error {
	s.Recorder.Close()
	return errors.Join(s.w.Close(), s.Models.Close())
}

// Close ends the recording (after the UI exited): it records the end if
// the UI did not, closes the files and rewrites meta.json with the size,
// the theme and the counts.
func (s *RecordSession) Close() (recording.Meta, error) {
	err := s.closeFiles()
	m := s.meta
	m.Cols, m.Rows = s.Recorder.Size()
	if d := s.Recorder.Dark(); d != nil {
		m.Dark = *d
	}
	m.End = time.Now().Round(0)
	m.Events = s.w.Count()
	m.ModelCalls = s.Models.Calls()
	if s.Snapshot.Git {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if out, gerr := s.cfg.Git.Run(ctx, s.cfg.Root, nil, "rev-parse", "--verify", "-q", "HEAD^{commit}"); gerr == nil {
			m.FinalHead = strings.TrimSpace(string(out))
		}
		cancel()
	}
	s.meta = m
	return m, errors.Join(err, recording.WriteMeta(s.Dir, m))
}

// buildRevision is the vcs revision the binary was built from ("" when
// unknown).
func buildRevision() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	rev, dirty := "", false
	for _, st := range bi.Settings {
		switch st.Key {
		case "vcs.revision":
			rev = st.Value
		case "vcs.modified":
			dirty = st.Value == "true"
		}
	}
	if rev != "" && dirty {
		rev += "-dirty"
	}
	return rev
}
