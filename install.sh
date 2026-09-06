#!/bin/sh
# Install a checksum-verified Tailmux binary from an official GitHub release.
set -eu

REPO=sean-brydon/Tailmux
VERSION=${TAILMUX_VERSION:-latest}
INSTALL_DIR=${TAILMUX_INSTALL_DIR:-${HOME:?HOME must be set}/.local/bin}
TMP=
STAGED=
fail() { printf 'tailmux: %s\n' "$*" >&2; exit 1; }
cleanup() {
  [ -z "$TMP" ] || rm -rf "$TMP"
  [ -z "$STAGED" ] || rm -f "$STAGED"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || fail '--version requires a value'; VERSION=$2; shift 2 ;;
    --bin-dir) [ "$#" -ge 2 ] || fail '--bin-dir requires a directory'; INSTALL_DIR=$2; shift 2 ;;
    -h|--help)
      printf 'Usage: install.sh [--version vX.Y.Z] [--bin-dir DIR]\nDefault: latest release, ~/.local/bin. No sudo required.\n'
      exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

case "$(uname -s)" in Darwin) OS=darwin ;; Linux) OS=linux ;; *) fail 'supported systems: macOS and Linux' ;; esac
case "$(uname -m)" in x86_64|amd64) ARCH=amd64 ;; arm64|aarch64) ARCH=arm64 ;; *) fail 'supported architectures: amd64 and arm64' ;; esac
command -v curl >/dev/null 2>&1 || fail 'curl is required'
command -v tar >/dev/null 2>&1 || fail 'tar is required'
if command -v sha256sum >/dev/null 2>&1; then HASH=sha256sum
elif command -v shasum >/dev/null 2>&1; then HASH=shasum
else fail 'sha256sum or shasum is required'; fi

fetch() { curl --proto '=https' --tlsv1.2 -fsSL --retry 3 --connect-timeout 10 --max-time 120 "$@"; }
if [ "$VERSION" = latest ]; then
  RELEASE_URL=$(fetch -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") || fail 'no published release found; build from source until the first release is published'
  VERSION=${RELEASE_URL##*/}
fi
printf '%s\n' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([.-][a-zA-Z0-9.-]+)?$' || fail 'version must be a release tag such as v0.1.0'
[ -n "$INSTALL_DIR" ] || fail 'install directory cannot be empty'
case "$INSTALL_DIR" in /*) ;; *) INSTALL_DIR="$PWD/$INSTALL_DIR" ;; esac

ASSET="tailmux_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$VERSION"
TMP=$(mktemp -d)
printf 'Downloading Tailmux %s for %s/%s…\n' "$VERSION" "$OS" "$ARCH"
fetch "$BASE/$ASSET" -o "$TMP/$ASSET" || fail "could not download $ASSET for $VERSION"
fetch "$BASE/checksums.txt" -o "$TMP/checksums.txt" || fail 'could not download release checksums'
EXPECTED=$(awk -v name="$ASSET" '$2 == name { count++; hash=$1 } END { if (count != 1) exit 1; print hash }' "$TMP/checksums.txt") || fail 'missing or duplicate asset checksum'
printf '%s\n' "$EXPECTED" | grep -Eq '^[0-9a-f]{64}$' || fail 'invalid SHA-256 checksum'
if [ "$HASH" = sha256sum ]; then ACTUAL=$(sha256sum "$TMP/$ASSET" | awk '{print $1}')
else ACTUAL=$(shasum -a 256 "$TMP/$ASSET" | awk '{print $1}'); fi
[ "$EXPECTED" = "$ACTUAL" ] || fail 'checksum mismatch; existing installation was not changed'
tar -xzf "$TMP/$ASSET" -C "$TMP" tailmux || fail 'release archive does not contain tailmux'
[ -f "$TMP/tailmux" ] && [ ! -L "$TMP/tailmux" ] || fail 'release binary must be a regular file'
mkdir -p "$INSTALL_DIR" || fail "cannot create $INSTALL_DIR"
[ ! -d "$INSTALL_DIR/tailmux" ] || fail 'destination tailmux is a directory'
STAGED=$(mktemp "$INSTALL_DIR/.tailmux.XXXXXX")
cp "$TMP/tailmux" "$STAGED"
chmod 755 "$STAGED"
mv -f "$STAGED" "$INSTALL_DIR/tailmux"
STAGED=
printf 'Installed %s/tailmux\n' "$INSTALL_DIR"
case ":$PATH:" in *":$INSTALL_DIR:"*) ;; *) printf 'Add this directory to your PATH: %s\n' "$INSTALL_DIR" ;; esac
printf 'Run tailmux --version to verify. If updating, run tailmux stop before your next connection.\n'
