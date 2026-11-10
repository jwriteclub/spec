// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package edit

import (
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/jwriteclub/spec/internal/golden"
	"github.com/jwriteclub/spec/internal/pos"
)

func sp(start, end int, old, new string) Splice {
	return Splice{Range: pos.Range{Start: start, End: end}, Old: old, New: new}
}

func TestApplyBasic(t *testing.T) {
	src := []byte("hello brave new world")
	out, err := Apply(src, []Splice{sp(6, 11, "brave", "cruel"), sp(0, 5, "hello", "goodbye")})
	require.NoError(t, err)
	assert.Equal(t, "goodbye cruel new world", string(out), "unsorted input is fine")
	assert.Equal(t, "hello brave new world", string(src), "source is not modified")
}

func TestApplyNoSplices(t *testing.T) {
	out, err := Apply([]byte("abc"), nil)
	require.NoError(t, err)
	assert.Equal(t, "abc", string(out))
}

func TestApplyInsertAndDelete(t *testing.T) {
	out, err := Apply([]byte("abcdef"), []Splice{sp(3, 3, "", "XY"), sp(4, 6, "ef", "")})
	require.NoError(t, err)
	assert.Equal(t, "abcXYd", string(out))
}

func TestApplyTouchingAndSameOffsetInserts(t *testing.T) {
	out, err := Apply([]byte("abcdef"), []Splice{sp(0, 3, "abc", "1"), sp(3, 6, "def", "2")})
	require.NoError(t, err)
	assert.Equal(t, "12", string(out))

	out, err = Apply([]byte("ab"), []Splice{sp(1, 1, "", "X"), sp(1, 1, "", "Y")})
	require.NoError(t, err)
	assert.Equal(t, "aXYb", string(out), "inserts at one offset keep their order")
}

func TestApplyMismatch(t *testing.T) {
	_, err := Apply([]byte("hello"), []Splice{sp(0, 5, "HELLO", "x")})
	var me *MismatchError
	require.ErrorAs(t, err, &me)
	assert.Equal(t, "hello", me.Found)
	assert.Contains(t, err.Error(), "changed since it was parsed")
}

func TestApplyOutOfBounds(t *testing.T) {
	for _, s := range []Splice{sp(0, 6, "hello!", ""), sp(-1, 2, "", ""), sp(4, 2, "", "")} {
		_, err := Apply([]byte("hello"), []Splice{s})
		var me *MismatchError
		require.ErrorAs(t, err, &me, s.Range.String())
		assert.True(t, me.Bounds)
	}
}

func TestApplyOverlap(t *testing.T) {
	_, err := Apply([]byte("abcdef"), []Splice{sp(0, 4, "abcd", "x"), sp(3, 6, "def", "y")})
	var oe *OverlapError
	require.ErrorAs(t, err, &oe)
}

func TestApplyLeavesOtherBytesExact(t *testing.T) {
	src := []byte("\xef\xbb\xbf**$A.Bc.** x\r\n$A.Bc \xff\x00 end")
	i := strings.Index(string(src), "$A.Bc.")
	j := strings.LastIndex(string(src), "$A.Bc")
	out, err := Apply(src, []Splice{sp(i+1, i+5, "A.Bc", "Z.Yx"), sp(j+1, j+5, "A.Bc", "Z.Yx")})
	require.NoError(t, err)
	assert.Equal(t, strings.ReplaceAll(string(src), "A.Bc", "Z.Yx"), string(out))
}

func TestApplyRandomAgainstNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 300; iter++ {
		n := rng.Intn(60)
		src := make([]byte, n)
		for i := range src {
			src[i] = byte('a' + rng.Intn(26))
		}
		// Build non-overlapping splices left to right.
		var splices []Splice
		var want []byte
		at := 0
		for at < n {
			skip := rng.Intn(6)
			if at+skip > n {
				skip = n - at
			}
			want = append(want, src[at:at+skip]...)
			at += skip
			if at >= n {
				break
			}
			l := rng.Intn(5)
			if at+l > n {
				l = n - at
			}
			repl := strings.Repeat("Z", rng.Intn(4))
			splices = append(splices, sp(at, at+l, string(src[at:at+l]), repl))
			want = append(want, repl...)
			at += l
			if l == 0 {
				want = append(want, src[at])
				at++
			}
		}
		rng.Shuffle(len(splices), func(i, j int) { splices[i], splices[j] = splices[j], splices[i] })
		got, err := Apply(src, splices)
		// Shuffling can reorder same-offset inserts; skip those comparisons.
		if err != nil {
			t.Fatalf("iter %d: %v", iter, err)
		}
		if len(splices) < 2 || !hasSameOffset(splices) {
			assert.Equal(t, string(want), string(got), "iter %d", iter)
		}
	}
}

