#!/usr/bin/env bash
# install-mac.sh — turn-key Mac side of the codex-remote relay.
# Generates secrets, builds the bridge, renders frpc.toml + the two launchd
# agents, and (re)loads them so the bridge + frpc come up at login and stay up.
#
# Re-running is safe: it reuses deploy/secrets.env if present and reloads.
set -euo pipefail

# ---- edit if your server / subdomain differ (overridable via env) ----
SERVER_HOST="${SERVER_HOST:-203.0.113.10}"  # frps host that frpc dials (raw IP: local DNS is proxied)
SUBDOMAIN="${SUBDOMAIN:-relay.example.com}"    # public wss host (Cloudflare -> nginx)
# ----------------------------------------------------------------------

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
BRIDGE_DIR="$REPO/bridge"
BIN_DIR="$BRIDGE_DIR/bin"
LOG_DIR="$HOME/Library/Logs/codex-remote"
LA_DIR="$HOME/Library/LaunchAgents"
SECRETS="$HERE/secrets.env"

mkdir -p "$BIN_DIR" "$LOG_DIR" "$LA_DIR"

# 1) secrets (generated once, kept out of git)
if [[ ! -f "$SECRETS" ]]; then
  {
    echo "BRIDGE_TOKEN=$(openssl rand -hex 32)"
    echo "FRP_TOKEN=$(openssl rand -hex 32)"
  } > "$SECRETS"
  chmod 600 "$SECRETS"
  echo "→ generated $SECRETS"
fi
# shellcheck disable=SC1090
source "$SECRETS"

# 2) locate the codex binary (the bridge spawns `codex app-server`)
CODEX_BIN="${CODEX_BIN:-/Applications/Codex.app/Contents/Resources/codex}"
[[ -x "$CODEX_BIN" ]] || CODEX_BIN="$(command -v codex || true)"
[[ -x "$CODEX_BIN" ]] || { echo "ERROR: codex binary not found — set CODEX_BIN=… and re-run"; exit 1; }
echo "→ codex: $CODEX_BIN"

# 3) build the bridge
echo "→ building codexbridge…"
( cd "$BRIDGE_DIR" && go build -o "$BIN_DIR/codexbridge" ./cmd/codexbridge )

# 4) locate frpc
FRPC_BIN="$(command -v frpc || true)"
[[ -x "$FRPC_BIN" ]] || {
  echo "ERROR: frpc not found. Install one of:"
  echo "   brew install frpc"
  echo "   # or download darwin build from https://github.com/fatedier/frp/releases"
  exit 1
}
echo "→ frpc: $FRPC_BIN"

# 5) render frpc.toml
sed -e "s|__FRP_TOKEN__|$FRP_TOKEN|g" \
    -e "s|serverAddr = \"example.com\"|serverAddr = \"$SERVER_HOST\"|" \
    "$HERE/frpc.toml.example" > "$HERE/frpc.toml"
chmod 600 "$HERE/frpc.toml"

# 6) render the launchd agents
render() {
  sed -e "s|__BRIDGE_BIN__|$BIN_DIR/codexbridge|g" \
      -e "s|__CODEX_BIN__|$CODEX_BIN|g" \
      -e "s|__BRIDGE_TOKEN__|$BRIDGE_TOKEN|g" \
      -e "s|__FRPC_BIN__|$FRPC_BIN|g" \
      -e "s|__FRPC_TOML__|$HERE/frpc.toml|g" \
      -e "s|__LOG_DIR__|$LOG_DIR|g" \
      "$1"
}
render "$HERE/launchd/com.example.codexbridge.plist.example" > "$LA_DIR/com.example.codexbridge.plist"
render "$HERE/launchd/com.example.frpc.plist.example"        > "$LA_DIR/com.example.frpc.plist"
# These plists embed BRIDGE_TOKEN / FRP_TOKEN; default 0644 would leak them to any
# local account. Restrict to the owner (the rendered token is the live secret).
chmod 600 "$LA_DIR/com.example.codexbridge.plist" "$LA_DIR/com.example.frpc.plist"

# 7) (re)load both agents
for L in com.example.codexbridge com.example.frpc; do
  launchctl unload "$LA_DIR/$L.plist" 2>/dev/null || true
  launchctl load   "$LA_DIR/$L.plist"
  echo "→ loaded $L"
done

cat <<EOF

✅ Mac side is up (bridge + frpc, auto-start at login).

   Phone connects to:
     wss://$SUBDOMAIN/ws?token=$BRIDGE_TOKEN

   Paste this into the server's /etc/frp/frps.toml (auth.token):
     $FRP_TOKEN

   Logs: $LOG_DIR/{bridge,frpc}.{log,err.log}
   Stop: launchctl unload $LA_DIR/com.example.{codexbridge,frpc}.plist
EOF
