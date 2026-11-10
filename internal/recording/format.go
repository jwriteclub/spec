// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package recording is the on-disk form of a recorded `spec ui` session
// (`spec ui --record DIR`): the starting workspace, the stream of input
// events, and every model reply in canned form, so the session can be
// played back through whatever build is current
// (notes/plan-record-replay.md).
package recording

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jwriteclub/spec/internal/infer"
)

// The files of a recording directory.
const (
	MetaFile     = "meta.json"
	WorkspaceTar = "workspace.tar"
	HeadTar      = "head.tar"
	IndexTar     = "index.tar"
	ConfigFile   = "config.toml"
	CannedFile   = "canned.toml"
	RequestsFile = "requests.jsonl"
	SessionFile  = "session.jsonl"
	FinalFile    = "final.txt"
	FinalPlain   = "final.plain.txt"
	// ReplayLog is what the last replay did not reproduce
	// (`spec ui --replay` writes it).
	ReplayLog = "replay.log"
	// ReplayFinal is the last replay's final frame without styles, to
	// compare with FinalPlain.
	ReplayFinal   = "replay.final.plain.txt"
	FormatVersion = 1
)

// CannedPathToken stands for the absolute path of the replies file in a
// recording's config.toml; replay substitutes it.
const CannedPathToken = "@CANNED@"

// Meta describes a recording (meta.json). It is written when recording
// starts and rewritten when it ends.
type Meta struct {
	Version int `json:"version"`
	// Start is the wall time recording started.
	Start time.Time `json:"start"`
	// Cols and Rows are the terminal's first size.
	Cols int `json:"cols"`
	Rows int `json:"rows"`
	// Dark is the theme the terminal's background chose (true when it
	// reported none).
	Dark bool `json:"dark"`
	// Target is the command line's place to open ("" for the default).
	Target        string `json:"target,omitempty"`
	HashToSection bool   `json:"hash_to_section"`
	// Provider, Model and Effort are --provider, --engine and --effort.
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort,omitempty"`
	// GitHead is HEAD when recording started ("" without git); FinalHead
	// is HEAD when it ended.
	GitHead   string `json:"git_head"`
	FinalHead string `json:"final_head,omitempty"`
	// IDsSeed seeds the proposal ids (64 hex digits).
	IDsSeed string `json:"ids_seed"`
	// Build is the vcs revision of the recording binary.
	Build string `json:"build,omitempty"`
	// End is the wall time recording ended (zero while it runs).
	End        time.Time `json:"end,omitzero"`
	Events     int       `json:"events"`
	ModelCalls int       `json:"model_calls"`
}

// Event kinds.
const (
	KindKey    = "key"
	KindPaste  = "paste"
	KindResize = "resize"
	KindTheme  = "theme"
	KindFiles  = "files"
	KindMouse  = "mouse"
	KindEnd    = "end"
)

// Event is one line of session.jsonl. Every event has Seq (from 1), T (ms
// since recording started), Keys (the key presses so far in tape units,
// this one included for a key) and Kind; the other fields depend on Kind.
type Event struct {
	Seq  int    `json:"seq"`
	T    int64  `json:"t"`
	Keys int    `json:"keys"`
	Kind string `json:"kind"`

	// Key is the key's string (tea String(): "ctrl+o", "a"); Code its
	// rune, Repeat whether it was auto-repeated.
	Key    string `json:"key,omitempty"`
	Code   rune   `json:"code,omitempty"`
	Repeat bool   `json:"repeat,omitempty"`
	// Text is a key's text, or a paste's.
	Text string `json:"text,omitempty"`
	// Mod is the modifiers of a key or a mouse event (tea.KeyMod).
	Mod int `json:"mod,omitempty"`

	// Cols and Rows are a resize's size.
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`
	// Dark is a theme event's background.
	Dark *bool `json:"dark,omitempty"`

	// Source is what reported a files event: SourceWatcher or
	// SourceProposals.
	Source  string       `json:"source,omitempty"`
	Changes []FileChange `json:"changes,omitempty"`

	*Mouse

	// FrameSHA is the sha256 of the final plain frame (end).
	FrameSHA string `json:"frame_sha,omitempty"`
}

// Sources of a files event.
const (
	SourceWatcher   = "watcher"
	SourceProposals = "proposals"
)

// FileChange is one file another process changed: its new content (as
// text when it is valid UTF-8, else base64) or its removal.
type FileChange struct {
	// Path is project-relative, with slashes.
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	ContentB64 string `json:"content_b64,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
}

