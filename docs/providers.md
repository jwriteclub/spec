<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Providers

How to connect `spec` to a model for contradiction checks and `spec analyze`.

## Adding a connection

Videos: [setting up spec with OpenRouter](https://youtu.be/tYKEDGLEhCs) and
[with DeepSeek](https://youtu.be/Jcp0z4ZBU-0).

In `spec ui`, press ^G, then ^T for Settings, and choose Add connection:

- **OpenRouter API:** one key sets up three models: a decision gate (Jev by default), a fast triage
  model and the check model, each from a short list or any model id. The gate list includes
  Gemini Decision 1 (`google/gemini-decision-1`) alongside Jev.
- **Google Gemini Decisions** sets up Gemini Decision 1 as the gate. Supply a Google API key
  through `GEMINI_API_KEY`, your `env_file`, or the masked session key field.
- **Claude account:** uses the Claude Code CLI on your own login (`claude auth login`), so no API
  key is needed.
- **ChatGPT / Codex:** uses the Codex CLI on your own login (`codex login`).
- **Custom API endpoint:** any OpenAI-compatible chat-completions API, such as DeepSeek, OpenAI,
  Cloudflare Workers AI or a local server.

OpenRouter needs an API key with credit. For a Claude or ChatGPT connection, install the
[Claude Code CLI](https://code.claude.com/docs/en/quickstart) or the
[Codex CLI](https://developers.openai.com/codex/cli/) first and log in with it. A chat account alone
does not install the CLI. After you save the settings, run `spec doctor` in the project. It sends one
small test request to the configured provider and reports the result.

## The gate

The gate is what makes per-edit checks cheap. Before the full check runs, Jev, a decision model on
OpenRouter, answers yes/no questions about the edit in about 0.3 s; a triage model follows, and the
full check runs only when neither clears the edit. Settings are saved to
`~/.config/spec/config.toml` (or under `$XDG_CONFIG_HOME`), and keys can live in a private `env_file`. `spec explain config` has
every option.

## Using Jev as a gate with Claude Code or Codex

The gate is configured separately from the check model, so Jev on OpenRouter can screen edits while
Claude Code or Codex, on your own login, does the triage and the full check. You need an OpenRouter
API key with a little credit for Jev; the checks themselves run on your Claude or ChatGPT plan.

In `spec ui` (^G, then ^T for Settings):

1. Add connection, then **Claude account** (or **ChatGPT / Codex**). Set its check model and its
   triage model (for example `sonnet` and `haiku`). The first chat connection you add becomes the default
   check provider.
2. Add connection again, then **OpenRouter API**. In the Decision (gate) model row choose
   `typesafe/jev-1.14`. In the Triage model and Inference (check) model rows choose
   **Keep existing**, so they stay on your Claude or Codex connection. Keep existing is offered only
   for a role that is already set, so give the Claude or Codex connection its triage model first;
   otherwise the triage row has only OpenRouter models to choose from.
3. Give the key as `OPENROUTER_API_KEY` in your environment or your `env_file`, or type it for the
   session. Review and save, then press ^T in Model routing to test each role, or run `spec doctor`.

The same thing by hand in `~/.config/spec/config.toml`:

    default = "claude"
    env_file = "~/.config/spec/keys.env"         # optional: holds OPENROUTER_API_KEY=...

    [providers.claude]                           # or kind = "codex-cli"
    kind = "claude-cli"
    model = "sonnet"
    triage_model = "haiku"

    [providers.jev]                              # a gate, not a chat provider
    kind = "decisions"
    api_key_env = "OPENROUTER_API_KEY"
    model = "typesafe/jev-1.14"

    [gate]
    provider = "jev"
    escalate_class_at = 0.45
    skip_material_below = 0

With this, Jev screens each changed anchor first, the Claude or Codex `triage_model` screens what
Jev passes on, and the full check runs on `model` only when neither clears the edit. A gate that
fails passes the edit on to the next stage. The spell check's model pass runs only when both
`[gate]` and a `[triage]` provider are set; add `[triage]` with `provider = "claude"` if you want
it. `spec explain config` describes every key.

## Using Gemini Decision 1 through Google

Gemini Decision 1 is a decision model, like Jev: a gate option that clears logically insignificant
edits before they reach the check. It runs on Google's own API, or through OpenRouter (pick it in the
OpenRouter connection's gate list). For the Google route, create an API key in
[Google AI Studio](https://aistudio.google.com/apikey) and export it as `GEMINI_API_KEY`, or put it
in your `env_file`.

Choose **Google Gemini Decisions** in Add connection, then review and save. This sets the gate
provider. You can add a chat connection for checks before or after setting up the gate.
The equivalent TOML sets the provider in the `[gate]` table.

    [providers.gemini]
    kind = "decisions"
    api = "google"
    model = "gemini-decision-1"
    api_key_env = "GEMINI_API_KEY"

    [gate]
    provider = "gemini"

The native API sends the gate policy, the edit and its surrounding context to
`https://generativelanguage.googleapis.com/v1beta/decisions`. The response includes token usage.
The response has no per-request price. [Google Decisions API reference](https://aistudio.google.com/docs/decisions-api)

Omitting `api` or setting it to `"openrouter"` uses the OpenRouter Decisions API. Either API
supports a `base_url` override.
