// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jwriteclub/spec/internal/baseline"
)

// headMovedMsg says HEAD may point at another commit (a commit, reset or
// checkout outside spec ui), so the HEAD baseline is stale.
type headMovedMsg struct{}

// headWatcher reports when HEAD may have moved. The project watcher sees the
// project's files only, and the git directory is none of them (in a linked
// worktree it is not even below the project). So this polls the few files git
// rewrites whenever HEAD moves: HEAD itself, its reflog, packed-refs and the
// checked-out branch's ref. A handful of stats a second costs nothing, and a
// false alarm only reloads the baseline.
type headWatcher struct {
	every time.Duration
	ch    chan headMovedMsg
	done  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup
}

// newHeadWatcher starts watching the HEAD of the repository holding root.
func newHeadWatcher(root string, every time.Duration) *headWatcher {
	w := &headWatcher{every: every, ch: make(chan headMovedMsg, 1), done: make(chan struct{})}
	w.wg.Add(1)
	go w.run(root)
	return w
}

// gitDirs returns the repository's git directory (HEAD, its reflog) and
// common directory (refs, packed-refs) for root, as absolute paths.
func gitDirs(ctx context.Context, root string) (gitDir, commonDir string, err error) {
	ctx, cancel := context.WithTimeout(ctx, baselineTimeout)
	defer cancel()
	out, err := baseline.Loader{}.Run(ctx, root, nil, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return "", "", fmt.Errorf("git rev-parse: unexpected output %q", out)
	}
	return lines[0], lines[1], nil
}

// headStamp summarizes what moves with HEAD: the symbolic ref HEAD holds and
// the size and time of each file that changes when HEAD or its branch moves.
func headStamp(gitDir, commonDir string) string {
	var b strings.Builder
	head, _ := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	b.Write(head)
	files := []string{filepath.Join(gitDir, "HEAD"), filepath.Join(gitDir, "logs", "HEAD"), filepath.Join(commonDir, "packed-refs")}
	if ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: "); ok {
		files = append(files, filepath.Join(commonDir, filepath.FromSlash(ref)))
	}
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%s %d %d\n", f, st.Size(), st.ModTime().UnixNano())
		} else {
			fmt.Fprintf(&b, "%s -\n", f)
		}
	}
	return b.String()
}

// run is the watcher's goroutine.
func (w *headWatcher) run(root string) {
	defer w.wg.Done()
	// Close cancels a git that is still starting.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-w.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	gitDir, commonDir, err := gitDirs(ctx, root)
	cancel()
	if err != nil {
		lg.WithError(err).Debug("no git directory: HEAD is not watched")
		return
	}
	last := headStamp(gitDir, commonDir)
	tick := time.NewTicker(w.every)
	defer tick.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-tick.C:
			now := headStamp(gitDir, commonDir)
			if now == last {
				continue
			}
			last = now
			select {
			case w.ch <- headMovedMsg{}:
			default: // one report is already waiting
			}
		}
	}
}

// next returns a command that waits for the next report.
func (w *headWatcher) next() tea.Cmd {
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
func (w *headWatcher) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() { close(w.done) })
	w.wg.Wait()
}