// ChangeOf builds the change of path to data (nil data with deleted).
func ChangeOf(path string, data []byte, deleted bool) FileChange {
	c := FileChange{Path: path, Deleted: deleted}
	switch {
	case deleted:
	case utf8.Valid(data):
		c.Content = string(data)
	default:
		c.ContentB64 = base64.StdEncoding.EncodeToString(data)
	}
	return c
}

// Bytes returns the change's content.
func (c FileChange) Bytes() ([]byte, error) {
	if c.ContentB64 != "" {
		return base64.StdEncoding.DecodeString(c.ContentB64)
	}
	return []byte(c.Content), nil
}

// Mouse actions.
const (
	MouseClick   = "click"
	MouseRelease = "release"
	MouseDrag    = "drag"
	MouseWheel   = "wheel"
	MouseHover   = "hover"
	MouseUnhover = "unhover"
)

// Mouse is a mouse event's semantic effect, with the raw cell and button
// as the fallback when the target cannot be found again.
type Mouse struct {
	Action string `json:"action"`
	// Button is the tea button's name ("left", "wheelup").
	Button string `json:"button,omitempty"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	// Screen is the top screen: main, review, commit, hunkedit or help.
	Screen string `json:"screen"`
	// Pane is where the event landed: editor, gutter, outline, sidebar,
	// list or none.
	Pane   string       `json:"pane,omitempty"`
	Target *MouseTarget `json:"target,omitempty"`
	Result *MouseResult `json:"result,omitempty"`
}

// Pos is a place in a spec file that survives layout changes: a 1-based
// line and a 1-based byte column within it.
type Pos struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

// Span is a selection, from its anchor to the cursor.
type Span struct {
	From Pos `json:"from"`
	To   Pos `json:"to"`
}

// MouseTarget is what a mouse event aimed at, resolved with the layout it
// saw.
type MouseTarget struct {
	// Kind of a hover: anchor, section, diag or gutter.
	Kind string `json:"kind,omitempty"`
	// Pos is the clicked place (an editor click) or the hovered token's
	// start.
	Pos *Pos `json:"pos,omitempty"`
	// Name is a hovered anchor's name (no $), Number a section's number,
	// Code a diagnostic's code.
	Name   string `json:"name,omitempty"`
	Number string `json:"number,omitempty"`
	Code   string `json:"code,omitempty"`
	// Line is a gutter hover's 1-based line, or an outline entry's or
	// list item's line.
	Line int `json:"line,omitempty"`
	// File and Label name an outline entry or a list item.
	File  string `json:"file,omitempty"`
	Label string `json:"label,omitempty"`
	// Proposal and Item name a sidebar card (Item is the card's key);
	// Index is its place, or a review hunk's number.
	Proposal string `json:"proposal,omitempty"`
	Item     string `json:"item,omitempty"`
	Index    *int   `json:"index,omitempty"`
	// Change is a commit-screen change id.
	Change string `json:"change,omitempty"`
	// Outside is a click outside a list popup.
	Outside bool `json:"outside,omitempty"`
	// Direction is a wheel's: up or down.
	Direction string `json:"direction,omitempty"`
}

// MouseResult is what the event did.
type MouseResult struct {
	Cursor    *Pos    `json:"cursor,omitempty"`
	Selection *Span   `json:"selection,omitempty"`
	Sel       *int    `json:"sel,omitempty"`
	Ticked    *bool   `json:"ticked,omitempty"`
	Scroll    *Scroll `json:"scroll,omitempty"`
	// Popup is the first line of the popup opened (plain text).
	Popup string `json:"popup,omitempty"`
}

// Scroll is a pane's scroll position: an editor's top row (Line is
// 0-based, Sub the wrapped row within it), or another pane's offset.
type Scroll struct {
	Line   int `json:"line"`
	Sub    int `json:"sub"`
	Offset int `json:"offset"`
}

// RequestRecord is one line of requests.jsonl: a model call, in the order
// the calls started.
type RequestRecord struct {
	Purpose string `json:"purpose"`
	// Index is the call's place among its purpose's (0-based), which is
	// its entry in canned.toml.
	Index int `json:"index"`
	// T is when the call started (ms since recording started); Keys the
	// key presses by then.
	T        int64  `json:"t"`
	Keys     int    `json:"keys"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Head is infer.RequestHead or infer.GateHead of the request.
	Head string `json:"head"`
	// Request is the whole infer.Request or infer.GateRequest.
	Request   json.RawMessage `json:"request"`
	ElapsedMS int64           `json:"elapsed_ms"`
	// Outcome is ok, cancelled, timeout or error; Error the error's text.
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

// Outcomes of a model call.
const (
	OutcomeOK        = "ok"
	OutcomeCancelled = "cancelled"
	OutcomeTimeout   = "timeout"
	OutcomeError     = "error"
)

// Writer appends events to session.jsonl, numbering them; each event is
// written through at once, so a crash keeps everything before it. It is
// safe for concurrent use.
type Writer struct {
	mu   sync.Mutex
	path string
	f    *os.File
	seq  int
	err  error
}

// NewWriter creates dir/session.jsonl.
func NewWriter(dir string) (*Writer, error) {
	path := filepath.Join(dir, SessionFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{path: path, f: f}, nil
}

// Path is the session file's path.
func (w *Writer) Path() string { return w.path }

// Write numbers ev and appends it. The first error is kept (Err) and
// later writes are dropped.
func (w *Writer) Write(ev Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil || w.f == nil {
		return
	}
	w.seq++
	ev.Seq = w.seq
	b, err := json.Marshal(ev)
	if err == nil {
		_, err = w.f.Write(append(b, '\n'))
	}
	if err != nil {
		w.err = err
	}
}

// Count is the number of events written.
func (w *Writer) Count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}

// Err is the first write error.
func (w *Writer) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Close closes the file and returns the first error.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return w.err
	}
	err := w.f.Close()
	w.f = nil
	if w.err != nil {
		return w.err
	}
	return err
}

