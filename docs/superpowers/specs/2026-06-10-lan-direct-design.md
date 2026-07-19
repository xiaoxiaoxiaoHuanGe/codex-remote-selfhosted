# LAN 直连 + 公网分层 设计（2026-06-10）

## 背景与目标

现状：手机即使与桌面机在同一局域网，也永远绕道公网中继（hub）。本设计让手机在
同一 WiFi 下自动直连桌面 `codexbridge`，并把连接路径做成产品分层：

- **免费档（未激活）**：仅局域网直连。装完即可同 WiFi 使用，无需任何服务端。
- **订阅档（订阅码激活）**：局域网 + 公网中继，自动优先直连，可手动锁定。

硬性需求（已与用户确认）：
1. 仅 Flutter app 支持直连（小程序生产环境连不了局域网裸 IP ws，保持走中继）。
2. 桌面 LAN 监听**默认开**，可通过配置关闭。
3. **中继宕机/断网时**，同一局域网下配对和使用必须完整可用（候选必须本地缓存，
   不依赖实时询问 hub）。
4. 切换形态：auto（自动优先直连，回落中继）+ 手动三档 `auto | lanOnly | relayOnly`。

## 方案选型

选定 **带内候选下发 + 连接竞速**（方案 A）：

- bridge 把自己的 LAN 候选地址通过现有连接推给手机（新 `lanInfo` 帧），托盘 QR
  附带配对当刻的候选作种子；手机按 machineId 本地缓存。
- 连接时 happy-eyeballs 竞速：LAN 候选先拨，中继 300ms 后起拨，先握手者胜。

否决的备选：
- **mDNS/Bonjour**（方案 B）：IP 变化即时正确，但两端新依赖 + iOS 本地网络权限 +
  企业 WiFi 禁多播仍需回落逻辑，是 A 全部工作之上再加一层。留作后续增强。
- **hub 下发候选**（方案 C）：要改 hub 协议并重新部署；中继宕机时拿不到新鲜候选，
  被 A 严格支配。

## 架构

```
同一 WiFi（免费档）:  手机 ──ws(+E2EE)──> codexbridge LAN 监听 (:8767  /ws /file)
公网（订阅档）:      手机 ──wss──> relay.example.com (codexhub) ──agent ws──> codexbridge
```

两条路径说同一套手机协议、用同一个 E2EE codec（PSK 按 machineId 存，与传输路径
无关）。分层的强制点在服务端：未激活机器 hub 拒绝注册（license 模式既有行为），
公网天然不可用；LAN 监听始终可用。app 端 `pub` 标志只驱动 UX 文案，不承担强制。

## 桌面端（bridge + 托盘）

### 1. agent 模式加 LAN 监听

- `runAgent` 同时把现有 `Server.Handler()`（`/ws` `/file` `/healthz`）挂到 LAN
  端口，默认 `:8767`（全接口）。
- 配置：环境变量 `CODEX_LAN_ADDR`，默认 `:8767`，设为 `off` 关闭。
- 端口被占/监听失败：日志警告，agent 正常继续（LAN 失败不致命），QR 与 `lanInfo`
  不带候选（不发布假地址）。
- 认证零新增：`NewServer(cx, token)` 已把机器 token 装进 `s.tokens`，LAN `/ws`
  `/file` 复用常量时间 token 门与全部 clamp（turn 沙箱、cwd、approval、媒体
  ext+magic）。`serve` 子命令行为不变。
- 未激活机器：hub 注册被拒（既有），LAN 监听照常 → 免费档闭环。

### 2. `lanInfo` 帧（候选下发，hub 零改动）

手机会话建立后（E2EE 武装完成后，与 resync 同时机）bridge 推送：

```json
{"type":"lanInfo","candidates":["192.168.1.5:8767","10.0.0.3:8767"],"pub":true}
```

- `candidates`：枚举本机非环回私网 IPv4 + LAN 端口（v1 仅 IPv4；LAN 监听关闭或
  失败时为空数组）。
- `pub`：机器是否已激活公网档（凭据文件存在与否，同托盘 `activated()` 判定）。
- 走中继时它是普通 `msg` 帧负载，hub 逐字转发——hub 不改、不重新部署。
- v1 只在会话建立时发一次；网络变化的刷新靠下次连接。

### 3. 托盘 QR 种子

连接串追加查询参数：`&lan=192.168.1.5:8767,10.0.0.3:8767&pub=0|1`。

- 配对扫码当场拿到直连地址 → 中继完全不可达时也能完成配对（bridge 直连路径
  本来就处理 `pair_init`）。
- 兼容性：老 app 忽略未知参数；新 app 扫老 QR（无 `lan`）行为同现状。
- 托盘菜单顺带显示 LAN 直连状态（监听地址 / 已关闭）。

## App 端（Flutter）

### 数据模型

`Machine` 增字段（JSON 序列化向后兼容，旧存档缺字段取默认值）：

- `lanCandidates: List<String>` — 直连候选缓存（QR 种子 → 每次 `lanInfo` 刷新）。
- `pubEnabled: bool` — 公网档开通标志（QR `pub` → `lanInfo` 刷新），默认 `true`
  （旧存档来自中继配对，公网可用）。
