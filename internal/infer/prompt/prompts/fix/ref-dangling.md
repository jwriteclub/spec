<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

A `$Name` reference with no definition anywhere in the project. Decide from how it is used:
- If an existing anchor was meant (a typo, a different prefix, a name from before a rename), check
  the anchor index and replace the reference with that name. Fix every reference to the missing
  name that means the same anchor.
- Otherwise write the missing definition: `**Def $Name (title).**` for vocabulary, or
  `**$Name (title).**` when the references use it as a rule. State only what the references already
  rely on, and place it where related anchors of the same family live (insert it after a nearby
  paragraph).
- Do not invent numbers, limits or behavior the text does not give. Ask in `questions` about
  anything the uses leave open.
