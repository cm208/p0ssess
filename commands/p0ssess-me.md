---
description: Set up p0ssess and open its local page (budget, Claude plan, privacy, rules, status line)
allowed-tools: mcp__plugin_p0ssess_p0ssess__budget_status, mcp__plugin_p0ssess_p0ssess__settings_set, mcp__plugin_p0ssess_p0ssess__rules_list, mcp__plugin_p0ssess_p0ssess__tendencies, mcp__plugin_p0ssess_p0ssess__shortcuts_suggest, Bash(p0ssess setup show), Bash(p0ssess me:*)
---

Walk the user through p0ssess's settings in the terminal, then open its local page. Keep every message short. Ask one thing at a time and show the current value with each question, so moving on with no change is easy.

1. Call `budget_status`. If the tool is missing, say the p0ssess MCP server is not connected (check `/mcp`; the first start builds the binary and can take a minute) and stop.
2. Say in two lines what you found: spend this month against the cap, the plan, the privacy mode, and whether a Claude usage reading exists. If the OpenRouter key file is missing, say where it goes (`~/.config/p0ssess/secrets/openrouter_key`, mode 0600, or `P0SSESS_OPENROUTER_KEY_FILE`) and that you will never read it aloud.
3. Ask, in this order, and save each answer with `settings_set` as soon as the user gives it:
   - **Plan**: `subscription` (Pro, Max or Ultra), `api` (pay per token), or leave `auto` (API pricing until a status line reading shows a subscription). On a subscription, ask whether usage credits are on (`usage_credits`).
   - **Monthly OpenRouter cap** (`monthly_usd`, default $10) and **auto-approve per task** (`per_task_usd`, default $0.02). Media jobs usually cost more than $0.02, so they ask first unless a standing approval covers them.
   - **Daily soft limit** (`daily_usd`): 0 paces the monthly cap over the days left, a number fixes it, -1 turns it off.
   - **Privacy** (`privacy.mode`): `no-training` (default; every call tells OpenRouter not to use providers that train on prompts) or `zdr` (zero-retention endpoints only; this removes some media models).
   - **Pressure curve** (`lambda.preset`): `balanced` (default), `gentle`, `eager` or `list`. Offer `save_claude` as the quick switch for "route more while my limits are tight".
4. Run `p0ssess setup show` and read the result. Then, one at a time, offer each change it lists. Explain each in one or two lines, and run the command only after the user says yes:
   - **Allow the plugin's tools** (`p0ssess setup allow`). Background relay agents cannot ask for permission, so without this the agent path and shortcut agents fail. It adds allow rules for the relay tools, the direct tool, the engine's read-only tools and `history_record_outcome` (which only appends to the local ledger) to the user's Claude Code settings; rule, approval and settings changes and shortcut files still ask, and those prompts are the user's confirmation, so keep them out of the allow list.
   - **Status line segment** (`p0ssess setup statusline`). Plugins cannot install a status line, so p0ssess wraps the user's: their current command keeps running first and the p0ssess segment follows. It also lets the engine see Claude's usage windows.
   - **Background progress summaries**, only if the user asks about overhead (`p0ssess setup summaries`): it sets `CLAUDE_CODE_DISABLE_AGENT_PROGRESS_SUMMARIES=1`, an undocumented Claude Code switch that turns off one-line status summaries for all background agents.
   - **Sandbox image** (`p0ssess jail build`), only when `setup show` says it is not built and the user wants the agent path: it downloads OpenCode (checked against pinned SHA-256 digests) and builds a local podman image of about 1.1 GB. Run it in the background; it takes a few minutes.
   Each settings change is backed up first and `p0ssess setup undo` reverts all of them. Never change settings files any other way.
5. Call `rules_list`, `tendencies` and `shortcuts_suggest`. List active rules with their scope, one line each. Read out any suggestions and say the user can accept one by asking.
6. Start the page with `p0ssess me --open` in the background (it keeps running) and give the user the URL it prints. It listens on 127.0.0.1 only, makes no outside requests, and stops after two hours.

Any later change works in plain language too ("cap OpenRouter at $5 this month", "never use grok"): turn it into a setting or a rule, show the one-line rule, and save it only after the user confirms.
