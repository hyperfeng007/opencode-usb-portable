#!/usr/bin/env bash
# Cross-compiles the two Windows executables (pure Go, no cgo needed).
# Usage: ./scripts/build.sh [output-dir]   (default: ./releases)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$ROOT/releases}"
mkdir -p "$OUT"

echo "==> launcher"
(cd "$ROOT/payload/launcher/src" && \
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
    -ldflags "-H windowsgui -s -w" -o "$OUT/OpenCode-Portable.exe" .)

echo "==> builder"
(cd "$ROOT/builder" && \
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w" -o "$OUT/OpenCode-Portable-Builder.exe" .)

echo "==> done"
ls -l "$OUT"
