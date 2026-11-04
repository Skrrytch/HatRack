#!/usr/bin/env bash
# Build this checkout, install it as ~/.local/bin/hat and restart what runs.
#
#   scripts/update.sh            build, install, restart server/agent/tray
#   scripts/update.sh --test     run the tests first, stop if they fail
#   scripts/update.sh --setup    first time: build, install, then run `hat setup`
#
# HAT_BIN changes the install path (default ~/.local/bin/hat).
set -euo pipefail
cd "$(dirname "$0")/.."

DEST="${HAT_BIN:-$HOME/.local/bin/hat}"
TEST=0; SETUP=0
for arg in "$@"; do
  case "$arg" in
    --test) TEST=1 ;;
    --setup) SETUP=1 ;;
    -h|--help) sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $arg (try --help)" >&2; exit 2 ;;
  esac
done

say() { printf '\033[1m%s\033[0m\n' "$*"; }
die() { echo "update.sh: $*" >&2; exit 1; }

# Go: from PATH, else the user-level install under ~/.local/go-sdk
if ! command -v go >/dev/null 2>&1; then
  for d in "$HOME/.local/go-sdk/go/bin" /usr/local/go/bin; do
    if [ -x "$d/go" ]; then
      export PATH="$d:$PATH"
      [ "$d" = "$HOME/.local/go-sdk/go/bin" ] && export GOPATH="${GOPATH:-$HOME/.local/go-sdk/gopath}"
      break
    fi
  done
fi
command -v go >/dev/null 2>&1 || die "Go not found (need 1.24 or newer, see README)"

if [ "$TEST" = 1 ]; then
  say "Running tests"
  go vet ./... && go test ./...
fi

# Server or agent started by hand keep running the old binary. Say so, and
# for --setup stop here, because the services would fight over the port.
managed() { grep -q 'hatrack-' "/proc/$1/cgroup" 2>/dev/null; }
foreground=()
while read -r pid cmd; do
  [ -n "$pid" ] || continue
  managed "$pid" || foreground+=("$pid  $cmd")
done < <(pgrep -u "$USER" -af '(^|/)hat (server|agent run)( |$)' | grep -v 'pgrep' || true)

if [ "$SETUP" = 1 ] && [ ${#foreground[@]} -gt 0 ]; then
  printf '  %s\n' "${foreground[@]}"
  die "stop these first (Ctrl+C in their terminals), then run --setup again"
fi

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
say "Building hat $VERSION"
mkdir -p "$(dirname "$DEST")"
tmp="$(mktemp "$DEST.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X hatrack/internal/cli.Version=$VERSION" -o "$tmp" ./cmd/hat
chmod 755 "$tmp"
mv -f "$tmp" "$DEST"          # replacing a running binary is fine with mv
trap - EXIT
echo "installed: $DEST ($("$DEST" --version))"

if [ "$SETUP" = 1 ]; then
  exec "$DEST" setup          # asks a few questions and does the rest
fi

# Restart what is installed as a user service
units=()
for u in hatrack-server hatrack-agent; do
  systemctl --user cat "$u.service" >/dev/null 2>&1 && units+=("$u.service")
done
if [ ${#units[@]} -gt 0 ]; then
  say "Restarting ${units[*]}"
  systemctl --user restart "${units[@]}"
fi
"$DEST" tray restart || true

if [ ${#foreground[@]} -gt 0 ]; then
  echo
  echo "These run in a terminal with the OLD binary; stop and start them again,"
  echo "or run 'scripts/update.sh --setup' to make them services:"
  printf '  %s\n' "${foreground[@]}"
fi

# short health check
if [ ${#units[@]} -gt 0 ]; then
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    "$DEST" status >/dev/null 2>&1 && { say "Status"; "$DEST" status; exit 0; }
    sleep 0.5
  done
  echo "agent did not answer yet: systemctl --user status hatrack-agent" >&2
fi
