// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package diag defines the diagnostic type shared by the parser (mdspec), the
// linter, project loading and the git baseline. Its shape follows the
// Language Server Protocol so the language server can forward diagnostics
// without translation (ranges are byte ranges; convert them with package pos).
package diag

import (
	"sort"

	"github.com/jwriteclub/spec/internal/pos"
)

// Severity is how serious a diagnostic is.
type Severity string

// Severities, from most to least serious.
const (
	Error   Severity = "error"
	Warning Severity = "warning"
	Hint    Severity = "hint"
)

// Rank orders severities: Error 0, Warning 1, Hint 2, anything else 3.
func (s Severity) Rank() int {
	switch s {
	case Error:
		return 0
	case Warning:
		return 1
	case Hint:
		return 2
	}
	return 3
}

// Valid reports whether s is one of Error, Warning or Hint.
func (s Severity) Valid() bool {
	return s.Rank() < 3
}

// Diagnostic codes (plan section 7). The comment on each says which package
// produces it.
const (
	// Errors.
	CodeRefDangling         = "ref-dangling"          // mdspec.Build
	CodeSectionRefDangling  = "section-ref-dangling"  // mdspec.Build
	CodeSectionRefAmbiguous = "section-ref-ambiguous" // mdspec.Build
	CodeDefDuplicate        = "def-duplicate"         // mdspec.Build (and baseline)
	CodeDefMalformed        = "def-malformed"         // mdspec.ParseFile
	CodeDefNested           = "def-nested"            // mdspec.ParseFile
	CodeSectionDup          = "section-dup"           // mdspec.ParseFile
	CodeProjectNoFiles      = "project-no-files"      // project
	CodeProjectFileMissing  = "project-file-missing"  // project
	CodeFileNotUTF8         = "file-not-utf8"         // mdspec.ParseFile
	CodeFileSymlink         = "file-symlink"          // project
	CodeConfigInvalid       = "config-invalid"        // config / project
	CodeFileBOM             = "file-bom"              // mdspec.ParseFile (warning)
	CodeFileStemDuplicate   = "file-stem-duplicate"   // mdspec.Build (warning)
	CodeDefUnreferenced     = "def-unreferenced"      // lint (warning)
	CodeMathHazard          = "math-hazard"           // lint (warning; alias ref-adjacent)
	CodeAnchorLong          = "anchor-long"           // lint (warning)
	CodeCheckedByMissing    = "checked-by-missing"    // lint (warning, opt-in)
	CodeSectionHashRef      = "section-hash-ref"      // lint (hint)
	CodeSectionRefMalformed = "section-ref-malformed" // lint (hint)
	CodeRefNearly           = "ref-nearly"            // lint (hint)
	CodeRefInTitle          = "ref-in-title"          // lint (hint)
	CodeAnchorCodeAfter     = "anchor-code-after"     // lint (hint)
	CodeFormalSyntax        = "formal-syntax"         // formal (error)
	CodeFormalSemantic      = "formal-semantic"       // formal (error)
	CodeFormalRefDangling   = "formal-ref-dangling"   // formal (error)
	CodeFormalOpUncited     = "formal-op-uncited"     // formal (warning)
	CodeFormalTagMalformed  = "formal-tag-malformed"  // formal (hint)
	CodeFormalTagBlock      = "formal-tag-block"      // formal (hint)
	CodeFormalQuoteStale    = "formal-quote-stale"    // formal (hint)
	CodeFormalCheckLimit    = "formal-check-limit"    // project (hint)
	CodeSpell               = "spell"                 // spell (hint; shown as a spelling underline)
)

// Related is a secondary location attached to a diagnostic, such as the
// first definition of a duplicated name.
type Related struct {
	File    string    `json:"file"`
	Range   pos.Range `json:"range"`
	Message string    `json:"message"`
}

// Diagnostic is one finding about a file or the project. File is the
// project-relative slash path ("" for project-level findings such as
// project-no-files). Range is a half-open byte range into that file's raw
// source.
type Diagnostic struct {
	Code     string    `json:"code"`
	Severity Severity  `json:"severity"`
	File     string    `json:"file"`
	Range    pos.Range `json:"range"`
	Message  string    `json:"message"`
	Related  []Related `json:"related,omitempty"`
}

// Sort orders diagnostics by file, then start offset, then end offset, then
// severity, then code, then message. It is stable.
func Sort(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Range.Start != b.Range.Start {
			return a.Range.Start < b.Range.Start
		}
		if a.Range.End != b.Range.End {
			return a.Range.End < b.Range.End
		}
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() < b.Severity.Rank()
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
}

// Count returns how many diagnostics have severity s.
func Count(ds []Diagnostic, s Severity) int {
	n := 0
	for _, d := range ds {
		if d.Severity == s {
			n++
		}
	}
	return n
}

// HasErrors reports whether any diagnostic has severity Error.
func HasErrors(ds []Diagnostic) bool {
	return Count(ds, Error) > 0
}
