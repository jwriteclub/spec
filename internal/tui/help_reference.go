// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/termtext"
	"github.com/jwriteclub/spec/internal/workflow"
)

// referenceScreen owns both tabs, so switching tabs preserves the entire form.
// Its caller remains untouched on the screen stack.
type referenceScreen struct {
	callerGuards          map[Action]string
	theme                 *Theme
	savedPrompt           *prompt
	savedPopup            *popup
	returnSettings        bool
	anchored              bool
	caller                Context
	scroll, width, height int
	query                 string
	find, replaceQuery    bool
	match                 int
	feedback              string
	settings              *settingsScreen
	showSettings          bool
	columns               [][]helpReferenceLine
}
type helpReferenceLine struct {
	text    string
	heading bool
	alias   bool
	search  string
	id      string
	match   bool
}
type helpReferenceGroup struct {
	title string
	id    string
	rows  []helpReferenceLine
}

func newReferenceScreen(a *App) *referenceScreen {
	ref := &referenceScreen{theme: a.theme, caller: a.context(), match: -1}
	if r, ok := a.top().(*reviewScreen); ok {
		ref.callerGuards = map[Action]string{ActApprove: r.approveBlocked(a), ActRetryCheck: r.retryBlocked(a)}
	}
	if a.inModel() {
		for _, action := range modelClosedIn(ref.caller) {
			if ref.callerGuards == nil {
				ref.callerGuards = map[Action]string{}
			}
			ref.callerGuards[action] = modelClosed
		}
	}
	return ref
}
func (s *referenceScreen) context(a *App) Context {
	if s.showSettings {
		return s.settings.context(a)
	}
	return CtxHelp
}

