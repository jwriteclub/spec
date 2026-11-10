// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/tui/editor"
)

const (
	formalTestSpec  = "# a\n\n## 1. One\n\n**$A.One (one).** It MUST hold.\n"
	formalTestModel = "---- MODULE M ----\n\\* Spec: $A.One \"MUST hold\"\nInv_A == TRUE\n====\n"
)

// formalProject writes a project with one spec file and one model.
func formalProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tla"), 0o755))
	for name, text := range map[string]string{
		"spec.toml": "formal = [\"tla/*.tla\"]\n", "a.spec.md": formalTestSpec, "tla/M.tla": formalTestModel,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644))
	}
	return dir
}

// TestModelStaysOutOfTheSpec: a model is a separate set: not a path of the
// spec, not parsed as Markdown, not linted as a spec file.
func TestModelStaysOutOfTheSpec(t *testing.T) {
	a := newTestApp(t, testOptions(formalProject(t)))
	require.NoError(t, a.open(Location{Path: "tla/M.tla"}))
	assert.Equal(t, []string{"a.spec.md"}, a.ws.Paths())
	assert.Nil(t, a.ws.File("tla/M.tla"))
	assert.Nil(t, a.ws.Spec.File("tla/M.tla"))
	assert.Equal(t, []string{"tla/M.tla"}, a.ws.ModelPaths())
	assert.Equal(t, formalTestModel, string(a.ws.Source("tla/M.tla")))
	assert.Equal(t, 2, a.ws.Line("tla/M.tla", strings.Index(formalTestModel, "\\*")))
	assert.Contains(t, a.ws.LineText("tla/M.tla", 3), "Inv_A")
	// checked-by-missing and def-unreferenced see the tag, as in `spec lint`.
	assert.Empty(t, a.ws.Diags)
}

// TestTargetOutsideTheProject: a .tla that formal does not select is no target.
func TestTargetOutsideTheProject(t *testing.T) {
	root := formalProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "Other.tla"), []byte(formalTestModel), 0o644))
	_, err := New(Options{Root: root, FixedTheme: true, Target: Target{File: "Other.tla"}})
	var te *TargetError
	require.ErrorAs(t, err, &te)
	assert.Contains(t, te.Msg, "not a spec file or a formal model")
	assert.NotContains(t, te.Msg, "spec ls", "spec ls lists anchors, not files")
}

// TestHelpSaysWhyAModelIsClosed: Help names the reason beside each closed key.
func TestHelpSaysWhyAModelIsClosed(t *testing.T) {
	a := newTestApp(t, testOptions(formalProject(t)))
	require.NoError(t, a.open(Location{Path: "tla/M.tla"}))
	ref := newReferenceScreen(a)
	var text strings.Builder
	for _, g := range ref.groups(100) {
		if g.title != CtxEditor.String()+a.theme.uiText(" · Current screen") {
			continue
		}
		for _, row := range g.rows {
			text.WriteString(row.text + " ")
		}
	}
	got := strings.Join(strings.Fields(text.String()), " ")
	want := "Unavailable when Help opened: " + modelClosed
	assert.GreaterOrEqual(t, strings.Count(got, want), 3, "^P, ^F and ^N: %s", got)
	assert.Contains(t, got, "Ctrl+R Open Checks for pending work. also F3 Ctrl+B", "^R stays open: a model proposal arrives as a card")

	require.NoError(t, a.open(Location{Path: "a.spec.md"}))
	ref = newReferenceScreen(a)
	assert.Empty(t, ref.callerGuards[ActFormat])
}

