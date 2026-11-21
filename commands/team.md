---
description: p0ssess Team from chat: invite someone by email, seats and pending invites, spend by member and model, shared rules and approvals (use when the user asks to invite someone to their team, or about team seats, spend or rules)
argument-hint: "[invite EMAIL | status | spend [YYYY-MM] | rules [add ... | remove ID]]"
allowed-tools: mcp__plugin_p0ssess_p0ssess__team_status, mcp__plugin_p0ssess_p0ssess__team_spend, mcp__plugin_p0ssess_p0ssess__team_rules, mcp__plugin_p0ssess_p0ssess__team_invite
---

The user ran `/p0ssess:team $ARGUMENTS`. Call the matching p0ssess tool once:

- nothing, or `status`: `team_status`.
- `invite EMAIL`: `team_invite` with that email. Typing the command is the user's go-ahead; do not ask again. `invite link` (no address): `team_invite` without email.
- `spend`, optionally with a month `YYYY-MM`: `team_spend`.
- `rules`: `team_rules`.
- `rules add ...` (in words, e.g. "never use grok", "ask before image work", "cap tasks at 50 cents"): turn it into one shared rule and call `team_rules_add` with `dry_run` true, show its one line, and save (the same call without `dry_run`) only after the user says yes. An ask rule is a team approval: every seat asks before that work. A budget rule's field is `per_task_usd`, `daily_usd` or `monthly_usd`.
- `rules remove ID`: `team_rules_remove`.
- Anything else: say in one line what this command does.

Show the tool's text exactly as it is, in one code block, with nothing before or after it. If the tool fails, give its message in one plain line. Never print invite links unless the tool returned one, and never ask for or show tokens.
