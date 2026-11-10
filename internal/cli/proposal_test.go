// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
	"github.com/jwriteclub/spec/internal/spell"
)

// modelReply is a canned check result.
type modelReply struct {
	Summary   string         `json:"summary"`
	Findings  []modelFinding `json:"findings"`
	Questions []string       `json:"questions"`
}

type modelFinding struct {
	File        string `json:"file"`
	Anchor      string `json:"anchor,omitempty"`
	Issue       string `json:"issue"`
	Severity    string `json:"severity"`
	Explanation string `json:"explanation"`
	Find        string `json:"find,omitempty"`
	Replace     string `json:"replace,omitempty"`
}

func (m modelReply) json(t *testing.T) string {
	t.Helper()
	if m.Findings == nil {
		m.Findings = []modelFinding{}
	}
	if m.Questions == nil {
		m.Questions = []string{}
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

// inferSetup is a demo project wired to a fake provider.
type inferSetup struct {
	dir string
	env testEnv
	srv *providerServer
	// spellDict replaces the system word list when set.
	spellDict *spell.Dictionary
}

// setupInfer makes a committed demo project and a user config whose
// default provider is a fake that answers every request with reply.
func setupInfer(t *testing.T, reply http.HandlerFunc) *inferSetup {
	t.Helper()
	dir := demoProject(t)
	env := isolatedEnv(t)
	srv := fakeProvider(t, reply)
	writeUserConfig(t, env, "default = \"fake\"\n[providers.fake]\nbase_url = \""+srv.URL+"\"\nmodel = \"check-model\"\n")
	return &inferSetup{dir: dir, env: env, srv: srv}
}

// fastEnv is an Env with backoff and the wait poll shortened.
func (is *inferSetup) fastEnv(stdout, stderr *bytes.Buffer, stdin string) Env {
	return Env{
		Stdout: stdout, Stderr: stderr, Stdin: strings.NewReader(stdin), Cwd: is.dir, Getenv: is.env.get,
		tuneClient: func(c *infer.Client) { c.BaseDelay = time.Millisecond; c.MaxDelay = time.Millisecond },
		waitPoll:   20 * time.Millisecond,
		spellDict:  is.spellDict,
	}
}

func (is *inferSetup) run(t *testing.T, args ...string) jsonResult {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(append(append([]string{}, args...), "--json"), is.fastEnv(&out, &errb, ""))
	return decodeEnvelope(t, result{code: code, stdout: out.String(), stderr: errb.String()}, args)
}

func (is *inferSetup) runHuman(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, is.fastEnv(&out, &errb, stdin))
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func filedOf(t *testing.T, jr jsonResult) filedData {
	t.Helper()
	var d filedData
	jr.decode(t, &d)
	return d
}

func proposalFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(dir, ".spec", "proposals"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// userMessage returns the user message of the i-th request.
func userMessage(t *testing.T, srv *providerServer, i int) string {
	t.Helper()
	bodies, _ := srv.requests()
	require.Greater(t, len(bodies), i)
	msgs := bodies[i]["messages"].([]any)
	return msgs[len(msgs)-1].(map[string]any)["content"].(string)
}

const timingOld = "**$Frame.Timing (fragment timing).** Every fragment MUST have a duration above zero in $Time.Media.\nChecked by: TestFragment."
const timingNew = "**$Frame.Timing (fragment timing).** Every fragment MUST have a duration of at most 5 s in $Time.Media.\nChecked by: TestFragment."

// proseFix is a finding hunk on the prose paragraph of demo.spec.md.
var proseFix = modelFinding{
	File: "demo.spec.md", Issue: "missing_update", Severity: "inconsistent", Explanation: "mention the bound",
	Find: "Prose that mentions $Frame.Order for the reader.", Replace: "Prose that mentions $Frame.Order and the 5 s bound for the reader.",
}

func TestChangeApproveCommit(t *testing.T) {
	requireGit(t)
	reply := modelReply{Summary: "consistent; prose should mention the bound", Findings: []modelFinding{proseFix}}
	is := setupInfer(t, okReply(reply.json(t)))
	write(t, is.dir, "body.md", timingNew+"\n")

	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "bound fragment length", "--by", "agent-7")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	assert.Equal(t, "pending", jr.errCode)
	d := filedOf(t, jr)
	require.Len(t, d.Proposals, 1)
	p := d.Proposals[0]
	assert.Equal(t, "agent-7", p.Author)
	assert.Equal(t, proposal.KindChange, p.Kind)
	require.NotNil(t, p.Primary)
	assert.Equal(t, timingOld, p.Primary.OldText)
	assert.Equal(t, timingNew, p.Primary.NewText)
	require.Len(t, p.Hunks, 1)
	assert.Equal(t, 2, p.Hunks[0].N)
	assert.Equal(t, []string{"spec", "proposal", p.ID, "--wait", "30m"}, d.Next.Argv)
	assert.Equal(t, baseSpec, readFile(t, is.dir, "demo.spec.md"), "change never touches spec files")
	assert.Equal(t, "check-model", p.Inference.Model)
	assert.Equal(t, 12, p.Inference.PromptTokens)

	// The check saw the change, its neighborhood and the prose.
	msg := userMessage(t, is.srv, 0)
	assert.Contains(t, msg, "action: change\nanchor: $Frame.Timing")
	assert.Contains(t, msg, "<text>\n"+timingNew+"\n</text>")
	assert.Contains(t, msg, "### $Frame.Order (inv, demo.spec.md:")
	assert.Contains(t, msg, "### $Time.Media (def, demo.spec.md:")
	bodies, _ := is.srv.requests()
	assert.Contains(t, bodies[0]["messages"].([]any)[0].(map[string]any)["content"], "Never edit the changed anchor itself")

	// Listing and showing.
	jr = is.run(t, "proposals")
	require.Equal(t, 0, jr.code)
	var pl proposalsData
	jr.decode(t, &pl)
	require.Len(t, pl.Proposals, 1)
	assert.Equal(t, "$Frame.Timing", pl.Proposals[0].Anchor)
	assert.Equal(t, 2, pl.Proposals[0].Hunks)
	h := is.runHuman(t, "", "proposal", p.ID)
	assert.Equal(t, 0, h.code)
	assert.Contains(t, h.stdout, "[1] change $Frame.Timing in demo.spec.md")
	assert.Contains(t, h.stdout, "      + "+strings.Split(timingNew, "\n")[0])

	jr = is.run(t, "approve", p.ID, "--commit")
	require.Equal(t, 0, jr.code, jr.stdout)
	var ad approveData
	jr.decode(t, &ad)
	assert.Equal(t, proposal.StatusApplied, ad.Proposal.Status)
	assert.Equal(t, []int{1, 2}, ad.Proposal.Decision.Applied)
	require.Len(t, ad.Trailers, 1)
	assert.Regexp(t, `^Spec-Change: \$Frame\.Timing inv:[0-9a-f]{8} -> inv:[0-9a-f]{8}$`, ad.Trailers[0])
	assert.NotEmpty(t, ad.Commit)

	want := strings.Replace(strings.Replace(baseSpec, timingOld, timingNew, 1), proseFix.Find, proseFix.Replace, 1)
	assert.Equal(t, want, readFile(t, is.dir, "demo.spec.md"), "byte-exact result")
	assert.Equal(t, otherSpec, readFile(t, is.dir, "other.spec.md"))

	msgText := git(t, is.dir, "log", "-1", "--format=%B")
	assert.Contains(t, msgText, "spec: change $Frame.Timing (proposal "+p.ID+")")
	assert.Contains(t, msgText, "bound fragment length")
	assert.Contains(t, msgText, ad.Trailers[0])
	assert.Equal(t, ad.Commit, strings.TrimSpace(git(t, is.dir, "rev-parse", "HEAD")))
	assert.Empty(t, strings.TrimSpace(git(t, is.dir, "status", "--porcelain", "--", "demo.spec.md")))

	// The commit-msg check passes on that commit: replay it in the index.
	git(t, is.dir, "reset", "-q", "--soft", "HEAD~1")
	write(t, is.dir, "msg.txt", msgText)
	jr = is.run(t, "check", "--message-file", "msg.txt")
	assert.Equal(t, 0, jr.code, jr.stdout)

	// The receipt stays until acknowledged.
	jr = is.run(t, "proposal", p.ID, "--ack")
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Empty(t, proposalFiles(t, is.dir))
	jr = is.run(t, "proposal", p.ID)
	assert.Equal(t, "not_found", jr.errCode)
}

// runAsync starts spec in a goroutine and returns a channel with its
// result.
func (is *inferSetup) runAsync(args ...string) <-chan result {
	ch := make(chan result, 1)
	go func() {
		var out, errb bytes.Buffer
		code := Run(append(append([]string{}, args...), "--json"), is.fastEnv(&out, &errb, ""))
		ch <- result{code: code, stdout: out.String(), stderr: errb.String()}
	}()
	return ch
}

// waitForProposal polls until a proposal file exists and returns its id.
func waitForProposal(t *testing.T, dir string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range proposalFiles(t, dir) {
			if strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, ".") {
				return strings.TrimSuffix(n, ".json")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no proposal appeared")
	return ""
}

func TestRejectWakesWaitingAgent(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "fine"}.json(t)))
	write(t, is.dir, "body.md", timingNew)

	done := is.runAsync("change", "Frame.Timing", "--body-file", "body.md", "--why", "w", "--by", "agent", "--wait", "20s")
	id := waitForProposal(t, is.dir)
	jr := is.run(t, "reject", id, "--why", "5 s is too short for keyframe gaps")
	require.Equal(t, 0, jr.code, jr.stdout)

	var r result
	select {
	case r = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the waiting change did not return")
	}
	wjr := decodeEnvelope(t, r, []string{"change", "--wait"})
	assert.Equal(t, exitRejected, wjr.code)
	assert.Equal(t, "rejected", wjr.errCode)
	assert.Contains(t, wjr.errMessage, "5 s is too short for keyframe gaps")
	d := filedOf(t, wjr)
	require.Len(t, d.Proposals, 1)
	assert.Equal(t, "5 s is too short for keyframe gaps", d.Proposals[0].Decision.Reason)
	assert.Empty(t, proposalFiles(t, is.dir), "a decision delivered to the waiter is acknowledged")
	assert.Equal(t, baseSpec, readFile(t, is.dir, "demo.spec.md"))

	// --wait that runs out: exit 2.
	jr = is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w", "--wait", "100ms")
	assert.Equal(t, exitPending, jr.code)
	id = filedOf(t, jr).Proposals[0].ID

	// A late waiter on an approved proposal gets exit 0 from the receipt.
	jr = is.run(t, "approve", id)
	require.Equal(t, 0, jr.code, jr.stdout)
	jr = is.run(t, "proposal", id, "--wait", "1s")
	assert.Equal(t, 0, jr.code, jr.stdout)
	assert.Equal(t, []int{1}, filedOf(t, jr).Proposals[0].Decision.Applied)
}

