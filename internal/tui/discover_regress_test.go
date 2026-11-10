// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/project"
)

// waitReport takes the watcher's next report off its channel, leaving it for
// the test to deliver when it wants.
func waitReport(t *testing.T, w *watcher) fileChangedMsg {
	t.Helper()
	select {
	case m := <-w.ch:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no report within 5 s")
		return fileChangedMsg{}
	}
}

// hintRig is a watcher that is not running, and an fsnotify watcher to hand its
// hint method: what an event means, with no timing involved.
func hintRig(t *testing.T, root string) (*watcher, *fsnotify.Watcher) {
	t.Helper()
	p, err := project.Open(root)
	require.NoError(t, err)
	fw, err := fsnotify.NewWatcher()
	require.NoError(t, err)
	t.Cleanup(func() { _ = fw.Close() })
	return &watcher{root: root, paths: membersOf(p), last: p}, fw
}

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755))
	}
}

// TestDiscoverDirectoryMovedOut: a directory moved out of the project in one
// rename, as Finder's Move to Trash does, takes its files with it. The files get
// no event of their own, and kqueue has dropped the directory's watch by the time
// the event arrives.
func TestDiscoverDirectoryMovedOut(t *testing.T) {
	for name, config := range map[string]string{"default glob": "", "literal prefix": "files = [\"*.spec.md\", \"docs/*.spec.md\"]\n"} {
		t.Run(name, func(t *testing.T) {
			root := discoverProject(t, config)
			mkdirs(t, root, "docs")
			writeFile(t, root, "docs/d.spec.md", discoverLater)
			r := openDiscover(t, root)
			r.do(func(a *App) { require.NoError(t, a.open(Location{Path: "main.spec.md"})) })
			require.NoError(t, os.Rename(r.abs("docs"), filepath.Join(t.TempDir(), "docs")))
			r.waitGone("docs/d.spec.md")
			r.waitFlash("docs/d.spec.md left the project")
			r.do(func(a *App) { assert.Empty(t, a.ws.Spec.Lookup("Later.Def")) })
		})
	}
}

// TestHintRemovedDirectory: what the removal of a name means, whichever of a known
// file or a watch sits below it. Neither test needs a real event to prove this.
func TestHintRemovedDirectory(t *testing.T) {
	root := discoverProject(t, "")
	mkdirs(t, root, "docs", "empty/deep", "docs2")
	writeFile(t, root, "docs/d.spec.md", discoverLater)
	w, fw := hintRig(t, root)
	require.NoError(t, fw.Add(filepath.Join(root, "empty", "deep")))
	for name, want := range map[string]bool{
		"docs":  true,  // its watch is gone, a known file is below
		"empty": true,  // no file below, but a watch to drop
		"docs2": false, // nothing known, nothing watched; not "docs" either
		"gone":  false,
	} {
		for _, op := range []fsnotify.Op{fsnotify.Remove, fsnotify.Rename} {
			_, rescan := w.hint(fw, fsnotify.Event{Name: filepath.Join(root, name), Op: op})
			assert.Equal(t, want, rescan, "%s %s", op, name)
		}
	}
}

// TestDiscoverAncestorRenamed: renaming a directory above a glob's literal prefix.
func TestDiscoverAncestorRenamed(t *testing.T) {
	root := discoverProject(t, "files = [\"*.spec.md\", \"a/b/c/*.spec.md\"]\n")
	mkdirs(t, root, "a/b/c")
	writeFile(t, root, "a/b/c/d.spec.md", discoverLater)
	r := openDiscover(t, root)
	require.NoError(t, os.Rename(r.abs("a/b"), r.abs("a/z")))
	r.waitGone("a/b/c/d.spec.md")
	require.NoError(t, os.Rename(r.abs("a/z"), r.abs("a/b")))
	r.waitFile("a/b/c/d.spec.md")
}

