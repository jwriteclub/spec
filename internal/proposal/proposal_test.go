// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package proposal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
)

const doc = `# demo

## 1. Rules

**$A.First (first).** The first rule holds for 30 s.

**$A.Second (second).** The second rule follows $A.First.

**Def $A.Term (term).** A term.

## 2. More

Prose about $A.Second.
`

func specOf(files map[string]string) *mdspec.Spec {
	var fs []*mdspec.File
	for p, src := range files {
		fs = append(fs, mdspec.ParseFile(p, []byte(src)))
	}
	return mdspec.Build(fs)
}

func TestValidID(t *testing.T) {
	for _, ok := range []string{"abcd", "k3x7a", "zz2277"} {
		assert.True(t, ValidID(ok), ok)
	}
	for _, bad := range []string{"", "abc", "abcdefg", "ABCD", "ab1d", "ab.d", "../x", "k3x9a"} {
		assert.False(t, ValidID(bad), bad)
	}
	for i := 0; i < 50; i++ {
		id, err := newID(nil)
		require.NoError(t, err)
		assert.True(t, ValidID(id), id)
	}
}

func TestStore(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }

	ps, err := st.List()
	require.NoError(t, err)
	assert.Empty(t, ps)
	_, err = os.Stat(filepath.Join(root, ".spec"))
	assert.True(t, os.IsNotExist(err), "listing creates nothing")

	p := &Proposal{Kind: KindAnalysis, Author: AuthorHuman}
	require.NoError(t, st.Create(p))
	assert.True(t, ValidID(p.ID))
	assert.Equal(t, StatusPending, p.Status)
	assert.Equal(t, now, p.Created)

	raw, err := os.ReadFile(st.Path(p.ID))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, []any{}, m["hunks"], "lists are never null on disk")
	assert.Nil(t, m["primary"])

	got, err := st.Load(p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
	_, err = st.Load("zzzz")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = st.Load("../../etc")
	assert.ErrorIs(t, err, ErrNotFound)

	// A decided proposal is a receipt until it expires.
	q := &Proposal{Kind: KindAnalysis}
	require.NoError(t, st.Create(q))
	q.Status = StatusApplied
	q.Decision = &Decision{By: "human", At: now}
	require.NoError(t, st.Save(q))
	ps, err = st.List()
	require.NoError(t, err)
	assert.Len(t, ps, 2)

	now = now.Add(ReceiptTTL + time.Minute)
	ps, err = st.List()
	require.NoError(t, err)
	require.Len(t, ps, 1, "the expired receipt is removed")
	assert.Equal(t, p.ID, ps[0].ID)
	_, err = st.Load(q.ID)
	assert.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, st.Remove(p.ID))
	assert.ErrorIs(t, st.Remove(p.ID), ErrNotFound)

	// No temp files are left behind.
	ents, err := os.ReadDir(filepath.Join(root, Dir))
	require.NoError(t, err)
	assert.Empty(t, ents)
}

func TestWait(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	p := &Proposal{Kind: KindAnalysis}
	require.NoError(t, st.Create(p))

	// Times out while pending.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	got, err := st.Wait(ctx, []string{p.ID}, 10*time.Millisecond)
	cancel()
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	require.Len(t, got, 1)
	assert.Equal(t, StatusPending, got[0].Status)

	// Wakes when another writer decides it (poll set long, so fsnotify
	// must deliver it).
	done := make(chan []*Proposal, 1)
	go func() {
		ps, _ := st.Wait(context.Background(), []string{p.ID}, time.Hour)
		done <- ps
	}()
	time.Sleep(50 * time.Millisecond)
	p.Status = StatusRejected
	p.Decision = &Decision{Reason: "no", By: "human", At: time.Now()}
	require.NoError(t, st.Save(p))
	select {
	case ps := <-done:
		require.Len(t, ps, 1)
		assert.Equal(t, "no", ps[0].Decision.Reason)
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not wake on the decision")
	}

	// A vanished proposal ends the wait.
	require.NoError(t, st.Remove(p.ID))
	_, err = st.Wait(context.Background(), []string{p.ID}, 10*time.Millisecond)
	assert.ErrorIs(t, err, ErrNotFound)
}

