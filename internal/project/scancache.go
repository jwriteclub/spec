// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"crypto/sha256"
	"path"
	"sync"

	"github.com/jwriteclub/spec/internal/formal"
)

// ScanCache keeps the models a long-lived caller (the language server, the
// UI) has scanned, so a load does not parse a model again while its text, path
// and parser are the same. It holds one scan per path and at most max paths. It is
// safe for concurrent use. A scanned formal.File is never changed after its
// scan, so loads share it.
type ScanCache struct {
	mu      sync.Mutex
	max     int
	entries map[string]scanEntry
	// checks keeps the models' full checks for the same caller.
	checks *CheckCache
}

// scanEntry is the latest scan of one path and what it was scanned from.
type scanEntry struct {
	key  string
	file *formal.File
}

// NewScanCache returns a cache of at most size paths (at least 1).
func NewScanCache(size int) *ScanCache {
	return &ScanCache{max: max(1, size), entries: map[string]scanEntry{}, checks: NewCheckCache()}
}

// Checks returns the cache of the models' full checks that goes with c; nil
// (check every time) for a nil c.
func (c *ScanCache) Checks() *CheckCache {
	if c == nil {
		return nil
	}
	return c.checks
}

// Scan returns the scan of src at rel, from the cache when it holds one of the
// same bytes by the same parser version; nil when no language reads rel's
// extension. A nil cache always scans.
func (c *ScanCache) Scan(rel string, src []byte) *formal.File {
	ext := path.Ext(rel)
	scan, ok := scanners[ext]
	if !ok {
		return nil
	}
	if c == nil {
		return scan(rel, src)
	}
	sum := sha256.Sum256(src)
	key := scanVersions[ext] + "\x00" + string(sum[:])
	c.mu.Lock()
	e, ok := c.entries[rel]
	c.mu.Unlock()
	if ok && e.key == key {
		return e.file
	}
	f := scan(rel, src)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, have := c.entries[rel]; !have && len(c.entries) >= c.max {
		for p := range c.entries {
			delete(c.entries, p) // any one: the bound matters, not which goes
			break
		}
	}
	c.entries[rel] = scanEntry{key: key, file: f}
	return f
}
