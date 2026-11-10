// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import "strings"

// uiText converts generated UI literals before interpolation and layout.
// Never pass document text, diffs, paths, author names or other user data.
func (t *Theme) uiText(text string) string {
	if t == nil || !t.ASCII {
		return text
	}
	return asciiUIGlyphs.Replace(text)
}

var asciiUIGlyphs = strings.NewReplacer(
	"·", "|", "…", "...", "—", "-", "–", "-", "→", "->", "←", "<-",
	"↑", "Up", "↓", "Down", "⇧", "S-", "│", "|", "─", "-", "✓", "OK",
	"•", "*", "◦", "o", "§", "S", "×", "x", "✗", "x", "⚠", "!",
	"┌", "+", "┐", "+", "└", "+", "┘", "+", "├", "+", "┤", "+",
	"▶", ">", "▪", "*", "↔", "<->",
	"⌥", "Alt", "⌘", "Cmd", "⏎", "Enter", "↵", "Enter",
)

func uiTheme(themes []*Theme) *Theme {
	if len(themes) > 0 {
		return themes[0]
	}
	return nil
}
