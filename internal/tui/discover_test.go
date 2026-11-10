// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/project"
)

const (
	discoverMain  = "# main\n\n## 1. One\n\n**$Main.One (one).** It MUST hold.\n\n**$Main.Two (two).** It MUST hold after $Main.One and $Later.Def.\n"
	discoverLater = "# later\n\n## 1. Later\n\n**$Later.Def (later).** It MUST arrive.\n"
	discoverModel = "---- MODULE N ----\n\\* Spec: $Main.One \"MUST hold\"\nInv_N == TRUE\n====\n"
)

// discoverRig is the UI on a scratch project with the watcher running.
type discoverRig struct {
	t    *testing.T
	d    *Driver
	root string
}

// discoverProject writes a project: spec.toml and main.spec.md, with $Later.Def
// dangling until something defines it.
func discoverProject(t *testing.T, config string) string {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "spec.toml"), []byte(config), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.spec.md"), []byte(discoverMain), 0o644))
	return root
}

func openDiscover(t *testing.T, root string, tweak ...func(*Options)) *discoverRig {
	t.Helper()
	opts := testOptions(root)
	opts.Watch = true
	for _, fn := range tweak {
		fn(&opts)
	}
	d, err := NewDriver(opts, 120, 40)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	return &discoverRig{t: t, d: d, root: root}
}

func (r *discoverRig) abs(rel string) string {
	return filepath.Join(r.root, filepath.FromSlash(rel))
}

// write puts text at rel, making its directories.
func (r *discoverRig) write(rel, text string) {
	r.t.Helper()
	require.NoError(r.t, os.MkdirAll(filepath.Dir(r.abs(rel)), 0o755))
	require.NoError(r.t, os.WriteFile(r.abs(rel), []byte(text), 0o644))
}

func (r *discoverRig) remove(rel string) {
	r.t.Helper()
	require.NoError(r.t, os.RemoveAll(r.abs(rel)))
}

func (r *discoverRig) do(fn func(a *App)) {
	r.t.Helper()
	require.NoError(r.t, r.d.do(fn))
}

// waitFor waits until cond holds on the UI's goroutine.
func (r *discoverRig) waitFor(what string, cond func(a *App) bool) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ok, state := false, ""
		r.do(func(a *App) {
			ok = cond(a)
			state = fmt.Sprintf("flash %q, files %v, models %v", a.flash, a.ws.Paths(), a.ws.ModelPaths())
		})
		if ok {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s; %s", what, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *discoverRig) waitFile(rel string) {
	r.t.Helper()
	r.waitFor(rel+" in the project", func(a *App) bool {
		return slices.Contains(a.ws.Paths(), rel) || slices.Contains(a.ws.ModelPaths(), rel)
	})
}

func (r *discoverRig) waitGone(rel string) {
	r.t.Helper()
	r.waitFor(rel+" out of the project", func(a *App) bool {
		return !slices.Contains(a.ws.Paths(), rel) && !slices.Contains(a.ws.ModelPaths(), rel)
	})
}

func (r *discoverRig) waitFlash(text string) {
	r.t.Helper()
	r.waitFor("the flash "+text, func(a *App) bool { return strings.Contains(a.flash, text) })
}

// flashes returns how many flashes the UI has set.
func (r *discoverRig) flashes() int {
	r.t.Helper()
	n := 0
	r.do(func(a *App) { n = a.flashSeq })
	return n
}

// quickOpenHas reports whether ^Q offers item.
func quickOpenHas(a *App, key string) bool {
	a.openQuickOpen()
	defer func() { a.list = nil }()
	return slices.ContainsFunc(a.list.all, func(it listItem) bool { return strings.Contains(it.key, key) })
}

// outlineHas reports whether the outline has a file row for path.
func outlineHas(a *App, path string) bool {
	return slices.ContainsFunc(a.outline.entries, func(e outlineEntry) bool { return e.kind == entryFile && e.loc.Path == path })
}

func hasDiag(a *App, code, text string) bool {
	return slices.ContainsFunc(a.ws.Diags, func(d diag.Diagnostic) bool { return d.Code == code && strings.Contains(d.Message, text) })
}

// TestDiscoverSpecFileInKnownDir: a new file in a known directory shows in the
// outline and quick open, joins the index, and its flash names it.
func TestDiscoverSpecFileInKnownDir(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	before := r.flashes()
	r.write("later.spec.md", discoverLater)
	r.waitFile("later.spec.md")
	r.waitFlash("Found new file later.spec.md")
	r.do(func(a *App) {
		assert.True(t, outlineHas(a, "later.spec.md"))
		assert.True(t, quickOpenHas(a, "later.spec.md"))
		assert.Len(t, a.ws.Spec.Lookup("Later.Def"), 1, "its definitions join the index")
		_, err := a.ws.Doc("later.spec.md")
		assert.NoError(t, err, "it opens")
	})
	assert.Equal(t, before+1, r.flashes())
}

// TestDiscoverDanglingResolves: a reference that dangled resolves when the file
// defining it arrives, and dangles again when that file goes.
func TestDiscoverDanglingResolves(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	r.do(func(a *App) { require.True(t, hasDiag(a, diag.CodeRefDangling, "Later.Def")) })
	r.write("later.spec.md", discoverLater)
	r.waitFile("later.spec.md")
	r.do(func(a *App) { assert.False(t, hasDiag(a, diag.CodeRefDangling, "Later.Def")) })
	r.remove("later.spec.md")
	r.waitGone("later.spec.md")
	r.do(func(a *App) {
		assert.True(t, hasDiag(a, diag.CodeRefDangling, "Later.Def"))
		assert.Empty(t, a.ws.Spec.Lookup("Later.Def"))
	})
}