func apply(t *testing.T, spec *mdspec.Spec, p *Proposal, accept ...int) (map[string]string, *Prepared) {
	t.Helper()
	set := map[int]bool{}
	for _, n := range accept {
		set[n] = true
	}
	prep, err := p.Prepare(spec, set)
	require.NoError(t, err)
	out := map[string]string{}
	for _, fe := range prep.Edits {
		after, err := edit.Apply(fe.Snapshot, fe.Splices)
		require.NoError(t, err)
		out[fe.Path] = string(after)
	}
	return out, prep
}

func TestPrepareChangeAndHunks(t *testing.T) {
	spec := specOf(map[string]string{"a.spec.md": doc})
	a := spec.Anchor("A.First")
	old := spec.File("a.spec.md").Text(a.BodyRange)
	p := &Proposal{
		Primary: &Primary{N: 1, Action: specctx.ActionChange, Name: "$A.First", File: "a.spec.md", OldText: old, NewText: strings.Replace(old, "30 s", "60 s", 1)},
		Hunks: []finding.Hunk{
			{N: 2, File: "a.spec.md", Find: "Prose about $A.Second.", Replace: "Prose about $A.Second, kept."},
			{N: 3, File: "a.spec.md", Find: "missing text", Replace: "x"},
			{N: 4, File: "a.spec.md", Find: "holds for 30 s", Replace: "holds"},
		},
	}
	out, prep := apply(t, spec, p, 1, 2, 3, 4)
	assert.Equal(t, []int{1, 2}, prep.Applied)
	require.Len(t, prep.Failed, 2)
	assert.Equal(t, 3, prep.Failed[0].N)
	assert.Contains(t, prep.Failed[0].Reason, "occurs 0 times")
	assert.Equal(t, 4, prep.Failed[1].N)
	assert.Contains(t, prep.Failed[1].Reason, "overlaps")
	want := strings.Replace(strings.Replace(doc, "for 30 s", "for 60 s", 1), "Prose about $A.Second.", "Prose about $A.Second, kept.", 1)
	assert.Equal(t, want, out["a.spec.md"])

	// Only hunk 2.
	out, prep = apply(t, spec, p, 2)
	assert.Equal(t, []int{2}, prep.Applied)
	assert.NotContains(t, out["a.spec.md"], "60 s")

	// Stale: the anchor changed since.
	edited := specOf(map[string]string{"a.spec.md": strings.Replace(doc, "for 30 s", "for 45 s", 1)})
	_, err := p.Prepare(edited, map[int]bool{1: true})
	var stale *StaleError
	require.ErrorAs(t, err, &stale)
	assert.Contains(t, stale.Error(), "changed since")

	// --keep: the change is already there, nothing to write for hunk 1.
	p.Primary.InWorktree = true
	_, prep = apply(t, spec, p, 1)
	assert.Equal(t, []int{1}, prep.Applied)
	assert.Empty(t, prep.Edits)
}

func TestRemoveAndAddRoundTrip(t *testing.T) {
	for name, src := range map[string]string{
		"lf":   doc,
		"crlf": strings.ReplaceAll(doc, "\n", "\r\n"),
	} {
		t.Run(name, func(t *testing.T) {
			for _, anchor := range []string{"A.First", "A.Second", "A.Term"} {
				spec := specOf(map[string]string{"a.spec.md": src})
				f := spec.File("a.spec.md")
				a := spec.Anchor(anchor)
				text := f.Text(a.BodyRange)
				pl := PlacementOf(f, a)

				removed, err := edit.Apply(f.Source(), []edit.Splice{RemovalSplice(f.Source(), a.BodyRange)})
				require.NoError(t, err)
				assert.NotContains(t, string(removed), "$"+anchor+" (")

				back := mdspec.ParseFile("a.spec.md", removed)
				sp, err := InsertSplice(back, pl, text)
				require.NoError(t, err)
				again, err := edit.Apply(removed, []edit.Splice{sp})
				require.NoError(t, err)
				assert.Equal(t, src, string(again), "remove then insert %s is byte-exact", anchor)
			}
		})
	}

	// At the start of a file, with and without a byte order mark (which
	// stays first).
	for _, bom := range []string{"", "\xef\xbb\xbf"} {
		src := bom + "**$X.One (one).** First.\n\n**$X.Two (two).** Second.\n"
		f := mdspec.ParseFile("x.spec.md", []byte(src))
		a := f.Anchors[0]
		pl := PlacementOf(f, a)
		assert.True(t, pl.AtStart)
		removed, err := edit.Apply([]byte(src), []edit.Splice{RemovalSplice([]byte(src), a.BodyRange)})
		require.NoError(t, err)
		assert.Equal(t, bom+"**$X.Two (two).** Second.\n", string(removed))
		sp, err := InsertSplice(mdspec.ParseFile("x.spec.md", removed), pl, f.Text(a.BodyRange))
		require.NoError(t, err)
		again, err := edit.Apply(removed, []edit.Splice{sp})
		require.NoError(t, err)
		assert.Equal(t, src, string(again), "bom=%v", bom != "")
	}
}

