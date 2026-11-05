#!/usr/bin/env bash
# Cross-compiles PhotoBridge for Windows, macOS and Linux into dist/.
# Usage: scripts/build.sh [version]   (default: git describe or "dev")
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always 2>/dev/null || echo dev)}"
OUT=dist
rm -rf "$OUT"
mkdir -p "$OUT"

targets=(
  windows/amd64 windows/arm64
  darwin/arm64 darwin/amd64
  linux/amd64 linux/arm64
)

for t in "${targets[@]}"; do
  os="${t%/*}"; arch="${t#*/}"
  name="photobridge-${VERSION}-${os}-${arch}"
  dir="$OUT/$name"
  mkdir -p "$dir"
  bin="photobridge"; [ "$os" = windows ] && bin="photobridge.exe"
  echo "building $name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" -o "$dir/$bin" .
  cp README.md LICENSE "$dir/"
  if [ "$os" = linux ]; then
    tar -C "$OUT" -czf "$OUT/$name.tar.gz" "$name"
  else
    (cd "$OUT" && zip -qr "$name.zip" "$name")
  fi
  rm -rf "$dir"
done

(cd "$OUT" && sha256sum ./*.zip ./*.tar.gz > SHA256SUMS.txt)
ls -lh "$OUT"
