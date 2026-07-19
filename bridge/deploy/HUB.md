# 多机中继 (Hub) 部署

让一批机器(Win/Mac)都能被手机远程操控,**加机器不用加域名、不用改服务器**。

## 架构

```
机器(Win/Mac) 的桥 ──拨出 wss──▶  relay.example.com  ──▶ codexhub(127.0.0.1:8090)
   agent 模式,上报 machineId+token   (Cloudflare→nginx)         ▲
                                                                 │ 按 token 找到对应机器,透明转发
手机 ──wss──▶ relay.example.com/ws?token=<该机token>  ───────────┘
```

- **一个域名永远够用**:`relay.example.com`(已配 Cloudflare + nginx + hub)。
- **加一台机器** = 在那台跑一个 `codexbridge agent`(填 hub 地址 + machineId + 该机 token + 共享 agentKey)。没了。
- 手机协议不变,hub 只做透明转发(连 `/file` 媒体也代理)。

## Token 模型(怎么对应机器)

| 值 | 谁持有 | 作用 |
|---|---|---|
| **AGENT_KEY** | 所有机器共享(=hub 的 `HUB_AGENT_KEY`) | 机器向 hub 注册的"准入钥匙" |
| **机器 token** | 每台一个(唯一) | = 机器钥匙。手机用它连 → hub 按 token 找到那台机器 |

手机侧每台机器存一条:`{名字, wss://relay.example.com/ws?token=<该机token>}`。
**token 唯一对应一台机器**;换某台 token 只影响那台。machine 参数可省(hub 按 token 认机器)。

> 现网值在本机 `bridge/deploy/secrets.env`:`AGENT_KEY`(共享)、每台机器各自的 token。**绝不入库**。

## 部署一台机器

### Mac
```bash
cd codex-remote/bridge/deploy
HUB=wss://relay.example.com/agent AGENT_KEY=<共享key> \
MACHINE_ID=mac-studio MACHINE_NAME="我的 Mac" \
./install-agent-mac.sh        # 生成该机 token、装 launchd 自启、打印手机连接串
```

### Windows(PowerShell,管理员)
```powershell
cd codex-remote\bridge\deploy
# 先确认 Codex 的 codex.exe 路径(官方桌面端);找不到用 -CodexPath 指定
./install-agent-windows.ps1 -Hub wss://relay.example.com/agent -AgentKey <共享key> `
  -MachineId office-win -MachineName "办公室Win" -CodexPath "C:\path\to\codex.exe"
# 装成 Windows 服务(nssm,自动下载),开机自启;打印手机连接串
```

两个脚本都会:取/生成该机 token → 渲染 agent 配置 → 装服务自启 → 打印
`wss://relay.example.com/ws?token=<该机token>`,把它填进手机 App 即可。

## 验证

```bash
# 服务器上看在线机器:
curl -s http://127.0.0.1:8090/machines    # [{"id":"...","name":"..."}]
```
手机 App 连 `wss://relay.example.com/ws?token=<该机token>`,应能拉到该机会话。

## 吊销 / 下线一台

- 停掉该机的服务(launchd unload / Windows 停服务),它就从 `/machines` 消失。
- 想让旧 token 彻底失效:换该机 token 重装即可(其它机器不受影响)。

## License 模式(订阅内测发 key)

默认关闭;开启后 agent 注册从共享 `HUB_AGENT_KEY` 切换为**每机凭证**(由激活签发),
并启用 `/api/activate`、`/api/roster`、`/api/roster/deactivate`。

```bash
# hub 侧开启(systemd unit 的 Environment 里加):
HUB_LICENSE_MODE=1
HUB_DB=/var/lib/codexhub/hub.db        # 不设默认 ./hub.db

# 运营发 key(在 hub 主机上跑,与运行中的 hub 共存安全 — SQLite WAL):
codexhub admin issue-key  -plan beta -machines 3 -months 6   # key 只打印一次
codexhub admin list-keys
codexhub admin revoke-key  crk_...                           # 一小时内踢线
codexhub admin extend-key  -months 6 crk_...
codexhub admin unbind      <machine-id>                      # 兜底人工解绑(不限频)

# 用户侧激活(桌面机上跑一次,凭证写本机文件):
codexbridge activate -hub https://<HUB域名> -key crk_... -id my-mac -out ~/.codex-remote-cred
codexbridge agent -hub wss://<HUB域名>/agent -id my-mac -cred-file ~/.codex-remote-cred -token <手机配对token>
```

规则:每 key 限绑 N 台(满了要先解绑一台);自助解绑每 30 天 3 次(防多人轮流共用 key);
同机重装(硬件指纹匹配)自动复用名额不计次;订阅到期有 72h 宽限(带续费提醒),宽限过后拒绝
注册但**名册保留**,续费即恢复;局域网直连模式不经 hub,到期后仍可用(降级不锁死)。

**备份**:`hub.db` 是名册唯一真相源,建议每日备份:
`sqlite3 /var/lib/codexhub/hub.db ".backup /var/backups/hub-$(date +%F).db"`

## 安全要点(经对抗式 review 加固)

- **token 鉴权**:hub 按常量时间比对 token + agentKey;**token 必须每机唯一**(注册时拒绝重复 token,避免按 token 路由到错机器);machineId 已在线则拒绝重复注册(防顶替)。
- **降权仍在机器侧**:每台桥强制远程 turn 降权(untrusted + 只读沙箱)、审批闸钳制、`/file` 媒体白名单(拿不到 `~/.codex/auth.json`)。
- **DoS 护栏**:每机手机连接数上限(8);`/agent` 注册前小读上限 + 10s 超时(防 slow-loris);`/file` 单文件上限 12MB(防 OOM)。
- **`/machines` 需鉴权**:`?key=<AGENT_KEY>` 才返回,公网枚举不到机器名单。
- 传输:手机↔hub、机器↔hub 都过 Cloudflare TLS;hub 只绑 127.0.0.1,公网仅经 nginx;链路两端每 25s ping 保活。

### 信任模型 / 已知限制(测试期可接受)

- **每台机器的 token = 该机完全控制权**(单一拥有者模型)。同一台机器的 token 若同时给多部手机,这几部手机之间**不互相隔离**:会看到彼此的事件流,也能应答彼此 turn 的审批(都受只读降权约束)。要严格按手机隔离 turn/审批,需后续给每部手机独立身份(留作 Phase 7)。
- **媒体走整文件缓冲**(≤12MB),非分块流式;大文件目前会 413。分块流式 + Range、按 turn 路由事件、每会话取消在途 RPC、慢对端独立队列等性能/隔离细化是已记录的后续项。
