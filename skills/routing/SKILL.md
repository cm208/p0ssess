---
name: routing
description: How to route work with p0ssess, which hands work to other models through OpenRouter while Claude stays in charge. Use when the user names a non-Claude model ("use deepseek flash for ..."), attaches or mentions video, audio or image-generation work, asks for bulk one-shot work (summarize, translate, caption or extract over many items), mentions the OpenRouter budget, spend, rules or standing approvals, or when a p0ssess hint appears in context.
---

# Routing with p0ssess

p0ssess gives you three things: the **engine** (which route is cheapest per accepted result), the **direct** tool (one OpenRouter call with local files) and the **relay** subagent (a sandboxed OpenCode session for multi-step work on files). You make the final call and you review what comes back. All tools are on the `p0ssess` MCP server (`mcp__plugin_p0ssess_p0ssess__<tool>`).

## When to ask the engine

Call `recommend` before:
- work you cannot do yourself: understanding video or audio, transcription, image, audio or video generation;
- bulk one-shot text work (many files or items to summarize, translate, classify, caption or extract);
- long agent work where a cheaper model might do (test drafts, migrations with tests that decide acceptance).

Do not call it for ordinary coding, questions, or small edits: they stay with you. Pass `use_case` when you know the catalog id (`p0ssess engine usecases` lists them), else a `profile` with a short description of the job (never the user's content), its input and output modalities and its size (`audio_seconds`, `video_seconds`, `items`, `in_tokens`).

When the user names a model ("use deepseek flash for the test stubs"), that is the route: skip `recommend` unless cost is unclear, and still announce it.

## Stakes

Rate every task 1 to 4 yourself and pass it as `stakes`:

| Stakes | Typical work |
|---|---|
| 1 trivial | renames, log summaries, captions; easy to undo, errors are obvious |
| 2 routine | test drafts, transcripts, bulk edits you review |
| 3 important | features that ship, security write-ups, migration plans |
| 4 critical | auth, firewalls, infrastructure, production, money, anything sent to other people |

Signals: how reversible it is, blast radius, sensitive areas, whether the result ships or reaches people, how cheaply you can check it, and the user's own words ("quick draft", "this matters"). Say the rating only when it changes the routing ("important: keeping this on Claude"). If the user corrects it, use theirs. Claude subagents are Claude and may take stakes-4 work; other models need an explicit `allow` rule at stakes 4, and you review all of it.

## Announce in one line

Before delegating, say one line and nothing more, using the `announce` field: "handing this to gemini-3.8-flash: describe the screen recording". When you keep work, say nothing unless the user asked about routing.

## Approval

- An option with `needs_approval: true` needs a yes from the user first. Ask in one line with the cost: "This is about $0.06 on gemini-3.8-flash (first use of this model). Go ahead?"
- An option covered by a standing approval (`standing_approval` set) runs without asking. Still announce it.
- Pass the option's `max_cost_usd` as the direct tool's `max_cost_usd`: it is the ceiling already authorized (the standing approval's per-run limit, or the per-task limit). When the user approved a stated cost instead, pass that amount. Never more.

## Running it

- **Media and one-shot jobs:** call `direct` with the model, a prompt, absolute file paths, `max_cost_usd` (see Approval), and the `offer_id` and `use_case` from `recommend`. The direct tool estimates conservatively (full output allowance at the highest endpoint price) and refuses a call whose estimate is above the ceiling; if that happens, ask the user before raising it. Videos over the size or length limit come back with an error naming `video_prep` options (`downscale`, `keyframes`, `segment`); pick one and say which.
- **Multi-step work on files:** start the relay subagent with the Agent tool, `subagent_type: "p0ssess:p0ssess"`, prompt `"<model>: <task>"`, adding "edit mode" when it should change files. It runs in the background; continue the same session with SendMessage. Edits come back as a diff from a separate git worktree; nothing is merged.
- The agent path needs Linux with rootless podman. If a tool says it is unavailable, use `direct` for one-shot work or keep the work.

## Review by type

| Delegated work | Your review |
|---|---|
| Code edits | Always: read the diff in full, run the tests, then `git apply <diff_path>` only if it is right |
| Factual or analytical answers | Spot-check the claims you can verify |
| Pure transformations (transcripts, summaries, captions) | Pass through, labeled with the model that made it |

For **stakes 3-4 diffs**, `recommend` may return `second_opinion`: two models from different families that can review the diff, with their cost. It is optional and additive: offer it in one line when it is within budget ("A second opinion from gpt-6.1-sol and gemini-4-argon would cost about $0.14; want it?"). If the user agrees, send `diff_path` to each with `direct`, then reconcile both reviews with your own. You and the user decide; the reviews never replace yours.

## After every delegation

Call `history_record_outcome` with the `offer_id`, the model, the `run_id` (the relay's `session_id`, or the `run:` line of a direct result) and one outcome: `accepted` (you used it), `redo` (you had to redo it), `overridden` (another route ran) or `declined` (the user said no). The cost comes from the measured run. Never put prompt content in `reason`.

## Rules, budgets and standing approvals

When the user says something like "never use grok", "only use gemini-3.8-flash for video", "keep this repo on Claude", "auto-approve video summaries up to $0.20 each" or "raise the per-task limit to a dime for this session":
1. Call `rules_add` (or `approvals_add`) with `dry_run: true`, choosing the scope they meant: session, project or global.
2. Show the one-line `text` it returns and ask to save it.
3. Save it (same call without `dry_run`) only after they confirm.

Settings ("cap OpenRouter at $5 this month") go through `settings_set` after a one-line confirmation. `tendencies` may list suggestions ("you've approved 8 video summaries at about $0.15; make that a standing approval?"); offer them, never save them on your own.

## Shortcut agents

`shortcuts_suggest` lists named relay agents (for example `p0ssess-gemini-video`) for routes the user keeps accepting. When one is suggested, offer it in one line; call `shortcuts_create` only after the user agrees. There are at most 12.

## Privacy

Never attach secrets (`.env`, keys, credentials); the tools refuse them too. A `.p0ssess-private` file in a repository blocks all delegation from it. Every call tells OpenRouter not to use providers that train on prompts.
