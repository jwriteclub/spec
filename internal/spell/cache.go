// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CacheFile is where verdicts are kept, relative to the project root
// (.spec/ is the project's untracked state directory).
const CacheFile = ".spec/spell/verdicts.json"

// cacheTTL drops verdicts not asked again for this long when the cache is
// written.
const cacheTTL = 90 * 24 * time.Hour

// Stages that decide a verdict.
const (
	// StageKnown: the known-misspellings table flagged it.
	StageKnown = "known"
	// StageGate: Jev scored the word below the flag threshold.
	StageGate = "gate"
	// StageTriage: the triage model named it a typo (Typo) or did not.
	StageTriage = "triage"
	// StageGuard: the triage model's fix failed the fix guard; not shown.
	StageGuard = "guard"
)

// Verdict is the model pass's judgement of one candidate.
type Verdict struct {
	Word string `json:"word"`
	// Typo is true when the word is flagged; Fix is the correction (for a
	// guard verdict, the rejected one).
	Typo bool   `json:"typo"`
	Fix  string `json:"fix,omitempty"`
	// Stage is StageGate, StageTriage or StageGuard; Score is Jev's.
	Stage string  `json:"stage"`
	Score float64 `json:"score"`
	// Models names the models and prompt that judged it (Models.ID).
	Models string    `json:"models"`
	At     time.Time `json:"at"`
}

// Cache holds verdicts by candidate key, in memory and in CacheFile. It
// is safe for concurrent use; the file is re-read when another process
// changed it.
type Cache struct {
	path    string
	mu      sync.Mutex
	entries map[string]Verdict
	// stamp is the file's modification time and size when last read or
	// written.
	stamp fileStamp
	dirty map[string]bool
	// gen counts changes to entries; byWord indexes them by word as of
	// generation byWordGen (see wordVerdict).
	gen       uint64
	byWord    map[string][]string
	byWordGen uint64
}

type fileStamp struct {
	mod  time.Time
	size int64
}

var (
	cachesMu sync.Mutex
	caches   = map[string]*Cache{}
)

// OpenCache returns the cache of the project at root, shared by every
// caller in the process.
func OpenCache(root string) *Cache {
	path := filepath.Join(root, filepath.FromSlash(CacheFile))
	cachesMu.Lock()
	defer cachesMu.Unlock()
	if c, ok := caches[path]; ok {
		return c
	}
	c := &Cache{path: path, entries: map[string]Verdict{}, dirty: map[string]bool{}}
	caches[path] = c
	return c
}

// Path is the cache file's path.
func (c *Cache) Path() string { return c.path }

// Exists reports whether the cache file exists or this process holds
// verdicts: whether a spell check has run for the project.
func (c *Cache) Exists() bool {
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > 0 {
		return true
	}
	_, err := os.Stat(c.path)
	return err == nil
}

// refresh re-reads the file when it changed since the last read or write.
// The caller holds mu.
func (c *Cache) refresh() {
	st, err := os.Stat(c.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && c.stamp != (fileStamp{}) {
			// The file was removed: forget what it held.
			for k := range c.entries {
				if !c.dirty[k] {
					delete(c.entries, k)
				}
			}
			c.stamp = fileStamp{}
			c.gen++
		}
		return
	}
	now := fileStamp{mod: st.ModTime(), size: st.Size()}
	if now == c.stamp {
		return
	}
	disk, err := readVerdicts(c.path)
	if err != nil {
		lg.WithError(err).WithField("path", c.path).Warn("cannot read the spell cache; ignoring it")
		c.stamp = now
		return
	}
	for k, v := range disk {
		if !c.dirty[k] {
			c.entries[k] = v
		}
	}
	c.stamp = now
	c.gen++
}

// readVerdicts reads a cache file.
func readVerdicts(path string) (map[string]Verdict, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Version  int                `json:"version"`
		Verdicts map[string]Verdict `json:"verdicts"`
	}
	if err := json.Unmarshal(src, &file); err != nil {
		return nil, err
	}
	if file.Verdicts == nil {
		file.Verdicts = map[string]Verdict{}
	}
	return file.Verdicts, nil
}

// Lookup returns the verdict of key.
func (c *Cache) Lookup(key string) (Verdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refresh()
	v, ok := c.entries[key]
	return v, ok
}

// Put records a verdict; Save writes it.
func (c *Cache) Put(key string, v Verdict) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = v
	c.dirty[key] = true
	c.gen++
}

// Gen returns the cache's generation: it changes whenever a verdict is
// added, changed or dropped, in this process or (once seen) in the file.
func (c *Cache) Gen() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refresh()
	return c.gen
}

// verdictOf returns the verdict on candidate cand: the one for its exact
// key, else the latest model verdict on the same word in another sentence,
// so a word judged in one sentence keeps its verdict while the text around
// it changes. A fallback typo verdict on the word in other capitals is
// used only when its fix follows the word's capitals. The caller holds mu
// and has refreshed.
func (c *Cache) verdictOf(cand Candidate) (Verdict, bool) {
	if v, ok := c.entries[cand.Key]; ok {
		return v, true
	}
	if c.byWord == nil || c.byWordGen != c.gen {
		c.byWord = map[string][]string{}
		for k, v := range c.entries {
			if v.Stage != StageKnown {
				lw := strings.ToLower(v.Word)
				c.byWord[lw] = append(c.byWord[lw], k)
			}
		}
		c.byWordGen = c.gen
	}
	var best Verdict
	bestKey := ""
	for _, k := range c.byWord[strings.ToLower(cand.Word)] {
		v := c.entries[k]
		if v.Typo && v.Word != cand.Word {
			if v.Fix != MatchCase(v.Word, strings.ToLower(v.Fix)) {
				continue
			}
			v.Fix = MatchCase(cand.Word, strings.ToLower(v.Fix))
		}
		exact, bestExact := v.Word == cand.Word, best.Word == cand.Word
		if bestKey == "" || exact && !bestExact || exact == bestExact && (v.At.After(best.At) || v.At.Equal(best.At) && k > bestKey) {
			best, bestKey = v, k
		}
	}
	return best, bestKey != ""
}

// Len is the number of verdicts held.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refresh()
	return len(c.entries)
}

// Save merges the file's verdicts with this process's new ones and writes
// it back atomically, dropping verdicts older than 90 days.
func (c *Cache) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.dirty) == 0 {
		return nil
	}
	merged := map[string]Verdict{}
	if disk, err := readVerdicts(c.path); err == nil {
		merged = disk
	} else if !errors.Is(err, fs.ErrNotExist) {
		lg.WithError(err).WithField("path", c.path).Warn("cannot read the spell cache; rewriting it")
	}
	for k := range c.dirty {
		merged[k] = c.entries[k]
	}
	cut := time.Now().Add(-cacheTTL)
	for k, v := range merged {
		if v.At.Before(cut) {
			delete(merged, k)
		}
	}
	raw, err := json.MarshalIndent(map[string]any{"version": 1, "verdicts": merged}, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".verdicts-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), c.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	c.entries = merged
	c.dirty = map[string]bool{}
	c.gen++
	if st, err := os.Stat(c.path); err == nil {
		c.stamp = fileStamp{mod: st.ModTime(), size: st.Size()}
	}
	return nil
}
