<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/readme/hero-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="assets/readme/hero-light.svg">
    <img src="assets/readme/hero-light.svg" width="100%" alt="p0ssess. Stop paying full Claude price for repetitive work. Four measured hand-offs, each with the receipt line its answer ends with: unit tests for 15 Python modules, (deepseek-v4.1-flash-1017, $0.03). 300 support tickets triaged, (deepseek-v4.1-flash-1017, $0.08). A ten-minute film watched with its sound, (qwen3.8-omni-flash, $0.02). A 1024-pixel image, (recraft-v4.1-flash, $0.0070).">
  </picture>
</p>

<p align="center">A Claude Code plugin that hands heavy, bulk and media work to cheaper models through OpenRouter.<br>
Claude stays in charge.</p>

<p align="center">
  <a href="https://github.com/cm208/p0ssess/releases/latest"><img src="https://img.shields.io/github/v/release/cm208/p0ssess?style=flat-square&color=7457e0&label=release" alt="Latest release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-7457e0?style=flat-square" alt="MIT license"></a>
  <a href="https://claude.com/claude-code"><img src="https://img.shields.io/badge/Claude_Code-plugin-7457e0?style=flat-square" alt="Claude Code plugin"></a>
  <a href="https://openrouter.ai"><img src="https://img.shields.io/badge/OpenRouter-models-7457e0?style=flat-square" alt="Routes through OpenRouter"></a>
  <img src="https://img.shields.io/badge/platforms-linux%20%C2%B7%20macOS%20%C2%B7%20windows-555?style=flat-square" alt="Linux, macOS and Windows">
  <img src="https://img.shields.io/badge/go-1.27-555?style=flat-square" alt="Written in Go 1.27">
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#plans">Plans</a> ·
  <a href="#security-and-privacy">Security</a> ·
  <a href="https://docs.p0ssess.me">Docs</a> ·
  <a href="https://github.com/cm208/p0ssess/releases">Releases</a> ·
  <a href="https://p0ssess.me">p0ssess.me</a>
</p>

Tell p0ssess once which chores don't need Claude's rates. From then on Claude hands them to a cheaper model, checks what comes back and answers you as usual. It also does what Claude can't: watch a video, hear a recording, make an image. Small edits and plain questions never leave Claude.

Four hand-offs we measured, by the other model's bill: unit tests for 15 Python modules, $0.0291; 300 support tickets triaged, $0.0781; a ten-minute film watched with its sound, $0.0162; a 1024-pixel image, $0.0070. Each answer ends with one receipt line: the model, the cost, and the saving against Claude when there is one.

