// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jwriteclub/spec/internal/config"
)

// ProbeResult describes only the explicit connection snapshot that was tested.
type ProbeResult struct {
	Summary, Model string
	Latency        time.Duration
}

// probeError preserves cancellation and setup error identity while keeping a
// provider's echoed credential out of the user-visible message.
type probeError struct {
	message string
	cause   error
}

func (e *probeError) Error() string { return e.message }
func (e *probeError) Unwrap() error { return e.cause }

// ProbeProvider sends synthetic connectivity text only after the caller's
// explicit Start action. Saving settings never invokes it.
func ProbeProvider(ctx context.Context, name string, p config.Provider, role, sessionKey string, getenv func(string) string) (result ProbeResult, err error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	redactions := []string{sessionKey}
	if !p.IsCLI() && p.APIKeyEnv != "" {
		redactions = append(redactions, getenv(p.APIKeyEnv))
	}
	original := getenv
	if sessionKey != "" && !p.IsCLI() {
		p.APIKeyEnv = "__SPEC_PROBE_CREDENTIAL"
		getenv = func(k string) string {
			if k == p.APIKeyEnv {
				return sessionKey
			}
			return original(k)
		}
	}
	defer func() {
		if err != nil {
			text := err.Error()
			for _, secret := range redactions {
				if secret != "" {
					text = strings.ReplaceAll(text, secret, "[redacted]")
				}
			}
			err = &probeError{message: text, cause: err}
		}
	}()
	start := time.Now()
	reasoning := false
	if role == "gate" {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		g, e := OpenGate(name, p, getenv)
		if e != nil {
			return result, e
		}
		g.SetLimits(15*time.Second, 1)
		d, e := g.Decide(ctx, GateRequest{Policy: "A spec states requirements.", Situation: "The edit changes nothing.", Questions: []Question{{ID: QuestionMaterialChange, Instructions: "Does the edit change a requirement?"}}})
		if e != nil {
			return result, e
		}
		result.Model = d.Model
	} else {
		if role != "check" && role != "triage" {
			return result, fmt.Errorf("unknown connection test role %q", role)
		}
		bound, attempts := 65*time.Second, 2
		if p.IsCLI() {
			bound, attempts = 30*time.Second, 1
		}
		ctx, cancel := context.WithTimeout(ctx, bound)
		defer cancel()
		c, e := Open(name, p, getenv)
		if e != nil {
			return result, e
		}
		c.SetLimits(30*time.Second, attempts)
		purpose := PurposeCheck
		if role == "triage" {
			purpose = PurposeTriage
		}
		response, e := c.Chat(ctx, Request{Purpose: purpose, Messages: []Message{User("Reply with the single word OK")}, MaxTokens: 8, Triage: role == "triage"})
		if e != nil {
			return result, e
		}
		result.Model = response.Model
		reasoning = response.Content == "" && (response.Reasoning != "" || response.FinishReason == "length")
		if response.Content == "" && !reasoning {
			return result, errors.New("the model returned an empty reply")
		}
	}
	result.Latency = time.Since(start)
	result.Summary = probeSummary(result, reasoning)
	return result, nil
}

// probeSummary words a successful test. A reasoning model spends the
// test's few tokens thinking and returns no content, only reasoning text
// or, where the provider hides it, a "length" finish. The request still
// authenticated and reached the model, so that is a pass too.
func probeSummary(result ProbeResult, reasoning bool) string {
	summary := fmt.Sprintf("Test passed · %d ms", result.Latency.Milliseconds())
	if reasoning {
		summary += " · reasoning model"
	}
	if result.Model == "" {
		summary += " · model ID not reported"
	}
	return summary
}
