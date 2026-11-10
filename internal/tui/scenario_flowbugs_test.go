// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// Bugs the demo-flow director found while scripting videos (2026-11-08).

// renameEdits rename $Time.Media to $Time.Stamp by hand: the definition and
// both references.
var renameEdits = []scenes.Edit{
	{File: "demo.spec.md", Find: "$Time.Media", Replace: "$Time.Stamp"},
	{File: "demo.spec.md", Find: "$Time.Media", Replace: "$Time.Stamp"},
	{File: "demo.spec.md", Find: "$Time.Media", Replace: "$Time.Stamp"},
}

// renamedFlash is the flash for a change that is renames only.
const renamedFlash = "Nothing to check: $Time.Media was renamed to $Time.Stamp; the commit records renames with Spec-Rename lines"

// noFailedRun fails the scenario when a check run or card failed.
func noFailedRun(s *Scenario) {
	s.t.Helper()
	s.ExpectNoText("Check failed")
	s.Expect("no failed run", func(st tui.State) bool {
		for _, r := range st.Runs {
			if r.Failed != "" {
				return false
			}
		}
		return true
	})
}

// TestScenarioRenameIsNoFailedCheck: ^R and ^P after a hand rename check
// nothing and fail nothing. The rename is committed with its Spec-Rename
// line; the anchors whose references follow it do not differ from HEAD.
func TestScenarioRenameIsNoFailedCheck(t *testing.T) {
	s := Open(t, scenes.Demo, At("Frame.Timing"), Edits(renameEdits...), Replies(infer.CannedFile{
		Check: []infer.CannedEntry{cleanReply},
	}))
	s.ExpectStatus("Uncommitted")
	s.Press("ctrl+r")
	s.Idle()
	s.ExpectFocus("sidebar")
	s.ExpectFlashText(renamedFlash)
	noFailedRun(s)
	s.Press("esc")
	s.ExpectFocus("editor")
	s.Press("ctrl+p")
	s.Idle()
	s.ExpectFlashText(renamedFlash)
	noFailedRun(s)
	if ps := s.Proposals(); len(ps) != 0 {
		s.fail("a rename files no proposal, got %d", len(ps))
	}
}

// TestScenarioRenameBesideAnEditChecksTheEdit: with a rename and a real edit,
// ^R checks the edited anchor only.
func TestScenarioRenameBesideAnEditChecksTheEdit(t *testing.T) {
	edits := append(append([]scenes.Edit(nil), renameEdits...), scenes.Edit{File: "other.spec.md", Find: limitFind, Replace: limitReplace})
	s := Open(t, scenes.Demo, At("Frame.Timing"), Edits(edits...), Replies(infer.CannedFile{
		Check: []infer.CannedEntry{cleanReply},
	}))
	s.Press("ctrl+r")
	s.Idle()
	noFailedRun(s)
	ps := s.Proposals()
	if len(ps) != 1 || ps[0].Primary == nil || ps[0].Primary.Name != "$Extra.Limit" {
		s.fail("want one check of $Extra.Limit, got %d proposals", len(ps))
	}
}

// TestScenarioOutsideCommitMovesTheBaseline: a git commit made outside spec
// ui moves its HEAD baseline. The edit no longer reads as uncommitted, and
// ^R checks nothing rather than failing on an anchor that matches HEAD.
func TestScenarioOutsideCommitMovesTheBaseline(t *testing.T) {
	s := Open(t, scenes.Demo, At("Frame.Timing"), Watch(), Edits(timingEdit), Replies(infer.CannedFile{
		Check: []infer.CannedEntry{cleanReply},
	}))
	s.ExpectStatus("Uncommitted")
	s.Shell("git", "commit", "-qam", "bound fragment duration")
	s.WaitUntil("the status line drops Uncommitted", func(st tui.State) bool { return !strings.Contains(st.Status, "Uncommitted") })
	s.Press("ctrl+r")
	s.Idle()
	noFailedRun(s)
	if ps := s.Proposals(); len(ps) != 0 {
		s.fail("nothing differs from HEAD, so nothing is checked; got %d proposals", len(ps))
	}
}

// TestScenarioCommitViewCarriesModelAndSettings: the commit view lists a
// changed model and spec.toml beside the spec's changes, and A commits them.
func TestScenarioCommitViewCarriesModelAndSettings(t *testing.T) {
	s := Open(t, scenes.Demo, At("Frame.Timing"), Edits(timingEdit), Replies(infer.CannedFile{
		Check: []infer.CannedEntry{cleanReply},
		Draft: []infer.CannedEntry{draftReply},
	}))
	if err := os.MkdirAll(filepath.Join(s.Dir(), "tla"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.WriteFile("tla/Demo.tla", "---- MODULE Demo ----\nMaxSecs == 5\n====\n", true)
	s.WriteFile("spec.toml", "name = \"demo\"\nformal = [\"tla/*.tla\"]\n", false)
	s.Press("ctrl+r")
	s.Idle()
	s.Press("A")
	s.Idle()
	s.ExpectFocus("commit")
	s.Expect("the model and the settings listed and ticked", func(st tui.State) bool {
		want := map[string]string{"$Frame.Timing": "", "file:spec.toml": "settings", "file:tla/Demo.tla": "model"}
		if st.Commit == nil || len(st.Commit.Items) != len(want) {
			return false
		}
		for _, it := range st.Commit.Items {
			w, ok := want[it.ID]
			if !ok || !it.Ticked || w != "" && it.State != w {
				return false
			}
		}
		return true
	})
	s.ExpectText("model tla/Demo.tla")
	s.ExpectText("settings spec.toml")
	s.Press("A")
	s.Idle()
	s.ExpectFlash(`^Committed [0-9a-f]{7}: 3 changes`)
	if out := s.Shell("git", "show", "--name-only", "--format=", "HEAD"); out != "demo.spec.md\nspec.toml\ntla/Demo.tla\n" {
		s.fail("the commit holds %q", out)
	}
}
