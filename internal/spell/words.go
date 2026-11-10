// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// WordsFile is the project word list, next to spec.toml: one word per
// line, # starts a comment. It is committed with the specs.
const WordsFile = "spec.words"

// ParseWords reads a word list: one word per line, blank lines and text
// after # ignored.
func ParseWords(src []byte) []string {
	var out []string
	// Split rather than bufio.Scanner, whose 64 KiB line limit would silently
	// drop the rest of a hand-edited list.
	for _, raw := range bytes.Split(src, []byte{'\n'}) {
		line := strings.TrimSuffix(string(raw), "\r")
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if w := strings.TrimSpace(line); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// ReadWords reads root/spec.words; a missing file is an empty list.
func ReadWords(root string) ([]string, error) {
	src, err := os.ReadFile(filepath.Join(root, WordsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseWords(src), nil
}

// ValidWord reports whether w can go in spec.words: one run of letters,
// digits, _ . - and apostrophes, with a letter.
func ValidWord(w string) bool {
	if w == "" || strings.ContainsAny(w, "#") {
		return false
	}
	letter := false
	for _, r := range w {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r), r == '_', r == '.', r == '-', r == '\'':
		default:
			return false
		}
	}
	return letter
}

// AddWord appends w to root/spec.words (creating it with a header comment)
// unless it is already there, case-insensitively. It reports whether the
// file changed.
func AddWord(root, w string) (bool, error) {
	if !ValidWord(w) {
		return false, fmt.Errorf("%q is not a word spec.words can hold (letters, digits, _ . - and ')", w)
	}
	path := filepath.Join(root, WordsFile)
	src, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	for _, have := range ParseWords(src) {
		if strings.EqualFold(have, w) {
			return false, nil
		}
	}
	var b bytes.Buffer
	if len(src) == 0 {
		b.WriteString("# Words the spell check accepts in this project, one per line (`spec explain spell`).\n")
	} else {
		b.Write(src)
		if !bytes.HasSuffix(src, []byte("\n")) {
			b.WriteByte('\n')
		}
	}
	b.WriteString(w + "\n")
	return true, os.WriteFile(path, b.Bytes(), 0o644)
}
