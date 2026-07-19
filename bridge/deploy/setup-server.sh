#!/usr/bin/env bash
# setup-server.sh — run ON the Tencent Cloud server (example.com) as root.
# Installs frps, writes /etc/frp/frps.toml, sets up systemd, opens the control
# port. The nginx subdomain + Let's Encrypt + reverse proxy is done in BaoTa
# afterwards (see the printed next-steps / DEPLOY.md §二.3).
#
# Usage (FRP_TOKEN is printed by the Mac's install-mac.sh):
#   sudo FRP_TOKEN=xxxxxxxx ./setup-server.sh
#   # optional: MAC_IP=1.2.3.4 to restrict the control port to your Mac's egress IP
set -euo pipefail

FRP_VERSION="${FRP_VERSION:-0.69.0}"   # keep close to the Mac's frpc (brew: 0.69.0)
FRP_TOKEN="${FRP_TOKEN:-}"
MAC_IP="${MAC_IP:-}"                    # if set, only this source IP may reach :7000

[[ $EUID -eq 0 ]] || { echo "run as root (sudo)"; exit 1; }
[[ -n "$FRP_TOKEN" ]] || { echo "ERROR: set FRP_TOKEN=... (from the Mac's install-mac.sh output)"; exit 1; }

arch="$(uname -m)"; case "$arch" in
  x86_64|amd64) FA=amd64 ;; aarch64|arm64) FA=arm64 ;;
  *) echo "unsupported arch $arch"; exit 1 ;;
esac

echo "→ downloading frp ${FRP_VERSION} (${FA})"
tmp="$(mktemp -d)"
curl -fsSL -o "$tmp/frp.tgz" \
  "https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}/frp_${FRP_VERSION}_linux_${FA}.tar.gz"
tar -xzf "$tmp/frp.tgz" -C "$tmp"
install -m0755 "$tmp/frp_${FRP_VERSION}_linux_${FA}/frps" /usr/local/bin/frps
rm -rf "$tmp"

id -u frp >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin frp
mkdir -p /etc/frp

echo "→ writing /etc/frp/frps.toml"
cat > /etc/frp/frps.toml <<EOF
bindPort = 7000
proxyBindAddr = "127.0.0.1"
auth.method = "token"
auth.token = "${FRP_TOKEN}"
transport.tls.force = true
allowPorts = [{ start = 8700, single = 8700 }]
EOF
# chown the DIR too — frps runs as user frp and 750 root:root would block it from
# even traversing into /etc/frp (open frps.toml: permission denied).
chown root:frp /etc/frp /etc/frp/frps.toml
chmod 750 /etc/frp
chmod 640 /etc/frp/frps.toml

echo "→ installing systemd unit"
cat > /etc/systemd/system/frps.service <<'EOF'
[Unit]
Description=frp server (codex-remote relay)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=frp
ExecStart=/usr/local/bin/frps -c /etc/frp/frps.toml
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now frps
sleep 1
systemctl --no-pager --full status frps | head -n 6 || true

echo "→ firewall: opening TCP 7000"
if command -v firewall-cmd >/dev/null 2>&1; then
  if [[ -n "$MAC_IP" ]]; then
    firewall-cmd --permanent --add-rich-rule="rule family=ipv4 source address=${MAC_IP}/32 port port=7000 protocol=tcp accept"
  else
    firewall-cmd --permanent --add-port=7000/tcp
  fi
  firewall-cmd --reload
elif command -v ufw >/dev/null 2>&1; then
  [[ -n "$MAC_IP" ]] && ufw allow from "$MAC_IP" to any port 7000 proto tcp || ufw allow 7000/tcp
else
  echo "  (no firewalld/ufw found — open TCP 7000 in the Tencent Cloud 安全组 manually)"
fi
echo "  NOTE: also open TCP 7000 in the Tencent Cloud 安全组 console. 8700 stays localhost-only — do NOT open it."

cat <<'EOF'

✅ frps is up. Remaining (BaoTa, GUI):
  1. DNS: add A record  relay.example.com -> this server's public IP.
  2. BaoTa → 网站 → 添加站点 relay.example.com (static) → SSL → Let's Encrypt → 强制 HTTPS.
  3. That site → 反向代理 → target http://127.0.0.1:8700, 发送域名 $host, WebSocket 开关 ON,
     proxy_read_timeout 3600s, 该 server 关 access_log（避免记 ?token=）。原始配置见 deploy/nginx-codex.conf。

Verify:
  curl -s http://127.0.0.1:8700/healthz            # expect: ok  (= tunnel to Mac bridge is live)
  curl -s https://relay.example.com/healthz      # expect: ok  (= nginx+TLS path works)
EOF
