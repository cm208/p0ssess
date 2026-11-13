<p align="center"><img src="assets/ghost.svg" width="120" alt=""></p>
<h1 align="center">p0ssess</h1>

<p align="center">Claude Code hands work to other models through OpenRouter, and stays in charge.</p>

p0ssess is a Claude Code plugin. When a task needs something Claude cannot do (watch a video, hear a recording, make an image), or when a cheaper model would do just as well (summarizing a pile of logs, drafting tests that the test suite will judge), Claude hands that piece to another model, checks what comes back, and carries on. There is no extra step to approve: "watch this video" gets you the answer, with the model that watched it named in a few words:

```
The crash happens at 0:42, right after the second tap on Save: … (gemini-3.8-flash, $0.004)
```

The hand-off runs in a small background agent on Haiku, which makes the calls and returns only the result, so the work never fills your main conversation. Everything else stays with Claude.

## What it does

- **Routes by expected cost per accepted result.** The p0ssess engine scores every route that could do a task, Claude included: one OpenRouter call, a sandboxed agent session on another model, a smaller Claude subagent, or a local tool. It weighs the cash, the Claude usage it saves, how likely each model is to get it right (from public benchmarks and your own history), and how much a wrong answer would hurt. Your budget, rules and approvals are enforced on your machine, whatever the engine says.
- **Rates stakes.** Claude rates each task from trivial to critical. Critical work (auth, infrastructure, production, money) stays with Claude unless you say otherwise.
- **Reviews everything that matters.** Code edits come back as a diff from a separate git worktree and are never applied unreviewed. For important diffs Claude can offer an optional second opinion from two other model families, in addition to its own review.
- **Asks only when it matters.** A job runs without a question when its estimate is under your per-task auto-approve limit. Claude asks, in one line with the cost, only when the estimate is above that limit, the monthly cap would be passed, a rule of yours says to ask, or the work is critical. Asking before a model's first run or before agent edits is a setting you can turn on.
- **Respects a budget.** A monthly cap, a per-task auto-approve limit and a daily pace, enforced by the tools that spend, not left to Claude's judgment. Each call's maximum is reserved before it is sent and settled to the measured cost after, so parallel calls cannot overshoot; a cost OpenRouter never reports counts at the reserved maximum.
- **Takes rules in plain language.** "Never use grok." "Only use gemini-3.8-flash for video." "Auto-approve video summaries up to 20 cents each today." Claude shows the rule it wrote in one line and saves it when you confirm. A repository's own `.p0ssess/rules.toml` can only tighten your limits, never loosen them.
- **Learns quietly.** It notices what you accept, redo and decline, adjusts its offers, and suggests rules and shortcut agents. It never creates either on its own.
- **Hands you the files.** Images (and documents an agent run makes) come back as files: Claude prints each absolute path and, when you follow the session from the Claude app or work in a cloud session, sends them to you with Claude Code's own file tool.
- **Stays out of the way.** A hint Claude sees (you do not) only when a prompt has media or bulk work in it or is work one of your rules sends to a model, a small status line segment, and a local page.

## Install

