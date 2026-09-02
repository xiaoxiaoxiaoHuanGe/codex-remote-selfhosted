# codexmenubar 二维码审计与最小修复报告

日期：2026-09-01
分支：`fix/direct-public-connection`

## 结论

源码证明二维码不是订阅功能的付费开关。托盘菜单中的二维码、复制连接串、复制 Token 只在托盘配置加载失败时统一禁用；有效配置下没有按 subscription/license、LAN candidate、在线状态或 relay 状态单独禁用 QR 的逻辑。

本次发现并修复了公网 Direct 场景的真实连接问题：原代码看到裸 IP 就自动选择 `ws://`，会把已由 nginx/OpenResty 提供 TLS 的公网入口错误降级为明文 `ws://`。Flutter 端在扫码后还会丢弃原始 scheme，再次按 IP 生成 `ws://`。现在两端均保留/生成公网 `wss://`，LAN candidate 仍保持 `ws://`。

## 1. 二维码菜单位置与启用条件

文件：`bridge/cmd/codexmenubar/main.go`

- 菜单创建在 `onReady()`，约第 198 行：`mQR = systray.AddMenuItem("显示二维码…", ...)`。
- “复制连接串”和“复制 Token”紧随其后创建，约第 199-200 行。
- 只有 `cfgErr != nil` 时，约第 212-216 行，以下菜单项统一调用 `Disable()`：激活订阅码、显示二维码、复制连接串、复制 Token、重启代理、重置密钥、打开数据目录。
- `cfgErr` 来自 `loadConfig()`：读取默认 Windows 配置 `C:\ProgramData\codex-remote\menubar.env` 失败，或其中 `TOKEN` 为空时失败。
- `mQR` 创建后没有其它 `Disable()` 调用；`handleClicks()` 会直接调用 `showQR()`。

因此，如果运行中的托盘确实显示“在线”且只有二维码/复制项灰显，这与当前源码不一致，优先怀疑运行的是旧 exe、另一个配置目录下的 exe，或观察到的“在线”不是当前实例的实时菜单状态。源码没有“未激活所以二维码禁用”的条件。

“复制连接”和“复制 Token”与二维码使用完全相同的启用条件：都是 `cfgErr == nil` 时可用。

## 2. “激活订阅码”的真实作用

`runActivation()` 在 `main.go` 约第 280-313 行：

1. 弹窗要求输入 `crk_` 开头的 key。
2. 调用 `codexbridge activate`，向 Hub 的 `/api/activate` 兑换本机 credential。
3. credential 写入 `CRED_FILE`，然后重启 agent。
4. Hub license 模式下，agent 后续使用每机 credential 注册，替代共享 `AGENT_KEY`。

这不是二维码生成所需条件。二维码中的 `pub=1|0` 只是手机侧公网档位提示；Flutter 端的 `pubEnabled` 主要影响“仅公网”选项显示，不能替代服务端授权。Direct `codexbridge serve` 的 token gate 与 license credential 独立，`serve` 仍可用 token 提供 `/ws`。

`crk_`、`internal/license`、`/api/activate` 均属于本仓库自有的 Hub/license 机制。源码没有显示这是 OpenAI 或 Codex 官方要求；自托管 Direct `serve` 路径不需要该订阅码。

## 3. “局域网直连：开 0”的含义

托盘显示的是 `cfg.lanAddr` 原始配置值：

- 未配置时默认 `:8767`。
- 只有精确值 `off` 才表示关闭 LAN listener。
- `:0` 会让 Go 监听操作系统分配的临时端口；但托盘的 QR seed 读取的是配置值，不能从 agent 运行时获知实际端口，因此不适合作为当前 LAN QR 配置。
- 如果配置是字面量 `0` 而不是 `:0`，它不是特殊开关，通常会成为无效的 `net.Listen` 地址并导致 LAN listener 启动失败。

所以“0”不是订阅状态、二维码数量或机器 ID；它是 LAN_ADDR 配置值的一部分。建议固定使用 `:8767`，关闭时使用 `off`。

## 4. QR payload 精确格式

文件：`bridge/cmd/codexmenubar/main.go`，`pairQR()` 与 `lanParams()`，约第 411-455 行。

payload 是普通 URI，不是 JSON、不是整体 base64、不是自定义 scheme：

```text
wss://<PUBLIC_HOST>/ws?token=<URL编码后的TOKEN>&pub=0|1
```

可选字段：

```text
&lan=<URL编码后的逗号分隔 ip:port 列表>
&pid=<32 字节随机值的 hex>
&epub=<URL编码后的 base64 enrollment public key>
```

字段说明：

- `/ws` 是手机 WebSocket 路径。
- `token` 是 bridge token；不写死在源码，来自 `menubar.env` 的 `TOKEN`。
- `pub` 由 credential 文件或 `AGENT_KEY` 是否存在推导，仅为公网档位提示。
- `lan` 只有检测到 LAN 地址时才追加，因此 LAN candidate 不是生成 QR 的前提。
- `pid` 和 `epub` 仅在 E2EE 配置有效时追加；`pid` 单次、约 5 分钟有效。
- `machineID` 不在 QR URI 中；`machineName` 也不在 QR URI 中。E2EE 配对时 Flutter 会把本地显示名放进后续 `pair_init` 的 `name` 字段。

原代码对 IP 按 IP 猜 scheme：裸 IP 生成 `ws://`，域名生成 `wss://`。这对“公网 IP + nginx/OpenResty TLS”是错误的。

## 5. Flutter 扫码端对照

文件：

- `app/lib/src/ui/qr_scan_screen.dart`
- `app/lib/src/ui/connect_screen.dart`
- `app/lib/src/config.dart`

扫码页只返回二维码中的原始字符串。`ConnectScreen._applyConnectString()` 随后：

