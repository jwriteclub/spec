<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Markup

A spec is a set of `*.spec.md` files in GitHub-flavored Markdown, plus three things: named anchors
written `$Name`, definition labels, and numbered sections referenced with `§`. Everything renders
normally on GitHub. The file set is configured in `spec.toml` (see `spec explain config`).

## Names

`Name := Segment ("." Segment)*`, `Segment := [A-Z][A-Za-z0-9]*`, and the whole name must contain at
least one lowercase letter.

| Valid           | Invalid         | Why                                  |
|-----------------|-----------------|--------------------------------------|
| `Frame.Timing`  | `frame.timing`  | a segment starts with an uppercase letter |
| `HttpTimeout`   | `HTTP`          | no lowercase letter                  |
| `Sdk.Lease2`    | `Frame_x`       | `_` is not allowed                   |
| `Interval`      | `Frame.2x`      | a segment starts with a letter       |

Group names by area with a prefix: `$Relay.OneSession`, `$Relay.NoOrphans`, `$Time.Media`. The name
is the anchor's identity; its file, position and section number are not.

## References

Write `$Name` anywhere in prose: `Every fragment obeys $Frame.Timing.` The rules:

- The `$` must not follow a letter, digit, `_` or `$`: `a$Frame.Timing` and `$$Frame.Timing` are
  plain text. Anything else may come before it: `($Frame.Timing)`, `"$Frame.Timing"`, `*$Frame.Timing*`.
- `\$Frame.Timing` is an escape and stays plain text.
- **Maximal munch.** After the `$` the scanner takes the longest run of `[A-Za-z0-9_]`, taking a `.`
  only when another of those characters follows it. In `obeys $Frame.Timing.` the last period is
  punctuation; in `$Frame.Timing's` the apostrophe ends the name.
- **No partial matches.** If the whole run is not a valid name, none of it is a reference:
  `$Frame.timing` is plain text, not `$Frame` followed by `.timing`. Hint `ref-nearly` suggests the
  anchor you probably meant. So shell and money text is safe: `$HOME $PATH $5.00 $1 $USD` contains no
  reference.
- **Opaque regions** never contain references: code spans, fenced and indented code blocks, raw HTML
  (HTML blocks, and inline tags with their attributes), autolinks, link and image destinations and
  titles, and front matter (`---` YAML or `+++` TOML at the very start of the file). Text is not
  opaque even when it sits between inline tags or inside a link: `[$Frame.Timing](notes.md)` is a
  reference.
- A reference to a name that has no definition is error `ref-dangling`.

**GitHub math hazard** (warning `math-hazard`; `ref-adjacent` is its old name). GitHub renders
`$...$` as inline math. A `$` closes the math only when the character before it is not a space and
the character after it is not a letter, digit or `_`. A reference's own `$` is followed by a letter,
so it never closes math: `$Frame.Timing/$Time.Media` and `$Frame.Timing ($Time.Media)` are plain
text. The hazard is a stray `$` later on the same line: `$Frame.Timing costs 5$ today` and
`$Frame.Timing$` render as math. Put the stray `$` and its word in backticks (`` `5$` ``) or reword.
A backslash does not help: `x\$` still closes the math. A `$` inside code, HTML or a link
destination never counts.

## Anchors: invariants and definitions

An anchor is an invariant (`inv`: a property that MUST hold) or a definition (`def`: vocabulary that
invariants rely on). Both are referenced the same way, as `$Name`.

### Paragraph form

A bold label at the very start of a top-level paragraph:

    **$Frame.Timing.** Every fragment MUST have a duration in (0, 5 s].
    **$Frame.Timing (fragment timing).** The same, with a title.
    **Def $Time.Media.** Media time is an integer count of 90 kHz ticks.
    **Def $Time.Media (media time).** A definition with a title.

