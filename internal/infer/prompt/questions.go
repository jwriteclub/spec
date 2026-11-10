// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package prompt

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jwriteclub/spec/internal/infer"
)

// Kinds of the per-edit cascade's question sets (prompts/gate.md and
// prompts/triage.md).
const (
	// Gate is the gate's policy and yes/no questions.
	Gate = "gate"
	// Triage is the triage stage's policy, questions and reply format.
	Triage = "triage"
)

// Questions is a question set: the standing policy, the four questions
// of the per-edit cascade (material_change first, then the class
// questions), and, for triage, the reply instruction.
type Questions struct {
	Policy    string
	Questions []infer.Question
	// Reply says how a chat model answers (triage only).
	Reply string
}

// questionBlock matches "## name" followed by a ```text block.
var questionBlock = regexp.MustCompile("(?ms)^## (.+?)\n\n```text\n(.*?)\n```")

// LoadQuestions returns the question set of kind (Gate or Triage): the
// file at override when it is not "", else the embedded one. The file
// holds a `## policy` block and one block per question id, each a `text`
// code block under its heading; triage also needs a `## reply` block.
func LoadQuestions(kind, override string) (*Questions, error) {
	text, err := Load(kind, override)
	if err != nil {
		return nil, err
	}
	blocks := map[string]string{}
	for _, m := range questionBlock.FindAllStringSubmatch(text, -1) {
		blocks[strings.TrimSpace(m[1])] = strings.TrimSpace(m[2])
	}
	where := "the embedded " + kind + " prompt"
	if override != "" {
		where = "the " + kind + " prompt override " + override
	}
	q := &Questions{Policy: blocks["policy"], Reply: blocks["reply"]}
	if q.Policy == "" {
		return nil, fmt.Errorf("%s has no \"## policy\" text block", where)
	}
	for _, id := range append([]string{infer.QuestionMaterialChange}, infer.ClassQuestions...) {
		text := blocks[id]
		if text == "" {
			return nil, fmt.Errorf("%s has no \"## %s\" text block", where, id)
		}
		q.Questions = append(q.Questions, infer.Question{ID: id, Instructions: text})
	}
	if kind == Triage && q.Reply == "" {
		return nil, fmt.Errorf("%s has no \"## reply\" text block", where)
	}
	return q, nil
}

// TriageMessages renders a triage request: the policy, the questions and
// the reply instruction as the system message, the situation (the edit and
// its neighbourhood) as the user message.
func TriageMessages(q *Questions, situation string) []infer.Message {
	var sys strings.Builder
	sys.WriteString(q.Policy)
	sys.WriteString("\n\nQuestions, each answered with the probability (0 to 1) that the answer is yes:\n")
	for _, x := range q.Questions {
		fmt.Fprintf(&sys, "\n## %s\n\n%s\n", x.ID, x.Instructions)
	}
	sys.WriteString("\n" + q.Reply + "\n")
	return []infer.Message{infer.System(sys.String()), infer.User(situation)}
}

// Spell is the spell check's prompt (prompts/spell.md).
const Spell = "spell"

// SpellPrompt is the spell check's policy, Jev question and triage reply
// instruction.
type SpellPrompt struct {
	Policy   string
	Question string
	Reply    string
}

// LoadSpell returns the spell check's prompt: the file at override when it
// is not "", else the embedded one. The file holds `## policy`, `##
// question` (with {id} for the candidate's number) and `## reply` text
// blocks.
func LoadSpell(override string) (*SpellPrompt, error) {
	text, err := Load(Spell, override)
	if err != nil {
		return nil, err
	}
	blocks := map[string]string{}
	for _, m := range questionBlock.FindAllStringSubmatch(text, -1) {
		blocks[strings.TrimSpace(m[1])] = strings.TrimSpace(m[2])
	}
	where := "the embedded spell prompt"
	if override != "" {
		where = "the spell prompt override " + override
	}
	p := &SpellPrompt{Policy: blocks["policy"], Question: blocks["question"], Reply: blocks["reply"]}
	for _, b := range []struct{ name, text string }{{"policy", p.Policy}, {"question", p.Question}, {"reply", p.Reply}} {
		if b.text == "" {
			return nil, fmt.Errorf("%s has no \"## %s\" text block", where, b.name)
		}
	}
	if !strings.Contains(p.Question, "{id}") {
		return nil, fmt.Errorf("%s: the question block must contain {id}", where)
	}
	return p, nil
}
