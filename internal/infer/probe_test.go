// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jwriteclub/spec/internal/config"
	"github.com/stretchr/testify/require"
)

func TestProbeUsesOnlySyntheticTextAndPrivateCredential(t *testing.T) {
	const key = "test-session-credential"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		body, _ := json.Marshal(req)
		require.Contains(t, string(body), "Reply with the single word OK")
		require.NotContains(t, string(body), key)
		require.Equal(t, float64(8), req["max_tokens"])
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"fixture/model","choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	p := config.Provider{Kind: "openai", BaseURL: srv.URL, Model: "fixture/model", APIKeyEnv: "KEY"}
	result, err := ProbeProvider(context.Background(), "fixture", p, "check", key, func(string) string { return "" })
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, "fixture/model", result.Model)
	require.Contains(t, result.Summary, "Test passed")
	require.Equal(t, "KEY", p.APIKeyEnv, "probe must not mutate the saved provider")
}
func TestProbeRedactsProviderEchoAndHonorsCancel(t *testing.T) {
	const key = "fixture-private-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"message":"echo `+key+`"}}`)
	}))
	defer srv.Close()
	p := config.Provider{Kind: "openai", BaseURL: srv.URL, Model: "fixture/model", APIKeyEnv: "KEY"}
	for _, session := range []string{"", key} {
		_, err := ProbeProvider(context.Background(), "fixture", p, "check", session, func(string) string { return key })
		require.Error(t, err)
		require.False(t, strings.Contains(err.Error(), key))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ProbeProvider(ctx, "fixture", p, "check", key, func(string) string { return "" })
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled), "redaction must preserve cancellation identity")
}

// probeServer answers every chat request with body.
func probeServer(t *testing.T, body string) config.Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return config.Provider{Kind: "openai", BaseURL: srv.URL, Model: "fixture/model", APIKeyEnv: "KEY"}
}

// A reasoning model spends the probe's 8 tokens thinking. The request
// authenticated and reached the model, so the summary is a plain pass
// (it read "Request succeeded · reasoning reached test cap"); an empty
// reply with no reasoning stays a failure.
func TestProbeSummaryClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body, want, wantErr string
	}{
		{"answer", `{"model":"m","choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`, "Test passed · ", ""},
		{"reasoning at cap", `{"model":"m","choices":[{"message":{"content":"","reasoning":"The user wants"},"finish_reason":"length"}]}`, " · reasoning model", ""},
		{"hidden reasoning at cap", `{"model":"m","choices":[{"message":{"content":""},"finish_reason":"length"}]}`, " · reasoning model", ""},
		{"no model ID", `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`, " · model ID not reported", ""},
		{"empty reply", `{"model":"m","choices":[{"message":{"content":""},"finish_reason":"stop"}]}`, "", "empty reply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := probeServer(t, tc.body)
			for _, role := range []string{"check", "triage"} {
				result, err := ProbeProvider(context.Background(), "fixture", p, role, "k", func(string) string { return "" })
				if tc.wantErr != "" {
					require.ErrorContains(t, err, tc.wantErr)
					continue
				}
				require.NoError(t, err)
				require.True(t, strings.HasPrefix(result.Summary, "Test passed · "), result.Summary)
				require.Contains(t, result.Summary, tc.want)
				require.NotContains(t, result.Summary, "test cap")
			}
		})
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer failing.Close()
	_, err := ProbeProvider(context.Background(), "fixture", config.Provider{Kind: "openai", BaseURL: failing.URL, Model: "m", APIKeyEnv: "KEY"}, "check", "k", func(string) string { return "" })
	require.Error(t, err, "an auth failure is never a pass")
}
