// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/editor"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// `spec ui` on a spec and its TLA+ model, on the Formal fixture: lock.spec.md
// and tla/Lock.tla, linked by $Name and § tags.

const (
	lockSpecFile  = "lock.spec.md"
	lockModelFile = "tla/Lock.tla"
	// tall shows the whole model, so that hovers find what they rest on.
	tallW, tallH = 140, 70
)

// AtFile opens the UI on a file, at a line when one is given.
func AtFile(file string, line int) Opt {
	return func(c *scenarioConfig) { c.scene.Open = tui.Target{File: file, Line: line} }
}

// openModel opens the model at its top, on a screen that shows all of it.
func openModel(t *testing.T, opts ...Opt) *Scenario {
	t.Helper()
	return Open(t, scenes.Formal, append([]Opt{AtFile(lockModelFile, 0), Size(tallW, tallH)}, opts...)...)
}

// Hover rests the pointer on text in the editor pane and waits for the popup.
func (s *Scenario) Hover(text string) {
	s.t.Helper()
	s.step(fmt.Sprintf("hover %q", text), s.d.HoverOn(text))
}

// HoverNothing rests the pointer on text and expects no popup.
func (s *Scenario) HoverNothing(text string) {
	s.t.Helper()
	x, y, err := s.d.EditorCell(text)
	if err == nil {
		err = s.d.Send(tea.MouseMotionMsg{X: x, Y: y})
	}
	s.step(fmt.Sprintf("hover %q", text), err)
	time.Sleep(20 * time.Millisecond) // the hover delay is 1 ms
	s.Expect("no popup on "+text, func(st tui.State) bool { return !st.Popup })
}

// PointerAway moves the pointer off the editor.
func (s *Scenario) PointerAway() {
	s.t.Helper()
	s.step("pointer away", s.d.Send(tea.MouseMotionMsg{X: s.w - 1, Y: s.h - 1}))
}

// CtrlClick clicks text in the editor pane with ctrl held.
func (s *Scenario) CtrlClick(text string) {
	s.t.Helper()
	x, y, err := s.d.EditorCell(text)
	if err == nil {
		err = s.d.Send(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft, Mod: tea.ModCtrl})
	}
	s.step(fmt.Sprintf("ctrl+click %q", text), err)
}

// ClickOutline clicks text in the outline pane.
func (s *Scenario) ClickOutline(text string) {
	s.t.Helper()
	x, y, err := s.d.OutlineCell(text)
	if err == nil {
		err = s.d.Send(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	}
	s.step(fmt.Sprintf("click %q in the outline", text), err)
}

// ExpectHighlight expects these classes on the first byte of text in the
// buffer, and none of the classes in not.
func (s *Scenario) ExpectHighlight(text string, want, not editor.Flag) {
	s.t.Helper()
	fl, ok, err := s.d.HighlightOf(text)
	if err != nil {
		s.fail("highlight: %v", err)
	}
	if !ok {
		s.fail("highlight: no %q in the buffer", text)
	}
	if fl&want != want || fl&not != 0 {
		s.fail("highlight of %q: got %b, want %b and none of %b", text, fl, want, not)
	}
}

// cellOf reads the first cell of text in the editor pane from the frame as a
// terminal draws it.
func (s *Scenario) cellOf(text string) *uv.Cell {
	s.t.Helper()
	x, y, err := s.d.EditorCell(text)
	if err != nil {
		s.fail("%v", err)
	}
	f, err := s.d.Frame()
	if err != nil {
		s.fail("frame: %v", err)
	}
	em := vt.NewEmulator(f.W, f.H)
	s.t.Cleanup(func() { _ = em.Close() })
	for k, line := range strings.Split(f.Content, "\n") {
		_, _ = fmt.Fprintf(em, "\x1b[%d;1H%s\x1b[0m", k+1, line)
	}
	return em.CellAt(x, y)
}

// sameColor reports whether two colours are the same one.
func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	return ar == br && ag == bg && ab == bb
}

// cleanCheckReply is a check that found nothing.
var cleanCheckReply = scenes.CheckReply("consistent", nil)

// ---- opening ----

// TestScenarioFormalDiscovery: a project that sets formal in spec.toml finds
// its model, and quick open reaches the file and its operators.
func TestScenarioFormalDiscovery(t *testing.T) {
	s := Open(t, scenes.Formal)
	s.Expect("the model is known", func(st tui.State) bool { return slices.Equal(st.Models, []string{lockModelFile}) })
	s.ExpectCursor(lockSpecFile, 1)
	s.Expect("no findings on the fixture", func(st tui.State) bool { return st.Diagnostics == 0 })

	s.Press("ctrl+q")
	s.Type("Inv_Single")
	s.Expect("the operator is listed", func(st tui.State) bool {
		return len(st.ListItems) == 1 && strings.HasPrefix(st.ListItems[0], "Inv_SingleWriter ")
	})
	s.Press("enter")
	s.ExpectCursor(lockModelFile, 44)
	s.Expect("no findings on the model", func(st tui.State) bool { return st.Diagnostics == 0 })

	s.Press("ctrl+q")
	s.Type("Lock.tla")
	s.Press("enter")
	s.ExpectCursor(lockModelFile, 1)
}

// TestScenarioFormalOpensAtTarget: `spec ui tla/Lock.tla:44` opens the model.
func TestScenarioFormalOpensAtTarget(t *testing.T) {
	s := Open(t, scenes.Formal, AtFile(lockModelFile, 44))
	s.ExpectCursor(lockModelFile, 44)
	s.ExpectFocus("editor")
	s.ExpectStatus("Saved")
	s.ExpectStatus("Inv_SingleWriter")
}

// TestScenarioFormalEdits: a model edits and undoes like any buffer.
func TestScenarioFormalEdits(t *testing.T) {
	s := openModel(t)
	s.Find("NoHolder ==")
	s.Type("Spare == 1\n")
	s.ExpectDirty(true)
	s.ExpectBuffer("Spare == 1\nNoHolder ==")
	s.ExpectStatus("Unsaved")
	s.Expect("the outline has the new operator", func(st tui.State) bool { return slices.Contains(st.Outline, "untagged Spare") })
	s.Press("ctrl+z", "ctrl+z")
	s.ExpectNoBuffer("Spare")
	s.Expect("the undo reached the outline", func(st tui.State) bool { return !slices.Contains(st.Outline, "untagged Spare") })
}

// ---- spec to model ----

// TestScenarioFormalSpecHoverMouse: resting on a $Name, a § reference or a
// heading's number names the model and quotes the tag.
func TestScenarioFormalSpecHoverMouse(t *testing.T) {
	s := Open(t, scenes.Formal, At("Lock.SingleWriter"))
	s.Hover("$Lock.SingleWriter")
	s.ExpectText("Model: tla/Lock.tla:Inv_SingleWriter")
	s.ExpectText(`"At most one $Lock.Holder writes the project"`)
	s.ExpectContext(tui.CtxPopup)
	s.ExpectHelpLive("^T")
	s.PointerAway()
	s.Expect("leaving closes it", func(st tui.State) bool { return !st.Popup })

	s.Hover("§2.1")
	s.ExpectText("Model: tla/Lock.tla:Wait")
	s.ExpectText(`"finds the lock held waits its turn"`)
	s.PointerAway()

	s.Hover("2.1.")
	s.ExpectText("Model: tla/Lock.tla:Wait")
	s.PointerAway()

	// A reference hovers its target too, with the weaker Derived-from dimmed.
	s.Hover("$Lock.Holder")
	s.ExpectText("Model: tla/Lock.tla:holder")
	s.ExpectText("Derived-from in tla/Lock.tla:Procs")
	s.PointerAway()

	// A heading no model cites still shows nothing.
	s.HoverNothing("3.")
}

