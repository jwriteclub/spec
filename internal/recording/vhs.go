// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// TapeOptions shape the VHS tape of a recording (`spec replay`).
type TapeOptions struct {
	// Spec is the spec binary the tape runs ("spec" when empty).
	Spec string
	// Video is the tape's Output file (DIR/replay.mp4 when empty).
	Video string
	// Width and Height are the video's pixels (3840 x 2160 when 0),
	// rounded up to even.
	Width  int
	Height int
	// FontSize is the VHS font size; 0 picks the largest that fits the
	// recorded terminal in the video (FitFontSize).
	FontSize int
	// Padding is VHS's padding in pixels (DefaultPadding when 0, none
	// when negative).
	Padding int
	// Framerate is the video's frames per second (DefaultFramerate when
	// 0).
	Framerate int
}

// Tape defaults. The video is 4K; the font is as large as fits the
// recorded terminal, and the replay centres the recorded layout in
// whatever grid VHS ends up with (spec ui --replay lays out at the
// recorded size in a larger terminal), so the estimate below only has to
// never come out too small.
const (
	DefaultWidth   = 3840
	DefaultHeight  = 2160
	DefaultPadding = 10
	// DefaultFramerate: VHS cannot capture 4K at 60 fps (it writes a
	// 25 fps file with the timing squeezed); 30 keeps the timing exact.
	DefaultFramerate = 30
	// TapeLineHeight is the tape's LineHeight setting; the font size
	// estimate assumes it.
	TapeLineHeight = 1.2
	// tapeCellWidth, tapeCellHeight and tapeOverhead are a deliberately
	// large estimate of VHS's xterm.js grid at LineHeight 1.2: a cell per
	// point of font size, and the pixels lost on each axis besides the
	// padding. Measured: about 0.6 wide, 1.38-1.42 high, 80-150 px lost.
	tapeCellWidth  = 0.62
	tapeCellHeight = 1.45
	tapeOverhead   = 160
	// minFontSize and maxFontSize bound the font size FitFontSize picks.
	minFontSize = 6
	maxFontSize = 200
	// ReadyPattern is what the tape waits for before it shows the UI: the
	// help bar's ^G entry, which the first frame always has.
	ReadyPattern = `\^G`
	// tapeTail is how long the tape waits (hidden) after the recording's
	// end, for the replay to exit.
	tapeTail = time.Second
	// quitGap: a recording that ends within this of its last key ended
	// on that key (^X).
	quitGap = 100
)

// even rounds n up to an even number.
func even(n int) int { return n + n%2 }

// GridFits says whether a terminal of cols x rows surely fits a width x
// height video at fontSize with padding on every side (by the
// conservative cell estimate).
func GridFits(cols, rows, width, height, fontSize, padding int) bool {
	fs := float64(fontSize)
	w := math.Ceil(float64(cols)*fs*tapeCellWidth) + tapeOverhead + float64(2*padding)
	h := math.Ceil(float64(rows)*fs*tapeCellHeight) + tapeOverhead + float64(2*padding)
	return w <= float64(width) && h <= float64(height)
}

// FitFontSize is the largest font size at which a terminal of cols x rows
// surely fits a width x height video (GridFits), from minFontSize to
// maxFontSize.
func FitFontSize(cols, rows, width, height, padding int) int {
	fs := minFontSize
	for fs < maxFontSize && GridFits(cols, rows, width, height, fs+1, padding) {
		fs++
	}
	return fs
}

