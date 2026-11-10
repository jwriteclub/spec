<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Citations: pointing from code to the spec

A citation says which statement of the spec a test checks or a line of code enforces. It tells a
reader why the test exists, lets grep find the spec's tests, and, with a quote, shows when the spec's
text no longer says what the test checks.

## Tests

One line per anchor, directly above the test function. Lines may stack:

    // Spec: $Frame.Timing "decode time MUST equal the sum of all earlier durations"
    // Spec: $Frame.Numbering "segments are only dropped from the front"
    func TestTrackShape(t *testing.T) {

- `$Name` is the anchor. The quote is optional, but include one: copy it verbatim from the anchor's
  body (line breaks and runs of spaces may differ), and pick the clause the test actually checks.
- Property tests, model-based tests and fault-injection tests cite the same way, on the property or
  fault point.
- When the subject is a section's prose rather than an anchor, cite the section with its file:
  `// Spec: §hls:1.2 "quote"`. Code has no "same file", so always qualify section citations in code.

## Code

At the line that enforces an invariant, no quote:

    defer conn.Close() // $Relay.CloseOrder: only after the upstream reader has stopped

## Models

In a TLA+ model, one `\*` line directly above each invariant operator, naming the statement it checks:

    \* Spec: $Relay.OneSession "at most one open RTSP session per stream name"
    Inv_OneSession == Cardinality({s \in Streams : conn[s]}) <= 1

The tag is the only link: the spec does not change, so citing or renaming an operator costs no
`Spec-Change` trailer. A `Checked by:` line in the anchor stays optional. `spec` reads models listed
under `formal` in `spec.toml` (`spec explain config`). Each model has one mutant
(`RelayHub_Mut1_NoDeadCheck.tla`) that deletes a guard and must fail, which shows the model can catch
a violation. Files named `*_Mut*`, with no lowercase letter right after `_Mut` (so `Queue_Mutex.tla` is a
model), are read but do not count as coverage.

There are three tags, one per line, and lines may stack:

    \* Spec: <ref> ["verbatim quote"]            what this checks
    \* Derived-from: <ref> ["verbatim quote"]    where this comes from, not a check
    \* Spec-file: <stem>                         where bare section numbers look first

- `<ref>` is `$Name` or `§[stem:]N.N`: `$Queue.OneTurn`, `§4.2`, `§04-queue:4.2`. Use the anchor when
  there is one. Section numbers get reassigned; names do not.
- Text after the reference is commentary. The quote is the first `"..."` and ends at the next `"`, so it
  cannot hold one. In a `\*` comment it stays on the tag's line; in a block comment it may span lines.
- Quotes are compared with the cited text ignoring line breaks, letter case, `*` and backticks. Copy the
  clause the operator checks.
- A bare `§4.2` resolves in the module's `Spec-file:` files, then anywhere it is unique in the project.
  If several files have it, qualify it or add one `Spec-file:` line under the `MODULE` line.

What a tag binds to:

    \* Spec: §1                 in column 0: the next definition or declaration
    Inv_A == TRUE

    Claim(s) ==
        \* Spec: §2             indented in a body: this definition
        /\ held[s] = FALSE

    VARIABLES
        \* Spec: §3             above a name in a list: that name
        queue,
        sem,    \* Spec: §4     trailing a name: that name

A tag above `EXTENDS` or `INSTANCE` is about the module and binds to nothing. Models are parsed with
prudence, a Go port of SANY's parser, so declarations are the module's top-level ones and text
before the `MODULE` line or after its closing `====` is ignored, as SANY ignores it. A syntax error is
lint error `formal-syntax`, and the declaration holding it and those after it are missing until it is fixed. A
model without a syntax error is then checked with the modules it extends (`formal-semantic`, for
example an undefined name), which are looked up as SANY does: `<Name>.tla` beside the model, an
editor's unsaved text first, and the standard modules (Naturals, Sequences, TLC and so on) only when
the model's directory has no file of that name. Names match in case on every system, and nothing
outside the project is read. The check includes level checking, and a module nobody has is an error
on its name; `spec ui`'s status line says how far the check got. prudence spends at most a budget
of visits (1,000,000 by default) in each pass while it reads a model and the modules it loads; a model
that needs more stops there, and its semantic check is incomplete. That stop is hint
`formal-check-limit`, which says "the model hit the acquisition limit" and names the fix: raise it
for one run with `--max-acquisition-visits N`, or for the project with `max_acquisition_visits = N`
under `[tla]` in `spec.toml` (`spec explain config`). A check prudence stops for any other reason,
including a later inspection that runs out of the same budget, is a plain incomplete check with no
such hint. A symbolic operator (`a ++ b == ...`) cannot be named, so a tag on one draws a hint. Keep tags out of
`(* *)` comments: SANY ends the comment at the first `*)`, even inside a quote, which is a syntax error
in the model. Existing block tags are read, and each draws a hint.

Lint codes, all in `spec lint`:

| Code | Default | Fires on |
|------|---------|----------|
| `formal-syntax` | error | a syntax error in the model, as prudence parses it |
| `formal-semantic` | error | a semantic error in the model, such as an undefined name, as prudence checks it with the modules it extends |
| `formal-ref-dangling` | error | a reference or `Spec-file:` stem that resolves to nothing, or to several things |
| `formal-op-uncited` | warning | an `Inv_*` operator with no tag, outside mutants |
| `formal-tag-malformed` | hint | no usable reference (`rc-host §10`, `model bookkeeping`), an open quote, a symbolic operator |
| `formal-tag-block` | hint | a tag in a `(* *)` comment |
| `formal-quote-stale` | hint | a quote that is not in the cited text |
| `formal-check-limit` | hint | a model whose check stopped at the acquisition limit; raise `--max-acquisition-visits` or `[tla] max_acquisition_visits` |

A `Spec:` tag outside the mutants also satisfies `checked-by-missing` for the `$Name` it cites, and any
tag outside the mutants counts as a reference for `def-unreferenced`.
`spec rename` rewrites `$Name` in tags and their quotes, and `spec refs` lists tags. `spec ui` and the
language server navigate both ways (`spec explain integrations`).

## Tests without a spec statement

Say why the test exists instead of leaving it uncited:

    // Spec: DEFENSIVE - a nil reader must not panic
    // Spec: EDGE_CASE - a zero-length file round-trips
    // Spec: REGRESSION - BUG-FLUSH-2, corrupted buffer files were dropped silently

There is no INTERNAL category. A behaviour that matters enough to test usually belongs in the spec:
find the anchor, or report the gap to the user.

## Checking citations

`spec` does not read code yet. Planned: lint for a citation of a name that does not exist, for a quote
that no longer appears in the anchor's body, and for an invariant no test cites; and `spec rename`
rewriting citations in code. Until then:
- After `spec rename Old.Name New.Name`, update citations by hand:
  `grep -rn 'Old\.Name' --include='*.go' .`
- A project can enforce citations with one Go test, such as a `spec/spec_test.go`: collect the
  names (`spec ls --json`) and bodies (`spec show NAME --json`), walk the `_test.go` files for
  `Spec: $Name "quote"`, and fail when a cited name does not exist, when a quote is not in the body
  (compare with whitespace collapsed), or when an invariant has no citation at all.
