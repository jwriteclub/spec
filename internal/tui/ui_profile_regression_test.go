// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestThemeTokenFallbackPreservesProfiles(t *testing.T) {
	customInk := paletteRole{Dark: "#123456", Light: "#abcdef", Dark256: 23, Light256: 154}
	for _, dark := range []bool{false, true} {
		for _, reduced := range []bool{false, true} {
			t.Run(fmt.Sprintf("dark=%v/reduced=%v", dark, reduced), func(t *testing.T) {
				want := func(p paletteRole) color.Color {
					if reduced {
						if dark {
							return lipgloss.Color(strconv.Itoa(p.Dark256))
						}
						return lipgloss.Color(strconv.Itoa(p.Light256))
					}
					if dark {
						return lipgloss.Color(p.Dark)
					}
					return lipgloss.Color(p.Light)
				}
				th := &Theme{Dark: dark, Reduced: reduced, scheme: &colorScheme{roles: map[string]paletteRole{"ink": customInk}}}
				require.Equal(t, want(customInk), th.Token("unknown-role"), "unknown role uses selected foreground")
				require.Equal(t, want(builtinColorSchemes[0].roles["error"]), th.Token("error"), "known missing role retains its semantics")
				th.scheme = &colorScheme{}
				require.Equal(t, want(builtinColorSchemes[0].roles["ink"]), th.Token("unknown-role"))
				th.scheme = nil
				require.Equal(t, want(builtinColorSchemes[0].roles["human"]), th.Token("human"))
				require.Equal(t, want(builtinColorSchemes[0].roles["ink"]), th.Token("unknown-role"))
			})
		}
	}
}

func TestBuiltinRolesCoverProductionTokenCalls(t *testing.T) {
	// Inspect the maps directly: rendering fallback must not hide a typo.
	checkRole := func(name, site string) {
		t.Helper()
		for _, scheme := range builtinColorSchemes {
			_, exists := scheme.roles[name]
			require.True(t, exists, "%s is missing %q used by %s", scheme.id, name, site)
		}
	}
	for _, scheme := range builtinColorSchemes[1:] {
		require.Len(t, scheme.roles, len(builtinColorSchemes[0].roles))
		for name := range builtinColorSchemes[0].roles {
			checkRole(name, "built-in keyset")
		}
	}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	literalCalls, dynamicCalls := 0, 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err)
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Token" {
				return true
			}
			require.Len(t, call.Args, 1)
			if literal, ok := call.Args[0].(*ast.BasicLit); ok && literal.Kind == token.STRING {
				name, err := strconv.Unquote(literal.Value)
				require.NoError(t, err)
				checkRole(name, fset.Position(call.Pos()).String())
				literalCalls++
			} else {
				require.Equal(t, "status.go", file, "new dynamic role callers need coverage")
				dynamicCalls++
			}
			return true
		})
	}
	require.Positive(t, literalCalls)
	require.Equal(t, 4, dynamicCalls, "update finite dynamic role coverage when adding callers")
	// statusSegments' receipt accents, rule, badges and stored backgrounds.
	for _, role := range []string{"statusPositive", "statusWarning", "statusError", "statusAgent", "statusHuman", "statusFix", "statusRule", "warning", "error", "warningCount", "proposalCount", "agent", "otherCount", "human", "status"} {
		checkRole(role, "dynamic status roles")
	}
}

func TestGeneratedStatusGlyphsRespectProfileAndWidth(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		t.Run(fmt.Sprint(ascii), func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "spec.toml", "name=\"profile fixture\"\n")
			name := strings.Repeat("long", 20) + ".spec.md"
			writeFile(t, dir, name, "# 1. Title\n\nbody\n")
			a := newTestApp(t, testOptions(dir))
			a.theme.ASCII = ascii
			tail, busy := "…", "▪ "
			if ascii {
				tail, busy = "...", "* "
			}
			for _, width := range []int{80, 100, 140, 220} {
				before := a.statusSegments(width)
				a.setFlash("%s", strings.Repeat("receipt ", 50))
				a.job = &job{label: "Checking"}
				after := a.statusSegments(width)
				require.Contains(t, ansi.Strip(after[0].text), tail)
				require.Contains(t, ansi.Strip(after[2].text), busy)
				require.Contains(t, ansi.Strip(after[2].text), tail)
				for i := range before {
					require.Equal(t, ansi.StringWidth(before[i].text), ansi.StringWidth(after[i].text))
				}
				require.Equal(t, width, ansi.StringWidth(a.statusLine(width)))
				if ascii {
					requireASCIIChrome(t, ansi.Strip(a.statusLine(width)))
				}
			}
			a.setFlash("%s", "author·name → §3…")
			require.Contains(t, ansi.Strip(a.statusLine(220)), busy+"author·name → §3…")
			shortPath := "café·→§.spec.md"
			writeFile(t, dir, shortPath, "# 1. Title\n")
			a = newTestApp(t, testOptions(dir))
			a.theme.ASCII = ascii
			require.NoError(t, a.open(Location{Path: shortPath}))
			require.Contains(t, ansi.Strip(a.statusLine(220)), shortPath)
		})
	}
}

