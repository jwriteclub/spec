// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package scenes

import (
	"encoding/json"

	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/infer/prompt"
)

// The demo project of the UI tests (internal/tui harness_test.go).
const demoSpec = `# demo

## 1. Terms

**Def $Time.Media (media time).** Media time is the timestamp carried by a frame, in ticks of 1/90000 s.

## 2. Rules

**$Frame.Timing (fragment timing).** Every fragment MUST have a duration above zero in $Time.Media.
Checked by: TestFragment.

**$Frame.Order (frame order).** Fragments MUST arrive in order of $Time.Media, see $Frame.Timing.
Checked by: TestOrder.

Prose that mentions $Frame.Order for the reader.
`

const otherSpec = `# other

## 1. Extras

**$Extra.Limit (extra limit).** The extra queue MUST hold at most 8 items. Checked by: TestLimit.
`

// Finding is one finding of a canned check reply; Find and Replace make
// it a suggested edit, FixedBy links it to the findings whose edits
// resolve it.
type Finding struct {
	File, Anchor, Issue, Severity, Explanation string
	Find, Replace                              string
	FixedBy                                    []int
}

// CheckReply is a check's (or an analysis's, or a lint fix's) reply.
func CheckReply(summary string, questions []string, fs ...Finding) infer.CannedEntry {
	res := finding.Result{Summary: summary, Questions: questions, Findings: []finding.Finding{}}
	if res.Questions == nil {
		res.Questions = []string{}
	}
	for _, f := range fs {
		ff := finding.Finding{File: f.File, Anchor: f.Anchor, Issue: f.Issue, Severity: f.Severity, Explanation: f.Explanation, FixedBy: f.FixedBy}
		if f.Find != "" {
			find, replace := f.Find, f.Replace
			ff.Find, ff.Replace = &find, &replace
		}
		res.Findings = append(res.Findings, ff)
	}
	return infer.CannedEntry{Reply: mustJSON(res)}
}

// GateReply is a gate's scores: material_change 0.95, the class questions
// as given, and a reported latency of 0.3 s.
func GateReply(numeric, contradiction, stale float64) infer.CannedEntry {
	return infer.CannedEntry{LatencyMS: 300, Scores: map[string]float64{
		infer.QuestionMaterialChange: 0.95, infer.QuestionNumericMismatch: numeric,
		infer.QuestionContradiction: contradiction, infer.QuestionStaleReference: stale,
	}}
}

// TriageReply is a triage model's scores (material_change 1).
func TriageReply(numeric, contradiction, stale float64) infer.CannedEntry {
	return infer.CannedEntry{Reply: mustJSON(map[string]float64{
		infer.QuestionMaterialChange: 1, infer.QuestionNumericMismatch: numeric,
		infer.QuestionContradiction: contradiction, infer.QuestionStaleReference: stale,
	})}
}

// DraftReply is a drafted commit message.
func DraftReply(subject, body string) infer.CannedEntry {
	return infer.CannedEntry{Reply: mustJSON(prompt.CommitDraft{Subject: subject, Body: body})}
}

// Slow holds e back for ms milliseconds.
func Slow(e infer.CannedEntry, ms int) infer.CannedEntry {
	e.DelayMS = ms
	return e
}

// mustJSON encodes v: strings, numbers and slices of them, which always
// encode.
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
