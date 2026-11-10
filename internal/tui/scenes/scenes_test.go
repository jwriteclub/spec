// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package scenes

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/tui/shot"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if testing.Short() {
		t.Skip("scenes run the UI with git fixtures")
	}
}

var sceneName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// TestSceneRegistry: names are unique file stems, every scene says what
// it shows, and capture names are unique within a scene.
func TestSceneRegistry(t *testing.T) {
	all := All()
	assert.GreaterOrEqual(t, len(all), 15)
	seen := map[string]bool{}
	for _, sc := range all {
		assert.Regexp(t, sceneName, sc.Name)
		assert.False(t, seen[sc.Name], "duplicate scene %s", sc.Name)
		seen[sc.Name] = true
		assert.NotEmpty(t, sc.Description, sc.Name)
		caps := map[string]bool{}
		for _, c := range sc.Captures() {
			assert.False(t, caps[c], "%s: duplicate capture %q", sc.Name, c)
			caps[c] = true
			if c != "" {
				assert.Regexp(t, sceneName, c)
			}
		}
	}
}

// TestSceneRuns runs every scene at its first size, dark: each step
// reaches its wait, and every capture fills the terminal and no more.
func TestSceneRuns(t *testing.T) {
	needGit(t)
	for _, sc := range All() {
		t.Run(sc.Name, func(t *testing.T) {
			v := sc.Variants()[0]
			shots, err := Run(sc, v, Options{})
			require.NoError(t, err)
			require.Len(t, shots, len(sc.Captures()))
			for _, s := range shots {
				lines := strings.Split(s.Frame.Content, "\n")
				assert.LessOrEqual(t, len(lines), v.Size.H, "%s %q", sc.Name, s.Capture)
				for k, l := range lines {
					assert.LessOrEqual(t, ansi.StringWidth(l), v.Size.W, "%s %q line %d", sc.Name, s.Capture, k)
				}
				assert.Equal(t, v.Size.W, s.Frame.W)
			}
		})
	}
}

// TestFormalScenesEverySize: the walk from a spec to its TLA+ model reaches
// every step at every standard size, in both themes.
func TestFormalScenesEverySize(t *testing.T) {
	needGit(t)
	for _, name := range []string{"formal-links", "formal-links-256-ascii"} {
		sc, ok := Find(name)
		require.True(t, ok, name)
		for _, v := range sc.Variants() {
			shots, err := Run(sc, v, Options{})
			require.NoError(t, err, "%s %s %s", name, v.Size, v.Theme)
			require.Len(t, shots, len(sc.Captures()))
		}
	}
}

// TestSceneDeterministic: two runs of a scene write the same PNG bytes,
// for scenes with proposal ids, stage times and a running spinner.
func TestSceneDeterministic(t *testing.T) {
	needGit(t)
	for _, name := range []string{"cascade-running", "check-finding", "review"} {
		sc, ok := Find(name)
		require.True(t, ok, name)
		for _, v := range []Variant{{Size: Size140, Theme: Dark}, {Size: Size140, Theme: Light}} {
			var pngs [2][][]byte
			for run := range pngs {
				shots, err := Run(sc, v, Options{})
				require.NoError(t, err)
				for _, s := range shots {
					b, missing, err := shot.Encode(s.Frame, shot.Options{})
					require.NoError(t, err)
					assert.Empty(t, missing, "every glyph drawn")
					pngs[run] = append(pngs[run], b)
				}
			}
			require.Equal(t, len(pngs[0]), len(pngs[1]))
			for k := range pngs[0] {
				assert.True(t, string(pngs[0][k]) == string(pngs[1][k]), "%s %s capture %d differs between runs", name, v.Theme, k)
			}
		}
	}
}
