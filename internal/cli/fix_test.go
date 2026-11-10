// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/explain"
	"github.com/jwriteclub/spec/internal/proposal"
)

// The fix project: $Frame.Order has a stray $ after $Time.Media
// (math-hazard), and $Time.Wall is referenced nowhere (def-unreferenced).
const (
	orderGlued = "**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, at most 5$ apart, see $Frame.Timing.\nChecked by: TestOrder."
	orderFixed = "**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, at most `5$` apart, see $Frame.Timing.\nChecked by: TestOrder."
	wallDef    = "**Def $Time.Wall (wall time).** Wall-clock time of the host, in seconds."
	timingWall = "**$Frame.Timing (fragment timing).** Every fragment MUST have a duration above zero in $Time.Media.\nChecked by: TestFragment."
	timingCite = "**$Frame.Timing (fragment timing).** Every fragment MUST have a duration above zero in $Time.Media, not in $Time.Wall.\nChecked by: TestFragment."
)

func fixSpecText() string {
	s := strings.Replace(baseSpec, "**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, see $Frame.Timing.\nChecked by: TestOrder.", orderGlued, 1)
	return strings.Replace(s, "## 2. Rules", wallDef+"\n\n## 2. Rules", 1)
}

// fixSetup is a committed fix project with a fake provider answering
// reply (or no user config when reply is "").
func fixSetup(t *testing.T, reply string) *inferSetup {
	t.Helper()
	requireGit(t)
	dir := repoWith(t, map[string]string{"spec.toml": "name = \"demo\"\n", "demo.spec.md": fixSpecText(), "other.spec.md": otherSpec})
	env := isolatedEnv(t)
	is := &inferSetup{dir: dir, env: env}
	if reply != "" {
		is.srv = fakeProvider(t, okReply(reply))
		writeUserConfig(t, env, "default = \"fake\"\n[providers.fake]\nbase_url = \""+is.srv.URL+"\"\nmodel = \"check-model\"\n")
	}
	return is
}

// place returns FILE:LINE:COL of needle in demo.spec.md.
func place(t *testing.T, src, needle string) string {
	t.Helper()
	i := strings.Index(src, needle)
	require.GreaterOrEqual(t, i, 0, needle)
	line := strings.Count(src[:i], "\n") + 1
	col := i - strings.LastIndex(src[:i], "\n")
	return "demo.spec.md:" + strconvItoa(line) + ":" + strconvItoa(col)
}

// Mechanical, no provider configured: one hunk, approved with --commit
// byte for byte and with the trailer.
func TestFixMechanical(t *testing.T) {
	is := fixSetup(t, "")
	src := fixSpecText()
	jr := is.run(t, "fix", place(t, src, "$ apart"))
	require.Equal(t, exitPending, jr.code, jr.stdout)
	d := filedOf(t, jr)
	require.Len(t, d.Proposals, 1)
	p := d.Proposals[0]
	assert.Equal(t, proposal.KindFix, p.Kind)
	assert.Nil(t, p.Primary)
	require.NotNil(t, p.Fix)
	assert.Equal(t, "math-hazard", p.Fix.Code)
	assert.Equal(t, "mechanical", p.Fix.Class)
	assert.Nil(t, p.Inference)
	assert.Equal(t, "Fix math-hazard: put 5$ in backticks", p.Why)
	require.Len(t, p.Hunks, 1)
	assert.Equal(t, "math-hazard", p.Hunks[0].Issue)
	assert.Equal(t, "$Frame.Order", p.Hunks[0].Anchor)
	assert.Equal(t, src, readFile(t, is.dir, "demo.spec.md"), "fix never touches spec files")

	h := is.runHuman(t, "", "proposal", p.ID)
	assert.Contains(t, h.stdout, "fixes: math-hazard at demo.spec.md:")
	assert.Contains(t, h.stdout, "(mechanical)")

	jr = is.run(t, "approve", p.ID, "--commit")
	require.Equal(t, 0, jr.code, jr.stdout)
	var ad approveData
	jr.decode(t, &ad)
	assert.Equal(t, strings.Replace(src, orderGlued, orderFixed, 1), readFile(t, is.dir, "demo.spec.md"), "byte-exact")
	require.Len(t, ad.Trailers, 1)
	assert.Regexp(t, `^Spec-Change: \$Frame\.Order inv:[0-9a-f]{8} -> inv:[0-9a-f]{8}$`, ad.Trailers[0])
	msg := git(t, is.dir, "log", "-1", "--format=%B")
	assert.Contains(t, msg, "spec: fix math-hazard in demo.spec.md (proposal "+p.ID+")")
	assert.Contains(t, msg, "\nFix math-hazard: put 5$ in backticks\n")
	assert.Contains(t, msg, ad.Trailers[0])
}

