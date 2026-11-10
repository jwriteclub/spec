// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package prompt holds the system prompts of the inference checks
// (embedded, each replaceable by a file named in the user config) and
// renders the messages sent to the model.
package prompt

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/infer/specctx"
)

// Kinds of check.
const (
	Change  = "change"
	Analyze = "analyze"
	Revise  = "revise"
	// Fix is the shared system prompt of a lint fix; FixInstruction adds
	// the rule's own instruction to it.
	Fix = "fix"
	// Commit drafts the commit message in spec ui's commit view.
	Commit = "commit"
)

//go:embed prompts/*.md prompts/fix/*.md
var files embed.FS

// Embedded returns the built-in system prompt of kind, without its
// copyright comment.
func Embedded(kind string) (string, error) {
	b, err := files.ReadFile("prompts/" + kind + ".md")
	if err != nil {
		return "", fmt.Errorf("no prompt %q", kind)
	}
	return stripComments(string(b)), nil
}

// Load returns the system prompt of kind: the file at override when it is
// not "", else the embedded one.
func Load(kind, override string) (string, error) {
	if override == "" {
		return Embedded(kind)
	}
	b, err := os.ReadFile(override)
	if err != nil {
		return "", fmt.Errorf("reading the %s prompt override: %w", kind, err)
	}
	text := stripComments(string(b))
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("the %s prompt override %s is empty", kind, override)
	}
	return text, nil
}

// stripComments drops leading HTML comments and the blank lines after them.
func stripComments(s string) string {
	for {
		t := strings.TrimLeft(s, " \t\r\n")
		if !strings.HasPrefix(t, "<!--") {
			return strings.TrimLeft(s, "\r\n")
		}
		end := strings.Index(t, "-->")
		if end < 0 {
			return s
		}
		s = t[end+3:]
	}
}

// FixInstruction returns the instruction for fixing lint rule code
// (prompts/fix/CODE.md), without its copyright comment, and false when the
// rule has none.
func FixInstruction(code string) (string, bool) {
	if code == "" || strings.ContainsAny(code, "/.\\") {
		return "", false
	}
	b, err := files.ReadFile("prompts/fix/" + code + ".md")
	if err != nil {
		return "", false
	}
	return stripComments(string(b)), true
}

// FixSystem joins the shared fix prompt and the rule's instruction.
func FixSystem(shared, code, instruction string) string {
	return strings.TrimRight(shared, "\n") + "\n\n## How to fix `" + code + "`\n\n" + strings.TrimRight(instruction, "\n") + "\n"
}

// FixInput is the lint finding a fix is asked for.
type FixInput struct {
	Code     string
	Severity string
	File     string
	Line     int
	Col      int
	Message  string
	// Text is the text the finding covers; Lines are the whole lines
	// around it.
	Text  string
	Lines string
}

// FixMessages renders the messages that ask for a fix of one lint finding,
// with the spec context c.
func FixMessages(system string, in FixInput, c *specctx.Context) []infer.Message {
	var u strings.Builder
	u.WriteString("Fix this lint finding. Answer with the JSON object only.\n\n## Lint finding\n\n")
	fmt.Fprintf(&u, "code: %s\nseverity: %s\nfile: %s\nline: %d\ncolumn: %d\nmessage: %s\n\n", in.Code, in.Severity, in.File, in.Line, in.Col, strings.Join(strings.Fields(in.Message), " "))
	u.WriteString("The text it flags:\n")
	writeTag(&u, "text", in.Text)
	u.WriteString("\nThe lines it is on:\n")
	writeTag(&u, "lines", in.Lines)
	u.WriteString("\n")
	u.WriteString(c.Render())
	return []infer.Message{infer.System(system), infer.User(u.String())}
}

// writeTag writes text between <tag> and </tag> lines.
func writeTag(b *strings.Builder, tag, text string) {
	fmt.Fprintf(b, "<%s>\n%s", tag, text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "</%s>\n", tag)
}

// Messages renders the system and user messages of a check over c.
func Messages(kind, system string, c *specctx.Context) []infer.Message {
	var u strings.Builder
	switch kind {
	case Change:
		u.WriteString("Review this proposed change. Answer with the JSON object only.\n\n")
	default:
		if len(c.Regions) > 0 {
			u.WriteString("Check the edited regions against the rest of the specification. Answer with the JSON object only.\n\n")
		} else {
			u.WriteString("Check the whole specification for inconsistencies. Answer with the JSON object only.\n\n")
		}
	}
	u.WriteString(c.Render())
	return []infer.Message{infer.System(system), infer.User(u.String())}
}

// RetryMessage is appended (after the model's reply) when the reply was not
// the JSON asked for.
const RetryMessage = "Your reply was not a valid JSON object of the required shape. Answer again with only the JSON object: {\"summary\": ..., \"findings\": [...], \"questions\": [...]}."

// ReviseInput is one edit to revise.
type ReviseInput struct {
	File        string
	FileText    string
	Find        string
	Replace     string
	Explanation string
	Feedback    string
}

