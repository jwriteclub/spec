// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/jwriteclub/spec/internal/config"
)

// Request purposes: what a chat request is for. The workflow sets
// Request.Purpose; the canned provider serves its replies by purpose.
const (
	// PurposeCheck is a contradiction check or an analysis (also a request
	// that sets no purpose).
	PurposeCheck = "check"
	// PurposeTriage is the per-edit cascade's triage stage.
	PurposeTriage = "triage"
	// PurposeDraft drafts a commit message.
	PurposeDraft = "draft"
	// PurposeFix proposes the fix of one lint finding.
	PurposeFix = "fix"
	// PurposeRevise redoes one hunk after the reviewer's feedback.
	PurposeRevise = "revise"
	// PurposeGate is a gate decision (Gate.Decide), not a chat request.
	PurposeGate = "gate"
	// PurposeSpellGate is the spell check's Jev decision (Gate.Decide).
	PurposeSpellGate = "spell_gate"
	// PurposeSpell is the spell check's triage request: the typos and
	// their fixes.
	PurposeSpell = "spell"
)

// CannedPurposes are the tables of a canned replies file, in the order
// the documentation lists them.
var CannedPurposes = []string{PurposeCheck, PurposeTriage, PurposeGate, PurposeDraft, PurposeFix, PurposeRevise, PurposeSpellGate, PurposeSpell}

// Canned errors: what a reply entry's error key may say.
const (
	// CannedTimeout fails the call as a timeout would (the context's
	// deadline, after the entry's delay).
	CannedTimeout = "timeout"
	// CannedHTTP500 fails the call with an HTTP 500.
	CannedHTTP500 = "http_500"
	// CannedBadJSON answers with text that is not JSON.
	CannedBadJSON = "bad_json"
)

// cannedBadJSON is the reply of an entry with error = "bad_json".
const cannedBadJSON = "I could not decide; here are my thoughts instead of JSON."

// CannedEntry is one hand-written reply: a [[check]], [[triage]], [[gate]],
// [[draft]], [[fix]] or [[revise]] table of the replies file.
type CannedEntry struct {
	// Reply is the chat reply's content (every table but gate).
	Reply string `toml:"reply,omitempty"`
	// Scores are a gate's scores by question id (gate only).
	Scores map[string]float64 `toml:"scores,omitempty"`
	// DelayMS holds the reply back this long (the call's context still
	// cancels it), to show spinners and running stages.
	DelayMS int `toml:"delay_ms,omitempty"`
	// LatencyMS is the latency the reply reports (default DelayMS), so a
	// stage's time reads the same on every run.
	LatencyMS int `toml:"latency_ms,omitempty"`
	// Error fails the call instead: CannedTimeout, CannedHTTP500 or
	// CannedBadJSON.
	Error string `toml:"error,omitempty"`
	// Model is the model the reply names (default: the provider's).
	Model string `toml:"model,omitempty"`
	// FinishReason is the reply's finish reason (default "stop"; chat
	// entries only).
	FinishReason string `toml:"finish_reason,omitempty"`
	// CostUSD is the cost the reply reports (default 0).
	CostUSD float64 `toml:"cost_usd,omitempty"`
	// PromptTokens and CompletionTokens are the usage the reply reports
	// (default 0).
	PromptTokens     int `toml:"prompt_tokens,omitempty"`
	CompletionTokens int `toml:"completion_tokens,omitempty"`
}

// CannedFile is a canned replies file: the entries of each purpose, served
// in order.
type CannedFile struct {
	Check  []CannedEntry `toml:"check,omitempty"`
	Triage []CannedEntry `toml:"triage,omitempty"`
	Gate   []CannedEntry `toml:"gate,omitempty"`
	Draft  []CannedEntry `toml:"draft,omitempty"`
	Fix    []CannedEntry `toml:"fix,omitempty"`
	Revise []CannedEntry `toml:"revise,omitempty"`
	// SpellGate and Spell answer the spell check's Jev decisions and
	// triage requests.
	SpellGate []CannedEntry `toml:"spell_gate,omitempty"`
	Spell     []CannedEntry `toml:"spell,omitempty"`
}

