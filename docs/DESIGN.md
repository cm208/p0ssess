# p0ssess: design

p0ssess lets Claude Code hand work to other models through OpenRouter while Claude stays in charge: it plans, routes, reviews, and answers.
Its core is the **Predictive Model Engine (PME)**, which decides when another model is the better choice.

Status: design agreed and researched ([RESEARCH.md](RESEARCH.md)); built as a Claude Code plugin. Where the build departs from this page, [ENGINE.md](ENGINE.md), [RELAY.md](RELAY.md) and [MEDIA.md](MEDIA.md) describe what shipped.

## Principles

- **Every model is a candidate, Claude included.** No preference for Claude; it is chosen when the numbers say so.
- **Quiet.** Routing is announced in one line ("handing this to mimo-v2.6-flash to summarize the logs") and otherwise stays out of the way.
- **Claude reviews what comes back.** Nothing a delegated model writes lands without the right level of review.
- **Local and private by default.** No telemetry. Learning data stays on the user's machine.
- **Only clean data.** Benchmarks and usage data are used only where their license clearly allows it, and every number carries its source and date.

## How a request is routed

1. A `UserPromptSubmit` hook adds a one-line hint **only when it matters**: an attached media file, a Claude usage limit that is close, a task that looks cheap and bulky, or a route the user's history favors. Otherwise it adds nothing.
2. Claude can ask the engine for a ranked list of options for the task at hand.
3. Claude rates the task's **stakes** (see below), and the engine scores every candidate that fits the task (modalities, context size, tool use) by its expected cost per **accepted** result:

   ```
   cost(model, task) = cash                          OpenRouter spend (0 for Claude)
                     + λ(pressure) × claude_price    Claude's cost; λ rises as the user's limit nears
                     + overhead + review             delegation and checking turns
                     + p_fail(model, task) × (redo + harm(stakes))
   ```

   - `p_fail` is the chance the result is not accepted, from benchmark evidence and the user's history.
   - `harm(stakes)` is the cost of a bad result beyond redoing it; small at stakes 1, steep at each level above.
   - Stakes also gates: a model qualifies only if its `p_fail` is below the ceiling for that level.
   - On API-key plans `λ` is 1 and `claude_price` is the list price. On subscriptions `λ(pressure)` follows the projected end-of-window usage (the worse of the 5-hour and weekly windows); its shape is a user preset.
   - The lowest total wins. Delegation that cannot save anything (overhead and review above the work's own cost) is never offered.
4. **Claude makes the final call** and may override the ranking with a stated reason.
5. Claude routes on its own when the work needs a capability it lacks (video, audio, image generation) or is cheap and within the per-task budget. It asks first when the cost is above the user's threshold, when the work edits the user's code, or the first time a model is used.

## Stakes

Claude rates each task on four levels from signals it can see: how reversible it is (read-only, an edit in a worktree, a production change), its blast radius, sensitive areas (auth, secrets, firewalls, infrastructure, payments), whether the result ships or reaches other people, how cheaply Claude can check it, the user's own words ("quick draft", "this matters"), and the user's rules.

| Stakes | Typical work |
|---|---|
| 1 Trivial | renames, log summaries, captions; easy to undo, errors are obvious |
| 2 Routine | test drafts, transcripts, bulk edits Claude reviews |
| 3 Important | features that ship, security write-ups, migration plans |
| 4 Critical | auth, firewall and infrastructure changes, production, money, anything sent to other people |

- The rating is shown only when it changes the routing ("important: keeping this on Claude"). The user can correct it in plain language, and corrections feed the history.
- Stakes 4 stays on Claude unless a global or session `allow` rule names the model with `max_stakes = 4`, and even then Claude reviews the result. The tools enforce it: a call that does not state its stakes takes the use case's catalog stakes, or 3.
- A cheap model plus a Claude check can take an important task when checking costs much less than doing.

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

These are enforced where money is spent, not only advised. Before the direct tool or a relay run sends anything, it applies the rules, the allow and deny lists, the stakes gate, privacy mode, `.p0ssess-private`, and the per-task, daily and monthly limits on its own, whatever `recommend` said, and fails closed when the budget, rules or ledger cannot be read. It then reserves the call's maximum cost in the ledger under a file lock, so concurrent calls cannot each pass the same headroom; the measured cost settles the reservation afterwards. A call above the per-task limit with no standing approval is refused as "needs approval" and Claude asks the user. A cost that is never reported is recorded at the reserved maximum, marked estimated, until OpenRouter reports it.

`/p0ssess-me` walks through these settings in the terminal and opens a local page (served on `localhost`, works offline) showing spend against each limit, learned tendencies, recent delegations, and editable settings.

## Rules and standing approvals

Every setting, rule, budget and stakes rating can be changed in plain language as well as in `/p0ssess-me`. Users set **rules** that gate what the engine may do, either in `/p0ssess-me` or in plain language ("no Claude agents for this", "only use gemini-3.8-flash for video", "mix deepseek and glm for bulk edits", "no OpenCode", "stop using model X").
Claude turns a plain-language request into a rule, shows the rule it wrote in one line, and saves it once the user confirms.

Rule kinds:

- **Who may run work:** allow or forbid Claude agents, a specific model, a set of models (a mix, with optional weights), or the OpenCode agent path.
- **Standing approvals:** pre-approve spend for a kind of task up to a per-run price, with an optional count or total ("auto-approve 10-minute video edits up to $0.20 each", "up to $5 today"). Within a standing approval, runs go through without asking and are announced in one line as usual.
- **Budget overrides:** raise or lower the per-task limit, daily limit, or monthly cap, for the session or permanently.

Scope: each rule applies to the **session**, a **project** (`.p0ssess/rules.toml` in the repository), or **globally** (`~/.config/p0ssess/rules.toml`). A rule that forbids something always beats one that allows it. Session and global rules are the user's own and have full power; between them the session wins. A project file arrives with whatever repository the user clones, so **project scope only tightens**: it may forbid models, providers, Claude agents, OpenCode or all delegation, narrow the user's `only`/`mix` choice, and lower the per-task, daily or monthly limit. Anything that would loosen (allow rules, standing approvals, lifting the stakes-4 gate, raising or turning off a limit) is ignored and reported once per session: "project rules tried to loosen …; ignored — add it yourself if you want it". Rules can expire ("for the next hour").

Rules are never inferred silently: the engine may *suggest* one from the user's history ("you've approved 8 video edits at about $0.15; make that a standing approval?"), but only the user creates it. `/p0ssess-me` lists every active rule with its scope and lets the user edit or remove it.

## Learning

A local history file records each delegation: kind of task, model offered and chosen, cost, whether the user accepted, overrode, or declined, and whether Claude had to redo the work.
From it the engine learns tendencies (for example "always accepts Gemini for media", "declines cheap models for Swift edits") and adjusts its offers.
Planned, not built: reading the user's Claude Code memory files for stated preferences (never writing them), and an opt-in evaluation set (a few real tasks per family of use cases, on a small capped budget) to measure routing quality and publish aggregate results.

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

## Background agent status summaries

Claude Code asks for a one-line status summary of each running background agent about every 30 seconds. Relay agents are long-running background agents, so these requests can eat into what routing saves.

- Planned, not built: measuring how many summary requests relay jobs cause and what they cost, next to the delegation itself.
- `p0ssess setup summaries` (offered by `/p0ssess-me`) sets Claude Code's own switch, `CLAUDE_CODE_DISABLE_AGENT_PROGRESS_SUMMARIES=1` (Claude Code 2.1.318 and later, currently undocumented), written to the `env` block of the user's settings only with their consent, labeled as undocumented, and reversible with one command. The trade-off is stated: it turns status lines off for all background agents, and after a context compaction Claude no longer sees progress lines for running agents.
- p0ssess never patches, preloads into, or otherwise modifies Claude Code at runtime.

## Privacy

- Only providers that do not train on prompts are used by default; the user can loosen this.
- A `.p0ssess-private` file in a repository blocks all delegation from it and from every directory below it, including prompt-only calls; the tools refuse, not only the advice.
- Zero-retention mode (`privacy.mode = "zdr"`) is enforced at the call: the direct tool sends `provider.zdr = true` and refuses models with no zero-retention endpoint, and the relay's key proxy adds `zdr = true` to every request.
- The ledger holds no prompt content, but the relay keeps each run's task and output transcript under `~/.local/state/p0ssess/relay/runs/<session>/`, and nothing prunes it yet.
- Files that look like secrets (`.env`, keys) are never attached.

## Sandbox for the agent path

OpenCode runs in a rootless container, and every relay session gets its own: created for the session's first run, kept while the session can still be continued, and removed when it ends or idles out, so no two sessions share a server, a database or a worktree. Only the repository (read-only) and the session's own git worktree are mounted, no credentials or SSH keys are present, the server may reach the internet but not local networks, VPN ranges or the host, and hard deny rules cover privilege escalation, remote shells, pushes, container and service control, and edits to `.git`, `.env`, and `.ssh`. Jails left by a crashed process are removed at the next start.
Every command the model runs goes through a shell wrapper that scrubs the environment and puts the command in fresh user, network, mount and pid namespaces, so it has no network at all (the API, the key proxy, the LAN and the internet are unreachable), cannot read the server's environment or `/state`, and — for a read-only task — has nothing writable. A user can turn on internet for shell commands per project, in their own settings only: the commands then reach public addresses on ports 80 and 443 through an egress proxy that refuses every local, private and VPN address, and nothing else changes. `formatter` and `lsp` are off so repository-controlled programs never run in the server. The full confinement is in `docs/RELAY.md`.
Questions OpenCode would ask the user are rejected on read-only tasks and returned to Claude on edit tasks; "always allow" is never answered. A reply or interruption the relay did not issue aborts the session as tampered.

## Data sources

Used, with attribution and the date of each value:

- OpenRouter public model list: prices, context, modalities, supported parameters, uptime
- OpenRouter Data API (CC BY 4.0): model rankings, top apps by category, task-type shares, OpenRouter's own evaluations; filtered to the referenced models and attributed, never a full mirror
- Epoch AI benchmarks that Epoch runs itself (CC BY)
- LMArena's published leaderboard dataset (CC BY 4.0)
- Aider's leaderboard data (Apache-2.0): checked, but no referenced model appears in it, so it contributes no values (the file ships as an empty header)
- models.dev (MIT) as a cross-check

Not used: any source whose terms restrict republishing or commercial use, or whose license is unclear.

Live data is refreshed on the user's machine (cached about a day). Planned, not built: a weekly job in this repository that rebuilds the registry with its own zero-credit key and opens a pull request for a human to merge; today `registry/scripts/` is run by hand.

## Research deliverables

1. A cited report: how OpenRouter is used, which apps and tools drive the most tokens, the model and benchmark landscape.
2. `catalog/usecases.yaml`: at least 100 distinct, evidence-backed use cases, about half grounded in OpenRouter usage data and half in Claude Code gaps. Each entry: description and evidence, recommended model plus alternatives (Claude included), expected cost per run, budget sensitivity, and whether Claude verifies the output.
3. `registry/`: a first snapshot of models with prices, capabilities, and benchmark scores, each value with its source and date.
