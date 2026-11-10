// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package lint applies the lint rules of plan section 7 to a resolved
// mdspec.Spec. Rules are pure functions over a Spec. Run also passes through
// the diagnostics that mdspec and project already produced, so that the
// severity overrides from spec.toml's [lint] table apply to every code.
package lint

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jwriteclub/spec/internal/diag"
)

// Off is the pseudo-severity that disables a rule.
const Off diag.Severity = "off"

// MaxBodyLines is the longest body (in lines) that does not trigger
// anchor-long.
const MaxBodyLines = 25

// Rule describes one lint rule.
type Rule struct {
	// Code is the diagnostic code, which doubles as the rule name used in
	// spec.toml's [lint] table.
	Code string
	// Default is the severity without an override; Off for opt-in rules.
	Default diag.Severity
	// Origin is the package that emits the code ("mdspec", "project",
	// "config", "lint", "formal").
	Origin string
	// Summary is one line describing what the rule flags.
	Summary string
}

var rules = []Rule{
	{diag.CodeRefDangling, diag.Error, "mdspec", "a $Name reference with no definition"},
	{diag.CodeSectionRefDangling, diag.Error, "mdspec", "a section reference that matches no numbered section"},
	{diag.CodeSectionRefAmbiguous, diag.Error, "mdspec", "a section reference that matches several sections"},
	{diag.CodeDefDuplicate, diag.Error, "mdspec", "a name defined more than once"},
	{diag.CodeDefMalformed, diag.Error, "mdspec", "a bold label that looks like a definition but does not parse"},
	{diag.CodeDefNested, diag.Error, "mdspec", "a definition inside a list, quote, table or another anchor"},
	{diag.CodeSectionDup, diag.Error, "mdspec", "a section number used twice in one file"},
	{diag.CodeProjectNoFiles, diag.Error, "project", "the project selects no spec files"},
	{diag.CodeProjectFileMissing, diag.Error, "project", "a spec.toml files entry that matches nothing"},
	{diag.CodeFileNotUTF8, diag.Error, "mdspec", "a spec file that is not valid UTF-8"},
	{diag.CodeFileSymlink, diag.Error, "project", "a spec file that is a symbolic link"},
	{diag.CodeConfigInvalid, diag.Error, "project", "an invalid spec.toml or user configuration"},
	{diag.CodeFormalSyntax, diag.Error, "formal", "a syntax error in a formal model, as prudence parses it"},
	{diag.CodeFormalSemantic, diag.Error, "formal", "a semantic or level error in a formal model, as prudence checks it with the modules it extends"},
	{diag.CodeFormalRefDangling, diag.Error, "formal", "a tag in a formal model whose reference does not resolve"},
	{diag.CodeDefUnreferenced, diag.Warning, "lint", "a definition that no other anchor, prose or formal model tag references"},
	{diag.CodeMathHazard, diag.Warning, "lint", "a $ that closes GitHub math opened by an earlier $Name on the line"},
	{diag.CodeFileBOM, diag.Warning, "mdspec", "a file that starts with a byte order mark"},
	{diag.CodeFileStemDuplicate, diag.Warning, "mdspec", "two spec files with the same stem"},
	{diag.CodeAnchorLong, diag.Warning, "lint", "an anchor body longer than 25 lines"},
	{diag.CodeCheckedByMissing, Off, "lint", "an invariant without a \"Checked by:\" line (opt-in)"},
	{diag.CodeFormalOpUncited, diag.Warning, "formal", "an Inv_* operator of a formal model with no tag"},
	{diag.CodeSectionHashRef, diag.Hint, "lint", "#N.N text that would resolve as a section reference"},
	{diag.CodeSectionRefMalformed, diag.Hint, "lint", "a section reference followed by letters or digits, such as §5.2ab"},
	{diag.CodeRefNearly, diag.Hint, "lint", "an invalid $ name that starts like, or differs only in case from, a defined name"},
	{diag.CodeRefInTitle, diag.Hint, "lint", "a reference inside a label title, which is not hashed"},
	{diag.CodeAnchorCodeAfter, diag.Hint, "lint", "a fenced code block right after an anchor body, outside the hash"},
	{diag.CodeFormalTagMalformed, diag.Hint, "formal", "a tag in a formal model with no usable reference"},
	{diag.CodeFormalTagBlock, diag.Hint, "formal", "a tag in a (* *) comment, where quote text can end the comment"},
	{diag.CodeFormalQuoteStale, diag.Hint, "formal", "a tag's quote that is not in the text it cites"},
	{diag.CodeFormalCheckLimit, diag.Hint, "formal", "a formal model whose check stopped at the acquisition limit, so its semantic check is incomplete"},
	{diag.CodeSpell, diag.Hint, "spell", "a word the last spell check flagged as misspelled, with its fix"},
}