// entries returns the entries of purpose.
func (f *CannedFile) entries(purpose string) []CannedEntry {
	switch purpose {
	case PurposeTriage:
		return f.Triage
	case PurposeGate:
		return f.Gate
	case PurposeDraft:
		return f.Draft
	case PurposeFix:
		return f.Fix
	case PurposeRevise:
		return f.Revise
	case PurposeSpellGate:
		return f.SpellGate
	case PurposeSpell:
		return f.Spell
	}
	return f.Check
}

// ParseCanned parses and checks a canned replies file.
func ParseCanned(src []byte) (*CannedFile, error) {
	var f CannedFile
	dec := toml.NewDecoder(bytes.NewReader(src)).DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		var strict *toml.StrictMissingError
		var derr *toml.DecodeError
		switch {
		case errors.As(err, &strict):
			var keys []string
			for _, e := range strict.Errors {
				row, _ := e.Position()
				keys = append(keys, fmt.Sprintf("%s (line %d)", strings.Join(e.Key(), "."), row))
			}
			return nil, fmt.Errorf("unknown key %s (tables: check, triage, gate, draft, fix, revise, spell_gate, spell; keys: reply, scores, delay_ms, latency_ms, error, model, finish_reason, cost_usd, prompt_tokens, completion_tokens)", strings.Join(keys, ", "))
		case errors.As(err, &derr):
			row, _ := derr.Position()
			return nil, fmt.Errorf("line %d: %s: %s", row, strings.Join(derr.Key(), "."), strings.TrimPrefix(derr.Error(), "toml: "))
		}
		return nil, err
	}
	for _, purpose := range CannedPurposes {
		for k, e := range f.entries(purpose) {
			at := fmt.Sprintf("[[%s]] #%d", purpose, k+1)
			switch e.Error {
			case "", CannedTimeout, CannedHTTP500, CannedBadJSON:
			default:
				return nil, fmt.Errorf("%s: error %q must be %q, %q or %q", at, e.Error, CannedTimeout, CannedHTTP500, CannedBadJSON)
			}
			if e.DelayMS < 0 || e.LatencyMS < 0 {
				return nil, fmt.Errorf("%s: delay_ms and latency_ms must not be negative", at)
			}
			if e.CostUSD < 0 || e.PromptTokens < 0 || e.CompletionTokens < 0 {
				return nil, fmt.Errorf("%s: cost_usd, prompt_tokens and completion_tokens must not be negative", at)
			}
			gate := purpose == PurposeGate || purpose == PurposeSpellGate
			if gate && e.FinishReason != "" {
				return nil, fmt.Errorf("%s: finish_reason is for chat entries; a gate entry takes scores", at)
			}
			switch {
			case gate && e.Reply != "":
				return nil, fmt.Errorf("%s: a gate entry takes scores, not reply", at)
			case !gate && e.Scores != nil:
				return nil, fmt.Errorf("%s: scores are for [[gate]] entries; a chat entry takes reply", at)
			}
		}
	}
	return &f, nil
}

// cannedState is the replies of one file and how many of each purpose
// were served. It is shared by every provider opened on the file (the
// workflow opens one per operation), so the replies go on in order across
// operations; a file whose content changed starts over.
type cannedState struct {
	mu     sync.Mutex
	sum    [sha256.Size]byte
	file   *CannedFile
	served map[string]int
}

var (
	cannedMu     sync.Mutex
	cannedStates = map[string]*cannedState{}
)

