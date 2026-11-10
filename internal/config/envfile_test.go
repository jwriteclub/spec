// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/pos"
)

// envSecret is a value that must never appear in an error or report.
const envSecret = "sk-envfile-sentinel-9f8e7d"

func TestParseEnvFile(t *testing.T) {
	src := "# provider keys\n" +
		"\n" +
		"DEEPSEEK_API_KEY=" + envSecret + "\n" +
		"export CLOUDFLARE_ACCOUNT_ID = acct42 \r\n" +
		"export\tTABBED=t\n" +
		"DOUBLE=\"a b # not a comment\"\n" +
		"SINGLE='$HOME ${X} \\n'\n" +
		"UNMATCHED=\"open\n" +
		"EMPTY=\n" +
		"EQUALS=a=b=c\n" +
		"  # indented comment\n" +
		"DUP=first\n" +
		"DUP=second\n" +
		"export=plain\n"
	vars, err := ParseEnvFile("/x/env", []byte(src))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"DEEPSEEK_API_KEY":      envSecret,
		"CLOUDFLARE_ACCOUNT_ID": "acct42",
		"TABBED":                "t",
		"DOUBLE":                "a b # not a comment",
		"SINGLE":                `$HOME ${X} \n`,
		"UNMATCHED":             `"open`,
		"EMPTY":                 "",
		"EQUALS":                "a=b=c",
		"DUP":                   "second",
		"export":                "plain",
	}, vars)
}

func TestParseEnvFileMalformedNamesLineNotValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		line int
	}{
		{"no equals", "OK=1\n# c\n" + envSecret + "\n", 3},
		{"bad name", "OK=1\n" + envSecret + "-x=" + envSecret + "\n", 2},
		{"digit first", "1ABC=" + envSecret + "\n", 1},
		{"empty name", "=" + envSecret + "\n", 1},
		{"export only", "export " + envSecret + "\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEnvFile("/x/env", []byte(tc.src))
			var fe *EnvFileError
			require.ErrorAs(t, err, &fe)
			assert.Equal(t, tc.line, fe.Line)
			assert.Equal(t, "/x/env", fe.Path)
			assert.Contains(t, err.Error(), "/x/env:"+strconv.Itoa(tc.line)+":")
			assert.NotContains(t, err.Error(), envSecret)
			assert.NotContains(t, fe.Fix(), envSecret)
			assert.NotContains(t, err.Error(), "sk-")
		})
	}
}

func writeEnv(t *testing.T, dir, name, text string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(text), 0o600))
	require.NoError(t, os.Chmod(p, mode))
	return p
}

func TestReadEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := writeEnv(t, dir, "env", "KEY="+envSecret+"\nACCT=a1\n", 0o600)
	d, err := ReadEnvFile(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), d.Mode)
	assert.Equal(t, []string{"ACCT", "KEY"}, d.Names())
	assert.Equal(t, envSecret, d.Vars["KEY"])

	ro := writeEnv(t, dir, "ro", "KEY=x\n", 0o400)
	_, err = ReadEnvFile(ro)
	require.NoError(t, err, "owner read-only is private")
}

func TestReadEnvFileRefuses(t *testing.T) {
	dir := t.TempDir()
	open := writeEnv(t, dir, "open", "KEY="+envSecret+"\n", 0o644)
	groupWrite := writeEnv(t, dir, "gw", "KEY="+envSecret+"\n", 0o620)
	writeOnly := writeEnv(t, dir, "wo", "KEY="+envSecret+"\n", 0o200)
	target := writeEnv(t, dir, "target", "KEY="+envSecret+"\n", 0o600)
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(target, link))
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o700))

	for _, tc := range []struct {
		name, path, problem, fix string
	}{
		{"world readable", open, "mode 0644", "chmod 600 " + open},
		{"group writable", groupWrite, "mode 0620", "chmod 600 " + groupWrite},
		{"owner unreadable", writeOnly, "not readable by its owner (mode 0200)", "chmod 600 " + writeOnly},
		{"symlink", link, "symbolic link", "not a link"},
		{"directory", sub, "not a regular file", "regular file"},
		{"missing", filepath.Join(dir, "nope"), "does not exist", "remove env_file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadEnvFile(tc.path)
			var fe *EnvFileError
			require.ErrorAs(t, err, &fe)
			assert.Contains(t, err.Error(), tc.path)
			assert.Contains(t, err.Error(), tc.problem)
			assert.Contains(t, fe.Fix(), tc.fix)
			assert.NotContains(t, err.Error(), envSecret)
		})
	}
}

func TestEnvFilesCache(t *testing.T) {
	dir := t.TempDir()
	p := writeEnv(t, dir, "env", "KEY=one\n", 0o644)
	var c EnvFiles
	_, err := c.Load(p)
	require.Error(t, err)
	require.NoError(t, os.Chmod(p, 0o600))
	d, err := c.Load(p)
	require.NoError(t, err, "a refused file is read again once fixed")
	assert.Equal(t, "one", d.Vars["KEY"])
	again, err := c.Load(p)
	require.NoError(t, err)
	assert.Same(t, d, again, "an unchanged file is read once")

	// A rewrite is read anew, and a chmod is checked anew.
	require.NoError(t, os.WriteFile(p, []byte("KEY=second\n"), 0o600))
	d, err = c.Load(p)
	require.NoError(t, err)
	assert.Equal(t, "second", d.Vars["KEY"])
	require.NoError(t, os.Chmod(p, 0o640))
	_, err = c.Load(p)
	assert.ErrorContains(t, err, "mode 0640")
	require.NoError(t, os.Remove(p))
	_, err = c.Load(p)
	assert.ErrorContains(t, err, "does not exist")
}

