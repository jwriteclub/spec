// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/proposal"
)

// StatusEdited is a ChangedAnchor's status when its text differs from HEAD
// but its hash does not (whitespace outside code): it needs no trailer.
const StatusEdited = "edited"

// ChangedAnchor is an anchor a Change edits.
type ChangedAnchor struct {
	// Name carries the $ sigil.
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Status is the anchor's difference from HEAD: a baseline status
	// (added, removed, changed, kind_changed, renamed, retitled) or
	// StatusEdited.
	Status string `json:"status"`
	// OldName is the name at HEAD of a renamed anchor, with the $ sigil.
	OldName string `json:"old_name,omitempty"`
}

// Change is one part of the working tree's difference from HEAD that can
// be committed on its own. The edits of one anchor always travel together
// (across files when the anchor moved, and with the anchor it was renamed
// from), so committing a change never leaves an anchor half-changed; a
// file's edits outside every anchor form one prose change per file.
type Change struct {
	// ID names the change for CommitChanges: the anchors' names joined by
	// commas, or "prose:FILE".
	ID string `json:"id"`
	// Anchors are the anchors the change edits, in order of appearance;
	// empty for prose.
	Anchors []ChangedAnchor `json:"anchors"`
	// Files are the files it edits, in project order.
	Files []string `json:"files"`
	// File and Line are where it starts in the working tree (1-based).
	File string `json:"file"`
	Line int    `json:"line"`
	// Added and Removed count the lines it adds and removes.
	Added   int `json:"added"`
	Removed int `json:"removed"`
	// Kind is "" for a change of spec files, else KindModel or KindSettings:
	// a model or spec.toml, which a commit carries whole (OtherChanges).
	Kind string `json:"kind,omitempty"`

	// edits are the line edits per file, in HEAD coordinates; ranges the
	// same edits in the working tree (byte ranges, empty for a deletion).
	edits  map[string][]udiff.Edit
	ranges map[string][][2]int
	// whole is a model's or spec.toml's working-tree text; nil when the
	// file was deleted. head is its text at HEAD ("" when absent).
	whole *string
	head  string
}

// Kinds of the changes OtherChanges lists.
const (
	KindModel    = "model"
	KindSettings = "settings"
)

// Prose reports whether the change edits spec prose only: no anchor, and
// no model or settings file.
func (c Change) Prose() bool { return len(c.Anchors) == 0 && c.Kind == "" }

// Whole reports whether the change is a model or spec.toml, committed whole.
func (c Change) Whole() bool { return c.Kind != "" }

// WholeLines describes a whole-file change line by line ("added: TEXT",
// "removed: TEXT"), at most limit lines and then how many more.
func (c Change) WholeLines(limit int) []string {
	var out []string
	more := 0
	note := func(verb, text string) {
		for _, l := range strings.SplitAfter(text, "\n") {
			if l = strings.TrimRight(l, "\n"); strings.TrimSpace(l) == "" {
				continue
			}
			if len(out) < limit {
				out = append(out, verb+": "+l)
			} else {
				more++
			}
		}
	}
	for _, e := range c.edits[c.File] {
		note("removed", c.head[e.Start:e.End])
		note("added", e.New)
	}
	if more > 0 {
		out = append(out, fmt.Sprintf("and %d more changed lines", more))
	}
	return out
}

// Ranges returns the byte ranges the change covers in file's working-tree
// text (empty ranges for deleted lines).
func (c Change) Ranges(file string) [][2]int { return c.ranges[file] }

