// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package scenes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/pelletier/go-toml/v2"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/format"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/recording"
	"github.com/jwriteclub/spec/internal/tui"
	"github.com/jwriteclub/spec/internal/workflow"
)

// Clock is the frozen time of every scene: proposal stamps, job and stage
// times.
var Clock = time.Date(2026, 11, 1, 15, 4, 0, 0, time.UTC)

// Shot is one captured frame of a scene.
type Shot struct {
	// Capture is the step's capture name, "" for the final frame.
	Capture string
	Frame   tui.Frame
}

// Options configures a run.
type Options struct {
	// RepoRoot is the logedit checkout, where testdata/ is read from;
	// "" finds it from the working directory (the nearest go.mod above).
	RepoRoot string
	// Trace, when set, hears each step's plain frame (debugging a scene).
	Trace func(step int, frame string)
	// TempDir, when set, holds the fixture and the user config (a test's
	// t.TempDir()); otherwise a fresh directory is made and removed.
	TempDir string
	// Tweak, when set, adjusts the UI's options before it opens (a
	// scenario turns on file watching, say).
	Tweak func(*tui.Options)
}

// GitEnv returns the git environment of the fixtures, for a scenario's
// own git calls.
func GitEnv() []string { return append([]string(nil), gitEnv...) }

// gitEnv makes the fixture's git calls independent of the machine's
// configuration and of the clock.
var gitEnv = []string{
	"GIT_AUTHOR_NAME=Spec Scenes", "GIT_AUTHOR_EMAIL=scenes@example.com",
	"GIT_COMMITTER_NAME=Spec Scenes", "GIT_COMMITTER_EMAIL=scenes@example.com",
	"GIT_AUTHOR_DATE=2026-11-01T12:00:00Z", "GIT_COMMITTER_DATE=2026-11-01T12:00:00Z",
	"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
}

// Session is a scene's UI running on its fixture, before any step: what
// Start returns. Scenario tests drive it step by step.
type Session struct {
	// Dir is the project: a committed git repository with the scene's
	// edits and agents' proposals on top.
	Dir string
	// Driver runs the UI.
	Driver *tui.Driver
	// Workflow is the UI's workflow (the canned provider, the frozen
	// clock).
	Workflow *workflow.Workflow
	// Env is the user environment of the workflow: HOME and
	// XDG_CONFIG_HOME, whose config names the canned provider.
	Env map[string]string
	// Replies is the canned replies file.
	Replies string
	// Opts are the UI's options and Size the terminal's, for Reopen.
	Opts   tui.Options
	Size   Size
	tmp    string
	ownTmp bool
}

// Reopen closes the UI and opens it again on the same project, as a
// restart of `spec ui` would, and waits for the baseline.
func (s *Session) Reopen() error {
	s.Driver.Close()
	s.Driver = nil
	d, err := tui.NewDriver(s.Opts, s.Size.W, s.Size.H)
	if err != nil {
		return err
	}
	s.Driver = d
	return d.Wait(tui.Cond{Baseline: true, Idle: true})
}

// Getenv reads the session's user environment (empty for other keys).
func (s *Session) Getenv(k string) string { return s.Env[k] }

// Close stops the UI and removes the session's files, unless they live
// in Options.TempDir.
func (s *Session) Close() {
	if s.Driver != nil {
		s.Driver.Close()
	}
	if s.ownTmp {
		_ = os.RemoveAll(s.tmp)
	}
}

// Step runs one step of a scene.
func (s *Session) Step(st Step) error { return runStep(s.Driver, st) }

