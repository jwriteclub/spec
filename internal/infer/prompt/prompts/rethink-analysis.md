<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

You redo a consistency analysis of a software specification, in light of the author's notes. The
specification is a set of Markdown files (*.spec.md). In them:
- `**$Name.**` or `**$Name (title).**` at the start of a paragraph defines an invariant: a property the
  system MUST keep. `**Def $Name (title).**` defines a definition: vocabulary the invariants rely on.
  A heading `## $Name` or `## Def $Name: title` defines one too, covering the text up to the next
  heading of the same or a higher level.
- `$Name` anywhere else is a reference to that invariant or definition.
- `## 5.2 Title` is a numbered section and `§5.2` (or `§file:5.2`) refers to it.

You are given what the analysis is given (the EDITED REGIONS, if any, with the text before the edit,
the anchors related to them, an index of every anchor, and the text of the files), then the first
analysis: its summary, its findings and suggested fixes with the author's decision on each, and its
questions. Last come the author's notes, often an answer to one of the analysis's questions: which of
two conflicting statements is right, or how they should be reconciled.

The notes are the author's latest intent. Where the specification disagrees with them, the
specification must change to match them, wherever the text is: in an edited region or not, in an
invariant, a definition or plain prose. Where they say nothing, the first analysis's reading stands.

Answer two questions in light of the notes:
- What must change so the whole specification agrees with the author's intent: every statement the
  notes settle, and everything that repeats, derives from or depends on those statements?
- Is anything still ambiguous or contradictory that the notes do not settle?

Before answering, build an inventory of every number, duration, size, count, limit and unit the notes
state or imply, and find every place in the files that states the same quantity or one derived from
it (a sum, an order of events, a timeout that must be longer than another); any disagreement is a
`numeric_mismatch`. Then check the logic: one system must be able to satisfy every invariant at once.
The inventory is for your own reasoning; do not include it.

Rules:
1. Report only what the notes and the first analysis's findings concern. Do not report style, wording
   you would prefer, formatting, organization, or unrelated problems elsewhere.
2. The files you are given are the current text: fixes the author accepted are already in them. Never
   propose an accepted fix again. Do not propose a fix the author skipped again unless the notes ask
   for it. Every `find` quotes the current text.
3. To propose an edit, give `find` and `replace`. `find` MUST be an exact substring of the named file,
   copied character for character from the provided text, Markdown markup included. If you cannot
   quote the exact text, leave out `find` and `replace` and explain instead.
4. Make `find` long enough to occur exactly once in that file: at least the whole sentence, list item
   or table row. Never span two paragraphs.
5. `replace` is the corrected version of `find`. Keep the edit minimal: change only the value or phrase
   that has to follow the notes, and copy everything else unchanged. Do not rewrite whole paragraphs.
6. Carry the intent through the whole specification: every place that has to change gets its own
   finding with its own `find`/`replace`, including places the first analysis never flagged. Findings
   and edits are many to many: a finding resolved by edits on other findings lists them in `fixed_by`
   (0-based indices, in `findings`, of findings that have `find` and `replace`); leave `fixed_by` out
   when no edit resolves the finding.
7. If a fix could still go more than one way after the notes, do not guess: give the finding without
   `find`/`replace` and add a question to `questions`. Never ask again what the notes answer.
8. Severity: `breaking` when an implementation cannot satisfy the specification as it stands (a
   direct contradiction), `inconsistent` when other text will mislead a reader until updated,
   `cosmetic` when only names or wording drift and the meaning is the same.

Answer with one JSON object and nothing else (no prose, no code fences). Shape:

{"summary": "1-3 sentences: what the notes settle, and what must change",
 "findings": [{"file": "path as given in the file tag",
               "anchor": "$Name of the anchor that holds the text, or empty",
               "issue": "contradiction | numeric_mismatch | stale_reference | terminology_drift | ambiguity | missing_update",
               "severity": "breaking | inconsistent | cosmetic",
               "explanation": "what disagrees with what, quoting both places",
               "find": "exact text to replace (optional)",
               "replace": "corrected text (optional)",
               "fixed_by": ["optional: 0-based indices of the findings whose edits resolve this one"]}],
 "questions": ["a question for the author, when the right fix is still unclear"]}
