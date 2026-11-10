// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package project

import (
	"errors"
	"os"
)

var errLockBusy = errors.New("lock is held")

func flockExclusive(f *os.File) error {
	return errors.New("the writer lock is not supported on this platform")
}

func funlock(f *os.File) error {
	return nil
}
