<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

A bold label at the start of a paragraph that looks like a definition but does not parse. The valid
labels are `**$Name.**`, `**$Name (title).**`, `**Def $Name.**` and `**Def $Name (title).**`: `Def`
exactly, the name in dotted segments that start with a capital letter, a title in parentheses with
no parentheses inside.
- Repair the label to the form it was meant to be ("**Definition $Time.Media.**" ->
  "**Def $Time.Media.**", "**$Frame.Timing extra words.**" -> "**$Frame.Timing (extra words).**").
  Change only the label.
- If the name itself is invalid, choose the valid name the references use; if none does, ask.
- If the bold text was never meant to define anything, change it so it no longer reads as a label
  (drop the bold or reword the start of the sentence), keeping its meaning.
