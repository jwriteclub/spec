// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package project finds a spec project (the directory holding spec.toml),
// reads its configuration, selects its spec files, loads them into an
// mdspec.Spec (with optional unsaved-buffer overrides), and implements the
// .spec/lock writer lock.
package project

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/pelletier/go-toml/v2"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/lint"
	"github.com/jwriteclub/spec/internal/pos"
)

// ConfigFile is the name of the project configuration file.
const ConfigFile = "spec.toml"

// DefaultGlob is the file pattern used when spec.toml has no files entry.
const DefaultGlob = "**/*.spec.md"

// Config is the parsed spec.toml.
type Config struct {
	// Name is the project name; empty means "use the directory name".
	Name string `toml:"name"`
	// Files are doublestar globs relative to the spec.toml directory. Empty
	// means DefaultGlob. Either way the directory exclusions apply.
	Files []string `toml:"files"`
	// Formal are doublestar globs selecting formal models, such as
	// "tla/*.tla". They are a separate set: a model is never a spec file.
	Formal []string `toml:"formal"`
	// Lint holds rule = "off"|"hint"|"warning"|"error" overrides.
	Lint lint.Overrides `toml:"lint"`
	// Format configures `spec fmt` and ^F in `spec ui`.
	Format FormatConfig `toml:"format"`
	// TLA configures the TLA+ model check (prudence).
	TLA TLAConfig `toml:"tla"`

	// Inference is present only to detect a misplaced [inference] table.
	Inference map[string]any `toml:"inference"`
}

// FormatConfig is spec.toml's [format] table.
type FormatConfig struct {
	// Width is the column paragraphs wrap at; 0 means the default (100).
	Width int `toml:"width"`
}

// TLAConfig is spec.toml's [tla] table.
type TLAConfig struct {
	// MaxAcquisitionVisits is prudence's per-pass budget of visits while it
	// acquires a model and the modules it loads; nil means prudence's
	// default. A model too large for it is checked incomplete.
	// --max-acquisition-visits overrides it.
	MaxAcquisitionVisits *int `toml:"max_acquisition_visits"`
}

// CheckLimits returns the bounds spec.toml sets on model checks.
func (c Config) CheckLimits() formal.CheckLimits {
	var l formal.CheckLimits
	if v := c.TLA.MaxAcquisitionVisits; v != nil && *v > 0 {
		l.MaxAcquisitionVisits = *v
	}
	return l
}

// ParseConfig parses the text of a spec.toml. Problems become config-invalid
// diagnostics (File is ConfigFile, ranges index src); the returned Config
// holds whatever could be read, so a caller can still lint with it. A TOML
// syntax or type error yields a zero Config.
func ParseConfig(src []byte) (Config, []diag.Diagnostic) {
	var cfg Config
	var ds []diag.Diagnostic
	index := pos.NewIndex(src)
	bad := func(r pos.Range, format string, args ...any) {
		ds = append(ds, diag.Diagnostic{
			Code: diag.CodeConfigInvalid, Severity: diag.Error, File: ConfigFile,
			Range: r, Message: fmt.Sprintf(format, args...),
		})
	}
	// at converts a TOML row/column to a range covering length bytes.
	at := func(row, col, length int) pos.Range {
		off, ok := index.Offset(pos.Position{Line: row, Col: col})
		if !ok {
			return pos.Range{}
		}
		end := off + length
		if end > len(src) {
			end = len(src)
		}
		return pos.Range{Start: off, End: end}
	}

	dec := toml.NewDecoder(bytes.NewReader(src)).DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		var strict *toml.StrictMissingError
		var derr *toml.DecodeError
		switch {
		case errors.As(err, &strict):
			for _, e := range strict.Errors {
				row, col := e.Position()
				key := e.Key()
				name := strings.Join(key, ".")
				length := 0
				if len(key) > 0 {
					length = len(key[len(key)-1])
				}
				bad(at(row, col, length), "unknown key %q in %s (known keys: name, files, formal, lint, format, tla)", name, ConfigFile)
			}
		case errors.As(err, &derr):
			row, col := derr.Position()
			name := strings.Join(derr.Key(), ".")
			if name != "" {
				bad(at(row, col, 0), "%s: %s", name, strings.TrimPrefix(derr.Error(), "toml: "))
			} else {
				bad(at(row, col, 0), "%s", strings.TrimPrefix(derr.Error(), "toml: "))
			}
			return Config{}, ds
		default:
			bad(pos.Range{}, "%v", err)
			return Config{}, ds
		}
	}

	if cfg.Inference != nil {
		bad(findHeader(src, "inference"), "[inference] is not allowed in %s: provider settings live in ~/.config/spec/config.toml", ConfigFile)
	}
	for _, p := range cfg.Files {
		if msg := badPattern(p); msg != "" {
			bad(findString(src, p), "files entry %q: %s", p, msg)
		}
	}
	for _, p := range cfg.Formal {
		if msg := badPattern(p); msg != "" {
			bad(findString(src, p), "formal entry %q: %s", p, msg)
		}
	}
	keys := make([]string, 0, len(cfg.Lint))
	for k := range cfg.Lint {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := findKeyInTable(src, "lint", k)
		if !lint.Known(k) {
			bad(r, "[lint] %s: unknown rule (known rules: %s)", k, strings.Join(lint.KnownCodes(), ", "))
			continue
		}
		if _, err := lint.ParseLevel(cfg.Lint[k]); err != nil {
			bad(r, "[lint] %s: %v", k, err)
		}
	}
	if cfg.Format.Width < 0 {
		bad(findKeyInTable(src, "format", "width"), "[format] width must be a positive number of columns, not %d", cfg.Format.Width)
		cfg.Format.Width = 0
	}
	if v := cfg.TLA.MaxAcquisitionVisits; v != nil && *v < 1 {
		bad(findKeyInTable(src, "tla", "max_acquisition_visits"), "[tla] max_acquisition_visits must be a positive number of visits, not %d", *v)
		cfg.TLA.MaxAcquisitionVisits = nil
	}
	return cfg, ds
}

