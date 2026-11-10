<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Integrations

Two hooks: a git `commit-msg` hook that refuses a commit whose message lacks the required trailers,
and a Claude Code hook that runs `spec status` after an agent edits a spec file. Neither is installed
by `spec init`; ask the user before installing either. Editors connect through the language server
(last section).

## git commit-msg hook

`.git/hooks/commit-msg` (make it executable with `chmod +x .git/hooks/commit-msg`):

    #!/bin/sh
    # Require Spec-Change:/Spec-Rename: trailers for changed anchors (spec explain workflow).
    if ! command -v spec >/dev/null 2>&1; then
        echo "commit-msg: spec not found on PATH; spec trailers not checked" >&2
        exit 0
    fi
    # SPEC_BASE=HEAD^ git commit --amend: see "Amending" below.
    exec spec check --message-file "$1" --base "${SPEC_BASE:-HEAD}"

`spec check --message-file F` compares the staged spec with `HEAD` and exits 4, printing exactly the
missing trailer lines, when the message lacks one. Git runs hooks from the top of the work tree; if
`spec.toml` is in a subdirectory, add `--project path/to/dir`. With a hook manager (lefthook, husky,
pre-commit), register the same command for the `commit-msg` stage. `git commit --no-verify` skips the
hook; agents never use it.

**Amending.** The hook cannot detect `git commit --amend`; it compares with `HEAD`, the commit being
amended. When amending a commit that already changed anchors, the trailers must cover the change since
`HEAD^`: get them with `spec status --base HEAD^`, and pass the base to the hook yourself:
`SPEC_BASE=HEAD^ git commit --amend`.

## Claude Code PostToolUse hook

Runs `spec status --json` after every edit to a `*.spec.md` file and hands the result to the agent,
so it learns what its edit changed without having to ask. Save as `.claude/hooks/spec-status.sh`
(executable; needs `jq`):

    #!/bin/sh
    # After an edit to a *.spec.md file, give the agent the output of spec status.
    f=$(jq -r '.tool_input.file_path // empty')
    case "$f" in
        *.spec.md) ;;
        *) exit 0 ;;
    esac
    out=$(cd "$(dirname "$f")" && spec status --json 2>&1)
    jq -n --arg out "$out" '{hookSpecificOutput: {hookEventName: "PostToolUse",
        additionalContext: ("spec status --json after editing a spec file:\n" + $out)}}'

and register it in `.claude/settings.json`:

    {
      "hooks": {
        "PostToolUse": [
          {
            "matcher": "Edit|MultiEdit|Write",
            "hooks": [
              { "type": "command", "command": "\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/spec-status.sh" }
            ]
          }
        ]
      }
    }

`spec status` finds the project by walking up from the edited file's directory. Its `data.next`
(`spec explain json`) tells the agent what to do next.

## Editors: `spec lsp`

`spec lsp` is a language server on stdin and stdout. It gives any LSP editor diagnostics (lint, plus
an information note on each anchor changed since `HEAD`), hover, go to definition, references,
highlights, rename across files, completion after `$`, `§` and `#` (a picked section always inserts
`§`), quick fixes for `section-hash-ref` and `ref-nearly`, an outline and workspace symbols. It
finds the project by walking up from each file to `spec.toml`; files outside a project get nothing.
It asks the editor to report changes to Markdown files, models and `spec.toml`, so files created or
deleted while it runs count at once; an editor that cannot register for that sees them at the next edit.
Logs go to stderr; set `SPEC_LSP_LOG=debug` for more. For TLA+ models it adds the links in the next
section.

Semantic tokens use standard types, so themes color them without configuration:

    invariant $Name        variable
    definition $Name       type
    section number, § ref  namespace
    definition site        + declaration modifier

The editors below attach the server to every Markdown and TLA+ file, so highlighting and settings
keep working; it answers only for the files a `spec.toml` project selects and ignores the rest.

