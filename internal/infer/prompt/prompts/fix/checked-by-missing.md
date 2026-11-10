<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

An invariant whose body has no `Checked by:` line saying what checks it.
- Add a line `Checked by: NAME.` as the last line of its body (directly after its text, no blank
  line), naming the test or check that verifies it.
- Name only a test or check the text names for this invariant (in prose, a note, or another
  anchor). A name that merely fits the pattern of the other `Checked by:` lines is invented. Never
  invent one: if nothing in the specification says what checks it, propose no edit and ask in
  `questions`.
