// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// The editor (manual check items 1 to 16, 22, 25 and 43) on the lensd
// fixture (testdata/lensd).

// TestScenarioOpen (item 1): lensd opens on its first line with the
// outline, the lint counts on the status line and the editor's sixteen
// help bar entries plus ^^ Outline and ⇧Tab Next Pane.
func TestScenarioOpen(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.ExpectFocus("editor")
	s.ExpectCursor("lensd.spec.md", 1)
	s.Expect("the outline shown, the sidebar not", func(st tui.State) bool { return st.OutlineShown && !st.SideShown })
	s.ExpectStatus("Clean")
	s.ExpectStatus("1:1")
	want := []string{"^G Help", "^X Exit", "^O Save", "^Q Open", "^W Where Is", "^P Propose", "^K Cut", "^U Paste",
		"^Z Undo", "^Y Redo", "^T Follow", "^R Checks", "^B Back", "^F Format", "^C Copy", "^N Analyze", "^^ Outline", "⇧Tab Next Pane"}
	if got := s.State().HelpText(); strings.Join(got, "|") != strings.Join(want, "|") {
		s.fail("help bar %q, want %q", got, want)
	}
}

// TestScenarioTypeUndo (item 3): typing marks the buffer modified; ^Z
// removes the whole typed run and ^Y brings it back.
func TestScenarioTypeUndo(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.Press("ctrl+_")
	s.Type("15\n")
	s.ExpectCursor("", 15)
	s.Press("ctrl+e")
	s.Type(" A typed sentence.")
	s.ExpectDirty(true)
	s.ExpectStatus("Unsaved")
	s.Press("ctrl+z")
	s.ExpectNoBuffer("A typed sentence.")
	s.ExpectDirty(false)
	s.Press("ctrl+y")
	s.ExpectBuffer("of scope. A typed sentence.")
}

// TestScenarioDanglingWhileTyping (item 4): a $Name opens the completion
// list; an unknown name is an error until it is finished.
func TestScenarioDanglingWhileTyping(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.Find("of scope.")
	s.Press("ctrl+e")
	s.Type(" see $")
	s.Expect("the completion list", func(st tui.State) bool { return st.Completion == "$" })
	s.ExpectContext(tui.CtxCompletion)
	s.Type("Frame.Timin")
	s.Press("esc")
	s.ExpectContext(tui.CtxEditor)
	s.ExpectText("E1 W1 H0")
	s.Type("g")
	s.ExpectText("E0 W1 H0")
}

// TestScenarioLookPopup (item 5): ^L on a $Name opens its preview; Esc
// closes it.
func TestScenarioLookPopup(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.Find("$Time.Elapsed),")
	s.Press("ctrl+l")
	s.ExpectContext(tui.CtxPopup)
	s.ExpectText("def $Time.Elapsed (elapsed time)")
	s.ExpectText("lensd.spec.md:46")
	s.Press("esc")
	s.ExpectContext(tui.CtxEditor)
	s.Expect("no popup", func(st tui.State) bool { return !st.Popup })
}

// TestScenarioJumpAndBack (item 6): ^T follows a reference to its
// definition, ^B goes back, alt+→ forward again; ^\ lists the references
// and Enter jumps to one.
func TestScenarioJumpAndBack(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.Find("$Time.Elapsed),")
	s.ExpectCursor("", 13)
	s.Press("ctrl+t")
	s.ExpectCursor("", 46)
	s.ExpectAnchor("$Time.Elapsed")
	s.Press("ctrl+b")
	s.ExpectCursor("", 13)
	s.Press("alt+right")
	s.ExpectCursor("", 46)
	s.Press("ctrl+\\")
	s.ExpectContext(tui.CtxList)
	s.Expect("a list of references", func(st tui.State) bool { return len(st.ListItems) > 1 })
	s.Press("enter")
	s.ExpectContext(tui.CtxEditor)
	s.Expect("on a reference", func(st tui.State) bool { return st.Line != 46 })
}

// TestScenarioQuickOpen (item 7): ^Q finds a section by number and an
// anchor by a fuzzy name.
func TestScenarioQuickOpen(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.Press("ctrl+q")
	s.ExpectContext(tui.CtxList)
	s.Type("0.2\n")
	s.ExpectCursor("", 49)
	s.Press("ctrl+q")
	s.Type("frame.tim\n")
	s.ExpectAnchor("$Frame.Timing")
	s.ExpectCursor("", 65)
}

