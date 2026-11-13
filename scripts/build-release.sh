#!/bin/sh
# Builds a p0ssess release from the committed source (git archive HEAD), never from the work tree.
# The build is reproducible: the same commit and Go version give byte-identical files, so anyone can
# check a published binary against the source. docs/RELEASE.md has the whole procedure.
#
#   scripts/build-release.sh sums     build the binaries and write dist/SHA256SUMS and dist/VERSION,
#                                     to be committed before the release is tagged
#   scripts/build-release.sh          build release/<version>/: the binaries, SHA256SUMS, the licenses
#                                     and p0ssess-<version>.tar.gz (the plugin with checked binaries in
#                                     dist/); refuses unless HEAD's dist/SHA256SUMS matches the build
#   scripts/build-release.sh --check  build twice and compare, and check bin/p0ssess.exe against its source
#   scripts/build-release.sh launcher build the Windows launcher (cmd/launcher) into bin/p0ssess.exe,
#                                     to be committed when cmd/launcher changes
#
# Needs Go 1.27 or later, git, GNU tar, gzip and sha256sum.
set -eu
cd "$(dirname "$0")/.."
version=$(sed -n 's/^version=//p' bin/p0ssess)
targets="linux/amd64 linux/arm64 darwin/arm64 windows/amd64"

die() { echo "build-release: $*" >&2; exit 1; }

[ -n "$version" ] || die "no version in bin/p0ssess"
grep -q "\"version\": \"$version\"" .claude-plugin/plugin.json || die ".claude-plugin/plugin.json is not at $version"
grep -q "^var version = \"$version\"" cmd/p0ssess/main.go || die "cmd/p0ssess/main.go is not at $version"
if [ -n "$(git status --porcelain -- '*.go' go.mod go.sum vendor bin data.go assets internal/pme/table/default.json)" ]; then
  echo "build-release: note: uncommitted changes are not in the build (it uses HEAD)" >&2
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

# export SRC: HEAD's tree into SRC.
export_head() {
  mkdir -p "$1"
  git archive --format=tar HEAD | tar -x -C "$1"
}

# binaries SRC OUT: the release binaries and their SHA256SUMS.
binaries() {
  mkdir -p "$2"
  for t in $targets; do
    goos=${t%/*} goarch=${t#*/} ext=""
    [ "$goos" = windows ] && ext=.exe
    (cd "$1" && CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -buildvcs=false -mod=vendor \
      -ldflags "-s -w -buildid= -X main.version=$version" -o "$2/p0ssess-$goos-$goarch$ext" ./cmd/p0ssess)
  done
  (cd "$2" && sha256sum p0ssess-* > SHA256SUMS)
}

# launcher SRC OUT: the Windows launcher, bin/p0ssess.exe. It reads the version from plugin.json, so
# it changes only when cmd/launcher does.
launcher() {
  (cd "$1" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -mod=vendor \
    -ldflags "-s -w -buildid=" -o "$2" ./cmd/launcher)
}

# check_launcher SRC: HEAD's bin/p0ssess.exe must be the build of HEAD's cmd/launcher.
check_launcher() {
  launcher "$1" "$work/p0ssess.exe"
  cmp -s "$work/p0ssess.exe" "$1/bin/p0ssess.exe" ||
    die "bin/p0ssess.exe is not the build of cmd/launcher (run \`scripts/build-release.sh launcher\` and commit it)"
  echo "bin/p0ssess.exe matches cmd/launcher: $(sha256sum "$1/bin/p0ssess.exe" | cut -c1-64)"
}

case "${1:-}" in
launcher)
  export_head "$work/src"
  launcher "$work/src" "$PWD/bin/p0ssess.exe"
  echo "bin/p0ssess.exe: $(sha256sum bin/p0ssess.exe | cut -c1-64); commit it"
  exit 0
  ;;
--check)
  export_head "$work/src"
  check_launcher "$work/src"
  binaries "$work/src" "$work/a"
  go clean -cache
  binaries "$work/src" "$work/b"
  if cmp -s "$work/a/SHA256SUMS" "$work/b/SHA256SUMS"; then echo "reproducible:"; cat "$work/a/SHA256SUMS"; exit 0; fi
  echo "builds differ:" >&2; diff "$work/a/SHA256SUMS" "$work/b/SHA256SUMS" >&2; exit 1
  ;;
sums)
  export_head "$work/src"
  binaries "$work/src" "$work/bin"
  mkdir -p dist
  cp "$work/bin/SHA256SUMS" dist/SHA256SUMS
  printf '%s\n%s\n' "$version" "$(go version | awk '{print $3}')" > dist/VERSION
  echo "p0ssess $version with $(go version | awk '{print $3}'); commit dist/SHA256SUMS and dist/VERSION:"
  cat dist/SHA256SUMS
  exit 0
  ;;
"") ;;
*) die "unknown argument $1 (sums, launcher, --check, or nothing)" ;;
esac

export_head "$work/src"
[ "$(head -n 1 "$work/src/dist/VERSION" 2>/dev/null)" = "$version" ] ||
  die "HEAD has no dist/VERSION for $version; run \`scripts/build-release.sh sums\` and commit dist/ first"
check_launcher "$work/src"
binaries "$work/src" "$work/bin"
cmp -s "$work/bin/SHA256SUMS" "$work/src/dist/SHA256SUMS" ||
  die "HEAD's dist/SHA256SUMS does not match this build (other source or Go version?); rerun \`sums\` and commit"

out=${OUT:-release/$version}
rm -rf "$out"
mkdir -p "$out"
licenses() {
  cp "$work/src/LICENSE" "$work/src/NOTICE" "$1/"
  cp "$work/src/vendor/github.com/BurntSushi/toml/COPYING" "$1/LICENSE.BurntSushi-toml"
}

# The plugin archive: HEAD's tree with the checked binaries in dist/, which the launcher runs without Go.
pkg=$work/pkg/p0ssess-$version
mkdir -p "$work/pkg"
cp -R "$work/src" "$pkg"
cp "$work/bin"/p0ssess-* "$pkg/dist/"
mtime=$(git log -1 --format=%ct HEAD)
(cd "$work/pkg" && tar --sort=name --mtime="@$mtime" --owner=0 --group=0 --numeric-owner --mode='u+rw,go-w' \
  -cf - "p0ssess-$version") | gzip -n -9 > "$out/p0ssess-$version.tar.gz"

cp "$work/bin"/p0ssess-* "$out/"
licenses "$out"
(cd "$out" && sha256sum p0ssess-* > SHA256SUMS)
echo "p0ssess $version ($(git rev-parse --short HEAD)) with $(go version | awk '{print $3}') in $out:"
cat "$out/SHA256SUMS"
