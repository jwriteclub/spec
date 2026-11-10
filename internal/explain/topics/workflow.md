<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Workflow: changing a spec

Edit `*.spec.md` files directly, then ask `spec status` what the edit changed. Git is the change
log: a commit that changes an invariant or definition says so in a trailer, and a change that needs
broader review gets its own pull request.

## What counts as a change

Anchors are compared by name across the whole project, against `HEAD` by default:

| Edit                                                  | Result         | Trailer         |
|-------------------------------------------------------|----------------|-----------------|
| prose outside every anchor, headings, examples        | prose only     | none            |
| reflowing an anchor's body (line breaks, spaces)      | nothing        | none            |
| moving an anchor to another place or file             | nothing        | none            |
| an anchor's title                                     | `retitled`     | none            |
| an anchor's body text, including whitespace in code   | `changed`      | `Spec-Change:`  |
| `**$X.**` to `**Def $X.**` or back                    | `kind_changed` | `Spec-Change:`  |
| a new anchor                                          | `added`        | `Spec-Change:`  |
| a deleted anchor                                      | `removed`      | `Spec-Change:`  |
| `spec rename Old.Name New.Name`                       | `renamed`      | `Spec-Rename:`  |
| a rename and a reworded body together                 | `removed` and `added` | two `Spec-Change:` |

A rename rewrites every reference, and the references do not count as changes.

## After every edit

    spec status

It compares the working tree with `HEAD`, runs lint, and says one of:
- `clean`: nothing differs.
- `prose_only`: done; fix any lint errors it lists.
- `anchors_changed`: it lists each changed anchor and prints the exact trailer lines to paste.
- `no_baseline`: not a git repository, or `git` is missing; lint still runs.

It ends with a `next:` line naming the command to run (for changed anchors, `spec propose` with their
names, leaving out what an applied proposal already explains). In a repository with no commits yet,
every anchor is `added`.

Other views: `spec diff` (the anchor diff only), `spec diff --against main` (everything a branch
changed), `spec diff --staged` (the index against `HEAD`), `spec ls --changed`.

## Formatting

    spec fmt                         wrap and tidy every spec file
    spec fmt --check                 list unformatted files, exit 4 (for a pre-commit hook)

`spec fmt` wraps paragraphs and list-item text to `[format] width` (default 100), strips trailing
whitespace, collapses runs of blank lines and ends each file with one newline. It never breaks inside
a `$Name`, a `§` reference, a label, code or a link, and leaves code blocks, tables, HTML, headings
and quotes as they are. It changes whitespace only and checks it: a file whose anchor hashes or
Markdown structure would change is left alone, with the reason (exit 1). Hashes do not change, so a
formatting commit needs no trailers. In `spec ui`, ^F formats the open buffer as one undo step.

## Trailers

Git trailers at the end of the commit message, after a blank line, exactly as `spec status` prints
them:

    Spec-Change: $Frame.Timing inv:3f2a91c0 -> inv:8b77d0a4      changed
    Spec-Change: $Frame.Timing - -> inv:8b77d0a4                 added
    Spec-Change: $Frame.Timing inv:3f2a91c0 -> -                 removed
    Spec-Change: $Time.Media inv:5d0e44a1 -> def:5d0e44a1        kind changed
    Spec-Rename: $Frame.Timing -> $Frame.Duration                renamed

(The right-hand column is commentary, not part of the line.) Grammar:

    Spec-Change: $Name <old> -> <new>
    Spec-Rename: $Old -> $New
    <old>, <new> := <kind>:<short hash> | -
    <kind>       := inv | def
    <short hash> := the first 8 hex characters of the body hash

`spec check` enforces this. As a `commit-msg` hook (`spec explain integrations`) it compares the
staged spec with `HEAD`, and if any required line is missing from the message it prints exactly the
missing lines and exits 4. Hashes in the lines must match the staged text: if you edit an anchor
again after copying its line, run `spec status` again. A spec change and the test change that checks
it belong in the same commit.

**Amending.** When you `git commit --amend` a commit that already changed anchors, the trailers must
cover the change since `HEAD^`, not since the commit being amended. Run `spec status --base HEAD^` for
the lines. The hook cannot detect an amend, so pass the base yourself:
`SPEC_BASE=HEAD^ git commit --amend` (`spec explain integrations`).

