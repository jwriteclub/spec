// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jwriteclub/spec/internal/config"
)

// Gate scores one edit on yes/no questions and returns a probability per
// question; it does not chat. It decides whether the expensive check runs
// (notes/plan-step5-check-pipeline.md, stage 1). *Decisions (kind decisions) and
// *Canned (kind canned) implement it.
type Gate interface {
	// Decide asks req's questions about req's situation. A provider
	// failure, or a reply without a score for every question, is an
	// *Error; the context's own cancellation is returned as is.
	Decide(ctx context.Context, req GateRequest) (*Decision, error)
	// Info describes the gate as configured.
	Info() Info
	// SetLimits bounds each attempt and the number of attempts.
	SetLimits(timeout time.Duration, attempts int)
}

// Question ids of the per-edit gate. The class questions say the edit
// introduces a problem; material_change says it changes a claim at all.
const (
	QuestionMaterialChange  = "material_change"
	QuestionNumericMismatch = "numeric_mismatch"
	QuestionContradiction   = "contradiction"
	QuestionStaleReference  = "stale_reference"
)

// ClassQuestions are the questions whose scores escalate an edit.
var ClassQuestions = []string{QuestionNumericMismatch, QuestionContradiction, QuestionStaleReference}

// Question is one yes/no question: its id and the instructions that say
// what yes means and when to score near 0.
type Question struct {
	ID           string
	Instructions string
}

// GateRequest is one decision: the standing policy (what a spec is, what
// the situation holds), the situation (the edit and its neighbourhood, as
// specctx renders it) and the questions.
type GateRequest struct {
	// Model overrides the gate's model; empty uses it.
	Model string
	// Purpose says what the decision is for: "" (or PurposeGate) is the
	// per-edit gate, PurposeSpellGate the spell check's. Only the canned
	// provider reads it.
	Purpose   string
	Policy    string
	Situation string
	Questions []Question
}

// Decision is a gate's answer.
type Decision struct {
	// Scores is the probability of yes per question id, each in [0, 1].
	Scores map[string]float64
	// Provider is the gate's config name.
	Provider string
	// RequestedModel is the model id sent; Model the one that answered
	// ("" when the reply named none).
	RequestedModel string
	Model          string
	// Upstream is who served the request behind the router ("TypeSafe"),
	// when the reply says.
	Upstream string
	// ID is the reply's id, when it has one.
	ID    string
	Usage Usage
	// CostUSD is the cost the provider reported (0 when it reported none).
	CostUSD float64
	// Latency is the wall time of the whole call, retries included.
	Latency time.Duration
	// Attempts is how many HTTP requests were sent.
	Attempts int
}

// MaxClass returns the highest class-question score and whether any class
// question was scored.
func (d *Decision) MaxClass() (float64, bool) {
	return maxScore(d.Scores, ClassQuestions)
}

// GateOutcome is what a gate rule makes of a decision.
type GateOutcome struct {
	// Escalate says the edit goes on to the next stage.
	Escalate bool
	// Reason says why, for a log line or a status badge.
	Reason string
}

// Escalates applies the gate rule of g to scores: an edit escalates when
// any class question scores at or above g.EscalateAt(), unless the material
// skip is on (SkipMaterialBelow > 0) and material_change scores below it.
// Scores with no class question escalate: the gate fails open.
func Escalates(g config.Gate, scores map[string]float64) GateOutcome {
	cls, ok := maxScore(scores, ClassQuestions)
	if !ok {
		return GateOutcome{Escalate: true, Reason: "no class question was scored; failing open"}
	}
	if g.SkipMaterialBelow > 0 {
		if m, ok := scores[QuestionMaterialChange]; ok && m < g.SkipMaterialBelow {
			return GateOutcome{Reason: fmt.Sprintf("material_change %.2f < %.2f: editorial", m, g.SkipMaterialBelow)}
		}
	}
	at := g.EscalateAt()
	if cls >= at {
		return GateOutcome{Escalate: true, Reason: fmt.Sprintf("class score %.2f >= %.2f", cls, at)}
	}
	return GateOutcome{Reason: fmt.Sprintf("every class score < %.2f (max %.2f)", at, cls)}
}

// maxScore is the highest score among ids, and whether any was present.
func maxScore(scores map[string]float64, ids []string) (float64, bool) {
	best, any := 0.0, false
	for _, id := range ids {
		v, ok := scores[id]
		if !ok {
			continue
		}
		if !any || v > best {
			best = v
		}
		any = true
	}
	return best, any
}

// OpenGate builds the gate called name from its config. Kind jev is a
// gate, and kind canned serves its [[gate]] replies; a missing API key
// variable is a *config.EnvError.
func OpenGate(name string, p config.Provider, getenv func(string) string) (Gate, error) {
	if p.KindOrDefault() == config.KindCanned {
		return NewCanned(name, p, getenv)
	}
	if !p.IsGate() {
		return nil, fmt.Errorf("provider %q: kind %q is not a gate (only kinds %s and %s are)", name, p.KindOrDefault(), config.KindDecisions, config.KindCanned)
	}
	return NewDecisions(name, p, getenv)
}

// questionIDs returns the ids of qs, for an error message.
func questionIDs(qs []Question) []string {
	out := make([]string, 0, len(qs))
	for _, q := range qs {
		out = append(out, q.ID)
	}
	slices.Sort(out)
	return out
}
