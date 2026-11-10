// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package explain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This conservative source inventory catches new workflow/CLI error codes even
// when a command's tests do not exercise the new failure. It is not a whole-
// program reachability analysis. Dynamic forwarding is explicitly bounded.
func TestJSONErrorsCoverSourceCodes(t *testing.T) {
	doc, ok := Topic("json")
	require.True(t, ok)
	_, section, ok := strings.Cut(doc, "## Errors\n")
	require.True(t, ok)
	section, _, ok = strings.Cut(section, "\n## Shared objects")
	require.True(t, ok)
	codes := map[string]bool{}
	literal := func(expr ast.Expr) (string, bool) {
		v, ok := expr.(*ast.BasicLit)
		if !ok || v.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(v.Value)
		require.NoError(t, err)
		return value, true
	}
	selector := func(expr ast.Expr, owner, field string) bool {
		s, ok := expr.(*ast.SelectorExpr)
		if !ok || s.Sel.Name != field {
			return false
		}
		x, ok := s.X.(*ast.Ident)
		return ok && x.Name == owner
	}
	for _, pkg := range []string{"cli", "workflow"} {
		dir := filepath.Join("..", pkg)
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			fs := token.NewFileSet()
			file, err := parser.ParseFile(fs, filepath.Join(dir, name), nil, 0)
			require.NoError(t, err)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fn, func(node ast.Node) bool {
					switch n := node.(type) {
					case *ast.CallExpr:
						id, ok := n.Fun.(*ast.Ident)
						if !ok || id.Name != "fail" {
							break
						}
						require.GreaterOrEqual(t, len(n.Args), 2, fs.Position(n.Pos()))
						if code, ok := literal(n.Args[1]); ok {
							codes[code] = true
						} else {
							// fromWF forwards an Error from the inventoried workflow.
							require.True(t, pkg == "cli" && name == "inference.go" && fn.Name.Name == "fromWF" && selector(n.Args[1], "e", "Code"),
								"new dynamic error constructor needs inventory coverage: %s", fs.Position(n.Pos()))
						}
					case *ast.CompositeLit:
						typ, ok := n.Type.(*ast.Ident)
						if !ok || typ.Name != "outcome" && typ.Name != "Error" {
							break
						}
						for _, elt := range n.Elts {
							kv, ok := elt.(*ast.KeyValueExpr)
							require.True(t, ok, "positional error constructor needs inventory coverage: %s", fs.Position(n.Pos()))
							key, ok := kv.Key.(*ast.Ident)
							if !ok || key.Name != "code" && key.Name != "Code" {
								continue
							}
							if code, ok := literal(kv.Value); ok {
								codes[code] = true
							} else {
								arg, ok := kv.Value.(*ast.Ident)
								require.True(t, fn.Name.Name == "fail" && ok && arg.Name == "code", "new dynamic error field: %s", fs.Position(kv.Pos()))
							}
						}
					case *ast.AssignStmt:
						for i, lhs := range n.Lhs {
							field, ok := lhs.(*ast.SelectorExpr)
							if !ok || field.Sel.Name != "code" && field.Sel.Name != "Code" {
								continue
							}
							require.Equal(t, len(n.Lhs), len(n.Rhs), "new tuple code source: %s", fs.Position(n.Pos()))
							if code, ok := literal(n.Rhs[i]); ok {
								codes[code] = true
							} else {
								// Fix normalizes its lint-rule input, not an outcome code.
								require.True(t, pkg == "workflow" && name == "fix.go" && fn.Name.Name == "Fix" && selector(lhs, "req", "Code"),
									"new dynamic code assignment: %s", fs.Position(n.Pos()))
							}
						}
					}
					return true
				})
			}
		}
	}
	internal := map[string]string{
		"needs_model": "UI-only NoModel fix probe; CLI never sets NoModel",
		"lsp":         "LSP uses its own streaming protocol and rejects --json before dispatch",
	}
	for code := range codes {
		if internal[code] != "" {
			continue
		}
		require.Contains(t, section, "- `"+code+"`:", "error code %q needs an Errors entry", code)
	}
	for code := range internal {
		require.True(t, codes[code], "remove obsolete internal exclusion for %s", code)
	}
}
