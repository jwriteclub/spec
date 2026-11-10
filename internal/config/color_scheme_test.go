// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestColourSchemeConfigurationIsPersonalAndOptIn(t *testing.T) {
	for _, source := range []string{
		"", "color_scheme='synthwave'", "color_scheme='synthwave-quiet'",
		"color_scheme_dir='colours'",
		"color_scheme='custom:mine'\ncolor_scheme_dir='colours'",
		"color_scheme='custom:my-colours_2'\ncolor_scheme_dir='~/.config/spec/colours'",
	} {
		_, ds := Parse("user.toml", []byte(source))
		require.Empty(t, ds, source)
	}
	for _, source := range []string{
		"color_scheme='unknown'", "color_scheme='custom:mine'",
		"color_scheme='custom:../project'\ncolor_scheme_dir='colours'",
		"color_scheme='custom:Bad Name'\ncolor_scheme_dir='colours'",
		"color_scheme_dir='''\x00'''",
	} {
		_, ds := Parse("user.toml", []byte(source))
		require.NotEmpty(t, ds, source)
	}
	c, ds := Parse("user.toml", []byte("color_scheme='custom:mine'\ncolor_scheme_dir='colours'"))
	require.Empty(t, ds)
	require.Equal(t, "custom:mine", c.ColorScheme)
	require.Equal(t, "colours", c.ColorSchemeDir)
}