// ReviseMessages renders the messages that ask for one edit to be revised.
func ReviseMessages(system string, in ReviseInput) []infer.Message {
	var u strings.Builder
	u.WriteString("Revise this edit. Answer with the JSON object only.\n\n")
	fmt.Fprintf(&u, "<file path=%q>\n%s", in.File, in.FileText)
	if !strings.HasSuffix(in.FileText, "\n") {
		u.WriteString("\n")
	}
	u.WriteString("</file>\n\n")
	edit, _ := json.Marshal(Revision{Find: in.Find, Replace: in.Replace, Explanation: in.Explanation})
	fmt.Fprintf(&u, "edit: %s\n\nfeedback: %s\n", edit, in.Feedback)
	return []infer.Message{infer.System(system), infer.User(u.String())}
}

// Revision is the model's answer to ReviseMessages.
type Revision struct {
	Find        string `json:"find"`
	Replace     string `json:"replace"`
	Explanation string `json:"explanation"`
}

// CommitChange is one change going into a commit.
type CommitChange struct {
	// What names it: "inv $Frame.Timing changed", "prose in lensd.spec.md".
	What string
	// Before and After are a changed anchor's text at HEAD and now ("" for
	// an anchor added or removed).
	Before, After string
	// Words says what changed in prose, word by word ("added X",
	// "replaced Y with Z"); empty with Reformat for whitespace only.
	Words    []string
	Reformat bool
}

// CommitInput is what a commit message is drafted from.
type CommitInput struct {
	Changes []CommitChange
	// Renames maps each renamed anchor's old name to its new one, both
	// with the $ sigil; a draft may name no other rename (FalseRename).
	Renames map[string]string
	// Accepted are the reasons of the suggested fixes the author applied;
	// a draft may claim no more fixes than these (FalseFix). Findings left
	// open are not in the commit and have no place here.
	Accepted []string
}

// CommitDraft is the model's answer to CommitMessages.
type CommitDraft struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// Message joins the draft's subject and body into a commit message.
func (d CommitDraft) Message() string {
	subject := strings.Join(strings.Fields(d.Subject), " ")
	body := strings.TrimSpace(d.Body)
	if body == "" {
		return subject
	}
	return subject + "\n\n" + body
}

// renameClaims match a draft's claims that an anchor was renamed:
// "renamed $Old to $New", "renames $Old -> $New", "$Old is renamed to
// $New", with or without backquotes. The first group is the old name.
var renameClaims = []*regexp.Regexp{
	regexp.MustCompile("(?i)\\brenam\\w*\\s+(?:from\\s+)?`?(\\$[\\w.]+)`?\\s+(?:to|->|→)\\s+`?(\\$[\\w.]+)"),
	regexp.MustCompile("(?i)`?(\\$[\\w.]+)`?\\s+(?:is|was|has\\s+been|gets)\\s+renamed\\s+(?:to|as)\\s+`?(\\$[\\w.]+)"),
}

// FalseRename returns the first rename draft d claims that is not among
// in.Renames ("$A to $B"), such as an anchor renamed to itself, or "".
// The model sometimes reports a changed anchor as renamed.
func (in CommitInput) FalseRename(d CommitDraft) string {
	text := d.Subject + "\n" + d.Body
	for _, re := range renameClaims {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			from, to := strings.TrimRight(m[1], "."), strings.TrimRight(m[2], ".")
			if from == to || in.Renames[from] != to {
				return from + " to " + to
			}
		}
	}
	return ""
}

// fixClaims match a draft's body bullets that claim a fix: "- Fix ...",
// "- Fixes ...", "- Fixed ...", "* fixing ...".
var fixClaims = regexp.MustCompile(`(?im)^[ \t]*[-*][ \t]+fix(?:es|ed|ing)?\b`)

// FalseFix returns how many fixes draft d claims, in "- Fix ..." bullets,
// when that is more than the fixes the author applied (in.Accepted), else
// 0. A draft once listed "Fix ..." bullets for findings left open.
func (in CommitInput) FalseFix(d CommitDraft) int {
	n := len(fixClaims.FindAllStringIndex(d.Body, -1))
	if n > len(in.Accepted) {
		return n
	}
	return 0
}

// CommitMessages renders the messages that ask for a commit message.
func CommitMessages(system string, in CommitInput) []infer.Message {
	var u strings.Builder
	u.WriteString("Write the commit message for these changes. Answer with the JSON object only.\n\n## Changes\n")
	for _, c := range in.Changes {
		fmt.Fprintf(&u, "\n### %s\n\n", c.What)
		switch {
		case c.Before != "" || c.After != "":
			if c.Before != "" {
				u.WriteString("Before:\n")
				writeTag(&u, "before", c.Before)
			}
			if c.After != "" {
				u.WriteString("After:\n")
				writeTag(&u, "after", c.After)
			}
		case len(c.Words) > 0:
			for _, w := range c.Words {
				fmt.Fprintf(&u, "- %s\n", w)
			}
		case c.Reformat:
			u.WriteString("Whitespace and line wrapping only (reformat).\n")
		}
	}
	if len(in.Accepted) > 0 {
		u.WriteString("\n## Suggested fixes the author applied\n\n")
		for _, f := range in.Accepted {
			fmt.Fprintf(&u, "- %s\n", f)
		}
	}
	return []infer.Message{infer.System(system), infer.User(u.String())}
}
