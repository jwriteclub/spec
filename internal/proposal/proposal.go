// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package proposal stores reviewable spec changes under .spec/proposals and
// turns an approved proposal into byte-range splices.
//
// A proposal is one JSON file, <id>.json, written atomically; proposal files
// need no lock. A `change` proposal has a primary hunk (the anchor added,
// changed or removed) plus follow-on finding hunks from the contradiction
// check; an `analysis` proposal has finding hunks only, and so has a `fix`
// proposal (the edits that fix one lint finding); a `model` proposal edits
// one formal model, with hunks applied as a whole. When a proposal is
// approved or rejected it stays on disk as a receipt (status and decision)
// until it is acknowledged or ReceiptTTL old, so a process that was waiting
// for it, or that restarted, can still learn the outcome.
package proposal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/formal"
)

// Kinds.
const (
	KindChange   = "change"
	KindAnalysis = "analysis"
	KindFix      = "fix"
	// KindModel edits one formal model (model.go).
	KindModel = "model"
)

// Statuses.
const (
	StatusPending  = "pending"
	StatusApplied  = "applied"
	StatusRejected = "rejected"
)

// AuthorHuman is the default display author for explicitly human work.
const AuthorHuman = "human"

// ReceiptTTL is how long a decided proposal is kept unacknowledged.
const ReceiptTTL = 7 * 24 * time.Hour

// Placement says where an added anchor goes. It is resolved against the
// file when the anchor is inserted: after the anchor After, else after the
// first anchor of Earlier present, else after the unique line AfterLine,
// else at the end of Section, else (AtStart) at the start of the file. Sep
// is the whitespace written between the preceding text and the anchor
// (after the anchor when AtStart).
type Placement struct {
	File  string `json:"file"`
	After string `json:"after"`
	// Earlier are the anchors before After in an unbroken run, nearest
	// first; one of them is used when After is absent.
	Earlier   []string `json:"earlier"`
	AfterLine string   `json:"after_line"`
	Section   string   `json:"section"`
	AtStart   bool     `json:"at_start"`
	Sep       string   `json:"sep"`
}

// Primary is the change itself: hunk N (always 1) of a change proposal.
type Primary struct {
	N int `json:"n"`
	// Action is change, add, remove or kind (specctx actions).
	Action string `json:"action"`
	// Name carries the $ sigil.
	Name    string `json:"name"`
	File    string `json:"file"`
	OldKind string `json:"old_kind"`
	NewKind string `json:"new_kind"`
	// OldText is the anchor's exact text in the file the proposal applies
	// to ("" for add); applying verifies it is still there.
	OldText string `json:"old_text"`
	// NewText replaces it ("" for remove).
	NewText string `json:"new_text"`
	// Placement is set for add.
	Placement *Placement `json:"placement"`
	// InWorktree is true for `spec propose --keep`: the change is already
	// in the working tree, and approving it writes nothing.
	InWorktree bool `json:"in_worktree"`
}

// Failure is a hunk that could not be applied.
type Failure struct {
	N      int    `json:"n"`
	Reason string `json:"reason"`
}

// Decision is how a proposal was decided.
type Decision struct {
	// Accepted are the hunk numbers approved (empty on reject).
	Accepted []int `json:"accepted"`
	// Applied are the hunks written; Failed those that were stale.
	Applied []int     `json:"applied"`
	Failed  []Failure `json:"failed"`
	// Reason is the rejection reason (or why the proposal failed).
	Reason string    `json:"reason"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	// Commit is the commit made by approve --commit, else "".
	Commit string `json:"commit"`
}

// Fix is the lint finding a fix proposal fixes.
type Fix struct {
	// Code is the lint rule.
	Code string `json:"code"`
	File string `json:"file"`
	// Line and Col are 1-based; Col counts bytes, as `spec lint` prints.
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Message string `json:"message"`
	// Class is how the fix was made: "mechanical" (computed, no model) or
	// "model".
	Class string `json:"class"`
}

// Inference records the model call(s) behind a proposal.
type Inference struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	EchoedModel      string `json:"echoed_model"`
	Calls            int    `json:"calls"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	LatencyMS        int64  `json:"latency_ms"`
	// Kind is the provider's kind (openai, claude-cli, codex-cli); absent in
	// proposals made before kinds existed.
	Kind string `json:"kind,omitempty"`
	// Effort is the effort level spec set, when it set one.
	Effort string `json:"effort,omitempty"`
	// CostUSD totals the cost the provider reported (claude-cli reports
	// it, and OpenRouter as usage.cost; most other OpenAI-compatible
	// endpoints do not).
	CostUSD float64 `json:"cost_usd,omitempty"`
	// RunawayGuard is true when a reply used the whole reply budget without
	// answering and was asked again with the provider's triage_extra.
	RunawayGuard bool `json:"runaway_guard,omitempty"`
	// Stages are the per-edit cascade's stages that ran, in order (gate,
	// triage, check); absent in proposals made before the cascade and in
	// analyses and fixes. The fields above are the check stage's.
	Stages []Stage `json:"stages,omitempty"`
	// ClearedBy names the stage that cleared the edit ("gate" or
	// "triage"); empty when the check ran.
	ClearedBy string `json:"cleared_by,omitempty"`
}

