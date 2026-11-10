// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/mdspec"
)

// diffWork compares the baseline at src with the working tree.
func (r *repo) diffWork(src Source) *Diff {
	r.t.Helper()
	b := r.load(src)
	require.NotNil(r.t, b.Spec)
	l, err := r.project().Load(nil)
	require.NoError(r.t, err)
	return Compare(b.Spec, l.Spec)
}

// diffStaged compares HEAD with the index (what `spec check` does).
func (r *repo) diffStaged() *Diff {
	r.t.Helper()
	old := r.load(Source{})
	cur := r.load(Source{Index: true})
	require.NotNil(r.t, old.Spec)
	return Compare(old.Spec, cur.Spec)
}

func entryNames(d *Diff, s Status) []string {
	var out []string
	for _, e := range d.Entries {
		if e.Status == s {
			out = append(out, e.Name)
		}
	}
	return out
}

func entry(t *testing.T, d *Diff, name string) Entry {
	t.Helper()
	for _, e := range d.Entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no entry for %s in %+v", name, d.Entries)
	return Entry{}
}

func TestDiffClean(t *testing.T) {
	r := seeded(t)
	d := r.diffWork(Source{})
	assert.Empty(t, d.Entries)
	assert.Equal(t, 3, d.Unchanged)
	assert.False(t, d.SourcesDiffer)
	assert.Equal(t, StateClean, d.State())
	assert.True(t, d.Required().Empty())
}

func TestDiffProseOnlyEdit(t *testing.T) {
	r := seeded(t)
	// Reflow inside an anchor and edit prose outside every anchor.
	r.write("a.spec.md", strings.Replace(strings.Replace(baseA, "Media time is a count of ticks.", "Media time is a count\nof ticks.", 1), "A prose paragraph.", "A different prose paragraph.", 1))
	d := r.diffWork(Source{})
	assert.Empty(t, d.Entries)
	assert.True(t, d.SourcesDiffer)
	assert.Equal(t, StateProseOnly, d.State())
	assert.True(t, d.Required().Empty())
}

func TestDiffChangedAddedRemoved(t *testing.T) {
	r := seeded(t)
	a := strings.Replace(baseA, "Media time is a count of ticks.", "Media time is a count of 90 kHz ticks.", 1)
	a += "\n**$Brand.New (new).** A brand new invariant about $Time.Media.\n"
	r.write("a.spec.md", a)
	r.write("b.spec.md", "# B\n") // Hub.Idle removed
	d := r.diffWork(Source{})

	assert.Equal(t, []string{"Time.Media"}, entryNames(d, Changed))
	assert.Equal(t, []string{"Brand.New"}, entryNames(d, Added))
	assert.Equal(t, []string{"Hub.Idle"}, entryNames(d, Removed))
	assert.Equal(t, StateAnchorsChanged, d.State())

	ch := entry(t, d, "Time.Media")
	assert.True(t, ch.Changed)
	assert.False(t, ch.KindChanged)
	assert.NotEqual(t, ch.OldHash, ch.NewHash)
	assert.Len(t, ch.OldShort, 8)
	// Frame.Timing is untouched even though it references the changed definition.
	assert.NotContains(t, entryNames(d, Changed), "Frame.Timing")

	assert.Equal(t, []string{
		"Spec-Change: $Brand.New - -> inv:" + entry(t, d, "Brand.New").NewShort,
		"Spec-Change: $Hub.Idle inv:" + entry(t, d, "Hub.Idle").OldShort + " -> -",
		"Spec-Change: $Time.Media def:" + ch.OldShort + " -> def:" + ch.NewShort,
	}, d.Required().Lines())
}

func TestDiffKindChanged(t *testing.T) {
	r := seeded(t)
	r.write("a.spec.md", strings.Replace(baseA, "**Def $Time.Media (media time).**", "**$Time.Media (media time).**", 1))
	d := r.diffWork(Source{})
	require.Len(t, d.Entries, 1)
	e := d.Entries[0]
	assert.Equal(t, KindChanged, e.Status)
	assert.True(t, e.KindChanged)
	assert.False(t, e.Changed)
	assert.Equal(t, mdspec.KindDefinition, e.OldKind)
	assert.Equal(t, mdspec.KindInvariant, e.NewKind)
	assert.Equal(t, []string{"Spec-Change: $Time.Media def:" + e.OldShort + " -> inv:" + e.NewShort}, d.Required().Lines())
	assert.Equal(t, e.OldShort, e.NewShort, "only the kind prefix differs")
}

