// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestProjectSettingsReloadIncludesNewMemberAndOutline(t *testing.T) {
	root := lensdProject(t)
	if err := os.WriteFile(filepath.Join(root, "extra.spec.md"), []byte("# Extra\n\n**$Extra.Requirement.** The extra field MUST be set.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "spec.toml"), []byte("files=['lensd.spec.md']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t, testOptions(root))
	if a.ws.File("extra.spec.md") != nil {
		t.Fatal("fixture file should initially be excluded")
	}
	a.watcher = newWatcher(root, a.ws.Paths())
	old := a.watcher
	defer a.Close()
	if err := os.WriteFile(filepath.Join(root, "spec.toml"), []byte("files=['*.spec.md']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.reloadProjectSettings(); err != nil {
		t.Fatal(err)
	}
	if a.ws.File("extra.spec.md") == nil || a.ws.Spec.File("extra.spec.md") == nil {
		t.Fatal("new member is missing from parsed analysis")
	}
	if _, err := a.ws.Doc("extra.spec.md"); err != nil {
		t.Fatal("new member cannot be opened", err)
	}
	found := false
	for _, entry := range a.outline.entries {
		if entry.kind == entryFile && entry.loc.Path == "extra.spec.md" {
			found = true
		}
	}
	if !found {
		t.Fatal("outline did not include the new member")
	}
	if a.watcher == old || !a.watcher.paths["extra.spec.md"] {
		t.Fatal("watcher membership was not refreshed")
	}
}
func TestProjectSettingsExclusionRetainsDirtyDraftExitAndSaveAll(t *testing.T) {
	root := lensdProject(t)
	a := newTestApp(t, testOptions(root))
	d := a.cur
	path := d.Ed.Path()
	d.Ed.Insert("Draft prose.\n\n")
	wanted := bytes.Clone(d.Ed.Buffer().Bytes())
	cursor := d.Ed.Cursor()
	if err := os.WriteFile(filepath.Join(root, "extra.spec.md"), []byte("# Extra\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "spec.toml"), []byte("files=['extra.spec.md']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.reloadProjectSettings(); err != nil {
		t.Fatal(err)
	}
	if a.cur != d || !d.Ed.Buffer().Dirty() || d.Ed.Cursor() != cursor || !bytes.Equal(d.Ed.Buffer().Bytes(), wanted) {
		t.Fatal("membership reload changed the open draft")
	}
	if a.ws.Spec.File(path) != nil {
		t.Fatal("excluded document leaked into project spec analysis")
	}
	if !slices.Contains(a.ws.OpenPaths(), path) {
		t.Fatal("orphan draft was omitted from open paths")
	}
	a.exit()
	if a.prompt == nil || a.prompt.kind != promptSave || !strings.Contains(a.prompt.label, path) {
		t.Fatal("exit did not ask to save excluded dirty document")
	}
	a.prompt = nil
	prompted := false
	a.saveFirst("Check", func() tea.Cmd { prompted = true; return nil })
	if prompted || a.prompt == nil || a.prompt.kind != promptSaveFirst {
		t.Fatal("save-before-action lost excluded draft")
	}
	a.prompt = nil
	if !a.saveAll() {
		t.Fatal("explicit Save all failed", a.flash)
	}
	disk, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(disk, wanted) || d.Ed.Buffer().Dirty() {
		t.Fatal("Save all omitted the excluded open draft")
	}
}
func TestSettingsPasteRespectsFrozenVisibleScopes(t *testing.T) {
	s := fixtureSettings(t)
	s.section = 4
	f := settingsFieldNamed(t, s, "Project name")
	before := s.value(f)
	for _, confirm := range []string{"save", "close", "revert", "remove", "test", "result"} {
		s.confirm = confirm
		s.paste("unexpected")
		if s.value(f) != before {
			t.Fatalf("paste mutated hidden field in %s", confirm)
		}
	}
	s.confirm = ""
	s.probeRunning = true
	s.paste("unexpected")
	if s.value(f) != before {
		t.Fatal("paste mutated field during test")
	}
	s.probeRunning = false
	for _, state := range []string{"setup", "rail", "selector"} {
		s.setup = state == "setup"
		s.rail = state == "rail"
		s.selector = state == "selector"
		s.paste("unexpected")
		if s.value(f) != before {
			t.Fatalf("paste mutated hidden field in %s", state)
		}
	}
}
func TestHelpFindAcceptsLiteralBracketedPaste(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	a.openHelp()
	ref := a.top().(*referenceScreen)
	ref.find = true
	ref.replaceQuery = true
	ref.query = "old query"
	send(a, tea.PasteMsg{Content: "Ctrl+P literal text"})
	if ref.query != "Ctrl+P literal text" {
		t.Fatal("Find paste was not routed", ref.query)
	}
	send(a, tea.PasteMsg{Content: "two\nlines"})
	if ref.query != "Ctrl+P literal text" {
		t.Fatal("multiline Find paste changed the query")
	}
	ref.find = false
	send(a, tea.PasteMsg{Content: "ignored outside input"})
	if ref.query != "Ctrl+P literal text" {
		t.Fatal("reference canvas consumed paste")
	}
}
