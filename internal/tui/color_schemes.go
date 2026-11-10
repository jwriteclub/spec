// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/termtext"
)

const defaultColorScheme = "synthwave"

// The built-ins stay in the binary; opening Settings never writes scheme files.
// Include the upstream palette's license with the embedded resources.
//
//go:embed color_schemes/*.json color_schemes/LICENSE-SynthWave84.txt
var colorSchemeFiles embed.FS

type colorScheme struct {
	id, name string
	roles    map[string]paletteRole
}

var builtinColorSchemes = func() []*colorScheme {
	decode := func(id, name string, src []byte) *colorScheme {
		var p struct {
			Roles map[string]paletteRole `json:"roles"`
		}
		if err := json.Unmarshal(src, &p); err != nil {
			panic(err)
		}
		return &colorScheme{id: id, name: name, roles: p.Roles}
	}
	quiet, err := colorSchemeFiles.ReadFile("color_schemes/synthwave-quiet.json")
	if err != nil {
		panic(err)
	}
	return []*colorScheme{decode(defaultColorScheme, "Synthwave", paletteJSON), decode("synthwave-quiet", "Synthwave Quiet", quiet)}
}()

type colorSchemeCatalog struct {
	schemes  []*colorScheme
	problems []string
	invalid  map[string]error
}

func (c *colorSchemeCatalog) find(id string) (*colorScheme, error) {
	if id == "" {
		id = defaultColorScheme
	}
	for _, s := range c.schemes {
		if s.id == id {
			return s, nil
		}
	}
	if err := c.invalid[id]; err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("colour scheme %q is unavailable; custom schemes require color_scheme_dir and a valid JSON file", id)
}

var colorSchemeFileName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var colorSchemeHex = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// loadColorSchemes reads only an explicitly configured, personal directory.
// Relative paths resolve beside the user config, never against the project.
func loadColorSchemes(directory, configPath, home string) *colorSchemeCatalog {
	c := &colorSchemeCatalog{schemes: append([]*colorScheme(nil), builtinColorSchemes...), invalid: map[string]error{}}
	if directory == "" {
		return c
	}
	directory = config.PromptPath(directory, configPath, home)
	entries, err := os.ReadDir(directory)
	if err != nil {
		c.problems = append(c.problems, termtext.Clean(fmt.Sprintf("Colour scheme folder: %v", err)))
		return c
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), ".json")
		id := "custom:" + stem
		path := filepath.Join(directory, entry.Name())
		var scheme *colorScheme
		if !colorSchemeFileName.MatchString(stem) {
			err = fmt.Errorf("scheme filename must use lowercase letters, digits, hyphens or underscores (1-64 characters)")
		} else {
			scheme, err = readColorScheme(path, id, stem)
		}
		if err != nil {
			err = fmt.Errorf("%s: %w", entry.Name(), err)
			c.invalid[id] = err
			c.problems = append(c.problems, termtext.Clean(err.Error()))
			continue
		}
		c.schemes = append(c.schemes, scheme)
	}
	return c
}

type schemeRoleOverride struct {
	Dark     *string `json:"dark"`
	Light    *string `json:"light"`
	Dark256  *int    `json:"dark256"`
	Light256 *int    `json:"light256"`
}

type colorSchemeFile struct {
	Version int                           `json:"version"`
	Name    string                        `json:"name"`
	Base    string                        `json:"base"`
	Roles   map[string]schemeRoleOverride `json:"roles"`
	// Accepted when copying an entire built-in palette; these are preview
	// metadata, never a request to paint the terminal background.
	TerminalSamples map[string]string `json:"terminalSamples,omitempty"`
}

func readColorScheme(path, id, fallbackName string) (*colorScheme, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const maxBytes = 1 << 20
	src, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(src) > maxBytes {
		return nil, fmt.Errorf("scheme exceeds 1 MiB")
	}
	return decodeColorScheme(src, id, fallbackName)
}

func decodeColorScheme(src []byte, id, fallbackName string) (*colorScheme, error) {
	var p colorSchemeFile
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("expected one JSON object")
	}
	if p.Version != 1 {
		return nil, fmt.Errorf("version must be 1")
	}
	if p.Name == "" {
		p.Name = fallbackName
	}
	if !utf8.ValidString(p.Name) || utf8.RuneCountInString(p.Name) > 64 || strings.TrimSpace(p.Name) == "" || strings.ContainsFunc(p.Name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return nil, fmt.Errorf("name must be 1-64 printable characters")
	}
	if p.Base == "" {
		p.Base = defaultColorScheme
	}
	var base *colorScheme
	for _, s := range builtinColorSchemes {
		if s.id == p.Base {
			base = s
		}
	}
	if base == nil {
		return nil, fmt.Errorf("base must be synthwave or synthwave-quiet")
	}
	if len(p.Roles) == 0 {
		return nil, fmt.Errorf("roles must contain at least one colour override")
	}
	s := &colorScheme{id: id, name: p.Name, roles: maps.Clone(base.roles)}
	for name, patch := range p.Roles {
		role, ok := s.roles[name]
		if !ok {
			return nil, fmt.Errorf("unknown colour role %q", name)
		}
		if patch.Dark == nil && patch.Light == nil && patch.Dark256 == nil && patch.Light256 == nil {
			return nil, fmt.Errorf("%s: specify dark, light, dark256 or light256", name)
		}
		for _, field := range []struct {
			name     string
			value    *string
			color    *string
			fallback *int
		}{
			{"dark", patch.Dark, &role.Dark, &role.Dark256},
			{"light", patch.Light, &role.Light, &role.Light256},
		} {
			if field.value != nil {
				if !colorSchemeHex.MatchString(*field.value) {
					return nil, fmt.Errorf("%s.%s must be #RRGGBB", name, field.name)
				}
				*field.color = strings.ToLower(*field.value)
				*field.fallback = nearestScheme256(*field.value)
			}
		}
		for _, field := range []struct {
			name     string
			value    *int
			fallback *int
		}{
			{"dark256", patch.Dark256, &role.Dark256},
			{"light256", patch.Light256, &role.Light256},
		} {
			if field.value != nil {
				if *field.value < 0 || *field.value > 255 {
					return nil, fmt.Errorf("%s.%s must be 0-255", name, field.name)
				}
				*field.fallback = *field.value
			}
		}
		s.roles[name] = role
	}
	return s, nil
}

// Custom RGB overrides get an approximation when no indexed override is given.
// Avoid 0–15: users can redefine those ANSI entries in their terminal profile.
func nearestScheme256(hex string) int {
	rgb, _ := strconv.ParseUint(hex[1:], 16, 24)
	r, g, b := int(rgb>>16), int(rgb>>8&255), int(rgb&255)
	best, distance := 16, int(^uint(0)>>1)
	levels := [...]int{0, 95, 135, 175, 215, 255}
	for n := 16; n < 256; n++ {
		var rr, gg, bb int
		if n < 232 {
			k := n - 16
			rr, gg, bb = levels[k/36], levels[k/6%6], levels[k%6]
		} else {
			rr = 8 + 10*(n-232)
			gg, bb = rr, rr
		}
		d := 30*(r-rr)*(r-rr) + 59*(g-gg)*(g-gg) + 11*(b-bb)*(b-bb)
		if d < distance {
			best, distance = n, d
		}
	}
	return best
}
