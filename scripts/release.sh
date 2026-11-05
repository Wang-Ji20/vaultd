#!/bin/sh
# Builds the release binaries into dist/. Usage: scripts/release.sh <version>
set -eu

version="${1:?usage: scripts/release.sh <version>}"
mkdir -p dist

for target in linux/amd64 linux/arm64 windows/amd64; do
  os="${target%/*}"
  arch="${target#*/}"
  suffix=""
  [ "$os" = windows ] && suffix=".exe"
  output="dist/vaultd-$version-$os-$arch$suffix"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$output" ./cmd/vaultd
  echo "$output"
done
