# 公网中继部署 (Phase 4)

让手机在任何网络下通过 `wss://relay.example.com` 控制 Mac 上的 Codex。

```
iPhone ──wss/TLS──▶ nginx(relay.example.com:443) ──▶ 127.0.0.1:8700
                                                          │ (frps, 仅监听本机)
                                                  frp 隧道 │ 控制口 7000 + frp-TLS
                                                          ▼
   Mac ── frpc ──▶ 127.0.0.1:8767  codexbridge ──▶ codex app-server(共享 ~/.codex)
```

公网只暴露 **443**(nginx)和 **7000**(frp 控制口,有 token+TLS)。桥端口 8767/8700
两头都只绑 localhost,外网打不到。子域名方案让 App 的 `/ws`、`/file` 路径无需改动。

---

## 一、Mac 端(可一键)

前置:`go`、`frpc`(`brew install frpc`)、Codex 桌面端已登录。

```bash
cd codex-remote/bridge/deploy
./install-mac.sh
```

脚本会:生成 `secrets.env`(BRIDGE_TOKEN + FRP_TOKEN,chmod 600)→ 编译 `codexbridge`
→ 渲染 `frpc.toml` 和两个 launchd agent → 加载它们(开机自启、崩溃自拉)。

结束会打印两样东西,**记下来**:

- 手机连接地址:`wss://relay.example.com/ws?token=<BRIDGE_TOKEN>`
- 要填到服务器的 `FRP_TOKEN`

> frpc 现在会一直尝试连 `example.com:7000`,等服务器端起来后自动接通。

---

## 二、服务器端(腾讯云 example.com)

> 下面命令都在**服务器**上执行(和 Mac 是两台机器)。先把部署文件弄上去,并进入该目录,
> 否则后面的 `cp frps.toml.example …` / `cp systemd/frps.service …` 会找不到文件:
>
> ```bash
> git clone <你的仓库地址> codex-remote   # 或用 scp 把 bridge/deploy 整个传上来
> cd codex-remote/bridge/deploy
> ```

### 1. 装 frps

```bash
# 取对应架构的二进制(示例 amd64)。版本尽量和 Mac 端 frpc 接近:
#   brew 当前给的是 0.69.0,服务器也用 0.69.0 最稳妥。
VER=0.69.0   # 以 github.com/fatedier/frp/releases 最新为准
curl -L -o /tmp/frp.tgz https://github.com/fatedier/frp/releases/download/v${VER}/frp_${VER}_linux_amd64.tar.gz
tar -xzf /tmp/frp.tgz -C /tmp
install -m755 /tmp/frp_${VER}_linux_amd64/frps /usr/local/bin/frps

useradd -r -s /usr/sbin/nologin frp 2>/dev/null || true
mkdir -p /etc/frp
# 把本仓库的 frps.toml.example 拷过去,改名 frps.toml
cp frps.toml.example /etc/frp/frps.toml
```

编辑 `/etc/frp/frps.toml`,把 `__FRP_TOKEN__` 换成 Mac 端打印的 FRP_TOKEN。然后收紧权限,
让密钥只对 root 和 frp 服务用户可读(否则 `cp` 出来的文件是 0644,全机可读):

```bash
chown root:frp /etc/frp/frps.toml && chmod 640 /etc/frp/frps.toml
chmod 750 /etc/frp
```

```bash
cp systemd/frps.service /etc/systemd/system/frps.service
systemctl daemon-reload && systemctl enable --now frps
systemctl status frps      # 看是否 active
```

### 2. 放行 7000

frp 控制口 **TCP 7000** 要让 Mac 连得上。最稳是**只放行 Mac 的出口 IP**(在腾讯云安全组
里把来源设成你家/公司宽带的公网 IP,而不是 0.0.0.0/0),这样 7000 不对全网开放。443 通常已开。
**不要**对外放行 8700(它只绑 127.0.0.1,本就打不到)。

```bash
# firewalld:只允许某个来源 IP 访问 7000(把 1.2.3.4 换成 Mac 出口 IP)
firewall-cmd --permanent --add-rich-rule='rule family=ipv4 source address=1.2.3.4/32 port port=7000 protocol=tcp accept'
firewall-cmd --reload
# 出口 IP 不固定时,退而求其次放行全网(靠 frp 的 token+TLS 兜底):
# firewall-cmd --permanent --add-port=7000/tcp && firewall-cmd --reload
```

> 进阶:给 frps 的 "auth failed" 日志加一条 fail2ban 规则,阻止对 7000 的 token 爆破。

接通后 Mac 端 `~/Library/Logs/codex-remote/frpc.log` 会显示 `start proxy success`。

### 3. nginx + 子域名 + 证书(宝塔)

1. DNS 加一条:`relay.example.com` A 记录 → 服务器公网 IP。
2. 宝塔 → 网站 → 添加站点 `relay.example.com`(纯静态即可)。
3. 该站点 → SSL → Let's Encrypt 申请并开启「强制 HTTPS」。
4. 该站点 → 反向代理 → 添加:
   - 目标 URL:`http://127.0.0.1:8700`
   - 发送域名:`$host`
   - **打开 WebSocket 开关**
