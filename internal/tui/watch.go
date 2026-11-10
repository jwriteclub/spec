// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/jwriteclub/spec/internal/project"
)

// fileChangedMsg reports what changed on disk. paths are the project files
// whose content changed, or that joined or left the project, and spec.toml when
// it changed. scan is the re-glob behind those last two (discover.go); nil when
// the reporter did not need one. from is the watcher that made the report, which
// a settings reload may since have replaced; nil for a report that did not come
// from a watcher (a replay).
type fileChangedMsg struct {
	paths []string
	scan  *projectScan
	from  *watcher
}

// Watch timing.
const (
	watchSettle    = 100 * time.Millisecond // gather a burst of events
	watchMaxSettle = time.Second            // a steady writer delays a report no longer
	pollEvery      = time.Second            // polling fallback
	// emptySettle is how long an emptied spec.toml must stay so before it counts:
	// that is what a writer leaves between truncating and writing, and on a busy
	// machine that gap was measured at over 270 ms.
	emptySettle = time.Second
	// scanRounds bounds re-globbing while directories keep appearing.
	scanRounds = 8
)

// watchConfig says what a watcher watches.
type watchConfig struct {
	root string
	// paths are project-relative slash paths whose changes are reported, even
	// when no glob selects them (an open draft that left the project).
	paths []string
	// base is the project the caller knows; the first re-glob reports any
	// difference from it, closing the gap before the watches existed. Nil opens
	// one to start from.
	base *project.Project
	// poll skips fsnotify.
	poll bool
	// addWatch replaces fsnotify's Add (tests).
	addWatch func(fw *fsnotify.Watcher, dir string) error
	// settle replaces the wait for spec.toml to settle (tests).
	settle func(d time.Duration)
}

