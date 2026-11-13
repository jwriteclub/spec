// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package config

// OpenRouter setup: the models `spec ui`'s Settings → Add connection →
// OpenRouter API offers for each role on one OpenRouter key. Measured
// choices and their request settings are documented in notes/evals and
// notes/providers.md; the setup is recorded in notes/decisions.md
// ("OpenRouter setup sets up all three roles").

// OpenRouterBaseURL is OpenRouter's OpenAI-compatible chat API root.
const OpenRouterBaseURL = "https://openrouter.ai/api/v1"

// OpenRouterKeyEnv is the environment variable the OpenRouter connections
// name for their key.
const OpenRouterKeyEnv = "OPENROUTER_API_KEY"

// Role is a stage of the per-edit cascade a connection serves.
type Role string

// Roles, in cascade order.
const (
	// RoleGate is the decisions model behind [gate] provider.
	RoleGate Role = "gate"
	// RoleTriage is the chat model behind [triage] provider.
	RoleTriage Role = "triage"
	// RoleCheck is the chat model behind default: the contradiction check.
	RoleCheck Role = "check"
)

// OpenRouterModel is one available model for a role and the request
// settings it needs.
type OpenRouterModel struct {
	Role Role
	// Label names the model in the picker.
	Label string
	// Model is the OpenRouter model id.
	Model string
	// Note describes the model in the picker.
	Note string
	// JSONMode, Extra, Timeout and IdleTimeout are the connection's keys
	// of the same names (chat roles only).
	JSONMode    bool
	Extra       map[string]any
	Timeout     int
	IdleTimeout int
}

// lowEffort is OpenRouter's unified reasoning object at effort low, the
// spelling measured on OpenRouter (notes/providers.md).
func lowEffort() map[string]any {
	return map[string]any{"effort": "low"}
}

// pinned routes a request to one OpenRouter endpoint with no fallback.
func pinned(endpoint string) map[string]any {
	return map[string]any{"order": []any{endpoint}, "allow_fallbacks": false}
}

// OpenRouterModels lists each role's available models. The first entry of
// a role is its default.
var OpenRouterModels = []OpenRouterModel{
	{Role: RoleGate, Label: "Jev 1.14", Model: "typesafe/jev-1.14",
		Note: "The product gate: every problem escalated at 0.45, about 0.25 s a call (evals 2026-10-30, 2026-11-05)."},
	{Role: RoleGate, Label: "Gemini Decision 1", Model: "google/gemini-decision-1",
		Note: "Available through the OpenRouter Decisions API."},
	{Role: RoleGate, Label: "Morph Reflex", Model: "morph/reflex",
		Note: "In the gate baseline beside Jev: every problem escalated at 0.45 with fewer false escalations, about 0.7 s (eval 2026-11-05)."},

	{Role: RoleTriage, Label: "GLM 5.3 Flash, low effort", Model: "z-ai/glm-5.3-flash", JSONMode: true,
		Extra: map[string]any{"reasoning": lowEffort()},
		Note:  "The triage baseline: 11/11 problems, 0/30 false alarms, 1.8 s p50. Needs effort low (eval 2026-10-30)."},
	{Role: RoleTriage, Label: "DeepSeek V4.1 Flash, low effort", Model: "deepseek/deepseek-v4.1-flash-1017", JSONMode: true,
		Extra: map[string]any{"reasoning": lowEffort()},
		Note:  "Tied GLM on recall and cost, slower tail (p95 7 s); the fallback if GLM's host is flaky (eval 2026-10-30)."},
	{Role: RoleTriage, Label: "Qwen3.8 27B on Cerebras, low effort", Model: "qwen/qwen3.8-27b",
		Extra: map[string]any{"reasoning": lowEffort(), "provider": pinned("cerebras")},
		Note:  "Tied GLM with the fastest tail (p95 2.8 s) at about 19x the cost; Cerebras takes no JSON mode (eval 2026-10-30)."},

	{Role: RoleCheck, Label: "DeepSeek V4.1 Flash, low effort", Model: "deepseek/deepseek-v4.1-flash-1017", JSONMode: true,
		Extra: map[string]any{"reasoning": lowEffort()}, Timeout: 1800, IdleTimeout: 120,
		Note: "The best check model: 98-100% holdout recall; about 40-65 s median (eval 2026-11-05). Thinking stays on with JSON mode."},
	{Role: RoleCheck, Label: "DeepSeek V4.1 Flash on Fireworks ultrafast", Model: "deepseek/deepseek-v4.1-flash-1017", JSONMode: true,
		Extra: map[string]any{"reasoning": lowEffort(), "provider": pinned("fireworks/ultrafast")}, Timeout: 1800, IdleTimeout: 120,
		Note: "Same model pinned to one endpoint: about 4-5x faster on a demo check, priciest endpoint, no fallback; only 2 runs (2026-11-08)."},
	{Role: RoleCheck, Label: "GLM 5.3, low effort", Model: "z-ai/glm-5.3", JSONMode: true,
		Extra: map[string]any{"reasoning": lowEffort()},
		Note:  "Fast (11 s median) with few false alarms, but 81-85% holdout recall (eval 2026-11-05, on Cloudflare)."},
	{Role: RoleCheck, Label: "Qwen3.8 27B on Cerebras, medium effort", Model: "qwen/qwen3.8-27b",
		Extra: map[string]any{"reasoning": map[string]any{"effort": "medium"}, "provider": pinned("cerebras")},
		Note:  "About 7 s per whole-spec analyze, 0 false positives in 3 runs, but misses the hardest plant (eval 2026-10-30)."},
}

// OpenRouterChoices returns role's models, its default first.
func OpenRouterChoices(role Role) []OpenRouterModel {
	var ms []OpenRouterModel
	for _, m := range OpenRouterModels {
		if m.Role == role {
			ms = append(ms, m)
		}
	}
	return ms
}

// Provider returns m's [providers.<name>] table on OpenRouter: a decisions
// connection for the gate (its base_url is the default), an openai one
// otherwise. A model id pasted by the user is an OpenRouterModel with only
// Role and Model set, and gets no tuned settings.
func (m OpenRouterModel) Provider() map[string]any {
	if m.Role == RoleGate {
		return map[string]any{"kind": KindDecisions, "model": m.Model, "api_key_env": OpenRouterKeyEnv}
	}
	p := map[string]any{"kind": KindOpenAI, "base_url": OpenRouterBaseURL, "model": m.Model, "api_key_env": OpenRouterKeyEnv}
	if m.JSONMode {
		p["json_mode"] = true
	}
	if len(m.Extra) > 0 {
		p["extra"] = cloneTable(m.Extra)
	}
	if m.Timeout > 0 {
		p["timeout"] = int64(m.Timeout)
	}
	if m.IdleTimeout > 0 {
		p["idle_timeout"] = int64(m.IdleTimeout)
	}
	return p
}