// referenceFocus implements the universal Ctrl+G route without nesting Help.
func (s *referenceScreen) referenceFocus() {
	if s.showSettings {
		s.returnSettings = true
	}
	s.showSettings = false
	s.find = false
}
func (s *referenceScreen) settingsTab(a *App) {
	if s.settings == nil {
		s.settings = newSettingsScreen(a)
		s.settings.back = func() { s.returnSettings = true; s.showSettings = false }
		s.settings.close = func() { a.pop() }
	}
	s.showSettings = true
}
func (s *referenceScreen) key(a *App, msg tea.KeyPressMsg, b Binding, ok bool) tea.Cmd {
	if s.showSettings {
		return s.settings.key(a, msg, b, ok)
	}
	k := msg.String()
	if k == "ctrl+g" || k == "f1" {
		s.referenceFocus()
		return nil
	}
	if s.find {
		switch k {
		case "esc":
			s.find = false
		case "ctrl+w":
			s.replaceQuery = true
		case "ctrl+u":
			s.query = ""
			s.match = -1
		case "enter", "shift+enter":
			s.visitMatch(k == "shift+enter")
		case "backspace":
			r := []rune(s.query)
			if len(r) > 0 {
				s.query = string(r[:len(r)-1])
			}
			s.match = -1
		default:
			if msg.Key().Text != "" && msg.Key().Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
				if s.replaceQuery {
					s.query = ""
					s.replaceQuery = false
				}
				s.query += msg.Key().Text
				s.match = -1
			}
		}
		return nil
	}
	switch k {
	case "ctrl+t":
		s.settingsTab(a)
	case "ctrl+w":
		s.find = true
		s.replaceQuery = true
	case "s":
		a.push(&statusReferenceScreen{})
	case "enter", "shift+enter":
		s.visitMatch(k == "shift+enter")
	case "home":
		s.scroll = 0
	case "end":
		s.scroll = s.maxScroll()
	case "esc", "ctrl+x", "ctrl+c", "q":
		if k == "esc" && s.returnSettings {
			s.returnSettings = false
			s.showSettings = true
			return nil
		}
		if s.settings != nil && s.settings.probeRunning {
			s.showSettings = true
			s.settings.notice = "Cancel the running test with Ctrl+C before closing"
		} else if s.settings != nil && (s.settings.dirty() > 0 || len(s.settings.secrets) > 0) {
			s.showSettings = true
			s.settings.confirm = "close"
		} else {
			a.pop()
		}
	case "up":
		s.scroll--
	case "down":
		s.scroll++
	case "pgup":
		s.scroll -= max(s.height-5, 1)
	case "pgdown", "ctrl+v":
		s.scroll += max(s.height-5, 1)
	}
	s.scroll = min(max(s.scroll, 0), s.maxScroll())
	return nil
}
func (s *referenceScreen) mouse(a *App, msg tea.MouseMsg) tea.Cmd {
	if s.showSettings {
		return s.settings.mouse(a, msg)
	}
	if w, ok := msg.(tea.MouseWheelMsg); ok {
		if w.Button == tea.MouseWheelUp {
			s.scroll -= 3
		} else if w.Button == tea.MouseWheelDown {
			s.scroll += 3
		}
		s.scroll = min(max(s.scroll, 0), s.maxScroll())
	}
	if m, ok := msg.(tea.MouseClickMsg); ok && m.Y == 0 {
		if m.X >= 10 && m.X < 22 {
			s.settingsTab(a)
		} else if m.X >= 24 {
			a.push(&statusReferenceScreen{})
		}
	}
	return nil
}
func helpColumns(w int) (int, int) {
	n := 1
	if w >= 200 {
		n = 4
	} else if w >= 140 {
		n = 3
	} else if w >= 100 {
		n = 2
	}
	return n, max((w-4-4*(n-1))/n, 1)
}
func helpWrap(text string, w int) []string {
	if w < 1 {
		return nil
	}
	return strings.Split(ansi.Wrap(text, w, ""), "\n")
}
func referenceKeyName(k string) string {
	parts := strings.Split(k, "+")
	for i, p := range parts {
		switch p {
		case "ctrl":
			parts[i] = "Ctrl"
		case "alt":
			parts[i] = "Alt"
		case "shift":
			parts[i] = "Shift"
		case "pgup":
			parts[i] = "PgUp"
		case "pgdown":
			parts[i] = "PgDn"
		default:
			r := []rune(p)
			if len(r) > 0 && (len(r) > 1 || len(parts) > 1) {
				r[0] = unicode.ToUpper(r[0])
				parts[i] = string(r)
			}
		}
	}
	return strings.Join(parts, "+")
}
func (s *referenceScreen) groups(colw int) []helpReferenceGroup {
	common := helpReferenceGroup{title: "Common conventions", id: "common"}
	for i, t := range []string{"^ means Ctrl; M- aliases mean Alt/Option. Ctrl+S is reserved for terminal flow control. Ctrl+6 and Ctrl+^ toggle the outline.", "Mac fn+Up/Down send page keys. Tab inserts editor text; Shift+Tab changes panes. Return/Enter and Backspace aliases depend on the terminal.", "Ctrl+G opens Help and preserves your caller. Ctrl+R opens Checks in Editor and hides it in Checks. y applies a fix, selects a hunk, or ticks a commit group depending on the screen; it never means saved or committed."} {
		for _, ln := range helpWrap(t, colw) {
			common.rows = append(common.rows, helpReferenceLine{text: ln, id: fmt.Sprintf("common.%d", i)})
		}
	}
	groups := []helpReferenceGroup{common}
	for _, c := range Contexts {
		g := helpReferenceGroup{title: c.String(), id: fmt.Sprint(c)}
		if c == s.caller {
			g.title += s.theme.uiText(" · Current screen")
		}
		for bi, b := range BindingsIn(c) {
			id := fmt.Sprintf("%d.%d", c, bi)
			key := referenceKeyName(b.Keys[0])
			desc := s.theme.uiText(compactHelpDescription(c, b))
			if c == s.caller {
				if why := s.callerGuards[b.Action]; why != "" {
					desc += " Unavailable when Help opened: " + termtext.Clean(why)
				}
			}
			keys := helpWrap(key, min(15, colw))
			descs := helpWrap(desc, max(colw-17, 1))
			if colw < 24 {
				for _, ln := range helpWrap(key+"  "+desc, colw) {
					g.rows = append(g.rows, helpReferenceLine{text: ln, id: id, alias: true})
				}
			} else {
				for i := 0; i < max(len(keys), len(descs)); i++ {
					left, right := "", ""
					if i < len(keys) {
						left = keys[i]
					}
					if i < len(descs) {
						right = descs[i]
					}
					metadata := ""
					if i == 0 {
						metadata = b.Label + " " + b.Desc + " " + strings.Join(b.Keys, " ")
					}
					g.rows = append(g.rows, helpReferenceLine{text: padTo(left, 15) + "  " + right, id: id, search: metadata})
				}
			}
			if len(b.Keys) > 1 {
				var aliases []string
				for _, k := range b.Keys[1:] {
					aliases = append(aliases, referenceKeyName(k))
				}
				for _, ln := range helpWrap("also "+strings.Join(aliases, ", "), colw) {
					g.rows = append(g.rows, helpReferenceLine{text: ln, id: id, alias: true})
				}
			}
		}
		groups = append(groups, g)
	}
	terminal := helpReferenceGroup{title: "Terminal / mouse", id: "terminal"}
	for _, line := range helpWrap("Click places the cursor; drag selects; wheel scrolls. Ctrl+click or Alt+click follows references. Hover previews without executing commands.", colw) {
		terminal.rows = append(terminal.rows, helpReferenceLine{text: line, id: "terminal.mouse"})
	}
	groups = append(groups, terminal)

	return groups
}
func (s *referenceScreen) layout(w, h int) {
	anchor := ""
	if len(s.columns) > 0 && s.scroll < len(s.columns[0]) {
		anchor = s.columns[0][s.scroll].id
	}
	n, cw := helpColumns(w)
	groups := s.groups(cw)
	for i, g := range groups {
		if g.id == fmt.Sprint(s.caller) {
			groups = append([]helpReferenceGroup{g}, append(groups[:i], groups[i+1:]...)...)
			break
		}
	}
	s.columns = make([][]helpReferenceLine, n)
	total := 0
	for _, g := range groups {
		total += len(g.rows) + 3
	}
	target := (total + n - 1) / n
	col := 0
	query := strings.ToLower(strings.TrimSpace(s.query))
	for _, g := range groups {
		if col < n-1 && len(s.columns[col]) > 0 && len(s.columns[col])+len(g.rows)+2 > target {
			col++
		}
		rows := []helpReferenceLine{{text: g.title, heading: true, id: g.id}}
		rows = append(rows, g.rows...)
		rows = append(rows, helpReferenceLine{})
		for _, ln := range rows {
			ln.match = query != "" && strings.Contains(strings.ToLower(ln.text+" "+ln.search), query)
			s.columns[col] = append(s.columns[col], ln)
		}
	}
	if w != s.width && anchor != "" {
		for _, c := range s.columns {
			for i, ln := range c {
				if ln.id == anchor {
					s.scroll = i
					break
				}
			}
		}
	}
	s.anchored = true

	s.width = w
	s.height = h
	s.scroll = min(max(s.scroll, 0), s.maxScroll())
}
func (s *referenceScreen) maxScroll() int {
	n := 0
	for _, c := range s.columns {
		n = max(n, len(c))
	}
	return max(n-max(s.height-3, 1), 0)
}
func (s *referenceScreen) visitMatch(back bool) {
	var rows []int
	seen := map[string]bool{}
	for _, c := range s.columns {
		for i, ln := range c {
			if ln.match && !seen[ln.id] {
				seen[ln.id] = true
				rows = append(rows, i)
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	old := s.match
	if back {
		s.match--
	} else {
		s.match++
	}
	if s.match < 0 {
		s.match = len(rows) - 1
	}
	if s.match >= len(rows) {
		s.match = 0
	}
	s.scroll = min(rows[s.match], s.maxScroll())
	s.feedback = fmt.Sprintf("Match %d of %d", s.match+1, len(rows))
	if old >= 0 && ((!back && s.match == 0) || (back && s.match == len(rows)-1)) {
		s.feedback = s.theme.uiText("Wrapped · ") + s.feedback
	}
}
func (s *referenceScreen) view(a *App, w, h int) []string {
	s.theme = a.theme
	if s.showSettings {
		return s.settings.view(a, w, h)
	}
	s.layout(w, h)
	out := make([]string, h)
	if h == 0 {
		return out
	}
	out[0] = "  " + a.theme.PopupTitle.Render("Help") + "    " + a.theme.HelpLabel.Render("Settings") + "    " + a.theme.HelpKey.Render("s") + " Status details"
	if h > 1 {
		out[1] = a.theme.PopupDim.Render(fmt.Sprintf(a.theme.uiText("  Keys by screen · opened from %s · %d/%d"), s.caller, s.scroll+1, s.maxScroll()+1))
	}
	if h > 2 && (s.find || s.query != "") {
		label := "Find keys or actions"
		if s.find {
			label += " (typing)"
		}
		out[2] = "  " + label + ": [" + s.query + "]  " + s.feedback
	}
	_, cw := helpColumns(w)
	for row := 3; row < h; row++ {
		var line strings.Builder
		line.WriteString("  ")
		for col, c := range s.columns {
			txt := ""
			i := row - 3 + s.scroll
			if i < len(c) {
				ln := c[i]
				txt = ln.text
				if ln.heading {
					txt = a.theme.PopupTitle.Render(txt)
				} else if ln.alias {
					txt = a.theme.PopupDim.Render(txt)
				} else if ln.match {
					txt = a.theme.Prompt.Render(txt)
				}
			}
			line.WriteString(padTo(ansi.Truncate(txt, cw, ""), cw))
			if col < len(s.columns)-1 {
				line.WriteString("    ")
			}
		}
		out[row] = ansi.Truncate(line.String(), w, "")
	}
	for i := range out {
		out[i] = ansi.Truncate(out[i], w, "")
	}
	return settingsClip(out, w)
}

// statusReferenceScreen describes the stable counters without inventing totals.
type statusReferenceScreen struct{ scroll, total, height int }

func (*statusReferenceScreen) context(*App) Context { return CtxHelp }
func (s *statusReferenceScreen) key(a *App, m tea.KeyPressMsg, _ Binding, _ bool) tea.Cmd {
	switch m.String() {
	case "esc", "q", "ctrl+x":
		a.pop()
	case "up":
		s.scroll--
	case "down":
		s.scroll++
	case "pgup":
		s.scroll -= max(s.height-2, 1)
	case "pgdown", "ctrl+v":
		s.scroll += max(s.height-2, 1)
	case "home":
		s.scroll = 0
	case "end":
		s.scroll = max(s.total-s.height, 0)
	}
	s.scroll = min(max(s.scroll, 0), max(s.total-s.height, 0))
	return nil
}
func (s *statusReferenceScreen) mouse(_ *App, msg tea.MouseMsg) tea.Cmd {
	if m, ok := msg.(tea.MouseWheelMsg); ok {
		if m.Button == tea.MouseWheelUp {
			s.scroll -= 3
		} else if m.Button == tea.MouseWheelDown {
			s.scroll += 3
		}
		s.scroll = min(max(s.scroll, 0), max(s.total-s.height, 0))
	}
	return nil
}
func (s *statusReferenceScreen) view(a *App, w, h int) []string {
	out := make([]string, h)
	var rows []string
	add := func(text string) { rows = append(rows, helpWrap(termtext.Clean(text), max(w-4, 1))...) }
	add("Status details")
	add("")
	if a.cur != nil {
		position := a.cur.Ed.Buffer().Index().Position(a.cur.Ed.Cursor())
		add(fmt.Sprintf(a.theme.uiText("Document: %s · %s · line %d, column %d"), a.cur.Ed.Path(), a.persistence(), position.Line, position.Col))
		if b := a.ws.Base; b != nil && b.State != baseline.StateUnavailable && b.Spec != nil && !a.inModel() {
			saved := "differs from HEAD"
			if f := b.Spec.File(a.cur.Ed.Path()); f != nil && bytes.Equal(f.Source(), a.cur.Disk) {
				saved = "matches HEAD"
			}
			add("Saved version: " + saved)
		}
		add("Open buffers:")
		for _, path := range a.ws.OpenPaths() {
			if d := a.ws.Docs()[path]; d != nil {
				state := "saved"
				if d.Ed.Buffer().Dirty() {
					state = "unsaved"
				}
				add("  " + path + a.theme.uiText(" · ") + state)
			}
		}
		add("")
	}
	if a.flash != "" {
		add("Current message: " + a.flash)
	} else {
		add("Current message: none")
	}
	if a.wait != nil {
		add("Waiting: " + a.waitStatus())
	}
	if a.job != nil {
		add("Running: " + a.jobText())
		if a.job.irreversible {
			add("The operation has been dispatched and cannot be cancelled safely.")
		}
	}
	for _, screen := range a.screens {
		if ref, ok := screen.(*referenceScreen); ok && ref.settings != nil && ref.settings.probeRunning {
			add("Connection test is running with synthetic text only.")
			for _, plan := range ref.settings.probes {
				add("  " + plan.role + " / " + plan.name + a.theme.uiText(" · ") + plan.provider.Model)
			}
			add("Return to Settings and use Ctrl+C to cancel this test.")
		}
	}
	add("")
	if a.cur != nil {
		e, wr, hi, proposals, other := a.statusTotals()
		add(fmt.Sprintf(a.theme.uiText("Current-file diagnostics: %d errors · %d warnings · %d hints"), e, wr, hi))
		add(fmt.Sprintf(a.theme.uiText("Whole pending proposals: %d · includes checking and failed checks"), proposals))
		add(fmt.Sprintf("Other unsettled review units: %d", other))
		add(fmt.Sprintf(a.theme.uiText("Review awaiting: %d · pending proposals plus other units"), proposals+other))
	}
	add("")
	if c := a.side.selected(); c != nil {
		add("Selected review item: " + c.p.ID + a.theme.uiText(" · ") + groupOriginTitle(c.p, a.theme))
		if c.p.Primary != nil {
			add("Primary file: " + c.p.Primary.File)
		}
		if c.kind == cardEdit {
			add("Fix anchor: " + c.hunk.Anchor)
			add("Fix file: " + c.hunk.File)
			if why := a.cardBlocked(c); why != "" {
				add("Applicability: " + why)
			}
			add("Explanation: " + c.hunk.Explanation)
			add("Before: " + c.hunk.Find)
			add("After: " + c.hunk.Replace)
		} else if c.kind == cardProblem {
			add("Problem anchor: " + c.prob.anchor)
			for _, scope := range a.problemScopeFields(c) {
				add("Related scope: " + scope)
			}
			add("Explanation: " + c.prob.explanation)
		} else if c.kind == cardQuestion {
			add("Question: " + c.text)
		}
		if r := c.p.Rethink; r != nil {
			add("Rethink of: " + r.Of)
			for _, n := range r.Notes {
				add("Your notes: " + n.Text)
			}
		}
		if c.p.Why != "" {
			add("Rationale: " + c.p.Why)
		}
		if c.p.Summary != "" {
			add("Check summary: " + c.p.Summary)
		}
		if c.p.CheckState != "" {
			add("Check state: " + c.p.CheckState)
		}
		if c.p.CheckMessage != "" {
			add("Check detail: " + c.p.CheckMessage)
		}
		if c.p.Inference != nil {
			for _, stage := range c.p.Inference.Stages {
				add("  " + workflow.StageLine(stage))
			}
		}
		add("")
	}
	add("Child fixes and running jobs add zero. A standalone fix counts once when it has no owning problem. Diagnostic counts may overlap review findings; do not add them to awaiting work.")
	add("")
	add("Applied to buffer is unsaved. Saving files and committing saved change groups are separate outcomes.")
	add("")
	add("Esc returns to the exact caller and its reading position.")
	s.total = len(rows)
	s.height = h
	s.scroll = min(max(s.scroll, 0), max(s.total-h, 0))
	for i := 0; i < h; i++ {
		j := s.scroll + i
		if j < len(rows) {
			text := rows[j]
			if j == 0 {
				text = a.theme.PopupTitle.Render(text)
			}
			out[i] = "  " + text
		}
	}
	return settingsClip(out, w)
}
func (s *statusReferenceScreen) helperBindings(*App) []Binding {
	return []Binding{displayBinding("Esc", "Back"), displayBinding("^G", "Help"), displayBinding("PgUp", "Page up"), displayBinding("^V", "Page down"), displayBinding("Up", "Scroll up"), displayBinding("Down", "Scroll down")}
}
func (s *statusReferenceScreen) helperEnabled(_ *App, b Binding) bool {
	switch b.Label {
	case "Page up", "Scroll up":
		return s.scroll > 0
	case "Page down", "Scroll down":
		return s.scroll < max(s.total-s.height, 0)
	}
	return true
}

func (s *referenceScreen) helperBindings(a *App) []Binding {
	if s.showSettings {
		return s.settings.helperBindings(a)
	}
	if s.find {
		return []Binding{displayBinding("Ret", "Next match"), displayBinding("Esc", "Reference"), displayBinding("^W", "Find"), displayBinding("^U", "Clear"), displayBinding("PgUp", "Page up"), displayBinding("^V", "Page down"), displayBinding("Up", "Scroll up"), displayBinding("Down", "Scroll down")}
	}
	return []Binding{displayBinding("^T", "Settings"), displayBinding("Esc", "Close"), displayBinding("^W", "Find"), displayBinding("Ret", "Next match"), displayBinding("PgUp", "Page up"), displayBinding("^V", "Page down"), displayBinding("Up", "Scroll up"), displayBinding("Down", "Scroll down")}
}
func (s *referenceScreen) helperEnabled(a *App, b Binding) bool {
	if s.showSettings {
		return s.settings.helperEnabled(a, b)
	}
	switch b.Label {
	case "Next match":
		if strings.TrimSpace(s.query) == "" {
			return false
		}
		for _, c := range s.columns {
			for _, ln := range c {
				if ln.match {
					return true
				}
			}
		}
		return false
	case "Clear":
		return s.query != ""
	case "Page up", "Scroll up":
		return s.scroll > 0
	case "Page down", "Scroll down":
		return s.scroll < s.maxScroll()
	}
	return true
}

// compactHelpDescription keeps the reference scannable while dispatch retains
// the complete binding description and aliases in the shared keymap.
func compactHelpDescription(c Context, b Binding) string {
	switch b.Action {
	case ActToTarget:
		return "Follow the referenced anchor or section."
	case ActAccept:
		if c == CtxCompletion {
			return "Insert the selected name; # inserts a section mark."
		}
		if c == CtxChecks {
			return "Open the selected proposal or go to its scope."
		}
	case ActHelp:
		return "Open Help; preserve this screen and its draft."
	case ActWriteOut:
		return "Save the current document."
	case ActNextPane:
		return "Focus the next visible pane."
	case ActAcceptHunk:
		if c == CtxChecks {
			return "Apply the selected fix to the buffer · unsaved."
		}
		return "Select this hunk for application."
	case ActSkipHunk:
		if c == CtxChecks {
			return "Dismiss this problem or skip its suggested fix."
		}
		return "Skip this generated-fix hunk."
	case ActEditHunk:
		return "Edit the selected replacement before applying."
	case ActFeedback:
		if c == CtxChecks {
			return "Request a revision using your feedback; on a question, answer it and rethink the check or analysis."
		}
		return "Request a revision using your feedback."
	case ActApprove:
		return "Apply selected edits to the buffer · unsaved."
	case ActRejectAll:
		return "Reject the whole proposal with a reason."
	case ActReview:
		if c == CtxChecks {
			return "Hide Checks and return to the editor."
		}
		return "Open Checks for pending work."
	case ActCommitView:
		if c == CtxCommit {
			return "Commit the ticked saved change groups."
		}
		return "Choose saved change groups to commit."
	case ActPropose:
		return "Check the change under the cursor."
	case ActAnalyze:
		return "Analyze the current spec for consistency."
	case ActOutline:
		return "Show or focus the outline."
	}
	text := b.Desc
	if i := strings.Index(text, " ("); i >= 0 {
		text = text[:i]
	}
	if i := strings.Index(text, "; "); i >= 0 {
		text = text[:i]
	}
	if i := strings.Index(text, ". "); i >= 0 {
		text = text[:i+1]
	}
	words := strings.Fields(text)
	if len(words) > 15 {
		text = strings.Join(words[:15], " ") + "…"
	}
	if text == "" {
		return b.Label
	}
	return text
}
func (s *referenceScreen) paste(text string) {
	if s.showSettings {
		s.settings.paste(text)
		return
	}
	if !s.find {
		return
	}
	if strings.ContainsAny(text, "\r\n") {
		s.feedback = "Find accepts one line; paste was not inserted"
		return
	}
	if s.replaceQuery {
		s.query = ""
		s.replaceQuery = false
	}
	s.query += text
	s.match = -1
}
