// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"image/color"
	"math"

	"charm.land/lipgloss/v2"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/tui/editor"
)

// Theme holds the styles for one terminal background (dark or light).
// Colors are chosen per background with lipgloss.LightDark, Lip Gloss v2's
// replacement for adaptive colors; the background is learned from the
// terminal (tea.BackgroundColorMsg) and defaults to dark.
type Theme struct {
	scheme  *colorScheme
	Dark    bool
	Reduced bool
	ASCII   bool
	// PlainUnderline underlines diagnostics and bad references without an
	// underline colour or style (see Options.PlainUnderline).
	PlainUnderline bool

	inv, def, section, bad, code, diagErr, diagWarn, diagHint, dim, accent, add color.Color
	// spell is the underline of a word the spell check flagged: near red.
	spell color.Color
	// fix is the colour of a suggested fix: its box in the Checks sidebar
	// and its words shown inline in the editor (one semantic token, so the
	// colour pass can change it in one place).
	fix color.Color
	// bars are local marker backgrounds while a proposal is active.
	// Each scheme supplies independent truecolor and xterm-256 values.
	bars map[barKind]color.Color

	Ink, Secondary, Structure, Focus                   lipgloss.Style
	Human, Agent, FixOrigin                            lipgloss.Style
	SideCard, SideCardFocus, SideChild, SideChildFocus lipgloss.Style
	Gutter                                             lipgloss.Style
	GutterCur                                          lipgloss.Style
	MarkError                                          lipgloss.Style
	MarkWarn                                           lipgloss.Style
	MarkHint                                           lipgloss.Style
	Status                                             lipgloss.Style
	StatusDirty                                        lipgloss.Style
	Flash                                              lipgloss.Style
	HelpKey                                            lipgloss.Style
	HelpLabel                                          lipgloss.Style
	// HelpKeyDim and HelpLabelDim draw a help bar entry whose action
	// cannot run now.
	HelpKeyDim   lipgloss.Style
	HelpLabelDim lipgloss.Style
	Border       lipgloss.Style
	PopupTitle   lipgloss.Style
	PopupDim     lipgloss.Style
	Selected     lipgloss.Style
	OutlineFile  lipgloss.Style
	OutlineSec   lipgloss.Style
	OutlineInv   lipgloss.Style
	OutlineDef   lipgloss.Style
	Banner       lipgloss.Style
	Prompt       lipgloss.Style
	DiffAdd      lipgloss.Style
	DiffDel      lipgloss.Style
	DiffAddWord  lipgloss.Style
	DiffDelWord  lipgloss.Style
	Accepted     lipgloss.Style
	Skipped      lipgloss.Style
	Stale        lipgloss.Style
	HunkHead     lipgloss.Style
	// FixHead is the first line of a suggested fix's box ("suggested fix
	// in $Name").
	FixHead lipgloss.Style
	// Bar draws a marker cell on a proposal bar: the bar's background, and
	// the diagnostic letter bold in black or white, whichever contrasts
	// more.
	Bar map[barKind]lipgloss.Style
	// SideHead is the sidebar's (and the commit view's) title band;
	// SideHeadFocus while the sidebar has the keyboard; SideGroup the band
	// naming the proposal a run of cards comes from.
	SideHead      lipgloss.Style
	SideHeadFocus lipgloss.Style
	SideGroup     lipgloss.Style
	// FocusEdge is the edge of a side pane (sidebar, outline) that has
	// the keyboard.
	FocusEdge lipgloss.Style
	// Tick marks the extent of the anchor under the cursor in the gutter;
	// TickSection that of the section when the cursor is in no anchor.
	Tick        lipgloss.Style
	TickSection lipgloss.Style
	// Pipe draws the thin pipe in the marker cell of a line changed since
	// HEAD outside the changed anchors: prose with words added (green),
	// removed (red) or both (yellow), or format only (cyan).
	Pipe map[barKind]lipgloss.Style

	runs map[editor.Flag]lipgloss.Style
}

// NewTheme returns the theme for a dark or light background; with
// plainUnderline, underlines carry no colour or style.
func NewTheme(dark, plainUnderline bool) *Theme {
	return NewThemeProfile(dark, plainUnderline, false, false)
}

// NewThemeProfile applies foreground, colour-depth and glyph capabilities
// independently. Background fills remain local to components.
func NewThemeProfile(dark, plainUnderline, reduced, ascii bool) *Theme {
	return newThemeScheme(dark, plainUnderline, reduced, ascii, builtinColorSchemes[0])
}

