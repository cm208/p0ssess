# Install

p0ssess is a Claude Code plugin with one Go binary behind it. This page covers installing it, the first run, and the binary. [INSTALL-NOTES.md](INSTALL-NOTES.md) lists everything it puts on your machine and how to remove each piece.

## Requirements

| | Needed for |
|---|---|
| Claude Code 2.1.319 or later | everything |
| An OpenRouter API key | any delegated call |
| Go 1.27 or later | building the binary on first use (see [The binary](#the-binary)) |
| Linux with rootless podman 5 (pasta), `nft` and git | the sandboxed agent path only |
| ffmpeg and ffprobe | optional: video size checks, keyframes, downscaling, splitting |

On macOS the direct path (video, audio, images, PDFs, one-shot jobs), the engine, the status line and the local page work. The agent path reports itself unavailable.

## Install the plugin

From the repository's marketplace:

```sh
claude plugin marketplace add <repository URL>
claude plugin install p0ssess@p0ssess
```

The repository URL can be a GitHub `owner/repo`, an `https://…/p0ssess.git` URL, or an SSH `git@host:owner/p0ssess.git` address. For an SSH server on a non-standard port, add a `Host` alias with that `Port` to `~/.ssh/config` and use the alias, because the `ssh://host:port/` form is not accepted.

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

Start Claude Code and run `/p0ssess:p0ssess-me`. It:

1. shows spend, your plan, privacy and whether a key was found;
2. asks for the plan (subscription or API), the monthly cap, the per-task auto-approve limit, the daily limit, privacy and the pressure curve;
3. offers each change to Claude Code's settings, one at a time, and makes it only after you say yes:
   - `p0ssess setup allow`: allow rules for the relay tools, the direct tool, the engine's read-only tools and `history_record_outcome` (it only appends to the local ledger). `rules_add`, `approvals_add`, `settings_set` and the shortcut tools keep asking: their permission prompt is how you approve spending above your limits, so do not allow them. Background relay agents cannot ask for permission, so the agent path needs these. Plugins cannot ship allow rules.
   - `p0ssess setup statusline`: wraps your status line. Your command keeps running first and the p0ssess segment follows. Plugins cannot install a status line.
   - `p0ssess jail build`: builds the sandbox image for the agent path (about 1.1 GB), on Linux with podman.
4. lists your rules and suggestions, and opens the local page.

The same commands work from a shell. Inside Claude Code the plugin puts `p0ssess` on the Bash tool's PATH; from your own shell run the launcher by its path, `<plugin directory>/bin/p0ssess` (`claude plugin list` shows where the plugin is read from). `p0ssess setup show` reports what is set up, and `p0ssess setup undo` reverts every settings change p0ssess made.

## The binary

`bin/p0ssess` in the plugin is a small launcher script. On first use it finds or makes the binary for your platform, in this order:

1. `$P0SSESS_BIN`, if set (development);
2. `dist/p0ssess-<os>-<arch>` inside the plugin, for release archives that ship binaries;
3. the cached build in `~/.cache/p0ssess/bin/<version>/`;
4. a release download, used only when the plugin has `dist/release-url` and `dist/SHA256SUMS`, and only when the file matches its checksum;
5. a local build with Go: `CGO_ENABLED=0 go build -trimpath` from the plugin's own source. Dependencies are vendored, so this needs no network and takes a few seconds.

The build is reproducible: the same source and Go version produce byte-identical binaries. `scripts/build-release.sh` builds linux/amd64, linux/arm64 and darwin/arm64 with their SHA-256 sums, and `scripts/build-release.sh --check` builds twice and compares.

Why build locally by default: Claude Code installs a plugin by cloning its repository, and plugin conventions keep binaries out of git (they bloat every clone and cannot be reviewed). A local build from reviewed source needs nothing but Go and no network. The tradeoff is the Go requirement; users without Go need a release that ships `dist/` binaries, or a downloaded binary checked against `SHA256SUMS`.

## Updating

```sh
claude plugin marketplace update p0ssess
claude plugin update p0ssess@p0ssess
```

A new version builds its binary on first use. The status line's copy in `~/.config/p0ssess/bin/` is refreshed the next time the MCP server starts.

## Checking it end to end

`scripts/e2e-claude.sh DIR` runs the plugin in a real Claude Code session inside a scratch config directory, so your own `~/.claude` is untouched. It checks the prompt hint, a named-model request through the relay agent, a video on the direct path, a plain-language standing approval covering a repeat job, `/p0ssess:p0ssess-me` and the status line. It spends a few cents. The first run asks you to log in once inside the scratch config.
