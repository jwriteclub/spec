// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ^F formats the buffer with spec.toml's [format] width as one undo step,
// keeping the cursor on its word; a second ^F says it is formatted.
func TestFormatKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "spec.toml", "name = \"f\"\n\n[format]\nwidth = 30\n")
	src := "# F\n\n\n\nOne two three four five six seven eight nine ten.   \n"
	writeFile(t, dir, "f.spec.md", src)
	a := newTestApp(t, testOptions(dir))
	ed := a.cur.Ed
	at, _ := ed.Find("nine", 0)
	ed.SetCursor(at.Start)

	send(a, key("ctrl+f"))
	want := "# F\n\nOne two three four five six\nseven eight nine ten.\n"
	assert.Equal(t, want, string(ed.Buffer().Bytes()))
	assert.Equal(t, "Formatted: 3 lines changed", a.flash)
	assert.Equal(t, "nine ten.\n", string(ed.Buffer().Bytes()[ed.Cursor():]), "the cursor stays on its word")
	assert.True(t, ed.Buffer().Dirty())

	send(a, key("ctrl+f"))
	assert.Equal(t, "Already formatted", a.flash)

	send(a, key("ctrl+z"))
	assert.Equal(t, src, string(ed.Buffer().Bytes()), "one undo step")

	// Jump forward kept its alt keys.
	b, ok := Lookup(CtxEditor, "alt+right")
	require.True(t, ok)
	assert.Equal(t, ActForward, b.Action)
}

// A refusal changes nothing and says why.
func TestFormatKeyRefuses(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "spec.toml", "name = \"f\"\n")
	writeFile(t, dir, "f.spec.md", "# F\r\n\r\nText.  \r\n")
	a := newTestApp(t, testOptions(dir))
	send(a, key("ctrl+f"))
	assert.Equal(t, "Not formatted: it has CR line endings; convert them to LF first", a.flash)
	assert.False(t, a.cur.Ed.Buffer().Dirty())
}