func TestDiffRetitledNeedsNoTrailer(t *testing.T) {
	r := seeded(t)
	r.write("a.spec.md", strings.Replace(baseA, "(media time)", "(media clock)", 1))
	d := r.diffWork(Source{})
	require.Len(t, d.Entries, 1)
	assert.Equal(t, Retitled, d.Entries[0].Status)
	assert.Equal(t, "media time", d.Entries[0].OldTitle)
	assert.Equal(t, "media clock", d.Entries[0].NewTitle)
	assert.True(t, d.Required().Empty())
	assert.Equal(t, StateProseOnly, d.State(), "a title alone needs no trailer")
}

// renameAll rewrites every $Frame.Timing to $Frame.Clock in the working tree.
func renameAll(r *repo, from, to string) {
	for _, f := range []string{"a.spec.md", "b.spec.md"} {
		b, err := os.ReadFile(r.path(f))
		require.NoError(r.t, err)
		r.write(f, strings.ReplaceAll(string(b), "$"+from, "$"+to))
	}
}

func TestDiffRenameMovesNoReferencerToChanged(t *testing.T) {
	r := seeded(t)
	renameAll(r, "Frame.Timing", "Frame.Clock")
	d := r.diffWork(Source{})
	require.Len(t, d.Entries, 1, "%+v", d.Entries)
	e := d.Entries[0]
	assert.Equal(t, Renamed, e.Status)
	assert.Equal(t, "Frame.Clock", e.Name)
	assert.Equal(t, "Frame.Timing", e.OldName)
	assert.False(t, e.Changed)
	assert.Equal(t, map[string]string{"Frame.Timing": "Frame.Clock"}, d.Renames)
	assert.Equal(t, StateAnchorsChanged, d.State())
	assert.Empty(t, entryNames(d, Changed), "Hub.Idle references the renamed name but did not change")
	assert.Equal(t, []string{"Spec-Rename: $Frame.Timing -> $Frame.Clock"}, d.Required().Lines())
}

func TestDiffRenamePlusRewordIsRemovedAndAdded(t *testing.T) {
	r := seeded(t)
	renameAll(r, "Frame.Timing", "Frame.Clock")
	b, err := os.ReadFile(r.path("a.spec.md"))
	require.NoError(t, err)
	r.write("a.spec.md", strings.Replace(string(b), "Uses $Time.Media for ticks.", "Counts in ticks of $Time.Media.", 1))
	d := r.diffWork(Source{})
	assert.Empty(t, d.Renames)
	assert.Equal(t, []string{"Frame.Timing"}, entryNames(d, Removed))
	assert.Equal(t, []string{"Frame.Clock"}, entryNames(d, Added))
	assert.Empty(t, entryNames(d, Renamed))
}

func TestDiffRenameAcrossFilesStillRenamed(t *testing.T) {
	r := seeded(t)
	renameAll(r, "Hub.Idle", "Hub.Park")
	d := r.diffWork(Source{})
	require.Len(t, d.Entries, 1)
	assert.Equal(t, Renamed, d.Entries[0].Status)
}

func TestDiffRenamedAndReworded_ReferencerChanged(t *testing.T) {
	r := seeded(t)
	renameAll(r, "Frame.Timing", "Frame.Clock")
	b, err := os.ReadFile(r.path("b.spec.md"))
	require.NoError(t, err)
	r.write("b.spec.md", strings.Replace(string(b), "to stop.", "to stop promptly.", 1))
	d := r.diffWork(Source{})
	assert.Equal(t, []string{"Frame.Clock"}, entryNames(d, Renamed))
	assert.Equal(t, []string{"Hub.Idle"}, entryNames(d, Changed))
}

