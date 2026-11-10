// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/mdspec"
)

// Thresholds and limits of the model pass.
const (
	// FlagAt flags a candidate Jev scores at or above it in a batch.
	FlagAt = 0.7
	// ReaskFrom: a batch score from here up to FlagAt is asked again, on
	// its own.
	ReaskFrom = 0.3
	// ReaskFlagAt flags a candidate whose own re-ask scores at or above it.
	ReaskFlagAt = 0.5
	// BatchSize is the most candidates in one request, Jev or triage.
	BatchSize = 16
	// MaxCandidates bounds the candidates one run sends; the rest wait for
	// the next run (the cache then holds the first ones).
	MaxCandidates = 256
	// gateParallel is how many Jev requests run at once.
	gateParallel = 4
	// triageMaxTokens is the reply budget of a triage request: the
	// answer is short, but a reasoning model thinks first.
	triageMaxTokens = 4096
	// MaxDistance is the furthest a fix may be from the word.
	MaxDistance = 2
)

// Prompt is the spell check's prompt: the policy both models get, the Jev
// question (with {id} standing for the candidate's number) and the triage
// model's reply instruction.
type Prompt struct {
	Policy   string
	Question string
	Reply    string
}

// Version identifies the prompt's text, for the cache.
func (p Prompt) Version() string {
	h := sha256.Sum256([]byte(p.Policy + "\x00" + p.Question + "\x00" + p.Reply))
	return hex.EncodeToString(h[:4])
}

// Models are the model pass: the Jev gate and the triage chat model.
type Models struct {
	Gate   infer.Gate
	Triage infer.Provider
	Prompt Prompt
}

// ID names the models and the prompt version: a cached verdict is reused
// only by the same ones.
func (m *Models) ID() string {
	gi, ti := m.Gate.Info(), m.Triage.Info()
	tm := ti.TriageModel
	if tm == "" {
		tm = ti.Model
	}
	return gi.Model + "|" + tm + "|" + m.Prompt.Version()
}

// Stats describes a model pass.
type Stats struct {
	// Candidates are the distinct candidates in scope, by cache key (the
	// word, its sentence and its use-count bucket, so one word in two
	// sentences counts twice); Cached those the cache answered; Asked
	// those sent to Jev.
	Candidates int `json:"candidates"`
	Cached     int `json:"cached"`
	Asked      int `json:"asked"`
	// GateCalls and TriageCalls count requests; Escalated the candidates
	// Jev flagged.
	GateCalls   int     `json:"gate_calls"`
	TriageCalls int     `json:"triage_calls"`
	Escalated   int     `json:"escalated"`
	CostUSD     float64 `json:"cost_usd"`
	// LatencyMS is the model pass's wall time.
	LatencyMS int64 `json:"latency_ms"`
	// Skipped are candidates over MaxCandidates, left for the next run.
	Skipped int `json:"skipped"`
}

// Report is a spell check's result.
type Report struct {
	Flags []Flag `json:"flags"`
	Stats Stats  `json:"stats"`
	// Models is "" when the model pass was off (no [gate] or [triage]),
	// else the models' ID.
	Models string `json:"models"`
	// Error says why the model pass failed or was cut short: it fails
	// closed (no model flags for what it could not judge).
	Error string `json:"error,omitempty"`
	// NoDictionary is true when no system word list was found.
	NoDictionary bool `json:"no_dictionary,omitempty"`
	// Off is true when spec.toml turns the spell check off ([lint]
	// spell = "off"): nothing was checked.
	Off bool `json:"off,omitempty"`
}

// Run checks files in scope: the known misspellings, the cached verdicts,
// and, with m, the model pass over the candidates the cache does not
// answer. Without m, cached verdicts of any models are shown. A model
// failure fails closed: the report says so and holds the other flags.
// Every flag is recorded in the cache, the known misspellings too, so the
// cache exists once a run found anything: the editor and language server
// show flags (Cached) only then.
func Run(ctx context.Context, p *Project, files []*mdspec.File, scope Scope, m *Models, cache *Cache) *Report {
	rep := &Report{NoDictionary: p.Dict.Empty()}
	flags, cands := p.Check(files, scope)
	rep.Flags = flags
	now := time.Now()
	for _, f := range flags {
		cache.Put(f.key, Verdict{Word: f.Word, Typo: true, Fix: f.Fix, Stage: StageKnown, Models: StageKnown, At: now})
	}
	if m != nil {
		rep.Models = m.ID()
		start := time.Now()
		err := m.judge(ctx, p, cands, cache, &rep.Stats)
		rep.Stats.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			rep.Error = err.Error()
			lg.WithError(err).Warn("spell: the model pass failed; showing known misspellings and cached verdicts only")
		}
	}
	if err := cache.Save(); err != nil {
		lg.WithError(err).Warn("spell: cannot write the cache")
	}
	rep.Flags = append(rep.Flags, cached(files, cands, cache, rep.Models)...)
	if rep.Flags == nil {
		rep.Flags = []Flag{}
	}
	sortFlags(rep.Flags)
	return rep
}

