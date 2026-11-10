// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package proposal

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/jwriteclub/spec/internal/edit"
	"github.com/jwriteclub/spec/internal/finding"
	"github.com/jwriteclub/spec/internal/infer/specctx"
	"github.com/jwriteclub/spec/internal/mdspec"
	"github.com/jwriteclub/spec/internal/pos"
)

// StaleError says the primary hunk no longer applies: the anchor changed
// since the proposal was made. The whole proposal fails.
type StaleError struct {
	Name   string
	Reason string
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("%s: %s", e.Name, e.Reason)
}

// Prepared is the result of Prepare: the splices to write and what each
// accepted hunk became.
type Prepared struct {
	Edits   []edit.FileEdit
	Applied []int
	Failed  []Failure
}

// Prepare computes the splices for the accepted hunks (by number) against
// spec, the current working tree. A stale primary hunk is a *StaleError; a
// stale finding hunk (its find no longer occurs exactly once, or it now
// overlaps another accepted hunk) fails alone and is listed in Failed.
func (p *Proposal) Prepare(spec *mdspec.Spec, accept map[int]bool) (*Prepared, error) {
	out := &Prepared{Applied: []int{}, Failed: []Failure{}}
	byFile := map[string][]edit.Splice{}
	if pr := p.Primary; pr != nil && accept[pr.N] && !p.AppliedHunk(pr.N) {
		if !pr.InWorktree {
			path, sp, err := pr.splice(spec)
			if err != nil {
				return nil, err
			}
			byFile[path] = append(byFile[path], sp)
		}
		out.Applied = append(out.Applied, pr.N)
	}
	for _, h := range p.Hunks {
		if !accept[h.N] || p.AppliedHunk(h.N) {
			continue
		}
		f := spec.File(h.File)
		if f == nil {
			out.Failed = append(out.Failed, Failure{N: h.N, Reason: h.File + " is no longer a spec file"})
			continue
		}
		sp, why := locate(f.Source(), h, byFile[h.File])
		if why != "" {
			out.Failed = append(out.Failed, Failure{N: h.N, Reason: why})
			continue
		}
		byFile[h.File] = append(byFile[h.File], sp)
		out.Applied = append(out.Applied, h.N)
	}
	paths := make([]string, 0, len(byFile))
	for path := range byFile {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		out.Edits = append(out.Edits, edit.FileEdit{Path: path, Snapshot: spec.File(path).Source(), Splices: byFile[path]})
	}
	return out, nil
}

// locate finds hunk h in src, the current text of its file, as a splice that
// overlaps none of taken; else it says why it no longer applies.
func locate(src []byte, h finding.Hunk, taken []edit.Splice) (edit.Splice, string) {
	text := string(src)
	if c := strings.Count(text, h.Find); c != 1 {
		return edit.Splice{}, fmt.Sprintf("the text to replace occurs %d times in %s now (it must occur exactly once); the file changed since the proposal was made", c, h.File)
	}
	start := strings.Index(text, h.Find)
	r := pos.Range{Start: start, End: start + len(h.Find)}
	for _, o := range taken {
		if r.Start < o.Range.End && o.Range.Start < r.End || r.Start == o.Range.Start && o.Range.Empty() {
			return edit.Splice{}, "it overlaps another accepted hunk"
		}
	}
	return edit.Splice{Range: r, Old: h.Find, New: h.Replace}, ""
}

