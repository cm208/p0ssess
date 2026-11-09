<p align="center"><img src="assets/ghost.svg" width="120" alt=""></p>
<h1 align="center">p0ssess</h1>

<p align="center">Claude Code hands work to other models through OpenRouter, and stays in charge.</p>

p0ssess is a Claude Code plugin. When a task needs something Claude cannot do (watch a video, hear a recording, make an image), or when a cheaper model would do just as well (summarizing a pile of logs, drafting tests that the test suite will judge), Claude hands that piece to another model, checks what comes back, and carries on. You see one line when it happens:

```
handing this to gemini-3.8-flash: describe the screen recording
```

Everything else stays with Claude.

## What it does

- **Routes by expected cost per accepted result.** A local engine scores every route that could do a task, Claude included: one OpenRouter call, a sandboxed agent session on another model, a smaller Claude subagent, or a local tool. It weighs the cash, the Claude usage it saves, how likely each model is to get it right (from public benchmarks and your own history), and how much a wrong answer would hurt.
- **Rates stakes.** Claude rates each task from trivial to critical. Critical work (auth, infrastructure, production, money) stays with Claude unless you say otherwise.
- **Reviews everything that matters.** Code edits come back as a diff from a separate git worktree and are never applied unreviewed. For important diffs Claude can offer an optional second opinion from two other model families, in addition to its own review.
- **Respects a budget.** A monthly cap, a per-task auto-approve limit and a daily pace, enforced by the tools that spend, not left to Claude's judgment. Each call's maximum is reserved before it is sent and settled to the measured cost after, so parallel calls cannot overshoot; a cost OpenRouter never reports counts at the reserved maximum.
- **Takes rules in plain language.** "Never use grok." "Only use gemini-3.8-flash for video." "Auto-approve video summaries up to 20 cents each today." Claude shows the rule it wrote in one line and saves it when you confirm. A repository's own `.p0ssess/rules.toml` can only tighten your limits, never loosen them.
- **Learns quietly.** It notices what you accept, redo and decline, adjusts its offers, and suggests rules and shortcut agents. It never creates either on its own.
- **Stays out of the way.** A one-line hint only when a prompt has media or bulk work in it, a small status line segment, and a local settings page.

## Install

You need Claude Code and an [OpenRouter](https://openrouter.ai) API key. The plugin runs one Go binary: a release archive ships it for Linux (amd64, arm64) and macOS (Apple silicon), checked against SHA-256 sums before it runs; from a plain clone the plugin builds it on first use with Go 1.27 or later, offline, in a few seconds. For the sandboxed agent path you also need Linux with rootless podman; without it, media and one-shot jobs still work.

```sh
claude plugin marketplace add <repository URL>
claude plugin install p0ssess@p0ssess
```

Put your OpenRouter key where p0ssess reads it:

```sh
mkdir -p ~/.config/p0ssess/secrets
install -m 600 /dev/null ~/.config/p0ssess/secrets/openrouter_key
"${EDITOR:-vi}" ~/.config/p0ssess/secrets/openrouter_key
```

Then start Claude Code and run `/p0ssess:p0ssess-me`. It walks you through the budget, your Claude plan and privacy, and offers (only with your yes) to allow the plugin's tools and add its status line segment. [docs/INSTALL.md](docs/INSTALL.md) has the details, other ways to install, and how to remove everything.

## Quick start

Ask for things the way you normally would:

- "What happens in `bug-repro.mp4`?" Claude asks the engine, tells you the cost if it is above your auto-approve limit, sends the video to a model that can watch it, and answers from what comes back.
- "Transcribe `standup.m4a` and list the action items."
- "Use deepseek flash to draft tests for `stats.py`." The relay agent runs DeepSeek in a sandbox; the tests come back as a diff for Claude to review.
- "Auto-approve short media jobs up to 5 cents each today." Saved as a standing approval after you confirm; matching jobs then run without asking.
- "How much have I spent on OpenRouter this month?"

`/p0ssess:p0ssess-me` opens a local page with spend against your limits, Claude's usage windows, recent delegations, learned tendencies, rules and settings.

## Privacy

- Every call tells OpenRouter not to route to providers that train on prompts. A zero-retention mode is one setting away.
- Files that look like secrets (`.env`, keys, credentials) are never sent. A `.p0ssess-private` file in a repository blocks all delegation from it.
- The sandbox for the agent path has no access to your home directory, SSH keys or credentials and cannot reach your local network. Each delegated session gets its own sandbox, removed when the session ends. Your OpenRouter key stays in a separate proxy container the model cannot reach. The model's shell commands have no network unless you turn on internet for a project yourself, and even then only public addresses.
- No telemetry. The history the engine learns from lives in local files and never stores prompt content.
- Agent-path transcripts and diffs are written to `~/.local/state/p0ssess/relay/runs/` (mode 0600) and kept until you delete them.
- p0ssess never reads, stores or relays your Claude credentials.

## How it works

- [docs/DESIGN.md](docs/DESIGN.md): the design
- [docs/ENGINE.md](docs/ENGINE.md): how routes are scored, the defaults and why, the config files and the tools
- [docs/RELAY.md](docs/RELAY.md): the sandboxed agent path
- [docs/MEDIA.md](docs/MEDIA.md): video, audio, images and PDFs on the direct path, with measured costs
- [docs/RELEASE.md](docs/RELEASE.md): how releases are built, checked and published
- [docs/RESEARCH.md](docs/RESEARCH.md), [catalog/](catalog/) and [registry/](registry/): the research, the 104 use cases and the model data behind the engine, each value with its source and date

## The ghost

The mark is a 15-by-15 pixel ghost, the thing that possesses another model for a moment and hands the work back. In the status line it is `ᗣ`: muted when idle, a little brighter while something runs, amber when you are close to your budget. Never red, never animated.

## License

MIT. See [LICENSE](LICENSE).
