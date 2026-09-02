# Codex Remote

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](./bridge/go.mod)
[![Flutter](https://img.shields.io/badge/Flutter-3.3+-02569B?logo=flutter&logoColor=white)](./app/pubspec.yaml)

自托管的 **Codex 手机远程控制**：用手机 App 操作本机 [Codex](https://openai.com/codex)——实时查看进行中的操作、读写会话、发送提示词、内嵌预览图片/视频，并审批命令执行。

这是基于 [yunyuchen/codex-remote](https://github.com/yunyuchen/codex-remote) 的社区维护衍生版本，重点维护已验证的自托管 Direct/WSS、frp 穿透、Windows 托盘二维码配对和动态模型同步能力。本项目不是 OpenAI 官方产品，也不代表 OpenAI 官方立场。

远程链路完全自托管，**不走 OpenAI 官方账号中继**。AI 算力与鉴权沿用桌面端已登录的 ChatGPT / Codex 账号，**无额外 API 费用**。

> 仓库内的 `relay.example.com`、`203.0.113.10` 均为占位符，部署时请替换为你自己的域名与服务器。

---

## 目录

- [功能特性](#功能特性)
- [架构](#架构)
- [公网穿透是怎么工作的](#公网穿透是怎么工作的)
- [仓库结构](#仓库结构)
- [环境要求](#环境要求)
- [快速开始](#快速开始)
- [Windows 端启动与手机连接](#windows-端启动与手机连接)
- [构建可分发 App](#构建可分发-app)
- [公网部署](#公网部署)
- [安全模型](#安全模型)
- [文档](#文档)
- [贡献](#贡献)
- [许可证](#许可证)
- [免责声明](#免责声明)

## 功能特性

- **会话控制**：列出会话、读取内容、发送 / 打断 turn，流式展示 agent 输出
- **审批闸**：远程命令执行可走手机端审批；桥侧强制降权，拒绝“永久放行”类决策
- **媒体预览**：经受控的 `/file` 端点内嵌查看图片 / 视频（扩展名白名单 + 路径沙箱）
- **双链路**：同一 Wi‑Fi 下局域网直连；外出时走自托管 frp + nginx 中继
- **配对安全**：逐设备 token、扫码配对；token 存手机钥匙串，**不打进安装包**
- **E2EE 骨架**：控制面端到端加密相关实现已落地（详见 `bridge/internal/bridge/e2ee*.go`）
- **多端客户端**：Flutter App（iOS / Android / macOS）、微信小程序轻量版、桌面托盘助手

## 架构

```
iPhone / Android
        │  wss/TLS（公网）或 ws（局域网）
        ▼
 nginx (relay.example.com:443) ──► 127.0.0.1:8700
                                         ▲
                                  frp 隧道（token + TLS）
                                         │
   桌面 ── frpc ──► 127.0.0.1:8767  codexbridge ──► codex app-server
                                                    （共享 ~/.codex）
```

- 公网只暴露 **443**（nginx）与 **7000**（frp 控制口，token + TLS）
- 桥端口两端均绑定 **localhost**，外网不可直达
- 核心思路：复用桌面 Codex 内部的 `codex app-server`（JSON-RPC），不破解客户端  
  可行性结论见 [FINDINGS.md](./FINDINGS.md)

## 公网穿透是怎么工作的

如果手机和 Windows 电脑不在同一个 Wi‑Fi，手机不能直接访问电脑的 `127.0.0.1`。本项目使用一台有公网 IP 的 Linux 服务器作为入口：

1. 手机通过 HTTPS/WSS 连接服务器的 nginx。
2. nginx 把 `/ws` 和 `/file` 请求转给服务器本机的 frps 映射端口。
3. Windows 上的 frpc 主动连接 frps，并把本机 `codexbridge` 的端口通过加密隧道映射出去。
4. frps 通过这条隧道把请求送到 Windows 的 codexbridge。
5. codexbridge 再连接本机 Codex app-server。

可以把 frpc/frps 理解成“把电脑里的本地服务接到公网入口的一条加密管道”。frpc 负责从电脑向外建立管道，frps 负责在公网服务器接收和转发流量；它们不是 Codex 协议本身。

本仓库不复制 frp/frpc 源码，部署时按系统安装对应的官方版本，并使用仓库中的示例配置。frp 是可选的第三方组件，版权和许可证请以 [frp 官方仓库](https://github.com/fatedier/frp) 及其 [Apache-2.0 许可证](https://github.com/fatedier/frp/blob/dev/LICENSE)为准。

### 三种连接方式

| 方式 | 适用场景 | 手机连接地址 |
|------|----------|--------------|
| 局域网直连 | 手机和电脑在同一网络 | `ws://电脑局域网地址:8767/ws?token=...` |
| Direct 公网 | 公网入口直接转发到 bridge | `wss://你的域名或IP:端口/ws?token=...` |
| frp + nginx | 推荐的自托管公网部署 | `wss://你的域名/ws?token=...` |

无论使用哪种方式，`CODEX_BRIDGE_TOKEN` 都只用于 bridge 鉴权。请使用自己的占位符或环境变量，不要把真实 token 写入源码、README、二维码截图或 APK。

## 仓库结构

```text
codex-remote/
├── bridge/                 # Go：桥接器 / hub / 托盘 / 部署套件
│   ├── cmd/codexbridge/    # 主桥（serve / agent / activate / 只读 CLI）
│   ├── cmd/codexhub/       # 多机中继 hub + 许可证 / 管理 API
│   ├── cmd/codexmenubar/   # 桌面托盘（配对二维码、激活订阅码）
│   ├── deploy/             # frp / nginx / launchd / systemd / 安装脚本
│   └── internal/           # 协议翻译、鉴权、E2EE、provision 等
├── app/                    # Flutter 手机 / 桌面客户端
├── miniprogram/            # 微信小程序轻量版
├── protocol/               # app-server JSON Schema
├── website/                # 产品落地页（静态）
├── scripts/                # 只读探针（复现 Phase 0 验证）
├── docs/                   # 设计说明与开发笔记
├── build-apk-release.sh    # 可分发 APK 构建（禁止内置 token）
├── FINDINGS.md             # 可行性验证结论
└── LICENSE                 # MIT
```

## 环境要求

### 已验证的复现基线

下面这组环境是本项目当前已实际验证通过的基线。想复刻同样效果，建议先保持版本一致：

| 组件 | 要求 |
|------|------|
| 操作系统 | Windows 11 x64 |
| Flutter | 3.47.2 stable |
| Dart | 3.13.2 |
| Java | Eclipse Temurin 17.0.20 |
| Android SDK | 已安装并能被 Flutter 识别；示例路径为 `E:\Android\sdk` |
| Go | 1.26 或更高版本 |
| frp/frpc | Windows 侧已验证 `0.71.0`；frps 与 frpc 建议使用相同版本 |
| 桌面端 | 已安装并登录 [Codex 桌面版](https://openai.com/codex)（推荐使用 App 自带 CLI） |
| 手机 App | Android 手机，USB 调试或可安装 Release APK |
| 公网中继 | Linux 服务器、域名、TLS 证书、nginx/OpenResty、frps |

先检查环境：

```powershell
flutter --version
flutter doctor -v
java -version
go version
frpc.exe --version
```

如果这些版本和上表差异较大，仍可能可以运行，但不再属于本 README 保证的复现基线；尤其是 Codex Desktop 更新后，app-server 的模型列表和 RPC 能力可能变化。

## 快速开始

### 1. 启动桥（本机）

桥**必须**配置 token；未配置时拒绝启动（生产路径不会静默生成随机 token）。

```bash
git clone https://github.com/xiaoxiaoxiaoHuanGe/codex-remote-selfhosted.git
cd codex-remote-selfhosted/bridge

# 只读：列出会话 / 读取某个会话
go run ./cmd/codexbridge list
go run ./cmd/codexbridge read <THREAD_ID>

# 本地开发：固定 token
CODEX_BRIDGE_TOKEN=devtoken go run ./cmd/codexbridge serve -addr 127.0.0.1:8767

# 或一次性随机 token（仅本地；会打印到 stderr）
go run ./cmd/codexbridge serve -addr 127.0.0.1:8767 -dev-random-token
```

启动后连接地址形如：

```text
ws://127.0.0.1:8767/ws?token=<你的 token>
```

日志只打印 token **指纹**，不打印明文。

> 远程发起的 turn 会被桥强制降权：`approvalPolicy=untrusted` + 只读沙箱（`workspace-write` 且默认无网络）。

### 2. 运行 Flutter App

```bash
cd app
flutter pub get
flutter run -d <device> \
  --dart-define=WS_URL='ws://127.0.0.1:8767/ws?token=devtoken'
```

生产 / 自托管中继时，用自己的域名覆盖默认占位符：

```bash
flutter run -d <device> \
  --dart-define=RELAY_HOST=relay.example.com
```

### 3. 只读探针（可选）

用于复现「共享 `~/.codex` 的 app-server 可读会话」这一结论：

```bash
# 优先使用桌面 App 自带 CLI（功能更全）
export CODEX=/Applications/Codex.app/Contents/Resources/codex
python3 scripts/probe_list.py
python3 scripts/probe_read.py <THREAD_ID>
```

## Windows 端启动与手机连接

下面是“Windows 电脑 + 公网服务器 + frpc + Android App”的最短流程。命令中的地址、端口和 token 都是示例，请替换成你自己的值。

### 1. 编译并启动 codexbridge

在 Windows PowerShell 中：

```powershell
cd D:\codex-remote\bridge
go build -o codexbridge.exe ./cmd/codexbridge

# 只在当前 PowerShell 会话中设置；不要写入源码或提交到 Git
$env:CODEX_BRIDGE_TOKEN = "YOUR_BRIDGE_TOKEN"
.\codexbridge.exe -codex "C:\Path\To\codex.exe" serve -addr 127.0.0.1:8767
```

如果 `codex.exe` 已在 PATH 中，可以省略 `-codex`。bridge 正常启动后，只监听 Windows 本机的 `127.0.0.1:8767`。

### 2. 启动 frpc

在公网服务器上运行 frps，在 Windows 上运行 frpc。先复制示例配置：

```powershell
cd D:\codex-remote\bridge\deploy
Copy-Item .\frpc.toml.example .\frpc.toml
notepad .\frpc.toml
```

把示例中的服务器地址、FRP token、远端映射端口按 [公网部署文档](./bridge/deploy/DEPLOY.md) 填好，然后运行：

```powershell
frpc.exe -c .\frpc.toml
```

`frpc.toml` 已被 `.gitignore` 忽略，因为其中会包含 FRP token。不要把真实配置提交到公开仓库。

### 3. 配置 Windows 托盘程序

托盘程序读取 `%ProgramData%\codex-remote\menubar.env`。Direct 公网模式可以使用下面的最小配置：

```text
MACHINE_ID=my-windows-pc
MACHINE_NAME=我的 Windows 电脑
HUB=wss://your-public-host:7446
TOKEN=YOUR_BRIDGE_TOKEN
LAN_ADDR=off
```

这里的 `HUB` 是历史配置字段名；Direct 模式下它只表示公网 WSS 的基础地址，不需要填写 `/agent`，也不需要 `AGENT_KEY` 或订阅码。托盘程序会据此生成：

```text
wss://your-public-host:7446/ws?token=YOUR_BRIDGE_TOKEN
```

编译并启动托盘：

```powershell
cd D:\codex-remote\bridge
go build -ldflags="-H windowsgui" -o codexmenubar.exe ./cmd/codexmenubar
.\codexmenubar.exe
```

看到托盘图标后，右键选择“显示二维码…”，手机 App 扫码即可；也可以选择“复制连接串”，在 App 的手工连接页面粘贴。

### 4. Android App 连接

手机和 Windows 不必在同一 Wi‑Fi。扫码或手工输入以下信息：

- 电脑名称：随便填写一个便于识别的名称
- 访问令牌：与 bridge 的 `CODEX_BRIDGE_TOKEN` 完全一致
- 中继/Direct 地址：`wss://your-public-host:7446`、`https://your-public-host:7446` 或 `your-public-host:7446`

App 会统一规范化为 `wss://your-public-host:7446/ws?token=...`，并把 token 存在手机安全存储中。若连接失败，先依次检查 bridge、frpc、服务器 nginx/WebSocket 转发和 token 是否一致。

### 5. 如何确认连接成功

连接成功后，App 应能看到桌面 Codex 的 sessions，并能读取 thread、发送 prompt 和接收实时回复。模型选择器会在 bridge 从 Codex app-server 获取成功时显示当前桌面端真实可用模型；获取失败时会明确提示并使用 fallback 列表。

## 构建可分发 App

**切勿把本机 token 打进安装包。** 配对 token 存在手机钥匙串，由用户扫桌面托盘二维码写入。

```bash
./build-apk-release.sh            # 默认输出到 ./dist/
./build-apk-release.sh ~/Desktop  # 或指定目录
```

在 Windows 上也可以直接构建：

```powershell
cd D:\codex-remote\app
flutter clean
flutter pub get
flutter analyze
flutter test
flutter build apk --release
```

成功后 APK 位于：

```text
D:\codex-remote\app\build\app\outputs\flutter-apk\app-release.apk
```

构建命令不会把 Bridge Token 写入 APK。Token 由用户扫码或手工连接时输入，并保存在手机安全存储中。

脚本**不传** `--dart-define=TOKEN`，并带安全网：若产物中扫到本机 token 则拒绝输出。

```bash
# ❌ 危险：仅限本人私用调试，产物禁止外发
cd app && flutter build apk --release \
  --dart-define=TOKEN="$(sed -n 's/^TOKEN=//p' ~/.codex-remote/menubar.env)"
```

## 公网部署

完整步骤见 **[bridge/deploy/DEPLOY.md](./bridge/deploy/DEPLOY.md)**。

第一次部署时，建议先完成局域网连接，再配置公网服务器和 frp。这样可以把“App、bridge、Codex”本身的问题与“公网 DNS、证书、nginx、frp”问题分开排查。

概要：

1. **服务器**：安装 frps + nginx，TLS 终止于 nginx，反代到本机 frp 映射端口；限制 7000 来源更佳  
2. **桌面**：`bridge/deploy/install-mac.sh`（或 Windows 安装器）生成密钥、编译桥、注册开机自启  
3. **手机**：扫托盘二维码配对；外出走 `wss://你的域名/ws?token=...`

多机中继 / 许可证模式见 [bridge/deploy/HUB.md](./bridge/deploy/HUB.md)。

占位符替换清单：

| 占位符 | 含义 |
|--------|------|
| `relay.example.com` | 你的中继域名 |
| `203.0.113.10` | 你的服务器 IP（RFC 5737 文档地址） |
| `RELAY_HOST` / `-hub` | App 与 agent 使用的中继地址 |

## 安全模型

详细部署加固见 DEPLOY.md「安全」章节。桥侧基线包括：

| 机制 | 说明 |
|------|------|
| Token 鉴权 | `/ws`、`/file` 均校验；SHA-256 + 常量时间比较；空 token 表 fail-closed |
| 逐设备 token | 可单独吊销；审批归属绑定发起设备 |
| 远程降权 | 每 turn 覆盖策略，resume 旧会话也降权 |
| 审批钳制 | 不转发 `acceptForSession` / 持久化 amendment 等“永久放行” |
| `/file` 沙箱 | 媒体根目录 + 扩展名白名单 + symlink 解析后前缀校验；不暴露 `~/.codex` |
| 中继面收敛 | frp 控制口独立 token+TLS；桥只绑 localhost；nginx 建议关 access_log、限流 |

报告漏洞请参阅 [SECURITY.md](./SECURITY.md)，**不要**开公开 Issue 贴利用细节。

## 文档

| 文档 | 内容 |
|------|------|
| [FINDINGS.md](./FINDINGS.md) | Phase 0 可行性验证（协议能力、探针结论） |
| [bridge/deploy/DEPLOY.md](./bridge/deploy/DEPLOY.md) | 公网中继部署 |
| [bridge/deploy/HUB.md](./bridge/deploy/HUB.md) | 多机 hub 模式 |
| [bridge/PUSH.md](./bridge/PUSH.md) | 后台重同步与推送骨架（APNs / FCM） |
| [miniprogram/README.md](./miniprogram/README.md) | 微信小程序说明 |
| [docs/superpowers/](./docs/superpowers/) | 设计说明与实现计划 |
| [CONTRIBUTING.md](./CONTRIBUTING.md) | 贡献指南 |
| [SECURITY.md](./SECURITY.md) | 安全策略 |

## 贡献

欢迎 Issue 与 Pull Request。提交前请阅读 [CONTRIBUTING.md](./CONTRIBUTING.md)。

行为准则见 [CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md)。

## 许可证

本项目以 [MIT License](./LICENSE) 发布。

本仓库保留上游项目的 MIT 许可和来源说明。第三方依赖（包括 Flutter/Dart 包、Go 模块、frp/frpc 和 nginx）仍受各自许可证约束；发布二进制时请同时遵守对应组件的许可证要求。

## 免责声明

- 本项目驱动的是**你自己机器上、你已登录的** Codex / ChatGPT 会话，请自行遵守 [OpenAI 服务条款](https://openai.com/policies) 与当地法律法规。
- 远程控制具备执行命令与读写工作区文件的能力。请妥善保管配对 token，仅在受控设备上安装客户端，并按 DEPLOY.md 加固中继。
- 软件按 “AS IS” 提供，作者不对滥用、配置失误或第三方服务变更导致的损失负责。
