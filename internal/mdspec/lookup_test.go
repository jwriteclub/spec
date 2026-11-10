// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package mdspec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseDir parses every *.spec.md in dir, as the fixtures are laid out.
func parseDir(t *testing.T, dir string) []*File {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.spec.md"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	var files []*File
	for _, p := range paths {
		src, err := os.ReadFile(p)
		require.NoError(t, err)
		files = append(files, ParseFile(filepath.Base(p), src))
	}
	return files
}

// The point lookups an editor needs (hover, go-to-definition, references)
// must agree with the lists: for every byte of the file, the token or anchor
// reported at that byte is the one whose range holds it, and nothing else.
func TestAtLookupsAgreeWithLists(t *testing.T) {
	for _, dir := range []string{"edge", "multi"} {
		for _, f := range parseDir(t, filepath.Join(testdata, dir)) {
			if !f.ValidUTF8 {
				continue
			}
			n := len(f.Source())
			for off := 0; off <= n; off++ {
				var wantOcc *Occurrence
				for _, o := range f.Occurrences {
					if off >= o.Range.Start && off <= o.Range.End {
						require.Nil(t, wantOcc, "%s: tokens touch at %d", f.Path, off)
						wantOcc = o
					}
				}
				assert.Same(t, wantOcc, f.OccurrenceAt(off), "%s offset %d", f.Path, off)

				var wantRef *SectionRef
				for _, r := range f.SectionRefs {
					if off >= r.Range.Start && off <= r.Range.End {
						require.Nil(t, wantRef, "%s: section refs touch at %d", f.Path, off)
						wantRef = r
					}
				}
				assert.Same(t, wantRef, f.SectionRefAt(off), "%s offset %d", f.Path, off)

				var wantAnchor *Anchor
				for _, a := range f.Anchors {
					if a.BodyRange.Contains(off) {
						wantAnchor = a
					}
				}
				assert.Same(t, wantAnchor, f.AnchorAt(off), "%s offset %d", f.Path, off)
			}
		}
	}
}

func TestOccurrenceAtInclusiveEnd(t *testing.T) {
	f := ParseFile("t.spec.md", []byte("See $Ab.Cd here.\n"))
	require.Len(t, f.Occurrences, 1)
	o := f.Occurrences[0]
	assert.Nil(t, f.OccurrenceAt(o.Range.Start-1))
	assert.Same(t, o, f.OccurrenceAt(o.Range.Start))
	assert.Same(t, o, f.OccurrenceAt(o.Range.End))
	assert.Nil(t, f.OccurrenceAt(o.Range.End+1))
	assert.Nil(t, f.OccurrenceAt(-5))
	assert.Nil(t, f.OccurrenceAt(1000))
}

// Spec.Resolve maps every reference occurrence of an included file to its
// entry in Refs, and nothing else.
func TestResolveMapsOccurrencesToRefs(t *testing.T) {
	files := parseDir(t, filepath.Join(testdata, "multi"))
	bad := ParseFile("bad.spec.md", []byte("See $Ab.Cd \xff\n"))
	s := Build(append(files, bad))
	require.False(t, bad.ValidUTF8)

	seen := map[*ResolvedRef]bool{}
	for _, f := range s.Files {
		for _, o := range f.Occurrences {
			rr := s.Resolve(o)
			if !f.ValidUTF8 || o.Role != RoleReference {
				assert.Nil(t, rr, "%s %s", f.Path, o.Name)
				continue
			}
			require.NotNil(t, rr, "%s %s", f.Path, o.Name)
			assert.Same(t, o, rr.Occurrence)
			seen[rr] = true
		}
		for _, r := range f.SectionRefs {
			rs := s.ResolveSectionRef(r)
			if !f.ValidUTF8 {
				assert.Nil(t, rs)
				continue
			}
			require.NotNil(t, rs)
			assert.Same(t, r, rs.SectionRef)
		}
	}
	assert.Len(t, seen, len(s.Refs))

	// An occurrence from a File this Spec was not built from is unknown.
	other := ParseFile("other.spec.md", []byte("See $Ab.Cd\n"))
	assert.Nil(t, s.Resolve(other.Occurrences[0]))
	assert.Nil(t, s.Resolve(nil))
}
