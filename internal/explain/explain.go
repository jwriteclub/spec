// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package explain holds the texts printed by `spec explain`, the skeleton
// spec written by `spec init`, the suggested CLAUDE.md snippet and the page
// printed by `spec adopt`. All of them are Markdown files embedded in the
// binary.
//
// Each embedded file starts with an HTML comment carrying the copyright
// notice. The loader strips leading HTML comments and the blank lines after
// them, so users and agents see only the text itself.
package explain

import (
	"bytes"
	"embed"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

//go:embed topics/*.md skeleton.spec.md.tmpl claude-snippet.md adopt-command.md.tmpl
var files embed.FS

// names lists the topics in index order.
var names = []string{
	"method",
	"markup",
	"adopt",
	"workflow",
	"spell",
	"agent",
	"citations",
	"config",
	"integrations",
	"json",
}

// Names returns the topic names in index order. The caller owns the slice.
func Names() []string {
	out := make([]string, len(names))
	copy(out, names)
	return out
}

// Index returns the text of bare `spec explain`: one line per topic plus how
// to start.
func Index() string {
	text, err := load("topics/index.md")
	if err != nil {
		return ""
	}
	return text
}

// Topic returns the text of `spec explain NAME`. The second result is false
// when there is no such topic.
func Topic(name string) (string, bool) {
	known := false
	for _, n := range names {
		if n == name {
			known = true
			break
		}
	}
	if !known {
		return "", false
	}
	text, err := load("topics/" + name + ".md")
	if err != nil {
		return "", false
	}
	return text, true
}

// TopicSummary is one topic's line in the index.
type TopicSummary struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// summaryLine matches a topic's line in the index: `spec explain NAME  summary`.
var summaryLine = regexp.MustCompile(`(?m)^[ \t]+spec explain ([a-z]+)[ \t]+(\S.*?)[ \t]*$`)

// Summaries returns every topic with its one-line summary from the index, in
// index order. It fails when a topic has no line in the index.
func Summaries() ([]TopicSummary, error) {
	found := map[string]string{}
	for _, m := range summaryLine.FindAllStringSubmatch(Index(), -1) {
		found[m[1]] = m[2]
	}
	out := make([]TopicSummary, 0, len(names))
	for _, n := range names {
		summary, ok := found[n]
		if !ok {
			return nil, fmt.Errorf("topic %q has no line in the explain index", n)
		}
		out = append(out, TopicSummary{Name: n, Summary: summary})
	}
	return out, nil
}

// adoptData is the template input for the `spec adopt` text.
type adoptData struct {
	Lines []string
}

// Adopt returns the text of `spec adopt`: what specs are, how to start, and
// every documentation command, the topics taken from the explain index.
func Adopt() (string, error) {
	topics, err := Summaries()
	if err != nil {
		return "", err
	}
	type row struct{ cmd, what string }
	rows := []row{{"spec explain", "the index of topics"}}
	for _, t := range topics {
		rows = append(rows, row{"spec explain " + t.Name, t.Summary})
	}
	rows = append(rows, row{"spec --help", "every command"}, row{"spec COMMAND --help", "a command's flags"})
	width := 0
	for _, r := range rows {
		width = max(width, len(r.cmd))
	}
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, fmt.Sprintf("%-*s  %s", width, r.cmd, r.what))
	}
	src, err := load("adopt-command.md.tmpl")
	if err != nil {
		return "", fmt.Errorf("reading adopt template: %w", err)
	}
	tmpl, err := template.New("adopt").Option("missingkey=error").Parse(src)
	if err != nil {
		return "", fmt.Errorf("parsing adopt template: %w", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, adoptData{Lines: lines}); err != nil {
		return "", fmt.Errorf("rendering adopt template: %w", err)
	}
	return strings.TrimRight(out.String(), "\n") + "\n", nil
}

// ClaudeSnippet returns the CLAUDE.md section that `spec init` prints (and
// never writes) for the project's agent instructions.
func ClaudeSnippet() string {
	text, err := load("claude-snippet.md")
	if err != nil {
		return ""
	}
	return text
}

// ValidProjectName reports whether name can be a project name and therefore
// the stem of the skeleton file `<name>.spec.md`: an ASCII letter followed by
// letters, digits, '_' or '-'. Section qualifiers (`§name:1`) require this.
func ValidProjectName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		letter := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
		if i == 0 {
			if !letter {
				return false
			}
			continue
		}
		if !letter && !(c >= '0' && c <= '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// skeletonData is the template input for the skeleton spec.
type skeletonData struct {
	Name string
}

// Skeleton returns the skeleton spec that `spec init` writes to
// `<name>.spec.md`. It shows one definition, one invariant, numbered
// sections and every reference form, and it passes `spec lint` with no
// diagnostics. name must satisfy ValidProjectName.
func Skeleton(name string) (string, error) {
	if !ValidProjectName(name) {
		return "", fmt.Errorf("invalid project name %q: use a letter followed by letters, digits, '_' or '-'", name)
	}
	src, err := files.ReadFile("skeleton.spec.md.tmpl")
	if err != nil {
		return "", fmt.Errorf("reading skeleton template: %w", err)
	}
	tmpl, err := template.New("skeleton").Option("missingkey=error").Parse(string(src))
	if err != nil {
		return "", fmt.Errorf("parsing skeleton template: %w", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, skeletonData{Name: name}); err != nil {
		return "", fmt.Errorf("rendering skeleton template: %w", err)
	}
	return out.String(), nil
}

// load reads an embedded file and strips its copyright header.
func load(path string) (string, error) {
	b, err := files.ReadFile(path)
	if err != nil {
		return "", err
	}
	return stripHeader(string(b)), nil
}

// stripHeader removes leading HTML comments (each on its own lines) and the
// blank lines around them.
func stripHeader(s string) string {
	for {
		trimmed := strings.TrimLeft(s, " \t\r\n")
		if !strings.HasPrefix(trimmed, "<!--") {
			return trimmed
		}
		end := strings.Index(trimmed, "-->")
		if end < 0 {
			return trimmed
		}
		s = trimmed[end+len("-->"):]
	}
}
