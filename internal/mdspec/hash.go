// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package mdspec

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jwriteclub/spec/internal/pos"
)

// HashPrefix is prepended to the normalized body before hashing (plan 1.5).
const HashPrefix = "spec-hash-v1\n"

// ShapePlaceholder replaces every reference name when computing a shape
// hash. It cannot occur in a valid name.
const ShapePlaceholder = "_"

// hashInput is what an anchor's hash is computed from: the body range minus
// the label, the code regions inside it (kept byte-exact), and the
// references inside it (which renames rewrite).
type hashInput struct {
	hashRange pos.Range
	code      []pos.Range // sorted, inside hashRange
	refs      []refSpan   // sorted, inside hashRange, names without $
}

type refSpan struct {
	r    pos.Range
	name string
}

// Substitution replaces the source bytes of Range with Text during
// normalization.
type Substitution struct {
	Range pos.Range
	Text  string
}

// HashNormalized returns the hex sha256 of HashPrefix + normalized.
func HashNormalized(normalized string) string {
	sum := sha256.Sum256([]byte(HashPrefix + normalized))
	return hex.EncodeToString(sum[:])
}

// ShortHash returns the first 8 hex characters of hash (all of it when
// shorter).
func ShortHash(hash string) string {
	if len(hash) < 8 {
		return hash
	}
	return hash[:8]
}

// Normalize applies the v1 body normalization to src[r] (plan 1.5): CRLF
// becomes LF; outside the code ranges every run of Unicode whitespace becomes
// one space; bytes inside code ranges are kept exactly (apart from CRLF);
// the result is trimmed of leading and trailing spaces. subs replace source
// ranges with other text before normalization (used to rename references);
// they must not overlap each other or the code ranges. Invalid UTF-8 bytes
// are kept as they are. code and subs need not be sorted.
func Normalize(src []byte, r pos.Range, code []pos.Range, subs []Substitution) string {
	if r.Start < 0 {
		r.Start = 0
	}
	if r.End > len(src) {
		r.End = len(src)
	}
	code = append([]pos.Range(nil), code...)
	sort.Slice(code, func(i, j int) bool { return code[i].Start < code[j].Start })
	subs = append([]Substitution(nil), subs...)
	sort.Slice(subs, func(i, j int) bool { return subs[i].Range.Start < subs[j].Range.Start })

	var b strings.Builder
	b.Grow(r.Len())
	pending := false // a whitespace run is waiting to be written as one space
	flush := func() {
		if pending && b.Len() > 0 {
			b.WriteByte(' ')
		}
		pending = false
	}
	ci, si := 0, 0
	i := r.Start
	for i < r.End {
		for ci < len(code) && code[ci].End <= i {
			ci++
		}
		for si < len(subs) && subs[si].Range.Start < i {
			si++
		}
		if si < len(subs) && subs[si].Range.Start == i && subs[si].Range.End > i {
			flush()
			b.WriteString(subs[si].Text)
			i = subs[si].Range.End
			si++
			continue
		}
		if ci < len(code) && code[ci].Start <= i {
			// Inside code: byte-exact, except CRLF becomes LF.
			flush()
			end := min(code[ci].End, r.End)
			for ; i < end; i++ {
				if src[i] == '\r' && i+1 < len(src) && src[i+1] == '\n' {
					continue
				}
				b.WriteByte(src[i])
			}
			continue
		}
		c, size := utf8.DecodeRune(src[i:r.End])
		if c == utf8.RuneError && size <= 1 {
			flush()
			b.WriteByte(src[i])
			i++
			continue
		}
		if unicode.IsSpace(c) {
			pending = true
			i += size
			continue
		}
		flush()
		b.Write(src[i : i+size])
		i += size
	}
	return b.String()
}

func (a *Anchor) normalized(name func(string) string) string {
	var subs []Substitution
	if name != nil {
		for _, rs := range a.input.refs {
			if n := name(rs.name); n != rs.name {
				subs = append(subs, Substitution{Range: rs.r, Text: n})
			}
		}
	}
	return Normalize(a.src, a.input.hashRange, a.input.code, subs)
}

// HashInput returns the normalized text the anchor's hash is computed from.
func (a *Anchor) HashInput() string {
	return a.normalized(nil)
}

// HashWithRenames returns the hash the anchor's body would have if every
// reference $Old in it were written $New, for each Old → New in renames
// (names without $). A nil or empty map gives a.Hash. The baseline diff uses
// it to apply detected renames before comparing hashes (plan 4, step 2).
func (a *Anchor) HashWithRenames(renames map[string]string) string {
	if len(renames) == 0 {
		return HashNormalized(a.normalized(nil))
	}
	return HashNormalized(a.normalized(func(n string) string {
		if to, ok := renames[n]; ok {
			return to
		}
		return n
	}))
}

// TitleWithRenames returns the title as it would read if every reference
// $Old in it were written $New, for each Old → New in renames (names without
// $). The baseline diff uses it so that a rename, which rewrites references
// in titles too, does not make the anchors whose titles mention the renamed
// one look retitled.
func (a *Anchor) TitleWithRenames(renames map[string]string) string {
	if len(renames) == 0 || len(a.titleRefs) == 0 {
		return a.Title
	}
	tr := a.TitleRange
	var b strings.Builder
	at := tr.Start
	for _, rs := range a.titleRefs {
		to, ok := renames[rs.name]
		if !ok {
			continue
		}
		b.Write(a.src[at:rs.r.Start])
		b.WriteString(to)
		at = rs.r.End
	}
	b.Write(a.src[at:tr.End])
	return strings.TrimSpace(b.String())
}

// ShapeHash returns the body hash with every reference name replaced by
// ShapePlaceholder (plan 4, step 1). Two bodies that differ only in which
// names they reference have the same shape hash.
func (a *Anchor) ShapeHash() string {
	return HashNormalized(a.normalized(func(string) string { return ShapePlaceholder }))
}

// HashRange returns the source range the hash is computed from: the body
// minus the label (and one following '.' or ':' and whitespace).
func (a *Anchor) HashRange() pos.Range {
	return a.input.hashRange
}