- `linkMode: 'auto' | 'lanOnly' | 'relayOnly'` — 用户三档，默认 `auto`。

### 连接竞速（`ConnectionRacer`）

`bridge_client.connect(wsUrl)` 之上加一层候选解析（新文件，约 150-250 行）：

- URL 构造：LAN 候选 → `ws://<host:port>/ws?token=…`（裸 IP 走 ws，复用
  `hostIsBareIp` 逻辑）；中继 → 现有 `wsUrlForToken`（含 device-auth 变体）。
- `auto`：LAN 候选立刻并发拨号，中继延迟 300ms 起拨；**先完成 WS 升级握手者胜**，
  败者在发出任何帧前关闭。E2EE codec 只在胜者连接上武装——不存在双链路 seq 冲突。
- `lanOnly`/`relayOnly`：只拨对应一侧。`pubEnabled=false` 等效 `lanOnly`。
- **配对首连例外**：`pid`/`epub` 一次性，不竞速。顺序：LAN 候选（1.5s 超时）→
  中继。
- 重连即重竞速（接到现有 `_scheduleReconnect` 的指数退避上）：回家自动切直连，
  出门自动切中继。
- 胜者 URL 成为 `BridgeClient.url` → `/file` 媒体 URL 自动跟随同一路径（直连时
  媒体不过中继）。device-auth 的 `/file` ticket 仅中继路径存在；直连路径用 token
  query（既有 serve 模式行为）。
- 暴露 `transport: direct | relay` 状态。

### UI

- 机器横幅/卡片：「直连」/「中继」徽章。
- 机器详情页：三档选择；`pubEnabled=false` 时中继档置灰 + 文案「订阅后可公网远程」。
- `lanOnly` 连接失败文案：「局域网内找不到这台电脑，确认手机和电脑在同一 WiFi」。

### iOS 权限

直连 LAN IP 触发 iOS 14+ 本地网络权限弹窗：`Info.plist` 加
`NSLocalNetworkUsageDescription`。用户拒绝 → LAN 拨号全失败 → auto 档无感回落
中继；设置页提示去系统设置开启。

## 协议变更面（三端同步清单）

1. 新帧 bridge→phone `lanInfo`：`server.go` 头部协议注释 + `bridge_client.dart`
   处理。
2. QR 连接串新参数 `lan=`、`pub=`：托盘生成 + `config.dart` 解析。
3. `miniprogram/utils/relay.js`：不支持直连，只需确认未知帧类型走 default 分支
   安全忽略（实现时验证）。
4. hub：零改动。

## 安全

- LAN 端点是同一个 `Server` 对象，全部既有安全模型原样生效；`server.go` 头部
  安全模型注释补充 LAN 监听一节。**不削弱任何 clamp。**
- E2EE 同一 PSK 跨两条路径；LAN 上 ws:// 传输层明文但负载有应用层加密。token 在
  URL 跨 LAN 明文，与现状（公网 ws://39001 明文）同级且暴露面更小，可接受；
  后续可叠加直连 device-auth。
- 监听 `0.0.0.0:8767`：macOS 首弹「允许传入网络连接」，安装文档注明。
- 隐私：`lanInfo` 暴露内网 IP 给已配对手机——本来就是同一信任域，可接受。

## 错误处理

| 场景 | 行为 |
|---|---|
| LAN 候选全失败（不同网/过期/权限拒绝） | auto 回落中继；lanOnly 显示找不到提示 |
| LAN 监听端口被占 | 日志警告，agent 继续；QR/lanInfo 无候选 |
| 候选过期（换网/DHCP） | 竞速兜底 + 连接成功后 `lanInfo` 刷新缓存 |
| 中继宕机 | LAN 直连不受影响（候选在本地缓存） |
| 未激活机器走中继 | hub 拒绝（既有）；app 端 `pubEnabled=false` 提前置灰 |
| 配对时双路径 | 不竞速，顺序尝试，`pid` 只消费一次 |

## 测试

- **Go 单测**：agent 模式 LAN 监听（默认开 / `CODEX_LAN_ADDR=off` / 端口冲突不
  崩且不发布候选）；LAN `/ws` token 门生效；`lanInfo` 候选枚举（接口 mock）。
- **Dart 单测**：`ConnectionRacer`（LAN 先胜 / LAN 失败回落中继 / 三档锁定 /
  配对不竞速顺序）；`Machine` 新字段序列化往返 + 旧 JSON 兼容。
- **手动 E2E**：① 断中继，同 WiFi 扫码配对 + 对话 + 看图（免费档闭环）；
  ② 激活后出门走中继；③ 回家重连自动切直连（看徽章）；④ 三档锁定各自生效。

## 范围外（明确不做）

- mDNS/Bonjour 发现（后续增强）。
- 小程序直连。
- IPv6 候选。
- 直连路径的 device-auth 挑战握手。
- hub 协议任何改动。
- `kRelayHost` 默认值迁移到 `relay.example.com`（相关但独立的事项，单独处理）。
