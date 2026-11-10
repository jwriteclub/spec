// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package mdspec

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzParse checks acceptance criterion 2 for arbitrary input: ParseFile and
// Build never panic and every range is in bounds; for valid UTF-8, ranges
// fall on rune boundaries, every reference starts with $ (or §), and no
// token overlaps an opaque region (see checkFileInvariants).
func FuzzParse(f *testing.F) {
	for _, dir := range []string{"edge", "lensd", "hash"} {
		paths, _ := filepath.Glob(filepath.Join(testdata, dir, "*.spec.md"))
		for _, p := range paths {
			if b, err := os.ReadFile(p); err == nil {
				f.Add(b)
			}
		}
	}
	for _, s := range []string{
		"", "$", "§", "**", "**$Ab.Cd.**", "\xEF\xBB\xBF", "\xEF\xBB", "---\n", "+++\na\n",
		"# $Ab.Cd: t\n\n**$Ef.Gh.** x\n- y\n", "| **$Ab.Cd.** |\n|-|\n", "> **$Ab.Cd.**\nlazy $Ab.Cd\n",
		"[x](y \"$Ab.Cd\")$Ab.Cd", "§ x:1.2a §5.2ab #1.2 \\#1.2", "``` \n$Ab.Cd\n", "\t- **$Ab.Cd.**\r\n",
		"__$Ab.Cd.__\n===\n", "*[a](b)*$Ab.Cd ~~[c](d)~~", "<x>$Ab.Cd</x>", "a\r\rb $Ab.Cd\r",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		file := ParseFile("fuzz.spec.md", data)
		checkFileInvariants(t, file)
		s := Build([]*File{file, ParseFile("other.spec.md", data)})
		for _, a := range s.Anchors {
			_ = a.HashWithRenames(map[string]string{a.Name: "Renamed.Name"})
		}
		for _, d := range s.Diagnostics {
			if !d.Range.Valid(len(data)) {
				t.Errorf("diagnostic %s range %v out of bounds", d.Code, d.Range)
			}
		}
	})
}
