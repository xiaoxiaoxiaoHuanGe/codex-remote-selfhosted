<p align="center">
  <img src="app/assets/icon/icon.png" width="112" alt="Codex Remote 应用图标">
</p>

<h1 align="center">Codex Remote</h1>

<p align="center">通过自托管连接，在手机上操作电脑里的 Codex。</p>

<p align="center">
  <a href="https://github.com/xiaoxiaoxiaoHuanGe/codex-remote-selfhosted/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/xiaoxiaoxiaoHuanGe/codex-remote-selfhosted?style=flat-square"></a>
  <a href="bridge/go.mod"><img alt="Go 1.26+" src="https://img.shields.io/badge/Go-1.26%2B-526273?style=flat-square"></a>
  <a href="LICENSE"><img alt="MIT license" src="https://img.shields.io/badge/License-MIT-526273?style=flat-square"></a>
</p>

<p align="center">
  <a href="#快速开始">快速开始</a> ·
  <a href="bridge/deploy/DEPLOY.md">公网部署</a> ·
  <a href="docs/REFERENCE.md">完整说明</a> ·
  <a href="https://github.com/xiaoxiaoxiaoHuanGe/codex-remote-selfhosted/issues">反馈问题</a>
</p>

---

Codex Remote 由 **Go 桥接服务 + Flutter 客户端**组成，复用电脑上的 `codex app-server`，
让手机查看会话、发送提示词、接收实时输出和处理执行审批。远程连接由你部署；模型使用与额度取决于本机 Codex 配置。