func hasSameOffset(ss []Splice) bool {
	seen := map[int]bool{}
	for _, s := range ss {
		if seen[s.Range.Start] {
			return true
		}
		seen[s.Range.Start] = true
	}
	return false
}

// --- plans and the write protocol ---

func writeFile(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), mode))
	require.NoError(t, os.Chmod(p, mode))
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(b)
}

func noTemps(t *testing.T, root string) {
	t.Helper()
	var left []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".spec-edit-") {
			left = append(left, p)
		}
		return nil
	})
	assert.Empty(t, left, "temp files must not be left behind")
}

// twoFiles creates a.md and sub/b.md and returns edits turning "old" into "new" in both.
func twoFiles(t *testing.T) (string, []FileEdit) {
	root := t.TempDir()
	writeFile(t, root, "a.md", "one old two\n", 0o644)
	writeFile(t, root, "sub/b.md", "old three\n", 0o755)
	return root, []FileEdit{
		{Path: "sub/b.md", Snapshot: []byte("old three\n"), Splices: []Splice{sp(0, 3, "old", "new")}},
		{Path: "a.md", Snapshot: []byte("one old two\n"), Splices: []Splice{sp(4, 7, "old", "new")}},
	}
}

func TestPlanDoesNotWrite(t *testing.T) {
	root, edits := twoFiles(t)
	plan, err := NewPlan(root, edits)
	require.NoError(t, err)
	require.Len(t, plan.Outputs, 2)
	assert.Equal(t, "a.md", plan.Outputs[0].Path, "sorted by path")
	assert.Equal(t, "one new two\n", string(plan.Outputs[0].After))
	assert.Equal(t, "one old two\n", readFile(t, root, "a.md"))
}

func TestPlanOmitsNoOpsAndMergesDuplicates(t *testing.T) {
	root := t.TempDir()
	snap := []byte("abc def")
	plan, err := NewPlan(root, []FileEdit{
		{Path: "x.md", Snapshot: snap, Splices: []Splice{sp(0, 3, "abc", "abc")}},
		{Path: "y.md", Snapshot: snap, Splices: []Splice{sp(0, 3, "abc", "1")}},
		{Path: "y.md", Snapshot: snap, Splices: []Splice{sp(4, 7, "def", "2")}},
		{Path: "z.md", Snapshot: snap},
	})
	require.NoError(t, err)
	require.Len(t, plan.Outputs, 1)
	assert.Equal(t, "1 2", string(plan.Outputs[0].After))
}

func TestPlanVerifiesSplices(t *testing.T) {
	_, err := NewPlan(t.TempDir(), []FileEdit{{Path: "x.md", Snapshot: []byte("abc"), Splices: []Splice{sp(0, 3, "xyz", "1")}}})
	var me *MismatchError
	require.ErrorAs(t, err, &me)
	assert.Equal(t, "x.md", me.Path)

	_, err = NewPlan(t.TempDir(), []FileEdit{
		{Path: "x.md", Snapshot: []byte("abc")}, {Path: "x.md", Snapshot: []byte("abd")},
	})
	assert.Error(t, err)
}

func TestCommitSuccessPreservesModeAndLeavesNoTemps(t *testing.T) {
	root, edits := twoFiles(t)
	plan, err := NewPlan(root, edits)
	require.NoError(t, err)
	require.NoError(t, plan.Commit())
	assert.Equal(t, "one new two\n", readFile(t, root, "a.md"))
	assert.Equal(t, "new three\n", readFile(t, root, "sub/b.md"))
	st, err := os.Stat(filepath.Join(root, "sub/b.md"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), st.Mode().Perm())
	st, err = os.Stat(filepath.Join(root, "a.md"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), st.Mode().Perm())
	noTemps(t, root)
}

