# Codex Remote — Windows 安装包

一个 NSIS 安装包,把这台 Windows 装成可被手机远程控制的 Codex 机器:装好 **agent**(连 hub)+ **托盘客户端**(状态/复制连接串/二维码)。

## 用户怎么装
1. 双击 **CodexRemoteSetup.exe**(会请求管理员权限)—— **安装过程零填写**。
2. 装完点任务栏托盘的 **>_** 图标 → **「激活订阅码…」**,填入 `crk_` 订阅码
   (向作者申请)。托盘调 `codexbridge activate` 换取这台机器的专属凭证(占一个
   机器名额;同机重装按硬件指纹自动复用);后台 agent 检测到凭证后自动上线。
3. 再点 **显示二维码**,手机 App 扫码连接。

> 前提:这台 Windows 已安装 Codex(`npm i -g @openai/codex`)。安装包会自动定位 npm 自带的原生 `codex.exe`。

静默/批量安装(激活同样事后做):
```
CodexRemoteSetup.exe /S
```

自定义/自托管中继(直接跑载荷):
```
powershell -File install-core.ps1 -Hub wss://你的域名/agent [-MachineId 自定义id]
```
> 机器 id 默认取计算机名;如果激活报「machine id already in use」(撞名),用
> `-MachineId` 重装指定一个独特的。

## 它做了什么
- 装到 `C:\ProgramData\codex-remote\`(codexbridge.exe / codexmenubar.exe / 图标 / 配置)
- 生成每台机唯一 token(`machine-<id>.token`);写托盘配置 `menubar.env`
  (含 `CRED_FILE`/`BRIDGE`,供托盘激活流程使用)
- 注册两个计划任务,**登录时在你的交互会话里运行**(这样 codex 能读到你的登录认证、托盘图标可见;不存储密码):
  - `CodexRemoteAgent` — 桥,连 hub(`-token-file` + `-cred-file`,秘密不上 argv;
    未激活时安静等待凭证文件出现,不会崩溃重启)
  - `CodexRemoteTray` — 托盘客户端(「激活订阅码…」入口在这里)
- 安装目录 ACL 锁死(仅 SYSTEM/管理员/本人),token 与凭证不暴露给其它本地账户
- 写卸载程序(出现在「添加或删除程序」);卸载保留 `machine-*.token` 重装复用同一连接串,凭证删除(重装时重新激活签发)

## 怎么构建(在 Mac/Linux 上)
```
brew install nsis          # 或 apt install nsis
./build-installer.sh        # 交叉编译两个 exe + makensis 打包 → CodexRemoteSetup.exe
```

## 文件
- `CodexRemote.nsi` — NSIS 安装界面(欢迎 → 填订阅码 → 安装 → 完成 + 卸载)
- `install-core.ps1` — 安装载荷(找 codex、激活订阅码、生成 token、建计划任务、写配置)
- `build-installer.sh` — 一键构建