The text inside the bold must match exactly `^(Def[ ]+)?\$NAME([ ]+\(TITLE\))?\.?$`, where TITLE has
no parentheses or newlines and `Def` is case-sensitive. `__...__` works like `**...**`. The period may
sit inside the bold, outside it (`**$Frame.Timing**. Text`), or be left out.

The label's `$Name` is the definition, not a reference. Bold `$Name` anywhere else is an ordinary
reference: `Frames obey **$Frame.Timing** too.` Careful: a paragraph that *starts* with a bold
reference is a label, so `**$Frame.Timing** is strict.` defines `$Frame.Timing` again (error
`def-duplicate`). Start the sentence another way, or drop the bold.

A `$Name` inside a title is a reference, but titles are not hashed, so a change there is invisible to
change detection (hint `ref-in-title`). Put dependencies in the body:
`**$Frame.Titled (depends on $Time.Media).**` is flagged.

### Definitions are top-level only

A label counts only in a paragraph directly under the document. In a list item, blockquote or table
cell it is error `def-nested`:

    - **$Frame.InList.** Text.          def-nested
    > **$Frame.InQuote.** Text.         def-nested

Write the label as its own paragraph and put the list directly after it (next section). This keeps
every anchor's body unambiguous.

### Body

The body is the labelled paragraph, plus a list or blockquote that starts on the very next line:

    **$Sdk.Preempt (preempt only as needed).** A viewer MUST preempt only when:
    - fewer leases are preempted than viewers are waiting, and
    - the victim is the newest analysis lease, else the oldest viewer lease.

A blank line ends the body: a list after a blank line is not part of the anchor. A fenced code block on
the next line is never part of the body (hint `anchor-code-after`); move what matters into the body's
text. A body over 25 lines draws warning `anchor-long`: split the anchor or use the section form.

The body hash ignores reflowing: line breaks and runs of whitespace count as one space, except inside
code, where every byte counts. The title and the kind are not hashed; `spec diff` reports them
separately.

### Malformed labels (error `def-malformed`)

A paragraph that starts with bold text beginning with `$` (or a `Def`-like word and a `$`) followed by
a capital letter or a dotted lowercase name, but does not match the rule. Bold text such as
`**$5.00 off**`, `**$ amounts**` or `**$x**` is plain text.

| Written                          | Problem               | Write instead                      |
|----------------------------------|-----------------------|------------------------------------|
| `**$frame.timing.**`             | invalid name          | `**$Frame.Timing.**`               |
| `**$HTTP.**`                     | no lowercase letter   | `**$Http.Status.**`                |
| `**Definition $Time.Media.**`    | the prefix is `Def`   | `**Def $Time.Media.**`             |
| `**Def. $Time.Media.**`          | no period after `Def` | `**Def $Time.Media.**`             |
| `**$Frame.Timing extra words.**` | words after the name  | `**$Frame.Timing (extra words).**` |

### Section form

For a long anchor, the label is a whole heading. The title follows a colon:

    ### $Relay.OneSession: one session per camera
    ### Def $Relay.Term

The heading text must match exactly `^(Def[ ]+)?\$NAME(:[ ]+TITLE)?$`. The body is everything after
the heading up to the next heading of the same or a higher level; deeper plain headings stay inside
it. Another label inside that body, of either form, is error `def-nested`. A heading is either a
numbered section or a section-form anchor, never both; to number it, put the anchor under a numbered
heading.

**Setext trap.** A line followed by `---` or `===` is a heading, not a paragraph:

    **$Frame.Setext.**
    ---

defines nothing, because the heading's text is not the section form. Put a blank line before `---` to
get a horizontal rule.

## Numbered sections

A heading is a numbered section when its text is a number token, at least one space, and a title.