func TestProposeRestoresHeadText(t *testing.T) {
	requireGit(t)
	reply := modelReply{Summary: "ok", Findings: []modelFinding{proseFix}}
	is := setupInfer(t, okReply(reply.json(t)))
	edited := strings.Replace(baseSpec, timingOld, timingNew, 1)
	write(t, is.dir, "demo.spec.md", edited)

	// status says what to do next.
	_, sd := statusOf(t, is.dir)
	assert.Contains(t, sd.Next.Text, "spec propose")
	// A human is told to run the check of their own edit before committing,
	// not to commit straight away.
	assert.Contains(t, sd.Next.Text, "humans: run `spec propose --keep --why WHY` (or ^P in `spec ui`) to review the follow-on fixes, then commit with the trailer lines above")

	jr := is.run(t, "propose", "Frame.Timing", "--why", "bound it", "--by", "agent")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	d := filedOf(t, jr)
	require.Len(t, d.Proposals, 1)
	p := d.Proposals[0]
	assert.Equal(t, timingOld, p.Primary.OldText)
	assert.Equal(t, timingNew, p.Primary.NewText)
	assert.False(t, p.Primary.InWorktree)
	assert.Equal(t, "bound it", p.Why)
	assert.Contains(t, d.Next.Text, "wait for a human to review "+p.ID)
	assert.Equal(t, baseSpec, readFile(t, is.dir, "demo.spec.md"), "the HEAD text is back, byte for byte")
	_, sd = statusOf(t, is.dir)
	assert.Equal(t, "clean", sd.State)
	// The check ran against the restored text.
	msg := userMessage(t, is.srv, 0)
	assert.Contains(t, msg, "<text>\n"+timingOld+"\n</text>")
	assert.Contains(t, msg, "reason: bound it\n")

	jr = is.run(t, "approve", p.ID)
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Equal(t, strings.Replace(edited, proseFix.Find, proseFix.Replace, 1), readFile(t, is.dir, "demo.spec.md"))
	_, sd = statusOf(t, is.dir)
	assert.Equal(t, "anchors_changed", sd.State)
	assert.Contains(t, sd.Trailers[0], "Spec-Change: $Frame.Timing")
}