// WriteMeta writes dir/meta.json.
func WriteMeta(dir string, m Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(dir, MetaFile), append(b, '\n'))
}

// WriteFileAtomic writes data to path through a temp file and a rename.
func WriteFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".rec-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// Recording is a recording directory read back.
type Recording struct {
	Dir    string
	Meta   Meta
	Events []Event
	// Canned is canned.toml parsed (nil when absent).
	Canned   *infer.CannedFile
	Requests []RequestRecord
	// Final is the last frame (ANSI), "" when absent.
	Final string
}

// Load reads the recording in dir: meta.json and session.jsonl are
// required, the rest optional.
func Load(dir string) (*Recording, error) {
	rec := &Recording{Dir: dir}
	b, err := os.ReadFile(filepath.Join(dir, MetaFile))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &rec.Meta); err != nil {
		return nil, fmt.Errorf("%s: %w", MetaFile, err)
	}
	if rec.Meta.Version != FormatVersion {
		return nil, fmt.Errorf("%s: version %d; this build reads version %d", MetaFile, rec.Meta.Version, FormatVersion)
	}
	if err := readJSONL(filepath.Join(dir, SessionFile), func(line []byte) error {
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return err
		}
		rec.Events = append(rec.Events, ev)
		return nil
	}); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(dir, CannedFile)); err == nil {
		f, err := infer.ParseCanned(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", CannedFile, err)
		}
		rec.Canned = f
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	err = readJSONL(filepath.Join(dir, RequestsFile), func(line []byte) error {
		var r RequestRecord
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		rec.Requests = append(rec.Requests, r)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(dir, FinalFile)); err == nil {
		rec.Final = string(b)
	}
	return rec, nil
}

// readJSONL calls fn with each non-empty line of path.
func readJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
	n := 0
	for sc.Scan() {
		n++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return fmt.Errorf("%s line %d: %w", filepath.Base(path), n, err)
		}
	}
	return sc.Err()
}