// Start builds scene sc's fixture and opens the UI on it in variant v,
// waiting for the HEAD baseline; it runs none of the scene's steps.
func Start(sc Scene, v Variant, o Options) (*Session, error) {
	root := o.RepoRoot
	if root == "" {
		r, err := FindRepoRoot()
		if err != nil {
			return nil, err
		}
		root = r
	}
	s := &Session{tmp: o.TempDir}
	if s.tmp == "" {
		tmp, err := os.MkdirTemp("", "spec-scene-")
		if err != nil {
			return nil, err
		}
		s.tmp, s.ownTmp = tmp, true
	}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	s.Dir = filepath.Join(s.tmp, "proj")
	if err := buildFixture(root, sc.Fixture, s.Dir); err != nil {
		return nil, fmt.Errorf("fixture %s: %w", sc.Fixture, err)
	}
	if err := applyEdits(s.Dir, sc.Edits); err != nil {
		return nil, err
	}
	wf, env, err := sceneWorkflow(s.tmp, sc)
	if err != nil {
		return nil, err
	}
	s.Workflow, s.Env, s.Replies = wf, env, filepath.Join(s.tmp, "replies.toml")
	for k, ag := range sc.Agents {
		if err := fileAgentChange(wf, s.Dir, ag); err != nil {
			return nil, fmt.Errorf("agent change %d: %w", k+1, err)
		}
	}
	opts := tui.Options{
		Root: s.Dir, Target: sc.Open, HashToSection: true, FixedTheme: true, Light: v.Theme == Light, ReducedColor: sc.ReducedColor, ASCIIGlyphs: sc.ASCIIGlyphs,
		Baseline: true, Workflow: wf, Now: func() time.Time { return Clock },
		HoverDelay: time.Millisecond,
	}
	if o.Tweak != nil {
		o.Tweak(&opts)
	}
	s.Opts, s.Size = opts, v.Size
	d, err := tui.NewDriver(opts, v.Size.W, v.Size.H)
	if err != nil {
		return nil, err
	}
	s.Driver = d
	if err := d.Wait(tui.Cond{Baseline: true, Idle: true}); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

// Run runs scene sc in variant v and returns its captures in order: the
// intermediate ones, then the final frame.
func Run(sc Scene, v Variant, o Options) ([]Shot, error) {
	s, err := Start(sc, v, o)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	d := s.Driver
	var shots []Shot
	for k, st := range sc.Steps {
		if err := runStep(d, st); err != nil {
			return nil, fmt.Errorf("step %d (%s): %w", k+1, st.StepText(), err)
		}
		if o.Trace != nil || st.Capture != "" {
			f, err := d.Frame()
			if err != nil {
				return nil, err
			}
			if o.Trace != nil {
				o.Trace(k+1, ansi.Strip(f.Content))
			}
			if st.Capture != "" {
				shots = append(shots, Shot{Capture: st.Capture, Frame: f})
			}
		}
	}
	f, err := d.Frame()
	if err != nil {
		return nil, err
	}
	return append(shots, Shot{Frame: f}), nil
}

// runStep runs one step on d.
func runStep(d *tui.Driver, st Step) error {
	if st.Find != "" {
		if err := d.Press("ctrl+w"); err != nil {
			return err
		}
		if err := d.Type(st.Find); err != nil {
			return err
		}
		if err := d.Press("enter"); err != nil {
			return err
		}
	}
	if err := d.Press(st.Keys...); err != nil {
		return err
	}
	if st.Text != "" {
		if err := d.Type(st.Text); err != nil {
			return err
		}
	}
	if st.HoverCounter != 0 {
		if err := d.HoverStatusCounter(st.HoverCounter); err != nil {
			return err
		}
	}
	if st.Hover != "" {
		if err := d.HoverOn(st.Hover); err != nil {
			return err
		}
	}
	return d.Wait(st.Wait)
}

// FindRepoRoot returns the nearest directory above the working directory
// that holds go.mod (the logedit checkout).
func FindRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory: run from the logedit checkout")
		}
		dir = parent
	}
}

// git runs git in dir with gitEnv.
func git(dir string, args ...string) error {
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

// buildFixture writes fixture fx into dir as a committed git repository.
func buildFixture(repoRoot string, fx Fixture, dir string) error {
	files := map[string]string{".gitignore": ".spec/\n"}
	switch fx {
	case Demo:
		files["spec.toml"] = "name = \"demo\"\n"
		files["demo.spec.md"] = demoSpec
		files["other.spec.md"] = otherSpec
	case Lensd, LensdFormatted:
		src, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "lensd", "lensd.spec.md"))
		if err != nil {
			return err
		}
		files["spec.toml"] = "name = \"lensd\"\n"
		if fx == LensdFormatted {
			res, err := format.Source("lensd.spec.md", src, format.Options{})
			if err != nil {
				return err
			}
			src = res.Text
		}
		files["lensd.spec.md"] = string(src)
	case Formal:
		files["spec.toml"] = "name = \"lock\"\nformal = [\"tla/*.tla\"]\n"
		files["lock.spec.md"] = lockSpec
		files["tla/Lock.tla"] = lockModel
	default:
		return fmt.Errorf("unknown fixture %q", fx)
	}
	for name, text := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			return err
		}
	}
	if err := git(dir, "init", "-q"); err != nil {
		return err
	}
	if err := git(dir, "add", "-A"); err != nil {
		return err
	}
	return git(dir, "commit", "-q", "-m", "base")
}

