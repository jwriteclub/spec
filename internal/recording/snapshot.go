// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/project"
)

// SnapshotInfo is what Snapshot wrote.
type SnapshotInfo struct {
	// Files is the number of files in workspace.tar, Bytes their size.
	Files int
	Bytes int64
	// Git is true when the project is in a git work tree; Head is HEAD's
	// commit ("" when it has none).
	Git  bool
	Head string
	// Paths are the files in workspace.tar (project-relative, slashes).
	Paths []string
}

// tarTime is every tar member's time, so a snapshot's bytes depend only
// on the files.
var tarTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Snapshot writes the starting state of the project at root into dir:
// workspace.tar (the working tree: tracked files and untracked ones git
// does not ignore, and .spec/ but for its locks, log and temp files),
// head.tar (HEAD's version of the files under root, when there is a
// commit) and index.tar (the staged version of the files whose index entry
// differs from HEAD, when there are any). Without git, the working tree is
// walked, skipping any .git and the top .dev. Anything under dir itself is skipped.
func Snapshot(ctx context.Context, git baseline.Loader, root, dir string) (SnapshotInfo, error) {
	var info SnapshotInfo
	root, err := filepath.Abs(root)
	if err != nil {
		return info, err
	}
	skipDir := ""
	if abs, err := filepath.Abs(dir); err == nil {
		if rel, err := filepath.Rel(root, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			skipDir = filepath.ToSlash(rel)
		}
	}
	keep := func(p string) bool {
		if skipDir != "" && (p == skipDir || strings.HasPrefix(p, skipDir+"/")) {
			return false
		}
		return !skippedSpecFile(p)
	}

	out, gitErr := git.Run(ctx, root, nil, "rev-parse", "--is-inside-work-tree")
	info.Git = gitErr == nil && strings.TrimSpace(string(out)) == "true"
	var paths []string
	if info.Git {
		out, err := git.Run(ctx, root, nil, "ls-files", "-co", "--exclude-standard", "-z")
		if err != nil {
			return info, err
		}
		paths = splitZ(out)
		spec, err := walk(root, project.LockDir)
		if err != nil {
			return info, err
		}
		paths = append(paths, spec...)
	} else {
		paths, err = walk(root, "")
		if err != nil {
			return info, err
		}
	}
	paths = uniqueSorted(paths, keep)

	var ws bytes.Buffer
	tw := tar.NewWriter(&ws)
	for _, p := range paths {
		abs := filepath.Join(root, filepath.FromSlash(p))
		st, err := os.Lstat(abs)
		if err != nil {
			continue // a tracked file deleted in the working tree
		}
		switch {
		case st.Mode().IsRegular():
			data, err := os.ReadFile(abs)
			if err != nil {
				return info, err
			}
			if err := addFile(tw, p, data, int64(st.Mode().Perm())); err != nil {
				return info, err
			}
			info.Bytes += int64(len(data))
		case st.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(abs)
			if err != nil {
				return info, err
			}
			if err := addLink(tw, p, target); err != nil {
				return info, err
			}
		default:
			continue // a directory (a submodule), a socket
		}
		info.Files++
		info.Paths = append(info.Paths, p)
	}
	if err := tw.Close(); err != nil {
		return info, err
	}
	if err := os.WriteFile(filepath.Join(dir, WorkspaceTar), ws.Bytes(), 0o644); err != nil {
		return info, err
	}
	if !info.Git {
		return info, nil
	}

	head, err := git.Run(ctx, root, nil, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil {
		return info, nil // no commit yet: no head.tar, no index.tar
	}
	info.Head = strings.TrimSpace(string(head))
	out, err = git.Run(ctx, root, nil, "ls-tree", "-r", "-z", "HEAD")
	if err != nil {
		return info, err
	}
	headEntries := parseEntries(out, false, keep)
	if err := writeBlobTar(ctx, git, root, filepath.Join(dir, HeadTar), headEntries); err != nil {
		return info, err
	}
	out, err = git.Run(ctx, root, nil, "ls-files", "-s", "-z")
	if err != nil {
		return info, err
	}
	inHead := map[string]string{}
	for _, e := range headEntries {
		inHead[e.path] = e.mode + " " + e.sha
	}
	var staged []treeEntry
	for _, e := range parseEntries(out, true, keep) {
		if inHead[e.path] != e.mode+" "+e.sha {
			staged = append(staged, e)
		}
	}
	if len(staged) > 0 {
		if err := writeBlobTar(ctx, git, root, filepath.Join(dir, IndexTar), staged); err != nil {
			return info, err
		}
	}
	return info, nil
}

// skippedSpecFile reports whether p is one of .spec's files a snapshot
// leaves out: the locks, the UI's log and temp files.
func skippedSpecFile(p string) bool {
	if !strings.HasPrefix(p, project.LockDir+"/") {
		return false
	}
	switch p {
	case project.LockFile, project.UILockFile, project.LockDir + "/ui.log":
		return true
	}
	base := pathBase(p)
	return strings.HasPrefix(base, ".tmp-") || strings.HasSuffix(base, ".tmp") || strings.HasPrefix(base, ".spec-ui-")
}

func pathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// walk lists the files under root/sub (project-relative, slashes),
// skipping .git at any depth and .dev at the top. A missing sub is empty.
func walk(root, sub string) ([]string, error) {
	start := filepath.Join(root, filepath.FromSlash(sub))
	var out []string
	err := filepath.WalkDir(start, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == start && os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if path != start && isGitName(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil // a worktree's or a submodule's .git file
		}
		if d.IsDir() {
			if rel == ".dev" {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return out, err
}

func uniqueSorted(paths []string, keep func(string) bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if p == "" || seen[p] || !keep(p) {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func splitZ(b []byte) []string {
	var out []string
	for _, s := range strings.Split(string(b), "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// treeEntry is a blob of git ls-tree or ls-files -s.
type treeEntry struct {
	mode, sha, path string
}

// parseEntries parses `ls-tree -r -z` ("mode type sha\tpath") or, with
// stage, `ls-files -s -z` ("mode sha stage\tpath") output, keeping blobs
// (and symlinks) only.
func parseEntries(out []byte, stage bool, keep func(string) bool) []treeEntry {
	var es []treeEntry
	for _, rec := range splitZ(out) {
		tab := strings.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		f := strings.Fields(rec[:tab])
		path := rec[tab+1:]
		var e treeEntry
		switch {
		case stage && len(f) == 3:
			if f[2] != "0" {
				continue // a merge conflict's stages
			}
			e = treeEntry{mode: f[0], sha: f[1], path: path}
		case !stage && len(f) == 3:
			if f[1] != "blob" {
				continue
			}
			e = treeEntry{mode: f[0], sha: f[2], path: path}
		default:
			continue
		}
		if e.mode == "160000" || !keep(e.path) {
			continue
		}
		es = append(es, e)
	}
	return es
}

// writeBlobTar writes the blobs of es into a tar at path.
func writeBlobTar(ctx context.Context, git baseline.Loader, root, path string, es []treeEntry) error {
	var in bytes.Buffer
	for _, e := range es {
		in.WriteString(e.sha + "\n")
	}
	out, err := git.Run(ctx, root, &in, "cat-file", "--batch")
	if err != nil {
		return err
	}
	r := bufio.NewReader(bytes.NewReader(out))
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range es {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("git cat-file: %w", err)
		}
		f := strings.Fields(hdr)
		if len(f) != 3 {
			return fmt.Errorf("git cat-file: unexpected %q", strings.TrimSpace(hdr))
		}
		size, err := strconv.Atoi(f[2])
		if err != nil {
			return fmt.Errorf("git cat-file: %w", err)
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(r, data); err != nil {
			return fmt.Errorf("git cat-file: %w", err)
		}
		if _, err := r.ReadByte(); err != nil { // the newline after the content
			return fmt.Errorf("git cat-file: %w", err)
		}
		switch e.mode {
		case "120000":
			err = addLink(tw, e.path, string(data))
		case "100755":
			err = addFile(tw, e.path, data, 0o755)
		default:
			err = addFile(tw, e.path, data, 0o644)
		}
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func addFile(tw *tar.Writer, name string, data []byte, mode int64) error {
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(data)), Mode: mode, ModTime: tarTime, Format: tar.FormatPAX}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func addLink(tw *tar.Writer, name, target string) error {
	return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777, ModTime: tarTime, Format: tar.FormatPAX})
}

// ReadTar returns the regular files of the tar at path by name (symlinks
// map to their target, prefixed "-> "). A missing file is an empty map
// and a nil error.
func ReadTar(path string) (map[string][]byte, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out := map[string][]byte{}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch h.Typeflag {
		case tar.TypeReg:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			out[h.Name] = b
		case tar.TypeSymlink:
			out[h.Name] = []byte("-> " + h.Linkname)
		}
	}
}
