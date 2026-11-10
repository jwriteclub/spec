// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
)

// An env_file holds provider variables (API keys, account ids) outside the
// shell environment: lines of NAME=VALUE, read only when a provider needs a
// variable the environment lacks. Its values are secrets: no error, log line
// or report built here carries one, only names, paths and line numbers.

// EnvFilePath resolves the user config's env_file entry like a [prompts]
// entry: "" stays "", "~" and "~/" are the home directory, and a relative
// path is relative to the directory of the config file at cfgPath. ${VAR}
// is not expanded.
func EnvFilePath(entry, cfgPath, home string) string {
	return PromptPath(entry, cfgPath, home)
}

// EnvFileError is an env_file that cannot be used. It names the file and,
// for a malformed line, the line number; never a value.
type EnvFileError struct {
	// Path is the env_file's resolved path.
	Path string
	// Line is the 1-based line of a malformed line, 0 otherwise.
	Line int
	// Problem says what is wrong.
	Problem string
	// FixText says how to resolve it.
	FixText string
}

func (e *EnvFileError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("env_file %s:%d: %s", e.Path, e.Line, e.Problem)
	}
	return fmt.Sprintf("env_file %s: %s", e.Path, e.Problem)
}

// Fix says how to resolve the error.
func (e *EnvFileError) Fix() string { return e.FixText }

// EnvFileData is a loaded env_file.
type EnvFileData struct {
	// Path is the file's resolved path.
	Path string
	// Mode is the file's permission bits (never group or other access).
	Mode fs.FileMode
	// Vars maps each name the file defines to its value (the last one wins).
	Vars map[string]string
	// info is the file as it was read, for EnvFiles to notice a change.
	info fs.FileInfo
}

// Names returns the names the file defines, sorted.
func (d *EnvFileData) Names() []string {
	names := make([]string, 0, len(d.Vars))
	for n := range d.Vars {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ParseEnvFile parses env_file text. path labels the errors. Each line is
// blank, a # comment, or NAME=VALUE with an optional leading "export ".
// VALUE is everything after the first "=", with surrounding whitespace
// trimmed and one pair of matching single or double quotes stripped; there
// are no escapes or expansions. A name defined twice keeps its last value.
// A malformed line is an *EnvFileError naming the line, never its text.
func ParseEnvFile(path string, src []byte) (map[string]string, error) {
	vars := map[string]string{}
	for i, raw := range bytes.Split(src, []byte("\n")) {
		line := strings.TrimSpace(string(raw))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimLeft(rest, " \t")
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, &EnvFileError{Path: path, Line: i + 1, Problem: "expected NAME=VALUE (the line's text is not shown)",
				FixText: "make every line NAME=VALUE, a # comment or blank"}
		}
		name = strings.TrimSpace(name)
		if !envName.MatchString(name) {
			return nil, &EnvFileError{Path: path, Line: i + 1, Problem: "the text before = is not an environment variable name (the line's text is not shown)",
				FixText: "start the line with a name of letters, digits and _ (not a digit first), then ="}
		}
		vars[name] = unquote(strings.TrimSpace(value))
	}
	return vars, nil
}

// unquote strips one pair of matching single or double quotes around v.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// ReadEnvFile reads and parses the env_file at path. It refuses a file that
// is missing, is not a regular file (a symbolic link included) or that its
// group or others may read or write.
func ReadEnvFile(path string) (*EnvFileData, error) {
	refuse := func(problem, fix string) error {
		return &EnvFileError{Path: path, Problem: problem, FixText: fix}
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, refuse("does not exist", "create it with NAME=VALUE lines and run chmod 600 "+path+", or remove env_file from the user config")
	case err != nil:
		return nil, refuse("cannot be read: "+ioReason(err), "check the file and its directory")
	}
	if err := checkEnvFileInfo(path, info); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, refuse("cannot be read: "+ioReason(err), "check the file and its directory")
	}
	defer f.Close()
	// The file opened must be the one checked: no swap in between.
	opened, err := f.Stat()
	if err != nil {
		return nil, refuse("cannot be read: "+ioReason(err), "check the file")
	}
	if !os.SameFile(info, opened) {
		return nil, refuse("changed while it was read", "run the command again")
	}
	if err := checkEnvFileInfo(path, opened); err != nil {
		return nil, err
	}
	src, err := io.ReadAll(f)
	if err != nil {
		return nil, refuse("cannot be read", "check the file")
	}
	vars, err := ParseEnvFile(path, src)
	if err != nil {
		return nil, err
	}
	return &EnvFileData{Path: path, Mode: opened.Mode().Perm(), Vars: vars, info: opened}, nil
}