// TestStaleReportAfterSettingsReload: a report the old watcher made before a
// settings save, delivered after it, must not bring the old project back. What it
// says about a file's content still counts.
func TestStaleReportAfterSettingsReload(t *testing.T) {
	root := discoverProject(t, "name = \"old\"\n")
	a := newTestApp(t, testOptions(root))
	defer a.Close()
	a.startWatcher()
	defer func() { a.watcher.Close() }()
	old := a.watcher
	writeFile(t, root, "extra.spec.md", discoverLater)
	queued := waitReport(t, old)
	require.NotNil(t, queued.scan)
	require.NotNil(t, queued.scan.project)

	writeFile(t, root, "spec.toml", "name = \"new\"\nfiles = [\"main.spec.md\"]\n")
	a.Update(settingsSavedMsg{scope: "Project"})
	require.Equal(t, "new", a.ws.Project.Name)
	require.NotSame(t, old, a.watcher)
	seq := a.flashSeq

	a.Update(queued)
	assert.Equal(t, "new", a.ws.Project.Name)
	assert.Equal(t, []string{"main.spec.md"}, a.ws.Paths())
	assert.Equal(t, seq, a.flashSeq, "no flash about a project that is gone")

	writeFile(t, root, "main.spec.md", discoverMain+"\nEdited outside.\n")
	queued.scan, queued.paths = nil, []string{"main.spec.md"}
	a.Update(queued)
	assert.Contains(t, string(a.cur.Ed.Buffer().Bytes()), "Edited outside.")
}

// TestDiscoverEmacsLock: while a buffer is modified Emacs keeps a dangling symlink
// .#name beside the file. It is nothing to the project, while it lasts or after.
func TestDiscoverEmacsLock(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	before := r.flashes()
	require.NoError(t, os.Symlink("user@host.123:456", r.abs(".#main.spec.md")))
	// The sentinel's re-glob happens with the lock on disk.
	r.write("sentinel.spec.md", "# sentinel\n")
	r.waitFlash("Found new file sentinel.spec.md")
	assert.Equal(t, before+1, r.flashes())
	r.do(func(a *App) {
		assert.False(t, hasDiag(a, diag.CodeFileSymlink, ""), "no finding")
		assert.Equal(t, []string{"main.spec.md", "sentinel.spec.md"}, a.ws.Paths())
	})
	require.NoError(t, os.Remove(r.abs(".#main.spec.md")))
	r.write("sentinel2.spec.md", "# sentinel\n")
	r.waitFile("sentinel2.spec.md")
	r.do(func(a *App) { assert.False(t, hasDiag(a, diag.CodeFileSymlink, "")) })
}

// TestHintLooksAfterASymlink: the Create of a dangling symlink is all kqueue says
// about a directory it stops listing there.
func TestHintLooksAfterASymlink(t *testing.T) {
	root := discoverProject(t, "")
	w, fw := hintRig(t, root)
	lock := filepath.Join(root, ".#main.spec.md")
	require.NoError(t, os.Symlink("user@host.123:456", lock))
	_, rescan := w.hint(fw, fsnotify.Event{Name: lock, Op: fsnotify.Create})
	assert.True(t, rescan)
}

// TestHintIgnoresEditorDebris: an editor's temp files, locks and probes cause no
// re-glob, under any event, even where a wide glob would match them. None of them
// exists here; a lock that does is TestHintLooksAfterASymlink.
func TestHintIgnoresEditorDebris(t *testing.T) {
	root := discoverProject(t, "files = [\"**/*.spec.md\", \"docs/*\"]\nformal = [\"tla/*.tla\"]\n")
	mkdirs(t, root, "docs", "tla")
	writeFile(t, root, "tla/M.tla", discoverModel)
	w, fw := hintRig(t, root)
	for _, junk := range []string{
		".main.spec.md.swp", ".#main.spec.md", ".tmp-abc.spec.md", ".spec-ui-1.tmp", "main.spec.md~", "#main.spec.md#", "4913",
		"docs/.a.md.swp", "docs/.#a.md", "docs/a.md~", "docs/#a.md#", "docs/4913",
		"tla/.N.tla.swp", "tla/.#N.tla", "tla/.N.tla", "tla/N.tla~", "tla/4913", "tla/N.cfg",
	} {
		for _, op := range []fsnotify.Op{fsnotify.Create, fsnotify.Write, fsnotify.Remove, fsnotify.Rename, fsnotify.Chmod} {
			changed, rescan := w.hint(fw, fsnotify.Event{Name: filepath.Join(root, filepath.FromSlash(junk)), Op: op})
			assert.Empty(t, changed, "%s %s", op, junk)
			assert.False(t, rescan, "%s %s", op, junk)
		}
	}
}

