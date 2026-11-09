# Releases

A p0ssess release is a tagged commit plus files built from it: one static binary per platform, their SHA-256 sums, the licenses, and a plugin archive with the checked binaries inside. The launcher (`bin/p0ssess`) runs a binary only if it matches `dist/SHA256SUMS` for its own version; see [INSTALL.md](INSTALL.md#the-binary).

## What a release contains

`scripts/build-release.sh` builds everything from `git archive HEAD`, never from the work tree, with `CGO_ENABLED=0 go build -trimpath -buildvcs=false -mod=vendor -ldflags "-s -w -buildid= -X main.version=<version>"`. The same commit and Go version give byte-identical files.

| File | What it is |
|---|---|
| `p0ssess-linux-amd64`, `p0ssess-linux-arm64`, `p0ssess-darwin-arm64` | the static binaries |
| `SHA256SUMS` | sums of the binaries and the archive |
| `LICENSE`, `NOTICE`, `LICENSE.BurntSushi-toml`, `LICENSE.yaml.v3` | p0ssess's license and the notices of the vendored code the binaries carry |
| `p0ssess-<version>.tar.gz` | the plugin at the tagged commit, with the three binaries in `dist/` next to `dist/SHA256SUMS` and `dist/VERSION`. Unpacked, it is a plugin that needs no Go: `claude plugin marketplace add /path/to/p0ssess-<version>` |

In the repository, `dist/SHA256SUMS` (the binaries only) and `dist/VERSION` (the version, then the Go version that built it) are committed for each release, so a clone carries the checksums its launcher checks against. `dist/release-url`, when present, holds the base URL the launcher downloads from: `<url>/v<version>/p0ssess-<os>-<arch>`. Gitea and GitHub both serve release files at `https://<host>/<owner>/<repo>/releases/download/<tag>/<file>`, so the base URL is `https://<host>/<owner>/p0ssess/releases/download`.

## Cutting a release

Needs Go 1.27 or later, git, GNU tar, gzip and sha256sum. Use the same Go version for every step.

1. Bump the version in `bin/p0ssess` (`version=`), `.claude-plugin/plugin.json` and `cmd/p0ssess/main.go`; update the docs; commit. `go test ./...` checks that the three agree.
2. Check reproducibility: `scripts/build-release.sh --check` builds twice (with a clean build cache in between) and compares.
3. Write the checksums: `scripts/build-release.sh sums` builds from `HEAD` and writes `dist/SHA256SUMS` and `dist/VERSION`. Commit them (`git add dist/SHA256SUMS dist/VERSION`). The binaries do not depend on `dist/`, so this commit does not change them.
4. Tag: `git tag -a v<version> -m "p0ssess <version>"`, then push `main` and the tag.
5. Build the release: `scripts/build-release.sh` builds from the tagged `HEAD` into `release/<version>/` and refuses unless the binaries match the committed `dist/SHA256SUMS`.
6. Publish `release/<version>/*` as the release for the tag, below.

### On Gitea (now)

In the web UI: the repository's **Releases**, **New release**, choose the tag `v<version>`, title `p0ssess <version>`, notes, then attach every file in `release/<version>/`. The same over the API, with a token that has repository write access in `$GITEA_TOKEN`:

```sh
api=https://<gitea host>/api/v1/repos/<owner>/p0ssess
id=$(curl -fsS -H "Authorization: token $GITEA_TOKEN" -H 'Content-Type: application/json' \
  -d '{"tag_name":"v<version>","name":"p0ssess <version>","body":"<notes>"}' "$api/releases" | jq .id)
for f in release/<version>/*; do
  curl -fsS -H "Authorization: token $GITEA_TOKEN" -F "attachment=@$f" "$api/releases/$id/assets?name=$(basename "$f")"
done
```

While the repository is private, release files need a login to download, so leave `dist/release-url` out: installs use the release archive (which carries the binaries) or build with Go.

### On GitHub (later)

Once the repository is public on GitHub:

```sh
gh release create v<version> release/<version>/* --title "p0ssess <version>" --notes-file <notes>
```

Then commit `dist/release-url` with `https://github.com/<owner>/p0ssess/releases/download`, so a plain clone downloads and checks the binary instead of needing Go. Do this in the release commit of step 3 for the next release, or as its own commit after the release is published. A download that fails or does not match falls through to a Go build, so the URL can be committed before the files are published without breaking anything.

## Checking a release

Anyone can check a published binary against the source:

```sh
git checkout v<version>
scripts/build-release.sh sums      # with the Go version on the second line of dist/VERSION
git diff --exit-code dist/SHA256SUMS
sha256sum -c --ignore-missing <downloaded SHA256SUMS>
```
