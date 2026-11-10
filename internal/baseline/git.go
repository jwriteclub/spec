// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package baseline reads the spec files of a project as they were at a git
// revision (or in the index), compares two specs anchor by anchor, and
// defines the Spec-Change / Spec-Rename commit trailer grammar that records
// those differences.
//
// The comparison is by anchor name over the whole project, so moving an
// anchor between files is not a change, and renames are detected with the
// shape hash so that a rename moves no referencer into "changed".
package baseline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/project"
)

// State says what kind of baseline Load produced.
type State string

// Baseline states.
const (
	// StateOK: the baseline was read from a commit or the index.
	StateOK State = "ok"
	// StateUnborn: the repository has no commits yet. The baseline is empty,
	// so every anchor in the current spec is "added".
	StateUnborn State = "unborn"
	// StateUnavailable: git is missing or the project is not in a git work
	// tree. There is no baseline (Spec is nil); diff, ls --changed and
	// check must fail with no_baseline.
	StateUnavailable State = "unavailable"
)

// Source says where a baseline is read from.
type Source struct {
	// Index reads the git index (staged content) instead of a commit.
	Index bool
	// Ref is the revision to read; "" means HEAD. Ignored when Index is set.
	Ref string
}

// String describes the source for messages: "HEAD", "main~3" or "the index".
func (s Source) String() string {
	if s.Index {
		return "the index"
	}
	if s.Ref == "" {
		return "HEAD"
	}
	return s.Ref
}

// Baseline is a project's spec files at a revision or in the index.
type Baseline struct {
	State  State
	Source Source
	// Commit is the full commit hash read (StateOK from a ref); "" for the
	// index, unborn and unavailable.
	Commit string
	// Reason explains StateUnborn ("no commits yet") and StateUnavailable
	// ("git is not installed", "not a git repository").
	Reason string
	// Spec holds the baseline files, parsed under their project-relative
	// paths. It is empty for StateUnborn and nil for StateUnavailable.
	Spec *mdspec.Spec
	// Others are the project's other files a commit carries, by
	// project-relative path: spec.toml and the files the working tree's
	// formal patterns select as models. Nil when there are none.
	Others map[string][]byte
}

// BadRefError says the requested revision does not name a commit (CLI error
// code bad_ref).
type BadRefError struct {
	Ref    string
	Detail string
}

func (e *BadRefError) Error() string {
	msg := fmt.Sprintf("%q is not a commit in this repository", e.Ref)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Loader runs git. The zero value runs "git" from PATH with the process
// environment, which is what the CLI uses; tests set Git and Env.
type Loader struct {
	// Git is the git executable; "" means "git".
	Git string
	// Env is appended to the process environment for every git call (later
	// entries win), for example GIT_CONFIG_GLOBAL=/dev/null in tests.
	Env []string
}

// gitErr is a failed git invocation.
type gitErr struct {
	args   []string
	stderr string
	err    error
}

func (e *gitErr) Error() string {
	msg := strings.TrimSpace(e.stderr)
	if msg == "" {
		msg = e.err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.args, " "), msg)
}

func (e *gitErr) Unwrap() error { return e.err }

// Run runs git with args in dir and returns its stdout. A failure carries
// git's stderr.
func (l Loader) Run(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	return l.run(ctx, dir, stdin, args...)
}

