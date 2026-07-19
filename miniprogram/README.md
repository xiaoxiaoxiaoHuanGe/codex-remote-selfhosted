# Caret 小程序轻量版（MVP 脚手架）

国内分发的**前台轻量获客版**：扫码配对 → 看「项目/对话」两段式会话 → 读会话+思考过程 → 发提示词 → 前台审批 → 看图片。完整「省心远控」（后台常驻、真推送、E2EE）仍由原生 App 承担。详见 `docs/商业化方案.md` §7。

与原生 App **共用同一套中继协议**——本目录只是又一个说同样 JSON-over-WebSocket 协议的前端（原生 WXML/WXSS/JS，无需构建，直接用微信开发者工具打开）。

## 怎么跑

1. 微信开发者工具 → 导入项目 → 选 `miniprogram/` 目录。
2. AppID：填你的小程序 AppID；没有就选「测试号」（`project.config.json` 里占位 `touristappid`）。
3. 开发期已设 `urlCheck:false`，可直接连 `relay.example.com`。**上线前**必须在小程序后台把它加进：
   - **socket 合法域名**（`wss://relay.example.com`）
   - **request 合法域名**（`https://relay.example.com`，用于 `/file` 取图）
4. 打开后在「连接电脑」页扫描电脑托盘二维码（或粘贴令牌）即可。

## 目录

```
miniprogram/
  app.js / app.json / app.wxss      # 入口、页面注册、全局样式（Caret 靛紫）
  config.js                         # 中继域名 + wsUrlForToken / fileUrl / tokenFromText
  utils/
    relay.js                        # ★ 单例中继客户端（严格对齐 App 的 bridge_client.dart）
    format.js                       # 相对时间 + 项目/对话两段式分组
  pages/
    connect/                        # 扫码/粘贴令牌配对
    sessions/                       # 项目 / 对话 两段式列表
    chat/                           # 读会话 + 思考过程流式 + 发送 + 审批 + 看图
```

## MVP 已实现

- ✅ 扫码 / 粘贴令牌配对（令牌存 `wx.storage`）
- ✅ 会话列表「项目 / 对话」两段式（按 cwd 分组、相对时间）
- ✅ 读历史会话；**思考过程**流式（reasoning summary/text delta）
- ✅ 发送提示词；助手回复流式拼接；turn 运行态 + 停止
- ✅ 前台审批闸（同意 / 拒绝，对齐桥的 accept/decline）
- ✅ 看图片（base64 内联）
- ✅ 改名 / 归档实时同步；断线重连退避

## 首版按计划裁剪（见 §7）

- ✂️ 后台常驻 / 后台重同步（平台不允许）→ 退后台即断，回前台自动重连
- ✂️ 局域网直连（小程序只能连白名单域名）→ 全走中继
- ✂️ 完整 E2EE / 硬件级安全存储 → 首版仅 TLS + 令牌；E2EE 留原生差异化
- ✂️ 语音输入、付费订阅（iOS 虚拟支付受限）

## 待接（下一步）

- ⏳ **微信订阅消息**：替代推送，在 turn 完成 / 需审批等关键节点提醒（需服务端拿 openid + 配模板 + 节点触发）
- ⏳ **账号体系**：openid 登录 + 短时令牌，与原生 App 共用权益
- ⏳ Markdown 渲染（当前助手文本按纯文本+换行展示，可接 towxml）
- ⏳ 远程历史图片走 `/file`（当前仅内联 base64 图直显）
- ⏳ 同名文件夹消歧（App 里 `sessions_screen` 的完整逻辑）

> 注意：本目录在 `~/codex-remote-build`（主开发副本）。改动前先 `git pull`。
