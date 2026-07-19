# codex-remote

自托管的 Codex 手机远程控制:手机 App(Flutter)远程操作本机 Codex —— 实时查看正在进行的操作、
读取当前会话、发送提示词、内嵌看图片/视频。绕开 OpenAI 官方"账号 + 云中继"限制,远程链路自托管(腾讯云 frp)。

## 核心机制

Codex 桌面 app 内部就是一个 `codex app-server`(JSON-RPC over stdio)。本方案再起一个**共享 `~/.codex`
的 app-server**,用协议读会话 / 读内容 / 驱动 Codex,**鉴权与模型沿用桌面 app(ChatGPT 账号),零额外 API 费用**。
详见 [FINDINGS.md](./FINDINGS.md)。

## 架构(公网链路)

```
iPhone ─wss/TLS→ nginx(relay.example.com:443) → 127.0.0.1:8700
                                                     ↑ frps(只绑 localhost,公网仅开 443+7000)
                                             frp 隧道(token+TLS)
   Mac ── frpc ──→ 127.0.0.1:8767  codexbridge ──→ codex app-server(共享 ~/.codex)
```

公网只暴露 443(nginx)和 7000(frp 控制口,token+TLS)。桥端口两头只绑 localhost。

## 组件

- `bridge/` — Go 桥:spawn 并驱动 codex app-server;翻译+加固手机协议;媒体 `/file` 端点;推送骨架。
- `bridge/deploy/` — 公网中继部署套件:frps/frpc TOML、nginx 子域名反代、launchd/systemd、一键 `install-mac.sh`、
  双语 [DEPLOY.md](./bridge/deploy/DEPLOY.md)。
- `bridge/PUSH.md` — 后台重同步 + 推送骨架(APNs/FCM 通道接入说明)。
- `app/` — Flutter App(iOS 为主):会话按工作区分组/折叠、Markdown 渲染、图片/视频内嵌播放。
- `protocol/` — app-server 协议 JSON Schema(`codex app-server generate-json-schema --out protocol`)。
- `scripts/` — Phase 0 只读探针。

## 状态

| 阶段 | 内容 | 状态 |
|---|---|---|
| 0 | 可行性(attach 共享 app-server) | ✅ |
| 1 | Go 桥骨架 | ✅ |
| 3 | Flutter App MVP | ✅ |
| 4 | 公网中继(frp+nginx)+ 安全加固 | ✅ 桥侧完成,待服务器端落地 |
| 5 | 后台重同步 + 推送骨架 | ✅ 骨架完成,真实通道(APNs/FCM)待定 |
| 6 | 安全加固(逐设备 token/审批归属/审计) | ✅ |

待办:① 在腾讯云按 DEPLOY.md 跑 frps+nginx 做真机公网验证;② 定推送通道并接 APNs/FCM + app 侧注册;
③ app 侧 UI 打磨。

## 本地快速跑

桥需要一个 token(没有就拒绝启动,绝不在公网用随机 token):

```bash
cd bridge
go run ./cmd/codexbridge list                 # 列会话(只读)
go run ./cmd/codexbridge read <THREAD_ID>     # 读会话(只读)
CODEX_BRIDGE_TOKEN=devtoken go run ./cmd/codexbridge serve -addr 127.0.0.1:8767
# 本地试手没 token 也行:加 -dev-random-token(仅本地,会打印一次性随机 token)
```

`serve` 启动打印 `ws://HOST:PORT/ws (token <指纹>)`(只打指纹不打明文)。
手机/测试客户端用 `ws://...:8767/ws?token=<你的token>` 连接;手机协议见 `internal/bridge/server.go` 顶部注释。
**远程发起的 turn 一律被桥强制降权**(approvalPolicy=untrusted + 只读沙箱)。

App:
```bash
cd app
flutter run -d <iPhone 模拟器/真机> --dart-define=WS_URL='ws://127.0.0.1:8767/ws?token=devtoken'
```

## 构建可分发 App(不内置 token)

打**可分发 / 上架**的 APK 用这个脚本——**绝不**把本机 token 打进包里:

```bash
./build-apk-release.sh            # 产物 → ./dist/caret-<时间戳>.apk
./build-apk-release.sh ~/Desktop  # 也可指定输出目录
```

原理:App 的 token 存在**手机钥匙串**里(用户扫桌面托盘二维码配对后写入),
不在二进制里。脚本里**不传** `--dart-define=TOKEN`,且带一道保险——万一产物里
搜到本机 token 就拒绝输出。

> ⚠️ 切勿用 `--dart-define=TOKEN=...` 打可分发包——那等于把你机器的钥匙塞进 APK,
> 谁拿到文件谁就能控制你的电脑。需要"自己手机免扫码"的私用包时,才手动加该参数
> (脚本顶部注释有命令),且**该文件不要外发**。

## 公网部署

见 [bridge/deploy/DEPLOY.md](./bridge/deploy/DEPLOY.md):Mac 端 `./install-mac.sh` 一键(生成密钥/编译/装 launchd),
服务器端装 frps + 宝塔建子域名反代。

> 仓库里的 `relay.example.com` / `203.0.113.10` 均为占位符——部署时换成你自己的中继域名与服务器 IP。
> App 侧中继地址用 `--dart-define=RELAY_HOST=<你的域名>` 覆盖默认值。

## 安全模型(摘要,详见 DEPLOY.md §四)

- 所有 `/ws`、`/file` 按 token 做 SHA-256 常量时间比对;空 token 拒绝;支持**逐设备 token**(可单独吊销)。
- 远程 turn 强制 untrusted + 只读沙箱(每 turn 覆盖,resume 旧会话也降权)。
- 审批闸钳制:绝不转发 `acceptForSession`/`*_amendment` 等"永久放行";审批**只由发起设备应答**(归属绑定)。
- `/file` 仅限媒体子目录 + 扩展名白名单(**拿不到 `~/.codex/auth.json` 凭证**)。
- frp:控制口独立 token+TLS,桥端口只绑 localhost;nginx `limit_req` + 关 access_log + 清 Referer;`audit:` 审计日志。

## 只读探针(复现验证)

```bash
CODEX=/Applications/Codex.app/Contents/Resources/codex   # 0.136,功能全
python3 scripts/probe_list.py                            # 列出全部会话
python3 scripts/probe_read.py <THREAD_ID>                # 读某会话内容
```

## License

[MIT](./LICENSE)
