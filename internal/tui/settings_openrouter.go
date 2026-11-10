// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/config"
)

// The OpenRouter setup (Settings → Add connection → OpenRouter API) stages
// three connections on one key: a decision (gate) model, a triage model and
// an inference (check) model, each chosen from config.OpenRouterModels or
// typed in. A role that is already routed is kept unless another model is
// chosen for it explicitly.

// openRouterPick is the kind of one option in a role's picker.
type openRouterPick int

const (
	// pickRecommended stages a model of config.OpenRouterModels.
	pickRecommended openRouterPick = iota
	// pickKeep leaves the role's existing route alone.
	pickKeep
	// pickCustom stages a model id the user types or pastes.
	pickCustom
)

type openRouterOption struct {
	pick        openRouterPick
	model       config.OpenRouterModel
	recommended bool   // the role's default model
	existing    string // pickKeep: the route kept
	note        string
}

// text is the option's one-line label. Only UI literals go through
// uiText; the existing route names the user's own connections.
func (o openRouterOption) text(th *Theme) string {
	switch o.pick {
	case pickKeep:
		return th.uiText("Keep existing · ") + o.existing
	case pickCustom:
		return "Other model ID"
	}
	text := th.uiText(o.model.Label+" · ") + o.model.Model
	if o.recommended {
		text += th.uiText(" · recommended")
	}
	return text
}

// openRouterRole is one role's row: its options, the chosen one and any
// typed model id.
type openRouterRole struct {
	role     config.Role
	title    string
	base     string // connection name stem
	existing string // the existing route, "" when there is none
	options  []openRouterOption
	pick     int
	custom   string
}

func (r openRouterRole) chosen() openRouterOption { return r.options[r.pick] }

// openRouterSetup is the setup screen's state.
type openRouterSetup struct {
	roles   []openRouterRole
	row     int // a role, then the key row, then the stage row
	picking bool
	choice  int
	key     string
}

func (o *openRouterSetup) keyRow() int   { return len(o.roles) }
func (o *openRouterSetup) stageRow() int { return len(o.roles) + 1 }

// openRouterExisting describes what already routes role in the draft, or
// returns "".
func (s *settingsScreen) openRouterExisting(role config.Role) string {
	str := func(path ...string) string {
		v, _ := s.user.Get(path...)
		text, _ := v.(string)
		return text
	}
	switch role {
	case config.RoleCheck:
		if d := str("default"); d != "" {
			return fmt.Sprintf("default = %q", d)
		}
	case config.RoleTriage:
		if t := str("triage", "provider"); t != "" {
			return fmt.Sprintf("[triage] provider = %q", t)
		}
		if d := str("default"); d != "" {
			if m := str("providers", d, "triage_model"); m != "" {
				return fmt.Sprintf("triage_model %q on %s", m, d)
			}
		}
	case config.RoleGate:
		if g := str("gate", "provider"); g != "" {
			return fmt.Sprintf("[gate] provider = %q", g)
		}
	}
	return ""
}

func (s *settingsScreen) openOpenRouterSetup() {
	o := &openRouterSetup{}
	for _, r := range []struct {
		role        config.Role
		title, base string
	}{
		{config.RoleGate, "Decision (gate) model", "openrouter-gate"},
		{config.RoleTriage, "Triage model", "openrouter-triage"},
		{config.RoleCheck, "Inference (check) model", "openrouter"},
	} {
		row := openRouterRole{role: r.role, title: r.title, base: r.base, existing: s.openRouterExisting(r.role)}
		if row.existing != "" {
			row.options = append(row.options, openRouterOption{pick: pickKeep, existing: row.existing,
				note: "Already set: left unchanged unless you choose a model here."})
		}
		for i, m := range config.OpenRouterChoices(r.role) {
			row.options = append(row.options, openRouterOption{pick: pickRecommended, model: m, recommended: i == 0, note: m.Note})
		}
		note := "Paste or type any OpenRouter model id; no tuned settings are added."
		if r.role == config.RoleGate {
			note = "Paste or type a Decisions API model id (a gate cannot use a chat model)."
		}
		row.options = append(row.options, openRouterOption{pick: pickCustom, note: note})
		o.roles = append(o.roles, row)
	}
	s.openrouter = o
	s.notice = ""
}

