#!/bin/sh
# Installs HatRack for this user, without root: the single binary `hat` goes
# to ~/.local/bin, then `hat setup` asks the few remaining questions.
#
#   from the unpacked tarball:   ./install.sh
#   without downloading first:   curl -fsSL https://github.com/be-spex/HatRack/releases/latest/download/install.sh | sh
#
# HAT_BIN changes the install path. Nothing else is written here: services,
# the tray and the configuration all come from `hat setup`.
set -eu

REPO="be-spex/HatRack"
DEST="${HAT_BIN:-$HOME/.local/bin/hat}"
BASE="${HATRACK_RELEASE_URL:-https://github.com/$REPO/releases/latest/download}"  # override for tests

die() { echo "install.sh: $*" >&2; exit 1; }

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "no HatRack release for $(uname -m)" ;;
esac
[ "$(uname -s)" = Linux ] || die "HatRack runs on Linux only"

HERE="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ -f "$HERE/hat" ]; then
  # unpacked tarball: the binary sits next to this script
  SRC="$HERE/hat"
else
  command -v curl >/dev/null 2>&1 || die "curl is needed to download HatRack"
  echo "Downloading HatRack ($ARCH) ..."
  curl -fsSL -o "$TMP/hatrack.tar.gz" "$BASE/hatrack_linux_$ARCH.tar.gz"
  curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt"
  want="$(grep "  hatrack_linux_$ARCH.tar.gz\$" "$TMP/checksums.txt" | cut -d' ' -f1)"
  [ -n "$want" ] || die "no checksum for hatrack_linux_$ARCH.tar.gz"
  got="$(sha256sum "$TMP/hatrack.tar.gz" | cut -d' ' -f1)"
  [ "$got" = "$want" ] || die "checksum does not match; nothing was installed"
  tar -xzf "$TMP/hatrack.tar.gz" -C "$TMP" hat
  SRC="$TMP/hat"
fi

mkdir -p "$(dirname "$DEST")"
install -m 0755 "$SRC" "$DEST.new"
mv -f "$DEST.new" "$DEST"   # replacing a running binary is fine with mv
echo "Installed: $DEST ($("$DEST" --version))"

case ":$PATH:" in
  *":$(dirname "$DEST"):"*) ;;
  *) echo "Note: $(dirname "$DEST") is not in PATH; add it to your shell profile." ;;
esac

# hat setup asks questions, so it needs a terminal; with a pipe, run it by hand
if [ -t 0 ]; then
  exec "$DEST" setup
elif ( : </dev/tty ) 2>/dev/null; then
  exec "$DEST" setup </dev/tty
else
  echo "Now run: $DEST setup"
fi