// Tape writes the VHS tape that replays rec: it sets the video's size
// (4K unless o says otherwise), the largest font at which the recorded
// terminal surely fits, and the framerate, starts
// `spec ui --replay DIR` hidden, waits for the first frame, then types the
// recorded keys and pastes with the recorded gaps between them, verbatim.
// Mouse events and other processes' file changes are not in the tape (the
// replaying UI injects them); comments mark where they happen. warnings
// lists what the tape cannot reproduce: keys VHS has no command for (a
// comment stands in their place), resizes after the first, and a recorded
// terminal that may not fit the video at the font size given. The tape
// hides the terminal again when the session ends, so the replay's report stays
// out of the video.
//
// The keys are the recording's and it is untrusted, so VHS must have no shell to
// type them into: the launch is `exec`, and when the UI quits the terminal is
// gone. A comment holds nothing that would end its line, since VHS would obey the
// next one.
func Tape(rec *Recording, o TapeOptions) (tape string, warnings []string) {
	if o.Spec == "" {
		o.Spec = "spec"
	}
	if o.Video == "" {
		o.Video = filepath.Join(rec.Dir, "replay.mp4")
	}
	if o.Padding < 0 {
		o.Padding = 0
	} else if o.Padding == 0 {
		o.Padding = DefaultPadding
	}
	if o.Framerate <= 0 {
		o.Framerate = DefaultFramerate
	}
	w, h := DefaultWidth, DefaultHeight
	if o.Width > 0 {
		w = o.Width
	}
	if o.Height > 0 {
		h = o.Height
	}
	// The video encoder (libx264) refuses odd sizes, and VHS then writes
	// an empty video yet exits 0.
	w, h = even(w), even(h)
	cols, rows := rec.Meta.Cols, rec.Meta.Rows
	if o.FontSize <= 0 {
		o.FontSize = FitFontSize(cols, rows, w, h, o.Padding)
	}
	if cols > 0 && rows > 0 && !GridFits(cols, rows, w, h, o.FontSize, o.Padding) {
		warnings = append(warnings, fmt.Sprintf("the recorded %dx%d terminal may not fit %dx%d pixels at font size %d; "+
			"a smaller terminal replays at its own size, unlike the recording", cols, rows, w, h, o.FontSize))
	}
	var b strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format, args...)
		b.WriteByte('\n')
	}
	line("# spec replay of %s (recorded %s, %dx%d); timings verbatim", commentText(rec.Dir),
		rec.Meta.Start.UTC().Format(time.RFC3339), rec.Meta.Cols, rec.Meta.Rows)
	line("Output %s", quoteVHS(o.Video))
	line("Set Shell %s", quoteVHS("bash"))
	line("Set FontSize %d", o.FontSize)
	line("Set LineHeight %s", strconv.FormatFloat(TapeLineHeight, 'f', -1, 64))
	line("Set Padding %d", o.Padding)
	line("Set Width %d", w)
	line("Set Height %d", h)
	line("Set Framerate %d", o.Framerate)
	line("Set TypingSpeed 0ms")
	line("Hide")
	for _, l := range typeLines("exec " + shellQuote(o.Spec) + " ui --replay " + shellQuote(rec.Dir) + " || exit") {
		line("%s", l)
	}
	line("Enter")
	line("Wait+Screen@30s /%s/", ReadyPattern)
	line("Show")

	// The first key's gap runs from the first frame (the first resize).
	var prevT int64
	for _, ev := range rec.Events {
		if ev.Kind == KindResize {
			prevT = ev.T
			break
		}
	}
	sized := false
	cols, rows = 0, 0
	sleep := func(t int64) {
		if gap := t - prevT; gap > 0 {
			line("Sleep %dms", gap)
		}
		if t > prevT {
			prevT = t
		}
	}
	keyT := int64(0)
	end := int64(-1)
	for _, ev := range rec.Events {
		switch ev.Kind {
		case KindKey:
			sleep(ev.T)
			keyT = ev.T
			if cmd, ok := VHSKey(ev); ok {
				line("%s", cmd)
			} else {
				line("# unsupported key %q (#%d)", ev.Key, ev.Keys)
				warnings = append(warnings, fmt.Sprintf("key %q (#%d) has no VHS command and is left out; events after it may land one key late", ev.Key, ev.Keys))
			}
		case KindPaste:
			sleep(ev.T)
			keyT = ev.T
			for _, l := range typeLines(ev.Text) {
				line("%s", l)
			}
		case KindFiles, KindMouse:
			line("# feeder: %s (after key #%d, +%dms)", commentText(Describe(ev)), ev.Keys, max(ev.T-keyT, 0))
		case KindResize:
			if !sized || (ev.Cols == cols && ev.Rows == rows) {
				sized, cols, rows = true, ev.Cols, ev.Rows
				continue
			}
			cols, rows = ev.Cols, ev.Rows
			warnings = append(warnings, fmt.Sprintf("resize to %dx%d after key #%d cannot be replayed", ev.Cols, ev.Rows, ev.Keys))
			line("# resize to %dx%d (not replayed)", ev.Cols, ev.Rows)
		case KindEnd:
			end = ev.T
		}
	}
	// The UI quits at the end and the terminal goes back to the shell,
	// where the replay prints what it did not reproduce: keep that out of
	// the video, and right away when the last key is what quit.
	if end-prevT >= quitGap {
		line("Sleep %dms", end-prevT)
	}
	line("Hide")
	if end-prevT > 0 && end-prevT < quitGap {
		line("Sleep %dms", end-prevT)
	}
	line("Sleep %dms", tapeTail.Milliseconds())
	return b.String(), warnings
}

