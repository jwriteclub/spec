// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/infer"
)

// Prepared is a recording's starting state restored into a temp directory
// for `spec ui --replay`: the project, a user config whose providers all
// serve the recording's canned replies, and the environment that finds it.
// A recording is untrusted input (shared, or edited by hand): nothing in it may
// run a command, contact anything, or write outside this temp directory. What it
// is trusted with is the project's text in the temp directory, the replies, the
// keys and the display.
type Prepared struct {
	// Root is the restored project.
	Root string
	// Home is the replay's HOME; Env holds HOME, XDG_CONFIG_HOME and PATH.
	Home string
	Env  map[string]string
	// Canned is the copy of canned.toml the user config names.
	Canned string
	// Git runs git in Root with a pinned identity.
	Git baseline.Loader
	// Rec is the recording.
	Rec *Recording
	// Tmp is the temp directory holding all of it; Cleanup removes it.
	Tmp string
}

// Getenv reads the prepared environment ("" for other keys).
func (p *Prepared) Getenv(k string) string { return p.Env[k] }

// Cleanup removes the temp directory.
func (p *Prepared) Cleanup() error { return os.RemoveAll(p.Tmp) }

// GitEnv is the git environment of a replay: a fixed identity and no
// user or system configuration, so restoring and committing do not depend
// on the machine.
func GitEnv(at time.Time) []string {
	date := at.UTC().Format(time.RFC3339)
	return []string{
		"GIT_AUTHOR_NAME=Spec Replay", "GIT_AUTHOR_EMAIL=replay@example.com",
		"GIT_COMMITTER_NAME=Spec Replay", "GIT_COMMITTER_EMAIL=replay@example.com",
		"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
	}
}

// Prepare restores the recording in dir into a new temp directory under
// parent ("" for the system's): the project as it was when recording
// started (HEAD as one commit, the staged files staged, the working tree
// on top), and HOME with the recording's user config pointing every
// provider at a copy of canned.toml.
func Prepare(ctx context.Context, dir, parent string) (*Prepared, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	rec, err := Load(dir)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(parent, "spec-replay-")
	if err != nil {
		return nil, err
	}
	// The temp dir may sit behind a symlink (/var on macOS); git and the
	// watchers report resolved paths.
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}
	p := &Prepared{Tmp: tmp, Rec: rec, Root: filepath.Join(tmp, "project"), Home: filepath.Join(tmp, "home"),
		Git: baseline.Loader{Env: GitEnv(rec.Meta.Start)}}
	ok := false
	defer func() {
		if !ok {
			_ = p.Cleanup()
		}
	}()
	if err := os.MkdirAll(p.Root, 0o755); err != nil {
		return nil, err
	}
	if err := p.restoreRepo(ctx, dir); err != nil {
		return nil, err
	}
	if err := p.writeUserConfig(dir); err != nil {
		return nil, err
	}
	ok = true
	return p, nil
}