// TestDiscoverTempUnderWideGlob: a swap file under a glob that would match it does
// not join and is not news, and neither does a hidden temp named like a spec file.
func TestDiscoverTempUnderWideGlob(t *testing.T) {
	root := discoverProject(t, "files = [\"**/*.spec.md\", \"docs/*\"]\n")
	mkdirs(t, root, "docs")
	writeFile(t, root, "docs/a.md", discoverLater)
	r := openDiscover(t, root)
	before := r.flashes()
	r.write("docs/.a.md.swp", "\x00binary")
	r.write(".tmp-abc.spec.md", discoverLater)
	r.write("docs/sentinel.md", "# sentinel\n")
	r.waitFlash("Found new file docs/sentinel.md")
	assert.Equal(t, before+1, r.flashes())
	r.do(func(a *App) {
		assert.Equal(t, []string{"docs/a.md", "docs/sentinel.md", "main.spec.md"}, a.ws.Paths())
	})
}

// TestDiscoverRetainedDraftCommit: a draft whose file is gone is not part of "the
// files as saved", so the commit view opens.
func TestDiscoverRetainedDraftCommit(t *testing.T) {
	gitEnv(t)
	root := discoverProject(t, "")
	writeFile(t, root, ".gitignore", ".spec/\n")
	writeFile(t, root, "later.spec.md", discoverLater)
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")
	writeFile(t, root, "main.spec.md", discoverMain+"\n**$Main.Three (t).** It MUST hold.\n")
	r := openDiscover(t, root)
	r.do(func(a *App) {
		require.NoError(t, a.open(Location{Path: "later.spec.md"}))
		a.cur.Ed.Insert("draft\n")
	})
	r.remove("later.spec.md")
	r.waitFlash("deleted on disk")
	r.do(func(a *App) {
		require.Contains(t, a.ws.Paths(), "later.spec.md", "the draft still counts")
		_ = a.openCommit()
		view, ok := a.top().(*commitScreen)
		require.True(t, ok, "flash: %s", a.flash)
		if view.cancelDraft != nil {
			view.cancelDraft()
		}
	})
}

// TestDiscoverOpenFileLeavesByConfig: an open file that an edit to spec.toml takes
// out of the project stays watched, as after a settings save. So does one that
// goes after that, and comes back.
func TestDiscoverOpenFileLeavesByConfig(t *testing.T) {
	root := discoverProject(t, "files = [\"*.spec.md\"]\n")
	writeFile(t, root, "other.spec.md", discoverLater)
	r := openDiscover(t, root)
	r.do(func(a *App) { require.Equal(t, "main.spec.md", a.cur.Ed.Path()) })
	r.write("spec.toml", "files = [\"other.spec.md\"]\n")
	r.waitGone("main.spec.md")
	has := func(text string) func(a *App) bool {
		return func(a *App) bool { return strings.Contains(string(a.cur.Ed.Buffer().Bytes()), text) }
	}
	r.write("main.spec.md", discoverMain+"\nEdited outside.\n")
	r.waitFor("the buffer to reload", has("Edited outside."))
	// A later re-glob must not take the watch away.
	r.write("spec.toml", "name = \"again\"\nfiles = [\"other.spec.md\"]\n")
	r.waitFlash("Reloaded spec.toml")
	r.write("main.spec.md", discoverMain+"\nEdited again.\n")
	r.waitFor("the buffer to reload again", has("Edited again."))
}

// TestRealReportDoesNotGlobInUpdate: a report from the watcher is believed. It
// carries no scan when the watcher found nothing to say about membership, and the
// UI does not go and look for itself.
func TestRealReportDoesNotGlobInUpdate(t *testing.T) {
	root := discoverProject(t, "files = [\"*.spec.md\"]\n")
	writeFile(t, root, "other.spec.md", discoverLater)
	a := newTestApp(t, testOptions(root))
	defer a.Close()
	// main.spec.md left through a settings save and is on screen, so the new
	// watcher reports it; its deletion changes no file set.
	writeFile(t, root, "spec.toml", "files = [\"other.spec.md\"]\n")
	require.NoError(t, a.ws.ReloadProject())
	a.startWatcher()
	defer func() { a.watcher.Close() }()
	require.NoError(t, os.Remove(filepath.Join(root, "main.spec.md")))
	m := waitReport(t, a.watcher)
	require.Nil(t, m.scan)
	a.watcher.Close()

	// Only a glob on this goroutine could find this now.
	writeFile(t, root, "spec.toml", "files = [\"*.spec.md\"]\n")
	writeFile(t, root, "proof.spec.md", "# proof\n")
	a.Update(m)
	assert.NotContains(t, a.ws.Paths(), "proof.spec.md")
}