// splice returns the primary hunk's splice in the current spec.
func (pr *Primary) splice(spec *mdspec.Spec) (string, edit.Splice, error) {
	name := strings.TrimPrefix(pr.Name, "$")
	as := spec.Lookup(name)
	if pr.Action == specctx.ActionAdd {
		if len(as) > 0 {
			return "", edit.Splice{}, &StaleError{Name: pr.Name, Reason: fmt.Sprintf("is already defined (%s); the proposal adds it", as[0].File)}
		}
		pl := pr.Placement
		if pl == nil {
			return "", edit.Splice{}, &StaleError{Name: pr.Name, Reason: "the proposal has no placement"}
		}
		f := spec.File(pl.File)
		if f == nil {
			return "", edit.Splice{}, &StaleError{Name: pr.Name, Reason: pl.File + " is no longer a spec file"}
		}
		sp, err := InsertSplice(f, *pl, pr.NewText)
		if err != nil {
			return "", edit.Splice{}, &StaleError{Name: pr.Name, Reason: err.Error()}
		}
		return f.Path, sp, nil
	}
	switch len(as) {
	case 0:
		return "", edit.Splice{}, &StaleError{Name: pr.Name, Reason: "is no longer defined"}
	case 1:
	default:
		return "", edit.Splice{}, &StaleError{Name: pr.Name, Reason: "is now defined more than once"}
	}
	a := as[0]
	f := spec.File(a.File)
	if cur := f.Text(a.BodyRange); cur != pr.OldText {
		return "", edit.Splice{}, &StaleError{Name: pr.Name, Reason: "changed since the proposal was made (its text is not the text the proposal replaces)"}
	}
	if pr.Action == specctx.ActionRemove {
		return f.Path, RemovalSplice(f.Source(), a.BodyRange), nil
	}
	return f.Path, edit.Splice{Range: a.BodyRange, Old: pr.OldText, New: pr.NewText}, nil
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// RemovalSplice deletes the anchor text at body together with the
// whitespace that separates it from the preceding text (or, at the start of
// the file, from the following text), so removing and re-inserting with
// PlacementOf's Sep round-trips byte for byte.
func RemovalSplice(src []byte, body pos.Range) edit.Splice {
	start := bomLen(src)
	p := wsBefore(src, body.Start)
	if p > start {
		r := pos.Range{Start: p, End: body.End}
		return edit.Splice{Range: r, Old: string(src[r.Start:r.End])}
	}
	q := body.End
	for q < len(src) && isSpace(src[q]) {
		q++
	}
	r := pos.Range{Start: body.Start, End: q}
	return edit.Splice{Range: r, Old: string(src[r.Start:r.End])}
}

// PlacementOf records where anchor a sits in f, for re-inserting its text
// later with InsertSplice: after the anchor that ends right before it, or
// after the line that precedes it, within its section.
//
// When a run of anchors separated only by whitespace precedes it, After is
// the nearest and Earlier lists the others, nearest first, and AfterLine is
// the line before the run: when its neighbor is absent too (it is added or
// removed by another proposal, or being restored together with it), the
// anchor goes after the nearest one that is present, so adjacent anchors
// round-trip in any order.
func PlacementOf(f *mdspec.File, a *mdspec.Anchor) Placement {
	src := f.Source()
	s := a.BodyRange.Start
	p := wsBefore(src, s)
	pl := Placement{File: f.Path}
	if sec := f.SectionAt(s); sec != nil {
		pl.Section = sec.Number
	}
	if p <= bomLen(src) {
		q := a.BodyRange.End
		for q < len(src) && isSpace(src[q]) {
			q++
		}
		pl.AtStart = true
		pl.Sep = string(src[a.BodyRange.End:q])
		return pl
	}
	pl.Sep = string(src[p:s])
	var run []string
	for cur, end := a, p; ; {
		var prev *mdspec.Anchor
		for _, b := range f.Anchors {
			if b != cur && b.BodyRange.End == end {
				prev = b
			}
		}
		if prev == nil || len(run) >= maxRun {
			break
		}
		run = append(run, "$"+prev.Name)
		cur, end = prev, wsBefore(src, prev.BodyRange.Start)
		p = end
		if end <= bomLen(src) {
			break
		}
	}
	if len(run) > 0 {
		pl.After, pl.Earlier = run[0], run[1:]
	}
	ls := bytes.LastIndexByte(src[:p], '\n') + 1
	pl.AfterLine = string(src[ls:p])
	return pl
}

// maxRun bounds how many preceding anchors a placement records.
const maxRun = 64

// wsBefore returns the start of the whitespace run that ends at s; a
// leading byte order mark is not part of it.
func wsBefore(src []byte, s int) int {
	start := bomLen(src)
	for s > start && isSpace(src[s-1]) {
		s--
	}
	return s
}

// bomLen is the length of src's UTF-8 byte order mark, 0 when it has none.
// The text of a file starts after it.
func bomLen(src []byte) int {
	if bytes.HasPrefix(src, []byte(utf8BOM)) {
		return len(utf8BOM)
	}
	return 0
}

// utf8BOM is the UTF-8 byte order mark, as bytes.
const utf8BOM = "\xef\xbb\xbf"

// InsertSplice returns the splice that inserts text at pl in f.
func InsertSplice(f *mdspec.File, pl Placement, text string) (edit.Splice, error) {
	sep := pl.Sep
	if sep == "" {
		sep = "\n\n"
		if bytes.Contains(f.Source(), []byte("\r\n")) {
			sep = "\r\n\r\n"
		}
	}
	if pl.AtStart {
		at := bomLen(f.Source())
		return edit.Splice{Range: pos.Range{Start: at, End: at}, New: text + sep}, nil
	}
	off, err := insertOffset(f, pl)
	if err != nil {
		return edit.Splice{}, err
	}
	return edit.Splice{Range: pos.Range{Start: off, End: off}, New: sep + text}, nil
}

func insertOffset(f *mdspec.File, pl Placement) (int, error) {
	for _, after := range append([]string{pl.After}, pl.Earlier...) {
		if after == "" {
			continue
		}
		name := strings.TrimPrefix(after, "$")
		var hits []*mdspec.Anchor
		for _, a := range f.Anchors {
			if a.Name == name {
				hits = append(hits, a)
			}
		}
		if len(hits) == 1 {
			return hits[0].BodyRange.End, nil
		}
	}
	if pl.AfterLine != "" {
		if off, ok := uniqueLine(f.Source(), pl.AfterLine); ok {
			return off, nil
		}
	}
	if pl.Section != "" {
		for _, s := range f.Sections {
			if s.Number == pl.Section {
				return s.Range.End, nil
			}
		}
	}
	where := []string{}
	if pl.After != "" {
		where = append(where, "after "+pl.After)
	}
	if pl.AfterLine != "" {
		where = append(where, fmt.Sprintf("after the line %q", pl.AfterLine))
	}
	if pl.Section != "" {
		where = append(where, "in §"+pl.Section)
	}
	return 0, fmt.Errorf("cannot find where it goes in %s (%s)", f.Path, strings.Join(where, ", or "))
}

// uniqueLine returns the end offset of line text when exactly one line of
// src starts with it and has only whitespace after it.
func uniqueLine(src []byte, text string) (int, bool) {
	found, at := 0, 0
	for off := 0; off <= len(src); {
		end := bytes.IndexByte(src[off:], '\n')
		line := src[off:]
		if end >= 0 {
			line = src[off : off+end]
		}
		if bytes.HasPrefix(line, []byte(text)) && len(bytes.TrimSpace(line[len(text):])) == 0 {
			found++
			at = off + len(text)
		}
		if end < 0 {
			break
		}
		off += end + 1
	}
	return at, found == 1
}

// KindToggle returns a's text with its label switched to kind to: "Def "
// inserted before the name, or removed.
func KindToggle(f *mdspec.File, a *mdspec.Anchor, to mdspec.Kind) (string, error) {
	text := f.Text(a.BodyRange)
	ns := a.NameRange.Start - a.BodyRange.Start
	if a.Kind == to {
		return text, nil
	}
	if to == mdspec.KindDefinition {
		return text[:ns] + "Def " + text[ns:], nil
	}
	j := ns
	for j > 0 && text[j-1] == ' ' {
		j--
	}
	if j < 3 || text[j-3:j] != "Def" {
		return "", fmt.Errorf("$%s: cannot find the Def prefix in its label", a.Name)
	}
	return text[:j-3] + text[ns:], nil
}
