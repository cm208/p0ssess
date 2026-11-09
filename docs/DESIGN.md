# p0ssess: design

p0ssess lets Claude Code hand work to other models through OpenRouter while Claude stays in charge: it plans, routes, reviews, and answers.
Its core is the **Predictive Model Engine (PME)**, which decides when another model is the better choice.

Status: design agreed, research next. Nothing here is built yet.

## Principles

- **Every model is a candidate, Claude included.** No preference for Claude; it is chosen when the numbers say so.
- **Quiet.** Routing is announced in one line ("handing this to mimo-v2.6-flash to summarize the logs") and otherwise stays out of the way.
- **Claude reviews what comes back.** Nothing a delegated model writes lands without the right level of review.
- **Local and private by default.** No telemetry. Learning data stays on the user's machine.
- **Only clean data.** Benchmarks and usage data are used only where their license clearly allows it, and every number carries its source and date.

## How a request is routed

1. A `UserPromptSubmit` hook adds a one-line hint **only when it matters**: an attached media file, a Claude usage limit that is close, a task that looks cheap and bulky, or a route the user's history favors. Otherwise it adds nothing.
2. Claude can ask the engine for a ranked list of options for the task at hand.
3. The engine scores every candidate with one formula: expected quality for this kind of task × fit (modalities, context size, tool use) ÷ cost.
   - A Claude call costs its API list price, or, on a subscription, the user's share of the usage limit expressed as a dollar-equivalent that rises as the limit nears.
4. **Claude makes the final call** and may override the ranking with a stated reason.
5. Claude routes on its own when the work needs a capability it lacks (video, audio, image generation) or is cheap and within the per-task budget. It asks first when the cost is above the user's threshold, when the work edits the user's code, or the first time a model is used.

## Two ways to run delegated work

- **Direct:** a single model call for one-shot jobs (transcribe, summarize, caption, translate, describe a video).
- **Agent:** a sandboxed OpenCode session for multi-step work on files.

The engine picks the path per use case. Audio and video always take the direct path.

## Agents

- One generic `p0ssess` relay agent. Claude spawns it like any subagent and names the model in the task.
- Named shortcuts (for example `gemini-video`) are created the way Claude would create any helper when usage shows they are worth having, capped at about 12.
- Relay agents run on a small, cheap Claude model whose only job is to pass the task through and return the answer verbatim.

## Review

| Delegated work | Review |
|---|---|
| Code edits | Always reviewed by Claude as a diff from a separate git worktree before anything lands |
| Factual or analytical answers | Claude spot-checks claims it can verify |
| Pure transformations (transcripts, summaries, captions) | Passed through, labeled with the model that made them |

When Claude has to redo a model's work, that counts against the model's score for that kind of task.

## Budget

`~/.config/p0ssess/budget.toml`:

- monthly OpenRouter cap (default $10)
- per-task auto-approve limit (default $0.02); above it, Claude asks unless a standing approval covers the task (see Rules)
- daily soft limit
- optional "save Claude" mode that lowers the bar for routing as Claude limits near
- model and provider allow and deny lists

`/p0ssess-me` walks through these settings in the terminal and opens a local page (served on `localhost`, works offline) showing spend against each limit, learned tendencies, recent delegations, and editable settings.

## Rules and standing approvals

Users set **rules** that gate what the engine may do, either in `/p0ssess-me` or in plain language ("no Claude agents for this", "only use gemini-3.8-flash for video", "mix deepseek and glm for bulk edits", "no OpenCode", "stop using model X").
Claude turns a plain-language request into a rule, shows the rule it wrote in one line, and saves it once the user confirms.

Rule kinds:

- **Who may run work:** allow or forbid Claude agents, a specific model, a set of models (a mix, with optional weights), or the OpenCode agent path.
- **Standing approvals:** pre-approve spend for a kind of task up to a per-run price, with an optional count or total ("auto-approve 10-minute video edits up to $0.20 each", "up to $5 today"). Within a standing approval, runs go through without asking and are announced in one line as usual.
- **Budget overrides:** raise or lower the per-task limit, daily limit, or monthly cap, for the session or permanently.

