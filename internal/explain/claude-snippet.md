<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

## Specs

This project's specification is in `*.spec.md` files, managed with the `spec` tool. Before reading or
editing them, run `spec explain agent`; `spec adopt` and `spec explain` list the other topics.
- Edit spec files directly, then run `spec status` and do what its `next:` line says.
- A commit that adds, changes, removes or renames an invariant or definition carries the exact
  `Spec-Change:`/`Spec-Rename:` trailer lines that `spec status` prints.
- In shell commands, write anchor names without `$` (`spec show Frame.Timing`).
- Cite the spec from tests as `// Spec: $Name "quoted text"`, and from TLA+ models as
  `\* Spec: $Name "quoted text"` above each invariant.
- Propose an edit of an existing TLA+ model with `spec change --tla PATH --body-file NEW`, not by
  editing it in place.