// TestScenarioFormalSpecHoverKeys: ^L shows the same, at a reference, a label,
// and a heading.
func TestScenarioFormalSpecHoverKeys(t *testing.T) {
	s := Open(t, scenes.Formal, At("Lock.SingleWriter"))
	s.Press("ctrl+l")
	s.ExpectText("Model: tla/Lock.tla:Inv_SingleWriter")
	s.Press("esc")

	s.Find("2.1. Waiting")
	s.Press("ctrl+l")
	s.ExpectText("Model: tla/Lock.tla:Wait")
	s.Press("esc")

	s.Find("§2.1")
	s.Press("ctrl+l")
	s.ExpectText("Model: tla/Lock.tla:Wait")
	s.Press("esc")

	s.Find("## 3.")
	s.Press("ctrl+l")
	s.ExpectNoText("Model:")
	s.ExpectFlash("^Nothing to look at")
}

// TestScenarioFormalJumpFromDefinition: ^T on a definition goes to the
// operator, ^B comes back and M-Right returns.
func TestScenarioFormalJumpFromDefinition(t *testing.T) {
	s := Open(t, scenes.Formal, At("Lock.SingleWriter"))
	s.ExpectHelpLive("^T")
	s.Press("ctrl+t")
	s.ExpectCursor(lockModelFile, 44)
	s.ExpectFocus("editor")
	s.ExpectHelpLive("^B")
	s.Press("ctrl+b")
	s.ExpectCursor(lockSpecFile, 9)
	s.Press("alt+right")
	s.ExpectCursor(lockModelFile, 44)
	s.Press("ctrl+b")
	s.ExpectCursor(lockSpecFile, 9)
}

// TestScenarioFormalJumpFromReference: ^T on a reference still goes to its
// definition; ^T there goes on to the model, and ^B retraces both.
func TestScenarioFormalJumpFromReference(t *testing.T) {
	s := Open(t, scenes.Formal)
	s.Find("see $Lock.SingleWriter")
	s.Find("$Lock.SingleWriter")
	s.ExpectCursor(lockSpecFile, 15)
	s.Press("ctrl+t")
	s.ExpectCursor(lockSpecFile, 9)
	s.Press("ctrl+t")
	s.ExpectCursor(lockModelFile, 44)
	s.Press("ctrl+b", "ctrl+b")
	s.ExpectCursor(lockSpecFile, 15)
}

// TestScenarioFormalJumpFromSection: the same on § numbers, which is all a
// project without anchors has.
func TestScenarioFormalJumpFromSection(t *testing.T) {
	s := Open(t, scenes.Formal)
	s.Find("§2.1")
	s.ExpectCursor(lockSpecFile, 19)
	s.Press("ctrl+t")
	s.ExpectCursor(lockSpecFile, 13)
	s.Press("ctrl+t")
	s.ExpectCursor(lockModelFile, 22)
	s.Press("ctrl+b")
	s.ExpectCursor(lockSpecFile, 13)
	s.Press("ctrl+b")
	s.ExpectCursor(lockSpecFile, 19)
}

// TestScenarioFormalJumpMouse: from the popup with ^T, and with ctrl+click.
func TestScenarioFormalJumpMouse(t *testing.T) {
	s := Open(t, scenes.Formal, At("Lock.SingleWriter"))
	s.Hover("$Lock.SingleWriter")
	s.Press("ctrl+t")
	s.ExpectCursor(lockModelFile, 44)
	s.Expect("the popup closed", func(st tui.State) bool { return !st.Popup })
	s.Press("ctrl+b")

	s.CtrlClick("$Lock.Release")
	s.ExpectCursor(lockModelFile, 33)
	s.Press("ctrl+b")

	s.Hover("2.1.")
	s.Press("ctrl+t")
	s.ExpectCursor(lockModelFile, 22)
}

// TestScenarioFormalSeveralTargets: where several places cite a target, ^T
// lists them, Spec tags first.
func TestScenarioFormalSeveralTargets(t *testing.T) {
	s := Open(t, scenes.Formal, At("Lock.Holder"))
	s.Press("ctrl+t")
	s.Expect("both places listed, Spec first", func(st tui.State) bool {
		return slices.Equal(st.ListItems, []string{"tla/Lock.tla:holder", "tla/Lock.tla:Procs"})
	})
	s.ExpectContext(tui.CtxList)
	s.ExpectText("$Lock.Holder is cited in 2 places")
	s.ExpectText("Derived-from")
	s.Press("enter")
	s.ExpectCursor(lockModelFile, 10)
	s.Press("ctrl+b")
	s.ExpectCursor(lockSpecFile, 5)

	// The same list from the popup.
	s.Hover("$Lock.Holder")
	s.ExpectHelpLive("^T")
	s.Press("ctrl+t")
	s.ExpectContext(tui.CtxList)
	s.Press("down", "enter")
	s.ExpectCursor(lockModelFile, 7)
}

