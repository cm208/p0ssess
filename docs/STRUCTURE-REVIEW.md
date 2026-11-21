# Structure review (2026-11-21, at 1.1.0)

A review of the repository layout against Go and Claude Code plugin conventions. Nothing was moved or deleted. Checked with `git ls-files` (384 tracked files) and blob sizes; the packed history is about 10 MB.

What is already right: `cmd/` holds the two binaries (`p0ssess`, `launcher`), `internal/` holds every package, dependencies are vendored for offline builds, the plugin parts sit where Claude Code looks for them (`.claude-plugin/plugin.json` and `marketplace.json`, `agents/`, `commands/`, `skills/`, `hooks/hooks.json`), and build outputs (`build/`, `release/`, `site-docs-dist/`, `dist/p0ssess-*`) are ignored and untracked. The release binaries live on the releases page, not in git.

Since Claude Code installs a marketplace plugin by cloning the repository, everything tracked ships to every user. Several findings follow from that.

## High

1. **Personal details in public docs.**
   What: `docs/REVIEW-2026-11-09.md` (line 37) names the commit author's personal email; `docs/INSTALL.md` (line 37) links a mirror on a personal domain, and `docs/REVIEW-2026-11-09.md` (line 317) repeats that address.
   Why: the repository is public and the support address is `support@p0ssess.me`.
   Change: drop the mirror sentence from INSTALL.md; replace the email line in the review, or move the review out (see 6).
   Risk/effort: none; minutes. The docs site (`site-docs-dist/`) is built from these files, so rebuild it after.

2. **`.p0ssess/wt/` is excluded only on this machine.**
   What: p0ssess writes scratch worktrees under `<repo>/.p0ssess/wt/`. Here they are hidden by `.git/info/exclude`, which is not committed. In any other clone (a contributor's, or a user running p0ssess on this repo) they show up as untracked files, and a stray `git add .` would commit a whole worktree.
   Why: `.p0ssess/rules.toml` is a legitimate tracked project file, so the directory cannot be ignored wholesale, but `wt/` should never be tracked anywhere.
   Change: add `/.p0ssess/wt/` to `.gitignore`. Consider having p0ssess write its own `.p0ssess/.gitignore` containing `wt/` when it creates the directory in a user's repository.
   Risk/effort: none; one line.

3. **No CI.**
   What: there is no `.github/workflows/`. The release build, `scripts/build-release.sh --check` (which verifies the committed `bin/p0ssess.exe` byte for byte), `go vet` and `go test ./...` run only by hand.
   Why: a public plugin that refuses unverified binaries depends on `dist/SHA256SUMS`, `dist/VERSION`, the launcher's `version=` and `plugin.json` agreeing. A CI check catches a mismatch before users do.
   Change: one workflow on push and pull request: `go vet ./...`, `go test ./...` (without `P0SSESS_E2E`), `scripts/build-release.sh --check`, and a version-consistency check across `bin/p0ssess`, `dist/VERSION` and `.claude-plugin/plugin.json`.
   Risk/effort: low; an hour or two.

## Medium

4. **The website ships inside the plugin.**
   What: `site/` is 65 tracked files, about 2 MB: 30 PNG screenshots in `site/shots/`, fonts, `og.png`, a vendored Polar checkout script and the docs-site builder (`site/docs-build/`). None of it is used by the plugin or the binary.
   Why: every install and update clones it; plugin repositories conventionally carry only what the plugin runs. Every new set of screenshots also grows the history for all users.
   Change: move the site to its own repository (or an orphan `site` branch the marketplace does not clone), keeping `docs/` here as the docs source. At minimum, stop committing regenerated `site/shots/` (they come from `site/shoot.js`).
   Risk/effort: low risk, medium effort (the site deploy and `site/docs-build/config.toml` paths change).

5. **Go module path is `p0ssess`, not `github.com/cm208/p0ssess`.**
   What: `go.mod` declares `module p0ssess`.
   Why: works for the launcher's local build, but `go install github.com/cm208/p0ssess/cmd/p0ssess@v1.1.0` and pkg.go.dev cannot resolve it, and it is not the conventional form for a public module.
   Change: rename the module to `github.com/cm208/p0ssess` and rewrite imports (`gofmt -r` or `sed`), then confirm `scripts/build-release.sh --check` still produces byte-identical binaries (the module path ends up in the binary, so the next release's checksums change once).
   Risk/effort: low risk, under an hour; do it at a release boundary.

6. **Historical and internal material in `docs/`.**
   What: `docs/REVIEW-2026-11-09.md` is a review of 0.2.0, kept for history and published on the docs site next to the user docs.
   Why: user-facing docs should be current; history reads better in a changelog or an archive folder.
   Change: move it to `docs/history/` (and out of the docs-site navigation), or fold its outcome into a changelog (see 8).
   Risk/effort: none; minutes, plus fixing links in `site/docs-build/config.toml` if listed there.

7. **Unused asset.**
   What: `assets/polar-avatar.png` is referenced nowhere in the code, the site or the docs.
   Why: `assets/` is what the binary embeds (`data.go`); a store avatar belongs with the business material.
   Change: move it out of the repository.
   Risk/effort: none.

8. **No CHANGELOG, SECURITY or CONTRIBUTING file.**
   What: release notes live outside this repository; there is no security policy or contribution note.
   Why: GitHub surfaces `SECURITY.md` in the Security tab, and a public tool that sandboxes other models will get security reports. A `CHANGELOG.md` lets users see what an update changes without leaving Claude Code.
   Change: add `CHANGELOG.md` (one section per release, from the release notes), `SECURITY.md` (report to `support@p0ssess.me`, what is in scope: the sandbox, the key proxy, the launcher's checksum check), and a short `CONTRIBUTING.md` (build with `-mod=vendor`, how to run tests, that `e2e/` spends real money).
   Risk/effort: none; an hour.

## Low

9. **`bin/p0ssess.exe` is a 3 MB binary in git.** It is deliberate and documented in `docs/INSTALL.md` (without it a Windows clone cannot start), reproducible, and checked by `build-release.sh --check`. Keep it; Git LFS is not an option because marketplace clones may not fetch LFS objects. Just keep `cmd/launcher` stable, since each change adds 3 MB to every clone's history.

10. **`internal/pme/table/default.json` is 1.2 MB and grows the history on every refresh** (several versions in history already). Consider shipping it compressed (gzip and decompress at load) or as a compact signed binary payload. Low risk, small effort.

11. **`data.go` at the repository root** exists only so `go:embed` can reach `assets/`. That is a normal pattern, but `assets/assets.go` (package `assets`) would keep the root to manifests and docs. Cosmetic; only worth it alongside 5.

12. **`site-docs-dist/` is a top-level build output.** It is ignored, so nothing ships, but generated files usually live under one ignored directory (`build/docs/`, say) so the root lists only sources.

13. **`commands/` versus `skills/`.** The five slash commands are in `commands/`, which Claude Code supports. Newer plugin guidance leans toward `skills/<name>/SKILL.md` for the same job; consider moving them when touching them next, after checking that `/p0ssess:p0ssess-me` and the other names stay the same.

14. **Local leftovers (not tracked).** `dist/p0ssess-linux-amd64` is a 10 MB build from 2026-11-10 and `build/p0ssess-old` an older binary. Both are ignored and never ship, but the stale `dist/` one does not match the 1.1.0 checksums and could confuse a local test of the launcher. Delete when convenient.