You need Claude Code and an [OpenRouter](https://openrouter.ai) API key. The plugin runs one Go binary: a release archive ships it for Linux (amd64, arm64), macOS (Apple silicon) and Windows (x64), checked against SHA-256 sums before it runs. A plain clone downloads the same binary from the [releases page](https://github.com/cm208/p0ssess/releases) and checks it the same way, or, when that fails, builds it on first use with Go 1.27 or later in a few seconds. Releases are built reproducibly from the tagged commit, so anyone can rebuild one and compare ([docs/RELEASE.md](docs/RELEASE.md)). For the sandboxed agent path you also need Linux with rootless podman; without it (on macOS and Windows, say), media and one-shot jobs still work.

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

Then start Claude Code and run `/p0ssess:p0ssess-me`. It prints three lines (spend against the cap, your limits, what is running) and the address of a local page. When something is missing for quiet runs (the key, allow rules for the plugin's tools, the status line segment) it adds a `setup` line and offers that one step, only with your yes. [docs/INSTALL.md](docs/INSTALL.md) has the details, other ways to install, and how to remove everything.

## Quick start

Ask for things the way you normally would:

- "Watch `bug-repro.mp4` and tell me where it breaks." Claude sends the video to a model that can watch it (p0ssess picks the model) and answers from what comes back. It asks first only if the estimate is above your auto-approve limit.
- "Make an image of a ghost holding a lantern." The image comes back as a file; Claude shows you its path.
- "Transcribe `standup.m4a` and list the action items."
- "Use deepseek flash to draft tests for `stats.py`." The relay agent runs DeepSeek in a sandbox; the tests come back as a diff for Claude to review.
- "Auto-approve short media jobs up to 5 cents each today." Saved as a standing approval after you confirm; matching jobs then run without asking.
- "How much have I spent on OpenRouter this month?"

`/p0ssess:p0ssess-me` shows the summary and opens a local page: spend today and this month, what is running, Claude's usage windows, recent runs, your limits and rules, and the settings.

## Privacy

<!-- DRAFT for 1.0.0: states what the client sends; review before release. -->

p0ssess talks to two services: OpenRouter, which runs the models you hand work to, and the p0ssess engine service, which ranks the routes for a task.

What goes to OpenRouter:

- The prompt and files for the job you hand off, and nothing else. Every call tells OpenRouter not to route to providers that train on prompts. A zero-retention mode is one setting away.
- Files that look like secrets (`.env`, keys, credentials) are never sent. A `.p0ssess-private` file in a repository blocks all delegation from it.
- When a noisy recording is transcribed, p0ssess first cleans it up on your machine with ffmpeg (a speech band-pass and loudness normalization) and sends the cleaned copy, which it deletes after the call.

What goes to the engine service, with each request for a ranking:

- The use case (for example `video-understand`) or a short task description that Claude writes, such as "transcribe a 3-minute radio recording". Never your prompt, your files, their names or their contents.
- The input and output types and rough sizes (minutes of audio, number of pages), the stakes rating, and which Claude model leads the session.
- One number that weighs Claude's cost, worked out on your machine from your plan. Your Claude usage readings stay on your machine.
- The rules you set that limit which models may run ("never use grok", "only use gemini-3.8-flash for video"), your model and provider allow and deny lists, and the privacy mode. Rule notes, budgets, spending, standing approvals and ask-first rules are not sent.
- Any scoring settings you changed in `engine.toml`.
- How often you accepted, redid, declined or overrode each model, per use case, as plain counts. No dates, projects, sessions or task text.
- A random install token, made on your machine the first time, used only to rate-limit requests. It is not tied to you or to the machine.

Nothing is sent from a repository with a `.p0ssess-private` file. The prompt hook, which reads your prompts, never contacts the service.

The service keeps none of it: it does not log requests, tokens or IP addresses. Once a day p0ssess also downloads a signed table of default routes, so it keeps working, quietly, when the service cannot be reached.

Anonymous outcome records. When a Claude Code session ends, p0ssess sends the records about the delegated runs that have finished since the last send. If a session ends abruptly (a crash, a killed terminal), those records go out at the next session end or the next time the plugin starts. Nothing is sent when there are none, or from a repository with a `.p0ssess-private` file. Each record is exactly this:

```
{"v":1, "kind":"image.generate", "model":"google/gemini-3.4-flash-image",
 "route":"direct", "stakes":2, "outcome":"accepted", "cost":"<1c", "week":"2026-W46"}
```

That is the kind of task, the model, the route, the stakes, whether the result was used, the cost in a coarse bucket and the week. The service stores the records without the token, without your IP address and without the time they arrived, and uses them for success rates per model. Because an upload follows the end of a session, the service could in principle see roughly when your sessions end; it does not log requests, so it keeps no such record. Calibration results are not sent as outcome records. The local page (`/p0ssess:p0ssess-me`) shows the pending records line by line before they go. The upload runs in a background process, so closing Claude Code is never delayed by it. To turn records off, tell Claude "turn off outcome records", or set `outcomes.send = false` in `~/.config/p0ssess/budget.toml`. Then no records are built or sent.

Team. An install linked to an account in a p0ssess Team org fetches the org's signed policy (its shared rules and your monthly budget share) every few minutes while the plugin runs, and keeps the last one to enforce offline. It also reports each paid run to the org, so the owner can see spend: the day (UTC), the use case, the model, the route and the cost, nothing else. No prompts, files, file names, project paths, sessions or times of day, and nothing from a repository with a `.p0ssess-private` file. The reports wait in `~/.local/state/p0ssess/team-spend.jsonl` and go out with the policy fetch and at session end. Leaving the team (`p0ssess team leave`) deletes the policy and the queue.

On your machine:

- The sandbox for the agent path has no access to your home directory, SSH keys or credentials and cannot reach your local network. Each delegated session gets its own sandbox, removed when the session ends. Your OpenRouter key stays in a separate proxy container the model cannot reach. The model's shell commands have no network unless you turn on internet for a project yourself, and even then only public addresses.
- The full history the engine learns from stays in local files and never stores prompt content. Agent-path transcripts and diffs are written to `~/.local/state/p0ssess/relay/runs/` (mode 0600) and kept until you delete them.
- p0ssess never reads, stores or relays your Claude credentials.

## How it works

- [docs/DESIGN.md](docs/DESIGN.md): the design
- [docs/ENGINE.md](docs/ENGINE.md): how routes are scored, the defaults and why, the config files and the tools
- [docs/RELAY.md](docs/RELAY.md): the sandboxed agent path
- [docs/MEDIA.md](docs/MEDIA.md): video, audio, images and PDFs on the direct path, with measured costs
- [docs/RELEASE.md](docs/RELEASE.md): how releases are built, checked and published
- [docs/RESEARCH.md](docs/RESEARCH.md): the research behind the engine's use cases and model data

## The ghost

The mark is a 16-by-16 pixel ghost, the thing that possesses another model for a moment and hands the work back. In the status line it is `ᗣ`: muted when idle, a little brighter while something runs, amber when you are close to your budget. Never red, never animated.

## Questions or bugs

Open an issue at [github.com/cm208/p0ssess](https://github.com/cm208/p0ssess/issues), or write to [me@christianmoo.re](mailto:me@christianmoo.re).

## License

MIT. See [LICENSE](LICENSE).