// applyEdits makes edits in dir's working tree.
func applyEdits(dir string, edits []Edit) error {
	for _, e := range edits {
		path := filepath.Join(dir, e.File)
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(src, []byte(e.Find)) {
			return fmt.Errorf("edit of %s: %q not found", e.File, e.Find)
		}
		src = bytes.Replace(src, []byte(e.Find), []byte(e.Replace), 1)
		if err := os.WriteFile(path, src, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Provider names and models of the scenes' user config. They read like a
// real setup in the screenshots; every reply is canned.
const (
	checkProvider = "cf"
	checkModel    = "glm-5.3"
	triageModel   = "glm-5.3-flash"
	gateProvider  = "jev"
	gateModel     = "typesafe/jev-1.14"
)

// sceneWorkflow writes the scene's replies and a user config naming the
// canned provider under tmp, and returns the workflow that uses them.
func sceneWorkflow(tmp string, sc Scene) (*workflow.Workflow, map[string]string, error) {
	replies := filepath.Join(tmp, "replies.toml")
	b, err := toml.Marshal(sc.Replies)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(replies, b, 0o644); err != nil {
		return nil, nil, err
	}
	home := filepath.Join(tmp, "home")
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config")}
	var cfg strings.Builder
	fmt.Fprintf(&cfg, "default = %q\n\n[providers.%s]\nkind = \"canned\"\npath = %q\nmodel = %q\n", checkProvider, checkProvider, replies, checkModel)
	if sc.Cascade {
		fmt.Fprintf(&cfg, "triage_model = %q\n\n[providers.%s]\nkind = \"canned\"\npath = %q\nmodel = %q\n\n[gate]\nprovider = %q\n",
			triageModel, gateProvider, replies, gateModel, gateProvider)
	} else {
		cfg.WriteString("triage = false\n")
	}
	path := filepath.Join(env["XDG_CONFIG_HOME"], "spec", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(path, []byte(cfg.String()), 0o644); err != nil {
		return nil, nil, err
	}
	return &workflow.Workflow{
		Getenv: func(k string) string { return env[k] },
		Git:    baseline.Loader{Env: gitEnv},
		Now:    func() time.Time { return Clock },
		IDs:    seeded(sc.Name),
	}, env, nil
}

// fileAgentChange files ag's proposal as `spec propose --by` would.
func fileAgentChange(wf *workflow.Workflow, dir string, ag AgentChange) error {
	if err := applyEdits(dir, ag.Edits); err != nil {
		return err
	}
	proj, err := project.Open(dir)
	if err != nil {
		return err
	}
	req := workflow.ProposeRequest{Author: ag.Author, Why: ag.Why, SourceKind: "agent"}
	res, e := wf.Propose(context.Background(), proj, req)
	if e != nil && e.Code == "names_required" {
		// Its own edits are all that differ: file what the refusal lists.
		req.Names = e.Names
		res, e = wf.Propose(context.Background(), proj, req)
	}
	if e != nil {
		return fmt.Errorf("%s", e.Message)
	}
	if len(res.Proposals) == 0 {
		return fmt.Errorf("nothing filed (%s)", strings.Join(res.Skipped, "; "))
	}
	return nil
}

// seeded returns a byte source seeded by name.
func seeded(name string) *recording.SeededReader {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	var seed [32]byte
	sum := h.Sum64()
	for k := range seed {
		seed[k] = byte(sum >> (8 * (k % 8)))
	}
	return recording.NewSeeded(seed)
}
