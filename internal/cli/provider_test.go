// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/infer"
)

// providerServer is a fake OpenAI-compatible provider that records request
// bodies and Authorization headers.
type providerServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	auths  []string
}

func (p *providerServer) requests() ([]map[string]any, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.bodies...), append([]string(nil), p.auths...)
}

func fakeProvider(t *testing.T, reply http.HandlerFunc) *providerServer {
	t.Helper()
	p := &providerServer{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		p.mu.Lock()
		p.bodies = append(p.bodies, b)
		p.auths = append(p.auths, r.Header.Get("Authorization"))
		p.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		reply(w, r)
	}))
	t.Cleanup(p.Close)
	return p
}

func okReply(content string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   b.Model + "-echo",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 1, "total_tokens": 13},
		})
	}
}

// runFast runs spec with retry backoff removed.
func runFast(t *testing.T, env testEnv, dir string, args ...string) jsonResult {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(append(append([]string{}, args...), "--json"), Env{
		Stdout: &out, Stderr: &errb, Cwd: dir, Getenv: env.get,
		tuneClient: func(c *infer.Client) { c.BaseDelay = time.Millisecond; c.MaxDelay = time.Millisecond },
	})
	return decodeEnvelope(t, result{code: code, stdout: out.String(), stderr: errb.String()}, args)
}

func writeUserConfig(t *testing.T, env testEnv, text string) {
	t.Helper()
	cfg := filepath.Join(env["XDG_CONFIG_HOME"], "spec", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfg), 0o755))
	require.NoError(t, os.WriteFile(cfg, []byte(text), 0o644))
}

func doctorChecks(t *testing.T, jr jsonResult) (doctorData, map[string]doctorCheck) {
	t.Helper()
	var d doctorData
	jr.decode(t, &d)
	m := map[string]doctorCheck{}
	for _, c := range d.Checks {
		m[c.Name] = c
	}
	return d, m
}

const doctorKey = "sk-doctor-test-0123456789"

func TestDoctorLiveCheck(t *testing.T) {
	dir := demoProject(t)
	env := isolatedEnv(t)
	env["SPEC_DOCTOR_KEY"] = doctorKey
	env["SPEC_DOCTOR_ACCOUNT"] = "acct42"
	srv := fakeProvider(t, okReply("OK"))
	writeUserConfig(t, env, `default = "fake"

[providers.fake]
base_url = "`+srv.URL+`/accounts/${SPEC_DOCTOR_ACCOUNT}/v1"
model = "main-model"
triage_model = "triage-model"
api_key_env = "SPEC_DOCTOR_KEY"
extra = { reasoning_effort = "none" }

[providers.other]
base_url = "`+srv.URL+`/v1"
model = "other-model"
`)

	jr := runFast(t, env, dir, "doctor")
	require.Equal(t, 0, jr.code, jr.stdout)
	d, c := doctorChecks(t, jr)
	assert.Equal(t, "ok", c["inference"].Status)
	assert.Contains(t, c["inference"].Detail, "provider fake, model main-model: answered in")
	assert.Contains(t, c["inference"].Detail, "model id echoed: main-model-echo")
	assert.Contains(t, c["inference"].Detail, `reply "OK"`)
	require.NotNil(t, d.Inference)
	assert.Equal(t, doctorInference{
		Provider: "fake", Kind: "openai", Model: "main-model", EchoedModel: "main-model-echo", LatencyMS: d.Inference.LatencyMS,
		PromptTokens: 12, CompletionTokens: 1, TotalTokens: 13, FinishReason: "stop", Reply: "OK",
	}, *d.Inference)
	assert.NotContains(t, jr.stdout, doctorKey)

	bodies, auths := srv.requests()
	require.Len(t, bodies, 1)
	assert.Equal(t, "Bearer "+doctorKey, auths[0])
	assert.Equal(t, "main-model", bodies[0]["model"])
	assert.Equal(t, float64(8), bodies[0]["max_tokens"])
	assert.Equal(t, "none", bodies[0]["reasoning_effort"])
	assert.Equal(t, []any{map[string]any{"role": "user", "content": "Reply with the single word OK"}}, bodies[0]["messages"])

	// --provider and --engine choose another provider and model.
	jr = runFast(t, env, dir, "doctor", "--provider", "other", "--engine", "picked")
	require.Equal(t, 0, jr.code, jr.stdout)
	_, c = doctorChecks(t, jr)
	assert.Contains(t, c["provider_key"].Detail, "provider other names no api_key_env")
	assert.Contains(t, c["inference"].Detail, "provider other, model picked")
	bodies, auths = srv.requests()
	assert.Equal(t, "picked", bodies[1]["model"])
	assert.Empty(t, auths[1])

	// An unknown --provider fails and names the defined ones.
	jr = runFast(t, env, dir, "doctor", "--provider", "nope")
	assert.Equal(t, 1, jr.code)
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "fail", c["provider_key"].Status)
	assert.Contains(t, c["provider_key"].Detail, "defined: fake, other")
	assert.Equal(t, "skipped", c["inference"].Status)

	// A base_url variable that is unset warns and says how to set it.
	delete(env, "SPEC_DOCTOR_ACCOUNT")
	env["SPEC_DOCTOR_ACCOUNT"] = ""
	jr = runFast(t, env, dir, "doctor")
	require.Equal(t, 0, jr.code, jr.stdout)
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "warn", c["provider_key"].Status)
	assert.Contains(t, c["provider_key"].Detail, "${SPEC_DOCTOR_ACCOUNT}")
	assert.Contains(t, c["provider_key"].Fix, "export SPEC_DOCTOR_ACCOUNT=")
	assert.Equal(t, "skipped", c["inference"].Status)
	assert.Len(t, func() []map[string]any { b, _ := srv.requests(); return b }(), 2, "no request without a ready provider")
}

