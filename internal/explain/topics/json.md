<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# JSON output

Every command takes `--json`. Stdout then receives exactly one JSON object followed by a newline, and
nothing is written to stderr. Field names are snake_case. This text is the contract for schema
`spec/v1`.

## Envelope

    {
      "schema": "spec/v1",
      "ok": true,
      "data": { ... },
      "error": null,
      "diagnostics": [ ... ]
    }

- All five keys are always present.
- `ok` is true exactly when the exit code is 0.
- `error` is null when the exit code is 0, else `{"code": "...", "message": "..."}`.
- `data` is the command's result (below). It is present on exit 2, 3, 4 and 6, and on exit 0 except
  where a command says it is null. On exit 1 and 5 it is null, except where the error list below
  says otherwise. A filing or wait command that has already filed proposals can return the whole
  filed result on exit 1 or 5 too, so their IDs are not lost when a recovery, lock or provider error
  interrupts it. That result takes the place of single-proposal data, and carries the lock details,
  when there are any, as optional `lock`. Read `error.code` and `error.message` before following
  `next`.
- `diagnostics` is an array, empty when there is nothing to report. Every command that loads the
  project returns all of the project's diagnostics, after `[lint]` severity overrides; rules set to
  `"off"` are left out. `init`, `explain` and `adopt` always return `[]`. Sorted by file, start offset,
  severity (error first), then code. They describe the side that is read: the working tree, the index
  for `--staged` and `check --message-file`, and for `rename` the files after the rename. Formal models
  are read from the working tree only, so `formal-*` findings appear only for the working tree.

## Conventions

- `file`: the path relative to the directory holding `spec.toml`, with `/` separators. A diagnostic,
  a reference or a rename edit may name a formal model (`tla/F.tla`).
- `line`, `col`: 1-based; `col` counts bytes. A diagnostic that belongs to no file (`project-no-files`)
  has `line` and `col` 0.
- `range`: `{"start": N, "end": N}`, half-open byte offsets into the file's raw bytes (a BOM
  counts).
- Anchor names always carry the sigil: `"$Frame.Timing"`.
- A section `number` has no sigil and no trailing period (`"5.2a"`); `qualified` is
  `"§hls:5.2a"`.
- Absent strings are `""` (a missing title); absent objects are `null`. Arrays are never null.

## Errors

Exit 1 (`data` is null unless stated):
- `usage`: bad flags or arguments, including an argument the shell expanded (`.Timing`).
- `no_project`: no `spec.toml` found. `data`: `{"candidates": [dir, ...]}`.
- `project_exists`: `init` where `spec.toml` already exists.
- `not_found`: unknown anchor, section or explain topic.
- `ambiguous`: an argument matches several targets. `data`: `{"candidates": [string, ...]}`.
- `bad_name`: `rename`: NEW is not a valid name.
- `name_exists`: `rename`: NEW is already defined.
- `bad_ref`: git cannot resolve the ref.
- `no_baseline`: `diff`, `ls --changed` or `check` outside a git repository, or `git` is missing.
- `concurrent_edit`: `rename`: a file changed while it ran; nothing was written.
- `write_failed`: a file, proposal, lock or receipt operation failed. Read the message and inspect
  the saved state before retrying; filed proposals may remain. When `rename` or `init` cannot roll
  back, the message says so.
- `doctor_failed`: `doctor`: a check failed. `data`: the doctor result.
- `provider_unavailable`: a command that calls a model found no usable provider (no user config, an
  unknown `--provider`, an unset variable); the message says how to fix it.
- `provider_failed`: the provider failed after retries (an HTTP error, a timeout, an unreachable
  host); the message carries the status and how to fix it. API keys are never printed.
- `bad_model_output`: the model twice answered without the JSON asked for; nothing was filed.
- `not_fixable`: `fix`: the finding is about the project, not a spec file's text (a `spec.toml`
  entry, a file that is not UTF-8, a tag in a formal model); the message says what to do by hand.
  Nothing was filed.
- `no_fix`: `fix`: the model proposed no edit; the message carries its summary, notes and
  questions, and `data` is `{"proposal": proposal}` (not filed, `id` `""`).
- `bad_change`: `change`/`propose`: the new text does not define exactly that anchor, or an edit
  cannot be restored or applied; nothing was written.
- `name_exists`: also `change --add` of a defined name.
- `wrong_command`: the command does not fit its target: `change --tla` naming a spec file (spec
  edits go by anchor: `change NAME` or `propose NAME`), or `propose`/`change NAME` naming a TLA+ model
  (use `change --tla PATH --body-file F`; `propose` would revert uncommitted model edits). Nothing
  was written.
- `check_failed`: `change --tla`: the model checker found errors the proposed text adds over the
  model as it is. `data` is `{"proposal": proposal}` (not filed, `id` `""`) with
  `model.check.diagnostics`. Fix them and send it again; errors the model already had do not count.