A receipt is the other model's bill. Claude's own planning and checking is billed as usual, so what a job saves depends on the job. The measured comparisons against Claude alone, including the jobs where p0ssess did not win, are on [p0ssess.me](https://p0ssess.me/business/), and [site/README.md](site/README.md) gives the source of every figure.

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/readme/handoff-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="assets/readme/handoff-light.svg">
    <img src="assets/readme/handoff-light.svg" width="100%" alt="How a hand-off travels, as three stops on one line. Claude plans, reviews and answers, and reads only what comes back. A possessed Haiku subagent in the background makes the call and hands the answer back. A cheaper model does the one job your rule names, sandboxed when it edits files. What returns to Claude is the result and its receipt: (model, $cost, saved ~$X).">
  </picture>
</p>

The hand-off runs in a small background subagent on Haiku, the possession. It makes the call and brings back only the result, so the work never fills your conversation.

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

Start Claude Code. The tour opens in your first session and takes about a minute: your spending envelope (keep it or pick another), a live demo you choose, capped at one cent, and where to see spend. `/p0ssess:tour` runs it again any time.

<p align="center">
  <img src="assets/readme/tour.svg" width="100%" alt="The tour's first card, as the terminal shows it. p0ssess tour, 1 of 4. Welcome to p0ssess. Glad you're here. Claude stays in charge. Heavy, bulk and media work goes to cheaper models on OpenRouter, inside limits you set. You, then Claude, then p0ssess, then a cheaper model; back comes the result plus (model, cost, saved). Three quick steps: your limits, a live demo, where to look. The two choices beside it: Start the tour, or Skip the tour.">
</p>

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

Claude rates each task from trivial to critical. Critical work (auth, infrastructure, production, money) stays with Claude unless you say otherwise. A model works the day OpenRouter lists it, because prices and ids come from OpenRouter's live list. [docs/ENGINE.md](docs/ENGINE.md) has the scoring.

### The spending envelope

One envelope, enforced by the tools that spend and not left to Claude's judgment:

- a daily limit that warns,
- a monthly cap that stops paid runs,
- an auto-approve amount per task. Under it a job just runs. Over it Claude asks once, in one line with the cost, and that one yes covers retries.

A new install starts at $1 a day, $10 a month and $0.25 a task. Each call's maximum is reserved before it is sent and settled to the measured cost after, so parallel calls cannot overshoot. A run that fails before it reaches a model costs nothing. Change any limit in plain words: "cap OpenRouter at $5".

### Rules

"Never use grok." "Only use gemini-3.8-flash for video." Claude shows the rule it wrote in one line and saves it when you confirm. p0ssess may suggest a rule or a shortcut from your history, and it never creates one on its own. A repository's own `.p0ssess/rules.toml` can only tighten your limits, never loosen them.

### Receipts

Every result carries one line: `(model, $cost)`, plus `saved ~$X` when the same tokens on Claude Sonnet would have cost more. A cost under a cent prints to four places. A cost OpenRouter has not reported yet reads `about $…` and counts at the reserved maximum until it does. The local page keeps every receipt.

### Edits

Agent work on files runs in a sandbox on a separate git worktree and carries your uncommitted changes in. Afterwards p0ssess runs the project's tests on the result. When they pass and your checkout is clean, the change is applied with a line that reverts it; otherwise it stays a diff for Claude to review. [docs/RELAY.md](docs/RELAY.md) covers the agent path.

## Where to look

### `/p0ssess:p0ssess-me`

A card with spend meters and your limits, then the address of a local page. A new install reads $0.00 of $1.00 today and $0.00 of $10.00 this month.

<p align="center">
  <img src="assets/readme/me-card.svg" width="100%" alt="The /p0ssess:p0ssess-me card on a new install, as the terminal shows it. p0ssess 1.1.0, nothing running. Today $0.00 of $1.00, with an empty meter: the daily limit warns. Month $0.00 of $10.00, with an empty meter: paid runs stop at the cap. Limits: asks above $0.25 a task, no-training. Under the card, the local page's address on 127.0.0.1, and how to change a limit: tell Claude &quot;cap OpenRouter at $5&quot; or &quot;always ask before grok&quot;.">
</p>

After the first runs the card adds this month's runs and savings and the last run. With the status line set up, it also shows Claude's 5-hour and weekly usage when Claude Code reports them.

The page runs on 127.0.0.1 only, behind a random token, loads nothing from the network and stops after two hours. It shows receipts, notices, recent runs, rules, Claude's usage windows and editable limits. When something is missing for quiet runs (the key, allow rules, the status line segment), the card ends with a `setup` line and Claude offers that one step, only with your yes. `p0ssess setup undo` reverts every settings change p0ssess made.

### The status line

A segment after your own status line, such as `ᗣ p0ssess $0.42/$1`: today's spend against the daily limit, then what runs.

<p align="center">
  <img src="assets/readme/statusline.svg" width="100%" alt="The status line segment in its four states, in the terminal's colors. Calm, in gray: p0ssess $0.42/$1. While something runs, a little brighter: p0ssess $0.42/$1 · 1 running (deepseek-v4.1-flash-1017). Near the daily limit, in amber: p0ssess $0.91/$1. When the monthly cap is reached, in red: p0ssess month cap $10.00/$10 · paid runs stopped.">
</p>

Muted when calm, a little brighter while something runs, amber near a limit, and red only when paid runs stop. Never animated. It reads local files only.

## Plans

| | Free | Pro | Team |
|---|---|---|---|
| Price | $0, MIT | $4 a month or $36 a year | $8 a seat a month |
| Routing, rules, budgets, receipts | yes | yes | yes |
| Local sandbox for coding work (Linux) | yes | yes | yes |
| Cloud sandbox: coding hand-offs on macOS and Windows | | yes | yes |
| Savings report and calibration on your own work | | yes | yes |
| Shared rules that only tighten, a budget share per member, spend dashboard | | | yes |

On every plan, model calls bill to your own OpenRouter key. `/p0ssess:pro` shows the plans and upgrades in your browser, and [p0ssess.me/pricing](https://p0ssess.me/pricing/) has them in full. `/p0ssess:team` invites people by email and shows seats, spend by member and model, and the shared rules; owners can pre-approve spending for every seat and set a hard team budget.

## Security and privacy

- Agent work runs in a podman sandbox with no access to your home directory, SSH keys or credentials, and no route to your local network. Each session gets its own sandbox, removed when it ends. Shell commands inside have no network unless you turn on internet for a project yourself, and then only public addresses.
- Your OpenRouter key is read from one file (mode 0600) and is never printed or logged. On the agent path it lives only in a separate proxy container the model cannot reach. p0ssess never reads, stores or relays your Claude credentials.
- Every call tells OpenRouter not to route to providers that train on prompts. Privacy mode `zdr` sends only to zero-retention endpoints and refuses models without one.
- Files that look like secrets (`.env`, keys, credentials) never leave. A `.p0ssess-private` file in a repository blocks all delegation from it and every directory below.
- The engine sees no prompts. Ranking requests carry a short task description, input and output types and sizes, the stakes and your model limits, never your prompt, your files or their names. The service keeps no request logs.
- Anonymous outcome records, one line per finished run (kind, model, route, stakes, outcome, coarse cost, week), are shown on the local page before they go. "Turn off outcome records" stops them.
- Releases are reproducible. The same commit and Go version build byte-identical binaries, so anyone can rebuild a release and compare. [docs/RELEASE.md](docs/RELEASE.md) says how.

The full list of what goes where is in [docs/DESIGN.md](docs/DESIGN.md) and on [p0ssess.me/privacy](https://p0ssess.me/privacy/).

## Docs

Also at [docs.p0ssess.me](https://docs.p0ssess.me).

| Page | What it covers |
|---|---|
| [INSTALL.md](docs/INSTALL.md) | requirements, Windows, the binary, updating |
| [INSTALL-NOTES.md](docs/INSTALL-NOTES.md) | everything p0ssess puts on your machine, and how to remove it |
| [DESIGN.md](docs/DESIGN.md) | the design: routing, limits, rules, privacy |
| [ENGINE.md](docs/ENGINE.md) | how routes are scored, the defaults and why, config files and tools |
| [RELAY.md](docs/RELAY.md) | the sandboxed agent path |
| [MEDIA.md](docs/MEDIA.md) | video, audio, images and PDFs, with measured costs |
| [RELEASE.md](docs/RELEASE.md) | how releases are built, checked and published |
| [RESEARCH.md](docs/RESEARCH.md) | the research behind the use cases and model data |

## Support

Write to [support@p0ssess.me](mailto:support@p0ssess.me), or open an [issue](https://github.com/cm208/p0ssess/issues).

## License

MIT. See [LICENSE](LICENSE) and, for third-party material, [NOTICE](NOTICE).

<p align="center"><img src="assets/readme/mark.svg" width="48" height="48" alt="The p0ssess ghost"></p>
