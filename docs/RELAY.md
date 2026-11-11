# The agent path: relay and jail

This is the part of p0ssess that hands multi-step work on files to another model. Claude Code starts the `p0ssess` relay subagent, the subagent calls the `p0ssess` MCP server, and the MCP server runs the task in a jailed OpenCode server that talks to OpenRouter.

```
Claude Code (main model)
 └─ Agent(subagent_type="p0ssess:p0ssess")   background by default; SendMessage continues it
     └─ p0ssess agent on Haiku                tools: the routed p0ssess MCP tools, nothing else
         └─ p0ssess mcp (stdio)               run / wait / abort / answer_permission / models
             └─ pod p0ssess-<random>          one per relay session, rootless podman
                 ├─ <pod>-server              opencode serve 2.2.5
                 ├─ <pod>-proxy               holds the OpenRouter key, forwards to openrouter.ai
                 └─ <pod>-egress              only with shell internet: the shell's proxy to public addresses
```

Status: built, tested and shipped in the plugin. The relay tools live on the plugin's one MCP server next to the direct and engine tools; inside the plugin their names are `mcp__plugin_p0ssess_p0ssess__<tool>` and the agent is `p0ssess:p0ssess`. Each run's start and end are reported to the engine: the status line counts it as running, and its measured cost becomes spend.

## Pinned versions