// TestSettingsReloadKeepsAModel: a project reload rescans a model and keeps its
// draft; one dropped from formal leaves the index but stays open and protected.
func TestSettingsReloadKeepsAModel(t *testing.T) {
	root := formalProject(t)
	a := newTestApp(t, testOptions(root))
	defer a.Close()
	require.NoError(t, a.open(Location{Path: "tla/M.tla"}))
	a.cur.Ed.SetCursor(0)
	a.cur.Ed.Insert("\\* draft\n")
	a.reparse()

	require.NoError(t, a.reloadProjectSettings())
	assert.Equal(t, []string{"tla/M.tla"}, a.ws.ModelPaths())
	assert.True(t, a.ws.Model("tla/M.tla") != nil && strings.HasPrefix(string(a.ws.Model("tla/M.tla").Source()), "\\* draft"))
	assert.Len(t, a.ws.Formal.ToAnchor("A.One"), 1)
	assert.True(t, a.cur.Ed.Buffer().Dirty())

	require.NoError(t, os.WriteFile(filepath.Join(root, "spec.toml"), []byte("name = \"a\"\n"), 0o644))
	require.NoError(t, a.reloadProjectSettings())
	assert.Empty(t, a.ws.ModelPaths())
	assert.Empty(t, a.ws.Formal.ToAnchor("A.One"))
	assert.True(t, a.inModel(), "the open draft is still a model, not parsed as Markdown")
	assert.Nil(t, a.ws.File("tla/M.tla"))
	assert.True(t, a.cur.Ed.Buffer().Dirty())
	assert.Contains(t, a.ws.OpenPaths(), "tla/M.tla", "exit still asks about it")
	assert.Contains(t, a.ws.WatchPaths(), "tla/M.tla")
	// Its hover says why it links nothing, and draws no error.
	a.cur.Ed.SetCursor(strings.Index(string(a.cur.Ed.Buffer().Bytes()), "$A.One"))
	a.reparse()
	p := a.preview(a.tokenUnder(a.cur.Ed.Cursor(), true), a.cur.Ed.Cursor())
	require.NotNil(t, p)
	assert.Contains(t, strings.Join(p.lines, " "), "not in the project's formal list")

	require.NoError(t, os.WriteFile(filepath.Join(root, "spec.toml"), []byte("formal = [\"tla/*.tla\"]\n"), 0o644))
	require.NoError(t, a.reloadProjectSettings())
	assert.Len(t, a.ws.Formal.ToAnchor("A.One"), 1)
}

// TestOddModels: an empty model and one that is not UTF-8 open and draw.
func TestOddModels(t *testing.T) {
	for name, src := range map[string]string{"empty": "", "binary": "---- MODULE M ----\n\xff\xfe\n====\n", "cut": "(* never closed"} {
		t.Run(name, func(t *testing.T) {
			root := formalProject(t)
			require.NoError(t, os.WriteFile(filepath.Join(root, "tla/M.tla"), []byte(src), 0o644))
			a := newTestApp(t, testOptions(root))
			require.NoError(t, a.open(Location{Path: "tla/M.tla"}))
			content, _ := a.render()
			assert.NotEmpty(t, content)
			assert.False(t, a.tokenUnder(0, true).any())
			a.toTarget()
			a.refs()
			a.look()
			a.outline.rebuild(a.ws)
		})
	}
}

// TestModelBuffersStayModels: a model parses as one after an edit, and its
// highlight classes hold no spec flags from a Markdown parse.
func TestModelBuffersStayModels(t *testing.T) {
	a := newTestApp(t, testOptions(formalProject(t)))
	require.NoError(t, a.open(Location{Path: "tla/M.tla"}))
	a.cur.Ed.SetCursor(len(formalTestModel))
	a.cur.Ed.Insert("# not a heading\n")
	a.reparse()
	assert.Nil(t, a.ws.File("tla/M.tla"))
	for _, sp := range a.cur.Ed.Spans() {
		assert.Zero(t, sp.Flags&(editor.FLabel|editor.FCode), "%v", sp)
	}
	_, ok := a.cur.Ed.Analysis()
	assert.True(t, ok)
	assert.False(t, a.cur.Ed.Stale())
}

// TestReloadClosesAStalePopup: a popup holds offsets from the parse that built
// it. Once the file changes on disk they point elsewhere, so ^T must not follow
// them; a notice holds none and stays.
func TestReloadClosesAStalePopup(t *testing.T) {
	root := formalProject(t)
	a := newTestApp(t, testOptions(root))
	require.NoError(t, a.open(Location{Path: "a.spec.md", Off: strings.Index(formalTestSpec, "$A.One")}))
	a.look()
	require.NotNil(t, a.popup)
	require.NotNil(t, a.popup.target)

	moved := strings.Replace(formalTestModel, "\\* Spec:", "\\* one\n\\* two\n\\* three\n\\* Spec:", 1)
	require.NoError(t, os.WriteFile(filepath.Join(root, "tla/M.tla"), []byte(moved), 0o644))
	a.onFileChanged([]string{"tla/M.tla"})
	assert.Nil(t, a.popup, "the popup's target is from before the reload")

	a.popup = &popup{notice: true, lines: []string{"a notice"}}
	require.NoError(t, os.WriteFile(filepath.Join(root, "tla/M.tla"), []byte(formalTestModel), 0o644))
	a.onFileChanged([]string{"tla/M.tla"})
	assert.NotNil(t, a.popup, "a notice has no offsets")
}

