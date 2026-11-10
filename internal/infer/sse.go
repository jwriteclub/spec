// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// maxSSELine bounds one line of a streamed reply; a chunk is a few hundred
// bytes, so a longer line is a broken stream.
const maxSSELine = 4 << 20

// errSSELineTooLong is a stream line over maxSSELine.
var errSSELineTooLong = errors.New("a line of the stream is longer than 4 MiB")

// sseEvent is one server-sent event: its type ("" for the default) and its
// data lines joined with "\n".
type sseEvent struct {
	Event string
	Data  string
}

// sseReader splits a text/event-stream into events. Lines may end in LF,
// CRLF or CR; lines that start with ':' are comments (keep-alives such as
// ": ping"); an event ends at a blank line, or at the end of the stream.
type sseReader struct {
	r *bufio.Reader
}

func newSSEReader(r io.Reader) *sseReader {
	if br, ok := r.(*bufio.Reader); ok {
		return &sseReader{r: br}
	}
	return &sseReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// readLine returns the next line without its ending. A last line without
// an ending is returned before io.EOF.
func (s *sseReader) readLine() (string, error) {
	var buf []byte
	for {
		b, err := s.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && len(buf) > 0 {
				return string(buf), nil
			}
			return "", err
		}
		switch b {
		case '\n':
			return string(buf), nil
		case '\r':
			if next, err := s.r.Peek(1); err == nil && next[0] == '\n' {
				_, _ = s.r.ReadByte()
			}
			return string(buf), nil
		}
		if len(buf) >= maxSSELine {
			return "", errSSELineTooLong
		}
		buf = append(buf, b)
	}
}

// Next returns the next event that carries data. At the end of the stream
// it returns io.EOF (after any event left without a closing blank line).
func (s *sseReader) Next() (sseEvent, error) {
	var ev sseEvent
	var data []string
	for {
		line, err := s.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) && data != nil {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			return sseEvent{}, err
		}
		if line == "" {
			if data != nil {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			ev = sseEvent{}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			ev.Event = value
		}
	}
}

// payloads splits an event's data into JSON payloads. Data lines are joined
// with "\n" as the format says; a server that sends one chunk per data line
// without blank lines between them gets each line read on its own.
func payloads(data string) []string {
	trimmed := strings.TrimSpace(data)
	if trimmed == "[DONE]" || json.Valid([]byte(trimmed)) || !strings.Contains(trimmed, "\n") {
		return []string{trimmed}
	}
	var out []string
	for _, l := range strings.Split(trimmed, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// streamChunk is the subset of a chat.completion.chunk we read, plus the
// error shapes a provider may send in its place.
type streamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			Reasoning        string          `json:"reasoning"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage  *wireUsage        `json:"usage"`
	Error  json.RawMessage   `json:"error"`
	Errors []json.RawMessage `json:"errors"`
}

// streamError is an error event in a stream: its decoded message (not yet
// redacted) and a numeric code when it had one.
type streamError struct {
	raw  []byte
	code int
}

func (e *streamError) Error() string { return "the stream carried an error" }

// accumulator builds a reply from stream chunks.
type accumulator struct {
	model        string
	content      strings.Builder
	reasoning    strings.Builder
	reasoningAlt strings.Builder
	finish       string
	usage        *wireUsage
	sawChoice    bool
	chunks       int
	done         bool
}

// errStreamTooLarge is a streamed reply over maxResponseBytes of text.
var errStreamTooLarge = errors.New("the streamed reply is larger than 16 MiB")

// add folds one data payload into the reply. It returns a *streamError for
// an error event, and marks the end at [DONE].
func (a *accumulator) add(payload string) error {
	if payload == "" {
		return nil
	}
	if payload == "[DONE]" {
		a.done = true
		return nil
	}
	var ch streamChunk
	if err := json.Unmarshal([]byte(payload), &ch); err != nil {
		// Not a chunk (a provider's keep-alive text, say): skip it. A
		// stream of nothing else ends as errNotStream.
		lg.Debugf("skipping a stream event that is not JSON (%d bytes)", len(payload))
		return nil
	}
	a.chunks++
	if isError(ch.Error) || len(ch.Errors) > 0 {
		return &streamError{raw: []byte(payload), code: chunkErrorCode(ch)}
	}
	if ch.Model != "" {
		a.model = ch.Model
	}
	if ch.Usage != nil {
		u := *ch.Usage
		a.usage = &u
	}
	for _, c := range ch.Choices {
		if c.Index != 0 {
			continue
		}
		a.sawChoice = true
		a.content.WriteString(contentText(c.Delta.Content))
		a.reasoning.WriteString(c.Delta.ReasoningContent)
		a.reasoningAlt.WriteString(c.Delta.Reasoning)
		if c.FinishReason != "" {
			a.finish = c.FinishReason
		}
	}
	if a.content.Len()+a.reasoning.Len()+a.reasoningAlt.Len() > maxResponseBytes {
		return errStreamTooLarge
	}
	return nil
}

// reply returns what was accumulated.
func (a *accumulator) reply() *reply {
	r := &reply{
		Model:        a.model,
		Content:      a.content.String(),
		Reasoning:    a.reasoning.String(),
		FinishReason: a.finish,
		Streamed:     true,
	}
	if r.Reasoning == "" {
		r.Reasoning = a.reasoningAlt.String()
	}
	if a.usage != nil {
		r.Usage = a.usage.filled()
		r.CostUSD = a.usage.Cost
	}
	return r
}

// isError reports whether an "error" field is present and not null, false
// or empty.
func isError(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "false" && s != `""` && s != "{}"
}

// chunkErrorCode returns a numeric code from an error event, or 0.
func chunkErrorCode(ch streamChunk) int {
	if isError(ch.Error) {
		return codeOf(ch.Error)
	}
	if len(ch.Errors) > 0 {
		return codeOf(ch.Errors[0])
	}
	return 0
}

// readStream reads a whole event stream into a reply. Errors are plain
// errors or a *streamError; the caller classifies them.
func readStream(r io.Reader) (*reply, error) {
	sr := newSSEReader(r)
	var a accumulator
	for !a.done {
		ev, err := sr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if ev.Event == "error" {
			return nil, &streamError{raw: []byte(ev.Data), code: codeOf(json.RawMessage(ev.Data))}
		}
		for _, p := range payloads(ev.Data) {
			if err := a.add(p); err != nil {
				return nil, err
			}
			if a.done {
				break
			}
		}
	}
	switch {
	case a.chunks == 0 && !a.done:
		return nil, errNotStream
	case !a.done && a.finish == "":
		return nil, errStreamCut
	case !a.sawChoice:
		return nil, errNoChoices
	}
	return a.reply(), nil
}

var (
	// errStreamCut is a stream that ended before a finish_reason or [DONE].
	errStreamCut = errors.New("the stream ended before the reply was complete")
	// errNoChoices is a reply without a choice.
	errNoChoices = errors.New("the response has no choices")
	// errNotStream is a body with no JSON chunk at all.
	errNotStream = errors.New("the response is neither JSON nor an event stream")
)