func requireASCIIChrome(t *testing.T, text string) {
	t.Helper()
	for _, r := range text {
		require.Less(t, r, rune(128), "generated chrome: %q", text)
	}
}

func TestGeneratedNavigationGlyphsPreserveAuthoredData(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		t.Run(fmt.Sprint(ascii), func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "spec.toml", "name=\"profile fixture\"\n")
			data := "author·name → §3…"
			src := "# 1. " + data + "\n\nSee §1 and §77.\n\n# 2. Duplicate\n\n# 2. Duplicate again\n\nSee §2.\n\nplain text\n"
			writeFile(t, dir, "café.spec.md", src)
			a := newTestApp(t, testOptions(dir))
			a.theme.ASCII = ascii
			mark := "§"
			if ascii {
				mark = "S"
			}
			a.cur.Ed.SetCursor(strings.Index(src, "See §2") + len("See "))
			a.toTarget()
			require.NotNil(t, a.list)
			require.Equal(t, mark+"2 is ambiguous", a.list.title)
			a.list = nil
			a.cur.Ed.SetCursor(strings.Index(src, "§77"))
			a.toTarget()
			require.Equal(t, mark+"77 does not match any numbered section", a.flash)
			a.cur.Ed.SetCursor(strings.Index(src, "plain text") + 3)
			a.toTarget()
			require.Equal(t, "No $Name or "+mark+" reference under the cursor", a.flash)
			a.refs()
			require.Equal(t, "No $Name, "+mark+" reference, label or numbered heading under the cursor", a.flash)
			a.cur.Ed.SetCursor(0)
			a.refs()
			require.NotNil(t, a.list)
			require.Equal(t, "References to "+mark+"1 (1)", a.list.title)
			require.Equal(t, src, string(a.cur.Ed.Buffer().Bytes()))
			a.list = nil
			a.cur.Ed.SetCursor(len(src))
			send(a, typeText("#1 ")...)
			arrow := "→"
			if ascii {
				arrow = "->"
			}
			require.Equal(t, "#1 "+arrow+" "+mark+"1 (^Z undoes)", a.flash)
			require.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "§1 ")
			require.Contains(t, string(a.cur.Ed.Buffer().Bytes()), data)
		})
	}
}

func TestGeneratedDiskReceiptsPreserveUnicodePathAndError(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		t.Run(fmt.Sprint(ascii), func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "spec.toml", "name=\"profile fixture\"\n")
			name := "author·name → §3….spec.md"
			writeFile(t, dir, name, "# 1. Title\n")
			a := newTestApp(t, testOptions(dir))
			a.theme.ASCII = ascii
			a.cur.Ed.Insert("local draft ")
			a.openHelp()
			writeFile(t, dir, name, "external edit\n")
			a.onFileChanged([]string{name})
			separator := " · "
			if ascii {
				separator = " | "
			}
			require.Equal(t, "File changed"+separator+"unsaved buffer retained"+separator+name, a.flash)
			require.True(t, a.cur.Ed.Buffer().Dirty())
			require.Equal(t, CtxHelp, a.context())
			// A directory at the file path produces an ordinary OS error whose
			// path contains authored Unicode. It cannot be a snapshot conflict.
			require.NoError(t, os.Remove(filepath.Join(dir, name)))
			require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0700))
			a.save(false)
			require.Equal(t, statusFailure, a.flashKind)
			require.Contains(t, a.flash, "Save failed: "+name+separator)
			require.GreaterOrEqual(t, strings.Count(a.flash, name), 2, "path survives in both the receipt and OS error")
		})
	}
}

func TestColourSchemeDiagnosticRangesAreASCII(t *testing.T) {
	dir := t.TempDir()
	name := "café·→§.json"
	writeSchemeFixture(t, filepath.Join(dir, name), `{}`)
	catalog := loadColorSchemes(dir, "", "")
	require.Len(t, catalog.problems, 1)
	require.Contains(t, catalog.problems[0], name)
	require.Contains(t, catalog.problems[0], "1-64 characters")
	requireASCIIChrome(t, strings.TrimPrefix(catalog.problems[0], name+": "))
	for _, src := range []string{
		`{"version":1,"name":" ","roles":{"ink":{"dark256":23}}}`,
		`{"version":1,"roles":{"ink":{"dark256":256}}}`,
	} {
		_, err := decodeColorScheme([]byte(src), "custom:test", "test")
		require.Error(t, err)
		requireASCIIChrome(t, err.Error())
		require.Regexp(t, "1-64|0-255", err.Error())
	}
}