// TestDiscoverModelInNewDir: a .tla in a directory that did not exist when the
// UI opened; the glob's prefix is created after the watches were placed.
func TestDiscoverModelInNewDir(t *testing.T) {
	r := openDiscover(t, discoverProject(t, "formal = [\"tla/models/*.tla\"]\n"))
	r.write("tla/models/N.tla", discoverModel)
	r.waitFile("tla/models/N.tla")
	r.waitFlash("Found new file tla/models/N.tla")
	r.do(func(a *App) {
		assert.Equal(t, []string{"tla/models/N.tla"}, a.ws.ModelPaths())
		assert.True(t, outlineHas(a, "tla/models/N.tla"))
		assert.True(t, quickOpenHas(a, "Inv_N"))
		assert.Len(t, a.ws.Formal.ToAnchor("Main.One"), 1, "its tags join the index")
		assert.Nil(t, a.ws.File("tla/models/N.tla"), "a model is not a spec file")
	})
	// The watch that now exists sees the next one, without any directory event.
	r.write("tla/models/O.tla", strings.ReplaceAll(discoverModel, "MODULE N", "MODULE O"))
	r.waitFile("tla/models/O.tla")
}

// TestDiscoverNestedNewDirs: below ** a tree appears at once, and a file lands in
// its deepest directory after the watches caught up.
func TestDiscoverNestedNewDirs(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	r.write("a/b/c/deep.spec.md", "# deep\n")
	r.waitFile("a/b/c/deep.spec.md")
	r.write("a/b/c/d/e/deeper.spec.md", "# deeper\n")
	r.waitFile("a/b/c/d/e/deeper.spec.md")
	r.write("a/b/c/d/e/deepest.spec.md", "# deepest\n")
	r.waitFile("a/b/c/d/e/deepest.spec.md")
	r.write("a/b/sibling.spec.md", "# sibling\n")
	r.waitFile("a/b/sibling.spec.md")
}

// TestDiscoverTempThenRename: an editor's temp file does not show; the rename
// into place does.
func TestDiscoverTempThenRename(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	before := r.flashes()
	r.write(".later.spec.md.tmp123", discoverLater)
	require.NoError(t, os.Rename(r.abs(".later.spec.md.tmp123"), r.abs("later.spec.md")))
	r.waitFile("later.spec.md")
	r.waitFlash("Found new file later.spec.md")
	assert.Equal(t, before+1, r.flashes())
}

// TestDiscoverMvIntoPlace: a whole directory moved in from outside the project.
func TestDiscoverMvIntoPlace(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "dir", "moved.spec.md"), []byte("# moved\n"), 0o644))
	require.NoError(t, os.Rename(filepath.Join(outside, "dir"), r.abs("dir")))
	r.waitFile("dir/moved.spec.md")
}

// TestDiscoverRename: a rename reads as a remove plus an add, in one flash.
func TestDiscoverRename(t *testing.T) {
	root := discoverProject(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(root, "old.spec.md"), []byte(discoverLater), 0o644))
	r := openDiscover(t, root)
	before := r.flashes()
	require.NoError(t, os.Rename(r.abs("old.spec.md"), r.abs("new.spec.md")))
	r.waitFile("new.spec.md")
	r.waitGone("old.spec.md")
	r.waitFlash("Found new file new.spec.md; old.spec.md left the project")
	assert.Equal(t, before+1, r.flashes())
	r.do(func(a *App) { assert.Len(t, a.ws.Spec.Lookup("Later.Def"), 1) })
}

// TestDiscoverDeleteClean: a clean file that goes stops contributing, whether it
// was open or not.
func TestDiscoverDeleteClean(t *testing.T) {
	root := discoverProject(t, "formal = [\"tla/*.tla\"]\n")
	for name, text := range map[string]string{"later.spec.md": discoverLater, "opened.spec.md": "# opened\n", "tla/N.tla": discoverModel} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(text), 0o644))
	}
	r := openDiscover(t, root)
	r.do(func(a *App) {
		require.NoError(t, a.open(Location{Path: "opened.spec.md"}))
		require.NoError(t, a.open(Location{Path: "main.spec.md"}))
	})
	r.remove("later.spec.md")
	r.waitGone("later.spec.md")
	r.waitFlash("later.spec.md left the project")
	r.do(func(a *App) {
		assert.False(t, outlineHas(a, "later.spec.md"))
		assert.False(t, quickOpenHas(a, "later.spec.md"))
		assert.True(t, hasDiag(a, diag.CodeRefDangling, "Later.Def"))
	})
	r.remove("tla/N.tla")
	r.waitGone("tla/N.tla")
	r.do(func(a *App) { assert.Empty(t, a.ws.Formal.ToAnchor("Main.One"), "its tags go") })
	r.remove("opened.spec.md")
	r.waitGone("opened.spec.md")
	r.do(func(a *App) {
		_, open := a.ws.Docs()["opened.spec.md"]
		assert.False(t, open, "nothing is lost closing a clean buffer")
		a.goBack() // the jump list may still name it: a flash, not a crash
	})
}

