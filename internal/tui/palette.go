// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	_ "embed"
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
)

//go:embed design_palette.json
var paletteJSON []byte

type paletteRole struct {
	Dark     string `json:"dark"`
	Light    string `json:"light"`
	Dark256  int    `json:"dark256"`
	Light256 int    `json:"light256"`
}

// Token is a semantic colour from the agreed full or curated reduced palette.
func (t *Theme) Token(name string) color.Color {
	scheme := t.scheme
	if scheme == nil {
		scheme = builtinColorSchemes[0]
	}
	p, ok := scheme.roles[name]
	if !ok {
		// Preserve the requested role when a programmatically constructed scheme
		// is incomplete. Unknown roles fall back to foreground ink without
		// turning a rendering mistake into an application crash.
		p, ok = builtinColorSchemes[0].roles[name]
		if !ok {
			p, ok = scheme.roles["ink"]
		}
		if !ok {
			p, ok = builtinColorSchemes[0].roles["ink"]
		}
		if !ok {
			p = paletteRole{Dark: "#ffffff", Light: "#000000", Dark256: 231, Light256: 16}
		}
	}
	if t.Reduced {
		n := p.Light256
		if t.Dark {
			n = p.Dark256
		}
		return lipgloss.Color(fmt.Sprint(n))
	}
	c := p.Light
	if t.Dark {
		c = p.Dark
	}
	return lipgloss.Color(c)
}