// TestScenarioHashToSection (item 8): "#5 " becomes "§5 " and says so; ^Z
// restores it; a number with no section, and a # glued to a word, stay.
func TestScenarioHashToSection(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.Find("of scope.")
	s.Press("ctrl+e", "enter", "enter")
	s.Type("See #5 ")
	s.ExpectBuffer("See §5 ")
	s.ExpectFlashText("#5 → §5 (^Z undoes)")
	s.Press("ctrl+z")
	s.ExpectBuffer("See #5 ")
	s.Type("PR #99 and PR#5 ")
	s.ExpectBuffer("See #5 PR #99 and PR#5 ")
}

// TestScenarioFindCopy (item 9): ^W finds text, an empty ^W finds the
// next match; ^C copies a selection and, without one, copies nothing.
func TestScenarioFindCopy(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.Find("relay")
	first := s.State().Line
	s.Press("ctrl+w")
	s.Expect("the prompt offers the last search", func(st tui.State) bool { return strings.Contains(st.Prompt, "[relay]") })
	s.Press("enter")
	if s.State().Line == first && s.State().Col == 0 {
		s.fail("the second search did not move")
	}
	s.Press("ctrl+c")
	s.ExpectFlashText("")
	s.Select(5)
	s.Press("ctrl+c")
	s.ExpectFlashText("Copied 5 bytes")
}

// TestScenarioSaveAndExit (item 10): ^O writes the buffer and says how
// many lines; ^X on a modified buffer asks, and ^C keeps editing.
func TestScenarioSaveAndExit(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.Find("of scope.")
	s.Press("ctrl+e")
	s.Type(" Saved.")
	s.Press("ctrl+o")
	s.ExpectFlashText("Saved lensd.spec.md")
	s.ExpectDirty(false)
	if !strings.Contains(s.ReadFile("lensd.spec.md"), "of scope. Saved.") {
		s.fail("^O did not write the edit")
	}
	s.Type("!")
	s.Press("ctrl+x")
	s.ExpectContext(tui.CtxConfirm)
	s.Expect("the save question", func(st tui.State) bool { return strings.Contains(st.Prompt, "Save modified buffer") })
	s.Press("ctrl+c")
	s.ExpectContext(tui.CtxEditor)
	s.ExpectDirty(true)
}

// TestScenarioReloadClean (item 11): a file replaced on disk while its
// buffer is clean reloads and says so.
func TestScenarioReloadClean(t *testing.T) {
	s := Open(t, scenes.Lensd, Watch())
	s.WriteFile("lensd.spec.md", "AGENT\n"+s.ReadFile("lensd.spec.md"), true)
	s.WaitUntil("the buffer reloads", func(st tui.State) bool { return strings.HasPrefix(st.Text, "AGENT\n") })
	s.ExpectFlashText("Reloaded lensd.spec.md (changed on disk)")
	s.ExpectDirty(false)
}

// TestScenarioConflictModified (item 12): a file written in place while
// its buffer is modified asks; T takes the file.
func TestScenarioConflictModified(t *testing.T) {
	s := Open(t, scenes.Lensd, Watch())
	s.Type("mine ")
	s.WriteFile("lensd.spec.md", s.ReadFile("lensd.spec.md")+"AGENT2\n", false)
	s.WaitUntil("the conflict prompt", func(st tui.State) bool { return st.Context == tui.CtxConflict })
	s.ExpectHelp("K Keep Mine", "T Take Theirs", "D Show Diff")
	s.Press("t")
	s.ExpectContext(tui.CtxEditor)
	s.ExpectBuffer("AGENT2\n")
	s.ExpectNoBuffer("mine ")
}

// TestScenarioAgentRename (item 13): `spec rename` in another terminal
// succeeds while the UI is open, and the buffer reloads with the new
// name.
func TestScenarioAgentRename(t *testing.T) {
	s := Open(t, scenes.Lensd, Watch())
	s.Shell("spec", "rename", "Time.Elapsed", "Time.Monotonic")
	s.WaitUntil("the buffer reloads", func(st tui.State) bool { return strings.Contains(st.Text, "$Time.Monotonic") })
	s.ExpectNoBuffer("$Time.Elapsed")
}