## Proposals: the review loop

Every add, change or removal of an anchor gets a contradiction check by a model (`spec explain config`
sets the provider) and is reviewed before it lands.

An agent edits the anchor directly, then:

    spec propose Frame.Timing --why "fragments over 5 s stall players" --by agent-7 --wait 30m

For each anchor named, it records the new text, files a pending proposal under `.spec/proposals/`
without waiting for the check, and restores the `HEAD` text in the working tree. The edit lives in
the proposal until it is approved. Whether the text came from an agent or a human (`--source`,
default `agent`) is recorded separately from the optional author name (`--by`). The card shows the
check's state (queued, running, complete, failed or stale), and the proposal cannot be applied until
the check completes. The user can reject it with a reason while the check runs. A model result that
arrives after that does not overwrite the rejection, and does not stop the checks of the batch's
other proposals.

Names are required. The working tree cannot say whose edit is whose, and propose takes the edit out
of it, so an agent's `spec propose` with no names files nothing (exit 1 `names_required`, listing
what differs and the command to run). An anchor whose only uncommitted change is what applied
proposals wrote is skipped, even when named: it is never reverted and proposed again. If there is a
further edit on top, propose exits 1 `applied_not_committed`, because restoring `HEAD` would revert
the applied change; commit the applied change first. `--keep` and `--source human` may leave out the
names, and then cover every other changed anchor.

Scripts can skip the edit: `spec change Frame.Timing --body-file new.md --why W`, `--remove`,
`--def`/`--inv`, or `spec change Frame.Gap --add --after Frame.Timing --body-file F` (or
`--section 2`).

A proposal holds hunks. Hunk 1, the primary, is the change itself; the rest are follow-on fixes the
check proposed (each quotes text that occurs exactly once in its file; anything else is shown as a
note). The user
decides:

    spec proposals                   what is pending
    spec review k3x7a                each hunk: y apply, n skip, e give feedback (the model redoes it)
    spec approve k3x7a --hunks 1,3 --commit
    spec reject k3x7a --why "5 s is too short for keyframe gaps"

`approve` re-checks every hunk against the files: a fix whose text changed fails alone; a change
whose primary no longer applies blocks application and stays pending with durable `stale_primary`
evidence. It never becomes rejected automatically. `--commit` commits what the approval
wrote (and the anchor a `--keep` proposal checked) with the trailer lines and the reason; other edits,
of the same files too, stay in the working tree uncommitted. Without it, `spec status` prints the
lines as usual.

To answer a check's question, run `spec proposal k3x7a --answer "keep 5 s"` (`--question N` picks
one; in `spec ui`, press `f` on the question). The model redoes the check with the answer and with
its earlier findings and decisions. The new check is filed with `"rethink": {"of": "k3x7a", "notes":
[...], "primary_hunk": N}` and supersedes the old one. A change it proposes to the edit itself is
hunk `primary_hunk`, applied only when approved, like any hunk. An analysis's question is answered
the same way: the model redoes the analysis with the answer, its fixes may land anywhere in the
spec, and the new analysis (with no `primary_hunk`) supersedes the answered one.

A waiting agent gets the decision as its exit code: 0 applied, 3 rejected (the reason is in the
output), 6 stale while still pending, or 2 still pending when `--wait` ran out
(`spec proposal k3x7a --wait 30m` waits again). A waiter checks whether the change still applies
to the current files, without a model call, even when nobody has tried to approve it. Every waiter,
current or later, gets the saved stale reason and the whole proposal. In a batch, rejected wins over
stale, and stale wins over plain pending. A stale proposal is still pending, so it is never
acknowledged or pruned as decided. A decided proposal stays as a receipt until it is acknowledged
(automatically after a wait, or with `spec proposal k3x7a --ack`) or for 7 days. Acknowledging one with applied, uncommitted hunks moves it to
`.spec/proposals/acked/` for the rest of those 7 days, which is how propose still knows where that text
came from. In `spec ui`, approving edits the buffer, and the proposal stays
pending until ^O saves the change to disk. Undoing the approval with ^Z, or discarding the buffer,
leaves it pending to approve again.