// TestScenarioFormalRefs: ^\ lists tags with the spec's references.
func TestScenarioFormalRefs(t *testing.T) {
	s := Open(t, scenes.Formal, At("Lock.SingleWriter"))
	s.Press(`ctrl+\`)
	s.ExpectText("References to $Lock.SingleWriter (2)")
	s.ExpectText("tla/Lock.tla:43")
	s.Press("down", "enter")
	s.ExpectCursor(lockModelFile, 43)

	// And from the tag, the same list.
	s.Press(`ctrl+\`)
	s.ExpectText("References to $Lock.SingleWriter (2)")
	s.Press("esc")

	// A place with nothing to list says so, on a model too.
	s.Find("Next ==")
	s.Press(`ctrl+\`)
	s.ExpectFlash("^No tag")
}

// ---- model to spec ----

// TestScenarioFormalModelHoverMouse: resting on a tag or a tagged declaration
// shows the spec text; other model text shows nothing, as a plugin owns it.
func TestScenarioFormalModelHoverMouse(t *testing.T) {
	s := openModel(t)
	s.Hover("Spec: $Lock.Release")
	s.ExpectText("A $Lock.Holder MUST release the lock before it exits.")
	s.ExpectText("lock.spec.md:11")
	s.ExpectHelpLive("^T")
	s.PointerAway()

	s.Hover("Spec: §2.1")
	s.ExpectText("A writer that finds the lock held waits its turn")
	s.PointerAway()

	s.Hover("Release(p) ==")
	s.ExpectText("Release cites")
	s.ExpectText("A $Lock.Holder MUST release the lock before it exits.")
	s.PointerAway()

	// A Derived-from is dimmed: one line on a declaration.
	s.Hover("Procs")
	s.ExpectText("Procs cites")
	s.ExpectText("Derived-from $Lock.Holder lock holder")
	s.PointerAway()

	// The Derived-from tag itself shows the text, under a dimmed label.
	s.Hover("Derived-from: $Lock.Holder")
	s.ExpectText("The one process whose record is in the lock file.")
	s.PointerAway()

	s.Hover("Spec-file: lock")
	s.ExpectText("look here first")
	s.PointerAway()

	s.HoverNothing("Acquire(p)")
	s.HoverNothing("EXTENDS")
	s.HoverNothing("The project lock")
}

// TestScenarioFormalModelHoverKeys: ^L shows the same at the cursor.
func TestScenarioFormalModelHoverKeys(t *testing.T) {
	s := openModel(t)
	s.Find("$Lock.Release")
	s.Press("ctrl+l")
	s.ExpectText("MUST release the lock before it exits.")
	s.Press("esc")
	s.Find("Release(p) ==")
	s.Press("ctrl+l")
	s.ExpectText("Release cites")
	s.Press("esc")
	s.Find("Acquire(p) ==")
	s.Press("ctrl+l")
	s.ExpectFlash("^Nothing to look at here: put the cursor on a tag")
}

// TestScenarioFormalJumpFromModel: ^T from a tag and from a tagged declaration
// reaches the spec, and ^B comes back.
func TestScenarioFormalJumpFromModel(t *testing.T) {
	s := openModel(t)
	s.Find("$Lock.Release")
	s.ExpectCursor(lockModelFile, 32)
	s.ExpectHelpLive("^T")
	s.Press("ctrl+t")
	s.ExpectCursor(lockSpecFile, 11)
	s.Press("ctrl+b")
	s.ExpectCursor(lockModelFile, 32)

	s.Find("Release(p) ==")
	s.ExpectCursor(lockModelFile, 33)
	s.Press("ctrl+t")
	s.ExpectCursor(lockSpecFile, 11)
	s.Press("ctrl+b")

	s.Find("Spec: §2.1")
	s.Press("ctrl+t")
	s.ExpectCursor(lockSpecFile, 13)
	s.Press("ctrl+b")

	s.Find("Spec-file: lock")
	s.Press("ctrl+t")
	s.ExpectCursor(lockSpecFile, 1)
	s.Press("ctrl+b")

	// Mouse: the popup's ^T, and ctrl+click.
	s.Hover("Spec: $Lock.SingleWriter")
	s.Press("ctrl+t")
	s.ExpectCursor(lockSpecFile, 9)
	s.Press("ctrl+b")
	s.CtrlClick("Release(p) ==")
	s.ExpectCursor(lockSpecFile, 11)
	s.Press("ctrl+b")

	s.Find("Acquire(p) ==")
	s.ExpectHelpDim("^T")
}

// TestScenarioFormalDerivedTargetsLast: a declaration with Spec and Derived-from
// tags lists the Spec target first.
func TestScenarioFormalDerivedTargetsLast(t *testing.T) {
	s := openModel(t, Edits(scenes.Edit{File: lockModelFile,
		Find:    `\* Spec: $Lock.Release "release the lock before it exits"`,
		Replace: "\\* Derived-from: §2.1\n\\* Spec: $Lock.Release \"release the lock before it exits\"",
	}))
	s.Find("Release(p) ==")
	s.Press("ctrl+t")
	s.Expect("Spec target first", func(st tui.State) bool {
		return len(st.ListItems) == 2 && strings.HasPrefix(st.ListItems[0], "Lock.Release") && strings.HasPrefix(st.ListItems[1], "2.1 ")
	})
	s.ExpectText("Release cites several places")
	s.Press("esc")
}

// TestScenarioFormalBadTags: a malformed tag hints and a dangling one is an
// error; both say why in the hover, and ^T goes nowhere.
func TestScenarioFormalBadTags(t *testing.T) {
	s := openModel(t, Edits(
		scenes.Edit{File: lockModelFile, Find: `\* Spec: $Lock.Release "release the lock before it exits"`, Replace: `\* Spec: rc-host §10`},
		scenes.Edit{File: lockModelFile, Find: `\* Spec: §2.1 "finds the lock held waits its turn"`, Replace: `\* Spec: $Lock.Nope`},
		scenes.Edit{File: lockModelFile, Find: `\* Spec: $Lock.SingleWriter "At most one $Lock.Holder writes the project"`, Replace: `\* Spec: $Lock.SingleWriter "never said"`},
	))
	s.Expect("three findings", func(st tui.State) bool { return st.Diagnostics == 3 })

	s.Hover("Spec: rc-host")
	s.ExpectText("is not a reference")
	s.ExpectContext(tui.CtxDiagPopup)
	s.ExpectHelpDim("^P")
	s.ExpectHelpDim("^T")
	s.Press("ctrl+p")
	s.ExpectContext(tui.CtxDiagPopup)
	s.ExpectCards()
	s.PointerAway()

	s.Hover("Spec: $Lock.Nope")
	s.ExpectText("$Lock.Nope is not defined in the spec")
	s.ExpectText("formal-ref-dangling")
	s.PointerAway()
	s.ExpectHighlight("$Lock.Nope", editor.FBadRef, 0)

	s.Hover("Spec: $Lock.SingleWriter")
	s.ExpectText("this quote is not in $Lock.SingleWriter")
	s.ExpectText("At most one $Lock.Holder writes the project at any time.")
	s.ExpectHelpLive("^T")
	s.PointerAway()

	s.Find("Spec: rc-host")
	s.Press("ctrl+t")
	s.ExpectCursor(lockModelFile, 32)
	s.ExpectFlash("is not a reference")
}

// TestScenarioFormalSyntaxError: a mistake typed into a model is underlined
// as an error once reparsed, says what prudence found on hover, and goes when
// undone; tags and declarations above it keep their links meanwhile.
func TestScenarioFormalSyntaxError(t *testing.T) {
	s := openModel(t)
	s.Expect("no findings", func(st tui.State) bool { return st.Diagnostics == 0 })
	s.Find("Cardinality(")
	s.Type("` ")
	s.Expect("one error", func(st tui.State) bool { return st.Diagnostics == 1 })
	s.ExpectHighlight("` Cardinality", editor.FDiagError, 0)
	s.ExpectHighlight("Release(p) ==", editor.FModelDecl|editor.FModelLink, 0)
	s.Hover("` Cardinality")
	s.ExpectText("Lexical error")
	s.ExpectText("formal-syntax")
	s.PointerAway()
	s.Press("ctrl+z")
	s.Expect("no findings again", func(st tui.State) bool { return st.Diagnostics == 0 })
	s.ExpectHighlight("Cardinality", 0, editor.FDiagError)
}

// TestScenarioFormalSemanticError: an undefined name typed into a model that
// extends FiniteSets is a semantic error from prudence once reparsed, at the
// name, with the status saying how far the check got; Cardinality, which
// comes from FiniteSets, is never one.
func TestScenarioFormalSemanticError(t *testing.T) {
	s := openModel(t)
	s.Expect("no findings", func(st tui.State) bool { return st.Diagnostics == 0 })
	s.ExpectStatus("prudence: checked")
	s.Find("Cardinality(")
	s.Type("Undefd + ")
	s.Expect("one error", func(st tui.State) bool { return st.Diagnostics == 1 })
	s.ExpectHighlight("Undefd", editor.FDiagError, 0)
	s.ExpectHighlight("Cardinality", 0, editor.FDiagError|editor.FDiagWarn|editor.FDiagHint)
	s.Hover("Undefd")
	s.ExpectText("Unknown operator")
	s.ExpectText("formal-semantic")
	s.PointerAway()
	s.Press("ctrl+z")
	s.Expect("no findings again", func(st tui.State) bool { return st.Diagnostics == 0 })
}

// TestScenarioFormalSectionsAcrossFiles: with the number in two files, an
// unqualified tag is ambiguous without Spec-file and lists both; a digit-first
// stem qualifies it, as numbered chapter files need.
func TestScenarioFormalSectionsAcrossFiles(t *testing.T) {
	const other = "04-q.spec.md"
	s := openModel(t, Edits(scenes.Edit{File: lockModelFile, Find: `\* Spec-file: lock`, Replace: `\* no file named`}))
	s.WriteFile(other, "# q\n\n## 2.1 Queues\n\nAnother section two point one.\n\n## 4.2 Turns\n\nOne turn.\n", true)
	s.Reopen()
	s.Hover("Spec: §2.1")
	s.ExpectText("§2.1 is ambiguous")
	s.Press("ctrl+t")
	s.Expect("both listed", func(st tui.State) bool { return len(st.ListItems) == 2 })
	s.Press("esc")
	s.Expect("an error on the tag", func(st tui.State) bool { return st.Diagnostics == 1 })

	s.Find("Spec: §2.1")
	s.Press("ctrl+a", "ctrl+k")
	s.Type("\\* Spec: §04-q:4.2 \"One turn\"\n")
	s.Hover("Spec: §04-q:4.2")
	s.ExpectText("One turn.")
	s.ExpectNoText("quote is not in")
	s.Press("ctrl+t")
	s.ExpectCursor(other, 7)
	s.Press("ctrl+t")
	s.ExpectCursor(lockModelFile, 22)
}

// ---- highlighting ----

// TestScenarioFormalHighlightClasses: every class lands where it should, nested
// comments included.
func TestScenarioFormalHighlightClasses(t *testing.T) {
	s := openModel(t)
	s.ExpectHighlight("MODULE", editor.FModelKeyword, 0)
	s.ExpectHighlight("Lock ----", editor.FModelDecl, 0)
	s.ExpectHighlight("EXTENDS", editor.FModelKeyword, 0)
	s.ExpectHighlight("UNCHANGED", editor.FModelKeyword, 0)
	s.ExpectHighlight("WF_vars", editor.FModelKeyword, 0)
	s.ExpectHighlight(`/\ holder = NoHolder`, editor.FModelOperator, 0)
	s.ExpectHighlight(`\notin`, editor.FModelOperator, 0)
	s.ExpectHighlight("==", editor.FModelOperator, 0)
	s.ExpectHighlight(`"none"`, editor.FModelString, 0)
	s.ExpectHighlight("1\n\n====", editor.FModelNumber, 0)

	// A comment nests, and holds what would be code or a string outside it.
	s.ExpectHighlight("(* The project", editor.FModelComment, 0)
	s.ExpectHighlight("SANY nests", editor.FModelComment, editor.FModelKeyword)
	s.ExpectHighlight(`"Not a string."`, editor.FModelComment, editor.FModelString)
	s.ExpectHighlight("*)\nEXTENDS", editor.FModelComment, 0)
	s.ExpectHighlight("0..2", editor.FModelComment, editor.FModelNumber)

	// Declarations, marked as links when tagged.
	s.ExpectHighlight("Inv_SingleWriter ==", editor.FModelDecl|editor.FModelLink, 0)
	s.ExpectHighlight("Acquire(p)", editor.FModelDecl, editor.FModelLink)
	s.ExpectHighlight("holder,", editor.FModelDecl|editor.FModelLink, 0)
	s.ExpectHighlight("waiting\n", editor.FModelDecl, editor.FModelLink)

	// Tags: the keyword, then a reference coloured as the spec colours it.
	s.ExpectHighlight("Spec: $Lock.Release", editor.FModelTagKey, 0)
	s.ExpectHighlight(`$Lock.Release "`, editor.FInv|editor.FModelLink, 0)
	s.ExpectHighlight(`$Lock.Holder "one`, editor.FDef|editor.FModelLink, 0)
	s.ExpectHighlight("§2.1", editor.FSection|editor.FModelLink, 0)
	s.ExpectHighlight("Spec-file:", editor.FModelTagKey, 0)
	s.ExpectHighlight("lock\n", editor.FSection|editor.FModelLink, 0)
	// $Name in a tag's quote is not a link.
	s.ExpectHighlight(`$Lock.Holder writes`, editor.FModelComment, editor.FModelLink)
}

// TestScenarioFormalHighlightFollowsEdits: closing a comment changes what is
// a comment, after the reparse.
func TestScenarioFormalHighlightFollowsEdits(t *testing.T) {
	s := openModel(t)
	s.ExpectHighlight("EXTENDS", editor.FModelKeyword, editor.FModelComment)
	s.Press("ctrl+home")
	s.Find("EXTENDS")
	s.Type("(* ")
	s.ExpectHighlight("EXTENDS", editor.FModelComment, editor.FModelKeyword)
	s.Press("ctrl+z")
	s.ExpectHighlight("EXTENDS", editor.FModelKeyword, editor.FModelComment)
}

// TestScenarioFormalHighlightProfiles: the classes draw in design tokens, in
// every colour and glyph profile; a tag's link is underlined in all of them.
func TestScenarioFormalHighlightProfiles(t *testing.T) {
	for _, p := range []struct {
		name                     string
		reduced, ascii, plain    bool
		wantTagged, wantUntagged string
	}{
		{name: "truecolor", wantTagged: "• Release", wantUntagged: "◦ Acquire"},
		{name: "256", reduced: true, wantTagged: "• Release", wantUntagged: "◦ Acquire"},
		{name: "ascii", ascii: true, wantTagged: "* Release", wantUntagged: "o Acquire"},
		{name: "256-ascii", reduced: true, ascii: true, wantTagged: "* Release", wantUntagged: "o Acquire"},
		{name: "plain-underline", plain: true, wantTagged: "• Release", wantUntagged: "◦ Acquire"},
	} {
		t.Run(p.name, func(t *testing.T) {
			s := openModel(t, Tweak(func(o *tui.Options) {
				o.ReducedColor, o.ASCIIGlyphs, o.PlainUnderline = p.reduced, p.ascii, p.plain
			}))
			th := tui.NewThemeProfile(true, p.plain, p.reduced, p.ascii)
			for _, c := range []struct {
				text, role string
				attrs      uint8
			}{
				{"EXTENDS", "syntaxSection", 0},
				{`\notin`, "structure", 0},
				{`"none"`, "syntaxInvariant", 0},
				{"(* The project", "secondary", uv.AttrItalic},
				{"Wait(p) ==", "syntaxDefinition", uv.AttrBold},
				{"Spec: §2.1", "control", uv.AttrBold},
				{"$Lock.Release \"", "syntaxInvariant", 0},
				{"§2.1", "syntaxSection", 0},
			} {
				cell := s.cellOf(c.text)
				if !sameColor(cell.Style.Fg, th.Token(c.role)) {
					s.fail("%q: foreground %v, want the token %s (%v)", c.text, cell.Style.Fg, c.role, th.Token(c.role))
				}
				if c.attrs != 0 && cell.Style.Attrs&c.attrs == 0 {
					s.fail("%q: attributes %b, want %b", c.text, cell.Style.Attrs, c.attrs)
				}
			}
			if cell := s.cellOf("§2.1"); cell.Style.Underline == uv.UnderlineNone {
				s.fail("a tag's reference is not underlined")
			}
			if cell := s.cellOf("Acquire(p) =="); cell.Style.Underline != uv.UnderlineNone {
				s.fail("an untagged declaration is underlined")
			}
			s.ExpectText(p.wantTagged)
			s.ExpectText(p.wantUntagged)
			// A Derived-from stays dimmer than a citation.
			s.Hover("Procs")
			if cell := s.cellOf("Derived-from $Lock.Holder lock"); !sameColor(cell.Style.Fg, th.Token("secondary")) || cell.Style.Attrs&uv.AttrItalic == 0 {
				s.fail("Derived-from is not dimmed: %v %b", cell.Style.Fg, cell.Style.Attrs)
			}
		})
	}
}

// ---- outline ----

// TestScenarioFormalOutlineKeys: the outline lists the model's operators after
// the spec, marks the tagged ones, follows the cursor and jumps with Return.
func TestScenarioFormalOutlineKeys(t *testing.T) {
	s := openModel(t)
	st := s.State()
	want := []string{lockModelFile, "tagged Procs", "tagged holder", "untagged waiting", "untagged vars", "untagged NoHolder", "untagged Init",
		"tagged Wait", "untagged Acquire", "tagged Release", "untagged Next", "untagged Live", "tagged Inv_SingleWriter"}
	if !slices.Contains(st.Outline, lockSpecFile) || !slices.Equal(st.Outline[len(st.Outline)-len(want):], want) {
		s.fail("outline %q, want it to end %q", st.Outline, want)
	}

	s.Find("Acquire(p) ==")
	s.Press("alt+o")
	s.ExpectFocus("outline")
	s.Expect("the selection follows the cursor", func(st tui.State) bool { return st.Outline[st.OutlineSel] == "untagged Acquire" })
	s.ExpectHelpLive("Ret")
	s.Press("down")
	s.Expect("down moves it", func(st tui.State) bool { return st.Outline[st.OutlineSel] == "tagged Release" })
	s.Press("enter")
	s.ExpectFocus("editor")
	s.ExpectCursor(lockModelFile, 33)
	s.Press("ctrl+b")
	s.ExpectCursor(lockModelFile, 27)
}

// TestScenarioFormalOutlineMouse: a click on an operator or the file jumps
// there, from the spec too.
func TestScenarioFormalOutlineMouse(t *testing.T) {
	s := Open(t, scenes.Formal, Size(tallW, tallH))
	s.ClickOutline("• Wait")
	s.ExpectFocus("editor")
	s.ExpectCursor(lockModelFile, 22)
	s.ClickOutline(lockModelFile)
	s.ExpectCursor(lockModelFile, 1)
	s.ClickOutline("$Lock.Release")
	s.ExpectCursor(lockSpecFile, 11)
	s.Press("ctrl+b")
	s.ExpectCursor(lockModelFile, 1)
}

// TestScenarioFormalOutlineFollowsTags: removing a tag clears the mark.
func TestScenarioFormalOutlineFollowsTags(t *testing.T) {
	s := openModel(t)
	s.Find("Spec: $Lock.Release")
	s.Press("ctrl+k")
	s.Expect("the mark went", func(st tui.State) bool { return slices.Contains(st.Outline, "untagged Release") })
	s.Press("ctrl+z")
	s.Expect("the mark came back", func(st tui.State) bool { return slices.Contains(st.Outline, "tagged Release") })
}

// ---- save and watch ----

// TestScenarioFormalSavesDirectly: ^O writes the file at once, with no card, no
// proposal and no trailer, and the spec's hover follows.
func TestScenarioFormalSavesDirectly(t *testing.T) {
	s := Open(t, scenes.Formal, At("Lock.SingleWriter"), Size(tallW, tallH), Tweak(func(o *tui.Options) { o.ParseDelay = time.Hour }))
	s.Press("ctrl+t")
	s.ExpectCursor(lockModelFile, 44)
	s.Select(len("Inv_SingleWriter"))
	s.Type("Inv_OneWriter")
	s.ExpectDirty(true)
	s.ExpectStatus("Unsaved")
	s.Press("ctrl+o")
	s.ExpectDirty(false)
	s.ExpectStatus("Saved")
	s.ExpectFlash("^Saved tla/Lock.tla")
	if got := s.ReadFile(lockModelFile); !strings.Contains(got, "\nInv_OneWriter ==") {
		s.fail("the file holds %q", got)
	}
	s.ExpectCards()
	if ps := s.Proposals(); len(ps) != 0 {
		s.fail("a model's save filed %d proposals", len(ps))
	}
	if st := s.Shell("git", "status", "--short"); strings.TrimSpace(st) != "M tla/Lock.tla" {
		s.fail("git status %q", st)
	}

	s.Press("ctrl+b")
	s.ExpectCursor(lockSpecFile, 9)
	s.Hover("$Lock.SingleWriter")
	s.ExpectText("Model: tla/Lock.tla:Inv_OneWriter")
	s.ExpectNoText("Inv_SingleWriter")
}

// TestScenarioFormalWatchOpen: an outside edit reloads a clean model buffer.
func TestScenarioFormalWatchOpen(t *testing.T) {
	s := openModel(t, Watch())
	s.WriteFile(lockModelFile, strings.Replace(s.ReadFile(lockModelFile), "NoHolder ==", "Outside == 1\n\nNoHolder ==", 1), true)
	s.WaitUntil("the reload", func(st tui.State) bool { return strings.Contains(st.Text, "Outside == 1") })
	s.ExpectFlash("^Reloaded tla/Lock.tla")
	s.ExpectDirty(false)
	s.Expect("the outline has it", func(st tui.State) bool { return slices.Contains(st.Outline, "untagged Outside") })
	s.ExpectHighlight("Outside ==", editor.FModelDecl, 0)
}

// TestScenarioFormalWatchUnopened: an outside edit of a model nobody opened
// reaches the spec's hover, without a flash.
func TestScenarioFormalWatchUnopened(t *testing.T) {
	s := Open(t, scenes.Formal, At("Lock.SingleWriter"), Watch())
	s.WriteFile(lockModelFile, strings.ReplaceAll(s.ReadFile(lockModelFile), "Inv_SingleWriter", "Inv_Outside"), true)
	s.WaitUntil("the index", func(st tui.State) bool { return slices.Contains(st.Outline, "tagged Inv_Outside") })
	s.ExpectFlashText("")
	s.Hover("$Lock.SingleWriter")
	s.ExpectText("Model: tla/Lock.tla:Inv_Outside")
}

// TestScenarioFormalWatchConflict: an outside edit of a modified model asks, as
// for a spec file; taking theirs takes it.
func TestScenarioFormalWatchConflict(t *testing.T) {
	s := openModel(t, Watch())
	s.Find("NoHolder ==")
	s.Type("Mine == 1\n")
	s.WriteFile(lockModelFile, s.ReadFile(lockModelFile)+"\\* theirs\n", true)
	s.WaitUntil("the conflict prompt", func(st tui.State) bool { return st.Context == tui.CtxConflict })
	s.Press("t")
	s.ExpectNoBuffer("Mine == 1")
	s.ExpectBuffer(`\* theirs`)
	s.ExpectDirty(false)
}

// TestScenarioFormalSaveConflict: a save over an outside edit asks too.
func TestScenarioFormalSaveConflict(t *testing.T) {
	s := openModel(t)
	s.Find("NoHolder ==")
	s.Type("Mine == 1\n")
	s.WriteFile(lockModelFile, s.ReadFile(lockModelFile)+"\\* theirs\n", true)
	s.Press("ctrl+o")
	s.ExpectContext(tui.CtxConflict)
	s.Press("k")
	s.ExpectDirty(true)
	s.Press("ctrl+o")
	s.ExpectDirty(false)
	if got := s.ReadFile(lockModelFile); !strings.Contains(got, "Mine == 1") || strings.Contains(got, "theirs") {
		s.fail("the file holds %q", got)
	}
}

// TestScenarioFormalWatchDeleted: a deleted model with unsaved edits can be
// written back.
func TestScenarioFormalWatchDeleted(t *testing.T) {
	s := openModel(t)
	s.Find("NoHolder ==")
	s.Type("Mine == 1\n")
	s.Shell("rm", lockModelFile)
	s.Deliver("deleted", tui.FilesChanged(lockModelFile))
	s.ExpectFlash("was deleted on disk; \\^O writes your buffer back")
	s.Press("ctrl+o")
	s.ExpectDirty(false)
	if got := s.ReadFile(lockModelFile); !strings.Contains(got, "Mine == 1") {
		s.fail("the file holds %q", got)
	}
}

// TestScenarioFormalExitAsks: ^X asks about a modified model, and y saves it.
func TestScenarioFormalExitAsks(t *testing.T) {
	s := openModel(t, NoLayoutCheck())
	s.Find("NoHolder ==")
	s.Type("Mine == 1\n")
	s.Press("ctrl+x")
	s.ExpectContext(tui.CtxConfirm)
	if st := s.State(); !strings.Contains(st.Prompt, "Save modified buffer tla/Lock.tla") {
		s.fail("prompt %q", st.Prompt)
	}
	s.Press("y")
	if got := s.ReadFile(lockModelFile); !strings.Contains(got, "Mine == 1") {
		s.fail("the file holds %q", got)
	}
}

// ---- closed actions ----

// expectClosed presses a dimmed key and expects nothing to change.
func (s *Scenario) expectClosed(entry, key string) {
	s.t.Helper()
	s.ExpectHelpDim(entry)
	before := s.State()
	s.Press(key)
	after := s.State()
	switch {
	case after.Context != before.Context || after.Focus != before.Focus:
		s.fail("%s moved %s/%s to %s/%s", key, before.Context, before.Focus, after.Context, after.Focus)
	case after.Text != before.Text:
		s.fail("%s changed the buffer", key)
	case after.Flash != before.Flash:
		s.fail("%s flashed %q", key, after.Flash)
	case after.Job != "" || after.Prompt != "":
		s.fail("%s started %q %q", key, after.Job, after.Prompt)
	case after.SideShown != before.SideShown:
		s.fail("%s toggled the sidebar", key)
	}
}

// TestScenarioFormalClosedKeys: ^P, ^F and ^N are dimmed and inert on a model,
// in the editor and the outline; the same keys work again on the spec. ^R stays
// live, since a model proposal arrives as a card: it shows the sidebar and,
// though the buffer is modified, starts no check of changed anchors.
func TestScenarioFormalClosedKeys(t *testing.T) {
	s := Open(t, scenes.Formal, AtFile(lockModelFile, 0), Replies(infer.CannedFile{Check: []infer.CannedEntry{cleanCheckReply}}))
	s.Find("NoHolder ==")
	s.Type("Mine == 1\n")
	for _, k := range [][2]string{{"^P", "ctrl+p"}, {"^F", "ctrl+f"}, {"^N", "ctrl+n"}} {
		s.expectClosed(k[0], k[1])
	}
	s.ExpectHelpLive("^R")
	s.Press("ctrl+r")
	s.ExpectFocus("sidebar")
	if st := s.State(); st.Job != "" || st.Prompt != "" {
		s.fail("^R on a model started %q %q", st.Job, st.Prompt)
	}
	s.Press("ctrl+r")
	s.ExpectFocus("editor")
	s.Press("alt+o")
	s.ExpectFocus("outline")
	for _, k := range [][2]string{{"^P", "ctrl+p"}, {"^N", "ctrl+n"}} {
		s.expectClosed(k[0], k[1])
	}
	s.Press("esc")
	s.ExpectCards()
	if ps := s.Proposals(); len(ps) != 0 {
		s.fail("%d proposals", len(ps))
	}

	s.Press("ctrl+o")
	s.ClickOutline("$Lock.Release")
	s.ExpectCursor(lockSpecFile, 11)
	s.ExpectHelpLive("^F")
	s.ExpectHelpLive("^R")
	s.ExpectHelpLive("^N")
}

// TestScenarioFormalNoGutterDiff: an edit marks no gutter and underlines no word
// as added, since a model has no diff against HEAD; and ^] does not stop there.
func TestScenarioFormalNoGutterDiff(t *testing.T) {
	s := openModel(t)
	s.Find("NoHolder ==")
	s.Type("Fresh == 1\n")
	s.Press("ctrl+o")
	s.Type("Unsaved == 2\n")
	s.Expect("no gutter marks", func(st tui.State) bool { return len(st.Gutter) == 0 })
	if cell := s.cellOf("waiting\n"[:7]); cell.Style.Underline != uv.UnderlineNone {
		s.fail("an old word is underlined as added")
	}
	if cell := s.cellOf("Fresh"); cell.Style.Underline != uv.UnderlineNone {
		s.fail("a new word is underlined as added")
	}
	s.Expect("no ^] stops", func(st tui.State) bool { return len(st.Edits) == 0 })
	s.Press("ctrl+]")
	s.ExpectCursor(lockModelFile, 17)
	s.ExpectFlash("^No edits since HEAD")
}

// TestScenarioFormalCommitListsModel: from the spec, a modified model is not
// asked about before a check or commit; the commit view lists its saved text
// as one whole-file change (2026-11-08: the commit carries models).
func TestScenarioFormalCommitListsModel(t *testing.T) {
	s := Open(t, scenes.Formal, AtFile(lockModelFile, 0), Replies(infer.CannedFile{Check: []infer.CannedEntry{cleanCheckReply}}))
	s.Find("NoHolder ==")
	s.Type("Saved == 1\n")
	s.Press("ctrl+o")
	s.Type("Unsaved == 2\n")

	s.ClickOutline("$Lock.Release")
	s.Find("before it exits")
	s.Type("always ")
	s.Press("ctrl+o")
	s.ExpectDirty(false)

	s.Press("ctrl+p")
	s.Expect("no prompt about the model", func(st tui.State) bool { return st.Prompt == "" })
	s.Idle()
	s.Press("ctrl+r")
	s.Idle()
	s.ExpectFocus("sidebar")
	s.Press("A")
	s.ExpectFocus("commit")
	s.Idle()
	var models []string
	for _, c := range s.State().Commit.Items {
		if strings.Contains(c.ID, "tla") {
			models = append(models, c.ID)
		}
	}
	if len(models) != 1 || models[0] != "file:"+lockModelFile {
		s.fail("want the model as one whole-file change, got %q", models)
	}
	s.ExpectText("model " + lockModelFile)
	s.Press("esc")
	for _, p := range s.Proposals() {
		if strings.Contains(fmt.Sprint(p.Primary), ".tla") {
			s.fail("a proposal on a model: %v", p.Primary)
		}
	}
}

// TestScenarioFormalCommitClosedFromModel: with the sidebar focused over a
// model, A is dimmed and inert, and ^R still hides it.
func TestScenarioFormalCommitClosedFromModel(t *testing.T) {
	s := Open(t, scenes.Formal, Replies(infer.CannedFile{Check: []infer.CannedEntry{cleanCheckReply}}))
	s.Press("ctrl+r")
	s.ExpectFocus("sidebar")
	s.Press("esc")
	s.ClickOutline("• Wait")
	s.ExpectCursor(lockModelFile, 22)
	s.Press("shift+tab")
	s.ExpectFocus("sidebar")
	s.expectClosed("A", "A")
	s.ExpectHelpLive("^R")
	s.Press("ctrl+r")
	s.Expect("hidden", func(st tui.State) bool { return !st.SideShown })
}

// TestScenarioFormalTypingStaysTLA: # means "not equal" in TLA+, so nothing turns
// it into a §, and sigils in code complete nothing.
func TestScenarioFormalTypingStaysTLA(t *testing.T) {
	s := openModel(t)
	s.Find("NoHolder ==")
	s.Type("A == x #2.1 y\n")
	s.ExpectBuffer("A == x #2.1 y\n")
	s.Expect("no completion", func(st tui.State) bool { return st.Completion == "" })
	s.Type("B == $")
	s.Expect("no completion on $", func(st tui.State) bool { return st.Completion == "" })
	s.Type("\n\\* $")
	s.Expect("no completion in other comments", func(st tui.State) bool { return st.Completion == "" })
}

// TestScenarioFormalCompletion: after "Spec:", $ and § complete as in the spec,
// and the new tag links at once.
func TestScenarioFormalCompletion(t *testing.T) {
	s := openModel(t)
	s.Find("Acquire(p) ==")
	s.Type(`\* Spec: $`)
	s.Expect("anchors offered", func(st tui.State) bool { return st.Completion == "$" })
	s.Type("Lock.Rel")
	s.Press("tab")
	s.Type("\n")
	s.ExpectBuffer("\\* Spec: $Lock.Release\nAcquire(p)")
	s.Expect("the operator is tagged now", func(st tui.State) bool { return slices.Contains(st.Outline, "tagged Acquire") })
	s.ExpectHighlight("Acquire(p)", editor.FModelDecl|editor.FModelLink, 0)

	s.Find("Next ==")
	s.Type(`\* Derived-from: §`)
	s.Expect("sections offered", func(st tui.State) bool { return st.Completion == "§" })
	s.Type("2.1")
	s.Press("tab")
	s.ExpectBuffer(`\* Derived-from: §2.1`)
}

// TestScenarioFormalHashStaysTLAInATag: # means "not equal" even where a tag's
// reference goes: it opens nothing and stays a #, as in the language server.
func TestScenarioFormalHashStaysTLAInATag(t *testing.T) {
	s := openModel(t)
	s.Find("Acquire(p) ==")
	s.Type(`\* Spec: #`)
	s.Expect("no completion on #", func(st tui.State) bool { return st.Completion == "" })
	s.Type("2.1\n")
	s.ExpectBuffer("\\* Spec: #2.1\nAcquire(p)")
	s.ExpectNoBuffer("\\* Spec: §2.1\nAcquire(p)")

	s.Find("Next ==")
	s.Type(`\* Derived-from: §`)
	s.Expect("sections offered", func(st tui.State) bool { return st.Completion == "§" })
	s.Type("#")
	s.Expect("# closes it and opens nothing", func(st tui.State) bool { return st.Completion == "" })
	s.ExpectBuffer(`\* Derived-from: §#`)
}

// TestScenarioFormalCompletionAddressesThePick: a Spec-file tag sends a bare §N
// to its file, so a pick in another file with that number is written qualified;
// each inserted reference resolves to the section picked.
func TestScenarioFormalCompletionAddressesThePick(t *testing.T) {
	const other = "05-r.spec.md"
	s := openModel(t) // \* Spec-file: lock
	s.WriteFile(other, "# r\n\n## 2.1 Queues\n\nAnother section two point one.\n\n## 9.9 Elsewhere\n\nOnly here.\n", true)
	s.Reopen()

	s.Find("Acquire(p) ==")
	s.Type(`\* Spec: §Queues`)
	s.Press("tab")
	s.ExpectBuffer(`\* Spec: §05-r:2.1`)
	s.Type(` "Another section"` + "\n")
	s.Hover("Spec: §05-r:2.1")
	s.ExpectText("Another section two point one.")
	s.ExpectNoText("quote is not in")
	s.Press("ctrl+t")
	s.ExpectCursor(other, 3)
}

// TestScenarioFormalCompletionKeepsTheBareForm: the shortest form stays when
// the bare number does reach the pick: in the Spec-file's file, or a number
// only one file has.
func TestScenarioFormalCompletionKeepsTheBareForm(t *testing.T) {
	const other = "05-r.spec.md"
	s := openModel(t) // \* Spec-file: lock
	s.WriteFile(other, "# r\n\n## 2.1 Queues\n\nAnother section two point one.\n\n## 9.9 Elsewhere\n\nOnly here.\n", true)
	s.Reopen()

	s.Find("Acquire(p) ==")
	s.Type(`\* Spec: §Waiting`)
	s.Press("tab")
	s.Type("\n")
	s.ExpectBuffer("\\* Spec: §2.1\nAcquire(p)")
	s.ExpectNoBuffer(`§lock:2.1`)

	s.Find("Next ==")
	s.Type(`\* Spec: §Elsewhere`)
	s.Press("tab")
	s.Type("\n")
	s.ExpectBuffer("\\* Spec: §9.9\nNext ==")
}

// TestScenarioFormalCompletionSkipsTheUnaddressable: a number no form reaches
// (two files share the stem and the number) is not offered, as in the language
// server: the qualified form would be ambiguous too.
func TestScenarioFormalCompletionSkipsTheUnaddressable(t *testing.T) {
	s := openModel(t)
	for _, dir := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(s.Dir(), dir), 0o755); err != nil {
			s.fail("mkdir: %v", err)
		}
		s.WriteFile(dir+"/q.spec.md", "# q\n\n## 7.7 Shared\n\nSame stem and number.\n", true)
	}
	s.Reopen()

	s.Find("Acquire(p) ==")
	s.Type(`\* Spec: §`)
	s.Expect("sections offered", func(st tui.State) bool { return st.Completion == "§" })
	s.Type("Shared")
	s.Expect("no form reaches it", func(st tui.State) bool { return st.Completion == "" })
}

