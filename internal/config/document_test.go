// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentSaveRetainsUneditedTablesAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	src := []byte("# saved comment\nforeground_profile = 'dark'\n[providers.local]\nbase_url='http://localhost/v1'\nmodel='custom/model'\n[providers.local.extra]\ntemperature=0.4\n[providers.local.extra.reasoning]\nmode='quiet'\n")
	if err := os.WriteFile(path, src, 0640); err != nil {
		t.Fatal(err)
	}
	d := OpenDocument(path, func(b []byte) []string {
		_, ds := Parse(path, b)
		var out []string
		for _, x := range ds {
			out = append(out, x.Message)
		}
		return out
	})
	// Add the required default in the staged draft; keep arbitrary request maps.
	d.Set("local", "default")
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	v, ok := d.Get("providers", "local", "extra", "reasoning", "mode")
	if !ok || v != "quiet" {
		t.Fatalf("nested request options lost: %#v", d.Values)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0640 {
		t.Fatal(st.Mode())
	}
	if len(d.Changes()) != 0 {
		t.Fatal("saved snapshot stays dirty")
	}
}
func TestDocumentDoesNotWriteOnOpenOrUnchangedSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "config.toml")
	d := OpenDocument(path, nil)
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing file created without changes")
	}
	existing := filepath.Join(t.TempDir(), "spec.toml")
	src := []byte("# preserve this exactly\nname='fixture'\n")
	_ = os.WriteFile(existing, src, 0600)
	d = OpenDocument(existing, nil)
	_ = d.Save()
	got, _ := os.ReadFile(existing)
	if string(got) != string(src) {
		t.Fatal("unchanged save rewrote source")
	}
}
func TestDocumentConflictKeepsDraft(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(p, []byte("a=1\n"), 0600)
	d := OpenDocument(p, nil)
	d.Set(2, "a")
	_ = os.WriteFile(p, []byte("a=3\n"), 0600)
	if err := d.Save(); err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatal(err)
	}
	v, _ := d.Get("a")
	if v != 2 {
		t.Fatal("draft discarded")
	}
	disk, _ := os.ReadFile(p)
	if string(disk) != "a=3\n" {
		t.Fatal("concurrent file overwritten")
	}
}
func TestDocumentUnknownFieldBlocksSchemaSaveWithoutDroppingData(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	src := []byte("future_setting=true\n")
	_ = os.WriteFile(p, src, 0600)
	d := OpenDocument(p, func(b []byte) []string {
		_, ds := Parse(p, b)
		var out []string
		for _, x := range ds {
			out = append(out, x.Message)
		}
		return out
	})
	d.Set("dark", "foreground_profile")
	if err := d.Save(); err == nil {
		t.Fatal("unknown key should block save")
	}
	if v, ok := d.Get("future_setting"); !ok || v != true {
		t.Fatal("unknown field lost")
	}
	disk, _ := os.ReadFile(p)
	if string(disk) != string(src) {
		t.Fatal("unsafe source rewrite")
	}
}
func TestDocumentUseDefaultAndTableRemovalCounts(t *testing.T) {
	d := NewDocument("fixture", []byte("foreground_profile='dark'\n[providers.x]\nmodel='x'\nbase_url='http://localhost'\n"), true, nil)
	d.Set(nil, "providers", "x")
	d.Set(nil, "foreground_profile")
	if got := d.Changes(); len(got) != 2 {
		t.Fatal(got)
	}
	d.Revert()
	if len(d.Changes()) != 0 {
		t.Fatal(d.Changes())
	}
}
func TestAppearanceSchema(t *testing.T) {
	for _, src := range []string{"foreground_profile='dark'\ncolor_capability='256'\nglyph_profile='ascii'\n", "foreground_profile='auto'\ncolor_capability='truecolor'\nglyph_profile='unicode'\n"} {
		_, ds := Parse("fixture", []byte(src))
		if len(ds) != 0 {
			t.Fatal(ds)
		}
	}
	for _, src := range []string{"foreground_profile='pink'", "color_capability='16'", "glyph_profile='emoji'"} {
		_, ds := Parse("fixture", []byte(src))
		if len(ds) == 0 {
			t.Fatalf("accepted %s", src)
		}
	}
}
