<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Spell check

`spec analyze` spell-checks the text it analyzes: the regions that differ from HEAD by default,
every file with `--all`, or the named files. `spec propose` and `spec change` check the proposed text
of each anchor. Flags print like lint findings, one per word, after the other findings:

    demo.spec.md:12:9: spell: "recieve" may be misspelled: did you mean "receive"?

With `--json` they are `data.spell.flags` (`file`, `line`, `col`, `range`, `word`, `fix`, `by`), with
`data.spell.stats` (candidates, cached, asked, calls, cost) and `data.spell.error` when the model pass
failed. A flag never fails `analyze`, `propose` or `change`; `spec lint --spell` exits 4 only when
`[lint] spell = "error"` (see "Turning it off"). The check expects US spelling: `colour` is flagged,
with the fix `color`.

## How a word is judged

1. **Classical, on this machine.** The prose is split into words; code spans and blocks, URLs, paths,
   `$Name` references, `§` references, link targets, HTML comments and front matter are skipped.
   Identifiers are split into parts (`liveIdleTimeout`, `max_transcoders`, `Pool.EvictOldest`) and
   each part of 4 letters or more is checked; all-capital parts of up to 5 letters are acronyms. A
   known misspelling (golangci/misspell's list, with UK spellings mapped to US) is flagged with its
   fix at once. A word is accepted when it is in the system word list (with plurals, verb forms,
   common prefixes and two-word compounds), in `spec.words`, part of an anchor name or a code
   identifier, or used 3 or more times or in 2 or more files of the project.
2. **Jev** (the `[gate]` provider) scores the remaining words in context, 16 per request: the
   sentence, how often the project uses the word, near dictionary words and near project terms. A
   score of 0.7 or more flags the word; one between 0.3 and 0.7 is asked again on its own and
   flagged at 0.5.
3. **Triage** (the `[triage]` provider) gets only the words Jev flagged, in batches, and names the
   typos and their fixes as `ID=fix` lines.
4. **Fix guard.** A fix is kept only when it is within 2 edits of the word (for an identifier, of
   its misspelled part) and is a dictionary word or a project term; otherwise nothing is shown.

The model pass runs when the user config has both `[gate]` and `[triage]` (`spec explain config`).
Without them, or when either model fails or times out, it fails closed: there are no model flags,
but the known misspellings and earlier verdicts still show. The prompt is replaceable with `[prompts] spell`
(a `## policy`, `## question` with `{id}`, and `## reply` text block).

## Cache

Every verdict is cached in `.spec/spell/verdicts.json` (untracked), keyed by the word, its sentence,
how often the project uses it, and the models and prompt that judged it. A repeated analyze asks
nothing it has asked before; editing a sentence asks about its words again. Until then a word keeps
the verdict it got in another sentence, so fixing one typo, or typing elsewhere in the sentence,
leaves the other flags in place. Delete the file to start over.

## Editors

Once a spell check has run, `spec ui` and `spec lsp` show every known misspelling and the cached
verdicts without calling a model. `spec ui` draws a plain near-red underline (^P on the word puts the
fix in). The language server sends information diagnostics from source `spell`, with two quick
fixes: the correction, and adding the word to `spec.words`. The language server's `spec.spellCheck` command (one argument, a document URI)
runs the models and republishes. `spec lint --spell` adds every known misspelling and the cached
verdicts to the lint output, offline.

## Turning it off

`[lint] spell = "off"` in `spec.toml` turns the spell check off for the project: no flags in any
command or editor, and no model calls. `"warning"` or `"error"` raises the severity of its
diagnostics (with `"error"`, `spec lint --spell` exits 4 when it flags a word).

## spec.words

A committed word list next to `spec.toml`: one word per line, `#` starts a comment. Its words are
always accepted, even one the known-misspellings list names:

    # Words the spell check accepts in this project
    lensd
    transcoderd

## Without a system word list

macOS ships `/usr/share/dict/words`; many Linux systems have none unless a words package is
installed (`wamerican` on Debian and Ubuntu, `words` on Fedora). Without one, more words go to the
models, and a fix is kept only when it is a project term or a known correction. `spec doctor` says
which word list it found.
