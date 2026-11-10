// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"io"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/sirupsen/logrus"
	"github.com/tenta-browser/polychromatic"

	"github.com/jwriteclub/spec/internal/project"
)

// LogFile is the project-relative log of the UI. The terminal belongs to
// the UI, so nothing may log to stdout or stderr while it runs.
const LogFile = project.LockDir + "/ui.log"

// openLog points polychromatic at LogFile (or discards logs when it cannot
// be opened) and returns a function that closes it.
func openLog(root string) func() {
	dir := filepath.Join(root, project.LockDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		polychromatic.SetOutput(io.Discard)
		return func() {}
	}
	f, err := os.OpenFile(filepath.Join(root, filepath.FromSlash(LogFile)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		polychromatic.SetOutput(io.Discard)
		return func() {}
	}
	polychromatic.SetOutput(f)
	polychromatic.SetLogLevel(logrus.InfoLevel)
	return func() {
		polychromatic.SetLogLevel(logrus.PanicLevel)
		polychromatic.SetOutput(io.Discard)
		_ = f.Close()
	}
}

// Run runs the UI on in and out until the user exits. The caller holds
// the UI lock (project.AcquireUILock).
func Run(a *App, in io.Reader, out io.Writer) error {
	closeLog := openLog(a.ws.Root)
	defer closeLog()
	lg.WithField("root", a.ws.Root).Info("spec ui started")
	if a.opts.Watch {
		a.startWatcher()
		defer a.watcher.Close()
	}
	defer a.Close()
	p := tea.NewProgram(a, tea.WithInput(in), tea.WithOutput(out))
	_, err := p.Run()
	if err != nil {
		lg.WithError(err).Warn("spec ui stopped")
		return err
	}
	lg.Info("spec ui exited")
	return nil
}
