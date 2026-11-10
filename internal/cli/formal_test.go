// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const formalSpecText = "# Queue\n\n" +
	"**$Queue.One (one turn).** There is exactly one active turn per session.\n\n" +
	"## 4.2 Mutex\n\nThe mutex is released by a deferred unlock.\n"

// formalProject writes a project with one spec and one model.
func formalProject(t *testing.T, model string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "spec.toml", "formal = [\"tla/*.tla\"]\n")
	write(t, dir, "q.spec.md", formalSpecText)
	write(t, dir, "tla/M.tla", model)
	return dir
}

func TestLintShowsModelFindings(t *testing.T) {
	model := strings.Join([]string{
		"---- MODULE M ----",
		`\* Spec: $Queue.Nope`,
		`Inv_Dangling == TRUE`,
		`Inv_Bare == TRUE`,
		`\* Spec: rc-host §10`,
		`A == 1`,
		`(* Spec: §4.2 *)`,
		`B == 1`,
		`\* Spec: §4.2 "released by a timer"`,
		`C == 1`,
		"====",
		"",
	}, "\n")
	dir := formalProject(t, model)

	r := run(t, dir, "lint")
	assert.Equal(t, exitCheck, r.code, "a dangling tag is an error")
	assert.Contains(t, r.stdout, "tla/M.tla:2:10: error: $Queue.Nope is not defined in the spec [formal-ref-dangling]")
	assert.Contains(t, r.stdout, "tla/M.tla:4:1: warning: invariant Inv_Bare cites nothing")
	assert.Contains(t, r.stdout, "[formal-tag-malformed]")
	assert.Contains(t, r.stdout, "[formal-tag-block]")
	assert.Contains(t, r.stdout, "[formal-quote-stale]")

	jr := runJSON(t, dir, "lint")
	var codes []string
	for _, d := range jr.diagnostics {
		codes = append(codes, d["code"].(string))
		assert.Equal(t, "tla/M.tla", d["file"])
		assert.Positive(t, d["line"], d["code"])
	}
	assert.ElementsMatch(t, []string{"formal-ref-dangling", "formal-op-uncited", "formal-tag-malformed", "formal-tag-block", "formal-quote-stale"}, codes)
}

// Prudence's syntax errors are lint errors, in lint and in status, placed by
// byte with multi-byte text before them on the line.
func TestLintShowsModelSyntaxErrors(t *testing.T) {
	dir := formalProject(t, "---- MODULE M ----\nA == \"café\" \\o ` \n====\n")
	r := run(t, dir, "lint")
	assert.Equal(t, exitCheck, r.code)
	assert.Contains(t, r.stdout, "tla/M.tla:2:17: error: Lexical error at line 2, column 16.", "a byte column; SANY counts characters")
	assert.Contains(t, r.stdout, "[formal-syntax]")

	jr := runJSON(t, dir, "lint")
	require.Len(t, jr.diagnostics, 1)
	assert.Equal(t, "formal-syntax", jr.diagnostics[0]["code"])
	assert.Equal(t, "error", jr.diagnostics[0]["severity"])

	s := run(t, dir, "status")
	assert.Equal(t, 4, s.code, s.stdout+s.stderr)
	assert.Contains(t, s.stdout, "lint: 1 error")

	write(t, dir, "tla/M.tla", "---- MODULE M ----\nA == \"café\"\n====\n")
	assert.Equal(t, 0, run(t, dir, "lint").code, "a clean model draws nothing")
}

func TestLintOverridesApplyToModelFindings(t *testing.T) {
	dir := formalProject(t, "---- MODULE M ----\n\\* Spec: $Queue.Nope\nA == 1\n====\n")
	write(t, dir, "spec.toml", "formal = [\"tla/*.tla\"]\n[lint]\nformal-ref-dangling = \"hint\"\n")
	r := run(t, dir, "lint")
	assert.Equal(t, 0, r.code)
	assert.Contains(t, r.stdout, "hint: $Queue.Nope")
}

func TestCheckedByMissingSatisfiedByTag(t *testing.T) {
	dir := formalProject(t, "---- MODULE M ----\n====\n")
	write(t, dir, "spec.toml", "formal = [\"tla/*.tla\"]\n[lint]\nchecked-by-missing = \"warning\"\n")
	assert.Contains(t, run(t, dir, "lint").stdout, "[checked-by-missing]")
	write(t, dir, "tla/M.tla", "---- MODULE M ----\n\\* Spec: $Queue.One\nInv_One == TRUE\n====\n")
	assert.NotContains(t, run(t, dir, "lint").stdout, "[checked-by-missing]")
}