// Model, with the fake provider: the request carries the rule's
// instruction and the finding; the hunk cites the name.
func TestFixModel(t *testing.T) {
	reply := modelReply{Summary: "cite $Time.Wall where durations are measured", Findings: []modelFinding{{
		File: "demo.spec.md", Anchor: "$Frame.Timing", Explanation: "the duration is not wall time",
		Find: timingWall, Replace: timingCite,
	}}}.json(t)
	is := fixSetup(t, reply)
	src := fixSpecText()
	jr := is.run(t, "fix", place(t, src, "$Time.Wall"), "--code", "def-unreferenced", "--why", "Cite the wall clock", "--by", "agent-7")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	assert.Equal(t, "model", p.Fix.Class)
	assert.Equal(t, "Cite the wall clock", p.Why)
	assert.Equal(t, "agent-7", p.Author)
	require.NotNil(t, p.Inference)
	assert.Equal(t, "check-model", p.Inference.Model)
	require.Len(t, p.Hunks, 1)
	assert.Contains(t, p.Hunks[0].Replace, "$Time.Wall")
	assert.Equal(t, "def-unreferenced", p.Hunks[0].Issue)

	bodies, _ := is.srv.requests()
	require.Len(t, bodies, 1)
	system := bodies[0]["messages"].([]any)[0].(map[string]any)["content"].(string)
	assert.Contains(t, system, "You fix one lint finding")
	assert.Contains(t, system, "## How to fix `def-unreferenced`")
	assert.Contains(t, system, "Cite the definition there")
	user := userMessage(t, is.srv, 0)
	assert.Contains(t, user, "code: def-unreferenced\n")
	assert.Contains(t, user, "<text>\n$Time.Wall\n</text>")
	assert.Contains(t, user, `<file path="demo.spec.md">`)

	jr = is.run(t, "approve", p.ID, "--commit")
	require.Equal(t, 0, jr.code, jr.stdout)
	var ad approveData
	jr.decode(t, &ad)
	assert.Equal(t, strings.Replace(src, timingWall, timingCite, 1), readFile(t, is.dir, "demo.spec.md"))
	require.Len(t, ad.Trailers, 1)
	assert.Contains(t, ad.Trailers[0], "Spec-Change: $Frame.Timing ")
	assert.Equal(t, 0, is.run(t, "lint").code)
	assert.NotContains(t, is.runHuman(t, "", "lint").stdout, "def-unreferenced", "fixed")
}

// A model reply with no edit files nothing: exit 1 no_fix, with the reply.
func TestFixModelNoEdit(t *testing.T) {
	is := fixSetup(t, modelReply{Summary: "nothing relies on it", Questions: []string{"Is $Time.Wall still needed?"}}.json(t))
	jr := is.run(t, "fix", place(t, fixSpecText(), "$Time.Wall"))
	assert.Equal(t, exitFail, jr.code, jr.stdout)
	assert.Equal(t, "no_fix", jr.errCode)
	assert.Contains(t, jr.stdout, "nothing relies on it")
	assert.Contains(t, jr.stdout, "Is $Time.Wall still needed?")
	assert.Empty(t, proposalFiles(t, is.dir))
}

// Not fixable here: exit 1 with what to do by hand; nothing is filed.
func TestFixNotFixable(t *testing.T) {
	is := fixSetup(t, "")
	write(t, is.dir, "spec.toml", "name = \"demo\"\nfiles = [\"demo.spec.md\", \"other.spec.md\", \"gone.spec.md\"]\n")
	jr := is.run(t, "fix", "spec.toml:2")
	assert.Equal(t, exitFail, jr.code, jr.stdout)
	assert.Equal(t, "not_fixable", jr.errCode)
	assert.Contains(t, jr.stdout, "a files entry in spec.toml matches nothing: correct the entry or remove it")
	assert.Empty(t, proposalFiles(t, is.dir))
}

func TestFixUsage(t *testing.T) {
	is := fixSetup(t, "")
	for _, c := range []struct {
		args      []string
		code, msg string
	}{
		{[]string{"fix", "demo.spec.md"}, "usage", "FILE:LINE"},
		{[]string{"fix", "demo.spec.md:0"}, "usage", "FILE:LINE"},
		{[]string{"fix", "demo.spec.md:x"}, "usage", "FILE:LINE"},
		{[]string{"fix", "missing.spec.md:3"}, "not_found", "no file missing.spec.md"},
		{[]string{"fix", "demo.spec.md:1"}, "not_found", "no lint finding at demo.spec.md:1"},
		{[]string{"fix", "demo.spec.md:3", "--code", "nope"}, "usage", "not a lint rule"},
		{[]string{"fix", "demo.spec.md:9", "--wait", "-1s"}, "usage", "--wait"},
	} {
		jr := is.run(t, c.args...)
		assert.Equal(t, exitFail, jr.code, "%v: %s", c.args, jr.stdout)
		assert.Equal(t, c.code, jr.errCode, "%v", c.args)
		assert.Contains(t, jr.stdout, c.msg, "%v", c.args)
	}
}

// The json topic documents spec fix, its errors and the fix object.
func TestFixDocumented(t *testing.T) {
	doc, ok := explain.Topic("json")
	require.True(t, ok)
	for _, want := range []string{"**`spec fix", "`not_fixable`", "`no_fix`", `"fix": {"code"`, "`fix`"} {
		assert.Contains(t, doc, want)
	}
	wf, ok := explain.Topic("workflow")
	require.True(t, ok)
	assert.Contains(t, wf, "spec fix ")
}
