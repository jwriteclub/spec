// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/formal/tla"
)

// maxVisitsFlag is --max-acquisition-visits: prudence's per-pass budget of
// visits while it acquires a TLA+ model and the modules it loads. Set
// accepts only a positive integer; zero means the flag was not given.
type maxVisitsFlag int

// String implements pflag.Value.
func (f *maxVisitsFlag) String() string {
	if *f == 0 {
		return ""
	}
	return strconv.Itoa(int(*f))
}

// Set implements pflag.Value.
func (f *maxVisitsFlag) Set(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return fmt.Errorf("must be a positive integer number of visits")
	}
	*f = maxVisitsFlag(n)
	return nil
}

// Type implements pflag.Value.
func (f *maxVisitsFlag) Type() string {
	return "N"
}

// checkLimits returns the model-check bounds the command line sets; the zero
// value when it sets none.
func (s *session) checkLimits() formal.CheckLimits {
	return formal.CheckLimits{MaxAcquisitionVisits: int(s.maxVisits)}
}

// addLimitFlags adds --max-acquisition-visits to root, for every command:
// any command that lints a project checks its models.
func (s *session) addLimitFlags(root *cobra.Command) {
	root.PersistentFlags().Var(&s.maxVisits, "max-acquisition-visits", fmt.Sprintf(
		"TLA+ model check: prudence's per-pass acquisition visit budget, a positive integer; overrides [tla] max_acquisition_visits in spec.toml (default %d)",
		tla.DefaultMaxAcquisitionVisits))
}

// applyLimits makes the registered TLA+ checker apply the command line's
// bounds over spec.toml's for the rest of this run, and returns what undoes
// it. Run is one process's command line, so the registry is the place: every
// path that checks a model (lint, model proposals, spec ui, the language
// server) asks it.
func (s *session) applyLimits() (restore func()) {
	limits := s.checkLimits()
	if limits == (formal.CheckLimits{}) {
		return func() {}
	}
	prev := formal.CheckerFor(".tla")
	formal.RegisterChecker(".tla", formal.WithLimits(prev, limits))
	return func() { formal.RegisterChecker(".tla", prev) }
}