A TLA+ model listed under `formal` is not a spec file: the user edits it directly, and an agent files
its edit with `spec change --tla tla/F.tla --body-file NEW.tla --why W` (the whole new text;
the working tree is untouched). It has no contradiction check or trailer, its hunks apply as a whole,
and a model has one pending proposal at a time. The card names the model, the definitions touched,
the new tag-link problems and prudence's syntax, semantic and level-check results. Errors the text
adds refuse filing, and accepting a model left with errors asks twice. `spec
approve --commit` commits only the model's lines the proposal wrote. `spec explain agent` has the
agent's steps.

Proposals do not stack. A proposal built on a pending proposal's unapplied text, or one that edits
the same text, goes stale. An agent that needs to change what its own pending proposal changes first
withdraws it with `spec reject ID --why "withdrawn: ..." --by agent-7`, then sends one proposal with
all the changes: `spec propose Frame.Timing` naming each anchor, or `spec change Frame.Timing
--body-file F`. Anything waiting on the old ID exits 3. `spec explain agent` has the steps.

**Rechecking.** `spec recheck ID` keeps the proposal's ID and its primary, checks it against the
current files, and regenerates the findings and follow-on hunks; any earlier selection of follow-on
hunks is dropped. `--retry` reruns the check on the original snapshot, and only when the source files
have not changed. If a check is still running, wait for it instead of starting a second one. After a
check fails or is interrupted, ask for a `--retry`; after the files have changed, use a plain
recheck. Only a successful `spec recheck ID` without `--retry` clears a stale primary. A failed
recheck, a revised follow-on hunk or putting the old text back does not. Neither command applies
edits.

**Partly applied proposals.** When some hunks applied and the rest failed, `decision.applied` lists
the applied ones and the rest stay pending. A hunk is never applied twice, and a proposal with
applied edits cannot be rechecked or retried. Look at what landed and what remains. The user can
close the rest with `spec reject ID --why "inspection and remaining-work decision"`, which keeps the
record of what was applied, then file only the remaining edits as a new proposal. The closed
proposal has status `applied`: `decision.reason` records the rejected remainder and
`decision.applied` lists the edits that were applied. A waiter on it can therefore exit 0 even
though some edits were rejected, so read its decision too. Agents report the
situation and ask the user to decide; they never close it themselves.