// TestDiscoverDeleteCurrentClean: the file on screen stays there, out of the
// project, until the user goes elsewhere; ^O writes it back.
func TestDiscoverDeleteCurrentClean(t *testing.T) {
	root := discoverProject(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(root, "later.spec.md"), []byte(discoverLater), 0o644))
	r := openDiscover(t, root)
	var cur *Doc
	r.do(func(a *App) {
		require.NoError(t, a.open(Location{Path: "main.spec.md", Off: 5}))
		cur = a.cur
	})
	r.remove("main.spec.md")
	r.waitGone("main.spec.md")
	r.waitFlash("main.spec.md was deleted on disk; ^O writes it back")
	r.do(func(a *App) {
		assert.Same(t, cur, a.cur)
		assert.Equal(t, 5, a.cur.Ed.Cursor())
		assert.Nil(t, a.ws.Spec.File("main.spec.md"))
		content, _ := a.render()
		assert.NotEmpty(t, content)
	})
	require.NoError(t, r.d.Press("ctrl+o"))
	r.waitFile("main.spec.md")
	assert.FileExists(t, r.abs("main.spec.md"))
}

// TestDiscoverRenameCurrent: the file on screen renamed away says both halves.
func TestDiscoverRenameCurrent(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	before := r.flashes()
	require.NoError(t, os.Rename(r.abs("main.spec.md"), r.abs("renamed.spec.md")))
	r.waitFile("renamed.spec.md")
	r.waitFlash("main.spec.md was deleted on disk; ^O writes it back; Found new file renamed.spec.md")
	assert.Equal(t, before+1, r.flashes())
}

// TestDiscoverDeleteLastFile: every file gone leaves an orphan buffer on screen,
// and a working UI that finds the next file.
func TestDiscoverDeleteLastFile(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	r.remove("main.spec.md")
	r.waitGone("main.spec.md")
	r.do(func(a *App) {
		content, _ := a.render()
		assert.NotEmpty(t, content)
		assert.True(t, hasDiag(a, diag.CodeProjectNoFiles, ""))
	})
	r.write("again.spec.md", "# again\n")
	r.waitFile("again.spec.md")
	r.do(func(a *App) { assert.False(t, hasDiag(a, diag.CodeProjectNoFiles, "")) })
}

// TestDiscoverDeleteDirty: unsaved edits keep the buffer, its analysis and the
// message; ^O puts the file back with no "found" flash.
func TestDiscoverDeleteDirty(t *testing.T) {
	root := discoverProject(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(root, "later.spec.md"), []byte(discoverLater), 0o644))
	r := openDiscover(t, root)
	r.do(func(a *App) {
		require.NoError(t, a.open(Location{Path: "later.spec.md", Off: 0}))
		a.cur.Ed.Insert("draft\n\n")
		a.reparse()
	})
	r.remove("later.spec.md")
	r.waitFlash("later.spec.md was deleted on disk; ^O writes your buffer back")
	var text []byte
	r.do(func(a *App) {
		assert.Contains(t, a.ws.Paths(), "later.spec.md", "it still counts while unsaved")
		assert.Len(t, a.ws.Spec.Lookup("Later.Def"), 1)
		assert.True(t, a.cur.Ed.Buffer().Dirty())
		text = bytes.Clone(a.cur.Ed.Buffer().Bytes())
	})
	require.NoError(t, r.d.Press("ctrl+o"))
	r.waitFlash("Saved later.spec.md")
	disk, err := os.ReadFile(r.abs("later.spec.md"))
	require.NoError(t, err)
	assert.Equal(t, text, disk)
	// The save's own events have passed once the next file's have.
	before := r.flashes()
	r.write("sentinel.spec.md", "# sentinel\n")
	r.waitFlash("Found new file sentinel.spec.md")
	assert.Equal(t, before+1, r.flashes())
}

// TestDiscoverBurst: many files in one settle make one flash with a count.
func TestDiscoverBurst(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	stage := t.TempDir()
	const n = 12
	for i := range n {
		require.NoError(t, os.WriteFile(filepath.Join(stage, fmt.Sprintf("f%d.spec.md", i)), []byte("# f\n"), 0o644))
	}
	before := r.flashes()
	for i := range n {
		name := fmt.Sprintf("f%d.spec.md", i)
		require.NoError(t, os.Rename(filepath.Join(stage, name), r.abs(name)))
	}
	r.waitFlash(fmt.Sprintf("Found %d new files", n))
	assert.Equal(t, before+1, r.flashes())
	r.do(func(a *App) { assert.Len(t, a.ws.Paths(), n+1) })
}

// TestDiscoverIgnoresEditorDebris: swap files, backups and write probes change
// nothing: no flash, no reload, not in the project.
func TestDiscoverIgnoresEditorDebris(t *testing.T) {
	root := discoverProject(t, "files = [\"**/*.spec.md\"]\nformal = [\"tla/*.tla\"]\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tla"), 0o755))
	r := openDiscover(t, root)
	before := r.flashes()
	for _, junk := range []string{".main.spec.md.swp", "main.spec.md~", "4913", "#main.spec.md#", "tla/.N.tla.swp", "tla/N.tla~", "tla/4913", "tla/N.cfg"} {
		r.write(junk, "junk")
	}
	r.remove("4913")
	// The sentinel's re-glob happens with the debris on disk, so anything it would
	// select would be there and counted in the flash. TestHintIgnoresEditorDebris
	// has it that no event about the debris re-globs at all.
	r.write("sentinel.spec.md", "# sentinel\n")
	r.waitFlash("Found new file sentinel.spec.md")
	assert.Equal(t, before+1, r.flashes())
	r.do(func(a *App) {
		assert.Equal(t, []string{"main.spec.md", "sentinel.spec.md"}, a.ws.Paths())
		assert.Empty(t, a.ws.ModelPaths())
	})
}