| Heading                        | Section | Note                                   |
|--------------------------------|---------|----------------------------------------|
| `## 1. Frames`                 | `1`     | a single number needs the period       |
| `## 5a. Retry`                 | `5a`    | one lowercase letter may follow        |
| `### 0.1 Time`                 | `0.1`   | a dotted number needs no period        |
| `### 1.2. Timing`              | `1.2`   | but may have one                       |
| `#### 5.2a. Retry`             | `5.2a`  |                                        |
| `## 10 Gbit uplink`            | none    | single number without a period: plain heading |
| `## 3 reasons`                 | none    | same                                   |
| `## 05. Zero padded`           | `05`    | compared literally: `05` is not `5`    |

The period rule exists so headings that merely start with a number stay plain headings. Only
top-level headings count, not headings inside lists or blockquotes. Numbers are per file, so every
file can start at 1. The same number twice in one file is error `section-dup`.

## Section references

`§`, then optionally one space or tab, then optionally a file qualifier `stem:`, then the number. The
stem is the file name minus `.spec.md`: in `hls.spec.md` it is `hls`.

| Text          | Meaning                                                        |
|---------------|----------------------------------------------------------------|
| `§5.2a`       | section 5.2a, resolved as below                                |
| `§ 5.2a`      | the same; one space or tab is allowed after the sign           |
| `§hls:1.2`    | section 1.2 of `hls.spec.md`                                   |
| `§04-queue:4.2` | section 4.2 of `04-queue.md`: a stem may start with a digit when it holds a letter or `-` |
| `§10:30`      | section 10, then `:30`: digits alone are never a stem          |
| `see §1.2.`   | section 1.2; a final period not followed by a digit is punctuation |
| `§5.2ab`      | not a reference: a letter or digit follows (hint `section-ref-malformed`) |
| `#5.2a`       | never a reference (hint `section-hash-ref` when it would resolve) |

Resolution order:

1. Qualified, `§hls:1.2`: only in `hls.spec.md`. Not there: error `section-ref-dangling`.
2. Unqualified, `§1.2`: the referencing file first.
3. Otherwise the project, if exactly one file has section 1.2.
4. Several other files have it: error `section-ref-ambiguous`. Qualify it: `§hls:1.2`.
5. No file has it: error `section-ref-dangling`.

Two spec files with the same stem (`a/hls.spec.md`, `b/hls.spec.md`) draw warning
`file-stem-duplicate`, and a qualified reference to that stem is ambiguous; rename one file.

No `§` key? On a Mac it is Option-6. Lint hint `section-hash-ref` points out `#1.2` written where
`§1.2` was meant; nothing is ever rewritten automatically.

## Names in shell commands

The shell expands `$Frame` to an empty string, so `spec show $Frame.Timing` runs `spec show .Timing`.
Write names without the sigil:

    spec show Frame.Timing
    spec refs Time.Media
    spec rename Frame.Timing Frame.Duration
    spec show 5.2a        spec show hls:5.2a

Commands also accept `$Frame.Timing` (single-quoted in the shell), `§5.2a` and `#5.2a`. An argument
that starts with `.` gets the error "did your shell expand `$`?". JSON output always writes `$Name`.

## Lint codes

- Errors: `ref-dangling`, `section-ref-dangling`, `section-ref-ambiguous`, `def-duplicate`,
  `def-malformed`, `def-nested`, `section-dup`, `project-no-files`, `project-file-missing`,
  `file-not-utf8`, `file-symlink`, `config-invalid`, `formal-ref-dangling`.
- Warnings: `def-unreferenced` (nothing outside the anchor refers to it), `math-hazard`, `file-bom`,
  `file-stem-duplicate`, `anchor-long`, `checked-by-missing` (off unless enabled in `spec.toml`),
  `formal-op-uncited`.
- Hints: `section-hash-ref`, `section-ref-malformed`, `ref-nearly`, `ref-in-title`,
  `anchor-code-after`, `formal-tag-malformed`, `formal-tag-block`, `formal-quote-stale`,
  `formal-check-limit`, `spell` (a word the spell check flagged; `spec explain spell`).

The `formal-*` codes are about tags in TLA+ models (`spec explain citations`). `spec lint` exits 4 on any error and 0 when there are only warnings and hints.
