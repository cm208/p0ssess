#!/bin/sh
# Builds the release binaries into dist/ with SHA-256 sums. The build is reproducible: the same source
# and Go version give byte-identical files, so anyone can check a published binary against the source.
#   scripts/build-release.sh            all targets
#   scripts/build-release.sh --check    build twice and compare
set -eu
cd "$(dirname "$0")/.."
version=$(sed -n 's/^version=//p' bin/p0ssess)
targets="linux/amd64 linux/arm64 darwin/arm64"
out=${OUT:-dist}

build() {
  dir=$1
  mkdir -p "$dir"
  for t in $targets; do
    goos=${t%/*} goarch=${t#*/}
    CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -buildvcs=false \
      -ldflags "-s -w -buildid= -X main.version=$version" -o "$dir/p0ssess-$goos-$goarch" ./cmd/p0ssess
  done
  (cd "$dir" && sha256sum p0ssess-* > SHA256SUMS)
  # The binaries carry vendored code, so its licenses and notices ship next to them.
  cp LICENSE NOTICE "$dir/"
  cp vendor/github.com/BurntSushi/toml/COPYING "$dir/LICENSE.BurntSushi-toml"
  cp vendor/gopkg.in/yaml.v3/LICENSE "$dir/LICENSE.yaml.v3"
}

if [ "${1:-}" = "--check" ]; then
  a=$(mktemp -d) b=$(mktemp -d)
  build "$a"
  go clean -cache
  build "$b"
  if cmp -s "$a/SHA256SUMS" "$b/SHA256SUMS"; then echo "reproducible:"; cat "$a/SHA256SUMS"; rm -rf "$a" "$b"; exit 0; fi
  echo "builds differ:" >&2; diff "$a/SHA256SUMS" "$b/SHA256SUMS" >&2; exit 1
fi
build "$out"
echo "p0ssess $version with $(go version | awk '{print $3}'):"
cat "$out/SHA256SUMS"