// cannedStateOf parses path and returns its shared state.
func cannedStateOf(path string) (*cannedState, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := ParseCanned(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	sum := sha256.Sum256(src)
	cannedMu.Lock()
	defer cannedMu.Unlock()
	if st, ok := cannedStates[path]; ok && st.sum == sum {
		return st, nil
	}
	st := &cannedState{sum: sum, file: f, served: map[string]int{}}
	cannedStates[path] = st
	return st, nil
}

// ForgetCanned drops the shared state of the replies file at path (as
// NewCanned resolves it), so the next provider opened on it starts at the
// first entry of each purpose even when its content is unchanged.
func ForgetCanned(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	cannedMu.Lock()
	defer cannedMu.Unlock()
	delete(cannedStates, path)
}

// next takes the next entry of purpose; ok is false when none is left.
func (st *cannedState) next(purpose string) (e CannedEntry, index, given int, ok bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	es := st.file.entries(purpose)
	index = st.served[purpose]
	st.served[purpose]++
	if index >= len(es) {
		return CannedEntry{}, index, len(es), false
	}
	return es[index], index, len(es), true
}

// Canned is a provider (kind canned) that answers from a file of
// hand-written replies instead of a model: the UI scenes and tests use it
// to show what the UI does when a given reply arrives. Replies are served
// in order within each purpose (Request.Purpose), never matched by prompt,
// so prompt edits cannot break a scene. It is also a Gate. A call with no
// entry left fails with the purpose, the index and the request's first
// lines, which is the entry to write next.
type Canned struct {
	name        string
	path        string
	model       string
	triageModel string
	st          *cannedState
}

// NewCanned builds the canned provider called name from its config: path
// is the replies file (a leading "~/" is the home directory from getenv).
func NewCanned(name string, p config.Provider, getenv func(string) string) (*Canned, error) {
	path := p.Path
	if path == "~" || strings.HasPrefix(path, "~/") {
		home := getenv("HOME")
		if home == "" {
			return nil, &SetupError{Provider: name, Msg: "path starts with ~ but HOME is not set", FixText: "give the replies file's absolute path"}
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := cannedStateOf(abs)
	if err != nil {
		return nil, &SetupError{Provider: name, Msg: "cannot read the canned replies: " + err.Error(),
			FixText: "fix the replies file named by path (`spec explain config` shows its format)"}
	}
	model := p.Model
	if model == "" {
		model = "canned"
	}
	return &Canned{name: name, path: abs, model: model, triageModel: p.TriageModel, st: st}, nil
}

// Chat serves the next reply of req's purpose (Provider).
func (c *Canned) Chat(ctx context.Context, req Request) (*Response, error) {
	purpose := req.Purpose
	if purpose == "" {
		purpose = PurposeCheck
	}
	model := modelFor(req, c.model, c.triageModel)
	e, err := c.take(ctx, purpose, model, RequestHead(req))
	if err != nil {
		return nil, err
	}
	content := e.Reply
	if e.Error == CannedBadJSON {
		content = cannedBadJSON
	}
	echoed := model
	if e.Model != "" {
		echoed = e.Model
	}
	finish := e.FinishReason
	if finish == "" {
		finish = "stop"
	}
	return &Response{
		Content: content, Provider: c.name, RequestedModel: model, Model: echoed,
		FinishReason: finish, Usage: e.usage(), CostUSD: e.CostUSD,
		Latency: e.latency(), Attempts: 1, JSONMode: req.JSON,
	}, nil
}

// Decide serves the next [[gate]] entry's scores (Gate). A reply without
// a score for every question is an *Error, as a real gate's is.
func (c *Canned) Decide(ctx context.Context, req GateRequest) (*Decision, error) {
	model := c.model
	if req.Model != "" {
		model = req.Model
	}
	purpose := req.Purpose
	if purpose == "" {
		purpose = PurposeGate
	}
	e, err := c.take(ctx, purpose, model, GateHead(req))
	if err != nil {
		return nil, err
	}
	if e.Error == CannedBadJSON {
		return nil, &Error{Provider: c.name, Model: model, Status: http.StatusOK, Attempts: 1, Message: "the reply is not JSON (canned bad_json)"}
	}
	var missing []string
	for _, q := range req.Questions {
		if _, ok := e.Scores[q.ID]; !ok {
			missing = append(missing, q.ID)
		}
	}
	if len(missing) > 0 {
		return nil, &Error{Provider: c.name, Model: model, Attempts: 1,
			Message: fmt.Sprintf("canned gate reply has no score for %s", strings.Join(missing, ", "))}
	}
	echoed := model
	if e.Model != "" {
		echoed = e.Model
	}
	scores := make(map[string]float64, len(e.Scores))
	for k, v := range e.Scores {
		scores[k] = v
	}
	return &Decision{Scores: scores, Provider: c.name, RequestedModel: model, Model: echoed,
		Usage: e.usage(), CostUSD: e.CostUSD, Latency: e.latency(), Attempts: 1}, nil
}

// take waits out the next entry's delay and fails the call as the entry
// says; a missing entry is an *Error that says what to write.
func (c *Canned) take(ctx context.Context, purpose, model, head string) (CannedEntry, error) {
	e, index, given, ok := c.st.next(purpose)
	if !ok {
		return e, &Error{Provider: c.name, Model: model, Attempts: 1, fix: "add a [[" + purpose + "]] entry to " + c.path,
			Message: fmt.Sprintf("canned replies %s: no [[%s]] entry #%d (the file has %d); the request begins:\n%s", c.path, purpose, index+1, given, head)}
	}
	if e.DelayMS > 0 {
		t := time.NewTimer(time.Duration(e.DelayMS) * time.Millisecond)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return e, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return e, err
	}
	switch e.Error {
	case CannedTimeout:
		return e, &Error{Provider: c.name, Model: model, Attempts: 1, Retryable: true, cause: fmt.Errorf("canned timeout: %w", context.DeadlineExceeded)}
	case CannedHTTP500:
		return e, &Error{Provider: c.name, Model: model, Attempts: 1, Retryable: true, Status: http.StatusInternalServerError, Message: "canned http_500"}
	}
	return e, nil
}

// latency is the latency the entry reports.
func (e CannedEntry) latency() time.Duration {
	if e.LatencyMS > 0 {
		return time.Duration(e.LatencyMS) * time.Millisecond
	}
	return time.Duration(e.DelayMS) * time.Millisecond
}

// usage is the token usage the entry reports.
func (e CannedEntry) usage() Usage {
	return Usage{PromptTokens: e.PromptTokens, CompletionTokens: e.CompletionTokens,
		TotalTokens: e.PromptTokens + e.CompletionTokens}
}

// Info describes the provider (Provider, Gate).
func (c *Canned) Info() Info {
	return Info{Name: c.name, Kind: config.KindCanned, Model: c.model, TriageModel: c.triageModel, Transport: c.path}
}

// SetModel replaces the model and the triage model (Provider).
func (c *Canned) SetModel(id string) { c.model, c.triageModel = id, "" }

// SetEffort does nothing: canned replies have no effort (Provider).
func (c *Canned) SetEffort(string) {}

// SetLimits does nothing: a canned reply takes its delay_ms (Provider,
// Gate).
func (c *Canned) SetLimits(time.Duration, int) {}

// RunawayExtra is nil: the runaway guard is off (Provider).
func (c *Canned) RunawayExtra() map[string]any { return nil }

// RequestHead is the start of a chat request, for a missing-entry error
// and for telling a replay's requests from a recording's: the first lines
// of its last message.
func RequestHead(req Request) string {
	if len(req.Messages) == 0 {
		return "(no messages)"
	}
	return firstLines(req.Messages[len(req.Messages)-1].Content, 3)
}

// GateHead is the start of a gate request, as RequestHead is of a chat
// request: the first lines of its situation.
func GateHead(req GateRequest) string {
	return firstLines(req.Situation, 3)
}

// firstLines returns the first n non-blank lines of s, each indented.
func firstLines(s string, n int) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if len(out) == n {
			out = append(out, "  …")
			break
		}
		out = append(out, "  "+shorten(l))
	}
	if len(out) == 0 {
		return "  (empty)"
	}
	return strings.Join(out, "\n")
}
