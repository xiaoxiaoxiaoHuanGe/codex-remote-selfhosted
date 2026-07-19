#!/usr/bin/env bash
#
# Build a DISTRIBUTABLE Caret release APK — with NO secrets baked in.
#
# Caret reads its connection (machine + token) from the on-device keychain after
# the user pairs by scanning the QR from the desktop tray. This script therefore
# deliberately does NOT pass --dart-define=TOKEN, so the resulting APK is safe to
# share or upload to an app store. A built-in safety net refuses to emit an APK
# that happens to contain the local machine token.
#
# Usage:
#   ./build-apk-release.sh [output-dir]
#     output-dir   where to copy the finished APK (default: ./dist)
#
# Personal "auto-connect" build (NEVER share the file it produces): append the
# token yourself, on purpose:
#   cd app && flutter build apk --release \
#     --dart-define=TOKEN="$(sed -n 's/^TOKEN=//p' ~/.codex-remote/menubar.env)"
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR="$SCRIPT_DIR/app"
OUT_DIR="${1:-$SCRIPT_DIR/dist}"

cd "$APP_DIR"

echo "▶ Building clean release APK (no token baked in)…"
flutter build apk --release

APK="$APP_DIR/build/app/outputs/flutter-apk/app-release.apk"
[ -f "$APK" ] || { echo "✗ build produced no APK at $APK"; exit 1; }

# Safety net: refuse to emit an APK that contains the local machine token.
ENVF="$HOME/.codex-remote/menubar.env"
if [ -f "$ENVF" ]; then
  TOK="$(sed -n 's/^TOKEN=//p' "$ENVF")"
  if [ -n "$TOK" ] && unzip -p "$APK" 'lib/*' 'assets/*' 2>/dev/null | grep -qa "$TOK"; then
    echo "✗ ABORT: the built APK contains your machine token — refusing to emit it."
    echo "  (Did a stale --dart-define=TOKEN leak in? Run 'cd app && flutter clean' and retry.)"
    exit 2
  fi
fi

mkdir -p "$OUT_DIR"
STAMP="$(date +%Y%m%d-%H%M)"
DEST="$OUT_DIR/caret-$STAMP.apk"
cp -f "$APK" "$DEST"

echo "✓ Clean APK → $DEST  ($(du -h "$DEST" | cut -f1))"
echo "  Safe to distribute. Users pair by scanning the QR from the desktop tray."
