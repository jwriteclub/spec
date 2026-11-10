<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

A `§N` reference that matches sections in more than one file.
- Replace it with the qualified form `§stem:N` of the section the sentence means: stem is the file
  name without `.spec.md`. Usually that is the section in the same file; decide from the wording.
- If one file numbers two sections the same, the fix is to renumber the duplicate (rule
  `section-dup`): propose no edit here and say so in `summary`.
- If the sentence does not say which section it means, propose no edit and ask.
