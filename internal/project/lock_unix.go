// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package project

import (
	"errors"
	"os"
	"syscall"
)

var errLockBusy = errors.New("lock is held")

func flockExclusive(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return errLockBusy
		}
		return err
	}
}

func funlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