func TestProposeAddAndRemoveRoundTrip(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	added := "**$Frame.Gap (frame gap).** Two fragments are at most 2 s apart.\nChecked by: TestGap."
	edited := strings.Replace(baseSpec, "\n\nProse that mentions", "\n\n"+added+"\n\nProse that mentions", 1)
	edited = strings.Replace(edited, "**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, see $Frame.Timing.\nChecked by: TestOrder.\n\n", "", 1)
	edited = strings.Replace(edited, "Prose that mentions $Frame.Order for the reader.\n", "Prose for the reader.\n", 1)
	write(t, is.dir, "demo.spec.md", edited)

	jr := is.run(t, "propose", "--source", "human", "--why", "restructure")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	d := filedOf(t, jr)
	require.Len(t, d.Proposals, 2)
	actions := map[string]string{}
	for _, p := range d.Proposals {
		actions[p.Primary.Name] = p.Primary.Action
	}
	assert.Equal(t, map[string]string{"$Frame.Gap": "add", "$Frame.Order": "remove"}, actions)
	// Both anchors are back to HEAD; the prose edit stays.
	assert.Equal(t, strings.Replace(baseSpec, "Prose that mentions $Frame.Order for the reader.\n", "Prose for the reader.\n", 1), readFile(t, is.dir, "demo.spec.md"))

	for _, p := range d.Proposals {
		jr = is.run(t, "approve", p.ID)
		require.Equal(t, 0, jr.code, jr.stdout)
	}
	assert.Equal(t, edited, readFile(t, is.dir, "demo.spec.md"), "approving both reproduces the edit byte for byte")
}