// Cached returns the flags for files in scope without calling a model:
// every known misspelling, and the cache's typo verdicts on the
// candidates, for the editor, the language server and `spec lint --spell`.
// A candidate whose sentence changed since it was judged keeps the verdict
// on its word (Cache.verdictOf).
func Cached(p *Project, files []*mdspec.File, scope Scope, cache *Cache) []Flag {
	flags, cands := p.Check(files, scope)
	out := append(flags, cached(files, cands, cache, "")...)
	sortFlags(out)
	return out
}

// cached turns the cache's typo verdicts for cands into flags; models,
// when set, must match.
func cached(files []*mdspec.File, cands []Candidate, cache *Cache, models string) []Flag {
	if cache == nil || len(cands) == 0 {
		return nil
	}
	byPath := make(map[string]*mdspec.File, len(files))
	for _, f := range files {
		byPath[f.Path] = f
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.refresh()
	var out []Flag
	for _, c := range cands {
		v, ok := cache.verdictOf(c)
		if !ok || !v.Typo || (models != "" && v.Models != models) {
			continue
		}
		f := byPath[c.File]
		if f == nil {
			continue
		}
		out = append(out, newFlag(c.File, f.Index(), Token{Range: c.Range, Text: c.Word}, v.Fix, ByModel))
	}
	return out
}

// sortFlags orders flags by file and offset.
func sortFlags(fs []Flag) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		return fs[i].Range.Start < fs[j].Range.Start
	})
}

// judge runs the model pass over the candidates the cache does not answer
// for these models, and caches what it decides.
func (m *Models) judge(ctx context.Context, p *Project, cands []Candidate, cache *Cache, st *Stats) error {
	id := m.ID()
	seen := map[string]bool{}
	var ask []Candidate
	for _, c := range cands {
		if seen[c.Key] {
			continue
		}
		seen[c.Key] = true
		st.Candidates++
		if v, ok := cache.Lookup(c.Key); ok && v.Models == id {
			st.Cached++
			continue
		}
		ask = append(ask, c)
	}
	if len(ask) > MaxCandidates {
		st.Skipped = len(ask) - MaxCandidates
		ask = ask[:MaxCandidates]
	}
	st.Asked = len(ask)
	if len(ask) == 0 {
		return nil
	}
	infos := make([]string, len(ask))
	for i, c := range ask {
		infos[i] = p.Info(c)
	}
	scores, gateErr := m.score(ctx, ask, infos, st)
	var flagged []int
	now := time.Now()
	for i, c := range ask {
		s, ok := scores[i]
		if !ok {
			continue
		}
		if s.flag {
			flagged = append(flagged, i)
			continue
		}
		cache.Put(c.Key, Verdict{Word: c.Word, Stage: StageGate, Score: s.score, Models: id, At: now})
	}
	st.Escalated = len(flagged)
	triageErr := m.fix(ctx, p, ask, infos, flagged, cache, st)
	return errors.Join(gateErr, triageErr)
}

// score is Jev's verdict on one candidate.
type score struct {
	score float64
	flag  bool
}

// score asks Jev about every candidate, in batches, and once more on its
// own about each that lands between ReaskFrom and FlagAt. A failed batch
// leaves its candidates out of the result.
func (m *Models) score(ctx context.Context, ask []Candidate, infos []string, st *Stats) (map[int]score, error) {
	out := map[int]score{}
	var mu sync.Mutex
	var errs []error
	var reask []int
	run := func(idx []int, single bool) {
		d, err := m.Gate.Decide(ctx, m.gateRequest(ask, infos, idx))
		mu.Lock()
		defer mu.Unlock()
		st.GateCalls++
		if err != nil {
			errs = append(errs, fmt.Errorf("spell gate: %w", err))
			return
		}
		st.CostUSD += d.CostUSD
		for k, i := range idx {
			s := d.Scores["typo_"+strconv.Itoa(k+1)]
			switch {
			case single:
				out[i] = score{score: s, flag: s >= ReaskFlagAt}
			case s >= FlagAt:
				out[i] = score{score: s, flag: true}
			case s >= ReaskFrom:
				reask = append(reask, i)
			default:
				out[i] = score{score: s}
			}
		}
	}
	var batches [][]int
	for lo := 0; lo < len(ask); lo += BatchSize {
		var idx []int
		for i := lo; i < min(lo+BatchSize, len(ask)); i++ {
			idx = append(idx, i)
		}
		batches = append(batches, idx)
	}
	parallel(batches, func(idx []int) { run(idx, false) })
	sort.Ints(reask)
	var singles [][]int
	for _, i := range reask {
		singles = append(singles, []int{i})
	}
	parallel(singles, func(idx []int) { run(idx, true) })
	return out, errors.Join(errs...)
}

