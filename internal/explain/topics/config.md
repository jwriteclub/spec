<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Configuration

Two files: `spec.toml` in the project (committed), and a user config in your home directory (never
committed). Project settings never hold provider settings, and the user config never holds project
settings.

## spec.toml

    name = "lensd"                               # optional; default: the directory name
    files = ["spec/*.spec.md", "docs/design.md"] # optional; default: ["**/*.spec.md"]
    formal = ["tla/*.tla"]                       # optional; default: none

    [lint]
    checked-by-missing = "warning"               # opt-in rule, off by default
    anchor-long = "hint"
    ref-in-title = "off"
    spell = "off"                                # no spelling flags (spec explain spell)

    [format]
    width = 100                                  # spec fmt and ^F wrap paragraphs here

    [tla]
    max_acquisition_visits = 2100000             # optional; default: prudence's, 1000000

- `name`: the project name.
- `files`: doublestar globs (`**` crosses directories) relative to the directory holding
  `spec.toml`. They replace the default rather than adding to it. Each entry must match at least one
  file, or error `project-file-missing`. List a spec with another name here (`SPEC.md`): its stem for
  `§` qualifiers is the file name minus its extension (`§SPEC:1.2`).
- `formal`: globs selecting formal models, relative to `spec.toml`, read for the tags that link them
  to the spec (`spec explain citations`). Only `.tla` files count. A model is not a spec file: it is
  never hashed or formatted, and its edits need no trailer. `spec ui` opens it and saves it
  directly; an agent proposes its edits with `spec change --tla`. Each entry must match at least
  one model, or error `project-file-missing`. A file both lists select stays a spec file, with error
  `config-invalid`.