// TestScenarioHelpScreen (item 14): ^G shows the help screen and ^X
// returns; M-X hides and shows the help bar; ^S does nothing.
func TestScenarioHelpScreen(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.Press("ctrl+g")
	s.ExpectFocus("help")
	s.ExpectText("Checks sidebar")
	s.Press("ctrl+x")
	s.ExpectFocus("editor")
	s.Press("alt+x")
	s.Expect("the help bar hidden", func(st tui.State) bool { return !st.HelpBarShown })
	s.Press("alt+x")
	s.Expect("the help bar shown", func(st tui.State) bool { return st.HelpBarShown })
	before := s.State()
	s.Press("ctrl+s")
	after := s.State()
	if after.Text != before.Text || after.Flash != "" || after.Context != tui.CtxEditor {
		s.fail("^S did something")
	}
}

// TestScenarioDeletedWhileModified (item 15): a file removed on disk while
// its buffer is modified says ^O writes it back, and ^O does.
func TestScenarioDeletedWhileModified(t *testing.T) {
	s := Open(t, scenes.Lensd, Watch())
	s.Type("kept ")
	if err := os.Remove(filepath.Join(s.Dir(), "lensd.spec.md")); err != nil {
		s.fail("rm: %v", err)
	}
	s.WaitUntil("the deletion is noticed", func(st tui.State) bool {
		return st.Flash == "lensd.spec.md was deleted on disk; ^O writes your buffer back"
	})
	s.Press("ctrl+o")
	if !strings.HasPrefix(s.ReadFile("lensd.spec.md"), "kept ") {
		s.fail("^O did not write the buffer back")
	}
}

// TestScenarioCompletion (item 16): $ completes anchors and # sections
// (inserting §); inside a code span neither opens.
func TestScenarioCompletion(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.Find("of scope.")
	s.Press("ctrl+e", "enter", "enter")
	s.Type("A $fr")
	s.Expect("anchors offered", func(st tui.State) bool { return st.Completion == "$" })
	s.Press("tab")
	s.Expect("a $Frame name inserted", func(st tui.State) bool { return strings.Contains(st.Text, "A $Frame.") })
	s.ExpectContext(tui.CtxEditor)
	s.Type(" #0.")
	s.Expect("sections offered", func(st tui.State) bool { return st.Completion == "§" || st.Completion == "#" })
	s.Press("enter")
	s.ExpectBuffer(" §0.1")
	s.Type(" a$")
	s.Expect("nothing after a letter", func(st tui.State) bool { return st.Completion == "" })
	s.Find("// Spec:")
	s.Type(" $")
	s.Expect("nothing in the indented code block", func(st tui.State) bool { return st.Completion == "" })
}

// TestScenarioMathHazardFix (items 21, 25): a stray $ is a math-hazard
// warning; ^P on it fixes it at once without a prompt, and ^Z / ^Y undo
// and redo the fix; nothing is committed.
func TestScenarioMathHazardFix(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.Find(" MUST be tried before abandoning")
	s.Type(" 5$")
	s.Press("left")
	s.ExpectStatus("$Frame.SegmentShape")
	s.Press("ctrl+p")
	s.ExpectBuffer("`5$`")
	s.ExpectFlashText("Put 5$ in backticks; ^Z undoes")
	s.ExpectContext(tui.CtxEditor)
	s.Press("ctrl+z")
	s.ExpectNoBuffer("`5$`")
	s.ExpectBuffer(" 5$")
	s.Press("ctrl+y")
	s.ExpectBuffer("`5$`")
	if n := strings.TrimSpace(s.Shell("git", "rev-list", "--count", "HEAD")); n != "1" {
		s.fail("the fix committed: %s commits", n)
	}
}

// TestScenarioDiagPopup (item 21): ^L on a warning shows the diagnostic,
// with ^P Fix on the help bar.
func TestScenarioDiagPopup(t *testing.T) {
	s := FromScene(t, "diag-popup")
	s.Play()
	s.ExpectContext(tui.CtxDiagPopup)
	s.ExpectText("def-unreferenced")
	s.ExpectHelp("^T Follow", "^P Fix", "Esc Close")
}

// TestScenarioFormat (item 22): ^F wraps the buffer and says how many
// lines changed; ^Z undoes it in one step; ^F twice is already formatted.
func TestScenarioFormat(t *testing.T) {
	s := Open(t, scenes.Lensd)
	before := s.State().Text
	s.Press("ctrl+f")
	s.ExpectFlash(`^Formatted: \d+ lines changed$`)
	s.Expect("every line at most 100 columns", func(st tui.State) bool {
		for _, l := range strings.Split(st.Text, "\n") {
			if len([]rune(l)) > 100 && !strings.HasPrefix(l, "    ") && !strings.HasPrefix(l, "|") {
				return false
			}
		}
		return true
	})
	s.Press("ctrl+f")
	s.ExpectFlashText("Already formatted")
	s.Press("ctrl+z")
	s.Expect("one ^Z undoes the format", func(st tui.State) bool { return st.Text == before })
}