// badPattern returns why a files entry is unusable, or "".
func badPattern(p string) string {
	switch {
	case strings.TrimSpace(p) == "":
		return "is empty"
	case strings.Contains(p, "\\"):
		return "uses a backslash; write paths with forward slashes"
	case strings.HasPrefix(p, "/") || (len(p) > 1 && p[1] == ':'):
		return "is absolute; paths are relative to the spec.toml directory"
	case !doublestar.ValidatePattern(p):
		return "is not a valid glob pattern"
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "escapes the project root"
	}
	return ""
}

// findString returns the range of the first quoted occurrence of s in src.
func findString(src []byte, s string) pos.Range {
	for _, q := range []string{`"`, `'`} {
		if i := bytes.Index(src, []byte(q+s+q)); i >= 0 {
			return pos.Range{Start: i + 1, End: i + 1 + len(s)}
		}
	}
	return pos.Range{}
}

// findHeader returns the range of "[name" (table header) in src.
func findHeader(src []byte, name string) pos.Range {
	h := []byte("[" + name)
	for off := 0; off < len(src); {
		i := bytes.Index(src[off:], h)
		if i < 0 {
			break
		}
		start := off + i
		if start == 0 || src[start-1] == '\n' {
			return pos.Range{Start: start, End: start + len(h)}
		}
		off = start + 1
	}
	return pos.Range{}
}

// findKeyInTable returns the range of key inside the [table] section, or the
// table header when the key cannot be found.
func findKeyInTable(src []byte, table, key string) pos.Range {
	hdr := findHeader(src, table)
	if hdr.End == 0 {
		return pos.Range{}
	}
	rest := src[hdr.End:]
	end := len(rest)
	// The table ends at the next line starting with '['.
	if i := bytes.Index(rest, []byte("\n[")); i >= 0 {
		end = i
	}
	section := rest[:end]
	for _, form := range []string{key, `"` + key + `"`, `'` + key + `'`} {
		off := 0
		for off < len(section) {
			i := bytes.Index(section[off:], []byte(form))
			if i < 0 {
				break
			}
			s := off + i
			lineStart := bytes.LastIndexByte(section[:s], '\n') + 1
			if len(bytes.TrimSpace(section[lineStart:s])) == 0 {
				abs := hdr.End + s
				if len(form) > len(key) {
					abs++
				}
				return pos.Range{Start: abs, End: abs + len(key)}
			}
			off = s + 1
		}
	}
	return hdr
}