func TestDiffRenameWithKindChange(t *testing.T) {
	r := seeded(t)
	renameAll(r, "Frame.Timing", "Frame.Clock")
	b, err := os.ReadFile(r.path("a.spec.md"))
	require.NoError(t, err)
	r.write("a.spec.md", strings.Replace(string(b), "**$Frame.Clock (timing).**", "**Def $Frame.Clock (timing).**", 1))
	d := r.diffWork(Source{})
	require.Len(t, d.Entries, 1)
	e := d.Entries[0]
	assert.Equal(t, Renamed, e.Status)
	assert.True(t, e.KindChanged)
	lines := d.Required().Lines()
	require.Len(t, lines, 2)
	assert.Equal(t, "Spec-Rename: $Frame.Timing -> $Frame.Clock", lines[0])
	assert.Equal(t, "Spec-Change: $Frame.Clock inv:"+e.OldShort+" -> def:"+e.NewShort, lines[1])
}

func TestDiffIdenticalBodiesAreNotFalseRename(t *testing.T) {
	r := newRepo(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", "**$Old.One.** Same body text.\n")
	r.commitAll("initial")
	// One removed, two added with the same shape: not unique, so no rename.
	r.write("a.spec.md", "**$New.One.** Same body text.\n\n**$New.Two.** Same body text.\n")
	d := r.diffWork(Source{})
	assert.Empty(t, d.Renames)
	assert.Equal(t, []string{"Old.One"}, entryNames(d, Removed))
	assert.ElementsMatch(t, []string{"New.One", "New.Two"}, entryNames(d, Added))

	// Two removed, two added, all four identical: still no rename.
	r.commitAll("second")
	r.write("a.spec.md", "**$A.One.** Same body text.\n\n**$A.Two.** Same body text.\n")
	r.commitAll("third")
	r.write("a.spec.md", "**$B.One.** Same body text.\n\n**$B.Two.** Same body text.\n")
	d = r.diffWork(Source{})
	assert.Empty(t, d.Renames)
	assert.Len(t, entryNames(d, Removed), 2)
	assert.Len(t, entryNames(d, Added), 2)
}

func TestDiffSwapOfTwoDistinctBodiesIsTwoRenames(t *testing.T) {
	r := newRepo(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", "**$Old.One.** First body.\n\n**$Old.Two.** Second, different body.\n")
	r.commitAll("initial")
	r.write("a.spec.md", "**$New.One.** First body.\n\n**$New.Two.** Second, different body.\n")
	d := r.diffWork(Source{})
	assert.Equal(t, map[string]string{"Old.One": "New.One", "Old.Two": "New.Two"}, d.Renames)
	assert.Len(t, entryNames(d, Renamed), 2)
}

func TestDiffMovedBetweenFilesIsUnchanged(t *testing.T) {
	r := seeded(t)
	// Move Hub.Idle from b.spec.md into a.spec.md.
	r.write("b.spec.md", "# B\n")
	r.write("a.spec.md", baseA+"\n"+strings.TrimPrefix(baseB, "# B\n\n"))
	d := r.diffWork(Source{})
	assert.Empty(t, d.Entries)
	assert.True(t, d.SourcesDiffer)
	assert.Equal(t, StateProseOnly, d.State())
	assert.True(t, d.Required().Empty())
}

func TestDiffDeletedAndRenamedSpecFile(t *testing.T) {
	r := seeded(t)
	r.git("mv", "b.spec.md", "c.spec.md")
	d := r.diffWork(Source{})
	assert.Empty(t, d.Entries, "a renamed file keeps its anchors")

	r.remove("c.spec.md")
	d = r.diffWork(Source{})
	assert.Equal(t, []string{"Hub.Idle"}, entryNames(d, Removed))
	assert.Equal(t, StateAnchorsChanged, d.State())
}

func TestDiffUntrackedNewSpecFile(t *testing.T) {
	r := seeded(t)
	r.write("new.spec.md", "**$Fresh.One.** Brand new file.\n")
	d := r.diffWork(Source{})
	assert.Equal(t, []string{"Fresh.One"}, entryNames(d, Added))
}

func TestDiffUnbornEverythingAdded(t *testing.T) {
	r := newRepo(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", baseA)
	d := r.diffWork(Source{})
	assert.Len(t, entryNames(d, Added), 2)
	assert.Equal(t, StateAnchorsChanged, d.State())
}

func TestDiffAgainstOtherRef(t *testing.T) {
	r := seeded(t)
	r.write("a.spec.md", baseA+"\n**$Second.Commit.** Added in the second commit.\n")
	r.commitAll("second")
	d := r.diffWork(Source{Ref: "HEAD~1"})
	assert.Equal(t, []string{"Second.Commit"}, entryNames(d, Added))
	assert.Empty(t, r.diffWork(Source{}).Entries)
}

func TestDiffDuplicateNamesSkipped(t *testing.T) {
	r := seeded(t)
	r.write("a.spec.md", baseA+"\n**$Frame.Timing.** A second definition.\n")
	d := r.diffWork(Source{})
	assert.Equal(t, []Skipped{{Name: "Frame.Timing", Side: "new"}}, d.Skipped)
	require.Len(t, d.Diagnostics, 1)
	assert.Equal(t, diag.CodeDefDuplicate, d.Diagnostics[0].Code)
	assert.Equal(t, diag.Error, d.Diagnostics[0].Severity)
	assert.Empty(t, entryNames(d, Changed))
	assert.Empty(t, entryNames(d, Removed), "a skipped name is not 'removed' either")
	assert.True(t, d.Required().Empty())
}

func TestDiffDuplicateOnBaselineSide(t *testing.T) {
	r := newRepo(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", "**$Dup.Name.** one\n\n**$Dup.Name.** two\n")
	r.commitAll("initial")
	r.write("a.spec.md", "**$Dup.Name.** fixed\n")
	d := r.diffWork(Source{})
	assert.Equal(t, []Skipped{{Name: "Dup.Name", Side: "old"}}, d.Skipped)
	assert.Contains(t, d.Diagnostics[0].Message, "baseline")
	assert.Empty(t, d.Entries)
}

func TestDiffInSubdirectoryProject(t *testing.T) {
	r := newRepo(t)
	r.proj = "svc"
	r.write("svc/spec.toml", "")
	r.write("svc/a.spec.md", baseA)
	r.write("elsewhere.spec.md", "**$Outside.Proj.** outside.\n")
	r.commitAll("initial")
	r.write("svc/a.spec.md", strings.Replace(baseA, "count of ticks", "count of ticks!", 1))
	d := r.diffWork(Source{})
	assert.Equal(t, []string{"Time.Media"}, entryNames(d, Changed))
	assert.Len(t, d.Entries, 1)
}

func TestDiffStagedVersusWorkingTree(t *testing.T) {
	r := seeded(t)
	r.write("a.spec.md", baseA+"\n**$Staged.One.** Staged.\n")
	r.git("add", "a.spec.md")
	r.write("a.spec.md", baseA+"\n**$Staged.One.** Staged.\n\n**$Later.One.** Only in the work tree.\n")

	// diff --staged: the working tree against the index.
	d := r.diffWork(Source{Index: true})
	assert.Equal(t, []string{"Later.One"}, entryNames(d, Added))
	// check: the index against HEAD.
	d = r.diffStaged()
	assert.Equal(t, []string{"Staged.One"}, entryNames(d, Added))
}

func TestDiffUnavailable(t *testing.T) {
	requireGit(t)
	r := plainDir(t)
	r.write("spec.toml", "")
	r.write("a.spec.md", baseA)
	b := r.load(Source{})
	assert.Equal(t, StateUnavailable, b.State)
	// Compare with a nil old side treats everything as added.
	l, err := r.project().Load(nil)
	require.NoError(t, err)
	d := Compare(nil, l.Spec)
	assert.Len(t, entryNames(d, Added), 2)
}

// TestCheckFlow follows criterion 7: check passes with the trailers status
// printed and fails, listing exactly the missing lines, without them.
func TestCheckFlow(t *testing.T) {
	r := seeded(t)
	renameAll(r, "Frame.Timing", "Frame.Clock")
	b, err := os.ReadFile(r.path("a.spec.md"))
	require.NoError(t, err)
	r.write("a.spec.md", strings.Replace(string(b), "Media time is a count of ticks.", "Media time counts 90 kHz ticks.", 1)+"\n**$Added.One.** New.\n")
	r.write("b.spec.md", strings.Replace(baseB, "Uses $Frame.Timing to stop. Checked by: test.", "Gone.", 1)+"\n")
	r.git("add", "-A")

	d := r.diffStaged()
	req := d.Required()
	lines := req.Lines()
	require.NotEmpty(t, lines)

	// Without trailers: everything is missing.
	res := Check(req, "Update the spec\n")
	assert.False(t, res.OK())
	assert.Equal(t, lines, res.MissingLines())

	// With all trailers: passes, whatever else is in the message (comments, extras).
	msg := "Update the spec\n\n# Please enter the commit message\n" + strings.Join(lines, "\n") + "\nSigned-off-by: x\n"
	assert.True(t, Check(req, msg).OK())

	// Drop one: exactly that line is missing.
	for i := range lines {
		rest := append(append([]string{}, lines[:i]...), lines[i+1:]...)
		res := Check(req, "msg\n\n"+strings.Join(rest, "\n")+"\n")
		assert.Equal(t, []string{lines[i]}, res.MissingLines(), "dropping %q", lines[i])
	}

	// Committing the change then makes the diff clean.
	r.git("commit", "-q", "-m", "Update the spec\n\n"+strings.Join(lines, "\n"))
	assert.Empty(t, r.diffStaged().Entries)
	assert.Empty(t, r.diffWork(Source{}).Entries)
}

func TestLoadContextCancelled(t *testing.T) {
	r := seeded(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b, err := r.loader().Load(ctx, r.project(), Source{})
	// A cancelled context must not look like a successful baseline.
	if err == nil {
		assert.NotEqual(t, StateOK, b.State)
	}
}

func TestExplicitFilesFilterAppliesToBothSides(t *testing.T) {
	r := newRepo(t)
	r.write("spec.toml", "files = [\"docs/*.md\"]\n")
	r.write("docs/a.md", "**$Doc.One.** in docs.\n")
	r.write("x.spec.md", "**$Not.Selected.** ignored.\n")
	r.commitAll("initial")
	r.write("x.spec.md", "**$Not.Selected.** changed but ignored.\n")
	r.write("docs/a.md", "**$Doc.One.** in docs, changed.\n")
	d := r.diffWork(Source{})
	assert.Equal(t, []string{"Doc.One"}, entryNames(d, Changed))
	assert.Len(t, d.Entries, 1)
}

func TestCompareSelfIsClean(t *testing.T) {
	for _, dir := range []string{"../../testdata/multi"} {
		l, err := projectLoad(dir)
		require.NoError(t, err)
		d := Compare(l, l)
		assert.Empty(t, d.Entries, dir)
		assert.Equal(t, StateClean, d.State())
	}
}

func projectLoad(dir string) (*mdspec.Spec, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*mdspec.File
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".spec.md") {
			b, err := os.ReadFile(dir + "/" + e.Name())
			if err != nil {
				return nil, err
			}
			files = append(files, mdspec.ParseFile(e.Name(), b))
		}
	}
	return mdspec.Build(files), nil
}

// TestRenameAcrossMultiFixture is criterion 6: after a rename across files,
// lint is clean (checked in package edit), git changes only the expected
// tokens, and the diff reports exactly one renamed and zero changed.
func TestRenameAcrossMultiFixture(t *testing.T) {
	r := newRepo(t)
	names := []string{"spec.toml", "rws.spec.md", "index.spec.md", "hls.spec.md"}
	orig := map[string]string{}
	for _, n := range names {
		b, err := os.ReadFile("../../testdata/multi/" + n)
		require.NoError(t, err)
		orig[n] = string(b)
		r.write(n, string(b))
	}
	r.commitAll("initial")

	l, err := r.project().Load(nil)
	require.NoError(t, err)
	res, err := edit.PlanRename(r.root(), l.Spec, "Rws.Session", "Rws.Connection")
	require.NoError(t, err)
	require.NoError(t, res.Plan.Commit())

	for _, n := range names[1:] {
		b, err := os.ReadFile(r.path(n))
		require.NoError(t, err)
		assert.Equal(t, strings.ReplaceAll(orig[n], "$Rws.Session", "$Rws.Connection"), string(b), n)
	}
	d := r.diffWork(Source{})
	require.Len(t, d.Entries, 1, "%+v", d.Entries)
	assert.Equal(t, Renamed, d.Entries[0].Status)
	assert.Empty(t, entryNames(d, Changed))
	assert.Equal(t, []string{"Spec-Rename: $Rws.Session -> $Rws.Connection"}, d.Required().Lines())
}