// otherSpecFile sits beside lock.spec.md with a §2.1 of its own and a number
// only it has.
const (
	otherSpecFile = "05-r.spec.md"
	otherSpec     = "# r\n\n## 2.1 Queues\n\nAnother section two point one.\n\n## 9.9 Elsewhere\n\nOnly here.\n"
)

// openSpecBesideOther opens lock.spec.md with the other spec file in the project.
func openSpecBesideOther(t *testing.T, opts ...Opt) *Scenario {
	t.Helper()
	s := Open(t, scenes.Formal, append([]Opt{AtFile(lockSpecFile, 0), Size(tallW, tallH)}, opts...)...)
	s.WriteFile(otherSpecFile, otherSpec, true)
	s.Reopen()
	return s
}

// TestScenarioSpecCompletionAddressesThePick: in a spec file a bare §N resolves
// in that file first, so a pick in another file with that number is written
// qualified, and the inserted reference reaches the section picked.
func TestScenarioSpecCompletionAddressesThePick(t *testing.T) {
	s := openSpecBesideOther(t)
	s.Find("Readers take no lock.")
	s.Press("ctrl+e")
	s.Type(" §Queues")
	s.Press("tab")
	s.ExpectBuffer(" §05-r:2.1")
	s.Press("left", "ctrl+t")
	s.ExpectCursor(otherSpecFile, 3)

	// And from the other file, back to lock's.
	s.Press("ctrl+e")
	s.Type(" §Waiting")
	s.Press("tab")
	s.ExpectBuffer(" §lock:2.1")
	s.Press("left", "ctrl+t")
	s.ExpectCursor(lockSpecFile, 13)
}

