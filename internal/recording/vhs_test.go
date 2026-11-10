// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/golden"
)

// keyEv is the recorded event of a key press.
func keyEv(code rune, mod tea.KeyMod, text string) Event {
	k := tea.Key{Code: code, Mod: mod, Text: text}
	return Event{Kind: KindKey, Key: k.String(), Code: code, Mod: int(mod), Text: text}
}

// vhsCases are key presses and their VHS commands ("" when VHS has none).
var vhsCases = []struct {
	ev   Event
	want string
}{
	{keyEv('a', 0, "a"), `Type "a"`},
	{keyEv('a', tea.ModShift, "A"), `Type "A"`},
	{keyEv('"', 0, `"`), "Type '\"'"},
	{keyEv('§', 0, "§"), `Type "§"`},
	{keyEv(tea.KeySpace, 0, " "), "Space"},
	{keyEv(tea.KeyEnter, 0, ""), "Enter"},
	{keyEv(tea.KeyTab, 0, ""), "Tab"},
	{keyEv(tea.KeyTab, tea.ModShift, ""), "Shift+Tab"},
	{keyEv(tea.KeyBackspace, 0, ""), "Backspace"},
	{keyEv(tea.KeyDelete, 0, ""), "Delete"},
	{keyEv(tea.KeyInsert, 0, ""), "Insert"},
	{keyEv(tea.KeyEscape, 0, ""), "Escape"},
	{keyEv(tea.KeyUp, 0, ""), "Up"},
	{keyEv(tea.KeyDown, 0, ""), "Down"},
	{keyEv(tea.KeyLeft, 0, ""), "Left"},
	{keyEv(tea.KeyRight, 0, ""), "Right"},
	{keyEv(tea.KeyPgUp, 0, ""), "PageUp"},
	{keyEv(tea.KeyPgDown, 0, ""), "PageDown"},
	{keyEv('o', tea.ModCtrl, ""), "Ctrl+O"},
	{keyEv(']', tea.ModCtrl, ""), "Ctrl+]"},
	{keyEv('\\', tea.ModCtrl, ""), `Ctrl+\`},
	{keyEv('^', tea.ModCtrl, ""), "Ctrl+^"},
	{keyEv(tea.KeySpace, tea.ModCtrl, ""), "Ctrl+Space"},
	{keyEv(tea.KeyLeft, tea.ModCtrl, ""), "Ctrl+Left"},
	{keyEv('x', tea.ModCtrl|tea.ModAlt, ""), "Ctrl+Alt+X"},
	{keyEv('x', tea.ModCtrl|tea.ModShift, ""), "Ctrl+Shift+X"},
	{keyEv('x', tea.ModAlt, ""), "Alt+x"},
	{keyEv('x', tea.ModAlt|tea.ModShift, ""), "Alt+X"},
	{keyEv('.', tea.ModAlt, ""), "Alt+."},
	{keyEv(tea.KeyEnter, tea.ModAlt, ""), "Alt+Enter"},
	// VHS has no command for these.
	{keyEv(tea.KeyHome, 0, ""), ""},
	{keyEv(tea.KeyEnd, 0, ""), ""},
	{keyEv(tea.KeyF1, 0, ""), ""},
	{keyEv(tea.KeyUp, tea.ModShift, ""), ""},
	{keyEv(tea.KeyUp, tea.ModAlt, ""), ""},
	{keyEv(',', tea.ModAlt, ""), ""},
	{keyEv('1', tea.ModCtrl, ""), ""},
	{keyEv('/', tea.ModCtrl, ""), ""},
	{keyEv(tea.KeyTab, tea.ModCtrl, ""), ""},
	{keyEv('a', tea.ModSuper, ""), ""},
}

func TestVHSKey(t *testing.T) {
	for _, c := range vhsCases {
		got, ok := VHSKey(c.ev)
		assert.Equal(t, c.want != "", ok, "%s", c.ev.Key)
		assert.Equal(t, c.want, got, "%s", c.ev.Key)
	}
}

// The recorded string stands in when the code is missing.
func TestVHSKeyFromString(t *testing.T) {
	got, ok := VHSKey(Event{Kind: KindKey, Key: "ctrl+r"})
	assert.True(t, ok)
	assert.Equal(t, "Ctrl+R", got)
}

func TestQuoteType(t *testing.T) {
	assert.Equal(t, []string{`"plain"`}, quoteType("plain"))
	assert.Equal(t, []string{`'say "hi"'`}, quoteType(`say "hi"`))
	assert.Equal(t, []string{"`it's \"x\"`"}, quoteType(`it's "x"`))
	got := quoteType("a\"b'c`d")
	assert.Greater(t, len(got), 1)
	var back strings.Builder
	for _, q := range got {
		assert.False(t, strings.ContainsRune(q[1:len(q)-1], rune(q[0])), q)
		back.WriteString(q[1 : len(q)-1])
	}
	assert.Equal(t, "a\"b'c`d", back.String())
	assert.Equal(t, []string{`Type "ab"`, "Enter", `Type "c"`, "Tab", `Type "d"`}, typeLines("ab\r\nc\td"))
}

// sampleRecording is a small hand-built recording.
func sampleRecording() *Recording {
	dark := true
	ev := func(t int64, keys int, e Event) Event { e.T, e.Keys = t, keys; return e }
	return &Recording{
		Dir: "/rec/demo",
		Meta: Meta{Version: FormatVersion, Start: time.Date(2026, 11, 1, 14, 2, 3, 0, time.UTC), Cols: 140, Rows: 40,
			Dark: true},
		Events: []Event{
			ev(10, 0, Event{Kind: KindResize, Cols: 140, Rows: 40}),
			ev(12, 0, Event{Kind: KindTheme, Dark: &dark}),
			ev(1840, 1, keyEv('o', tea.ModCtrl, "")),
			ev(2252, 2, keyEv('x', 0, "x")),
			ev(2400, 3, keyEv(tea.KeyHome, 0, "")),
			ev(2600, 3, Event{Kind: KindMouse, Mouse: &Mouse{Action: MouseHover, X: 50, Y: 4, Screen: "main", Pane: "editor",
				Target: &MouseTarget{Kind: "anchor", Name: "Time.Media", Pos: &Pos{File: "demo.spec.md", Line: 5, Col: 3}}}}),
			ev(3000, 3, Event{Kind: KindFiles, Source: SourceWatcher, Changes: []FileChange{{Path: "other.spec.md", Content: "x"}}}),
			ev(3500, 14, Event{Kind: KindPaste, Text: "say \"hi\"\nok"}),
			ev(3600, 14, Event{Kind: KindResize, Cols: 120, Rows: 40}),
			ev(4100, 15, keyEv(tea.KeyEnter, 0, "")),
			ev(5000, 15, Event{Kind: KindEnd, FrameSHA: "abc"}),
		},
	}
}

func TestTapeGolden(t *testing.T) {
	tape, warns := Tape(sampleRecording(), TapeOptions{Spec: "/usr/local/bin/spec"})
	golden.Check(t, filepath.Join("testdata", "sample.tape"), []byte(tape))
	assert.Equal(t, []string{
		`key "home" (#3) has no VHS command and is left out; events after it may land one key late`,
		"resize to 120x40 after key #14 cannot be replayed",
	}, warns)
	assert.Contains(t, tape, "Set Width 3840\nSet Height 2160\nSet Framerate 30\n")
	assert.Contains(t, tape, "Set FontSize 34\n", "140x40 in 4K: the rows limit it (40*1.45*35+180 > 2160)")
	assert.Contains(t, tape, "# feeder: hover editor $Time.Media (after key #3, +200ms)\n")
}

// ParseTape gives back the keys, the text and the recorded gaps.
func TestParseTape(t *testing.T) {
	tape, _ := Tape(sampleRecording(), TapeOptions{Spec: "spec", Width: 1000, Height: 600})
	assert.Contains(t, tape, "Set Width 1000\n")
	steps, err := ParseTape(tape)
	require.NoError(t, err)
	ms := func(n int) TapeStep { return TapeStep{Sleep: time.Duration(n) * time.Millisecond} }
	assert.Equal(t, []TapeStep{
		ms(1830), {Key: "ctrl+o"},
		ms(412), {Text: "x"},
		ms(148), // home: left out, its gap kept
		ms(1100), {Text: `say "hi"`}, {Key: "enter"}, {Text: "ok"},
		ms(600), {Key: "enter"},
		ms(900), ms(1000),
	}, steps)
}

// Every mapped key form and a generated tape pass `vhs validate` (which
// only parses; skipped without vhs).
func TestTapeVHSValidate(t *testing.T) {
	vhs, err := exec.LookPath("vhs")
	if err != nil {
		t.Skip("vhs is not installed")
	}
	dir := t.TempDir()
	var keys strings.Builder
	for _, c := range vhsCases {
		if c.want != "" {
			keys.WriteString(c.want + "\n")
		}
	}
	tape, _ := Tape(sampleRecording(), TapeOptions{Spec: "/usr/local/bin/spec"})
	for name, text := range map[string]string{"keys.tape": keys.String(), "sample.tape": tape} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(text), 0o644))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, vhs, "validate", path).CombinedOutput()
		cancel()
		assert.NoError(t, err, "%s:\n%s", name, out)
	}
}