// A run of adjacent anchors: each placement names its whole run, and
// removing every anchor of the run, then inserting them back in any order,
// is byte-exact.
func TestPlacementOfRun(t *testing.T) {
	spec := specOf(map[string]string{"a.spec.md": doc})
	f := spec.File("a.spec.md")
	pl := PlacementOf(f, spec.Anchor("A.Term"))
	assert.Equal(t, "$A.Second", pl.After)
	assert.Equal(t, []string{"$A.First"}, pl.Earlier)
	assert.Equal(t, "## 1. Rules", pl.AfterLine, "the line before the run")
	pl = PlacementOf(f, spec.Anchor("A.First"))
	assert.Equal(t, "", pl.After)
	assert.Empty(t, pl.Earlier)

	names := []string{"A.First", "A.Second", "A.Term"}
	texts := map[string]string{}
	pls := map[string]Placement{}
	var removals []edit.Splice
	for _, n := range names {
		a := spec.Anchor(n)
		texts[n], pls[n] = f.Text(a.BodyRange), PlacementOf(f, a)
		removals = append(removals, RemovalSplice(f.Source(), a.BodyRange))
	}
	empty, err := edit.Apply(f.Source(), removals)
	require.NoError(t, err)
	for _, order := range [][]string{{"A.First", "A.Second", "A.Term"}, {"A.Term", "A.Second", "A.First"}, {"A.Second", "A.Term", "A.First"}} {
		cur := empty
		for _, n := range order {
			sp, err := InsertSplice(mdspec.ParseFile("a.spec.md", cur), pls[n], texts[n])
			require.NoError(t, err)
			cur, err = edit.Apply(cur, []edit.Splice{sp})
			require.NoError(t, err)
		}
		assert.Equal(t, doc, string(cur), "order %v", order)
	}
}

func TestPrepareAddAndRemove(t *testing.T) {
	spec := specOf(map[string]string{"a.spec.md": doc})
	add := &Proposal{Primary: &Primary{
		N: 1, Action: specctx.ActionAdd, Name: "$A.Third", File: "a.spec.md",
		NewText:   "**$A.Third (third).** Third.",
		Placement: &Placement{File: "a.spec.md", After: "$A.Second", Sep: "\n\n"},
	}}
	out, _ := apply(t, spec, add, 1)
	assert.Contains(t, out["a.spec.md"], "follows $A.First.\n\n**$A.Third (third).** Third.\n\n**Def $A.Term")

	// By section, when the anchor named in After is gone.
	add.Primary.Placement = &Placement{File: "a.spec.md", After: "$A.Gone", Section: "2", Sep: "\n\n"}
	out, _ = apply(t, spec, add, 1)
	assert.True(t, strings.HasSuffix(out["a.spec.md"], "Prose about $A.Second.\n\n**$A.Third (third).** Third.\n"), out["a.spec.md"])

	// Nowhere to go: stale.
	add.Primary.Placement = &Placement{File: "a.spec.md", After: "$A.Gone", Sep: "\n\n"}
	_, err := add.Prepare(spec, map[int]bool{1: true})
	var stale *StaleError
	require.ErrorAs(t, err, &stale)
	assert.Contains(t, stale.Error(), "cannot find where it goes")

	// Already defined: stale.
	add.Primary.Name = "$A.First"
	_, err = add.Prepare(spec, map[int]bool{1: true})
	require.ErrorAs(t, err, &stale)
	assert.Contains(t, stale.Error(), "already defined")

	a := spec.Anchor("A.Term")
	rm := &Proposal{Primary: &Primary{N: 1, Action: specctx.ActionRemove, Name: "$A.Term", File: "a.spec.md", OldText: spec.File("a.spec.md").Text(a.BodyRange)}}
	out, _ = apply(t, spec, rm, 1)
	assert.Equal(t, strings.Replace(doc, "\n\n**Def $A.Term (term).** A term.", "", 1), out["a.spec.md"])
}

