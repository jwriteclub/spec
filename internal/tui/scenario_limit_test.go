// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui_test

import (
	"testing"

	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// TestScenarioFormalAcquisitionLimit: with spec.toml's acquisition limit set
// too low for the model, the status line says the check stopped there and
// the model carries the formal-check-limit hint, which names the fix.
func TestScenarioFormalAcquisitionLimit(t *testing.T) {
	s := openModel(t, Edits(scenes.Edit{
		File: "spec.toml", Find: "formal = [\"tla/*.tla\"]\n",
		Replace: "formal = [\"tla/*.tla\"]\n\n[tla]\nmax_acquisition_visits = 5\n",
	}))
	s.Expect("the limit's hint", func(st tui.State) bool { return st.Diagnostics == 1 })
	s.ExpectStatus("prudence: incomplete at ")
	s.ExpectStatus("the model hit the acquisition limit")
	s.Hover("---- MODULE Lock")
	s.ExpectText("formal-check-limit")
	s.ExpectText("--max-acquisition-visits")
}