// TestDiscoverKeepsTheUserInPlace: dirty buffers, the cursor, the scroll, the
// current file and an open prompt stay as they are.
func TestDiscoverKeepsTheUserInPlace(t *testing.T) {
	root := discoverProject(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(root, "long.spec.md"), []byte("# long\n\n"+strings.Repeat("A line of prose.\n", 200)), 0o644))
	r := openDiscover(t, root)
	var (
		cur  *Doc
		text []byte
		off  int
		top  any
	)
	r.do(func(a *App) {
		require.NoError(t, a.open(Location{Path: "long.spec.md", Off: 1000}))
		a.cur.Ed.CenterCursor()
		a.cur.Ed.Insert("mine ")
		cur, off, top, text = a.cur, a.cur.Ed.Cursor(), a.cur.Ed.TopRow(), bytes.Clone(a.cur.Ed.Buffer().Bytes())
		a.prompt = newTextPrompt(promptFind, "Find: ", "half-typed")
	})
	r.write("later.spec.md", discoverLater)
	r.waitFile("later.spec.md")
	r.do(func(a *App) {
		assert.Same(t, cur, a.cur)
		assert.Equal(t, off, a.cur.Ed.Cursor())
		assert.Equal(t, top, a.cur.Ed.TopRow())
		assert.True(t, a.cur.Ed.Buffer().Dirty())
		assert.Equal(t, text, a.cur.Ed.Buffer().Bytes())
		require.NotNil(t, a.prompt)
		assert.Equal(t, "half-typed", a.prompt.input)
		a.prompt = nil
	})
}

// TestDiscoverKeepsAPopup: an arriving file moves no offsets, so a hover stays;
// one that leaves may be what it points at, so it goes.
func TestDiscoverKeepsAPopup(t *testing.T) {
	root := discoverProject(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(root, "other.spec.md"), []byte("# other\n"), 0o644))
	r := openDiscover(t, root)
	r.do(func(a *App) {
		require.NoError(t, a.open(Location{Path: "main.spec.md", Off: strings.Index(discoverMain, "$Main.One")}))
		a.look()
		require.NotNil(t, a.popup)
	})
	r.write("later.spec.md", discoverLater)
	r.waitFile("later.spec.md")
	r.do(func(a *App) { assert.NotNil(t, a.popup) })
	r.remove("other.spec.md")
	r.waitGone("other.spec.md")
	r.do(func(a *App) { assert.Nil(t, a.popup) })
}

// TestDiscoverKeepsAgentFlash: a proposal's flash outlives a discovery.
func TestDiscoverKeepsAgentFlash(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	r.do(func(a *App) {
		a.setStatus(statusAgent, "", "New proposal p1")
		a.agentFlash = a.flash
	})
	r.write("later.spec.md", discoverLater)
	r.waitFile("later.spec.md")
	r.do(func(a *App) { assert.Equal(t, "New proposal p1", a.flash) })
}

// TestDiscoverOwnSaveIsQuiet: ^O flashes that it saved, and nothing more.
func TestDiscoverOwnSaveIsQuiet(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	var proj *project.Project
	r.do(func(a *App) {
		proj = a.ws.Project
		a.cur.Ed.Insert("mine\n")
	})
	require.NoError(t, r.d.Press("ctrl+o"))
	r.waitFlash("Saved main.spec.md")
	before := r.flashes()
	// The watch on the saved file sees an outside edit only once the save's events
	// have passed; whatever they did is done when its flash comes.
	r.write("main.spec.md", "# outside\n")
	r.waitFlash("Reloaded main.spec.md")
	assert.Equal(t, before+1, r.flashes())
	r.do(func(a *App) { assert.Same(t, proj, a.ws.Project, "no reload") })
}

// TestDiscoverPolling: with no fsnotify, discovery works the same, by re-globbing
// on each tick.
func TestDiscoverPolling(t *testing.T) {
	r := openDiscover(t, discoverProject(t, "formal = [\"tla/*.tla\"]\n"), func(o *Options) { o.WatchPoll = true })
	before := r.flashes()
	r.write("later.spec.md", discoverLater)
	r.write("tla/N.tla", discoverModel)
	r.waitFile("later.spec.md")
	r.waitFile("tla/N.tla")
	r.waitFlash("Found 2 new files")
	assert.Equal(t, before+1, r.flashes())
	r.remove("tla")
	r.waitGone("tla/N.tla")
	r.write("spec.toml", "name = \"changed\"\nformal = [\"models/*.tla\"]\n")
	r.waitFlash("Reloaded spec.toml")
}

// TestWatchFailureFallsBackToPolling: a directory that cannot be watched turns
// the watcher to polling, at the start or later on, and it still discovers.
func TestWatchFailureFallsBackToPolling(t *testing.T) {
	for name, failAfter := range map[string]int{"at the start": 0, "on a new directory": 1} {
		t.Run(name, func(t *testing.T) {
			root := discoverProject(t, "")
			added := 0
			w := openWatcher(watchConfig{root: root, paths: []string{"main.spec.md"}, addWatch: func(fw *fsnotify.Watcher, dir string) error {
				if added >= failAfter {
					return syscall.EMFILE
				}
				added++
				return fw.Add(dir)
			}})
			defer w.Close()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "n.spec.md"), []byte("# n\n"), 0o644))
			assert.Contains(t, waitChange(t, w), "sub/n.spec.md")
		})
	}
}