func TestProposeKeep(t *testing.T) {
	requireGit(t)
	reply := modelReply{Summary: "ok", Findings: []modelFinding{proseFix}}
	is := setupInfer(t, okReply(reply.json(t)))
	edited := strings.Replace(baseSpec, timingOld, timingNew, 1)
	write(t, is.dir, "demo.spec.md", edited)

	jr := is.run(t, "propose", "Frame.Timing", "--keep", "--source", "human", "--why", "mine")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	assert.True(t, p.Primary.InWorktree)
	assert.Equal(t, "human", p.Author)
	assert.Equal(t, edited, readFile(t, is.dir, "demo.spec.md"), "--keep leaves the edit")
	assert.Contains(t, userMessage(t, is.srv, 0), "<text>\n"+timingNew+"\n</text>")

	jr = is.run(t, "approve", p.ID)
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Equal(t, strings.Replace(edited, proseFix.Find, proseFix.Replace, 1), readFile(t, is.dir, "demo.spec.md"))

	// Nothing changed: nothing to propose.
	git(t, is.dir, "checkout", "--", "demo.spec.md")
	jr = is.run(t, "propose", "--why", "x")
	assert.Equal(t, 0, jr.code)
	assert.Empty(t, filedOf(t, jr).Proposals)
	jr = is.run(t, "propose", "Frame.Order", "--why", "x")
	assert.Equal(t, "not_found", jr.errCode)
}