Neovim (0.11+), in `init.lua`:

    vim.lsp.config('spec', { cmd = { 'spec', 'lsp' }, filetypes = { 'markdown', 'tla' }, root_markers = { 'spec.toml' } })
    vim.lsp.enable('spec')

Helix, in `languages.toml` (keep any Markdown servers you already use in the list):

    [language-server.spec]
    command = "spec"
    args = ["lsp"]

    [[language]]
    name = "markdown"
    language-servers = ["marksman", "spec"]

VS Code: install a generic LSP client such as "Generic LSP Client (v2)" (`zsol.vscode-glspc`) and set
`"glspc.server.command": "spec"`, `"glspc.server.commandArguments": ["lsp"]` and
`"glspc.server.languageId": ["markdown", "tlaplus"]`.

Zed starts language servers only from extensions, and there is no spec extension yet; until there
is, run `spec lint` from a task.

### TLA+ models

With `formal` in `spec.toml` and tags in the models (`spec explain citations`), the links work both
ways, on `$Name` and on `§` numbers, and follow unsaved edits on either side:

    From a tag or tagged declaration   hover: the cited spec text, labelled "spec"
                                       go to definition: the anchor or section
    From $Name, §4.2 or a heading's    hover: adds "Model: tla/F.tla:Op" lines
    number in the spec                 go to implementation: each operator that cites it
    Either side                        references: spec references and tags together
    After "Spec:" in a \* line         completion of $ and §
    Rename from the spec               rewrites tags, in models that are not open too

`Derived-from:` tags show in italics, as one line, and mutants come last.

The server is built to run beside a TLA+ plugin such as vscode-tlaplus, so on a model it answers only
on tags and declaration names, and sends no semantic tokens or symbols. It does publish syntax errors
(`formal-syntax`, from prudence, a Go port of SANY) and semantic errors (`formal-semantic`, checked
with the modules the model extends) with the tags' findings. Hover on an operator's use, and
go to definition from one, stay with the plugin; use the declaration, or go to implementation from the
spec. Neovim's default key for that is `gri`; in VS Code it is "Go to Implementations". Both merge hovers
from several servers.

Neovim and VS Code are the target clients. Helix needs `spec` added to a `tla` language entry, as
for Markdown above; Helix and other clients are unverified.

`spec ui` opens models too, with the same links on the same keys as the rest of the editor:

    From $Name, §4.2 or a heading's    hover or ^L: adds "Model: tla/F.tla:Op" and the tag's quote
    number in the spec                 ^T on a definition or heading: to the operator, or a list of them
                                       ^T on a reference: to its definition, as ever; ^T again goes on
    From a tag or tagged declaration   hover or ^L: the cited spec text; ^T: to the spec
    Anywhere                           ^B back, ^\ references with tags, ^Q finds operators

It highlights TLA+ with prudence's lexer (nested comments as SANY reads them), underlines syntax
errors as you type (`formal-syntax`, after the short pause before a reparse) and semantic errors
such as an undefined name (`formal-semantic`), shows on the status line how far prudence's check
got (a model too large for the acquisition limit says so there and as hint `formal-check-limit`;
`spec ui --max-acquisition-visits N` raises it), draws a tag's reference
as a link, and lists each model's operators in the outline, filled when tagged. While a syntax error
stands, the operators after it stay, from the model's last scan without one: moved with the edit,
links intact. An operator whose name or bounds the edit touches stays in the outline dimmed, with no
jump or links, until the error is fixed.
`spec ui tla/F.tla:12` opens one.

A model edits and saves directly: ^O writes it, with no trailer, card or proposal, and outside edits
reload as for a spec file. An agent's edit of a model (`spec change --tla`) arrives as a card
naming the model, the definitions touched, the check and the diff: y applies it into the model's
buffer as a whole (a second y when the model check reports errors), ^O records it, n rejects it,
Enter reviews it. ^P, ^F, ^R, ^N, the gutter diff and commit are off on a model, dimmed in the help
bar, with the reason in ^G; review model changes with git. On a use of an operator, or anything else
without a tag, hover and ^T do nothing; that is left to the TLA+ plugin in your other editor.
