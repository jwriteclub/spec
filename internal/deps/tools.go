// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

//go:build tools

// Package deps pins module dependencies that later packages use, so that
// `go mod tidy` keeps them in go.mod before any code imports them.
package deps

import (
	_ "github.com/bmatcuk/doublestar/v4"
	_ "github.com/pelletier/go-toml/v2"
	_ "github.com/sirupsen/logrus"
	_ "github.com/spf13/cobra"
	_ "github.com/stretchr/testify/assert"
	_ "github.com/tenta-browser/polychromatic"
	_ "github.com/yuin/goldmark"
	_ "go.abhg.dev/goldmark/frontmatter"
)