func TestCommitEmptyPlan(t *testing.T) {
	assert.NoError(t, (&Plan{Root: t.TempDir()}).Commit())
}

func TestCommitConcurrentEdit(t *testing.T) {
	root, edits := twoFiles(t)
	plan, err := NewPlan(root, edits)
	require.NoError(t, err)
	writeFile(t, root, "sub/b.md", "old three!\n", 0o755) // someone else edits
	err = plan.Commit()
	var ce *ConcurrentEditError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "sub/b.md", ce.Path)
	assert.Equal(t, "one old two\n", readFile(t, root, "a.md"), "nothing was written")
	assert.Equal(t, "old three!\n", readFile(t, root, "sub/b.md"))
	noTemps(t, root)
}

func TestCommitConcurrentEditBetweenPrepareAndVerify(t *testing.T) {
	root, edits := twoFiles(t)
	plan, err := NewPlan(root, edits)
	require.NoError(t, err)
	afterPrepare = func() { writeFile(t, root, "a.md", "one old two EDITED\n", 0o644) }
	defer func() { afterPrepare = func() {} }()
	err = plan.Commit()
	var ce *ConcurrentEditError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "a.md", ce.Path)
	assert.Equal(t, "old three\n", readFile(t, root, "sub/b.md"))
	noTemps(t, root)
}

func TestCommitTargetVanished(t *testing.T) {
	root, edits := twoFiles(t)
	plan, err := NewPlan(root, edits)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(root, "a.md")))
	var ce *ConcurrentEditError
	require.ErrorAs(t, plan.Commit(), &ce)
	noTemps(t, root)
}

func TestCommitRefusesSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "real.md", "old\n", 0o644)
	require.NoError(t, os.Symlink("real.md", filepath.Join(root, "link.md")))
	plan, err := NewPlan(root, []FileEdit{{Path: "link.md", Snapshot: []byte("old\n"), Splices: []Splice{sp(0, 3, "old", "new")}}})
	require.NoError(t, err)
	var we *WriteError
	require.ErrorAs(t, plan.Commit(), &we)
	assert.Equal(t, "old\n", readFile(t, root, "real.md"))
	noTemps(t, root)
}

func TestCommitRenameFailureRestores(t *testing.T) {
	root, edits := twoFiles(t)
	plan, err := NewPlan(root, edits)
	require.NoError(t, err)

	calls := 0
	renameFile = func(from, to string) error {
		calls++
		if calls == 2 { // the second target fails; the restore renames (calls 3+) succeed
			return errors.New("disk on fire")
		}
		return os.Rename(from, to)
	}
	defer func() { renameFile = os.Rename }()

	err = plan.Commit()
	var we *WriteError
	require.ErrorAs(t, err, &we)
	assert.Equal(t, "sub/b.md", we.Path)
	assert.Equal(t, []string{"a.md"}, we.Restored)
	assert.Empty(t, we.RestoreFailed)
	assert.Contains(t, err.Error(), "disk on fire")
	assert.Equal(t, "one old two\n", readFile(t, root, "a.md"), "restored from the snapshot")
	assert.Equal(t, "old three\n", readFile(t, root, "sub/b.md"))
	st, err := os.Stat(filepath.Join(root, "a.md"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), st.Mode().Perm())
	noTemps(t, root)
}

func TestCommitRestoreFailureIsReported(t *testing.T) {
	root, edits := twoFiles(t)
	plan, err := NewPlan(root, edits)
	require.NoError(t, err)
	calls := 0
	renameFile = func(from, to string) error {
		calls++
		if calls >= 2 {
			return errors.New("still on fire")
		}
		return os.Rename(from, to)
	}
	defer func() { renameFile = os.Rename }()

	err = plan.Commit()
	var we *WriteError
	require.ErrorAs(t, err, &we)
	assert.Equal(t, []string{"a.md"}, we.RestoreFailed)
	assert.Contains(t, err.Error(), "COULD NOT restore a.md")
	assert.Equal(t, "one new two\n", readFile(t, root, "a.md"))
	noTemps(t, root)
}
