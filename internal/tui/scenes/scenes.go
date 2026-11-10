// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package scenes is the registry of `spec ui` scenes: each is a fixture
// (a spec project committed to a fresh git repository, with edits and
// agents' proposals on top), a terminal size and theme, the keys that
// bring the UI to a state, the canned model replies those keys need, and
// the frames to capture. tools/scenes renders them to PNG for design
// review; scenario tests and VHS tapes are to drive the same table.
//
// A scene is data: steps are keys, typed text and waits, and model
// replies are hand-written canned entries (infer.Canned), served in order
// within each purpose. Runs are deterministic: the clock is frozen,
// proposal ids come from a source seeded by the scene's name, and every
// reply is canned.
package scenes

import (
	"fmt"
	"strings"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/tui"
)

// Fixture names the spec project a scene starts from.
type Fixture string

// Fixtures.
const (
	// Demo is the two-file demo project of the UI tests: demo.spec.md
	// ($Time.Media, $Frame.Timing, $Frame.Order) and other.spec.md
	// ($Extra.Limit).
	Demo Fixture = "demo"
	// Lensd is testdata/lensd/lensd.spec.md as it is: long lines, one
	// def-unreferenced warning ($Time.Wall, line 40).
	Lensd Fixture = "lensd"
	// LensdFormatted is lensd formatted as `spec fmt` (and ^F) would.
	LensdFormatted Fixture = "lensd-formatted"
	// Formal is a spec of the project lock (lock.spec.md) and a TLA+ model of
	// it (tla/Lock.tla), linked by $Name and § tags.
	Formal Fixture = "formal"
)

// Size is a terminal size in cells.
type Size struct{ W, H int }

func (s Size) String() string { return fmt.Sprintf("%dx%d", s.W, s.H) }

// The standard sizes: a sidebar column beside the text, and a wide
// screen.
var (
	Size140 = Size{140, 40}
	Size220 = Size{220, 50}
	Size100 = Size{100, 30}
	Size80  = Size{80, 24}
)

// DefaultSizes are the sizes of a scene that names none.
var DefaultSizes = []Size{Size140, Size220}

// Theme is the terminal background a frame is drawn for.
type Theme string

// Themes.
const (
	Dark  Theme = "dark"
	Light Theme = "light"
)

// DefaultThemes are the themes of a scene that names none.
var DefaultThemes = []Theme{Dark, Light}

// Edit is a change made to the working tree before the UI opens: the
// first Find in File becomes Replace (a missing Find fails the run).
type Edit struct {
	File, Find, Replace string
}

// AgentChange is a proposal an agent files before the UI opens, as `spec
// propose --by Author` would: the edits are made, the check runs (on the
// scene's canned replies) and files the proposal, and the working tree is
// restored.
type AgentChange struct {
	Author string
	Why    string
	Edits  []Edit
}

// Step is one step of a scene: Find moves the cursor (^W text Enter),
// then Keys are pressed and Text typed, then Wait is waited for, then the
// frame is captured when Capture names it.
type Step struct {
	// Note says what the step does, for the gallery.
	Note string
	// Find moves the editor's cursor to the first match of this text
	// after it (^W, the text, Enter).
	Find string
	// Keys are Bubble Tea key strings ("ctrl+p", "shift+tab", "y").
	Keys []string
	// Text is typed after the keys ("\n" is Enter).
	Text string
	// HoverCounter moves the pointer over status counter 1, 2, or 3. Zero leaves it alone.
	HoverCounter int
	// Hover rests the pointer on the first occurrence of this text in the
	// editor pane and waits for the popup.
	Hover string
	// Wait is waited for after the keys and text.
	Wait tui.Cond
	// Capture names an intermediate capture taken after this step; the
	// file is <scene>-<Capture>-<theme>-<size>.png. The final frame is
	// always captured as <scene>-<theme>-<size>.png.
	Capture string
}

