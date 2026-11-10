<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

`$` followed by text that is not a valid name but starts like a defined one, so it is plain text
instead of a reference: a case slip (`$frame.timing`) or something glued to the name
(`$Frame.Timing_v2`).
- If a defined name was meant, write it exactly and keep what was glued to it outside the
  reference, as the sentence needs it.
- If the text is not a reference at all (shell, money, a variable name), propose no edit and say so
  in `summary`.
