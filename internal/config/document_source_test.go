// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDocumentSourcePreservesExactUneditedBytes(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(map[string]string{"\n": "LF", "\r\n": "CRLF"}[newline], func(t *testing.T) {
			src := "# Copyright note\ndefault = 'a.b' # default note\n# connection note\n[providers.\"a.b\"] # header note\nmodel = 'old#model'  # model note\nbase_url = '''http://localhost/#path\nsecond line'''\n[providers.\"a.b\".extra]\nvalues = [\n 1, # array comment\n 2,\n]\ninline = { key = \"a#b\", nested = { keep = 'yes' } }\n"
			src = strings.ReplaceAll(src, "\n", newline)
			p := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(p, []byte(src), 0640))
			d := OpenDocument(p, nil)
			d.Set("new#model", "providers", "a.b", "model")
			require.NoError(t, d.Save())
			got, err := os.ReadFile(p)
			require.NoError(t, err)
			require.Equal(t, strings.Replace(src, "'old#model'", "'new#model'", 1), string(got))
			st, err := os.Stat(p)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0640), st.Mode().Perm())
			require.Empty(t, d.Changes())
		})
	}
}

func TestDocumentSourceInsertDeleteRenameAndInline(t *testing.T) {
	src := "# copyright\n[providers.\"old.name\"] # connection\nmodel='old' # model\n[providers.\"old.name\".extra]\ninline={ keep='yes', change=1 }\n# retained end\n"
	d := NewDocument("config.toml", []byte(src), true, nil)
	d.Set(2, "providers", "old.name", "extra", "inline", "change")
	b, err := d.Bytes()
	require.NoError(t, err)
	require.Equal(t, strings.Replace(src, "change=1", "change=2", 1), string(b))
	d.Set("old.name", "default")
	d.Set("http://localhost/v1", "providers", "old.name", "base_url")
	b, err = d.Bytes()
	require.NoError(t, err)
	require.Contains(t, string(b), "# copyright\n\"default\" = 'old.name'\n[providers.")
	require.Contains(t, string(b), "# connection")
	require.Contains(t, string(b), "# model")
	require.Contains(t, string(b), "\"base_url\" = 'http://localhost/v1'\n[providers.")
	// Rename with nested extras while preserving every comment.
	value, _ := d.Get("providers", "old.name")
	d.Set(value, "providers", "new.name")
	d.Set(nil, "providers", "old.name")
	d.Set("new.name", "default")
	b, err = d.Bytes()
	require.NoError(t, err)
	for _, comment := range []string{"# copyright", "# connection", "# model", "# retained end"} {
		require.Contains(t, string(b), comment)
	}
	parsed := NewDocument("config.toml", b, true, nil)
	require.Nil(t, parsed.LoadError)
	_, old := parsed.Get("providers", "old.name")
	require.False(t, old)
	got, _ := parsed.Get("providers", "new.name", "extra", "inline", "change")
	require.EqualValues(t, 2, got)
}

func TestDocumentSourceDefaultPreservesCommentsAndMissingNewline(t *testing.T) {
	d := NewDocument("spec.toml", []byte("# heading\nvalue = [\n 1, # nested\n 2\n] # inline\nother='keep#hash'"), true, nil)
	d.Set(nil, "value")
	b, err := d.Bytes()
	require.NoError(t, err)
	require.Contains(t, string(b), "# nested\n")
	require.Contains(t, string(b), "# inline\nother='keep#hash'")
	require.False(t, strings.HasSuffix(string(b), "\n"))
}

func TestDocumentSourceUnsupportedEditRefusesOnlyAffectedValue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	src := "simple=1\noptions={ keep='yes', remove=2 }\nvalues=[1, # retain\n2]\n"
	require.NoError(t, os.WriteFile(p, []byte(src), 0600))
	d := OpenDocument(p, nil)
	d.Set(3, "simple")
	require.NoError(t, d.Save())
	disk, err := os.ReadFile(p)
	require.NoError(t, err)
	d.Set(nil, "options", "remove")
	err = d.Save()
	require.ErrorContains(t, err, "inline-table member")
	require.ErrorContains(t, err, "TOML source")
	got, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, disk, got)
	require.NotEmpty(t, d.Changes())
	d.Revert()
	d.Set([]int{3, 4}, "values")
	err = d.Save()
	require.ErrorContains(t, err, "contains comments")
	got, err = os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, disk, got)
}

func TestDocumentSourceSafeInsertionCannotReopenInlineTable(t *testing.T) {
	d := NewDocument("config.toml", []byte("options={ keep=1 }\n"), true, nil)
	d.Set(2, "options", "new")
	_, err := d.Bytes()
	require.ErrorContains(t, err, "cannot safely edit")
	require.NotEmpty(t, d.Changes())
}

func validateSourceUserConfig(src []byte) []string {
	_, ds := Parse("config.toml", src)
	var problems []string
	for _, d := range ds {
		problems = append(problems, d.Message)
	}
	return problems
}

