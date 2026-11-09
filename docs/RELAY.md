# The agent path: relay and jail

This is the part of p0ssess that hands multi-step work on files to another model. Claude Code starts the `p0ssess` relay subagent, the subagent calls the `p0ssess` MCP server, and the MCP server runs the task in a jailed OpenCode server that talks to OpenRouter.

```
Claude Code (main model)
 └─ Agent(subagent_type="p0ssess")            background by default; SendMessage continues it
     └─ relay subagent on Haiku               tools: the five p0ssess MCP tools, nothing else
         └─ p0ssess mcp (stdio)               run / wait / abort / answer_permission / models
             └─ pod p0ssess-<hash>            one per repository, rootless podman
                 ├─ <pod>-server              opencode serve 2.2.5
                 └─ <pod>-proxy               holds the OpenRouter key, forwards to openrouter.ai
```

Status: milestones R1 (OpenCode client and jail) and R2 (relay MCP server and agent) are built and tested. The plugin, the generated per-model agents and the routing engine come later.

## Pinned versions

| Piece | Version | Check |
|---|---|---|
| OpenCode | 2.2.5, `opencode-linux-x64.tar.gz` | SHA-256 `09f44302…2a79614e` (the release's published digest) and binary SHA-256 `8d060e1d…23a92050`, both checked by `p0ssess jail build` |
| Base image | `registry.fedoraproject.org/fedora:44@sha256:ead52905…2f649e` | pinned by digest |
| OpenCode API | `internal/opencode/testdata/openapi-2.2.5.json` | the contract test checks every operation and field the client uses |

The client refuses any server that does not report version 2.2.5. To upgrade OpenCode: add the new spec next to the old one, run the contract test against it, record a new event stream, and add the version to `SupportedVersions`.

## The jail

`p0ssess jail up --repo DIR` (or the first `run` in that repository) creates:

- **A pod** with `--userns=keep-id` (files written in the jail belong to you) and one published port, `127.0.0.1:<port>` → 4096. The password is 32 random bytes in `~/.config/p0ssess/jails/<pod>.pw` (mode 0600).
- **An egress filter** in the pod's network namespace. nftables rules are loaded from outside (`podman unshare nsenter -t <infra pid> -n nft -f -`) after the pod starts and before the server starts, so nothing inside the pod can change them. Rejected destinations: `0/8`, `10/8`, `100.64/10` (Tailscale), `127/8` off loopback, `169.254/16` (includes pasta's host mapping, 169.254.1.2), `172.16/12`, `192.168/16`, multicast and broadcast; IPv6 `::1`, `fc00::/7` (includes `fd00::/8` and Tailscale's `fd7a:115c:a1e0::/48`), `fe80::/10`, `ff00::/8`; and any global prefix the host itself is on. The only allowed link-local destination is pasta's DNS forwarder, 169.254.1.1 port 53. Everything else (the internet) is allowed. The relay checks that the table is present before every run and refuses to run without it.
- **The proxy container.** It runs `p0ssess proxy` from the host binary (mounted read-only) and is the only place the OpenRouter key exists. It forwards `/api/v1/*` to `https://openrouter.ai`, replaces the `Authorization` header, and sets `provider.data_collection` to `"deny"` on every request that names a model. Compressed request bodies are refused, so the rule cannot be bypassed. Its log lists method, path, model and status, never bodies.
- **The server container.** It runs `opencode serve`. Read-only root filesystem, `--cap-drop ALL`, `no-new-privileges`, a 2048 process limit and a private `/tmp`. Its state (OpenCode's database) is the volume `<pod>-state`. Its OpenRouter key is a placeholder; its base URL points at the proxy. Mounts:
  - the repository, read-only, at its host path, so session directories and Claude's paths agree;
  - `<repo>/.p0ssess/wt` and `<repo>/.git/worktrees`, read-write, for edit-mode worktrees.

  Nothing else from your home directory is mounted, and the container has no SSH keys or credentials.

### OpenCode configuration

The jailed server gets its global config through `OPENCODE_CONFIG_CONTENT`; project config files and Claude Code skill folders are ignored (`OPENCODE_DISABLE_PROJECT_CONFIG`, `OPENCODE_DISABLE_CLAUDE_CODE*`). The config:

- **Policies** (hard denies that OpenCode applies after every agent and session rule, so an allow-all session cannot undo them): `sudo`, `su`, `doas`, `pkexec`, `runuser`, `ssh`, `scp`, `sftp`, `rsync`, `sshpass`, `git push` (including `git <opts> push`), `docker`, `podman`, `buildah`, `nerdctl`, `kubectl`, `systemctl`, `loginctl`, `nsenter`, `unshare`, `chroot`, bare `sh`, `bash`, `bash -c` (and `-lc`, `-ec` …), `zsh`, `dash`, `fish`, `ksh`, `eval`, `exec`, `rm -rf /`; edits to `.git`, `.git/*`, `.ssh`, `.ssh/*`, `.env`, `.env.*`, `.envrc` and reads of `.ssh/*`, `.env*`, each in both relative (`x/*`) and absolute (`*/x/*`) form; `external_directory` everywhere; every provider except `openrouter`; every MCP server.
- **The title agent is disabled.** OpenCode would otherwise send each first prompt to the provider's "small" model, which nobody chose.
- `share` disabled, updates disabled.

OpenCode splits pipelines before matching, so `curl … | sh` is stopped by the bare-shell rules, not by a pipe pattern.

### Per-session rules

Rules are set when the relay creates a session. The last matching rule wins, so each list starts with a catch-all `* * ask`:

| | read mode | edit mode |
|---|---|---|
| read, glob, grep, todo | allow | allow |
| edit | deny | allow (inside the worktree; policies still protect `.git`, `.ssh`, `.env`) |
| shell | allowlist of read-only commands (`ls`, `cat`, `rg`, `git status/diff/log/show` …), anything else asks | the same plus build and test tools (`python3`, `pytest`, `go test`, `npm test`, `make`, `mkdir`, `mv`, `rm`, `sed` …), anything else asks |
| webfetch, websearch | ask | ask |
| question, subagent, skill, image generation, external directories | deny | deny |

An ask in read mode is rejected at once with feedback ("not allowed in this delegated read-only task … continue without it"), and the model carries on. An ask in edit mode pauses the run and comes back to Claude as `needs_permission`. The relay answers only `once` or `reject`; `always` is refused, because it would save a project-wide allow rule.

## The MCP tools

| Tool | Does |
|---|---|
| `run` | `task`, `model` (OpenRouter id), `mode` (`read` default, or `edit`), `cwd` (default `CLAUDE_PROJECT_DIR`), `attachments` (file paths), `session` (continue), `kind` (for the history), `wait_s`. Blocks until the run ends, pauses on a permission request, or `wait_s` passes (default 25 minutes). |
| `wait` | Keeps blocking on a session that returned `still_running`. |
| `abort` | Interrupts a session. |
| `answer_permission` | `once` or `reject` for a paused edit task, then blocks like `wait`. |
| `models` | Models with context, price per million tokens and modalities, from `registry/models.json` (or `P0SSESS_REGISTRY`, or `registry_file` in the config) or, with `live: true`, from OpenRouter. |

Results are JSON with `answer` first, then `status` (`done`, `failed`, `interrupted`, `needs_permission`, `still_running`), `session_id`, `model`, `mode`, `cost_usd`, `tokens`, `duration_s`, `transcript_path`, and for edit mode `worktree`, `diff_stat`, `diff_path` and `diff`. The answer is capped at 30,000 characters and the diff at 8,000, so a result stays under Claude Code's 50,000-character limit; past that, Claude Code saves the result to a file and the relay would lose the session id. The full answer and transcript are on disk.

- **Progress** goes out every 15 seconds while a run is active, which keeps Claude Code's idle timer from aborting the call.
- **Limits** per run: a cost cap (`max_run_cost_usd`, default $0.50) and a maximum runtime (`max_runtime_s`, default one hour). Either interrupts the run.
- **Cancellation**: a cancelled call and a closed stdin both interrupt the OpenCode run. Claude Code does not send a cancellation when its idle timer fires, so the server-side limits matter.
- **Attachments** are sent as `data:` URIs, so the jail never needs the files mounted. Text, PDF, PNG, JPEG, GIF and WebP are accepted, up to 20 MiB each. Audio and video are refused (OpenCode 2.2.5 drops them silently; they belong to the direct path), and so are files that look like secrets.
- **`.p0ssess-private`** at a repository root blocks delegation from it.

### Edit mode

Each edit task gets a detached worktree of `HEAD` at `<repo>/.p0ssess/wt/w<time>-<id>`. `.p0ssess/` is added to `.git/info/exclude`. The session's directory is the matching subdirectory of the worktree. When the run ends (or pauses), the relay stages the worktree's changes in the worktree's own index and returns `git diff --cached <base>`. Nothing is committed or merged. Uncommitted changes in your checkout are not in the worktree. To take a change: `git apply <diff_path>` in your checkout, or review in the worktree; `git worktree remove <path>` when done.

### History and files

Everything lives under `~/.config/p0ssess` (or `$P0SSESS_HOME`):

| File | Holds |
|---|---|
| `config.json` | optional settings: `openrouter_key_file`, `image`, `selinux` (`relabel` or `disable`), `max_run_cost_usd`, `max_runtime_s`, `registry_file` |
| `history.jsonl` | one line per finished run: time, kind, model, mode, outcome, cost, tokens, duration, session, turn, a 12-character SHA-256 prefix of the task (never its text), and counts of rejected and escalated asks |
| `sessions/<id>.json` | what the relay needs to continue a session: repository, directory, mode, model, worktree |
| `runs/<id>/` | `transcript.md`, `diff.patch`, and the full answer when it was truncated |
| `jails/<pod>.json`, `.pw` | the jail record and its password |

The OpenRouter key is read from `openrouter_key_file` (default `~/.config/p0ssess/secrets/openrouter_key`, or `$P0SSESS_OPENROUTER_KEY_FILE`). It is passed to the proxy container through its environment and is never printed, logged, written elsewhere or baked into an image.

## The relay agent

`agents/p0ssess.md` runs on Haiku with only the five MCP tools, `background: true` and `omitClaudeMd: true`. It copies the task, calls `run`, and replies with exactly one `<answer>` block per run followed by one metadata line (session, status, model, cost, transcript; for edit mode the worktree and the diff). In testing the answers came back exact, including a follow-up sent while the first run was still going (both answers, oldest first).

To try it in a project, copy `agents/p0ssess.md` to `.claude/agents/`, add `examples/mcp.json` as `.mcp.json`, and allow the tools (`examples/settings.json`). Background subagents cannot ask for permission, and Claude Code ignores a project's `permissions.allow` in a folder you have not trusted, so allow the tools at user level, trust the folder, or pass `--allowedTools`.

## Tests

- `go test ./...` runs the unit tests: the contract test against the pinned OpenAPI document, the recorded event stream, the MCP protocol (progress, cancellation, stdin close), the proxy, the policy and nftables builders, the session rules and attachment checks.
- `P0SSESS_IT=1 P0SSESS_OPENROUTER_KEY_FILE=… go test -run Integration -v ./internal/relay/` is the R1 exit check against a real jail (about $0.005 per run). It starts a jail on a temporary repository and checks:
  - **Egress**: a host service through pasta's 169.254.1.2 mapping, 100.100.100.100:53, `[fd7a:115c:a1e0::53]:53` and any targets in `P0SSESS_IT_LAN` all fail from inside, while a control container with no filter reaches the host service and the LAN targets; `https://openrouter.ai` works.
  - **Identity**: your uid, no effective capabilities, an empty `/home`, no real key in the server's environment.
  - **A coding task** in edit mode completes, the diff has the change, and the main checkout is untouched.
  - **Hard denies** in an allow-all session: `echo` runs; `sudo`, `su`, `ssh`, `scp`, `git push`, `docker`, `podman`, `systemctl`, `sh -c`, `bash -c`, `bash` and `curl … | sh` are all "Blocked by configuration policy", and so are writes to `.env`, `.ssh/config` and `.git/config`.
  - **Read mode** rejects an ask and the model reports it could not run the command.

## Known limits

- **The OpenCode password is visible inside the jail.** OpenCode reads it from its environment, and a shell command run by the model can read `/proc/1/environ`. With it the model could call the OpenCode API from inside the pod, for example to approve its own permission requests. It still cannot leave the jail: the repository is read-only, writes are confined to worktrees that Claude reviews as diffs, and egress is filtered. The OpenRouter key is not exposed this way; it lives only in the proxy container, which has its own PID namespace.
- **Read mode is not a read-only filesystem for the worktrees.** A read task runs in the same jail as edit tasks for that repository, so a model could write into another task's worktree with an allowlisted command (`find -delete`, for instance). Such a change would show up in that task's diff.
- **The model can spend through the proxy** like it can through OpenCode. The per-run cost cap and the key's own limit on OpenRouter bound this.
- **`data_collection: deny` narrows the providers** OpenRouter may use, and some of the cheapest endpoints drop out, so a run can cost more than the model's headline price.
- **SELinux relabeling**: with the default `selinux: relabel`, podman gives mounted repositories the shared `container_file_t` label. Host programs keep working. `restorecon -R <repo>` restores the default labels. Set `"selinux": "disable"` to run the containers with `label=disable` instead, which leaves your files alone but turns off SELinux separation for the jail.
- **OpenCode 2.2.5's durable session log** (`/api/experimental/session/{id}/log`) returns only its `log.synced` marker: its filter drops every session event. The relay uses that marker for the current sequence number, takes live events from `/api/event` (subscribed before each prompt, because the feed has no replay), and polls the session and its permission requests to catch anything missed across a reconnect.
- **Repositories whose `.git` is a file** (a worktree or a submodule) can be read but not edited: edit mode needs the `.git` directory.