- `tokenFromWsUrl()` 读取 `token`；
- `hostFromWsUrl()` 读取 authority（主机和端口）；
- `pairingIdFromConnectString()`、`enrollPubFromConnectString()`、`lanFromConnectString()`、`pubFromConnectString()` 读取可选字段；
- `MachineStore.add()` 将 token、host、LAN candidate、授权提示和配对标志保存到现有 secure storage 的机器列表中。

原先的关键问题是：扫码得到 `wss://<IP>:7446/...` 后，App 只保存 host，`wsUrlForToken()` 又按裸 IP 生成 `ws://<IP>:7446/ws?...`，所以即使 QR payload 是 TLS 地址，最终连接仍可能错误地使用明文 WS。

现在 `wsUrlForToken()` 接受裸 host、`https://...`、`wss://...`，统一构造：

```text
wss://<host>:<port>/ws?token=<Uri.queryParameters 编码后的 token>
```

LAN `lanWsUrl()` 仍明确构造 `ws://`，没有改变 LAN 直连策略。Legacy `BridgeClient.connect()` 传入的完整 `WS_URL` 仍按调用方给定的完整 URL 使用。

## 6. Direct / Hub 差异

Direct：

```text
手机 -> 公网 wss /ws -> nginx/OpenResty -> FRP -> codexbridge serve /ws
```

Hub/Relay：

```text
手机 -> Hub /ws?token=...       (手机侧)
机器 -> Hub /agent             (agent 注册侧)
```

两者都可以让手机使用 `/ws?token=...`，差异在服务端入口和机器连接方式，不靠 QR 中的 `machine` 字段区分。本次没有新增或删除 `machine`、`/agent`、`HUB_AGENT_KEY` 逻辑，也没有修改 Bridge、WebSocket 消息协议、FRP 或 1Panel 配置。

## 7. 修改文件与影响

1. `bridge/cmd/codexmenubar/main.go`
   - 从 `HUB` URL 保存 `ws/wss/http/https` 的显式 scheme。
   - `phoneURL()` 使用配置的 scheme，不再因 host 是 IP 就强制变成 `ws://`。
   - 使用 `url.QueryEscape()` 编码 token。
   - 影响：公网 `wss://IP:7446` 可保持 TLS；明确配置为 `ws://` 的旧式本地/遗留入口仍保持 `ws://`。

2. `bridge/cmd/codexmenubar/main_test.go`
   - 新增公网 IP WSS 和显式旧式 WS 的 URL 回归测试。

3. `app/lib/src/config.dart`
   - 公网/relay URL 支持裸地址、`https://`、`wss://` 输入，统一生成 `wss://.../ws`。
   - 用 `Uri` 的 query parameters 编码 token。
   - LAN candidate 继续使用 `ws://`。

4. `app/test/config_test.dart`
   - 新增三种公网地址形式和特殊 token 的回归测试。

没有把真实公网 IP 或 token 写入源码、APK 配置或本报告。

## 8. 验证结果

通过：

- `D:\flutter\bin\flutter.bat analyze`：无本次修改错误；仅有项目原有的 5 条弃用提示。
- `D:\flutter\bin\flutter.bat test`：28 项全部通过。
- `go test ./cmd/codexmenubar`：通过。
- `D:\flutter\bin\flutter.bat clean`：通过。
- `D:\flutter\bin\flutter.bat pub get`：通过。
- `D:\flutter\bin\flutter.bat build apk --release`：成功。

APK：

`D:\codex-remote\app\build\app\outputs\flutter-apk\app-release.apk`

本次构建大小约 67.1 MiB，SHA-256：

```text
F2B8A348DEED4422102123FCD854A501DC8A0FF71E96B552E5D7B5519A833E3A
```

说明：`go test ./...` 还发现 4 个既有 `internal/bridge` Windows 临时路径测试失败；失败来自测试对 Unix 路径边界的假设（`TestValidateCwd`），不在本次 QR/URL 改动文件或调用路径内，未为此做无关修改。`cmd/codexmenubar` 自身测试通过。

## 9. 运行部署注意事项与尚存风险

- 新 exe 必须替换并重启实际运行的 `codexmenubar.exe`；托盘进程只在启动时读取 `menubar.env`。
- Windows 配置必须至少包含非空 `TOKEN`；Direct 公网 QR 还需要 `HUB` 的 authority 指向实际公网入口，例如 `wss://<PUBLIC_HOST>:7446`。这里的 `<PUBLIC_HOST>` 仅为占位符，不应写成报告中的字面值。
- 如果 `menubar.env` 不存在或 `TOKEN` 为空，源码会按设计禁用二维码和复制项；这不是订阅绕过问题，应修复部署配置或使用已有安装器生成配置。
- 当前环境没有连接到用户实际 Windows 托盘会话、手机摄像头和真实公网入口，因此无法在本次工具会话中完成“点击托盘 QR → 手机扫码 → 实网 sessions”端到端现场验收。URL、扫码字段解析、E2EE 配对字段和 APK 编译均已回归验证。
- `token` 放在 WebSocket URL query 中会出现在部分代理/诊断日志中；现有 Bridge 协议就是 token query 鉴权，本次未改变协议。应继续保护 QR、Token 和安装目录权限。
- Flutter 当前提示 Gradle 8.14、AGP 8.11.1、Kotlin 2.2.20 将来需要升级；这不是本次二维码修复的阻塞项。

## 10. 后续升级建议

当前 MVP 不建议为二维码问题批量升级 Gradle、AGP、Kotlin 或 Flutter。待业务稳定后，应单独建立升级分支，按 Flutter 兼容矩阵逐项升级并重新验证 Android、Windows bridge 和 Hub/Relay。
