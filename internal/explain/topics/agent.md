<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Agent rules

For any agent reading or editing this project's `*.spec.md` files. The spec states what the system
guarantees. Your job is to keep the code true to it, and to change the spec only when the user
wants the change.

## Read

    spec ls                      every anchor: kind, name, title, file:line, short hash, references
    spec show Frame.Timing       an anchor's body, who references it, where it is mentioned
    spec show 5.2a               a section and the anchors in it (also hls:5.2a)
    spec refs Frame.Timing       every place that references it, file:line:col
    spec lint                    problems; exit 4 when there are errors

Add `--json` when you parse the output (`spec explain json`). Write names without `$` in commands:
the shell expands `$Frame` to nothing.

## Edit

Edit spec files with your normal file tools, then run `spec status` and do what its `next:` line
says. If a hook already ran it after your edit, read that output instead.

- **Prose** (anything outside an anchor's body): edit freely, then fix whatever `spec lint` reports.
  A finding inside an anchor is an anchor edit: `spec fix FILE:LINE --by YOUR-NAME` files its fix.
- **Anchors** (adding, changing, removing, or switching between `Def` and invariant): only when the user
  asked for it or agreed to it. Edit, then file it for review, naming each anchor you edited:

      spec propose Frame.Timing --why "the reason" --by YOUR-NAME --wait 30m

  Names are required (exit 1 `names_required` lists what differs): the working tree cannot say whose
  edit is whose, so name only your own. An applied proposal's uncommitted change is skipped, named or
  not; commit it. A further edit on top of one is exit 1 `applied_not_committed`: commit the applied
  change first. `spec propose` restores the old text while the proposal waits. Then:
  - Exit 0, applied: run `spec status` and commit with its trailer lines, unless the output says it
    was committed.
  - Exit 3, rejected: read the reason, rework the edit, propose again.
  - Exit 2, still pending: tell the user the id, or wait again with `spec proposal ID --wait 30m`.
  - Exit 6, `stale`: the primary (the change itself) no longer applies, and the proposal stays
    pending. Read `spec proposal ID`, report its `stale_primary.reason`, and ask the user how to close
    or refile the remaining work. Waiting again returns the same result, and `--ack` cannot clear it.

  In your report, list every anchor you proposed and its outcome.
- **Changing what a pending proposal changes:** proposals do not stack. A second one built on a
  pending one's unapplied text, or overlapping it, goes stale. Withdraw yours and send one that holds
  all the changes:

      spec proposal ID                                        read what it changes
      spec reject ID --why "withdrawn: resending with ..." --by YOUR-NAME
      spec propose Frame.Timing --why "the reason" --by YOUR-NAME --wait 30m      after redoing the edits

  The working tree holds the old text, so make the first change again too, and name every anchor you
  are resending (or skip the edit: `spec change Frame.Timing --body-file new.md --why "the reason"
  --by YOUR-NAME --wait 30m`). `--by` marks the withdrawal as yours; without it the receipt reads
  `rejected by human`. Anyone waiting on the old ID exits 3 with your reason, and the user sees its
  card go and a new one arrive. Exit 1 `not_pending` means it was decided first: read
  `spec proposal ID` and start from there. If it has `decision.applied`, do not reject it, because
  that closes it as `applied` (see below).
- **Renames:** `spec rename Old.Name New.Name`, never search-and-replace (renaming `Frame` must not
  touch `$Frame.Timing`). Run it with `--dry-run` first. It rewrites tags in formal models, and reports
  occurrences in code that it left alone; fix those by hand if they are citations.

## Interrupted checks and stale work

A required check is queued, running, complete, failed or stale. `check_required` means the proposal
cannot be applied yet. If the check is running, wait for it. If it failed or was interrupted, ask the
user to authorize `spec recheck ID --retry` when the source files are unchanged, or `spec recheck ID`
to check against the current files. `stale_check` also needs `spec recheck ID`, without `--retry`. A recheck keeps the
primary and the ID, rebuilds the check and the follow-on hunks, and applies nothing. Only a
successful `spec recheck ID` without `--retry` clears `stale_primary`; editing a follow-on hunk or a
failed recheck does not. If the primary is obsolete, show the user what you found and ask them to
close or refile it.

After a partial application, `decision.applied` lists what was applied, even while the proposal is
still pending. A recheck or retry cannot fix the remaining work. Look at the applied and remaining
edits, then ask the user to close the remainder and file only that remainder as a new proposal.
Closing it keeps an `applied` receipt and records the rejected remainder in `decision.reason`, so
read that decision even when the waiter exits 0.

`recovery_required` comes before retrying checks or waiting. Stop changing files and inspect
`spec recover`, `HEAD`, the working tree, `git diff --cached` and the saved proposal. An active
writer returns `lock_held`; wait for it. Acknowledging recovery archives the record and clears only
the project-wide guard. It does not repair files or the index, confirm that writes happened, or
unblock the interrupted proposal. Report what you found to the user and ask them to inspect and
decide. Do not run `recover --acknowledge`, approve or reject on your own. Keep any `warning` from
an approval's output, and report an uncertain commit or receipt before doing anything else.

## When code and spec disagree

The code is wrong until the user decides otherwise. Do not edit an invariant to match the code or to make
a test pass. Fix the code, or stop and tell the user: quote the anchor, show the conflicting code
(`file:line`), and propose new text. A change that needs discussion goes in its own pull request
(`spec explain workflow`, path 2).

## Writing code and tests

Before implementing, find the anchors that cover the area (`spec ls`, `spec refs`). Cite them from
tests as `spec explain citations` describes. A test with no spec citation should exist only as a
defensive, edge-case or regression test, and say so (`spec explain citations`, "Tests without a spec
statement").

## Writing a TLA+ model

Directly above every operator that checks something, put one `\*` line naming the statement it
checks, with a verbatim quote.

    \* Spec: $Queue.OneTurn "exactly one active turn per session"
    Inv_OneTurn == \A s \in Sessions : ...

- Cite `$Name` when the statement has an anchor, otherwise `§N.N`. Stack lines for several statements.
- Every `Inv_*` operator gets a tag. `\* Derived-from: <ref>` marks a variable or action that comes from
  a statement without checking it. Tags go on `\*` lines, never in `(* *)`.
- Mutants (`*_Mut*`) need none. The project lists its models under `formal` in `spec.toml`.
- `spec lint` catches a dead reference (error) and a quote that is not in the cited text (hint).
  `spec refs NAME` lists what cites an anchor. `spec explain citations` has the full grammar.

### Changing an existing model

Models are not spec files, so there is no anchor, trailer or contradiction check, but your edits are
still reviewed. Do not edit a model file in place: write the model's whole new text to a scratch file
and file it, leaving the working tree alone:

    spec change --tla tla/LaneQueue.tla --body-file /tmp/LaneQueue.tla --why "the reason" --by YOUR-NAME --wait 30m

`--body-file -` reads stdin. Exit codes are those of `spec change`: 0 applied (commit the model; it
needs no trailer, and `spec approve --commit` may have committed it already), 3 rejected (read the
reason, rework, send again), 2 still pending (`spec proposal ID --wait 30m`), 6 (`stale`): the user or
another edit changed the lines yours replaces; the proposal stays pending. Inspect it, withdraw it and
send the change again against the current text.

- One proposal edits one model and is applied as a whole. A model has at most one pending proposal: a
  second is exit 1 `model_pending`. To change it, withdraw yours (`spec reject ID --why "withdrawn:
  ..." --by YOUR-NAME`) and send one with all the changes. Edits elsewhere in the model, the user's
  included, do not stale it.
- Not `spec propose tla/F.tla`: it restores `HEAD` text and would revert uncommitted model edits, so it
  refuses with exit 1 `wrong_command`, as does `spec change --tla` on a spec file.
- New tag links that do not resolve are listed on the proposal (`model.links`); fix them before the user
  reads it. prudence checks the new text's syntax and, with the modules it extends, its semantics and levels (run
  TLC yourself for behaviour); an undefined name or a missing module is an error. When prudence
  cannot finish (a construct it does not support yet), `completion` is `incomplete`, a hint
  `analysis/incomplete` says so, the check reads "syntax checked", and its semantic errors are hints
  labelled `[incomplete semantic check]` that do not refuse filing. A check that stopped at the
  acquisition limit (a large model) carries hint `analysis/acquisition-limit` instead, saying "the
  model hit the acquisition limit"; send it again with `--max-acquisition-visits N`, or ask the user
  to set `[tla] max_acquisition_visits` in `spec.toml`. Errors your
  text adds refuse filing (exit 1 `check_failed`, diagnostics in `data.proposal.model.check`); errors
  the model already had do not. Run `spec lint` on your scratch copy's project first: syntax errors
  are `formal-syntax`, semantic ones `formal-semantic`.
- Creating or deleting a model is not done by proposal: ask the user.
- The user edits models directly in `spec ui`; those edits save at once with no proposal.

## Never

- Write a `Spec-Change:` or `Spec-Rename:` line by hand. Copy it from `spec status`, after your last
  edit to that anchor.
- Commit with `--no-verify` to get past the `commit-msg` hook.
- Weaken, delete or reword an invariant to make failing code pass.
- Create change-request files or directories; pull requests are the review channel.
- Pass `--auto-approve` to any command, run `spec approve` or `spec review`, or run `spec reject` on
  anything but your own pending proposal, to withdraw it: deciding proposals is the user's job.
- Edit a proposal file under `.spec/` by hand.

## Exit codes

| Code | Meaning                                                                 |
|------|-------------------------------------------------------------------------|
| 0    | ok; warnings and hints may be present                                   |
| 1    | usage, not found, bad ref, no baseline, concurrent edit, provider failure, a refused command (including stale, check and recovery refusals), internal error, failed doctor check |
| 2    | a proposal is waiting for review (`change`, `propose`, `fix`, `proposal --wait`)  |
| 3    | a proposal was rejected; the output carries the reason                  |
| 4    | lint errors, or trailers missing from the commit message                |
| 5    | another `spec` process holds `.spec/lock`; the output names it. Wait and retry |
| 6    | the primary of a pending proposal is stale; read its reason and ask the user how to close or refile it |

For a batch, rejected (3) wins over stale (6), and stale over plain pending (2). Operational
failures, including `recovery_required`, win over all of these.
`spec diff` and `spec analyze` exit 0 whatever they find.
