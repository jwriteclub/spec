// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"os"
	"strings"

	"github.com/jwriteclub/spec/internal/config"
)

// Appearance capabilities are independent: Terminal.app still uses the palette
// and spacing, with plain underlines and the curated 256-colour mapping.
func (a *App) applyAppearance(c config.Config) error {
	dark := a.detectedDark
	switch c.ForegroundProfile {
	case "dark":
		dark = true
	case "light":
		dark = false
	}
	getenv := os.Getenv
	if a.wf != nil && a.wf.Getenv != nil {
		getenv = a.wf.Getenv
	}
	reduced := a.opts.ReducedColor
	switch c.ColorCapability {
	case "256":
		reduced = true
	case "truecolor":
		reduced = false
	default:
		reduced = reduced || getenv("TERM_PROGRAM") == "Apple_Terminal" || (getenv("TERM") != "" && !strings.Contains(strings.ToLower(getenv("COLORTERM")), "truecolor") && !strings.Contains(strings.ToLower(getenv("COLORTERM")), "24bit"))
	}
	ascii := a.opts.ASCIIGlyphs || c.GlyphProfile == "ascii"
	if c.GlyphProfile == "unicode" {
		ascii = false
	}
	plain := a.opts.PlainUnderline
	switch c.UnderlineColor {
	case "on":
		plain = false
	case "off":
		plain = true
	}
	catalog := loadColorSchemes(c.ColorSchemeDir, a.userConfigPath(), getenv("HOME"))
	scheme, err := catalog.find(c.ColorScheme)
	if err != nil {
		scheme = builtinColorSchemes[0]
	}
	a.theme = newThemeScheme(dark, plain, reduced, ascii, scheme)
	if err != nil {
		a.setStatus(statusFailure, "Colour scheme", a.theme.uiText("Colour scheme: %v · using Synthwave"), err)
	}
	return err
}

func (a *App) openHelp() {
	a.statusTip = nil
	if ref, ok := a.top().(*referenceScreen); ok {
		a.suspendHelpOverlay(ref)
		ref.referenceFocus()
		return
	}
	if _, ok := a.top().(*statusReferenceScreen); ok {
		a.pop()
		if ref, ok := a.top().(*referenceScreen); ok {
			a.suspendHelpOverlay(ref)
			ref.referenceFocus()
			return
		}
	}
	ref := newReferenceScreen(a)
	ref.savedPrompt, ref.savedPopup = a.prompt, a.popup
	a.prompt, a.popup = nil, nil
	a.push(ref)
}

func (a *App) suspendHelpOverlay(ref *referenceScreen) {
	if a.prompt != nil && ref.savedPrompt == nil {
		ref.savedPrompt = a.prompt
	}
	if a.popup != nil && ref.savedPopup == nil {
		ref.savedPopup = a.popup
	}
	a.prompt, a.popup = nil, nil
}

// userConfigPath is the user config of the environment the CLI injected, ""
// without one. cli.Run always injects it (Env.Getenv defaults to os.Getenv),
// so only a test builds an App with none, and that App must not fall back on
// the process's HOME: the settings editor can write what it opens.
func (a *App) userConfigPath() string {
	if a.wf == nil || a.wf.Getenv == nil {
		return ""
	}
	return a.wf.UserConfigPath()
}

// loadUserConfig loads userConfigPath's file. config.Load("") would look in
// the process's HOME, so no environment means an empty config here.
func (a *App) loadUserConfig() (*config.Loaded, error) {
	if a.wf == nil || a.wf.Getenv == nil {
		return &config.Loaded{}, nil
	}
	return config.Load(a.wf.UserConfigPath())
}
