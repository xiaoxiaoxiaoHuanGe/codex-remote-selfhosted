#!/usr/bin/env bash
# build-installer.sh — produce CodexRemoteSetup.exe from macOS/Linux/Windows.
# Needs: Go (cross-compiles Windows amd64) + NSIS (makensis).
#   macOS:  brew install nsis
#   Ubuntu: apt install nsis
# Usage: ./build-installer.sh [output.exe]
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BRIDGE="$(cd "$HERE/../.." && pwd)"            # .../bridge
STAGE="$(mktemp -d)"
OUT="${1:-$HERE/CodexRemoteSetup.exe}"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" GOSUMDB="${GOSUMDB:-off}"

echo "→ cross-compiling Windows binaries…"
# -H windowsgui on BOTH so neither pops a console window at logon. The agent has
# no console then, so it logs to a file (agent.log next to the exe) instead.
( cd "$BRIDGE" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -ldflags="-H windowsgui -s -w" -o "$STAGE/codexbridge.exe" ./cmd/codexbridge )
( cd "$BRIDGE" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -ldflags="-H windowsgui -s -w" -o "$STAGE/codexmenubar.exe" ./cmd/codexmenubar )

cp "$BRIDGE/cmd/codexmenubar/icon_on.ico" \
   "$BRIDGE/cmd/codexmenubar/icon_off.ico" \
   "$HERE/install-core.ps1" "$STAGE/"

echo "→ makensis…"
makensis -DASSETS="$STAGE" -DOUTFILE="$OUT" "$HERE/CodexRemote.nsi"
rm -rf "$STAGE"
echo "✅ $OUT"