func newThemeScheme(dark, plainUnderline, reduced, ascii bool, scheme *colorScheme) *Theme {
	t := &Theme{Dark: dark, PlainUnderline: plainUnderline, Reduced: reduced, ASCII: ascii,
		scheme: scheme,
		runs:   map[editor.Flag]lipgloss.Style{}, bars: map[barKind]color.Color{}, Bar: map[barKind]lipgloss.Style{}}
	t.inv = t.Token("syntaxInvariant")
	t.def = t.Token("syntaxDefinition")
	t.section = t.Token("syntaxSection")
	t.bad = t.Token("error")
	t.code = t.Token("secondary")
	t.diagErr = t.Token("error")
	t.diagWarn = t.Token("warning")
	t.diagHint = t.Token("control")
	t.dim = t.Token("structure")
	t.accent = t.Token("control")
	t.add = t.Token("positive")
	t.fix = t.Token("fix")
	t.spell = t.Token("spell")
	s := lipgloss.NewStyle
	t.Ink = s().Foreground(t.Token("ink"))
	t.Secondary = s().Foreground(t.Token("secondary"))
	t.Structure = s().Foreground(t.Token("structure"))
	t.Focus = s().Foreground(t.Token("focus")).Bold(true)
	t.Human = s().Foreground(t.Token("human"))
	t.Agent = s().Foreground(t.Token("agent"))
	t.FixOrigin = s().Foreground(t.Token("fix"))
	t.SideCard = s().Background(t.Token("card"))
	t.SideCardFocus = s().Background(t.Token("selectedCard"))
	t.SideChild = s().Background(t.Token("childCard"))
	t.SideChildFocus = s().Background(t.Token("selectedChildCard"))
	t.Gutter = t.Structure
	t.GutterCur = t.Secondary.Bold(true)
	t.MarkError = s().Foreground(t.diagErr).Bold(true)
	t.MarkWarn = s().Foreground(t.diagWarn).Bold(true)
	t.MarkHint = s().Foreground(t.diagHint)
	t.Status = s().Foreground(t.Token("statusInk")).Background(t.Token("status"))
	t.StatusDirty = t.Status.Foreground(t.Token("statusWarning"))
	t.Flash = t.Status
	t.HelpKey = s().Foreground(t.accent)
	t.HelpLabel = t.Ink
	t.HelpKeyDim = s().Foreground(t.Token("disabled"))
	t.HelpLabelDim = t.HelpKeyDim
	t.Border = t.Structure
	t.PopupTitle = t.Focus
	t.PopupDim = t.Secondary
	t.Selected = s().Background(t.Token("selectedCard")).Bold(true)
	t.OutlineFile = t.Focus
	t.OutlineSec = s().Foreground(t.section)
	t.OutlineInv = s().Foreground(t.inv)
	t.OutlineDef = s().Foreground(t.def)
	t.Banner = s().Foreground(t.Token("ink")).Background(t.Token("warningCount")).Bold(true)
	t.Prompt = t.Focus
	t.DiffAdd = s().Foreground(t.add)
	t.DiffDel = s().Foreground(t.bad)
	t.DiffAddWord = t.DiffAdd.Bold(true)
	t.DiffDelWord = t.DiffDel.Strikethrough(true)
	t.Accepted = t.DiffAdd.Bold(true)
	t.Skipped = t.Secondary
	t.Stale = s().Foreground(t.diagWarn).Bold(true)
	t.HunkHead = t.Focus
	t.FixHead = t.FixOrigin.Bold(true)
	t.SideHead = t.Focus
	t.SideHeadFocus = t.Focus
	t.SideGroup = t.Secondary.Bold(true)
	t.FocusEdge = s().Foreground(t.accent)
	t.Tick = s().Foreground(t.Token("gutterTick"))
	t.TickSection = t.Tick
	t.bars = map[barKind]color.Color{barAnchor: t.Token("gutterHuman"), barFix: t.Token("childCard"), barFixStale: t.Token("card"), barOther: t.Token("gutterHuman")}
	t.Pipe = map[barKind]lipgloss.Style{barProseAdd: s().Foreground(t.add), barProseDel: s().Foreground(t.bad), barProseBoth: s().Foreground(t.diagWarn), barFormat: s().Foreground(t.fix)}
	for k, bg := range t.bars {
		t.Bar[k] = s().Background(bg).Foreground(t.Token("ink"))
	}
	return t
}

// onColor is black or white, whichever contrasts more with bg.
func onColor(bg color.Color) color.Color {
	black, white := lipgloss.Color("#000000"), lipgloss.Color("#ffffff")
	if contrast(black, bg) >= contrast(white, bg) {
		return black
	}
	return white
}

// luminance is the WCAG relative luminance of c.
func luminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	ch := func(v uint32) float64 {
		x := float64(v) / 0xffff
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(r) + 0.7152*ch(g) + 0.0722*ch(b)
}

// contrast is the WCAG contrast ratio of a and b, from 1 to 21.
func contrast(a, b color.Color) float64 {
	x, y := luminance(a), luminance(b)
	if x < y {
		x, y = y, x
	}
	return (x + 0.05) / (y + 0.05)
}

