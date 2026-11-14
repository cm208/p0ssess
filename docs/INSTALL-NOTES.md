# Install notes

What a p0ssess install puts on a machine, and how to remove each piece. Everything is per user. Nothing needs root, nothing touches an existing OpenCode install (`~/.opencode`, `~/.config/opencode`), and nothing in Claude Code's settings changes unless you approve it in `/p0ssess:p0ssess-me` (or run `p0ssess setup` yourself).

`$CLAUDE_CONFIG_DIR` below is `~/.claude` unless you set it. With `P0SSESS_HOME` set, `~/.config/p0ssess` is that directory, and the cache and state directories are its `cache/` and `state/`.

## What gets installed

| Item | Where | Created by |
|---|---|---|
| the plugin (a clone of the repository) | `$CLAUDE_CONFIG_DIR/plugins/cache/p0ssess/p0ssess/<version>/`, plus the marketplace clone in `$CLAUDE_CONFIG_DIR/plugins/marketplaces/` | `claude plugin install` |
| marketplace and enabled-plugin entries | `extraKnownMarketplaces` and `enabledPlugins` in `$CLAUDE_CONFIG_DIR/settings.json` | `claude plugin marketplace add`, `claude plugin install` |
| the p0ssess binary | `~/.cache/p0ssess/bin/<version>/p0ssess-<os>-<arch>` (`.exe` on Windows) | the launcher, on first use (checked against `dist/SHA256SUMS`, or built from source) |
| the OpenRouter key | `~/.config/p0ssess/secrets/openrouter_key` | you |
| settings | `~/.config/p0ssess/budget.toml` (including the shell internet projects), `engine.toml`, `rules.toml`, optional `config.json` | `/p0ssess:p0ssess-me`, `settings_set`, the local page, rules you confirm |
| project rules | `<repo>/.p0ssess/rules.toml` | project-scoped rules you confirm |
| the ledger | `~/.local/state/p0ssess/history.jsonl` (offers, outcomes, measured runs; never prompt content), session rules in `~/.local/state/p0ssess/sessions/` | the engine, the relay, the direct path |
| caches | `~/.cache/p0ssess/usage.json` (Claude usage readings), `route-table.json` (the last signed route table that verified), `engine-offline` (present while the engine service cannot be reached), `running/` (running jobs) | the status line, the engine, the relay and direct path |
| engine service state | `~/.local/state/p0ssess/install-token` (a random token for rate limits), `outcomes.json` (which outcome records were sent), `outcomes-refused.jsonl` (records the service refused, if any); with a linked account also `~/.config/p0ssess/secrets/p0ssess_token`, `~/.local/state/p0ssess/account.json`, `calibration/`, and for a team `team-policy.json` and `team-spend.jsonl` | the engine client, the outcome sender, `p0ssess account` and `p0ssess team` |
| allow rules | 13 `mcp__plugin_p0ssess_p0ssess__…` entries in `permissions.allow` of `$CLAUDE_CONFIG_DIR/settings.json` | `p0ssess setup allow`, with your consent |
| status line wrapper | `statusLine` in `$CLAUDE_CONFIG_DIR/settings.json` points at `~/.config/p0ssess/bin/p0ssess` (`p0ssess.exe` on Windows); your previous command is kept in `engine.toml` as `statusline.command` | `p0ssess setup statusline`, with your consent |
| progress-summary switch | `env.CLAUDE_CODE_DISABLE_AGENT_PROGRESS_SUMMARIES` in `$CLAUDE_CONFIG_DIR/settings.json` | `p0ssess setup summaries`, only if you ask |
| setup record and backups | `~/.config/p0ssess/setup.json`; the settings file as it was before each change in `~/.config/p0ssess/backups/` | `p0ssess setup` |
| file locks (Windows only) | `%TEMP%\p0ssess-locks\`: empty lock files, because Windows cannot lock a directory | the binary, while it rewrites a shared file |
| shortcut agents | `$CLAUDE_CONFIG_DIR/agents/p0ssess-<name>.md` (at most 12, each marked as made by p0ssess) | `shortcuts_create`, after you agree |
| the agent path (Linux): | | |
| jail image | `localhost/p0ssess-opencode:2.2.5-1` (about 1.1 GB: Fedora 44 base, git, Python, ripgrep, OpenCode) and the pinned `registry.fedoraproject.org/fedora:44` base | `p0ssess jail build` |
| pods | one `p0ssess-<random>` pod per relay session (`-infra`, `-proxy`, `-server`, and `-egress` with shell internet), removed when the session ends or idles out, or by the next start after a crash; the containers mount the `p0ssess` binary read-only at `/opt/p0ssess`. No volumes. | each relay session |
| relay state | `~/.local/state/p0ssess/relay/`: `sessions/`, `runs/` (transcripts and diffs), `jails/` (each running jail's record, password and sockets), `relay-runs.jsonl` (the relay's run log) | the relay |
| in each repository used | `.p0ssess/wt/` (edit-mode worktrees), `.git/worktrees/<name>`, a `/.p0ssess/` line in `.git/info/exclude`; with SELinux relabeling on, the shared `container_file_t` label on the repository's files | the relay |

## Remove

The launcher is `<plugin directory>/bin/p0ssess`; `claude plugin list` shows the plugin directory. Inside Claude Code, `p0ssess` is on the Bash tool's PATH.

### 1. Revert the settings changes

```sh
p0ssess setup undo
```

This removes the allow rules p0ssess added, restores your previous status line (or removes it if you had none) and the progress-summary switch. The backups in `~/.config/p0ssess/backups/` hold each earlier version of the file.

### 2. Shortcut agents

```sh
rm -f "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"/agents/p0ssess-*.md
```

Check first that you have no agents of your own named `p0ssess-*`; the ones p0ssess made contain the line `made by p0ssess`.

### 3. The agent path (Linux)

`p0ssess jail status` lists running jails, `p0ssess jail down --name <pod>` removes one, and `p0ssess jail sweep` removes every jail whose owning process is gone. Remove all of it (end your Claude Code sessions first, so no run is active):

```sh
podman pod rm -f $(podman pod ls -q --filter label=p0ssess=1)
podman volume rm $(podman volume ls -q --filter label=p0ssess=1)   # only 0.3 left volumes
podman rmi localhost/p0ssess-opencode:2.2.5-1
podman image prune -f            # build layers left by jail build
```

Keep the base image if other containers use it; otherwise `podman rmi registry.fedoraproject.org/fedora:44`.

In each repository that was used (check for worktrees you still want first):

```sh
git worktree list
for w in .p0ssess/wt/*; do git worktree remove --force "$w"; done
git worktree prune && rm -rf .p0ssess
sed -i '\#^/\.p0ssess/$#d' .git/info/exclude
restorecon -R .                   # only if SELinux relabeling was on
```

### 4. The plugin

```sh
claude plugin uninstall p0ssess@p0ssess
claude plugin marketplace remove p0ssess
```

### 5. p0ssess's own files

```sh
rm -rf ~/.config/p0ssess ~/.cache/p0ssess ~/.local/state/p0ssess
```

This deletes your key file, settings, rules and history. Project rules live in each repository's `.p0ssess/`.

## Upgrading from 0.6

Install and update the same way; there is nothing to set up. Your key, settings, rules, budgets, approvals and history carry over.

- **Routing uses the hosted engine.** The scoring code, the model registry and the catalog left the plugin. `recommend` now asks the p0ssess engine service (`https://cloud.p0ssess.me`) to rank routes, and every limit (budgets, rules, approvals, the monthly cap) is still enforced on your machine. When the service cannot be reached, p0ssess routes from a signed table of default routes, downloaded once a day and checked against a key built into the binary, and the status line ghost turns a dimmer grey. [README.md](../README.md#privacy) lists exactly what a request sends; never your prompt, files or file names.
- **Anonymous outcome records are on.** At the end of a session p0ssess sends one short record per finished delegated run (kind, model, route, stakes, outcome, cost bucket, week). The local page shows the pending ones first. To turn them off, tell Claude "turn off outcome records" or set `outcomes.send = false` under `[outcomes]` in `~/.config/p0ssess/budget.toml`.
- **`p0ssess engine refresh`** fetches the signed route table instead of the OpenRouter model list. The old `openrouter-models.json` and `registry-*.json` files in `~/.cache/p0ssess/` are no longer read and can be deleted.
- **The 0.6.0 engine stays MIT.** Releases up to 0.6.0, with the engine, its registry and its catalog, remain in this repository's history and on the releases page. `git checkout v0.6.0` gives the fully local version.

## Upgrading from 0.3

- **Relay state moves** from `~/.config/p0ssess/` to `~/.local/state/p0ssess/relay/` on the first start of 0.4 (`sessions/`, `runs/`, `history.jsonl` → `relay-runs.jsonl`, `jails/` → `legacy-jails/`), under a lock and only where the new path does not exist yet. If a move fails, the old files stay and are still read through 0.4.x.
- **One jail per session** replaces 0.3's one jail per repository. Sessions started by 0.3 cannot be continued. Once no 0.3 process runs (end Claude Code sessions started before the upgrade), remove 0.3's shared pods and their volumes with `p0ssess jail sweep --legacy`, and `rm -rf ~/.local/state/p0ssess/relay/legacy-jails`.
- **The systemd unit is gone** (0.4 has no long-lived jails). If you installed it:

  ```sh
  for u in $(systemctl --user list-units --all --plain --no-legend 'p0ssess-jail@*' | awk '{print $1}'); do
    systemctl --user disable --now "$u"
  done
  rm -f ~/.config/systemd/user/p0ssess-jail@.service && systemctl --user daemon-reload
  ```
- **The allow-list tool names did not change**, so `p0ssess setup allow` needs no rerun.
