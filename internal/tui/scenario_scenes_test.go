// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"testing"

	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// TestScenarioScenes plays every scene of the registry as a scenario at
// its first size: each step reaches its wait, and the frame's structure
// holds after every step (ExpectLayout: the terminal's size, the status
// line with the whole flash, the help bar's entries whole). A scene
// ends at rest: no job runs (but in the scene of a running cascade) and
// no notice is open, so its screenshot does not race a background job.
func TestScenarioScenes(t *testing.T) {
	for _, sc := range scenes.All() {
		t.Run(sc.Name, func(t *testing.T) {
			s := FromScene(t, sc.Name)
			s.Play()
			if sc.Name == "cascade-running" {
				return
			}
			s.Expect("the scene at rest", func(st tui.State) bool { return st.Job == "" && !st.Notice })
		})
	}
}