// Cascade stage names.
const (
	StageGate   = "gate"
	StageTriage = "triage"
	StageCheck  = "check"
)

// Stage outcomes.
const (
	// OutcomeRunning is a stage that has started (progress only; never
	// recorded).
	OutcomeRunning = "running"
	// OutcomeClear: the stage found nothing to check; the cascade stops.
	OutcomeClear = "clear"
	// OutcomePass: the stage passed the edit on to the next stage.
	OutcomePass = "pass"
	// OutcomeFailedOpen: the stage failed (timeout, HTTP error, a reply
	// that did not parse) and passed the edit on; Reason says why.
	OutcomeFailedOpen = "failed_open"
	// OutcomeDone: the check answered (Findings counts what it found).
	OutcomeDone = "done"
	// OutcomeFailed: the check failed (progress only: nothing is filed).
	OutcomeFailed = "failed"
)

// Stage is one stage of the per-edit cascade as it ran.
type Stage struct {
	// Name is StageGate, StageTriage or StageCheck.
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Scores are the gate's or triage's probability per question id.
	Scores    map[string]float64 `json:"scores,omitempty"`
	LatencyMS int64              `json:"latency_ms"`
	CostUSD   float64            `json:"cost_usd,omitempty"`
	// Outcome is one of the Outcome constants.
	Outcome string `json:"outcome"`
	// Reason says why, in words ("every class score < 0.45 (max 0.21)",
	// "HTTP 500 ...").
	Reason string `json:"reason,omitempty"`
	// Findings counts the check's hunks, notes and questions.
	Findings int `json:"findings,omitempty"`
}

// Base holds the hashes of what the proposal was made against: sha256 of
// each touched file's bytes, and the body hash of each touched anchor
// ("$Name" keys).
type Base struct {
	Files   map[string]string `json:"files"`
	Anchors map[string]string `json:"anchors"`
}

// StalePrimary records a primary that no longer applies. It is independent of
// a refreshable required-check snapshot and never decides the proposal.
type StalePrimary struct {
	Reason string `json:"reason"`
}

// Proposal is one reviewable change.
type Proposal struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Author  string    `json:"author,omitempty"`
	// SourceKind is explicit provenance, independent of author and text location.
	SourceKind      string            `json:"source_kind,omitempty"`
	Revision        int               `json:"revision,omitempty"`
	CheckOwner      string            `json:"check_owner,omitempty"`
	CheckGeneration int               `json:"check_generation,omitempty"`
	CheckState      string            `json:"check_state,omitempty"`
	CheckMessage    string            `json:"check_message,omitempty"`
	CheckFiles      map[string]string `json:"check_files,omitempty"`
	Kind            string            `json:"kind"`
	Why             string            `json:"why"`
	Summary         string            `json:"summary"`
	Primary         *Primary          `json:"primary"`
	StalePrimary    *StalePrimary     `json:"stale_primary,omitempty"`
	// Fix is set on a fix proposal.
	Fix *Fix `json:"fix"`
	// Model is set on a model proposal.
	Model     *Model         `json:"model,omitempty"`
	Hunks     []finding.Hunk `json:"hunks"`
	Notes     []finding.Note `json:"notes"`
	Questions []string       `json:"questions"`
	Base      Base           `json:"base"`
	Status    string         `json:"status"`
	Decision  *Decision      `json:"decision"`
	Inference *Inference     `json:"inference"`
	// Rethink is set on a check or an analysis redone with the author's
	// notes (an answer to its question): what it supersedes and the notes.
	Rethink *Rethink `json:"rethink,omitempty"`
	// Scope is set on an analysis: what it covered. A newer analysis that
	// covers the same scope supersedes an older one nobody has acted on.
	Scope *Scope `json:"scope,omitempty"`
}

// Scope is what an analysis covered.
type Scope struct {
	// Mode is "changed" (the text that differs from HEAD), "all" or
	// "files".
	Mode string `json:"mode"`
	// Files are the files of a "files" analysis.
	Files []string `json:"files,omitempty"`
	// Head is the commit a "changed" analysis compared against ("" when
	// there was none).
	Head string `json:"head,omitempty"`
}