// TestDoctorLiveCheckStreams: the live check takes the streamed path every
// check takes, and says whether the reply was streamed.
func TestDoctorLiveCheckStreams(t *testing.T) {
	dir := demoProject(t)
	env := isolatedEnv(t)
	var srv *providerServer
	srv = fakeProvider(t, func(w http.ResponseWriter, r *http.Request) {
		bodies, _ := srv.requests()
		if bodies[len(bodies)-1]["stream"] != true {
			okReply("OK")(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": ping\n\n"+
			`data: {"model":"glm-echo","choices":[{"index":0,"delta":{"reasoning_content":"The user"}}]}`+"\n\n"+
			`data: {"model":"glm-echo","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}]}`+"\n\n"+
			`data: {"model":"glm-echo","choices":[],"usage":{"prompt_tokens":18,"completion_tokens":6,"total_tokens":24}}`+"\n\n"+
			"data: [DONE]\n\n")
	})
	writeUserConfig(t, env, "default = \"cf\"\n[providers.cf]\nbase_url = \""+srv.URL+"\"\nmodel = \"glm\"\n")
	jr := runFast(t, env, dir, "doctor")
	require.Equal(t, 0, jr.code, jr.stdout)
	d, c := doctorChecks(t, jr)
	assert.Contains(t, c["inference"].Detail, "(streamed; model id echoed: glm-echo; 18 prompt + 6 completion tokens)")
	require.NotNil(t, d.Inference)
	assert.True(t, d.Inference.Streamed)
	assert.Equal(t, "OK", d.Inference.Reply)
	assert.Equal(t, 24, d.Inference.TotalTokens)
	bodies, _ := srv.requests()
	assert.Equal(t, map[string]any{"include_usage": true}, bodies[0]["stream_options"])

	// stream = false: one JSON reply, reported as not streamed.
	writeUserConfig(t, env, "default = \"cf\"\n[providers.cf]\nbase_url = \""+srv.URL+"\"\nmodel = \"glm\"\nstream = false\n")
	jr = runFast(t, env, dir, "doctor")
	require.Equal(t, 0, jr.code, jr.stdout)
	d, c = doctorChecks(t, jr)
	assert.Contains(t, c["inference"].Detail, "(not streamed;")
	assert.False(t, d.Inference.Streamed)
	bodies, _ = srv.requests()
	assert.NotContains(t, bodies[1], "stream")
}

func TestDoctorLiveCheckReasoningCutOff(t *testing.T) {
	dir := demoProject(t)
	env := isolatedEnv(t)
	srv := fakeProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"model":"glm","choices":[{"message":{"content":"","reasoning_content":"The user wants"},"finish_reason":"length"}],"usage":{"prompt_tokens":18,"completion_tokens":8}}`)
	})
	writeUserConfig(t, env, "default = \"cf\"\n[providers.cf]\nbase_url = \""+srv.URL+"\"\nmodel = \"glm\"\n")
	jr := runFast(t, env, dir, "doctor")
	require.Equal(t, 0, jr.code, jr.stdout)
	d, c := doctorChecks(t, jr)
	assert.Equal(t, "ok", c["inference"].Status)
	assert.Contains(t, c["inference"].Detail, "still reasoning when the 8-token cap cut it off")
	assert.Equal(t, "length", d.Inference.FinishReason)
	assert.Equal(t, 8, d.Inference.CompletionTokens)
}

func TestDoctorLiveCheckFailures(t *testing.T) {
	dir := demoProject(t)
	env := isolatedEnv(t)
	env["SPEC_DOCTOR_KEY"] = doctorKey

	// The provider refuses the key, and echoes it: the key never reaches
	// the output.
	srv := fakeProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid key `+doctorKey+` (`+r.Header.Get("Authorization")+`)"}}`)
	})
	writeUserConfig(t, env, "default = \"p\"\n[providers.p]\nbase_url = \""+srv.URL+"\"\nmodel = \"m\"\napi_key_env = \"SPEC_DOCTOR_KEY\"\n")
	jr := runFast(t, env, dir, "doctor")
	assert.Equal(t, 1, jr.code)
	assert.Equal(t, "doctor_failed", jr.errCode)
	d, c := doctorChecks(t, jr)
	assert.Equal(t, "fail", c["inference"].Status)
	assert.Contains(t, c["inference"].Detail, "HTTP 401")
	assert.Contains(t, c["inference"].Fix, "refused the API key")
	assert.Nil(t, d.Inference)
	assert.NotContains(t, jr.stdout, doctorKey)
	assert.Contains(t, jr.stdout, "[redacted]")
	bodies, _ := srv.requests()
	assert.Len(t, bodies, 1, "401 is not retried")

	// Human output too.
	var out, errb bytes.Buffer
	code := Run([]string{"doctor"}, Env{Stdout: &out, Stderr: &errb, Cwd: dir, Getenv: env.get})
	assert.Equal(t, 1, code)
	assert.Contains(t, out.String(), "fail    inference")
	assert.NotContains(t, out.String()+errb.String(), doctorKey)

	// An unreachable provider: retried, then reported with a fix.
	srv.Close()
	jr = runFast(t, env, dir, "doctor")
	assert.Equal(t, 1, jr.code)
	_, c = doctorChecks(t, jr)
	assert.Equal(t, "fail", c["inference"].Status)
	assert.Contains(t, c["inference"].Detail, "after 2 attempts")
	assert.Contains(t, c["inference"].Fix, "could not be reached")
}

