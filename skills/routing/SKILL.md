---
name: routing
description: How to hand work to other models with p0ssess (OpenRouter) through its Haiku agent while Claude stays in charge. Use when the user wants a video watched, audio heard or transcribed, or an image made or edited; names a non-Claude model ("use deepseek flash for ..."); asks for bulk one-shot work (summarize, translate, caption or extract over many items); mentions the OpenRouter budget, spend, rules or approvals; when a p0ssess rule sends the work at hand to a model; or when a p0ssess hint appears in context.
---

# Routing with p0ssess

p0ssess is set up when the `p0ssess:p0ssess` agent exists. The user installed it so that "watch this video" just works, quietly.

## Every routed task goes to the p0ssess agent

Hand the request to the p0ssess agent with the Agent tool: `subagent_type: "p0ssess:p0ssess"`, and a one-sentence prompt with the user's request and absolute file paths (resolve `~` and relative paths first). Add "model <id>" only if the user named one, "edit mode" when files should change, "stakes N" when not 1-2, "max $X" only if the user named a ceiling. It runs on Haiku in the background, makes the p0ssess calls and returns only the result, so its tool traffic never fills this conversation.

Do not call `direct`, `run`, `wait`, `recommend` or the other routing tools yourself. A hook refuses them outside the agent unless the user explicitly asks you to call p0ssess in the main session.

Write nothing before or while it runs: no preamble, no plan, no "I've sent this to …", no "I'll tell you when it's back", no confirmation question. After the Agent call, end your turn without any text. When it returns:

- An answer: give the agent's answer as it came, tidied at most, never expanded, re-explained or hedged, followed by the credit in parentheses, for example "(gemini-3.8-flash, $0.0004)". No heading, no intro line, no closing offer.
- `files:`: see below.
- `error: needs approval …`: ask the user in one line with the cost. On a yes, call `approvals_add` yourself with exactly the arguments the error gives (its permission prompt is the user's confirmation), then send the agent "approved, run it again" with SendMessage. Never add an approval the user did not agree to.
- Any other `error:`: say it in one line.

## Generated files: present them

When the result has a `files:` line (JSON `[{path, kind}]`):

1. Print each absolute path on its own line.
2. If this session has a `SendUserFile` tool (a Remote Control client such as the Claude app is connected, or this is a cloud session), send the files with it: `display: "render"` for image, video and audio, `"attach"` for documents. One call for all of them.
3. Nothing else: no viewer, no `xdg-open`, no base64, no extra commands. A plain terminal shows the path; kitty and most terminals make it clickable.

Do not open a generated file yourself (an image costs this conversation its own context) unless the user asks you to check it.

## Work a rule sends to a model

An `only` or `mix` rule limited to a use case or kind ("always send unit-test writing to deepseek flash") is a decision the user or their team already made. When a p0ssess hint says a rule covers the request, hand it to the agent right away with the model and use case the hint names, plus "edit mode" when files should change. Do not price the route, weigh doing it yourself, or keep it on Claude. A team rule comes from the team's owner and cannot be loosened here. The same holds when `rules_list` shows such a rule for the work in hand and no hint fired. If the request is not actually that work, ignore the hint.

## Edits from an agent run

The agent returns the model's short report (each file it changed, the test command it ran and the counts), `diff: <path> (summary)` and `apply: <command>`, never the diff itself. The diff and your check of it are the safety net, so edit runs within the user's limits do not ask first. Check in proportion to the stakes:

- Stakes 1-2 (test drafts, bulk data files, translations, docs, mechanical edits): run the `apply` command, then the project's tests or the check the user asked for, once. If it passes and the summary matches the request, you are done. Do not re-read every file, redo the work, or run the same check twice. Open a file only when a check fails, the summary looks wrong (missing files, files nobody asked for, deletions) or the report says something was skipped.
- A data file the user asked for (JSON, CSV) at stakes 1-2: after applying, check it parses and has every item, and spot-check two or three entries against the source. Do not redo the batch.
- Stakes 3-4, or code that ships (features, migrations, auth, payments, infrastructure, anything security-relevant): read the whole diff before applying it and review it as you would your own change.

Continue a run with SendMessage to the same agent. A `permission:` line for ordinary work inside the run's copy (reading, writing there, running the project's tests) can be approved: SendMessage "approve once". Reject network access or writes outside the copy unless the user asked for them.

## Approval

The tools enforce every limit (rules, allow and deny lists, stakes, privacy, `.p0ssess-private`, per-task, daily and monthly limits). They ask only when the estimate is above the per-task auto-approve limit, the monthly cap would be passed, a rule says to ask, or the stakes are 4. A daily soft limit is lifted only by a session `daily_usd` override, and only when the user asks.

## Stakes

Rate the task 1-4 and put "stakes N" in the agent prompt when it is not 1-2:

| Stakes | Typical work |
|---|---|
| 1 trivial | log summaries, captions, describing a clip; easy to undo, errors are obvious |
| 2 routine | test drafts, transcripts, bulk edits you review |
| 3 important | features that ship, security write-ups, migration plans |
| 4 critical | auth, firewalls, infrastructure, production, money, anything sent to other people |

Stakes 4 stays on Claude unless the user has an `allow` rule with `max_stakes = 4` for the model, and then it always asks. Mention the rating only when it changes the routing ("important: keeping this on Claude").

## When to price a route

Ordinary coding, questions and small edits stay with you and need no tool. Ask the p0ssess agent to price a route ("which model for X, and what would it cost?") only when the user asks, or before bulk one-shot or long agent work where the model is not obvious.

## Outcomes

A finished run you used counts as accepted on its own. Call `history_record_outcome` only when it was not used: `redo` (you had to redo it), `declined` (the user said no) or `overridden` (another route ran), with the run id from the `via:` line. Never put prompt content in `reason`.

## Rules, budgets and standing approvals

Plain-language changes ("never use grok", "only use gemini-3.8-flash for video", "always ask before using gpt", "auto-approve video summaries up to 20 cents", "raise the per-task limit to a dime for this session"):

1. Call `rules_add` (kinds forbid, allow, only, mix, ask, budget) or `approvals_add` with `dry_run: true` and the scope they meant: session, project or global.
2. Show the one line it returns and ask to save it.
3. Save it (same call without `dry_run`) after they confirm.

Settings ("cap OpenRouter at $5 this month") go through `settings_set` after a one-line confirmation. `ask.first_use` and `ask.code_edits` turn back on asking before a model's first run or before edit runs. Suggestions from `tendencies` and `shortcuts_suggest` are offered in one line, never saved on your own.

## Privacy

Never attach secrets (`.env`, keys, credentials); the tools refuse them too. A `.p0ssess-private` file blocks all delegation from that repository. Every call tells OpenRouter not to use providers that train on prompts.