// Rules returns every rule, errors first, in plan order.
func Rules() []Rule {
	return append([]Rule(nil), rules...)
}

// aliases maps a rule's old name to its current one. spec.toml's [lint]
// table and `spec fix --code` accept either.
var aliases = map[string]string{
	"ref-adjacent": diag.CodeMathHazard, // renamed in step 4d
}

// Canonical returns the current name of rule code: the rule an alias
// stands for, else code itself.
func Canonical(code string) string {
	if c, ok := aliases[code]; ok {
		return c
	}
	return code
}

// Known reports whether code names a rule, by its name or an alias.
func Known(code string) bool {
	code = Canonical(code)
	for _, r := range rules {
		if r.Code == code {
			return true
		}
	}
	return false
}

// KnownCodes returns the rule names, sorted, for error messages. Aliases
// are left out.
func KnownCodes() []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Code)
	}
	sort.Strings(out)
	return out
}

// DefaultSeverity returns the default severity of code, and false when the
// code is not a rule. Opt-in rules return Off.
func DefaultSeverity(code string) (diag.Severity, bool) {
	for _, r := range rules {
		if r.Code == code {
			return r.Default, true
		}
	}
	return "", false
}

// ParseLevel parses a spec.toml [lint] value: "off", "hint", "warning" or
// "error".
func ParseLevel(s string) (diag.Severity, error) {
	switch diag.Severity(s) {
	case Off, diag.Hint, diag.Warning, diag.Error:
		return diag.Severity(s), nil
	}
	return "", fmt.Errorf("lint level %q is not one of off, hint, warning, error", s)
}

// Overrides maps a rule name to its level from spec.toml's [lint] table.
// Values are "off", "hint", "warning" or "error" (see ParseLevel).
type Overrides map[string]string

// Validate returns one error per unknown rule name or bad level, in sorted
// key order, each naming the offending key.
func (o Overrides) Validate() []error {
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var errs []error
	for _, k := range keys {
		if !Known(k) {
			errs = append(errs, fmt.Errorf("[lint] %s: unknown rule (known rules: %s)", k, strings.Join(KnownCodes(), ", ")))
			continue
		}
		if _, err := ParseLevel(o[k]); err != nil {
			errs = append(errs, fmt.Errorf("[lint] %s: %w", k, err))
		}
	}
	return errs
}

// raw returns the override of rule code, given by its name or, when that
// is absent, by an alias of it.
func (o Overrides) raw(code string) (string, bool) {
	if v, ok := o[code]; ok {
		return v, true
	}
	for alias, c := range aliases {
		if c == code {
			if v, ok := o[alias]; ok {
				return v, true
			}
		}
	}
	return "", false
}

// level returns the effective severity of code. Unknown codes and bad
// overrides fall back to the default (or the diagnostic's own severity).
func (o Overrides) level(code string) (diag.Severity, bool) {
	if v, ok := o.raw(code); ok {
		if s, err := ParseLevel(v); err == nil {
			return s, true
		}
	}
	return DefaultSeverity(code)
}
