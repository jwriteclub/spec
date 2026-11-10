<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Choosing models

These numbers come from our own testing between 30 October and 5 November 2026, on a small corpus of spec
edits with known answers. An automated, reproducible harness is planned. Results vary by spec, and with
two runs per case, differences of a few points are noise.

`spec` uses up to three models, one per role (`spec explain config` has the settings):

- **Gate** (`[gate]`, a `kind = "decisions"` provider): answers yes/no questions about one edit with
  probabilities, and clears edits that cannot conflict with anything.
- **Triage** (`[triage]`, or the provider's `triage_model`): a fast chat model asked the same questions; it
  clears what the gate passed on but should not have.
- **Check** (the provider's `model`): the contradiction check and `spec analyze`. It files the findings.

Each stage fails open: a timeout or bad reply passes the edit to the next stage.

## How we measured

**The corpus.** 64 synthetic cases with 81 planted issues. 27 of the cases are clean specs or clean edits
that should produce no finding, and some of those are built to bait false alarms (a precedence rule two
sections away, units that look mismatched but are converted). Topics are logic puzzles, calculators, money,
date and integer arithmetic, protocols, access control, payments and queues. The specs total about 6,500
lines. Cases are split into 42 dev and 22 holdout; prompts were tuned on dev only, and the check numbers
below are holdout.

**Scoring.** Each model ran each case twice, through `spec analyze --all` for whole-spec cases and
`spec propose` for edit cases. An Opus judge compared every run's findings with the answer key. A second,
independent judge agreed on 310 of 310 issue matches and on 218 of 220 false-positive calls.

- **Recall** is the share of planted issues found.
- **All found** is the share of runs that found every planted issue in the case.
- **False alarm** is the share of clean-case runs with at least one false positive.
- **Median** is wall seconds per run.

## Check

Holdout, 22 cases, 2 runs each, run 5 November 2026.

| Model | Route | Effort | Recall | All found | False alarm | Median | Cost per run |
|---|---|---|---:|---:|---:|---:|---:|
| DeepSeek V4.1 Flash (`deepseek-flash`) | DeepSeek API | high | 100% | 100% | 0% | 65 s | not reported [1] |
| DeepSeek V4.1 Flash (`deepseek-flash`) | DeepSeek API | low | 98% | 96% | 6% | 40 s | not reported [1] |
| Claude Sonnet 5.5 | Claude Code CLI | medium | 98% | 96% | 0% | 12 s | $0.020 [2] |
| Claude Haiku 5.5 | Claude Code CLI | default | 94% | 85% | 11% | 18 s | $0.0028 [2] |
| GPT-6.1 Sol | Codex CLI | default | 100% | 100% | 22% | 20 s | subscription |
| GPT-6 Luna | Codex CLI | medium | 71% | 54% | 39% | 10 s | subscription |
| GLM 5.3 | Cloudflare Workers AI | low | 81% | 58% | 6% | 11 s | not reported [3] |
| GLM 5.3 Flash | Cloudflare Workers AI | low | 77% | 54% | 6% | 29 s | about $0.0003 [4] |

[1] DeepSeek's API reports no cost per call. At list prices, one edit check at low effort came to about
$0.0075, and one whole-spec check at high effort about $0.03 off-peak.

[2] What Claude Code reports, as an API-equivalent price. On a subscription the plan absorbs it.

[3] Cloudflare bills in its own units, not dollars. The GLM 5.3 row is a separate run later the same day;
an earlier run in the 8-model batch gave 85% recall, 77% all found and 11% false alarm.

[4] Estimated from one spec of about 7.5k tokens, not measured on the corpus.

An earlier pass on 31 October gave DeepSeek at low effort 100% recall with no false alarms, Sonnet 97%,
Haiku 95% and GLM 5.3 Flash 81%. The ranking held between the two passes.

Notes on the rows:

- GPT-6.1 Sol finds everything but over-reports. Half of its false positives were on one case.
- Haiku misses more than Sonnet and false-alarms on clean specs more often. In one sample it ignored a
  rule that said the more specific rule governs.
- GLM 5.3 Flash finds every planted issue on edits but misses many on whole-spec analysis (62% on holdout),
  and its misses repeat from run to run. It is a triage model, not a check model.
- High effort on DeepSeek took 60% longer than low and found about the same.

### What we use

DeepSeek V4.1 Flash at low effort (`extra = { reasoning_effort = "low" }`). It found 98-100% of planted
issues across two passes with few false alarms, at about 40 s and under a cent per edit check. High effort
found no more. On a Claude subscription, use
Sonnet at medium effort: nearly the same recall and no false alarms, in 12 s. On a ChatGPT subscription,
GPT-6.1 Sol finds the most, but expect a false alarm on about one clean spec in five. We do not recommend
GPT-6 Luna or either GLM model as the check model.

## Gate

The corpus's 31 edit cases, run twice: 16 edits that cause a problem, 5 editorial edits (wording only)
and 10 material edits that conflict with nothing. Counts are case-runs. A good gate sends on every
problem and as few of the rest as possible; every edit it sends on costs a triage or check call. All
models ran through OpenRouter's Decisions API with the same policy and questions, at the default
threshold of 0.45. Run 5 November 2026.

| Model | Problems sent on | Editorial sent on | Material, no conflict, sent on | p50 latency | Cost per call |
|---|---:|---:|---:|---:|---:|
| TypeSafe Jev 1.14 (`typesafe/jev-1.14`) | 32/32 | 0/10 [5] | 13/20 | 0.23 s | $0.000089 |
| Morph reflex (`morph/reflex`) | 32/32 | 0/10 | 4/20 | 0.69 s | $0.000133 |
| Liquid d1.1 (`liquid/d1.1`) | 31/32 | 1/10 | 7/20 | 0.29 s | $0.000039 |
| Perplexity decider flash 1.2 | 28/32 | 0/10 | 0/20 | 0.30 s | $0.000060 |
| Microsoft decision-1 | 32/32 | 7/10 | 17/20 | 0.43 s | $0.000079 |

[5] A separately recorded run the same day had Jev send on 1 of 5 editorial edits, and reflex 0 of 5.

- The 0.45 threshold was tuned on Jev's earlier results, which favours Jev in this table.
- Jev's known false alarm is a changed number that nothing else in the spec states: it scores that edit
  high on "numeric mismatch". Triage then clears it.
- Jev's weak spot is a contradiction with no shared number or wording, only a rule in one place that
  breaks a rule in another. On an earlier 41-edit set it caught one such edit at 0.49-0.55, just over the
  threshold.
- With a per-model threshold, Perplexity's model needs about 0.15 to catch every problem, and reflex works
  up to about 0.70. We have not validated those thresholds beyond this corpus.
- Every problem here sits within two references of the edited text, inside what the gate sees. Problems
  further away were not tested.

### What we use

Jev 1.14 at `escalate_class_at = 0.45`. It sent on every problem in every run we made, in a quarter of a
second, for under a hundredth of a cent. Morph reflex also caught every problem and sent on fewer harmless
edits (4 of 20 against 13 of 20), at about three times the latency; it is the alternative `spec ui` offers.
Leave `skip_material_below` at 0: Jev's `material_change` score was unreliable on rewordings in the corpus.

## Triage

Triage was measured on a separate set: 41 edits of one spec, with 11 that cause a problem and 30 that do
not (13 of them editorial). One run per model, so this is a rough comparison, not a ranking. A model sends
an edit on at a class score of 0.5 or more. Run 30 October 2026.

| Model | Route | Effort | Problems sent on | False alarms | p50 / p95 latency | Cost per call |
|---|---|---|---:|---:|---:|---:|
| GLM 5.3 Flash | OpenRouter | low | 11/11 | 0/30 | 1.8 / 5.5 s | $0.00013 |
| DeepSeek V4.1 Flash | DeepSeek API | low | 11/11 | 0/30 | 2.5 / 7.2 s | about $0.00013 [6] |
| Qwen3.8-27B | OpenRouter, pinned to Cerebras | low | 11/11 | 0/30 | 1.0 / 2.8 s | $0.0024 |
| gpt-oss-120b | OpenRouter, pinned to Cerebras | low | 10/11 [7] | 2/30 [7] | 0.5 / 1.0 s | $0.00066 |

[6] Estimated from list prices; DeepSeek's API reports no cost.

[7] 7 of 41 replies did not parse. A reply that does not parse counts as "send on", so 5 of its 10
catches and both false alarms come from parse failures.

In the corpus runs, GLM 5.3 Flash triage cleared 10 of 20 edit runs and none of those hid a planted issue.
On Cloudflare Workers AI, GLM 5.3 Flash answered triage in 1.1-8.4 s in a live check; two early Cloudflare
probes took 5 s and 43 s, so measure Cloudflare's queueing before relying on it for speed. Claude Haiku and
GPT-6 Luna were not measured in the triage role.

### What we use

GLM 5.3 Flash at low effort. It cleared every harmless edit in the set and passed every problem, for about a
hundredth of a cent per call. DeepSeek V4.1 Flash at low effort scored the same at the same cost with a
longer tail. Qwen3.8-27B on Cerebras is the fastest of the three but costs about 19 times as much per call,
and needs `json_mode = false`. We do not recommend gpt-oss-120b for triage.

## One spec, three planted issues

Before the corpus existed we ran whole-spec checks (`spec analyze --all`) on one service spec of about 7.5k
tokens, with three planted issues: a mismatched number, a contradiction between two rules, and prose left
stale by a rule change. This covers some models the corpus did not. Three runs per model unless noted; cost
is per run where the provider reported it.

| Model | Route | Effort | Issues found per run (of 3) | False positives per run | Mean latency | Cost per run |
|---|---|---|---:|---:|---:|---:|
| DeepSeek V4.1 Flash | DeepSeek API | high | 3.0 (4 runs) | 0.75 | 240 s | about $0.031 off-peak |
| Claude Haiku 5.5 | Claude Code CLI | default | 3.0 | 0 | 35 s | $0.0056 [2] |
| GPT-6 Sol | Codex CLI | default | 3.0 | 1.7 | 65 s | subscription |
| GPT-6 Astra | OpenAI API | default | 3.0 | 2.0 | 73 s | about $0.28 (estimated) |
| Beam 1.0 501B-A23B | OpenRouter, pinned to BaseTen | default | 2.3 | 0 | 44 s | $0.027 |
| GPT-6 Luna | Codex CLI | default | 2.3 | 0.7 | 8 s | subscription |
| Claude Sonnet 5.5 | Claude Code CLI | medium | 2.0 | 0 | 11 s | $0.033 cold cache, $0.018 warm [2] |
| Qwen3.8-27B | OpenRouter, pinned to Cerebras | medium | 2.0 (2 runs) | 0 | 7 s | $0.022 |
| GLM 5.3 Flash | Cloudflare Workers AI | low | 2.0 (6 runs) | 0.17 | 52 s | about $0.0003 |
| GLM 5.3 | OpenRouter | low | 1.7 | 0 | 12 s | about $0.007 |

Every model found the mismatched number and the contradiction in most runs; the stale prose separated them.
This set also shows how much results depend on the spec: Haiku found everything here and beat Sonnet, but
on the corpus Sonnet found more and false-alarmed less. Qwen3.8-27B is one of the check models `spec ui`
offers for OpenRouter, and this table is all we have measured for it in that role.

## Excluded

- **gpt-oss-120b on Cerebras at high effort.** Every call stopped at 8,192 output tokens before it
  answered, so its scores measure a provider cap, not the model.
- **GLM 5.3 Flash at medium effort.** Stopped partway through the corpus: about 15 times slower than low
  effort and less accurate.
- **Runs that hit rate limits** (some GLM runs on Cloudflare, caused by running several evals at once) are
  left out of recall.
- **ScaleDown classify-1** as a gate: it needs a request shape `spec` does not send yet.
- The DeepSeek endpoint pinned to OpenRouter's `fireworks/ultrafast`, offered in `spec ui`, has not been
  measured.
