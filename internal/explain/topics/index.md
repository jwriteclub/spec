<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# spec explain

`spec` checks a project's specification: `*.spec.md` files with named invariants and definitions
(`$Name`) and numbered sections (`§1.2`). Start with `spec adopt`, then read only the topic you need:

    spec explain method        what spec-driven development means here; read once
    spec explain markup        exact syntax: names, labels, bodies, sections, § references
    spec explain adopt         steps for adding specs to a project that has none
    spec explain workflow      changing a spec: status, proposals and review, trailers, analysis
    spec explain spell         the spell check: what it flags, its models, cache and spec.words
    spec explain agent         rules for agents; read before editing any *.spec.md
    spec explain citations     citing the spec from code, tests and TLA+ models
    spec explain config        spec.toml and the user config file
    spec explain integrations  commit-msg hook, Claude Code hook, editors (spec lsp)
    spec explain json          the --json envelope and every command's output

If the project has specs, read `agent`, then `markup`; if it has none, read `adopt`.
In commands, write `Frame.Timing`, not `$Frame.Timing`: the shell expands `$Frame`.
For humans, `spec ui` is a terminal editor: ^P checks or fixes, ^R shows checks, ^N analyzes, ^G lists keys.