// TestDiscoverMemberInUnwatchedDirectory: a member a re-glob finds in a directory
// no glob names (hidden) is watched for content from then on.
func TestDiscoverMemberInUnwatchedDirectory(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	r.write(".plans/x.spec.md", discoverLater)
	r.write("trigger.spec.md", "# trigger\n")
	r.waitFile(".plans/x.spec.md")
	r.write(".plans/x.spec.md", discoverLater+"\n**$Later.Edit (e).** It MUST.\n")
	r.waitFor("the new content", func(a *App) bool { return strings.Contains(string(a.ws.Source(".plans/x.spec.md")), "Later.Edit") })
}

// TestDiscoverHiddenDirectoryNamed: a hidden directory that a glob names below **.
func TestDiscoverHiddenDirectoryNamed(t *testing.T) {
	root := discoverProject(t, "files = [\"*.spec.md\", \"**/.notes/*.md\"]\n")
	mkdirs(t, root, "a/.notes")
	r := openDiscover(t, root)
	r.write("a/.notes/x.md", discoverLater)
	r.waitFile("a/.notes/x.md")
}

// TestDiscoverNestedProjectDissolved: a nested project's spec.toml goes, and its
// files join this project; it comes back, and they leave.
func TestDiscoverNestedProjectDissolved(t *testing.T) {
	root := discoverProject(t, "")
	mkdirs(t, root, "sub")
	writeFile(t, root, "sub/spec.toml", "")
	writeFile(t, root, "sub/n.spec.md", discoverLater)
	r := openDiscover(t, root)
	r.do(func(a *App) { require.NotContains(t, a.ws.Paths(), "sub/n.spec.md") })
	r.remove("sub/spec.toml")
	r.waitFile("sub/n.spec.md")
	r.write("sub/spec.toml", "")
	r.waitGone("sub/n.spec.md")
}

// TestOutlineViewHoldsWhenAFileJoins: a file sorting above the view pushes the rows
// down; the view goes with them, so the same row stays on top.
func TestOutlineViewHoldsWhenAFileJoins(t *testing.T) {
	root := discoverProject(t, "")
	var long strings.Builder
	for i := range 80 {
		fmt.Fprintf(&long, "## %d. Sec\n\n", i+1)
	}
	writeFile(t, root, "main.spec.md", "# main\n\n"+long.String())
	a := newTestApp(t, testOptions(root))
	defer a.Close()
	const height = 20
	for name, sel := range map[string]int{"selection in view": 40, "selection above the view": 3} {
		t.Run(name, func(t *testing.T) {
			a.outline.sel, a.outline.scroll, a.outline.shown = sel, 30, sel
			top, selected := a.outline.entries[30].text, a.outline.entries[sel].text
			writeFile(t, root, "a"+strings.ReplaceAll(name, " ", "")+".spec.md", "# a\n\n## 1. A\n\n## 2. B\n\n## 3. C\n\n")
			a.onFileChanged([]string{"a" + strings.ReplaceAll(name, " ", "") + ".spec.md"})
			_ = a.outline.view(a, 30, height)
			assert.Equal(t, top, a.outline.entries[a.outline.scroll].text)
			assert.Equal(t, selected, a.outline.entries[a.outline.sel].text)
		})
	}
}

// TestScanBeforeWriteBack: a scan that saw a file gone, applied after ^O wrote it
// back, does not say it left, nor take it out of the project.
func TestScanBeforeWriteBack(t *testing.T) {
	root := discoverProject(t, "")
	writeFile(t, root, "zz.spec.md", discoverLater)
	a := newTestApp(t, testOptions(root))
	defer a.Close()
	a.startWatcher()
	defer func() { a.watcher.Close() }()
	require.Equal(t, "main.spec.md", a.cur.Ed.Path())
	require.NoError(t, os.Remove(filepath.Join(root, "main.spec.md")))
	m := waitReport(t, a.watcher)
	require.NotNil(t, m.scan)
	_, err := a.ws.Save(a.cur)
	require.NoError(t, err)
	seq := a.flashSeq
	a.Update(m)
	assert.Equal(t, seq, a.flashSeq, "flash: %s", a.flash)
	assert.Contains(t, a.ws.Paths(), "main.spec.md")
}