func TestModelsStayOutOfSpecCommands(t *testing.T) {
	model := "---- MODULE M ----\n\\* Spec:   $Queue.One   \nInv_One == TRUE\n====\n"
	dir := formalProject(t, model)
	assert.Equal(t, 0, run(t, dir, "fmt", "--check").code, "fmt never claims a model")
	r := run(t, dir, "fmt")
	assert.Equal(t, 0, r.code)
	assert.NotContains(t, r.stdout, "M.tla")
	assert.Equal(t, model, readFile(t, dir, "tla/M.tla"))
	assert.NotContains(t, run(t, dir, "ls").stdout, "M.tla")
}

func TestRefsListTags(t *testing.T) {
	dir := formalProject(t, "---- MODULE M ----\n\\* Spec: $Queue.One \"exactly one\"\nInv_One == TRUE\n\\* Derived-from: §4.2\nB == 1\n====\n")
	r := run(t, dir, "refs", "Queue.One")
	assert.Contains(t, r.stdout, `tla/M.tla:2:10: \* Spec: $Queue.One "exactly one"`)
	r = run(t, dir, "refs", "4.2")
	assert.Contains(t, r.stdout, `tla/M.tla:4:18: \* Derived-from: §4.2`)
}

func TestRenameRewritesModelTags(t *testing.T) {
	model := "---- MODULE M ----\n\\* Spec: $Queue.One \"exactly one\"\nInv_One == TRUE\n\\* see $Queue.One\nA == 1\n====\n"
	dir := formalProject(t, model)

	jr := runJSON(t, dir, "rename", "Queue.One", "Queue.Single", "--dry-run")
	require.Equal(t, 0, jr.code)
	var data struct {
		Files []struct{ File string }
		Edits []struct {
			File string
			Line int
			Col  int
			Role string
		}
		LeftAlone []struct {
			File string
			Line int
			Kind string
		} `json:"left_alone"`
	}
	jr.decode(t, &data)
	var tag bool
	for _, e := range data.Edits {
		if e.Role == "tag" {
			tag = true
			assert.Equal(t, "tla/M.tla", e.File)
			assert.Equal(t, 2, e.Line)
			assert.Equal(t, 10, e.Col)
		}
	}
	assert.True(t, tag)
	require.Len(t, data.LeftAlone, 1)
	assert.Equal(t, "model_comment", data.LeftAlone[0].Kind)
	assert.Equal(t, model, readFile(t, dir, "tla/M.tla"), "a dry run writes nothing")

	r := run(t, dir, "rename", "Queue.One", "Queue.Single")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "tla/M.tla  1")
	got := readFile(t, dir, "tla/M.tla")
	assert.Contains(t, got, `\* Spec: $Queue.Single "exactly one"`)
	assert.Contains(t, got, `\* see $Queue.One`, "not a tag: reported, left alone")
	assert.Equal(t, 0, run(t, dir, "lint").code, "the tag still resolves")
}

func TestFixRefusesModelFinding(t *testing.T) {
	dir := formalProject(t, "---- MODULE M ----\n\\* Spec: $Queue.Nope\nA == 1\n====\n")
	r := run(t, dir, "fix", "tla/M.tla:2")
	assert.Equal(t, exitFail, r.code)
	assert.Contains(t, r.stderr, "formal-ref-dangling")
	assert.Contains(t, r.stderr, "correct the tag")
}

// A stem with a leading digit resolves end to end, as the ambiguity advice says.
func TestDigitFirstStemInCommands(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec.toml", "files = [\"*.md\"]\n")
	write(t, dir, "04-q.md", "# Q\n\n## 4.2 Mutex\n\nA.\n")
	write(t, dir, "05-r.md", "# R\n\n## 4.2 Other\n\nB.\n")
	write(t, dir, "c.md", "# C\n\nSee §4.2.\n")
	r := run(t, dir, "lint")
	assert.Equal(t, exitCheck, r.code)
	assert.Contains(t, r.stdout, "e.g. §04-q:4.2")
	write(t, dir, "c.md", "# C\n\nSee §04-q:4.2.\n")
	assert.Equal(t, 0, run(t, dir, "lint").code)
	r = run(t, dir, "show", "04-q:4.2")
	assert.Equal(t, 0, r.code)
	assert.Contains(t, r.stdout, "Mutex")
}

// A name only a model's tag cites is still something that references it.
func TestRefsListADanglingTag(t *testing.T) {
	dir := formalProject(t, "---- MODULE M ----\n\\* Spec: $Queue.Nope\nA == 1\n====\n")
	r := run(t, dir, "refs", "Queue.Nope")
	assert.Equal(t, exitOK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "tla/M.tla:2:10")
	assert.Contains(t, r.stdout, "[dangling]")
}

