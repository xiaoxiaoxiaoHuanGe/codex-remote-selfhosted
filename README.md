# Codex Remote

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](./bridge/go.mod)
[![Flutter](https://img.shields.io/badge/Flutter-3.3+-02569B?logo=flutter&logoColor=white)](./app/pubspec.yaml)

自托管的 **Codex 手机远程控制**：用手机 App 操作本机 [Codex](https://openai.com/codex)——实时查看进行中的操作、读写会话、发送提示词、内嵌预览图片/视频，并审批命令执行。

远程链路完全自托管，**不走 OpenAI 官方账号中继**。AI 算力与鉴权沿用桌面端已登录的 ChatGPT / Codex 账号，**无额外 API 费用**。

> 仓库内的 `relay.example.com`、`203.0.113.10` 均为占位符，部署时请替换为你自己的域名与服务器。

---

## 目录

- [功能特性](#功能特性)
- [架构](#架构)
- [仓库结构](#仓库结构)
- [环境要求](#环境要求)
- [快速开始](#快速开始)
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

| 组件 | 要求 |
|------|------|
| 桌面端 | 已安装并登录 [Codex 桌面版](https://openai.com/codex)（推荐使用 App 自带 CLI） |
| 桥 | Go **1.26+** |
| 手机 App | Flutter **3.3+** / Dart **3.3+** |
| 公网中继（可选） | Linux 服务器、域名、nginx、frps；桌面侧 `frpc`（`brew install frpc`） |

## 快速开始

### 1. 启动桥（本机）

桥**必须**配置 token；未配置时拒绝启动（生产路径不会静默生成随机 token）。

```bash
git clone https://github.com/yunyuchen/codex-remote.git
cd codex-remote/bridge

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

## 构建可分发 App

**切勿把本机 token 打进安装包。** 配对 token 存在手机钥匙串，由用户扫桌面托盘二维码写入。

```bash
./build-apk-release.sh            # 默认输出到 ./dist/
./build-apk-release.sh ~/Desktop  # 或指定目录
```

脚本**不传** `--dart-define=TOKEN`，并带安全网：若产物中扫到本机 token 则拒绝输出。

```bash
# ❌ 危险：仅限本人私用调试，产物禁止外发
cd app && flutter build apk --release \
  --dart-define=TOKEN="$(sed -n 's/^TOKEN=//p' ~/.codex-remote/menubar.env)"
```

## 公网部署

完整步骤见 **[bridge/deploy/DEPLOY.md](./bridge/deploy/DEPLOY.md)**。

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

## 免责声明

- 本项目驱动的是**你自己机器上、你已登录的** Codex / ChatGPT 会话，请自行遵守 [OpenAI 服务条款](https://openai.com/policies) 与当地法律法规。
- 远程控制具备执行命令与读写工作区文件的能力。请妥善保管配对 token，仅在受控设备上安装客户端，并按 DEPLOY.md 加固中继。
- 软件按 “AS IS” 提供，作者不对滥用、配置失误或第三方服务变更导致的损失负责。
