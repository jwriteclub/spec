<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

A definition that nothing else references: its `$Name` appears only in its own label. A definition
exists so that invariants can rely on it, so an unreferenced one is either used without being cited,
or dead.
- Find the places that rely on the concept in words instead of by name: the same term, a synonym,
  or the quantity or clock it defines. Cite the definition there: add `$Name` where the sentence
  already uses the concept ("in wall-clock time" becomes "in wall-clock time ($Time.Wall)"), or
  replace the plain term with the reference when the sentence reads as well. Prefer invariants over
  prose; one to three citations at the places that depend on it most are enough.
- If nothing relies on it, the definition is dead: propose deleting its whole paragraph (`find` the
  full definition, `replace` empty) and say in `summary` that it is unused. Ask in `questions` when
  you are not sure.
- A citation names a concept the sentence already uses. Never add a reference that changes what a
  sentence requires, and do not reword anything else.
