# 订阅制与机器绑定设计（阶段一：内测）

> 日期：2026-06-10
> 范围：把 codex-remote 从自用工具变为可分发的 SaaS 托管 relay 产品的**控制面**设计 —— License Key、机器名册、激活/换机/到期。
> **阶段定位：当前为内测，不接支付。** key 由运营者人工签发（免费发给内测用户）；支付/计费整体预留为后期能力（见 §7、§10）。机器绑定的全部机制（名册/激活/换机/到期）在内测期即落地，将来开收费只是「发 key 的方式」从人工变成支付回调，控制面不用改。
> 关系文档：`docs/商业化方案.md` 是商业层蓝图；本文将其 §2（账号与订阅）、§3（token 体系）落成更轻的阶段一实现方案。`bridge/internal/hub/hub.go` 与 `bridge/internal/bridge/server.go` 的包注释仍是链路协议与安全模型的权威。

---

## 1. 已确认的关键决策

| 决策点 | 选择 | 理由 |
|---|---|---|
| 分发模式 | **SaaS 托管 relay**：用户下载 bridge 安装包 + 手机 App，机器必须注册到你运营的 hub | 授权校验全在服务端，客户端无校验代码可绕过；计量计费天然 |
| 账号体系 | **License Key 起步，可升级邮箱账号**：key 挂在 account 记录下（email 可空），后期「key + 邮箱认领」平滑迁移 | 与现有 token 驱动架构契合度最高；零登录 UI；个人开发者无支付/微信开放平台资质也能收款 |
| 限制维度 | **只限机器数**（跑 bridge 的桌面机）；手机设备数不限 | 机器是真正的价值载体；实现最简（hub 在 agent 注册时校验） |
| 绑定语义 | **机器名册 + 自助换机（限频）**：Tailscale/JetBrains 式槽位制 | 体验与防滥用的最佳平衡；硬件指纹只做辅助不做硬门槛 |

## 2. 非目标（阶段一不做）

- **任何支付接入**（爱发电/Lemon Squeezy/IAP 全部后置）—— 内测期 key 由 admin CLI 人工签发
- 邮箱/微信/Apple 登录（数据模型预留，UI 与流程不做）
- 手机设备数限制、并发/用量配额
- 团队/席位、多用户管理
- P2P/TURN、多节点 relay（见商业化方案 §4 的 C 阶段）

## 3. 总体架构

所有授权逻辑放在 **codexhub**（运营方服务器）。bridge 与手机 App **不含任何校验代码** —— 绕过校验的唯一方式是不用托管 relay，而那正是付费的东西。

- hub 增加 **SQLite 持久化** + 独立 `internal/license` 包（接口干净，将来可平移成独立服务），维持单二进制运维。
- 现有 `HUB_AGENT_KEY` 共享密钥模式**保留**为 self-host/开发模式，由开关切换（如 `HUB_LICENSE_MODE=1` 启用授权模式）；不影响现有自用部署。
- **不触碰 `server.go` 的安全钳制**（turn clamp / cwd clamp / 审批钳制 / `/file` 门控原样不动）。本设计只新增 agent 准入层。

## 4. 数据模型

```
account        id, email(可空,唯一), created_at
license_key    key_hash(SHA-256), account_id, created_at, revoked
subscription   id, account_id, plan, machine_limit, status,
               expires_at, source(afdian|lemonsqueezy|manual), external_ref
machine        id, account_id, machine_id, name, fp_hint,
               cred_hash(SHA-256), activated_at, last_seen, deactivated_at(可空)
swap_event     id, account_id, ts
```

- key 与机器凭证**只存哈希**，泄库不泄凭证（沿用现有 SHA-256/`subtle` 常量时间比较惯例）。
- `machine` 软删除（`deactivated_at`）留审计痕迹。
- `account` 现在 email 全空；后期账号体系直接挂在这张表上。

## 5. 机器身份

现状：`machineId` 为用户自起名（存 `menubar.env`），token 随机 —— 拷贝配置文件即可克隆机器。新方案：

1. **服务端签发机器凭证**：激活时 hub 生成随机 32 字节凭证（存哈希），下发写入本机 `menubar.env`。agent 注册改用该凭证（license 模式下取代共享 `HUB_AGENT_KEY`）。
2. **硬件指纹只做辅助**：macOS `IOPlatformUUID` / Windows `MachineGuid` 取 SHA-256 作 `fp_hint`：
   - 重装系统（凭证丢失但指纹匹配）→ 自动复用原槽位，不烧名额、不计换机；
   - 同一指纹出现在多个 key 下 → 记风控日志，不自动封禁；
   - **不做硬门槛**（虚拟机/换主板会误伤）。
3. **配置整份拷走**：两机同凭证同时注册 → hub 现有 duplicate-register 拒绝逻辑直接挡住，并记冲突事件。

## 6. 流程

### 6.1 激活

