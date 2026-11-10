<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Setup

How to configure a project for `spec` by hand, and how to work on it with an agent.

## Quick start

Install Git for change tracking and proposals. `spec init` does not run `git init`, so in a new
project run `git init` first. Then, in the repository that will hold the spec:

    spec init

This writes `spec.toml`, a skeleton `NAME.spec.md` (NAME defaults to the directory name, lowercased) and a
`.spec/` line in `.gitignore`, and prints a suggested CLAUDE.md section. The skeleton shows a
definition, an invariant and every reference form. Run `spec lint` to check it.

`spec init` does not install the `commit-msg` hook that requires `Spec-Change:` trailers on commits.
`spec explain integrations` shows the hook and how to install it.

An invariant looks like this:

    **$Key.Equals (equals with nothing pending).** An invariant states one property that MUST hold, in
    the words of the definitions: pressing `=` with no $Op.Pending operation MUST change nothing.
    Checked by: `internal/calc/keys_test.go:TestEqualsNothingPending`.

Open the editor:

    spec ui

The keys are nano-style, and ^G lists them all. The ones used most:

- ^O saves and ^X exits.
- ^P checks the anchor under the cursor against the rest of the spec, or fixes the diagnostic under
  it.
- ^N analyzes everything you changed since `HEAD`.
- ^R opens the Checks sidebar, where findings and proposals wait; y applies a fix, n dismisses it,
  and A opens the commit view.

Editing, `spec lint` and the TLA+ syntax, semantic and level checks work without a model.
Contradiction checks and `spec analyze` need one.

Connecting a model provider: see [providers.md](providers.md).

`spec` keeps those files logically consistent:

- `spec lint` checks names, references and sections; `spec status` and the `commit-msg` hook make
  every change to an anchor show up in the commit as a `Spec-Change:` trailer.
- A change to an anchor gets a contradiction check by a model, and an agent's change becomes a
  proposal that a human reviews and approves.
- `spec analyze` compares the edited text with the whole spec: numbers stated in two places,
  citations against what they cite, invariants about the same thing, and spelling.
- TLA+ models link to the spec with `\* Spec: $Name "quote"` tags. `spec` checks a model's syntax,
  semantics and levels with prudence, a Go port of the SANY parser. Run TLC yourself to check a
  model's behaviour.
- `spec ui` is a terminal editor that knows the markup, and `spec lsp` serves any LSP editor.

## Working with an agent

Add the section `spec init` printed to the project's CLAUDE.md (or your agent's equivalent). It
tells the agent to read `spec explain agent` before touching a spec. The agent follows these steps:

1. Reads the spec with `spec ls`, `spec show Key.Equals` and `spec refs Key.Equals`, writing
   names without the `$` in shell commands.
2. Edits `*.spec.md` files with its normal tools, then runs `spec status` and does what its `next:`
   line says.
3. Files each anchor it changed for review, and waits for your decision:

       spec propose Key.Equals --why "= with nothing pending must change nothing" --by agent-7 --wait 30m

`spec propose` puts the old text back in the working tree while the proposal waits. The proposal
appears as a card in your `spec ui` Checks sidebar, with the model's findings and any follow-on fixes
it suggests. y applies it to the buffer and ^O saves it; n rejects it with a reason. In a terminal,
`spec review ID` does the same.
The agent's command exits 0 when the change is applied and 3, with your reason, when it is rejected.
Agents never approve or reject proposals themselves; that is your job.

An agent proposes an edit of a TLA+ model the same way, with
`spec change --tla tla/Model.tla --body-file NEW.tla --why W`.

## Concepts

`spec explain` prints the index, and `spec explain TOPIC` prints one topic:

| Topic          | What it covers                                                        |
|----------------|-----------------------------------------------------------------------|
| `method`       | spec-driven development as `spec` practises it; read once             |
| `markup`       | exact syntax: names, labels, bodies, sections, `§` references         |
| `adopt`        | steps for adding specs to a project that has none                     |
| `workflow`     | changing a spec: status, proposals and review, trailers, analysis     |
| `spell`        | the spell check: what it flags, its models, cache and `spec.words`    |
| `agent`        | rules for agents; read before editing any `*.spec.md`                 |
| `citations`    | citing the spec from code, tests and TLA+ models                      |
| `config`       | `spec.toml` and the user config file                                  |
| `integrations` | the `commit-msg` hook, the Claude Code hook, editors (`spec lsp`)     |
| `json`         | the `--json` envelope and every command's output                      |