// TestUnwatchableDirectoryIsNoFailure: a directory gone or closed to us is
// skipped, and fsnotify stays in use.
func TestUnwatchableDirectoryIsNoFailure(t *testing.T) {
	for _, errOf := range []error{os.ErrNotExist, syscall.EACCES} {
		root := discoverProject(t, "")
		require.NoError(t, os.MkdirAll(filepath.Join(root, "closed"), 0o755))
		w := openWatcher(watchConfig{root: root, paths: []string{"main.spec.md"}, addWatch: func(fw *fsnotify.Watcher, dir string) error {
			if filepath.Base(dir) == "closed" {
				return errOf
			}
			return fw.Add(dir)
		}})
		started := time.Now()
		require.NoError(t, os.WriteFile(filepath.Join(root, "n.spec.md"), []byte("# n\n"), 0o644))
		assert.Contains(t, waitChange(t, w), "n.spec.md")
		assert.Less(t, time.Since(started), pollEvery, "fsnotify reported it, not polling")
		w.Close()
	}
}

// TestDiscoverSpecTomlChange: adding formal = [...] on disk while running.
func TestDiscoverSpecTomlChange(t *testing.T) {
	root := discoverProject(t, "name = \"before\"\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tla"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tla/N.tla"), []byte(discoverModel), 0o644))
	r := openDiscover(t, root)
	before := r.flashes()
	r.do(func(a *App) {
		assert.Empty(t, a.ws.ModelPaths())
		a.cur.Ed.Insert("draft\n\n")
	})
	r.write("spec.toml", "name = \"before\"\nformal = [\"tla/*.tla\"]\n")
	r.waitFile("tla/N.tla")
	r.waitFlash("Reloaded spec.toml; Found new file tla/N.tla")
	assert.Equal(t, before+1, r.flashes())
	r.do(func(a *App) {
		assert.Len(t, a.ws.Formal.ToAnchor("Main.One"), 1)
		assert.True(t, a.cur.Ed.Buffer().Dirty(), "the draft stays")
		assert.Equal(t, "draft\n\n", string(a.cur.Ed.Buffer().Bytes()[:7]))
	})
	// The new glob is watched: the next model arrives on its own.
	r.write("tla/O.tla", strings.ReplaceAll(discoverModel, "MODULE N", "MODULE O"))
	r.waitFile("tla/O.tla")
	// A change that touches no file still takes: a lint override.
	r.write("spec.toml", "name = \"after\"\nformal = [\"tla/*.tla\"]\n[lint]\nref-dangling = \"off\"\n")
	r.waitFlash("Reloaded spec.toml")
	r.waitFor("the new name", func(a *App) bool { return a.ws.Project.Name == "after" })
	r.do(func(a *App) { assert.False(t, hasDiag(a, diag.CodeRefDangling, "Later.Def")) })
}

// TestDiscoverBrokenSpecToml: a spec.toml that does not parse keeps the old one,
// warns once, and takes the fixed one.
func TestDiscoverBrokenSpecToml(t *testing.T) {
	r := openDiscover(t, discoverProject(t, "files = [\"*.spec.md\"]\n"))
	before := r.flashes()
	r.write("spec.toml", "files = [\"*.spec.md\"\n")
	r.waitFlash("spec.toml has an error")
	assert.Equal(t, before+1, r.flashes(), "said once")
	r.do(func(a *App) {
		assert.Equal(t, statusWarning, a.flashKind)
		assert.Equal(t, []string{"*.spec.md"}, a.ws.Project.Config.Files, "the old config stays")
		assert.Equal(t, []string{"main.spec.md"}, a.ws.Paths())
	})
	r.write("later.spec.md", discoverLater) // nothing to judge by until it parses
	r.write("spec.toml", "files = [\"*.spec.md\", \"x/*.md\"]\n")
	r.waitFile("later.spec.md")
	r.waitFlash("Reloaded spec.toml; Found new file later.spec.md")
	r.do(func(a *App) { assert.Equal(t, []string{"*.spec.md", "x/*.md"}, a.ws.Project.Config.Files) })
	r.write("x/y.md", "# y\n")
	r.waitFile("x/y.md")
}

// midWrite is a watcher on a project whose spec.toml is being written: it starts
// out as before, the test leaves it mid-write, and settle is the writer going on
// while the watcher pauses. No timing: the watcher's goroutine does not run.
func midWrite(t *testing.T, config string) (*watcher, *project.Project, string) {
	t.Helper()
	root := discoverProject(t, config)
	base, err := project.Open(root)
	require.NoError(t, err)
	w := &watcher{root: root, paths: map[string]bool{}, last: base, done: make(chan struct{})}
	return w, base, filepath.Join(root, project.ConfigFile)
}

// TestWatcherHoldsSpecTomlMidWrite: a look at spec.toml while it is being written
// keeps the project in use, whatever it finds: a file truncated by the writer (an
// empty spec.toml is a valid one, with no files), a part that parses, a part that
// does not. The writer's event brings the next look, which takes the whole file.
func TestWatcherHoldsSpecTomlMidWrite(t *testing.T) {
	const old = "files = [\"*.spec.md\"]\n"
	const whole = "name = \"x\"\nfiles = [\"*.spec.md\", \"x/*.md\"]\n"
	for name, partial := range map[string]string{
		"truncated":      "",
		"parses so far":  "name = \"x\"\n",
		"broken so far":  "name = \"x\"\nfiles = [\"*.sp",
		"old, then more": old + "[lint",
	} {
		t.Run(name, func(t *testing.T) {
			w, base, path := midWrite(t, old)
			require.NoError(t, os.WriteFile(path, []byte(partial), 0o644))
			w.settle = func(time.Duration) { require.NoError(t, os.WriteFile(path, []byte(whole), 0o644)) }
			scan, changed, err := w.rescan(nil)
			require.NoError(t, err)
			assert.Nil(t, scan, "nothing to report mid-write")
			assert.Empty(t, changed)
			assert.Same(t, base, w.last, "the project in use stays")
			assert.Empty(t, w.warned, "no warning about a file not finished yet")

			w.settle = func(time.Duration) {}
			scan, changed, err = w.rescan(nil)
			require.NoError(t, err)
			require.NotNil(t, scan)
			require.NotNil(t, scan.project)
			assert.Equal(t, []string{"*.spec.md", "x/*.md"}, scan.project.Config.Files)
			assert.Contains(t, changed, project.ConfigFile)
		})
	}
}

// TestWatcherHoldsARepeatingWriter: the same part seen twice is no proof, when the
// file was written again in between. A writer going round truncates it every time.
func TestWatcherHoldsARepeatingWriter(t *testing.T) {
	w, base, path := midWrite(t, "files = [\"*.spec.md\"]\n")
	require.NoError(t, os.WriteFile(path, nil, 0o644))
	w.settle = func(time.Duration) {
		st, err := os.Stat(path)
		require.NoError(t, err)
		later := st.ModTime().Add(time.Second)
		require.NoError(t, os.Chtimes(path, later, later))
	}
	scan, _, err := w.rescan(nil)
	require.NoError(t, err)
	assert.Nil(t, scan)
	assert.Same(t, base, w.last)
}

// TestWatcherJudgesASettledSpecToml: once spec.toml stays as it is, it counts. A
// broken one keeps the old config and warns once; an emptied one is an empty
// config, since someone meant it; a deleted one keeps the old.
func TestWatcherJudgesASettledSpecToml(t *testing.T) {
	const old = "files = [\"*.spec.md\"]\n"
	t.Run("broken", func(t *testing.T) {
		w, base, path := midWrite(t, old)
		require.NoError(t, os.WriteFile(path, []byte("files = [\"*.spec.md\"\n"), 0o644))
		var pauses []time.Duration
		w.settle = func(d time.Duration) { pauses = append(pauses, d) }
		scan, _, err := w.rescan(nil)
		require.NoError(t, err)
		assert.Equal(t, []time.Duration{watchSettle}, pauses)
		require.NotNil(t, scan)
		assert.Nil(t, scan.project)
		assert.Contains(t, scan.warn, "spec.toml has an error")
		assert.Same(t, base, w.last)
		assert.Equal(t, []string{"*.spec.md"}, w.last.Config.Files, "the old config stays")
		scan, _, err = w.rescan(nil)
		require.NoError(t, err)
		assert.Nil(t, scan, "said once")
	})
	t.Run("emptied", func(t *testing.T) {
		w, _, path := midWrite(t, old)
		require.NoError(t, os.WriteFile(path, nil, 0o644))
		var pauses []time.Duration
		w.settle = func(d time.Duration) { pauses = append(pauses, d) }
		scan, _, err := w.rescan(nil)
		require.NoError(t, err)
		assert.Equal(t, []time.Duration{emptySettle}, pauses, "a truncating writer may be stalled")
		require.NotNil(t, scan)
		require.NotNil(t, scan.project)
		assert.Nil(t, scan.project.Config.Files)
	})
	t.Run("deleted", func(t *testing.T) {
		w, base, path := midWrite(t, old)
		require.NoError(t, os.Remove(path))
		w.settle = func(time.Duration) {}
		scan, _, err := w.rescan(nil)
		require.NoError(t, err)
		require.NotNil(t, scan)
		assert.Contains(t, scan.warn, "spec.toml was deleted")
		assert.Same(t, base, w.last)
	})
	t.Run("unchanged needs no pause", func(t *testing.T) {
		w, _, _ := midWrite(t, old)
		w.settle = func(time.Duration) { t.Error("paused over a spec.toml that did not change") }
		scan, _, err := w.rescan(nil)
		require.NoError(t, err)
		assert.Nil(t, scan)
	})
}

// TestDiscoverSpecTomlDeleted: losing spec.toml keeps the project running, and
// putting it back resumes discovery.
func TestDiscoverSpecTomlDeleted(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	r.remove("spec.toml")
	r.waitFlash("spec.toml was deleted")
	r.do(func(a *App) {
		assert.Equal(t, []string{"main.spec.md"}, a.ws.Paths())
		content, _ := a.render()
		assert.NotEmpty(t, content)
	})
	r.write("spec.toml", "")
	r.write("later.spec.md", discoverLater)
	r.waitFile("later.spec.md")
}

// TestDiscoverNestedProject: a spec.toml appearing below takes its directory out
// of this project, as it would at startup.
func TestDiscoverNestedProject(t *testing.T) {
	root := discoverProject(t, "")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "a.spec.md"), []byte("# a\n"), 0o644))
	r := openDiscover(t, root)
	r.do(func(a *App) { require.NoError(t, a.open(Location{Path: "main.spec.md"})) })
	r.write("sub/spec.toml", "")
	r.waitGone("sub/a.spec.md")
}

