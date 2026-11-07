#!/usr/bin/env bash
# Builds the release archives into dist/: static `grid` binaries for macOS, Linux, and Windows,
# on amd64 and arm64, each with the README; a .zip for Windows, a .tar.gz for the rest, and
# checksums.txt over them all.
#
#   .github/scripts/package.sh v1.2.3
set -euo pipefail

tag="${1:?usage: package.sh <tag>}"
targets=(darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64)

rm -rf dist
mkdir -p dist
for target in "${targets[@]}"; do
    os="${target%/*}"
    arch="${target#*/}"
    name="grid_${tag#v}_${os}_${arch}"
    binary="grid"
    if [[ "$os" == "windows" ]]; then
        binary="grid.exe"
    fi
    mkdir -p "dist/$name"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
        -ldflags "-s -w -X github.com/diphyx/grid/cmd.VERSION=$tag" -o "dist/$name/$binary" .
    cp README.md "dist/$name/"
    if [[ "$os" == "windows" ]]; then
        (cd dist && zip -qr "$name.zip" "$name")
    else
        tar -C dist -czf "dist/$name.tar.gz" "$name"
    fi
    rm -rf "dist/$name"
done
(cd dist && sha256sum -- *.tar.gz *.zip > checksums.txt)
