// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// envFileSecret is the API key that lives only in the env_file: no output
// of any command may contain it.
const envFileSecret = "sk-cli-envfile-sentinel-0451"

// writeEnvFile writes the env_file beside the user config and returns its
// path.
func writeEnvFile(t *testing.T, env testEnv, text string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(env["XDG_CONFIG_HOME"], "spec", "keys.env")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte(text), 0o600))
	require.NoError(t, os.Chmod(p, mode))
	return p
}

func envFileConfig(url string) string { return envFileConfigAt(url, "keys.env") }

func envFileConfigAt(url, envFile string) string {
	return `default = "fake"
env_file = "` + envFile + `"

[providers.fake]
base_url = "` + url + `/accounts/${SPEC_ENVFILE_ACCOUNT}/v1"
model = "main-model"
api_key_env = "SPEC_ENVFILE_KEY"
`
}

func TestDoctorEnvFile(t *testing.T) {
	dir := demoProject(t)
	env := isolatedEnv(t)
	env["SPEC_ENVFILE_ACCOUNT"] = "acct-env"
	env["SPEC_ENVFILE_KEY"] = ""
	srv := fakeProvider(t, okReply("OK"))
	writeUserConfig(t, env, envFileConfig(srv.URL))
	path := writeEnvFile(t, env, "# spec keys\nexport SPEC_ENVFILE_KEY=\""+envFileSecret+"\"\nSPEC_ENVFILE_ACCOUNT=acct-file\n", 0o600)

	jr := runFast(t, env, dir, "doctor")
	require.Equal(t, 0, jr.code, jr.stdout)
	_, c := doctorChecks(t, jr)
	assert.Equal(t, "ok", c["env_file"].Status)
	assert.Equal(t, path+" (mode 0600, private): defines 2 names: SPEC_ENVFILE_ACCOUNT, SPEC_ENVFILE_KEY", c["env_file"].Detail)
	assert.Equal(t, "ok", c["provider_key"].Status)
	assert.Equal(t, "provider fake: SPEC_ENVFILE_KEY is set (env_file), SPEC_ENVFILE_ACCOUNT is set (environment)", c["provider_key"].Detail)
	assert.Equal(t, "ok", c["inference"].Status)
	assert.NotContains(t, jr.stdout, envFileSecret)
	_, auths := srv.requests()
	assert.Equal(t, []string{"Bearer " + envFileSecret}, auths)

	// The human report says the same, and holds no value either.
	r := runIn(t, env, dir, "doctor")
	require.Equal(t, 0, r.code, r.stdout+r.stderr)
	assert.Contains(t, r.stdout, "env_file")
	assert.Contains(t, r.stdout, "SPEC_ENVFILE_KEY is set (env_file)")
	assert.NotContains(t, r.stdout+r.stderr, envFileSecret)
	assert.NotContains(t, r.stdout+r.stderr, "acct-file")
}

func TestDoctorEnvFileRefused(t *testing.T) {
	dir := demoProject(t)
	env := isolatedEnv(t)
	env["SPEC_ENVFILE_ACCOUNT"] = ""
	env["SPEC_ENVFILE_KEY"] = ""
	srv := fakeProvider(t, okReply("OK"))
	writeUserConfig(t, env, envFileConfig(srv.URL))
	path := writeEnvFile(t, env, "SPEC_ENVFILE_KEY="+envFileSecret+"\nSPEC_ENVFILE_ACCOUNT=a\n", 0o644)

	for _, args := range [][]string{{"doctor", "--json"}, {"doctor"}} {
		r := runIn(t, env, dir, args...)
		assert.Equal(t, 1, r.code)
		assert.NotContains(t, r.stdout+r.stderr, envFileSecret)
		assert.Contains(t, r.stdout, "chmod 600 "+path)
	}
	jr := runFast(t, env, dir, "doctor")
	_, c := doctorChecks(t, jr)
	assert.Equal(t, "fail", c["env_file"].Status)
	assert.Contains(t, c["env_file"].Detail, "mode 0644")
	assert.Equal(t, "chmod 600 "+path, c["env_file"].Fix)
	assert.Equal(t, "fail", c["provider_key"].Status)
	assert.Equal(t, "skipped", c["inference"].Status)
	b, _ := srv.requests()
	assert.Empty(t, b, "no request with a refused env_file")

	// A symlink and a malformed line are refused the same way.
	require.NoError(t, os.Chmod(path, 0o600))
	link := filepath.Join(filepath.Dir(path), "link.env")
	require.NoError(t, os.Symlink(path, link))
	writeUserConfig(t, env, envFileConfigAt(srv.URL, "link.env"))
	jr = runFast(t, env, dir, "doctor")
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "fail", c["env_file"].Status, jr.stdout)
	assert.Contains(t, c["env_file"].Detail, "symbolic link")

	writeUserConfig(t, env, envFileConfig(srv.URL))
	require.NoError(t, os.WriteFile(path, []byte("OK=1\n"+envFileSecret+"\n"), 0o600))
	jr = runFast(t, env, dir, "doctor")
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "fail", c["env_file"].Status)
	assert.Contains(t, c["env_file"].Detail, path+":2:")
	assert.NotContains(t, jr.stdout, envFileSecret)
}

func TestNoModelCommandsIgnoreBrokenEnvFile(t *testing.T) {
	dir := demoProject(t)
	env := isolatedEnv(t)
	env["SPEC_ENVFILE_KEY"] = ""
	writeUserConfig(t, env, envFileConfig("http://127.0.0.1:1"))
	for _, mode := range []os.FileMode{0o644, 0} {
		if mode == 0 {
			require.NoError(t, os.Remove(writeEnvFile(t, env, "x=1\n", 0o600)))
		} else {
			writeEnvFile(t, env, "SPEC_ENVFILE_KEY="+envFileSecret+"\n", mode)
		}
		for _, args := range [][]string{{"lint"}, {"ls"}, {"status"}} {
			r := runIn(t, env, dir, args...)
			assert.NotEqual(t, 1, r.code, "spec %v: %s%s", args, r.stdout, r.stderr)
			assert.NotContains(t, r.stdout+r.stderr, "env_file", "spec %v", args)
		}
	}
}