// restoreRepo rebuilds the project from the tars of dir.
func (p *Prepared) restoreRepo(ctx context.Context, dir string) error {
	headTar := filepath.Join(dir, HeadTar)
	if _, err := os.Stat(headTar); err == nil {
		if err := p.git(ctx, "init", "-q"); err != nil {
			return err
		}
		head, err := extractTar(headTar, p.Root)
		if err != nil {
			return fmt.Errorf("%s: %w", HeadTar, err)
		}
		if err := p.git(ctx, "add", "-A", "-f"); err != nil {
			return err
		}
		msg := "recorded HEAD"
		if p.Rec.Meta.GitHead != "" {
			msg += " " + p.Rec.Meta.GitHead
		}
		if err := p.git(ctx, "commit", "-q", "--allow-empty", "-m", msg); err != nil {
			return err
		}
		staged, err := extractTar(filepath.Join(dir, IndexTar), p.Root)
		if err != nil {
			return fmt.Errorf("%s: %w", IndexTar, err)
		}
		if len(staged) > 0 {
			if err := p.git(ctx, append([]string{"add", "-f", "--"}, staged...)...); err != nil {
				return err
			}
		}
		// The working tree: what was deleted there is in head.tar but not
		// in workspace.tar. Staged first: a file staged in place of a directory
		// of head.tar goes before the paths through it.
		if err := removeMembers(p.Root, slices.Concat(staged, head)); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := extractTar(filepath.Join(dir, WorkspaceTar), p.Root); err != nil {
		return fmt.Errorf("%s: %w", WorkspaceTar, err)
	}
	return nil
}

// removeMembers removes restored members and whatever replaced them.
func removeMembers(rootDir string, names []string) error {
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, name := range names {
		if err := root.RemoveAll(filepath.FromSlash(name)); err != nil {
			return err
		}
	}
	return nil
}

// git runs git in the restored project.
func (p *Prepared) git(ctx context.Context, args ...string) error {
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	_, err := p.Git.Run(ctx, p.Root, nil, full...)
	return err
}

// writeUserConfig writes HOME's config.toml from the recording's, every provider
// on a copy of canned.toml beside it, whatever the recording's config says
// (cannedCopy).
func (p *Prepared) writeUserConfig(dir string) error {
	p.Env = map[string]string{
		"HOME": p.Home, "XDG_CONFIG_HOME": filepath.Join(p.Home, "config"), "PATH": os.Getenv("PATH"),
	}
	cfgDir := filepath.Join(p.Env["XDG_CONFIG_HOME"], "spec")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return err
	}
	p.Canned = filepath.Join(p.Tmp, CannedFile)
	canned, err := os.ReadFile(filepath.Join(dir, CannedFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(p.Canned, canned, 0o644); err != nil {
		return err
	}
	infer.ForgetCanned(p.Canned)
	cfg, err := os.ReadFile(filepath.Join(dir, ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err = cannedCopy(cfg, p.Canned)
	if err != nil {
		return fmt.Errorf("%s: %w", ConfigFile, err)
	}
	return os.WriteFile(filepath.Join(cfgDir, "config.toml"), cfg, 0o644)
}

// extractTar writes the members of the tar at tarPath under rootDir (the
// executable bit and symlinks kept) and returns their names, sorted. A missing
// tar extracts nothing. Only regular files and symlinks are accepted; a member
// outside the project or in .git is an error. A recording is untrusted input:
// a member replaces what is at its path and never writes through a link, and
// a link that would lead out of the project, or a member whose name holds a
// control character (which would reach the terminal and the tape), is not created.
func extractTar(tarPath, rootDir string) ([]string, error) {
	f, err := os.Open(tarPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var names []string
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name, err := memberName(h)
		if err != nil {
			return nil, err
		}
		if HasControl(name) || h.Typeflag == tar.TypeSymlink && !linkStaysInside(name, h.Linkname) {
			continue
		}
		if err := makeParents(root, name); err != nil {
			return nil, err
		}
		if err := root.RemoveAll(filepath.FromSlash(name)); err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeSymlink {
			err = root.Symlink(h.Linkname, filepath.FromSlash(name))
		} else {
			err = writeMember(root, name, h.Mode, tr)
		}
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	dropped, err := dropLeavingLinks(root, rootDir)
	if err != nil {
		return nil, err
	}
	names = slices.DeleteFunc(names, func(n string) bool { return dropped[n] })
	sort.Strings(names)
	return names, nil
}

// memberName returns the cleaned project-relative name of a member, or an error
// when the member is not one a recording holds.
func memberName(h *tar.Header) (string, error) {
	if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeSymlink {
		return "", fmt.Errorf("member %q is neither a file nor a symbolic link", h.Name)
	}
	name := path.Clean(h.Name)
	if name == "." || !filepath.IsLocal(filepath.FromSlash(name)) {
		return "", fmt.Errorf("member %q is outside the project", h.Name)
	}
	if InGitDir(name) {
		return "", fmt.Errorf("member %q is in .git", h.Name)
	}
	return name, nil
}

// InGitDir reports whether the slash-separated path p has a component some file
// system takes for .git. Git runs hooks and the commands of its config from
// there, so a replay writes nothing to it.
func InGitDir(p string) bool {
	return slices.ContainsFunc(strings.Split(p, "/"), isGitName)
}

// isGitName is git's own test for the name: any case (macOS file systems usually
// ignore it), without the code points HFS+ ignores, without the trailing dots and
// spaces NTFS drops, or NTFS's short name.
func isGitName(name string) bool {
	name = strings.Map(func(r rune) rune {
		if hfsIgnored(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimRight(name, ". ")
	return strings.EqualFold(name, ".git") || strings.EqualFold(name, "git~1")
}

// hfsIgnored reports whether HFS+ leaves r out when it compares names.
func hfsIgnored(r rune) bool {
	return r >= 0x200c && r <= 0x200f || r >= 0x202a && r <= 0x202e || r >= 0x206a && r <= 0x206f || r == 0xfeff
}

// HasControl reports whether s holds a control character, which would act on a
// terminal or end a line of a tape.
func HasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

// linkStaysInside reports whether a link at name with this target points inside
// the project and not into .git, going by the text of the target.
// dropLeavingLinks catches the ones that leave by way of another link.
func linkStaysInside(name, target string) bool {
	if target == "" || path.IsAbs(target) || filepath.IsAbs(target) {
		return false
	}
	at := path.Join(path.Dir(name), filepath.ToSlash(target))
	return filepath.IsLocal(filepath.FromSlash(at)) && !InGitDir(at)
}

// makeParents makes the directories above name, replacing whatever else is on
// the way (as git does when a directory takes a link's place) rather than
// following it.
func makeParents(root *os.Root, name string) error {
	parts := strings.Split(name, "/")
	for k := 1; k < len(parts); k++ {
		dir := filepath.Join(parts[:k]...)
		st, err := root.Lstat(dir)
		if err == nil && st.IsDir() {
			continue
		}
		if err == nil {
			err = root.Remove(dir)
		} else if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		if err == nil {
			err = root.Mkdir(dir, 0o755)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// writeMember creates the file name, which must not exist, as 644 or as 755 when
// mode has an executable bit: no setuid, setgid, or group or world write.
func writeMember(root *os.Root, name string, mode int64, r io.Reader) error {
	perm := os.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	out, err := root.OpenFile(filepath.FromSlash(name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// dropLeavingLinks removes the links under the root that do not resolve inside
// it, whichever links they go through, and returns their names. A dangling link
// that stays inside is left alone; so is everything in .git.
func dropLeavingLinks(root *os.Root, rootDir string) (map[string]bool, error) {
	dropped := map[string]bool{}
	err := filepath.WalkDir(rootDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(rootDir, p)
		if err != nil {
			return err
		}
		if d.IsDir() && rel == ".git" {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		if _, err := root.Stat(rel); err == nil || errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		dropped[filepath.ToSlash(rel)] = true
		return root.Remove(rel)
	})
	return dropped, err
}
