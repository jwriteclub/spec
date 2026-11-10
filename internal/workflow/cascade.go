// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/infer/prompt"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/proposal"
)

// The per-edit cascade (notes/plan-step5-check-pipeline.md, part A): a
// changed anchor goes through the gate (Jev, when [gate] is configured),
// then triage (a fast chat model), then the check, and stops at the first
// stage that clears it. Every stage prefers a false positive: a stage that
// fails (timeout, HTTP error, a reply that does not parse) passes the edit
// on. The cascade only decides whether the check runs; any byte change to
// an anchor body is still a Spec-Change.

// Stage limits.
const (
	// GateTimeout bounds the gate stage when the gate provider sets no
	// timeout: a decision takes about 0.3 s, and the gate is only worth
	// waiting for while it is fast.
	GateTimeout = 10 * time.Second
	// TriageTimeout bounds the whole triage stage, retries included
	// (measured: 1.8 s p50, 5.5 s p95, 8.2 s max).
	TriageTimeout = 60 * time.Second
	// TriagePassAt is the class score at or above which triage passes an
	// edit on to the check.
	TriagePassAt = 0.5
)

// StageEvent is the progress of one anchor's cascade: a stage starting
// (Outcome running) or ending.
type StageEvent struct {
	// Anchor is the changed anchor, with its $.
	Anchor string
	Stage  proposal.Stage
	// Started is when the stage started.
	Started time.Time
}

// cascade is a checker's gate and triage stages.
type cascade struct {
	// gate is nil without [gate]; gateErr says why a configured gate
	// could not be opened (the stage then fails open).
	gate     infer.Gate
	gateCfg  config.Gate
	gateName string
	// gateModel is the configured model, for a gate that failed to open.
	gateModel string
	gateErr   error
	// triage answers the triage stage (nil: no triage stage, unless
	// triageErr says why the configured one could not be opened).
	triage     infer.Provider
	triageName string
	triageErr  error
	// fast is the [triage] provider, which also answers the fast requests
	// (commit drafts, the runaway guard's retry); nil without [triage].
	fast infer.Provider
}

// openCascade builds the gate and triage stages from the user config (read
// from cfgPath); check is the check's provider. Nothing here fails: a stage that cannot
// be opened fails open when it runs.
func (w *Workflow) openCascade(cfg config.Config, cfgPath string, check infer.Provider) cascade {
	var cs cascade
	if g := cfg.Gate; g != nil {
		cs.gateCfg, cs.gateName = *g, g.Provider
		p := cfg.Providers[g.Provider]
		cs.gateModel = p.Model
		resolved, getenv, err := w.credentialProvider(cfg, cfgPath, g.Provider, p)
		var gate infer.Gate
		if err == nil {
			gate, err = infer.OpenGate(g.Provider, resolved, getenv)
		}
		if err != nil {
			cs.gateErr = err
		} else {
			timeout := GateTimeout
			if p.Timeout > 0 {
				timeout = time.Duration(p.Timeout) * time.Second
			}
			// One attempt: a gate that fails passes the edit on, and a
			// retry's backoff would cost more than the gate saves.
			gate.SetLimits(timeout, 1)
			cs.gate = w.wrapGate(gate)
		}
	}
	if t := cfg.Triage; t != nil {
		p := cfg.Providers[t.Provider]
		cs.triageName = t.Provider
		resolved, getenv, err := w.credentialProvider(cfg, cfgPath, t.Provider, p)
		var fast infer.Provider
		if err == nil {
			fast, err = infer.Open(t.Provider, resolved, getenv)
		}
		if err != nil {
			if p.TriageOn() {
				cs.triageErr = err
			}
			lg.WithError(err).Warn("cannot open the [triage] provider; fast requests go to the check's provider")
			return cs
		}
		if hc, ok := fast.(*infer.Client); ok && w.TuneClient != nil {
			w.TuneClient(hc)
		}
		fast = w.wrapProvider(fast)
		cs.fast = fast
		if p.TriageOn() {
			cs.triage = fast
		}
		return cs
	}
	info := check.Info()
	if _, p, err := cfg.Provider(info.Name); err == nil && p.TriageOn() && info.TriageModel != "" {
		cs.triage, cs.triageName = check, info.Name
	}
	return cs
}

