<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Adopt: adding specs to a project

Steps for when the user wants to start adding specs to a project. The running example is lensd, a
made-up camera gateway, taken through a survey, a proposal, a draft and a review to 5 definitions and
25 invariants, each naming its checker. Read `spec explain method` first if you have not.

## 1. Survey the code and docs

Read the code, tests and docs for behaviour that must hold. Change nothing. Write the survey down (in
the project's `notes/` directory if it has one), one entry per candidate invariant:

    I-3. Every fragment handed to ffmpeg is a whole moof+mdat with a duration > 0.
    - Enforced at sources.go:62-104; the delta defaults to one frame when <= 0 or > 5 s.
    - Holds: yes, on the paths read. Test: TestMuxTS checks the first frame only.

For each, record where it is enforced (`file:line`), whether it holds (if not, the exact path that
breaks it), and which test covers it. Look hardest at:
- claims already written down: package comments, READMEs, design notes. If lensd's `sdk.go` says
  "the recorder never sees more sessions than its limit" and a code path breaks it, that is the first
  gap;
- shared state and lock order (races, leaks, orphans);
- units, clocks and bounds (ticks or seconds, inclusive or not, wall or monotonic);
- what happens on timeout, cancellation, out-of-memory and restart.

Rank what you found: which invariants need a TLA+ model (real interleavings), which a property test,
which a plain test, and which nothing yet.

## 2. Propose a layout and a namespace, then ask the user

First re-check against the code every survey claim the proposal depends on, and say which ones
held. Then propose:
- **Files.** One `<project>.spec.md` for a small system. Several for a larger one, split by
  subsystem, for example `rws.spec.md`, `index.spec.md` and `hls.spec.md`. The file name minus
  `.spec.md` is the stem used in `§hls:1.2`. Sections are numbered per file.
- **Namespace.** One prefix per area: `$Frame.*`, `$Relay.*`, `$Sdk.*`, `$Hub.*`, `$Time.*`. List
  every proposed name with a one-line statement.
- **Checkers.** For each invariant, the test, property test, model or runtime check that will verify
  it, and whether it exists yet.
- **Left out, and why.** Single-goroutine or single-mutex code gets property tests; models are for
  real interleavings. Liveness is written as bounded-time rules, not as model properties.

Stop and ask the user. Do not write the spec before they have agreed on the layout and the names.

## 3. `spec init`

In the directory that will hold `spec.toml` (usually the repository root; spec files may sit anywhere
below it):

    spec init --name lensd

This writes `spec.toml`, a skeleton `lensd.spec.md` (an existing one is kept unchanged)
and a `.spec/` line in `.gitignore`, and prints a suggested CLAUDE.md section.
Show that section to the user; do not add it yourself. `spec init` refuses to run where `spec.toml`
already exists. For an existing spec file with another name (`SPEC.md`, `docs/design.md`), list it in
`spec.toml` under `files` (see `spec explain config`) and delete the skeleton, or `git mv` it to
`<name>.spec.md` before running `spec init`.

## 4. Write definitions first, then invariants

Write these in order:
1. A short context section for a reader without the code: what the system is, its parts, its
   numbers.
2. `## 0. Definitions`: time, units, intervals, bounds and terms, as `**Def $Time.Media (media
   time).**` paragraphs. Settle "inclusive or not", "which clock" and "what counts as one" here, once.
3. Numbered sections per subsystem (`## 1. Frames, segments and tracks`), each with its invariants as
   `**$Frame.Timing (fragment timing).**` paragraphs. One statement each, using MUST, referencing the
   definitions as `$Time.Media`.
4. A `Checked by:` line in every invariant's body, naming the test (`file:TestName`), model
   (`tla/Model.tla:Inv_Name`) or runtime check. A checker that does not exist yet is named anyway and
   added to the work list.
5. `Known violated today` in the body of an invariant the code currently breaks, with the reason.
6. An `## Accepted behaviour` section (prose, no anchors) for things that look like bugs but are
   intended, such as "viewer rotation: with more viewers than slots, each newcomer takes the oldest
   viewer's slot".

Values that also exist in code are marked `(code: mergeGap)` so reviewers can find the matching value.
When one changes, check whether the other needs updating.
`spec explain markup` has the exact syntax.

## 5. `spec lint`

    spec lint
    spec ls
    spec ls --unreferenced
    spec show Frame.Timing

Fix every error; `spec lint` must exit 0. Read the warnings: `def-unreferenced` on a definition
usually means an invariant restates it instead of referencing it.

## 6. Review with the user

Before the walkthrough, get independent reviews of the draft if you can (two work well: one for
logic, one for failure modes). Present the findings grouped:
- contradictions between invariants;
- under-specified statements (equal timestamps? 32-bit wrap? which clock?);
- missing bounded-time rules (what bounds this wait, this retry, this memory?);
- behaviour to accept rather than specify.

Walk the user through the definitions, then the invariants. Resolve every finding with them, and
change the text to match their answers.

## 7. Commit on a branch and open a PR

    git switch -c spec
    spec status
    git add spec.toml lensd.spec.md .gitignore
    git commit

Every anchor is new, so `spec status` prints one `Spec-Change:` line per anchor
(`Spec-Change: $Frame.Timing - -> inv:8b77d0a4`). Put them at the end of the commit message, after a
blank line; the `commit-msg` hook, once installed, requires them (`spec explain workflow`). Open a PR
whose description lists the invariants, their checkers, and which ones are known violated today.
Retrofitting `// Spec:` citations on existing tests (`spec explain citations`) and writing the missing
tests are follow-up work, each test committed with the spec change it checks.

## What not to do

- No invariants for aspirational behaviour. A real requirement the code breaks today is an invariant
  marked `Known violated today`; a wish is prose or nothing.
- Do not transcribe the code. Specify the guarantee the system must give; where the code disagrees
  with what it should do, report it and ask.
- Do not fix bugs found in the survey as part of adoption; list them for the user.
- No change-request files, review-note files or numbering schemes. The process is git, commit
  trailers and pull requests.
- Do not model what has no interleavings, and keep one mutant per model.

## Migrating existing citations

- **Numbered ids** (`INV-FRAME-1`, `DEF-TIME-1`) become names (`$Frame.Timing`, `Def $Time.Media`).
  Keep a table of old id, new label and title in the PR description. Replace every citation of an
  old id in the spec with the `$Name`, and in code and tests change `// Spec: INV-FRAME-1 §1.2
  "quote"` to `// Spec: $Frame.Timing "quote"`. Nothing checks code citations yet, so find them with
  `grep -rn 'INV-FRAME-1' .`.
- **Section citations** (`§4.2`) keep working when the headings keep their numbers. Write single
  numbers with a period (`## 4. Queue`) and dotted ones as `### 4.2 Lanes`. A citation of another
  file's section becomes `§stem:4.2`.
- **Unnamed invariants in prose** ("the primary serialization invariant is...") become anchors when
  their section is next revisited. A large existing spec does not need converting all at once:
  `spec lint` is useful with zero anchors, and each pass adds anchors to the statements it touched.