// Rethink records how a check or an analysis was redone with the author's
// notes.
type Rethink struct {
	// Of is the check (or analysis) this one supersedes.
	Of string `json:"of"`
	// Notes are the author's notes, oldest first: a rethink of a rethink
	// keeps the earlier ones.
	Notes []RethinkNote `json:"notes"`
	// PrimaryHunk is the hunk that changes the author's own anchor (0 when
	// the rethink proposes none). Like every fix it is proposed, not applied.
	PrimaryHunk int `json:"primary_hunk,omitempty"`
}

// RethinkNote is one note the author sent with a rethink.
type RethinkNote struct {
	// Question is the check's question the note answers ("" for a note
	// that answers none).
	Question string `json:"question,omitempty"`
	Text     string `json:"text"`
}

// Normalize replaces nil lists and maps with empty ones (the JSON contract
// never has null arrays).
func (p *Proposal) Normalize() {
	if p.Hunks == nil {
		p.Hunks = []finding.Hunk{}
	}
	if p.Notes == nil {
		p.Notes = []finding.Note{}
	}
	if p.Questions == nil {
		p.Questions = []string{}
	}
	if p.Base.Files == nil {
		p.Base.Files = map[string]string{}
	}
	if p.Base.Anchors == nil {
		p.Base.Anchors = map[string]string{}
	}
	if m := p.Model; m != nil {
		if m.Decls == nil {
			m.Decls = []string{}
		}
		if m.Links == nil {
			m.Links = []LinkFinding{}
		}
		if m.Check.Diagnostics == nil {
			m.Check.Diagnostics = []formal.Diagnostic{}
		}
	}
	if p.Primary != nil && p.Primary.Placement != nil && p.Primary.Placement.Earlier == nil {
		p.Primary.Placement.Earlier = []string{}
	}
	if d := p.Decision; d != nil {
		if d.Accepted == nil {
			d.Accepted = []int{}
		}
		if d.Applied == nil {
			d.Applied = []int{}
		}
		if d.Failed == nil {
			d.Failed = []Failure{}
		}
	}
}

// Numbers returns every hunk number in order: the primary's, then the
// finding hunks'.
func (p *Proposal) Numbers() []int {
	var out []int
	if p.Primary != nil {
		out = append(out, p.Primary.N)
	}
	for _, h := range p.Hunks {
		out = append(out, h.N)
	}
	return out
}

// Decided reports whether the proposal is applied or rejected.
func (p *Proposal) Decided() bool {
	return p.Status == StatusApplied || p.Status == StatusRejected
}

// Expired reports whether a decided proposal's receipt is older than
// ReceiptTTL at now.
func (p *Proposal) Expired(now time.Time) bool {
	return p.Decided() && p.Decision != nil && now.Sub(p.Decision.At) > ReceiptTTL
}

// FileHash is the base hash of a file's bytes.
func FileHash(src []byte) string {
	sum := sha256.Sum256(src)
	return hex.EncodeToString(sum[:])
}

// ChecksBlocking reports an unfinished or failed required check. Legacy
// proposals without lifecycle metadata keep their existing behavior.
func (p *Proposal) ChecksBlocking() bool {
	return p.CheckState != "" && p.CheckState != "complete"
}

// AppliedHunk records actual successful application, not review selection.
func (p *Proposal) AppliedHunk(n int) bool {
	if p.Decision != nil {
		for _, done := range p.Decision.Applied {
			if done == n {
				return true
			}
		}
	}
	return false
}

// Clone makes an independent candidate, including nested placement and decision
// data, so normalization or a failed write never mutates the caller's state.
func (p *Proposal) Clone() (*Proposal, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var out Proposal
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CheckedTexts are the texts of a check's own anchor that the check stands
// for: the text it checked and, for a rethink that changes the author's
// edit, that text with the change made. Nil without a primary.
func (p *Proposal) CheckedTexts() []string {
	pr := p.Primary
	if pr == nil {
		return nil
	}
	out := []string{pr.NewText}
	if p.Rethink == nil || p.Rethink.PrimaryHunk == 0 {
		return out
	}
	for _, h := range p.Hunks {
		if h.N == p.Rethink.PrimaryHunk && h.Find != "" && strings.Count(pr.NewText, h.Find) == 1 {
			out = append(out, strings.Replace(pr.NewText, h.Find, h.Replace, 1))
		}
	}
	return out
}

// HumanSource honors explicit provenance before the legacy display-author fallback.
func (p *Proposal) HumanSource() bool {
	return p.SourceKind == "human" || p.SourceKind == "" && p.Author == AuthorHuman
}
