---
description: Set up p0ssess and open its local page (budget, Claude plan, privacy, rules)
allowed-tools: mcp__p0ssess-engine__budget_status, mcp__p0ssess-engine__settings_set, mcp__p0ssess-engine__rules_list, mcp__p0ssess-engine__tendencies, Bash(p0ssess-engine status:*), Bash(p0ssess-engine me:*)
---

Walk the user through p0ssess's settings in the terminal, then open its local page. Keep every message short. Ask one thing at a time and show the current value with each question, so pressing on with no change is easy.

1. Call `budget_status`. If the tool is missing, say the engine's MCP server is not registered, show the `p0ssess-engine` entry from `examples/engine-mcp.json`, and stop.
2. Say in two lines what you found: spend this month against the cap, the plan, the privacy mode, and whether a Claude usage reading exists.
3. Ask, in this order, and save each answer with `settings_set` as soon as the user gives it:
   - **Plan**: `subscription` (Pro, Max or Ultra), `api` (pay per token), or leave `auto`. On a subscription, ask whether usage credits are on (`usage_credits`).
   - **Monthly OpenRouter cap** (`monthly_usd`, default $10) and **auto-approve per task** (`per_task_usd`, default $0.02). Mention that media jobs usually cost more than $0.02, so they ask first unless a standing approval covers them.
   - **Daily soft limit** (`daily_usd`): 0 paces the monthly cap over the days left, a number fixes it, -1 turns it off.
   - **Privacy** (`privacy.mode`): `no-training` (default; every call tells OpenRouter not to use providers that train on prompts) or `zdr` (zero-retention endpoints only; this removes Gemini and some media models).
   - **Pressure curve** (`lambda.preset`): `balanced` (default), `gentle` (Claude stays cheap until a window is nearly spent), `eager` (routes earlier), or `list` (Claude always at list price). Offer `save_claude` as the quick switch for "route more while my limits are tight".
4. Ask whether they want the status line segment. If yes, explain that p0ssess wraps their current status line: their existing command moves to `statusline.command` (save it with `settings_set`) and Claude Code's `statusLine.command` becomes `p0ssess-engine statusline`. Show the exact JSON from `examples/engine-settings.json` and change their settings file only after they say yes. Never touch it otherwise.
5. Ask whether they want the one-line prompt hint (the `UserPromptSubmit` hook). Same rule: show the snippet, edit only with consent.
6. Call `rules_list` and `tendencies`. List active rules with their scope in one line each. If there are suggestions, read them out and say they can accept one by asking.
7. Start the page with `p0ssess-engine me --open` in the background and give the user the URL it prints. It listens on 127.0.0.1 only and stops after two hours.

Any later change works in plain language too ("cap OpenRouter at $5 this month", "never use grok"); turn it into a setting or a rule, show the one-line rule, and save it only after the user confirms.