// TestScenarioSpecCompletionKeepsTheBareForm: the shortest form stays when the
// bare number reaches the pick: in this file, or a number only one file has.
func TestScenarioSpecCompletionKeepsTheBareForm(t *testing.T) {
	s := openSpecBesideOther(t)
	s.Find("Readers take no lock.")
	s.Press("ctrl+e")
	s.Type(" §Waiting")
	s.Press("tab")
	s.Type(" and §Elsewhere")
	s.Press("tab")
	s.ExpectBuffer(" §2.1 and §9.9")
	s.ExpectNoBuffer("§lock:2.1")
}

// TestScenarioSpecCompletionSameTitle: two files with the same number and title
// are two picks, and each label shows the form that will be written.
func TestScenarioSpecCompletionSameTitle(t *testing.T) {
	s := Open(t, scenes.Formal, AtFile(lockSpecFile, 0), Size(tallW, tallH))
	s.WriteFile(otherSpecFile, "# r\n\n## 2.1 Waiting\n\nAnother section two point one.\n", true)
	s.Reopen()
	s.Find("Readers take no lock.")
	s.Press("ctrl+e")
	s.Type(" §Waiting")
	s.ExpectText("§2.1 Waiting")
	s.ExpectText("§05-r:2.1 Waiting")
	s.Type(" ")
	s.Type("§05")
	s.Press("tab")
	s.ExpectBuffer(" §05-r:2.1")
	s.Press("left", "ctrl+t")
	s.ExpectCursor(otherSpecFile, 3)
}

