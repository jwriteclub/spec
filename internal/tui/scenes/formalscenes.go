// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package scenes

import "github.com/jwriteclub/spec/internal/tui"

// lockSpec and lockModel are the Formal fixture: a spec with $Name anchors and
// numbered sections, and a TLA+ model that cites both.
const lockSpec = `# lock

## 1. Terms

**Def $Lock.Holder (lock holder).** The one process whose record is in the lock file.

## 2. Rules

**$Lock.SingleWriter (single writer).** At most one $Lock.Holder writes the project at any time.

**$Lock.Release (release on exit).** A $Lock.Holder MUST release the lock before it exits.

### 2.1. Waiting

A writer that finds the lock held waits its turn, see $Lock.SingleWriter.

## 3. Readers

Readers take no lock. Waiting is in §2.1, and leaving in $Lock.Release.
`

const lockModel = `---- MODULE Lock ----
(* The project lock: one writer at a time. (* SANY nests these. *) "Not a string." *)
EXTENDS Naturals, FiniteSets

\* Spec-file: lock

CONSTANT Procs \* Derived-from: $Lock.Holder "one process"

VARIABLES
    holder, \* Spec: $Lock.Holder "whose record is in the lock file"
    waiting

vars == <<holder, waiting>>

NoHolder == "none"

Init ==
    /\ holder = NoHolder
    /\ waiting = {}

\* Spec: §2.1 "finds the lock held waits its turn"
Wait(p) ==
    /\ holder \notin {NoHolder, p}
    /\ waiting' = waiting \cup {p}
    /\ UNCHANGED holder

Acquire(p) ==
    /\ holder = NoHolder
    /\ holder' = p
    /\ waiting' = waiting \ {p}

\* Spec: $Lock.Release "release the lock before it exits"
Release(p) ==
    /\ holder = p
    /\ holder' = NoHolder
    /\ UNCHANGED waiting

Next == \E p \in Procs : Wait(p) \/ Acquire(p) \/ Release(p)

Live == Init /\ [][Next]_vars /\ WF_vars(Next)

\* Inv_SingleWriter: never two holders, in any of 0..2 states.
\* Spec: $Lock.SingleWriter "At most one $Lock.Holder writes the project"
Inv_SingleWriter ==
    Cardinality({p \in Procs : holder = p}) <= 1

====
`

// formalLinks walks a spec's link to its model and back.
var formalLinks = []Step{
	{Note: "rest the pointer on an anchor: the hover names the model and quotes the tag", Hover: "$Lock.SingleWriter", Capture: "hover-spec"},
	{Note: "^T follows it to the operator; the model is highlighted", Keys: []string{"ctrl+t"}, Capture: "jump"},
	{Note: "rest the pointer on the tag: the hover shows the spec text", Hover: `Spec: $Lock.SingleWriter`, Capture: "hover-model"},
	{Note: "the outline lists the operators, those with tags filled", Keys: []string{"esc", "alt+o"}, Capture: "outline"},
	{Note: "^B goes back to the spec", Keys: []string{"esc", "ctrl+b"}},
}

// formalScenes are the scenes of a TLA+ model beside its spec.
func formalScenes() []Scene {
	sizes := []Size{Size80, Size100, Size140, Size220}
	open := tui.Target{Name: "Lock.SingleWriter"}
	return []Scene{
		{
			Name:        "formal-links",
			Description: "A spec linked to its TLA+ model by tags: hover an anchor to see the model and the tag's quote, follow it to the highlighted operator, hover the tag to see the spec text, then the operator outline, and back.",
			Fixture:     Formal, Open: open, Sizes: sizes, Steps: formalLinks,
		},
		{
			Name:         "formal-links-256-ascii",
			Description:  "The same walk with the curated 256 colours and text glyphs.",
			Fixture:      Formal,
			Open:         open,
			Sizes:        sizes,
			Steps:        formalLinks,
			ReducedColor: true,
			ASCIIGlyphs:  true,
		},
	}
}