// TestHelpGuardsFitTheContext: Help says a key is closed by the model only where
// the model closes it. In the sidebar ^R and ^F still work; on other screens
// their own guards stand.
func TestHelpGuardsFitTheContext(t *testing.T) {
	a := newTestApp(t, testOptions(formalProject(t)))
	require.NoError(t, a.open(Location{Path: "tla/M.tla"}))
	a.sideShown, a.sideFocus = true, true
	ref := newReferenceScreen(a)
	require.Equal(t, CtxChecks, ref.caller)
	assert.Equal(t, modelClosed, ref.callerGuards[ActCommitView])
	assert.Empty(t, ref.callerGuards[ActReview], "^R hides the sidebar on a model")
	assert.Empty(t, ref.callerGuards[ActFormat], "^F formats the selected card on a model")

	a.sideShown, a.sideFocus = false, false
	ref = newReferenceScreen(a)
	require.Equal(t, CtxEditor, ref.caller)
	for _, action := range modelClosedActions {
		assert.Equal(t, modelClosed, ref.callerGuards[action])
	}
	assert.Empty(t, ref.callerGuards[ActReview], "^R reaches a model proposal's card from the model")
}

// TestRefsFromATaggedDeclaration: ^T lands on a declaration's name, so ^\ works
// from there, as hover and ^T do.
func TestRefsFromATaggedDeclaration(t *testing.T) {
	a := newTestApp(t, testOptions(formalProject(t)))
	require.NoError(t, a.open(Location{Path: "a.spec.md", Off: strings.Index(formalTestSpec, "$A.One")}))
	send(a, key("ctrl+t"))
	require.Equal(t, Location{Path: "tla/M.tla", Off: strings.Index(formalTestModel, "Inv_A")}, a.here())
	send(a, key("ctrl+\\"))
	require.NotNil(t, a.list, "flash: %s", a.flash)
	assert.Equal(t, "References to $A.One (1)", a.list.title)
}

// TestModelPopupsUseTheProfile: what a popup generates about a tag follows the
// ASCII profile, and a Derived-from line wraps instead of being cut.
func TestModelPopupsUseTheProfile(t *testing.T) {
	root := formalProject(t)
	title := strings.TrimSpace(strings.Repeat("Very long section title ", 8))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.spec.md"), []byte("# a\n\n## 1. "+title+"\n\n## 2. Two\n\n## 2. Again\n"), 0o644))
	src := "---- MODULE M ----\n\\* Derived-from: §1\n\\* Spec: §2\n\\* Spec: §9.9\n\\* Spec: rc-host §1\nInv == 1\n====\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "tla/M.tla"), []byte(src), 0o644))
	opts := testOptions(root)
	opts.ASCIIGlyphs = true
	a := newTestApp(t, opts)
	require.NoError(t, a.open(Location{Path: "tla/M.tla"}))
	m := a.ws.Model("tla/M.tla")
	require.Len(t, m.Tags, 4)

	var lines []string
	for _, tag := range m.Tags {
		lines = append(lines, a.tagLines(m, tag, true)...)
		lines = append(lines, a.derivedLine(tag))
		// Where ^T says it cannot go.
		a.followSpec(tokenAt{tag: tag})
		a.list = nil
		lines = append(lines, a.flash)
		a.cur = a.ws.Docs()["tla/M.tla"]
	}
	for _, l := range lines {
		assert.NotContains(t, ansi.Strip(l), "§", "generated text in the ASCII profile")
	}

	p := &popup{}
	a.declPreview(p, m, m.Decls[0])
	joined := ansi.Strip(strings.Join(p.lines, "\n"))
	assert.Contains(t, joined, "S1")
	for _, l := range p.lines {
		assert.LessOrEqual(t, ansi.StringWidth(l), a.popupWidth(), "%q", ansi.Strip(l))
	}
}