// TestScenarioSpecCompletionSkipsTheUnaddressable: a number no form reaches
// (two files share the stem and the number) is not offered.
func TestScenarioSpecCompletionSkipsTheUnaddressable(t *testing.T) {
	s := Open(t, scenes.Formal, AtFile(lockSpecFile, 0), Size(tallW, tallH))
	for _, dir := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(s.Dir(), dir), 0o755); err != nil {
			s.fail("mkdir: %v", err)
		}
		s.WriteFile(dir+"/q.spec.md", "# q\n\n## 7.7 Shared\n\nSame stem and number.\n", true)
	}
	s.Reopen()
	s.Find("Readers take no lock.")
	s.Press("ctrl+e")
	s.Type(" §")
	s.Expect("sections offered", func(st tui.State) bool { return st.Completion == "§" })
	s.Type("Shared")
	s.Expect("no form reaches it", func(st tui.State) bool { return st.Completion == "" })
}

// TestScenarioSpecCompletionSeesUnparsedText: a section typed moments ago, not
// yet reparsed, takes the bare number from the other file's section.
func TestScenarioSpecCompletionSeesUnparsedText(t *testing.T) {
	s := openSpecBesideOther(t, Tweak(func(o *tui.Options) { o.ParseDelay = time.Hour }))
	s.Find("leaving in $Lock.Release.")
	s.Press("ctrl+e")
	s.Type("\n\n## 9.9 Mine\n\nSee §Elsewhere")
	s.Press("tab")
	s.ExpectBuffer("See §05-r:9.9")
}

