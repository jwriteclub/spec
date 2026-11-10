// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrNoJSON is returned by ExtractJSON when the text holds no JSON object.
var ErrNoJSON = errors.New("no JSON object in the reply")

// ExtractJSON finds the JSON object in a model's reply, for providers
// without json_mode and models that wrap their answer anyway. It drops
// <think>...</think> blocks, then tries, in order: the whole text, the body
// of each ``` code fence, and the outermost balanced {...} starting at each
// '{' from the left (the first 64). The first candidate that is a valid JSON object wins.
func ExtractJSON(text string) ([]byte, error) {
	text = stripThink(text)
	trimmed := strings.TrimSpace(text)
	if isObject(trimmed) {
		return []byte(trimmed), nil
	}
	for _, body := range fenceBodies(text) {
		if b := strings.TrimSpace(body); isObject(b) {
			return []byte(b), nil
		}
	}
	tries := 0
	for i := 0; i < len(text) && tries < maxCandidates; i++ {
		if text[i] != '{' {
			continue
		}
		tries++
		end, ok := balanced(text, i)
		if !ok {
			continue
		}
		if cand := text[i:end]; isObject(cand) {
			return []byte(cand), nil
		}
	}
	return nil, ErrNoJSON
}

// maxCandidates bounds how many '{' positions ExtractJSON tries, so a long
// reply full of braces stays linear.
const maxCandidates = 64

// isObject reports whether s is exactly one valid JSON object.
func isObject(s string) bool {
	return strings.HasPrefix(s, "{") && json.Valid([]byte(s))
}

// stripThink removes <think>...</think> blocks (an unclosed one runs to the
// end of the text).
func stripThink(s string) string {
	for {
		i := strings.Index(s, "<think>")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "</think>")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len("</think>"):]
	}
}

// fenceBodies returns the contents of each ``` fence in s, without the
// info string (```json).
func fenceBodies(s string) []string {
	var out []string
	for {
		i := strings.Index(s, "```")
		if i < 0 {
			return out
		}
		rest := s[i+3:]
		nl := strings.IndexByte(rest, '\n')
		if nl < 0 {
			return out
		}
		body := rest[nl+1:]
		j := strings.Index(body, "```")
		if j < 0 {
			out = append(out, body)
			return out
		}
		out = append(out, body[:j])
		s = body[j+3:]
	}
}

// balanced returns the end (exclusive) of the {...} that opens at s[i],
// skipping braces inside JSON strings.
func balanced(s string, i int) (int, bool) {
	depth := 0
	inString := false
	for j := i; j < len(s); j++ {
		ch := s[j]
		if inString {
			switch ch {
			case '\\':
				j++
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j + 1, true
			}
		}
	}
	return 0, false
}

// JSONError is a reply that held no parseable JSON object of the wanted
// shape.
type JSONError struct {
	// Content is the start of the reply.
	Content string
	Err     error
}

func (e *JSONError) Error() string {
	return fmt.Sprintf("the model's reply is not the JSON asked for: %v (reply starts: %q)", e.Err, e.Content)
}

func (e *JSONError) Unwrap() error { return e.Err }

// ChatJSON is the package ChatJSON over c.
func (c *Client) ChatJSON(ctx context.Context, req Request, v any) (*Response, error) {
	return ChatJSON(ctx, c, req, v)
}

// redactor is a provider that knows secrets to scrub from text it shows.
type redactor interface {
	redact(string) string
}

// ChatJSON sends req to p with JSON set, extracts the object from the reply
// and decodes it into v. On a reply without usable JSON it returns the
// Response (for its usage) and a *JSONError.
func ChatJSON(ctx context.Context, p Provider, req Request, v any) (*Response, error) {
	req.JSON = true
	resp, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	scrub := redactBearer
	if r, ok := p.(redactor); ok {
		scrub = r.redact
	}
	raw, err := ExtractJSON(resp.Content)
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(raw))
		err = dec.Decode(v)
	}
	if err != nil {
		return resp, &JSONError{Content: shorten(scrub(resp.Content)), Err: err}
	}
	return resp, nil
}