// commentText is s with whatever could end a tape's comment escaped.
func commentText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			fmt.Fprintf(&b, "\\u%04x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Describe says in a few words what a feeder event does.
func Describe(ev Event) string {
	switch ev.Kind {
	case KindFiles:
		var ps []string
		for _, c := range ev.Changes {
			p := c.Path
			if c.Deleted {
				p += " (deleted)"
			}
			ps = append(ps, p)
		}
		return ev.Source + " " + strings.Join(ps, ", ")
	case KindMouse:
		if ev.Mouse == nil {
			return "mouse"
		}
		s := ev.Action
		if ev.Pane != "" {
			s += " " + ev.Pane
		}
		t := ev.Target
		switch {
		case t == nil:
		case ev.Action == MouseHover && t.Kind == "anchor":
			s += " $" + t.Name
		case ev.Action == MouseHover && t.Kind == "section":
			s += " §" + t.Number
		case ev.Action == MouseHover && t.Kind == "gutter":
			s += fmt.Sprintf(" line %d %s", t.Line, t.Code)
		case ev.Action == MouseHover:
			s += " " + t.Code
		case t.Pos != nil:
			s += fmt.Sprintf(" %s:%d:%d", t.Pos.File, t.Pos.Line, t.Pos.Col)
		case t.Label != "":
			s += fmt.Sprintf(" %q", t.Label)
		case t.Proposal != "":
			s += " " + t.Proposal
		case t.Direction != "":
			s += " " + t.Direction
		}
		return s
	}
	return ev.Kind
}

// vhsNamed maps tea key names to VHS commands.
var vhsNamed = map[string]string{
	"enter": "Enter", "tab": "Tab", "backspace": "Backspace", "delete": "Delete", "insert": "Insert",
	"esc": "Escape", "up": "Up", "down": "Down", "left": "Left", "right": "Right",
	"pgup": "PageUp", "pgdown": "PageDown", "space": "Space",
}

// VHS's parser takes these after Ctrl+ (besides letters), and these after
// Alt+ (`vhs validate`, VHS 0.11); digits and most punctuation it rejects.
const (
	vhsCtrlPunct = "-.@[\\]^"
	vhsAltPunct  = ".[]"
)

// vhsCtrlNamed are the named keys VHS takes after Ctrl+ (and Ctrl+Alt+,
// Ctrl+Shift+).
var vhsCtrlNamed = map[string]bool{"enter": true, "space": true, "backspace": true, "up": true, "down": true, "left": true, "right": true}

// Keystroke is the tea keystroke ("ctrl+o", "shift+tab", "a") of a key
// event, from its code and modifiers (the recorded string when the code is
// missing).
func Keystroke(ev Event) string {
	if ev.Code == 0 {
		return ev.Key
	}
	return tea.Key{Code: ev.Code, Mod: tea.KeyMod(ev.Mod)}.Keystroke()
}

// VHSKey is the VHS command that presses the key of ev, false when VHS has
// none (Home, End, function keys, Shift+arrows, most Alt+punctuation).
func VHSKey(ev Event) (string, bool) {
	mod := tea.KeyMod(ev.Mod)
	if ev.Code == 0 {
		mod = strokeMods(ev.Key)
	}
	ctrl, alt, shift := mod.Contains(tea.ModCtrl), mod.Contains(tea.ModAlt), mod.Contains(tea.ModShift)
	if mod&^(tea.ModCtrl|tea.ModAlt|tea.ModShift|tea.ModCapsLock|tea.ModNumLock|tea.ModScrollLock) != 0 {
		return "", false // meta, hyper, super
	}
	stroke := Keystroke(ev)
	base := stroke
	if i := strings.LastIndex(stroke, "+"); i >= 0 && i < len(stroke)-1 {
		base = stroke[i+1:]
	} else if strings.HasSuffix(stroke, "++") {
		base = "+"
	}
	if !ctrl && !alt {
		if name, ok := vhsNamed[base]; ok {
			if shift {
				if base == "tab" {
					return "Shift+Tab", true
				}
				return "", false
			}
			return name, true
		}
		text := ev.Text
		if text == "" {
			if r := []rune(base); len(r) == 1 && unicode.IsPrint(r[0]) {
				text = base
				if shift {
					text = strings.ToUpper(text)
				}
			}
		}
		if text == "" {
			return "", false
		}
		ls := typeLines(text)
		if len(ls) != 1 {
			return "", false
		}
		return ls[0], true
	}
	r := []rune(base)
	letter := len(r) == 1 && r[0] < unicode.MaxASCII && unicode.IsLetter(r[0])
	if ctrl {
		var key string
		switch {
		case letter:
			key = strings.ToUpper(base)
		case len(r) == 1 && strings.ContainsRune(vhsCtrlPunct, r[0]) && !shift:
			key = base
		case vhsCtrlNamed[base]:
			key = vhsNamed[base]
		default:
			return "", false
		}
		cmd := "Ctrl+"
		if alt {
			cmd += "Alt+"
		}
		if shift {
			cmd += "Shift+"
		}
		return cmd + key, true
	}
	// alt without ctrl
	switch {
	case letter && shift:
		return "Alt+" + strings.ToUpper(base), true
	case letter:
		return "Alt+" + base, true
	case shift:
		return "", false
	case len(r) == 1 && strings.ContainsRune(vhsAltPunct, r[0]):
		return "Alt+" + base, true
	case base == "enter" || base == "tab":
		return "Alt+" + vhsNamed[base], true
	}
	return "", false
}

// strokeMods are the modifiers a keystroke string names.
func strokeMods(stroke string) tea.KeyMod {
	var mod tea.KeyMod
	parts := strings.Split(stroke, "+")
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl":
			mod |= tea.ModCtrl
		case "alt":
			mod |= tea.ModAlt
		case "shift":
			mod |= tea.ModShift
		case "meta":
			mod |= tea.ModMeta
		case "hyper":
			mod |= tea.ModHyper
		case "super":
			mod |= tea.ModSuper
		}
	}
	return mod
}