// parallel runs f over every item, gateParallel at a time.
func parallel(items [][]int, f func([]int)) {
	sem := make(chan struct{}, gateParallel)
	var wg sync.WaitGroup
	for _, it := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(it []int) {
			defer wg.Done()
			defer func() { <-sem }()
			f(it)
		}(it)
	}
	wg.Wait()
}

// situation renders candidates idx, numbered from 1, with their sentence
// (the word marked [[ ]]) and their Info line.
func situation(ask []Candidate, infos []string, idx []int) string {
	var b strings.Builder
	for k, i := range idx {
		c := ask[i]
		marked := c.Sentence[:c.Mark] + "[[" + c.Word + "]]" + c.Sentence[c.Mark+len(c.Word):]
		fmt.Fprintf(&b, "Candidate %d: %s\n  Info: %s\n", k+1, marked, infos[i])
	}
	return b.String()
}

// gateRequest is Jev's request about candidates idx.
func (m *Models) gateRequest(ask []Candidate, infos []string, idx []int) infer.GateRequest {
	req := infer.GateRequest{Purpose: infer.PurposeSpellGate, Policy: m.Prompt.Policy, Situation: situation(ask, infos, idx)}
	for k := range idx {
		n := strconv.Itoa(k + 1)
		req.Questions = append(req.Questions, infer.Question{ID: "typo_" + n, Instructions: strings.ReplaceAll(m.Prompt.Question, "{id}", n)})
	}
	return req
}

// fix asks the triage model for the typos among the flagged candidates and
// their fixes, in batches, and caches what passes the fix guard.
func (m *Models) fix(ctx context.Context, p *Project, ask []Candidate, infos []string, flagged []int, cache *Cache, st *Stats) error {
	id := m.ID()
	var errs []error
	for lo := 0; lo < len(flagged); lo += BatchSize {
		idx := flagged[lo:min(lo+BatchSize, len(flagged))]
		req := infer.Request{
			Messages: []infer.Message{
				infer.System(m.Prompt.Policy + "\n\n" + m.Prompt.Reply),
				infer.User(situation(ask, infos, idx)),
			},
			MaxTokens: triageMaxTokens, Triage: true, Purpose: infer.PurposeSpell,
		}
		resp, err := m.Triage.Chat(ctx, req)
		st.TriageCalls++
		if err != nil {
			errs = append(errs, fmt.Errorf("spell triage: %w", err))
			continue
		}
		st.CostUSD += resp.CostUSD
		if strings.TrimSpace(resp.Content) == "" && resp.FinishReason == "length" {
			errs = append(errs, errors.New("spell triage: the reply ran out of tokens before answering"))
			continue
		}
		fixes, err := ParseFixes(resp.Content, len(idx))
		if err != nil {
			errs = append(errs, fmt.Errorf("spell triage: %w", err))
			continue
		}
		now := time.Now()
		for k, i := range idx {
			c := ask[i]
			v := Verdict{Word: c.Word, Stage: StageTriage, Models: id, At: now}
			if raw, ok := fixes[k+1]; ok {
				if full, ok := p.Guard(c, raw); ok {
					v.Typo, v.Fix = true, full
				} else {
					v.Stage, v.Fix = StageGuard, raw
					lg.WithField("word", c.Word).WithField("fix", raw).Debug("spell: the fix guard rejected a fix")
				}
			}
			cache.Put(c.Key, v)
		}
	}
	return errors.Join(errs...)
}

// Guard accepts a model's fix for candidate c and returns it as it
// replaces the word: within MaxDistance of the word (a fix of only an
// identifier's unknown part is put back into the identifier first), not
// the word itself in another case, and made of words the project or the
// dictionary vouches for.
func (p *Project) Guard(c Candidate, fix string) (string, bool) {
	fix = strings.TrimSpace(fix)
	if fix == "" || strings.ContainsAny(fix, " \t\n") {
		return "", false
	}
	if c.Part != "" && !strings.EqualFold(fix, c.Word) && Distance(strings.ToLower(c.Word), strings.ToLower(fix)) > MaxDistance &&
		Distance(strings.ToLower(c.Part), strings.ToLower(fix)) <= MaxDistance {
		fix = c.Word[:c.PartOff] + MatchCase(c.Part, fix) + c.Word[c.PartOff+len(c.Part):]
	}
	if strings.EqualFold(fix, c.Word) {
		return "", false
	}
	if Distance(strings.ToLower(c.Word), strings.ToLower(fix)) > MaxDistance {
		return "", false
	}
	if p.Valid(fix) {
		return MatchCase(c.Word, fix), true
	}
	parts := Split(fix)
	if len(parts) == 0 {
		return "", false
	}
	for _, part := range parts {
		if skipPart(part.Text) {
			continue
		}
		if !p.Valid(part.Text) {
			return "", false
		}
	}
	return MatchCase(c.Word, fix), true
}
