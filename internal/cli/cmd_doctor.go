// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/baseline"
	"github.com/jwriteclub/spec/internal/config"
	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/infer"
	"github.com/jwriteclub/spec/internal/project"
	"github.com/jwriteclub/spec/internal/spell"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

type doctorData struct {
	Checks []doctorCheck `json:"checks"`
	// Inference is the live check's measurements; null when it did not
	// run or failed.
	Inference *doctorInference `json:"inference"`
}

// doctorInference is what the live check measured.
type doctorInference struct {
	Provider string `json:"provider"`
	// Kind is the provider's kind: openai, claude-cli or codex-cli.
	Kind  string `json:"kind"`
	Model string `json:"model"`
	// Effort is the effort level spec set ("" when none).
	Effort           string `json:"effort"`
	EchoedModel      string `json:"echoed_model"`
	LatencyMS        int64  `json:"latency_ms"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	FinishReason     string `json:"finish_reason"`
	Reply            string `json:"reply"`
	// Streamed is true when the reply came back as an event stream (the
	// path every check takes unless the provider has stream = false).
	Streamed bool `json:"streamed"`
	// CostUSD is the cost the provider reported (claude-cli; 0 otherwise).
	CostUSD float64 `json:"cost_usd"`
}

func (s *session) doctorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "check the project, git, user configuration and inference provider, and say how to fix what is wrong",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errTooMany("doctor")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s.result = s.runDoctor()
			return nil
		},
	}
	s.addProviderFlags(cmd)
	return cmd
}

const (
	stOK      = "ok"
	stWarn    = "warn"
	stFail    = "fail"
	stSkipped = "skipped"
)

func (s *session) runDoctor() *outcome {
	var checks []doctorCheck
	add := func(name, status, detail, fix string) {
		checks = append(checks, doctorCheck{Name: name, Status: status, Detail: detail, Fix: fix})
	}

	var v *view
	var proj *project.Project

	// project
	root, o := s.resolveRoot()
	if o == nil {
		proj, o = s.openRoot(root)
	}
	if o != nil {
		add("project", stFail, o.message, "run `spec init` in the project directory, or pass --project DIR")
		add("files", stSkipped, "no project", "")
		add("lint", stSkipped, "no project", "")
	} else {
		rel, err := filepath.Rel(s.env.Cwd, root)
		if err != nil || rel == "" {
			rel = root
		}
		var cfgErrs []string
		for _, d := range proj.Diagnostics {
			if d.Code == diag.CodeConfigInvalid && d.Severity == diag.Error {
				cfgErrs = append(cfgErrs, d.Message)
			}
		}
		if len(cfgErrs) > 0 {
			add("project", stFail, "spec.toml at "+rel+" is invalid: "+cfgErrs[0], "edit spec.toml (`spec explain config` lists the keys)")
		} else {
			add("project", stOK, "spec.toml at "+rel, "")
		}

		var loadErr *outcome
		v, loadErr = s.loadViewOf(proj)
		if loadErr != nil {
			add("files", stFail, loadErr.message, "report this as a bug")
			add("lint", stSkipped, "files did not load", "")
		} else {
			var problems []string
			for _, d := range v.diags {
				switch d.Code {
				case diag.CodeFileNotUTF8, diag.CodeProjectNoFiles, diag.CodeProjectFileMissing, diag.CodeFileSymlink:
					if d.Severity == diag.Error {
						problems = append(problems, d.Message)
					}
				}
			}
			if len(problems) > 0 {
				add("files", stFail, problems[0], "fix the file set in spec.toml, or the file itself (`spec lint` lists every problem)")
			} else {
				add("files", stOK, plural(len(v.spec.Files), "spec file")+" parsed", "")
			}
			c := v.counts()
			detail := fmt.Sprintf("%s, %s, %s", plural(c.Error, "error"), plural(c.Warning, "warning"), plural(c.Hint, "hint"))
			if c.Error > 0 {
				add("lint", stFail, detail, "run `spec lint` and fix the errors")
			} else {
				add("lint", stOK, detail, "")
			}
		}
	}

	// git
	gitBin := s.env.Git.Git
	if gitBin == "" {
		gitBin = "git"
	}
	ver, err := exec.Command(gitBin, "--version").Output()
	switch {
	case err != nil:
		add("git", stWarn, "git was not found on PATH", "install git: diff, status baselines and check need it; lint works without")
	case proj == nil:
		add("git", stOK, strings.TrimSpace(string(ver)), "")
	default:
		b, err := s.env.Git.Load(context.Background(), proj, baseline.Source{})
		gv := strings.TrimSpace(string(ver))
		switch {
		case err != nil:
			add("git", stWarn, gv+"; reading HEAD failed: "+err.Error(), "check the repository with `git status`")
		case b.State == baseline.StateUnavailable:
			add("git", stWarn, gv+"; "+b.Reason, "run `git init` in the project (diff, status baselines and check need a repository)")
		case b.State == baseline.StateUnborn:
			add("git", stOK, gv+"; "+b.Reason, "")
		default:
			add("git", stOK, gv+"; baseline HEAD readable", "")
		}
	}

	// user config, provider and the live check
	inf := s.doctorProvider(add)
	s.doctorSpell(proj, add)

	data := doctorData{Checks: checks, Inference: inf}
	failed := 0
	var b strings.Builder
	for _, c := range checks {
		if c.Status == stFail {
			failed++
		}
		fmt.Fprintf(&b, "%-7s %-13s %s\n", c.Status, c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(&b, "        fix: %s\n", c.Fix)
		}
	}
	out := &outcome{data: data, human: b.String()}
	if v != nil {
		out.diags = v.diagsJSON()
	}
	if failed > 0 {
		out.exit = exitFail
		out.code = "doctor_failed"
		out.message = fmt.Sprintf("%s failed", plural(failed, "doctor check"))
		out.stderr = "spec: " + out.message + "\n"
	}
	return out
}

// doctorPrompt is the live check's request.
const doctorPrompt = "Reply with the single word OK"

// doctorMaxTokens caps the live check's reply.
const doctorMaxTokens = 8

// doctorProvider adds the user_config, provider_key and inference checks
// and returns the live check's measurements (nil when it did not succeed).
func (s *session) doctorProvider(add func(name, status, detail, fix string)) *doctorInference {
	cfgPath := s.userConfigPath()
	if cfgPath == "" {
		add("user_config", stWarn, "cannot tell where the user config lives (no HOME or XDG_CONFIG_HOME)", "set HOME or XDG_CONFIG_HOME")
		add("provider_key", stSkipped, "no user config", "")
		add("inference", stSkipped, "no provider configured", "")
		return nil
	}
	loaded, err := config.Load(cfgPath)
	switch {
	case err != nil:
		add("user_config", stFail, fmt.Sprintf("cannot read %s: %v", cfgPath, err), "fix the file's permissions")
		add("provider_key", stSkipped, "user config unreadable", "")
		add("inference", stSkipped, "no provider configured", "")
		return nil
	case !loaded.Exists:
		add("user_config", stWarn, "no user config at "+cfgPath, "needed for inference; `spec explain config` shows the format")
		add("provider_key", stSkipped, "no user config", "")
		add("inference", stSkipped, "no provider configured", "")
		return nil
	case len(loaded.Diagnostics) > 0:
		add("user_config", stFail, cfgPath+": "+loaded.Diagnostics[0].Message, "edit "+cfgPath+" (`spec explain config` shows the format)")
		add("provider_key", stSkipped, "user config invalid", "")
		add("inference", stSkipped, "no provider configured", "")
		return nil
	}
	add("user_config", stOK, cfgPath, "")
	s.doctorEnvFile(loaded, add)

	defer s.doctorCascade(loaded, add)
	c, prob := s.selectProvider(loaded)
	if prob != nil {
		st := stFail
		if prob.Warn {
			st = stWarn
		}
		add("provider_key", st, prob.Msg, prob.Fix)
		add("inference", stSkipped, "provider not ready (see provider_key)", "")
		return nil
	}
	name, p, _ := loaded.Config.Provider(s.provider.name)
	info := c.Info()
	switch {
	case p.IsCLI():
		add("provider_key", stOK, "provider "+name+" runs "+info.Transport+" on its own login (no key needed)", "")
	case p.APIKeyEnv == "":
		add("provider_key", stOK, "provider "+name+" names no api_key_env (no key needed)", "")
	default:
		add("provider_key", stOK, "provider "+name+": "+s.doctorVars(loaded, p), "")
	}

	// One tiny request: a few dozen tokens, sent the way every check is
	// (streamed unless the provider says stream = false). Keep a dead
	// provider from holding doctor for minutes.
	c.SetLimits(30*time.Second, 2)
	resp, err := c.Chat(context.Background(), infer.Request{
		Messages:  []infer.Message{infer.User(doctorPrompt)},
		MaxTokens: doctorMaxTokens,
	})
	if err != nil {
		fix := "check base_url, the model id and the network; `spec doctor --provider NAME` tries another provider"
		var ie *infer.Error
		if errors.As(err, &ie) {
			fix = ie.Fix()
		}
		add("inference", stFail, err.Error(), fix)
		return nil
	}
	echoed := resp.Model
	if echoed == "" {
		echoed = "none"
	}
	ms := resp.Latency.Milliseconds()
	via := "streamed"
	switch {
	case info.Kind == config.KindClaudeCLI:
		via = "via claude -p"
	case info.Kind == config.KindCodexCLI:
		via = "via codex exec"
	case !resp.Streamed:
		via = "not streamed"
	}
	if info.Effort != "" {
		via += ", effort " + info.Effort
	}
	cost := ""
	if resp.CostUSD > 0 {
		cost = fmt.Sprintf("; $%.6f", resp.CostUSD)
	}
	detail := fmt.Sprintf("provider %s, model %s: answered in %d ms (%s; model id echoed: %s; %d prompt + %d completion tokens%s)",
		name, resp.RequestedModel, ms, via, echoed, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, cost)
	reply := strings.TrimSpace(resp.Content)
	switch {
	case reply == "" && resp.Reasoning != "":
		detail += fmt.Sprintf("; the model was still reasoning when the %d-token cap cut it off, which is fine for this check", doctorMaxTokens)
	case reply == "":
		detail += "; the reply was empty"
	default:
		r := []rune(reply)
		if len(r) > 40 {
			reply = string(r[:40]) + "..."
		}
		detail += fmt.Sprintf("; reply %q", reply)
	}
	add("inference", stOK, detail, "")
	return &doctorInference{
		Provider:         name,
		Kind:             info.Kind,
		Model:            resp.RequestedModel,
		Effort:           info.Effort,
		EchoedModel:      resp.Model,
		LatencyMS:        ms,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		FinishReason:     resp.FinishReason,
		Reply:            resp.Content,
		Streamed:         resp.Streamed,
		CostUSD:          resp.CostUSD,
	}
}

// doctorEnvFile adds the env_file check when the user config sets one: the
// file's path, its mode and the names it defines. Values are never shown.
func (s *session) doctorEnvFile(loaded *config.Loaded, add func(name, status, detail, fix string)) {
	if loaded.Config.EnvFile == "" {
		return
	}
	d, err := s.workflow().EnvFile(loaded.Config, loaded.Path)
	if err != nil {
		fix := "fix env_file in " + loaded.Path
		var fe *config.EnvFileError
		if errors.As(err, &fe) {
			fix = fe.Fix()
		}
		add("env_file", stFail, err.Error(), fix)
		return
	}
	names := d.Names()
	detail := fmt.Sprintf("%s (mode %04o, private): defines %s", d.Path, d.Mode, plural(len(names), "name"))
	if len(names) > 0 {
		detail += ": " + strings.Join(names, ", ")
	}
	add("env_file", stOK, detail, "")
}

// doctorVars says where each variable of p's settings was found: "KEY is
// set (environment)" or "(env_file)", then base_url's variables.
func (s *session) doctorVars(loaded *config.Loaded, p config.Provider) string {
	env, err := s.workflow().ProviderEnv(loaded.Config, loaded.Path, p)
	if err != nil {
		// The provider opened, so every variable it needs is in the
		// environment.
		env = config.EnvLookup{Getenv: s.env.Getenv}
	}
	var parts []string
	for _, n := range p.EnvNames() {
		src := env.Source(n)
		if src == "" {
			src = "unset"
		}
		parts = append(parts, n+" is set ("+src+")")
	}
	return strings.Join(parts, ", ")
}

// doctorCascade adds the gate and triage checks, each only when the user
// config has its table: the provider opens (its variables are set) and
// answers one tiny request.
func (s *session) doctorCascade(loaded *config.Loaded, add func(name, status, detail, fix string)) {
	cfg := loaded.Config
	wf := s.workflow()
	getenv := func(p config.Provider) (func(string) string, error) {
		env, err := wf.ProviderEnv(cfg, loaded.Path, p)
		if err != nil {
			return nil, err
		}
		return env.Get, nil
	}
	if g := cfg.Gate; g != nil {
		var gate infer.Gate
		get, err := getenv(cfg.Providers[g.Provider])
		if err == nil {
			gate, err = infer.OpenGate(g.Provider, cfg.Providers[g.Provider], get)
		}
		switch {
		case err != nil:
			fix := "fix [providers." + g.Provider + "] in the user config"
			var ee *config.EnvError
			var fe *config.EnvFileError
			switch {
			case errors.As(err, &ee):
				fix = ee.Fix()
			case errors.As(err, &fe):
				fix = fe.Fix()
			}
			add("gate", stWarn, err.Error()+"; the gate stage fails open (the edit goes on to triage or the check)", fix)
		default:
			gate.SetLimits(15*time.Second, 1)
			d, err := gate.Decide(context.Background(), infer.GateRequest{
				Policy:    "A spec states requirements.",
				Situation: "The edit changes nothing.",
				Questions: []infer.Question{{ID: infer.QuestionMaterialChange, Instructions: "Does the edit change a requirement?"}},
			})
			if err != nil {
				fix := "check the gate provider's model and key; the gate stage fails open meanwhile"
				var ie *infer.Error
				if errors.As(err, &ie) {
					fix = ie.Fix()
				}
				add("gate", stFail, err.Error(), fix)
				break
			}
			add("gate", stOK, fmt.Sprintf("provider %s, model %s: answered in %d ms (escalate_class_at %.2f, skip_material_below %.2f; $%.6f)",
				g.Provider, d.RequestedModel, d.Latency.Milliseconds(), g.EscalateAt(), g.SkipMaterialBelow, d.CostUSD), "")
		}
	}
	if t := cfg.Triage; t != nil {
		p := cfg.Providers[t.Provider]
		var c infer.Provider
		get, err := getenv(p)
		if err == nil {
			c, err = infer.Open(t.Provider, p, get)
		}
		if err != nil {
			fix := "fix [providers." + t.Provider + "] in the user config"
			var ee *config.EnvError
			var se *infer.SetupError
			var fe *config.EnvFileError
			switch {
			case errors.As(err, &ee):
				fix = ee.Fix()
			case errors.As(err, &fe):
				fix = fe.Fix()
			case errors.As(err, &se):
				fix = se.Fix()
			}
			add("triage", stWarn, err.Error()+"; triage fails open and the fast requests go to the check's provider", fix)
			return
		}
		c.SetLimits(30*time.Second, 2)
		resp, err := c.Chat(context.Background(), infer.Request{
			Messages: []infer.Message{infer.User(doctorPrompt)}, MaxTokens: doctorMaxTokens, Triage: true,
		})
		if err != nil {
			fix := "check the [triage] provider's base_url, model and key"
			var ie *infer.Error
			if errors.As(err, &ie) {
				fix = ie.Fix()
			}
			add("triage", stFail, err.Error(), fix)
			return
		}
		stage := "triage stage on"
		if !p.TriageOn() {
			stage = "triage stage off (triage = false); fast requests only"
		}
		add("triage", stOK, fmt.Sprintf("provider %s, model %s: answered in %d ms (%s)", t.Provider, resp.RequestedModel, resp.Latency.Milliseconds(), stage), "")
	}
}

// doctorSpell adds the spell check's line: the word list it reads, whether
// its model pass is on, and the cached verdicts. It is never a failure.
func (s *session) doctorSpell(proj *project.Project, add func(name, status, detail, fix string)) {
	var parts []string
	if d := spell.System(); d.Empty() {
		parts = append(parts, "no system word list (looked for "+strings.Join(spell.SystemWordLists, ", ")+"): known misspellings, project words and the models only")
	} else {
		parts = append(parts, fmt.Sprintf("word list %s (%d words)", d.Path, d.Len()))
	}
	models := "model pass off (needs [gate] and [triage])"
	if path := s.userConfigPath(); path != "" {
		if loaded, err := config.Load(path); err == nil && loaded.Config.Gate != nil && loaded.Config.Triage != nil {
			models = fmt.Sprintf("model pass on (%s, then %s)", loaded.Config.Gate.Provider, loaded.Config.Triage.Provider)
		}
	}
	parts = append(parts, models)
	if proj != nil {
		parts = append(parts, plural(spell.OpenCache(proj.Root).Len(), "cached verdict"))
	}
	add("spell", stOK, strings.Join(parts, "; "), "")
}
