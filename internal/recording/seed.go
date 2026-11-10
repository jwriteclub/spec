// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	mrand "math/rand/v2"
	"sync"
)

// SeededReader is a deterministic byte source (ChaCha8), safe for
// concurrent use: the proposal ids of a UI scene, and of a recorded
// session and its replay.
type SeededReader struct {
	mu  sync.Mutex
	src *mrand.ChaCha8
}

// NewSeeded returns the byte source of seed.
func NewSeeded(seed [32]byte) *SeededReader {
	return &SeededReader{src: mrand.NewChaCha8(seed)}
}

func (r *SeededReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.src.Read(p)
}

// NewSeed returns a random seed as 64 hex digits (Meta.IDsSeed).
func NewSeed() (string, error) {
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(seed[:]), nil
}

// Seeded returns the byte source of a seed written by NewSeed.
func Seeded(seedHex string) (*SeededReader, error) {
	b, err := hex.DecodeString(seedHex)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("ids seed %q is not 64 hex digits", seedHex)
	}
	var seed [32]byte
	copy(seed[:], b)
	return NewSeeded(seed), nil
}