// recoverTestModel has a declaration after the one the tests break.
const recoverTestModel = "---- MODULE M ----\n\\* Spec: $A.One \"MUST hold\"\nInv_A == TRUE\nNext == TRUE\n\\* Spec: $A.One \"MUST hold\"\nLater == TRUE\n====\n"

// modelRows returns the outline's declaration rows of tla/M.tla.
func modelRows(a *App) []outlineEntry {
	var out []outlineEntry
	for _, e := range a.outline.entries {
		if e.kind == entryDecl && e.loc.Path == "tla/M.tla" {
			out = append(out, e)
		}
	}
	return out
}

// TestSyntaxErrorKeepsLaterDeclarations: a parenthesis typed into Next stops
// the parse there, but Later stays in the outline, at its moved place, and
// still answers lookups and jumps; backspace leaves the plain parse.
func TestSyntaxErrorKeepsLaterDeclarations(t *testing.T) {
	root := formalProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "tla", "M.tla"), []byte(recoverTestModel), 0o644))
	a := newTestApp(t, testOptions(root))
	require.NoError(t, a.open(Location{Path: "tla/M.tla"}))
	require.Len(t, modelRows(a), 3)

	a.cur.Ed.SetCursor(strings.Index(recoverTestModel, "TRUE\nNext") + len("TRUE\nNext == "))
	send(a, typeText("((")...)
	m := a.ws.Model("tla/M.tla")
	require.True(t, m.HasError())
	src := string(a.ws.Source("tla/M.tla"))
	rows := modelRows(a)
	require.Len(t, rows, 3)
	later := rows[2]
	assert.Equal(t, "Later", later.text)
	assert.False(t, later.stale)
	assert.Equal(t, strings.Index(src, "Later =="), later.loc.Off, "moved by the two bytes typed")
	assert.True(t, later.tagged, "its tag is still bound")
	d := m.DeclAt(later.loc.Off)
	require.NotNil(t, d)
	assert.Equal(t, "Later", d.Name)

	send(a, key("backspace"), key("backspace"))
	assert.False(t, a.ws.Model("tla/M.tla").HasError())
	assert.Empty(t, a.ws.Model("tla/M.tla").Stale)
	assert.Len(t, modelRows(a), 3)
}

// TestStaleDeclarationIsNoTarget: an edit into Later's own name leaves its
// place unknown: the outline shows it dimmed, and selecting or clicking it
// goes nowhere, nor does a lookup at its old place find it.
func TestStaleDeclarationIsNoTarget(t *testing.T) {
	root := formalProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "tla", "M.tla"), []byte(recoverTestModel), 0o644))
	a := newTestApp(t, testOptions(root))
	require.NoError(t, a.open(Location{Path: "tla/M.tla"}))

	a.cur.Ed.SetCursor(strings.Index(recoverTestModel, "Later") + len("La"))
	send(a, typeText("(")...)
	m := a.ws.Model("tla/M.tla")
	require.True(t, m.HasError())
	require.Len(t, m.Stale, 1)
	assert.Equal(t, "Later", m.Stale[0].Name)
	for _, d := range m.Decls {
		assert.NotEqual(t, "Later", d.Name)
	}
	for _, tg := range m.Tags {
		if tg.Decl != nil {
			assert.NotEqual(t, "Later", tg.Decl.Name, "no tag binds to a stale declaration")
		}
	}

	rows := modelRows(a)
	require.Len(t, rows, 3)
	assert.Equal(t, "Later", rows[2].text)
	assert.True(t, rows[2].stale)
	for k, e := range a.outline.entries {
		if e.stale {
			a.outline.sel = k
			_, ok := a.outline.selected()
			assert.False(t, ok, "Enter on a stale row jumps nowhere")
			a.outline.scroll = k
			_, ok = a.outline.click(0)
			assert.False(t, ok, "a click on a stale row jumps nowhere")
		}
	}
	before := a.cur.Ed.Cursor()
	a.outline.selectNear(Location{Path: "tla/M.tla", Off: len(recoverTestModel)})
	assert.False(t, a.outline.entries[a.outline.sel].stale, "selectNear passes over a stale row")
	assert.Equal(t, before, a.cur.Ed.Cursor())
	assert.Contains(t, strings.Join(a.outline.view(a, 40, len(a.outline.entries)), "\n"), "Later")
}