// openRouterKey handles a key while the setup screen is open.
func (s *settingsScreen) openRouterKey(msg tea.KeyPressMsg) tea.Cmd {
	o := s.openrouter
	k := msg.String()
	if o.picking {
		r := &o.roles[o.row]
		switch k {
		case "esc":
			o.picking = false
		case "up", "shift+tab":
			o.choice = max(o.choice-1, 0)
		case "down", "tab":
			o.choice = min(o.choice+1, len(r.options)-1)
		case "enter", "space":
			r.pick = o.choice
			o.picking = false
			if r.chosen().pick == pickCustom {
				s.notice = "Type or paste the model id"
			}
		}
		return nil
	}
	text := msg.Key().Text
	printable := text != "" && msg.Key().Mod&(tea.ModCtrl|tea.ModAlt) == 0
	switch k {
	case "esc":
		s.openrouter = nil
		s.setup = true
		s.notice = ""
		return nil
	case "up", "shift+tab":
		o.row = max(o.row-1, 0)
		return nil
	case "down", "tab":
		o.row = min(o.row+1, o.stageRow())
		return nil
	case "enter":
		switch {
		case o.row < len(o.roles):
			o.picking = true
			o.choice = o.roles[o.row].pick
		case o.row == o.keyRow():
			o.row++
		default:
			s.stageOpenRouter()
		}
		return nil
	}
	switch {
	case o.row == o.keyRow():
		switch {
		case k == "backspace" || k == "delete" || k == "ctrl+u":
			o.key = ""
		case printable:
			o.key += text
		}
	case o.row < len(o.roles) && o.roles[o.row].chosen().pick == pickCustom:
		r := &o.roles[o.row]
		switch {
		case k == "backspace":
			if rs := []rune(r.custom); len(rs) > 0 {
				r.custom = string(rs[:len(rs)-1])
			}
		case k == "ctrl+u":
			r.custom = ""
		case printable && k != "space":
			r.custom += text
		}
	case o.row < len(o.roles) && k == "space":
		o.picking = true
		o.choice = o.roles[o.row].pick
	}
	return nil
}

// openRouterPaste puts pasted text in the key or a typed model id.
func (s *settingsScreen) openRouterPaste(text string) {
	o := s.openrouter
	if o.picking {
		return
	}
	if strings.ContainsAny(text, "\r\n") {
		s.notice = "Single-line field: paste must not contain a newline"
		return
	}
	switch {
	case o.row == o.keyRow():
		o.key += text
	case o.row < len(o.roles) && o.roles[o.row].chosen().pick == pickCustom:
		o.roles[o.row].custom += strings.TrimSpace(text)
	}
}