func TestStaleProposal(t *testing.T) {
	requireGit(t)
	reply := modelReply{Summary: "ok", Findings: []modelFinding{proseFix}}
	is := setupInfer(t, okReply(reply.json(t)))
	write(t, is.dir, "body.md", timingNew)

	// Optional human checks retain per-hunk matching; external required checks
	// instead refuse a changed source snapshot before any edit is applied.
	// A finding hunk whose text changed fails alone.
	jr := is.run(t, "change", "Frame.Timing", "--source", "human", "--body-file", "body.md", "--why", "w")
	require.Equal(t, exitPending, jr.code)
	id := filedOf(t, jr).Proposals[0].ID
	replaceIn(t, is.dir, "demo.spec.md", "for the reader.", "for every reader.")
	jr = is.run(t, "approve", id)
	require.Equal(t, 0, jr.code, jr.stdout)
	var ad approveData
	jr.decode(t, &ad)
	assert.Equal(t, []int{1}, ad.Proposal.Decision.Applied)
	require.Len(t, ad.Proposal.Decision.Failed, 1)
	assert.Equal(t, 2, ad.Proposal.Decision.Failed[0].N)
	assert.Contains(t, readFile(t, is.dir, "demo.spec.md"), timingNew)

	// A primary hunk whose anchor changed fails the proposal.
	git(t, is.dir, "checkout", "--", "demo.spec.md")
	jr = is.run(t, "change", "Frame.Timing", "--source", "human", "--body-file", "body.md", "--why", "w")
	id = filedOf(t, jr).Proposals[0].ID
	replaceIn(t, is.dir, "demo.spec.md", "above zero", "strictly above zero")
	before := readFile(t, is.dir, "demo.spec.md")
	jr = is.run(t, "approve", id)
	assert.Equal(t, 1, jr.code)
	assert.Equal(t, "stale", jr.errCode)
	assert.Contains(t, jr.errMessage, "$Frame.Timing")
	assert.Equal(t, before, readFile(t, is.dir, "demo.spec.md"), "nothing written")
	jr = is.run(t, "proposal", id)
	var pd proposalData
	jr.decode(t, &pd)
	assert.Equal(t, proposal.StatusPending, pd.Proposal.Status)
	assert.Nil(t, pd.Proposal.Decision)
	jr = is.run(t, "approve", id)
	assert.Equal(t, "stale", jr.errCode)
}

// Required checks authorize exactly the context they inspected, even when the
// primary and proposed fix still match after an unrelated context edit.
func TestExternalProposalRefusesChangedCheckedContext(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok", Findings: []modelFinding{proseFix}}.json(t)))
	write(t, is.dir, "body.md", timingNew)
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "bound it")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	require.Equal(t, "agent", p.SourceKind)
	require.Equal(t, "complete", p.CheckState)
	replaceIn(t, is.dir, "demo.spec.md", "Fragments MUST arrive", "Fragments must always arrive")
	before := readFile(t, is.dir, "demo.spec.md")
	jr = is.run(t, "approve", p.ID)
	require.Equal(t, exitFail, jr.code, jr.stdout)
	assert.Equal(t, "stale_check", jr.errCode)
	assert.Equal(t, before, readFile(t, is.dir, "demo.spec.md"), "no primary or child edit written")
	jr = is.run(t, "proposal", p.ID)
	var pd proposalData
	jr.decode(t, &pd)
	assert.Equal(t, proposal.StatusPending, pd.Proposal.Status)
	assert.Nil(t, pd.Proposal.Decision, "stale required context does not silently decide a proposal")
}

func TestMalformedModelJSON(t *testing.T) {
	requireGit(t)
	var calls atomic.Int32
	good := modelReply{Summary: "ok"}.json(t)
	is := setupInfer(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		content := "I think the change is fine, no JSON here."
		if n == 4 {
			content = "```json\n" + good + "\n```"
		}
		okReply(content)(w, r)
	})
	write(t, is.dir, "body.md", timingNew)

	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w")
	assert.Equal(t, exitPending, jr.code)
	assert.Equal(t, "pending", jr.errCode)
	failed := filedOf(t, jr).Proposals[0]
	assert.Equal(t, "failed", failed.CheckState)
	assert.Equal(t, int32(2), calls.Load(), "one retry with a reminder")
	bodies, _ := is.srv.requests()
	msgs := bodies[1]["messages"].([]any)
	assert.Len(t, msgs, 4, "the retry carries the bad reply and a reminder")
	assert.Equal(t, "assistant", msgs[2].(map[string]any)["role"])
	assert.Len(t, proposalFiles(t, is.dir), 1, "failed check retains the submitted primary")

	// A bad reply then a fenced good one: filed.
	jr = is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w")
	assert.Equal(t, exitPending, jr.code, jr.stdout)
	assert.Equal(t, 2, filedOf(t, jr).Proposals[0].Inference.Calls)
}

