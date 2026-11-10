// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/jwriteclub/spec/internal/format"
	"github.com/jwriteclub/spec/internal/workflow"
)

// formatBuffer formats the current buffer the way `spec fmt` formats a
// file, as one undo step, and flashes what it did.
func (a *App) formatBuffer() tea.Cmd {
	ed := a.cur.Ed
	src := string(ed.Buffer().Bytes())
	res, err := format.Source(ed.Path(), []byte(src), workflow.FormatOptions(a.ws.Project))
	var refused *format.RefusedError
	switch {
	case errors.As(err, &refused):
		a.setFlash("Not formatted: %s", refused.Reason)
		return nil
	case err != nil:
		a.setFlash("Not formatted: %v", err)
		return nil
	case res.Changed == 0:
		a.setFlash("Already formatted")
		return nil
	}
	out := string(res.Text)
	ed.ReplaceAll(out, mapOffset(src, out, ed.Cursor()))
	a.setFlash("Formatted: %s changed", plural(res.Changed, "line"))
	return a.edited()
}

// mapOffset maps offset off of before to the same place in after: past
// the edits before it, and to the start of an edit it falls inside.
func mapOffset(before, after string, off int) int {
	delta := 0
	for _, e := range udiff.Strings(before, after) {
		if e.Start >= off {
			break
		}
		if e.End > off {
			return e.Start + delta
		}
		delta += len(e.New) - (e.End - e.Start)
	}
	return min(max(off+delta, 0), len(after))
}