func TestDocumentSourceRemoveLastProvider(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, newline := range []string{"\n", "\r\n"} {
			t.Run(fmt.Sprintf("explicit=%t/CRLF=%t", explicit, newline == "\r\n"), func(t *testing.T) {
				parent := ""
				if explicit {
					parent = "[providers] # explicit parent\n"
				}
				src := "# heading\ndefault = 'a.b' # route\nforeground_profile = 'light'  # untouched\n" + parent + "[providers.\"a.b\"] # provider\nbase_url='http://localhost/v1'\nmodel='m'\n[providers.\"a.b\".extra]\nvalues=[1, # nested comment\n2]\n# end\n"
				src = strings.ReplaceAll(src, "\n", newline)
				path := filepath.Join(t.TempDir(), "config.toml")
				require.NoError(t, os.WriteFile(path, []byte(src), 0640))
				d := OpenDocument(path, validateSourceUserConfig)
				d.Set(nil, "providers", "a.b")
				d.Set(nil, "default")
				want := cloneTable(d.Values)
				require.NoError(t, d.Save())
				got, err := os.ReadFile(path)
				require.NoError(t, err)
				for _, keep := range []string{"# heading", "# route", "# provider", "# nested comment", "# end", "foreground_profile = 'light'  # untouched"} {
					require.Contains(t, string(got), keep)
				}
				if explicit {
					require.Contains(t, string(got), "[providers] # explicit parent"+newline)
				} else {
					require.Contains(t, string(got), "[\"providers\"]"+newline)
				}
				if newline == "\r\n" {
					require.NotContains(t, strings.ReplaceAll(string(got), newline, ""), "\n")
				}
				st, err := os.Stat(path)
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0640), st.Mode().Perm())
				require.Empty(t, d.Changes())
				reopened := OpenDocument(path, validateSourceUserConfig)
				require.Empty(t, reopened.Problems())
				require.True(t, sameMeaning(want, reopened.Values))
			})
		}
	}
}

func TestDocumentSourceLastProviderRetainsValidation(t *testing.T) {
	for _, route := range []string{"default = 'a'\n", "[gate]\nprovider='a'\n", "[triage]\nprovider='a'\n"} {
		t.Run(route, func(t *testing.T) {
			src := route + "[providers.a]\nbase_url='http://localhost/v1'\nmodel='m'\n"
			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(path, []byte(src), 0600))
			d := OpenDocument(path, validateSourceUserConfig)
			d.Set(nil, "providers", "a")
			require.Error(t, d.Save())
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, src, string(got))
			require.NotEmpty(t, d.Changes())
			for _, key := range []string{"default", "gate", "triage"} {
				d.Set(nil, key)
			}
			require.NoError(t, d.Save())
		})
	}
	for _, table := range []string{"[providers.empty]\n", "[unknown]\n"} {
		t.Run(table, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			src := "foreground_profile='dark'\n" + table
			require.NoError(t, os.WriteFile(path, []byte(src), 0600))
			d := OpenDocument(path, validateSourceUserConfig)
			d.Set("light", "foreground_profile")
			require.Error(t, d.Save())
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, src, string(got))
			require.NotEmpty(t, d.Changes())
		})
	}
}

func TestDocumentSourceRefusalNamesDocumentOrField(t *testing.T) {
	d := NewDocument("config.toml", []byte("options={ keep=1 }\n"), true, nil)
	d.Set(2, "options", "new")
	_, err := d.Bytes()
	require.ErrorContains(t, err, "cannot safely edit document config.toml:")
	require.NotContains(t, err.Error(), "edit  in")
	d.Revert()
	d.Set(nil, "options", "keep")
	_, err = d.Bytes()
	require.ErrorContains(t, err, `cannot safely edit "options"."keep" in config.toml:`)
}

// Two new connections in one save, and a new connection under an explicit
// [providers] header, used to repeat the [providers] header ("table
// providers already exists"), so Save refused Settings' OpenRouter setup.
func TestDocumentSourceInsertsSiblingTablesUnderExistingParent(t *testing.T) {
	for name, src := range map[string]string{
		"implicit parent": "default = 'cf'\n[providers.cf]\nmodel = 'x'\n",
		"explicit parent": "default = 'cf'\n[providers]\n[providers.cf]\nmodel = 'x'\n",
	} {
		t.Run(name, func(t *testing.T) {
			d := NewDocument("config.toml", []byte(src), true, nil)
			d.Set(map[string]any{"kind": "decisions", "model": "typesafe/jev-1.14"}, "providers", "gate.one")
			d.Set(map[string]any{"model": "z-ai/glm-5.3-flash", "extra": map[string]any{"reasoning": map[string]any{"effort": "low"}}}, "providers", "triage")
			d.Set(map[string]any{"provider": "gate.one"}, "gate")
			b, err := d.Bytes()
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(b), src), "existing bytes are kept:\n%s", b)
			again := NewDocument("config.toml", b, true, nil)
			require.True(t, sameMeaning(again.Values, d.Values), string(b))
		})
	}
}