本项目基于 [yunyuchen/codex-remote](https://github.com/yunyuchen/codex-remote) 维护，保留上游来源与许可。
这是社区衍生项目，不是 OpenAI 官方产品，也不代表其官方立场。

## 可以做什么

| 功能 | 使用方式 |
| --- | --- |
| 💬 会话控制 | 列出和读取桌面会话，发送或打断 turn，接收流式输出 |
| ✅ 远程审批 | 在手机确认或拒绝操作，桥侧限制沙箱和审批权限 |
| 🖼️ 媒体预览 | 通过受鉴权、路径和文件类型限制的 `/file` 端点查看图片及视频 |
| 🌐 自托管连接 | 局域网直连，或通过 Direct WSS、frp + nginx 连接公网入口 |
| 📱 扫码配对 | Windows 托盘生成连接二维码，客户端将 Token 保存在安全存储中 |
| 🧠 模型同步 | 从本机 app-server 获取可用模型；失败时明确使用备用列表 |

仓库另有 iOS / macOS 工程、微信小程序和 hub / E2EE 相关实现。
这些模式需额外配置，当前 README 的主路径是 **Windows + Android + 自托管 WSS**。

## 快速开始

### 1. 启动电脑上的桥

需要 Go 1.26+，以及已配置好账号、可运行 `app-server` 的 Codex CLI。
在 Windows PowerShell 执行；将 Token 换成自己生成的随机值：

```powershell
git clone https://github.com/xiaoxiaoxiaoHuanGe/codex-remote-selfhosted.git
cd codex-remote-selfhosted\bridge
go build -o codexbridge.exe ./cmd/codexbridge
$env:CODEX_BRIDGE_TOKEN = 'YOUR_RANDOM_BRIDGE_TOKEN'
.\codexbridge.exe serve -addr 127.0.0.1:8767
```

CLI 不在 PATH 时，在 `serve` 前加 `-codex "C:\Path\To\codex.exe"`。
未配置 Token 时服务拒绝启动；正常日志仅记录 Token 指纹。

在电脑的另一终端验证：

```powershell
Invoke-RestMethod http://127.0.0.1:8767/healthz
```

### 2. 配置手机可访问的入口

上面的桥只监听电脑本机。按 [公网部署](bridge/deploy/DEPLOY.md) 配置 frps、frpc 和 HTTPS/WSS 反向代理，
或使用自己的 Direct WSS 入口。需要可验证的 TLS 证书，并转发 `/ws`、`/file` 和 `/healthz`。

`relay.example.com`、`203.0.113.10` 均为文档占位符。手机不能通过自己的 `127.0.0.1` 访问电脑。
完整 Windows 托盘配置和三种连接方式见 [部署与参考](docs/REFERENCE.md)。

### 3. 连接 Android APP

从 [Releases](https://github.com/xiaoxiaoxiaoHuanGe/codex-remote-selfhosted/releases/latest) 下载 `app-release.apk`。
安装后扫描电脑托盘二维码，或手动输入电脑名称、访问令牌及 WSS 入口。

访问令牌与 `CODEX_BRIDGE_TOKEN` 一致。连接成功后应能看到会话列表，读取会话并收到提示词的实时回复。
连接失败时按顺序检查桥、frpc、反向代理、证书和 Token。

## 权限与安全

> [!IMPORTANT]
> 配对 Token 可用于读取桌面会话和发起远程操作。不要将真实 Token 写入仓库、安装包、公开日志或二维码截图。

| 机制 | 当前行为 |
| --- | --- |
| 远程沙箱 | 固定为 `workspace-write`，允许工作区写入，沙箱内网络关闭 |
| 默认审批 | `on-request`；手机请求 full 模式时钳制为 `on-failure`，仍受相同沙箱限制 |
| 自动审批 | 需要桥端显式允许，手机单独选择无法开启 |
| 设备权限 | 支持逐设备 Token 和单独吊销，审批绑定发起设备 |
| 媒体读取 | Token 校验、目录限制、符号链接校验及文件类型检查 |

Direct WSS 不等同于已启用控制面 E2EE。hub / E2EE 模式需按对应文档配置。
反向代理日志可能记录 URL 中的 Token，公网部署需遵循 [安全说明](bridge/deploy/DEPLOY.md#四安全模型重要)。

## 开发与文档

Flutter 环境与项目此前记录的实机基线见 [完整说明](docs/REFERENCE.md#环境要求)。
`app/pubspec.yaml` 中的 `>=3.3.0 <4.0.0` 是 Dart SDK 范围。

<details>
<summary>构建客户端与运行检查</summary>

从仓库根目录在独立终端执行：

```powershell
cd bridge
go test ./...
```

```powershell
cd app
flutter pub get
flutter analyze
flutter test
flutter build apk --release
```

APK 输出：`app/build/app/outputs/flutter-apk/app-release.apk`。
Linux / macOS 也可从根目录运行 `./build-apk-release.sh`，默认输出到 `dist/`。
可分发安装包不得通过 `TOKEN` 或包含 Token 的 `WS_URL` 写入真实凭据；用户在安装后配对。

</details>

| 文档 | 内容 |
| --- | --- |
| [部署与参考](docs/REFERENCE.md) | Windows 托盘、环境记录、连接方式、仓库结构与只读探针 |
| [公网部署](bridge/deploy/DEPLOY.md) | frp、反向代理、TLS 与排障 |
| [Hub 模式](bridge/deploy/HUB.md) | 多机中继与许可证模式 |
| [推送说明](bridge/PUSH.md) | APNs / FCM 推送骨架及配置 |
| [小程序](miniprogram/README.md) | 微信客户端 |
| [FINDINGS.md](FINDINGS.md) | app-server 协议与可行性记录 |

## 作者、许可与参与

上游作者 **yunyuchen**，衍生仓库由 [xiaoxiaoxiaoHuanGe](https://github.com/xiaoxiaoxiaoHuanGe) 维护。
保留上游 [MIT License](LICENSE)；Flutter/Dart 包、Go 模块、frp/frpc 和 nginx 遵循各自许可。
frp 是可选的独立组件，其源码与许可见 [官方仓库](https://github.com/fatedier/frp)。

贡献请阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 和 [行为准则](CODE_OF_CONDUCT.md)；漏洞报告见 [SECURITY.md](SECURITY.md)。
请仅操作自己机器上已获授权的会话，并遵守 [OpenAI 服务条款](https://openai.com/policies) 及适用法律。
软件按 “AS IS” 提供，作者不对滥用、配置失误或第三方服务变更造成的损失负责。
