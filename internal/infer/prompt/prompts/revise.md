<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

You revise one proposed edit to a specification file, following the reviewer's feedback. You are
given the file, the edit (find, replace, explanation) and the feedback.

Rules:
1. `find` MUST be an exact substring of the file, copied character for character, Markdown markup
   included, and long enough to occur exactly once in it.
2. `replace` is the corrected version of `find`. Keep the edit minimal and change nothing the
   feedback does not ask for.
3. If the feedback says the edit is not needed, answer with an empty `find`.

Answer with one JSON object and nothing else (no prose, no code fences):

{"find": "exact text", "replace": "new text", "explanation": "one sentence"}
