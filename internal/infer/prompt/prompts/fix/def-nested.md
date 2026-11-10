<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

A definition label inside a list item, blockquote, table cell or another anchor's body, where it
cannot define anything. Labels count only in top-level paragraphs.
- Move it out: make the label and its text a top-level paragraph of its own, directly before or
  after the container, and leave the container without it. If the definition had a list under it,
  the list goes directly under the new paragraph.
- Give the move as one edit: `find` covers the container, and `replace` holds the paragraph and
  what is left of the container. When the definition is the container's only item, drop the list
  marker (or the `>`) and the indentation, nothing else.
- Keep its text word for word.
- If it was not meant to define anything (an example or a quotation), drop the bold so it is plain
  text, and say so in `summary`.
