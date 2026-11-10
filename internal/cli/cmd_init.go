// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/explain"
	"github.com/jwriteclub/spec/internal/project"
)

type initFile struct {
	File   string `json:"file"`
	Action string `json:"action"`
}

type initData struct {
	Name     string     `json:"name"`
	Root     string     `json:"root"`
	Files    []initFile `json:"files"`
	ClaudeMD string     `json:"claude_md"`
	Next     nextJSON   `json:"next"`
}

// nextJSON is the next object of the JSON contract.
type nextJSON struct {
	Text string   `json:"text"`
	Argv []string `json:"argv"`
}

func (s *session) initCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "init [--name NAME]",
		Short: "create spec.toml and a skeleton NAME.spec.md in the project directory",
		Long: "Creates spec.toml and a skeleton NAME.spec.md (an existing NAME.spec.md is adopted unchanged),\n" +
			"appends .spec/ to .gitignore if absent, and prints a suggested CLAUDE.md section.\n" +
			"The default NAME is the directory name lowercased to [a-z0-9-]. The directory is the\n" +
			"current one, or --project DIR.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errTooMany("init [--name NAME]")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runInit(name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "project name (default: the directory name)")
	return cmd
}

// defaultProjectName lowercases a directory name to [a-z0-9-].
func defaultProjectName(dir string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(filepath.Base(dir)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// gitignoreHas reports whether .gitignore content already ignores .spec/.
func gitignoreHas(content string) bool {
	for _, l := range strings.Split(content, "\n") {
		switch strings.TrimSpace(l) {
		case ".spec", ".spec/", "/.spec", "/.spec/":
			return true
		}
	}
	return false
}

func (s *session) runInit(nameFlag string) *outcome {
	dir := s.project
	if dir == "" {
		dir = s.env.Cwd
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.env.Cwd, dir)
	}
	dir = filepath.Clean(dir)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return usageErr("%s is not a directory", dir)
	}

	name := nameFlag
	if name == "" {
		name = defaultProjectName(dir)
		if !explain.ValidProjectName(name) {
			return usageErr("cannot derive a project name from the directory %q; pass --name NAME (a letter followed by letters, digits, '_' or '-')", filepath.Base(dir))
		}
	} else if !explain.ValidProjectName(name) {
		return usageErr("invalid --name %q: use a letter followed by letters, digits, '_' or '-'", name)
	}
	skeleton, err := explain.Skeleton(name)
	if err != nil {
		return usageErr("%v", err)
	}

	// Preflight everything before writing anything.
	tomlPath := filepath.Join(dir, project.ConfigFile)
	if _, err := os.Lstat(tomlPath); err == nil {
		return fail(exitFail, "project_exists", "%s already exists; this directory is already a spec project (spec init refuses to overwrite it)", tomlPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fail(exitFail, "write_failed", "cannot inspect %s: %v", tomlPath, err)
	}

	specName := name + ".spec.md"
	specPath := filepath.Join(dir, specName)
	adopt := false
	if st, err := os.Lstat(specPath); err == nil {
		if !st.Mode().IsRegular() {
			return usageErr("%s exists but is not a regular file", specPath)
		}
		adopt = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fail(exitFail, "write_failed", "cannot inspect %s: %v", specPath, err)
	}

	giPath := filepath.Join(dir, ".gitignore")
	var giOld []byte
	giExists := false
	if b, err := os.ReadFile(giPath); err == nil {
		giOld, giExists = b, true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fail(exitFail, "write_failed", "cannot read %s: %v", giPath, err)
	}
	giHas := giExists && gitignoreHas(string(giOld))

	toml := "# Configuration for the spec tool; `spec explain config` describes every key.\nname = " + strconv.Quote(name) + "\n"

	files := []initFile{{File: project.ConfigFile, Action: "created"}}
	if adopt {
		files = append(files, initFile{File: specName, Action: "adopted"})
	} else {
		files = append(files, initFile{File: specName, Action: "created"})
	}
	switch {
	case giHas:
		files = append(files, initFile{File: ".gitignore", Action: "unchanged"})
	case giExists:
		files = append(files, initFile{File: ".gitignore", Action: "appended"})
	default:
		files = append(files, initFile{File: ".gitignore", Action: "created"})
	}

	// Writes: skeleton, .gitignore, then spec.toml last, so a failed run
	// leaves no spec.toml and is safely retryable.
	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	writeFail := func(path string, err error) *outcome {
		rollback()
		return fail(exitFail, "write_failed", "writing %s failed: %v; files written earlier by this run were removed or restored", path, err)
	}
	if !adopt {
		if err := writeFileAtomic(specPath, []byte(skeleton), 0o644); err != nil {
			return writeFail(specPath, err)
		}
		undo = append(undo, func() { _ = os.Remove(specPath) })
	}
	if !giHas {
		content := giOld
		if len(content) > 0 && content[len(content)-1] != '\n' {
			content = append(append([]byte{}, content...), '\n')
		}
		content = append(append([]byte{}, content...), []byte(".spec/\n")...)
		mode := fs.FileMode(0o644)
		if st, err := os.Stat(giPath); err == nil {
			mode = st.Mode().Perm()
		}
		if err := writeFileAtomic(giPath, content, mode); err != nil {
			return writeFail(giPath, err)
		}
		if giExists {
			old := giOld
			undo = append(undo, func() { _ = writeFileAtomic(giPath, old, mode) })
		} else {
			undo = append(undo, func() { _ = os.Remove(giPath) })
		}
	}
	if err := writeFileAtomic(tomlPath, []byte(toml), 0o644); err != nil {
		return writeFail(tomlPath, err)
	}

	next := nextJSON{
		Text: fmt.Sprintf("edit %s: replace the example anchors with real ones (`spec explain markup` has the syntax), then run `spec lint`", specName),
		Argv: []string{"spec", "lint"},
	}
	if adopt {
		next.Text = fmt.Sprintf("run `spec lint` on the adopted %s", specName)
	}
	data := initData{Name: name, Root: dir, Files: files, ClaudeMD: explain.ClaudeSnippet(), Next: next}

	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "%-9s %s\n", f.Action, f.File)
	}
	b.WriteString("\nSuggested CLAUDE.md section (printed, not written; add it only if the user agrees):\n\n")
	b.WriteString(strings.TrimRight(data.ClaudeMD, "\n") + "\n\n")
	b.WriteString("next: " + next.Text + "\n")
	return &outcome{data: data, human: b.String()}
}

// writeFileAtomic writes data to a temp file in path's directory and renames
// it over path.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".spec-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// errTooMany is the argument error for commands that take too many
// arguments.
func errTooMany(usage string) error {
	return fmt.Errorf("too many arguments; usage: spec %s", usage)
}