// TestScenarioFormalSmallScreen: the walk holds its layout at 80x24.
func TestScenarioFormalSmallScreen(t *testing.T) {
	s := Open(t, scenes.Formal, At("Lock.SingleWriter"), Size(80, 24))
	s.Hover("$Lock.SingleWriter")
	s.ExpectText("Model: tla/Lock.tla:Inv_SingleWriter")
	s.Press("ctrl+t")
	s.ExpectCursor(lockModelFile, 44)
	s.Hover("Spec: $Lock.SingleWriter")
	s.ExpectText("At most one $Lock.Holder")
	s.Press("esc", "alt+o")
	s.ExpectFocus("outline")
	s.Press("esc", "ctrl+b")
	s.ExpectCursor(lockSpecFile, 9)
}

// TestScenarioFormalCardAcceptedFromModel: focusing the sidebar from a model
// shows its card in the spec; y applies the fix there and leaves the model alone.
func TestScenarioFormalCardAcceptedFromModel(t *testing.T) {
	const find, replace = "Readers take no lock.", "Readers never take the lock."
	s := Open(t, scenes.Formal, At("Lock.Release"), Replies(infer.CannedFile{Check: []infer.CannedEntry{scenes.CheckReply("readers", nil,
		scenes.Finding{File: lockSpecFile, Issue: "missing_update", Severity: "inconsistent", Explanation: "say it plainly", Find: find, Replace: replace})}}))
	s.Find("before it exits")
	s.Type("always ")
	s.Press("ctrl+o", "ctrl+p")
	s.Idle()
	s.WaitUntil("the fix card", func(st tui.State) bool { return len(st.CardsOf("fix")) == 1 })
	s.Press("ctrl+r")
	s.ExpectFocus("sidebar")
	s.Press("esc")

	s.ClickOutline("• Wait")
	s.ExpectCursor(lockModelFile, 22)
	model := s.State().Text
	s.Press("shift+tab")
	s.ExpectFocus("sidebar")
	s.Press("y")
	s.Idle()
	s.Expect("no card left", func(st tui.State) bool { return len(st.Cards) == 0 })
	s.ExpectBuffer(replace)
	s.Press("esc")
	s.ClickOutline("• Wait")
	s.Expect("the model is untouched", func(st tui.State) bool { return st.Text == model })
	s.ExpectDirty(false)
}