// TestWatcherGlobsAgainAfterAddingAWatch: a file landing between the glob that
// found a new directory and the watch on it causes no event; only the next glob
// sees it. The watcher is not running, so nothing else can look.
func TestWatcherGlobsAgainAfterAddingAWatch(t *testing.T) {
	root := discoverProject(t, "")
	sub := filepath.Join(root, "sub")
	w, fw := hintRig(t, root)
	w.addWatch = func(fw *fsnotify.Watcher, dir string) error {
		if dir == sub {
			writeFile(t, sub, "n.spec.md", "# n\n")
		}
		return fw.Add(dir)
	}
	scan, changed, err := w.rescan(fw)
	require.NoError(t, err)
	require.Nil(t, scan, "the watches on what exists; nothing new")
	require.Empty(t, changed)

	require.NoError(t, os.Mkdir(sub, 0o755))
	scan, changed, err = w.rescan(fw)
	require.NoError(t, err)
	require.NotNil(t, scan)
	assert.Equal(t, []string{"sub/n.spec.md"}, changed)
}

// TestWatcherFirstGlobClosesTheGap: a file created after the UI read the project
// and before the watches existed causes no event; the first glob reports it.
func TestWatcherFirstGlobClosesTheGap(t *testing.T) {
	root := discoverProject(t, "")
	base, err := project.Open(root)
	require.NoError(t, err)
	writeFile(t, root, "n.spec.md", "# n\n")
	w := openWatcher(watchConfig{root: root, paths: []string{"main.spec.md"}, base: base})
	defer w.Close()
	assert.Equal(t, []string{"n.spec.md"}, waitChange(t, w))
}

func TestMergeScan(t *testing.T) {
	first, second := &project.Project{Name: "first"}, &project.Project{Name: "second"}
	assert.Nil(t, mergeScan(nil, nil))
	only := &projectScan{project: first}
	assert.Same(t, only, mergeScan(nil, only))
	assert.Same(t, only, mergeScan(only, nil))

	later := mergeScan(&projectScan{project: first, unreadable: []string{"a"}}, &projectScan{project: second, unreadable: []string{"b"}})
	assert.Same(t, second, later.project, "the later project wins")
	assert.Equal(t, []string{"a", "b"}, later.unreadable)

	warned := mergeScan(&projectScan{project: first, unreadable: []string{"a"}}, &projectScan{warn: "broken"})
	assert.Same(t, first, warned.project, "a warning does not lose the project before it")
	assert.Equal(t, "broken", warned.warn)
	assert.Equal(t, []string{"a"}, warned.unreadable)
}

// TestWatcherMergesAnUndeliveredReport: a UI too busy to take a report gets it
// merged with the next, and loses nothing.
func TestWatcherMergesAnUndeliveredReport(t *testing.T) {
	w := &watcher{ch: make(chan fileChangedMsg, 1), done: make(chan struct{})}
	p := &project.Project{Name: "first"}
	w.send(map[string]bool{"a": true}, &projectScan{project: p})
	w.send(map[string]bool{"b": true}, &projectScan{warn: "broken"})
	m := <-w.ch
	assert.Equal(t, []string{"a", "b"}, m.paths)
	assert.Same(t, p, m.scan.project)
	assert.Equal(t, "broken", m.scan.warn)
	assert.Empty(t, w.ch)
}

// TestBaselineOutOfOrder: git's answer for the project before a spec.toml change,
// arriving after the answer for the project now, is dropped.
func TestBaselineOutOfOrder(t *testing.T) {
	gitEnv(t)
	root := discoverProject(t, "files = [\"main.spec.md\"]\n")
	writeFile(t, root, ".gitignore", ".spec/\n")
	writeFile(t, root, "extra.spec.md", discoverLater)
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")
	opts := testOptions(root)
	opts.Baseline = true
	a := newTestApp(t, opts)
	defer a.Close()
	older := a.reloadBaseline()
	writeFile(t, root, "spec.toml", "files = [\"*.spec.md\"]\n")
	a.onFileChanged([]string{"spec.toml"})
	require.True(t, slices.Contains(a.ws.Paths(), "extra.spec.md"))
	newer := a.reloadBaseline()
	a.Update(newer())
	require.NotNil(t, a.ws.Base.Spec.File("extra.spec.md"))
	a.Update(older())
	assert.NotNil(t, a.ws.Base.Spec.File("extra.spec.md"))
}