// Terminals send a pasted newline as "\r" (bracketed paste); the editor
// pastes it as a newline, so the tape types Enter for it and counts it.
func TestTypeLinesLoneCR(t *testing.T) {
	assert.Equal(t, []string{`Type "a"`, "Enter", `Type "b"`, "Enter", `Type "c"`}, typeLines("a\rb\r\nc"))
	assert.Equal(t, 5, PasteKeys("a\rb\r\nc"))
	assert.Equal(t, 4, PasteKeys("ab\r\nc"))
}

// TestTapeSizeEven: the video encoder refuses odd sizes, so the tape's
// Width and Height are even, by default or given.
func TestTapeSizeEven(t *testing.T) {
	rec := &Recording{Meta: Meta{Cols: 140, Rows: 40}}
	tape, warns := Tape(rec, TapeOptions{})
	assert.Contains(t, tape, "Set Width 3840\n")
	assert.Contains(t, tape, "Set Height 2160\n")
	assert.Empty(t, warns)
	tape, _ = Tape(rec, TapeOptions{Width: 1001, Height: 701})
	assert.Contains(t, tape, "Set Width 1002\n")
	assert.Contains(t, tape, "Set Height 702\n")
}

// The font is the largest at which the recorded grid surely fits, by an
// estimate never smaller than VHS's measured grid (cell about 0.6 x 1.42
// of the font size at LineHeight 1.2, plus up to 150 px).
func TestFitFontSize(t *testing.T) {
	cases := []struct{ cols, rows, w, h, want int }{
		{168, 40, 3840, 2160, 34}, // near 16:9 in pixels: both axes almost full
		{140, 40, 3840, 2160, 34},
		{100, 30, 3840, 2160, 45},
		{240, 40, 3840, 2160, 24}, // width-bound
		{100, 30, 1920, 1080, 20},
		{1000, 400, 1920, 1080, minFontSize},
	}
	for _, c := range cases {
		fs := FitFontSize(c.cols, c.rows, c.w, c.h, DefaultPadding)
		assert.Equal(t, c.want, fs, "%dx%d in %dx%d", c.cols, c.rows, c.w, c.h)
		if fs == minFontSize {
			continue
		}
		assert.True(t, GridFits(c.cols, c.rows, c.w, c.h, fs, DefaultPadding))
		assert.False(t, GridFits(c.cols, c.rows, c.w, c.h, fs+1, DefaultPadding))
		// The measured grid at the upper end fits too.
		measuredW := float64(c.cols)*float64(fs)*0.6 + 150 + 2*DefaultPadding
		measuredH := float64(c.rows)*float64(fs)*1.42 + 150 + 2*DefaultPadding
		assert.LessOrEqual(t, measuredW, float64(c.w))
		assert.LessOrEqual(t, measuredH, float64(c.h))
	}
	assert.Equal(t, maxFontSize, FitFontSize(0, 0, 3840, 2160, 0), "no recorded size: bounded")
}

// --font-size, --framerate and the size are the user's; a font at which
// the grid may not fit is warned.
func TestTapeOptionsOverride(t *testing.T) {
	rec := &Recording{Meta: Meta{Cols: 168, Rows: 40}}
	tape, warns := Tape(rec, TapeOptions{FontSize: 15, Framerate: 60, Width: 1920, Height: 1080})
	assert.Contains(t, tape, "Set FontSize 15\nSet LineHeight 1.2\nSet Padding 10\nSet Width 1920\nSet Height 1080\nSet Framerate 60\n")
	assert.Empty(t, warns)
	tape, warns = Tape(rec, TapeOptions{FontSize: 40})
	assert.Contains(t, tape, "Set FontSize 40\n")
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0], "168x40")
}