// watcher reports changes to the project: fsnotify on the directories that
// can hold its files (Project.WatchDirs), or polling when fsnotify is
// unavailable or a watch cannot be added. An event is only a hint: what joins
// or leaves the project is decided by re-globbing, here and not in Update.
type watcher struct {
	root string
	// paths is what content changes are reported for. It and everything below
	// it belong to the watcher's goroutine.
	paths  map[string]bool
	last   *project.Project // the file set last reported
	warned string           // the warning last reported, to say it once
	// unreadable lists the files last reported as impossible to read, likewise.
	unreadable []string
	addWatch   func(fw *fsnotify.Watcher, dir string) error
	settle     func(d time.Duration)
	// open is the UI's open documents as of its last scan (setOpen): reported
	// whatever the globs say, since a buffer depends on them.
	open atomic.Pointer[map[string]bool]

	ch   chan fileChangedMsg
	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

// newWatcher starts watching paths under root and the project there.
func newWatcher(root string, paths []string) *watcher {
	return openWatcher(watchConfig{root: root, paths: paths})
}

// startWatcher starts the App's watcher, from the project as it now stands.
func (a *App) startWatcher() {
	a.watcher = openWatcher(watchConfig{root: a.ws.Root, paths: a.ws.WatchPaths(), base: a.ws.Project, poll: a.opts.WatchPoll})
}

// openWatcher starts a watcher.
func openWatcher(cfg watchConfig) *watcher {
	w := &watcher{root: cfg.root, paths: map[string]bool{}, last: cfg.base, addWatch: cfg.addWatch, settle: cfg.settle,
		ch: make(chan fileChangedMsg, 1), done: make(chan struct{})}
	for _, p := range cfg.paths {
		w.paths[p] = true
	}
	if w.addWatch == nil {
		w.addWatch = func(fw *fsnotify.Watcher, dir string) error { return fw.Add(dir) }
	}
	if w.settle == nil {
		w.settle = w.waitSettle
	}
	if w.last == nil {
		w.last, _, _ = globProject(w.root, nil) // no project: the paths alone
	}
	var fw *fsnotify.Watcher
	if !cfg.poll {
		fw = w.startNotify()
	}
	w.wg.Add(1)
	go w.run(fw)
	return w
}

// startNotify makes an fsnotify watcher with its first watches, or nil.
func (w *watcher) startNotify() *fsnotify.Watcher {
	fw, err := fsnotify.NewWatcher()
	if err == nil {
		if _, err = w.syncWatches(fw, w.last); err != nil {
			_ = fw.Close()
		}
	}
	if err != nil {
		lg.WithError(err).Warn("fsnotify unavailable; polling for file changes")
		return nil
	}
	return fw
}

// run is the watcher's goroutine.
func (w *watcher) run(fw *fsnotify.Watcher) {
	defer w.wg.Done()
	if fw != nil {
		err := w.notify(fw)
		_ = fw.Close()
		if err == nil {
			return
		}
		lg.WithError(err).Warn("a directory cannot be watched; polling for file changes")
	}
	w.poll()
}

// setOpen tells the watcher which documents are open. Safe from any goroutine.
func (w *watcher) setOpen(paths map[string]bool) {
	if w != nil {
		w.open.Store(&paths)
	}
}

// isOpen reports whether rel is an open document.
func (w *watcher) isOpen(rel string) bool {
	open := w.open.Load()
	return open != nil && (*open)[rel]
}

// known returns every path whose content changes are reported.
func (w *watcher) known() map[string]bool {
	out := maps.Clone(w.paths)
	if open := w.open.Load(); open != nil {
		maps.Copy(out, *open)
	}
	return out
}

// syncWatches makes fw watch what p needs and the directories of the known paths
// and of p's files, and reports whether it added any. A directory gone or closed
// to us meanwhile is no failure: it can hold nothing to read.
func (w *watcher) syncWatches(fw *fsnotify.Watcher, p *project.Project) (added bool, err error) {
	want := map[string]bool{}
	dirOf := func(rel string) {
		want[filepath.Dir(filepath.Join(w.root, filepath.FromSlash(rel)))] = true
	}
	for rel := range w.known() {
		dirOf(rel)
	}
	if p != nil {
		for rel := range membersOf(p) {
			dirOf(rel)
		}
		for _, d := range p.WatchDirs() {
			want[filepath.Join(w.root, filepath.FromSlash(d))] = true
		}
	}
	for _, have := range fw.WatchList() {
		if want[have] {
			delete(want, have)
		} else if rerr := fw.Remove(have); rerr != nil {
			lg.WithError(rerr).WithField("dir", have).Debug("removing a watch")
		}
	}
	for d := range want {
		switch aerr := w.addWatch(fw, d); {
		case aerr == nil:
			added = true
		case errors.Is(aerr, fs.ErrNotExist), errors.Is(aerr, fs.ErrPermission):
			lg.WithError(aerr).WithField("dir", d).Debug("skipping a directory")
		default:
			return added, aerr
		}
	}
	return added, nil
}

// waitSettle waits d, or until the watcher stops.
func (w *watcher) waitSettle(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-w.done:
	}
}

// settledConfig reads spec.toml and reports whether it can be judged: unchanged
// from the project in use, or not written during a pause (longer for an empty
// file). A writer that truncates and then writes leaves an empty file or a part of
// one for a moment, and either may parse: an empty spec.toml is a valid one that
// selects the default glob. A watcher looks whenever it likes, so it must not take
// what it sees for what was meant. The modification time counts as well as the
// bytes: a writer going round again can show the same part twice. Whatever a writer
// does during the pause reports an event or shows at the next poll, so a look that
// is put off is not lost. A writer stalled for longer than the pause is not seen;
// its next write corrects the result.
func (w *watcher) settledConfig() (src []byte, settled bool) {
	path := filepath.Join(w.root, project.ConfigFile)
	look := func() ([]byte, stamp) {
		st, err := os.Stat(path)
		if err != nil {
			return nil, stamp{}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, stamp{}
		}
		return b, stamp{st.ModTime(), st.Size()}
	}
	src, at := look()
	if w.last == nil && src == nil || w.last != nil && bytes.Equal(src, w.last.ConfigSource()) {
		return src, true
	}
	pause := watchSettle
	if src != nil && len(src) == 0 {
		pause = emptySettle
	}
	w.settle(pause)
	again, then := look()
	return src, bytes.Equal(src, again) && at == then
}

