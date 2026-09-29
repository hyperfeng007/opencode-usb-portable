#!/usr/bin/env bash
# Builds both executables and packs everything into a USB-ready zip.
# Usage: ./scripts/package.sh [version]    (default: version from source)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-$(grep -oP 'launcherVersion = "\K[^"]+' "$ROOT/payload/launcher/src/version.go")}"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

"$ROOT/scripts/build.sh" "$STAGE/tools-out"
mkdir -p "$STAGE/pkg/tools/7za"
cp "$STAGE/tools-out/OpenCode-Portable.exe" "$STAGE/pkg/"
cp "$STAGE/tools-out/OpenCode-Portable-Builder.exe" "$STAGE/pkg/tools/"
cp "$ROOT/tools/7za/7za.exe" "$STAGE/pkg/tools/7za/"
cp "$ROOT/payload/OpenCode-Portable.json" "$STAGE/pkg/"
cp "$ROOT/payload/launcher/"*.cmd "$STAGE/pkg/"
cp "$ROOT/docs/使用说明.md" "$STAGE/pkg/"

(cd "$STAGE/pkg" && sha256sum OpenCode-Portable.exe tools/OpenCode-Portable-Builder.exe tools/7za/7za.exe \
  "Build portable version.cmd" "Update OpenCode.cmd" "OpenCode-debug.cmd" "OpenCode-Portable.json" 使用说明.md > SHA256SUMS.txt)

OUT="$ROOT/opencode-usb-portable-v$VERSION.zip"
rm -f "$OUT"
(cd "$STAGE/pkg" && python3 - "$OUT" <<'PY'
import os, sys, zipfile
out = sys.argv[1]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    for root, dirs, files in os.walk("."):
        dirs.sort(); files.sort()
        for name in files:
            path = os.path.join(root, name)
            info = zipfile.ZipInfo(os.path.relpath(path, "."))
            info.external_attr = 0o644 << 16
            with open(path, "rb") as handle:
                z.writestr(info, handle.read(), zipfile.ZIP_DEFLATED, 9)
print("packed", out)
PY
)
echo "==> $OUT"
sha256sum "$OUT"
