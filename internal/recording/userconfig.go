// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/jwriteclub/spec/internal/config"
)

// cannedKeep are the provider keys a canned copy keeps.
var cannedKeep = map[string]bool{"model": true, "triage_model": true, "triage": true}

// configKeep are the top-level keys of the user config a canned copy keeps: what
// decides the stages a session runs and how they read, and how the UI looks. Not
// [prompts] or color_scheme_dir, whose paths are files of the machine that reads
// them, and so not a custom:<name> color_scheme, which needs that directory.
var configKeep = map[string]bool{
	"default": true, "providers": true, "gate": true, "triage": true,
	"max_context_tokens": true, "max_output_tokens": true, "underline_color": true,
	"foreground_profile": true, "color_capability": true, "glyph_profile": true, "color_scheme": true,
}

// CannedConfig returns the user config of loaded for a recording's config.toml,
// every provider on the replies file CannedPathToken (replay substitutes the real
// path), so a replay runs the same stages. A config that does not exist gives an
// empty one.
func CannedConfig(loaded *config.Loaded) ([]byte, error) {
	if loaded == nil || !loaded.Exists {
		return []byte{}, nil
	}
	out, err := cannedCopy(loaded.Source, CannedPathToken)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", loaded.Path, err)
	}
	return out, nil
}

// cannedCopy rewrites a user config so that nothing in it runs a command, reaches
// a network or reads a file but replies: each provider becomes kind canned on the
// replies file, keeping model, triage_model and triage and losing everything else
// (command, base_url, api_key_env, extra, ...), and only configKeep stays. A
// custom:<name> color_scheme goes with its directory, so the replay shows
// Synthwave, as the UI does for a custom scheme it cannot load, and the rest
// of the recorded appearance still applies. A replay does this to the recording's config.toml, which is untrusted, whatever
// the recording claims to have done already.
func cannedCopy(src []byte, replies string) ([]byte, error) {
	var raw map[string]any
	if err := toml.NewDecoder(bytes.NewReader(src)).Decode(&raw); err != nil {
		return nil, err
	}
	for k := range raw {
		if !configKeep[k] {
			delete(raw, k)
		}
	}
	if scheme, ok := raw["color_scheme"].(string); ok && strings.HasPrefix(scheme, "custom:") {
		delete(raw, "color_scheme")
	}
	if v, ok := raw["providers"]; ok {
		ps, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("providers is not a table")
		}
		for name, v := range ps {
			p, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("provider %q is not a table", name)
			}
			out := map[string]any{"kind": config.KindCanned, "path": replies}
			for k, val := range p {
				if cannedKeep[k] {
					out[k] = val
				}
			}
			ps[name] = out
		}
	}
	return toml.Marshal(raw)
}