func (l Loader) run(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	bin := l.Git
	if bin == "" {
		bin = "git"
	}
	full := append([]string{"-c", "core.quotepath=off", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"), l.Env...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, &gitErr{args: args, stderr: errb.String(), err: err}
	}
	return out.Bytes(), nil
}

// missingGit reports whether err means the git executable does not exist.
func missingGit(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

type blobEntry struct {
	path string // project-relative
	sha  string
}

// Load reads the project's spec files from src.
//
// The file list comes from the baseline itself (git ls-tree for a commit,
// git ls-files --stage for the index) so that deleted and renamed spec
// files keep their old anchors, filtered by the working tree's spec.toml
// through project.Select. Paths in git are relative to the git top level;
// they are mapped to project-relative paths by the project's prefix inside
// the repository.
//
// A missing git or a project outside a git work tree gives StateUnavailable
// and a nil error. A repository without commits gives StateUnborn for a
// ref source. An unknown ref returns *BadRefError. Other git failures are
// returned as errors.
func (l Loader) Load(ctx context.Context, p *project.Project, src Source) (*Baseline, error) {
	b := &Baseline{Source: src}
	unavailable := func(reason string) (*Baseline, error) {
		b.State = StateUnavailable
		b.Reason = reason
		return b, nil
	}

	out, err := l.run(ctx, p.Root, nil, "rev-parse", "--is-inside-work-tree", "--show-prefix")
	if err != nil {
		if missingGit(err) {
			return unavailable("git is not installed")
		}
		return unavailable("not a git repository (or no git work tree here)")
	}
	lines := strings.SplitN(string(out), "\n", 3)
	if strings.TrimSpace(lines[0]) != "true" {
		return unavailable("not inside a git work tree")
	}
	prefix := ""
	if len(lines) > 1 {
		prefix = strings.TrimSpace(lines[1]) // "" or "sub/dir/"
	}

	var entries []blobEntry
	if src.Index {
		out, err := l.run(ctx, p.Root, nil, "ls-files", "--stage", "-z", "--full-name", "--", ".")
		if err != nil {
			return nil, err
		}
		entries = parseListing(out, prefix, true)
	} else {
		ref := src.Ref
		if ref == "" {
			ref = "HEAD"
		}
		if strings.HasPrefix(ref, "-") {
			return nil, &BadRefError{Ref: ref, Detail: "a revision cannot start with '-'"}
		}
		out, err := l.run(ctx, p.Root, nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		if err != nil {
			var ge *gitErr
			if errors.As(err, &ge) && !missingGit(err) {
				if ref == "HEAD" && l.unbornHEAD(ctx, p.Root) {
					b.State = StateUnborn
					b.Reason = "no commits yet"
					b.Spec = mdspec.Build(nil)
					return b, nil
				}
				return nil, &BadRefError{Ref: src.Ref, Detail: "unknown revision"}
			}
			return nil, err
		}
		b.Commit = strings.TrimSpace(string(out))
		tree, err := l.run(ctx, p.Root, nil, "ls-tree", "-r", "-z", "--full-name", b.Commit, "--", ".")
		if err != nil {
			return nil, err
		}
		entries = parseListing(tree, prefix, false)
	}

	candidates := make([]string, len(entries))
	byPath := make(map[string]string, len(entries))
	for i, e := range entries {
		candidates[i] = e.path
		byPath[e.path] = e.sha
	}
	selected := p.Select(candidates)
	isSpec := make(map[string]bool, len(selected))
	for _, rel := range selected {
		isSpec[rel] = true
	}
	var others []string
	for _, rel := range candidates {
		if !isSpec[rel] && (rel == project.ConfigFile || p.IsModelPath(rel)) {
			others = append(others, rel)
		}
	}
	shas := make([]string, 0, len(selected)+len(others))
	for _, rel := range append(append([]string(nil), selected...), others...) {
		shas = append(shas, byPath[rel])
	}
	blobs, err := l.readBlobs(ctx, p.Root, shas)
	if err != nil {
		return nil, err
	}
	files := make([]*mdspec.File, len(selected))
	for i, rel := range selected {
		files[i] = mdspec.ParseFile(rel, blobs[i])
	}
	for i, rel := range others {
		if b.Others == nil {
			b.Others = map[string][]byte{}
		}
		b.Others[rel] = blobs[len(selected)+i]
	}
	b.State = StateOK
	b.Spec = mdspec.Build(files)
	return b, nil
}

// unbornHEAD reports whether HEAD is a symbolic ref to a branch that has no
// commits.
func (l Loader) unbornHEAD(ctx context.Context, dir string) bool {
	if _, err := l.run(ctx, dir, nil, "symbolic-ref", "-q", "HEAD"); err != nil {
		return false
	}
	_, err := l.run(ctx, dir, nil, "rev-parse", "--verify", "--quiet", "HEAD")
	return err != nil
}

// parseListing parses NUL-separated `ls-tree -r -z` (index false) or
// `ls-files --stage -z` (index true) output, keeping regular blobs inside
// prefix and returning project-relative paths.
func parseListing(out []byte, prefix string, index bool) []blobEntry {
	var res []blobEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		meta := strings.Fields(string(rec[:tab]))
		path := string(rec[tab+1:])
		var mode, sha string
		if index {
			// <mode> <sha> <stage>
			if len(meta) != 3 || meta[2] != "0" {
				continue
			}
			mode, sha = meta[0], meta[1]
		} else {
			// <mode> <type> <sha>
			if len(meta) != 3 || meta[1] != "blob" {
				continue
			}
			mode, sha = meta[0], meta[2]
		}
		if mode != "100644" && mode != "100755" {
			continue // symlinks (120000) and submodules (160000)
		}
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		res = append(res, blobEntry{path: strings.TrimPrefix(path, prefix), sha: sha})
	}
	return res
}

// readBlobs returns the contents of the blobs, in order, using one
// `git cat-file --batch` process.
func (l Loader) readBlobs(ctx context.Context, dir string, shas []string) ([][]byte, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	var in bytes.Buffer
	for _, s := range shas {
		in.WriteString(s)
		in.WriteByte('\n')
	}
	out, err := l.run(ctx, dir, &in, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	res := make([][]byte, 0, len(shas))
	for _, sha := range shas {
		nl := bytes.IndexByte(out, '\n')
		if nl < 0 {
			return nil, fmt.Errorf("git cat-file: truncated output at %s", sha)
		}
		header := strings.Fields(string(out[:nl]))
		if len(header) != 3 || header[1] != "blob" {
			return nil, fmt.Errorf("git cat-file: unexpected %q for %s", out[:nl], sha)
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size < 0 || nl+1+size > len(out) {
			return nil, fmt.Errorf("git cat-file: bad size in %q", out[:nl])
		}
		res = append(res, append([]byte(nil), out[nl+1:nl+1+size]...))
		out = out[nl+1+size:]
		if len(out) > 0 && out[0] == '\n' {
			out = out[1:]
		}
	}
	return res, nil
}