// typedText is text as typing reproduces it: a line break ("\r\n", or
// "\r" alone, as terminals send a pasted newline) is "\n", as the editor
// pastes it.
func typedText(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

// PasteKeys is the number of key presses typing text takes: one per rune,
// a line break ("\r\n", "\r" or "\n") one Enter.
func PasteKeys(text string) int {
	return utf8.RuneCountInString(typedText(text))
}

// typeLines are the VHS lines that type text: Type commands, with Enter for
// a line break and Tab for "\t".
func typeLines(text string) []string {
	text = typedText(text)
	var out []string
	var run strings.Builder
	flush := func() {
		if run.Len() > 0 {
			for _, q := range quoteType(run.String()) {
				out = append(out, "Type "+q)
			}
			run.Reset()
		}
	}
	for _, c := range text {
		switch c {
		case '\n':
			flush()
			out = append(out, "Enter")
		case '\t':
			flush()
			out = append(out, "Tab")
		default:
			run.WriteRune(c)
		}
	}
	flush()
	return out
}

// vhsQuotes are the delimiters a VHS string may use; it has no escapes.
const vhsQuotes = "\"'`"

// quoteType quotes s for VHS Type: with a delimiter s does not hold, or,
// when it holds all three, in several pieces.
func quoteType(s string) []string {
	var out []string
	var cur strings.Builder
	usable := vhsQuotes
	for _, c := range s {
		next := strings.Map(func(q rune) rune {
			if q == c {
				return -1
			}
			return q
		}, usable)
		if next == "" {
			out = append(out, wrap(cur.String(), usable))
			cur.Reset()
			next = strings.Map(func(q rune) rune {
				if q == c {
					return -1
				}
				return q
			}, vhsQuotes)
		}
		usable = next
		cur.WriteRune(c)
	}
	if cur.Len() > 0 {
		out = append(out, wrap(cur.String(), usable))
	}
	return out
}

// wrap delimits s with the first of usable.
func wrap(s, usable string) string {
	q := usable[:1]
	return q + s + q
}

// quoteVHS quotes a setting's string.
func quoteVHS(s string) string {
	ls := quoteType(s)
	if len(ls) == 1 {
		return ls[0]
	}
	return strconv.Quote(s)
}

// shellQuote quotes s for bash.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TapeStep is one action of a tape after its Show: a pause, a key press
// (a tea keystroke such as "ctrl+o") or text typed.
type TapeStep struct {
	Sleep time.Duration
	Key   string
	Text  string
}

// ParseTape reads back the steps of a tape Tape wrote, from its first
// Show on (the setup before it is skipped): Sleep, Type and key commands;
// comments are skipped.
func ParseTape(tape string) ([]TapeStep, error) {
	var steps []TapeStep
	shown := false
	for n, raw := range strings.Split(tape, "\n") {
		l := strings.TrimSpace(raw)
		if !shown {
			shown = l == "Show"
			continue
		}
		if l == "" || l == "Hide" || l == "Show" || strings.HasPrefix(l, "#") {
			continue
		}
		cmd, arg, _ := strings.Cut(l, " ")
		switch cmd {
		case "Sleep":
			d, err := time.ParseDuration(arg)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", n+1, err)
			}
			steps = append(steps, TapeStep{Sleep: d})
		case "Type":
			if len(arg) < 2 || arg[0] != arg[len(arg)-1] || !strings.ContainsRune(vhsQuotes, rune(arg[0])) {
				return nil, fmt.Errorf("line %d: bad Type %q", n+1, arg)
			}
			steps = append(steps, TapeStep{Text: arg[1 : len(arg)-1]})
		default:
			k, err := teaKey(cmd)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", n+1, err)
			}
			steps = append(steps, TapeStep{Key: k})
		}
	}
	return steps, nil
}

// teaKey maps a VHS key command back to a tea keystroke.
func teaKey(cmd string) (string, error) {
	parts := strings.Split(cmd, "+")
	if strings.HasSuffix(cmd, "++") {
		parts = append(parts[:len(parts)-2], "+")
	}
	var mods []string
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "Ctrl", "Alt", "Shift":
			mods = append(mods, strings.ToLower(p))
		default:
			return "", fmt.Errorf("unknown modifier %q in %q", p, cmd)
		}
	}
	last := parts[len(parts)-1]
	for name, v := range vhsNamed {
		if v == last {
			return strings.Join(append(mods, name), "+"), nil
		}
	}
	r := []rune(last)
	if len(r) != 1 {
		return "", fmt.Errorf("unknown key command %q", cmd)
	}
	if unicode.IsUpper(r[0]) {
		if len(mods) > 0 && mods[0] == "alt" {
			mods = append(mods, "shift")
		}
		last = strings.ToLower(last)
	}
	return strings.Join(append(mods, last), "+"), nil
}
