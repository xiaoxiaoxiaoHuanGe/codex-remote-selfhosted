# Phase 0 — 可行性验证结论（已实测通过）

> 目标：手机 App 远程操作本机 Codex —— 实时看到正在发生的操作、读取当前会话、发送提示词。
> 不走 OpenAI 官方账号中继，远程链路自托管（腾讯云 frp）。

## 一句话结论

**完全可行，且比最初设想更简单。** 关键在于：Codex 的桌面 app 内部就是一个 `codex app-server`（JSON-RPC over stdio/ws）。
我们**不需要破解桌面 app**，只要让一个共享 `~/.codex` 的 `codex app-server` 跑起来，就能用协议读会话、读内容、驱动 Codex，
**鉴权与模型完全沿用桌面 app（ChatGPT 账号 + gpt-5.5），零额外 API 费用**。

## 环境实况（2026-06-02，本机 macOS）

- 用户 CLI：`codex 0.125.0`（`/Applications/ServBay/package/node/current/bin/codex`）
- 桌面 app 自带 CLI：`codex 0.136.0-alpha.2`（`/Applications/Codex.app/Contents/Resources/codex`）—— **功能更全，产品应使用它**
- 桌面 Codex.app 正在运行（Electron，PID 71706），内部已起 `codex app-server` 子进程（PID 71886），二者用 stdio socketpair 私聊
- 桌面 app 配置 `~/.codex/config.toml`：`model=gpt-5.5`、`service_tier=fast`、**`approval_policy=never`、`sandbox_mode=danger-full-access`**（全自动、无审批、全盘权限 —— 远程安全的重点风险，见下）

## 已验证的能力（只读探针，未改动任何会话）

| 能力 | 方法 | 结果 |
|---|---|---|
| 握手 | `initialize` + `initialized` 通知 | OK，返回 `userAgent/codexHome/platformFamily/platformOs` |
| 列出全部会话 | `thread/list {limit}` | OK，读到 8 个真实会话（threadId/title/cwd/updatedAt） |
| 读取会话内容 | `thread/read {threadId, includeTurns:true}` | OK，item 类型：`userMessage / agentMessage / fileChange / mcpToolCall` |
| 列 turns | `thread/turns/list` | 需在 initialize 时声明 `capabilities.experimentalApi=true` |

探针脚本见 `scripts/probe_list.py`、`scripts/probe_read.py`。协议完整 JSON Schema 见 `protocol/`（由 `codex app-server generate-json-schema --out protocol` 导出）。

## 协议关键方法（客户端 → 服务端）

- 读：`thread/list`、`thread/loaded/list`、`thread/read`、`thread/turns/list`(experimental)
- 控：`thread/resume {threadId,...}`（挂接某会话）、`turn/start`（发提示词）、**`turn/steer`（往进行中的 turn 插话）**、`turn/interrupt`（打断）
- 远程：`remoteControl/enable|disable`、`remoteControl/status/read`（官方手机控制底层，enroll 到 OpenAI 中继，账号锁定，我们不用）

## 实时镜像的推送流（服务端 → 客户端通知）

`item/agentMessage/delta`（回答逐字）、`item/reasoning/textDelta`/`summaryTextDelta`（推理流）、
`item/commandExecution/outputDelta`（命令实时输出）、`item/started`/`item/completed`、
`turn/started`/`turn/completed`、`turn/diff/updated`（diff）、`thread/tokenUsage/updated`（用量）。

## 远程暴露：桌面 0.136 的 app-server 直接支持（无需自写协议层）

```
codex app-server --listen ws://127.0.0.1:PORT \
  --ws-auth capability-token --ws-token-file <PATH> --ws-token-sha256 <HEX>
# 或 signed-bearer-token (JWT: --ws-shared-secret-file/--ws-issuer/--ws-audience)
```

另有 `codex app-server daemon {start|restart|stop|enable-remote-control|bootstrap}`：
常驻共享实例 + 控制套接字 `~/.codex/app-server-control/app-server-control.sock`，多客户端用 `codex app-server proxy` 挂接。

> 注：桌面 GUI 用的是它**自己进程内**的 app-server，默认不开放控制套接字（`ipc-501.sock` 是 Electron 主进程 IPC，协议不通）。
> 我们的方案用**另起一个共享 `~/.codex` 的 app-server**（stdio 或 daemon），与 GUI 共享磁盘上的会话存储。

## 修订后的架构

```
 Flutter App (iOS/Android)
     │  ① 局域网直连 wss        ② 公网 frp 中继 (relay.example.com)
     ▼
 ┌──────────────────────────── 桌面 Bridge (C#/.NET 8) ────────────────────────────┐
 │  child: codex app-server (0.136, stdio, 共享 ~/.codex, 沿用账号鉴权)              │
 │  · 协议翻译: app-server JSON-RPC  ⇄  精简且加固的手机协议                          │
 │  · 远程安全闸: 远程发起的 turn 强制 approvalPolicy=untrusted / sandbox 收紧        │
 │    （本机 config 是 danger-full-access，绝不能让公网指令直接全权执行）             │
 │  · QR 配对 + 每设备 token；frpc 出站隧道；事件转发                                 │
 │  · push 分发器 → FCM/APNs（后台/杀进程时由服务器侧推送）                          │
 └──────────────────────────────────────────────────────────────────────────────────┘
 ┌──── 腾讯云 + 宝塔 + nginx (复用) ────┐
 │  frps(loopback) + nginx vhost(wss/TLS) + 推送转发                                  │
 └────────────────────────────────────┘
```

## 待定的一个架构岔路

- **Shape 1（薄桥）**：Flutter 直接说 app-server JSON-RPC（连 `app-server --listen ws`），桥只做 frp + 推送。代码最少，但手机端要实现完整协议、且本机 full-auto 没有远程审批闸。
- **Shape 2（中间桥，推荐）**：C#/.NET 桥在中间翻译+加固，手机协议简单；**能强制远程审批/沙箱**（本机配置是全权全自动，必须由桥补上闸门）；推送有服务端落点；契合你 C# 强项。

## 诚实的边界

- 从**手机发起**的 turn：完全实时流式（逐字、命令输出、diff）。
- 看**你在桌面 GUI 里手动发起**的 turn 的逐字实时镜像：需 GUI 与桥共用同一个 app-server 实例（如都接 daemon，或用 `codex --remote`）—— 留作后续课题；当前可用轮询 `thread/read` 在 turn 边界近实时同步。
