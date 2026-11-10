<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

You redo the review of one proposed change to a software specification, in light of the author's
notes. The specification is a set of Markdown files (*.spec.md). In them:
- `**$Name.**` or `**$Name (title).**` at the start of a paragraph defines an invariant: a property the
  system MUST keep. `**Def $Name (title).**` defines a definition: vocabulary the invariants rely on.
  A heading `## $Name` or `## Def $Name: title` defines one too, covering the text up to the next
  heading of the same or a higher level.
- `$Name` anywhere else is a reference to that invariant or definition.
- `## 5.2 Title` is a numbered section and `§5.2` (or `§file:5.2`) refers to it.

You are given what the first review was given (the action, the anchor's committed text as "Current
text" and the author's edit as "Proposed text", the author's reason, the related anchors, the prose that mentions it, an index of every anchor, and
the text of the files), then the first review: its summary, its findings and suggested fixes with the
author's decision on each, and its questions. Last come the author's notes, often an answer to one of
the review's questions.

The notes are the author's latest intent. Where they disagree with the proposed text, the notes win:
the proposed text, and everything that follows from it, must change to match them. Where they say
nothing, the proposed text stays the intent, as in the first review.

Answer three questions in light of the notes:
- What must change so the specification agrees with the author's intent: anchors and prose that
  reference the changed anchor, repeat its numbers or describe it, and anchors that only make sense
  with the old text?
- Does the author's own edit itself need to change to follow the notes?
- Is anything still ambiguous or contradictory that the notes do not settle?

Before answering, build an inventory of every number, duration, size, count, limit and unit the notes
and the proposed text state, and find every other place that states the same quantity; any
disagreement is a `numeric_mismatch`. Then check the logic. The inventory is for your own reasoning;
do not include it.

Rules:
1. Report only what the change and the notes cause or reveal. Do not report style, wording you would
   prefer, formatting, organization, or unrelated problems elsewhere.
2. The files you are given are the current text: fixes the author accepted are already in them. Never
   propose an accepted fix again. Do not propose a fix the author skipped again unless the notes ask
   for it. Every `find` quotes the current text.
3. To propose a follow-on edit, give `find` and `replace`. `find` MUST be an exact substring of the
   named file, copied character for character from the provided text, Markdown markup included. If
   you cannot quote the exact text, leave out `find` and `replace` and explain instead.
4. Make `find` long enough to occur exactly once in that file: at least the whole sentence, list item
   or table row. Never span two paragraphs.
5. `replace` is the corrected version of `find`. Keep the edit minimal: change only the value or phrase
   that has to follow, and copy everything else unchanged.
6. The changed anchor is the author's own edit. Change it only when the notes require it, and then
   only in the `primary` block, never in `findings`: `find` is an exact substring of the proposed text
   (which is what the files hold now), `replace` its corrected version, `explanation` one sentence on
   how the notes change it. Leave `primary` out (or null) when the author's edit stands.
7. Carry the intent through the whole specification: every other place that states the same quantity
   or rule gets its own finding with its own `find`/`replace` (usually `missing_update`), including
   places the first review never flagged. Findings and edits are many to many: a finding resolved by
   edits on other findings lists them in `fixed_by` (0-based indices, in `findings`, of findings that
   have `find` and `replace`); leave `fixed_by` out when no edit resolves the finding.
8. If a fix could still go more than one way after the notes, do not guess: give the finding without
   `find`/`replace` and add a question to `questions`. Never ask again what the notes answer.
9. Severity: `breaking` when the specification cannot hold as it stands (a direct contradiction, a
   reference to a removed anchor), `inconsistent` when other text will mislead a reader until updated,
   `cosmetic` when only names or wording drift and the meaning is the same.

Answer with one JSON object and nothing else (no prose, no code fences). Shape:

{"summary": "1-3 sentences: what the notes change, and what else must change",
 "primary": {"find": "exact text of the changed anchor", "replace": "its corrected version",
             "explanation": "one sentence: how the notes change the author's edit"},
 "findings": [{"file": "path as given in the file tag",
               "anchor": "$Name of the anchor that holds the text, or empty",
               "issue": "contradiction | numeric_mismatch | stale_reference | terminology_drift | ambiguity | missing_update",
               "severity": "breaking | inconsistent | cosmetic",
               "explanation": "what disagrees with what, quoting both places",
               "find": "exact text to replace (optional)",
               "replace": "corrected text (optional)",
               "fixed_by": ["optional: 0-based indices of the findings whose edits resolve this one"]}],
 "questions": ["a question for the author, when the right fix is still unclear"]}

`primary` is optional: leave it out, or null, when the author's edit stands.
