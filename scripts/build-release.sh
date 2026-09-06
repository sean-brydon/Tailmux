#!/bin/sh
set -eu
VERSION=${1:?Usage: scripts/build-release.sh vX.Y.Z}
printf '%s\n' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([.-][a-zA-Z0-9.-]+)?$' || { echo 'Invalid release version' >&2; exit 1; }
ROOT=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
cd "$ROOT"
mkdir -p dist
OUT=$(mktemp -d "$ROOT/dist/.release.XXXXXX")
trap 'rm -rf "$OUT"' EXIT
for OS in darwin linux; do
  for ARCH in amd64 arm64; do
    printf 'Building %s/%s\n' "$OS" "$ARCH"
    CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" go build -trimpath -ldflags "-s -w -X github.com/sean-brydon/Tailmux/internal/tailmux.Version=$VERSION" -o "$OUT/tailmux" ./cmd/tailmux
    COPYFILE_DISABLE=1 tar -czf "$OUT/tailmux_${OS}_${ARCH}.tar.gz" -C "$OUT" tailmux
  done
done
cp install.sh "$OUT/install.sh"
(cd "$OUT" && if command -v sha256sum >/dev/null 2>&1; then sha256sum ./*.tar.gz install.sh; else shasum -a 256 ./*.tar.gz install.sh; fi) | sed 's|  \./|  |' > "$OUT/checksums.txt"
for FILE in "$OUT"/*.tar.gz "$OUT/install.sh" "$OUT/checksums.txt"; do mv "$FILE" "$ROOT/dist/"; done
printf 'Release assets are in dist/\n'