| Piece | Version | Check |
|---|---|---|
| OpenCode | 2.2.5, `opencode-linux-x64.tar.gz` | SHA-256 `09f44302…2a79614e` (the release's published digest) and binary SHA-256 `8d060e1d…23a92050`, both checked by `p0ssess jail build` |
| Base image | `registry.fedoraproject.org/fedora:44@sha256:ead52905…2f649e` | pinned by digest |
| OpenCode API | `internal/opencode/testdata/openapi-2.2.5.json` | the contract test checks every operation and field the client uses |

The client refuses any server that does not report version 2.2.5. To upgrade OpenCode: add the new spec next to the old one, run the contract test against it, record a new event stream, and add the version to `SupportedVersions`.

## The jail

Every relay session gets its own jail. The first `run` of a session creates it, follow-ups in the same session reuse it, and it is removed when the session ends (see [Session lifetime](#session-lifetime)). Nothing is shared between sessions: not the server, not OpenCode's database, not a worktree, not a volume. A cold jail starts in about 0.65 seconds (0.75 with shell internet) and is removed in about 0.2 seconds; see [Tests](#tests) for the measurement.

A jail is:

- **A pod** with `--userns=keep-id` (files written in the jail belong to you) and one published port, a random free `127.0.0.1` port → 4096. Its password is 32 random bytes in the jail's record directory (`<state>/relay/jails/<pod>/pw`, mode 0600). Its labels tie it to the state directory that holds its record, so a sweep only ever looks at its own jails.
- **An egress filter** in the pod's network namespace. nftables rules are loaded from outside (`podman unshare nsenter -t <infra pid> -n nft -f -`) after the pod starts and before anything else runs in it, so nothing inside the pod can change them. Rejected destinations: `0/8`, `10/8`, `100.64/10` (Tailscale), `127/8` off loopback, `169.254/16` (includes pasta's host mapping, 169.254.1.2), `172.16/12`, `192.168/16`, multicast and broadcast; IPv6 `::1`, `fc00::/7` (includes `fd00::/8` and Tailscale's `fd7a:115c:a1e0::/48`), `fe80::/10`, `ff00::/8`; any global prefix the host itself is on; and any range in `blocked_cidrs` in `config.json`. The only allowed link-local destination is pasta's DNS forwarder, 169.254.1.1 port 53. Everything else (the internet) is allowed for the server itself. The relay checks that the table is present before every run and refuses to run without it.
- **The proxy container.** It runs `p0ssess proxy` from the host binary (mounted read-only) and is the only place the OpenRouter key exists. It forwards `/api/v1/*` to `https://openrouter.ai`, replaces the `Authorization` header, and sets `provider.data_collection` to `"deny"` on every request that names a model, plus `provider.zdr = true` when the budget's privacy mode is `zdr` (`--zdr`; a session started without it cannot continue once the mode is turned on). Compressed request bodies are refused, so the rule cannot be bypassed. It also refuses every request after a **deadline** the relay writes for it: the run's maximum runtime after each prompt, and one minute after the run ends. A run whose relay process died therefore stops spending at its maximum runtime, even though nothing interrupts it. Its log lists method, path, model and status, never bodies.
- **The egress container**, only when the user turned on [shell internet](#shell-internet) for the project.
- **The server container.** It runs `opencode serve`. Read-only root filesystem, `--cap-drop ALL`, `no-new-privileges`, a 2048 process limit, a private `/tmp`, and a private `/state` (OpenCode's database) on a tmpfs, so the session's transcript lives only as long as its jail. Its OpenRouter key is a placeholder; its base URL points at the proxy. Mounts:
  - the repository, read-only, at its host path, so session directories and Claude's paths agree;
  - empty read-only tmpfs mounts over `<repo>/.p0ssess` and `<repo>/.git/worktrees`, which hide every other session's worktree and git admin directory;
  - for an edit session, its own worktree `<repo>/.p0ssess/wt/<name>` and its admin directory `<repo>/.git/worktrees/<name>`, read-write, on top of those.

  Nothing else from your home directory is mounted, and the container has no SSH keys or credentials. A read session has nothing writable under the repository.

  The repository is the git work tree around the run's directory: `cwd` when the caller passes it, else the Claude Code session's working directory as its status line and hooks recorded it, else `CLAUDE_PROJECT_DIR`, else the server's own directory. A run is refused, before any jail exists, when that directory is not inside a git work tree, or when the repository is the root, your home directory or a directory above it, has `.ssh`, `.gnupg`, `.config`, `.aws`, `.azure`, `.kube`, `.docker`, `.password-store`, `.netrc`, `.git-credentials` or `.pgpass` at its top, or holds (or sits inside) p0ssess's config, cache or state directory or the OpenRouter key file. `jail.Start` repeats the check, so no caller can mount such a directory. The answer tells Claude to pass the project's directory or use the direct tool.

## Security: a directory outside a repository was mounted (fixed in 0.5.4)

**Affected:** every version with the agent path, 0.1.0 through 0.5.3. **Fixed in 0.5.4. Upgrade.** The direct path (video, audio, images) mounts nothing and was not affected.

**The bug.** The relay mounts the run's repository into the jail. When the run's directory was not inside a git repository, it used the directory itself as the repository. The p0ssess agent does not pass `cwd`, so the directory was Claude Code's project directory. Starting Claude Code in your home directory and asking for agent work therefore mounted your whole home directory read-only into the server container. Any other directory outside a repository was mounted the same way. Edit mode was refused there (it needs a `.git` directory), so only read sessions were affected, unless the directory was itself a git repository, as a home directory managed with git can be.

**Impact.** The mount was read-only, and a read session cannot edit files, so nothing was changed. Inside the mounted directory the model could read every file you can read: through its read, glob and grep tools (the read tool's denies cover only `.ssh/*`, `.env`, `.env.*` and `.envrc`) and through the read-only shell commands a read session allows, such as `cat`, which those denies do not cover. That includes SSH private keys, cloud and service tokens under `.config`, and p0ssess's own OpenRouter key file when it is in the default place (`~/.config/p0ssess/openrouter_key`). The key proxy keeps the key out of the server container's environment and memory; it does not help when the key file itself is mounted, so in this case the key was readable by the model like any other file. Whatever the model read went into its context, which is sent to the OpenRouter provider serving the run, and could appear in the answer returned to Claude Code. There was no automatic way to send files anywhere else: the shell has no network unless you turned on shell internet for that directory (and then only public addresses on ports 80 and 443, through commands a read session must ask for), and webfetch asks first. The egress filter, the jail's other limits and the per-run cost cap applied as usual.

**Check whether you were affected.** Each agent session's record names its repository: `grep -h '"repo"' ~/.local/state/p0ssess/relay/sessions/*.json | sort -u` (0.4.0 and later; with `P0SSESS_HOME` set, `$P0SSESS_HOME/state/relay/sessions`). A `repo` that is your home directory, or any directory that is not a git repository, means that directory was mounted for that session. If a run in such a session reached the model, treat the files in that directory as shown to that run's provider: rotate the OpenRouter key and any credentials stored there.

### Session lifetime

| When | What happens to the jail |
|---|---|
| a session's first `run` | created (the worktree first, for edit mode), then the OpenCode session |
| a follow-up `run` with `session` (Claude's SendMessage to the relay agent) | reused; it is verified (all containers running, egress filter loaded) before every prompt |
| a run ends | an idle timer starts: `session_idle_s` in `config.json`, default 1800 (30 minutes); the key proxy refuses model calls from a minute after the run until the next prompt |
| the idle timer fires with no new run | removed; the session is marked ended, and a later `wait` returns its last result while a later `run` says "has ended; start a new run" |
| the Claude Code session ends (the MCP server's stdin closes) | runs still active are interrupted and recorded, then every jail the server owns is removed |
| the p0ssess process is killed or crashes | the jail outlives it; the next p0ssess start (the MCP server, any relay command, or `p0ssess jail sweep`) removes every jail whose owner is gone |
| `p0ssess run` from a shell | the jail is removed when the run ends, unless `--keep` keeps it for `session_idle_s` so `p0ssess run --session` can continue it; with `--detach`, or a run paused on a permission request, it is kept until the run's maximum runtime plus `session_idle_s` |

A jail's record names the process that owns it (pid and start time, so a reused pid does not count). A sweep removes a jail whose owner is gone, unless it was detached on purpose, and then only once its keep time has passed. A `wait` or `answer` from another process adopts a detached jail. Pods with no record and records with no pod are removed after two minutes, so a jail another process is still creating is left alone. p0ssess 0.3's shared per-repository pods and their `-state` volumes are not touched automatically: remove them with `p0ssess jail sweep --legacy` once no 0.3 process runs.

### OpenCode configuration

The jailed server gets its global config through `OPENCODE_CONFIG_CONTENT`; project config files and Claude Code skill folders are ignored (`OPENCODE_DISABLE_PROJECT_CONFIG`, `OPENCODE_DISABLE_CLAUDE_CODE*`). The config:

- **Policies** (hard denies that OpenCode applies after every agent and session rule, so an allow-all session cannot undo them): `sudo`, `su`, `doas`, `pkexec`, `runuser`, `ssh`, `scp`, `sftp`, `rsync`, `sshpass`, `git push` (including `git <opts> push`), `docker`, `podman`, `buildah`, `nerdctl`, `kubectl`, `systemctl`, `loginctl`, `nsenter`, `unshare`, `chroot`, bare `sh`, `bash`, `bash -c` (and `-lc`, `-ec` …), `zsh`, `dash`, `fish`, `ksh`, `eval`, `exec`, `rm -rf /`; edits to `.git`, `.git/*`, `.ssh`, `.ssh/*`, `.env`, `.env.*`, `.envrc` and reads of `.ssh/*`, `.env*`, each in both relative (`x/*`) and absolute (`*/x/*`) form; **reads of any absolute path** (`read:/*`, with `/proc/*` and `/state*` spelled out), which blocks every literal read outside the worktree, `/proc/1/environ` and the server's `/state` among them, since in-worktree reads carry a relative resource; **webfetch to every loopback, link-local and private host** (`127.*`, `localhost`, `[::1]`, `169.254.*`, `10.*`, `192.168.*`, `172.*`, `100.*`, both schemes), which fences the loopback API and the key proxy from the one in-process tool that could otherwise reach them; `external_directory` everywhere; every provider except `openrouter`; every MCP server.
- **The shell is the jail wrapper, not `/bin/sh`.** `shell` points at the host `p0ssess` binary (mounted read-only at `/opt/p0ssess`). See "The jailed shell" below.
- **`formatter` and `lsp` are off.** Both run repository-controlled programs inside the server process (a formatter on save, a language server on open); neither was asked for.
- **The title agent is disabled.** OpenCode would otherwise send each first prompt to the provider's "small" model, which nobody chose.
- `share` disabled, updates disabled.

OpenCode splits pipelines before matching, so `curl … | sh` is stopped by the bare-shell rules, not by a pipe pattern.

### The jailed shell

OpenCode takes its shell from config and runs `<shell> -c <command>`, so `shell: /opt/p0ssess` makes every command the model runs pass through the host binary's `-c` wrapper (`internal/jail/shell.go`) instead of a plain `/bin/sh`. For each command the wrapper:

- **Scrubs the environment.** It drops `OPENCODE_*`, `P0SSESS_*` and anything whose name contains `KEY`, `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD` or `CREDENTIAL`, so `echo $OPENCODE_PASSWORD` and the proxy's variables yield nothing, and points `HOME`/`TMPDIR` at the writable `/tmp` (because `/state` is masked, below).
- **Enters fresh user, network, mount and pid namespaces** (`unshare -U -r -n -m -p --fork`). The new **network** namespace has nothing but a loopback interface, so the OpenCode API, the key proxy, the LAN and the internet are unreachable from a command. The new **user** namespace makes `/proc/1/environ` (the server's own environment) unreadable and blocks ptrace of the server; the cross-user-namespace denial is checked empirically by the integration test. In the new **mount** namespace it overmounts `/state` with an empty `tmpfs`, so a command sees an empty `/state` and cannot touch the session's database. The mounts it inherits from the server are locked together by the kernel, so a command cannot unmount the masks over other sessions' worktrees (tested).
- **With shell internet only**, starts a forwarder inside the command's namespaces and sets `HTTP_PROXY`/`HTTPS_PROXY` (below).

**Without shell internet, commands have no network:** `pip`, `npm` and `go` module downloads fail. That is the default. Offline, vendored builds and tests still work (the integration test adds a function and runs `python3 -m unittest` inside the jail). The whole mechanism depends on the repository mounts carrying the shared SELinux label (`:z`, the default); with `selinux: disable` the containers run with `label=disable` instead, which is weaker and not recommended.

### Shell internet

Off by default. The user turns it on per project, in their own `budget.toml` only (`[sandbox] shell_internet = ["/path/to/project"]`, set through `settings_set` with `sandbox.shell_internet.add`, `/p0ssess-me`, or by asking Claude, which needs the user's yes at the tool's permission prompt). A repository's own `.p0ssess/rules.toml` cannot turn it on; project scope only tightens. It is fixed when a session's jail starts: a session started with it cannot continue after the user turns it off. Every result of such a session says `shell_internet: true` with a note, and the relay agent's metadata line ends with `shell internet: on`, so it is never on quietly.

How it works, without loosening anything else:

1. The jail gets one more container, `<pod>-egress`, running `p0ssess egress` with the same confinement as the others (read-only root, no capabilities, `no-new-privileges`, no key). It listens on a Unix socket in a directory shared, read-only for the server, between the two containers. It is an HTTP proxy for `CONNECT` and absolute `http://` requests. It resolves each name itself and connects only to public unicast addresses on ports 80 and 443. The check runs on the resolved address the socket is about to connect to, so a name that resolves to a private address is refused too. Refused: loopback (the OpenCode API and the key proxy), `0/8`, `10/8`, `100.64/10`, `169.254/16`, `172.16/12`, `192.168/16`, the documentation, benchmarking, reserved and 6to4/Teredo/NAT64 ranges, multicast, IPv6 loopback, ULA (`fc00::/7`, including Tailscale), link-local, the host's own prefixes and `blocked_cidrs`. The refusal says only "destination refused", so a command cannot learn whether an internal name exists. It runs in the pod's network namespace, so the nftables filter is a second fence behind it.
2. The wrapper starts a forwarder inside the command's own namespaces: it brings the namespace's loopback up, listens on `127.0.0.1:3128`, and relays to the socket. A Unix socket is a file, not a network endpoint, so it is the namespace's only way out, and only to the proxy. Tools that honor `HTTP(S)_PROXY` (pip, npm, go, cargo, git over HTTPS, curl, Python's urllib) work; anything that resolves names itself or opens raw sockets has no route at all.
3. In edit mode the session rules also allow `pip install`, `npm install`/`ci`, `go mod download`/`tidy`, `go get`, `cargo fetch`, `git fetch`/`clone` and `curl`; without shell internet they keep asking.

The integration test proves it in a real jail: a public HTTPS fetch works with the opt-in and fails without it; the API and the key proxy (by address and as `localhost`), the LAN targets from `P0SSESS_IT_LAN` (on their own port, 80 and 443), Tailscale's addresses, pasta's host mapping and every address of the host are all refused through the proxy; direct connections to anything have no route; and the proxy, server and egress containers run unprivileged with a read-only root, no added capabilities and `no-new-privileges`.

What it does not cover: a router that forwards ports from the network's public address back into the LAN (hairpin NAT) makes that public address a way back in. Add it to `blocked_cidrs` in `config.json` if that applies. Name resolution goes through the host's resolver, so the resolver sees the names a command asks for.

### Per-session rules

Rules are set when the relay creates a session. The last matching rule wins, so each list starts with a catch-all `* * ask`:

| | read mode | edit mode |
|---|---|---|
| read, glob, grep, todo | allow | allow |
| edit | deny | allow (inside the worktree; policies still protect `.git`, `.ssh`, `.env`) |
| shell | allowlist of read-only commands (`ls`, `cat`, `rg`, `git status/diff/log/show` …), anything else asks | the same plus build and test tools (`python3`, `pytest`, `go test`, `npm test`, `make`, `mkdir`, `mv`, `rm`, `sed` …), plus fetch and install commands with shell internet; anything else asks |
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

Results are JSON with `answer` first, then `status` (`done`, `failed`, `interrupted`, `needs_permission`, `still_running`, `tampered`), `session_id`, `model`, `mode`, `cost_usd`, `tokens`, `duration_s`, `transcript_path`, `shell_internet` when it was on, `jail_start_s` on a session's first run, and for edit mode `worktree`, `diff_stat`, `diff_path` and `diff`.

### Model ids

`run` takes an exact OpenRouter id (`vendor/model`, optionally `openrouter/…` or `…#variant`), which is accepted as given so the registry snapshot can lag OpenRouter. A bare name is resolved against the registry only when exactly one model matches; otherwise the relay returns the candidates (or "no model matches") instead of guessing (`ResolveModelID`, `internal/relay/models.go`).

### Tamper detection

Only the relay answers permission requests, interrupts, or otherwise steers a session. The watcher subscribes to `permission.replied` and the interruption events and records every reply and interrupt the relay itself issues. A permission reply the relay did not issue (a model that found the API password answering its own request, say) or an interruption the relay did not request aborts the session and returns `status: tampered` with the reason. In normal operation this never fires, because a model's shell commands have no network and webfetch to the loopback API is denied; it is the backstop if those ever fail. The answer is capped at 30,000 characters and the diff at 8,000, so a result stays under Claude Code's 50,000-character limit; past that, Claude Code saves the result to a file and the relay would lose the session id. The full answer and transcript are on disk.

- **Progress** goes out every 15 seconds while a run is active, which keeps Claude Code's idle timer from aborting the call.
- **Limits** per run: a cost cap and a maximum runtime (`max_runtime_s`, default one hour). Either interrupts the run. The cap is the run's `max_cost_usd`, which defaults to and cannot exceed `max_run_cost_usd` (default $0.50).
- **The budget guard.** Before each prompt the relay asks the engine to authorize it: rules, the allow and deny lists, stakes (`stakes`, else the `use_case`'s catalog stakes, else 3), privacy, `.p0ssess-private`, and the per-task, daily and monthly limits, checked against the run's cap and reserved in the ledger (see ENGINE.md, Enforcement). A refusal returns before the jail is touched; a cap above the per-task limit needs a standing approval. A run that fails before its prompt is sent gives the reservation back; one whose session cost cannot be read is recorded at its cap, marked estimated. A run that ends while nobody waits on it is still recorded. Without the guard (no engine attached) the relay refuses to run. A model with no known price needs an explicit `max_cost_usd` above 0: OpenCode prices runs from its own model list, so the cap may never trip, and a run it reports at $0 for billed tokens is recorded at its cap, marked estimated. The command-line `p0ssess run` uses the same guard.
- **The command line** (`p0ssess run`, `wait`, `answer`) stays until the run ends or pauses on a permission request, printing progress to stderr, because the watch that enforces the cap and the maximum runtime and records the cost lives in that process. `--detach` returns `still_running` after `--wait` seconds instead. A detached run is unwatched: its cost cap is not enforced and its cost is not recorded until a later `p0ssess wait --session <id>` (or the MCP `wait` tool) picks it up, and its reservation counts as spent at the full cap once its hold (`max_runtime_s` + 15 minutes) passes. Its maximum runtime still holds: the key proxy refuses model calls after it. The MCP server keeps watches alive for as long as it runs.
- **Cancellation**: a cancelled call and a closed stdin both interrupt the OpenCode run. Claude Code does not send a cancellation when its idle timer fires, so the server-side limits matter.
- **Attachments** are sent as `data:` URIs, so the jail never needs the files mounted. Text, PDF, PNG, JPEG, GIF and WebP are accepted, up to 20 MiB each. Audio and video are refused (OpenCode 2.2.5 drops them silently; they belong to the direct path), and so are files that look like secrets.
- **`.p0ssess-private`** in a repository, or any directory above the working directory, blocks delegation from it.
- **Transcripts** of each run's task and output are kept under `~/.local/state/p0ssess/relay/runs/<session>/`; nothing prunes them yet. Inside the jail the session's database is gone with the jail.

### Edit mode

Each edit session gets a detached worktree of `HEAD` at `<repo>/.p0ssess/wt/w<time>-<id>`, created before its jail so the jail can mount it. `.p0ssess/` is added to `.git/info/exclude`. The session's directory is the matching subdirectory of the worktree. When a run ends (or pauses), the relay stages the worktree's changes in the worktree's own index and returns `git diff --cached <base>`. Nothing is committed or merged. Uncommitted changes in your checkout are not in the worktree. To take a change: `git apply <diff_path>` in your checkout, or review in the worktree; `git worktree remove <path>` when done. The worktree stays after its jail is removed.

### History and files

The relay keeps its state under `~/.local/state/p0ssess/relay/` (`$XDG_STATE_HOME` honored; `$P0SSESS_HOME/state/relay` with `P0SSESS_HOME` set) and its settings in `~/.config/p0ssess/`:

| File | Holds |
|---|---|
| `~/.config/p0ssess/config.json` | optional settings: `openrouter_key_file`, `image`, `selinux` (`relabel` or `disable`), `max_run_cost_usd`, `max_runtime_s`, `session_idle_s`, `blocked_cidrs`, `registry_file` |
| `relay/relay-runs.jsonl` | one line per finished run: time, kind, model, mode, outcome, cost, tokens, duration, session, turn, a 12-character SHA-256 prefix of the task (never its text), and counts of rejected and escalated asks |
| `relay/sessions/<id>.json` | what the relay needs to continue a session: repository, directory, mode, model, worktree, jail, whether it has ended |
| `relay/runs/<id>/` | `transcript.md`, `diff.patch`, `result.json` (the last result), and the full answer when it was truncated |
| `relay/jails/<pod>/` | the jail's record, its password, the proxy's deadline and the egress socket; removed with the jail |

p0ssess 0.3 kept `sessions/`, `runs/`, `history.jsonl` and `jails/` in `~/.config/p0ssess/`. On its first start 0.4 moves them, under a lock, to `relay/sessions/`, `relay/runs/`, `relay/relay-runs.jsonl` and `relay/legacy-jails/`, each only when the new path does not exist yet. When a move fails (the two directories on different filesystems, say) the old path stays and session records are still read from it. That fallback is kept through 0.4.x and removed in 0.5.0. Sessions started by 0.3 ran in shared jails and cannot be continued.

The OpenRouter key is read from `openrouter_key_file` (default `~/.config/p0ssess/secrets/openrouter_key`, or `$P0SSESS_OPENROUTER_KEY_FILE`). It is passed to the proxy container through its environment and is never printed, logged, written elsewhere or baked into an image.

## The relay agent

`agents/p0ssess.md` is the possession agent: it runs on Haiku, in the background, with `omitClaudeMd: true` and only the routed MCP tools (`direct`, `run`, `wait`, `answer_permission`, `abort`, `models`, `recommend`). Every routed task goes through it; main Claude never calls those tools itself (a `PreToolUse` hook refuses them outside a subagent, see DESIGN.md, Agents). For the agent path it calls `run` (and `wait` while the run is going), then replies with the other model's answer in one `<answer>` block and a `via: <model> · <cost> · <session>` line, plus, only when present, `files:`, `diff: <diff_path> (<diff stat>)` (never the diff itself; Claude reads the file when it reviews), `session:`, `permission:`, `shell internet: on` and `note:`. Results also list the media and office files an edit run added to its worktree (`files`, regular files inside the worktree only).

The plugin installs it. Background subagents cannot ask for permission, and Claude Code ignores a project's `permissions.allow` in a folder you have not trusted, so the relay's tools must be allowed at user level: `p0ssess setup allow` adds those rules (with your consent, through `/p0ssess:p0ssess-me`). Named shortcut agents with a fixed model can be generated as well (see [ENGINE.md](ENGINE.md#shortcut-agents)).

On macOS, and on Linux without podman, the agent path reports itself unavailable; the direct tool and the engine still work.

## Tests

- `go test ./...` runs the unit tests: the contract test against the pinned OpenAPI document, the recorded event stream, the MCP protocol (progress, cancellation, stdin close), the key proxy and its deadline, the egress proxy's address policy and its CONNECT and plain-HTTP paths, the policy and nftables builders, the session rules, the jail ownership rules, the state migration (including concurrent processes and a failed move) and attachment checks.
- `P0SSESS_IT=1 P0SSESS_OPENROUTER_KEY_FILE=… [P0SSESS_IT_LAN=host:port,…] go test -run Integration -v ./internal/relay/` is the end-to-end check against real jails (under a cent in all). It checks:
  - **Egress**: a host service through pasta's 169.254.1.2 mapping, 100.100.100.100:53, `[fd7a:115c:a1e0::53]:53` and the `P0SSESS_IT_LAN` targets all fail from inside, while a control container with no filter reaches the host service and the LAN targets; `https://openrouter.ai` works.
  - **Identity**: your uid, no effective capabilities, an empty `/home`, no real key in the server's environment.
  - **A coding task** in edit mode completes, the diff has the change, and the main checkout is untouched; a follow-up runs in the same jail; ending the session removes the pod and its record, a later run is refused and `wait` returns the last result.
  - **Hard denies** in an allow-all session: `echo` runs; `sudo`, `su`, `ssh`, `scp`, `git push`, `docker`, `podman`, `systemctl`, `sh -c`, `bash -c`, `bash` and `curl … | sh` are all "Blocked by configuration policy", and so are writes to `.env`, `.ssh/config` and `.git/config`.
  - **Read mode** rejects an ask and the model reports it could not run the command.
  - **The jailed shell**: no password or key in the environment, `/proc/1/environ` unreadable, the API, the key proxy and the internet unreachable, an empty `/state`, the masks over other sessions' worktrees cannot be unmounted, and the repository is read-only.
  - **The read tool and webfetch**: literal reads of `/proc/1/environ` and `/state` and a webfetch to the loopback API are blocked by policy.
  - **Isolation**: two sessions in one repository get two jails; from inside the second, the first's transcript (a marker that is present in the first's own `/state`), worktree, git admin directory, the relay's state and the first's API are all out of reach, and a worktree symlink to the first's file finds nothing.
  - **Shell internet**: as described above, off and on.
  - **Startup and crash**: five cold starts and removals with and without shell internet (median, minimum and maximum are logged), and a jail whose owning process is killed with SIGKILL is removed by the next sweep.

Measured on the development machine (rootless podman 5.8, pasta, image cached): cold start median 0.65 s (0.64–0.66) without shell internet and 0.75 s (0.73–0.77) with it, removal 0.16–0.23 s.

## Known limits

- **The OpenCode password is in the server's environment, but a model cannot reach it usefully.** A shell command the model runs has its environment scrubbed, no network (or only the egress proxy, which refuses loopback), and an unreadable `/proc/1/environ` (the shell wrapper's user and network namespaces), so it can neither read the password nor call the API. The in-process read tool cannot read `/proc/1/environ` or `/state` through a literal path (the `read:/*` policy), and a worktree symlink to `/proc/1/environ` is refused by OpenCode's own binary-file guard (the environment is NUL-separated). OpenCode 2.2.5 still resolves the read permission resource lexically and then follows symlinks, so a worktree symlink can make the read tool open a text file elsewhere in the server container; with one jail per session, the only such files are the session's own database and the read-only repository, so no other session's state is reachable. Even a leaked password is unusable: webfetch to the loopback API is denied by policy and the relay tamper check aborts any reply it did not issue. The OpenRouter key is never in the server at all; it lives only in the proxy container.
- **The model can spend through the proxy** like it can through OpenCode. The per-run cost cap, the proxy's deadline and the key's own limit on OpenRouter bound this.
- **`data_collection: deny` narrows the providers** OpenRouter may use, and some of the cheapest endpoints drop out, so a run can cost more than the model's headline price.
- **SELinux relabeling**: with the default `selinux: relabel`, podman gives mounted repositories the shared `container_file_t` label. Host programs keep working. `restorecon -R <repo>` restores the default labels. Set `"selinux": "disable"` to run the containers with `label=disable` instead, which leaves your files alone but turns off SELinux separation for the jail.
- **OpenCode 2.2.5's durable session log** (`/api/experimental/session/{id}/log`) returns only its `log.synced` marker: its filter drops every session event. The relay uses that marker for the current sequence number, takes live events from `/api/event` (subscribed before each prompt, because the feed has no replay), and polls the session and its permission requests to catch anything missed across a reconnect.
- **Repositories whose `.git` is a file** (a worktree or a submodule) can be read but not edited: edit mode needs the `.git` directory.
- **An idle session holds memory.** Each jail's OpenCode server stays up for `session_idle_s` after its last run so a follow-up can continue it. Lower `session_idle_s` if many sessions pile up.