Scope: each rule applies to the **session**, a **project** (`.p0ssess/rules.toml` in the repository), or **globally** (`~/.config/p0ssess/rules.toml`). The narrowest scope wins; a rule that forbids something always beats one that allows it. Rules can expire ("for the next hour", "until the end of this project").

Rules are never inferred silently: the engine may *suggest* one from the user's history ("you've approved 8 video edits at about $0.15; make that a standing approval?"), but only the user creates it. `/p0ssess-me` lists every active rule with its scope and lets the user edit or remove it.

## Learning

A local history file records each delegation: kind of task, model offered and chosen, cost, whether the user accepted, overrode, or declined, and whether Claude had to redo the work.
From it the engine learns tendencies (for example "always accepts Gemini for media", "declines cheap models for Swift edits") and adjusts its offers.
The user's Claude Code memory files are read for stated preferences, never written.
An opt-in evaluation set (a few real tasks per family of use cases, run on a small capped budget) measures routing quality; aggregate results are published.

## Claude usage signal

Claude Code reports 5-hour and weekly usage to status line scripts on Pro and Max plans.
p0ssess wraps the user's status line, saves those readings with their timestamp to a local file, and the engine and hook read that file.
With no reading (before the first message of a session, or on a pay-as-you-go plan) the signal is "unknown" and only the budget settings apply.
p0ssess never reads, stores, or relays the user's Claude credentials.

## Status line

A quiet segment after the user's own status line, using the ghost mark and the `p0ssess` wordmark:

```
idle, nothing spent today   (nothing shown)
idle, spent today           ghost  p0ssess  $0.14 today
working                     ghost  p0ssess  2 running  $0.14
working, wide terminal      ghost  p0ssess  2 running  $0.14 today  mimo-v2.6-flash, gemini-3.1-pro
near the budget (80%+)      ghost  p0ssess  $8.10 of $10 this month      (amber ghost)
```

Muted when idle, slightly brighter while running, amber near the budget. Never red, never animated.

## Privacy

- Only providers that do not train on prompts are used by default; the user can loosen this.
- A `.p0ssess-private` file in a repository blocks all delegation from it.
- Files that look like secrets (`.env`, keys) are never attached.

## Sandbox for the agent path

OpenCode runs in a rootless container: only the repository (or a per-task git worktree) is mounted, no credentials or SSH keys are present, internet access is allowed but local networks and VPN ranges are blocked, and hard deny rules cover privilege escalation, remote shells, pushes, container and service control, and edits to `.git`, `.env`, and `.ssh`.
Questions OpenCode would ask the user are rejected on read-only tasks and returned to Claude on edit tasks; "always allow" is never answered.

## Data sources

Used, with attribution and the date of each value:

- OpenRouter public model list: prices, context, modalities, supported parameters, uptime
- OpenRouter Data API (CC BY 4.0): model rankings, top apps by category, task-type shares, OpenRouter's own evaluations; derived results only, never a mirror
- Epoch AI benchmarks that Epoch runs itself (CC BY)
- LMArena's published leaderboard dataset (CC BY 4.0)
- Aider's leaderboard data (Apache-2.0)
- models.dev (MIT) as a cross-check

Not used: any source whose terms restrict republishing or commercial use, or whose license is unclear.

Live data is refreshed on the user's machine (cached about a day). A weekly job in this repository rebuilds the registry with its own zero-credit key and opens a pull request for a human to merge.

## Research deliverables

1. A cited report: how OpenRouter is used, which apps and tools drive the most tokens, the model and benchmark landscape.
2. `catalog/usecases.yaml`: at least 100 distinct, evidence-backed use cases, about half grounded in OpenRouter usage data and half in Claude Code gaps. Each entry: description and evidence, recommended model plus alternatives (Claude included), expected cost per run, budget sensitivity, and whether Claude verifies the output.
3. `registry/`: a first snapshot of models with prices, capabilities, and benchmark scores, each value with its source and date.