// Label names the change for a list: its anchors, "prose in FILE", or
// "model FILE" and "settings spec.toml".
func (c Change) Label() string {
	if c.Whole() {
		return c.Kind + " " + c.File
	}
	if c.Prose() {
		return "prose in " + c.File
	}
	names := make([]string, len(c.Anchors))
	for i, a := range c.Anchors {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}

// anchorSpan is an anchor's whole lines: label through body.
func anchorSpan(src []byte, a *mdspec.Anchor) [2]int {
	s := a.LabelRange.Start
	for s > 0 && src[s-1] != '\n' {
		s--
	}
	e := a.BodyRange.End
	for e < len(src) && src[e] != '\n' {
		e++
	}
	if e < len(src) {
		e++
	}
	return [2]int{s, e}
}

// spanHits reports whether the line range r (empty for a point between
// lines) touches the span: overlaps it, or for a point lies strictly
// inside it.
func spanHits(span, r [2]int) bool {
	if r[0] == r[1] {
		return span[0] < r[0] && r[0] < span[1]
	}
	return r[0] < span[1] && span[0] < r[1]
}

// lineEdit is one line edit of a file with its place on both sides.
type lineEdit struct {
	file  string
	e     udiff.Edit
	cur   [2]int
	names []string
}

// Changes splits the difference between head (the HEAD baseline's spec;
// an empty spec when there are no commits) and cur (the working tree)
// into changes, in project order. Only spec files are considered.
func Changes(head, cur *mdspec.Spec) []Change {
	if head == nil {
		head = mdspec.Build(nil)
	}
	diff := baseline.Compare(head, cur)
	status := map[string]string{}
	kinds := map[string]string{}
	alias := map[string]string{}   // old name of a rename -> new name
	oldName := map[string]string{} // new name of a rename -> old name
	for _, e := range diff.Entries {
		status[e.Name] = string(e.Status)
		if e.NewKind != "" {
			kinds[e.Name] = string(e.NewKind)
		} else {
			kinds[e.Name] = string(e.OldKind)
		}
		if e.Status == baseline.Renamed && e.OldName != "" {
			alias[e.OldName] = e.Name
			oldName[e.Name] = e.OldName
		}
	}

	var paths []string
	seen := map[string]bool{}
	for _, f := range cur.Files {
		if !seen[f.Path] {
			seen[f.Path] = true
			paths = append(paths, f.Path)
		}
	}
	for _, f := range head.Files {
		if !seen[f.Path] {
			seen[f.Path] = true
			paths = append(paths, f.Path)
		}
	}

	var all []*lineEdit
	for _, path := range paths {
		var old, now []byte
		var oldAnchors, newAnchors []*mdspec.Anchor
		if f := head.File(path); f != nil {
			old, oldAnchors = f.Source(), f.Anchors
		}
		if f := cur.File(path); f != nil {
			now, newAnchors = f.Source(), f.Anchors
		}
		if string(old) == string(now) {
			continue
		}
		delta := 0
		for _, e := range udiff.Lines(string(old), string(now)) {
			le := &lineEdit{file: path, e: e}
			le.cur = [2]int{e.Start + delta, e.Start + delta + len(e.New)}
			delta += len(e.New) - (e.End - e.Start)
			names := map[string]bool{}
			for _, a := range newAnchors {
				if spanHits(anchorSpan(now, a), le.cur) {
					names[a.Name] = true
				}
			}
			for _, a := range oldAnchors {
				if spanHits(anchorSpan(old, a), [2]int{e.Start, e.End}) {
					n := a.Name
					if to, ok := alias[n]; ok {
						n = to
					}
					names[n] = true
				}
			}
			for n := range names {
				le.names = append(le.names, n)
			}
			sort.Strings(le.names)
			all = append(all, le)
		}
	}

	// Union the edits that share an anchor; a file's anchorless edits form
	// one group.
	parent := make([]int, len(all))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	first := map[string]int{}
	join := func(key string, i int) {
		if j, ok := first[key]; ok {
			parent[find(i)] = find(j)
			return
		}
		first[key] = i
	}
	for i, le := range all {
		if len(le.names) == 0 {
			join("\x00prose\x00"+le.file, i)
			continue
		}
		for _, n := range le.names {
			join(n, i)
		}
	}

	groups := map[int]*Change{}
	var order []int
	for i, le := range all {
		root := find(i)
		c, ok := groups[root]
		if !ok {
			c = &Change{edits: map[string][]udiff.Edit{}, ranges: map[string][][2]int{}}
			groups[root] = c
			order = append(order, root)
		}
		if !slices.Contains(c.Files, le.file) {
			c.Files = append(c.Files, le.file)
		}
		c.edits[le.file] = append(c.edits[le.file], le.e)
		c.ranges[le.file] = append(c.ranges[le.file], le.cur)
		c.Removed += strings.Count(headText(head, le.file)[le.e.Start:le.e.End], "\n")
		c.Added += strings.Count(le.e.New, "\n")
		for _, n := range le.names {
			if !slices.ContainsFunc(c.Anchors, func(a ChangedAnchor) bool { return a.Name == "$"+n }) {
				st := status[n]
				if st == "" {
					st = StatusEdited
				}
				kind := kinds[n]
				if kind == "" {
					if as := cur.Lookup(n); len(as) > 0 {
						kind = string(as[0].Kind)
					}
				}
				ca := ChangedAnchor{Name: "$" + n, Kind: kind, Status: st}
				if o := oldName[n]; o != "" && o != n {
					ca.OldName = "$" + o
				}
				c.Anchors = append(c.Anchors, ca)
			}
		}
		if c.File == "" {
			c.File = le.file
			if f := cur.File(le.file); f != nil {
				c.Line = f.Index().Line(min(le.cur[0], len(f.Source())))
			} else {
				c.Line = 1
			}
		}
	}
	out := make([]Change, 0, len(order))
	for _, root := range order {
		c := groups[root]
		if c.Prose() {
			c.ID = "prose:" + c.File
		} else {
			names := make([]string, len(c.Anchors))
			for i, a := range c.Anchors {
				names[i] = a.Name
			}
			c.ID = strings.Join(names, ",")
		}
		out = append(out, *c)
	}
	return out
}

// headText returns path's text in head ("" when absent).
func headText(head *mdspec.Spec, path string) string {
	if f := head.File(path); f != nil {
		return string(f.Source())
	}
	return ""
}

// OtherChanges lists the changes of the project's files beside the spec
// that a commit carries: spec.toml and the models (formal in spec.toml),
// against the baseline b. Each is one change, committed whole: settings
// first, then the models by path. A file is compared as it is on disk.
func OtherChanges(proj *project.Project, b *baseline.Baseline) ([]Change, error) {
	paths := []string{project.ConfigFile}
	seen := map[string]bool{project.ConfigFile: true}
	var models []string
	for _, p := range proj.Formal {
		if !seen[p] {
			seen[p] = true
			models = append(models, p)
		}
	}
	for p := range b.Others {
		if !seen[p] {
			seen[p] = true
			models = append(models, p)
		}
	}
	sort.Strings(models)
	paths = append(paths, models...)
	out := []Change{}
	for _, path := range paths {
		head, inHead := b.Others[path]
		now, err := os.ReadFile(filepath.Join(proj.Root, filepath.FromSlash(path)))
		onDisk := err == nil
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		if inHead == onDisk && bytes.Equal(head, now) || !inHead && !onDisk {
			continue
		}
		c := Change{ID: "file:" + path, Kind: KindModel, Files: []string{path}, File: path, Line: 1, edits: map[string][]udiff.Edit{}, head: string(head)}
		if path == project.ConfigFile {
			c.Kind = KindSettings
		}
		if onDisk {
			text := string(now)
			c.whole = &text
		}
		es := udiff.Lines(string(head), string(now))
		c.edits[path] = es
		for _, e := range es {
			c.Removed += strings.Count(string(head)[e.Start:e.End], "\n")
			c.Added += strings.Count(e.New, "\n")
		}
		out = append(out, c)
	}
	return out, nil
}

// headTextOf returns path's text at the baseline b: a spec file or one of
// its other files ("" when absent).
func headTextOf(b *baseline.Baseline, path string) string {
	if t, ok := b.Others[path]; ok {
		return string(t)
	}
	if b.Spec == nil {
		return ""
	}
	return headText(b.Spec, path)
}

// CommitSnapshot is what a reviewer saw of the files a commit reads: each
// spec file's hash and each other change's text hash ("" for a deletion),
// by path. CommitChangesSeen commits only when it still holds.
func CommitSnapshot(spec *mdspec.Spec, others []Change) map[string]string {
	out := map[string]string{}
	for _, f := range spec.Files {
		out[f.Path] = proposal.FileHash(f.Source())
	}
	for _, c := range others {
		h := ""
		if c.whole != nil {
			h = proposal.FileHash([]byte(*c.whole))
		}
		out[c.File] = h
	}
	return out
}

// Committed is what a commit of changes made.
type Committed struct {
	Commit   string   `json:"commit"`
	Trailers []string `json:"trailers"`
	// Files are the spec files the commit touched.
	Files []string `json:"files"`
	// Warning is set when the commit was made but the index entries of
	// its files could not be updated.
	Warning string `json:"warning,omitempty"`
}

// CommitChanges commits exactly the changes named by ids (Change.ID, as
// Changes listed them for the working tree on disk) with message and the
// trailers the committed content requires. Other edits, of the same files
// too, stay in the working tree, uncommitted and unstaged. It commits
// through a temporary index, so hooks and signing run as for any commit,
// then points the real index entries of the committed files at the
// committed content. A change that no longer exists is exit 1 `changed`:
// nothing is committed.
func (w *Workflow) CommitChanges(proj *project.Project, ids []string, message string) (*Committed, *Error) {
	return w.commitChanges(proj, ids, message, "", nil, false)
}

// CommitChangesSeen commits only the exact saved-file and HEAD snapshot shown
// to the reviewer. The validation runs under the same lock as the commit.
func (w *Workflow) CommitChangesSeen(proj *project.Project, ids []string, message, head string, files map[string]string) (*Committed, *Error) {
	return w.commitChanges(proj, ids, message, head, files, true)
}

func (w *Workflow) commitChanges(proj *project.Project, ids []string, message, head string, files map[string]string, verify bool) (*Committed, *Error) {
	if strings.TrimSpace(message) == "" {
		return nil, usageErr("a commit needs a message")
	}
	if len(ids) == 0 {
		return nil, usageErr("nothing is selected to commit")
	}
	lock, err := project.AcquireLock(proj.Root, "spec commit")
	if err != nil {
		return nil, LockErr(err)
	}
	defer func() { _ = lock.Release() }()

	if e := w.CheckRecovery(proj.Root, ""); e != nil {
		return nil, e
	}
	if e := w.commitPreflight(proj.Root); e != nil {
		return nil, e
	}

	proj, err = project.Open(proj.Root)
	if err != nil {
		return nil, internalErr(err)
	}
	b, e := w.LoadBaseline(proj, baseline.Source{}, false)
	if e != nil {
		return nil, e
	}
	if b.State == baseline.StateUnavailable {
		return nil, fail(ExitFail, "commit_failed", "cannot commit: %s", b.Reason)
	}
	loaded, err := proj.Load(nil)
	if err != nil {
		return nil, internalErr(err)
	}
	others, err := OtherChanges(proj, b)
	if err != nil {
		return nil, internalErr(err)
	}
	if verify && (head != b.Commit || !maps.Equal(files, CommitSnapshot(loaded.Spec, others))) {
		return nil, fail(ExitFail, "changed", "Saved files or HEAD changed since review; refresh the selection before committing")
	}
	all := append(Changes(b.Spec, loaded.Spec), others...)
	var sel []Change
	for _, id := range ids {
		k := slices.IndexFunc(all, func(c Change) bool { return c.ID == id })
		if k < 0 {
			return nil, fail(ExitFail, "changed", "%s no longer differs from HEAD as it did (the files changed since the list was made); nothing was committed", id)
		}
		sel = append(sel, all[k])
	}
	return w.commitSelected(proj, b, loaded.Spec, sel, message, nil)
}

// commitSelected commits the changes sel of the working tree cur against
// the baseline b, with message followed by the trailers.
func (w *Workflow) commitSelected(proj *project.Project, b *baseline.Baseline, cur *mdspec.Spec, sel []Change, message string, intent *RecoveryIntent) (*Committed, *Error) {
	bc, err := buildCommit(proj, b.Spec, cur, sel)
	if err != nil {
		return nil, internalErr(err)
	}
	if len(bc.contents) == 0 && len(bc.removed) == 0 {
		return nil, fail(ExitFail, "commit_failed", "nothing to commit: the selection does not differ from HEAD")
	}
	msg := strings.TrimSpace(message) + "\n"
	if len(bc.trailers) > 0 {
		msg += "\n" + strings.Join(bc.trailers, "\n") + "\n"
	}
	standalone := intent == nil
	if standalone {
		var err error
		intent, err = newRecovery("commit")
		if err != nil {
			return nil, internalErr(err)
		}
		intent.Parent = b.Commit
		for _, c := range sel {
			intent.Changes = append(intent.Changes, c.ID)
		}
		for _, path := range bc.paths {
			intent.Files = append(intent.Files, RecoveryFile{Path: path, Before: proposal.FileHash([]byte(headTextOf(b, path))), After: proposal.FileHash([]byte(bc.contents[path]))})
		}
		if err := saveRecovery(proj.Root, intent); err != nil {
			return nil, internalErr(err)
		}
	}
	hash, warning, e := w.gitCommit(proj.Root, b.Commit, bc.paths, bc.contents, bc.removed, msg, intent)
	if e != nil && intent.refUncertain {
		e.Code = "recovery_required"
		e.Message += "; run `spec recover` to inspect the prepared commit before retrying"
	} else if standalone && !intent.NeedsInspection {
		if err := finishRecovery(proj.Root); err != nil {
			if e == nil && hash != "" {
				warning += "; commit recorded, but interruption evidence cleanup could not be confirmed; inspect `spec recover`: " + err.Error()
			} else {
				return nil, internalErr(err)
			}
		}
	}
	if e != nil {
		e.Trailers = bc.trailers
		return nil, e
	}
	return &Committed{Commit: hash, Trailers: bc.trailers, Files: bc.paths, Warning: warning}, nil
}

// CommitTrailers returns the trailer lines a commit of sel (changes of cur
// against head) carries.
func CommitTrailers(proj *project.Project, head, cur *mdspec.Spec, sel []Change) ([]string, error) {
	bc, err := buildCommit(proj, head, cur, sel)
	if err != nil {
		return nil, err
	}
	return bc.trailers, nil
}

// builtCommit is the content of a commit of some changes.
type builtCommit struct {
	// paths are the files it touches, in project order.
	paths []string
	// contents are the files' committed text; removed the files it deletes.
	contents map[string]string
	removed  map[string]bool
	trailers []string
}

// buildCommit computes what committing sel commits: each touched file is
// HEAD's text with only sel's edits, and the trailers compare HEAD with
// HEAD plus those files.
func buildCommit(proj *project.Project, head, cur *mdspec.Spec, sel []Change) (*builtCommit, error) {
	if head == nil {
		head = mdspec.Build(nil)
	}
	edits := map[string][]udiff.Edit{}
	whole := map[string]*string{}
	for _, c := range sel {
		if c.Whole() {
			whole[c.File] = c.whole
			continue
		}
		for f, es := range c.edits {
			edits[f] = append(edits[f], es...)
		}
	}
	bc := &builtCommit{contents: map[string]string{}, removed: map[string]bool{}}
	for f := range edits {
		bc.paths = append(bc.paths, f)
	}
	for f := range whole {
		if _, ok := edits[f]; !ok {
			bc.paths = append(bc.paths, f)
		}
	}
	order := map[string]int{}
	for i, f := range proj.Files {
		order[f] = i + 1
	}
	sort.Slice(bc.paths, func(i, j int) bool {
		oi, oj := order[bc.paths[i]], order[bc.paths[j]]
		if oi != oj {
			return oi < oj
		}
		return bc.paths[i] < bc.paths[j]
	})
	for _, path := range bc.paths {
		if text, ok := whole[path]; ok {
			if text == nil {
				bc.removed[path] = true
			} else {
				bc.contents[path] = *text
			}
			continue
		}
		es := edits[path]
		sort.Slice(es, func(i, j int) bool { return es[i].Start < es[j].Start })
		text, err := udiff.Apply(headText(head, path), es)
		if err != nil {
			return nil, fmt.Errorf("building the commit of %s: %w", path, err)
		}
		if text == "" && cur.File(path) == nil {
			if head.File(path) != nil {
				bc.removed[path] = true
			}
			continue
		}
		bc.contents[path] = text
	}
	var files []*mdspec.File
	done := map[string]bool{}
	for _, f := range head.Files {
		done[f.Path] = true
		_, built := bc.contents[f.Path]
		switch {
		case bc.removed[f.Path]:
		case built:
			files = append(files, mdspec.ParseFile(f.Path, []byte(bc.contents[f.Path])))
		default:
			files = append(files, f)
		}
	}
	for _, path := range bc.paths {
		if _, isWhole := whole[path]; isWhole {
			continue // not a spec file
		}
		if _, built := bc.contents[path]; built && !done[path] {
			files = append(files, mdspec.ParseFile(path, []byte(bc.contents[path])))
		}
	}
	bc.trailers = baseline.Compare(head, mdspec.Build(files)).Required().Lines()
	return bc, nil
}

// gitCommit commits contents (and the removal of removed) on top of HEAD
// through a temporary index, then resets the real index entries of paths
// to the commit. It returns the commit hash and a warning when only the
// index update failed.
func (w *Workflow) gitCommit(root, parent string, paths []string, contents map[string]string, removed map[string]bool, msg string, intent *RecoveryIntent) (string, string, *Error) {
	ctx := context.Background()
	failed := func(what string, err error) *Error {
		return fail(ExitFail, "commit_failed", "nothing was committed: git %s failed: %v", what, err)
	}
	tmp, err := os.MkdirTemp("", "spec-commit-*")
	if err != nil {
		return "", "", failed("setup", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	git := w.Git
	tg := w.Git
	tg.Env = append(append([]string(nil), w.Git.Env...), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"))

	out, err := git.Run(ctx, root, nil, "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", failed("rev-parse", err)
	}
	prefix := strings.TrimSpace(string(out))
	if e := w.commitPreflight(root); e != nil {
		return "", "", e
	}
	symbolic, err := git.Run(ctx, root, nil, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return "", "", fail(ExitFail, "commit_failed", "Selective commit requires a branch; create or switch to a branch before committing")
	}
	ref := strings.TrimSpace(string(symbolic))
	headPathRaw, err := git.Run(ctx, root, nil, "rev-parse", "--path-format=absolute", "--git-path", "HEAD")
	if err != nil {
		return "", "", failed("locating HEAD", err)
	}
	headPath := strings.TrimSpace(string(headPathRaw))
	expectedHEAD, err := os.ReadFile(headPath)
	if err != nil {
		return "", "", failed("reading HEAD", err)
	}
	if strings.TrimSpace(string(expectedHEAD)) != "ref: "+ref {
		return "", "", fail(ExitFail, "changed", "HEAD changed while preparing the commit; refresh the selection")
	}
	if w.BeforeCommit != nil {
		w.BeforeCommit()
	}
	if parent != "" {
		if _, err := tg.Run(ctx, root, nil, "read-tree", parent); err != nil {
			return "", "", failed("read-tree", err)
		}
	}
	for _, path := range paths {
		if removed[path] {
			if _, err := tg.Run(ctx, root, nil, "update-index", "--force-remove", "--", path); err != nil {
				return "", "", failed("update-index", err)
			}
			continue
		}
		sha, err := tg.Run(ctx, root, strings.NewReader(contents[path]), "hash-object", "-w", "--stdin", "--path", path)
		if err != nil {
			return "", "", failed("hash-object", err)
		}
		mode := "100644"
		if ls, err := tg.Run(ctx, root, nil, "ls-files", "-s", "--", path); err == nil {
			if f := strings.Fields(string(ls)); len(f) > 0 {
				mode = f[0]
			}
		}
		// --cacheinfo paths are relative to the top of the repository.
		info := mode + "," + strings.TrimSpace(string(sha)) + "," + prefix + path
		if _, err := tg.Run(ctx, root, nil, "update-index", "--add", "--cacheinfo", info); err != nil {
			return "", "", failed("update-index", err)
		}
	}
	// Keep ordinary hook behavior with the temporary index, but construct the
	// commit with the frozen reviewed parent. `git commit` would re-read HEAD
	// and could silently put stale selected file contents on a newer parent.
	tg.Env = append(tg.Env, "GIT_EDITOR=:")
	hook := func(name string, args ...string) error {
		argv := []string{"hook", "run", "--ignore-missing", name}
		if len(args) > 0 {
			argv = append(append(argv, "--"), args...)
		}
		_, err := tg.Run(ctx, root, nil, argv...)
		return err
	}
	if err := hook("pre-commit"); err != nil {
		return "", "", failed("pre-commit", err)
	}
	messagePath := filepath.Join(tmp, "COMMIT_EDITMSG")
	if err := os.WriteFile(messagePath, []byte(msg), 0o600); err != nil {
		return "", "", failed("message", err)
	}
	if err := hook("prepare-commit-msg", messagePath, "message"); err != nil {
		return "", "", failed("prepare-commit-msg", err)
	}
	if err := hook("commit-msg", messagePath); err != nil {
		return "", "", failed("commit-msg", err)
	}
	tree, err := tg.Run(ctx, root, nil, "write-tree")
	if err != nil {
		return "", "", failed("write-tree", err)
	}
	args := []string{"commit-tree", strings.TrimSpace(string(tree))}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	if sign, err := git.Run(ctx, root, nil, "config", "--bool", "--get", "commit.gpgsign"); err == nil && strings.TrimSpace(string(sign)) == "true" {
		args = append(args, "-S")
	}
	args = append(args, "-F", messagePath)
	created, err := tg.Run(ctx, root, nil, args...)
	if err != nil {
		return "", "", failed("commit-tree", err)
	}
	hash := strings.TrimSpace(string(created))
	// Updating this exact branch ref compares against the frozen
	// parent. A concurrent commit wins; this unreferenced object is discarded.
	expected := parent
	if expected == "" {
		expected = strings.Repeat("0", len(hash))
	}
	intent.Parent, intent.PreparedCommit = parent, hash
	if err := saveRecovery(root, intent); err != nil {
		return "", "", internalErr(err)
	}
	// Hooks and other Git clients can start an operation after the first check.
	if e := w.commitPreflight(root); e != nil {
		return "", "", e
	}
	if e := w.commitRef(root, headPath, expectedHEAD, expected, hash, strings.Split(msg, "\n")[0], intent); e != nil {
		return "", "", e
	}
	intent.Commit = hash
	if err := saveRecovery(root, intent); err != nil {
		// The reference is already installed: preserve that outcome for callers.
		intent.NeedsInspection = true
		return hash, "committed, but interruption evidence could not be updated; inspect HEAD and index with `spec recover`: " + err.Error(), nil
	}
	_ = hook("post-commit")
	warning := ""
	if _, err := git.Run(ctx, root, nil, append([]string{"reset", "-q", hash, "--"}, paths...)...); err != nil {
		intent.NeedsInspection = true
		warning = fmt.Sprintf("committed %s, but the index entries of %s could not be updated (%v); inspect `git diff --cached` and `spec recover` before any index repair (paths: %s)",
			shortCommit(hash), strings.Join(paths, ", "), err, strings.Join(paths, " "))
	}
	return hash, warning, nil
}

// shortCommit shortens a commit hash.
func shortCommit(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// touches reports whether line ranges a and b share a line; an empty range
// (deleted lines) touches a range it lies in or at the edge of.
func touches(a, b [2]int) bool {
	if a[0] == a[1] || b[0] == b[1] {
		return a[0] <= b[1] && b[0] <= a[1]
	}
	return a[0] < b[1] && b[0] < a[1]
}

// lineRuns returns the byte ranges in after of the runs of lines that
// differ from before (empty for lines only deleted).
func lineRuns(before, after string) [][2]int {
	var out [][2]int
	delta := 0
	for _, e := range udiff.Lines(before, after) {
		start := e.Start + delta
		out = append(out, [2]int{start, start + len(e.New)})
		delta += len(e.New) - (e.End - e.Start)
	}
	return out
}

// SameChange compares the actual edits, not merely a stable anchor-group ID.
func SameChange(a, b Change) bool {
	return reflect.DeepEqual(a.edits, b.edits) && reflect.DeepEqual(a.Anchors, b.Anchors) && reflect.DeepEqual(a.whole, b.whole)
}

// commitRef uses Git's two-phase reference transaction. `prepare` holds both
// HEAD.lock and the current branch lock; only then do we verify exact HEAD
// identity. This supports Git 2.39 without hand-writing refs or reflogs.
func (w *Workflow) commitRef(root, headPath string, expectedHEAD []byte, parent, hash, subject string, intent *RecoveryIntent) *Error {
	bin := w.Git.Git
	if bin == "" {
		bin = "git"
	}
	cmd := exec.Command(bin, "-c", "core.quotepath=off", "-c", "core.fsmonitor=false", "update-ref", "-m", "commit: "+subject, "--stdin")
	cmd.Dir = root
	cmd.Env = append(append(os.Environ(), "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"), w.Git.Env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return internalErr(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return internalErr(err)
	}
	if err := cmd.Start(); err != nil {
		return internalErr(err)
	}
	failTransaction := func() *Error {
		_ = in.Close()
		_ = cmd.Wait()
		return fail(ExitFail, "changed", "HEAD changed or is busy; refresh the selection (nothing from this selection was committed): %s", strings.TrimSpace(stderr.String()))
	}
	if _, err := fmt.Fprintf(in, "start\nupdate HEAD %s %s\nprepare\n", hash, parent); err != nil {
		return failTransaction()
	}
	scan := bufio.NewScanner(out)
	if !scan.Scan() || scan.Text() != "start: ok" || !scan.Scan() || scan.Text() != "prepare: ok" {
		return failTransaction()
	}
	currentHEAD, err := os.ReadFile(headPath)
	if err != nil || string(currentHEAD) != string(expectedHEAD) {
		_, _ = fmt.Fprint(in, "abort\n")
		_ = in.Close()
		_ = cmd.Wait()
		return fail(ExitFail, "changed", "The checked-out branch changed while committing; refresh the selection (nothing from this selection was committed)")
	}
	intent.refUncertain = true
	if _, err := fmt.Fprint(in, "commit\n"); err != nil {
		return failTransaction()
	}
	_ = in.Close()
	if err := cmd.Wait(); err != nil {
		return fail(ExitFail, "commit_failed", "Git could not complete the reference transaction: %s", strings.TrimSpace(stderr.String()))
	}
	intent.refUncertain = false
	return nil
}