// TestScenarioOutlineKey (item 43): ctrl+6 focuses the outline, again
// hides it; F2 and M-O do the same; the help bar names ^^ Outline.
func TestScenarioOutlineKey(t *testing.T) {
	s := Open(t, scenes.Lensd)
	s.ExpectHelp("^^ Outline")
	for _, k := range []string{"ctrl+6", "f2", "alt+o"} {
		s.Press(k)
		s.ExpectFocus("outline")
		s.ExpectContext(tui.CtxOutline)
		s.Press(k)
		s.Expect("the outline hidden", func(st tui.State) bool { return !st.OutlineShown })
		s.ExpectFocus("editor")
		s.Press(k)
		s.ExpectFocus("outline")
		s.Press("esc")
		s.ExpectFocus("editor")
	}
}

// TestScenarioOutlineKeepsSelectionInView: the outline scrolls to keep its
// selection visible when the pane gets shorter, not only when the selection
// moves: here the last entry, selected on a tall screen, then a resize.
func TestScenarioOutlineKeepsSelectionInView(t *testing.T) {
	s := Open(t, scenes.Lensd, Size(100, 50))
	s.Press("alt+o")
	for range 60 {
		s.Press("down")
	}
	s.Expect("the last entry selected", func(st tui.State) bool {
		return st.OutlineSel == len(st.Outline)-1 && st.Outline[st.OutlineSel] == "$Auth.OneDecision"
	})
	_, _, err := s.d.OutlineCell("$Auth.OneDecision")
	s.step("the selection is in view on the tall screen", err)
	s.Resize(100, 12)
	_, _, err = s.d.OutlineCell("$Auth.OneDecision")
	s.step("the selection is in view after the resize", err)
	s.Press("up")
	_, _, err = s.d.OutlineCell("$Auth.Gated")
	s.step("up still moves within view", err)
}

// TestScenarioPaneCycleNoOutline (item 36): with the outline hidden ⇧Tab
// goes editor → sidebar → editor.
func TestScenarioPaneCycleNoOutline(t *testing.T) {
	s := Open(t, scenes.Demo, Edits(analyzeEdit), Replies(analyzeReply))
	s.Press("ctrl+n")
	s.WaitFor(tui.Cond{Idle: true, Cards: 3})
	s.Press("alt+o", "alt+o")
	s.Expect("the outline hidden", func(st tui.State) bool { return !st.OutlineShown })
	s.Press("shift+tab")
	s.ExpectFocus("sidebar")
	s.Press("tab")
	s.ExpectFocus("editor")
}

// TestScenarioSidebarWidth (item 34): at 220 columns the sidebar leaves
// the editor its wrap column and gutter; at 140 with the outline the
// sidebar still takes a column.
func TestScenarioSidebarWidth(t *testing.T) {
	s := Open(t, scenes.LensdFormatted, Size(220, 50))
	s.Press("ctrl+r")
	s.Expect("editor keeps the 100-column text", func(st tui.State) bool { return st.SideColumn && st.WrapWidth >= 100 })
	s.Resize(140, 40)
	s.Expect("still a column at 140", func(st tui.State) bool { return st.SideShown && st.SideColumn })
}

// TestScenarioWrapAt140: at 140 columns with the outline shown, the
// editor holds a formatted spec's 100-column lines whole: no row is the
// last word of a line wrapped alone. (With the sidebar column as well the
// text wraps narrower, by design: TestScenarioSidebarWidth.)
func TestScenarioWrapAt140(t *testing.T) {
	s := Open(t, scenes.LensdFormatted, At("Hub.TranscoderCap"))
	s.Expect("the editor holds the wrap column", func(st tui.State) bool { return st.WrapWidth >= 100 })
	for k, r := range s.Frame()[:37] {
		// An editor row: outline │ line number (blank on a wrapped row).
		_, rest, ok := strings.Cut(r, "│")
		if !ok || len(rest) < 4 || strings.TrimSpace(rest[:3]) != "" {
			continue
		}
		body := strings.TrimPrefix(rest[3:], "│") // the anchor's tick
		if text := strings.TrimSpace(body); text != "" && !strings.Contains(text, " ") {
			s.fail("row %d is one word wrapped alone: %q", k, text)
		}
	}
}
