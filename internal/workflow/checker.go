// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/proposal"
)

// Checker is a ready model connection with the user config's budgets and
// prompt overrides. Its requests end when its context does.
type Checker struct {
	Client  infer.Provider
	cfg     config.Config
	cfgPath string
	home    string
	full    bool
	ctx     context.Context
	// Record accumulates the model calls made through the checker.
	Record proposal.Inference
	// cascade holds the per-edit cascade's gate and triage stages and the
	// provider of the fast requests (cascade.go).
	cascade cascade
	// OnStage, when set, hears each cascade stage start and end
	// (CheckChange); it runs on the checker's goroutine.
	OnStage func(StageEvent)
	// now is the workflow's clock, for the stages' times.
	now func() time.Time
}

// OpenChecker loads the user config and the provider. full sends every
// spec file whole. The checker's requests are cancelled with ctx.
func (w *Workflow) OpenChecker(ctx context.Context, full bool) (*Checker, *Error) {
	c, loaded, e := w.OpenProviderConfig()
	if e != nil {
		return nil, e
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ck := &Checker{Client: c, cfg: loaded.Config, cfgPath: loaded.Path, home: w.home(), full: full, ctx: ctx, now: w.now}
	ck.cascade = w.openCascade(loaded.Config, loaded.Path, c)
	ck.ResetRecord()
	return ck, nil
}

// ResetRecord starts a new inference record (one per proposal).
func (ck *Checker) ResetRecord() {
	info := ck.Client.Info()
	ck.Record = proposal.Inference{Provider: info.Name, Model: info.Model, Kind: info.Kind, Effort: info.Effort}
}

// options returns the context options.
func (ck *Checker) options() specctx.Options {
	return specctx.Options{MaxTokens: ck.cfg.ContextBudget(), Full: ck.full}
}

// system returns the system prompt of kind, honoring [prompts].
func (ck *Checker) system(kind string) (string, *Error) {
	override := ""
	switch kind {
	case prompt.Change:
		override = ck.cfg.Prompts.Change
	case prompt.Analyze:
		override = ck.cfg.Prompts.Analyze
	case prompt.Commit:
		override = ck.cfg.Prompts.Commit
	case prompt.Gate:
		override = ck.cfg.Prompts.Gate
	case prompt.Triage:
		override = ck.cfg.Prompts.Triage
	}
	text, err := prompt.Load(kind, config.PromptPath(override, ck.cfgPath, ck.home))
	if err != nil {
		return "", fail(ExitFail, "provider_unavailable", "%v; fix the [prompts] entry in %s", err, ck.cfgPath)
	}
	return text, nil
}

// note adds a response to the inference record.
func (ck *Checker) note(r *infer.Response) {
	if r == nil {
		return
	}
	ck.Record.Calls++
	ck.Record.PromptTokens += r.Usage.PromptTokens
	ck.Record.CompletionTokens += r.Usage.CompletionTokens
	ck.Record.TotalTokens += r.Usage.TotalTokens
	ck.Record.LatencyMS += r.Latency.Milliseconds()
	ck.Record.CostUSD += r.CostUSD
	if r.Model != "" {
		ck.Record.EchoedModel = r.Model
	}
	if r.RequestedModel != "" {
		ck.Record.Model = r.RequestedModel
	}
}

// clock returns the time by the workflow's clock (time.Now for a checker
// built without one).
func (ck *Checker) clock() time.Time {
	if ck.now != nil {
		return ck.now()
	}
	return time.Now()
}

// cancelled maps a request that ended because the checker's context did.
func (ck *Checker) cancelled() *Error {
	if err := ck.ctx.Err(); err != nil {
		return fail(ExitFail, "cancelled", "cancelled: %v; nothing was filed", err)
	}
	return nil
}

// Run sends a check of kind over c and decodes the reply. A reply that
// spends the whole reply budget without answering gets the runaway guard
// (see ask); one without the JSON asked for is retried once with a
// reminder; a second bad reply is exit 1 `bad_model_output`.
func (ck *Checker) Run(kind string, c *specctx.Context) (*finding.Result, *Error) {
	system, e := ck.system(kind)
	if e != nil {
		return nil, e
	}
	return ck.runMessages(prompt.Messages(kind, system, c), infer.PurposeCheck)
}

// Fix asks for the fix of one lint finding: the shared fix prompt with the
// rule's instruction, the finding, and the spec context c. The reply has
// the shape of a check's and is handled as Run handles one.
func (ck *Checker) Fix(in prompt.FixInput, c *specctx.Context) (*finding.Result, *Error) {
	shared, e := ck.system(prompt.Fix)
	if e != nil {
		return nil, e
	}
	instruction, ok := prompt.FixInstruction(in.Code)
	if !ok {
		return nil, internalErr(fmt.Errorf("no fix instruction for %s", in.Code))
	}
	return ck.runMessages(prompt.FixMessages(prompt.FixSystem(shared, in.Code, instruction), in, c), infer.PurposeFix)
}

// runMessages sends msgs as a check (or a fix: purpose) and decodes the
// reply (see Run).
func (ck *Checker) runMessages(msgs []infer.Message, purpose string) (*finding.Result, *Error) {
	var guard runawayGuard
	res, resp, err := ask[finding.Result](ck, infer.Request{Messages: msgs, MaxTokens: ck.cfg.OutputBudget(), Purpose: purpose}, &guard)
	if e := ck.cancelled(); e != nil {
		return nil, e
	}
	var je *infer.JSONError
	if errors.As(err, &je) && resp != nil && resp.FinishReason == "length" {
		// The reply budget ran out (usually while a reasoning model was
		// still thinking): asking again the same way would spend the same
		// budget again.
		if ck.Client.Info().Kind == config.KindClaudeCLI {
			return nil, fail(ExitFail, "bad_model_output", "the model hit Claude Code's reply cap (%d tokens) without finishing its answer; nothing was filed. Lower the provider's effort (effort = \"low\", or --effort low) in %s", resp.Usage.CompletionTokens, ck.cfgPath)
		}
		if guard.fired {
			return nil, fail(ExitFail, "bad_model_output", "the model used its whole reply budget (max_output_tokens %d, %d tokens) without finishing its answer, again after a retry with the provider's triage_extra; nothing was filed. Lower the reasoning effort in triage_extra and extra, or raise max_output_tokens in %s", ck.cfg.OutputBudget(), resp.Usage.CompletionTokens, ck.cfgPath)
		}
		return nil, fail(ExitFail, "bad_model_output", "the model used its whole reply budget (max_output_tokens %d, %d tokens) without finishing its answer; nothing was filed. Add triage_extra = { reasoning_effort = \"low\" } to the provider (spec then retries a runaway reply once at that effort), lower its reasoning effort (for example extra = { reasoning_effort = \"low\" }), or raise max_output_tokens in %s", ck.cfg.OutputBudget(), resp.Usage.CompletionTokens, ck.cfgPath)
	}
	if errors.As(err, &je) {
		lg.WithError(err).Info("model reply was not JSON; asking again")
		retry := append(append([]infer.Message{}, msgs...),
			infer.Message{Role: "assistant", Content: resp.Content}, infer.User(prompt.RetryMessage))
		res, _, err = ask[finding.Result](ck, infer.Request{Messages: retry, MaxTokens: ck.cfg.OutputBudget(), Purpose: purpose}, &guard)
		if e := ck.cancelled(); e != nil {
			return nil, e
		}
		if errors.As(err, &je) {
			return nil, fail(ExitFail, "bad_model_output", "the model did not answer with the JSON asked for, twice: %v; nothing was filed. Run it again, or try another model (--engine)", je)
		}
	}
	if err != nil {
		return nil, ProviderErr(err)
	}
	res.Questions = finding.CleanQuestions(res.Questions)
	return res, nil
}

// runawayGuard is the state of the runaway guard over one check.
type runawayGuard struct {
	// fired is set once a reply ran away and the request was sent again
	// with the provider's triage_extra; later requests of the check keep
	// that setting.
	fired bool
}

// ask sends one JSON request, notes it in the inference record, and
// decodes the reply into a new T.
//
// The runaway guard: a reasoning model sometimes thinks until the reply cap
// and never answers (finish_reason "length" and no JSON). When the provider
// has a triage_extra (its low-effort setting, such as reasoning_effort =
// "low"), the request is sent once more with it merged in, and the record
// says the guard fired. Without a triage_extra the runaway reply is
// returned as is.
//
// With a [triage] provider the retry goes there instead, as a triage
// request (its triage_model, else its model, with its triage_extra): the
// runaway guard is one of the fast requests.
func ask[T any](ck *Checker, req infer.Request, guard *runawayGuard) (*T, *infer.Response, error) {
	return askVia[T](ck, ck.Client, req, guard)
}

// askVia is ask sent to provider p (the check's or the fast one).
func askVia[T any](ck *Checker, p infer.Provider, req infer.Request, guard *runawayGuard) (*T, *infer.Response, error) {
	low := p.RunawayExtra()
	retry, retryReq := p, withExtra(req, low)
	if fast := ck.cascade.fast; fast != nil && p == ck.Client {
		retry, retryReq = fast, req
		retryReq.Triage = true
	}
	if guard.fired {
		p, req = retry, retryReq
	}
	v := new(T)
	resp, err := infer.ChatJSON(ck.ctx, p, req, v)
	ck.note(resp)
	var je *infer.JSONError
	if guard.fired || (len(low) == 0 && retry == p) || !errors.As(err, &je) || resp == nil || resp.FinishReason != "length" {
		return v, resp, err
	}
	if retry == p {
		lg.Warnf("%s: the model used its whole reply budget (%d completion tokens) without answering; retrying once with the provider's triage_extra", p.Info().Name, resp.Usage.CompletionTokens)
	} else {
		lg.Warnf("%s: the model used its whole reply budget (%d completion tokens) without answering; retrying once on the triage provider %s", p.Info().Name, resp.Usage.CompletionTokens, retry.Info().Name)
	}
	guard.fired = true
	ck.Record.RunawayGuard = true
	v = new(T)
	resp, err = infer.ChatJSON(ck.ctx, retry, retryReq, v)
	ck.note(resp)
	return v, resp, err
}

// withExtra returns req with extra merged over its Extra (the client merges
// nested tables key by key).
func withExtra(req infer.Request, extra map[string]any) infer.Request {
	x := make(map[string]any, len(req.Extra)+len(extra))
	maps.Copy(x, req.Extra)
	maps.Copy(x, extra)
	req.Extra = x
	return req
}

// Revise asks the model to redo one hunk given the reviewer's feedback.
func (ck *Checker) Revise(in prompt.ReviseInput) (*prompt.Revision, *Error) {
	system, e := ck.system(prompt.Revise)
	if e != nil {
		return nil, e
	}
	var guard runawayGuard
	rev, _, err := ask[prompt.Revision](ck, infer.Request{
		Messages: prompt.ReviseMessages(system, in), MaxTokens: ck.cfg.OutputBudget(), Purpose: infer.PurposeRevise,
	}, &guard)
	if e := ck.cancelled(); e != nil {
		return nil, e
	}
	if err != nil {
		var je *infer.JSONError
		if errors.As(err, &je) {
			return nil, fail(ExitFail, "bad_model_output", "the model did not answer with the JSON asked for: %v", je)
		}
		return nil, ProviderErr(err)
	}
	return rev, nil
}

// DraftCommit asks the fast model for a commit message for the changes
// in: the [triage] provider's triage model (else its model) when the user
// config names one, else the provider's triage model when it has one.
func (ck *Checker) DraftCommit(in prompt.CommitInput) (*prompt.CommitDraft, *Error) {
	system, e := ck.system(prompt.Commit)
	if e != nil {
		return nil, e
	}
	var guard runawayGuard
	d, _, err := askVia[prompt.CommitDraft](ck, ck.Fast(), infer.Request{
		Messages: prompt.CommitMessages(system, in), MaxTokens: ck.cfg.OutputBudget(), Triage: true, Purpose: infer.PurposeDraft,
	}, &guard)
	if e := ck.cancelled(); e != nil {
		return nil, e
	}
	if err != nil {
		var je *infer.JSONError
		if errors.As(err, &je) {
			return nil, fail(ExitFail, "bad_model_output", "the model did not answer with the JSON asked for: %v", je)
		}
		return nil, ProviderErr(err)
	}
	if strings.TrimSpace(d.Subject) == "" {
		return nil, fail(ExitFail, "bad_model_output", "the model drafted no subject")
	}
	if r := in.FalseRename(*d); r != "" {
		return nil, fail(ExitFail, "bad_model_output", "the model's draft renames %s, a rename the changes do not make", r)
	}
	if n := in.FalseFix(*d); n > 0 {
		return nil, fail(ExitFail, "bad_model_output", "the model's draft claims %d fixes, but the commit applies %d", n, len(in.Accepted))
	}
	return d, nil
}

// SourcesOf maps every valid spec file's path to its bytes.
func SourcesOf(spec *mdspec.Spec) map[string][]byte {
	out := map[string][]byte{}
	for _, f := range spec.Files {
		if f.ValidUTF8 {
			out[f.Path] = f.Source()
		}
	}
	return out
}