// Scene is one state of `spec ui` worth looking at.
type Scene struct {
	// Explicit rendering capabilities, independent of terminal background.
	ReducedColor bool
	ASCIIGlyphs  bool
	// Name is the scene's file name stem (lowercase, dashes).
	Name string
	// Description says what the scene shows, for the gallery.
	Description string
	Fixture     Fixture
	// Open is where the UI opens (an anchor is centred on screen); the
	// zero value is the first spec file's top.
	Open tui.Target
	// Edits are uncommitted working-tree changes made before the UI opens.
	Edits []Edit
	// Agents are proposals filed before the UI opens, in order.
	Agents []AgentChange
	// Sizes and Themes default to DefaultSizes and DefaultThemes.
	Sizes  []Size
	Themes []Theme
	// Cascade configures the gate and triage stages on the canned
	// provider ([gate] and a triage_model), so ^P runs gate, triage and
	// check.
	Cascade bool
	// Replies are the canned model replies, in order within each purpose
	// (an agent's check comes first, then the UI's).
	Replies infer.CannedFile
	// Steps bring the UI to the scene's state.
	Steps []Step
}

// sizes returns the scene's sizes.
func (s Scene) sizes() []Size {
	if len(s.Sizes) == 0 {
		return DefaultSizes
	}
	return s.Sizes
}

// themes returns the scene's themes.
func (s Scene) themes() []Theme {
	if len(s.Themes) == 0 {
		return DefaultThemes
	}
	return s.Themes
}

// Variant is one size and theme of a scene.
type Variant struct {
	Size  Size
	Theme Theme
}

// Variants returns every size and theme of the scene.
func (s Scene) Variants() []Variant {
	var out []Variant
	for _, sz := range s.sizes() {
		for _, th := range s.themes() {
			out = append(out, Variant{Size: sz, Theme: th})
		}
	}
	return out
}

// Captures returns the names of the scene's captures in order: the
// intermediate ones, then "" for the final frame.
func (s Scene) Captures() []string {
	var out []string
	for _, st := range s.Steps {
		if st.Capture != "" {
			out = append(out, st.Capture)
		}
	}
	return append(out, "")
}

// FileName is the PNG name of capture (""  for the final frame) in
// variant v.
func (s Scene) FileName(capture string, v Variant) string {
	stem := s.Name
	if capture != "" {
		stem += "-" + capture
	}
	return fmt.Sprintf("%s-%s-%s.png", stem, v.Theme, v.Size)
}

// StepText describes a step in one line, for the gallery.
func (st Step) StepText() string {
	var parts []string
	if st.Find != "" {
		parts = append(parts, fmt.Sprintf("^W %q", st.Find))
	}
	for _, k := range st.Keys {
		parts = append(parts, keyLabel(k))
	}
	if st.Text != "" {
		parts = append(parts, fmt.Sprintf("type %q", st.Text))
	}
	if st.HoverCounter != 0 {
		parts = append(parts, fmt.Sprintf("hover status counter %d", st.HoverCounter))
	}
	if st.Hover != "" {
		parts = append(parts, fmt.Sprintf("hover %q", st.Hover))
	}
	if w := st.Wait.String(); w != "nothing" {
		parts = append(parts, "wait for "+w)
	}
	text := strings.Join(parts, ", ")
	if st.Note != "" {
		if text != "" {
			return st.Note + " (" + text + ")"
		}
		return st.Note
	}
	return text
}

// keyLabel writes a key string as the help bar does: "ctrl+p" is ^P.
func keyLabel(k string) string {
	if rest, ok := strings.CutPrefix(k, "ctrl+"); ok && len(rest) == 1 {
		return "^" + strings.ToUpper(rest)
	}
	if k == "shift+tab" {
		return "⇧Tab"
	}
	return k
}

// Find returns the scene called name.
func Find(name string) (Scene, bool) {
	for _, s := range All() {
		if s.Name == name {
			return s, true
		}
	}
	return Scene{}, false
}