```
购买 → 收到 license key（格式 crk_<base32>，含校验位）
桌面端安装 bridge → 托盘弹「输入订阅码」
bridge → hub: POST /api/activate {key, machineId, name, fp_hint}
hub 校验：key 有效 ∧ 订阅 active ∧ 名册有空槽
  ├─ 名册已有同 fp_hint 的活跃机器 → 复用槽位，重发凭证（重装场景）
  ├─ 有空槽 → 占槽，签发机器凭证
  └─ 满 → 4xx：「已绑定 N/N 台，请先在手机 App 解绑一台」
agent 此后用机器凭证注册；手机扫码配对流程不变
```

### 6.2 换机（自助 + 限频）

- 管理入口：**手机 App 机器列表页**（已有机器列表，加解绑操作）；小程序同步加入口。
- 解绑 = 置 `deactivated_at`，槽位立即释放，机器凭证作废，该 agent 被踢下线。
- **限频：每 30 天最多 3 次解绑**（`swap_event` 计数），超限报「换机次数已用完，X 天后恢复」。这是防「一 key 多人轮流绑」的主力手段。
- 指纹匹配的重装复用**不计**换机次数。
- 解绑操作的鉴权：**经由已配对机器的会话**。手机扫码配对到某台机器后，hub 知道该会话归属哪个 account，即可管理该 account 名册下的所有机器（手机端不需要持有 license key）。兜底：名下机器全部离线/丢失时，由 admin CLI 人工解绑。

### 6.3 订阅到期

- hub 在 **agent 注册时**校验订阅状态；对长连接**每日重验**（否则常驻在线的机器永不掉线）。
- 到期 → **3 天宽限期**：agent 可连，hub 向托盘/手机推送续费提醒。
- 宽限期满 → agent 注册被拒；机器名册**保留**，续费即恢复，无需重新绑定。
- **降级而非锁死**：bridge 的 LAN 直连模式（`/ws` 直连）不经 hub，到期后局域网内仍可用 —— 架构天然成立，也是更好的体验（商业化方案 §2 的建议）。

## 7. 发 key（阶段一：纯人工，支付预留）

内测期唯一的发 key 通道是 **admin CLI**：

```
codexhub admin issue-key --plan beta --machines 3 --months 6   # 签发内测 key
codexhub admin list-keys / revoke-key / extend-key             # 日常管理
```

- 内测 key 走和正式订阅完全相同的数据模型（`subscription.source = manual`，`plan = beta`），到期/续期/换机逻辑一律生效 —— 内测就是在真实地演练控制面。
- **支付预留**：将来开收费时只需新增 webhook 入口（国内爱发电/面包多，海外 Lemon Squeezy），回调里调用与 admin CLI 同一个 `internal/license` 发 key 接口，控制面零改动。本阶段不实现任何支付代码。

## 8. 分发渠道

| 件 | 渠道 | 备注 |
|---|---|---|
| 桌面端（bridge+托盘） | 官网下载：mac pkg / Windows NSIS（已有） | mac 需 Apple Developer（$99/年）签名+公证，否则 Gatekeeper 拦截 |
| 手机 App | TestFlight 内测 → App Store | 同一开发者账号 |
| 小程序 | 微信审核上架 | 个人主体可上；「远程控制」类目有审核风险 |
| 内测入口 | 邀请制：人工发 key + 下载链接 | 购买页/价格页属阶段二（届时国内域名需 ICP 备案，温州服务器域名/TLS 尚未落地） |

## 9. 风险与缓解

1. **Apple IAP 规则**（后期开收费时生效）：购买应全部发生在 App 外、激活在桌面端，iOS App 仅为免费客户端（类 BYO-server 模式），不在 App 内展示购买入口/价格/外链，降低审核风险。若在 App 内卖订阅，必须走 IAP。内测期 App 内无任何付费元素，无此风险。
2. **OpenAI 条款风险**：见商业化方案 §8，本设计不改变该风险面；上线前做条款评估。
3. **key 在客户端的存储**：手机 App 与桌面端持有 license key/机器凭证，存系统安全区（Keychain/DPAPI），沿用「绝不把凭证打进安装包」铁律（`build-apk-release.sh` 已有安全网）。
4. **hub 单点 + SQLite**：阶段一接受；数据每日备份。规模化时 license 包平移为独立服务（§3 已留接口边界）。
5. **防滥用强度**：名册+限频挡「多人共用 key」的主流形态；指纹仅风控信号。接受少量高技术成本的绕过（自建 relay 的用户本就不是付费目标客群）。

## 10. 升级路径（阶段二及以后）

- **支付接入**（用户量起来后再做）：爱发电/面包多（国内）、Lemon Squeezy（海外）webhook → 调用 `internal/license` 的同一发 key 接口；购买页挂到 `website/`。
- 邮箱账号：`account.email` 落值 + 「key + 邮箱」认领流程；登录后 key 隐于账号之下。
- 微信登录：待有企业主体后作为国内端附加登录方式。
- 设备数限制 / 团队席位 / 用量配额：在 `subscription` 表加字段即可承载。
- E2EE 设备公钥认证（`HUB_REQUIRE_DEVICE_AUTH`，已在路上）与本设计正交：前者管手机准入，本设计管机器准入。
