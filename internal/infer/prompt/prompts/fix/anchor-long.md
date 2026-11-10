<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

An anchor whose body is longer than 25 lines. Split it into two or more anchors of the same kind,
each a paragraph with its own label, so each states one requirement or one concept.
- Keep the original name on the part that holds the main requirement; give each new part a name in
  the same family (`$Frame.Timing` -> `$Frame.Timing.Gap`), following the file's naming.
- Where one part depends on another, reference it by name in the body.
- Keep every requirement, number and condition word for word: the split moves text, it does not
  reword, drop or merge anything. Lists stay directly under the paragraph they belong to.
- Give the whole split as one edit: `find` is the whole anchor text, `replace` the new anchors
  separated by blank lines.
- A list of separate requirements splits into groups of related items, each group an anchor that
  the original references. If no split keeps each part meaningful, propose no edit, say why in
  `summary` and ask how to group it. (The section form is no way out: it has the same limit.)
