// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"crypto/sha256"
	"strings"

	"github.com/jwriteclub/spec/internal/mdspec"
)

// Memo keeps what Cached computes for an editor between reparses: each
// spec file's words and flags, by content, and the classical pass over
// them, updated file by file. A reparse recomputes a file's flags only
// when its content, the standing of one of its words in the project,
// spec.words or the cache changed, and recomputes nothing when no spec
// file changed (a model edit). A Memo is not safe for concurrent use.
type Memo struct {
	files map[string]*memoFile
	// proj is the classical pass over files; code counts the files whose
	// code names each word, and base the words of the project name and
	// the anchors (see baseWords), which together make proj's explicit
	// words.
	proj *Project
	code map[string]int
	base map[string]bool
	// anchors and user (spec.words and the project name) are what base
	// and proj were built from; gen is the cache generation the flags
	// were computed at.
	anchors string
	user    string
	gen     uint64
	out     []Flag
	// computed counts the files whose flags were computed.
	computed int
	// dict is the word list (nil: System()).
	dict *Dictionary
}

// memoFile is one spec file's memo.
type memoFile struct {
	file  *mdspec.File
	sum   [sha256.Size]byte
	words *fileWords
	// flags hold while the standing of deps' words (Project.standing) is
	// unchanged; stale is set when the content changed.
	flags []Flag
	deps  map[string]bool
	stale bool
}

// NewMemo returns an empty Memo.
func NewMemo() *Memo {
	return &Memo{files: map[string]*memoFile{}}
}

// Computed is the number of file flag computations so far.
func (m *Memo) Computed() int { return m.computed }

// Flags returns Cached's flags for spec's files (of valid UTF-8): words
// are the spec.words entries and name the project name.
func (m *Memo) Flags(spec *mdspec.Spec, words []string, name string, cache *Cache) []Flag {
	gen := cache.Gen()
	user := name + "\x00" + strings.Join(words, "\x00")
	var anchors strings.Builder
	for _, a := range spec.Anchors {
		anchors.WriteString(a.Name)
		anchors.WriteByte(0)
	}
	if m.proj == nil || user != m.user {
		m.reset(words, name)
	}

	// The files that changed, with their old and new words.
	type change struct{ old, new *fileWords }
	var changes []change
	seen := make(map[string]bool, len(spec.Files))
	var order []*memoFile
	for _, f := range spec.Files {
		if !f.ValidUTF8 {
			continue
		}
		seen[f.Path] = true
		mf := m.files[f.Path]
		if mf == nil || mf.file != f {
			sum := sha256.Sum256(f.Source())
			if mf == nil || mf.sum != sum {
				nf := &memoFile{sum: sum, words: wordsOf(f), stale: true}
				ch := change{new: nf.words}
				if mf != nil {
					ch.old = mf.words
				}
				changes = append(changes, ch)
				mf = nf
				m.files[f.Path] = mf
			}
			mf.file = f
		}
		order = append(order, mf)
	}
	for p, mf := range m.files {
		if !seen[p] {
			changes = append(changes, change{old: mf.words})
			delete(m.files, p)
		}
	}
	if len(changes) == 0 && anchors.String() == m.anchors && gen == m.gen && m.out != nil {
		return m.out
	}

	// Apply the changes to the classical pass, noting the words whose
	// standing they changed.
	touched := map[string]uint8{}
	note := func(set map[string]bool) {
		for w := range set {
			touched[w] = m.proj.standing(w)
		}
	}
	for _, ch := range changes {
		for _, fw := range []*fileWords{ch.old, ch.new} {
			if fw != nil {
				note(fw.code)
				for w := range fw.uses {
					touched[w] = m.proj.standing(w)
				}
			}
		}
	}
	var newBase map[string]bool
	if anchors.String() != m.anchors {
		newBase = baseWords(name, spec.Anchors)
		note(m.base)
		note(newBase)
	}
	for _, ch := range changes {
		if ch.old != nil {
			m.proj.count(ch.old, -1)
			for w := range ch.old.code {
				if m.code[w]--; m.code[w] <= 0 {
					delete(m.code, w)
				}
			}
		}
		if ch.new != nil {
			m.proj.count(ch.new, 1)
			for w := range ch.new.code {
				m.code[w]++
			}
		}
	}
	if newBase != nil {
		m.base, m.anchors = newBase, anchors.String()
	}
	moved := map[string]bool{}
	for w, before := range touched {
		if m.base[w] || m.code[w] > 0 {
			m.proj.explicit[w] = true
		} else {
			delete(m.proj.explicit, w)
		}
		if m.proj.standing(w) != before {
			moved[w] = true
		}
	}

	all := gen != m.gen || m.out == nil
	out := []Flag{}
	for _, mf := range order {
		if all || mf.stale || mf.moved(moved) {
			m.compute(mf, cache)
		}
		out = append(out, mf.flags...)
	}
	sortFlags(out)
	m.out, m.gen = out, gen
	return out
}

// reset starts the classical pass over for new spec.words or a new
// project name: every file is counted again at the next Flags.
func (m *Memo) reset(words []string, name string) {
	m.proj = emptyProject(words, m.dict)
	m.code = map[string]int{}
	m.base = map[string]bool{}
	m.anchors = "\x00reset"
	m.user = name + "\x00" + strings.Join(words, "\x00")
	m.files = map[string]*memoFile{}
	m.out = nil
}

// moved reports whether one of mf's words is among moved.
func (mf *memoFile) moved(moved map[string]bool) bool {
	if len(moved) > len(mf.deps) {
		for w := range mf.deps {
			if moved[w] {
				return true
			}
		}
		return false
	}
	for w := range moved {
		if mf.deps[w] {
			return true
		}
	}
	return false
}

// compute computes mf's flags and notes the words they depend on.
func (m *Memo) compute(mf *memoFile, cache *Cache) {
	m.computed++
	f := mf.file
	flags, cands := m.proj.checkFile(f, mf.words.tokens, nil)
	mf.flags = append(flags, cached([]*mdspec.File{f}, cands, cache, "")...)
	sortFlags(mf.flags)
	if mf.deps == nil {
		mf.deps = make(map[string]bool, len(mf.words.uses))
		for w := range mf.words.uses {
			mf.deps[w] = true
		}
	}
	mf.stale = false
}
