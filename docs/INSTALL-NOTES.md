# Install notes

What a p0ssess install puts on a machine, and how to remove each piece. Everything is per user. Nothing needs root, nothing touches an existing OpenCode install (`~/.opencode`, `~/.config/opencode`), and nothing in Claude Code's settings changes unless you approve it in `/p0ssess:p0ssess-me` (or run `p0ssess setup` yourself).

`$CLAUDE_CONFIG_DIR` below is `~/.claude` unless you set it. With `P0SSESS_HOME` set, `~/.config/p0ssess` is that directory, and the cache and state directories are its `cache/` and `state/`.

## What gets installed

| Item | Where | Created by |
|---|---|---|
| the plugin (a clone of the repository) | `$CLAUDE_CONFIG_DIR/plugins/cache/p0ssess/p0ssess/<version>/`, plus the marketplace clone in `$CLAUDE_CONFIG_DIR/plugins/marketplaces/` | `claude plugin install` |
| marketplace and enabled-plugin entries | `extraKnownMarketplaces` and `enabledPlugins` in `$CLAUDE_CONFIG_DIR/settings.json` | `claude plugin marketplace add`, `claude plugin install` |
| the p0ssess binary | `~/.cache/p0ssess/bin/<version>/p0ssess-<os>-<arch>` | the launcher, on first use |
| the OpenRouter key | `~/.config/p0ssess/secrets/openrouter_key` | you |
| settings | `~/.config/p0ssess/budget.toml`, `engine.toml`, `rules.toml`, optional `config.json` | `/p0ssess:p0ssess-me`, `settings_set`, the local page, rules you confirm |
| project rules | `<repo>/.p0ssess/rules.toml` | project-scoped rules you confirm |
| the ledger | `~/.local/state/p0ssess/history.jsonl` (offers, outcomes, measured runs; never prompt content), session rules in `~/.local/state/p0ssess/sessions/` | the engine, the relay, the direct path |
| caches | `~/.cache/p0ssess/usage.json` (Claude usage readings), `openrouter-models.json` (public model list), `running/` (running jobs), `registry-*.json` (when the binary runs outside the plugin) | the status line, the engine, the relay and direct path |
| allow rules | 13 `mcp__plugin_p0ssess_p0ssess__…` entries in `permissions.allow` of `$CLAUDE_CONFIG_DIR/settings.json` | `p0ssess setup allow`, with your consent |
| status line wrapper | `statusLine` in `$CLAUDE_CONFIG_DIR/settings.json` points at `~/.config/p0ssess/bin/p0ssess`; your previous command is kept in `engine.toml` as `statusline.command` | `p0ssess setup statusline`, with your consent |
| progress-summary switch | `env.CLAUDE_CODE_DISABLE_AGENT_PROGRESS_SUMMARIES` in `$CLAUDE_CONFIG_DIR/settings.json` | `p0ssess setup summaries`, only if you ask |
| setup record and backups | `~/.config/p0ssess/setup.json`; the settings file as it was before each change in `~/.config/p0ssess/backups/` | `p0ssess setup` |
| shortcut agents | `$CLAUDE_CONFIG_DIR/agents/p0ssess-<name>.md` (at most 12, each marked as made by p0ssess) | `shortcuts_create`, after you agree |
| the agent path (Linux): | | |
| jail image | `localhost/p0ssess-opencode:2.2.5-1` (about 1.1 GB: Fedora 44 base, git, Python, ripgrep, OpenCode) and the pinned `registry.fedoraproject.org/fedora:44` base | `p0ssess jail build` |
| pods and volumes | `p0ssess-<hash>` pods (`-infra`, `-proxy`, `-server`) and `p0ssess-<hash>-state` volumes, one per repository; `-proxy` and `-server` mount the `p0ssess` binary read-only at `/opt/p0ssess` (the proxy, and the jailed shell wrapper configured as OpenCode's shell) | the first relay run in a repository |
| relay state | `~/.config/p0ssess/jails/` (records and passwords), `sessions/`, `runs/` (transcripts and diffs), `history.jsonl` (the relay's run log) | the relay |
| optional systemd unit | `~/.config/systemd/user/p0ssess-jail@.service` | you (`p0ssess jail unit`) |
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

Stop one repository's jail, keeping its sessions: `p0ssess jail down --repo /path/to/repo`. Remove it with its sessions and password: `p0ssess jail purge --repo /path/to/repo`. Remove all of it:

```sh
for u in $(systemctl --user list-units --all --plain --no-legend 'p0ssess-jail@*' | awk '{print $1}'); do
  systemctl --user disable --now "$u"
done
rm -f ~/.config/systemd/user/p0ssess-jail@.service && systemctl --user daemon-reload

podman pod rm -f $(podman pod ls -q --filter label=p0ssess=1)
podman volume rm $(podman volume ls -q --filter label=p0ssess=1)
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
