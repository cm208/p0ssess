# Install

p0ssess is a Claude Code plugin with one Go binary behind it. This page covers installing it, the first run, and the binary. [INSTALL-NOTES.md](INSTALL-NOTES.md) lists everything it puts on your machine and how to remove each piece.

## Requirements

| | Needed for |
|---|---|
| Claude Code 2.1.319 or later | everything |
| An OpenRouter API key | any delegated call |
| Go 1.27 or later | only when no checked binary is available (see [The binary](#the-binary)) |
| Linux with rootless podman 5 (pasta), `nft` and git | the sandboxed agent path only |
| ffmpeg and ffprobe | optional: video size checks, keyframes, downscaling, splitting |

On macOS the direct path (video, audio, images, PDFs, one-shot jobs), the engine, the status line and the local page work. The agent path reports itself unavailable.

## Install the plugin

From the repository's marketplace:

```sh
claude plugin marketplace add cm208/p0ssess
claude plugin install p0ssess@p0ssess
```

The repository is at https://github.com/cm208/p0ssess, mirrored at https://git.christianmoo.re/cmoore/p0ssess. A fork or mirror works the same way: the URL can be a GitHub `owner/repo`, an `https://…/p0ssess.git` URL, or an SSH `git@host:owner/p0ssess.git` address. For an SSH server on a non-standard port, add a `Host` alias with that `Port` to `~/.ssh/config` and use the alias, because the `ssh://host:port/` form is not accepted.

From a local clone (Claude Code loads it in place, so a `git pull` updates it):

```sh
claude plugin marketplace add /path/to/p0ssess
claude plugin install p0ssess@p0ssess
```

For development, without installing anything:

```sh
claude --plugin-dir /path/to/p0ssess
```

## The OpenRouter key

p0ssess reads the key from `~/.config/p0ssess/secrets/openrouter_key` (one line, mode 0600). To use a key file you already have, set `P0SSESS_OPENROUTER_KEY_FILE`, or put `{"openrouter_key_file": "/path/to/key"}` in `~/.config/p0ssess/config.json`. The key is never printed, logged, or written anywhere else, and on the agent path only the proxy container holds it.

A key with a credit limit set on OpenRouter is a good second fence next to p0ssess's own monthly cap.

## First run

Start Claude Code and run `/p0ssess:p0ssess-me`. It prints a short summary (spend today and this month against the cap, your limits, what is running) and the address of the local page, where every setting is one click away. The defaults are a $10 monthly cap, a $0.02 per-task auto-approve limit and a paced daily limit; change any of them on the page or by telling Claude ("cap OpenRouter at $5", "auto-approve up to 10 cents a task").

When something is missing for quiet runs, the summary ends with a `setup` line and Claude offers that one step, making it only after you say yes:

- `p0ssess setup allow`: allow rules for the relay tools, the direct tool, the engine's read-only tools and `history_record_outcome` (it only appends to the local ledger), so delegation runs without permission prompts. `rules_add`, `approvals_add`, `settings_set` and the shortcut tools keep asking: their permission prompt is how you approve spending above your limits, so do not allow them. Background relay agents cannot ask for permission, so the agent path needs these. Plugins cannot ship allow rules.
- `p0ssess setup statusline`: wraps your status line. Your command keeps running first and the p0ssess segment follows. Plugins cannot install a status line.
- a missing key: where to put it.

Two more steps are yours to run when you want them: `p0ssess jail build` builds the sandbox image for the agent path (about 1.1 GB, Linux with podman), and shell internet for a project lets sandboxed shell commands download packages (off by default; [RELAY.md](RELAY.md#shell-internet)).

The same commands work from a shell. Inside Claude Code the plugin puts `p0ssess` on the Bash tool's PATH; from your own shell run the launcher by its path, `<plugin directory>/bin/p0ssess` (`claude plugin list` shows where the plugin is read from). `p0ssess setup show` reports what is set up, and `p0ssess setup undo` reverts every settings change p0ssess made.

## The binary

`bin/p0ssess` in the plugin is a small launcher script. It runs only a binary it built from the plugin's own source, or one whose SHA-256 matches `dist/SHA256SUMS` for this exact version (`dist/VERSION`). It looks in this order:

1. `$P0SSESS_BIN`, if set: your own binary, not checked (development);
2. the cached copy in `~/.cache/p0ssess/bin/<version>/`, which the launcher writes only after a check or a build;
3. `dist/p0ssess-<os>-<arch>` inside the plugin (release archives ship these), checked against `dist/SHA256SUMS` and copied to the cache;
4. a release download from the URL in `dist/release-url` (or `$P0SSESS_RELEASE_URL`), `<url>/v<version>/p0ssess-<os>-<arch>`, checked the same way;
5. a local build with Go: `CGO_ENABLED=0 go build -trimpath -mod=vendor` from the plugin's own source, with `go` on `PATH`, in `/usr/local/go` or in `~/.local/go`. Dependencies are vendored, so this needs no network and takes a few seconds.

A file that does not match its checksum is refused and never run. When nothing works the launcher says what it tried and how to fix it:

```
p0ssess: cannot start: no verified p0ssess 0.5.0 binary for linux/amd64, and none could be built.
Tried:
  - dist/p0ssess-linux-amd64: not in this plugin (a release archive ships it)
  - release download: no dist/release-url in this plugin
  - local build: Go is not installed (looked on PATH, in /usr/local/go and ~/.local/go)
Fix one of these, then start a new Claude Code session:
  ...
```

The checksums come with the plugin's source (they are committed in `dist/`), and the build is reproducible: the same commit and Go version produce byte-identical binaries, so anyone can rebuild a release and compare. [RELEASE.md](RELEASE.md) describes how releases are built and published.

Which way you get the binary depends on how you installed the plugin. A release archive (unpacked into a directory and added with `claude plugin marketplace add /path/to/p0ssess-<version>`) carries checked binaries and needs no Go. A clone of the repository carries the checksums but not the binaries, so it downloads them when the repository names its release URL and otherwise builds with Go. Claude Code installs marketplace plugins by cloning, and plugin conventions keep binaries out of git (they bloat every clone and cannot be reviewed), which is why the binaries live in release archives rather than in the repository.

## Updating

```sh
claude plugin marketplace update p0ssess
claude plugin update p0ssess@p0ssess
```

A new version gets its binary on first use, the same way as the first install. The status line's copy in `~/.config/p0ssess/bin/` is refreshed the next time the MCP server starts. From 0.3, see [INSTALL-NOTES.md](INSTALL-NOTES.md#upgrading-from-03) for what moves.

## Checking it end to end

`scripts/e2e-claude.sh DIR` runs the plugin in a real Claude Code session inside a scratch config directory, so your own `~/.claude` is untouched. It checks the prompt hint, a named-model request through the relay agent, a video on the direct path, a plain-language standing approval covering a repeat job, `/p0ssess:p0ssess-me` and the status line. It spends a few cents. The first run asks you to log in once inside the scratch config.