func TestEnvFilePath(t *testing.T) {
	cfg := "/cfg/spec/config.toml"
	assert.Equal(t, "/home/u", EnvFilePath("~", cfg, "/home/u"))
	assert.Equal(t, "/home/u/.secrets/spec.env", EnvFilePath("~/.secrets/spec.env", cfg, "/home/u"))
	assert.Equal(t, "/cfg/spec/env", EnvFilePath("env", cfg, "/home/u"))
	assert.Equal(t, "/cfg/keys/env", EnvFilePath("../keys/env", cfg, "/home/u"))
	assert.Equal(t, "/abs/env", EnvFilePath("/abs/env", cfg, "/home/u"))
	assert.Equal(t, "/cfg/spec/${HOME}/env", EnvFilePath("${HOME}/env", cfg, "/home/u"), "no ${} expansion")
	assert.Equal(t, "", EnvFilePath("", cfg, "/home/u"))
}

func TestEnvLookupPrecedence(t *testing.T) {
	env := map[string]string{"BOTH": "from-env", "EMPTY": ""}
	l := EnvLookup{
		Getenv: func(k string) string { return env[k] },
		File:   map[string]string{"BOTH": "from-file", "EMPTY": "file-empty", "ONLY": "file-only"},
	}
	assert.Equal(t, "from-env", l.Get("BOTH"))
	assert.Equal(t, SourceEnvironment, l.Source("BOTH"))
	assert.Equal(t, "file-empty", l.Get("EMPTY"), "an empty environment variable falls back to the file")
	assert.Equal(t, SourceEnvFile, l.Source("EMPTY"))
	assert.Equal(t, "file-only", l.Get("ONLY"))
	assert.Equal(t, SourceEnvFile, l.Source("ONLY"))
	assert.Equal(t, "", l.Get("NONE"))
	assert.Equal(t, "", l.Source("NONE"))
}

func TestProviderResolvesThroughEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := writeEnv(t, dir, "env", "CLOUDFLARE_ACCOUNT_ID=acct9\nCLOUDFLARE_AI_TOKEN="+envSecret+"\n", 0o600)
	d, err := ReadEnvFile(p)
	require.NoError(t, err)
	l := EnvLookup{Getenv: func(string) string { return "" }, File: d.Vars}
	prov := Provider{BaseURL: "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/ai/v1", APIKeyEnv: "CLOUDFLARE_AI_TOKEN", Model: "m"}
	u, err := prov.ResolveBaseURL("cf", l.Get)
	require.NoError(t, err)
	assert.Equal(t, "https://api.cloudflare.com/client/v4/accounts/acct9/ai/v1", u)
	key, err := prov.Key("cf", l.Get)
	require.NoError(t, err)
	assert.Equal(t, envSecret, key)
	assert.Equal(t, []string{"CLOUDFLARE_AI_TOKEN", "CLOUDFLARE_ACCOUNT_ID"}, prov.EnvNames())
	assert.Nil(t, Provider{Kind: KindClaudeCLI, PassEnv: []string{"X"}}.EnvNames())
	assert.Nil(t, Provider{Kind: KindCanned, Path: "r.toml"}.EnvNames())
	assert.Equal(t, []string{"K"}, Provider{Kind: KindDecisions, APIKeyEnv: "K"}.EnvNames())
}

func TestParseEnvFileKey(t *testing.T) {
	cfg, ds := Parse("c.toml", []byte("env_file = \"~/.secrets/spec.env\"\n"+valid))
	require.Empty(t, ds)
	assert.Equal(t, "~/.secrets/spec.env", cfg.EnvFile)

	src := "default = \"cf\"\nenv_file = 5\n" + valid[len("default = \"cf\"\n"):]
	_, ds = Parse("c.toml", []byte(src))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "env_file")
	assert.NotEqual(t, pos.Range{}, ds[0].Range, "a located diagnostic")
	off := ds[0].Range.Start
	assert.Equal(t, 2, 1+strings.Count(src[:off], "\n"), "on the env_file line")

	src = "env_file = \"  \"\n" + valid
	_, ds = Parse("c.toml", []byte(src))
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].Message, "env_file is blank")
	assert.Equal(t, pos.Range{Start: 0, End: len("env_file")}, ds[0].Range)

	_, ds = Parse("c.toml", []byte("[providers.cf]\nenv_file = \"x\"\n"))
	require.NotEmpty(t, ds)
	assert.Contains(t, ds[0].Message, `unknown key "providers.cf.env_file"`)
	assert.Contains(t, ds[0].Message, "known: default, env_file,")
}
