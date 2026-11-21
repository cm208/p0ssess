<p align="center"><img src="site/ghost.svg" width="96" height="96" alt="The p0ssess ghost"></p>

<h1 align="center">p0ssess</h1>

<p align="center"><strong>Stop paying full Claude price for repetitive work.</strong><br>
Claude Code hands the work to cheaper models through OpenRouter, and stays in charge.</p>

<p align="center">
  <a href="https://github.com/cm208/p0ssess/releases"><img src="https://img.shields.io/github/v/release/cm208/p0ssess?color=7457e0&label=release" alt="Latest release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/cm208/p0ssess?color=7457e0" alt="MIT license"></a>
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#plans">Plans</a> ·
  <a href="https://docs.p0ssess.me">Docs</a> ·
  <a href="https://p0ssess.me">p0ssess.me</a>
</p>

---

p0ssess is a Claude Code plugin. Tell it once which chores don't need Claude's rates, and Claude hands them to a cheaper model, checks what comes back and answers you as usual. It also does what Claude can't: watch a video, hear a recording, make an image.

The hand-off runs in a small background subagent on Haiku, the possession. It makes the call and brings back only the result, so the work never fills your conversation. Every answer ends with one receipt line: the model, the cost, and the saving against Claude.

```text
The crash happens at 0:42, right after the second tap on Save. …
(qwen3.8-omni-flash, $0.0162)
```

Everything else stays with Claude. Small edits and plain questions never leave.

## Install