// TestDiscoverGitCheckout: a branch switch brings files, takes files and rewrites
// files, in one report, and the baseline follows.
func TestDiscoverGitCheckout(t *testing.T) {
	gitEnv(t)
	root := discoverProject(t, "")
	writeFile(t, root, "stays.spec.md", "# stays\n")
	writeFile(t, root, "only-main.spec.md", "# only\n")
	writeFile(t, root, ".gitignore", ".spec/\n")
	gitRun(t, root, "init", "-q", "-b", "main")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")
	gitRun(t, root, "checkout", "-q", "-b", "other")
	gitRun(t, root, "rm", "-q", "only-main.spec.md")
	writeFile(t, root, "stays.spec.md", "# stays, changed\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub", "dir"), 0o755))
	writeFile(t, root, "sub/dir/only-other.spec.md", discoverLater)
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "other")
	gitRun(t, root, "checkout", "-q", "main")

	r := openDiscover(t, root, func(o *Options) { o.Baseline = true })
	require.NoError(t, r.d.Wait(Cond{Baseline: true}))
	r.do(func(a *App) { require.NoError(t, a.open(Location{Path: "stays.spec.md", Off: 2})) })
	before := r.flashes()
	gitRun(t, root, "checkout", "-q", "other")
	r.waitFile("sub/dir/only-other.spec.md")
	r.waitGone("only-main.spec.md")
	r.waitFlash("Found new file sub/dir/only-other.spec.md; only-main.spec.md left the project")
	assert.Equal(t, before+1, r.flashes())
	r.waitFor("the open file to reload", func(a *App) bool { return strings.Contains(string(a.cur.Ed.Buffer().Bytes()), "changed") })
	r.do(func(a *App) { assert.False(t, hasDiag(a, diag.CodeRefDangling, "Later.Def")) })
	gitRun(t, root, "checkout", "-q", "main")
	r.waitGone("sub/dir/only-other.spec.md")
	r.waitFile("only-main.spec.md")
}