// Fast returns the provider of the fast requests: the [triage] provider
// when the user config names one, else the check's.
func (ck *Checker) Fast() infer.Provider {
	if ck.cascade.fast != nil {
		return ck.cascade.fast
	}
	return ck.Client
}

// emit reports a stage event to OnStage.
func (ck *Checker) emit(anchor string, st proposal.Stage, started time.Time) {
	if ck.OnStage != nil {
		ck.OnStage(StageEvent{Anchor: anchor, Stage: st, Started: started})
	}
}

// screen runs the gate and triage stages for change ch over spec. It
// returns the stages that ran, in order, and the one that cleared the
// edit (nil when the check must run). Only a change of an anchor's body
// is screened (what the gate was measured on); an added, removed or
// relabelled anchor goes to the check.
func (ck *Checker) screen(spec *mdspec.Spec, ch *specctx.Change) ([]proposal.Stage, *proposal.Stage) {
	cs := &ck.cascade
	if ch.Action != specctx.ActionChange || (cs.gate == nil && cs.gateErr == nil && cs.triage == nil && cs.triageErr == nil) {
		return nil, nil
	}
	anchor := "$" + ch.Name
	sit := situation(spec, ch, ck.cfg.ContextBudget())
	var stages []proposal.Stage
	if cs.gate != nil || cs.gateErr != nil {
		st := ck.runGate(anchor, sit)
		stages = append(stages, st)
		if st.Outcome == proposal.OutcomeClear {
			return stages, &stages[len(stages)-1]
		}
	}
	if cs.triage != nil || cs.triageErr != nil {
		st := ck.runTriage(anchor, sit)
		stages = append(stages, st)
		if st.Outcome == proposal.OutcomeClear {
			return stages, &stages[len(stages)-1]
		}
	}
	return stages, nil
}

// situation renders what the gate and triage judge: the change, the
// related anchors and the prose that mentions it, without the index or
// whole files (as the gate eval rendered it).
func situation(spec *mdspec.Spec, ch *specctx.Change, budget int) string {
	c := specctx.Build(spec, specctx.Target{Change: ch}, specctx.Options{MaxTokens: budget})
	c.Index, c.Files, c.Omitted = nil, nil, nil
	return c.Render()
}

// runGate runs the gate stage.
func (ck *Checker) runGate(anchor, sit string) proposal.Stage {
	cs := &ck.cascade
	st := proposal.Stage{Name: proposal.StageGate, Provider: cs.gateName, Model: cs.gateModel}
	start := ck.clock()
	ck.emit(anchor, withOutcome(st, proposal.OutcomeRunning), start)
	defer func() { ck.emit(anchor, st, start) }()
	if cs.gateErr != nil {
		st.Outcome, st.Reason = proposal.OutcomeFailedOpen, "cannot open the gate: "+cs.gateErr.Error()
		return st
	}
	q, err := ck.questions(prompt.Gate)
	if err != nil {
		st.Outcome, st.Reason = proposal.OutcomeFailedOpen, err.Error()
		return st
	}
	d, err := cs.gate.Decide(ck.ctx, infer.GateRequest{Policy: q.Policy, Situation: sit, Questions: q.Questions})
	st.LatencyMS = ck.clock().Sub(start).Milliseconds()
	if err != nil {
		st.Outcome, st.Reason = proposal.OutcomeFailedOpen, failReason(err)
		return st
	}
	if d.Model != "" {
		st.Model = d.Model
	}
	st.Scores, st.CostUSD, st.LatencyMS = d.Scores, d.CostUSD, d.Latency.Milliseconds()
	out := infer.Escalates(cs.gateCfg, d.Scores)
	st.Reason = out.Reason
	st.Outcome = proposal.OutcomeClear
	if out.Escalate {
		st.Outcome = proposal.OutcomePass
	}
	return st
}

// triageReply is a triage model's answer: a probability per question id.
type triageReply map[string]float64

