#!/bin/bash
# Copyright 2026 Christopher O'Connell
# SPDX-License-Identifier: Apache-2.0

# Pre-commit checks for logedit (the spec tool).
# Runs copyright checks, formatting, vet, lint and tests.
#
# Usage:
#   ./scripts/pre_commit.sh          # run all checks
#   ./scripts/pre_commit.sh --quick  # skip tests (formatting + vet + lint only)

set -e

# Run from the repository root, wherever the script is called from.
cd "$(dirname "$0")/.."

QUICK=false
if [ "$1" = "--quick" ]; then
    QUICK=true
fi

echo "Running pre-commit checks..."
echo ""

# --- 0. Clean up macOS resource fork files (._*) ---
# These are created by macOS Finder/Spotlight and break Go tooling (gofmt, go test)
# when they shadow real .go files (e.g., ._metadata.go looks like a .go file to parsers).
find . -name '._*' -not -path './vendor/*' -not -path './.git/*' -delete 2>/dev/null || true

# --- 1. Check copyright headers ---
# Every file in the repository (tracked, or new and not ignored) carries the personal
# copyright in its first lines (CLAUDE.md). Exempt: LICENSE, generated files (go.sum,
# golden outputs), testdata/ fixture bytes, the two strict-JSON palette assets,
# and the supplied upstream palette license (exact paths only).
echo "1. Checking copyright headers..."
MISSING_COPYRIGHT=()

while IFS= read -r -d '' f; do
    case "$f" in
        LICENSE | go.sum | testdata/* | */testdata/* | *.golden.json | internal/tui/design_palette.json | internal/tui/color_schemes/synthwave-quiet.json | internal/tui/color_schemes/LICENSE-SynthWave84.txt) continue ;;
    esac
    [ -f "$f" ] || continue
    if ! head -4 "$f" | grep -q "Copyright 2026 Christopher O'Connell"; then
        MISSING_COPYRIGHT+=("$f")
    fi
done < <(git ls-files -z --cached --others --exclude-standard)

if [ ${#MISSING_COPYRIGHT[@]} -gt 0 ]; then
    echo "FAIL: Missing copyright header in ${#MISSING_COPYRIGHT[@]} file(s):"
    for f in "${MISSING_COPYRIGHT[@]}"; do
        echo "  $f"
    done
    echo ""
    echo "Expected header (Go):  // Copyright 2026 Christopher O'Connell"
    echo "Expected header (sh, TOML, YAML):  # Copyright 2026 Christopher O'Connell"
    echo "Expected header (Markdown, HTML):  <!-- Copyright 2026 Christopher O'Connell -->"
    exit 1
else
    echo "OK: copyright headers present"
fi

# --- 2. Check Go formatting ---
echo ""
echo "2. Checking gofmt..."
UNFORMATTED=$(gofmt -l $(find . -name '*.go' -not -path './vendor/*' -not -path './.git/*') 2>&1 || true)

if [ -n "$UNFORMATTED" ]; then
    echo "FAIL: gofmt found issues in:"
    echo "$UNFORMATTED"
    echo ""
    echo "Run 'gofmt -w .' to fix"
    exit 1
else
    echo "OK: gofmt passed"
fi

# --- 3. Run go vet ---
echo ""
echo "3. Running go vet..."
TMP_VET=$(mktemp)
if go vet ./... > "$TMP_VET" 2>&1; then
    echo "OK: go vet passed"
    rm -f "$TMP_VET"
else
    echo "FAIL: go vet found issues:"
    cat "$TMP_VET"
    rm -f "$TMP_VET"
    exit 1
fi

# --- 4. Run golangci-lint (if available) ---
echo ""
echo "4. Running golangci-lint..."
if command -v golangci-lint &> /dev/null; then
    TMP_LINT=$(mktemp)
    if golangci-lint run ./... > "$TMP_LINT" 2>&1; then
        echo "OK: golangci-lint passed"
        rm -f "$TMP_LINT"
    else
        echo "FAIL: golangci-lint found issues:"
        cat "$TMP_LINT"
        rm -f "$TMP_LINT"
        exit 1
    fi
else
    echo "SKIP: golangci-lint not found"
    echo "  Install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
fi

# --- 5. Check for common issues ---
echo ""
echo "5. Checking for common issues..."
ISSUES=false

# Check for accidentally committed .env or credential files
if git ls-files --cached | grep -qE '\.env$|credentials\.json|\.secret'; then
    echo "FAIL: Sensitive files staged for commit"
    git ls-files --cached | grep -E '\.env$|credentials\.json|\.secret'
    ISSUES=true
fi

# Check for debug prints left in code (command output goes through io.Writers, not fmt.Print*)
DEBUG_PRINTS=$(grep -rnE 'fmt\.Print(ln|f)?\(' --include='*.go' --exclude-dir=vendor --exclude-dir=.git --exclude='*_test.go' . 2>/dev/null || true)
if [ -n "$DEBUG_PRINTS" ]; then
    echo "WARN: Found fmt.Print in non-test code (write to the command's writers instead):"
    echo "$DEBUG_PRINTS"
    # Warning only, not a hard failure
fi

if [ "$ISSUES" = true ]; then
    exit 1
else
    echo "OK: no common issues found"
fi

# --- 6. Run tests ---
if [ "$QUICK" = true ]; then
    echo ""
    echo "6. SKIP: tests (--quick mode)"
else
    echo ""
    echo "6. Running tests with race detector..."
    TMP_TEST=$(mktemp)
    if go test -race -count=1 ./... > "$TMP_TEST" 2>&1; then
        echo "OK: all tests passed"
        rm -f "$TMP_TEST"
    else
        echo "FAIL: tests failed:"
        cat "$TMP_TEST"
        rm -f "$TMP_TEST"
        exit 1
    fi
fi

# --- 7. Check go.mod tidiness ---
echo ""
echo "7. Checking go.mod..."
TMP_MOD=$(mktemp)
TMP_SUM=$(mktemp)
cp go.mod "$TMP_MOD"
cp go.sum "$TMP_SUM"
go mod tidy 2>/dev/null
if diff -q go.mod "$TMP_MOD" > /dev/null 2>&1 && diff -q go.sum "$TMP_SUM" > /dev/null 2>&1; then
    echo "OK: go.mod and go.sum are tidy"
else
    echo "WARN: go.mod or go.sum was not tidy (now fixed — re-stage them)"
fi
rm -f "$TMP_MOD" "$TMP_SUM"

# --- Summary ---
echo ""
echo "All pre-commit checks passed!"
