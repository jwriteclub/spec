// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package scenes

import (
	"strings"

	"github.com/jwriteclub/spec/internal/infer"
)

func settingsScenes() []Scene {
	sizes := []Size{{80, 24}, Size100, Size140, Size220}
	base := []Scene{
		{Name: "help-responsive", Description: "Grouped Help starts with the caller, uses word-wrapped compact actions, quiet aliases and fixed bottom helpers.", Fixture: Demo, Sizes: sizes, Steps: []Step{{Keys: []string{"ctrl+g"}}}},
		{Name: "settings-appearance", Description: "Independent User appearance preferences show defaults, source and staged form controls.", Fixture: Demo, Sizes: sizes, Steps: []Step{{Keys: []string{"ctrl+g", "ctrl+t", "ctrl+q", "down", "down", "enter"}}}},
		{Name: "settings-colour-scheme", Description: "Built-in colour scheme picker keeps both approved palettes embedded; custom discovery is opt-in.", Fixture: Demo, Sizes: sizes, Steps: []Step{{Keys: []string{"ctrl+g", "ctrl+t", "ctrl+q", "down", "down", "enter", "enter"}}}},
		{Name: "scheme-quiet", Description: "Choose and save the quieter built-in through Settings, then review an agent proposal with its actual active palette.", Fixture: Demo, Sizes: sizes, Agents: []AgentChange{agentTiming}, Replies: infer.CannedFile{Check: []infer.CannedEntry{boundReply}}, Steps: []Step{{Keys: []string{"ctrl+g", "ctrl+t", "ctrl+q", "down", "down", "enter", "enter", "down", "down", "enter", "ctrl+o", "u"}, Capture: "saved"}, {Keys: []string{"esc", "ctrl+r"}}}},
		{Name: "settings-setup", Description: "Optional reusable connection chooser. No login, credential read or provider request runs.", Fixture: Demo, Sizes: sizes, Steps: []Step{{Keys: []string{"ctrl+g", "ctrl+t", "tab", "enter"}}}},
		{Name: "settings-save-review", Description: "Concrete review of a staged Synthwave Quiet selection; no settings or document save is performed.", Fixture: Demo, Sizes: sizes, Steps: []Step{{Keys: []string{"ctrl+g", "ctrl+t", "ctrl+q", "down", "down", "enter", "enter", "down", "down", "enter", "ctrl+o"}}}},
	}
	base = append(base,
		Scene{Name: "status-counter-hover", Description: "Hover panels explain diagnostics, whole proposals and other review work without replacing the status message or moving focus.", Fixture: Demo, Sizes: sizes, Agents: []AgentChange{agentTiming}, Replies: infer.CannedFile{Check: []infer.CannedEntry{boundReply}}, Steps: []Step{{Keys: []string{"ctrl+r"}}, {HoverCounter: 1, Capture: "diagnostics"}, {HoverCounter: 2, Capture: "proposals"}, {HoverCounter: 3}}},
		Scene{Name: "proposal-card-spacing", Description: "Two independent agent proposals always have a blank separating row. Long optional author text stays in a bounded data field, apart from the UI-owned title.", Fixture: Demo, Sizes: sizes, Agents: []AgentChange{{Author: strings.Repeat("long-agent-name-", 8), Why: "bound fragment length", Edits: []Edit{timingEdit}}, {Author: "agent-2", Why: "adjust the queue bound", Edits: []Edit{{File: "other.spec.md", Find: limitFind, Replace: limitReplace}}}}, Replies: infer.CannedFile{Check: []infer.CannedEntry{cleanReply, cleanReply, cleanReply}}, Steps: []Step{{Keys: []string{"ctrl+r"}}}},
	)
	var all []Scene
	for _, sc := range base {
		all = append(all, sc)
		sc.Name += "-256-ascii"
		sc.ReducedColor = true
		sc.ASCIIGlyphs = true
		all = append(all, sc)
	}
	return all
}