// rescan re-globs the project, adding the watches each round finds it needs and
// globbing again after adding any, for files that landed before the watch. It
// returns a scan and the paths to report when something changed (the set, the
// config or a warning about it), and an error only when a watch cannot be added.
// While spec.toml is changing it reports nothing: the project in use stays.
func (w *watcher) rescan(fw *fsnotify.Watcher) (*projectScan, []string, error) {
	src, settled := w.settledConfig()
	if !settled {
		return nil, nil, nil
	}
	var p *project.Project
	var unreadable []string
	var warn string
	for range scanRounds {
		p, unreadable, warn = globProject(w.root, w.last)
		if p != nil && !bytes.Equal(p.ConfigSource(), src) {
			return nil, nil, nil // a writer began after the pause: look again at its event
		}
		if p == nil || fw == nil {
			break
		}
		added, err := w.syncWatches(fw, p)
		if err != nil {
			return nil, nil, err
		}
		if !added {
			break
		}
	}
	if warn != "" {
		if warn == w.warned {
			return nil, nil, nil
		}
		w.warned = warn
		return &projectScan{warn: warn}, []string{project.ConfigFile}, nil
	}
	w.warned = ""
	if p == nil {
		return nil, nil, nil
	}
	if w.last == nil {
		w.last = p
		return nil, nil, nil
	}
	news := slices.DeleteFunc(slices.Clone(unreadable), func(f string) bool { return slices.Contains(w.unreadable, f) })
	w.unreadable = unreadable
	if sameProject(w.last, p) {
		if len(news) > 0 {
			return &projectScan{unreadable: news}, nil, nil
		}
		return nil, nil, nil
	}
	scan, err := loadScan(p, news)
	if err != nil {
		lg.WithError(err).Warn("reading the project's files") // the next event looks again
		return nil, nil, nil
	}
	changed := w.noteSet(p)
	w.last = p
	return scan, changed, nil
}

// noteSet moves the known paths from the last file set to p's and returns what
// joined and left, with spec.toml if it changed.
func (w *watcher) noteSet(p *project.Project) []string {
	var changed []string
	now := membersOf(p)
	for path := range now {
		if !w.paths[path] {
			w.paths[path] = true
			changed = append(changed, path)
		}
	}
	for path := range membersOf(w.last) {
		if !now[path] {
			delete(w.paths, path)
			changed = append(changed, path)
		}
	}
	if !bytes.Equal(w.last.ConfigSource(), p.ConfigSource()) {
		changed = append(changed, project.ConfigFile)
	}
	return changed
}

// flush reports a settled batch: the content changes pending and, when rescan
// is set, what a re-glob finds.
func (w *watcher) flush(fw *fsnotify.Watcher, pending map[string]bool, rescan bool) error {
	var scan *projectScan
	var err error
	if rescan {
		var changed []string
		scan, changed, err = w.rescan(fw)
		for _, p := range changed {
			pending[p] = true
		}
	}
	w.send(pending, scan)
	return err
}

// send delivers a batch, merging it with an undelivered one.
func (w *watcher) send(changed map[string]bool, scan *projectScan) {
	if len(changed) == 0 && scan == nil {
		return
	}
	select {
	case prev := <-w.ch:
		for _, p := range prev.paths {
			changed[p] = true
		}
		scan = mergeScan(prev.scan, scan)
	default:
	}
	out := fileChangedMsg{scan: scan, from: w}
	for p := range changed {
		out.paths = append(out.paths, p)
	}
	sort.Strings(out.paths)
	select {
	case w.ch <- out:
	case <-w.done:
	}
}