// freeProviderName returns base, or base-2, base-3, ... when that
// connection exists.
func (s *settingsScreen) freeProviderName(base string) string {
	name := base
	for i := 2; ; i++ {
		if _, exists := s.user.Get("providers", name); !exists {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

// stageOpenRouter stages a connection and its route for every role not
// kept. A new [gate] gets the standard thresholds; an existing one keeps
// its thresholds and only changes provider. The session key, if typed,
// is staged for each new connection.
func (s *settingsScreen) stageOpenRouter() {
	o := s.openrouter
	for _, r := range o.roles {
		if r.chosen().pick != pickCustom {
			continue
		}
		id := strings.TrimSpace(r.custom)
		if id == "" || strings.ContainsAny(id, " \t") {
			s.notice = r.title + ": enter a model id without spaces, or choose a recommended model"
			return
		}
	}
	var staged, kept []string
	first := ""
	for _, r := range o.roles {
		opt := r.chosen()
		if opt.pick == pickKeep {
			kept = append(kept, string(r.role))
			continue
		}
		m := opt.model
		if opt.pick == pickCustom {
			m = config.OpenRouterModel{Role: r.role, Model: strings.TrimSpace(r.custom)}
		}
		name := s.freeProviderName(r.base)
		s.user.Set(m.Provider(), "providers", name)
		switch r.role {
		case config.RoleCheck:
			s.user.Set(name, "default")
			first = name
		case config.RoleTriage:
			s.user.Set(name, "triage", "provider")
		case config.RoleGate:
			if _, ok := s.user.Get("gate"); ok {
				s.user.Set(name, "gate", "provider")
			} else {
				s.user.Set(map[string]any{"provider": name, "escalate_class_at": config.DefaultEscalateClassAt, "skip_material_below": 0.0}, "gate")
			}
		}
		if first == "" {
			first = name
		}
		if o.key != "" {
			s.secrets[name] = o.key
			s.bumpCredential(name)
		}
		staged = append(staged, string(r.role)+" "+name)
	}
	s.openrouter = nil
	s.setup = false
	if len(staged) == 0 {
		s.notice = "Nothing staged: every role is already set. Choose a model for a role to replace it."
		return
	}
	s.text = map[string]string{}
	s.baseline = map[string]string{}
	s.touched = map[string]bool{}
	s.provider = first
	s.section = 1
	s.field = 0
	s.scroll = 0
	notice := "Staged " + strings.Join(staged, ", ")
	if len(kept) > 0 {
		notice += "; kept " + strings.Join(kept, ", ")
	}
	s.notice = notice + s.theme.uiText(" · ^T tests, ^O reviews and saves. No test has run.")
}

// openRouterView draws the setup screen into out.
func (s *settingsScreen) openRouterView(a *App, out []string, w, h int) {
	o := s.openrouter
	if s.notice == "" && h > 2 {
		out[2] = "  " + a.theme.PopupDim.Render(a.theme.uiText("OpenRouter API · one key sets up three models"))
	}
	var lines []string
	add := func(text string) { lines = append(lines, text) }
	wrap := func(text string) {
		for _, ln := range helpWrap(text, max(w-4, 1)) {
			add("  " + a.theme.PopupDim.Render(ln))
		}
	}
	card := func(text string, selected bool) string {
		style := a.theme.HelpLabel.Background(a.theme.Token("card"))
		if selected {
			style = a.theme.PopupTitle.Foreground(a.theme.Token("control")).Background(a.theme.Token("selectedCard"))
		}
		return "  " + style.Render(padTo(ansi.Truncate(text, max(w-4, 1), ""), max(w-4, 1)))
	}
	focus := 0
	if o.picking {
		r := o.roles[o.row]
		add("  " + a.theme.PopupTitle.Render("Choose the "+strings.ToLower(r.title[:1])+r.title[1:]))
		add("")
		for i, opt := range r.options {
			if i == o.choice {
				focus = len(lines) + 3
			}
			add(card(opt.text(a.theme), i == o.choice))
			for n, ln := range helpWrap(opt.note, max(w-6, 1)) {
				if n > 1 {
					break
				}
				add("    " + a.theme.PopupDim.Render(ln))
			}
		}
	} else {
		wrap(a.theme.uiText("This sets up a decision (gate) model, a triage model and an inference (check) model, each on its own connection using " + config.OpenRouterKeyEnv + " (environment or env_file) or the session key below. A role already set is kept unless you choose a model for it."))
		add("")
		field := func(i int, label, value, help string) {
			style := a.theme.HelpLabel
			if i == o.row {
				style = a.theme.PopupTitle.Foreground(a.theme.Token("control"))
				focus = len(lines) + 3
			}
			add("  " + style.Render(ansi.Truncate(label, max(w-4, 1), "")))
			add("  " + a.theme.HelpLabel.Background(a.theme.Token("card")).Render("[ "+ansi.Truncate(value, max(w-8, 1), "")+" ]"))
			add("  " + a.theme.PopupDim.Render(ansi.Truncate(help, max(w-4, 1), "")))
		}
		for i, r := range o.roles {
			opt := r.chosen()
			value, help := opt.text(a.theme)+"  v", a.theme.uiText(opt.note)
			if opt.pick == pickCustom {
				value = r.custom
				if i == o.row {
					value += "_"
				}
				if strings.TrimSpace(r.custom) == "" && i != o.row {
					value = a.theme.uiText("Other model ID · not entered")
				}
				help = a.theme.uiText("Enter · back to the list. " + opt.note)
			}
			if opt.pick == pickKeep && r.role == config.RoleTriage && strings.HasPrefix(r.existing, "triage_model") && o.roles[len(o.roles)-1].chosen().pick != pickKeep {
				help = "Applies only while that connection is the default; the check model below replaces it."
			}
			field(i, r.title, value, help)
		}
		masked := "        "
		if o.key != "" {
			masked = "********"
		}
		field(o.keyRow(), a.theme.uiText("Key for this session · optional"), masked, a.theme.uiText("Session only · masked. Blank uses "+config.OpenRouterKeyEnv+"; never written to config."))
		var adds []string
		for _, r := range o.roles {
			if r.chosen().pick != pickKeep {
				adds = append(adds, string(r.role))
			}
		}
		summary := "Every role is already set; nothing would be staged."
		if len(adds) > 0 {
			summary = "Stages " + strings.Join(adds, ", ") + "; Review before Save. No request is sent."
		}
		field(o.stageRow(), "Stage connections", a.theme.uiText("Enter · Stage"), summary)
	}
	// Scroll so the focused row stays on screen.
	room := max(h-4, 1)
	start := max(0, min(focus-room, len(lines)-room))
	for i := 0; i < room && start+i < len(lines); i++ {
		out[4+i] = lines[start+i]
	}
}