You need [Claude Code](https://claude.com/claude-code) 2.1.319 or later and an [OpenRouter](https://openrouter.ai) API key.

```sh
claude plugin marketplace add cm208/p0ssess
claude plugin install p0ssess@p0ssess
```

Put your OpenRouter key where p0ssess reads it:

```sh
mkdir -p ~/.config/p0ssess/secrets
install -m 600 /dev/null ~/.config/p0ssess/secrets/openrouter_key
"${EDITOR:-vi}" ~/.config/p0ssess/secrets/openrouter_key
```

Start Claude Code. The tour runs in your first session; `/p0ssess:p0ssess-me` shows where you stand any time after.

The plugin runs one Go binary, for Linux (amd64, arm64), macOS (Apple silicon) and Windows (x64). The launcher runs it only when its SHA-256 matches `dist/SHA256SUMS` for this version, or builds it from the plugin's own vendored source with Go 1.27 or later. The sandboxed agent path needs Linux with rootless podman; on macOS and Windows, media and one-shot jobs work without it. [docs/INSTALL.md](docs/INSTALL.md) has the details, Windows notes, updating, and [how to remove everything](docs/INSTALL-NOTES.md).

## Quick start

Ask the way you normally would:

| You say | What happens |
|---|---|
| "Watch `bug-repro.mp4` and tell me where it breaks." | The video goes to a model that can watch it. Claude answers from what comes back. |
| "Make an image of a ghost holding a lantern." | The image lands as a file and Claude prints its path. |
| "Transcribe `standup.m4a` and list the action items." | The recording goes to a speech model; Claude writes the list. |
| "Use deepseek flash to draft tests for `stats.py`." | DeepSeek works in a sandbox on its own git worktree. The edit comes back as a diff with the tests run. |
| "Always send unit test writing to deepseek flash." | Claude writes the rule, shows it in one line and saves it when you say yes. |
| "Auto-approve short media jobs up to 5 cents each today." | A standing approval, saved after you confirm. |
| "How much have I spent on OpenRouter this month?" | Claude reads your ledger. |

## How it works

### Routing

When no rule of yours covers a job, the p0ssess engine prices every route that could do it, Claude included: one OpenRouter call, a sandboxed agent session on another model, a smaller Claude subagent, or a local tool. It weighs the cash, the Claude usage it saves, how likely each model is to get it right (public benchmarks plus your own history), and how much a wrong answer would hurt. The work stays on Claude whenever handing it off wouldn't save anything.

Claude rates each task from trivial to critical. Critical work (auth, infrastructure, production, money) stays with Claude unless you say otherwise. New OpenRouter models work the day they are listed: prices and ids come from OpenRouter's live list. [docs/ENGINE.md](docs/ENGINE.md)

### The spending envelope

One envelope, enforced by the tools that spend, not left to Claude's judgment:

- **a daily limit** that warns,
- **a monthly cap** that stops paid runs,
- **an auto-approve amount per task**: under it a job just runs; over it Claude asks once, in one line with the cost. That one yes covers retries.

New installs start at $1 a day, $10 a month and $0.25 a task. Each call's maximum is reserved before it is sent and settled to the measured cost after, so parallel calls cannot overshoot. A run that fails before it reaches a model costs nothing. Change any limit in plain words: "cap OpenRouter at $5".

### Rules

"Never use grok." "Only use gemini-3.8-flash for video." Claude shows the rule it wrote in one line and saves it when you confirm. p0ssess may suggest a rule or a shortcut from your history; it never creates one on its own. A repository's own `.p0ssess/rules.toml` can only tighten your limits, never loosen them.

### Receipts

Every result carries one line: `(model, $cost)`, plus `saved ~$X` when the same tokens on Claude Sonnet would have cost more. A cost OpenRouter has not reported yet reads `about $…` and counts at the reserved maximum until it does. The local page keeps every receipt.

### Edits

Agent work on files runs in a sandbox on a separate git worktree and carries your uncommitted changes in. Afterwards p0ssess runs the project's tests on the result. When they pass and your checkout is clean, the change is applied with a line that reverts it; otherwise it stays a diff for Claude to review. [docs/RELAY.md](docs/RELAY.md)

### The tour

`/p0ssess:tour` is four steps, about a minute: what p0ssess does, your spending envelope (keep it or pick another), a live demo you choose, and where to see spend. Run it again any time.

### `/p0ssess:p0ssess-me`

A card with spend meters, this month's runs and savings, the last run and your limits, then the address of a local page:

```text
╭──────────────────────────────────────────────────────────────╮
│ ᗣ  p0ssess 1.1.0                             nothing running │
│                                                              │
│ today   $0.14    ▰▰▱▱▱▱▱▱▱▱▱▱▱▱▱▱  of $1.00                  │
│ month   $1.86    ▰▰▰▱▱▱▱▱▱▱▱▱▱▱▱▱  of $10.00                 │
│ runs    12 this month · ~$3.10 saved (63%)                   │
│ last    gemini-3.8-flash · <$0.01 · 4 min ago                │
│                                                              │
│ limits  asks above $0.25 a task · no-training                │
│ Claude  5-hour 31% · weekly 12%                              │
╰──────────────────────────────────────────────────────────────╯
  page    http://127.0.0.1:41733/t/…/
  change  tell Claude: "cap OpenRouter at $5", "always ask before grok"
```

*(Example figures.)* The page runs on 127.0.0.1 only, behind a random token, loads nothing from the network and stops after two hours. It shows receipts, notices, recent runs, rules, Claude's usage windows and editable limits. When something is missing for quiet runs (the key, allow rules, the status line segment), the card ends with a `setup` line and Claude offers that one step, only with your yes. `p0ssess setup undo` reverts every settings change p0ssess made.

### The status line

A segment after your own status line: today's spend against the daily limit, then what runs.

```text
ᗣ p0ssess $0.42/$1
ᗣ p0ssess $0.42/$1 · 1 running (deepseek-v4.1-flash-1017)
ᗣ p0ssess $0.91/$1                                      amber: near the daily limit
ᗣ p0ssess month cap $10.00/$10 · paid runs stopped      red: only when paid runs stop
```

Muted when calm, a little brighter while something runs. Never animated. It reads local files only.

## Plans

| | Free | Pro | Team |
|---|---|---|---|
| Price | $0, MIT | $4 a month or $36 a year | $8 a seat a month |
| Routing, rules, budgets, receipts | yes | yes | yes |
| Local sandbox for coding work (Linux) | yes | yes | yes |
| Cloud sandbox: coding hand-offs on macOS and Windows | | yes | yes |
| Savings report and calibration on your own work | | yes | yes |
| Shared rules that only tighten, a budget share per member, spend dashboard | | | yes |

On every plan, model calls bill to your own OpenRouter key. `/p0ssess:pro` shows the plans and upgrades in your browser. `/p0ssess:team` invites people by email and shows seats, spend by member and model, and the shared rules; owners can pre-approve spending for every seat and set a hard team budget.

## Security and privacy

- **The sandbox.** Agent work runs in a podman sandbox with no access to your home directory, SSH keys or credentials, and no route to your local network. Each session gets its own sandbox, removed when it ends. Shell commands inside have no network unless you turn on internet for a project yourself, and then only public addresses.
- **Your keys stay local.** The OpenRouter key is read from one file (mode 0600), never printed or logged. On the agent path it lives only in a separate proxy container the model cannot reach. p0ssess never reads, stores or relays your Claude credentials.
- **No training, zero retention one setting away.** Every call tells OpenRouter not to route to providers that train on prompts. Privacy mode `zdr` sends only to zero-retention endpoints and refuses models without one.
- **Secrets are never sent.** Files that look like secrets (`.env`, keys, credentials) never leave. A `.p0ssess-private` file in a repository blocks all delegation from it and every directory below.
- **The engine sees no prompts.** Ranking requests carry a short task description, input and output types and sizes, the stakes and your model limits. Never your prompt, your files or their names. The service keeps no request logs.
- **Anonymous outcome records**, one line per finished run (kind, model, route, stakes, outcome, coarse cost, week), are shown on the local page before they go. "Turn off outcome records" stops them.
- **Reproducible releases.** The same commit and Go version build byte-identical binaries, so anyone can rebuild a release and compare. [docs/RELEASE.md](docs/RELEASE.md)

The full list of what goes where is in [docs/DESIGN.md](docs/DESIGN.md) and on [p0ssess.me/privacy](https://p0ssess.me/privacy/).

## Docs

Also at [docs.p0ssess.me](https://docs.p0ssess.me).

| | |
|---|---|
| [INSTALL.md](docs/INSTALL.md) | requirements, Windows, the binary, updating |
| [INSTALL-NOTES.md](docs/INSTALL-NOTES.md) | everything p0ssess puts on your machine, and how to remove it |
| [DESIGN.md](docs/DESIGN.md) | the design: routing, limits, rules, privacy |
| [ENGINE.md](docs/ENGINE.md) | how routes are scored, the defaults and why, config files and tools |
| [RELAY.md](docs/RELAY.md) | the sandboxed agent path |
| [MEDIA.md](docs/MEDIA.md) | video, audio, images and PDFs, with measured costs |
| [RELEASE.md](docs/RELEASE.md) | how releases are built, checked and published |
| [RESEARCH.md](docs/RESEARCH.md) | the research behind the use cases and model data |

The measured comparisons against Claude alone, including the jobs where p0ssess did not win, are on [p0ssess.me](https://p0ssess.me).

## The ghost

The mark is a 16-by-16 pixel ghost, `ᗣ` in the terminal: the thing that possesses another model for a moment and hands the work back.

## Support

Write to [support@p0ssess.me](mailto:support@p0ssess.me), or open an [issue](https://github.com/cm208/p0ssess/issues).

## License

MIT. See [LICENSE](LICENSE) and, for third-party material, [NOTICE](NOTICE).