- `model_pending`: `change --tla`: the model already has a pending proposal, and model proposals do
  not stack. `data` holds that proposal. Withdraw it (`reject ID --why "withdrawn: ..." --by NAME`)
  and send one proposal with all the changes.
- `names_required`: `propose` by an agent with no names and no `--keep`. The message lists the anchors
  that differ from `HEAD` with no applied proposal to explain them; `data` is the filed shape with no
  proposals, and `data.next.argv` is the command naming them all. Name only your own edits. Nothing
  was written.
- `applied_not_committed`: `propose` of an anchor holding an applied proposal's uncommitted change and
  a further edit, without `--keep`: restoring `HEAD` would revert the applied change. Commit the
  applied change first. Nothing was written.
- `proposals_unreadable`: `propose`: the proposal store could not be read, so an applied change cannot
  be told from a new edit. Fix the store (`.spec/proposals`) and retry. Nothing was written.
- `stale`: `approve`/`review`: the primary no longer applies, or accepted edits cannot apply;
  `recheck`: the primary no longer applies, or `--retry` found changed source files. The proposal
  stays pending. When given, `data` is `{"proposal": proposal, "acked": false}`. Read the reason;
  run a plain recheck when the files the check read have changed, or ask the user to close or refile
  work that is obsolete. This refusal is exit 1; the stale result of a wait, below, is exit 6.
- `stale_check`: source files changed since the required check. `data` holds the proposal when it
  could be loaded. Recheck against the current files before applying; after a partial application,
  inspect the applied receipt and ask the user to close or refile only the remaining work.
- `check_required`: the required check is queued, running, failed or stale. `data` holds the
  proposal. If the check is running, wait for it. If it failed or was interrupted, ask the user to
  authorize `recheck --retry` when the files are unchanged, or `recheck` when they changed. Neither
  applies edits.
- `check_running`: another worker owns this proposal's check; wait for it. `recheck` returns the
  loaded proposal in `data` when it could be loaded.
- `retry_unavailable`: the proposal has no snapshot to retry its check from, is decided, or already
  has applied edits. A plain recheck can check a pending human edit that has no earlier snapshot;
  `--retry` needs one. `recheck` returns the loaded proposal in `data`. After a partial application,
  keep `decision.applied`, inspect the remaining edits, and ask the user to close or refile them.
- `recovery_required`: an approval or commit was interrupted and its record is unresolved, which
  blocks further writes and waits. Inspect `spec recover`, `HEAD`, the working tree, the index and
  the proposal's receipt before going on. `data` is null, a proposal result, or an approve result
  when writes were reported. An operation still running is reported as `lock_held` instead; a
  `--wait` waiter waits for it to finish and then reports its decision (or `recovery_required` if it
  did not finish). Acknowledging recovery archives the record and clears only the project-wide
  guard; the interrupted proposal stays blocked. While its receipt is undecided, the archived record
  blocks waits on it. Once the user closes it, a wait returns the normal decided result, though an
  active project-wide record still comes first.
- `cancelled`: an operation or lock wait was cancelled. A published proposal can remain pending;
  inspect it before retrying. `recheck` retains the loaded proposal in `data`.
- `already_applied`: a requested hunk revision targets an already-applied edit; inspect the saved
  receipt and edit the document through the normal workflow instead.
- `not_pending`: `approve`, `reject`, `review` of a decided proposal; `proposal --ack` of a pending
  one.
- `changed`: `review`: a hunk accepted in the review was revised elsewhere meanwhile; nothing was
  written. Review it again.
- `commit_failed`: `approve --commit`: the files were written but git failed. `data`: as for approve.
- `not_formatted`: `fmt`: the formatter left a file as it was; `message` says why for each.
  `data`: the fmt result (the other files were formatted).
- `internal`: a bug.

