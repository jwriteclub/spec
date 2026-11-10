<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

A fenced code block directly after an anchor's body. It is not part of the body, so editing the
code does not change the anchor's hash.
- If the code states part of the requirement (a format, a grammar, exact values the anchor relies
  on), make it part of the body: put it inside a list item that starts on the line right after the
  paragraph ("- Format:" followed by the fence, indented under the item), keeping the code byte for
  byte.
- If the code is only an example, propose no edit and say in `summary` that the hint can be left
  as it is.
