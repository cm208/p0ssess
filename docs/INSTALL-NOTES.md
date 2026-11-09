# Install notes

What a p0ssess install puts on a machine, and how to remove each piece. Everything is per user: rootless podman, a `systemd --user` template unit and files under `~/.config/p0ssess`. Nothing needs root, and nothing touches an existing OpenCode install (`~/.opencode`, `~/.config/opencode`, `opencode serve --service`).

## Install

Requirements: podman 5 with pasta (rootless), `nft` (nftables) on the host, git, Go 1.27 to build.

```sh
CGO_ENABLED=0 go build -trimpath -o bin/p0ssess ./cmd/p0ssess
mkdir -p ~/.config/p0ssess/bin && chmod 700 ~/.config/p0ssess
cp bin/p0ssess ~/.config/p0ssess/bin/p0ssess.new && mv ~/.config/p0ssess/bin/p0ssess.new ~/.config/p0ssess/bin/p0ssess
~/.config/p0ssess/bin/p0ssess jail build          # downloads and verifies OpenCode 2.2.5, builds the image
```

Replace the binary by renaming a new file over it, as above: a running jail's proxy container keeps the old file open, and copying onto it fails with "Text file busy".

Put the OpenRouter key in `~/.config/p0ssess/secrets/openrouter_key` (mode 0600), or point `openrouter_key_file` in `~/.config/p0ssess/config.json` at an existing key file:

```json
{ "openrouter_key_file": "/path/to/openrouter_key" }
```

Optional systemd unit, one instance per repository:

```sh
mkdir -p ~/.config/systemd/user
~/.config/p0ssess/bin/p0ssess jail unit > ~/.config/systemd/user/p0ssess-jail@.service
systemctl --user daemon-reload
systemctl --user start "p0ssess-jail@$(systemd-escape --path /path/to/repo).service"
# enable it the same way to start the jail at login
```

The unit is a oneshot that runs `p0ssess jail up` and `p0ssess jail down`. Without it, the relay starts a repository's jail on its first `run`.

## What gets installed

| Item | Where | Created by |
|---|---|---|
| p0ssess binary | `~/.config/p0ssess/bin/p0ssess` | you (above) |
| settings | `~/.config/p0ssess/config.json` | you, optional |
| jail image | `localhost/p0ssess-opencode:2.2.5-1` (about 1.1 GB: Fedora 44 base, git, Python, ripgrep, OpenCode) | `p0ssess jail build` |
| base image | `registry.fedoraproject.org/fedora:44@sha256:ead52905…` | pulled by `jail build` if it is not already present |
| pods | `p0ssess-<hash>` with containers `-infra`, `-proxy`, `-server`, one per repository | `p0ssess jail up` or the first `run` |
| volumes | `p0ssess-<hash>-state` (OpenCode's sessions for that repository) | same |
| jail records | `~/.config/p0ssess/jails/<pod>.json` and `.pw` (the password) | same |
| relay state | `~/.config/p0ssess/sessions/`, `runs/`, `history.jsonl` | the relay |
| systemd unit | `~/.config/systemd/user/p0ssess-jail@.service` | you, optional |
| in each repository used | `.p0ssess/wt/` (edit-mode worktrees), `.git/worktrees/<name>`, one line `/.p0ssess/` in `.git/info/exclude`; with `selinux: relabel`, the shared `container_file_t` SELinux label on the repository's files | the relay |

## Remove

Stop and remove one repository's jail, keeping its sessions:

```sh
~/.config/p0ssess/bin/p0ssess jail down --repo /path/to/repo
```

Remove it with its sessions and password:

```sh
~/.config/p0ssess/bin/p0ssess jail purge --repo /path/to/repo
```

Remove everything:

```sh
# 1. units
for u in $(systemctl --user list-units --all --plain --no-legend 'p0ssess-jail@*' | awk '{print $1}'); do
  systemctl --user disable --now "$u"
done
rm -f ~/.config/systemd/user/p0ssess-jail@.service && systemctl --user daemon-reload

# 2. pods, volumes, image
podman pod rm -f $(podman pod ls -q --filter label=p0ssess=1)
podman volume rm $(podman volume ls -q --filter label=p0ssess=1)
podman rmi localhost/p0ssess-opencode:2.2.5-1
podman image prune -f            # build layers left by jail build

# 3. files
rm -rf ~/.config/p0ssess

# 4. in each repository that was used (check for worktrees you still want first)
git worktree list
for w in .p0ssess/wt/*; do git worktree remove --force "$w"; done
git worktree prune && rm -rf .p0ssess
sed -i '\#^/\.p0ssess/$#d' .git/info/exclude
restorecon -R .                   # only if SELinux relabeling was on
```

Keep the base image if other containers use it; otherwise `podman rmi registry.fedoraproject.org/fedora:44`.