Exit 2 and 3 (`data` is the command's result):
- `pending`: `change`, `propose`, `fix`, `proposal --wait`: a proposal awaits review.
- `rejected`: the same commands: a proposal was rejected; `message` carries the reasons.

Exit 4 (`data` is the command's result):
- `lint_errors`: `lint` and `status`: error-severity diagnostics. `status` and `check`: a name
  defined more than once, which blocks the diff (`skipped`).
- `missing_trailers`: `check`: required trailer lines are missing from the message.
- `unformatted`: `fmt --check`: some files are not formatted (`unformatted` lists them).

Exit 5:
- `lock_held`: another process holds `.spec/lock` (or another UI holds `.spec/ui.lock`).
  `data`: `{"lock": lock}`, except a `recheck` that already loaded the proposal returns its proposal
  result and a filing/wait command can retain the complete filed result with optional `lock` as
  described above. The error message names the lock's owner when it is recorded. Wait and retry; a
  held lock does not mean recovery is needed.

Exit 6 (`data` is the complete filed result, including every requested proposal):
- `stale`: a pending proposal has a saved `stale_primary`. `proposal --wait`, and the wait of
  `change`/`propose`/`fix`, return at once, for a later waiter too. The proposal stays pending, with
  a `reason` and a `next` step; inspect it and ask the user how to close or refile the remaining
  work. `--ack` never removes a pending receipt.

`candidates` for `no_project` are directories holding a `spec.toml`, relative to the current
directory; for `ambiguous` they are qualified sections (`"§hls:5.2"`), or `"file:line"` locations of a
duplicated name or of sections the qualified form cannot tell apart. Operational failures
(including `lock_held` and `recovery_required`) take priority over wait outcomes. In a loaded batch, rejection (3) takes
priority over pending stale (6), which takes priority over ordinary pending (2); all applied is 0.
For command validation, exit 5 wins over 1, and 1 over 4.

## Shared objects

**diagnostic**

    {"code": "ref-dangling", "severity": "error", "file": "hls.spec.md", "line": 12, "col": 5,
     "range": {"start": 301, "end": 314}, "message": "...",
     "related": [{"file": "...", "line": 3, "col": 1, "range": {...}, "message": "..."}]}

`severity` is `error`, `warning` or `hint`; `line` and `col` are the start of `range`.

**anchor**

    {"name": "$Frame.Timing", "kind": "inv", "form": "paragraph", "title": "fragment timing",
     "file": "lensd.spec.md", "line": 57, "section": "§lensd:1",
     "hash": "<64 hex>", "short_hash": "8b77d0a4", "ref_count": 3}

`kind` is `inv` or `def`; `form` is `paragraph` or `section`. `line` is the label's line. `section`
is the qualified form of the innermost numbered section holding the label, or null. `ref_count`
counts reference tokens of this name outside the anchor's own body, titles included.

**section**

    {"number": "5.2a", "qualified": "§hls:5.2a", "title": "Retry", "level": 4,
     "file": "hls.spec.md", "line": 40}

**baseline**

    {"from": "HEAD", "to": "worktree", "state": "ok", "reason": ""}

`from` is the ref compared against (`"HEAD"`, or the `--base` value of `status`, `check` and
`diff`; `diff --against` is the same flag). `to` is what is compared
with it: `"worktree"`, or `"index"` for `--staged` (which `check` implies when given
`--message-file`), so `spec diff --staged` shows what `spec check` will examine.
`state` is `ok`, `unborn` (no commits yet: everything is added; `reason` is `"no commits yet"`) or
`unavailable` (`reason` says why, e.g. `"not a git repository"`).

**change**: one anchor that differs from the baseline. Unchanged anchors are never listed.

    {"name": "$Frame.Timing", "state": "changed", "renamed_from": null,
     "old": {"kind": "inv", "title": "...", "hash": "...", "short_hash": "3f2a91c0",
             "file": "lensd.spec.md", "line": 57},
     "new": {"kind": "inv", "title": "...", "hash": "...", "short_hash": "8b77d0a4",
             "file": "lensd.spec.md", "line": 59},
     "body_changed": true, "kind_changed": false, "retitled": false,
     "trailers": ["Spec-Change: $Frame.Timing inv:3f2a91c0 -> inv:8b77d0a4"]}

- `state` is the most significant of `added`, `removed`, `renamed`, `kind_changed`, `changed`,
  `retitled`, in that order; the three booleans say everything that differs.
- `old` is null for `added`, `new` is null for `removed`.
- For `renamed`, `name` is the new name and `renamed_from` the old one; otherwise
  `renamed_from` is null.
- `trailers` holds exactly the lines this anchor requires in a commit message (empty for a pure
  retitle).
- Lists of changes are sorted by `name`.

**proposal**: the file `.spec/proposals/ID.json`, as stored.

    {"id": "k3x7a", "created": "2026-10-30T14:02:11Z", "author": "agent-7", "kind": "change",
     "why": "...", "summary": "the check's summary",
     "primary": {"n": 1, "action": "change", "name": "$Frame.Timing", "file": "lensd.spec.md",
                 "old_kind": "inv", "new_kind": "inv", "old_text": "...", "new_text": "...",
                 "placement": null, "in_worktree": false},
     "fix": null,
     "hunks": [{"n": 2, "file": "hls.spec.md", "anchor": "$Hls.Part", "issue": "numeric_mismatch",
                "severity": "inconsistent", "explanation": "...", "find": "...", "replace": "...",
                "range": {"start": 10, "end": 52}}],
     "notes": [{"file": "...", "anchor": "", "issue": "...", "severity": "...", "explanation": "...",
                "find": "...", "replace": "", "reason": "no edit proposed", "fixed_by": [2]}],
     "questions": ["..."],
     "base": {"files": {"lensd.spec.md": "<sha256>"}, "anchors": {"$Frame.Timing": "<hash>"}},
     "status": "pending",
     "decision": null,
     "inference": {"provider": "cf", "model": "...", "echoed_model": "...", "calls": 1,
                   "prompt_tokens": 9120, "completion_tokens": 840, "total_tokens": 9960,
                   "latency_ms": 7400, "kind": "openai", "effort": "low"}}

- `kind` is `change` (with `primary`), `analysis`, `fix` or `model` (all three: `primary` null,
  hunks numbered from 1).
- `model` is absent except on a `model` proposal (`spec change --tla`), an edit of one TLA+ model
  applied as a whole: `{"file": "tla/M.tla", "module": "M", "base_hash": "...", "decls": ["Next"],
  "check": {...}, "links": [...]}`. `decls` are the definitions the edit touches; its hunks have
  `issue` `model edit` and no severity. `check` is the model checker's verdict on the proposed text:
  `state` is `not_checked` (no checker is registered for the model's file type; TLA+ checking is bundled), `clean`, `errors` (errors the model
  already had; a new one refuses filing with `check_failed`) or `failed` (the checker could not run;
  `message` says why), with `checker`, `version`, `level` (the furthest phase examined: `syntax`,
  `semantic`, or `complete` once levels were checked), `completion` (`complete` or `incomplete`: whether the full check finished; a check
  is semantic only when it is `complete` at level `semantic` or `complete`, otherwise it checked
  syntax only), `diagnostics` (`{"path", "start": {"line", "column", "offset"}, "severity", "code",
  "message"}`, positions in the proposed text; severity `error`, `warning` or `hint`, and a hint
  `analysis/incomplete` says the semantic check did not run, or `analysis/acquisition-limit` that it
  stopped at the acquisition limit, `--max-acquisition-visits`; only errors refuse filing), `hash`
  (the model text the check judged) and `rechecked` (true when approval checked again because the
  model had moved). `links` are the tag-link findings (`spec explain citations`) the proposed text
  adds: `{"code", "severity", "line", "col", "message"}`. On a model proposal `stale_primary` says
  that a hunk no longer applies, which stales the whole edit.
- `fix` is null except on a `fix` proposal, where it names the lint finding fixed:
  `"fix": {"code": "def-unreferenced", "file": "lensd.spec.md", "line": 40, "col": 7, "message":
  "...", "class": "model"}`. `class` is `mechanical` (computed, no model; `inference` is null) or
  `model`. A fix's hunks and notes carry the rule as `issue`, and `severity` `cosmetic` for a hint,
  else `inconsistent`.
- `inference` totals every model call behind the proposal. `kind` is the provider's kind,
  `effort` the effort level spec set (absent when none), and `cost_usd` the cost the provider
  reported (claude-cli reports it; absent otherwise). `"runaway_guard": true` (absent
  otherwise) says a reply used all of `max_output_tokens` without answering and the check was sent
  again with the provider's `triage_extra` (`spec explain config`); `calls` counts both.
  `stages` (absent without a gate or triage) lists the per-edit cascade's stages that ran, in order:
  `{"name": "gate" | "triage" | "check", "provider", "model", "scores": {"material_change": 0.99,
  ...}, "latency_ms", "cost_usd", "outcome": "clear" | "pass" | "failed_open" | "done", "reason",
  "findings"}`. `cleared_by` is `"gate"` or `"triage"` when that stage cleared the edit and the check
  did not run; the fields above are then the check's, with `calls` 0.
- `action` is `change`, `add`, `remove` or `kind`. `old_text`/`new_text` are the anchor's exact text,
  label included (`""` for an add or a removal). `placement` (an add) says where it goes:
  `{"file", "after", "earlier", "after_line", "section", "at_start", "sep"}` (`earlier`: the
  anchors before `after` in an unbroken run, nearest first, tried when `after` is absent).
  `in_worktree` is true for `propose --keep`: the change is already in the file.
- `issue`: `contradiction`, `numeric_mismatch`, `stale_reference`, `terminology_drift`, `ambiguity`,
  `missing_update`, or `spelling` on a note `spec ui` adds for a spell-check flag on the proposed text
  (severity `cosmetic`, `find` the word, `reason` "spell check"); `severity`: `breaking`,
  `inconsistent`, `cosmetic`. A hunk's `find` occurred
  exactly once in its file when the proposal was made; a finding that did not is a note.
- `fixed_by` (absent when empty) links findings to the edits that resolve them. On a note it lists
  the numbers of the hunks that resolve it: a conflict reported on the changed anchor, which no hunk
  may edit, points at the hunk that brings the other text in line. On a hunk it lists the other hunks
  the same problem also needs. One hunk can resolve several findings, and one finding can need
  several hunks.
- Optional `source_kind` is `agent` or `human`, set apart from `author`; older receipts may not
  have it. Optional `revision` is an opaque marker that changes with each write, not a version number
  to edit.
- Optional `check_state` is `queued`, `running`, `complete`, `failed` or `stale`; when it is absent,
  the proposal has no required check. `check_message` explains the state. `check_generation` counts
  check attempts; `check_files` maps file paths to the hashes the check read; `check_owner` names the
  worker that holds the check. spec keeps these fields itself; agents never edit them. A stale check
  (the files changed) is separate from a stale primary, and a recheck can refresh it on its own.
- Optional `stale_primary` is `{"reason": "..."}`: the saved finding that an unapplied primary no
  longer applies. A waiter checks the current files under the decision lock, without calling a
  model, and saves it, even when nobody has tried to approve. It stays through new waiters, revised
  follow-on hunks and failed checks. Only a successful plain recheck clears it; putting the old text
  back does not. It never marks the proposal decided or rejected.
- `status` is `pending`, `applied` or `rejected`. `decision` is normally null before application;
  after a partial application the proposal is still pending, and its decision lists the hunks that
  were `applied`. The decision shape is
  `{"accepted": [1, 2], "applied": [1], "failed": [{"n": 2, "reason": "..."}], "reason": "",
  "by": "human", "at": "...", "commit": ""}`; `reason` is set on rejection, `commit` by
  `approve --commit`. Explicitly rejecting the remaining edits after partial application closes
  the receipt as `applied`, retains `decision.applied`, and records `Remaining edits rejected: ...`
  in `decision.reason`; it does not claim the unaccepted remainder was written. A waiter then exits
  0 for that applied receipt, so inspect its decision as well as the exit code.

**next**: `{"text": "fix 2 lint errors, then commit", "argv": ["spec", "lint"]}`. `argv` is the
command to run, or `[]` when there is nothing to run.

**lock**: `{"pid": 4121, "host": "studio.local", "started": "2026-10-30T14:02:11Z", "command":
"spec rename Frame.Timing Frame.Duration"}`.

## Commands

**`spec init`**

    {"name": "lensd", "root": "/abs/path/to/project",
     "files": [{"file": "spec.toml", "action": "created"},
               {"file": "lensd.spec.md", "action": "created"},
               {"file": ".gitignore", "action": "appended"}],
     "claude_md": "## Specs\n...",
     "next": {...}}

`action` is `created`, `adopted` (an existing `<name>.spec.md`, left byte-for-byte unchanged),
`appended` or `unchanged` (`.gitignore` already listed `.spec/`). `claude_md` is the suggested CLAUDE.md
section, printed and never written.

**`spec explain [topic]`**

    {"topic": "markup", "text": "# Markup\n...", "topics": ["method", "markup", ...]}

`topic` is null for the index. An unknown topic is `not_found`.

**`spec adopt`**

    {"text": "# spec adopt\n...", "topics": [{"name": "method", "summary": "what spec-driven ..."}, ...]}

`text` is the printed page. `topics` lists every `spec explain` topic in index order with its line
from the index.

**`spec ls`**

    {"baseline": null, "anchors": [anchor + {"change": null}, ...]}

Anchors in project order (file order, then source order); `--kind` and `--unreferenced`
(`ref_count` 0) filter. With `--changed`: `baseline` is set, only anchors that exist now and differ are
listed, and each `change` is that anchor's `state` string.

**`spec show NAME`**

    {"type": "anchor", "anchor": anchor,
     "body": "**$Frame.Timing (fragment timing).** Every fragment ...",
     "referenced_by": [{"name": "$Frame.Forward", "kind": "inv", "file": "lensd.spec.md", "line": 88}],
     "mentioned_in": [{"file": "lensd.spec.md", "line": 3, "kind": "paragraph"}],
     "baseline": baseline, "change": change}

`body` is the anchor's source text, label included, exactly as in the file. `referenced_by` lists
the anchors whose bodies reference it, itself excluded. `mentioned_in` lists prose blocks outside
every anchor body that reference it; `kind` is `paragraph`, `list_item`, `heading`, `table_cell` or
`blockquote`. `change` is null when the anchor is unchanged or the baseline is unavailable
(`baseline.state` tells which). A name defined twice is `ambiguous`.

**`spec show SECTION`**

    {"type": "section", "section": section, "anchors": [anchor, ...]}

**`spec refs NAME|SECTION`**

    {"target": {"type": "anchor", "name": "$Frame.Timing", "section": null},
     "refs": [{"file": "hls.spec.md", "line": 12, "col": 5, "range": {...},
               "text": "the full source line, without its line ending",
               "within": "$Frame.Forward", "in_title": false, "state": "resolved"}]}

For a section target, `target` is `{"type": "section", "name": "", "section": section}`. `within` is
the anchor whose body holds the reference, or null. `state` is `resolved`, `dangling` or `ambiguous`.
An anchor target lists every reference to the name; a section target lists every `§` reference that
resolves to it or names it among ambiguous candidates. In source order, files in project order. Tags in
formal models come last, Spec tags first, mutants last; `range` is the tag's reference and `within` is
null.

**`spec lint`**

    {"files": 3, "anchors": 30, "sections": 12, "counts": {"error": 0, "warning": 2, "hint": 1}}

The findings are the envelope's `diagnostics`.

**`spec diff`**

    {"baseline": baseline, "changes": [change, ...], "skipped": ["$Hub.Dup"]}

`skipped` lists names defined more than once on either side, which are not compared. Exit 0 whatever
it finds.

**`spec status`**

    {"state": "anchors_changed", "baseline": baseline,
     "changed_files": ["lensd.spec.md"],
     "changes": [change, ...], "skipped": [],
     "lint": {"error": 0, "warning": 1, "hint": 0},
     "trailers": ["Spec-Change: $Frame.Timing inv:3f2a91c0 -> inv:8b77d0a4"],
     "next": next}

- `state`: `clean` (no spec file differs), `prose_only` (files differ but no anchor needs a trailer:
  `changes` is empty or holds only `retitled` anchors), `anchors_changed` (some anchor needs a
  trailer) or `no_baseline` (`changes`, `changed_files` and `trailers` empty).
- `changed_files`: spec files whose bytes differ from the baseline, including added and deleted ones.
- `trailers`: every line the commit needs: `Spec-Rename:` lines first, then `Spec-Change:` lines,
  each group sorted by name.
- Exit 4 when `lint.error` > 0 or `skipped` is not empty.

**`spec check`**

    {"baseline": baseline, "message_file": ".git/COMMIT_EDITMSG",
     "changes": [change, ...], "skipped": [],
     "required": ["..."], "present": ["..."], "missing": ["..."], "unexpected": ["..."],
     "malformed": [{"line": 3, "text": "Spec-Change: .Timing inv:... -> ...", "reason": "..."}]}

A required line is present when the message holds an identical trailer: the key is
case-insensitive, whitespace around the line is ignored, hash case is ignored, and lines starting
with `#` are ignored. `unexpected` lists `Spec-Change:` and `Spec-Rename:` lines in the message that
match no required line (a stale hash, say); they do not fail the check. `malformed` lists lines
that start with a trailer key but do not parse, with the reason (a `$` eaten by the shell, say).
Without `--message-file` there is no message and `message_file` is null, so every required line is
missing. Exit 4 with `missing_trailers` when `missing` is not empty, else with `lint_errors` when
`skipped` is not empty.

**`spec rename OLD NEW`**

    {"from": "$Frame.Timing", "to": "$Frame.Duration", "dry_run": false, "count": 7,
     "files": [{"file": "lensd.spec.md", "count": 7}],
     "edits": [{"file": "lensd.spec.md", "line": 57, "col": 3, "role": "definition",
                "in_title": false}],
     "left_alone": [{"file": "lensd.spec.md", "line": 9, "col": 14, "kind": "code_span"}],
     "skipped_files": [],
     "trailer": "Spec-Rename: $Frame.Timing -> $Frame.Duration"}

`edits` lists every token rewritten (with `--dry-run`, every token that would be), positioned in the
files as they were before the rename; `role` is
`definition`, `reference` or `tag` (a formal model's tag, or the quote of one). `left_alone` lists `$OLD`
written inside opaque regions; `kind` is `code_span`, `code_block`, `html_block`, `raw_html`, `autolink`,
`link_destination` or `front_matter`, or `model_comment` for a mention in a formal model outside a tag.
`skipped_files` lists spec files and models rename could not read and so did not rewrite:
`[{"file": "old.spec.md", "reason": "not valid UTF-8", "may_reference": true}]`; `may_reference` is
true when the file's bytes contain OLD, so it may hold references to fix by hand.

**`spec doctor [--provider NAME] [--engine ID] [--effort LEVEL]`**

    {"checks": [{"name": "project", "status": "ok", "detail": "spec.toml at .", "fix": ""}, ...],
     "inference": {"provider": "cf", "kind": "openai", "model": "@cf/zai-org/glm-5.3-flash",
                   "effort": "", "echoed_model": "@cf/zai-org/glm-5.3-flash", "latency_ms": 812,
                   "prompt_tokens": 18, "completion_tokens": 8, "total_tokens": 26,
                   "finish_reason": "length", "reply": "", "streamed": true, "cost_usd": 0}}

Checks in this order: `project`, `files`, `lint`, `git`, `user_config`, `provider_key`, `inference`,
then `gate` when the user config has `[gate]` and `triage` when it has `[triage]`: each opens that
provider and sends it one tiny request (a missing key is `warn`, since the stage then fails open).
`spell` comes last and is always `ok`: the word list the spell check reads (or that none was found),
whether its model pass is on, and how many verdicts its cache holds.
`status` is `ok`, `warn`, `fail` or `skipped`; `fix` says how to repair a `warn` or `fail`.
`provider_key` checks the provider chosen by `--provider` (else `default`): that it exists, that
the variables its `base_url` and `api_key_env` name are set, and for `kind = "claude-cli"` or
`"codex-cli"` that its `command` is found (and that `--effort` is a level of the kind). `inference` sends that provider one
request ("Reply with the single word OK", at most 8 tokens) and reports latency, the model id the
provider echoed (`echoed_model`, `""` when none) and the tokens used; `--engine` replaces the model.
`kind` is the provider's kind, `effort` the effort level spec set (`""` when none), and `cost_usd`
the cost the provider reported (claude-cli; `0` otherwise). A claude-cli or codex-cli provider has
no reply cap: it runs `claude -p` or `codex exec` once (`streamed` is false) and its login is what
`inference` checks; `codex exec` echoes no model id, so `echoed_model` is `""`.
A reasoning model may spend all 8 tokens thinking: `reply` is then `""` with `finish_reason`
`"length"`, which still passes. The request takes the path every check takes: `streamed` says
whether the reply came back as a stream (false with `stream = false`, or when the provider
answered with one JSON object). `inference` is null unless the request succeeded. A missing user
config, a missing default, or an unset variable is `warn` (`inference` is then `skipped`); a user
config that does not parse, an unknown `--provider`, or a provider that fails or cannot be reached is
`fail`. Any `fail` exits 1 with `doctor_failed`.

**`spec analyze [--changed | --all | FILE...]`**

    {"mode": "changed", "files": ["lensd.spec.md"], "regions": 2, "saved": true,
     "proposal": proposal, "spell": spell, "next": next}

`mode` is `changed`, `all` or `files`. `proposal` holds the result (an `analysis` proposal, null when
nothing differs from HEAD); `saved` says whether it was filed (only when it has hunks; otherwise its
`id` is `""`). Exit 0 whatever it finds. `spell` is the spell check of the same text
(`spec explain spell`):

    {"flags": [{"file": "lensd.spec.md", "line": 12, "col": 9, "range": {"start": 410, "end": 417},
                "word": "recieve", "fix": "receive", "by": "known"}],
     "stats": {"candidates": 6, "cached": 2, "asked": 4, "gate_calls": 1, "triage_calls": 1,
               "escalated": 1, "cost_usd": 0.0001, "latency_ms": 1800, "skipped": 0},
     "models": "typesafe/jev-1.14|@cf/zai-org/glm-5.3-flash|1a2b3c4d", "error": "..."}

`by` is `known` (the known-misspellings list) or `model`. `models` is `""` when the model pass is
off (no `[gate]` or `[triage]`); `error`, present only when the model pass failed, says why (it fails
closed: no model flags). `no_dictionary: true` says no system word list was found, and `off: true`
that `spec.toml` turns the spell check off (`[lint] spell = "off"`).

**`spec change NAME ...`** and **`spec propose NAME...`**

    {"proposals": [proposal, ...], "skipped": ["$X (renamed from $Y: ...)"], "warnings": [], "next": next}

When the per-edit cascade cleared anchors, `"cleared": [{"anchor": "$Frame.Timing", "cleared_by":
"gate", "reason": "No logically significant change found (0.3 s)", "id": "", "stages": [...]}]` lists them
(`id` is `""` with `--keep`, which files nothing for them), and `"cleared_by": "gate" | "triage"` is
set when the check ran for none of the anchors ("triage" when any was cleared by triage). Both are
absent when the check ran. `propose --keep` whose every anchor was cleared exits 0.

Optional `lock` carries the shared lock object when a filing/wait result is interrupted by
`lock_held`; the whole proposal list is still there. For `recovery_required`, `next` points to
`spec recover`, to inspect before going on.

`spell` (absent when no proposal has new text) is the spell check of the proposed text, as for
`spec analyze`; a flag's `file` is the anchor (`"$Frame.Timing"`) and its `line` counts from the
proposed text's first.

`warnings` is always a list. `propose` adds `"$Frame.Timing already has pending proposal k3x7a"` for
each anchor it files a proposal for while another one is still pending; the new proposal is filed
anyway (`change` always has `[]`).

Exit 2 (`pending`) once filed; with `--wait`, 0 when every proposal was applied, 3 (`rejected`) when
any was rejected, 6 (`stale`) when any pending primary is stale, and 2 when the wait ran out. A
stale result includes every requested proposal and leaves the pending receipt as it is.
`--auto-approve` applies an eligible proposal at once (exit 0). `propose` with nothing to file exits
0 with no proposals; `skipped` lists any anchor an applied proposal explains. `propose` names are optional only with `--keep` or `--source human`.

`spec change --tla PATH --body-file F` has the same shape, with one `model` proposal and no
cascade (`spec explain agent`). `--tla` takes the model's path; the LLM model id is `--engine`, on
every command that calls a model. A second proposal for a model with one pending is
`model_pending`; errors the model checker finds in the new text that the model did not have are
`check_failed`, and nothing is filed.

**`spec fmt [FILE...] [--check]`**

    {"check": false, "written": true,
     "files": [{"file": "lensd.spec.md", "changed": 12, "refused": ""}],
     "unformatted": ["lensd.spec.md"]}

`changed` counts the lines that changed (or, with `--check`, would); 0 for a formatted file.
`refused` is why a file was left as it was ("" when it was not). `unformatted` lists the files with
changes; `written` is true when they were written.

**`spec fix FILE:LINE[:COL] [--code CODE]`**

    {"proposals": [proposal], "skipped": [], "warnings": [], "next": next}

The data, exit codes and flags of `propose` (`--why` is optional: the commit message defaults to
`"Fix CODE: ..."`). FILE:LINE:COL is the place `spec lint` prints; without COL any finding on the
line counts. With several findings there, `--code` picks one, else the most severe is fixed.

**`spec proposals`**

    {"proposals": [{"id": "k3x7a", "kind": "change", "status": "pending",
                    "created": "2026-10-30T14:02:11Z", "author": "agent-7", "anchor": "$Frame.Timing",
                    "why": "...", "hunks": 2, "notes": 1, "questions": 0}]}

Oldest first; receipts older than 7 days are removed as a side effect.

**`spec proposal ID [--ack] [--wait D]`**

    {"proposal": proposal, "acked": false}

With `--wait`, the data and exit codes of `change`. Any waiter, current or later, sees a stale
primary at once, without waiting for the user to decide. A pending stale receipt is not
acknowledged, and is not removed after 7 days as a decided receipt is.

**`spec recheck ID [--full] [--retry]`**

    {"proposal": proposal, "acked": false}

Keeps the proposal ID and its primary. A plain recheck reads the current files and regenerates the
check, the findings and the follow-on hunks, and drops earlier selections of follow-on hunks.
`--retry` reuses the stored snapshot, only when the files are unchanged, and cannot clear
`stale_primary`. A plain recheck can check a pending human edit that has no earlier snapshot.
Neither applies edits. A failed check leaves the proposal as it was; only a successful plain recheck
clears `stale_primary`. A partly applied proposal cannot be rechecked: inspect its applied receipt,
ask the user to close the remaining edits, and file those edits as a new proposal. On failure,
`data` is this proposal result when the proposal was loaded; otherwise it is null, or the lock
result when there is one. Provider flags and `--full` have their usual meaning.

**`spec recover [--acknowledge --why NOTE]`**

    {"id": "<operation id>", "kind": "approve", "started": "...",
     "files": [{"path": "lensd.spec.md", "before": "<sha256>", "after": "<sha256>"}]}

Exit 0 returns the active intent, or `data: null` when none exists. Optional fields are `proposal`
(the saved proposal), `accepted` and `planned_applied` (hunk numbers), `changes` (selected change
IDs), `parent`, `prepared_commit`, `commit` (Git IDs), `acknowledged` (inspection note) and
`needs_inspection` (boolean). File values are hashes, not contents. An intent records the work that
was planned, not which parts of it happened. Inspect `HEAD`, the working tree, the index and the
saved receipt. With a non-blank note in `--why`, `--acknowledge` returns the acknowledged intent and
archives it under `.spec/recovery/`. It clears only the project-wide guard. It does not repair files
or the index, confirm any write, or unblock the interrupted proposal. The user closes that proposal
and files only the remaining work as a new one; receipts of applied work stay as the record. Agents
ask the user for this decision and never acknowledge or reject on their own.

**`spec approve ID [--hunks 1,3] [--commit]`** and **`spec review ID`**

    {"proposal": proposal, "commit": "<sha or empty>", "trailers": ["Spec-Change: ..."], "next": next}

Optional `warning` says that the commit, receipt or cleanup may not have finished; keep it and
check, even when `commit` is set. Follow `next`, and inspect `spec recover` before retrying.
`trailers` (with `--commit`) are the lines written into the commit message. `review` has no
`--json` form (it is interactive); its human output ends the same way.

**`spec reject ID --why W`**

    {"proposal": proposal, "acked": false}

It waits up to 10 s for `.spec/lock` (exit 5 after that): a rejection while an approval of the same
proposal is writing waits for it, and is then `not_pending`.
