<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Gate: policy and questions

The per-edit cascade's first stage (kind decisions). The `policy` block is sent as `state.policy`, the
rendered edit as `state.situation`, and each question block as one yes/no (`noul`) question with
that id. These are the texts measured in notes/evals/2026-10-30-gate-classifier.md and on the
corpus. An override keeps this layout: a `## name` heading, a blank line, a `text` code block.

## policy

```text
A spec is a Markdown document of definitions (**Def $Name (title).**) and invariants (**$Name (title).**). Other text cites them as $Name, and cites sections as §N. Normative statements use MUST, MUST NOT, SHOULD and MAY. A spec must be internally consistent: a number, unit, condition or obligation stated in two places must agree, and every citation of $Name must agree with what the cited anchor says. A "Known violated today" note describes the current code, not the spec, so it is not an inconsistency.

The situation is ONE edit to the spec: either a proposed change to one anchor (current text and proposed text) or an edited region (now and before). After it come the related anchors (anchors the edited text cites or that cite it, up to two hops) and the prose that mentions the edited anchor. The spec was consistent before the edit. Judge only what the edit introduces, against the related text shown.
```

## material_change

```text
Does the new text change what the spec requires, permits, defines or states as fact (any number, unit, condition, obligation level, negation, scope or referenced anchor), as opposed to only wording, grammar, punctuation or formatting? Score near 0 for editorial edits that keep every claim the same: inserting a filler or linking word ("is", "it is", "the", "that", "also"); a synonym with the same meaning ("begins" to "starts", "start" to "begin", "ensures" to "guarantees", "must" to "MUST"); punctuation that does not change what attaches to what (an Oxford comma, a semicolon to a full stop); splitting or joining sentences without changing the claims; bold, italics, or a list rewritten as prose; reordering items whose order carries no meaning; fixing a spelling typo. Score high for: any changed number or unit; adding or dropping "not"; MUST to SHOULD or MAY (or back); punctuation that moves a number onto a different quantity; a new or removed requirement, condition or test; a changed citation.
```

## numeric_mismatch

```text
Does the new text state a number, duration, count, size or unit for a quantity that disagrees with the value the related anchors or mentioning prose state for the SAME quantity (including arithmetic a definition implies, such as a frame's duration in ticks at a given frame rate)? Score near 0 when: the same number is used for two different quantities (for example two unrelated 30 s limits); the edited number is not stated or cited anywhere else in the situation; the new value matches the value in the related text (both sides were updated together); the edit does not touch a number.
```

## contradiction

```text
Does the new text require, permit or state something that cannot hold at the same time as a requirement or definition in the related anchors or mentioning prose, so that no implementation could satisfy both? Score near 0 when: the edit only adds detail that is compatible with the related text; the related text marks the behaviour "Known violated today" (that describes code, not the spec); the two statements are about different things; the edit only changes wording.
```

## stale_reference

```text
After this edit, does some citation ($Name or §N) point at an anchor that does not say what the citing text claims? This covers a citation in the new text that names the wrong anchor for its topic or describes the cited anchor differently from what it says, and related text or prose that cites the edited anchor and now describes its old behaviour. Score near 0 when: every cited anchor says what the citing text claims; the citing text and the cited anchor were updated consistently; the edit adds or changes no citation and leaves every citing passage true.
```
