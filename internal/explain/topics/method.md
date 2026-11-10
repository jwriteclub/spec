<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Method: spec-driven development as practiced here

The spec says what the system guarantees, and code and tests are checked against it. Five rules
follow from that:

1. **Spec first.** A guarantee is written in the spec before, or together with, the code that keeps
   it. When code and spec disagree, the code is wrong by default; changing the spec instead is a
   deliberate decision, recorded in the commit (see `spec explain workflow`).
2. **Named statements.** Each guarantee is one self-contained invariant with a name, such as
   `$Relay.OneSession`, not a sentence buried in a section. Definitions such as `$Time.Media` fix the
   vocabulary that invariants rely on. The name is the identity; file, order and section numbers can
   change freely.
3. **Every invariant says what checks it**, on a `Checked by:` line: a test, a property test, a model
   (a TLA+ invariant, with a mutant that must fail), or a runtime check. A checker that does not
   exist yet is named anyway and goes on the work list.
4. **Code cites the spec.** A test carries `// Spec: $Relay.OneSession "quoted text"` above its
   function, so a reader knows why it exists and the spec's tests can be found with grep (see
   `spec explain citations`).
5. **Drift is caught.** Every anchor has a body hash. Changing an invariant or definition shows up
   in `spec status` and needs a `Spec-Change:` trailer in the commit, which the `commit-msg` hook
   (`spec check`) enforces. A test whose quote no longer appears in the spec should fail.

## Why

When one model writes both the code and its tests, three kinds of drift pass every test: a requirement
misread the same way in both (timestamps as RFC 3339 strings instead of Unix nanoseconds); a
reasonable-looking default substituted for the specified value (1 GiB instead of 256 MB); and wiring
the spec implies but never states (no dispatcher, so every request hits a nil pointer). Only an
independent comparison against a written spec catches these. The comparison is mechanical when the
spec's statements are named, self-contained and checkable. Invariants left unnamed in prose are hard
to cite, so citations drift: one project had 8 test citations of a §17 that no longer existed.

## What belongs in a spec

- **Invariants:** properties that MUST hold at all times or at defined points. "At most one open RTSP
  session per stream name." Liveness is written as a bounded-time rule ("a stuck generation MUST be
  replaced within 10 s"), which a test can check.
- **Definitions:** units, clocks, bounds and terms, stated exactly. "(0, 5 s] means 1..450000 ticks
  inclusive." Invariants reference them as `$Time.Media` instead of re-deriving them.
- **Prose:** context for a reader without the code, the reasons behind rules, and an "Accepted
  behaviour" section for things that look like bugs but are intended. Prose has no anchors and needs
  no review to change.
- **Not:** implementation details that may change freely, test plans, wishes. A real requirement that
  the code breaks today is still an invariant, marked "Known violated today" in its body.

## A good invariant

    **$Queue.Capacity (queue capacity).** The queue MUST contain at most 100 pending requests
    (code: maxPending).
    Checked by: internal/queue/queue_test.go:TestCapacity.

It is one statement with MUST. It states its bounds ("at most", inclusive or not) and its units,
and uses definitions by reference. Values that also exist in code are marked `(code: name)` so
reviewers can find the matching value; when one changes, check whether the other needs updating. It
names its checker.

Verification, cheapest first: a unit test; a property test over generated inputs; a TLA+ model for
real interleavings (races, crash ordering), each with one mutant that deletes a guard and must fail,
which shows the model can catch a violation; a runtime check as a backstop. Write a model only where
interleavings matter.

## What the tool does today

- Read: `spec ls`, `spec show` and `spec refs` list and show anchors and their references.
- Check: `spec lint` lints the specs; `spec fmt` wraps them; `spec diff` and `spec status` diff
  anchors against git; `spec check` enforces commit trailers.
- Change: `spec rename` renames anchors across files; `spec propose`, `spec change` and `spec fix`
  file changes as proposals with a model's contradiction check, and `spec review` and `spec approve`
  decide them.
- Analyze: `spec analyze` checks edits for consistency and spelling.
- Models: tags link TLA+ models to the spec, and model edits are checked for syntax and semantics
  before they are filed (`spec explain citations`).
- Editors: `spec lsp` serves any LSP editor, and `spec ui` is a terminal editor that also proposes,
  reviews and analyzes.

It does not check citations in code yet (`spec explain citations`).