func TestOpenProvider(t *testing.T) {
	env := isolatedEnv(t)
	s := &session{env: Env{Getenv: env.get}}
	_, o := s.openProvider()
	require.NotNil(t, o)
	assert.Equal(t, "provider_unavailable", o.code)
	assert.Contains(t, o.message, "no user config")

	writeUserConfig(t, env, "default = \"p\"\n[providers.p]\nbase_url = \"http://localhost:1/v1\"\nmodel = \"m\"\ntriage_model = \"t\"\napi_key_env = \"SPEC_OPEN_KEY\"\n")
	env["SPEC_OPEN_KEY"] = ""
	_, o = s.openProvider()
	require.NotNil(t, o)
	assert.Contains(t, o.message, "SPEC_OPEN_KEY")
	assert.Contains(t, o.message, "export SPEC_OPEN_KEY=")

	env["SPEC_OPEN_KEY"] = "k-123456"
	c, o := s.openProvider()
	require.Nil(t, o)
	assert.Equal(t, "m", c.Info().Model)
	assert.Equal(t, "t", c.Info().TriageModel)
	assert.Equal(t, "openai", c.Info().Kind)
	assert.Equal(t, "", c.Info().Effort)

	s.provider = providerFlags{model: "x", effort: "high"}
	c, o = s.openProvider()
	require.Nil(t, o)
	assert.Equal(t, "x", c.Info().Model)
	assert.Equal(t, "x", c.Info().TriageModel)
	assert.Equal(t, "high", c.Info().Effort)

	s.provider = providerFlags{effort: "max"}
	_, o = s.openProvider()
	require.NotNil(t, o)
	assert.Contains(t, o.message, `--effort "max" is not a level of provider p (kind openai)`)
	assert.Contains(t, o.message, "none, minimal, low, medium, high, xhigh")

	s.provider = providerFlags{name: "q"}
	_, o = s.openProvider()
	require.NotNil(t, o)
	assert.Contains(t, o.message, `no provider "q"`)

	writeUserConfig(t, env, "bogus = 1\n")
	_, o = s.openProvider()
	require.NotNil(t, o)
	assert.True(t, strings.Contains(o.message, "bogus"), o.message)

	o = providerErr(&infer.Error{Provider: "p", Status: 429})
	assert.Equal(t, "provider_failed", o.code)
	assert.Contains(t, o.message, "rate limiting")
	assert.Equal(t, 1, o.exit)
}

// TestNoModelFlag checks that no command registers --model: the LLM model id
// is --engine and a TLA+ model is --tla, so --model could only confuse.
func TestNoModelFlag(t *testing.T) {
	root := (&session{}).rootCommand()
	withEngine := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		assert.Nil(t, c.Flags().Lookup("model"), "spec %s registers --model", c.CommandPath())
		assert.Nil(t, c.PersistentFlags().Lookup("model"), "spec %s registers --model", c.CommandPath())
		if c.Flags().Lookup("engine") != nil {
			withEngine++
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	assert.Positive(t, withEngine, "some command takes --engine")
}