**Interrupted writes and commits.** Before an approval writes files or commits, spec records what it
is about to do. That record shows the work was planned, not that it finished. While another process
is writing, commands exit 5 `lock_held`. When a writer died and left its record, or the record is
unresolved for another reason, commands exit 1 `recovery_required`, which takes priority over
ordinary wait results.
Before retrying, inspect `spec recover`, `HEAD`, the working tree, the index (`git diff --cached`)
and the proposal's receipt. An approval result may carry a `warning` that the commit, receipt or
cleanup is uncertain; keep it even when there is a commit ID. The user's `spec recover --acknowledge
--why NOTE` archives the inspected record and clears only the project-wide guard. It does not
repair files or the index or confirm that any write happened, and the interrupted proposal stays
blocked. Close that proposal explicitly with the inspection reason, and file only the remaining work
as a new proposal. After the proposal is closed, its archived record no longer takes priority over
its wait result; an active project-wide record still does. Agents ask the user to inspect and decide. They
never acknowledge recovery or reject the proposal on their own.

The user's own edit of an anchor gets the same check: `spec propose --keep --why W` leaves the edit in
place and proposes only the follow-on fixes; approving with `--commit` commits it with the trailers.
In `spec ui`, ^P runs the check with no prompt and ^N analyzes. What they find goes to the Checks
sidebar (^R): each problem names the places that conflict, with the fixes that resolve it listed
under it (`fixed_by`). y applies a fix into the buffer (on a problem, all its fixes; on the check's
title, every fix), n skips it, and Enter shows it in place in the text. Editing never commits: the
commit view (A in the sidebar) commits only the changes ticked there, through the usual hooks.
`--auto-approve` (humans only, never agents) applies the change at once when the check finds nothing
breaking and asks nothing.

**The per-edit cascade.** With `[gate]` or a triage model configured (`spec explain config`), the check
of a changed anchor runs in stages and stops at the first that clears it: the gate (Jev, about 0.3 s),
then triage (a fast model, about 2 s), then the check itself. A stage that times out, fails or replies
in the wrong shape passes the edit on to the next stage (it fails open). For `--keep` (and ^P) a cleared edit
files nothing and exits 0 with `data.cleared_by`; an agent's `spec propose` still files a proposal,
with no findings and a summary of "No logically significant change found" (the gate) or "No conflict
found" (triage), for the user to approve. Either way the edit still needs its `Spec-Change`
trailer: the cascade only decides whether the check runs. Each proposal's `inference.stages`
and the human output list the stages that ran, with their scores, time and cost. In `spec ui`, ^P
returns to the editor at once; the anchor's card at the top of the Checks sidebar and the status line
show each stage as it finishes, and a cleared edit's card leads with the same sentence.

## Consistency analysis

    spec analyze                     the text that differs from HEAD, against the whole spec
    spec analyze --all               everything

Advisory and never blocking (exit 0). The model works in four passes: quantities stated in more than
one place (30 s here, 60 s there), every citation of an anchor or section against what the cited text
says, invariants about the same thing against each other, then other logic. The fixes it can quote
exactly, and its questions, become an analysis proposal, reviewed like any other; `spec proposal ID
--answer TEXT` answers a question. The proposal records its `scope` (`changed` against a HEAD, `all`,
or `files`). A newer analysis that covers that scope, even one that finds nothing, supersedes the
earlier pending analyses nobody acted on: they are rejected with reason `superseded by ...`, and
their records are kept. Analyses filed by answering a question, partly applied ones, and ones with an
item decided in `spec ui` are left alone.

## Fixing lint findings

    spec fix lensd.spec.md:40:7      the finding `spec lint` printed at that place
    spec fix lensd.spec.md:129 --code math-hazard

`spec fix` files a proposal that fixes one lint finding. It is reviewed and approved like any other,
and an anchor it edits gets its `Spec-Change:` trailer when committed. Some rules are fixed without a
model: backticks around a stray `$` that closes GitHub math (`math-hazard`), a byte order mark,
`#5.2` written for `§5.2`, `§5.2for` (a reference run into the next word), and a misspelled name with
exactly one close match (`ref-nearly`, `ref-dangling`). The others (`def-unreferenced`,
`anchor-long`, `def-duplicate`, `def-nested`, ...) ask the model with the rule's own instruction,
which lets it change how a requirement is written but never what it says. Findings about the project
rather than a file's text (`spec.toml` entries, encodings, symbolic links, duplicate stems) are exit 1
`not_fixable`, with what to do by hand. `--why` sets the commit message (default `Fix CODE: ...`). In
`spec ui`, ^P on a diagnostic, or in its popup, fixes it, even inside an anchor changed since `HEAD`.
A fix made without a model goes into the buffer at once (^Z undoes it); a model's fix goes to the
Checks sidebar.

## Path 1: a local change

For a change the user has asked for or agreed to:

1. Edit the anchor, then `spec propose` (agents) and wait for the decision.
2. Once it is applied, `spec status`; fix lint errors until it exits 0.
3. Update the tests that cite the anchor (`grep -rn 'Frame.Timing' .`).
4. Commit with the trailer lines `spec status` printed, unless the user's `approve --commit` already
   committed it.

## Path 2: a change that needs broader review

Open a pull request for that one spec change: the spec edit, the tests it requires, and nothing
else. Its description carries what a change request would:

    ## Problem
    What is wrong or missing, with evidence (file:line, test, incident).
    ## Current text
    The anchor as it is now, verbatim.
    ## Proposed text
    The anchor as it should be, verbatim.
    ## Rationale
    Why this wording, and which alternatives were rejected.
    ## Analysis
    Anchors and code affected: `spec refs Frame.Timing`, contradictions found, tests to change.

The commit inside the PR carries the trailers as in path 1. There are no change-request files,
directories or numbers: the PR and its discussion are the record.
