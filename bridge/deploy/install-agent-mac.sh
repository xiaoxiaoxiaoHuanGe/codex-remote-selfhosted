#!/usr/bin/env bash
# install-agent-mac.sh — register THIS Mac with the hub (agent / dial-out mode).
# No frpc, no inbound port, no DNS. Builds the bridge, generates this machine's
# token, installs a launchd agent (auto-start + keepalive), prints the phone URL.
#
# Usage (env or flags):
#   HUB=wss://relay.example.com/agent AGENT_KEY=<shared> \
#   MACHINE_ID=mac-studio MACHINE_NAME="My Mac" ./install-agent-mac.sh
set -euo pipefail

HUB="${HUB:-wss://relay.example.com/agent}"
AGENT_KEY="${AGENT_KEY:-}"
MACHINE_ID="${MACHINE_ID:-}"
MACHINE_NAME="${MACHINE_NAME:-$MACHINE_ID}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
BRIDGE_DIR="$REPO/bridge"
# CRITICAL: the launchd agent binary must NOT live under ~/Documents (or Desktop/
# Downloads) — those are TCC-protected, and a background LaunchAgent has no access,
# so dyld hangs in open() trying to exec the binary (no banner, ~112KB RSS, never
# registers). Install it to a non-TCC location instead.
BIN_DIR="$HOME/.codex-remote"
LOG_DIR="$HOME/Library/Logs/codex-remote"
LA_DIR="$HOME/Library/LaunchAgents"

[[ -n "$AGENT_KEY" ]]  || { echo "ERROR: set AGENT_KEY (the hub's shared HUB_AGENT_KEY)"; exit 1; }
[[ -n "$MACHINE_ID" ]] || { echo "ERROR: set MACHINE_ID (e.g. mac-studio)"; exit 1; }
mkdir -p "$BIN_DIR" "$LOG_DIR" "$LA_DIR"

# 1) per-machine token (generated once, kept out of git)
SECRETS="$HERE/agent-${MACHINE_ID}.env"
if [[ ! -f "$SECRETS" ]]; then
  echo "MACHINE_TOKEN=$(openssl rand -hex 32)" > "$SECRETS"
  chmod 600 "$SECRETS"
  echo "→ generated $SECRETS"
fi
# shellcheck disable=SC1090
source "$SECRETS"

# 2) codex binary
CODEX_BIN="${CODEX_BIN:-/Applications/Codex.app/Contents/Resources/codex}"
[[ -x "$CODEX_BIN" ]] || CODEX_BIN="$(command -v codex || true)"
[[ -x "$CODEX_BIN" ]] || { echo "ERROR: codex binary not found — set CODEX_BIN=…"; exit 1; }

# 3) build
echo "→ building codexbridge…"
( cd "$BRIDGE_DIR" && go build -o "$BIN_DIR/codexbridge" ./cmd/codexbridge )

# 4) launchd agent
LABEL="com.example.codexagent.${MACHINE_ID}"
PLIST="$LA_DIR/$LABEL.plist"
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$BIN_DIR/codexbridge</string>
    <string>-codex</string><string>$CODEX_BIN</string>
    <string>agent</string>
    <string>-hub</string><string>$HUB</string>
    <string>-id</string><string>$MACHINE_ID</string>
    <string>-name</string><string>$MACHINE_NAME</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>CODEX_MACHINE_TOKEN</key><string>$MACHINE_TOKEN</string>
    <key>CODEX_AGENT_KEY</key><string>$AGENT_KEY</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>$LOG_DIR/agent-$MACHINE_ID.log</string>
  <key>StandardErrorPath</key><string>$LOG_DIR/agent-$MACHINE_ID.err.log</string>
</dict>
</plist>
EOF
# The plist embeds CODEX_MACHINE_TOKEN + CODEX_AGENT_KEY (full remote control of
# this Mac). `cat >` inherits umask 022 -> 0644 (world-readable), so lock it to the
# owner — otherwise any other local account could read both secrets.
chmod 600 "$PLIST"

launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"

cat <<EOF

✅ Agent installed for machine "$MACHINE_ID" ($MACHINE_NAME), auto-starts at login.

   Phone connects to:
     wss://relay.example.com/ws?token=$MACHINE_TOKEN

   Logs: $LOG_DIR/agent-$MACHINE_ID.{log,err.log}
   Stop: launchctl unload $PLIST
EOF