// TestDiscoverDuplicateDefinition: a new file repeating a definition is a normal
// lint finding.
func TestDiscoverDuplicateDefinition(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	r.write("again.spec.md", "# again\n\n**$Main.One (again).** It MUST hold twice.\n")
	r.waitFile("again.spec.md")
	r.do(func(a *App) {
		assert.True(t, hasDiag(a, diag.CodeDefDuplicate, "Main.One"))
		content, _ := a.render()
		assert.NotEmpty(t, content)
	})
}

// TestDiscoverCreatedAndDeletedInASettle: a file that lived briefly was never
// news.
func TestDiscoverCreatedAndDeletedInASettle(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	before := r.flashes()
	r.write("brief.spec.md", "# brief\n")
	r.remove("brief.spec.md")
	r.write("sentinel.spec.md", "# sentinel\n")
	r.waitFlash("Found new file sentinel.spec.md")
	assert.Equal(t, before+1, r.flashes())
	r.waitGone("brief.spec.md")
}

// TestDiscoverDirectoryDeleted: a watched directory that goes takes its files,
// and watching resumes when it comes back.
func TestDiscoverDirectoryDeleted(t *testing.T) {
	root := discoverProject(t, "formal = [\"tla/*.tla\"]\nfiles = [\"*.spec.md\", \"docs/*.md\"]\n")
	for name, text := range map[string]string{"docs/d.md": "# d\n", "docs/e.md": "# e\n", "tla/N.tla": discoverModel} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(text), 0o644))
	}
	r := openDiscover(t, root)
	r.do(func(a *App) { require.NoError(t, a.open(Location{Path: "main.spec.md"})) })
	r.remove("docs")
	r.waitGone("docs/d.md")
	r.waitGone("docs/e.md")
	r.waitFlash("2 files left the project")
	require.NoError(t, os.Rename(r.abs("tla"), r.abs("tla2")))
	r.waitGone("tla/N.tla")
	r.write("docs/f.md", "# f\n")
	r.waitFile("docs/f.md")
	r.write("tla/O.tla", strings.ReplaceAll(discoverModel, "MODULE N", "MODULE O"))
	r.waitFile("tla/O.tla")
	r.write("docs/g.md", "# g\n")
	r.waitFile("docs/g.md")
}

// TestDiscoverSymlink: a linked spec file is a project-level finding, as at
// startup; it does not join.
func TestDiscoverSymlink(t *testing.T) {
	r := openDiscover(t, discoverProject(t, ""))
	target := filepath.Join(t.TempDir(), "elsewhere.md")
	require.NoError(t, os.WriteFile(target, []byte(discoverLater), 0o644))
	require.NoError(t, os.Symlink(target, r.abs("linked.spec.md")))
	r.waitFor("the finding", func(a *App) bool { return hasDiag(a, diag.CodeFileSymlink, "linked.spec.md") })
	r.do(func(a *App) { assert.Equal(t, []string{"main.spec.md"}, a.ws.Paths()) })
	// kqueue reports nothing for a removed symlink; the next re-glob sees it.
	require.NoError(t, os.Remove(r.abs("linked.spec.md")))
	r.write("sentinel.spec.md", "# sentinel\n")
	r.waitFile("sentinel.spec.md")
	r.waitFor("the finding to go", func(a *App) bool { return !hasDiag(a, diag.CodeFileSymlink, "linked.spec.md") })
}

// TestDiscoverUnreadable: a file that cannot be read says so and stays out; it
// joins when it can be read, and does not stop other files joining.
func TestDiscoverUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	r := openDiscover(t, discoverProject(t, ""))
	r.write("locked.spec.md", discoverLater)
	r.waitFile("locked.spec.md")
	require.NoError(t, os.Chmod(r.abs("locked.spec.md"), 0))
	t.Cleanup(func() { _ = os.Chmod(r.abs("locked.spec.md"), 0o644) })
	r.waitGone("locked.spec.md")
	r.waitFlash("locked.spec.md cannot be read")
	r.write("other.spec.md", "# other\n")
	r.waitFile("other.spec.md")
	require.NoError(t, os.Chmod(r.abs("locked.spec.md"), 0o644))
	r.waitFile("locked.spec.md")

	// One created unreadable, by something else: fails nothing on the way.
	require.NoError(t, os.WriteFile(r.abs("closed.spec.md"), []byte("# c\n"), 0))
	t.Cleanup(func() { _ = os.Chmod(r.abs("closed.spec.md"), 0o644) })
	r.waitFlash("closed.spec.md cannot be read")
	r.do(func(a *App) { assert.NotContains(t, a.ws.Paths(), "closed.spec.md") })
}

