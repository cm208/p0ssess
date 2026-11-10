---
description: p0ssess at a glance (spend, limits, running jobs) and its local page
allowed-tools: Bash(p0ssess me:*), Bash(p0ssess setup:*), mcp__plugin_p0ssess_p0ssess__settings_set
---

!`p0ssess me --brief`

Show the lines above to the user exactly as they are, in one code block, with nothing before or after it. If they are not a p0ssess summary (the command failed), run `p0ssess me --brief` with Bash and show its output the same way; if that fails too, say in one line that the p0ssess plugin is not working and to check `/mcp`.

Only when the summary has a `setup` line, add one short sentence offering its first item, and run that command only after the user says yes:
- `p0ssess setup allow` adds allow rules for the plugin's tools to the user's Claude Code settings, so delegation runs without permission prompts. Rule, approval and settings changes keep asking: those prompts are how the user approves spending.
- `p0ssess setup statusline` wraps the user's status line and adds the p0ssess segment; it also lets p0ssess see Claude's usage windows.
- A missing OpenRouter key: say where it goes; never read it or ask for it in chat.

Each setup change is backed up and `p0ssess setup undo` reverts it. Later changes work in plain words ("cap OpenRouter at $5", "auto-approve video up to 20 cents", "always ask before using grok"): turn them into a setting or rule, show the one line, save after the user confirms.