// A definition used only by a model is not unreferenced, and a mutant is no use.
func TestDefUnreferencedCountsTags(t *testing.T) {
	const spec = "# Q\n\n**Def $Lock.Holder.** The holder.\n"
	dir := formalProject(t, "---- MODULE M ----\n\\* Derived-from: $Lock.Holder\nA == 1\n====\n")
	write(t, dir, "q.spec.md", spec)
	assert.NotContains(t, run(t, dir, "lint").stdout, "[def-unreferenced]")

	dir = formalProject(t, "---- MODULE M ----\n====\n")
	write(t, dir, "q.spec.md", spec)
	write(t, dir, "tla/M_Mut1.tla", "---- MODULE M_Mut1 ----\n\\* Spec: $Lock.Holder\nA == 1\n====\n")
	assert.Contains(t, run(t, dir, "lint").stdout, "[def-unreferenced]")
}

// A model about a mutex is a model, and its invariants need a tag.
func TestMutexModelIsNotAMutant(t *testing.T) {
	dir := formalProject(t, "---- MODULE M ----\n====\n")
	write(t, dir, "tla/Queue_Mutex.tla", "---- MODULE Queue_Mutex ----\nInv_Bare == TRUE\n====\n")
	assert.Contains(t, run(t, dir, "lint").stdout, "[formal-op-uncited]")
}

// spec fix honours the place in a model as it does in a spec file: the refusal
// is about the finding asked for, whatever else the model has.
func TestFixModelFindingAtPlace(t *testing.T) {
	model := strings.Join([]string{
		"---- MODULE M ----",
		`\* Spec: $Queue.Nope`,
		`A == 1`,
		`Inv_Bare == TRUE`,
		`(* Spec: $Queue.Gone *)`,
		`B == 1`,
		"====",
		"",
	}, "\n")
	dir := formalProject(t, model)
	tagText := `(* Spec: $Queue.Gone *)`
	tagCol := strconv.Itoa(strings.Index(tagText, "Spec") + 1)
	refCol := strconv.Itoa(strings.Index(tagText, "$") + 1)

	for _, c := range []struct {
		name  string
		args  []string
		code  string
		where string
	}{
		{"the warning on its line, not the error above it", []string{"tla/M.tla:4"}, "formal-op-uncited", "add a \\* Spec: tag"},
		{"the error on its line", []string{"tla/M.tla:2"}, "formal-ref-dangling", "correct the tag"},
		{"a column on the name", []string{"tla/M.tla:4:1"}, "formal-op-uncited", "add a \\* Spec: tag"},
		{"a column on the reference takes the worst there", []string{"tla/M.tla:5:" + refCol}, "formal-ref-dangling", "correct the tag"},
		{"a column on the tag, before the reference", []string{"tla/M.tla:5:" + tagCol}, "formal-tag-block", "move the tag out"},
		{"--code picks among those on the line", []string{"tla/M.tla:5", "--code", "formal-tag-block"}, "formal-tag-block", "move the tag out"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := run(t, dir, append([]string{"fix"}, c.args...)...)
			assert.Equal(t, exitFail, r.code)
			assert.Contains(t, r.stderr, c.code+" cannot be fixed")
			assert.Contains(t, r.stderr, c.where)
			assert.Equal(t, 1, strings.Count(r.stderr, "cannot be fixed"), r.stderr)
		})
	}

	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"a line with nothing, between findings", []string{"tla/M.tla:3"}, "no lint finding at tla/M.tla:3 "},
		{"a column off the finding", []string{"tla/M.tla:4:12"}, "no lint finding at tla/M.tla:4:12 "},
		{"a line past the end", []string{"tla/M.tla:99"}, "no lint finding at tla/M.tla:99 "},
		{"--code of a finding elsewhere", []string{"tla/M.tla:2", "--code", "formal-op-uncited"}, "no formal-op-uncited finding at tla/M.tla:2 "},
	} {
		t.Run(c.name, func(t *testing.T) {
			jr := runJSON(t, dir, append([]string{"fix"}, c.args...)...)
			assert.Equal(t, exitFail, jr.code, jr.stdout)
			assert.Equal(t, "not_found", jr.errCode)
			assert.Contains(t, jr.stdout, c.want)
		})
	}
}

// A model with no findings has nothing at any place; it is still a file of the
// project, and a model that cannot be read has its finding where lint puts it.
func TestFixModelNothingToFix(t *testing.T) {
	dir := formalProject(t, "---- MODULE M ----\nA == 1\n====\n")
	jr := runJSON(t, dir, "fix", "tla/M.tla:2")
	assert.Equal(t, exitFail, jr.code, jr.stdout)
	assert.Equal(t, "not_found", jr.errCode)
	assert.Contains(t, jr.stdout, "no lint finding at tla/M.tla:2 ")
	assert.NotContains(t, jr.stdout, "not a spec file")

	dir = formalProject(t, "---- MODULE M ----\n\\* caf\xe9\nA == 1\n====\n")
	lintOut := run(t, dir, "lint").stdout
	assert.Contains(t, lintOut, "tla/M.tla:1:1:")
	r := run(t, dir, "fix", "tla/M.tla:1:1")
	assert.Equal(t, exitFail, r.code)
	assert.Contains(t, r.stderr, "formal-tag-malformed cannot be fixed")
}