// ioReason is err's cause without the path (the error names the path
// already).
func ioReason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// checkEnvFileInfo refuses anything but a regular file private to its
// owner.
func checkEnvFileInfo(path string, info fs.FileInfo) error {
	mode := info.Mode()
	switch {
	case mode&fs.ModeSymlink != 0:
		return &EnvFileError{Path: path, Problem: "is a symbolic link; spec reads only a regular file",
			FixText: "point env_file at the file itself, not a link to it"}
	case !mode.IsRegular():
		return &EnvFileError{Path: path, Problem: "is not a regular file", FixText: "point env_file at a regular file"}
	case mode.Perm()&0o400 == 0:
		return &EnvFileError{Path: path, Problem: fmt.Sprintf("is not readable by its owner (mode %04o)", mode.Perm()),
			FixText: "chmod 600 " + path}
	case mode.Perm()&0o077 != 0:
		return &EnvFileError{Path: path, Problem: fmt.Sprintf("is readable or writable by group or others (mode %04o); it holds secrets", mode.Perm()),
			FixText: "chmod 600 " + path}
	}
	return nil
}

// EnvFiles reads each env_file once (one per command, or per `spec ui`
// session) and again only when it changes: a later Load costs one Lstat,
// and a file replaced, rewritten or chmodded since is read (and checked)
// anew. A failed load is not kept.
type EnvFiles struct {
	mu     sync.Mutex
	loaded map[string]*EnvFileData
}

// Load returns the env_file at path, reading it on first use or when it has
// changed since.
func (c *EnvFiles) Load(path string) (*EnvFileData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok := c.loaded[path]; ok {
		if now, err := os.Lstat(path); err == nil && unchanged(d.info, now) {
			return d, nil
		}
		delete(c.loaded, path)
	}
	d, err := ReadEnvFile(path)
	if err != nil {
		return nil, err
	}
	if c.loaded == nil {
		c.loaded = map[string]*EnvFileData{}
	}
	c.loaded[path] = d
	return d, nil
}

// unchanged reports whether now is the same file as was, untouched.
func unchanged(was, now fs.FileInfo) bool {
	return os.SameFile(was, now) && was.Mode() == now.Mode() && was.Size() == now.Size() && was.ModTime().Equal(now.ModTime())
}

// Env sources, as EnvLookup.Source reports them.
const (
	SourceEnvironment = "environment"
	SourceEnvFile     = "env_file"
)

// EnvLookup resolves the variables of a provider's settings: the process
// environment first, then the env_file for a variable the environment has
// unset or empty. Only provider settings (api_key_env, base_url's ${VAR})
// are read through it; a CLI provider's environment never is.
type EnvLookup struct {
	// Getenv reads the process environment.
	Getenv func(string) string
	// File is the env_file's variables; nil when none was needed or set.
	File map[string]string
}

// Get returns the variable's value: the environment's, else the file's.
func (l EnvLookup) Get(name string) string {
	if v := l.Getenv(name); v != "" {
		return v
	}
	return l.File[name]
}

// Source says where Get finds the variable: SourceEnvironment,
// SourceEnvFile, or "" when it is unset in both.
func (l EnvLookup) Source(name string) string {
	switch {
	case l.Getenv(name) != "":
		return SourceEnvironment
	case l.File[name] != "":
		return SourceEnvFile
	}
	return ""
}

// EnvNames returns the variables the provider's settings read: api_key_env
// and base_url's ${VAR} references, for the kinds that read them (openai
// and decisions). A malformed base_url contributes none (Parse reports it).
func (p Provider) EnvNames() []string {
	switch p.KindOrDefault() {
	case KindOpenAI, KindDecisions:
	default:
		return nil
	}
	var names []string
	if p.APIKeyEnv != "" {
		names = append(names, p.APIKeyEnv)
	}
	if refs, err := envRefs(p.BaseURL); err == nil {
		names = append(names, refs...)
	}
	return names
}