5. 反代里建议把超时调大(`proxy_read_timeout 3600s`),并对该 server 关掉 access_log
   (URL 里带 token)。需要手改时参照仓库 `nginx-codex.conf`。

> 不用宝塔反代功能时,直接把 `nginx-codex.conf` 内容并入该站点 vhost 即可。

---

## 三、验证

```bash
# 服务器本机:经隧道打到 Mac 桥的健康检查
curl -s http://127.0.0.1:8700/healthz        # 期望 ok

# 任意机器:走 nginx+TLS
curl -s https://relay.example.com/healthz  # 期望 ok
# 带错 token 应 401:
curl -s -o /dev/null -w '%{http_code}\n' "https://relay.example.com/file?path=/etc/hosts&token=wrong"  # 401
```

手机 App 连接屏填 `wss://relay.example.com/ws?token=<BRIDGE_TOKEN>`,应能拉到会话列表。

---

## 四、安全模型(重要)

- **鉴权**:所有 `/ws`、`/file` 请求按 token 做 SHA-256 + 常量时间比较;空 token 一律拒绝。
  token 由 `CODEX_BRIDGE_TOKEN` 环境变量注入,serve 时若没拿到 token 直接退出(绝不悄悄
  用随机 token 跑公网),且不再把 token 打到日志(只打指纹)。
- **逐设备 token / 吊销**:除默认 token 外,可设 `CODEX_BRIDGE_TOKENS="phone:tokA,ipad:tokB"`
  给每台设备发独立 token;丢了某台只需删掉它那对并重启,不必轮换全部。每条连接按 token
  标上设备名(进审计日志)。App 侧无需改动——各设备在连接串里用各自的 token 即可。
- **审批归属绑定**:某个 turn 的审批**只路由给、且只接受**发起该 turn 的设备来应答(按设备名
  绑定,断线重连仍有效)。另一台持别的 token 的连接能"看"但**应答会被拒并记审计**。
- **审计日志**:连接/鉴权失败/发指令/审批与决定都打 `audit:` 前缀(`bridge.log` 里 grep
  `audit:` 即可复盘谁连过、批准过什么)。
- **远程降权**:手机发起的 turn 强制 `approvalPolicy=untrusted` + `read-only` 沙箱。这是
  **每个 turn** 级别的覆盖,所以即便 resume 一个本机 `danger-full-access` 的会话也会被降权。
- **审批闸钳制**:手机的 accept/decline 会被映射成协议精确值,**绝不**转发
  `acceptForSession`/`approved_for_session`/`*_amendment` 这些"本会话永久放行"的选项——
  一次批准不会打开整段会话的免审批后门。
- **媒体白名单**:`/file` 仅允许 `~/Documents`、`~/.codex/generated_images`、
  `~/.codex/computer-use`(**不含** `~/.codex` 根目录,即拿不到 `auth.json` 凭证),并且只
  放行图片/视频扩展名;凭证/配置/源码/数据库一律 403。
- **隧道**:frp 控制口有独立 token + TLS;桥端口两头只绑 localhost,公网仅 nginx 可达。
- **限流/防爆破**:nginx 对该站点 `limit_req`/`limit_conn`;桥侧限制最大并发连接数。
- **日志**:nginx 该站点 `access_log off`(**必须**),并清空 Referer,避免 token 落盘/外泄。

> ⚠️ **token 即全部权限**:谁拿到 BRIDGE_TOKEN,就能读你所有桌面会话的历史和 cwd
> (`thread/list` + `thread/read`),并能发起(降权后的)指令。务必妥善保管,怀疑泄露立刻轮换。
> 当前是"单人单 token"模型;按设备区分身份/逐设备吊销留到 Phase 6。

### 轮换 token

改 `deploy/secrets.env`(或删掉让脚本重生)→ 重跑 `install-mac.sh` → 同步服务器
`frps.toml` 的 FRP_TOKEN 并 `systemctl restart frps` → App 里更新连接串。

---

## 五、排障

| 现象 | 排查 |
|---|---|
| frpc 日志 `login to server failed` | FRP_TOKEN 两端不一致 / 7000 未放行 / TLS 不匹配 |
| `https://…/healthz` 502 | frps 没起,或 frpc 未接通(看 frpc.log `start proxy success`) |
| WS 连上即断 | 宝塔反代没开 WebSocket 开关;或 `proxy_read_timeout` 太小 |
| App 401 | URL 里 token 与 secrets.env 不一致;或桥没读到 `CODEX_BRIDGE_TOKEN`(看 bridge.log 的 token 指纹) |
| bridge.log 显示退出 `no pairing token` | launchd 没注入 env;重跑 `install-mac.sh` 重渲染 plist |
| 视频/图片打不开 | 文件不在 `~/.codex`、`~/Documents` 白名单内(403) |
