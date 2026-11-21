#!/bin/sh
# The release gate: the owner's trials as real p0ssess runs (see e2e/gate_test.go). It spends well under
# a dollar of OpenRouter credit, capped by P0SSESS_E2E_MAX_USD (default 1.00), in a scratch P0SSESS_HOME.
#
#   scripts/e2e-gate.sh [go test flags, e.g. -run 'TestGate/tour']
#
# Builds with Go when it is installed, else in the golang:1.27-bookworm image with podman.
set -eu
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
version=$(sed -n 's/^version=//p' "$repo/bin/p0ssess")
if command -v go >/dev/null 2>&1; then
  (cd "$repo" && CGO_ENABLED=0 go build -trimpath -mod=vendor -ldflags "-X main.version=$version" -o "$out/p0ssess" ./cmd/p0ssess &&
    CGO_ENABLED=0 go test -c -mod=vendor -o "$out/e2e.test" ./e2e)
else
  podman run --rm --security-opt label=disable -v "$repo":/src:ro -v "$out":/out -w /src -e GOFLAGS=-mod=vendor -e CGO_ENABLED=0 -e GOCACHE=/tmp/gocache \
    docker.io/library/golang:1.27-bookworm sh -c \
    "go build -trimpath -ldflags '-X main.version=$version' -o /out/p0ssess ./cmd/p0ssess && go test -c -o /out/e2e.test ./e2e"
fi
P0SSESS_E2E=1 P0SSESS_E2E_BIN="$out/p0ssess" "$out/e2e.test" -test.v -test.count=1 -test.timeout 30m "$@"
