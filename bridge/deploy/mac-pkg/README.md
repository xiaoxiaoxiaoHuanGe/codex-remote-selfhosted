# Caret — macOS 安装包(.pkg)

与 Windows 的 NSIS 安装包对等:装 **agent**(连 hub)+ **托盘 Caret.app**。
**安装零填写** —— 激活在装完后的菜单栏托盘里做(「激活订阅码…」),agent
未激活时安静等待凭证文件出现。

## 构建

```bash
./build-pkg-mac.sh [output.pkg]      # 默认输出 ./Caret.pkg
```

签名/公证按本机环境自动升级,缺谁跳过谁(并打印补法):

| 条件 | 效果 |
|---|---|
| 无证书 | 未签名 pkg —— 仅本机测试,分发会被 Gatekeeper 拦 |
| 有 **Developer ID Application** 证书 | 二进制/App 带 hardened runtime + timestamp 签名 |
| 有 **Developer ID Installer** 证书 | pkg 本体签名(productsign) |
| 存过 notarytool 凭证 `caret-notary` | 自动公证 + staple —— 可公开分发 |

一次性准备(账号需已付费):
1. Xcode ▸ Settings ▸ Accounts ▸ Manage Certificates ▸ ＋ ▸ 分别创建
   **Developer ID Application** 与 **Developer ID Installer**。
2. appleid.apple.com 生成 App 专用密码,然后:
   `xcrun notarytool store-credentials caret-notary --apple-id <AppleID> --team-id <TEAMID> --password <App专用密码>`

## 安装包做了什么(postinstall)

- 找 codex(优先 `/Applications/Codex.app/Contents/Resources/codex`,否则 PATH)
- 写 `~/.codex-remote/`:`machine-<id>.token`(重装复用)/`menubar.env`
  (含 `CRED_FILE`/`BRIDGE`,供托盘「激活订阅码…」使用;凭证由激活时写入)
- 装两个 LaunchAgents 并立即拉起:
  - `com.example.caret.agent` — 桥(`-token-file` + `-cred-file`,秘密不上 argv)
  - `com.example.caret.tray` — 菜单栏 Caret.app
- 日志:`~/Library/Logs/codex-remote/{agent,tray}.log`

机器 id 取主机名小写;撞名报「machine id already in use」时,目前需联系作者
改名(或自行编辑 plist 后重激活)。

## 卸载(手动)

```bash
launchctl bootout gui/$(id -u)/com.example.caret.agent
launchctl bootout gui/$(id -u)/com.example.caret.tray
rm -rf /Applications/Caret.app /usr/local/codex-remote \
       ~/Library/LaunchAgents/com.example.caret.*.plist
# 配置/凭证(可选保留,重装免重配):rm -rf ~/.codex-remote
sudo pkgutil --forget com.example.caret
```
