<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Spell check: policy, question and reply format

The spell check's model pass. Jev (the `[gate]` provider, kind decisions) gets the `policy` block as
`state.policy`, the candidates as `state.situation`, and the `question` block once per candidate as a
yes/no (`noul`) question `typo_N`, with `{id}` replaced by the candidate's number. The triage model
(the `[triage]` provider) gets the `policy` and `reply` blocks as its system message and the
candidates Jev flagged as its user message. These are the texts measured in the spell probe
(notes/plan-spellcheck.md). An override keeps this layout: a `## name` heading, a blank line, a `text`
code block.

## policy

```text
You are the second pass of a spell checker for Markdown spec documents about software systems. A dictionary spell checker has flagged some words it does not know. Most flags are legitimate: technical terms (fsync, goroutine), product and library names, protocol and file-format names, abbreviations, and the project's own identifiers (camelCase, snake_case) that are used consistently. Only a few are real typos. Each candidate shows the sentence with the flagged word between [[ and ]], how often the word is used in the project, the dictionary's suggestions, and similar project terms. A word is a typo when its writer meant a different, existing word or project term and mistyped it (including one misspelled part of a camelCase or snake_case identifier, or a misspelling of a project term that is used correctly elsewhere). A word that is used several times, or is a known technical term, is usually intentional. A rare word with a near-identical frequent project term is usually a typo.
```

## question

```text
Is the word marked [[ ]] in candidate {id} a typo: a misspelling of a real English word or of a project term, rather than an intentional technical term, name or identifier? Score near 0 for a known technical term, a name, or a term used consistently in the project.
```

## reply

```text
List ONLY the candidates whose marked word is a typo, one per line as ID=intended spelling, for example 3=delay. Keep the case and the identifier shape of the original (liveIdleTimout becomes liveIdleTimeout). No other text, no spaces, no explanation. If none is a typo, reply with a single -.
```