// runTriage runs the triage stage.
func (ck *Checker) runTriage(anchor, sit string) proposal.Stage {
	cs := &ck.cascade
	st := proposal.Stage{Name: proposal.StageTriage, Provider: cs.triageName}
	req := infer.Request{MaxTokens: ck.cfg.OutputBudget(), Triage: true, Purpose: infer.PurposeTriage}
	if cs.triage != nil {
		st.Model = modelOf(cs.triage, req)
	}
	start := ck.clock()
	ck.emit(anchor, withOutcome(st, proposal.OutcomeRunning), start)
	defer func() { ck.emit(anchor, st, start) }()
	if cs.triageErr != nil {
		st.Outcome, st.Reason = proposal.OutcomeFailedOpen, "cannot open the triage provider: "+cs.triageErr.Error()
		return st
	}
	q, err := ck.questions(prompt.Triage)
	if err != nil {
		st.Outcome, st.Reason = proposal.OutcomeFailedOpen, err.Error()
		return st
	}
	req.Messages = prompt.TriageMessages(q, sit)
	ctx, cancel := context.WithTimeoutCause(ck.ctx, TriageTimeout,
		fmt.Errorf("triage took longer than %s: %w", TriageTimeout, context.DeadlineExceeded))
	defer cancel()
	var reply triageReply
	resp, err := infer.ChatJSON(ctx, cs.triage, req, &reply)
	st.LatencyMS = ck.clock().Sub(start).Milliseconds()
	if resp != nil {
		st.CostUSD = resp.CostUSD
		if resp.RequestedModel != "" {
			st.Model = resp.RequestedModel
		}
	}
	if err == nil && ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	if err != nil {
		st.Outcome, st.Reason = proposal.OutcomeFailedOpen, failReason(err)
		if ck.ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			st.Reason = fmt.Sprintf("no answer within %s", TriageTimeout)
		}
		return st
	}
	scores := map[string]float64{}
	var missing []string
	for _, x := range q.Questions {
		v, ok := reply[x.ID]
		if !ok || v < 0 || v > 1 {
			missing = append(missing, x.ID)
			continue
		}
		scores[x.ID] = v
	}
	st.Scores = scores
	if len(missing) > 0 {
		st.Outcome, st.Reason = proposal.OutcomeFailedOpen, "the reply has no probability in [0, 1] for "+strings.Join(missing, ", ")
		return st
	}
	cls, _ := maxClass(scores)
	if cls >= TriagePassAt {
		st.Outcome, st.Reason = proposal.OutcomePass, fmt.Sprintf("class score %.2f >= %.2f", cls, TriagePassAt)
	} else {
		st.Outcome, st.Reason = proposal.OutcomeClear, fmt.Sprintf("every class score < %.2f (max %.2f)", TriagePassAt, cls)
	}
	return st
}

// questions loads the gate's or triage's question set, honoring
// [prompts].
func (ck *Checker) questions(kind string) (*prompt.Questions, error) {
	override := ck.cfg.Prompts.Gate
	if kind == prompt.Triage {
		override = ck.cfg.Prompts.Triage
	}
	q, err := prompt.LoadQuestions(kind, config.PromptPath(override, ck.cfgPath, ck.home))
	if err != nil {
		return nil, fmt.Errorf("%v; fix the [prompts] entry in %s", err, ck.cfgPath)
	}
	return q, nil
}

// modelOf is the model provider p sends req to.
func modelOf(p infer.Provider, req infer.Request) string {
	info := p.Info()
	if req.Triage && info.TriageModel != "" {
		return info.TriageModel
	}
	return info.Model
}

// withOutcome returns st with its outcome set.
func withOutcome(st proposal.Stage, outcome string) proposal.Stage {
	st.Outcome = outcome
	return st
}

// failReason says in a line why a stage call failed.
func failReason(err error) string {
	var ie *infer.Error
	if errors.As(err, &ie) {
		return oneLineText(ie.Error())
	}
	var je *infer.JSONError
	if errors.As(err, &je) {
		return "the reply was not the JSON asked for"
	}
	return oneLineText(err.Error())
}

