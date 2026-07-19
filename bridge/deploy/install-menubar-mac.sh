#!/usr/bin/env bash
# install-menubar-mac.sh — install the Codex Remote menu-bar app (status + QR).
#
# Reuses the per-machine token created by install-agent-mac.sh; it does NOT run the
# bridge itself (the launchd *agent* does that). This builds a .app bundle, writes
# ~/.codex-remote/menubar.env, installs a login LaunchAgent, and launches it so a
# ">_" icon appears in the menu bar — click it to copy the connect string or show a
# QR the phone scans. No terminal needed afterwards.
#
# Run AFTER install-agent-mac.sh. Usage (env optional — most is auto-detected):
#   ./install-menubar-mac.sh
#   MACHINE_ID=mac-studio MACHINE_NAME="My Mac" AGENT_KEY=<shared> ./install-menubar-mac.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
BRIDGE_DIR="$REPO/bridge"
CR_DIR="$HOME/.codex-remote"
APP="$HOME/Applications/CodexRemote.app"
LA_DIR="$HOME/Library/LaunchAgents"
LABEL="com.example.codexmenubar"
HUB="${HUB:-wss://relay.example.com/agent}"

# 1) machine id — arg/env, else infer from the single agent-*.env
MACHINE_ID="${MACHINE_ID:-}"
if [[ -z "$MACHINE_ID" ]]; then
  shopt -s nullglob
  envs=( "$HERE"/agent-*.env )
  shopt -u nullglob
  if [[ ${#envs[@]} -eq 1 ]]; then
    b="$(basename "${envs[0]}" .env)"; MACHINE_ID="${b#agent-}"
  fi
fi
[[ -n "$MACHINE_ID" ]] || { echo "ERROR: set MACHINE_ID=… (couldn't infer a single agent-*.env)"; exit 1; }
MACHINE_NAME="${MACHINE_NAME:-$MACHINE_ID}"

# 2) per-machine token from agent-<id>.env
AGENT_ENV="$HERE/agent-${MACHINE_ID}.env"
[[ -f "$AGENT_ENV" ]] || { echo "ERROR: $AGENT_ENV not found — run install-agent-mac.sh first"; exit 1; }
# shellcheck disable=SC1090
source "$AGENT_ENV"
TOKEN="${MACHINE_TOKEN:-}"
[[ -n "$TOKEN" ]] || { echo "ERROR: MACHINE_TOKEN missing in $AGENT_ENV"; exit 1; }

# 3) shared agent key (lets the panel query the hub for true online status) —
#    optional; without it the panel falls back to "is the launchd agent loaded".
AGENT_KEY="${AGENT_KEY:-}"
if [[ -z "$AGENT_KEY" && -f "$HERE/secrets.env" ]]; then
  AGENT_KEY="$(grep -E '^AGENT_KEY=' "$HERE/secrets.env" | head -1 | cut -d= -f2- | tr -d '"' || true)"
fi

# 4) build the binary
mkdir -p "$CR_DIR"
echo "→ building codexmenubar…"
( cd "$BRIDGE_DIR" && GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" GOSUMDB="${GOSUMDB:-off}" \
    go build -o "$CR_DIR/codexmenubar" ./cmd/codexmenubar )

# 5) config (0600 — holds the token + agent key)
cat > "$CR_DIR/menubar.env" <<EOF
MACHINE_ID=$MACHINE_ID
MACHINE_NAME=$MACHINE_NAME
HUB=$HUB
TOKEN=$TOKEN
AGENT_KEY=$AGENT_KEY
EOF
chmod 600 "$CR_DIR/menubar.env"

# 6) .app bundle (LSUIElement = menu-bar only, no Dock icon)
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$CR_DIR/codexmenubar" "$APP/Contents/MacOS/codexmenubar"
cat > "$APP/Contents/Info.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>Codex Remote</string>
  <key>CFBundleDisplayName</key><string>Codex Remote</string>
  <key>CFBundleIdentifier</key><string>com.example.codexremote</string>
  <key>CFBundleExecutable</key><string>codexmenubar</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
</dict></plist>
EOF

# optional Finder/.app icon from the phone app's icon (best-effort)
SRC_ICON="$REPO/app/assets/icon/icon.png"
if [[ -f "$SRC_ICON" ]] && command -v iconutil >/dev/null 2>&1 && command -v magick >/dev/null 2>&1; then
  ICS="$(mktemp -d)/AppIcon.iconset"; mkdir -p "$ICS"
  for s in 16 32 128 256 512; do
    magick "$SRC_ICON" -resize ${s}x${s}     "$ICS/icon_${s}x${s}.png"    2>/dev/null || true
    magick "$SRC_ICON" -resize $((s*2))x$((s*2)) "$ICS/icon_${s}x${s}@2x.png" 2>/dev/null || true
  done
  if iconutil -c icns "$ICS" -o "$APP/Contents/Resources/AppIcon.icns" 2>/dev/null; then
    /usr/libexec/PlistBuddy -c "Add :CFBundleIconFile string AppIcon" "$APP/Contents/Info.plist" 2>/dev/null || true
  fi
fi

# 7) login LaunchAgent — RunAtLoad only; KeepAlive off so the menu "退出" actually
#    quits (until next login). Aqua session so the menu-bar icon shows.
mkdir -p "$LA_DIR"
PLIST="$LA_DIR/$LABEL.plist"
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL</string>
  <!-- Launch via LaunchServices (open) so the bundle's LSUIElement registers and
       the menu-bar status item actually appears; exec'ing the inner binary directly
       under launchd often yields a running process with no menu-bar icon. -->
  <key>ProgramArguments</key>
  <array><string>/usr/bin/open</string><string>$APP</string></array>
  <key>RunAtLoad</key><true/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
  <key>StandardOutPath</key><string>$HOME/Library/Logs/codex-remote/menubar.log</string>
  <key>StandardErrorPath</key><string>$HOME/Library/Logs/codex-remote/menubar.err.log</string>
</dict></plist>
EOF
mkdir -p "$HOME/Library/Logs/codex-remote"
# Stop any already-running instance FIRST. The plist launches via `open`, which
# REACTIVATES an existing app instead of spawning a new process — so an in-place
# reinstall (e.g. after a relay migration) would otherwise keep running the OLD
# binary with the OLD menubar.env (config is read once at startup). Killing it
# makes the freshly-built binary + updated config actually take effect.
pkill -f "$APP/Contents/MacOS/codexmenubar" 2>/dev/null && sleep 1 || true
launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"   # RunAtLoad starts the freshly-built app now (GUI session)

# Fallback: if RunAtLoad didn't bring it up (e.g. odd session), open the bundle —
# but only when nothing is already running, so we never get duplicate menu icons.
sleep 1
pgrep -f "$APP/Contents/MacOS/codexmenubar" >/dev/null 2>&1 || open "$APP" 2>/dev/null || true

cat <<EOF

✅ 菜单栏 App 已安装并启动(登录自启)。
   看屏幕右上角菜单栏的 ">_" 图标:
     • 绿色 = 在线 / 灰色 = 离线
     • 显示二维码… → 手机扫码连接
     • 复制连接串 / 复制 Token

   机器: $MACHINE_ID ($MACHINE_NAME)
   App:  $APP
   配置: $CR_DIR/menubar.env   (0600)
   停用自启: launchctl unload $PLIST
EOF