// TestReplyBudgetSpentIsNotRetried: a reply cut off by the token cap
// (a reasoning model still thinking) fails at once, naming the budget.
func TestReplyBudgetSpentIsNotRetried(t *testing.T) {
	requireGit(t)
	var calls atomic.Int32
	is := setupInfer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"model":"glm","choices":[{"message":{"content":"","reasoning_content":"Let me check every number"},"finish_reason":"length"}],"usage":{"prompt_tokens":7000,"completion_tokens":16384}}`)
	})
	write(t, is.dir, "body.md", timingNew)
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w")
	assert.Equal(t, exitPending, jr.code)
	assert.Equal(t, "pending", jr.errCode)
	failed := filedOf(t, jr).Proposals[0]
	assert.Equal(t, "failed", failed.CheckState)
	assert.Contains(t, failed.CheckMessage, "whole reply budget")
	assert.Contains(t, failed.CheckMessage, "reasoning_effort")
	assert.Contains(t, failed.CheckMessage, "triage_extra", "the fix names the runaway guard's setting")
	assert.Equal(t, int32(1), calls.Load(), "no retry: the provider has no triage_extra")
	assert.Len(t, proposalFiles(t, is.dir), 1, "failed check retains the submitted primary")
}

// runawayReply is a reasoning model that thought until the reply cap.
const runawayReply = `{"model":"glm","choices":[{"message":{"content":"","reasoning_content":"Let me check every number"},"finish_reason":"length"}],"usage":{"prompt_tokens":7000,"completion_tokens":16384}}`

// guardConfig is a provider with a low-effort triage_extra (and the
// cascade's triage stage off: the guard is under test).
func guardConfig(url string) string {
	return "default = \"fake\"\n[providers.fake]\nbase_url = \"" + url + "\"\nmodel = \"check-model\"\ntriage_model = \"cheap-model\"\ntriage = false\n" +
		"extra = { reasoning_effort = \"medium\", top_p = 0.9 }\ntriage_extra = { reasoning_effort = \"low\" }\n"
}

// TestRunawayGuard: a reply that spends the whole budget without answering
// is asked once more with the provider's triage_extra merged in, on the same
// model; the proposal's inference block says the guard fired.
func TestRunawayGuard(t *testing.T) {
	requireGit(t)
	var calls atomic.Int32
	good := okReply(modelReply{Summary: "ok", Findings: []modelFinding{proseFix}}.json(t))
	is := setupInfer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(w, runawayReply)
			return
		}
		good(w, r)
	})
	writeUserConfig(t, is.env, guardConfig(is.srv.URL))
	write(t, is.dir, "body.md", timingNew)

	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	require.NotNil(t, p.Inference)
	assert.True(t, p.Inference.RunawayGuard)
	assert.Equal(t, 2, p.Inference.Calls)
	assert.Equal(t, 7000+12, p.Inference.PromptTokens, "both calls are counted")
	assert.Contains(t, jr.stdout, `"runaway_guard":true`)
	require.Len(t, p.Hunks, 1)

	bodies, _ := is.srv.requests()
	require.Len(t, bodies, 2)
	assert.Equal(t, true, bodies[0]["stream"], "streamed by default")
	assert.Equal(t, "medium", bodies[0]["reasoning_effort"])
	assert.Equal(t, "low", bodies[1]["reasoning_effort"], "triage_extra merged in")
	assert.Equal(t, 0.9, bodies[1]["top_p"], "the rest of extra stays")
	assert.Equal(t, "check-model", bodies[1]["model"], "the same model, not the triage model")
	assert.Equal(t, bodies[0]["messages"], bodies[1]["messages"], "the same request")

	// A normal check does not mention the guard.
	calls.Store(1)
	jr = is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	assert.NotContains(t, jr.stdout, "runaway_guard")
	assert.Equal(t, 1, filedOf(t, jr).Proposals[0].Inference.Calls)
}

// TestRunawayGuardFiresOnce: when the low-effort retry runs away too, the
// check fails and says the guard was tried; no third call.
func TestRunawayGuardFiresOnce(t *testing.T) {
	requireGit(t)
	var calls atomic.Int32
	is := setupInfer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, runawayReply)
	})
	writeUserConfig(t, is.env, guardConfig(is.srv.URL))
	write(t, is.dir, "body.md", timingNew)
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w")
	assert.Equal(t, exitPending, jr.code)
	assert.Equal(t, "pending", jr.errCode)
	failed := filedOf(t, jr).Proposals[0]
	assert.Equal(t, "failed", failed.CheckState)
	assert.Contains(t, failed.CheckMessage, "again after a retry with the provider's triage_extra")
	assert.Equal(t, int32(2), calls.Load())
	assert.Len(t, proposalFiles(t, is.dir), 1, "failed check retains the submitted primary")
}

func TestPhantomFindBecomesNote(t *testing.T) {
	requireGit(t)
	phantom := modelFinding{File: "demo.spec.md", Issue: "numeric_mismatch", Severity: "breaking", Explanation: "e",
		Find: "Fragments MUST be 10 s long.", Replace: "Fragments MUST be 5 s long."}
	inside := modelFinding{File: "demo.spec.md", Issue: "ambiguity", Severity: "cosmetic", Explanation: "e",
		Find: "Checked by: TestFragment.", Replace: "Checked by: TestFragmentBound."}
	is := setupInfer(t, okReply(modelReply{Summary: "s", Findings: []modelFinding{phantom, inside}, Questions: []string{"5 s or 6 s?"}}.json(t)))
	write(t, is.dir, "body.md", timingNew)

	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w", "--auto-approve")
	require.Equal(t, exitPending, jr.code, "breaking findings and questions block --auto-approve: %s", jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	assert.Empty(t, p.Hunks)
	require.Len(t, p.Notes, 2)
	assert.Equal(t, "model quoted text that does not exist", p.Notes[0].Reason)
	assert.Equal(t, phantom.Find, p.Notes[0].Find)
	assert.Equal(t, "the edit overlaps the change under review", p.Notes[1].Reason)
	assert.Equal(t, []string{"5 s or 6 s?"}, p.Questions)
	assert.Equal(t, baseSpec, readFile(t, is.dir, "demo.spec.md"))
}

func TestAutoApprove(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok", Findings: []modelFinding{proseFix}}.json(t)))
	write(t, is.dir, "body.md", timingNew)
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w", "--auto-approve")
	require.Equal(t, 0, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	assert.Equal(t, proposal.StatusApplied, p.Status)
	assert.Equal(t, "human (auto-approve)", p.Decision.By)
	assert.Empty(t, proposalFiles(t, is.dir), "the caller saw the decision: no receipt is left")
	assert.Equal(t, strings.Replace(strings.Replace(baseSpec, timingOld, timingNew, 1), proseFix.Find, proseFix.Replace, 1), readFile(t, is.dir, "demo.spec.md"))

	// propose --auto-approve keeps the edit in place.
	git(t, is.dir, "checkout", "--", "demo.spec.md")
	edited := strings.Replace(baseSpec, orderFind, orderEdit, 1)
	write(t, is.dir, "demo.spec.md", edited)
	jr = is.run(t, "propose", "Frame.Order", "--why", "w", "--auto-approve")
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Equal(t, strings.Replace(edited, proseFix.Find, proseFix.Replace, 1), readFile(t, is.dir, "demo.spec.md"))
}

func TestChangeForms(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))

	// Kind change only.
	jr := is.run(t, "change", "Frame.Order", "--def", "--why", "it is vocabulary")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	assert.Equal(t, "kind", p.Primary.Action)
	assert.True(t, strings.HasPrefix(p.Primary.NewText, "**Def $Frame.Order (frame order).**"))
	jr = is.run(t, "approve", p.ID)
	require.Equal(t, 0, jr.code, jr.stdout)
	assert.Contains(t, readFile(t, is.dir, "demo.spec.md"), "**Def $Frame.Order (frame order).** Fragments")
	git(t, is.dir, "checkout", "--", "demo.spec.md")

	// A body without its label keeps the current label (paragraph form).
	jr = is.run(t, "change", "Frame.Order", "--body-file", "-", "--why", "w")
	assert.Equal(t, "usage", jr.errCode, "empty stdin")
	var out, errb bytes.Buffer
	code := Run([]string{"change", "Frame.Order", "--body-file", "-", "--why", "w", "--json"}, is.fastEnv(&out, &errb, "Fragments MUST arrive in order.\n"))
	jr = decodeEnvelope(t, result{code: code, stdout: out.String(), stderr: errb.String()}, nil)
	require.Equal(t, exitPending, jr.code, jr.stdout)
	assert.Equal(t, "**$Frame.Order (frame order).** Fragments MUST arrive in order.", filedOf(t, jr).Proposals[0].Primary.NewText)

	// Add after an anchor, then approve.
	write(t, is.dir, "new.md", "**$Extra.Floor (extra floor).** The extra queue MUST hold at least 1 item.\n")
	jr = is.run(t, "change", "Extra.Floor", "--add", "--after", "Extra.Limit", "--body-file", "new.md", "--why", "w")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p = filedOf(t, jr).Proposals[0]
	assert.Equal(t, "add", p.Primary.Action)
	jr = is.run(t, "approve", p.ID)
	require.Equal(t, 0, jr.code, jr.stdout)
	floor := "**$Extra.Floor (extra floor).** The extra queue MUST hold at least 1 item."
	assert.Equal(t, strings.TrimSuffix(otherSpec, "\n")+"\n\n"+floor+"\n", readFile(t, is.dir, "other.spec.md"))

	// Remove, by section placement errors and validation.
	jr = is.run(t, "change", "Extra.Limit", "--remove", "--why", "w")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"change", "Frame.Order", "--remove"}, "usage"},
		{[]string{"change", "Frame.Order", "--why", "w"}, "usage"},
		{[]string{"change", "Frame.Order", "--def", "--inv", "--why", "w"}, "usage"},
		{[]string{"change", "Time.Media", "--def", "--why", "w"}, "usage"},
		{[]string{"change", "Nope.Name", "--remove", "--why", "w"}, "not_found"},
		{[]string{"change", "Frame.Order", "--add", "--after", "Frame.Timing", "--body-file", "new.md", "--why", "w"}, "name_exists"},
		{[]string{"change", "Extra.Two", "--add", "--section", "9", "--body-file", "new.md", "--why", "w"}, "not_found"},
		{[]string{"change", "Extra.Two", "--add", "--section", "1", "--body-file", "new.md", "--why", "w"}, "ambiguous"},
		{[]string{"change", "Extra.Two", "--add", "--section", "other:1", "--body-file", "new.md", "--why", "w"}, "bad_change"},
	} {
		jr := is.run(t, c.args...)
		assert.Equal(t, c.code, jr.errCode, "%v: %s", c.args, jr.stdout)
	}
}

func TestApproveAndProposeRespectTheLock(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	write(t, is.dir, "body.md", timingNew)
	jr := is.run(t, "change", "Frame.Timing", "--body-file", "body.md", "--why", "w")
	require.Equal(t, exitPending, jr.code)
	id := filedOf(t, jr).Proposals[0].ID

	lock, err := project.AcquireLock(is.dir, "spec ui")
	require.NoError(t, err)
	jr = is.run(t, "approve", id)
	assert.Equal(t, exitLock, jr.code)
	assert.Equal(t, "lock_held", jr.errCode)
	write(t, is.dir, "demo.spec.md", strings.Replace(baseSpec, timingOld, timingNew, 1))
	jr = is.run(t, "propose", "Frame.Timing", "--why", "w")
	assert.Equal(t, exitLock, jr.code, "the restore write needs the lock")
	assert.Len(t, proposalFiles(t, is.dir), 1, "nothing filed when the restore cannot be written")
	// Filing with --keep needs no lock; a rejection waits for it.
	jr = is.run(t, "propose", "--keep", "--source", "human", "--why", "w")
	assert.Equal(t, exitPending, jr.code, jr.stdout)
	rejected := is.runAsync("reject", id, "--why", "no")
	require.NoError(t, lock.Release())
	jr = decodeEnvelope(t, <-rejected, []string{"reject"})
	assert.Equal(t, 0, jr.code, jr.stdout)
}

// TestProposeWhileUIOpen: an agent's `spec propose` (no --keep) works while
// `spec ui` holds .spec/ui.lock: the UI lock keeps out a second UI only,
// not the CLI; the proposal is filed and the agent's edit restored.
func TestProposeWhileUIOpen(t *testing.T) {
	requireGit(t)
	is := setupInfer(t, okReply(modelReply{Summary: "ok"}.json(t)))
	ui, err := project.AcquireUILock(is.dir, "spec ui")
	require.NoError(t, err)
	defer func() { require.NoError(t, ui.Release()) }()
	write(t, is.dir, "demo.spec.md", strings.Replace(baseSpec, timingOld, timingNew, 1))
	jr := is.run(t, "propose", "Frame.Timing", "--why", "agent test", "--by", "agent-7")
	require.Equal(t, exitPending, jr.code, jr.stdout)
	p := filedOf(t, jr).Proposals[0]
	assert.Equal(t, "agent-7", p.Author)
	assert.False(t, p.Primary.InWorktree, "the change waits in the proposal")
	assert.Equal(t, baseSpec, readFile(t, is.dir, "demo.spec.md"), "the agent's edit is restored to HEAD")
	assert.Len(t, proposalFiles(t, is.dir), 1)
}