// oneLineText collapses whitespace and caps the length of a reason.
func oneLineText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}

// maxClass is the highest class-question score and the question's id.
func maxClass(scores map[string]float64) (float64, string) {
	best, id := -1.0, ""
	for _, q := range infer.ClassQuestions {
		if v, ok := scores[q]; ok && v > best {
			best, id = v, q
		}
	}
	return max(best, 0), id
}

// shortQuestion is a question id as a stage line shows it.
func shortQuestion(id string) string {
	switch id {
	case infer.QuestionNumericMismatch:
		return "numeric"
	case infer.QuestionStaleReference:
		return "stale"
	case infer.QuestionMaterialChange:
		return "material"
	}
	return id
}

// ShortModel is a model id without its vendor path
// ("@cf/zai-org/glm-5.3-flash" is "glm-5.3-flash").
func ShortModel(m string) string {
	if i := strings.LastIndexByte(m, '/'); i >= 0 && i < len(m)-1 {
		return m[i+1:]
	}
	return m
}

// StageWho is a stage's provider and short model: "jev/jev-1.14".
func StageWho(st proposal.Stage) string {
	m := ShortModel(st.Model)
	switch {
	case st.Provider == "":
		return m
	case m == "":
		return st.Provider
	}
	return st.Provider + "/" + m
}

// StageVerdict is a finished stage's decision and why, in a few words:
// "escalate (contradiction 0.62)", "clear (max class 0.21)", "failed open:
// HTTP 500 ...", "2 findings".
func StageVerdict(st proposal.Stage) string {
	switch st.Outcome {
	case proposal.OutcomeFailedOpen:
		return "failed open: " + st.Reason
	case proposal.OutcomeDone:
		if st.Findings == 0 {
			return "nothing found"
		}
		return plural(st.Findings, "finding")
	case proposal.OutcomeFailed:
		return "failed: " + st.Reason
	case proposal.OutcomeRunning:
		return "running"
	}
	word := "pass"
	if st.Name == proposal.StageGate {
		word = "escalate"
	}
	cls, id := maxClass(st.Scores)
	if st.Outcome == proposal.OutcomeClear {
		if m, ok := st.Scores[infer.QuestionMaterialChange]; ok && strings.HasPrefix(st.Reason, infer.QuestionMaterialChange) {
			return fmt.Sprintf("clear (material %.2f)", m)
		}
		return fmt.Sprintf("clear (max class %.2f)", cls)
	}
	if id == "" {
		return word
	}
	return fmt.Sprintf("%s (%s %.2f)", word, shortQuestion(id), cls)
}

// StageLine is a finished stage in one line, as the CLI and the sidebar
// show it: "gate jev/jev-1.14 0.3 s: escalate (contradiction 0.62)".
func StageLine(st proposal.Stage) string {
	head := st.Name
	if who := StageWho(st); who != "" {
		head += " " + who
	}
	return fmt.Sprintf("%s %s: %s", head, Seconds(time.Duration(st.LatencyMS)*time.Millisecond), StageVerdict(st))
}

// Seconds formats a duration as "0.3 s", "14 s" or "2 min 5 s".
func Seconds(d time.Duration) string {
	switch s := d.Seconds(); {
	case s < 10:
		return fmt.Sprintf("%.1f s", s)
	case s < 120:
		return fmt.Sprintf("%.0f s", s)
	default:
		return fmt.Sprintf("%d min %d s", int(s)/60, int(s)%60)
	}
}

// ClearedText is the headline of a cleared edit, for a flash, a card or
// a summary: "No logically significant change found (0.3 s)" when the
// gate cleared it, "No conflict found (1.9 s)" when triage did. The stage
// lines (StageLine) say how each stage judged it.
func ClearedText(st proposal.Stage) string {
	took := Seconds(time.Duration(st.LatencyMS) * time.Millisecond)
	if st.Name == proposal.StageGate {
		return "No logically significant change found (" + took + ")"
	}
	return "No conflict found (" + took + ")"
}

// plural is "1 finding", "2 findings".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