// TestDiscoverUnreadableModel: a model read error stays with the model, for lint.
func TestDiscoverUnreadableModel(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	r := openDiscover(t, discoverProject(t, "formal = [\"*.tla\"]\n"))
	r.write("N.tla", discoverModel)
	r.waitFile("N.tla")
	require.NoError(t, os.Chmod(r.abs("N.tla"), 0))
	t.Cleanup(func() { _ = os.Chmod(r.abs("N.tla"), 0o644) })
	r.write("sentinel.spec.md", "# sentinel\n")
	r.waitFile("sentinel.spec.md")
	r.do(func(a *App) {
		content, _ := a.render()
		assert.NotEmpty(t, content)
	})
}

// TestDiscoverWithCommitViewOpen: the commit view stays on top with its snapshot;
// the new file is there for what comes after.
func TestDiscoverWithCommitViewOpen(t *testing.T) {
	gitEnv(t)
	root := discoverProject(t, "")
	writeFile(t, root, ".gitignore", ".spec/\n")
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")
	writeFile(t, root, "main.spec.md", strings.Replace(discoverMain, "It MUST hold.", "It MUST hold still.", 1))
	r := openDiscover(t, root)
	var view *commitScreen
	r.do(func(a *App) {
		_ = a.openCommit() // the draft's command stays unrun: no provider is called
		var ok bool
		view, ok = a.top().(*commitScreen)
		require.True(t, ok, "flash: %s", a.flash)
		t.Cleanup(func() {
			if view.cancelDraft != nil {
				view.cancelDraft()
			}
		})
	})
	r.write("later.spec.md", discoverLater)
	r.waitFile("later.spec.md")
	r.do(func(a *App) {
		assert.Same(t, view, a.top(), "the view stays")
		content, _ := a.render()
		assert.NotEmpty(t, content)
	})
}

// TestReportWithoutAScan: a report of a file the workspace does not know, from
// something that did not glob (a replay), settles membership right there; the
// real watcher's later report of the same state is no news.
func TestReportWithoutAScan(t *testing.T) {
	root := discoverProject(t, "")
	a := newTestApp(t, testOptions(root))
	defer a.Close()
	writeFile(t, root, "later.spec.md", discoverLater)
	a.onFileChanged([]string{"later.spec.md"})
	assert.Contains(t, a.ws.Paths(), "later.spec.md")
	assert.Equal(t, "Found new file later.spec.md", a.flash)
	seq := a.flashSeq

	p, unreadable, warn := globProject(root, a.ws.Project)
	require.Empty(t, warn)
	again, err := loadScan(p, unreadable)
	require.NoError(t, err)
	send(a, fileChangedMsg{paths: []string{"later.spec.md"}, scan: again})
	assert.Equal(t, seq, a.flashSeq, "no second flash")

	require.NoError(t, os.Remove(filepath.Join(root, "later.spec.md")))
	a.onFileChanged([]string{"later.spec.md"})
	assert.NotContains(t, a.ws.Paths(), "later.spec.md")
	assert.Equal(t, "later.spec.md left the project", a.flash)

	writeFile(t, root, "spec.toml", "name = \"x\"\n")
	a.onFileChanged([]string{"spec.toml"})
	assert.Equal(t, "x", a.ws.Project.Name)
	assert.Equal(t, "Reloaded spec.toml", a.flash)

	writeFile(t, root, "spec.toml", "broken = [\n")
	a.onFileChanged([]string{"spec.toml"})
	assert.Equal(t, "x", a.ws.Project.Name)
	assert.Contains(t, a.flash, "spec.toml has an error")
}

// TestWatcherReportsSetChanges: a re-glob's additions and removals are
// in the report's paths, where a session recorder reads them.
func TestWatcherReportsSetChanges(t *testing.T) {
	root := discoverProject(t, "")
	w := newWatcher(root, []string{"main.spec.md"})
	defer w.Close()
	require.NoError(t, os.WriteFile(filepath.Join(root, "n.spec.md"), []byte("# n\n"), 0o644))
	assert.Equal(t, []string{"n.spec.md"}, waitChange(t, w))
	require.NoError(t, os.Remove(filepath.Join(root, "n.spec.md")))
	assert.Equal(t, []string{"n.spec.md"}, waitChange(t, w))
	require.NoError(t, os.WriteFile(filepath.Join(root, "spec.toml"), []byte("files = [\n"), 0o644))
	assert.Equal(t, []string{"spec.toml"}, waitChange(t, w))
}

// TestWatcherStopsOnSettingsReload: the old watcher is gone, the new one runs.
func TestWatcherStopsOnSettingsReload(t *testing.T) {
	root := discoverProject(t, "")
	r := openDiscover(t, root)
	var old *watcher
	r.do(func(a *App) { old = a.watcher })
	r.write("spec.toml", "name = \"x\"\n")
	require.NoError(t, r.d.Send(settingsSavedMsg{scope: "Project"}))
	requireClosed(t, old.done, "the old watcher")
	r.write("later.spec.md", discoverLater)
	r.waitFile("later.spec.md")
}

// TestDiscoverWatchOff: Watch off leaves the watcher out.
func TestDiscoverWatchOff(t *testing.T) {
	root := discoverProject(t, "")
	r := openDiscover(t, root, func(o *Options) { o.Watch = false })
	r.do(func(a *App) { assert.Nil(t, a.watcher) })
}
