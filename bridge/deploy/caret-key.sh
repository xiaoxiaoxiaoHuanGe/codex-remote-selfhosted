#!/usr/bin/env bash
# caret-key.sh — 内测码运营脚本(在你自己的 Mac 上跑)。
#
# 服务器只存码的哈希、不存"这码是谁的",所以本脚本在本地维护一份
# 「码 ↔ 人」台账:~/.codex-remote/beta-keys.tsv(0600,含明文码,勿入 git/勿同步)。
#
#   caret-key.sh 张三              # 发码(默认 3 台/6 个月)并登记 + 打印可直接粘贴的消息
#   caret-key.sh 张三 1 3          # 发码:1 台 / 3 个月
#   caret-key.sh list              # 本地台账 + 服务器实况对照
#   caret-key.sh revoke 张三       # 按人名(或 crk_ 前缀)吊销,台账标记 REVOKED
#   caret-key.sh extend 张三 6     # 续期 6 个月
set -euo pipefail

HOST="root@203.0.113.10"
DB="/var/lib/codexhub/hub.db"
LEDGER="$HOME/.codex-remote/beta-keys.tsv"
SITE="https://relay.example.com"

# -db 必须紧跟子命令:Go 的 flag 解析遇到首个位置参数(如 crk_xxx)即停止。
remote() { local sub="$1"; shift; ssh "$HOST" codexhub admin "$sub" -db "$DB" "$@"; }

ensure_ledger() {
  mkdir -p "$(dirname "$LEDGER")"
  if [[ ! -f "$LEDGER" ]]; then
    printf "date\tname\tkey\tmachines\tmonths\tstatus\n" > "$LEDGER"
    chmod 600 "$LEDGER"
  fi
}

# 按人名或 crk_ 前缀在台账里找"最近一条仍 active"的码;找不到则报错退出。
find_key() {
  local who="$1" hit
  hit=$(awk -F'\t' -v w="$who" \
    '($2==w || index($3,w)==1) && $6=="active" {line=$0} END{print line}' "$LEDGER")
  [[ -n "$hit" ]] || { echo "✗ 台账里找不到「${who}」的有效码(见 $LEDGER)" >&2; exit 1; }
  printf '%s\n' "$hit"
}

mark_status() { # mark_status <key> <status> — 重写台账该行状态
  local key="$1" st="$2" tmp
  tmp=$(mktemp)
  awk -F'\t' -v OFS='\t' -v k="$key" -v s="$st" '$3==k {$6=s} {print}' "$LEDGER" > "$tmp"
  mv "$tmp" "$LEDGER" && chmod 600 "$LEDGER"
}

cmd="${1:-help}"
case "$cmd" in

  list)
    ensure_ledger
    echo "── 本地台账($LEDGER)"
    column -t -s$'\t' "$LEDGER"
    echo
    echo "── 服务器实况"
    remote list-keys
    ;;

  revoke)
    ensure_ledger
    [[ $# -ge 2 ]] || { echo "用法: caret-key.sh revoke <人名|crk_前缀>" >&2; exit 2; }
    row=$(find_key "$2")
    key=$(cut -f3 <<<"$row"); name=$(cut -f2 <<<"$row")
    echo "吊销「${name}」的码 ${key:0:12}… ?  [回车确认 / Ctrl-C 取消]"; read -r
    remote revoke-key "$key"
    mark_status "$key" REVOKED
    echo "✓ 已吊销并登记(在线机器一小时内被踢)"
    ;;

  extend)
    ensure_ledger
    [[ $# -ge 3 ]] || { echo "用法: caret-key.sh extend <人名|crk_前缀> <月数>" >&2; exit 2; }
    row=$(find_key "$2")
    key=$(cut -f3 <<<"$row"); name=$(cut -f2 <<<"$row")
    remote extend-key -months "$3" "$key"
    echo "✓ 已为「${name}」(${key:0:12}…)续期 $3 个月"
    ;;

  help|-h|--help)
    sed -n '2,11p' "$0"
    ;;

  *) # 默认:发码。 caret-key.sh <人名> [机器数] [月数]
    ensure_ledger
    name="$cmd"; machines="${2:-3}"; months="${3:-6}"
    [[ "$name" != crk_* ]] || { echo "✗ 第一个参数是人名,不是码" >&2; exit 2; }
    key=$(remote issue-key -plan beta -machines "$machines" -months "$months" 2>/dev/null)
    [[ "$key" == crk_* ]] || { echo "✗ 服务器返回异常: $key" >&2; exit 1; }
    printf "%s\t%s\t%s\t%s\t%s\tactive\n" \
      "$(date +%F)" "$name" "$key" "$machines" "$months" >> "$LEDGER"
    echo "✓ 已发码并登记 → $LEDGER"
    echo
    echo "──── 直接粘贴给「${name}」 ────"
    cat <<MSG
你的 Caret 内测码:${key}
(可绑 ${machines} 台电脑,手机不限,有效期 ${months} 个月)

安装三步:$SITE
① 装桌面助手 → ② 填内测码 → ③ 手机扫码配对

码请勿转发;换电脑可在手机 App 里自助解绑(每 30 天 3 次)。
MSG
    ;;
esac