// hint reads one event: a project file whose content may have changed, and
// whether the file set may have.
func (w *watcher) hint(fw *fsnotify.Watcher, ev fsnotify.Event) (changed string, rescan bool) {
	r, err := filepath.Rel(w.root, ev.Name)
	if err != nil {
		return "", false
	}
	rel := filepath.ToSlash(r)
	switch {
	case filepath.Base(r) == project.ConfigFile:
		return "", true // this project's settings, or a nested project now
	case w.paths[rel] || w.isOpen(rel):
		return rel, ev.Op != fsnotify.Write
	case ev.Has(fsnotify.Create):
		// A dangling symlink (an Emacs lock) ends kqueue's listing of the directory,
		// so this may be the only event for the files sorting after it; it says so
		// again with each change there, until it goes. Hence a look, though it never
		// joins.
		if st, err := os.Lstat(ev.Name); err == nil && (st.IsDir() || st.Mode()&fs.ModeSymlink != 0) {
			return "", true
		}
	case ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename):
		// A directory's files get no event when it is moved away, and kqueue has
		// already dropped its watch: what sits below the name is the only sign.
		if w.knownBelow(rel) || watchesBelow(fw, ev.Name) {
			return "", true
		}
	}
	return "", w.last != nil && w.last.Selects(rel)
}

// knownBelow reports whether a known file sits below the project-relative rel.
func (w *watcher) knownBelow(rel string) bool {
	below := func(p string) bool { return strings.HasPrefix(p, rel+"/") }
	if w.last != nil && (slices.ContainsFunc(w.last.Files, below) || slices.ContainsFunc(w.last.Formal, below)) {
		return true
	}
	for p := range w.paths {
		if below(p) {
			return true
		}
	}
	return false
}

// watchesBelow reports whether fw watches name or a directory under it.
func watchesBelow(fw *fsnotify.Watcher, name string) bool {
	for _, d := range fw.WatchList() {
		if d == name || strings.HasPrefix(d, name+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// notify runs the fsnotify loop. It returns nil when the watcher stops, or the
// error of a watch that could not be added.
func (w *watcher) notify(fw *fsnotify.Watcher) error {
	// The first re-glob closes the gap since the caller's project was read.
	if err := w.flush(fw, map[string]bool{}, true); err != nil {
		return err
	}
	pending := map[string]bool{}
	rescan := false
	var first time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	arm := func() {
		now := time.Now()
		if first.IsZero() {
			first = now
		}
		timer.Reset(min(watchSettle, first.Add(watchMaxSettle).Sub(now)))
	}
	for {
		select {
		case <-w.done:
			return nil
		case ev, ok := <-fw.Events:
			if !ok {
				return nil
			}
			p, again := w.hint(fw, ev)
			if p != "" {
				pending[p] = true
			}
			rescan = rescan || again
			if p != "" || again {
				arm()
			}
		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			// Events may be lost. A re-glob compares only the lists, so ask about
			// the content of every file as well.
			lg.WithError(err).Warn("file watcher")
			rescan = true
			maps.Copy(pending, w.known())
			arm()
		case <-timer.C:
			err := w.flush(fw, pending, rescan)
			if err != nil {
				return err
			}
			pending, rescan, first = map[string]bool{}, false, time.Time{}
		}
	}
}

// stamp is what polling compares.
type stamp struct {
	mod  time.Time
	size int64
}

// poll runs the polling fallback: it stats the known paths and re-globs on every
// tick.
func (w *watcher) poll() {
	stamps := map[string]stamp{}
	read := func(p string) stamp {
		st, err := os.Stat(filepath.Join(w.root, filepath.FromSlash(p)))
		if err != nil {
			return stamp{}
		}
		return stamp{st.ModTime(), st.Size()}
	}
	for p := range w.known() {
		stamps[p] = read(p)
	}
	_ = w.flush(nil, map[string]bool{}, true)
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-t.C:
			changed := map[string]bool{}
			for p := range w.known() {
				if s, seen := read(p), stamps[p]; s != seen {
					stamps[p] = s
					changed[p] = true
				}
			}
			_ = w.flush(nil, changed, true)
		}
	}
}

// next returns a command that waits for the next batch of changes.
func (w *watcher) next() tea.Cmd {
	if w == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case m := <-w.ch:
			return m
		case <-w.done:
			return nil
		}
	}
}

// Close stops the watcher and waits for its goroutine.
func (w *watcher) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() { close(w.done) })
	w.wg.Wait()
}