- `[lint]`: `<rule> = "off" | "hint" | "warning" | "error"`, for any rule code listed in
  `spec explain markup`. `checked-by-missing` (an invariant body without a `Checked by:` line or a
  model's `\* Spec:` tag) is off unless set here. `ref-adjacent`, the old name of `math-hazard`, is still accepted.
  `spell` (default `hint`) is the spell check: `off` turns it off for the project (no flags anywhere and
  no model calls), and another level changes the severity its flags carry in `spec lint --spell`,
  `spec ui` and `spec lsp` (still drawn as a spelling underline).
- `[format]`: `width`, the column `spec fmt` and ^F in `spec ui` wrap paragraphs at (default 100).
- `[tla]`: `max_acquisition_visits`, the budget of visits prudence may spend in each pass while it
  reads a model and the modules it loads, before the semantic check (default prudence's, 1,000,000).
  It must be a positive integer, or error `config-invalid`. A model too large for it is checked
  incomplete with hint `formal-check-limit` (`spec explain citations`); set it for a project whose
  models need more. `--max-acquisition-visits N`, which every command accepts, overrides it for one
  run, `spec ui` and `spec lsp` included.
- Any other key is error `config-invalid`, naming the key. An `[inference]` table is an error too:
  provider settings live in the user config.

**The file set.** Every glob, the default and explicit `files` and `formal` entries alike, skips `.git`,
`node_modules`, `build`, `vendor`, `.spec`, and any subdirectory with its own `spec.toml` (a nested
project). Files are deduplicated and ordered by their
project-relative path, byte by byte; that order is the order of `spec ls`. Symbolic links are never
followed: a matched file that is a symlink is error `file-symlink`. A hidden file or an editor's working
file (`.*`, `*~`, `#*#`, `4913`) is selected only by a pattern whose last component looks like one (`docs/.*`,
`*~`), so `docs/*` does not take `docs/.a.md.swp`. A path outside the project
directory is an error. No spec files at all is error `project-no-files`.

**Finding the project.** `--project DIR` names the directory that holds `spec.toml`. Without it,
`spec` walks up from the current directory. If it finds none it exits 1 and lists any `spec.toml`
files within the git work tree (up to 4 levels deep) to pass to `--project`.

**`.spec/`.** A gitignored directory next to `spec.toml` for the lock file `.spec/lock` (held only
while spec files are written), proposals, `.spec/proposals/ID.json`, and the spell check's cache,
`.spec/spell/verdicts.json`. Only commands that write create it; `spec init` adds `.spec/` to
`.gitignore`.

**`spec.words`.** An optional committed word list next to `spec.toml`, one word per line, `#` for
comments: words the spell check always accepts (`spec explain spell`).

## User config

`$XDG_CONFIG_HOME/spec/config.toml`, or `~/.config/spec/config.toml` when `XDG_CONFIG_HOME` is unset
(on macOS too). It names inference providers of three kinds (and gates, `kind = "decisions"`, and canned
replies for UI scenes, `kind = "canned"`, below): any OpenAI-compatible
chat-completions endpoint (`kind = "openai"`, the default), the Claude Code CLI on your own login
(`kind = "claude-cli"`, below), so a Claude subscription needs no API key, or the Codex CLI on your
own ChatGPT login (`kind = "codex-cli"`, below), so a ChatGPT plan with Codex needs none either.
Consistency analysis and the invariant change checks call the provider; `spec doctor` checks it
with one tiny request and reports the latency and the model id the provider echoed. Commands that call a
model take `--provider NAME` (instead of `default`), `--engine ID` (the LLM model id, instead of
the provider's `model` and `triage_model`) and `--effort LEVEL` (instead of its `effort`), for
example `spec doctor --provider deepseek` or `spec analyze --all --effort high`.

    default = "cf"
    env_file = "~/.config/spec/keys.env"         # optional: the variables below, kept out of the shell
    max_context_tokens = 60000                   # optional, see below
    max_output_tokens = 16384                    # optional
    underline_color = "auto"                     # optional, spec ui: "auto", "on" or "off"

    [providers.cf]                               # Cloudflare Workers AI
    base_url = "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/ai/v1"
    model = "@cf/zai-org/glm-5.3-flash"
    triage_model = "@cf/zai-org/glm-5.3-flash"
    api_key_env = "CLOUDFLARE_AI_TOKEN"
    json_mode = true
    extra = { reasoning_effort = "low" }
    triage_extra = { reasoning_effort = "low" }   # GLM ignores "none"; the guard retries at low

- `default`: the provider used without `--provider`.
- `env_file`: a private file that supplies the variables named by `api_key_env` and by `${VAR}`
  references in `base_url`, when the environment lacks them (**Keys in a file**, below).
- `kind`: `"openai"` (the default), `"claude-cli"` or `"codex-cli"` (`"decisions"` and `"canned"` are
  below). The keys below are for `openai`; a `claude-cli` or `codex-cli` provider takes only
  `model`, `triage_model`, `effort`, `timeout`, `command` and `pass_env`, and any other provider key
  is an error.
- `base_url` (required): the API root; requests go to `base_url` + `/chat/completions`. Each
  `${VAR}` is replaced from the environment when the provider is used (Cloudflare's account id).
  Only the `${VAR}` form expands.
- `model` (required): the model for contradiction checks and analysis.
- `triage_model`: an optional cheaper model for the cascade's triage stage and the fast requests
  (below); default `model`.
- `triage`: `false` turns the cascade's triage stage off for this provider (default on when it has a
  `triage_model` or is named by `[triage]`).
- `api_key_env`: the name of the environment variable that holds the API key, sent as
  `Authorization: Bearer`. Never the key itself: the file holds names only. Leave it out for a local
  server that needs no key.
- `json_mode`: `true` when the provider accepts `response_format = {"type": "json_object"}`.
  Without it the prompt asks for JSON and the reply is parsed leniently (code fences and prose
  around the object are dropped).
- `extra`: a table merged into every request body, over the standard fields, so it can also
  replace one (a model that only accepts its default `temperature`). Nested tables merge key by key.
  It may not set `model`, `messages`, `stream` or `stream_options`.
- `triage_extra`: merged over `extra` for triage requests, and for the runaway guard (below). Make it
  the provider's low-effort setting, such as `{ reasoning_effort = "low" }`.
- `effort`: the reasoning effort, sent as `reasoning_effort`: `none`, `minimal`, `low`, `medium`,
  `high` or `xhigh` (what a model accepts varies). An `extra` or `triage_extra` that sets
  `reasoning_effort` wins over it; `--effort` wins over everything, the runaway guard included. A
  provider that spells it differently (OpenRouter's `reasoning = { effort = "low" }`) keeps using
  `extra`.
- `max_tokens_param`: `"max_tokens"` (default) or `"max_completion_tokens"`, the field that caps
  the reply. OpenAI's reasoning models reject `max_tokens`.
- `stream` (default `true`): ask for the reply as a stream of server-sent events. A long reasoning
  reply then keeps its connection alive; without streaming, Cloudflare ends any reply that takes
  longer than about 235 s with HTTP 408, and other gateways have similar limits. `false` sends one
  request and waits for the whole reply. A provider that rejects streaming with HTTP 400 naming
  `stream` or `stream_options` is asked again at once without `stream_options`, then without
  streaming (logged as a warning); `stream = false` skips those requests.
- `timeout`: the most seconds one attempt may take, start to finish: default 900 when streamed, 120
  when not.
- `idle_timeout` (default 90): seconds a streamed attempt may go without receiving a byte, headers
  included, before it is abandoned and retried. Keep-alive comments count as bytes. It does not
  apply when `stream = false`.

- `underline_color`: whether `spec ui` may colour underlines (SGR 58), as it does for diagnostics
  and for words changed since HEAD (green added, red removed). `"auto"` colours them except in
  Terminal.app (`TERM_PROGRAM=Apple_Terminal`), which draws a coloured underline blinking; there
  added prose turns green with a plain underline and removed words are struck through. `"on"` and
  `"off"` force it either way.

Optional, for the checks behind `spec change`, `spec propose` and `spec analyze`, and the commit
message `spec ui` drafts:

    [prompts]                                    # replace the built-in system prompts
    change = "~/prompts/change.md"               # relative paths: next to config.toml
    analyze = "analyze.md"
    commit = "commit.md"                         # the commit view's draft message
    gate = "gate.md"                             # the gate's policy and questions (below)
    triage = "triage.md"                         # triage's policy, questions and reply format
    spell = "spell.md"                           # the spell check's policy, Jev question and reply format

A `gate` or `triage` file keeps the built-in layout: a `## policy` heading, then one heading per
question (`material_change`, `numeric_mismatch`, `contradiction`, `stale_reference`), and for triage
a `## reply` heading, each followed by a blank line and a `text` code block. A `spell` file has
`## policy`, `## question` (with `{id}` for the candidate's number) and `## reply` blocks.

- `max_context_tokens` (default 60000, estimated as bytes/4): over it, a check sends the sections
  around the change instead of whole files and lists what it left out; `--full` sends everything.
- `max_output_tokens` (default 16384): the reply cap; a reasoning model's thinking counts against it.

A variable that `base_url` or `api_key_env` names but neither the environment nor `env_file` sets
makes `spec doctor` warn with the `export` line to add; commands that need the provider stop with
`provider_unavailable`. API keys are never printed, in errors either.

**Keys in a file.** `env_file = "PATH"` lets the providers' variables live in a private file
instead of your shell's environment. The config itself still never holds a key: it names
variables, and the file holds their values. `~` and `~/` are your home directory, a relative path
is relative to the directory of `config.toml`, and `${VAR}` in the path is not expanded. The file
is lines of `NAME=VALUE`:

```text
# provider keys for spec
export DEEPSEEK_API_KEY=sk-...
CLOUDFLARE_ACCOUNT_ID="0123abcd"
```

- Blank lines and lines starting with `#` are skipped; a leading `export ` is allowed.
- `VALUE` is everything after the first `=`, taken literally with surrounding whitespace trimmed.
  One pair of matching single or double quotes around it is removed; there are no escapes or
  expansions, and a `#` after the value is part of it.
- A name defined twice keeps its last value. Any other line is an error naming the file and the
  line number, never the line's text.
- **Precedence**: the environment wins. The file supplies a variable only when the environment
  has it unset or empty.
- It applies only to provider settings: `api_key_env`, the `${VAR}` references in `base_url`,
  gates (`kind = "decisions"`) and `[triage]` providers, and `spec doctor`. A `claude-cli` or
  `codex-cli` provider's `pass_env` never forwards a value from it: the CLI sees only the real
  environment.
- The file must be a regular file (not a symbolic link) that only you can read or write: spec
  refuses one whose group or others have any access (mode `0600` or `0400`; fix: `chmod 600 PATH`).
  A missing file is an error too.
- spec reads the file once per command, and only when a provider needs a variable the environment
  lacks, so commands that call no model never fail over it. `spec ui` reads it again when it
  changes. `spec doctor` always checks it: the `env_file` check shows the path, the mode and the
  names it defines (never a value), and `provider_key` says where each variable was found, for
  example `DEEPSEEK_API_KEY is set (env_file)`.

Transient failures (HTTP 408, 409, 425, 429, 5xx, timeouts, network errors) are retried with
jittered backoff, three attempts in all; each attempt times out after `timeout`. A stream that
breaks, goes quiet for `idle_timeout` or ends before the reply is complete is retried the same way,
even after part of the reply arrived (the retry starts the reply over). An error event inside a
stream is retried unless its code is a client error (4xx other than 408, 409, 425 and 429). The API
key is redacted from every error, streamed or not.

**Reasoning models.** GLM 5.3 Flash always reasons, on Cloudflare and OpenRouter alike:
`reasoning_effort = "none"` only shortens the reasoning on Cloudflare, and OpenRouter rejects
`reasoning = { enabled = false }` with HTTP 400. Its reasoning tokens count against the reply cap,
so `spec doctor`'s 8-token check may come back with no visible reply; that still passes. Keep
`extra = { reasoning_effort = "low" }` for it: on a whole-spec `spec analyze` (about 7k tokens of
spec) its default effort, and `"medium"` too, thought through all 16384 reply tokens in about five
minutes without answering, while `"low"` answered in under 100 s with about 2-3k tokens. Streamed
with a 32768-token cap, `"medium"` still spent the whole cap (about 11 minutes) in 2 of 2 runs.

**Runaway guard.** A reasoning model sometimes thinks until `max_output_tokens` and never answers
(finish reason `length`, no JSON). When the provider has a `triage_extra`, the check is sent once
more, to the same model, with `triage_extra` merged over `extra`; the proposal's `inference` block
then has `"runaway_guard": true` and counts both calls. Without a `triage_extra`, or when the retry
runs away too, the command fails with `bad_model_output` and says what to change.

### The per-edit cascade: gate and triage

The check of one changed anchor (`spec propose`, `spec change`, ^P in `spec ui`) can run in stages,
stopping at the first stage that clears the edit:

    [providers.jev]                              # a gate, not a chat provider
    kind = "decisions"
    api_key_env = "OPENROUTER_API_KEY"
    model = "typesafe/jev-1.14"
    # base_url = "https://openrouter.ai/api/alpha"   # the default
    # timeout = 10                               # seconds; the gate stage's default is 10

    [gate]                                       # absent: no gate
    provider = "jev"
    escalate_class_at = 0.45                     # default 0.45
    skip_material_below = 0                      # default 0, off

    [triage]                                     # absent: the check provider's triage_model
    provider = "cf"

1. **Gate.** A `kind = "decisions"` provider runs the gate. The example uses TypeSafe's Jev on OpenRouter. It answers four
   yes/no questions about the edit (`material_change` and the class questions `numeric_mismatch`,
   `contradiction`, `stale_reference`) with probabilities, in about 0.3 s for well under $0.001. The
   edit goes on when any class question scores at or above `escalate_class_at`; below, the gate
   clears it. `skip_material_below` (0 is off) also clears an edit whose `material_change` scores
   below it, whatever the class questions say. A decisions provider takes `api`, `model`, `base_url`,
   `api_key_env` and `timeout`. The default API is `"openrouter"`. For the native Google API,
   set `api = "google"`, `model = "gemini-decision-1"` and `api_key_env = "GEMINI_API_KEY"`.
   Settings → Add connection → Google Gemini Decisions sets these fields and the gate route.
   The default check provider must support chat. The gate makes one attempt: a gate that
   fails passes the edit on.
2. **Triage.** A chat model asked the same four questions, as one JSON reply; a class score of 0.5
   or more passes the edit to the check, below clears it. It runs on the `[triage]` provider with
   its `triage_model` (else its `model`) and its `triage_extra`; without `[triage]`, on the check
   provider's `triage_model` when it has one. `triage = false` on that provider turns the stage off.
   The whole stage is bounded at 60 s.
3. **Check.** The contradiction check, on the provider's `model`; it files the findings.

`[triage]` also takes the fast requests: the commit message `spec ui` drafts, and the runaway
guard's retry, which then goes to the triage provider as a triage request instead of to the same
model with `triage_extra`.

**Fail open.** A timeout, an HTTP error, a missing key or a reply that does not parse in the gate or
triage passes the edit to the next stage, and the stage is recorded as `failed_open` with the reason.
Only a change of an anchor's body is screened; an added, removed or relabelled anchor goes straight
to the check. Clearing an edit never changes what counts as a change: any byte change to an anchor
body still needs its `Spec-Change` trailer. `spec doctor` checks `[gate]` and `[triage]` with one
tiny request each.

The same two providers run the spell check's model pass (`spec explain spell`): Jev scores the
words the word list does not know, and the `[triage]` provider names the typos and their fixes. It
runs only when both `[gate]` and `[triage]` are set, and fails closed.

**One OpenRouter key for all three.** In `spec ui`, Help (^G) → Settings (^T) → Add connection →
OpenRouter API sets up a decision (gate) model, a triage model and an inference (check) model, each
from a short list of recommended models or any model id you paste:

- Gate: `typesafe/jev-1.14` (default) or `morph/reflex`.
- Triage: `z-ai/glm-5.3-flash` at effort low (default), `deepseek/deepseek-v4.1-flash-1017` at
  low, or `qwen/qwen3.8-27b` pinned to Cerebras (no JSON mode there).
- Check: `deepseek/deepseek-v4.1-flash-1017` at effort low (default), the same model pinned to
  OpenRouter's `fireworks/ultrafast` endpoint (faster and dearer, with no fallback), `z-ai/glm-5.3`
  at low, or `qwen/qwen3.8-27b` on Cerebras at medium.

Each role gets its own connection (`openrouter-gate`, `openrouter-triage`, `openrouter`), so one
model's settings, such as an endpoint pin, never reach another. They all name `OPENROUTER_API_KEY`,
or take a key for this session. `default`, `[triage] provider` and `[gate] provider` point at them;
a new `[gate]` gets `escalate_class_at = 0.45` and `skip_material_below = 0`. A role already set
(`default`, `[triage]`, the default provider's `triage_model`, or `[gate]`) is kept unless you pick a
model for it. Nothing is written until you review and save, and ^T in Model routing tests each role
with one tiny request.

### Claude Code subscription (kind claude-cli)

    [providers.claude]                           # the Claude Code CLI, on your own login
    kind = "claude-cli"
    model = "sonnet"                             # alias (haiku, sonnet, opus) or a full model id
    triage_model = "haiku"
    effort = "low"                               # optional: low, medium, high, xhigh, max
    # command = "/opt/homebrew/bin/claude"       # default "claude", found on PATH
    # pass_env = ["ANTHROPIC_API_KEY"]           # only to bill an API key instead of the plan
    # timeout = 900                              # seconds per request (default 900)

Each request runs Claude Code once, as one model turn with nothing else switched on, and reads its
JSON result: the reply, the token usage, the cost Claude Code reports (`cost_usd` in `spec doctor`
and in a proposal's `inference`) and the model id that answered. Log in once with
`claude auth login`; `spec doctor` then checks it. spec runs exactly:

    $ claude -p --output-format json --model MODEL --system-prompt=PROMPT --tools ""
        --strict-mcp-config --mcp-config '{"mcpServers":{}}' --setting-sources ""
        --disable-slash-commands --no-session-persistence --max-turns 1 [--effort LEVEL]

- `-p --output-format json`: print mode, one JSON result instead of a session.
- `--system-prompt=PROMPT`: spec's check prompt replaces Claude Code's long default one.
- `--tools ""`, and `--strict-mcp-config` with an empty `--mcp-config`: no built-in tools and no
  MCP servers, so the model can only answer.
- `--setting-sources ""`: none of your user, project or local settings, so no hooks, plugins,
  skills or permission rules from them; `--disable-slash-commands` turns off skills and commands.
- `--no-session-persistence`: nothing is saved to disk, nothing to resume.
- `--max-turns 1`: one model turn, never an agent loop.
- `--effort LEVEL`: Claude Code's own effort control, when `effort` or `--effort` sets one;
  without it Claude Code uses the model's default.

The prompt and the spec text go on stdin, never in the arguments. Claude Code runs in a fresh
empty temporary directory (removed afterwards), so it finds no `CLAUDE.md` or project settings,
and with a minimal environment: `HOME`, `USER`, `LOGNAME`, `PATH`, `TMPDIR`, the locale and `TZ`,
proxy and CA-certificate variables, `CLAUDE_CONFIG_DIR`, the variables `pass_env` names, and
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` (no autoupdate or telemetry). An `ANTHROPIC_API_KEY`
in your shell is left out, so the plan's login pays, unless `pass_env` names it. Measured with
Claude Code 2.1, a request carries about 640 tokens besides spec's own (Claude Code's environment
notes). When the timeout passes, or spec is interrupted, Claude Code and everything it started
are killed.

What does not carry over: there is no JSON mode (the prompt asks for JSON and the reply is parsed
leniently), `max_output_tokens` does not cap the reply (Claude Code sets its own cap), so the
runaway guard does not apply, and spec does not retry, since Claude Code retries API errors itself.
Not logged in, a plan's usage limit, an overloaded API and an unknown model each fail with
`provider_failed` and a fix line.

### ChatGPT subscription through Codex (kind codex-cli)

    [providers.codex]                            # the Codex CLI, on your own ChatGPT login
    kind = "codex-cli"
    model = "gpt-6.1-terra"                      # `codex debug models` lists your Codex's ids
    triage_model = "gpt-6-luna"                  # smaller and faster
    effort = "low"                               # optional: none, low, medium, high, xhigh, max
    # command = "/opt/homebrew/bin/codex"        # default "codex", found on PATH
    # pass_env = ["OPENAI_API_KEY"]              # only to bill an API key instead of the plan
    # timeout = 900                              # seconds per request (default 900)

Each request runs `codex exec` once, as one model turn with every tool Codex lets you switch off
switched off, and reads its JSON events: the reply and the token usage (Codex reports no cost and
echoes no model id). Log in once with `codex login`; `spec doctor` then checks it. Model ids come
from your Codex: Codex 0.155 lists `gpt-6-luna` and `gpt-5.6-terra`, Codex 0.160 adds
`gpt-6.1-terra`; an id your Codex does not list fails with a fix line naming `codex debug models`.
spec runs exactly:

    $ codex exec --json --color never --strict-config --ignore-user-config --ignore-rules
        --ephemeral --skip-git-repo-check --sandbox read-only -m MODEL
        -c model_instructions_file="spec-instructions.md" -c approval_policy="never"
        -c web_search="disabled" -c project_doc_max_bytes=0 -c history.persistence="none"
        -c analytics.enabled=false -c check_for_update_on_startup=false
        -c model_reasoning_summary="none" -c hide_agent_reasoning=true
        -c include_permissions_instructions=false -c include_apps_instructions=false
        -c include_collaboration_mode_instructions=false -c include_environment_context=false
        -c skills.include_instructions=false -c skills.bundled.enabled=false
        -c tools.experimental_request_user_input.enabled=false -c agents.enabled=false
        --disable shell_tool --disable multi_agent --disable apps --disable plugins
        --disable remote_plugin --disable hooks --disable image_generation --disable view_image
        --disable browser_use --disable computer_use --disable goals --disable skill_search
        --disable tool_suggest --disable sleep_tool --disable shell_snapshot
        [-c model_reasoning_effort="LEVEL"] -

- `exec --json`: one non-interactive run whose events (the reply, the usage, a failure) come
  out as JSON lines; `--color never` keeps them plain.
- `--strict-config`: an override Codex does not know is an error instead of being ignored, so a
  setting below can never lapse silently after a Codex update; spec then says to update Codex.
- `--ignore-user-config`: none of `~/.codex/config.toml` (your MCP servers, plugins, notify
  command, default model and effort); the login in `CODEX_HOME` is still used.
  `--ignore-rules`: no execpolicy rules.
- `--ephemeral` and `history.persistence="none"`: no session or history written.
- `--skip-git-repo-check`: the run's directory is an empty temporary one, not a git repository.
- `--sandbox read-only` and `approval_policy="never"`: should anything try to run, it can write
  nothing and nothing waits for approval.
- `model_instructions_file`: spec's check prompt, written to that file in the run's directory,
  replaces Codex's built-in instructions. `project_doc_max_bytes=0`: no `AGENTS.md`.
- `web_search="disabled"`, `agents.enabled=false`, `tools.experimental_request_user_input`, and
  the `--disable` list: no web search, sub-agents, question tool, shell, apps, plugins, hooks,
  image, browser or computer-use tools, goals, skills or tool suggestions.
- The `include_*` and `skills.*` settings: none of Codex's notes on permissions, apps,
  collaboration modes, skills or the environment (cwd, shell, date) in the prompt.
- `analytics.enabled=false`, `check_for_update_on_startup=false`: no analytics events and no
  update check. `model_reasoning_summary="none"`, `hide_agent_reasoning=true`: no reasoning
  summaries or reasoning events, which spec never shows.
- `model_reasoning_effort`: when `effort` or `--effort` sets one; without it the model's default.
- `-`: the prompt and the spec text come on stdin, never in the arguments.

What remains, measured with Codex 0.155 and 0.160 on GPT-6 Luna and Terra: about 1690 input
tokens per request besides spec's own (about 3280 with sub-agents left on). The models' catalog
entries run them in Codex's code mode, which always offers an `exec` tool (a JavaScript sandbox
with no file system or network) and a `wait` tool; no setting removes them. spec warns when a
reply took more than one turn or ran a tool, which it has not done in testing.

The environment is the same minimal one as for claude-cli, with `CODEX_HOME` instead of
`CLAUDE_CONFIG_DIR`; an `OPENAI_API_KEY` or `CODEX_API_KEY` in your shell is left out, so the plan
pays, unless `pass_env` names it. As for claude-cli: no JSON mode, no reply cap from
`max_output_tokens` (so no runaway guard), no retries by spec (Codex retries API errors itself),
and the whole process group is killed on timeout or interrupt. Not logged in, a plan's usage
limit, an unknown model, an effort the model rejects and a Codex too old for these settings each
fail with `provider_failed` and a fix line.

**Effort, by kind.** `effort` and `--effort` take the levels of the provider's kind:

| kind | levels | sent as |
|---|---|---|
| `openai` | `none`, `minimal`, `low`, `medium`, `high`, `xhigh` | `reasoning_effort` in the request body (an `extra` that sets it wins over `effort`; `--effort` wins over both) |
| `claude-cli` | `low`, `medium`, `high`, `xhigh`, `max` | `claude --effort LEVEL` |
| `codex-cli` | `none`, `low`, `medium`, `high`, `xhigh`, `max` | `codex exec -c model_reasoning_effort="LEVEL"` |

Codex's `ultra` is left out (it hands work to sub-agents) and so is `minimal` (GPT-6 Luna rejects
it).

A level the kind does not have is a `config-invalid` error in the file, and `provider_unavailable`
on the command line.

### Canned replies (kind canned)

A canned provider answers from a file of hand-written replies instead of a model. It is for the UI
scenes (`go run ./tools/scenes`), tests and demos: each reply is written to make the UI show one
outcome (a finding, a cleared edit, a slow or failing call), so no key or network is needed and every
run is the same.

    [providers.scenes]
    kind = "canned"
    path = "/abs/path/replies.toml"              # "~/" is the home directory
    triage_model = "canned-triage"               # optional: turns the triage stage on

A canned provider takes only `path` (required), `model` (default `canned`), `triage_model` and
`triage`. It is also a gate: `[gate] provider = "scenes"` serves its `[[gate]]` entries. The
replies file has one array of tables per request purpose, `check` (a contradiction check or an
analysis), `triage`, `gate`, `draft` (a commit message), `fix` (a lint fix), `revise` (a hunk
redone after feedback), and the spell check's `spell_gate` (Jev's scores, `typo_1` to `typo_N`)
and `spell` (the `ID=fix` reply), served in order within each purpose, never matched by prompt:

    [[gate]]
    scores = { material_change = 0.95, numeric_mismatch = 0.1, contradiction = 0.62, stale_reference = 0.05 }
    latency_ms = 300                             # the latency the reply reports

    [[check]]
    reply = '''{"summary": "...", "findings": [], "questions": []}'''
    delay_ms = 1500                              # hold the reply back (spinners, ^C)

    [[triage]]
    error = "timeout"                            # or "http_500", or "bad_json" (a reply that is not JSON)

Each entry takes `reply` (`scores` for a gate), and optionally `delay_ms`, `latency_ms` (default
`delay_ms`), `error`, `model` (the model the reply names), `cost_usd`, `prompt_tokens` and
`completion_tokens` (the usage the reply reports, default 0) and, for a chat entry,
`finish_reason` (default `stop`; `spec ui --record` writes what the model said). The providers opened on one file share
its place in each list for the life of the process, so the replies go on in order across checks; a
file whose content changed starts over. A call with no entry left fails with the purpose, the
entry's number and the first lines of the request: the entry to write next.

### More providers

    [providers.deepseek]                         # DeepSeek direct
    base_url = "https://api.deepseek.com"
    model = "deepseek-flash"
    api_key_env = "DEEPSEEK_API_KEY"
    json_mode = true

Leave DeepSeek's thinking on with `json_mode`: with `extra = { thinking = { type = "disabled" } }`
its JSON replies came back malformed in testing.

    [providers.openrouter]                       # OpenRouter
    base_url = "https://openrouter.ai/api/v1"
    model = "z-ai/glm-5.3-flash"
    api_key_env = "OPENROUTER_API_KEY"
    json_mode = true
    extra = { reasoning = { effort = "low" } }

    [providers.openai]                           # OpenAI
    base_url = "https://api.openai.com/v1"
    model = "gpt-5.4-nano"
    api_key_env = "OPENAI_API_KEY"
    json_mode = true
    max_tokens_param = "max_completion_tokens"
    extra = { reasoning_effort = "none" }

    [providers.gemini]                           # Gemini, OpenAI-compatible endpoint
    base_url = "https://generativelanguage.googleapis.com/v1beta/openai"
    model = "gemini-flash-lite-latest"
    api_key_env = "GEMINI_API_KEY"
    json_mode = true

    [providers.anthropic]                        # Anthropic, OpenAI-compatible endpoint
    base_url = "https://api.anthropic.com/v1"
    model = "claude-haiku-5-5"
    api_key_env = "ANTHROPIC_API_KEY"

    [providers.local]                            # a local server (Ollama, llama.cpp, vLLM)
    base_url = "http://localhost:11434/v1"
    model = "qwen3:8b"

## Appearance and personal colour schemes

`spec ui` ships two embedded schemes: `synthwave` (default, the dialed-back SynthWave ’84 study) and
`synthwave-quiet` (the quieter version). Choose one in Help → Settings → User / Appearance, review
and save User settings. The built-ins do not need to be written to disk. Dark/light foregrounds,
colour capability, glyphs and underline support remain independent preferences.

At the top level of personal `config.toml`, before any provider tables:

```toml
color_scheme = "synthwave-quiet"
# Optional: enables custom scheme discovery. Omit to use only embedded schemes.
color_scheme_dir = "colours"
```

No folder is scanned by default. `colours` above means a folder beside the personal config file;
`~/...` and absolute paths are also accepted. The project config never enables scheme loading.
Only top-level `.json` files are considered. A filename uses 1–64 lowercase letters, digits,
hyphens or underscores, starting with a letter or digit. Select `mine.json` with
`color_scheme = "custom:mine"` or select its friendly name in the picker.

For example, create `colours/mine.json` yourself:

```json
{
  "version": 1,
  "name": "My Synthwave",
  "base": "synthwave-quiet",
  "roles": {
    "control": {
      "dark": "#42dddd",
      "light": "#006666",
      "dark256": 80,
      "light256": 23
    }
  }
}
```

`base` is `synthwave` or `synthwave-quiet` (omitted: `synthwave`). Omitted roles and profile values
inherit from the base, so a complete palette is not required. RGB values must be `#RRGGBB`.
Indexed overrides are optional integers 0–255; when a custom RGB value changes without an indexed
override, Spec chooses a nearest xterm approximation. The built-ins use separately curated indices.
Origin, syntax and status tokens are independent, even when a built-in shares their hues.

The 39 role names are: `ink`, `secondary`, `structure`, `focus`, `disabled`, `control`, `human`,
`agent`, `fix`, `error`, `warning`, `positive`, `syntaxInvariant`, `syntaxDefinition`, `syntaxSection`,
`card`, `selectedCard`, `childCard`, `selectedChildCard`, `counterHover`, `keycap`, `positiveKey`,
`onPositiveKey`, `status`, `note`, `warningCount`, `proposalCount`, `otherCount`, `statusInk`,
`statusRule`, `statusWarning`, `statusPositive`, `statusAgent`, `statusError`, `statusHuman`,
`statusFix`, `gutterHuman`, `gutterFocus` and `gutterTick`.

The picker refreshes the list when opened. Restart the UI after editing an active custom file to
reload its colours. Invalid files are excluded with an error in Settings; an unavailable selected
scheme uses the built-in default and reports a status error. No scheme paints the full terminal
canvas. Optional `terminalSamples` metadata in a copied full palette is accepted for previews and
never changes the terminal background.
