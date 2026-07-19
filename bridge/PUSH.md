# Phase 5 — 后台重同步 + 推送骨架

桥侧已经做好"通道无关"的部分;具体推送通道(APNs / FCM)以后定了再插。

## 已实现(provider 无关,已测)

**后台重同步**
- 手机断线/切后台再回来时,重连会**自动收到仍在等待的审批**(`handleWS` 连上即重发
  pending approvals),不会因为 WS 断过而漏掉"等你批准"。
- 新增 `{"type":"sync"}`:一次往返把**会话列表 + 待办审批**补齐(前台恢复时用)。
- 审批从"只存一个 channel"改成 `pendingApproval{id,method,params,ch}`,可被重新顶出。

**推送骨架**
- 设备注册表:`{"type":"registerPush","pushToken":"...","platform":"ios"}` → 桥按 token 去重
  存表,回 `{"type":"pushRegistered"}`。
- `Pusher` 接口:`Push(regs []PushReg, n Notification)`;默认实现 `LogPusher` 只打日志。
- 触发钩子:**有审批请求**(`onApproval`)和**任务完成**(`turn/completed`)时调用,
  且**仅当没有手机在线**才推(前台已经能看到实时 WS 事件,推了反而吵)。
- 运行时可换通道:`srv.SetPusher(yourPusher)`。

## 手机协议增量

```
client -> server:  {"type":"registerPush","pushToken":"<device token>","platform":"ios"}
                   {"type":"sync"}
server -> client:  {"type":"pushRegistered"}
                   {"type":"approval",...}   // 重连时会重发未决的
```

## 以后接真实通道怎么做

实现 `Pusher` 接口,在 `cmd/codexbridge/main.go` 的 `runServe` 里
`srv.SetPusher(...)`,凭证用环境变量注入(没配就保持 `LogPusher` 不推,不影响其它功能)。

### 方案 A:直连 APNs(自托管,推荐)
前置:Apple 开发者账号($99/年)、真机、App 开 Push 能力。
- 凭证:基于 token 的 `.p8` 密钥 + Key ID + Team ID + Topic(= App 的 Bundle ID)。
- 实现:用 ES256 对 JWT 签名(`crypto/ecdsa` + `x509.ParsePKCS8PrivateKey` 解析 .p8,
  零第三方依赖),HTTP/2 `POST https://api.push.apple.com/3/device/{deviceToken}`,
  头 `authorization: bearer <jwt>`、`apns-topic`、`apns-push-type: alert`。
- App 侧:注册远程通知拿到 APNs device token,通过 `registerPush` 发给桥。
- 注意:**模拟器收不到真 APNs**,必须真机验证。
- 计划环境变量:`APNS_KEY_PATH` `APNS_KEY_ID` `APNS_TEAM_ID` `APNS_TOPIC`。

### 方案 B:FCM / Firebase
省事、将来跨 Android 方便,但引入 Google 依赖;iOS 仍需 Apple 推送证书。
实现:`POST https://fcm.googleapis.com/v1/projects/<id>/messages:send`,OAuth2 服务账号鉴权。

> 当前选择:**先只做骨架,通道以后定**(2026-06-02)。