// Mark returns the gutter style for a diagnostic severity.
func (t *Theme) Mark(s diag.Severity) lipgloss.Style {
	switch s {
	case diag.Error:
		return t.MarkError
	case diag.Warning:
		return t.MarkWarn
	}
	return t.MarkHint
}

// Run returns the style of an editor highlight run.
func (t *Theme) Run(f editor.Flag) lipgloss.Style {
	if st, ok := t.runs[f]; ok {
		return st
	}
	st := lipgloss.NewStyle()
	switch {
	case f&editor.FBadRef != 0:
		st = t.underline(st.Foreground(t.bad), t.bad)
	case f&(editor.FInvSite) != 0:
		st = st.Foreground(t.inv).Bold(true)
	case f&(editor.FDefSite) != 0:
		st = st.Foreground(t.def).Bold(true)
	case f&editor.FInv != 0:
		st = st.Foreground(t.inv)
	case f&editor.FDef != 0:
		st = st.Foreground(t.def)
	case f&editor.FSection != 0:
		st = st.Foreground(t.section)
	case f&editor.FCode != 0:
		st = st.Foreground(t.code)
	case f&editor.FModelDecl != 0:
		st = st.Foreground(t.def).Bold(true)
	case f&editor.FModelTagKey != 0:
		st = st.Foreground(t.accent).Bold(true)
	case f&editor.FModelKeyword != 0:
		st = st.Foreground(t.section)
	case f&(editor.FModelString|editor.FModelNumber) != 0:
		st = st.Foreground(t.inv)
	case f&editor.FModelOperator != 0:
		st = st.Foreground(t.dim)
	case f&editor.FModelComment != 0:
		st = st.Foreground(t.code).Italic(true)
	}
	if f&editor.FModelLink != 0 {
		st = st.Underline(true)
	}
	if f&editor.FSection != 0 && f&(editor.FInv|editor.FDef|editor.FInvSite|editor.FDefSite|editor.FBadRef) == 0 {
		st = st.Foreground(t.section)
	}
	if f&editor.FLabel != 0 {
		st = st.Bold(true)
	}
	if f&editor.FBadRef == 0 {
		switch {
		case f&editor.FDiagError != 0:
			st = t.underline(st, t.diagErr)
		case f&editor.FDiagWarn != 0:
			st = t.underline(st, t.diagWarn)
		case f&editor.FDiagHint != 0 && t.PlainUnderline:
			st = st.Underline(true)
		case f&editor.FDiagHint != 0:
			st = st.UnderlineStyle(lipgloss.UnderlineDotted).UnderlineColor(t.diagHint)
		case f&editor.FSpell != 0:
			// A misspelled word: a plain near-red underline, as any
			// spell checker draws it.
			st = t.underline(st, t.spell)
		}
	}
	diagFlags := editor.FDiagError | editor.FDiagWarn | editor.FDiagHint | editor.FBadRef | editor.FSpell
	if f&editor.FAdded != 0 && f&diagFlags == 0 {
		// Added since HEAD: a green underline; where the terminal has no
		// underline colour, prose turns green and coloured tokens keep
		// their colour, both plainly underlined.
		if t.PlainUnderline {
			if f&(editor.FInv|editor.FInvSite|editor.FDef|editor.FDefSite|editor.FSection|editor.FCode) == 0 {
				st = st.Foreground(t.add)
			}
			st = st.Underline(true)
		} else {
			st = st.Underline(true).UnderlineColor(t.add)
		}
	}
	if f&editor.FRemoved != 0 {
		// Removed since HEAD (ghost text): dim, with a red underline, or
		// struck through where the terminal has no underline colour.
		st = st.Foreground(t.dim)
		if t.PlainUnderline {
			st = st.Strikethrough(true)
		} else {
			st = st.Underline(true).UnderlineColor(t.bad)
		}
	}
	if f&editor.FFixOld != 0 {
		// The words the selected fix replaces: struck through, dim.
		st = st.Foreground(t.dim).Strikethrough(true)
	}
	if f&editor.FFixNew != 0 {
		// The words it puts in (ghost text): bold, in the fix colour.
		st = st.Foreground(t.fix).Bold(true)
	}
	if f&editor.FMatch != 0 {
		st = st.Background(t.diagWarn).Foreground(lipgloss.Color("#000000"))
	}
	if f&editor.FSelect != 0 {
		st = st.Reverse(true)
	}
	t.runs[f] = st
	return st
}

// underline underlines st in colour c, or plainly with PlainUnderline.
func (t *Theme) underline(st lipgloss.Style, c color.Color) lipgloss.Style {
	if t.PlainUnderline {
		return st.Underline(true)
	}
	return st.Underline(true).UnderlineColor(c)
}
