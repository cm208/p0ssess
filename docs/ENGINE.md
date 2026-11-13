# The Predictive Model Engine

The engine decides when another model is the better choice. For a task Claude describes, it scores every route that could do it (Claude in the session, a native Claude subagent, one OpenRouter call, a sandboxed OpenCode session, a local tool) by its expected cost per **accepted** result, removes the routes that do not fit, are not allowed, or cannot save anything, and returns a ranked list with a cost breakdown, a one-line reason and whether the user has to approve it. Claude makes the final call.

This document is the method: how routes are scored, without the data. The scoring runs on p0ssess's engine service, which holds the catalog, the model registry and the benchmark evidence. The plugin sends it the task (a use-case id or a short profile Claude writes, never the user's words or files), the stakes, λ, the user's forbid, allow, only and mix rules, the model and provider lists, the privacy mode, any scoring keys changed in `engine.toml` and outcome counts per use case and model, with no timestamps, projects or sessions. Everything that spends or limits spending stays on the user's machine: budgets, rules, standing approvals, the ledger, approval (section 8) and the guard in front of every paid call (Enforcement). When the service cannot be reached, the plugin answers from a signed route table (the engine's answers at default settings), without saying so; a repository with a `.p0ssess-private` file never calls the service.

Status: built and tested. The weights it uses (harm, ceilings, the pressure curve, review costs) are presets, not measurements; the defaults and the reasons for them are below, and all of them can be changed.

## Try it

The plugin runs all of this for Claude. From a shell, with the plugin's launcher (or any `p0ssess` binary):

```sh
bin/p0ssess engine recommend --use-case video-understand
bin/p0ssess engine recommend --text "transcribe the call recording" --in audio --audio-seconds 1800
bin/p0ssess engine recommend --use-case code-implement-feature --stakes 3
bin/p0ssess engine status
bin/p0ssess engine usecases          # the use case ids in the route table
bin/p0ssess engine refresh           # fetch the signed route table now (otherwise once a day)
bin/p0ssess me --open                # the local page
```

Set `P0SSESS_HOME` to a scratch directory to try it without touching your real config: it becomes the config directory, with `cache/` and `state/` inside.

### How it reaches Claude Code

The plugin wires it in (see [INSTALL.md](INSTALL.md)):

- **MCP tools:** the plugin's one MCP server, `p0ssess mcp`, carries the engine tools next to the relay and direct tools. Inside the plugin they are named `mcp__plugin_p0ssess_p0ssess__<tool>`.
- **Prompt hint:** the plugin's `UserPromptSubmit` hook runs `p0ssess hook prompt`.
- **The possession:** routed tools (`direct`, `run`, `wait`, `abort`, `answer_permission`, `recommend`, `models`) are used by the `p0ssess` agent on Haiku. The plugin's `PreToolUse` hook (`p0ssess hook tool`) denies them in the main conversation unless the user's latest prompt asked for that, and its `PostToolUse` hook on the Agent call keeps main Claude quiet until the result arrives (DESIGN.md, Agents).
- **Routing behavior:** the plugin's `routing` skill teaches Claude to act on media requests with one `direct` call and no questions, rate stakes, ask only on "needs approval", present generated files, report outcomes it did not use and turn plain language into rules.
- **Status line and allow rules:** a plugin cannot install either, so `/p0ssess:p0ssess-me` offers `p0ssess setup statusline` and `p0ssess setup allow` when they are missing, each only after you say yes. `p0ssess setup undo` reverts them.

## Defaults decided for the first release

These were decided without measurements and are the owner's to change:

- **Harm and ceilings** keep the values in section 7.
- **Plan `auto`** prices Claude at the API list price until the status line sees a subscription's rate-limit reading, then switches to the subscription curve. Set `plan` to stop guessing.
- **Claude subagents may take stakes-4 work.** They are Claude; only the ceiling for the level applies to them. Other models still need an `allow` rule at stakes 4.
- **Cross-family second opinion.** For stakes 3-4 work that edits code, `recommend` may add `second_opinion`: two models from different families (the catalog's `dual-review-high-risk` entry, Claude excluded), with their cost for reviewing the diff. It is offered only when it fits the monthly cap and the daily limit, it asks first when a review costs more than the per-task limit (unless a standing approval covers it), and it is additive: Claude still reviews the diff and decides with the user.

## How a recommendation is made

### 1. The task

Claude passes a catalog id (`use_case`) when it knows one, and a **profile** for anything else: a short description (not the user's content), input and output modalities, and a size. With no id, a classifier ranks use cases by required modalities first, then word overlap with the description, then the path hint and the stakes range, and reports its confidence and the runners-up. A weak match (below 0.4) comes back with a note asking for an explicit id.

**Stakes** come from Claude (1 trivial to 4 critical, DESIGN.md's rubric). When Claude passes none, the catalog's first-listed level is used, with a note when the job spans a range. `limit-fallback-coding` inherits the stakes of the work it carries and assumes 2 until told.

**Size** comes from the profile (`size` T/S/M/R/L, `in_tokens`/`out_tokens`, `bytes` at 4 per token, `turns`, `items`, `audio_seconds`, `video_seconds`, `images`, `pages`, `chars`, `gen_seconds`). Anything missing comes from the engine's map of the catalog, which gives every use case a default size, its evidence family and its modalities. The size profiles are the catalog's: T 1k in / 50 out, S 5k / 1k, M 30k / 3k, R 30k / 6k, L 200k / 4k.

Media read by a chat model is converted to tokens at 32 per second of audio, 1,500 per image and 2,000 per PDF page (RESEARCH.md's ranges). Video uses the model's own rate where p0ssess's video check measured one (91 for both Gemini models, about 228 for the Qwen models, 287 for GLM-5.3-Flash) and 290 per second (Google's frame and audio rates) for every other model, including those whose count did not grow with length.

### 2. The routes

| Route | What it is | Cash | Claude usage |
|---|---|---|---|
| `claude` | the lead model does it in the session | 0 | the work itself |
| `claude-subagent` | a native subagent on a smaller Claude (Haiku 5.5 by default) | 0 | the work at the subagent's price, plus hand-off and review |
| `direct` | one OpenRouter call | yes | hand-off and review |
| `agent` | a sandboxed OpenCode session (use cases with turns) | yes | hand-off and review |
| `local` | the catalog's local tool (codemod, grep, ffmpeg, Presidio) | 0 | running it and checking it |
| `engine` | an OpenRouter PDF engine (`pdf-table-extract`) | yes | hand-off and review |

OpenRouter candidates are the models the catalog names anywhere plus every model with clean benchmark evidence (`candidates = "evidence"`), the use case's own list only (`"catalog"`), or every model in the registry including the live list (`"all"`). Batch (`:batch`) variants are offered only where the catalog names them, because they return hours later.

### 3. The filters

In order, a candidate is dropped when:

1. **It does not fit.** Every input modality must be accepted and the output produced (a transcript can come from a speech-to-text model or from a chat model that takes audio). The prompt plus output must fit the context window and the output limit. Agent routes need tool use.
2. **Privacy says no.** A `.p0ssess-private` file in the repository, or in any directory above the working directory, blocks every route but Claude and local tools. In `zdr` mode only models with a zero-retention endpoint qualify (optionally also safety-retention ones), priced at that endpoint, and batch variants never do. In the default `no-training` mode every call sends `provider.data_collection: "deny"`, so no model is dropped for policy.
3. **A rule or list forbids it.** Forbid rules, `only` and `mix` rules, the OpenCode and Claude-subagent switches, and the budget's model and provider allow and deny lists.
4. **The stakes gate.** Stakes 4 stays on Claude (the lead model) unless an `allow` rule with `max_stakes = 4` names the model; then Claude reviews all of it. `allow delegation` lifts the gate at most to stakes 3, and project rules cannot lift it. Otherwise the candidate's p_fail upper bound must be under the ceiling for the level. The PDF-engine route is delegation too: stakes 4 and zero-retention mode exclude it.
5. **It cannot save anything.** If Claude could do the work and `(1 - p_fail) × L_direct ≤ L_overhead + L_review`, delegating costs more Claude usage than it saves at any pressure, so it is never offered. This is RESEARCH.md's break-even rule; it is why one-turn questions and small edits stay on Claude. It does not apply when Claude cannot do the job at all.
6. **The monthly cap.** If this month's spend and open reservations plus the run's conservative cash would pass the cap.

Every dropped model the catalog names is reported with its reason (`excluded`).

### 4. The cost

```
cost = cash + λ·claude + λ·(overhead + review) + p_eff·(redo + harm_eff)
```

| Term | Meaning |
|---|---|
| `cash` | OpenRouter dollars for the run, at the cheapest endpoint that qualifies, times 1.055 for OpenRouter's credit fee. Models whose reasoning cannot be turned off bill another 1× the output as reasoning. Long-context price tiers apply above their threshold. Agent sessions are `turns × (context × cache-read + new input × input + output × output)` with 40k context, 3k new input and 1k output per turn; a test checks this lands within 2.5× of OpenRouter's 10–49-turn session-cost medians for Sonnet, Opus, Haiku, GPT-6.1 Sol, DeepSeek and GLM. |
| `claude` | Claude list-price dollars for the work done on Claude (lead or subagent). |
| `overhead` | the lead model's hand-off turns: $0.05 at Sonnet's price (RESEARCH.md measured $0.04–0.06), scaled by the lead model's output price, so $0.10 on Opus. |
| `review` | Claude checking the result: a fixed part by verification level plus the share of the result it reads at the lead model's input price (table below). Code edits are always reviewed in full. |
| `λ` | the weight on Claude's list price, from the plan and limit pressure (below). Overhead and review are Claude usage too, so λ applies to them. |
| `p_eff` | the chance the result is not accepted, after retries for mechanically checked jobs. |
| `redo` | what a failure costs to recover: `λ × L_direct` when Claude can redo it, or another run (cash plus hand-off) when only another model can. |
| `harm_eff` | `escape(verification) × harm(stakes)`: the cost of a bad result beyond redoing it, times the share of failures that get past the check. |

**Mechanical checks** (tests, schemas, placeholder parity, recomputation) let a failed run be retried automatically. With r retries (default 2), the miss rate is `p^(r+1)` and expected runs are `1 + p + … + p^r`, so cash and Claude work scale by that. This is how a cheap model takes a stakes-3 job when tests define acceptance, as RESEARCH.md found for test-gated migrations.

The lowest total wins. A `mix` rule marks the member furthest below its share as next, and the pick goes to it unless it costs more than 1.5× the cheapest option.

### 5. p_fail

p_fail starts from clean benchmark evidence for the use case's **task family**. Each family names one or more boards:

| Family | Board | f_best | f_worst |
|---|---|---|---|
| agentic_coding | LMArena agent (IPS) | 0.12 | 0.60 |
| coding | LMArena text, coding | 0.06 | 0.45 |
| webdev | LMArena webdev | 0.10 | 0.55 |
| chat | LMArena text, overall | 0.05 | 0.40 |
| instruction | LMArena text, instruction following | 0.05 | 0.40 |
| creative | LMArena text, creative writing | 0.08 | 0.45 |
| multilingual | LMArena text, non-English | 0.06 | 0.45 |
| long_context | LMArena text, longer query | 0.06 | 0.45 |
| hard_reasoning | LMArena text, hard prompts | 0.08 | 0.50 |
| factual | LMArena factuality | 0.06 | 0.45 |
| business, legal, science_writing, writing | LMArena text, the matching industry category | 0.06–0.08 | 0.45–0.50 |
| math | Epoch FrontierMath 1–3, LMArena math | 0.08 | 0.55 |
| science | Epoch GPQA Diamond, OpenRouter GPQA | 0.06 | 0.45 |
| tool_use | OpenRouter tau-bench airline | 0.10 | 0.50 |
| search | LMArena search | 0.08 | 0.45 |
| ocr, vision | LMArena vision, OCR and overall | 0.06 | 0.45 |
| document | LMArena document, longer query | 0.08 | 0.45 |
| image_gen, image_edit | LMArena text-to-image, image edit | 0.15 | 0.55 |
| video_gen | LMArena text-to-video, image-to-video | 0.20 | 0.60 |
| video_understanding | p0ssess's own video check ([MEDIA.md](MEDIA.md#video-accuracy-check)) | 0.15 | 0.60 |

On a board, a model's score becomes a relative quality `q` against the best model there:

- Bradley-Terry ratings: `q = 10^((r - best) / 400)`, the model's strength relative to the best one's. 50 points behind gives 0.75, 150 behind gives 0.42.
- IPS agent scores: `q = clamp(s / best, 0, 1)`. A score at or below zero means no measured lift.
- Accuracies (Epoch, OpenRouter evals, p0ssess's own checks): `q = acc / best`.

Then `p_fail = f_best + (f_worst - f_best) × (1 - q)`, averaged over the family's boards where the model appears. The model's best variant is used (Epoch and LMArena scores at high effort are upper bounds for default-effort calls). Dated snapshots share evidence: `deepseek-v4.1-flash-1017` borrows `deepseek-v4.1-flash`'s coding score.

**Uncertainty.** Each estimate carries a sigma: the board's own interval mapped through the same formula, plus 0.05 for the mapping itself, 0.08 more when the family only approximates the job (`proxy` in the engine map), and 0.03 more for borrowed snapshot evidence. The stakes gate checks `p_fail + sigma`.

**No clean evidence.** A model with no record on an evidenced family sits three quarters of the way to f_worst with sigma 0.20: wide and pessimistic, so it passes at stakes 1, fails at stakes 2 on agent work, and never passes at stakes 3. Families with no clean benchmark at all (speech-to-text 0.10, speech synthesis 0.12, audio understanding 0.15, music 0.25, embeddings 0.05, reranking 0.05, safety classification 0.15) use that prior for the catalog's recommended model, 0.03 more for its alternatives and 0.10 more (sigma 0.20) for models the catalog does not name. RESEARCH.md is explicit that these families have no clean evidence; logged outcomes are the only way to improve them.

**Measured by p0ssess.** Video understanding had no public benchmark either, and the catalog prior ranked models by price alone: GLM-5.3-Flash came first and MiMo-v2.6-flash third. A small check with known ground truth (three synthetic clips, two runs per model, [MEDIA.md](MEDIA.md#video-accuracy-check)) now stands in for a board, cited as "p0ssess measurement, 2026-11-09". f_best stays at the old prior (0.15), so the best measured model is unchanged, and f_worst is 0.60, the highest any family uses, so a model with no record still passes at stakes 1 and not at stakes 2, as elsewhere. The same check showed that a video's soundtrack is part of the job: models without audio input scored 0 of 8 on sound and two of them said every clip was silent, so `video-understand` now requires audio input, as its catalog entry already said. Since 0.5.2 it also drops a model that described a sound the clip does not have in more than 1 of 6 answers (counted in the same check). The accuracy score does not count an invented sound on its own, and once video was priced at measured token rates, Gemini 3.1 Flash Lite (5 of 6) ranked first on price at stakes 1 and 2. Gemini 3.8 Flash (1 of 6) and Qwen3.8 Omni Flash (0 of 6) stay.

**History.** Outcomes in the ledger update the prior as a beta-binomial: the prior counts as 8 pseudo-observations when it comes from benchmarks and 3 when it is a family prior. Accepted runs are successes and redone runs are failures; outcomes on other use cases in the same family count half. Sigma shrinks with the square root of the added weight. Declines and overrides do not change p_fail (they are preferences, not quality), but add a small friction term (the wasted hand-off times the smoothed decline rate) and a note when the user usually declines that route.

**Availability.** For OpenRouter models, one-day uptime turns into a failure chance, halved when more than one provider serves the model (OpenRouter falls back): `p = 1 - (1 - p_quality)(1 - unavailable)`.

### 6. λ, the weight on Claude

RESEARCH.md compared four ways to price Claude for a subscriber and recommended one family; the engine implements it.

- **API-key plan:** λ = 1. Claude costs its list price, which matches Claude Code's own cost figure.
- **Subscription:** `λ = floor + (1 - floor) × û^k`, where û is projected end-of-window use, `u / max(elapsed, e_min)`, taken from the worse of the 5-hour and weekly windows. λ reaches 1 (list price, the one documented dollar anchor: usage credits bill at API rates) exactly at projected exhaustion. Past exhaustion it stays 1 with usage credits on; with them off it rises by `beta × (û - 1)`, capped at 3, because a blocked window costs more than list price. With no fresh reading (none yet, or older than 2 hours) λ is the floor.
- **Plan `auto`** (the default) means subscription once the status line has seen a rate-limit reading, and API pricing until then, with a note asking you to set the plan.

The shape is a preset (`lambda.preset`):

| Preset | floor | k | What it does |
|---|---|---|---|
| `balanced` (default) | 0.15 | 2 | Claude is cheap early in a window and rises smoothly to list price at projected exhaustion |
| `gentle` | 0.10 | 3 | Claude stays cheap until a window is nearly spent |
| `eager` | 0.35 | 1 | routes earlier; this is what **save Claude** mode switches to |
| `list` | 1 | – | Claude always at list price (option A), on any plan |
| `custom` | your `floor` | your `exponent` | |

Why these numbers: none is a sourced fact, and RESEARCH.md says so. The floor exists because a large Claude job early in a window is not free (the limit is shared with claude.ai and other devices) and so routing does not flip abruptly when pressure crosses a threshold. 0.15 is low enough that a subscriber's Claude still beats a paid route on ordinary work early in a window, and high enough that big jobs still register. k = 2 keeps λ under 0.4 until a window is projected half spent, then climbs steeply. `e_min = 0.15` stops a reading in the first minutes of a window from being extrapolated more than about 6.7×. Every recommendation records the preset and λ in the ledger, so these can be calibrated against acceptance later.

Example, from the test suite: a 5-hour window 62% used with 90 minutes left projects to 89%, so balanced λ is 0.82 and save-Claude λ is 0.93.

### 7. Defaults for harm, ceilings and review

| Stakes | harm | p_fail ceiling |
|---|---|---|
| 1 trivial | $0.01 | 0.80 |
| 2 routine | $0.10 | 0.65 |
| 3 important | $1.00 | 0.40 |
| 4 critical | $10.00 | 0.20, and Claude only unless a rule allows |

| Verification | escape | fixed review | share read |
|---|---|---|---|
| none (pass-through, labeled) | 1.0 | $0.005 | 0 |
| mechanical | 0.1 | $0.01 | 0.1 |
| spot | 0.5 | $0.03 | 0.5 |
| full (diff from a worktree) | 0.2 | $0.06 | 1.0 |

Why: harm grows tenfold per level because DESIGN.md asks for "small at stakes 1, steep at each level above", and because the cost of a shipped bug or a wrong security write-up dwarfs the price of a run. It is expressed in dollars so it adds to the rest of the formula. The escape shares say how much review catches: a mechanical check catches almost everything it tests, a full diff review most things, a spot check about half, and a pass-through nothing. The review costs are RESEARCH.md's measured $0.04–0.12 hand-off-plus-check range split between overhead and review.

The ceilings were set against the clean agent evidence, which is where RESEARCH.md said the stakes gate matters: at stakes 3 the ceiling sits between GPT-6.1 Sol (p_fail 0.30 on agent work) and DeepSeek V4.1 Flash (0.54), so feature work can go to Sol but not to the flash models unless tests decide acceptance; at stakes 2 flash agents qualify with a full review; stakes 1 admits nearly anything with evidence or a catalog prior.

### 8. Approval

Whether to ask is decided from the **estimate**: the run's cash at list price, including retries, without the 1.5× headroom (`estimate_usd` on each option). The headroom and the relay's `max_run_cost_usd` are ceilings, enforced at spend time (see Enforcement), not reasons to ask. An option **needs approval** when any of these hold, unless a standing approval covers it:

- its estimate is above the per-task auto-approve limit (default $0.02);
- its estimate would pass the daily soft limit (only a session `daily_usd` override lifts that);
- the stakes are 4 (possible for another model only with an `allow` rule naming it with `max_stakes = 4`);
- an `ask` rule covers it ("always ask before using grok");
- `ask.first_use` is on and the model has never run (no run or accepted or redone outcome in the ledger), or `ask.code_edits` is on and the work edits code. Both are off by default: an edit always comes back as a diff Claude reviews before anything is applied.

A standing approval covers an option when it matches the use case or kind (and model, if it lists models), its per-run limit covers the estimate, and its count or total for the period is not used up. Stakes 4 and `ask` rules need a yes for this run: only a session-scope approval (the one-run approval Claude saves after the user says yes) covers them. Approval use is counted from the runs and reservations the tools tagged with the approval.

A monthly cap the run would pass is not an approval question: the option is excluded, and the tools refuse until the cap is raised.

The daily soft limit defaults to pacing: `(monthly cap - spent before today) / days left in the month`.

## Config files

| File | What it holds |
|---|---|
| `~/.config/p0ssess/budget.toml` | the budget, plan, privacy and model lists |
| `~/.config/p0ssess/engine.toml` | engine presets and the `[statusline]` settings |
| `~/.config/p0ssess/rules.toml` | global rules |
| `<repo>/.p0ssess/rules.toml` | project rules |
| `~/.local/state/p0ssess/sessions/<id>/rules.toml` | session rules (expire after 12 hours unless set) |
| `~/.local/state/p0ssess/history.jsonl` | the ledger |
| `~/.local/state/p0ssess/relay/` | the relay's sessions, transcripts, jail records and run log ([RELAY.md](RELAY.md#history-and-files)) |
| `~/.cache/p0ssess/usage.json` | Claude usage readings and recent sessions |
| `~/.cache/p0ssess/route-table.json` | the signed route table last fetched (checked once a day), with its tag; the binary carries one too |
| `~/.cache/p0ssess/engine-offline` | present while the last call to the engine service failed |
| `~/.local/state/p0ssess/install-token` | a random token, made once, that the engine service rate-limits by; it identifies nothing |
| `~/.cache/p0ssess/running/` | one file per running delegation, for the status line |
| `~/.cache/p0ssess/bin/<version>/` | the binary the plugin's launcher built or fetched |
| `~/.config/p0ssess/bin/p0ssess` | the copy the wrapped status line runs (`p0ssess setup statusline`) |
| `~/.config/p0ssess/setup.json`, `backups/` | what `p0ssess setup` changed in Claude Code's settings, and the file as it was before each change |

XDG variables are honored. With `P0SSESS_HOME` set, config lives there and cache and state go in its `cache/` and `state/`.

### budget.toml

```toml
monthly_usd = 10.0        # OpenRouter cash cap
per_task_usd = 0.02       # auto-approve limit per run
daily_usd = 0             # 0 paces the cap; a number fixes it; -1 turns it off
save_claude = false       # switch the λ curve to "eager"
plan = "auto"             # auto, api or subscription
usage_credits = false     # subscription usage credits on

[models]
allow = []                # empty allows all; "google/*", "deepseek" and full ids work
deny = ["x-ai"]

[providers]
allow = []
deny = []

[privacy]
mode = "no-training"      # or "zdr"
allow_safety_retention = false

[sandbox]
shell_internet = []       # project directories whose sandboxed shell commands may reach the internet

[ask]
first_use = false         # ask before the first run on a model you have not used
code_edits = false        # ask before an agent run in edit mode (its diff is reviewed either way)
```

`[ask]` is new in 0.5.0. A file without it keeps both off; nothing is rewritten until you change a setting.

`sandbox.shell_internet` is off for every project not listed. It lives only here, in your own file: a repository's `.p0ssess/rules.toml` cannot turn it on. Set it with `settings_set` keys `sandbox.shell_internet.add` and `sandbox.shell_internet.remove` (one project directory each; symlinks are resolved), or `sandbox.shell_internet` to replace the list. A listed directory covers the repositories inside it. [RELAY.md](RELAY.md#shell-internet) describes what it allows.

Change any of it with `p0ssess engine set KEY VALUE`, the `settings_set` tool, the local page, or by asking Claude in plain language.

### engine.toml

Every key is optional; anything missing keeps its default.

```toml
candidates = "evidence"     # evidence, catalog or all
subagent_model = "anthropic/claude-haiku-5.5"
default_lead = "anthropic/claude-sonnet-5.5"
overhead_usd = 0.05
mechanical_retries = 2
cash_headroom = 1.5

[lambda]
preset = "balanced"         # list, gentle, balanced, eager or custom
floor = 0.15                # custom only
exponent = 2.0              # custom only
e_min = 0.15
beta = 1.0
stale_minutes = 120

[harm]
s1 = 0.01
s2 = 0.10
s3 = 1.0
s4 = 10.0

[ceilings]
s1 = 0.80
s2 = 0.65
s3 = 0.40
s4 = 0.20

[escape]
none = 1.0
mechanical = 0.1
spot = 0.5
full = 0.2

[statusline]
command = ""                # your own status line command
mark = "ᗣ"                  # the ghost; "" hides it
width = 0                   # used when Claude Code does not set COLUMNS (0 means 120)
color = true
```

### rules.toml

Rules are structured. Claude turns plain language into one (`rules_add` or `approvals_add` with `dry_run`), shows the one line it renders, and saves it after the user confirms. The engine never creates a rule itself.

```toml
[[rule]]
id = "r-1a2b3c4d"
kind = "forbid"                       # forbid, allow, only, mix, approve, ask, budget
target = "model"                      # model, claude_agents, opencode, delegation
models = ["x-ai/grok-4.20"]
created = 2026-11-08T21:00:00Z
note = "stop using grok"

[[rule]]
id = "r-5e6f7a8b"
kind = "approve"
kinds = ["media"]                     # or use_cases = ["video-understand"]
max_per_run_usd = 0.20
max_total_usd = 5.0
period = "day"                        # "day" or "total" (since created)
created = 2026-11-08T21:00:00Z

[[rule]]
id = "r-9c0d1e2f"
kind = "mix"
models = ["deepseek/deepseek-v4.1-flash", "z-ai/glm-5.3-flash"]
weights = [1, 1]
kinds = ["agent"]
expires = 2026-11-09T09:00:00Z
created = 2026-11-08T21:00:00Z
```

| Plain language | Rule | One line |
|---|---|---|
| "no Claude agents for this" | `forbid`, `claude_agents`, session | never use Claude subagents (session) |
| "only use gemini-3.8-flash for video" | `only`, models, `kinds = ["media"]` | only use gemini-3.8-flash for media work |
| "no OpenCode" | `forbid`, `opencode` | never use the OpenCode agent path |
| "keep this repo on Claude" | `forbid`, `delegation`, project | keep work on Claude (project) |
| "auto-approve 10-minute video edits up to $0.20 each" | `approve`, `use_cases`, `max_per_run_usd = 0.2` | auto-approve for video-understand up to $0.20 each |
| "always ask before using grok" | `ask`, models | ask first for all delegations on grok-4.20 |
| "raise the per-task limit to a dime for this session" | `budget`, `per_task_usd = 0.10`, session | set the per-task limit to $0.10 (session) |
| "let Sonnet via OpenRouter do prod config with review" | `allow`, models, `max_stakes = 4` | allow claude-sonnet-5.5 up to stakes 4 |

Kinds: `media` (reads audio, video, images or documents; also covers `generate`), `generate` (makes images, audio, speech or video), `agent`, `review`, `code`, `text`.

Precedence: a forbid at any scope beats any allow. Session and global rules are yours and have full power; for rules that pick one value (`only`, `mix`, budget overrides) the session beats global, and the newest wins within a scope. Project rules only tighten, because `.p0ssess/rules.toml` arrives with whatever repository you clone:

- kept: `forbid` (any target); `ask`; `only` and `mix`, which narrow your own choice (a model must pass yours and the project's) and never bring in a model the use case does not name; `budget` overrides, applied only when lower than your limit (a project `daily_usd` must be above 0).
- ignored and reported once per session ("project rules tried to loosen …; ignored — add it yourself if you want it"): `allow` (including `max_stakes`), `approve`, and any budget override that would raise, pace or turn off a limit. `rules_add` refuses to save them at project scope.

Every rule is validated when it is read; an invalid one is ignored and reported. Budget overrides cannot be limited to use cases or kinds. A call that does not name its use case meets every `forbid`, `only` and `mix` rule whatever it is limited to, and only approvals that cover all work. Expired rules stay in the file, are shown as expired, and do nothing.

## The ledger

`history.jsonl` holds these kinds of line, and never prompt content:

- **offer**: written by `recommend` from the MCP server (or `--record`): offer id, use case, kind, family, stakes, project, session, the top options with total, cash, p_fail and approval status, λ and the preset.
- **outcome**: written by `history_record_outcome` when Claude did not use a result (a finished run nobody reports on counts as `accepted`; the ledger infers that outcome on reading and never writes it): the model, `accepted` (the result was used), `redo` (Claude had to redo it), `overridden` (another route ran instead; `chosen` says which) or `declined` (the user said no), the cash spent, and a reason of at most 160 characters. With an offer id, use case, stakes and the standing approval are filled in from the offer.

From it the engine reads spend (today, this month), first uses, standing-approval use, per-model outcomes for p_fail, and **tendencies** once a model and kind have three or more outcomes: "accepts gemini-3.8-flash for media 9/9", "declines deepseek-v4.1-flash for agent work 4/5", "redoes glm-5.3-flash's code 2/3". **Suggestions** are rules it proposes and never saves: a standing approval after five approved runs of a use case above the per-task limit (at the 90th percentile cost plus 25%), and a forbid rule after repeated declines.

- **run**: written by the relay and the direct path when a delegated call ends: run id (a relay `session_id/turn`, or `d-…` for a direct call), route, model, the measured cost and, for bring-your-own-key calls, the part billed upstream. **Spend is the sum of these runs.** Both paths take OpenRouter's own figure for every call, `usage.cost` plus, for bring-your-own-key calls, `upstream_inference_cost`: the direct path reads it from the answer, the relay from the key proxy's per-call meter (the laptop's cost guard for the cloud sandbox). A relay call whose cost cannot be read is priced from its tokens at the route table's price, else counted at the run's cap; OpenCode's session cost is used only for runs no meter saw. In testing both matched the OpenRouter key's own usage to within $0.000001.

- **reserve**, **release**, **settle**: before a direct call or relay run sends anything, its maximum cost is reserved; the run that ends it (or a release, when nothing was sent) closes the reservation. A reservation still open after its hold (the call's maximum runtime: two hours for a direct call, `max_runtime_s` plus 15 minutes for a relay run) counts as spent at its maximum, because the process that held it ended without measuring the call. A run whose cost was not reported is recorded at the reserved maximum with `estimated: true`; a later `settle` line replaces it with the cost OpenRouter reports for its generation ids. Spend, the daily and monthly limits and standing-approval use all count open reservations.

An outcome takes its cash from the run it names (`run_id`), or from the newest unclaimed run of the same model in the last three hours. An outcome that reports cash for a run p0ssess did not measure becomes a `reported` run, so nothing counts twice. Standing-approval use is counted from the runs and reservations the tools tagged with the approval when they authorized the call, not from outcomes Claude chooses to report.

## Enforcement

`recommend` is advice; the tools that spend enforce. Before the `direct` tool or a relay `run` sends anything, it applies, under a lock on the ledger (a file lock, so other p0ssess processes wait too):

1. `.p0ssess-private` in the working directory or above it: refused, even for a prompt-only call.
2. The budget, rules and ledger must be readable and valid: otherwise refused (fail closed).
3. Forbid rules (delegation, the model, OpenCode for the agent path), `only`/`mix`/project restrictions, and the model and provider allow and deny lists.
4. Stakes: the caller's rating, else the use case's catalog stakes, else 3. Stakes 4 is refused for a non-Claude model unless an `allow` rule names it with `max_stakes = 4`, and even then it needs a yes for the run (step 8).
5. Price: a model with no known price runs only with an explicit `max_cost_usd` above 0, which is reserved and counted in full. A zero price counts as free only for a `:free` variant or a text model the route table lists at zero; a zero from an endpoint or a media model billed by the image, second or character means unknown. `recommend` leaves such models out ("price unknown") rather than ranking them as cheap.
6. Privacy: in `zdr` mode a model whose endpoints are known and include no zero-retention one is refused, and the call goes out with `provider.zdr = true` (the direct client sets it; the relay's key proxy adds it to every request).
7. The ceiling: `max_cost_usd` when the caller set one; otherwise the direct call's own estimate (full output allowance at the highest endpoint price, also sent to OpenRouter as `max_price`), or for a relay run `max_run_cost_usd` from `config.json` ($0.50). A default ceiling is lowered to the room left under the monthly cap and the daily limit, when the estimate still fits; a caller's own is never lowered.
8. The monthly cap and the daily soft limit, checked against the ceiling and counting open reservations. The daily limit is lifted only by a session-scope `daily_usd` override.
9. Whether to ask, from the **estimate** (the direct call's estimate; for a relay run, a session of the use case's catalog turns, or 10, at list price): above the per-task limit, stakes 4, an `ask` rule, or the opt-in `ask.first_use` and `ask.code_edits`. A standing approval whose per-run, count and total limits cover the estimate answers it, and then its per-run limit also caps a default ceiling. Stakes 4 and `ask` rules take only a session approval. Otherwise the result is "needs approval", with the exact `approvals_add` arguments (scope session, one run, up to the ceiling) that record the user's yes; Claude Code's permission prompt for that tool is the confirmation. Keep `rules_add`, `approvals_add` and `settings_set` out of your allow list; `p0ssess setup show` warns when they are in it.

Then it reserves the ceiling. The direct client refuses a call whose estimate is above it; the relay interrupts a run whose cost reaches it.

The relay also keeps its own run log at `~/.local/state/p0ssess/relay/relay-runs.jsonl` (status, tokens and cost of each OpenCode run).

## Claude usage signal and the status line

`p0ssess statusline` reads Claude Code's status line JSON on stdin, saves `rate_limits` (both windows, with the time of the reading), the session id, directory and model to `usage.json`, runs your own status line command with the same input, and appends the segment:

```
idle, nothing spent today   (nothing)
idle, spent today           ᗣ p0ssess  $0.14 today
working                     ᗣ p0ssess  2 running  $0.14
working, wide terminal      ᗣ p0ssess  2 running  $0.14 today  mimo-v2.6-flash, gemini-3.1-pro
near the budget (80%+)      ᗣ p0ssess  $8.10 of $10 this month      (amber ghost)
```

It picks the longest form that fits the width (Claude Code's `COLUMNS`, else the setting) after your own line. Muted grey when idle, a little brighter while running, an amber mark near the budget, and a dimmer mark while the engine service cannot be reached; never red, never animated. `NO_COLOR` turns color off. A broken p0ssess setup still prints your line. Running jobs are files in `~/.cache/p0ssess/running/` (see `usage.StartJob` and `usage.FinishJob`); entries whose process is gone are cleared. The relay and the direct path create and remove these files, so the running count is real.

`p0ssess setup statusline` installs the wrapper: it copies the binary to `~/.config/p0ssess/bin/p0ssess` (the plugin's own path changes with each update; the MCP server refreshes this copy after an update), saves your current status line command in `statusline.command`, and points Claude Code's `statusLine` at the copy.

A reading older than two hours, or none (before the first response in a session, or on an API plan), counts as unknown.

## The prompt hint

`p0ssess hook prompt` is a `UserPromptSubmit` hook. Its line goes into Claude's context only (Claude Code shows hook context to Claude, not in the transcript), and only for: a video (a file, a link, or words like "watch this video"), audio (a file, "listen to", "transcribe the call", a voice memo), a request to make an image; a Claude window at 80% used or projected past 90%; bulk one-shot work (summarize, translate, classify, caption or extract over many items, or a pasted prompt over 40,000 characters); or, for reviews, a route the user reliably accepts. Otherwise it prints nothing. For media the line tells Claude to act now: one `direct` call with the file and the use case, no model, no `recommend`, no confirmation, and to present any generated file. When the OpenRouter key file is missing it says so instead. It reads only local files (it checks that the key file exists, never its contents), takes a few milliseconds, and stays silent in `.p0ssess-private` repositories.

## MCP tools

`p0ssess mcp` is the plugin's stdio MCP server, named `p0ssess`. Next to the relay tools ([RELAY.md](RELAY.md)) and the `direct` tool ([MEDIA.md](MEDIA.md)) it carries:

| Tool | What it does |
|---|---|
| `recommend` | the ranked list for a use case or profile; records the offer. By default one line per option (route, model, total, estimated cash, p_fail, why it would ask); `detail: true` returns the full breakdown. Each paid option's `estimate_usd` is what the approval check uses |
| `budget_status` | spend against limits, plan, λ and pressure, running jobs, file locations |
| `settings_set` | change one setting (`monthly_usd`, `per_task_usd`, `daily_usd`, `save_claude`, `plan`, `usage_credits`, `privacy.mode`, `privacy.allow_safety_retention`, `ask.first_use`, `ask.code_edits`, `outcomes.send`, `models.allow`, `models.deny`, `providers.allow`, `providers.deny`, `sandbox.shell_internet`, `sandbox.shell_internet.add`, `sandbox.shell_internet.remove`, `lambda.preset`, `statusline.command`, `statusline.mark`, `statusline.width`) |
| `rules_list`, `rules_add`, `rules_remove` | rules with scope; `dry_run` renders without saving |
| `approvals_add`, `approvals_list` | standing approvals and their use |
| `history_record_outcome` | redo, overridden or declined (or accepted, though a finished run counts as accepted on its own), with the `run_id` whose measured cost it takes |
| `tendencies` | learned patterns, suggestions and recent delegations |
| `shortcuts_suggest`, `shortcuts_create`, `shortcuts_remove` | named shortcut agents (below) |

The `direct` and `run` tools enforce every limit before they spend (see Enforcement); both take optional `stakes` and `use_case`.

### Shortcut agents

A shortcut is a named relay agent with a fixed model, such as `p0ssess-gemini-3-8-flash-media`, written to your Claude Code agent directory (`~/.claude/agents/`, or `$CLAUDE_CONFIG_DIR/agents/`). `shortcuts_suggest` proposes one for a model and kind of work you have accepted in at least 80% of three or more outcomes; nothing is written until Claude calls `shortcuts_create`, which Claude Code asks you to approve. A `direct` shortcut calls the direct tool; an `agent` shortcut calls the relay. There are at most 12, and `shortcuts_remove` deletes only files p0ssess made.

The server finds the session for session-scoped rules from `CLAUDE_CODE_SESSION_ID`, which Claude Code gives the MCP server (subagents' calls share it), else from the latest status line or hook reading for its project directory; tools also take `session_id` and `cwd`. A needs-approval answer names the session it resolved, so its `approvals_add` arguments work as given.

## The local page

`p0ssess me` serves a page on 127.0.0.1 at a random port, behind a random token in the path: one screen in the site's design language (black ground, violet hairline grid, Obel Mono numbers, IBM Plex Sans text, both fonts bundled). It shows spend today and this month against the cap, running jobs, Claude's windows and λ, recent runs, the limits as pills and every rule with a remove button; the settings form opens in a dialog. The ghost is the shipped `assets/ghost.svg`. It loads nothing from the network, sends a strict content security policy, refuses other Host headers, and requires the token in a header for every change. It stops after two hours (`--for`).

`p0ssess me --brief` prints the summary `/p0ssess:p0ssess-me` shows, three lines plus a `setup` line when something is missing:

```
ᗣ p0ssess 0.5.0   today $0.04   month $0.04 of $10.00 ▰▱▱▱▱▱▱▱▱▱   nothing running
limits   asks above $0.10 a task · $0.45 today (paced) · no-training · Claude 5-hour 1%, weekly 89%
change   tell Claude ("cap OpenRouter at $5", "always ask before grok") · page http://127.0.0.1:…/t/…/
```

It starts the page in the background when none is running (its address and end time are kept in `~/.cache/p0ssess/me.json`, mode 0600) and reuses a running one.

## Worked examples

API-key plan, no history:

```
$ p0ssess engine recommend --use-case video-understand --top 3
video-understand (#75, media) · stakes 1 · λ 1.00 (API-key plan: Claude at list price)
   route   model               total    cash     claude   risk     p_fail
1  direct  qwen3.8-omni-flash  $0.0926  $0.0223  $0.0550  $0.0153  0.19    ask: about $0.022, above the $0.02 auto-approve limit
2  direct  gemini-3.8-flash    $0.0957  $0.0275  $0.0550  $0.0131  0.15    ask: about $0.061, above the $0.02 auto-approve limit
3  direct  mimo-v2.6-flash     $0.1026  $0.0209  $0.0550  $0.0267  0.33    ask: about $0.026, above the $0.02 auto-approve limit
pick: handing this to qwen3.8-omni-flash: summarize a talk, meeting or lecture video
```

Claude cannot watch video, so there is no Claude row. p_fail comes from p0ssess's video check (Qwen3.8 Omni Flash scored 0.82 against Gemini 3.8 Flash's 0.88, so 0.19 with its single provider's uptime). Models without audio input are not offered, and neither is Gemini 3.1 Flash Lite, which described a sound that is not there in 5 of 6 answers. Video is priced at each model's measured token rate (91 per second on Gemini, 227 on Qwen3.8 Omni Flash; MiMo has no measured rate and gets 290). At stakes 1 a wrong pass-through answer is priced at a re-run plus $0.01 of harm, so price decides between Qwen and Gemini 3.8 Flash; at stakes 2 the larger harm puts Gemini 3.8 Flash first, by $0.0002. All three ask at the default $0.02 limit, because a 10-minute video costs more than that.

Feature work at stakes 3 on an API plan, then on a subscription with no reading yet:

```
code-implement-feature (#8, agent) · stakes 3 · λ 1.00
1  agent   gpt-6.1-sol        $0.5101  $0.2110  $0.1180  $0.1811  0.30    ask: about $0.422, above the $0.02 auto-approve limit
2  claude  claude-sonnet-5.5  $0.5395  $0.0000  $0.4000  $0.1395  0.23

code-implement-feature (#8, agent) · stakes 3 · λ 0.15
1  claude  claude-sonnet-5.5  $0.1204  $0.0000  $0.0600  $0.0604  0.23
2  agent   gpt-6.1-sol        $0.3072  $0.2110  $0.0177  $0.0785  0.30    ask: ...
```

Sol's row at λ = 1: $0.211 cash, $0.118 of Claude hand-off and full review, and risk 0.30 × ($0.40 redo + 0.2 × $1.00 harm) = $0.18. The DeepSeek and GLM flash agents never appear: their agent evidence puts p_fail at 0.54–0.60, above the stakes-3 ceiling. A one-turn coding question stays on Claude at any λ, because the hand-off alone costs more than the answer. Security-critical code offers only Claude.

## Known limits

- The presets are stances, not measurements. The ledger records λ, the preset and every outcome so they can be calibrated; the opt-in evaluation set in DESIGN.md does not exist yet.
- Audio, speech, embeddings and reranking have no clean benchmark. Their p_fail is the catalog's ordering until outcomes accumulate. Video understanding rests on p0ssess's own check: three synthetic clips, two runs per model, scored by hand. It is small and its clips are 8 to 15 seconds long. Speech-to-text and text-to-speech prices are read as per second and per character, which OpenRouter does not confirm; image and music prices are scaled from the catalog's estimates.
- Video token rates are measured only for the five models whose prompt grew with clip length in the video check, on 8- to 15-second clips at 640×360. Gemma 4 and MiMo (a fixed frame sample on one provider) and every unmeasured model are priced at 290 per second. Audio, image and page rates are still one figure for every model.
- The invented-sound rule is a hard cut on six answers per model, counted by hand. It does not apply to picture-only video jobs (`video-bug-repro`, `video-structure-scan`), where Gemini 3.1 Flash Lite is still offered.
- Rerank models show 0/0 prices and are never offered until a price appears.
- Spend is measured per run. A run counts as accepted unless Claude reports otherwise, so a redo Claude forgets to report makes the model look better than it is.
- The classifier is word overlap; an explicit use-case id is always better.
