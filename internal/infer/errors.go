// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Error is a failed call: an HTTP error status, a malformed reply, or a
// network failure. Its text never contains the API key.
type Error struct {
	// Provider and Model name what was called.
	Provider string
	Model    string
	// Status is the HTTP status, 0 when no response arrived. For a CLI
	// provider it is the API status the CLI reported, when it did.
	Status int
	// Via names the CLI that made the call ("claude -p"); "" for HTTP.
	Via string
	// Message is the provider's own error text, redacted and shortened.
	Message string
	// Attempts is how many requests were sent.
	Attempts int
	// Retryable says the failure was transient (it was retried while
	// attempts remained).
	Retryable bool
	// cause is the underlying error; its text is redacted in Error().
	cause error
	// fix, when set, replaces Fix's HTTP-oriented advice (a CLI provider's
	// failures).
	fix string
	// redact scrubs secrets from text.
	redact func(string) string
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "provider %s", e.Provider)
	if e.Model != "" {
		fmt.Fprintf(&b, " (model %s)", e.Model)
	}
	b.WriteString(": ")
	b.WriteString(e.short())
	if e.Attempts > 1 {
		fmt.Fprintf(&b, " (after %d attempts)", e.Attempts)
	}
	return b.String()
}

// short is the failure without the provider prefix.
func (e *Error) short() string {
	var parts []string
	if e.Via != "" {
		parts = append(parts, e.Via)
	}
	if e.Status != 0 && e.Via != "" {
		parts = append(parts, fmt.Sprintf("API error %d", e.Status))
	} else if e.Status != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d %s", e.Status, http.StatusText(e.Status)))
	}
	if e.cause != nil {
		parts = append(parts, e.scrub(e.cause.Error()))
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if len(parts) == 0 {
		return "request failed"
	}
	return strings.Join(parts, ": ")
}

func (e *Error) scrub(s string) string {
	if e.redact == nil {
		return redactBearer(s)
	}
	return e.redact(s)
}

// Unwrap returns the underlying error (a network error, for example), so
// errors.Is works for timeouts. Its own text is not redacted: print the
// *Error, not the cause.
func (e *Error) Unwrap() error { return e.cause }

// Fix says how a user might resolve the failure.
func (e *Error) Fix() string {
	switch {
	case e.fix != "":
		return e.fix
	case errors.Is(e.cause, ErrIdleTimeout):
		return "the provider stopped sending mid-reply; it may be overloaded: retry later, or raise the provider's idle_timeout (seconds) in the user config"
	case e.Status == http.StatusRequestTimeout || errors.Is(e.cause, context.DeadlineExceeded):
		return "the model did not finish in time; a reasoning model on a long check can think for minutes: lower its effort (for example extra = { reasoning_effort = \"low\" }) or raise the provider's timeout (seconds) in the user config; a provider with stream = false is cut off sooner (Cloudflare: about 235 s)"
	case e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden:
		return "the provider refused the API key: check the variable named by api_key_env, and that the key may use this model"
	case e.Status == http.StatusNotFound:
		return "check base_url and the model id (`--engine ID` tries another; `spec explain config` has working examples)"
	case e.Status == http.StatusTooManyRequests:
		return "the provider is rate limiting; wait and retry, or pick another provider with --provider NAME"
	case e.Status == http.StatusBadRequest || e.Status == http.StatusUnprocessableEntity:
		return "the provider rejected the request; check the model id and any extra settings for this provider"
	case e.Status >= 500:
		return "the provider is failing; retry later, or pick another provider with --provider NAME"
	case e.Status == 0:
		return "the provider could not be reached: check base_url and the network"
	}
	return "check base_url, the model id and the provider's status"
}

// newError builds an *Error whose texts are redacted with this client's
// key.
func (c *Client) newError(status int, msg string, cause error, retryable bool) *Error {
	e := &Error{Provider: c.Name, Status: status, Retryable: retryable, cause: cause, redact: c.redact}
	e.Message = c.redact(msg)
	return e
}

// bodyMessage is providerMessage of a body redacted first, and the decoded
// message redacted again before it is shortened (a key echoed JSON-escaped,
// "\/" or "\u0061", only appears once decoded), so shortening can never keep
// part of a key.
func (c *Client) bodyMessage(raw []byte) string {
	return providerMessage([]byte(c.redact(string(raw))), c.redact)
}

// bodySnippet is snippet of a body redacted first.
func (c *Client) bodySnippet(raw []byte) string {
	return snippet([]byte(c.redact(string(raw))))
}

// redact removes the API key and any bearer token from s.
func (c *Client) redact(s string) string {
	if k := c.apiKey(); len(k) >= 4 {
		s = strings.ReplaceAll(s, k, "[redacted]")
	}
	return redactBearer(s)
}

var bearer = regexp.MustCompile(`(?i)(bearer\s+)[^\s"',;]+`)

func redactBearer(s string) string {
	return bearer.ReplaceAllString(s, "${1}[redacted]")
}

// maxMessage bounds the provider text kept in an error.
const maxMessage = 300

// providerMessage extracts the error text from an error body: OpenAI's
// {"error": {"message"}}, a plain {"error": "..."}, Cloudflare's
// {"errors": [{"message"}]}, or {"message"}. It falls back to a snippet of
// the body. scrub is applied to a decoded message before it is shortened.
func providerMessage(raw []byte, scrub func(string) string) string {
	var v struct {
		Error   json.RawMessage            `json:"error"`
		Errors  []struct{ Message string } `json:"errors"`
		Message string                     `json:"message"`
	}
	if err := json.Unmarshal(raw, &v); err == nil {
		var obj struct {
			Message string `json:"message"`
		}
		var str string
		switch {
		case len(v.Error) > 0 && json.Unmarshal(v.Error, &obj) == nil && obj.Message != "":
			return shorten(scrub(obj.Message))
		case len(v.Error) > 0 && json.Unmarshal(v.Error, &str) == nil && str != "":
			return shorten(scrub(str))
		case len(v.Errors) > 0 && v.Errors[0].Message != "":
			return shorten(scrub(v.Errors[0].Message))
		case v.Message != "":
			return shorten(scrub(v.Message))
		}
	}
	return snippet(raw)
}

// snippet is the start of a body, whitespace collapsed, for an error.
func snippet(raw []byte) string {
	return shorten(strings.Join(strings.Fields(string(raw)), " "))
}

func shorten(s string) string {
	if len(s) <= maxMessage {
		return s
	}
	cut := maxMessage
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