func TestKindToggle(t *testing.T) {
	spec := specOf(map[string]string{"a.spec.md": doc + "\n## Def $A.Head: heading form\n\nBody.\n"})
	f := spec.File("a.spec.md")
	text, err := KindToggle(f, spec.Anchor("A.First"), mdspec.KindDefinition)
	require.NoError(t, err)
	assert.Equal(t, "**Def $A.First (first).** The first rule holds for 30 s.", text)
	text, err = KindToggle(f, spec.Anchor("A.Term"), mdspec.KindInvariant)
	require.NoError(t, err)
	assert.Equal(t, "**$A.Term (term).** A term.", text)
	text, err = KindToggle(f, spec.Anchor("A.Head"), mdspec.KindInvariant)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(text, "## $A.Head: heading form"), text)
	same, err := KindToggle(f, spec.Anchor("A.Term"), mdspec.KindDefinition)
	require.NoError(t, err)
	assert.Equal(t, f.Text(spec.Anchor("A.Term").BodyRange), same)
}

// An acknowledged receipt with uncommitted applied hunks is gone as a
// receipt but stays evidence until ReceiptTTL, and is then removed.
func TestAcknowledgeAndApplied(t *testing.T) {
	s := NewStore(t.TempDir())
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	decided := func(status string, d *Decision) *Proposal {
		p := &Proposal{Kind: KindChange, Status: status, Decision: d}
		require.NoError(t, s.Create(p))
		return p
	}
	kept := decided(StatusApplied, &Decision{Applied: []int{1}, At: now})
	committed := decided(StatusApplied, &Decision{Applied: []int{1}, Commit: "abc", At: now})
	rejected := decided(StatusRejected, &Decision{Reason: "no", At: now})
	receipt := decided(StatusApplied, &Decision{Applied: []int{1}, At: now.Add(time.Second)})
	for _, p := range []*Proposal{kept, committed, rejected} {
		require.NoError(t, s.Acknowledge(p))
		_, err := s.Load(p.ID)
		assert.ErrorIs(t, err, ErrNotFound, "no longer a receipt")
	}
	assert.ErrorIs(t, s.Acknowledge(kept), ErrNotFound, "already acknowledged")

	ids := func() []string {
		ps, err := s.Applied()
		require.NoError(t, err)
		out := []string{}
		for _, p := range ps {
			out = append(out, p.ID)
		}
		return out
	}
	assert.Equal(t, []string{kept.ID, receipt.ID}, ids(), "oldest decision first; committed and rejected ones leave nothing")
	listed, err := s.List()
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, receipt.ID, listed[0].ID)

	ev, err := s.Evidence()
	require.NoError(t, err)
	evIDs := []string{}
	for _, p := range ev {
		evIDs = append(evIDs, p.ID)
	}
	assert.Equal(t, []string{receipt.ID, kept.ID}, evIDs, "the list, then the applied acknowledged receipts")

	now = now.Add(ReceiptTTL + 2*time.Second)
	assert.Empty(t, ids())
	assert.NoFileExists(t, filepath.Join(s.dir, ackedDir, kept.ID+".json"))
}

// An acked directory that cannot be read costs only what it holds: the
// receipts List found are still evidence of applied text.
func TestAppliedKeepsReceiptsWhenAckedIsUnreadable(t *testing.T) {
	s := NewStore(t.TempDir())
	p := &Proposal{Kind: KindChange, Status: StatusApplied, Decision: &Decision{Applied: []int{1}, At: s.Now()}}
	require.NoError(t, s.Create(p))
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, ackedDir), []byte("not a directory"), 0o644))

	ps, err := s.Applied()
	require.NoError(t, err)
	require.Len(t, ps, 1)
	assert.Equal(t, p.ID, ps[0].ID)
}
