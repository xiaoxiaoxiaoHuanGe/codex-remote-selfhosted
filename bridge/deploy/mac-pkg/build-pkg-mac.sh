#!/usr/bin/env bash
# build-pkg-mac.sh — 产出 macOS 安装包 Caret.pkg。
#
#   ./build-pkg-mac.sh [output.pkg]
#
# 流水线:arm64+x86_64 通用二进制 → 托盘打成 /Applications/Caret.app →
# pkgbuild(postinstall 弹窗要订阅码并激活)→ 按本机证书自动升级:
#   · 有 "Developer ID Application" → 二进制/App 带 hardened runtime 签名
#   · 有 "Developer ID Installer"   → productsign 签 pkg
#   · 有 notarytool 凭证 caret-notary → 公证 + staple
# 三者缺谁就跳过谁并提示 —— 没证书也能出未签名 pkg 供本机测试。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BRIDGE="$(cd "$HERE/../.." && pwd)"             # .../bridge
REPO="$(cd "$BRIDGE/.." && pwd)"
OUT="${1:-$HERE/Caret.pkg}"
VERSION="${VERSION:-$(date +%Y.%m.%d)}"
IDENT="com.example.caret"
ICON_SRC="$REPO/website/assets/img/caret-icon.png"
STAGE="$(mktemp -d)"; trap 'rm -rf "$STAGE"' EXIT

echo "→ 编译通用二进制(arm64 + x86_64)…"
for tool in codexbridge codexmenubar; do
  for arch in arm64 amd64; do
    ( cd "$BRIDGE" && GOOS=darwin GOARCH=$arch CGO_ENABLED=1 \
        go build -trimpath -ldflags="-s -w" -o "$STAGE/$tool-$arch" "./cmd/$tool" )
  done
  lipo -create -output "$STAGE/$tool" "$STAGE/$tool-arm64" "$STAGE/$tool-amd64"
done

echo "→ 组装 payload(/usr/local/codex-remote + /Applications/Caret.app)…"
ROOT="$STAGE/root"
APP="$ROOT/Applications/Caret.app"
mkdir -p "$ROOT/usr/local/codex-remote" "$APP/Contents/MacOS" "$APP/Contents/Resources"
install -m755 "$STAGE/codexbridge"  "$ROOT/usr/local/codex-remote/codexbridge"
install -m755 "$STAGE/codexmenubar" "$APP/Contents/MacOS/codexmenubar"

# 图标:1024 png → icns
ICONSET="$STAGE/Caret.iconset"; mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -z $s $s             "$ICON_SRC" --out "$ICONSET/icon_${s}x${s}.png"      >/dev/null
  sips -z $((s*2)) $((s*2)) "$ICON_SRC" --out "$ICONSET/icon_${s}x${s}@2x.png"   >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/Caret.icns"

cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleIdentifier</key><string>$IDENT.tray</string>
  <key>CFBundleName</key><string>Caret</string>
  <key>CFBundleDisplayName</key><string>Caret</string>
  <key>CFBundleExecutable</key><string>codexmenubar</string>
  <key>CFBundleIconFile</key><string>Caret</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>LSUIElement</key><true/>
</dict></plist>
EOF

# ── 签名(有 Developer ID Application 证书才做;公证强制要求 hardened runtime) ──
APP_ID=$(security find-identity -v -p codesigning | awk -F'"' '/Developer ID Application/{print $2; exit}')
if [[ -n "$APP_ID" ]]; then
  echo "→ codesign($APP_ID)…"
  codesign --force --options runtime --timestamp -s "$APP_ID" "$ROOT/usr/local/codex-remote/codexbridge"
  codesign --force --options runtime --timestamp -s "$APP_ID" "$APP"
else
  echo "⚠ 未找到 Developer ID Application 证书 — 跳过二进制签名(Xcode ▸ Settings ▸ Accounts ▸ Manage Certificates 创建)"
fi

echo "→ pkgbuild…"
SCRIPTS="$STAGE/scripts"; mkdir -p "$SCRIPTS"
install -m755 "$HERE/postinstall" "$SCRIPTS/postinstall"
RAW="$STAGE/raw.pkg"
pkgbuild --root "$ROOT" --scripts "$SCRIPTS" \
  --identifier "$IDENT" --version "$VERSION" --install-location / "$RAW" >/dev/null

INST_ID=$(security find-identity -v | awk -F'"' '/Developer ID Installer/{print $2; exit}')
if [[ -n "$INST_ID" ]]; then
  echo "→ productsign($INST_ID)…"
  productsign --sign "$INST_ID" "$RAW" "$OUT" >/dev/null
else
  echo "⚠ 未找到 Developer ID Installer 证书 — 输出未签名 pkg(仅供本机测试,分发会被 Gatekeeper 拦)"
  cp "$RAW" "$OUT"
fi

# ── 公证(签了名 + 存过 caret-notary 凭证才做) ──
if [[ -n "$INST_ID" ]] && xcrun notarytool history --keychain-profile caret-notary >/dev/null 2>&1; then
  echo "→ 公证(notarytool,通常 1–5 分钟)…"
  xcrun notarytool submit "$OUT" --keychain-profile caret-notary --wait
  xcrun stapler staple "$OUT"
  echo "✓ 已公证并装订"
elif [[ -n "$INST_ID" ]]; then
  echo "⚠ 未存公证凭证 — 跳过公证(xcrun notarytool store-credentials caret-notary --apple-id … --team-id … --password <App专用密码>)"
fi

echo "✅ $OUT  (version $VERSION)"
